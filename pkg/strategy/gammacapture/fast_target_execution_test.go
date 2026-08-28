package gammacapture

import (
	"math"
	"testing"
	"time"
)

func fastTargetExecutionInput(direction int) FastTargetExecutionInput {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	return FastTargetExecutionInput{
		Now: now, ModelUpdatedAt: now.Add(-time.Second), Horizon: 15 * time.Minute,
		Direction: direction, CurrentInventoryBase: 1, TargetInventoryBase: 1.5,
		AvailableBase: 1, AvailableQuote: 10_000,
		BestBid: 99.9, BestBidSize: 0.2, BestAsk: 100.1, BestAskSize: 0.3,
		PassiveQuotePrice: 99.9, PassiveAvailable: true,
		TouchProbability: 0.1, TouchStdError: 0.02,
		InventoryReturnMeanBps: 80, InventoryPredictiveSDBps: 40,
		DirectionConfidence: 0.8,
		ConfidenceZScore:    1.96, MakerFeeBps: 10, TakerFeeBps: 10,
		MinimumQuantityBase: 0.001, MinimumNotionalJPY: 1,
		PairEquityJPY: 10_000, RiskAversion: 1,
	}
}

func TestFastTargetExecutionWeightsMakerBenefitByTouchProbability(t *testing.T) {
	in := fastTargetExecutionInput(1)
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	wantPremium := d.PassiveTouchProbabilityUpper * d.PassiveToTouchCostBps
	if math.Abs(d.ProbabilityWeightedPassiveCostBps-wantPremium) > 1e-12 {
		t.Fatalf("passive price benefit must exist only on fill branch: %+v", d)
	}
	wantFee := in.TakerFeeBps - d.PassiveTouchProbabilityUpper*in.MakerFeeBps
	if math.Abs(d.ExpectedExecutionFeeBps-wantFee) > 1e-12 {
		t.Fatalf("maker fee must exist only on fill branch: %+v", d)
	}
}

func TestFastTargetExecutionRejectedMakerComparesIOCWithHold(t *testing.T) {
	in := fastTargetExecutionInput(-1)
	in.TargetInventoryBase = .4
	in.InventoryReturnMeanBps = -80
	in.DirectionConfidence = -.8
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	in.PersistentDownsideActive = true
	in.PersistentDownsideEValue = 25
	in.PersistentDownsideForecastBps = 80
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.Reason != "Fast portfolio certainty equivalent favors IOC over hold after maker rejection" {
		t.Fatalf("rejected maker must fall back to IOC-versus-hold value: %+v", d)
	}
	if d.PassiveTouchProbabilityUpper != 0 || d.ExecutionCostBps != in.TakerFeeBps {
		t.Fatalf("hold counterfactual must not invent a maker fill or fee: %+v", d)
	}
	if d.UrgentFraction != 1 || math.Abs(d.Quantity-in.BestBidSize) > 1e-12 ||
		math.Abs(d.ResidualMakerGapBase-(d.TargetGapBase-d.Quantity)) > 1e-12 {
		t.Fatalf("cost-optimal rejected-maker target must not be shrunk twice: %+v", d)
	}
}

func TestFastTargetExecutionRejectedSellUsesFastPosteriorWithoutDuplicateGate(t *testing.T) {
	in := fastTargetExecutionInput(-1)
	in.TargetInventoryBase = .4
	in.InventoryReturnMeanBps = -80
	in.DirectionConfidence = -.8
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.ActiveCertaintyEquivalentBps <= 0 {
		t.Fatalf("risk-reducing Fast SELL must not require a second slow evidence gate: %+v", d)
	}
}

func TestFastTargetExecutionRejectedBuyUsesFastPosteriorWithoutDuplicateGate(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.ActiveCertaintyEquivalentBps <= 0 {
		t.Fatalf("risk-reducing Fast BUY must not require a second slow evidence gate: %+v", d)
	}
}

func TestFastTargetExecutionRejectedBuyUsesUpsideAndPositivePortfolioCE(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	in.PersistentUpsideActive = true
	in.PersistentUpsideEValue = 25
	in.PersistentUpsideForecastBps = 80
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.ActiveCertaintyEquivalentBps <= 0 || d.Reason != "Fast portfolio certainty equivalent favors IOC over hold after maker rejection" {
		t.Fatalf("risk-positive executable-ask upside should permit BUY fallback: %+v", d)
	}
}

