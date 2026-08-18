package gammacapture

import (
	"math"
	"testing"
	"time"
)

func dynamicInventoryAimInput() DynamicInventoryAimInput {
	return DynamicInventoryAimInput{
		CurrentInventoryRatio:   0.20,
		PolicyTargetRatio:       0.50,
		HardMinimumRatio:        0.0,
		HardMaximumRatio:        1.0,
		GrossInventoryReturnBps: 80,
		PredictiveVarianceBps2:  400,
		EffectiveSamples:        64,
		ForecastHorizon:         15 * time.Minute,
		ExecutionHorizon:        15 * time.Minute,
		AdjustmentPeriod:        5 * time.Minute,
		RiskAversion:            1,
		OneWayExecutionCostBps:  12,
		EvidencePriorSamples:    1,
	}
}

func TestDynamicInventoryAimPositiveSignalMovesTowardHigherTarget(t *testing.T) {
	d := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 8, EvidencePriorSamples: 1,
	}, dynamicInventoryAimInput())
	if !d.Enabled || !d.Applied || !d.GatePassed {
		t.Fatalf("positive signal should pass the unified target gate: %+v", d)
	}
	if d.AimTargetRatio <= d.PolicyTargetRatio ||
		d.AdjustedTargetRatio <= d.CurrentInventoryRatio ||
		d.AdjustedTargetRatio >= d.AimTargetRatio {
		t.Fatalf("partial adjustment should move between inventory and aim: %+v", d)
	}
	if d.NetReturnBps <= 0 || d.AdjustmentFraction <= 0 || d.AdjustmentFraction >= 1 {
		t.Fatalf("missing positive target economics: %+v", d)
	}
}

func TestDynamicInventoryAimNegativeSignalIsSymmetric(t *testing.T) {
	in := dynamicInventoryAimInput()
	in.CurrentInventoryRatio = 0.80
	in.GrossInventoryReturnBps = -80
	positive := dynamicInventoryAimInput()
	down := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 8, EvidencePriorSamples: 1,
	}, in)
	up := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 8, EvidencePriorSamples: 1,
	}, positive)
	if !down.GatePassed || down.AimTargetRatio >= down.PolicyTargetRatio ||
		down.AdjustedTargetRatio >= down.CurrentInventoryRatio {
		t.Fatalf("negative signal should reduce the target: %+v", down)
	}
	if math.Abs((up.AimTargetRatio-0.5)-(0.5-down.AimTargetRatio)) > 1e-12 {
		t.Fatalf("target shift must be side symmetric: up=%+v down=%+v", up, down)
	}
}

func TestDynamicInventoryAimFeeGateKeepsPolicyTarget(t *testing.T) {
	in := dynamicInventoryAimInput()
	in.CurrentInventoryRatio = in.PolicyTargetRatio
	in.GrossInventoryReturnBps = 5
	d := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 8, EvidencePriorSamples: 1,
	}, in)
	if d.GatePassed || d.AdjustedTargetRatio != d.PolicyTargetRatio ||
		d.GateReason != "confidence-adjusted fee gate" ||
		d.ConfidenceLowerBps >= in.OneWayExecutionCostBps {
		t.Fatalf("sub-fee alpha must not move target: %+v", d)
	}
}

func TestDynamicInventoryAimInsufficientEvidenceFailsClosed(t *testing.T) {
	in := dynamicInventoryAimInput()
	in.EffectiveSamples = 1
	d := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 8, EvidencePriorSamples: 1,
	}, in)
	if d.GatePassed || d.Applied || d.AdjustedTargetRatio != d.PolicyTargetRatio ||
		d.GateReason != "insufficient samples" {
		t.Fatalf("unmatured evidence must retain the prior target: %+v", d)
	}
	if d.EffectiveSamples != in.EffectiveSamples {
		t.Fatalf("decision must expose the evidence count used by the gate: got %v want %v", d.EffectiveSamples, in.EffectiveSamples)
	}
}

