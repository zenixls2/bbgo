package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func rollingMacroTestConfig() MacroInventoryConfig {
	return MacroInventoryConfig{
		Enabled: true, BarInterval: types.Duration(10 * time.Minute),
		Lookback: types.Duration(10 * 24 * time.Hour),
		RiskHorizons: []types.Duration{
			types.Duration(3 * time.Hour),
			types.Duration(6 * time.Hour),
			types.Duration(24 * time.Hour),
		},
		MinimumSamples: 8, RiskAversion: 1, PriorStrength: 0.05,
		DriftPriorSamples: 20, DownsideZScore: 2.326347874,
		CarryRiskBudgetRatio: 0.01, MaxWealthDrawdownRatio: 0.05,
		ReversalAccumulation: MacroReversalConfig{
			Enabled: true, EarlyDetection: true,
			EarlyPosteriorBars: 2, EarlyMinimumPriorBars: 6,
		},
	}
}

func rollingMacroModel(start time.Time, bars int, reversal bool) MacroInventoryModel {
	model := MacroInventoryModel{}
	logPrice := math.Log(100.0)
	for index := 0; index <= bars; index++ {
		if index > 0 {
			step := 0.00008*math.Sin(float64(index)*0.37) + 0.00004*math.Sin(float64(index)*0.11)
			remaining := bars - index
			if reversal && remaining < 18 {
				if remaining >= 6 {
					step = -0.001
				} else {
					step = 0.002
				}
			}
			logPrice += step
		}
		price := math.Exp(logPrice)
		model.bars = append(model.bars, macroInventoryBar{
			At:  start.Add(time.Duration(index) * 10 * time.Minute),
			Mid: price, Ask: price * 1.0001, Segment: 1,
		})
	}
	return model
}

func TestMacroRollingEstimateAdvancesEveryClosedBarWithOverlapCorrection(t *testing.T) {
	cfg := rollingMacroTestConfig()
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := rollingMacroModel(start, 10*24*6, false)
	now := model.bars[len(model.bars)-1].At
	for _, horizon := range []time.Duration{3 * time.Hour, 6 * time.Hour, 24 * time.Hour} {
		estimate := model.Estimate(now, horizon, 0, cfg)
		overlap := horizon.Seconds() / (10 * time.Minute).Seconds()
		wantEffective := float64(estimate.RawSamples) / overlap
		if estimate.RawSamples <= estimate.Samples || math.Abs(estimate.EffectiveSamples-wantEffective) > 1e-12 {
			t.Fatalf("%s did not correct rolling overlap: %+v", horizon, estimate)
		}
		if !estimate.Sufficient {
			t.Fatalf("%s should be sufficient after ten rolling days: %+v", horizon, estimate)
		}
	}
	before := model.Estimate(now, 3*time.Hour, 0, cfg)
	last := model.bars[len(model.bars)-1]
	model.bars = append(model.bars, macroInventoryBar{
		At: last.At.Add(10 * time.Minute), Mid: last.Mid * 1.01,
		Ask: last.Ask * 1.01, Segment: last.Segment,
	})
	after := model.Estimate(now.Add(10*time.Minute), 3*time.Hour, 0, cfg)
	if after.LatestReturn == before.LatestReturn {
		t.Fatalf("rolling 3h state did not advance on the next 10m close: before=%+v after=%+v", before, after)
	}
}

func reversalInput(now time.Time) MacroReversalInput {
	return MacroReversalInput{
		Now: now, BaselineTargetRatio: 0.15, CurrentRiskyWeight: 0.16,
		PolicyMaxRatio: 1, RoundTripCostBps: 26,
		ConfidenceZScore: 1.645,
		RiskAversion:     1, FallbackVolatilityBpsPerSqrtSec: 0.5,
	}
}

