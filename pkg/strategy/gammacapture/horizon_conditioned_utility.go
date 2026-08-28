package gammacapture

import "math"

// HorizonConditionedUtilityCandidate contains one already-priced order
// candidate. Conditional values are measured only when the order fills:
// executionValue is the short fee-net terminal value and continuationValue is
// the value from the short horizon to the longer continuation horizon. The
// candidate must not choose a direction or price; it is a quantity-layer
// utility input.
type HorizonConditionedUtilityCandidate struct {
	NotionalJPY float64

	FillProbability         float64
	FillProbabilityStdError float64

	ConditionalExecutionValueJPY    float64
	ConditionalContinuationValueJPY float64
	ConditionalMeanStdErrorJPY      float64

	BaselineVarianceJPY2  float64
	AfterFillVarianceJPY2 float64
	EffectiveSamples      float64
	PairEquityJPY         float64
	RiskAversion          float64
	ConfidenceZ           float64
}

// HorizonConditionedUtilityDecision is the target-relative certainty
// equivalent of one candidate. The no-fill state is the baseline state, so
// fill probability is handled as a mixture rather than as a linear discount
// of the after-fill variance.
type HorizonConditionedUtilityDecision struct {
	Evaluated bool
	Approved  bool
	Reason    string

	NotionalJPY           float64
	FillProbability       float64
	ConditionalMeanJPY    float64
	ExpectedDeltaJPY      float64
	BaselineVarianceJPY2  float64
	AfterFillVarianceJPY2 float64
	MixtureVarianceJPY2   float64
	DeltaVarianceJPY2     float64

	BaselineStdErrorJPY      float64
	WholePositionStdErrorJPY float64
	MeanEstimateVarianceJPY2 float64
	LowerDeltaJPY            float64
	RiskPenaltyJPY           float64
	CertaintyEquivalentJPY   float64
}