func TestFastTargetExecutionRejectedBuyUsesTargetRelativeRiskAndBlocksNegativeCE(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	in.PersistentUpsideActive = true
	in.PersistentUpsideForecastBps = 80
	in.PersistentDownsideActive = true
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.ActiveCertaintyEquivalentBps <= 0 {
		t.Fatalf("a duplicate slow gate must not veto the selected Fast target: %+v", d)
	}
	in.PersistentDownsideActive = false
	in.InventoryReturnMeanBps = 5
	d = EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "Fast active certainty equivalent is nonpositive" {
		t.Fatalf("BUY must pay fee and portfolio variance in terminal CE: %+v", d)
	}
}

func TestFastTargetExecutionPassivePathRequiresPositiveCE(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.InventoryReturnMeanBps = 20
	in.InventoryReturnSEBps = 20
	in.InventoryPredictiveSDBps = 0
	in.TakerFeeBps = 10
	in.MakerFeeBps = 0
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "Fast active certainty equivalent is nonpositive" {
		t.Fatalf("passive path must not bypass terminal CE: %+v", d)
	}
}

func TestFastTargetExecutionUsesSignedRiskBenefit(t *testing.T) {
	in := fastTargetExecutionInput(-1)
	in.TargetInventoryBase = 0.4
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	in.InventoryReturnMeanBps = 0
	in.InventoryPredictiveSDBps = 400
	in.RiskAversion = 5
	in.PairEquityJPY = 200
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.InventoryVariancePenaltyBps >= 0 {
		t.Fatalf("risk-reducing IOC should receive a signed variance benefit: %+v", d)
	}
}

func TestFastTargetExecutionUsesPersistentBidDownsideOnlyForSell(t *testing.T) {
	sell := fastTargetExecutionInput(-1)
	sell.TargetInventoryBase = .4
	sell.PassiveQuotePrice = 100.1
	sell.InventoryReturnMeanBps = -1
	sell.InventoryPredictiveSDBps = 0
	sell.DirectionConfidence = -.2
	sell.PersistentDownsideActive = true
	sell.PersistentDownsideForecastBps = 80
	dSell := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, sell)
	if dSell.ExpectedAdverseMoveBps != 80 {
		t.Fatalf("SELL should consume persistent executable-bid forecast: %+v", dSell)
	}
	buy := fastTargetExecutionInput(1)
	buy.InventoryReturnMeanBps = 1
	buy.InventoryPredictiveSDBps = 0
	buy.PersistentDownsideActive = true
	buy.PersistentDownsideForecastBps = 80
	dBuy := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, buy)
	if dBuy.ExpectedAdverseMoveBps != 1 {
		t.Fatalf("downside evidence must not leak into BUY value: %+v", dBuy)
	}
}

func TestFastTargetExecutionBuyUsesEmpiricalMissProbabilityAndDepthCap(t *testing.T) {
	in := fastTargetExecutionInput(1)
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.Direction != 1 {
		t.Fatalf("expected BUY trigger, got %+v", d)
	}
	wantUpper := in.TouchProbability + in.ConfidenceZScore*in.TouchStdError
	if math.Abs(d.PassiveTouchProbabilityUpper-wantUpper) > 1e-12 {
		t.Fatalf("touch UCB got %.12f want %.12f", d.PassiveTouchProbabilityUpper, wantUpper)
	}
	if d.Quantity <= 0 || d.Quantity > in.BestAskSize || d.WorstPrice < in.BestAsk {
		t.Fatalf("BUY must be positive, ask-depth capped, and marketable: %+v", d)
	}
	if d.ResidualMakerGapBase <= 0 {
		t.Fatalf("expected residual target gap to remain maker, got %+v", d)
	}
}

func TestFastTargetExecutionSellIsSymmetric(t *testing.T) {
	in := fastTargetExecutionInput(-1)
	in.TargetInventoryBase = 0.4
	in.PassiveQuotePrice = 100.1
	in.InventoryReturnMeanBps = -80
	in.DirectionConfidence = -0.8
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.Direction != -1 {
		t.Fatalf("expected SELL trigger, got %+v", d)
	}
	if d.Quantity <= 0 || d.Quantity > in.BestBidSize || d.WorstPrice > in.BestBid {
		t.Fatalf("SELL must be positive, bid-depth capped, and marketable: %+v", d)
	}
}