func TestMacroReversalRaisesTargetOnFeePositiveDownToUpPath(t *testing.T) {
	cfg := rollingMacroTestConfig()
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := rollingMacroModel(start, 10*24*6, true)
	now := model.bars[len(model.bars)-1].At
	decision := cfg.DecideReversal(&model, reversalInput(now))
	if !decision.Healthy || !decision.Applied || decision.ActiveHorizons == 0 {
		t.Fatalf("expected fee-positive rolling reversal target uplift: %+v", decision)
	}
	if decision.TargetRatio <= decision.BaselineTargetRatio || decision.TargetRatio > 1 {
		t.Fatalf("reversal target escaped admissible range: %+v", decision)
	}
	if decision.HealthyHorizons != 3 || decision.ActiveHorizons != 1 {
		t.Fatalf("test must exercise one selected signal averaged with two healthy neutral horizons: %+v", decision)
	}
	activeTarget := 0.0
	for _, horizon := range decision.Horizons {
		if horizon.Applied {
			activeTarget = horizon.RobustTargetRatio
		}
	}
	if decision.TargetRatio >= activeTarget {
		t.Fatalf("healthy neutral horizons did not denoise the selected-window target: %+v", decision)
	}
	if decision.AdditionalHeadroomRatio <= 0 {
		t.Fatalf("reversal did not create executable inventory headroom: %+v", decision)
	}
}

func TestMacroReversalFailsClosedWithoutTurnOrAfterExcessiveFees(t *testing.T) {
	cfg := rollingMacroTestConfig()
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := rollingMacroModel(start, 10*24*6, false)
	now := model.bars[len(model.bars)-1].At
	withoutTurn := cfg.DecideReversal(&model, reversalInput(now))
	if withoutTurn.Applied {
		t.Fatalf("oscillating path without a material down-to-up change must not increase target: %+v", withoutTurn)
	}
	model = rollingMacroModel(start, 10*24*6, true)
	normal := cfg.DecideReversal(&model, reversalInput(now))
	if !normal.Applied {
		t.Fatalf("test path must establish a fee-positive cached reversal: %+v", normal)
	}
	builds := model.reversalCacheBuilds
	input := reversalInput(now)
	input.RoundTripCostBps = 10_000
	expensive := cfg.DecideReversal(&model, input)
	if model.reversalCacheBuilds != builds {
		t.Fatalf("fee-only update unexpectedly rebuilt BIC structure: before=%d after=%d", builds, model.reversalCacheBuilds)
	}
	if expensive.Applied {
		t.Fatalf("reversal must not bypass a negative fee-adjusted lower edge: %+v", expensive)
	}
}

func TestMacroClosedBarCachesAvoidIntrabarHistoryRescans(t *testing.T) {
	cfg := rollingMacroTestConfig()
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := rollingMacroModel(start, 10*24*6, true)
	now := model.bars[len(model.bars)-1].At
	inventoryInput := MacroInventoryInput{
		Now: now, WealthJPY: 10_000, WealthPeakJPY: 10_000,
		RiskyNotionalJPY: 1_600, PriorTargetRatio: 0.5, PolicyMaxRatio: 1,
		FallbackVolatilityBpsPerSqrtSec: 0.5,
	}
	cfg.Decide(&model, inventoryInput)
	cfg.DecideReversal(&model, reversalInput(now))
	if model.estimateCacheBuilds != 3 || model.reversalCacheBuilds != 3 {
		t.Fatalf("first evaluation should build one cache entry per horizon: estimates=%d reversals=%d",
			model.estimateCacheBuilds, model.reversalCacheBuilds)
	}
	for second := 1; second <= 100; second++ {
		inventoryInput.Now = now.Add(time.Duration(second) * time.Second)
		inventoryInput.WealthJPY += 0.1
		cfg.Decide(&model, inventoryInput)
		reversal := reversalInput(inventoryInput.Now)
		reversal.ConfidenceZScore += float64(second) * 1e-7
		cfg.DecideReversal(&model, reversal)
	}
	if model.estimateCacheBuilds != 3 || model.reversalCacheBuilds != 3 {
		t.Fatalf("intrabar ticks rescanned history: estimates=%d reversals=%d",
			model.estimateCacheBuilds, model.reversalCacheBuilds)
	}
	last := model.bars[len(model.bars)-1]
	model.bars = append(model.bars, macroInventoryBar{
		At: last.At.Add(10 * time.Minute), Mid: last.Mid * 1.001,
		Ask: last.Ask * 1.001, Segment: last.Segment,
	})
	inventoryInput.Now = model.bars[len(model.bars)-1].At
	cfg.Decide(&model, inventoryInput)
	cfg.DecideReversal(&model, reversalInput(inventoryInput.Now))
	if model.estimateCacheBuilds != 6 || model.reversalCacheBuilds != 6 {
		t.Fatalf("new closed bar did not invalidate exactly one entry per horizon: estimates=%d reversals=%d",
			model.estimateCacheBuilds, model.reversalCacheBuilds)
	}
}

