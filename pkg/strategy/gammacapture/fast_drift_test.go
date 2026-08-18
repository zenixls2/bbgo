package gammacapture

import (
	"math"
	"testing"
	"time"
)

func trainSyntheticFastDrift(t *testing.T, invertLabels bool) (*MarketMakerHorizonModel, FastDriftFeatures) {
	t.Helper()
	horizon := time.Minute
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	model := &MarketMakerHorizonModel{}
	price := 100_000.0
	features := make([]FastDriftFeatures, 32)
	for index := range features {
		features[index] = FastDriftFeatures{
			Direction:     []float64{-0.8, -0.2, 0.3, 0.9}[index%4],
			BookImbalance: []float64{-0.7, 0.4, 0.8, -0.1}[(index/2)%4],
			BBOStateTag:   []float64{-0.9, 0.6, -0.2, 0.8}[(index/3)%4],
		}
	}
	model.ObserveFastDrift(start, price-1, price+1, horizon, 24*time.Hour, features[0], false)
	for index := 1; index < len(features); index++ {
		previous := features[index-1]
		noise := []float64{-0.25, 0.15, 0.30, -0.20, 0.05}[index%5]
		returnBps := 2 + 7*previous.Direction + 3*previous.BookImbalance +
			5*previous.BBOStateTag + noise
		if invertLabels && index > 16 {
			returnBps = -returnBps
		}
		price *= math.Exp(returnBps / 10_000)
		model.ObserveFastDrift(
			start.Add(time.Duration(index)*horizon), price-1, price+1,
			horizon, 24*time.Hour, features[index], false)
	}
	return model, features[len(features)-1]
}

func TestFastDriftLearnsMaturedExecutableSideReturns(t *testing.T) {
	model, features := trainSyntheticFastDrift(t, false)
	decision := model.FastDriftDecision(time.Minute, features)
	if !decision.Fitted || !decision.Healthy || !decision.Enabled {
		t.Fatalf("expected validated online drift, got %+v", decision)
	}
	want := 2 + 7*features.Direction + 3*features.BookImbalance + 5*features.BBOStateTag
	if math.Abs(decision.CenterMeanBps-want) > 0.15 {
		t.Fatalf("unexpected center prediction: got %.6f want %.6f", decision.CenterMeanBps, want)
	}
	if decision.Strength < 0.95 || decision.ValidationProbability < 0.975 {
		t.Fatalf("strong causal signal should receive nearly full weight: %+v", decision)
	}
	if decision.Samples != 31 || decision.ValidationSamples < fastDriftFeatureCount || decision.PrequentialSkill <= 0 {
		t.Fatalf("unexpected causal support: %+v", decision)
	}
}

func TestFastDriftBBOStateTagIsBoundedAndSideSymmetric(t *testing.T) {
	model := &MarketMakerHorizonModel{}
	horizon := time.Minute
	start := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	for second := 0; second <= 90; second++ {
		price := 100.0
		switch {
		case second < 45:
			price -= float64(second) * 0.01
		default:
			price -= 0.45
			price += float64(second-45) * 0.02
		}
		model.ObserveBookWithSizes(
			start.Add(time.Duration(second)*time.Second),
			price-0.01, 1, price+0.01, 1, MarketMakerConfig{})
	}
	tag, ok := model.FastDriftBBOStateTag(horizon)
	if !ok || tag <= 0 || tag > 1 {
		t.Fatalf("rebound state should produce bounded positive tag: tag=%v ok=%v", tag, ok)
	}
	state := model.conditionalExecutionState(horizon)
	reflected := conditionalExecutionState{
		Valid:          true,
		BuyDrawdownBps: state.SellRunupBps, BuyRebound30Bps: state.SellReversal30Bps,
		BuyQVBps: state.SellQVBps, SellRunupBps: state.BuyDrawdownBps,
		SellReversal30Bps: state.BuyRebound30Bps, SellQVBps: state.BuyQVBps,
		SpreadBps: state.SpreadBps,
	}
	reflectedTag, reflectedOK := fastDriftBBOStateTagFromState(reflected, horizon)
	if !reflectedOK {
		t.Fatal("reflected valid state did not produce a tag")
	}
	if math.Abs(tag+reflectedTag) > 1e-12 {
		t.Fatalf("side reflection must negate BBO tag: tag=%v reflected=%v", tag, reflectedTag)
	}
}

