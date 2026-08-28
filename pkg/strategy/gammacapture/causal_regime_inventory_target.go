package gammacapture

import "math"

// CausalRegimeInventoryTargetConfig controls the standalone causal regime
// target optimizer.  PriorWeight is deliberately a soft prior: it is not an
// inventory cap and it must not be confused with a risk-free allocation.
//
// All return and cost inputs are in basis points.  RiskAversion has units of
// 1/bps and PriorStrengthBps has units of bps, so every term in the objective
// is a certainty-equivalent amount in bps.
type CausalRegimeInventoryTargetConfig struct {
	RiskAversion     float64 `json:"riskAversion" yaml:"riskAversion"`
	PriorStrengthBps float64 `json:"priorStrengthBps" yaml:"priorStrengthBps"`
}

func (c *CausalRegimeInventoryTargetConfig) setDefaults() {
	if c.RiskAversion <= 0 || !finiteCausalRegimeTarget(c.RiskAversion) {
		c.RiskAversion = 0.02
	}
	if c.PriorStrengthBps <= 0 || !finiteCausalRegimeTarget(c.PriorStrengthBps) {
		c.PriorStrengthBps = 8
	}
}

// CausalRegimeInventoryTargetInput is the complete state needed by the pure
// target optimizer.  The forecast must be formed causally: for a pivot source
// of truth, it may only use the currently active pivot leg and completed legs
// observed before the decision timestamp.
type CausalRegimeInventoryTargetInput struct {
	CurrentWeight float64
	PriorWeight   float64
	HardMinimum   float64
	HardMaximum   float64

	SignedExpectedReturnBps float64
	PredictiveVarianceBps2  float64
	EffectiveSamples        float64
	Reliability             float64
	OneWayCostBps           float64
}

// CausalRegimeInventoryTargetDecision is the single output of this alpha.  It
// owns only a target weight; price, quantity, quote-side admission and fill
// logic remain downstream responsibilities.
type CausalRegimeInventoryTargetDecision struct {
	Ready  bool
	Reason string

	CurrentWeight     float64
	PriorWeight       float64
	TargetWeight      float64
	TargetDeltaWeight float64
	TargetShiftWeight float64

	GrossExpectedReturnBps  float64
	ShrunkExpectedReturnBps float64
	// ExpectedReturnSEBps is the standard error of the shrunk return forecast.
	// PredictiveVarianceBps2 contains next-window variance plus estimation
	// variance, so under the same moment decomposition used by this solver the
	// mean standard error is sqrt(predictive variance/(N+1)).
	ExpectedReturnSEBps    float64
	PredictiveVarianceBps2 float64
	EffectiveSamples       float64
	SampleShrink           float64
	Reliability            float64
	DirectionConfidence    float64
	RiskCoefficientBps     float64
	PriorStrengthBps       float64
	OneWayCostBps          float64

	PriorCEBps   float64
	OptimalCEBps float64
}

// BuildCausalRegimeInventoryTargetInputFromPivot is the pivot-source adapter.
// A positive active pivot leg supplies a positive expected remaining return;
// a negative active leg supplies a negative one.  The pivot filter's
// reliability and completed-leg count are used as empirical-Bayes shrinkage,
// not as a binary permission gate.  Predictive variance is supplied by the
// causal continuation/leg-magnitude estimator because PivotRegimeFilter only
// stores the first moment of completed legs.
func BuildCausalRegimeInventoryTargetInputFromPivot(
	decision PivotRegimeDecision,
	currentWeight, priorWeight, hardMinimum, hardMaximum,
	predictiveVarianceBps2, oneWayCostBps float64,
) (CausalRegimeInventoryTargetInput, bool) {
	if !decision.Ready || (decision.Direction != 1 && decision.Direction != -1) ||
		!finiteCausalRegimeTarget(decision.RemainingAmplitudeBps) ||
		!finiteCausalRegimeTarget(decision.Reliability) ||
		decision.RemainingAmplitudeBps < 0 || decision.Reliability <= 0 {
		return CausalRegimeInventoryTargetInput{}, false
	}
	return CausalRegimeInventoryTargetInput{
		CurrentWeight:           currentWeight,
		PriorWeight:             priorWeight,
		HardMinimum:             hardMinimum,
		HardMaximum:             hardMaximum,
		SignedExpectedReturnBps: float64(decision.Direction) * decision.RemainingAmplitudeBps,
		PredictiveVarianceBps2:  predictiveVarianceBps2,
		EffectiveSamples:        float64(decision.CompletedLegSamples),
		Reliability:             decision.Reliability,
		OneWayCostBps:           oneWayCostBps,
	}, true
}