func TestMacroRegimeLeaseRetainsEarlyAllocationAcrossOneBarSignalLoss(t *testing.T) {
	now := time.Date(2026, 8, 3, 13, 20, 0, 0, time.UTC)
	state := MacroInventoryState{}
	direct := MacroReversalDecision{
		Enabled: true, Applied: true, Direction: 1, BaselineTargetRatio: 0.18,
		TargetRatio: 0.90, CurrentRiskyWeight: 0.16,
		AggregateProbability: 0.98, AggregateNetEdgeBps: 10.5,
		SignalChangeAt: now.Add(-80 * time.Minute), SignalForecastHorizon: 80 * time.Minute,
	}
	got, changed := state.ApplyRegimeLease(now, 0, 0.93, direct)
	if !changed || got.LeaseSurvivalProbability != 1 || state.RegimeTargetRatio != 0.90 {
		t.Fatalf("direct regime signal was not persisted: decision=%+v state=%+v", got, state)
	}
	missing := MacroReversalDecision{
		Enabled: true, BaselineTargetRatio: 0.18, TargetRatio: 0.18,
		CurrentRiskyWeight: 0.16,
	}
	got, changed = state.ApplyRegimeLease(now.Add(10*time.Minute), 0, 0.93, missing)
	wantSurvival := math.Exp(-10.0 / 80.0)
	wantTarget := 0.18 + wantSurvival*(0.90-0.18)
	if changed || !got.Applied || !got.LeaseApplied || math.Abs(got.TargetRatio-wantTarget) > 1e-12 || got.TargetRatio <= 0.5 {
		t.Fatalf("one adverse bar erased the early majority target: got=%+v wantTarget=%.12f", got, wantTarget)
	}
	capped, _ := state.ApplyRegimeLease(now.Add(20*time.Minute), 0, 0.40, missing)
	if math.Abs(capped.TargetRatio-0.40) > 1e-12 {
		t.Fatalf("regime persistence bypassed the live wealth-drawdown cap: %+v", capped)
	}
}
func rollingMacroBearishModel(start time.Time, bars int) MacroInventoryModel {
	model := MacroInventoryModel{}
	logPrice := math.Log(100.0)
	for index := 0; index <= bars; index++ {
		if index > 0 {
			step := 0.00008*math.Sin(float64(index)*0.37) + 0.00004*math.Sin(float64(index)*0.11)
			remaining := bars - index
			if remaining < 18 {
				if remaining >= 6 {
					step = 0.001
				} else {
					step = -0.002
				}
			}
			logPrice += step
		}
		price := math.Exp(logPrice)
		model.bars = append(model.bars, macroInventoryBar{
			At:  start.Add(time.Duration(index) * 10 * time.Minute),
			Mid: price, Bid: price * 0.9999, Ask: price * 1.0001, Segment: 1,
		})
	}
	return model
}

func TestMacroBearishReversalCutsLongOnlyTargetAfterFees(t *testing.T) {
	cfg := rollingMacroTestConfig()
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := rollingMacroBearishModel(start, 10*24*6)
	now := model.bars[len(model.bars)-1].At
	input := reversalInput(now)
	input.CurrentRiskyWeight = 0.85
	decision := cfg.DecideReversal(&model, input)
	if !decision.Healthy || !decision.Applied || decision.Direction != -1 || decision.ActiveHorizons == 0 {
		t.Fatalf("expected a statistically confirmed bearish inventory reduction: %+v", decision)
	}
	if decision.TargetRatio <= 0 || decision.TargetRatio >= decision.BaselineTargetRatio ||
		decision.InventoryAdjustmentRatio >= 0 {
		t.Fatalf("multi-horizon bearish target must reduce continuously without an aggregate jump to zero: %+v", decision)
	}

	input.RoundTripCostBps = 10_000
	expensive := cfg.DecideReversal(&model, input)
	if expensive.Applied {
		t.Fatalf("bearish liquidation must fail closed when avoided loss does not cover fees: %+v", expensive)
	}
}

