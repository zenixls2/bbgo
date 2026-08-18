package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestJointPathPayoffPenalizesSellingIntoContinuation(t *testing.T) {
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		HorizonLookback: types.Duration(50 * time.Minute),
	}
	for second := 0; second <= 60*60; second++ {
		mid := 100 * math.Exp(float64(second)*2.0/60.0/10_000)
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	stats := model.JointPathPayoffStatistics(
		start.Add(time.Hour), config, 10*time.Minute, 5, 5)
	if stats.EffectiveSamples < 2 {
		t.Fatalf("insufficient path samples: %+v", stats)
	}
	if stats.SellDominant.SellMeanBps >= 0 {
		t.Fatalf("continued rise must make passive SELL terminal wealth adverse: %+v", stats)
	}
	if math.Abs(stats.BuyDominant.BuyMeanBps) > 1e-9 {
		t.Fatalf("untouched BUY should contribute zero payoff: %+v", stats)
	}
}

func TestJointPathPayoffRewardsCompletedOscillation(t *testing.T) {
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		HorizonLookback: types.Duration(40 * time.Minute),
	}
	horizon := 5 * time.Minute
	for second := 0; second <= 45*60; second++ {
		phase := 2 * math.Pi * float64(second) / horizon.Seconds()
		mid := 100 * math.Exp(0.006*math.Sin(phase))
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	stats := model.JointPathPayoffStatistics(
		start.Add(45*time.Minute), config, horizon, 30, 30)
	decision := stats.Evaluate(500, 500, 7000, 1, 1.282)
	if stats.EffectiveSamples < 3 {
		t.Fatalf("insufficient path samples: %+v", stats)
	}
	if decision.ExpectedPnLJPY <= 0 || decision.CertaintyEquivalent <= 0 {
		t.Fatalf("completed fee-net oscillation should have positive wealth utility: stats=%+v decision=%+v", stats, decision)
	}
}

func TestJointPathPayoffConditionsOneSidedMeanOnCurrentBookImbalance(t *testing.T) {
	if !sideImbalancePayoffEnabled(15*time.Minute) ||
		sideImbalancePayoffEnabled(10*time.Minute) ||
		sideImbalancePayoffEnabled(30*time.Minute) {
		t.Fatal("side-imbalance payoff must remain on its statistically accepted 15m clock")
	}
	start := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	horizon := 15 * time.Minute
	now := start.Add(2 * time.Hour)
	exposures := make([]marketMakerHorizonExposure, 100)
	for index := range exposures {
		imbalance := -0.8
		terminalBid := 99.5
		if index%2 == 1 {
			imbalance = 0.8
			terminalBid = 101.5
		}
		exposures[index] = marketMakerHorizonExposure{
			At:       start.Add(time.Duration(index) * time.Minute),
			EndAt:    start.Add(time.Duration(index)*time.Minute + horizon),
			StartBid: 99.9, StartAsk: 100.1,
			StartBBOWeightedPrice: 100, WindowBBOWeightedPrice: 100,
			StartBookImbalance: imbalance, StartBookDepthReady: true,
			TerminalBid: terminalBid, TerminalAsk: terminalBid + 0.1,
			BuyExcursionBps: 20, NextMinute: index + 1,
		}
	}
	statistics := func(currentImbalance float64, liveDepthReady, trainingDepthReady bool) JointPathPayoffStats {
		pathData := append([]marketMakerHorizonExposure(nil), exposures...)
		if !trainingDepthReady {
			for index := range pathData {
				pathData[index].StartBookDepthReady = false
			}
		}
		model := MarketMakerHorizonModel{
			points: []MarketMakerHorizonPoint{{
				At: now, Bid: 100, Ask: 100.1,
				BookImbalance: currentImbalance, BookDepthReady: liveDepthReady,
			}},
			crossingExposureCaches: map[time.Duration]*marketMakerHorizonExposureCache{
				horizon: {Initialized: true, LastPointAt: now, Exposures: pathData},
			},
		}
		return model.JointPathPayoffStatistics(now, MarketMakerConfig{
			HorizonLookback: types.Duration(3 * time.Hour), MakerFeeBps: 10,
		}, horizon, 10, 10)
	}

	high := statistics(0.8, true, true)
	low := statistics(-0.8, true, true)
	missing := statistics(0.8, false, true)
	unconditional := statistics(0.8, true, false)
	if high.BuyDominant.BuyMeanBps <= low.BuyDominant.BuyMeanBps {
		t.Fatalf("BUY payoff mean must follow its separately trained depth slope: high=%+v low=%+v",
			high.BuyDominant, low.BuyDominant)
	}
	wantBaseline := unconditional.BuyDominant.BuyMeanBps
	if math.Abs(missing.BuyDominant.BuyMeanBps-wantBaseline) > 1e-9 {
		t.Fatalf("missing live depth must retain the unconditional payoff mean: got=%v want=%v",
			missing.BuyDominant.BuyMeanBps, wantBaseline)
	}
	if high.SellDominant.SellMeanBps != low.SellDominant.SellMeanBps {
		t.Fatal("BUY imbalance conditioning must not alter the SELL payoff component")
	}
}

