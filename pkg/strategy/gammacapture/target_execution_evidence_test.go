package gammacapture

import (
	"math"
	"testing"
)

func TestFastTargetExecutionEvidenceUsesCausalTargetOwner(t *testing.T) {
	causal := EvaluateCausalRegimeInventoryTarget(
		CausalRegimeInventoryTargetConfig{RiskAversion: 0.01, PriorStrengthBps: 1},
		causalRegimeTargetTestInput(-40, 25, 0.5),
	)
	evidence := BuildFastTargetExecutionEvidence(
		true, causal,
		DynamicInventoryAimDecision{Applied: true, GatePassed: true, ExecutionReturnBps: 90},
		PosteriorInventoryTargetDecision{Enabled: true, InventoryReturnMean: 90, EffectiveSamples: 20},
	)
	if !evidence.Ready || evidence.Source != "causal-pivot-ce" {
		t.Fatalf("causal target owner was not selected: %+v", evidence)
	}
	if evidence.InventoryReturnMeanBps >= 0 || evidence.DirectionConfidence >= 0 {
		t.Fatalf("causal signed forecast was not preserved: %+v", evidence)
	}
}

func TestCausalTargetEvidenceCanDriveRebalancingIOC(t *testing.T) {
	causal := EvaluateCausalRegimeInventoryTarget(
		CausalRegimeInventoryTargetConfig{RiskAversion: 0.01, PriorStrengthBps: 1},
		causalRegimeTargetTestInput(-40, 25, 0.5),
	)
	evidence := BuildFastTargetExecutionEvidence(true, causal, DynamicInventoryAimDecision{}, PosteriorInventoryTargetDecision{})
	in := fastTargetExecutionInput(-1)
	in.CurrentInventoryBase = 1
	in.TargetInventoryBase = 0.4
	in.PassiveAvailable = false
	in.PassiveQuotePrice = 0
	in.TouchProbability = 0
	in.TouchStdError = 0
	in.InventoryReturnMeanBps = evidence.InventoryReturnMeanBps
	in.InventoryReturnSEBps = evidence.InventoryReturnSEBps
	in.InventoryPredictiveSDBps = evidence.InventoryPredictiveSDBps
	in.DirectionConfidence = evidence.DirectionConfidence
	decision := EvaluateFastTargetExecution(FastTargetExecutionConfig{Enabled: true}, in)
	if !evidence.Ready || !decision.Trigger || decision.Direction != -1 {
		t.Fatalf("causal target evidence did not produce a valid rebalancing IOC decision: evidence=%+v decision=%+v", evidence, decision)
	}
}

func TestFastTargetExecutionEvidenceUsesDynamicAimWithoutPosterior(t *testing.T) {
	evidence := BuildFastTargetExecutionEvidence(
		false, CausalRegimeInventoryTargetDecision{},
		DynamicInventoryAimDecision{
			Applied: true, GatePassed: true, Reason: "dynamic target ready",
			ExecutionReturnBps: -40, PredictiveStdDevBps: 12,
			EffectiveSamples: 8, SignalStrength: -0.7,
		},
		PosteriorInventoryTargetDecision{},
	)
	if !evidence.Ready || evidence.Source != "dynamic-inventory-aim" {
		t.Fatalf("dynamic target evidence was not selected: %+v", evidence)
	}
	wantSE := 12 / math.Sqrt(9)
	if math.Abs(evidence.InventoryReturnSEBps-wantSE) > 1e-12 {
		t.Fatalf("dynamic predictive uncertainty was not converted to mean SE: got=%v want=%v", evidence.InventoryReturnSEBps, wantSE)
	}
}

func TestFastTargetExecutionEvidenceFallsBackToPosterior(t *testing.T) {
	evidence := BuildFastTargetExecutionEvidence(
		false, CausalRegimeInventoryTargetDecision{},
		DynamicInventoryAimDecision{},
		PosteriorInventoryTargetDecision{
			Enabled: true, Reason: "posterior target ready", InventoryReturnMean: 30,
			InventoryReturnSE: 4, InventoryPredictiveSD: 10,
			DirectionConfidence: 0.8, EffectiveSamples: 16,
		},
	)
	if !evidence.Ready || evidence.Source != "posterior-inventory-target" || evidence.InventoryReturnMeanBps != 30 {
		t.Fatalf("posterior target fallback was not selected: %+v", evidence)
	}
}

func TestFastTargetExecutionEvidenceDoesNotTurnLegacyPivotIntoIOCForecast(t *testing.T) {
	evidence := BuildFastTargetExecutionEvidence(
		false, CausalRegimeInventoryTargetDecision{},
		DynamicInventoryAimDecision{Applied: true, GatePassed: true, PivotRegimeEnabled: true},
		PosteriorInventoryTargetDecision{},
	)
	if evidence.Ready || evidence.Source != "dynamic-pivot-target" {
		t.Fatalf("legacy pivot target must remain target-only for IOC: %+v", evidence)
	}
}