// EvaluateHorizonConditionedUtility evaluates the Bellman-style candidate
// against doing nothing. If the order does not fill, both the short and the
// continuation increment are zero and the existing target-relative inventory
// distribution remains the baseline. For a conditional filled mean m and
// fill probability p:
//
//	E[Delta] = p*m
//	Var[whole] = (1-p)*Var[baseline] + p*Var[after] + p*(1-p)*m^2
//
// The confidence term compares whole-position and baseline standard errors;
// it does not reuse the incremental-order SE that caused the previous CE
// scale error.
func EvaluateHorizonConditionedUtility(candidate HorizonConditionedUtilityCandidate) HorizonConditionedUtilityDecision {
	d := HorizonConditionedUtilityDecision{NotionalJPY: candidate.NotionalJPY}
	if candidate.NotionalJPY < 0 || candidate.PairEquityJPY <= 0 ||
		candidate.EffectiveSamples <= 0 ||
		!finiteHorizonUtility(candidate.NotionalJPY) ||
		!finiteHorizonUtility(candidate.PairEquityJPY) ||
		!finiteHorizonUtility(candidate.EffectiveSamples) {
		d.Reason = "invalid candidate capital or sample inputs"
		return d
	}
	if candidate.FillProbability < 0 || candidate.FillProbability > 1 ||
		!finiteHorizonUtility(candidate.FillProbability) ||
		candidate.FillProbabilityStdError < 0 ||
		!finiteHorizonUtility(candidate.FillProbabilityStdError) {
		d.Reason = "invalid fill probability inputs"
		return d
	}
	if candidate.ConditionalExecutionValueJPY != 0 &&
		!finiteHorizonUtility(candidate.ConditionalExecutionValueJPY) {
		d.Reason = "invalid conditional execution value"
		return d
	}
	if candidate.ConditionalContinuationValueJPY != 0 &&
		!finiteHorizonUtility(candidate.ConditionalContinuationValueJPY) {
		d.Reason = "invalid conditional continuation value"
		return d
	}
	if candidate.ConditionalMeanStdErrorJPY < 0 ||
		!finiteHorizonUtility(candidate.ConditionalMeanStdErrorJPY) {
		d.Reason = "invalid conditional mean uncertainty"
		return d
	}
	if candidate.BaselineVarianceJPY2 < 0 || candidate.AfterFillVarianceJPY2 < 0 ||
		!finiteHorizonUtility(candidate.BaselineVarianceJPY2) ||
		!finiteHorizonUtility(candidate.AfterFillVarianceJPY2) {
		d.Reason = "invalid inventory variance inputs"
		return d
	}

	p := candidate.FillProbability
	conditionalMean := candidate.ConditionalExecutionValueJPY +
		candidate.ConditionalContinuationValueJPY
	mixtureVariance := (1-p)*candidate.BaselineVarianceJPY2 +
		p*candidate.AfterFillVarianceJPY2 + p*(1-p)*conditionalMean*conditionalMean
	mixtureVariance = math.Max(0, mixtureVariance)
	effectiveSamples := math.Max(1, candidate.EffectiveSamples)
	baselineSE := math.Sqrt(candidate.BaselineVarianceJPY2 / effectiveSamples)
	meanEstimateVariance := p * p * candidate.ConditionalMeanStdErrorJPY *
		candidate.ConditionalMeanStdErrorJPY
	if candidate.FillProbabilityStdError > 0 {
		meanEstimateVariance += conditionalMean * conditionalMean *
			candidate.FillProbabilityStdError * candidate.FillProbabilityStdError
	}
	meanEstimateVariance = math.Max(0, meanEstimateVariance)
	wholeSE := math.Sqrt(mixtureVariance/effectiveSamples + meanEstimateVariance)
	z := math.Max(0, candidate.ConfidenceZ)
	if !finiteHorizonUtility(z) {
		z = 0
	}
	expectedDelta := p * conditionalMean
	deltaVariance := mixtureVariance - candidate.BaselineVarianceJPY2
	riskPenalty := math.Max(0, candidate.RiskAversion) * deltaVariance /
		(2 * candidate.PairEquityJPY)
	lower := expectedDelta - z*(wholeSE-baselineSE)

	d.Evaluated = true
	d.FillProbability = p
	d.ConditionalMeanJPY = conditionalMean
	d.ExpectedDeltaJPY = expectedDelta
	d.BaselineVarianceJPY2 = candidate.BaselineVarianceJPY2
	d.AfterFillVarianceJPY2 = candidate.AfterFillVarianceJPY2
	d.MixtureVarianceJPY2 = mixtureVariance
	d.DeltaVarianceJPY2 = deltaVariance
	d.BaselineStdErrorJPY = baselineSE
	d.WholePositionStdErrorJPY = wholeSE
	d.MeanEstimateVarianceJPY2 = meanEstimateVariance
	d.LowerDeltaJPY = lower
	d.RiskPenaltyJPY = riskPenalty
	d.CertaintyEquivalentJPY = lower - riskPenalty
	d.Approved = d.NotionalJPY > 0 && d.CertaintyEquivalentJPY > 0
	d.Reason = "evaluated against no-fill baseline"
	if d.Approved {
		d.Reason = "positive horizon-conditioned CE"
	}
	return d
}

// SelectHorizonConditionedUtility chooses the highest CE candidate. A
// no-order candidate should be included by the caller when it wants an
// explicit abstention row. Ties prefer the smaller notional, making quantity
// conservative without adding a second threshold gate.
func SelectHorizonConditionedUtility(candidates []HorizonConditionedUtilityCandidate) HorizonConditionedUtilityDecision {
	best := HorizonConditionedUtilityDecision{Reason: "no valid candidate"}
	for _, candidate := range candidates {
		decision := EvaluateHorizonConditionedUtility(candidate)
		if !decision.Evaluated {
			continue
		}
		if !best.Evaluated ||
			decision.CertaintyEquivalentJPY > best.CertaintyEquivalentJPY+1e-12 ||
			(math.Abs(decision.CertaintyEquivalentJPY-best.CertaintyEquivalentJPY) <= 1e-12 &&
				decision.NotionalJPY < best.NotionalJPY) {
			best = decision
		}
	}
	if !best.Evaluated {
		return best
	}
	best.Approved = best.NotionalJPY > 0 && best.CertaintyEquivalentJPY > 0
	if best.Approved {
		best.Reason = "positive horizon-conditioned CE"
	} else {
		best.Reason = "no positive horizon-conditioned CE candidate"
	}
	return best
}

func finiteHorizonUtility(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