func TestPairedOutwardDistanceRequiresPathwiseImprovement(t *testing.T) {
	start := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	horizon := 5 * time.Minute
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		HorizonLookback: types.Duration(40 * time.Minute),
	}
	model := MarketMakerHorizonModel{}
	for second := 0; second <= 45*60; second++ {
		phase := 2 * math.Pi * float64(second) / horizon.Seconds()
		mid := 100 * math.Exp(0.008*math.Sin(phase))
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	d := model.jointBalancedPathPayoffDifference(
		start.Add(45*time.Minute), config, horizon,
		20, 20, 40, 40, conditionalExecutionState{})
	if !d.Evaluated || d.MeanBps <= 0 ||
		d.MeanBps-1.645*d.StdErrorBps <= 0 {
		t.Fatalf("a wider level filled on the same oscillations should retain its extra edge: %+v", d)
	}
}

func TestPairedOutwardDistanceChargesLostBaseFills(t *testing.T) {
	start := time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC)
	horizon := 5 * time.Minute
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		HorizonLookback: types.Duration(40 * time.Minute),
	}
	model := MarketMakerHorizonModel{}
	for second := 0; second <= 45*60; second++ {
		phase := 2 * math.Pi * float64(second) / horizon.Seconds()
		mid := 100 * math.Exp(0.004*math.Sin(phase))
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	d := model.jointBalancedPathPayoffDifference(
		start.Add(45*time.Minute), config, horizon,
		20, 20, 120, 120, conditionalExecutionState{})
	if !d.Evaluated || d.MeanBps >= 0 {
		t.Fatalf("an outward level that misses profitable base cycles must be worse: %+v", d)
	}
}

func TestSimultaneousDistanceZControlsCandidateSearch(t *testing.T) {
	base := 1.645
	one := simultaneousOneSidedZ(base, 1)
	many := simultaneousOneSidedZ(base, 12)
	if math.Abs(one-base) > 1e-12 || many <= base || many >= 4 {
		t.Fatalf("unexpected one-sided family correction: one=%v many=%v", one, many)
	}
}

func TestInventoryTargetDirectionUsesWindowBBOAverageWithoutLiquidationHaircut(t *testing.T) {
	start := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		HorizonLookback:  types.Duration(5 * time.Minute),
		MaxTradingWindow: types.Duration(time.Minute),
	}
	for second := 0; second <= 5*60; second++ {
		model.ObserveBookWithSizesAndGap(
			start.Add(time.Duration(second)*time.Second),
			99, 1, 101, 1, config, false)
	}
	stats := model.JointPathPayoffStatistics(
		start.Add(5*time.Minute), config, time.Minute, 5, 5)
	if stats.EffectiveSamples <= 1 {
		t.Fatalf("insufficient completed paths: %+v", stats)
	}
	if stats.BuyDominant.InventoryMeanBps >= 0 {
		t.Fatalf("liquidation stress should retain the deliberate half-spread haircut: %+v", stats.BuyDominant)
	}
	if math.Abs(stats.BuyDominant.InventoryDirectionalMeanBps) > 1e-12 {
		t.Fatalf("flat BBO-weighted windows must have neutral target direction: %+v", stats.BuyDominant)
	}
	target := PosteriorInventoryRiskTarget(0.5, 0, 1, stats)
	if !target.Enabled || math.Abs(target.TargetBase-0.5) > 1e-12 || target.UpProbability != 0.5 {
		t.Fatalf("spread alone must not push the inventory target bearish: %+v", target)
	}
}

