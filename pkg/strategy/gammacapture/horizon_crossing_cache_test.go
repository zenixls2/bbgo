package gammacapture

import (
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func crossingDecisionAtSideDistancesReference(
	m *MarketMakerHorizonModel,
	now time.Time,
	c MarketMakerConfig,
	horizon time.Duration,
	buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps float64,
) MarketMakerHorizonDecision {
	c.setDefaults()
	d := MarketMakerHorizonDecision{
		Horizon: horizon, HorizonSeconds: int64(horizon.Seconds()), QuoteDistanceBps: grossQuoteEdgeBps / 2,
		BuyTouchDistanceBps: buyDistanceBps, SellTouchDistanceBps: sellDistanceBps,
		Reason: "insufficient completed horizon samples", EstimatorSource: "bbo-side", UpdatedAt: now,
	}
	if horizon <= 0 || buyDistanceBps <= 0 || sellDistanceBps <= 0 {
		return d
	}
	cutoff := now.Add(-time.Duration(c.HorizonLookback))
	var up, down int
	var first, last, lastUp, lastDown, lastExposure time.Time
	var effectiveSamples, weightedUpTouches, weightedDownTouches, weightedBothTouches float64
	var upSpacing, downSpacing []time.Duration
	for index, start := range m.points {
		startBid, startAsk := start.bidPrice(), start.askPrice()
		endAt := start.At.Add(horizon)
		if start.At.Before(cutoff) || start.GapBefore || startBid <= 0 || startAsk < startBid ||
			endAt.After(now) || (!last.IsZero() && start.At.Sub(last) < time.Minute) {
			continue
		}
		maxBid, minAsk := 0.0, math.Inf(1)
		valid, futurePoints := true, 0
		for future := index + 1; future < len(m.points) && m.points[future].At.Before(endAt); future++ {
			point := m.points[future]
			bid, ask := point.bidPrice(), point.askPrice()
			if point.GapBefore || bid <= 0 || ask < bid {
				valid = false
				break
			}
			futurePoints++
			maxBid = math.Max(maxBid, bid)
			minAsk = math.Min(minAsk, ask)
		}
		if !valid || futurePoints == 0 || maxBid <= 0 || math.IsInf(minAsk, 1) {
			continue
		}
		sellTouched := math.Log(maxBid/startBid)*10_000 >= sellDistanceBps
		buyTouched := math.Log(startAsk/minAsk)*10_000 >= buyDistanceBps
		weight := 1.0
		if !lastExposure.IsZero() {
			weight = math.Min(1, start.At.Sub(lastExposure).Seconds()/horizon.Seconds())
		}
		if weight <= 0 {
			continue
		}
		effectiveSamples += weight
		if sellTouched {
			weightedUpTouches += weight
		}
		if buyTouched {
			weightedDownTouches += weight
		}
		if buyTouched && sellTouched {
			weightedBothTouches += weight
		}
		lastExposure = start.At
		if sellTouched && (lastUp.IsZero() || start.At.Sub(lastUp) >= horizon) {
			up++
			if !lastUp.IsZero() {
				upSpacing = append(upSpacing, start.At.Sub(lastUp))
			}
			lastUp = start.At
		}
		if buyTouched && (lastDown.IsZero() || start.At.Sub(lastDown) >= horizon) {
			down++
			if !lastDown.IsZero() {
				downSpacing = append(downSpacing, start.At.Sub(lastDown))
			}
			lastDown = start.At
		}
		if first.IsZero() {
			first = start.At
		}
		last = start.At
	}
	if first.IsZero() || !last.After(first) {
		return d
	}
	d.ObservedHours = last.Sub(first).Hours()
	d.UpCrosses, d.DownCrosses = up, down
	d.UpCrossesPerHour, d.DownCrossesPerHour = float64(up)/d.ObservedHours, float64(down)/d.ObservedHours
	d.MeanUpSpacing, d.MeanDownSpacing = meanDuration(upSpacing), meanDuration(downSpacing)
	d.EffectiveSamples = effectiveSamples
	d.BuyTouchProbability, d.BuyTouchStdError = jeffreysBernoulliPosterior(weightedDownTouches, effectiveSamples)
	d.SellTouchProbability, d.SellTouchStdError = jeffreysBernoulliPosterior(weightedUpTouches, effectiveSamples)
	d.BothTouchProbability, d.BothTouchStdError = jeffreysBernoulliPosterior(weightedBothTouches, effectiveSamples)
	lowerJoint := math.Max(0, d.BuyTouchProbability+d.SellTouchProbability-1)
	upperJoint := math.Min(d.BuyTouchProbability, d.SellTouchProbability)
	d.BothTouchProbability = math.Max(lowerJoint, math.Min(upperJoint, d.BothTouchProbability))
	d.TouchCovariance = d.BothTouchProbability - d.BuyTouchProbability*d.SellTouchProbability
	d.NetRoundTripEdgeBps = grossQuoteEdgeBps - 2*c.MakerFeeBps - 2*c.AdverseSelectionBps - c.MinimumNetEdgeBps
	edge := math.Max(0, d.NetRoundTripEdgeBps)
	if horizonHours := horizon.Hours(); horizonHours > 0 {
		d.ScoreBpsPerHour = math.Min(d.BuyTouchProbability, d.SellTouchProbability) / horizonHours * edge
		d.ScoreStdErrorBpsHour = math.Max(d.BuyTouchStdError, d.SellTouchStdError) / horizonHours * edge
	}
	d.Reason = "max fee-adjusted two-sided edge per hour"
	return d
}

func TestCrossingExposureCacheMatchesReferenceRandomPaths(t *testing.T) {
	rng := rand.New(rand.NewSource(7302026))
	config := MarketMakerConfig{
		HorizonLookback: types.Duration(8 * time.Minute),
		MakerFeeBps:     1, AdverseSelectionBps: 0.5, MinimumNetEdgeBps: 1,
	}
	horizons := []time.Duration{30 * time.Second, 75 * time.Second, 3 * time.Minute}
	for path := 0; path < 12; path++ {
		model := MarketMakerHorizonModel{}
		start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
		mid := 300_000.0
		for second := 0; second < 12*60; second++ {
			mid *= math.Exp(rng.NormFloat64() * 0.00008)
			spreadBps := 2 + 18*rng.Float64()
			bid := mid * math.Exp(-spreadBps/20_000)
			ask := mid * math.Exp(spreadBps/20_000)
			model.points = append(model.points, MarketMakerHorizonPoint{
				At: start.Add(time.Duration(second) * time.Second), Bid: bid, Ask: ask, Mid: mid,
				GapBefore: second > 0 && rng.Float64() < 0.004,
			})
		}
		now := model.points[len(model.points)-1].At.Add(750 * time.Millisecond)
		for _, horizon := range horizons {
			for query := 0; query < 8; query++ {
				buyDistance := 3 + 80*rng.Float64()
				sellDistance := 3 + 80*rng.Float64()
				grossEdge := buyDistance + sellDistance
				got := model.CrossingDecisionAtSideDistances(now, config, horizon, buyDistance, sellDistance, grossEdge)
				want := crossingDecisionAtSideDistancesReference(&model, now, config, horizon, buyDistance, sellDistance, grossEdge)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("path=%d horizon=%s query=%d cache mismatch\n got: %+v\nwant: %+v", path, horizon, query, got, want)
				}
			}
		}
	}
}

