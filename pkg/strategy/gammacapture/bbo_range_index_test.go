package gammacapture

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestBBORangeIndexIncrementalSyncMatchesLinearScan(t *testing.T) {
	start := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	all := make([]MarketMakerHorizonPoint, 300)
	for n := range all {
		bid := 100.0 + float64((n*17)%41)/100.0
		ask := bid + 0.03 + float64(n%3)/100.0
		all[n] = MarketMakerHorizonPoint{
			At: start.Add(time.Duration(n) * time.Second), Bid: bid, Ask: ask,
			GapBefore: n == 71 || n == 193,
		}
	}

	model := MarketMakerHorizonModel{points: append([]MarketMakerHorizonPoint(nil), all[:180]...)}
	index := model.executableBBORangeIndex()
	assertBBORangeIndexMatchesLinear(t, index, model.points)

	// Exercise both retention trimming and append-only growth without rebuilding
	// an index over the whole active horizon.
	model.points = append([]MarketMakerHorizonPoint(nil), all[53:260]...)
	index = model.executableBBORangeIndex()
	assertBBORangeIndexMatchesLinear(t, index, model.points)

	// ObserveBook may update the current second in place. The index must replace
	// that mutable tail, including its validity flag and extrema.
	last := len(model.points) - 1
	model.points[last].Bid = 98.25
	model.points[last].Ask = 98.29
	model.points[last].GapBefore = true
	index = model.executableBBORangeIndex()
	assertBBORangeIndexMatchesLinear(t, index, model.points)
}

func assertBBORangeIndexMatchesLinear(t *testing.T, index *marketMakerBBORangeIndex, points []MarketMakerHorizonPoint) {
	t.Helper()
	if index == nil || index.pointCount() != len(points) {
		t.Fatalf("index size mismatch: got=%v want=%d", index, len(points))
	}
	thresholds := []float64{98.28, 100.05, 100.17, 100.31, 101.0}
	for left := 0; left < len(points); left += 13 {
		for right := left + 1; right <= len(points); right += 29 {
			valid := true
			for _, point := range points[left:right] {
				bid, ask := point.bidPrice(), point.askPrice()
				if point.GapBefore || bid <= 0 || ask < bid {
					valid = false
					break
				}
			}
			if got := index.validRange(left, right); got != valid {
				t.Fatalf("validRange(%d,%d)=%v want=%v", left, right, got, valid)
			}
			for _, quote := range thresholds {
				wantAsk, wantBid := -1, -1
				for n := left; n < right; n++ {
					point := points[n]
					bad := point.GapBefore || point.bidPrice() <= 0 || point.askPrice() < point.bidPrice()
					if !bad && wantAsk < 0 && point.askPrice() <= quote {
						wantAsk = n
					}
					if !bad && wantBid < 0 && point.bidPrice() >= quote {
						wantBid = n
					}
				}
				if got := index.firstAskAtOrBelow(left, right, quote); got != wantAsk {
					t.Fatalf("firstAskAtOrBelow(%d,%d,%v)=%d want=%d", left, right, quote, got, wantAsk)
				}
				if got := index.firstBidAtOrAbove(left, right, quote); got != wantBid {
					t.Fatalf("firstBidAtOrAbove(%d,%d,%v)=%d want=%d", left, right, quote, got, wantBid)
				}
			}
		}
	}
}

func TestTwoStageCompletionGivesOppositeSideFreshHorizon(t *testing.T) {
	start := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	horizon := 5 * time.Minute
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{HorizonLookback: 30 * 60 * 1e9, MaxTradingWindow: 30 * 60 * 1e9}
	for second := 0; second <= 8*60; second++ {
		bid, ask := 99.99, 100.01
		switch {
		case second == 2*60:
			// Opening BUY first-passage inside the initial five-minute window.
			bid, ask = 99.79, 99.80
		case second == 6*60:
			// The SELL does not touch by start+H, but it does touch before
			// the BUY fill's independent deadline at fill+H.
			bid, ask = 100.20, 100.21
		}
		model.ObserveBook(start.Add(time.Duration(second)*time.Second), bid, ask, config)
	}
	exposure, ok := horizonExposureAtIndex(model.points, 0, horizon)
	if !ok {
		t.Fatal("expected a complete initial horizon exposure")
	}
	terminalBid, completed, valid := model.twoStageCompletionOutcome(
		model.executableBBORangeIndex(), exposure, horizon, 99.80, 100.20, true)
	if !valid || !completed {
		t.Fatalf("fresh post-fill horizon should complete: terminal=%v completed=%v valid=%v", terminalBid, completed, valid)
	}
	if terminalBid != 99.99 {
		t.Fatalf("terminal liquidation must be the last executable bid before fill+H: got=%v", terminalBid)
	}
}

func TestCrossHorizonContinuationUsesLongestConfiguredFastWindow(t *testing.T) {
	config := MarketMakerConfig{
		FastWindows: []types.Duration{
			types.Duration(5 * time.Minute),
			types.Duration(15 * time.Minute),
			types.Duration(30 * time.Minute),
		},
		JointDistanceQuantity: JointDistanceQuantityConfig{
			TwoStageContinuation:     true,
			CrossHorizonContinuation: true,
		},
	}
	if got := jointContinuationHorizon(config, 5*time.Minute); got != 30*time.Minute {
		t.Fatalf("short entry should use the longest configured completion horizon: got=%s", got)
	}
	config.JointDistanceQuantity.CrossHorizonContinuation = false
	if got := jointContinuationHorizon(config, 5*time.Minute); got != 5*time.Minute {
		t.Fatalf("ordinary two-stage continuation must retain the entry horizon: got=%s", got)
	}
}

