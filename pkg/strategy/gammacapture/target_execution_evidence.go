package gammacapture

import "math"

// FastTargetExecutionEvidence is the execution-facing forecast attached to
// whichever component owns the currently selected inventory target. It is
// deliberately separate from PosteriorInventoryTargetDecision: IOC is an
// execution actuator and must not depend on one particular target owner.
//
// A target owner may be able to move a strategic target without exposing a
// price forecast suitable for crossing. In that case Ready is false and the
// ordinary maker/rebalancing path remains responsible for execution.
type FastTargetExecutionEvidence struct {
	Ready  bool
	Source string
	Reason string

	InventoryReturnMeanBps   float64
	InventoryReturnSEBps     float64
	InventoryPredictiveSDBps float64
	DirectionConfidence      float64
	EffectiveSamples         float64
}

// BuildFastTargetExecutionEvidence selects evidence from the target owner in
// precedence order. Causal pivot CE owns production when applied, followed by
// the dynamic target actuator, then the legacy posterior target. The source
// of the target and the source of the executable forecast must remain aligned;
// otherwise IOC could evaluate a stale forecast against a newly shifted target.
func BuildFastTargetExecutionEvidence(
	causalApplied bool,
	causal CausalRegimeInventoryTargetDecision,
	dynamic DynamicInventoryAimDecision,
	posterior PosteriorInventoryTargetDecision,
) FastTargetExecutionEvidence {
	if causalApplied {
		if !causal.Ready {
			return FastTargetExecutionEvidence{
				Source: "causal-pivot-ce",
				Reason: "causal pivot target is not ready",
			}
		}
		return newFastTargetExecutionEvidence(
			"causal-pivot-ce", causal.Reason,
			causal.ShrunkExpectedReturnBps, causal.ExpectedReturnSEBps,
			math.Sqrt(math.Max(0, causal.PredictiveVarianceBps2)),
			causal.DirectionConfidence, causal.EffectiveSamples)
	}

	if dynamic.Applied && dynamic.GatePassed {
		// The legacy pivot actuator explicitly refuses to export an executable
		// price forecast. It can still own a target, but IOC must not turn its
		// geometry into an uncalibrated taker bet.
		if dynamic.PivotRegimeEnabled {
			return FastTargetExecutionEvidence{
				Source: "dynamic-pivot-target",
				Reason: "dynamic pivot target has no executable price forecast",
			}
		}
		return newFastTargetExecutionEvidence(
			"dynamic-inventory-aim", dynamic.Reason,
			dynamic.ExecutionReturnBps,
			dynamicReturnSEBps(dynamic.PredictiveStdDevBps, dynamic.EffectiveSamples),
			dynamic.PredictiveStdDevBps, dynamic.SignalStrength,
			dynamic.EffectiveSamples)
	}

	if posterior.Enabled {
		return newFastTargetExecutionEvidence(
			"posterior-inventory-target", posterior.Reason,
			posterior.InventoryReturnMean, posterior.InventoryReturnSE,
			posterior.InventoryPredictiveSD, posterior.DirectionConfidence,
			posterior.EffectiveSamples)
	}

	return FastTargetExecutionEvidence{
		Source: "none",
		Reason: "selected inventory target has no executable price forecast",
	}
}

func newFastTargetExecutionEvidence(
	source, reason string, mean, standardError, predictiveSD,
	directionConfidence, effectiveSamples float64,
) FastTargetExecutionEvidence {
	evidence := FastTargetExecutionEvidence{
		Source:                   source,
		Reason:                   reason,
		InventoryReturnMeanBps:   mean,
		InventoryReturnSEBps:     standardError,
		InventoryPredictiveSDBps: predictiveSD,
		DirectionConfidence:      directionConfidence,
		EffectiveSamples:         effectiveSamples,
	}
	if !finiteFastTargetExecutionEvidence(mean) ||
		!finiteFastTargetExecutionEvidence(standardError) || standardError < 0 ||
		!finiteFastTargetExecutionEvidence(predictiveSD) || predictiveSD < 0 ||
		!finiteFastTargetExecutionEvidence(directionConfidence) ||
		math.Abs(directionConfidence) > 1 ||
		!finiteFastTargetExecutionEvidence(effectiveSamples) || effectiveSamples <= 0 {
		evidence.Ready = false
		if reason == "" {
			evidence.Reason = "selected target evidence is incomplete"
		}
		return evidence
	}
	evidence.Ready = true
	return evidence
}

func dynamicReturnSEBps(predictiveSD, effectiveSamples float64) float64 {
	if predictiveSD <= 0 || effectiveSamples <= 0 ||
		!finiteFastTargetExecutionEvidence(predictiveSD) ||
		!finiteFastTargetExecutionEvidence(effectiveSamples) {
		return 0
	}
	// The dynamic target stores predictive variance = sample variance * (1+1/N).
	// Therefore the standard error of the mean is predictiveSD/sqrt(N+1).
	return predictiveSD / math.Sqrt(effectiveSamples+1)
}

func finiteFastTargetExecutionEvidence(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