// EvaluateCausalRegimeInventoryTarget solves
//
//	CE(w) = mu*w - gamma*sigma^2*w^2/2
//	         - kappa*(w-w0)^2/2 - c*|w-wCurrent|.
//
// The L1 term is the fee/adverse-selection cost of changing the risky weight.
// It creates a no-trade region around the current position without imposing a
// permanent 50% target.  The two stationary points on either side of the
// current weight, the kink, and both hard bounds are compared explicitly.
// Consequently a sufficiently strong causal regime can select 0% or 100%.
func EvaluateCausalRegimeInventoryTarget(
	config CausalRegimeInventoryTargetConfig,
	in CausalRegimeInventoryTargetInput,
) CausalRegimeInventoryTargetDecision {
	config.setDefaults()
	d := CausalRegimeInventoryTargetDecision{
		TargetWeight:           0.5,
		PriorWeight:            0.5,
		Reason:                 "causal regime target has no usable evidence",
		PriorStrengthBps:       config.PriorStrengthBps,
		PredictiveVarianceBps2: math.Max(0, in.PredictiveVarianceBps2),
	}
	if !finiteCausalRegimeTarget(in.HardMinimum) || !finiteCausalRegimeTarget(in.HardMaximum) ||
		in.HardMinimum > in.HardMaximum {
		d.Reason = "invalid causal regime target bounds"
		return d
	}
	minimum := clampCausalRegimeTarget(in.HardMinimum, 0, 1)
	maximum := clampCausalRegimeTarget(in.HardMaximum, minimum, 1)
	prior := clampCausalRegimeTarget(in.PriorWeight, minimum, maximum)
	current := clampCausalRegimeTarget(in.CurrentWeight, minimum, maximum)
	d.PriorWeight, d.CurrentWeight = prior, current
	d.TargetWeight = prior

	if !finiteCausalRegimeTarget(in.SignedExpectedReturnBps) ||
		!finiteCausalRegimeTarget(in.PredictiveVarianceBps2) || in.PredictiveVarianceBps2 < 0 ||
		!finiteCausalRegimeTarget(in.EffectiveSamples) || in.EffectiveSamples <= 0 ||
		!finiteCausalRegimeTarget(in.Reliability) || in.Reliability < 0 ||
		!finiteCausalRegimeTarget(in.OneWayCostBps) || in.OneWayCostBps < 0 {
		d.Reason = "invalid causal regime target evidence"
		return d
	}

	reliability := clampCausalRegimeTarget(in.Reliability, 0, 1)
	sampleShrink := in.EffectiveSamples / (in.EffectiveSamples + 1)
	sampleShrink = clampCausalRegimeTarget(sampleShrink, 0, 1)
	mu := in.SignedExpectedReturnBps * reliability * sampleShrink
	variance := math.Max(0, in.PredictiveVarianceBps2)
	riskCoefficient := config.RiskAversion * variance
	denominator := riskCoefficient + config.PriorStrengthBps
	if !finiteCausalRegimeTarget(denominator) || denominator <= 0 {
		d.Reason = "causal regime target has invalid CE denominator"
		return d
	}

	d.Ready = true
	d.Reason = "causal pivot-regime CE target ready"
	d.GrossExpectedReturnBps = in.SignedExpectedReturnBps
	d.ShrunkExpectedReturnBps = mu
	d.PredictiveVarianceBps2 = variance
	d.EffectiveSamples = in.EffectiveSamples
	d.ExpectedReturnSEBps = math.Sqrt(variance / (in.EffectiveSamples + 1))
	d.SampleShrink = sampleShrink
	d.Reliability = reliability
	if variance > 0 {
		d.DirectionConfidence = clampCausalRegimeTarget(2*normalCDFCausalRegimeTarget(
			mu/math.Sqrt(variance))-1, -1, 1)
	} else if mu > 0 {
		d.DirectionConfidence = 1
	} else if mu < 0 {
		d.DirectionConfidence = -1
	}
	d.RiskCoefficientBps = riskCoefficient
	d.OneWayCostBps = in.OneWayCostBps

	// The L1 switching cost changes the derivative by -c above current and +c
	// below current.  Include the kink/current point and the hard bounds so
	// that clipping is a consequence of the objective, not a hidden shift cap.
	baseNumerator := mu + config.PriorStrengthBps*prior
	upStationary := (baseNumerator - in.OneWayCostBps) / denominator
	downStationary := (baseNumerator + in.OneWayCostBps) / denominator
	candidates := []float64{
		current,
		minimum,
		maximum,
		clampCausalRegimeTarget(upStationary, current, maximum),
		clampCausalRegimeTarget(downStationary, minimum, current),
	}
	ce := func(weight float64) float64 {
		return mu*weight -
			0.5*config.RiskAversion*variance*weight*weight -
			0.5*config.PriorStrengthBps*(weight-prior)*(weight-prior) -
			in.OneWayCostBps*math.Abs(weight-current)
	}
	priorCE := ce(prior)
	best := current
	bestCE := ce(current)
	for _, candidate := range candidates {
		candidate = clampCausalRegimeTarget(candidate, minimum, maximum)
		candidateCE := ce(candidate)
		if candidateCE > bestCE+1e-12 ||
			(math.Abs(candidateCE-bestCE) <= 1e-12 && math.Abs(candidate-current) < math.Abs(best-current)) {
			best, bestCE = candidate, candidateCE
		}
	}
	d.TargetWeight = best
	d.TargetDeltaWeight = best - current
	d.TargetShiftWeight = best - prior
	d.PriorCEBps = priorCE
	d.OptimalCEBps = bestCE
	return d
}

func finiteCausalRegimeTarget(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func normalCDFCausalRegimeTarget(value float64) float64 {
	return 0.5 * (1 + math.Erf(value/math.Sqrt2))
}

func clampCausalRegimeTarget(value, lower, upper float64) float64 {
	return math.Max(lower, math.Min(upper, value))
}