func TestMacroTacticalTargetIsContinuousAroundFeeThreshold(t *testing.T) {
	const baseline = 0.5
	bullish := macroTacticalTarget(baseline, 0.0001, 0.01, 0, 1)
	bearish := macroTacticalTarget(baseline, -0.0001, 0.01, 0, 1)
	if math.Abs(bullish-0.51) > 1e-12 || math.Abs(bearish-0.49) > 1e-12 {
		t.Fatalf("fee-positive edge should create a continuous mean/variance shift: bullish=%.12f bearish=%.12f", bullish, bearish)
	}
	if got := macroTacticalTarget(baseline, -0.000001, 0.01, 0, 1); got <= 0 || got >= baseline {
		t.Fatalf("an arbitrarily small bearish edge jumped to the long-only floor: %.12f", got)
	}
}

func TestMacroBearishSignalImmediatelyOverridesBullishLease(t *testing.T) {
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	state := MacroInventoryState{}
	bullish := MacroReversalDecision{
		Enabled: true, Applied: true, Direction: 1,
		BaselineTargetRatio: 0.18, TargetRatio: 0.90, CurrentRiskyWeight: 0.20,
		AggregateProbability: 0.98, AggregateNetEdgeBps: 40,
		SignalChangeAt: now.Add(-time.Hour), SignalForecastHorizon: 2 * time.Hour,
	}
	if _, changed := state.ApplyRegimeLease(now, 0, 0.95, bullish); !changed {
		t.Fatal("bullish episode was not persisted")
	}
	bearish := MacroReversalDecision{
		Enabled: true, Applied: true, Direction: -1,
		BaselineTargetRatio: 0.18, TargetRatio: 0, CurrentRiskyWeight: 0.85,
		AggregateProbability: 0.99, AggregateNetEdgeBps: -35,
		SignalChangeAt: now.Add(10 * time.Minute), SignalForecastHorizon: time.Hour,
	}
	got, changed := state.ApplyRegimeLease(now.Add(10*time.Minute), 0, 0.95, bearish)
	if !changed || got.Direction != -1 || got.TargetRatio != 0 || state.RegimeDirection != -1 {
		t.Fatalf("bearish evidence did not immediately replace bullish persistence: decision=%+v state=%+v", got, state)
	}
	missing := MacroReversalDecision{
		Enabled: true, BaselineTargetRatio: 0.18, TargetRatio: 0.18,
		CurrentRiskyWeight: 0.80,
	}
	leased, _ := state.ApplyRegimeLease(now.Add(20*time.Minute), 0, 0.95, missing)
	if !leased.LeaseApplied || leased.Direction != -1 || leased.TargetRatio >= leased.BaselineTargetRatio {
		t.Fatalf("bearish regime did not persist below baseline: %+v", leased)
	}
}

func TestMacroSubBarReplayGapDoesNotDiscardClosedHistory(t *testing.T) {
	cfg := rollingMacroTestConfig()
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := rollingMacroModel(start, 10*24*6, true)
	last := model.bars[len(model.bars)-1]
	model.currentStart = last.At
	model.currentMid = last.Mid
	model.currentBid = last.Mid * 0.9999
	model.currentAsk = last.Ask
	model.currentSegment = last.Segment
	model.lastObservation = last.At

	model.ObserveBBO(last.At.Add(5*time.Minute), last.Mid, last.Mid*0.9999, last.Ask, true, cfg)
	model.ObserveBBO(last.At.Add(10*time.Minute+time.Second), last.Mid, last.Mid*0.9999, last.Ask, false, cfg)
	if got := model.bars[len(model.bars)-1].Segment; got != last.Segment {
		t.Fatalf("sub-bar replay/websocket gap created a new macro segment: got %d want %d", got, last.Segment)
	}
	if points := model.rollingExecutableBars(last.At.Add(10*time.Minute), 3*time.Hour, 10*time.Minute); len(points) < 8 {
		t.Fatalf("sub-bar startup gap discarded usable rolling history: bars=%d", len(points))
	}
}

