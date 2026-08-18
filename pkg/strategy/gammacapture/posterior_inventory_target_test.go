package gammacapture

import (
	"math"
	"testing"
)

func TestPosteriorInventoryRiskTargetMovesFromNeutralTowardSupportedSide(t *testing.T) {
	stats := func(mean float64) JointPathPayoffStats {
		return JointPathPayoffStats{
			EffectiveSamples: 4,
			BuyDominant: jointPathPayoffMoments{
				InventoryDirectionalMeanBps: mean,
				InventoryDirectionalVarBps2: 400,
			},
		}
	}
	up := PosteriorInventoryRiskTarget(5, 0, 10, stats(10))
	down := PosteriorInventoryRiskTarget(5, 0, 10, stats(-10))
	// The next path retains its 20 bps standard deviation; only the mean's
	// 10 bps standard error is added as estimation uncertainty.
	wantPredictiveSD := math.Sqrt(400.0 + 400.0/4.0)
	wantProbability := 0.5 * (1 + math.Erf(10/wantPredictiveSD/math.Sqrt2))
	if !up.Enabled || !down.Enabled ||
		math.Abs(up.UpProbability-wantProbability) > 1e-12 ||
		math.Abs(down.UpProbability-(1-wantProbability)) > 1e-12 {
		t.Fatalf("posterior sign probabilities are not reflected: up=%+v down=%+v", up, down)
	}
	if math.Abs(up.InventoryReturnSE-10) > 1e-12 ||
		math.Abs(up.InventoryPredictiveSD-wantPredictiveSD) > 1e-12 {
		t.Fatalf("mean and predictive uncertainty must stay distinct: %+v", up)
	}
	if math.Abs(up.TargetBase-(10*wantProbability)) > 1e-12 ||
		math.Abs(down.TargetBase-(10*(1-wantProbability))) > 1e-12 {
		t.Fatalf("posterior targets are not symmetric around the strategic prior: up=%+v down=%+v", up, down)
	}
}

func TestPosteriorInventoryRiskTargetDoesNotTurnTinyMeanIntoCertainty(t *testing.T) {
	d := PosteriorInventoryRiskTarget(5, 0, 10, JointPathPayoffStats{
		EffectiveSamples: 1_000_000,
		BuyDominant: jointPathPayoffMoments{
			InventoryDirectionalMeanBps: 0.01,
			InventoryDirectionalVarBps2: 400,
		},
	})
	if !d.Enabled {
		t.Fatalf("predictive target should be available: %+v", d)
	}
	if d.UpProbability >= 0.501 || math.Abs(d.TargetBase-5) >= 0.01 {
		t.Fatalf("large n must not turn a negligible predictive edge into a hard-band target: %+v", d)
	}
}

func TestPosteriorInventoryRiskTargetConvergesToPredictiveNotMeanProbability(t *testing.T) {
	stats := JointPathPayoffStats{
		EffectiveSamples: 1_000_000,
		BuyDominant: jointPathPayoffMoments{
			InventoryDirectionalMeanBps: 10,
			InventoryDirectionalVarBps2: 400,
		},
	}
	d := PosteriorInventoryRiskTarget(5, 0, 10, stats)
	want := 0.5 * (1 + math.Erf(0.5/math.Sqrt2))
	if math.Abs(d.UpProbability-want) > 1e-6 {
		t.Fatalf("posterior must converge to P(next return > 0), got=%+v want=%v", d, want)
	}
	if d.TargetBase >= 9 {
		t.Fatalf("one-half predictive Sharpe must not saturate the hard band: %+v", d)
	}
}

func TestPosteriorInventoryRiskTargetUsesPolicyPriorWithoutEvidence(t *testing.T) {
	d := PosteriorInventoryRiskTarget(4, 0, 10, JointPathPayoffStats{})
	if d.Enabled || d.TargetBase != 4 || d.UpProbability != 0.5 || d.DirectionConfidence != 0 {
		t.Fatalf("missing evidence must retain the strategic target: %+v", d)
	}
}

func TestPosteriorInventoryRiskTargetIsScaleInvariant(t *testing.T) {
	stats := JointPathPayoffStats{
		EffectiveSamples: 25,
		BuyDominant: jointPathPayoffMoments{
			InventoryDirectionalMeanBps: 5,
			InventoryDirectionalVarBps2: 625,
		},
	}
	base := PosteriorInventoryRiskTarget(0.5, 0, 1, stats)
	notional := PosteriorInventoryRiskTarget(5_000, 0, 10_000, stats)
	if !base.Enabled || !notional.Enabled ||
		math.Abs(base.TargetBase*10_000-notional.TargetBase) > 1e-9 {
		t.Fatalf("posterior target must not depend on inventory units: base=%+v notional=%+v", base, notional)
	}
}

func TestPosteriorInventoryRiskTargetDoesNotInheritBuyDominantQuoteWeights(t *testing.T) {
	stats := JointPathPayoffStats{
		EffectiveSamples: 25,
		BuyDominant: jointPathPayoffMoments{
			InventoryDirectionalMeanBps: 20,
			InventoryDirectionalVarBps2: 100,
		},
		InventoryTargetEffectiveSamples: 25,
		InventoryTarget: jointPathPayoffMoments{
			InventoryDirectionalMeanBps: -20,
			InventoryDirectionalVarBps2: 100,
		},
	}
	d := PosteriorInventoryRiskTarget(5, 0, 10, stats)
	if !d.Enabled || d.InventoryReturnMean >= 0 || d.TargetBase >= 5 {
		t.Fatalf("single inventory aim inherited BUY quote-allocation weights: %+v", d)
	}
}
