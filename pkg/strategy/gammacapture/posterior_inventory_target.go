package gammacapture

import "math"

// PosteriorInventoryTargetDecision is the posterior expectation of two
// self-financing inventory benchmarks: the session base anchor and the policy
// target. The executable BUY path supplies the probability of the higher-base
// state; no frozen training artifact or BPS direction threshold is required.
type PosteriorInventoryTargetDecision struct {
	Enabled             bool
	Reason              string
	TargetBase          float64
	UpProbability       float64
	InventoryReturnMean float64
	InventoryReturnSE   float64
	EffectiveSamples    float64
}

func PosteriorExpectedInventoryTarget(
	anchorBase, policyTargetBase, hardMinBase, hardMaxBase float64,
	stats JointPathPayoffStats,
) PosteriorInventoryTargetDecision {
	d := PosteriorInventoryTargetDecision{
		Reason:           "terminal inventory posterior unavailable",
		TargetBase:       policyTargetBase,
		UpProbability:    0.5,
		EffectiveSamples: stats.EffectiveSamples,
	}
	finite := func(value float64) bool {
		return !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	if !finite(anchorBase) || !finite(policyTargetBase) ||
		!finite(hardMinBase) || !finite(hardMaxBase) ||
		hardMinBase > hardMaxBase || anchorBase < 0 || policyTargetBase < 0 {
		return d
	}
	anchorBase = math.Max(hardMinBase, math.Min(hardMaxBase, anchorBase))
	policyTargetBase = math.Max(hardMinBase, math.Min(hardMaxBase, policyTargetBase))
	lowerTarget := math.Min(anchorBase, policyTargetBase)
	upperTarget := math.Max(anchorBase, policyTargetBase)
	d.TargetBase = 0.5 * (lowerTarget + upperTarget)
	if stats.EffectiveSamples <= 1 ||
		!finite(stats.BuyDominant.InventoryMeanBps) ||
		!finite(stats.BuyDominant.InventoryVarBps2) ||
		stats.BuyDominant.InventoryVarBps2 < 0 {
		return d
	}
	d.InventoryReturnMean = stats.BuyDominant.InventoryMeanBps
	d.InventoryReturnSE = math.Sqrt(
		stats.BuyDominant.InventoryVarBps2 / stats.EffectiveSamples)
	switch {
	case d.InventoryReturnSE > 0:
		d.UpProbability = 0.5 * (1 + math.Erf(
			d.InventoryReturnMean/(d.InventoryReturnSE*math.Sqrt2)))
	case d.InventoryReturnMean > 0:
		d.UpProbability = 1
	case d.InventoryReturnMean < 0:
		d.UpProbability = 0
	}
	d.TargetBase = lowerTarget + d.UpProbability*(upperTarget-lowerTarget)
	d.TargetBase = math.Max(hardMinBase, math.Min(hardMaxBase, d.TargetBase))
	d.Enabled = true
	d.Reason = "posterior expected inventory target"
	return d
}