func TestMacroReversalPolicyIncludesCarryCap(t *testing.T) {
	config := MarketMakerConfig{InventoryCapitalMaxRatio: 1}
	decision := MacroInventoryDecision{CapitalFloorRatio: 0.32, DrawdownCapRatio: 0.9, CapitalCapRatio: 0.68}
	if got := macroReversalPolicyMinRatio(config, decision); math.Abs(got-0.32) > 1e-12 {
		t.Fatalf("reversal policy must retain the macro active-risk floor: got %.8f", got)
	}
	if got := macroReversalPolicyMaxRatio(config, decision); math.Abs(got-0.68) > 1e-12 {
		t.Fatalf("reversal policy must retain the macro active-risk cap: got %.8f", got)
	}
}

func macroEarlyTurnModel(start time.Time, direction int) MacroInventoryModel {
	const bars = 10 * 24 * 6
	model := MacroInventoryModel{}
	logPrice := math.Log(100.0)
	for index := 0; index <= bars; index++ {
		if index > 0 {
			step := 0.00015*math.Sin(float64(index)*0.37) + 0.00008*math.Sin(float64(index)*0.11)
			remaining := bars - index
			if remaining <= 6 {
				if remaining >= 2 {
					step = -float64(direction) * 0.002
				} else {
					step = float64(direction) * 0.006
				}
			}
			logPrice += step
		}
		price := math.Exp(logPrice)
		model.bars = append(model.bars, macroInventoryBar{
			At:  start.Add(time.Duration(index) * 10 * time.Minute),
			Mid: price, Bid: price * 0.9999, Ask: price * 1.0001, Segment: 1,
		})
	}
	return model
}

func earlyReversalInput(now time.Time) MacroReversalInput {
	return MacroReversalInput{
		Now: now, BaselineTargetRatio: 0.5, CurrentRiskyWeight: 0.5,
		PolicyMinRatio: 0.3, PolicyMaxRatio: 0.7, RoundTripCostBps: 26,
		ConfidenceZScore: 1.645, RiskAversion: 1,
		FallbackVolatilityBpsPerSqrtSec: 0.5,
	}
}

func TestMacroSequentialEarlyBullishReversalActsAfterTwoClosedBars(t *testing.T) {
	cfg := rollingMacroTestConfig()
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := macroEarlyTurnModel(start, 1)

	before := model
	before.bars = append([]macroInventoryBar(nil), model.bars[:len(model.bars)-1]...)
	beforeNow := before.bars[len(before.bars)-1].At
	beforeDecision := cfg.DecideReversal(&before, earlyReversalInput(beforeNow))
	if len(beforeDecision.Horizons) == 0 || beforeDecision.Horizons[0].Early {
		t.Fatalf("one posterior bar must not trigger the early path: %+v", beforeDecision)
	}

	now := model.bars[len(model.bars)-1].At
	decision := cfg.DecideReversal(&model, earlyReversalInput(now))
	if !decision.Applied || decision.Direction != 1 || len(decision.Horizons) == 0 || !decision.Horizons[0].Early {
		t.Fatalf("two fee-positive posterior bars did not trigger early bullish accumulation: %+v", decision)
	}
	if decision.Horizons[0].PosteriorBars != 2 || math.Abs(decision.Horizons[0].SignalReliability-0.2) > 1e-12 {
		t.Fatalf("unexpected early evidence reliability: %+v", decision.Horizons[0])
	}
	if decision.TargetRatio <= 0.5 || decision.TargetRatio >= 0.7 {
		t.Fatalf("early bullish target must be staged strictly inside the full risk interval: %+v", decision)
	}
}

func TestMacroSequentialEarlyBearishReversalActsSymmetrically(t *testing.T) {
	cfg := rollingMacroTestConfig()
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := macroEarlyTurnModel(start, -1)
	now := model.bars[len(model.bars)-1].At
	decision := cfg.DecideReversal(&model, earlyReversalInput(now))
	if !decision.Applied || decision.Direction != -1 || len(decision.Horizons) == 0 || !decision.Horizons[0].Early {
		t.Fatalf("two fee-positive posterior bars did not trigger early bearish reduction: %+v", decision)
	}
	if decision.TargetRatio >= 0.5 || decision.TargetRatio <= 0.3 {
		t.Fatalf("early bearish target must be staged strictly inside the full risk interval: %+v", decision)
	}
}
