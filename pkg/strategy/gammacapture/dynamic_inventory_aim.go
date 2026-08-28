package gammacapture

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// DynamicInventoryAimConfig enables the single target/speed controller.  It
// is intentionally a quantity/inventory component: it must not own quote
// distance, order submission, or an independent side gate.
type DynamicInventoryAimConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	ShadowOnly bool `json:"shadowOnly" yaml:"shadowOnly"`
	// PriceBetaTarget is an optional cap on marked spot inventory beta,
	// approximately inventory*mid/equity. It is applied only to the existing
	// inventory target actuator; zero keeps the current policy unchanged.
	PriceBetaTarget float64 `json:"priceBetaTarget" yaml:"priceBetaTarget"`
	// RegimeConditionedTarget is the single bounded regime-to-target adapter.
	// When enabled it replaces the legacy directional aim calculation inside
	// this actuator; it owns no quote, quantity, or side admission policy.
	RegimeConditionedTarget RegimeConditionedTargetConfig `json:"regimeConditionedTarget" yaml:"regimeConditionedTarget"`
	// PivotRegimeTarget is the pivot-first replacement for the ML-tag/bucket
	// target path. It consumes causal directional-change state and emits only a
	// bounded, fee-aware target shift. When enabled it takes precedence over
	// RegimeConditionedTarget so two regime actuators cannot stack.
	PivotRegimeTarget PivotRegimeTargetConfig `json:"pivotRegimeTarget" yaml:"pivotRegimeTarget"`
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
	c.RegimeConditionedTarget.setDefaults()
	c.PivotRegimeTarget.setDefaults()
}

// PivotRegimeTargetConfig wires the causal pivot filter to the inventory
// target actuator. The legacy +/-20% actuator and the causal CE owner are
// separate switches so the former cannot silently constrain the latter.
type PivotRegimeTargetConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// CausalCEEnabled is the causal pivot-regime CE target owner. It uses the
	// pivot state with a 50% soft prior and the configured 0..100% hard capital
	// band. It is separate from Enabled so the former +/-20% actuator cannot
	// be silently reused.
	CausalCEEnabled        bool           `json:"causalCEEnabled" yaml:"causalCEEnabled"`
	CausalRiskAversion     float64        `json:"causalRiskAversion" yaml:"causalRiskAversion"`
	CausalPriorStrengthBps float64        `json:"causalPriorStrengthBps" yaml:"causalPriorStrengthBps"`
	ReversalBps            float64        `json:"reversalBps" yaml:"reversalBps"`
	MaxGap                 types.Duration `json:"maxGap" yaml:"maxGap"`
	// StartupWarmup is the causal BBO history needed to observe enough
	// completed same-direction legs for the CE owner. It is separate from the
	// shorter Fast horizon because a recent checkpoint may otherwise preserve
	// a healthy quote model while leaving the pivot target statistically cold.
	StartupWarmup   types.Duration `json:"startupWarmup" yaml:"startupWarmup"`
	MinLegSamples   int            `json:"minLegSamples" yaml:"minLegSamples"`
	PriorLegSamples float64        `json:"priorLegSamples" yaml:"priorLegSamples"`
	MaxShiftRatio   float64        `json:"maxShiftRatio" yaml:"maxShiftRatio"`
	RiskPenaltyBps  float64        `json:"riskPenaltyBps" yaml:"riskPenaltyBps"`
}

func (c *PivotRegimeTargetConfig) setDefaults() {
	if c.ReversalBps <= 0 || !dynamicInventoryAimFinite(c.ReversalBps) {
		c.ReversalBps = 26
	}
	if c.MaxGap <= 0 {
		c.MaxGap = types.Duration(15 * time.Minute)
	}
	if c.StartupWarmup <= 0 {
		c.StartupWarmup = types.Duration(24 * time.Hour)
	}
	if c.MinLegSamples <= 0 {
		c.MinLegSamples = 2
	}
	if c.PriorLegSamples <= 0 || !dynamicInventoryAimFinite(c.PriorLegSamples) {
		c.PriorLegSamples = 2
	}
	if c.MaxShiftRatio <= 0 || !dynamicInventoryAimFinite(c.MaxShiftRatio) {
		c.MaxShiftRatio = 0.20
	}
	if c.MaxShiftRatio > 1 {
		c.MaxShiftRatio = 1
	}
	if c.RiskPenaltyBps < 0 || !dynamicInventoryAimFinite(c.RiskPenaltyBps) {
		c.RiskPenaltyBps = 0
	}
}