func TestCrossHorizonCompletionDoesNotExtendOpeningLease(t *testing.T) {
	start := time.Date(2026, 8, 15, 0, 30, 0, 0, time.UTC)
	openingHorizon := 5 * time.Minute
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{HorizonLookback: types.Duration(time.Hour), MaxTradingWindow: types.Duration(30 * time.Minute)}
	for second := 0; second <= 13*60; second++ {
		bid, ask := 99.99, 100.01
		switch second {
		case 2 * 60:
			bid, ask = 99.79, 99.80
		case 9 * 60:
			bid, ask = 100.20, 100.21
		}
		model.ObserveBook(start.Add(time.Duration(second)*time.Second), bid, ask, config)
	}
	exposure, ok := horizonExposureAtIndex(model.points, 0, openingHorizon)
	if !ok {
		t.Fatal("expected a complete opening exposure")
	}
	_, shortCompleted, shortValid := model.twoStageCompletionOutcome(
		model.executableBBORangeIndex(), exposure, openingHorizon, 99.80, 100.20, true)
	if !shortValid || shortCompleted {
		t.Fatalf("same-horizon completion should miss the late opposite touch: completed=%v valid=%v", shortCompleted, shortValid)
	}
	_, longCompleted, longValid := model.twoStageCompletionOutcome(
		model.executableBBORangeIndex(), exposure, 10*time.Minute, 99.80, 100.20, true)
	if !longValid || !longCompleted {
		t.Fatalf("long completion horizon should capture the late opposite touch: completed=%v valid=%v", longCompleted, longValid)
	}

	// A would-be opening touch after five minutes must still be excluded because
	// exposure.EndAt, not the continuation horizon, bounds first passage.
	lateOpening := MarketMakerHorizonModel{}
	for second := 0; second <= 13*60; second++ {
		bid, ask := 99.99, 100.01
		if second == 6*60 {
			bid, ask = 99.79, 99.80
		}
		lateOpening.ObserveBook(start.Add(time.Duration(second)*time.Second), bid, ask, config)
	}
	lateExposure, ok := horizonExposureAtIndex(lateOpening.points, 0, openingHorizon)
	if !ok {
		t.Fatal("expected a complete late-opening exposure")
	}
	_, _, valid := lateOpening.twoStageCompletionOutcome(
		lateOpening.executableBBORangeIndex(), lateExposure, 10*time.Minute, 99.80, 100.20, true)
	if valid {
		t.Fatal("a longer completion horizon must not extend the opening quote lease")
	}
}

func TestTwoStageCompletionDoesNotExtendOpeningLease(t *testing.T) {
	start := time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC)
	horizon := 5 * time.Minute
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{HorizonLookback: 30 * 60 * 1e9, MaxTradingWindow: 30 * 60 * 1e9}
	for second := 0; second <= 11*60; second++ {
		bid, ask := 99.99, 100.01
		if second == 6*60 {
			// A BUY touch after the initial H is not an opening fill and must
			// not manufacture a continuation cycle.
			bid, ask = 99.79, 99.80
		}
		model.ObserveBook(start.Add(time.Duration(second)*time.Second), bid, ask, config)
	}
	exposure, ok := horizonExposureAtIndex(model.points, 0, horizon)
	if !ok {
		t.Fatal("expected a complete initial horizon exposure")
	}
	_, _, valid := model.twoStageCompletionOutcome(
		model.executableBBORangeIndex(), exposure, horizon, 99.80, 100.20, true)
	if valid {
		t.Fatal("an opening quote that misses H must not receive a second horizon")
	}
}

func TestBBORangeIndexRejectsGapAcrossContinuationPath(t *testing.T) {
	start := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)
	horizon := 5 * time.Minute
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{HorizonLookback: 30 * 60 * 1e9, MaxTradingWindow: 30 * 60 * 1e9}
	for second := 0; second <= 8*60; second++ {
		bid, ask := 99.99, 100.01
		if second == 2*60 {
			bid, ask = 99.79, 99.80
		}
		model.ObserveBookWithGap(start.Add(time.Duration(second)*time.Second), bid, ask, config, second == 4*60)
	}
	exposure, ok := horizonExposureAtIndex(model.points, 0, horizon)
	if ok {
		t.Fatal("the initial cached path itself must reject a gap")
	}

	// Put the gap only after the initial path so the continuation validator,
	// rather than the initial exposure builder, owns the rejection.
	model = MarketMakerHorizonModel{}
	for second := 0; second <= 8*60; second++ {
		bid, ask := 99.99, 100.01
		if second == 2*60 {
			bid, ask = 99.79, 99.80
		}
		model.ObserveBookWithGap(start.Add(time.Duration(second)*time.Second), bid, ask, config, second == 6*60)
	}
	exposure, ok = horizonExposureAtIndex(model.points, 0, horizon)
	if !ok {
		t.Fatal("initial path should be complete before the later gap")
	}
	_, _, valid := model.twoStageCompletionOutcome(
		model.executableBBORangeIndex(), exposure, horizon, 99.80, 100.20, true)
	if valid {
		t.Fatal("a data gap inside the fresh completion horizon must censor the path")
	}
}
