package gammacapture

import (
	"math"
	"time"
)

// DynamicInventoryAimConfig enables the single target/speed controller.  It
// is intentionally a quantity/inventory component: it must not own quote
// distance, order submission, or an independent side gate.
type DynamicInventoryAimConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	ShadowOnly bool `json:"shadowOnly" yaml:"shadowOnly"`
	// MinimumSamples is retained for configuration/checkpoint compatibility.
	// It is no longer an economic gate: overlapping path evidence is admitted
	// when its predictive bound clears cost. The evaluator only requires
	// EffectiveSamples > 1 to form a non-degenerate predictive estimate.
	MinimumSamples       int     `json:"minimumSamples" yaml:"minimumSamples"`
	EvidencePriorSamples float64 `json:"evidencePriorSamples" yaml:"evidencePriorSamples"`
	// ConfidenceZScore is the one-sided normal critical value used by the
	// fee-adjusted predictive bound. 1.645 is a 95% one-sided bound.
	ConfidenceZScore float64 `json:"confidenceZScore" yaml:"confidenceZScore"`
}

func (c *DynamicInventoryAimConfig) setDefaults(fallbackMinimumSamples int) {
	if c.MinimumSamples <= 0 {
		c.MinimumSamples = fallbackMinimumSamples
	}
	if c.MinimumSamples < 2 {
		c.MinimumSamples = 2
	}
	if c.EvidencePriorSamples < 0 || math.IsNaN(c.EvidencePriorSamples) || math.IsInf(c.EvidencePriorSamples, 0) {
		c.EvidencePriorSamples = 1
	}
	if c.EvidencePriorSamples == 0 {
		c.EvidencePriorSamples = 1
	}
	if c.ConfidenceZScore <= 0 || math.IsNaN(c.ConfidenceZScore) || math.IsInf(c.ConfidenceZScore, 0) {
		c.ConfidenceZScore = 1.645
	}
}

// DynamicInventoryAimInput is the causal state available at a quote
// decision. GrossInventoryReturnBps is the same-symbol, future-BBO-weighted
// return of owning inventory over the selected horizon before the one-way
// execution cost. PredictiveVarianceBps2 includes both path variance and
// estimation variance.
type DynamicInventoryAimInput struct {
	CurrentInventoryRatio float64
	PolicyTargetRatio     float64
	HardMinimumRatio      float64
	HardMaximumRatio      float64

	GrossInventoryReturnBps float64
	PredictiveVarianceBps2  float64
	EffectiveSamples        float64

	ForecastHorizon  time.Duration
	ExecutionHorizon time.Duration
	AdjustmentPeriod time.Duration
	AlphaHalfLife    time.Duration

	RiskAversion           float64
	OneWayExecutionCostBps float64
	EvidencePriorSamples   float64
}

// DynamicInventoryAimDecision exposes the complete target calculation. Aim is
// the frictionless risk-adjusted target after the fee gate; AdjustedTarget is
// the position that should be reached during the next adjustment period.
type DynamicInventoryAimDecision struct {
	Enabled    bool
	Applied    bool
	ShadowOnly bool
	GatePassed bool
	Reason     string
	GateReason string

	CurrentInventoryRatio float64
	PolicyTargetRatio     float64
	HardMinimumRatio      float64
	HardMaximumRatio      float64
	// EffectiveSamples is copied from the causal path estimator so logs and
	// replay reports expose the evidence actually used by the gate.  The
	// legacy MinimumSamples config field is intentionally not used as an
	// economic threshold; the evaluator only requires EffectiveSamples > 1.
	EffectiveSamples    float64
	AimTargetRatio      float64
	AdjustedTargetRatio float64

	GrossReturnBps           float64
	ShrunkReturnBps          float64
	RiskAdjustedReturnBps    float64
	NetReturnBps             float64
	PredictiveStdDevBps      float64
	ConfidenceLowerBps       float64
	ConfidenceUpperBps       float64
	PositiveReturnProb       float64
	RiskAdjustedPositiveProb float64
	SignalStrength           float64
	AlphaPersistence         float64
	RiskGain                 float64
	AdjustmentFraction       float64
	TargetShiftRatio         float64
}

func dynamicInventoryAimFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clampDynamicInventoryAim(value, lower, upper float64) float64 {
	return math.Max(lower, math.Min(upper, value))
}