func (c PivotRegimeTargetConfig) filterConfig() PivotRegimeConfig {
	return PivotRegimeConfig{
		ReversalBps: c.ReversalBps,
		MaxGap:      time.Duration(c.MaxGap), MinLegSamples: c.MinLegSamples,
		PriorLegSamples: c.PriorLegSamples,
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

	// RegimeConditioned is causal Fast/BOCPD/horizon posterior evidence. It is
	// consumed only when Config.RegimeConditionedTarget.Enabled is true.
	RegimeConditioned RegimeConditionedTargetInput
	// A supplied decision is the bucketed sample-and-hold posterior produced by
	// RefreshRegimeConditionedTargetDecision. It avoids treating every BBO tick
	// as an independent target observation.
	RegimeConditionedDecision         RegimeConditionedTargetDecision
	RegimeConditionedDecisionSupplied bool

	// PivotRegime is a causal directional-change decision observed on every
	// valid BBO event. It is consumed only when Config.PivotRegimeTarget.Enabled
	// is true; unlike the legacy regime path it is never sample-and-held by a
	// time bucket.
	PivotRegimeDecision         PivotRegimeDecision
	PivotRegimeDecisionSupplied bool
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

	GrossReturnBps  float64
	ShrunkReturnBps float64
	// ExecutionReturnBps is the directional, executable return forecast that
	// may be consumed by Fast crossing. NetReturnBps is deliberately different:
	// it contains the inventory-risk gradient used to move the target and must
	// never be interpreted as a price markout.
	ExecutionReturnBps       float64
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

	RegimeConditionedEnabled      bool
	RegimeConditionedPosterior    float64
	RegimeConditionedScore        float64
	RegimeConditionedReliability  float64
	RegimeConditionedSamples      float64
	RegimeConditionedSampleShrink float64
	RegimeConditionedSignalCount  int

	PivotRegimeEnabled       bool
	PivotRegimeReady         bool
	PivotRegimeHealthy       bool
	PivotRegimeDirection     int
	PivotRegimeRemainingBps  float64
	PivotRegimeExpectedBps   float64
	PivotRegimeQuantityScale float64

	PriceBetaTarget     float64
	PriceBetaCapApplied bool
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
	if config.PivotRegimeTarget.Enabled {
		return evaluatePivotRegimeInventoryAim(config, in)
	}
	if config.RegimeConditionedTarget.Enabled {
		return evaluateRegimeConditionedInventoryAim(config, in)
	}
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
	if config.PriceBetaTarget > 0 && dynamicInventoryAimFinite(config.PriceBetaTarget) {
		betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
		if prior > betaCap {
			d.AimTargetRatio, d.AdjustedTargetRatio = betaCap, betaCap
			d.PriceBetaCapApplied = true
		}
		d.PriceBetaTarget = betaCap
	}

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
	// Keep the risk-adjusted gradient out of the execution-price model. The
	// empirical-Bayes-shrunk return is the only directional price forecast that
	// this component is allowed to export to Fast execution.
	d.ExecutionReturnBps = d.ShrunkReturnBps
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
	if config.PriceBetaTarget > 0 && dynamicInventoryAimFinite(config.PriceBetaTarget) {
		betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
		if d.AimTargetRatio > betaCap {
			d.AimTargetRatio = betaCap
			d.PriceBetaCapApplied = true
		}
		d.PriceBetaTarget = betaCap
	}

	// Partial adjustment is the execution-speed component. A short adjustment
	// period relative to the execution horizon permits a large first move when
	// the aim is far away; later moves shrink as the inventory gap closes.
	d.AdjustmentFraction = 1 - math.Exp(-in.AdjustmentPeriod.Seconds()/in.ExecutionHorizon.Seconds())
	d.AdjustmentFraction = clampDynamicInventoryAim(d.AdjustmentFraction, 0, 1)
	d.AdjustedTargetRatio = clampDynamicInventoryAim(
		current+d.AdjustmentFraction*(d.AimTargetRatio-current), minimum, maximum)
	if config.PriceBetaTarget > 0 && dynamicInventoryAimFinite(config.PriceBetaTarget) {
		betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
		if d.AdjustedTargetRatio > betaCap {
			d.AdjustedTargetRatio = betaCap
			d.PriceBetaCapApplied = true
		}
		d.PriceBetaTarget = betaCap
	}
	d.GatePassed = true
	d.Applied = !config.ShadowOnly
	d.Reason = "dynamic inventory aim passed fee/risk/capacity gates"
	d.GateReason = "fee-cleared quadratic target with partial adjustment"
	return d
}

// evaluatePivotRegimeInventoryAim maps pivot geometry into the same target
// actuator used by the rest of the strategy. The pivot model is deliberately
// not exported as an execution-price forecast: its remaining-leg estimate is
// used for continuous inventory sizing only. This prevents a single noisy
// pivot tag from creating a second Fast crossing signal.
func evaluatePivotRegimeInventoryAim(config DynamicInventoryAimConfig, in DynamicInventoryAimInput) DynamicInventoryAimDecision {
	d := DynamicInventoryAimDecision{
		Enabled:               true,
		ShadowOnly:            config.ShadowOnly,
		Reason:                "pivot regime target has no usable evidence",
		GateReason:            "pivot evidence unavailable",
		CurrentInventoryRatio: in.CurrentInventoryRatio,
		PolicyTargetRatio:     in.PolicyTargetRatio,
		HardMinimumRatio:      in.HardMinimumRatio,
		HardMaximumRatio:      in.HardMaximumRatio,
		AimTargetRatio:        in.PolicyTargetRatio,
		AdjustedTargetRatio:   in.PolicyTargetRatio,
		PivotRegimeEnabled:    true,
	}
	finite := dynamicInventoryAimFinite
	if !finite(in.CurrentInventoryRatio) || !finite(in.PolicyTargetRatio) ||
		!finite(in.HardMinimumRatio) || !finite(in.HardMaximumRatio) ||
		!finite(in.OneWayExecutionCostBps) || in.OneWayExecutionCostBps < 0 ||
		in.HardMinimumRatio > in.HardMaximumRatio ||
		in.ExecutionHorizon <= 0 || in.AdjustmentPeriod <= 0 {
		d.Reason, d.GateReason = "invalid pivot regime target input", "invalid input"
		return d
	}
	minimum := clampDynamicInventoryAim(in.HardMinimumRatio, 0, 1)
	maximum := clampDynamicInventoryAim(in.HardMaximumRatio, minimum, 1)
	current := clampDynamicInventoryAim(in.CurrentInventoryRatio, minimum, maximum)
	policy := clampDynamicInventoryAim(in.PolicyTargetRatio, minimum, maximum)
	d.CurrentInventoryRatio, d.PolicyTargetRatio = current, policy
	d.HardMinimumRatio, d.HardMaximumRatio = minimum, maximum
	d.AimTargetRatio, d.AdjustedTargetRatio = policy, policy
	if config.PriceBetaTarget > 0 && finite(config.PriceBetaTarget) {
		betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
		if policy > betaCap {
			d.AimTargetRatio, d.AdjustedTargetRatio = betaCap, betaCap
			d.PriceBetaCapApplied = true
		}
		d.PriceBetaTarget = betaCap
	}

	pivot := in.PivotRegimeDecision
	d.PivotRegimeReady = pivot.Ready
	d.PivotRegimeHealthy = pivot.Healthy
	d.PivotRegimeDirection = pivot.Direction
	d.PivotRegimeRemainingBps = pivot.RemainingAmplitudeBps
	d.PivotRegimeExpectedBps = pivot.ExpectedLegAmplitudeBps
	d.EffectiveSamples = float64(pivot.CompletedLegSamples)
	d.PivotRegimeQuantityScale = pivot.Reliability
	if !in.PivotRegimeDecisionSupplied {
		d.Reason, d.GateReason = "pivot regime decision was not supplied", "missing causal pivot state"
		return d
	}
	if !pivot.Ready {
		if pivot.Reason != "" {
			d.Reason = pivot.Reason
		}
		return d
	}

	sizing := EvaluatePivotRegimeSizing(PivotRegimeSizingInput{
		Decision: pivot, CostBps: in.OneWayExecutionCostBps,
		RiskPenaltyBps: config.PivotRegimeTarget.RiskPenaltyBps,
		MaxTargetShift: config.PivotRegimeTarget.MaxShiftRatio,
		TargetScaleBps: pivot.ExpectedLegAmplitudeBps,
	})
	d.PivotRegimeQuantityScale = sizing.QuantityScale
	d.NetReturnBps = float64(sizing.Direction) * math.Max(0, sizing.NetRemainingBps)
	d.RiskAdjustedReturnBps = d.NetReturnBps
	d.SignalStrength = sizing.SignedQuantityScale
	d.AlphaPersistence = clampDynamicInventoryAim(pivot.Reliability, 0, 1)
	// A pivot remaining-leg estimate is not calibrated tightly enough to be an
	// executable Fast price forecast. Keep this field zero so the pivot target
	// cannot manufacture IOC crossing budgets downstream.
	d.ExecutionReturnBps = 0
	if !sizing.Applied {
		d.Reason, d.GateReason = sizing.Reason, "continuous fee/risk sizing"
		return d
	}
	d.TargetShiftRatio = sizing.TargetShiftRatio
	d.AimTargetRatio = clampDynamicInventoryAim(policy+sizing.TargetShiftRatio, minimum, maximum)
	if config.PriceBetaTarget > 0 && finite(config.PriceBetaTarget) {
		betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
		if d.AimTargetRatio > betaCap {
			d.AimTargetRatio = betaCap
			d.PriceBetaCapApplied = true
		}
		d.PriceBetaTarget = betaCap
	}
	d.AdjustmentFraction = 1 - math.Exp(-in.AdjustmentPeriod.Seconds()/in.ExecutionHorizon.Seconds())
	d.AdjustmentFraction = clampDynamicInventoryAim(d.AdjustmentFraction, 0, 1)
	d.AdjustedTargetRatio = clampDynamicInventoryAim(
		current+d.AdjustmentFraction*(d.AimTargetRatio-current), minimum, maximum)
	if config.PriceBetaTarget > 0 && finite(config.PriceBetaTarget) {
		betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
		if d.AdjustedTargetRatio > betaCap {
			d.AdjustedTargetRatio = betaCap
			d.PriceBetaCapApplied = true
		}
		d.PriceBetaTarget = betaCap
	}
	d.GatePassed = true
	d.Applied = !config.ShadowOnly
	d.Reason = "pivot remaining amplitude passed fee/risk bounds"
	d.GateReason = "continuous pivot sizing with one-way cost and hard inventory bounds"
	return d
}

// evaluateRegimeConditionedInventoryAim maps one bounded regime posterior to
// the existing target actuator.  It intentionally bypasses the legacy
// directional-return target formula when enabled: otherwise path return,
// Fast direction, and BOCPD would each shift the target independently.  The
// terminal-wealth optimizer remains the only downstream order-admission gate.
func evaluateRegimeConditionedInventoryAim(config DynamicInventoryAimConfig, in DynamicInventoryAimInput) DynamicInventoryAimDecision {
	d := DynamicInventoryAimDecision{
		Enabled:                  true,
		ShadowOnly:               config.ShadowOnly,
		Reason:                   "regime-conditioned target has no usable evidence",
		GateReason:               "regime evidence unavailable",
		CurrentInventoryRatio:    in.CurrentInventoryRatio,
		PolicyTargetRatio:        in.PolicyTargetRatio,
		HardMinimumRatio:         in.HardMinimumRatio,
		HardMaximumRatio:         in.HardMaximumRatio,
		AimTargetRatio:           in.PolicyTargetRatio,
		AdjustedTargetRatio:      in.PolicyTargetRatio,
		RegimeConditionedEnabled: true,
	}
	finite := dynamicInventoryAimFinite
	if !finite(in.CurrentInventoryRatio) || !finite(in.PolicyTargetRatio) ||
		!finite(in.HardMinimumRatio) || !finite(in.HardMaximumRatio) ||
		in.HardMinimumRatio > in.HardMaximumRatio || in.AdjustmentPeriod <= 0 ||
		in.ExecutionHorizon <= 0 {
		d.Reason, d.GateReason = "invalid regime-conditioned target input", "invalid input"
		return d
	}
	minimum := clampDynamicInventoryAim(in.HardMinimumRatio, 0, 1)
	maximum := clampDynamicInventoryAim(in.HardMaximumRatio, minimum, 1)
	current := clampDynamicInventoryAim(in.CurrentInventoryRatio, minimum, maximum)
	policy := clampDynamicInventoryAim(in.PolicyTargetRatio, minimum, maximum)
	d.CurrentInventoryRatio, d.PolicyTargetRatio = current, policy
	d.HardMinimumRatio, d.HardMaximumRatio = minimum, maximum
	if config.PriceBetaTarget > 0 && dynamicInventoryAimFinite(config.PriceBetaTarget) {
		betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
		if policy > betaCap {
			d.AimTargetRatio, d.AdjustedTargetRatio = betaCap, betaCap
			d.PriceBetaCapApplied = true
		}
		d.PriceBetaTarget = betaCap
	}

	regime := in.RegimeConditionedDecision
	if !in.RegimeConditionedDecisionSupplied {
		regime = EvaluateRegimeConditionedTarget(
			config.RegimeConditionedTarget,
			in.RegimeConditioned,
		)
	}
	regime = ApplyRegimeConditionedTarget(
		config.RegimeConditionedTarget,
		regime, policy, minimum, maximum,
	)
	d.RegimeConditionedSignalCount = regime.SignalCount
	d.EffectiveSamples = regime.EffectiveSamples
	// The regime posterior supplies direction and a bounded prior shift. It is
	// not, by itself, an economic reason to carry more inventory. Reuse the
	// terminal-path risk gradient and fee-adjusted confidence bound from the
	// ordinary target actuator before allowing the posterior to move the target.
	// This prevents a calibrated classification signal from becoming an
	// unpriced directional inventory bet.
	d.GrossReturnBps = in.GrossInventoryReturnBps
	d.PredictiveStdDevBps = math.Sqrt(math.Max(0, in.PredictiveVarianceBps2))
	if dynamicInventoryAimFinite(in.RiskAversion) && in.RiskAversion > 0 &&
		dynamicInventoryAimFinite(in.PredictiveVarianceBps2) && in.PredictiveVarianceBps2 >= 0 &&
		dynamicInventoryAimFinite(in.GrossInventoryReturnBps) &&
		dynamicInventoryAimFinite(in.OneWayExecutionCostBps) && in.OneWayExecutionCostBps >= 0 {
		riskGradient := in.GrossInventoryReturnBps -
			in.RiskAversion*in.PredictiveVarianceBps2*(current-policy)
		d.RiskAdjustedReturnBps = riskGradient
		d.ConfidenceLowerBps = riskGradient - configConfidenceZ(config)*d.PredictiveStdDevBps
		d.ConfidenceUpperBps = riskGradient + configConfidenceZ(config)*d.PredictiveStdDevBps
		d.NetReturnBps = math.Copysign(math.Max(0, math.Abs(riskGradient)-in.OneWayExecutionCostBps), riskGradient)
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
		if regime.TargetShiftRatio > 0 {
			if d.ConfidenceLowerBps <= in.OneWayExecutionCostBps {
				regime.Ready = false
				regime.Reason = "bullish target shift lacks a fee-positive predictive bound"
			}
		} else if regime.TargetShiftRatio < 0 {
			if d.ConfidenceUpperBps >= -in.OneWayExecutionCostBps {
				regime.Ready = false
				regime.Reason = "bearish target shift lacks a fee-positive predictive bound"
			}
		}
	} else {
		regime.Ready = false
		regime.Reason = "regime target awaits finite terminal-path risk evidence"
	}
	d.RegimeConditionedPosterior = regime.CombinedPosterior
	d.RegimeConditionedScore = regime.DirectionScore
	d.RegimeConditionedReliability = regime.Reliability
	d.RegimeConditionedSamples = regime.EffectiveSamples
	d.RegimeConditionedSampleShrink = regime.SampleShrink
	// Preserve the path variance as an inventory-risk diagnostic, but do not
	// export the old path return as an independent execution-price forecast.
	// The regime posterior is the sole target signal in this mode.
	if in.PredictiveVarianceBps2 > 0 && dynamicInventoryAimFinite(in.PredictiveVarianceBps2) {
		d.PredictiveStdDevBps = math.Sqrt(in.PredictiveVarianceBps2)
	}
	d.AimTargetRatio = regime.TargetRatio
	d.TargetShiftRatio = regime.TargetShiftRatio
	d.PositiveReturnProb = regime.CombinedPosterior
	d.AlphaPersistence = regime.SampleShrink
	if !regime.Ready || math.Abs(regime.TargetShiftRatio) <= 1e-12 {
		d.AimTargetRatio = policy
		d.AdjustedTargetRatio = policy
		if config.PriceBetaTarget > 0 && dynamicInventoryAimFinite(config.PriceBetaTarget) {
			betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
			if d.AimTargetRatio > betaCap {
				d.AimTargetRatio, d.AdjustedTargetRatio = betaCap, betaCap
				d.PriceBetaCapApplied = true
			}
			d.PriceBetaTarget = betaCap
		}
		if regime.Ready {
			d.Reason = "regime posterior is neutral; retaining policy target"
			d.GateReason = "bounded posterior has zero target shift"
		}
		return d
	}

	d.AdjustmentFraction = 1 - math.Exp(-in.AdjustmentPeriod.Seconds()/in.ExecutionHorizon.Seconds())
	d.AdjustmentFraction = clampDynamicInventoryAim(d.AdjustmentFraction, 0, 1)
	d.AdjustedTargetRatio = clampDynamicInventoryAim(
		current+d.AdjustmentFraction*(d.AimTargetRatio-current), minimum, maximum)
	if config.PriceBetaTarget > 0 && dynamicInventoryAimFinite(config.PriceBetaTarget) {
		betaCap := clampDynamicInventoryAim(config.PriceBetaTarget, minimum, maximum)
		if d.AimTargetRatio > betaCap {
			d.AimTargetRatio = betaCap
			d.PriceBetaCapApplied = true
		}
		if d.AdjustedTargetRatio > betaCap {
			d.AdjustedTargetRatio = betaCap
			d.PriceBetaCapApplied = true
		}
		d.PriceBetaTarget = betaCap
	}
	d.GatePassed = true
	d.Applied = !config.ShadowOnly
	d.Reason = "bounded regime posterior target passed evidence and terminal risk bounds"
	d.GateReason = "single clipped regime shift after fee-adjusted terminal-path gate"
	return d
}

func configConfidenceZ(config DynamicInventoryAimConfig) float64 {
	// The target sub-config intentionally has no second confidence parameter;
	// use the same one-sided critical value as the parent inventory actuator.
	if config.ConfidenceZScore > 0 && dynamicInventoryAimFinite(config.ConfidenceZScore) {
		return config.ConfidenceZScore
	}
	return 1.645
}
