package gammacapture

import "math"

// applyContinuationMixture selects the executable continuation posterior as
// the predictive law when it is healthy. Its ExpectedReturn and ReturnVariance
// already include the Dirichlet censor outcome as a zero return. Multiplying
// either quantity by (1-CensorProbability) again would count quiet-path
// uncertainty twice.
//
// QV crossing remains the causal fallback when the continuation posterior is
// unavailable. The two estimators are deliberately not precision-averaged:
// they use the same symbol and overlapping price history, so treating them as
// conditionally independent would create false confidence.
func applyContinuationMixture(
	c NoTradeInventoryConfig,
	in NoTradeInventoryInput,
	d NoTradeInventoryDecision,
	minimum, maximum float64,
) NoTradeInventoryDecision {
	continuation := in.TrendContinuation
	if !c.ContinuationMixtureEnabled || !continuation.Healthy ||
		in.TrendExcursion.Healthy || continuation.ForecastHorizon <= 0 {
		return d
	}

	mean := continuation.ExpectedReturn
	variance := math.Max(0, continuation.ReturnVariance)
	denominator := in.RiskAversion*variance + in.PriorStrength
	prior := clampRatio(in.PriorTargetRatio, minimum, maximum)

	d.ForecastObservation = continuation.ForecastHorizon
	d.ForecastReturn = mean
	d.ForecastVariance = variance
	d.BaseForecastVariance = variance
	d.FastRiskApplied = false
	d.FastRiskDenominatorScale = 0
	if denominator > 0 {
		d.AimRatio = clampRatio(
			(mean+in.PriorStrength*prior)/denominator,
			minimum, maximum)
		d.AimMeasurementVariance = math.Min(
			math.Pow(maximum-minimum, 2),
			math.Pow(math.Max(0, continuation.MeanSE)/denominator, 2))
	}
	d.BaseAimRatio = d.AimRatio
	d.RiskAdjustedAimRatio = d.AimRatio
	d.RawAimRatio = d.AimRatio
	d.ContinuationMixtureApplied = true
	// A complete, causally matured continuation posterior can own the target
	// even while sparse first-passage crossings leave QV health degraded.
	d.Healthy = true
	return d
}
