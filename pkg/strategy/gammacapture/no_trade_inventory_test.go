package gammacapture

import (
	"math"
	"testing"
	"time"
)

func noTradeTestInput() NoTradeInventoryInput {
	return NoTradeInventoryInput{
		CurrentRiskyWeight:                0.5,
		PriorTargetRatio:                  0.5,
		PolicyMinRatio:                    0,
		PolicyMaxRatio:                    1,
		CrossingUp:                        30,
		CrossingDown:                      10,
		CrossingHealth:                    HealthHealthy,
		CrossingQVRatePerSecond:           4e-10,
		ExecutableCrossingUp:              24,
		ExecutableCrossingDown:            12,
		ExecutableCrossingHealth:          HealthHealthy,
		ExecutableObserved:                6 * time.Hour,
		ExecutableCrossingQVRatePerSecond: 3e-10,
		BarrierWidth:                      0.001,
		Observed:                          6 * time.Hour,
		DriftPriorSamples:                 20,
		BuyVolatilityBpsPerSqrtSec:        0.2,
		SellVolatilityBpsPerSqrtSec:       0.2,
		RiskAversion:                      1,
		PriorStrength:                     0.05,
		OneWayCostBps:                     10,
		PairEquityJPY:                     100_000,
		MinimumExecutableNotionalJPY:      0,
	}
}

func TestNoTradeInventorySignedCrossingsMoveOneAim(t *testing.T) {
	cfg := NoTradeInventoryConfig{Enabled: true}
	bullish := EvaluateNoTradeInventory(cfg, noTradeTestInput())
	bearishInput := noTradeTestInput()
	bearishInput.CrossingUp, bearishInput.CrossingDown = bearishInput.CrossingDown, bearishInput.CrossingUp
	bearishInput.ExecutableCrossingUp, bearishInput.ExecutableCrossingDown =
		bearishInput.ExecutableCrossingDown, bearishInput.ExecutableCrossingUp
	bearish := EvaluateNoTradeInventory(cfg, bearishInput)

	if !bullish.Healthy || bullish.AimRatio <= 0.5 || bullish.DriftPerQV <= 0 {
		t.Fatalf("bullish signed crossings must increase the QV-time aim: %+v", bullish)
	}
	if !bearish.Healthy || bearish.AimRatio >= 0.5 || bearish.DriftPerQV >= 0 {
		t.Fatalf("bearish signed crossings must decrease the QV-time aim: %+v", bearish)
	}
	if bullish.AimRatio <= bearish.AimRatio {
		t.Fatalf("signed crossing evidence must order the aims monotonically: bullish=%+v bearish=%+v", bullish, bearish)
	}
}

func TestNoTradeInventoryExecutableDisagreementNeutralizesMicropriceDrift(t *testing.T) {
	in := noTradeTestInput()
	in.ExecutableCrossingUp, in.ExecutableCrossingDown = 8, 28
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	if !d.Healthy || d.MicroSignedDirection <= 0 || d.ExecutableSignedDirection >= 0 ||
		d.SignedDirection != 0 || d.DriftPerQV != 0 || d.AimRatio != in.PriorTargetRatio {
		t.Fatalf("disagreeing executable evidence must neutralize microprice Macro drift: %+v", d)
	}
}

func TestNoTradeInventoryExecutableEvidenceBoundsDirectionAndQV(t *testing.T) {
	in := noTradeTestInput()
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	if math.Abs(d.SignedDirection) > math.Abs(d.MicroSignedDirection)+1e-12 ||
		math.Abs(d.SignedDirection) > math.Abs(d.ExecutableSignedDirection)+1e-12 {
		t.Fatalf("confirmed direction escaped the weaker posterior: %+v", d)
	}
	if math.Abs(d.QVRatePerSecond-in.ExecutableCrossingQVRatePerSecond) > 1e-18 {
		t.Fatalf("confirmed QV must use the conservative common rate: %+v", d)
	}
}

