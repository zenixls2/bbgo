package gammacapture

import (
	"math"
	"time"
)

// RegimeConditionedTargetConfig controls the one target shift produced by
// current regime evidence.  It deliberately owns no quote, quantity, or side
// admission policy.
type RegimeConditionedTargetConfig struct {
	Enabled               bool    `json:"enabled" yaml:"enabled"`
	Kappa                 float64 `json:"kappa" yaml:"kappa"`
	MaxShiftRatio         float64 `json:"maxShiftRatio" yaml:"maxShiftRatio"`
	PriorEffectiveSamples float64 `json:"priorEffectiveSamples" yaml:"priorEffectiveSamples"`
}

func (c *RegimeConditionedTargetConfig) setDefaults() {
	if c.Kappa <= 0 || math.IsNaN(c.Kappa) || math.IsInf(c.Kappa, 0) {
		c.Kappa = 0.20
	}
	if c.MaxShiftRatio <= 0 || math.IsNaN(c.MaxShiftRatio) || math.IsInf(c.MaxShiftRatio, 0) {
		c.MaxShiftRatio = 0.20
	}
	c.MaxShiftRatio = math.Min(1, c.MaxShiftRatio)
	if c.PriorEffectiveSamples <= 0 || math.IsNaN(c.PriorEffectiveSamples) || math.IsInf(c.PriorEffectiveSamples, 0) {
		c.PriorEffectiveSamples = 8
	}
}

// RegimeConditionedTargetInput contains only information available at the
// current decision timestamp.  Posterior values are probabilities of an up
// move; confidence is a [0,1] reliability score and effective samples are
// causal, matured information mass.
type RegimeConditionedTargetInput struct {
	FastPosterior        float64
	FastConfidence       float64
	FastEffectiveSamples float64

	BOCPDPosterior        float64
	BOCPDConfidence       float64
	BOCPDEffectiveSamples float64

	HorizonPosterior        float64
	HorizonConfidence       float64
	HorizonEffectiveSamples float64
}

// BuildRegimeConditionedTargetInput is the single live/replay adapter from
// causal Fast, BOCPD and horizon evidence. Keeping this mapping here prevents
// production replay from silently testing an empty regime input.
func BuildRegimeConditionedTargetInput(
	rawFastDirection, fastDirectionCoverage, fastDirectionConfidence, fastEffectiveSamples,
	bocpdPosterior, bocpdConfidence, bocpdEffectiveSamples,
	horizonUpRatePerHour, horizonDownRatePerHour, horizonEffectiveSamples float64,
) RegimeConditionedTargetInput {
	coverage := clampRegimeTarget(fastDirectionCoverage, 0, 1)
	in := RegimeConditionedTargetInput{
		FastPosterior:           clampRegimeTarget(0.5+0.5*clampRegimeTarget(rawFastDirection*coverage, -1, 1), 0, 1),
		FastConfidence:          clampRegimeTarget(fastDirectionConfidence*coverage, 0, 1),
		FastEffectiveSamples:    fastEffectiveSamples,
		BOCPDPosterior:          clampRegimeTarget(bocpdPosterior, 0, 1),
		BOCPDConfidence:         clampRegimeTarget(bocpdConfidence, 0, 1),
		BOCPDEffectiveSamples:   bocpdEffectiveSamples,
		HorizonPosterior:        0.5,
		HorizonConfidence:       0,
		HorizonEffectiveSamples: 0,
	}
	horizonRate := horizonUpRatePerHour + horizonDownRatePerHour
	if horizonRate > 0 && horizonEffectiveSamples > 1 {
		in.HorizonPosterior = clampRegimeTarget(horizonUpRatePerHour/horizonRate, 0, 1)
		in.HorizonConfidence = 1
		in.HorizonEffectiveSamples = horizonEffectiveSamples
	}
	return in
}

