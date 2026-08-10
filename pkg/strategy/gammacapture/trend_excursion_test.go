package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func trendExcursionTestModel(start time.Time, count int) MacroInventoryModel {
	model := MacroInventoryModel{}
	logPrice := math.Log(100)
	for i := 0; i < count; i++ {
		if i > 0 {
			if i%12 <= 5 {
				logPrice += 0.003
			} else {
				logPrice -= 0.003
			}
		}
		mid := math.Exp(logPrice)
		model.bars = append(model.bars, macroInventoryBar{
			At:  start.Add(time.Duration(i) * 10 * time.Minute),
			Mid: mid, Bid: mid * 0.9999, Ask: mid * 1.0001, Segment: 1,
		})
	}
	return model
}

func TestTrendExcursionFindsCausalEarlyUpswingAnalogs(t *testing.T) {
	cfg := MacroInventoryConfig{
		Enabled: true, BarInterval: types.Duration(10 * time.Minute),
		Lookback: types.Duration(48 * time.Hour),
		RiskHorizons: []types.Duration{
			types.Duration(20 * time.Minute),
			types.Duration(40 * time.Minute),
			types.Duration(60 * time.Minute),
		},
		MinimumSamples: 4,
	}
	model := trendExcursionTestModel(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), 121)
	now := model.LatestClosedBarAt()
	decision := model.EstimateTrendExcursion(now, cfg, true, 20)
	if !decision.Healthy || decision.Direction != 1 || decision.ExpectedReturn <= 0 ||
		decision.ModelProbability <= 0 || decision.ExpectedPivot <= 0 ||
		decision.ExpectedPivot > 20*time.Minute {
		t.Fatalf("repeated causal early-upswing analog was not identified: %+v", decision)
	}
	if decision.Samples <= decision.Neighbors || decision.Neighbors < cfg.MinimumSamples {
		t.Fatalf("sqrt-N neighbor selection or independent sample pool is invalid: %+v", decision)
	}
}

func TestTrendExcursionRequiresExecutableSidesToAgree(t *testing.T) {
	if got := conservativeTrendMean(0.01, -0.02); got != 0 {
		t.Fatalf("opposing ask/bid forecasts must be neutral, got %.8f", got)
	}
	if got := conservativeTrendMean(0.01, 0.02); got != 0.01 {
		t.Fatalf("bullish executable forecasts must retain weaker magnitude, got %.8f", got)
	}
	if got := conservativeTrendMean(-0.01, -0.02); got != -0.01 {
		t.Fatalf("bearish executable forecasts must retain weaker magnitude, got %.8f", got)
	}
}

func TestNoTradeInventoryCanUseTrendWhenCrossingsAreDegraded(t *testing.T) {
	in := noTradeTestInput()
	in.CrossingHealth = HealthDegraded
	in.ExecutableCrossingHealth = HealthDegraded
	in.TrendExcursion = TrendExcursionDecision{
		Enabled: true, Healthy: true, ForecastHorizon: 3 * time.Hour,
		Direction: 1, ExpectedReturn: 0.02, ReturnVariance: 0.0001,
		MeanSE: 0.001, ModelProbability: 0.9,
	}
	decision := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, TrendExcursionEnabled: true}, in)
	if !decision.Healthy || decision.RawAimRatio <= in.PriorTargetRatio ||
		decision.ForecastReturn <= 0 {
		t.Fatalf("healthy long-window posterior did not replace unavailable crossings: %+v", decision)
	}
}

func TestTrendExcursionStructuralPosteriorSelectsConditionalSide(t *testing.T) {
	trend := TrendExcursionDecision{
		Enabled: true, Healthy: true, Direction: -1,
		PosteriorUpProbability: 0.4, ModelProbability: 0.2,
		UpRemainingExcursion: 0.03, DownRemainingExcursion: 0.01,
		UpProfitProbability: 0.8, DownProfitProbability: 0.6,
		UpMeanSE: 0.002, DownMeanSE: 0.004,
		UpExpectedPivot: 40 * time.Minute, DownExpectedPivot: 20 * time.Minute,
	}
	got := ConditionTrendExcursionOnReversal(trend, MacroReversalDecision{
		Applied: true, Direction: 1, AggregateProbability: 0.8,
	}, 1.645)
	if got.Direction != 1 || got.StructuralDirection != 1 ||
		math.Abs(got.ModelProbability-0.6) > 1e-12 ||
		got.ExpectedReturn != trend.UpRemainingExcursion ||
		got.MeanSE != trend.UpMeanSE || got.ExpectedPivot != trend.UpExpectedPivot {
		t.Fatalf("structural posterior did not select bullish conditional excursion once: %+v", got)
	}
}

