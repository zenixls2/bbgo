package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func macroTestConfig() MacroInventoryConfig {
	return MacroInventoryConfig{
		Enabled: true, BarInterval: types.Duration(24 * time.Hour),
		Lookback:       types.Duration(10 * 24 * time.Hour),
		RiskHorizons:   []types.Duration{types.Duration(24 * time.Hour)},
		MinimumSamples: 4, RiskAversion: 1, PriorStrength: 0.05,
		DriftPriorSamples: 4, DownsideZScore: 2.326347874,
		CarryRiskBudgetRatio: 0.01, MaxWealthDrawdownRatio: 0.05,
	}
}

func macroModelFromReturns(start time.Time, returns []float64) MacroInventoryModel {
	model := MacroInventoryModel{}
	price := 100.0
	model.bars = append(model.bars, macroInventoryBar{At: start, Mid: price, Segment: 1})
	for index, value := range returns {
		price *= math.Exp(value)
		model.bars = append(model.bars, macroInventoryBar{
			At: start.Add(time.Duration(index+1) * 24 * time.Hour), Mid: price, Segment: 1,
		})
	}
	return model
}

func TestMacroInventoryEstimateUsesNonOverlappingClosedBars(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	returns := []float64{-0.04, -0.02, 0.01, -0.03, 0.02, -0.01}
	model := macroModelFromReturns(start, returns)
	cfg := macroTestConfig()
	estimate := model.Estimate(start.Add(6*24*time.Hour), 24*time.Hour, 0, cfg)
	if estimate.Samples != len(returns) || !estimate.Sufficient || estimate.UsedFallback {
		t.Fatalf("expected sufficient non-overlapping daily returns: %+v", estimate)
	}
	wantMean := -0.07 / 6
	if math.Abs(estimate.Mean-wantMean) > 1e-12 {
		t.Fatalf("unexpected horizon mean: got %.12f want %.12f", estimate.Mean, wantMean)
	}
	if estimate.DownsideLoss < 0.04 {
		t.Fatalf("tail loss must not be smaller than the worst observed loss: %+v", estimate)
	}
}

func TestMacroInventoryFallbackHasZeroDriftAndHorizonScaledRisk(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := macroModelFromReturns(start, []float64{-0.02})
	cfg := macroTestConfig()
	estimate := model.Estimate(start.Add(24*time.Hour), 24*time.Hour, 0.5, cfg)
	if !estimate.UsedFallback || estimate.Sufficient || estimate.ShrunkMean != 0 {
		t.Fatalf("insufficient history must use zero-drift fallback: %+v", estimate)
	}
	wantStdDev := 0.5 / 10_000 * math.Sqrt((24 * time.Hour).Seconds())
	if math.Abs(estimate.StandardDeviation-wantStdDev) > 1e-12 {
		t.Fatalf("fallback risk must scale with sqrt(horizon): got %.12f want %.12f", estimate.StandardDeviation, wantStdDev)
	}
}

func TestMacroInventoryDecisionBudgetsActiveDeviationAroundStrategicCore(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := macroModelFromReturns(start, []float64{-0.04, -0.02, 0.01, -0.03, 0.02, -0.01, -0.05, 0.01})
	cfg := macroTestConfig()
	decision := cfg.Decide(&model, MacroInventoryInput{
		Now: start.Add(8 * 24 * time.Hour), WealthJPY: 10_000, WealthPeakJPY: 10_000,
		RiskyNotionalJPY: 5_000, PriorTargetRatio: 0.5, PolicyMinRatio: 0, PolicyMaxRatio: 1,
	})
	if !decision.Healthy || decision.DownsideLoss <= 0 {
		t.Fatalf("expected healthy historical macro decision: %+v", decision)
	}
	if decision.UtilityTargetRatio >= decision.PriorTargetRatio {
		t.Fatalf("negative drift and absolute variance must reduce the 50%% prior: %+v", decision)
	}
	headroom := cfg.CarryRiskBudgetRatio / decision.DownsideLoss
	if decision.CapitalCapRatio-decision.PriorTargetRatio > headroom+1e-12 ||
		decision.PriorTargetRatio-decision.CapitalFloorRatio > headroom+1e-12 {
		t.Fatalf("active deviation escaped the symmetric chance-loss budget: %+v", decision)
	}
	if decision.CapitalFloorRatio >= decision.PriorTargetRatio || decision.CapitalCapRatio <= decision.PriorTargetRatio {
		t.Fatalf("strategic core was not surrounded by an active-risk interval: %+v", decision)
	}
	if decision.TargetRatio < decision.CapitalFloorRatio-1e-12 ||
		decision.TargetRatio > decision.CapitalCapRatio+1e-12 {
		t.Fatalf("target must remain inside the active-risk interval: %+v", decision)
	}
}