func TestCrossingExposureCacheDefersSameSecondReplacement(t *testing.T) {
	config := MarketMakerConfig{HorizonLookback: types.Duration(10 * time.Minute)}
	model := MarketMakerHorizonModel{}
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	for second := 0; second <= 10*60; second++ {
		price := 100 * math.Exp(0.00001*float64(second))
		model.ObserveBookWithGap(start.Add(time.Duration(second)*time.Second), price, price+0.01, config, false)
	}
	now := start.Add(10*time.Minute + 500*time.Millisecond)
	_ = model.CrossingDecisionAtSideDistances(now, config, time.Minute, 5, 5, 10)
	model.ObserveBookWithGap(start.Add(10*time.Minute+900*time.Millisecond), 50, 50.01, config, false)
	got := model.CrossingDecisionAtSideDistances(now, config, time.Minute, 5, 5, 10)
	want := crossingDecisionAtSideDistancesReference(&model, now, config, time.Minute, 5, 5, 10)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("same-second pending replacement changed a completed window\n got: %+v\nwant: %+v", got, want)
	}
	model.ObserveBookWithGap(start.Add(10*time.Minute+time.Second), 50, 50.01, config, false)
	now = start.Add(10*time.Minute + time.Second + 500*time.Millisecond)
	got = model.CrossingDecisionAtSideDistances(now, config, time.Minute, 5, 5, 10)
	want = crossingDecisionAtSideDistancesReference(&model, now, config, time.Minute, 5, 5, 10)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("new-second cache rebuild mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestIncrementalExposureBatchMatchesFreshExactBuild(t *testing.T) {
	rng := rand.New(rand.NewSource(15082026))
	start := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	horizon := 2 * time.Minute
	all := make([]MarketMakerHorizonPoint, 0, 12*60)
	mid := 300_000.0
	for second := 0; second < 12*60; second++ {
		mid *= math.Exp(rng.NormFloat64() * 0.00005)
		spread := 1 + 8*rng.Float64()
		all = append(all, MarketMakerHorizonPoint{
			At:               start.Add(time.Duration(second) * time.Second),
			Bid:              mid * math.Exp(-spread/20_000),
			Ask:              mid * math.Exp(spread/20_000),
			BBOWeightedPrice: mid * math.Exp(rng.NormFloat64()*0.00001),
			BookImbalance:    rng.Float64()*2 - 1,
			BookDepthReady:   true,
			GapBefore:        second == 9*60+17,
		})
	}
	incremental := MarketMakerHorizonModel{points: append([]MarketMakerHorizonPoint(nil), all[:8*60]...)}
	_ = incremental.crossingExposures(horizon)
	incremental.points = append(incremental.points, all[8*60:]...)
	got := incremental.crossingExposures(horizon)
	fresh := MarketMakerHorizonModel{points: append([]MarketMakerHorizonPoint(nil), all...)}
	want := fresh.crossingExposures(horizon)
	equalExposure := func(a, b marketMakerHorizonExposure) bool {
		if !a.At.Equal(b.At) || !a.EndAt.Equal(b.EndAt) || a.NextMinute != b.NextMinute ||
			a.StartBookDepthReady != b.StartBookDepthReady ||
			a.ConditionalState.Valid != b.ConditionalState.Valid {
			return false
		}
		av := []float64{
			a.StartBid, a.StartAsk, a.StartBBOWeightedPrice, a.WindowBBOWeightedPrice,
			a.StartBookImbalance,
			a.TerminalBid, a.TerminalAsk, a.BuyExcursionBps, a.SellExcursionBps,
			a.ConditionalState.BuyDrawdownBps, a.ConditionalState.BuyRebound30Bps,
			a.ConditionalState.BuyQVBps, a.ConditionalState.SellRunupBps,
			a.ConditionalState.SellReversal30Bps, a.ConditionalState.SellQVBps,
			a.ConditionalState.SpreadBps,
		}
		bv := []float64{
			b.StartBid, b.StartAsk, b.StartBBOWeightedPrice, b.WindowBBOWeightedPrice,
			b.StartBookImbalance,
			b.TerminalBid, b.TerminalAsk, b.BuyExcursionBps, b.SellExcursionBps,
			b.ConditionalState.BuyDrawdownBps, b.ConditionalState.BuyRebound30Bps,
			b.ConditionalState.BuyQVBps, b.ConditionalState.SellRunupBps,
			b.ConditionalState.SellReversal30Bps, b.ConditionalState.SellQVBps,
			b.ConditionalState.SpreadBps,
		}
		for index := range av {
			if math.Abs(av[index]-bv[index]) > 1e-8 {
				return false
			}
		}
		return true
	}
	if len(got) != len(want) {
		t.Fatalf("incremental batch length differs: got=%d want=%d", len(got), len(want))
	}
	for index := range got {
		if !equalExposure(got[index], want[index]) {
			t.Fatalf("incremental batch differs at %d:\n got=%+v\nwant=%+v", index, got[index], want[index])
		}
	}
}