func TestMultiplicityCorrectedReversalProbabilityRetainsStrongWindow(t *testing.T) {
	reversal := MacroReversalDecision{
		Direction: 1, HealthyHorizons: 3, AggregateProbability: 0.58,
		Horizons: []MacroReversalHorizonDecision{
			{Direction: 1, Applied: true, ReversalProbability: 0.95},
			{Direction: 0}, {Direction: 0},
		},
	}
	got := multiplicityCorrectedReversalProbability(reversal)
	odds := 0.95 / 0.05 / 3
	want := odds / (1 + odds)
	if math.Abs(got-want) > 1e-12 || got <= reversal.AggregateProbability {
		t.Fatalf("selection-penalized posterior lost strong horizon evidence: got=%.12f want=%.12f", got, want)
	}
}

func TestRobustStructuralExcursionUsesSlopeConfidenceAndCapsProjection(t *testing.T) {
	reversal := MacroReversalDecision{
		Direction: 1,
		Horizons: []MacroReversalHorizonDecision{{
			Applied: true, Direction: 1, ReversalProbability: 0.9,
			ForecastMeanBps: 100, ForecastMeanSEBps: 10,
			ForecastHorizon: 30 * time.Minute,
		}},
	}
	got := robustStructuralExcursion(reversal, 1, 90*time.Minute, 2)
	// Projection is capped at the observed 30-minute post-change leg:
	// (100 - 2*10) bps = 80 bps = 0.008.
	if math.Abs(got-0.008) > 1e-12 {
		t.Fatalf("unexpected confidence-bounded structural excursion: %.12f", got)
	}
}

func TestTrendContinuationUsesOneCompetingRiskSimplex(t *testing.T) {
	samples := make([]trendExcursionSample, 9)
	for i := range samples {
		samples[i] = trendExcursionSample{
			continuationOutcome: -1, continuationDownExcursion: 0.004,
			continuationFirstStep: 2,
		}
	}
	samples[len(samples)-1] = trendExcursionSample{
		continuationOutcome: 1, continuationUpExcursion: 0.003,
		continuationFirstStep: 3,
	}
	got := summarizeTrendContinuation(samples, trendConsolidationState{
		window: 30 * time.Minute, recentDirection: -1, consolidationScore: 0.8,
	}, 3*time.Hour, 10*time.Minute, 2.326347874)
	if !got.Healthy || got.Direction != -1 || got.DownFirst != 8 ||
		got.UpFirst != 1 || got.Censored != 0 || got.ExpectedReturn >= 0 ||
		got.DownGivenMoveLower <= 0.5 {
		t.Fatalf("credible down-first continuation was not retained: %+v", got)
	}
	if math.Abs(got.UpProbability+got.DownProbability+got.CensorProbability-1) > 1e-12 {
		t.Fatalf("competing-risk posterior left the probability simplex: %+v", got)
	}
}

func TestTrendConsolidationStateKeepsDirectionDuringFlatPause(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]macroInventoryBar, 7)
	prices := []float64{100, 99, 98, 97, 96.9, 97.0, 96.9}
	for i, price := range prices {
		bars[i] = macroInventoryBar{
			At:  start.Add(time.Duration(i) * 10 * time.Minute),
			Mid: price, Bid: price - 0.01, Ask: price + 0.01, Segment: 1,
		}
	}
	state, ok := trendConsolidationStateAt(
		bars, len(bars)-1, []time.Duration{60 * time.Minute}, 10*time.Minute)
	if !ok || state.recentDirection != -1 || state.consolidationScore <= 0.25 ||
		state.window != 30*time.Minute {
		t.Fatalf("flat pause lost its preceding bearish direction: ok=%v state=%+v", ok, state)
	}
}
