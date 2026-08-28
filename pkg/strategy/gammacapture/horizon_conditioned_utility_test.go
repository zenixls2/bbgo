package gammacapture

import (
	"math"
	"testing"
)

func TestHorizonConditionedUtilityContinuationCanOffsetShortMarkout(t *testing.T) {
	decision := EvaluateHorizonConditionedUtility(HorizonConditionedUtilityCandidate{
		NotionalJPY:                     1_000,
		FillProbability:                 1,
		ConditionalExecutionValueJPY:    -2,
		ConditionalContinuationValueJPY: 5,
		BaselineVarianceJPY2:            0,
		AfterFillVarianceJPY2:           0,
		EffectiveSamples:                100,
		PairEquityJPY:                   10_000,
		RiskAversion:                    1,
		ConfidenceZ:                     1.645,
	})
	if !decision.Evaluated || !decision.Approved || decision.CertaintyEquivalentJPY <= 0 {
		t.Fatalf("positive continuation should offset short markout: %+v", decision)
	}
	if math.Abs(decision.ExpectedDeltaJPY-3) > 1e-12 {
		t.Fatalf("unexpected combined conditional value: %+v", decision)
	}
}

func TestHorizonConditionedUtilityFillMixtureIncludesArrivalVariance(t *testing.T) {
	decision := EvaluateHorizonConditionedUtility(HorizonConditionedUtilityCandidate{
		NotionalJPY:                  1_000,
		FillProbability:              0.5,
		ConditionalExecutionValueJPY: 10,
		BaselineVarianceJPY2:         4,
		AfterFillVarianceJPY2:        4,
		EffectiveSamples:             1,
		PairEquityJPY:                10_000,
		RiskAversion:                 1,
		ConfidenceZ:                  0,
	})
	if !decision.Evaluated {
		t.Fatalf("expected valid mixture decision: %+v", decision)
	}
	// 0.5*4 + 0.5*4 + 0.5*0.5*10^2 = 29.
	if math.Abs(decision.MixtureVarianceJPY2-29) > 1e-12 ||
		math.Abs(decision.DeltaVarianceJPY2-25) > 1e-12 {
		t.Fatalf("arrival uncertainty was omitted: %+v", decision)
	}
	if decision.RiskPenaltyJPY <= 0 {
		t.Fatalf("fill/no-fill variance must be risk charged: %+v", decision)
	}
}

func TestHorizonConditionedUtilityRiskRepairCanPassSmallNegativeAlpha(t *testing.T) {
	decision := EvaluateHorizonConditionedUtility(HorizonConditionedUtilityCandidate{
		NotionalJPY:                  1_000,
		FillProbability:              1,
		ConditionalExecutionValueJPY: -1,
		BaselineVarianceJPY2:         10_000,
		AfterFillVarianceJPY2:        0,
		EffectiveSamples:             100,
		PairEquityJPY:                10_000,
		RiskAversion:                 3,
		ConfidenceZ:                  0,
	})
	if !decision.Evaluated || !decision.Approved || decision.DeltaVarianceJPY2 >= 0 ||
		decision.RiskPenaltyJPY >= 0 || decision.CertaintyEquivalentJPY <= 0 {
		t.Fatalf("risk reduction should support a small hedge loss: %+v", decision)
	}
}

func TestHorizonConditionedUtilityRejectsInvalidAndSelectsConservatively(t *testing.T) {
	invalid := EvaluateHorizonConditionedUtility(HorizonConditionedUtilityCandidate{
		NotionalJPY: 1, FillProbability: 1.1, PairEquityJPY: 1, EffectiveSamples: 1,
	})
	if invalid.Evaluated {
		t.Fatalf("invalid fill probability was accepted: %+v", invalid)
	}
	selected := SelectHorizonConditionedUtility([]HorizonConditionedUtilityCandidate{
		{NotionalJPY: 100, FillProbability: 1, ConditionalExecutionValueJPY: 1,
			EffectiveSamples: 100, PairEquityJPY: 10_000},
		{NotionalJPY: 200, FillProbability: 1, ConditionalExecutionValueJPY: 2,
			EffectiveSamples: 100, PairEquityJPY: 10_000},
	})
	if !selected.Approved || selected.NotionalJPY != 200 {
		t.Fatalf("highest positive utility candidate was not selected: %+v", selected)
	}
}