func TestBearishContinuationCapsOnlyNewLongExposure(t *testing.T) {
	in := noTradeTestInput()
	in.CurrentRiskyWeight = 0.3
	in.TrendExcursion = TrendExcursionDecision{
		Enabled: true, Healthy: true, ForecastHorizon: 3 * time.Hour,
		Direction: 1, ExpectedReturn: 0.02, ReturnVariance: 0.0004,
		MeanSE: 0.002, ModelProbability: 0.75,
	}
	in.TrendContinuation = TrendContinuationDecision{
		Healthy: true, RecentDirection: -1, Direction: -1,
		ExpectedReturn: -0.003,
	}
	got := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, TrendExcursionEnabled: true}, in)
	if !got.Healthy || !got.ContinuationCapApplied ||
		got.ContinuationCapRatio != in.CurrentRiskyWeight ||
		got.RawAimRatio > in.CurrentRiskyWeight {
		t.Fatalf("bearish continuation allowed premature new long exposure: %+v", got)
	}
}

func TestNoTradeInventoryDoesNothingInsideAndReflectsToNearestBoundary(t *testing.T) {
	cfg := NoTradeInventoryConfig{Enabled: true}
	centered := EvaluateNoTradeInventory(cfg, noTradeTestInput())
	insideInput := noTradeTestInput()
	insideInput.CurrentRiskyWeight = centered.AimRatio
	inside := EvaluateNoTradeInventory(cfg, insideInput)
	if inside.Direction != 0 || math.Abs(inside.ExecutionTargetRatio-insideInput.CurrentRiskyWeight) > 1e-12 {
		t.Fatalf("inventory inside the free boundaries must add no Macro turnover: %+v", inside)
	}

	belowInput := noTradeTestInput()
	belowInput.CurrentRiskyWeight = math.Max(0, centered.LowerRatio-0.1)
	below := EvaluateNoTradeInventory(cfg, belowInput)
	if below.Direction != 1 || math.Abs(below.ExecutionTargetRatio-below.LowerRatio) > 1e-12 {
		t.Fatalf("inventory below the region must reflect only to its nearest boundary: %+v", below)
	}

	aboveInput := noTradeTestInput()
	aboveInput.CurrentRiskyWeight = math.Min(1, centered.UpperRatio+0.1)
	above := EvaluateNoTradeInventory(cfg, aboveInput)
	if above.Direction != -1 || math.Abs(above.ExecutionTargetRatio-above.UpperRatio) > 1e-12 {
		t.Fatalf("inventory above the region must reflect only to its nearest boundary: %+v", above)
	}
}

func TestNoTradeInventoryCostAndExecutableSideQVSetBoundaries(t *testing.T) {
	cfg := NoTradeInventoryConfig{Enabled: true}
	cheapInput := noTradeTestInput()
	cheapInput.OneWayCostBps = 1
	cheap := EvaluateNoTradeInventory(cfg, cheapInput)
	expensiveInput := cheapInput
	expensiveInput.OneWayCostBps = 10
	expensive := EvaluateNoTradeInventory(cfg, expensiveInput)
	if expensive.BuyHalfWidthRatio <= cheap.BuyHalfWidthRatio ||
		expensive.SellHalfWidthRatio <= cheap.SellHalfWidthRatio {
		t.Fatalf("higher proportional cost must widen the no-trade region: cheap=%+v expensive=%+v", cheap, expensive)
	}

	asymmetricInput := noTradeTestInput()
	asymmetricInput.BuyVolatilityBpsPerSqrtSec = 0.4
	asymmetricInput.SellVolatilityBpsPerSqrtSec = 0.1
	asymmetric := EvaluateNoTradeInventory(cfg, asymmetricInput)
	if asymmetric.BuyHalfWidthRatio <= asymmetric.SellHalfWidthRatio {
		t.Fatalf("larger executable-ask QV must widen the BUY-side boundary: %+v", asymmetric)
	}
}

func TestNoTradeInventoryUnhealthyCrossingsCannotMoveStrategicPrior(t *testing.T) {
	in := noTradeTestInput()
	in.CrossingHealth = HealthDegraded
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	if d.Healthy || d.AimRatio != in.PriorTargetRatio || d.DriftPerQV != 0 || d.ForecastReturn != 0 {
		t.Fatalf("unhealthy signed evidence moved the strategic center: %+v", d)
	}
}

