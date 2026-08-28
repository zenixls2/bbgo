package gammacapture

import (
	"math"
	"testing"
)

func dynamicPriceBetaTargetInput() DynamicPriceBetaTargetInput {
	return DynamicPriceBetaTargetInput{
		CurrentInventoryRatio:   0.80,
		PolicyTargetRatio:       0.50,
		HardMinimumRatio:        0,
		HardMaximumRatio:        1,
		GrossInventoryReturnBps: 80,
		PredictiveVarianceBps2:  400,
		EffectiveSamples:        64,
	}
}

func dynamicPriceBetaTargetConfig() DynamicPriceBetaTargetConfig {
	return DynamicPriceBetaTargetConfig{
		Enabled:               true,
		ChaseTarget:           0.20,
		ChaseZStart:           1,
		ChaseZFull:            2,
		PriorEffectiveSamples: 1,
	}
}

func TestDynamicPriceBetaTargetCapsOverweightPositiveChase(t *testing.T) {
	decision := EvaluateDynamicPriceBetaTarget(dynamicPriceBetaTargetConfig(), dynamicPriceBetaTargetInput())
	if !decision.Ready || !decision.Active || math.Abs(decision.Target-0.52) > 1e-12 {
		t.Fatalf("strong overweight positive forecast should lower the cap: %+v", decision)
	}
	if math.Abs(decision.ChaseScore-0.6) > 1e-12 || decision.TrendScore != 1 || math.Abs(decision.OverweightScore-0.6) > 1e-12 {
		t.Fatalf("chase scores should reflect evidence and overweight exposure: %+v", decision)
	}
}

func TestDynamicPriceBetaTargetDoesNotCapRangeOrBelowTarget(t *testing.T) {
	in := dynamicPriceBetaTargetInput()
	in.GrossInventoryReturnBps = 10
	in.PredictiveVarianceBps2 = 400
	decision := EvaluateDynamicPriceBetaTarget(dynamicPriceBetaTargetConfig(), in)
	if !decision.Ready || decision.Active || decision.Target != 0 || decision.RangeScore <= 0 {
		t.Fatalf("weak/range evidence must preserve the null cap: %+v", decision)
	}

	in = dynamicPriceBetaTargetInput()
	in.CurrentInventoryRatio = 0.20
	decision = EvaluateDynamicPriceBetaTarget(dynamicPriceBetaTargetConfig(), in)
	if decision.Active || decision.Target != 0 || decision.OverweightScore != 0 {
		t.Fatalf("below-target inventory must not be classified as chase: %+v", decision)
	}
}

func TestDynamicPriceBetaTargetDelegatesDeclineToEconomicRiskGradient(t *testing.T) {
	in := dynamicPriceBetaTargetInput()
	in.GrossInventoryReturnBps = -80
	decision := EvaluateDynamicPriceBetaTarget(dynamicPriceBetaTargetConfig(), in)
	if !decision.Ready || decision.Active || decision.Target != 0 || decision.ChaseScore != 0 {
		t.Fatalf("decline must not use the positive-chase cap: %+v", decision)
	}
	if decision.Reason != "negative or neutral forecast delegated to economic risk gradient" {
		t.Fatalf("decline reason must preserve the existing economic controller: %+v", decision)
	}
}

func TestDynamicPriceBetaTargetInterpolatesMonotonically(t *testing.T) {
	config := dynamicPriceBetaTargetConfig()
	low := dynamicPriceBetaTargetInput()
	low.GrossInventoryReturnBps = 30
	high := low
	high.GrossInventoryReturnBps = 60
	lowDecision := EvaluateDynamicPriceBetaTarget(config, low)
	highDecision := EvaluateDynamicPriceBetaTarget(config, high)
	if lowDecision.Target <= 0 || highDecision.Target <= 0 || highDecision.Target >= lowDecision.Target {
		t.Fatalf("stronger chase evidence should monotonically lower the cap: low=%+v high=%+v", lowDecision, highDecision)
	}

	lessOverweight := dynamicPriceBetaTargetInput()
	lessOverweight.CurrentInventoryRatio = 0.60
	lessDecision := EvaluateDynamicPriceBetaTarget(config, lessOverweight)
	moreDecision := EvaluateDynamicPriceBetaTarget(config, dynamicPriceBetaTargetInput())
	if lessDecision.Target <= moreDecision.Target {
		t.Fatalf("more overweight inventory should not relax the cap: less=%+v more=%+v", lessDecision, moreDecision)
	}
}

func TestDynamicPriceBetaTargetFailsClosedAndBoundsOutput(t *testing.T) {
	in := dynamicPriceBetaTargetInput()
	in.EffectiveSamples = 1
	decision := EvaluateDynamicPriceBetaTarget(dynamicPriceBetaTargetConfig(), in)
	if decision.Ready || decision.Active || decision.Target != 0 {
		t.Fatalf("immature evidence must preserve the null cap: %+v", decision)
	}

	in = dynamicPriceBetaTargetInput()
	in.HardMinimumRatio = 0.30
	in.HardMaximumRatio = 0.60
	config := dynamicPriceBetaTargetConfig()
	config.ChaseTarget = 0.05
	decision = EvaluateDynamicPriceBetaTarget(config, in)
	if !decision.Active || decision.Target < 0.30 || decision.Target > 0.60 {
		t.Fatalf("dynamic cap must remain inside hard bounds: %+v", decision)
	}
}