func TestOneSidedSellTerminalWealthUsesBidAndIgnoresUnexecutedRepurchaseAsk(t *testing.T) {
	start := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	horizon := 10 * time.Minute
	now := start.Add(time.Hour)
	statistics := func(terminalAsk float64) JointPathPayoffStats {
		exposures := make([]marketMakerHorizonExposure, 30)
		for index := range exposures {
			exposures[index] = marketMakerHorizonExposure{
				At:                     start.Add(time.Duration(index) * time.Minute),
				EndAt:                  start.Add(time.Duration(index)*time.Minute + horizon),
				StartBid:               100,
				StartAsk:               102,
				StartBBOWeightedPrice:  101,
				WindowBBOWeightedPrice: 101,
				TerminalBid:            99,
				TerminalAsk:            terminalAsk,
				SellExcursionBps:       20,
				NextMinute:             index + 1,
			}
		}
		model := MarketMakerHorizonModel{
			points: []MarketMakerHorizonPoint{{At: now, Bid: 99, Ask: terminalAsk}},
			crossingExposureCaches: map[time.Duration]*marketMakerHorizonExposureCache{
				horizon: {Initialized: true, LastPointAt: now, Exposures: exposures},
			},
		}
		return model.JointPathPayoffStatistics(now, MarketMakerConfig{
			HorizonLookback: types.Duration(2 * time.Hour),
			MakerFeeBps:     10, AdverseSelectionBps: 2,
		}, horizon, 10, 10)
	}

	tight, wide := statistics(99.01), statistics(150)
	if tight.EffectiveSamples <= 1 || wide.EffectiveSamples <= 1 {
		t.Fatalf("insufficient synthetic SELL paths: tight=%+v wide=%+v", tight, wide)
	}
	if math.Abs(tight.SellDominant.SellMeanBps-wide.SellDominant.SellMeanBps) > 1e-12 {
		t.Fatalf("an unexecuted terminal repurchase ask changed one-sided SELL wealth: tight=%+v wide=%+v", tight, wide)
	}
	quote := 100 * math.Exp(10.0/10_000)
	want := math.Log(quote/99)*10_000 - 12
	if math.Abs(tight.SellDominant.SellMeanBps-want) > 1e-9 {
		t.Fatalf("SELL must compare cash proceeds with terminal-bid liquidation: got=%v want=%v",
			tight.SellDominant.SellMeanBps, want)
	}
}

func TestWholePositionUtilityRewardsRiskReducingSellAndRestrainsBuy(t *testing.T) {
	moments := jointPathPayoffMoments{
		BuyMeanBps:           2,
		SellMeanBps:          -1,
		InventoryMeanBps:     -40,
		BuyVarBps2:           400,
		SellVarBps2:          400,
		InventoryVarBps2:     1_600,
		InventoryBuyCovBps2:  700,
		InventorySellCovBps2: -700,
	}
	stats := JointPathPayoffStats{
		EffectiveSamples: 100,
		BuyDominant:      moments,
		SellDominant:     moments,
	}
	const (
		inventoryJPY = 8_000
		orderJPY     = 1_000
		equityJPY    = 10_000
		riskAversion = 50
	)

	sell := stats.EvaluateWholePosition(
		inventoryJPY, 0, orderJPY, equityJPY, riskAversion, 0)
	if !sell.RiskReducing || sell.MarginalVarianceJPY2 >= 0 || sell.KellyPenaltyJPY >= 0 {
		t.Fatalf("SELL hedge must reduce whole-position variance: %+v", sell)
	}
	if sell.ExpectedPnLJPY >= 0 || sell.CertaintyEquivalent <= 0 {
		t.Fatalf("risk benefit should support a small negative-edge SELL when total wealth improves: %+v", sell)
	}

	buy := stats.EvaluateWholePosition(
		inventoryJPY, orderJPY, 0, equityJPY, riskAversion, 0)
	if buy.RiskReducing || buy.MarginalVarianceJPY2 <= 0 || buy.KellyPenaltyJPY <= 0 {
		t.Fatalf("BUY that compounds long exposure must add whole-position risk: %+v", buy)
	}
	if buy.ExpectedPnLJPY <= 0 || buy.CertaintyEquivalent >= 0 {
		t.Fatalf("local BUY edge must not override larger whole-position risk: %+v", buy)
	}
}

func TestWholePositionUtilityAllowsPositiveReversalBuyAtLowInventory(t *testing.T) {
	moments := jointPathPayoffMoments{
		BuyMeanBps: 40, SellMeanBps: -10,
		BuyVarBps2: 400, SellVarBps2: 400,
		InventoryVarBps2:    900,
		InventoryBuyCovBps2: 50, InventorySellCovBps2: -50,
	}
	stats := JointPathPayoffStats{EffectiveSamples: 100, BuyDominant: moments, SellDominant: moments}
	buy := stats.EvaluateWholePosition(1_000, 1_000, 0, 10_000, 1, 1.282)
	if buy.ExpectedPnLJPY <= 0 || buy.CertaintyEquivalent <= 0 {
		t.Fatalf("strong executable-price reversal evidence should support BUY at low inventory: %+v", buy)
	}
}