func TestMacroNoTradeSkipsHorizonTargetWeightsButKeepsRiskIntersection(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	model := macroModelFromReturns(start, []float64{-0.04, -0.02, 0.01, -0.03, 0.02, -0.01, -0.05, 0.01})
	cfg := macroTestConfig()
	cfg.NoTradeRegion.Enabled = true
	d := cfg.Decide(&model, MacroInventoryInput{
		Now: start.Add(8 * 24 * time.Hour), WealthJPY: 10_000, WealthPeakJPY: 10_000,
		RiskyNotionalJPY: 5_000, PriorTargetRatio: 0.5, PolicyMinRatio: 0, PolicyMaxRatio: 1,
		FallbackVolatilityBpsPerSqrtSec: 0.2,
		CrossingSnapshot:                ModelSnapshot{Health: HealthHealthy, Up: 30, Down: 10, Observed: 6 * time.Hour, GammaCaptureVolatility: 0.00002},
		ExecutableCrossingSnapshot:      ModelSnapshot{Health: HealthHealthy, Up: 24, Down: 12, Observed: 6 * time.Hour, GammaCaptureVolatility: 0.000017},
		BarrierWidth:                    0.001, BuyVolatilityBpsPerSqrtSec: 0.2, SellVolatilityBpsPerSqrtSec: 0.2,
		OneWayCostBps: 10, MinimumExecutableNotionalJPY: 100,
	})
	if !d.NoTrade.Enabled || d.UtilityHorizons != 0 || d.UtilityWeightSum != 0 {
		t.Fatalf("no-trade mode retained legacy horizon target weights: %+v", d)
	}
	if d.UtilityTargetRatio != d.NoTrade.AimRatio || d.TargetRatio != d.NoTrade.ExecutionTargetRatio {
		t.Fatalf("Macro compatibility targets do not expose the single controller output: %+v", d)
	}
	if d.CapitalFloorRatio <= 0 || d.CapitalCapRatio >= 1 {
		t.Fatalf("rolling horizons must remain as strict risk-bound intersections: %+v", d)
	}
}

func TestNoTradeHoldProtectionBlocksUncertainMovement(t *testing.T) {
	in := noTradeTestInput()
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true, HoldProtectionEnabled: true, HoldProtectionZScore: 1.645}, in)
	if !d.Healthy || !d.HoldProtectionApplied || d.AimRatio != in.CurrentRiskyWeight || d.ExecutionTargetRatio != in.CurrentRiskyWeight {
		t.Fatalf("uncertain Macro movement must remain hold-equivalent: %+v", d)
	}
	if d.ForecastEdgeLowerBps >= 2*in.OneWayCostBps {
		t.Fatalf("test posterior should not clear the round-trip cost: %+v", d)
	}
}

func TestNoTradeHoldProtectionAllowsCostPositiveMovement(t *testing.T) {
	in := noTradeTestInput()
	in.CrossingUp, in.CrossingDown = 1000, 1
	in.ExecutableCrossingUp, in.ExecutableCrossingDown = 1000, 1
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true, HoldProtectionEnabled: true, HoldProtectionZScore: 1.645}, in)
	if !d.Healthy || d.HoldProtectionApplied || d.AimRatio <= in.CurrentRiskyWeight || d.ForecastEdgeLowerBps < 2*in.OneWayCostBps {
		t.Fatalf("strong cost-positive posterior should permit inventory increase: %+v", d)
	}
}

func TestNoTradeHoldProtectionUsesBearishLowerBoundForReduction(t *testing.T) {
	in := noTradeTestInput()
	in.CurrentRiskyWeight = 0.8
	in.PriorTargetRatio = 0.5
	in.CrossingUp, in.CrossingDown = 1, 1000
	in.ExecutableCrossingUp, in.ExecutableCrossingDown = 1, 1000
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true, HoldProtectionEnabled: true, HoldProtectionZScore: 1.645}, in)
	if !d.Healthy || d.HoldProtectionApplied || d.AimRatio >= in.CurrentRiskyWeight || d.ForecastReturn >= 0 || d.ForecastEdgeLowerBps < 2*in.OneWayCostBps {
		t.Fatalf("strong bearish posterior should permit inventory reduction: %+v", d)
	}
}