func TestFastTargetExecutionWaitsWhenMakerCanReachTargetCheaply(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.TouchProbability = 0.95
	in.TouchStdError = 0.01
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "Fast passive wait loss does not exceed crossing cost" {
		t.Fatalf("expected maker wait, got %+v", d)
	}
}

func TestFastTargetExecutionDoesNotDuplicateTargetDirectionGate(t *testing.T) {
	in := fastTargetExecutionInput(-1)
	in.CurrentInventoryBase = 2
	in.TargetInventoryBase = 0.5
	in.AvailableBase = 2
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	// A mildly positive return forecast has already raised the dynamic target.
	// It must not veto a SELL whose target-relative portfolio CE is positive.
	in.InventoryReturnMeanBps = 2
	in.DirectionConfidence = 0.1
	in.InventoryPredictiveSDBps = 400
	in.PairEquityJPY = 200
	in.RiskAversion = 5
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger || d.ActiveCertaintyEquivalentBps <= 0 || d.InventoryVariancePenaltyBps >= 0 {
		t.Fatalf("positive target-relative SELL CE must own the final decision: %+v", d)
	}
}

func TestFastTargetExecutionUnalignedTargetStillRejectsNegativePortfolioCE(t *testing.T) {
	in := fastTargetExecutionInput(-1)
	in.CurrentInventoryBase = 2
	in.TargetInventoryBase = 0.5
	in.AvailableBase = 2
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	in.InventoryReturnMeanBps = 20
	in.DirectionConfidence = 0.2
	in.InventoryPredictiveSDBps = 20
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "Fast active certainty equivalent is nonpositive" {
		t.Fatalf("removing the duplicate sign gate must not authorize negative CE: %+v", d)
	}
}

func TestFastTargetExecutionAtMostOncePerModelUpdate(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.LastExecutionModelAt = in.ModelUpdatedAt
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "prior Fast IOC reference horizon is unresolved" ||
		d.ReferenceHorizonReady || d.DecisionEvaluated || d.TargetGapBase != 0.5 {
		t.Fatalf("expected duplicate model update rejection, got %+v", d)
	}
}

func TestFastTargetExecutionWaitsForPriorReferenceHorizon(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.LastExecutionModelAt = in.ModelUpdatedAt.Add(-10 * time.Minute)
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "prior Fast IOC reference horizon is unresolved" ||
		d.ReferenceHorizonReady || d.DecisionEvaluated ||
		d.ReferenceMaturityAt != in.LastExecutionModelAt.Add(in.Horizon) {
		t.Fatalf("overlapping posterior windows must not create repeated IOC evidence: %+v", d)
	}
	in.LastExecutionModelAt = in.ModelUpdatedAt.Add(-in.Horizon)
	d = EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger {
		t.Fatalf("a matured reference horizon may execute a new decision: %+v", d)
	}
}

func TestFastTargetExecutionSameSideRequiresDisjointEvidenceHorizon(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.LastExecutionDirection = 1
	in.LastExecutionModelAt = in.ModelUpdatedAt.Add(-in.Horizon)
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "prior Fast IOC reference horizon is unresolved" {
		t.Fatalf("same-side re-entry must wait for a fresh disjoint horizon: %+v", d)
	}
	in.LastExecutionModelAt = in.ModelUpdatedAt.Add(-2 * in.Horizon)
	d = EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger {
		t.Fatalf("same-side re-entry may use a fully disjoint matured horizon: %+v", d)
	}

	// A reversal is risk-reducing information, not another bet on the same
	// overlapping signal, and remains eligible after the original H matures.
	in.Direction = -1
	in.TargetInventoryBase = 0.4
	in.PassiveQuotePrice = 100.1
	in.InventoryReturnMeanBps = -80
	in.DirectionConfidence = -0.8
	in.LastExecutionModelAt = in.ModelUpdatedAt.Add(-in.Horizon)
	d = EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !d.Trigger {
		t.Fatalf("opposite-side risk exit must retain one-horizon maturity: %+v", d)
	}
}

func TestFastTargetExecutionHonorsExchangeMinimum(t *testing.T) {
	in := fastTargetExecutionInput(1)
	in.MinimumQuantityBase = 1
	d := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "Fast active quantity is below exchange minimum" {
		t.Fatalf("expected exchange minimum rejection, got %+v", d)
	}
}