// EvaluateDynamicInventoryAim implements a one-dimensional impulse-free
// approximation of the Gârleanu–Pedersen/HJB target:
//
//	max_d  mu*d - gamma*sigma^2*d^2/2 - c*|d|
//
// The fee term creates a mathematical no-trade region (soft threshold), the
// variance term limits the target displacement, and partial adjustment moves
// the actual inventory only a fraction toward the aim. No fixed BPS direction
// threshold or independent BUY/SELL multiplier is used.
func EvaluateDynamicInventoryAim(config DynamicInventoryAimConfig, in DynamicInventoryAimInput) DynamicInventoryAimDecision {
	d := DynamicInventoryAimDecision{
		Reason:                "disabled",
		GateReason:            "disabled",
		CurrentInventoryRatio: in.CurrentInventoryRatio,
		PolicyTargetRatio:     in.PolicyTargetRatio,
		HardMinimumRatio:      in.HardMinimumRatio,
		HardMaximumRatio:      in.HardMaximumRatio,
		EffectiveSamples:      in.EffectiveSamples,
		AimTargetRatio:        in.PolicyTargetRatio,
		AdjustedTargetRatio:   in.PolicyTargetRatio,
	}
	if !config.Enabled {
		return d
	}
	config.setDefaults(2)
	d.Enabled = true
	d.ShadowOnly = config.ShadowOnly
	finite := dynamicInventoryAimFinite
	if !finite(in.CurrentInventoryRatio) || !finite(in.PolicyTargetRatio) ||
		!finite(in.HardMinimumRatio) || !finite(in.HardMaximumRatio) ||
		!finite(in.GrossInventoryReturnBps) || !finite(in.PredictiveVarianceBps2) ||
		!finite(in.EffectiveSamples) || !finite(in.RiskAversion) ||
		!finite(in.OneWayExecutionCostBps) || !finite(in.EvidencePriorSamples) ||
		in.HardMinimumRatio > in.HardMaximumRatio || in.RiskAversion <= 0 ||
		in.PredictiveVarianceBps2 < 0 || in.EffectiveSamples <= 0 ||
		in.OneWayExecutionCostBps < 0 {
		d.Reason, d.GateReason = "invalid dynamic inventory aim input", "invalid input"
		return d
	}
	if in.ForecastHorizon <= 0 || in.ExecutionHorizon <= 0 || in.AdjustmentPeriod <= 0 {
		d.Reason, d.GateReason = "dynamic inventory aim horizon unavailable", "invalid horizon"
		return d
	}
	minimum := clampDynamicInventoryAim(in.HardMinimumRatio, 0, 1)
	maximum := clampDynamicInventoryAim(in.HardMaximumRatio, minimum, 1)
	prior := clampDynamicInventoryAim(in.PolicyTargetRatio, minimum, maximum)
	current := clampDynamicInventoryAim(in.CurrentInventoryRatio, minimum, maximum)
	d.CurrentInventoryRatio, d.PolicyTargetRatio = current, prior
	d.HardMinimumRatio, d.HardMaximumRatio = minimum, maximum
	// A failed evidence gate keeps the strategic target, not the current
	// position. This preserves ordinary two-sided quoting while refusing to
	// make a new inventory bet.
	d.AimTargetRatio, d.AdjustedTargetRatio = prior, prior

	// A fixed count such as six is not an invariant evidence requirement here:
	// the path estimator already discounts overlapping and stale observations,
	// so a long horizon can have fewer than six effective samples even when the
	// raw lookback is fully populated. The remaining floor only prevents a
	// one-observation variance estimate from driving a target shift. Economic
	// admissibility is decided below by the predictive fee-adjusted bound.
	if in.EffectiveSamples <= 1 {
		d.Reason, d.GateReason = "dynamic inventory aim awaits matured evidence", "insufficient samples"
		return d
	}
	priorSamples := in.EvidencePriorSamples
	if priorSamples < 0 || !finite(priorSamples) {
		priorSamples = 1
	}
	// Empirical-Bayes shrinkage prevents a sparse but extreme first estimate
	// from moving the target to a hard boundary.
	d.GrossReturnBps = in.GrossInventoryReturnBps
	d.ShrunkReturnBps = d.GrossReturnBps * in.EffectiveSamples /
		(in.EffectiveSamples + priorSamples)
	d.PredictiveStdDevBps = math.Sqrt(math.Max(0, in.PredictiveVarianceBps2))
	if d.PredictiveStdDevBps > 0 {
		d.PositiveReturnProb = 0.5 * (1 + math.Erf(
			d.ShrunkReturnBps/(d.PredictiveStdDevBps*math.Sqrt2)))
	} else if d.ShrunkReturnBps > 0 {
		d.PositiveReturnProb = 1
	} else if d.ShrunkReturnBps < 0 {
		d.PositiveReturnProb = 0
	} else {
		d.PositiveReturnProb = 0.5
	}
	d.PositiveReturnProb = clampDynamicInventoryAim(d.PositiveReturnProb, 0, 1)

	// A half-life is optional because the selected Fast window itself can be
	// the only validated causal clock. When supplied, it discounts a signal
	// that is expected to decay before the intended execution completes.
	d.AlphaPersistence = 1
	if in.AlphaHalfLife > 0 {
		tau := in.AlphaHalfLife.Seconds()
		execution := in.ExecutionHorizon.Seconds()
		d.AlphaPersistence = tau / (tau + execution)
	}
	d.AlphaPersistence = clampDynamicInventoryAim(d.AlphaPersistence, 0, 1)

	// Compare the current inventory with the policy target before applying the
	// switching cost. For an action d = a-current, the incremental certainty
	// equivalent is
	//
	//   [mu - gamma*sigma^2*(current-policy)]*d
	//       - gamma*sigma^2*d^2/2 - c*|d|.
	//
	// The risk-gradient term is essential: when inventory is far from policy
	// target, de-risking can be optimal even when the directional return alone
	// is smaller than one-way fees. The old implementation gated on |mu| > c,
	// filtering exactly those inventory-management actions.
	riskGradient := d.ShrunkReturnBps -
		in.RiskAversion*in.PredictiveVarianceBps2*(current-prior)
	d.RiskGain = riskGradient - d.ShrunkReturnBps
	d.RiskAdjustedReturnBps = riskGradient
	d.ConfidenceLowerBps = riskGradient - config.ConfidenceZScore*d.PredictiveStdDevBps
	d.ConfidenceUpperBps = riskGradient + config.ConfidenceZScore*d.PredictiveStdDevBps
	if d.PredictiveStdDevBps > 0 {
		d.RiskAdjustedPositiveProb = 0.5 * (1 + math.Erf(
			riskGradient/(d.PredictiveStdDevBps*math.Sqrt2)))
	} else if riskGradient > 0 {
		d.RiskAdjustedPositiveProb = 1
	} else if riskGradient < 0 {
		d.RiskAdjustedPositiveProb = 0
	} else {
		d.RiskAdjustedPositiveProb = 0.5
	}
	d.RiskAdjustedPositiveProb = clampDynamicInventoryAim(d.RiskAdjustedPositiveProb, 0, 1)
	d.SignalStrength = clampDynamicInventoryAim(2*d.RiskAdjustedPositiveProb-1, -1, 1)

	// The one-way fee/adverse-selection budget is an L1 switching cost. A
	// positive risk-adjusted signal must clear cost at the one-sided predictive
	// lower bound; a negative signal must clear it at the corresponding upper
	// bound. High volatility therefore remains conservative, while inventory
	// risk is no longer discarded before it reaches the bound.
	net := math.Copysign(math.Max(0, math.Abs(riskGradient)-in.OneWayExecutionCostBps), riskGradient)
	d.NetReturnBps = net
	confidenceFeeCleared := false
	if riskGradient > 0 {
		confidenceFeeCleared = d.ConfidenceLowerBps > in.OneWayExecutionCostBps
	} else if riskGradient < 0 {
		confidenceFeeCleared = d.ConfidenceUpperBps < -in.OneWayExecutionCostBps
	}
	if !confidenceFeeCleared {
		d.Reason = "dynamic inventory aim lacks a fee-positive predictive bound"
		d.GateReason = "confidence-adjusted fee gate"
		return d
	}

	variance := math.Max(1e-12, in.PredictiveVarianceBps2) / (10_000 * 10_000)
	mean := net / 10_000 * d.AlphaPersistence
	// The unconstrained shift is the quadratic-utility optimum after the
	// absolute switching-cost threshold. Hard bounds are applied exactly once.
	shift := mean / (in.RiskAversion * variance)
	if !finite(shift) {
		d.Reason, d.GateReason = "dynamic inventory aim is numerically unstable", "non-finite shift"
		return d
	}
	shift = clampDynamicInventoryAim(shift, minimum-current, maximum-current)
	d.TargetShiftRatio = shift
	d.AimTargetRatio = clampDynamicInventoryAim(current+shift, minimum, maximum)

	// Partial adjustment is the execution-speed component. A short adjustment
	// period relative to the execution horizon permits a large first move when
	// the aim is far away; later moves shrink as the inventory gap closes.
	d.AdjustmentFraction = 1 - math.Exp(-in.AdjustmentPeriod.Seconds()/in.ExecutionHorizon.Seconds())
	d.AdjustmentFraction = clampDynamicInventoryAim(d.AdjustmentFraction, 0, 1)
	d.AdjustedTargetRatio = clampDynamicInventoryAim(
		current+d.AdjustmentFraction*(d.AimTargetRatio-current), minimum, maximum)
	d.GatePassed = true
	d.Applied = !config.ShadowOnly
	d.Reason = "dynamic inventory aim passed fee/risk/capacity gates"
	d.GateReason = "fee-cleared quadratic target with partial adjustment"
	return d
}
