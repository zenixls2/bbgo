package gammacapture

import "math"

// DynamicPriceBetaTargetConfig controls the research-only state-dependent
// marked-inventory cap. It is deliberately a target actuator input: it does
// not own quote distance, quantity, crossing, or order admission.
type DynamicPriceBetaTargetConfig struct {
	Enabled bool

	// ChaseTarget is the cap used when the account is already overweight and
	// the executable inventory-return forecast is strongly positive. A lower
	// value reduces the tendency to chase a continuing move with inventory.
	ChaseTarget float64
	// ChaseZStart and ChaseZFull define the predeclared evidence ramp. Below
	// ChaseZStart the component is inactive; at ChaseZFull it reaches
	// ChaseTarget.
	ChaseZStart float64
	ChaseZFull  float64
	// PriorEffectiveSamples shrinks sparse return forecasts before the z-score
	// is formed. It is not a production sample gate.
	PriorEffectiveSamples float64
}

// DynamicPriceBetaTargetInput is causal state already available to
// DynamicInventoryAim. GrossInventoryReturnBps is the same-symbol
// executable-BBO inventory-return forecast for the frozen Fast horizon.
type DynamicPriceBetaTargetInput struct {
	CurrentInventoryRatio float64
	PolicyTargetRatio     float64
	HardMinimumRatio      float64
	HardMaximumRatio      float64

	GrossInventoryReturnBps float64
	PredictiveVarianceBps2  float64
	EffectiveSamples        float64
}

// DynamicPriceBetaTargetDecision is the one scalar that may be connected to
// DynamicInventoryAimConfig.PriceBetaTarget after this alpha passes screening.
// PriceBetaTarget==0 means no cap, preserving the null policy.
type DynamicPriceBetaTargetDecision struct {
	Ready  bool
	Active bool
	Reason string
	Target float64

	ShrunkReturnBps float64
	ZScore          float64
	TrendScore      float64
	OverweightScore float64
	ChaseScore      float64
	RangeScore      float64
}

func dynamicPriceBetaTargetFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clampDynamicPriceBetaTarget(value, lower, upper float64) float64 {
	return math.Max(lower, math.Min(upper, value))
}

func normalizeDynamicPriceBetaTargetConfig(config DynamicPriceBetaTargetConfig) DynamicPriceBetaTargetConfig {
	if config.ChaseTarget <= 0 || !dynamicPriceBetaTargetFinite(config.ChaseTarget) {
		config.ChaseTarget = 0.20
	}
	config.ChaseTarget = clampDynamicPriceBetaTarget(config.ChaseTarget, 0, 1)
	if config.ChaseZStart <= 0 || !dynamicPriceBetaTargetFinite(config.ChaseZStart) {
		config.ChaseZStart = 1
	}
	if config.ChaseZFull <= config.ChaseZStart || !dynamicPriceBetaTargetFinite(config.ChaseZFull) {
		config.ChaseZFull = config.ChaseZStart + 1
	}
	if config.PriorEffectiveSamples < 0 || !dynamicPriceBetaTargetFinite(config.PriorEffectiveSamples) {
		config.PriorEffectiveSamples = 1
	}
	return config
}

// EvaluateDynamicPriceBetaTarget implements a state-dependent beta cap:
//
//	chase = trendEvidence * overweightInventory
//	cap   = hardMaximum - chase*(hardMaximum - ChaseTarget)
//
// Trend evidence is intentionally one-sided. A positive forecast only caps
// an already overweight long inventory; a negative forecast is left to the
// existing fee/risk gradient, and a below-target inventory is not treated as
// "chasing". This avoids turning every range or decline into a low fixed
// target and keeps the hypothesis isolated to the chase-risk state.
func EvaluateDynamicPriceBetaTarget(config DynamicPriceBetaTargetConfig, in DynamicPriceBetaTargetInput) DynamicPriceBetaTargetDecision {
	decision := DynamicPriceBetaTargetDecision{Reason: "dynamic price-beta cap is disabled"}
	if !config.Enabled {
		return decision
	}
	config = normalizeDynamicPriceBetaTargetConfig(config)
	finite := dynamicPriceBetaTargetFinite
	if !finite(in.CurrentInventoryRatio) || !finite(in.PolicyTargetRatio) ||
		!finite(in.HardMinimumRatio) || !finite(in.HardMaximumRatio) ||
		!finite(in.GrossInventoryReturnBps) || !finite(in.PredictiveVarianceBps2) ||
		!finite(in.EffectiveSamples) || in.EffectiveSamples <= 1 ||
		in.PredictiveVarianceBps2 < 0 || in.HardMinimumRatio > in.HardMaximumRatio {
		decision.Reason = "dynamic price-beta cap has invalid or immature evidence"
		return decision
	}

	minimum := clampDynamicPriceBetaTarget(in.HardMinimumRatio, 0, 1)
	maximum := clampDynamicPriceBetaTarget(in.HardMaximumRatio, minimum, 1)
	policy := clampDynamicPriceBetaTarget(in.PolicyTargetRatio, minimum, maximum)
	current := clampDynamicPriceBetaTarget(in.CurrentInventoryRatio, minimum, maximum)
	prior := config.PriorEffectiveSamples
	shrunk := in.GrossInventoryReturnBps * in.EffectiveSamples / (in.EffectiveSamples + prior)
	std := math.Sqrt(math.Max(0, in.PredictiveVarianceBps2))
	decision.ShrunkReturnBps = shrunk
	if std > 0 {
		decision.ZScore = shrunk / std
	} else if shrunk > 0 {
		// Keep the output finite while treating a deterministic positive
		// forecast as stronger than the full chase threshold.
		decision.ZScore = config.ChaseZFull
	} else if shrunk < 0 {
		decision.ZScore = -config.ChaseZFull
	}
	if !finite(decision.ZScore) {
		decision.Reason = "dynamic price-beta cap has non-finite evidence"
		return decision
	}

	decision.TrendScore = clampDynamicPriceBetaTarget(
		(decision.ZScore-config.ChaseZStart)/(config.ChaseZFull-config.ChaseZStart), 0, 1)
	if maximum > policy {
		decision.OverweightScore = clampDynamicPriceBetaTarget(
			(current-policy)/(maximum-policy), 0, 1)
	}
	decision.ChaseScore = decision.TrendScore * decision.OverweightScore
	decision.RangeScore = clampDynamicPriceBetaTarget(
		1-math.Abs(decision.ZScore)/config.ChaseZStart, 0, 1)
	decision.Ready = true
	if decision.ChaseScore <= 0 {
		switch {
		case decision.ZScore <= 0:
			decision.Reason = "negative or neutral forecast delegated to economic risk gradient"
		case decision.OverweightScore <= 0:
			decision.Reason = "inventory is not overweight relative to policy target"
		default:
			decision.Reason = "positive forecast is below the chase threshold"
		}
		return decision
	}

	decision.Target = clampDynamicPriceBetaTarget(
		maximum-decision.ChaseScore*(maximum-config.ChaseTarget), minimum, maximum)
	decision.Active = decision.Target < maximum
	decision.Reason = "state-dependent chase beta cap is active"
	return decision
}