func TestDownsideRiskControlAllowsCostAwareReductionWithoutAnchoringAim(t *testing.T) {
	in := noTradeTestInput()
	in.CurrentRiskyWeight = 0.8
	in.CrossingUp, in.CrossingDown = 18, 22
	in.ExecutableCrossingUp, in.ExecutableCrossingDown = 18, 22
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{
		Enabled: true, DownsideRiskControlEnabled: true,
		HoldProtectionEnabled: true, HoldProtectionZScore: 1.645,
	}, in)
	if !d.Healthy || d.HoldProtectionApplied || d.AimRatio >= in.CurrentRiskyWeight ||
		d.ExecutionTargetRatio >= in.CurrentRiskyWeight || d.Direction != -1 {
		t.Fatalf("downside controller did not retain an independent risk-reducing target: %+v", d)
	}
}

func TestDownsideRiskControlProtectsIncreaseWithoutChangingLatentAim(t *testing.T) {
	in := noTradeTestInput()
	in.CurrentRiskyWeight = 0.2
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{
		Enabled: true, DownsideRiskControlEnabled: true,
		HoldProtectionEnabled: true, HoldProtectionZScore: 1.645,
	}, in)
	if !d.HoldProtectionApplied || d.AimRatio <= in.CurrentRiskyWeight ||
		d.ExecutionTargetRatio != in.CurrentRiskyWeight || d.Direction != 0 ||
		d.LowerRatio > in.CurrentRiskyWeight || d.UpperRatio < in.CurrentRiskyWeight {
		t.Fatalf("protected increase changed the latent aim or leaked into execution: %+v", d)
	}
}

func TestDownsideRiskControlRejectsUneconomicReduction(t *testing.T) {
	in := noTradeTestInput()
	in.CurrentRiskyWeight = 0.52
	in.CrossingUp, in.CrossingDown = 20, 20
	in.ExecutableCrossingUp, in.ExecutableCrossingDown = 20, 20
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{
		Enabled: true, DownsideRiskControlEnabled: true,
		HoldProtectionEnabled: true, HoldProtectionZScore: 1.645,
	}, in)
	if !d.HoldProtectionApplied || d.RiskReductionNetUtilityBps >= 0 ||
		d.ExecutionTargetRatio != in.CurrentRiskyWeight || d.Direction != 0 {
		t.Fatalf("uneconomic risk reduction escaped utility protection: %+v", d)
	}
}

func TestDownsideRiskControlUsesVarianceAtNeutralDirection(t *testing.T) {
	in := noTradeTestInput()
	in.CrossingUp, in.CrossingDown = 20, 20
	in.ExecutableCrossingUp, in.ExecutableCrossingDown = 20, 20
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{
		Enabled: true, DownsideRiskControlEnabled: true,
	}, in)
	if !d.Healthy || d.SignedDirection != 0 || d.AimRatio >= in.PriorTargetRatio {
		t.Fatalf("neutral return variance did not reduce long-only Merton exposure: %+v", d)
	}
}

func TestFastReservationIsSignedAndPathEfficient(t *testing.T) {
	d := NoTradeInventoryDecision{
		Enabled: true, Healthy: true,
		ForecastObservation: 30 * time.Minute,
		ForecastReturn:      -0.004,
		ForecastReturnSE:    0.001,
	}
	bearish := d.FastReservation(30*time.Minute, 1.645)
	wantAdverse := 0.5 * math.Erfc(-4/math.Sqrt2)
	if !bearish.Enabled || bearish.Direction != -1 ||
		math.Abs(bearish.AdverseProbability-wantAdverse) > 1e-12 ||
		math.Abs(bearish.ReservationShiftBps+40) > 1e-12 {
		t.Fatalf("unexpected bearish reservation: %+v", bearish)
	}

	bullishInput := d
	bullishInput.ForecastReturn = -d.ForecastReturn
	bullish := bullishInput.FastReservation(30*time.Minute, 1.645)
	if !bullish.Enabled || bullish.Direction != 1 ||
		math.Abs(bullish.ReservationShiftBps-40) > 1e-12 ||
		math.Abs(bullish.DirectionalProbability-bearish.DirectionalProbability) > 1e-12 {
		t.Fatalf("signed posterior is not symmetric: bearish=%+v bullish=%+v", bearish, bullish)
	}

	weak := d
	weak.ForecastReturn = -0.0005
	weak.TrendContinuation = TrendContinuationDecision{Healthy: true, ConsolidationScore: 0.9}
	weakResult := weak.FastReservation(30*time.Minute, 1.645)
	if !weakResult.Enabled || math.Abs(weakResult.ReservationShiftBps+0.5) > 1e-12 {
		t.Fatalf("consolidation efficiency must shrink rather than gate Fast: %+v", weakResult)
	}

	neutral := d
	neutral.ForecastReturn = 0
	reservation := neutral.FastReservation(30*time.Minute, 1.645)
	if reservation.Enabled || reservation.Strength != 0 {
		t.Fatalf("neutral posterior must leave Fast unchanged: %+v", reservation)
	}
}