func TestMacroInventoryDrawdownCushionTightensCap(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := macroModelFromReturns(start, []float64{-0.04, -0.02, 0.01, -0.03, 0.02, -0.01, -0.05, 0.01})
	cfg := macroTestConfig()
	input := MacroInventoryInput{
		Now: start.Add(8 * 24 * time.Hour), WealthJPY: 10_000, WealthPeakJPY: 10_000,
		RiskyNotionalJPY: 5_000, PriorTargetRatio: 0.5, PolicyMaxRatio: 1,
	}
	atPeak := cfg.Decide(&model, input)
	input.WealthJPY = 9_600
	input.RiskyNotionalJPY = 4_600
	drawnDown := cfg.Decide(&model, input)
	if drawnDown.DrawdownRatio <= 0 || drawnDown.DrawdownCapRatio >= atPeak.DrawdownCapRatio {
		t.Fatalf("wealth-cushion cap must tighten during drawdown: peak=%+v drawdown=%+v", atPeak, drawnDown)
	}
}

func TestMacroInventoryAggregatesUtilityButIntersectsRiskCaps(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := MacroInventoryModel{}
	price := 100.0
	model.bars = append(model.bars, macroInventoryBar{At: start, Mid: price, Segment: 1})
	for index := 0; index < 80; index++ {
		stepReturn := 0.002
		if index%7 == 0 {
			stepReturn = -0.006
		}
		price *= math.Exp(stepReturn)
		model.bars = append(model.bars, macroInventoryBar{
			At: start.Add(time.Duration(index+1) * 3 * time.Hour), Mid: price, Segment: 1,
		})
	}

	cfg := macroTestConfig()
	cfg.BarInterval = types.Duration(3 * time.Hour)
	cfg.RiskHorizons = []types.Duration{
		types.Duration(3 * time.Hour),
		types.Duration(6 * time.Hour),
		types.Duration(24 * time.Hour),
	}
	now := start.Add(10 * 24 * time.Hour)
	weightedUtility := 0.0
	weightSum := 0.0
	minimumUtility := 1.0
	minimumCap := 1.0
	for _, horizon := range []time.Duration{3 * time.Hour, 6 * time.Hour, 24 * time.Hour} {
		estimate := model.Estimate(now, horizon, 0, cfg)
		if !estimate.Sufficient {
			t.Fatalf("expected sufficient %s estimate: %+v", horizon, estimate)
		}
		utility := (estimate.ShrunkMean + cfg.PriorStrength*0.5) /
			(cfg.RiskAversion*estimate.Variance + cfg.PriorStrength)
		weight := macroUtilityReliabilityEffective(estimate.EffectiveSamples, cfg.DriftPriorSamples)
		weightedUtility += weight * utility
		weightSum += weight
		minimumUtility = math.Min(minimumUtility, utility)
		minimumCap = math.Min(minimumCap, 0.5+cfg.CarryRiskBudgetRatio/estimate.DownsideLoss)
	}
	wantUtility := weightedUtility / weightSum
	decision := cfg.Decide(&model, MacroInventoryInput{
		Now: now, WealthJPY: 10_000, WealthPeakJPY: 10_000,
		RiskyNotionalJPY: 5_000, PriorTargetRatio: 0.5, PolicyMaxRatio: 1,
	})
	if math.Abs(decision.UtilityTargetRatio-wantUtility) > 1e-12 {
		t.Fatalf("utility targets were not reliability-aggregated: got %.12f want %.12f", decision.UtilityTargetRatio, wantUtility)
	}
	if math.Abs(decision.UtilityTargetRatio-minimumUtility) < 1e-6 {
		t.Fatalf("utility aggregation unexpectedly retained the old worst-horizon minimum: %+v", decision)
	}
	if math.Abs(decision.CapitalCapRatio-minimumCap) > 1e-12 {
		t.Fatalf("hard carrying caps must retain their conservative intersection: got %.12f want %.12f", decision.CapitalCapRatio, minimumCap)
	}
	if decision.UtilityHorizons != 3 || math.Abs(decision.UtilityWeightSum-weightSum) > 1e-12 {
		t.Fatalf("unexpected utility aggregation telemetry: %+v", decision)
	}
	if decision.TargetRatio > decision.CapitalCapRatio+1e-12 {
		t.Fatalf("aggregated utility escaped the hard risk cap: %+v", decision)
	}
}