func TestHorizonExposureUsesTimeWeightedDepthWeightedBBOPrice(t *testing.T) {
	config := MarketMakerConfig{HorizonLookback: types.Duration(time.Minute)}
	model := MarketMakerHorizonModel{}
	start := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	model.ObserveBookWithSizesAndGap(start, 99, 1, 101, 1, config, false)
	// Microprice = (103*3 + 99*1)/(3+1) = 102. The first BBO is
	// valid for two seconds and this one for the remaining eight seconds.
	model.ObserveBookWithSizesAndGap(start.Add(2*time.Second), 99, 3, 103, 1, config, false)
	model.ObserveBookWithSizesAndGap(start.Add(10*time.Second), 100, 1, 102, 1, config, false)

	exposures := model.crossingExposures(10 * time.Second)
	if len(exposures) == 0 {
		t.Fatal("expected one completed BBO window")
	}
	got := exposures[0]
	if math.Abs(got.StartBBOWeightedPrice-100) > 1e-12 {
		t.Fatalf("unexpected starting BBO-weighted price: %+v", got)
	}
	if want := (100*2 + 102*8) / 10.0; math.Abs(got.WindowBBOWeightedPrice-want) > 1e-12 {
		t.Fatalf("event frequency must not replace time weighting: got=%v want=%v", got.WindowBBOWeightedPrice, want)
	}
}
