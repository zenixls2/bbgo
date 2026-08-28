package gammacapture

import (
	"math"
	"testing"
)

func causalRegimeTargetTestInput(mu, variance, current float64) CausalRegimeInventoryTargetInput {
	return CausalRegimeInventoryTargetInput{
		CurrentWeight:           current,
		PriorWeight:             0.5,
		HardMinimum:             0,
		HardMaximum:             1,
		SignedExpectedReturnBps: mu,
		PredictiveVarianceBps2:  variance,
		EffectiveSamples:        24,
		Reliability:             1,
		OneWayCostBps:           2,
	}
}

func TestCausalRegimeInventoryTargetTreats50PercentAsSoftPrior(t *testing.T) {
	config := CausalRegimeInventoryTargetConfig{
		RiskAversion: 0.01, PriorStrengthBps: 1,
	}
	up := EvaluateCausalRegimeInventoryTarget(config, causalRegimeTargetTestInput(40, 25, 0.5))
	if !up.Ready || up.TargetWeight != 1 {
		t.Fatalf("strong bullish regime must be allowed to reach full inventory: %+v", up)
	}
	down := EvaluateCausalRegimeInventoryTarget(config, causalRegimeTargetTestInput(-40, 25, 0.5))
	if !down.Ready || down.TargetWeight != 0 {
		t.Fatalf("strong bearish regime must be allowed to reach zero inventory: %+v", down)
	}
	if up.TargetWeight == 0.5 || down.TargetWeight == 0.5 {
		t.Fatalf("50%% was treated as an absolute target: up=%+v down=%+v", up, down)
	}
	if up.EffectiveSamples != 24 || up.ExpectedReturnSEBps <= 0 || up.DirectionConfidence <= 0 {
		t.Fatalf("causal target did not expose executable uncertainty evidence: %+v", up)
	}
	if down.DirectionConfidence >= 0 {
		t.Fatalf("bearish causal target must expose a negative direction confidence: %+v", down)
	}
}

func TestCausalRegimeInventoryTargetChargesCostWithoutBinaryGate(t *testing.T) {
	config := CausalRegimeInventoryTargetConfig{
		RiskAversion: 0.01, PriorStrengthBps: 1,
	}
	cheap := causalRegimeTargetTestInput(8, 25, 0.5)
	cheap.OneWayCostBps = 1
	expensive := cheap
	expensive.OneWayCostBps = 7
	cheapDecision := EvaluateCausalRegimeInventoryTarget(config, cheap)
	expensiveDecision := EvaluateCausalRegimeInventoryTarget(config, expensive)
	if !cheapDecision.Ready || !expensiveDecision.Ready {
		t.Fatalf("fee cost must not turn the target into an evidence gate: cheap=%+v expensive=%+v", cheapDecision, expensiveDecision)
	}
	if cheapDecision.TargetWeight <= expensiveDecision.TargetWeight {
		t.Fatalf("higher cost should reduce the target adjustment: cheap=%+v expensive=%+v", cheapDecision, expensiveDecision)
	}
	if expensiveDecision.TargetWeight <= 0.5 {
		t.Fatalf("cost should shrink a positive action, not force a false neutral target: %+v", expensiveDecision)
	}
}

func TestCausalRegimeInventoryTargetUsesHardBoundsOnlyAsBounds(t *testing.T) {
	config := CausalRegimeInventoryTargetConfig{RiskAversion: 0.01, PriorStrengthBps: 1}
	in := causalRegimeTargetTestInput(100, 0, 0.37)
	in.HardMinimum, in.HardMaximum = 0.1, 0.9
	decision := EvaluateCausalRegimeInventoryTarget(config, in)
	if !decision.Ready || decision.TargetWeight != 0.9 {
		t.Fatalf("target did not use the configured full admissible range: %+v", decision)
	}
	if decision.TargetShiftWeight <= 0.2 {
		t.Fatalf("target shift still looks like a hidden 20%% cap: %+v", decision)
	}
}

func TestBuildCausalRegimeInventoryTargetInputFromPivot(t *testing.T) {
	input, ok := BuildCausalRegimeInventoryTargetInputFromPivot(
		PivotRegimeDecision{
			Ready: true, Direction: -1, RemainingAmplitudeBps: 30,
			Reliability: 0.75, CompletedLegSamples: 12,
		}, 0.5, 0.5, 0, 1, 144, 4)
	if !ok {
		t.Fatal("ready pivot did not produce a target input")
	}
	if input.SignedExpectedReturnBps != -30 || input.EffectiveSamples != 12 || input.Reliability != 0.75 {
		t.Fatalf("pivot geometry was not carried into the causal target input: %+v", input)
	}
}

func TestBuildCausalRegimeInventoryTargetInputFromExhaustedPivotPreservesNoTradeKink(t *testing.T) {
	input, ok := BuildCausalRegimeInventoryTargetInputFromPivot(
		PivotRegimeDecision{
			Ready: true, Direction: -1, RemainingAmplitudeBps: 0,
			Reliability: 0.75, CompletedLegSamples: 12,
		}, 0.08, 0.5, 0, 1, 144, 12)
	if !ok {
		t.Fatal("a ready exhausted pivot must produce a zero-alpha CE input")
	}
	decision := EvaluateCausalRegimeInventoryTarget(
		CausalRegimeInventoryTargetConfig{RiskAversion: 0.02, PriorStrengthBps: 8}, input)
	if !decision.Ready {
		t.Fatalf("zero-alpha CE input was not evaluated: %+v", decision)
	}
	if math.Abs(decision.TargetWeight-input.CurrentWeight) > 1e-12 {
		t.Fatalf("exhausted pivot reverted inventory to the prior instead of preserving the no-trade kink: %+v", decision)
	}
}

func TestCausalRegimeInventoryTargetFailsClosed(t *testing.T) {
	in := causalRegimeTargetTestInput(math.NaN(), 25, 0.5)
	decision := EvaluateCausalRegimeInventoryTarget(CausalRegimeInventoryTargetConfig{}, in)
	if decision.Ready || decision.TargetWeight != 0.5 {
		t.Fatalf("non-finite forecast must fail closed at the prior: %+v", decision)
	}
	in = causalRegimeTargetTestInput(5, 25, 0.5)
	in.HardMinimum, in.HardMaximum = 0.8, 0.2
	decision = EvaluateCausalRegimeInventoryTarget(CausalRegimeInventoryTargetConfig{}, in)
	if decision.Ready || decision.Reason != "invalid causal regime target bounds" {
		t.Fatalf("invalid bounds must fail closed: %+v", decision)
	}
}