func TestMacroInventoryUtilityReliability(t *testing.T) {
	if got := macroUtilityReliability(0, 20); got != 0 {
		t.Fatalf("zero samples must carry zero utility weight: %.12f", got)
	}
	if got := macroUtilityReliability(20, 20); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("unexpected empirical-Bayes reliability: %.12f", got)
	}
	if got := macroUtilityReliability(20, 0); got != 1 {
		t.Fatalf("zero prior sample strength must fully weight observed utility: %.12f", got)
	}
}

func TestMacroInventoryModelExcludesOpenBar(t *testing.T) {
	cfg := macroTestConfig()
	cfg.BarInterval = types.Duration(15 * time.Minute)
	cfg.RiskHorizons = []types.Duration{types.Duration(6 * time.Hour)}
	cfg.MinimumSamples = 1
	var model MacroInventoryModel
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	for minute := -15; minute <= 720; minute++ {
		at := start.Add(time.Duration(minute) * time.Minute)
		model.Observe(at, 100+float64(minute)/100, false, cfg)
		if minute == 720 {
			model.Observe(at.Add(7*time.Minute), 10_000, false, cfg)
		}
	}
	// The 12:07 observation changes only the open 12:00 bar and must not
	// affect the two completed 6-hour returns ending at 06:00 and 12:00.
	estimate := model.Estimate(start.Add(12*time.Hour+7*time.Minute), 6*time.Hour, 0, cfg)
	if estimate.RawSamples != 25 || estimate.Samples != 1 || math.Abs(estimate.LatestReturn) > 0.1 {
		t.Fatalf("open bar leaked into macro returns: %+v", estimate)
	}
}

func TestMacroEstimateCacheKeepsIntrabarFallbackVolatilityLive(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := macroModelFromReturns(start, []float64{-0.02})
	cfg := macroTestConfig()
	low := model.Estimate(start.Add(24*time.Hour), 24*time.Hour, 0.1, cfg)
	high := model.Estimate(start.Add(24*time.Hour+time.Minute), 24*time.Hour, 1.0, cfg)
	if model.estimateCacheBuilds != 1 {
		t.Fatalf("intrabar fallback update rebuilt history %d times", model.estimateCacheBuilds)
	}
	if high.StandardDeviation <= low.StandardDeviation || high.DownsideLoss <= low.DownsideLoss {
		t.Fatalf("cached history froze live fallback risk: low=%+v high=%+v", low, high)
	}
}

func TestMacroFallbackVarianceWeightIsContinuousAtMinimumSamples(t *testing.T) {
	const minimumSamples = 8
	nearThresholdSamples := 7.92
	weight := macroFallbackVarianceWeight(nearThresholdSamples, minimumSamples)
	wantWeight := (float64(minimumSamples) - nearThresholdSamples) / float64(minimumSamples-1)
	if math.Abs(weight-wantWeight) > 1e-12 || weight >= 0.02 {
		t.Fatalf("near-threshold estimate received too much fallback weight: got %.12f want %.12f", weight, wantWeight)
	}
	if got := macroFallbackVarianceWeight(1, minimumSamples); got != 1 {
		t.Fatalf("one effective sample must use the full fallback: %.12f", got)
	}
	if got := macroFallbackVarianceWeight(minimumSamples, minimumSamples); got != 0 {
		t.Fatalf("a sufficient estimate must have zero fallback weight: %.12f", got)
	}

	base := MacroReturnEstimate{
		Horizon: 24 * time.Hour, Variance: 0.0004, StandardDeviation: 0.02,
		EffectiveSamples: nearThresholdSamples,
	}
	fallbackStdDev := 0.10
	fallbackBpsPerSqrtSecond := fallbackStdDev * 10_000 / math.Sqrt(base.Horizon.Seconds())
	got := applyMacroVolatilityFallback(base, fallbackBpsPerSqrtSecond, MacroInventoryConfig{
		MinimumSamples: minimumSamples, DownsideZScore: 2,
	})
	wantVariance := base.Variance + wantWeight*(fallbackStdDev*fallbackStdDev-base.Variance)
	if math.Abs(got.Variance-wantVariance) > 1e-12 || math.Abs(got.FallbackVarianceWeight-wantWeight) > 1e-12 {
		t.Fatalf("fallback variance was not continuously blended: got=%+v wantVariance=%.12f", got, wantVariance)
	}
	if got.StandardDeviation >= fallbackStdDev {
		t.Fatalf("a nearly sufficient horizon estimate jumped to the full intrabar fallback: %+v", got)
	}
}