func TestDynamicInventoryAimDoesNotUseFixedConfiguredSampleGate(t *testing.T) {
	in := dynamicInventoryAimInput()
	in.EffectiveSamples = 4
	in.PredictiveVarianceBps2 = 100
	d := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 8, EvidencePriorSamples: 1,
	}, in)
	if !d.GatePassed || d.GateReason == "insufficient samples" {
		t.Fatalf("a fee-positive lower bound should not be blocked by configured N=8: %+v", d)
	}
}

func TestDynamicInventoryAimHighVolatilityFailsPredictiveFeeBound(t *testing.T) {
	in := dynamicInventoryAimInput()
	in.CurrentInventoryRatio = in.PolicyTargetRatio
	in.EffectiveSamples = 4
	in.PredictiveVarianceBps2 = 10_000
	d := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 8, EvidencePriorSamples: 1,
	}, in)
	if d.GatePassed || d.GateReason != "confidence-adjusted fee gate" ||
		d.AdjustedTargetRatio != d.PolicyTargetRatio {
		t.Fatalf("high volatility must fail the predictive fee bound: %+v", d)
	}
}

func TestDynamicInventoryAimRiskGradientDeRisksInventoryBelowFeeReturn(t *testing.T) {
	in := dynamicInventoryAimInput()
	in.CurrentInventoryRatio = 0.80
	in.PolicyTargetRatio = 0.50
	in.GrossInventoryReturnBps = 0
	in.EffectiveSamples = 4
	in.PredictiveVarianceBps2 = 400
	d := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 8, EvidencePriorSamples: 1,
	}, in)
	if !d.GatePassed || d.RiskAdjustedReturnBps >= -in.OneWayExecutionCostBps ||
		d.AdjustedTargetRatio >= in.CurrentInventoryRatio {
		t.Fatalf("inventory risk should support de-risking even with zero directional return: %+v", d)
	}
}

func TestDynamicInventoryAimThirtyMinuteEvidenceCanUseLowerBound(t *testing.T) {
	in := dynamicInventoryAimInput()
	in.EffectiveSamples = 4.5
	in.GrossInventoryReturnBps = 80
	in.PredictiveVarianceBps2 = 100
	d := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, MinimumSamples: 6, EvidencePriorSamples: 1,
	}, in)
	if !d.GatePassed {
		t.Fatalf("30m evidence below legacy six-sample threshold should be admitted when its bound clears cost: %+v", d)
	}
}

func TestDynamicInventoryAimHardBoundsAndShadowMode(t *testing.T) {
	in := dynamicInventoryAimInput()
	in.PolicyTargetRatio = 0.5
	in.CurrentInventoryRatio = 0
	in.HardMinimumRatio = 0.25
	in.HardMaximumRatio = 0.60
	in.GrossInventoryReturnBps = 10_000
	in.PredictiveVarianceBps2 = 0.01
	d := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{
		Enabled: true, ShadowOnly: true, MinimumSamples: 8,
	}, in)
	if !d.GatePassed || d.Applied || d.AdjustedTargetRatio < 0.25 || d.AdjustedTargetRatio > 0.60 ||
		d.AimTargetRatio < 0.25 || d.AimTargetRatio > 0.60 {
		t.Fatalf("hard bounds or shadow mode failed: %+v", d)
	}
}

func TestDynamicInventoryAimFasterAdjustmentMovesMoreInitially(t *testing.T) {
	in := dynamicInventoryAimInput()
	fast := in
	fast.AdjustmentPeriod = 10 * time.Minute
	slow := in
	slow.AdjustmentPeriod = time.Minute
	dFast := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{Enabled: true, MinimumSamples: 8}, fast)
	dSlow := EvaluateDynamicInventoryAim(DynamicInventoryAimConfig{Enabled: true, MinimumSamples: 8}, slow)
	if dFast.AdjustedTargetRatio <= dSlow.AdjustedTargetRatio ||
		dFast.AdjustmentFraction <= dSlow.AdjustmentFraction {
		t.Fatalf("larger adjustment period should move more toward the same aim: fast=%+v slow=%+v", dFast, dSlow)
	}
}