// BuildRegimeConditionedTargetInputFromTerminalDrift maps forecasts of future
// executable-side BBO returns into the inventory target. Crossing counts and
// touch rates are deliberately excluded: they estimate passive-order arrival,
// not the sign of terminal wealth at the end of the selected Fast horizon.
func BuildRegimeConditionedTargetInputFromTerminalDrift(
	fastDrift FastDriftDecision,
	bocpdPosterior, bocpdConfidence, bocpdEffectiveSamples float64,
) RegimeConditionedTargetInput {
	in := RegimeConditionedTargetInput{
		FastPosterior:         0.5,
		BOCPDPosterior:        clampRegimeTarget(bocpdPosterior, 0, 1),
		BOCPDConfidence:       clampRegimeTarget(bocpdConfidence, 0, 1),
		BOCPDEffectiveSamples: bocpdEffectiveSamples,
		HorizonPosterior:      0.5,
	}
	if fastDrift.Healthy && fastDrift.CenterPredictiveBps2 > 0 &&
		regimeTargetFinite(fastDrift.CenterMeanBps) &&
		regimeTargetFinite(fastDrift.CenterPredictiveBps2) {
		z := fastDrift.CenterMeanBps / math.Sqrt(fastDrift.CenterPredictiveBps2)
		in.FastPosterior = clampRegimeTarget(standardNormalCDF(z), 0, 1)
		in.FastConfidence = clampRegimeTarget(fastDrift.Strength, 0, 1)
		in.FastEffectiveSamples = float64(fastDrift.ValidationSamples)
	}
	return in
}

// RegimeConditionedTargetDecision is the complete, bounded output of the
// target alpha.  TargetShiftRatio is the only value consumed by the target
// actuator after this component is promoted.
type RegimeConditionedTargetDecision struct {
	Ready       bool
	Reason      string
	SignalCount int

	CombinedPosterior float64
	DirectionScore    float64
	Reliability       float64
	EffectiveSamples  float64
	SampleShrink      float64

	TargetShiftRatio float64
	TargetRatio      float64
}

// RefreshRegimeConditionedTargetDecision updates the regime posterior once per
// model bucket and otherwise holds the last causal estimate. This is a sample-
// and-hold estimator, not another admission gate: the held posterior remains
// the sole target input while Fast continues to quote on every market event.
func RefreshRegimeConditionedTargetDecision(
	now time.Time,
	updateInterval time.Duration,
	config RegimeConditionedTargetConfig,
	in RegimeConditionedTargetInput,
	lastBucket time.Time,
	last RegimeConditionedTargetDecision,
) (RegimeConditionedTargetDecision, time.Time, bool) {
	if updateInterval <= 0 {
		updateInterval = 5 * time.Minute
	}
	bucket := now.UTC().Truncate(updateInterval)
	if lastBucket.IsZero() || !bucket.Equal(lastBucket) {
		return EvaluateRegimeConditionedTarget(config, in), bucket, true
	}
	return last, lastBucket, false
}

type regimeTargetSignal struct {
	posterior        float64
	confidence       float64
	effectiveSamples float64
}

func regimeTargetFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clampRegimeTarget(value, lower, upper float64) float64 {
	return math.Max(lower, math.Min(upper, value))
}

func regimeTargetLogit(probability float64) float64 {
	probability = clampRegimeTarget(probability, 1e-4, 1-1e-4)
	return math.Log(probability / (1 - probability))
}

func appendRegimeTargetSignal(signals []regimeTargetSignal, posterior, confidence, effectiveSamples float64) []regimeTargetSignal {
	if !regimeTargetFinite(posterior) || !regimeTargetFinite(confidence) || !regimeTargetFinite(effectiveSamples) ||
		effectiveSamples <= 0 || confidence <= 0 {
		return signals
	}
	return append(signals, regimeTargetSignal{
		posterior:        clampRegimeTarget(posterior, 0, 1),
		confidence:       clampRegimeTarget(confidence, 0, 1),
		effectiveSamples: effectiveSamples,
	})
}