func TestFastReservationRiskHorizonDoesNotAssumePublicCrossingsAreFills(t *testing.T) {
	if got := FastReservationRiskHorizon(10*time.Minute, 30*time.Minute); got != 30*time.Minute {
		t.Fatalf("return posterior horizon must own reservation risk: %s", got)
	}
	if got := FastReservationRiskHorizon(10*time.Minute, 0); got != 10*time.Minute {
		t.Fatalf("missing return horizon must fall back to Fast: %s", got)
	}
}

func TestApplyFastReservationShiftIsSymmetricAndPreservesFastDesign(t *testing.T) {
	plan := MarketMakerQuotePlan{
		BidPrice: 99.8, AskPrice: 100.2, BidDistanceBps: 20,
		AskDistanceBps: 20, BidHalfSpreadBps: 20, AskHalfSpreadBps: 20,
		HalfSpreadBps: 20, BidQuoteNotional: 100, AskQuoteNotional: 120,
		AllowBid: true, AllowAsk: true,
	}
	plan.BidTouchDistanceBps, plan.AskTouchDistanceBps, _ =
		MakerTouchDistances(99.95, 100.05, plan.BidPrice, plan.AskPrice)

	bearish := ApplyFastReservationShift(plan,
		FastReservationDecision{Enabled: true, ReservationShiftBps: -10},
		100, 99.95, 100.05)
	down := math.Exp(-10.0 / 10_000)
	if math.Abs(bearish.BidPrice-plan.BidPrice*down) > 1e-12 ||
		math.Abs(bearish.AskPrice-math.Max(100.05, plan.AskPrice*down)) > 1e-12 ||
		bearish.BidQuoteNotional != plan.BidQuoteNotional ||
		bearish.AskQuoteNotional != plan.AskQuoteNotional ||
		!bearish.AllowBid || !bearish.AllowAsk {
		t.Fatalf("bearish shift broke Fast invariants: %+v", bearish)
	}

	bullish := ApplyFastReservationShift(plan,
		FastReservationDecision{Enabled: true, ReservationShiftBps: 10},
		100, 99.95, 100.05)
	up := math.Exp(10.0 / 10_000)
	if math.Abs(bullish.BidPrice-math.Min(99.95, plan.BidPrice*up)) > 1e-12 ||
		math.Abs(bullish.AskPrice-plan.AskPrice*up) > 1e-12 ||
		bullish.BidQuoteNotional != plan.BidQuoteNotional ||
		bullish.AskQuoteNotional != plan.AskQuoteNotional ||
		!bullish.AllowBid || !bullish.AllowAsk {
		t.Fatalf("bullish shift broke Fast invariants: %+v", bullish)
	}
}

func TestApplyFastReservationShiftProjectsTargetSideAtMakerTouch(t *testing.T) {
	plan := MarketMakerQuotePlan{BidPrice: 99.8, AskPrice: 100.2, BidDistanceBps: 20, AllowBid: true, AllowAsk: true}
	bearish := ApplyFastReservationShift(plan,
		FastReservationDecision{Enabled: true, ReservationShiftBps: -100},
		100, 99.95, 100.05)
	if math.Abs(bearish.AskPrice-100.05) > 1e-12 ||
		math.Abs(bearish.BidPrice-plan.BidPrice*math.Exp(-100.0/10_000)) > 1e-12 {
		t.Fatalf("bearish maker projection is wrong: %+v", bearish)
	}
	bullish := ApplyFastReservationShift(plan,
		FastReservationDecision{Enabled: true, ReservationShiftBps: 100},
		100, 99.95, 100.05)
	if math.Abs(bullish.BidPrice-99.95) > 1e-12 ||
		math.Abs(bullish.AskPrice-plan.AskPrice*math.Exp(100.0/10_000)) > 1e-12 {
		t.Fatalf("bullish maker projection is wrong: %+v", bullish)
	}
}

