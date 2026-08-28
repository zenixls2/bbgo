package gammacapture

import (
	"math"
	"testing"
	"time"
)

func regimeTargetTestConfig() RegimeConditionedTargetConfig {
	return RegimeConditionedTargetConfig{
		Enabled:               true,
		Kappa:                 0.20,
		MaxShiftRatio:         0.20,
		PriorEffectiveSamples: 8,
	}
}

func TestRegimeConditionedTargetRefreshUsesModelBuckets(t *testing.T) {
	start := time.Date(2026, 8, 20, 10, 2, 0, 0, time.UTC)
	up := RegimeConditionedTargetInput{
		FastPosterior: 1, FastConfidence: 1, FastEffectiveSamples: 100,
	}
	first, bucket, updated := RefreshRegimeConditionedTargetDecision(
		start, 5*time.Minute, regimeTargetTestConfig(), up,
		time.Time{}, RegimeConditionedTargetDecision{})
	if !updated || !first.Ready || !bucket.Equal(start.Truncate(5*time.Minute)) {
		t.Fatalf("first bucket was not evaluated: decision=%+v bucket=%s updated=%t", first, bucket, updated)
	}
	down := RegimeConditionedTargetInput{
		FastPosterior: 0, FastConfidence: 1, FastEffectiveSamples: 100,
	}
	held, heldBucket, updated := RefreshRegimeConditionedTargetDecision(
		start.Add(2*time.Minute), 5*time.Minute, regimeTargetTestConfig(), down,
		bucket, first)
	if updated || held.DirectionScore != first.DirectionScore || !heldBucket.Equal(bucket) {
		t.Fatalf("posterior changed inside one model bucket: first=%+v held=%+v", first, held)
	}
	next, nextBucket, updated := RefreshRegimeConditionedTargetDecision(
		start.Add(4*time.Minute), 5*time.Minute, regimeTargetTestConfig(), down,
		heldBucket, held)
	if !updated || next.DirectionScore >= 0 || !nextBucket.After(bucket) {
		t.Fatalf("next model bucket did not accept fresh evidence: next=%+v bucket=%s", next, nextBucket)
	}
}

func TestRegimeConditionedTargetUsesTerminalDriftNotCrossingArrivals(t *testing.T) {
	in := BuildRegimeConditionedTargetInputFromTerminalDrift(FastDriftDecision{
		Healthy: true, CenterMeanBps: 12, CenterPredictiveBps2: 36,
		Strength: .8, ValidationSamples: 24,
	}, .5, 0, 0)
	if in.FastPosterior <= .5 || in.FastConfidence != .8 || in.FastEffectiveSamples != 24 {
		t.Fatalf("terminal Fast drift was not mapped to an up posterior: %+v", in)
	}
	if in.HorizonConfidence != 0 || in.HorizonEffectiveSamples != 0 {
		t.Fatalf("order-arrival evidence leaked into terminal target: %+v", in)
	}
	unhealthy := BuildRegimeConditionedTargetInputFromTerminalDrift(FastDriftDecision{
		CenterMeanBps: 100, CenterPredictiveBps2: 1, Strength: 1, ValidationSamples: 100,
	}, .5, 0, 0)
	if unhealthy.FastConfidence != 0 || unhealthy.FastEffectiveSamples != 0 {
		t.Fatalf("unvalidated drift must remain unavailable: %+v", unhealthy)
	}
}

func TestBuildRegimeConditionedTargetInputMatchesLiveCausalMapping(t *testing.T) {
	got := BuildRegimeConditionedTargetInput(
		0.8, 0.5, 0.6, 12,
		0.7, 0.4, 20,
		3, 1, 9)
	if math.Abs(got.FastPosterior-0.7) > 1e-12 ||
		math.Abs(got.FastConfidence-0.3) > 1e-12 ||
		got.FastEffectiveSamples != 12 {
		t.Fatalf("Fast mapping mismatch: %+v", got)
	}
	if got.BOCPDPosterior != 0.7 || got.BOCPDConfidence != 0.4 ||
		got.BOCPDEffectiveSamples != 20 {
		t.Fatalf("BOCPD mapping mismatch: %+v", got)
	}
	if got.HorizonPosterior != 0.75 || got.HorizonConfidence != 1 ||
		got.HorizonEffectiveSamples != 9 {
		t.Fatalf("horizon mapping mismatch: %+v", got)
	}

	immature := BuildRegimeConditionedTargetInput(0, 0, 0, 0, 0, 0, 0, 3, 1, 1)
	if immature.HorizonPosterior != 0.5 || immature.HorizonConfidence != 0 ||
		immature.HorizonEffectiveSamples != 0 {
		t.Fatalf("immature horizon evidence must remain neutral: %+v", immature)
	}
}

func TestRegimeConditionedTargetNeutralEvidence(t *testing.T) {
	decision := EvaluateRegimeConditionedTarget(regimeTargetTestConfig(), RegimeConditionedTargetInput{
		FastPosterior: 0.5, FastConfidence: 1, FastEffectiveSamples: 100,
		BOCPDPosterior: 0.5, BOCPDConfidence: 1, BOCPDEffectiveSamples: 100,
		HorizonPosterior: 0.5, HorizonConfidence: 1, HorizonEffectiveSamples: 100,
	})
	decision = ApplyRegimeConditionedTarget(regimeTargetTestConfig(), decision, 0.5, 0, 1)
	if !decision.Ready || math.Abs(decision.DirectionScore) > 1e-12 || math.Abs(decision.TargetShiftRatio) > 1e-12 || decision.TargetRatio != 0.5 {
		t.Fatalf("neutral evidence must retain the neutral target: %+v", decision)
	}
}

