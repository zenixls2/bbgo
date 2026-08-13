package gammacapture

import (
	"math"
	"testing"
)

func TestPosteriorExpectedInventoryTargetUsesCausalSignProbability(t *testing.T) {
	stats := JointPathPayoffStats{
		EffectiveSamples: 4,
		BuyDominant: jointPathPayoffMoments{
			InventoryMeanBps: 10,
			InventoryVarBps2: 400,
		},
	}
	d := PosteriorExpectedInventoryTarget(3, 5, 0, 10, stats)
	wantProbability := 0.5 * (1 + math.Erf(1/math.Sqrt2))
	if !d.Enabled || math.Abs(d.UpProbability-wantProbability) > 1e-12 {
		t.Fatalf("unexpected posterior target: %+v", d)
	}
	if want := 3 + wantProbability*2; math.Abs(d.TargetBase-want) > 1e-12 {
		t.Fatalf("unexpected expected target: got %v want %v", d.TargetBase, want)
	}
}

func TestPosteriorExpectedInventoryTargetFallsBackToMidpointWithoutVariance(t *testing.T) {
	d := PosteriorExpectedInventoryTarget(3, 5, 0, 10, JointPathPayoffStats{})
	if d.Enabled || d.TargetBase != 4 || d.UpProbability != 0.5 {
		t.Fatalf("unidentified posterior must use the symmetric prior: %+v", d)
	}
}

func TestPosteriorExpectedInventoryTargetAlwaysMapsUpToHigherExposure(t *testing.T) {
	stats := JointPathPayoffStats{
		EffectiveSamples: 4,
		BuyDominant: jointPathPayoffMoments{
			InventoryMeanBps: 100,
			InventoryVarBps2: 1,
		},
	}
	d := PosteriorExpectedInventoryTarget(7, 5, 0, 10, stats)
	if d.TargetBase < 6.999 || d.TargetBase > 7 {
		t.Fatalf("positive posterior must select the higher-base benchmark: %+v", d)
	}
}