func TestSelectFastReservationPlanFallsBackExactlyWithoutUtility(t *testing.T) {
	base := MarketMakerQuotePlan{
		BidPrice: 99.8, AskPrice: 100.2, BidQuoteNotional: 100,
		AskQuoteNotional: 100, AllowBid: true, AllowAsk: true,
	}
	got, utility := SelectFastReservationPlan(nil, MarketMakerConfig{}, time.Now(),
		30*time.Minute, base,
		FastReservationDecision{Enabled: true, ReservationShiftBps: -10},
		100, 99.95, 100.05, 100, 1_000, 1)
	if utility.Applied || got != base {
		t.Fatalf("unsupported candidate must return exact Fast plan: plan=%+v utility=%+v", got, utility)
	}
}

func TestFastReservationRealignmentUsesSignedGrowth(t *testing.T) {
	if !FastReservationRealignmentRequired(-10, 5, 1) {
		t.Fatal("sign change must trigger reservation realignment")
	}
	if !FastReservationRealignmentRequired(10, 5, 1) {
		t.Fatal("same-sign growth must trigger reservation realignment")
	}
	if FastReservationRealignmentRequired(4, 5, 1) {
		t.Fatal("shrinking signal must retain queue priority")
	}
}

func TestFastReservationDoesNotDuplicateActiveBoundaryCorrection(t *testing.T) {
	d := NoTradeInventoryDecision{
		Enabled: true, Healthy: true, Direction: -1,
		ForecastObservation: 30 * time.Minute,
		ForecastReturn:      -0.01, ForecastReturnSE: 0.001,
	}
	got := d.FastReservation(30*time.Minute, 1.282)
	if got.Enabled || got.Strength != 0 {
		t.Fatalf("active boundary correction must not duplicate signed reservation drift: %+v", got)
	}
}

func TestFastReservationNeedsUncertaintyEstimate(t *testing.T) {
	d := NoTradeInventoryDecision{
		Enabled: true, Healthy: true,
		ForecastObservation: 30 * time.Minute,
		ForecastReturn:      -0.01,
	}
	got := d.FastReservation(30*time.Minute, 1.645)
	if got.Enabled || got.Strength != 0 {
		t.Fatalf("missing posterior uncertainty must not become a hard BUY gate: %+v", got)
	}
}

func TestSignedReservationUsesFastInventoryTermAfterFill(t *testing.T) {
	config := MarketMakerConfig{
		MakerFeeBps: 0, AdverseSelectionBps: 0, MinimumNetEdgeBps: 0,
		MinimumHalfSpreadBps: 5, MaximumHalfSpreadBps: 100,
		InventoryTarget: 0.5, InventoryLimit: 0.5,
	}
	quote := func(inventory float64) MarketMakerQuotePlan {
		return config.Quote(MarketMakerQuoteInput{
			MidPrice: 100, BestBid: 99.9, BestAsk: 100.1,
			VolatilityPerSqrtSec: 1, TradingHorizonSeconds: 100,
			Inventory: inventory, InventoryMin: 0, InventoryMax: 1,
			HardInventoryMin: 0, HardInventoryMax: 1,
			QuoteNotionalBase: 100, CanBuy: true, CanSell: true,
		})
	}
	decision := FastReservationDecision{Enabled: true, ReservationShiftBps: -5}
	beforeSell := ApplyFastReservationShift(quote(0.6), decision, 100, 99.9, 100.1)
	afterSell := ApplyFastReservationShift(quote(0.4), decision, 100, 99.9, 100.1)
	if afterSell.BidPrice <= beforeSell.BidPrice || afterSell.AskPrice <= beforeSell.AskPrice {
		t.Fatalf("updated below-target inventory must raise the unified reservation after a sell: before=%+v after=%+v", beforeSell, afterSell)
	}
	if afterSell.BidQuoteNotional != beforeSell.BidQuoteNotional ||
		afterSell.AskQuoteNotional != beforeSell.AskQuoteNotional {
		t.Fatalf("reservation integration must not bypass Fast quantity ownership: before=%+v after=%+v", beforeSell, afterSell)
	}
}
