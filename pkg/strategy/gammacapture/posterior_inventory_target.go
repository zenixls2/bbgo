package gammacapture

import "math"

// PosteriorInventoryTargetDecision is the bounded same-horizon inventory aim.
// The executable inventory-return path supplies the posterior sign probability;
// no frozen training artifact or BPS direction threshold is required.
type PosteriorInventoryTargetDecision struct {
	Enabled               bool
	Reason                string
	TargetBase            float64
	UpProbability         float64
	DirectionConfidence   float64
	InventoryReturnMean   float64
	InventoryReturnSE     float64
	InventoryPredictiveSD float64
	EffectiveSamples      float64
}

// PosteriorInventoryRiskTarget maps the posterior-predictive probability of a
// positive same-horizon executable inventory return into the hard inventory
// interval. It deliberately does not use mean/SE: that is confidence that the
// historical population mean is positive and converges to one for any tiny
// positive mean as the sample grows. The next realized horizon still contains
// path variance. Under the moment-matched Gaussian predictive distribution,
//
//	predictiveVariance = sampleVariance + sampleVariance/effectiveSamples.
//
// The first term is irreducible next-window risk and the second is estimation
// risk. The strategic target is the neutral prior. Evidence above 50% moves
// only through the available upper room; evidence below 50% moves through the
// lower room. This remains scale invariant, bounded, and has no fitted BPS
// coefficient.
func PosteriorInventoryRiskTarget(
	policyTarget, hardMin, hardMax float64,
	stats JointPathPayoffStats,
) PosteriorInventoryTargetDecision {
	d := PosteriorInventoryTargetDecision{
		Reason:           "terminal inventory posterior unavailable",
		TargetBase:       policyTarget,
		UpProbability:    0.5,
		EffectiveSamples: stats.InventoryTargetEffectiveSamples,
	}
	finite := func(value float64) bool {
		return !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	if !finite(policyTarget) || !finite(hardMin) || !finite(hardMax) ||
		hardMin > hardMax || policyTarget < hardMin || policyTarget > hardMax {
		return d
	}
	moments := stats.InventoryTarget
	effectiveSamples := stats.InventoryTargetEffectiveSamples
	// Preserve compatibility for focused callers and synthetic tests that
	// construct JointPathPayoffStats directly. Production statistics always
	// provide the explicitly side-neutral inventory-target moments.
	if effectiveSamples <= 0 {
		moments = stats.BuyDominant
		effectiveSamples = stats.EffectiveSamples
	}
	d.EffectiveSamples = effectiveSamples
	if effectiveSamples <= 1 ||
		!finite(moments.InventoryDirectionalMeanBps) ||
		!finite(moments.InventoryDirectionalVarBps2) ||
		moments.InventoryDirectionalVarBps2 < 0 {
		return d
	}
	d.InventoryReturnMean = moments.InventoryDirectionalMeanBps
	d.InventoryReturnSE = math.Sqrt(moments.InventoryDirectionalVarBps2 / effectiveSamples)
	d.InventoryPredictiveSD = math.Sqrt(
		moments.InventoryDirectionalVarBps2 + d.InventoryReturnSE*d.InventoryReturnSE)
	switch {
	case d.InventoryPredictiveSD > 0:
		d.UpProbability = 0.5 * (1 + math.Erf(
			d.InventoryReturnMean/(d.InventoryPredictiveSD*math.Sqrt2)))
	case d.InventoryReturnMean > 0:
		d.UpProbability = 1
	case d.InventoryReturnMean < 0:
		d.UpProbability = 0
	}
	d.DirectionConfidence = math.Max(-1, math.Min(1, 2*d.UpProbability-1))
	if d.DirectionConfidence >= 0 {
		d.TargetBase = policyTarget + d.DirectionConfidence*(hardMax-policyTarget)
	} else {
		d.TargetBase = policyTarget + d.DirectionConfidence*(policyTarget-hardMin)
	}
	d.TargetBase = math.Max(hardMin, math.Min(hardMax, d.TargetBase))
	d.Enabled = true
	d.Reason = "posterior target-relative inventory aim"
	return d
}