// EvaluateRegimeConditionedTarget implements the bounded posterior rule:
// correlated evidence is combined once, its effective sample count is
// conservatively the smallest participating count, and the resulting shift is
// clipped before it can reach any downstream model.
func EvaluateRegimeConditionedTarget(config RegimeConditionedTargetConfig, in RegimeConditionedTargetInput) RegimeConditionedTargetDecision {
	config.setDefaults()
	d := RegimeConditionedTargetDecision{
		CombinedPosterior: 0.5,
		SampleShrink:      0,
		TargetRatio:       0.5,
		Reason:            "regime-conditioned target has no usable evidence",
	}
	signals := make([]regimeTargetSignal, 0, 3)
	signals = appendRegimeTargetSignal(signals, in.FastPosterior, in.FastConfidence, in.FastEffectiveSamples)
	signals = appendRegimeTargetSignal(signals, in.BOCPDPosterior, in.BOCPDConfidence, in.BOCPDEffectiveSamples)
	signals = appendRegimeTargetSignal(signals, in.HorizonPosterior, in.HorizonConfidence, in.HorizonEffectiveSamples)
	d.SignalCount = len(signals)
	if len(signals) == 0 {
		return d
	}

	weightSum := 0.0
	weightedLogit := 0.0
	minimumSamples := math.Inf(1)
	for _, signal := range signals {
		reliability := math.Sqrt(signal.effectiveSamples /
			(signal.effectiveSamples + config.PriorEffectiveSamples))
		weight := signal.confidence * clampRegimeTarget(reliability, 0, 1)
		if weight <= 0 || !regimeTargetFinite(weight) {
			continue
		}
		weightSum += weight
		weightedLogit += weight * regimeTargetLogit(signal.posterior)
		minimumSamples = math.Min(minimumSamples, signal.effectiveSamples)
	}
	if weightSum <= 0 || math.IsInf(minimumSamples, 0) || minimumSamples <= 1 {
		return d
	}

	// Dividing by (1+sum weights) keeps the aggregate conservative even when
	// several correlated sources point in the same direction.
	combinedLogit := weightedLogit / (1 + weightSum)
	d.DirectionScore = clampRegimeTarget(math.Tanh(combinedLogit), -1, 1)
	d.CombinedPosterior = clampRegimeTarget(0.5*(1+d.DirectionScore), 0, 1)
	d.Reliability = clampRegimeTarget(weightSum/(1+weightSum), 0, 1)
	d.EffectiveSamples = minimumSamples
	d.SampleShrink = math.Sqrt(minimumSamples / (minimumSamples + config.PriorEffectiveSamples))
	d.Ready = true
	d.Reason = "bounded regime posterior ready"
	return d
}

// ApplyRegimeConditionedTarget clips the single posterior shift to the hard
// inventory band.  Keeping this pure makes the target actuator easy to test
// without constructing a Strategy or touching account state.
func ApplyRegimeConditionedTarget(config RegimeConditionedTargetConfig, decision RegimeConditionedTargetDecision, policyTarget, hardMinimum, hardMaximum float64) RegimeConditionedTargetDecision {
	if !regimeTargetFinite(policyTarget) || !regimeTargetFinite(hardMinimum) || !regimeTargetFinite(hardMaximum) || hardMinimum > hardMaximum {
		decision.Ready = false
		decision.Reason = "invalid target bounds"
		return decision
	}
	config.setDefaults()
	minimum := clampRegimeTarget(hardMinimum, 0, 1)
	maximum := clampRegimeTarget(hardMaximum, minimum, 1)
	policy := clampRegimeTarget(policyTarget, minimum, maximum)
	if !decision.Ready {
		decision.TargetRatio = policy
		decision.TargetShiftRatio = 0
		return decision
	}
	shift := config.Kappa * decision.DirectionScore * decision.SampleShrink
	shift = clampRegimeTarget(shift, -config.MaxShiftRatio, config.MaxShiftRatio)
	decision.TargetShiftRatio = shift
	decision.TargetRatio = clampRegimeTarget(policy+shift, minimum, maximum)
	decision.TargetShiftRatio = decision.TargetRatio - policy
	return decision
}