func TestFastDriftBBOStateTagIsFilteredOncePerMinute(t *testing.T) {
	model := &MarketMakerHorizonModel{}
	config := MarketMakerConfig{}
	start := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	for second := 0; second <= 70; second++ {
		price := 100 + 0.01*float64(second)
		model.ObserveBookWithSizes(start.Add(time.Duration(second)*time.Second),
			price-0.01, 1, price+0.01, 1, config)
	}
	first, ok := model.FastDriftBBOStateTag(time.Minute)
	if !ok {
		t.Fatal("expected valid cached BBO state tag")
	}
	// An extreme same-minute event must not become a second trading signal.
	model.ObserveBookWithSizes(start.Add(71*time.Second), 89.99, 1, 90.01, 1, config)
	second, ok := model.FastDriftBBOStateTag(time.Minute)
	if !ok || second != first {
		t.Fatalf("same-minute BBO tag was not filtered: first=%v second=%v", first, second)
	}
	if got := model.fastDriftBBOStateTags[time.Minute].Bucket; !got.Equal(start.Add(time.Minute)) {
		t.Fatalf("unexpected cache bucket: %v", got)
	}
	model.ObserveBookWithSizes(start.Add(2*time.Minute), 89.98, 1, 90, 1, config)
	if _, ok := model.FastDriftBBOStateTag(time.Minute); !ok {
		t.Fatal("next-minute BBO state tag did not refresh")
	}
	if got := model.fastDriftBBOStateTags[time.Minute].Bucket; !got.Equal(start.Add(2 * time.Minute)) {
		t.Fatalf("BBO tag cache did not advance: %v", got)
	}
}

func TestFastDriftRejectsRegimeInversionAndScoresRawForecast(t *testing.T) {
	model, features := trainSyntheticFastDrift(t, true)
	decision := model.FastDriftDecision(time.Minute, features)
	if !decision.Fitted {
		t.Fatalf("regression should remain fitted for diagnostics: %+v", decision)
	}
	if decision.Healthy || decision.Enabled || decision.Strength != 0 || decision.CenterMeanBps != 0 {
		t.Fatalf("regime-inverted evidence must fail closed: %+v", decision)
	}
	regression := model.fastDrift[time.Minute]
	regression.Anchor = nil
	model.ObserveFastDrift(
		time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 99_999, 100_001,
		time.Minute, 48*time.Hour, features, false)
	if regression.Anchor == nil || math.Abs(regression.Anchor.PredictedCenter-decision.RawCenterMeanBps) > 1e-9 {
		t.Fatalf("prequential anchor must score raw forecast, decision=%+v anchor=%+v", decision, regression.Anchor)
	}
}

func TestFastDriftFailsClosedWhenPrequentialForecastLosesToZero(t *testing.T) {
	model, features := trainSyntheticFastDrift(t, false)
	regression := model.fastDrift[time.Minute]
	for index := range regression.Samples {
		regression.Samples[index].PredictionReady = true
		regression.Samples[index].PredictedCenter = -regression.Samples[index].CenterReturnBps
	}
	decision := model.FastDriftDecision(time.Minute, features)
	if !decision.Fitted {
		t.Fatalf("regression should remain fitted for diagnostics: %+v", decision)
	}
	if decision.Healthy || decision.Enabled || decision.PrequentialSkill > 0 || decision.Strength != 0 {
		t.Fatalf("negative prequential gain must fail closed: %+v", decision)
	}
}

func TestFastDriftIsInsideQuoteAndReplacesHeuristicEvidenceShift(t *testing.T) {
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		InventoryTarget: 0.5, InventoryLimit: 0.5,
		FastDrift: FastDriftConfig{Enabled: true},
	}
	input := MarketMakerQuoteInput{
		MidPrice: 100_000, BestBid: 99_999, BestAsk: 100_001,
		VolatilityPerSqrtSec: 0.5, BuyVolatilityPerSqrtSec: 0.5,
		SellVolatilityPerSqrtSec: 0.5, TradingHorizonSeconds: 600,
		Inventory: 0.5, InventoryMin: 0, InventoryMax: 1,
		HardInventoryMin: 0, HardInventoryMax: 1,
		DirectionSignal: -0.9, BookImbalance: -0.8,
		FastDrift: FastDriftDecision{
			Enabled: true, Healthy: true, CenterMeanBps: 8,
			CenterVarianceBps2: 100, Samples: 20, ValidationSamples: 10,
			PrequentialSkill: 0.2, Reason: "test",
		},
		CanBuy: true, CanSell: true,
	}
	first := config.Quote(input)
	input.DirectionSignal = 0.9
	input.BookImbalance = 0.8
	second := config.Quote(input)
	if !first.FastDriftApplied || !second.FastDriftApplied {
		t.Fatalf("validated Fast drift was not integrated: first=%+v second=%+v", first, second)
	}
	if math.Abs(first.BidPrice-second.BidPrice) > 1e-9 || math.Abs(first.AskPrice-second.AskPrice) > 1e-9 {
		t.Fatalf("heuristic direction/book shift was double-counted: first=%+v second=%+v", first, second)
	}
	config.FastDrift.ShadowOnly = true
	shadow := config.Quote(input)
	config.FastDrift.Enabled = false
	baseline := config.Quote(input)
	if shadow.FastDriftApplied || math.Abs(shadow.BidPrice-baseline.BidPrice) > 1e-9 || math.Abs(shadow.AskPrice-baseline.AskPrice) > 1e-9 {
		t.Fatalf("shadow drift must preserve the exact Fast baseline: shadow=%+v baseline=%+v", shadow, baseline)
	}
}