func TestRegimeConditionedTargetUpDownSymmetry(t *testing.T) {
	up := EvaluateRegimeConditionedTarget(regimeTargetTestConfig(), RegimeConditionedTargetInput{
		FastPosterior: 1, FastConfidence: 1, FastEffectiveSamples: 100,
		BOCPDPosterior: 1, BOCPDConfidence: 1, BOCPDEffectiveSamples: 100,
		HorizonPosterior: 1, HorizonConfidence: 1, HorizonEffectiveSamples: 100,
	})
	down := EvaluateRegimeConditionedTarget(regimeTargetTestConfig(), RegimeConditionedTargetInput{
		FastPosterior: 0, FastConfidence: 1, FastEffectiveSamples: 100,
		BOCPDPosterior: 0, BOCPDConfidence: 1, BOCPDEffectiveSamples: 100,
		HorizonPosterior: 0, HorizonConfidence: 1, HorizonEffectiveSamples: 100,
	})
	up = ApplyRegimeConditionedTarget(regimeTargetTestConfig(), up, 0.5, 0, 1)
	down = ApplyRegimeConditionedTarget(regimeTargetTestConfig(), down, 0.5, 0, 1)
	if up.TargetRatio <= 0.5 || down.TargetRatio >= 0.5 {
		t.Fatalf("monotone evidence must move the target symmetrically: up=%+v down=%+v", up, down)
	}
	if math.Abs(up.TargetShiftRatio+down.TargetShiftRatio) > 1e-12 {
		t.Fatalf("up/down target shifts must be symmetric: up=%+v down=%+v", up, down)
	}
}

func TestRegimeConditionedTargetConflictingEvidenceShrinks(t *testing.T) {
	decision := EvaluateRegimeConditionedTarget(regimeTargetTestConfig(), RegimeConditionedTargetInput{
		FastPosterior: 1, FastConfidence: 1, FastEffectiveSamples: 100,
		BOCPDPosterior: 0, BOCPDConfidence: 1, BOCPDEffectiveSamples: 100,
		HorizonPosterior: 0.5, HorizonConfidence: 1, HorizonEffectiveSamples: 100,
	})
	decision = ApplyRegimeConditionedTarget(regimeTargetTestConfig(), decision, 0.5, 0, 1)
	if math.Abs(decision.DirectionScore) > 1e-12 || math.Abs(decision.TargetShiftRatio) > 1e-12 {
		t.Fatalf("conflicting equal evidence should cancel, not amplify: %+v", decision)
	}
}

func TestRegimeConditionedTargetUsesConservativeEffectiveSamples(t *testing.T) {
	decision := EvaluateRegimeConditionedTarget(regimeTargetTestConfig(), RegimeConditionedTargetInput{
		FastPosterior: 1, FastConfidence: 1, FastEffectiveSamples: 100,
		BOCPDPosterior: 1, BOCPDConfidence: 1, BOCPDEffectiveSamples: 4,
		HorizonPosterior: 1, HorizonConfidence: 1, HorizonEffectiveSamples: 80,
	})
	if decision.EffectiveSamples != 4 {
		t.Fatalf("correlated evidence must use the smallest effective sample count: %+v", decision)
	}
	if decision.SampleShrink >= 1 || decision.SampleShrink <= 0 {
		t.Fatalf("sample shrink should be bounded in (0,1): %+v", decision)
	}
}

func TestRegimeConditionedTargetBoundsAndInvalidInput(t *testing.T) {
	config := regimeTargetTestConfig()
	decision := EvaluateRegimeConditionedTarget(config, RegimeConditionedTargetInput{
		FastPosterior: 1, FastConfidence: 1, FastEffectiveSamples: 100,
	})
	decision = ApplyRegimeConditionedTarget(config, decision, 0.5, 0.45, 0.55)
	if decision.TargetRatio < 0.45 || decision.TargetRatio > 0.55 {
		t.Fatalf("target must stay inside hard bounds: %+v", decision)
	}
	invalid := EvaluateRegimeConditionedTarget(config, RegimeConditionedTargetInput{
		FastPosterior: math.NaN(), FastConfidence: 1, FastEffectiveSamples: 100,
	})
	if invalid.Ready {
		t.Fatalf("invalid posterior must not create a target signal: %+v", invalid)
	}
	invalid = ApplyRegimeConditionedTarget(config, invalid, 0.5, 0.6, 0.4)
	if invalid.Reason != "invalid target bounds" || invalid.Ready {
		t.Fatalf("invalid bounds must fail closed: %+v", invalid)
	}
}

func TestRegimeConditionedTargetDoesNotExceedConfiguredShift(t *testing.T) {
	config := regimeTargetTestConfig()
	config.Kappa = 10
	config.MaxShiftRatio = 0.07
	decision := EvaluateRegimeConditionedTarget(config, RegimeConditionedTargetInput{
		FastPosterior: 1, FastConfidence: 1, FastEffectiveSamples: 10_000,
		BOCPDPosterior: 1, BOCPDConfidence: 1, BOCPDEffectiveSamples: 10_000,
		HorizonPosterior: 1, HorizonConfidence: 1, HorizonEffectiveSamples: 10_000,
	})
	decision = ApplyRegimeConditionedTarget(config, decision, 0.5, 0, 1)
	if math.Abs(decision.TargetShiftRatio) > 0.07+1e-12 {
		t.Fatalf("configured shift cap must hold even for extreme evidence: %+v", decision)
	}
}