func TestWholePositionZeroInventoryMatchesIncrementalModel(t *testing.T) {
	moments := jointPathPayoffMoments{
		BuyMeanBps: 20, SellMeanBps: 15,
		BuyVarBps2: 500, SellVarBps2: 600, CovBps2: -100,
		InventoryMeanBps: 5, InventoryVarBps2: 900,
		InventoryBuyCovBps2: 200, InventorySellCovBps2: -150,
	}
	stats := JointPathPayoffStats{EffectiveSamples: 50, BuyDominant: moments, SellDominant: moments}
	want := stats.Evaluate(400, 300, 10_000, 2, 1.282)
	got := stats.EvaluateWholePosition(0, 400, 300, 10_000, 2, 1.282)
	if got != want {
		t.Fatalf("zero inventory must preserve the incremental model: got=%+v want=%+v", got, want)
	}
}

func TestTargetRelativeRiskRewardsBuyTowardAimAndPenalizesOvershoot(t *testing.T) {
	moments := jointPathPayoffMoments{
		InventoryVarBps2:    100,
		BuyVarBps2:          100,
		InventoryBuyCovBps2: 100,
	}
	stats := JointPathPayoffStats{
		EffectiveSamples: 100,
		BuyDominant:      moments,
		SellDominant:     moments,
	}

	toward := stats.EvaluateTargetRelativePosition(
		400, 600, 100, 0, 1_000, 1, 0)
	if !toward.RiskReducing || toward.MarginalVarianceJPY2 >= 0 ||
		toward.KellyPenaltyJPY >= 0 || toward.RiskInventoryNotionalJPY != -200 {
		t.Fatalf("BUY below target must receive target-relative risk credit: %+v", toward)
	}

	overshoot := stats.EvaluateTargetRelativePosition(
		400, 600, 500, 0, 1_000, 1, 0)
	if overshoot.RiskReducing || overshoot.MarginalVarianceJPY2 <= 0 ||
		overshoot.KellyPenaltyJPY <= 0 {
		t.Fatalf("BUY beyond the reflected target distance must add risk: %+v", overshoot)
	}
}

func TestTargetRelativeRiskIsBuySellSymmetric(t *testing.T) {
	moments := jointPathPayoffMoments{
		InventoryVarBps2:     100,
		BuyVarBps2:           100,
		SellVarBps2:          100,
		InventoryBuyCovBps2:  100,
		InventorySellCovBps2: -100,
	}
	stats := JointPathPayoffStats{
		EffectiveSamples: 100,
		BuyDominant:      moments,
		SellDominant:     moments,
	}
	buy := stats.EvaluateTargetRelativePosition(400, 500, 50, 0, 1_000, 1, 0)
	sell := stats.EvaluateTargetRelativePosition(600, 500, 0, 50, 1_000, 1, 0)
	if !buy.RiskReducing || !sell.RiskReducing ||
		math.Abs(buy.MarginalVarianceJPY2-sell.MarginalVarianceJPY2) > 1e-12 ||
		math.Abs(buy.CertaintyEquivalent-sell.CertaintyEquivalent) > 1e-12 {
		t.Fatalf("reflected target-restoring actions must have equal risk: buy=%+v sell=%+v", buy, sell)
	}
}

func TestVolumeProfilePOCRiskChangesOnlyMatchingSideMoments(t *testing.T) {
	stats := JointPathPayoffStats{
		BuyDominant: jointPathPayoffMoments{
			BuyMeanBps: 10, SellMeanBps: 11, BuyVarBps2: 2, SellVarBps2: 3,
		},
		SellDominant: jointPathPayoffMoments{
			BuyMeanBps: 12, SellMeanBps: 13, BuyVarBps2: 5, SellVarBps2: 7,
		},
	}
	state := VolumeProfileState{
		Valid: true, POCDistanceBps: -8, ProfileScaleBps: 20,
		LocalDensityRatio: 0.5, LocalFlowImbalance: -0.4,
	}
	applyVolumeProfileSideTerminalRisk(&stats, state, 1)
	if stats.BuyDominant.BuyMeanBps >= 10 || stats.SellDominant.BuyMeanBps >= 12 ||
		stats.BuyDominant.SellMeanBps != 11 || stats.SellDominant.SellMeanBps != 13 ||
		stats.BuyDominant.BuyVarBps2 <= 2 || stats.SellDominant.BuyVarBps2 <= 5 ||
		stats.BuyDominant.SellVarBps2 != 3 || stats.SellDominant.SellVarBps2 != 7 {
		t.Fatalf("POC risk must affect only the adverse BUY moments: %+v", stats)
	}
}
