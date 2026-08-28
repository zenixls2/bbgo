package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestAdaptivePathDecayLearnsPersistentVolatilityAndTimeScales(t *testing.T) {
	state := adaptivePathDecayState{}
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	// Persistent absolute returns should produce a positive lag correlation
	// and a half-life determined by the observed one-minute clock, not 0.1.
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	for i, value := range values {
		state.observePath(start.Add(time.Duration(i)*time.Minute), value, 10*time.Minute, 6*time.Hour)
	}
	if state.autocorrelation() <= 0 {
		t.Fatalf("expected positive persistence: %+v", state)
	}
	h := state.halfLife(10*time.Minute, 6*time.Hour)
	if h < (10*time.Minute).Seconds() || h > (6*time.Hour).Seconds() || math.IsNaN(h) {
		t.Fatalf("half-life must stay within causal horizon bounds: %v", h)
	}
	near := state.decayFactor(time.Minute, 10*time.Minute, 6*time.Hour)
	far := state.decayFactor(time.Hour, 10*time.Minute, 6*time.Hour)
	if near <= far || near <= 0 || far <= 0 || far >= 1 {
		t.Fatalf("time-scaled decay is invalid: near=%v far=%v", near, far)
	}
}

func TestAdaptivePathDecayDoesNotBridgeGapAndNeffIsIdempotent(t *testing.T) {
	state := adaptivePathDecayState{}
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	state.observePath(start, 1, 10*time.Minute, time.Hour)
	state.resetSegment()
	state.observePath(start.Add(10*time.Minute), 100, 10*time.Minute, time.Hour)
	if state.PairCount != 0 {
		t.Fatalf("gap must not manufacture a lagged pair: %+v", state)
	}
	state.observeNeff(start.Add(10*time.Minute), 3, 10*time.Minute, time.Hour)
	state.observeNeff(start.Add(10*time.Minute), 100, 10*time.Minute, time.Hour)
	if state.NeffMean != 3 || state.NeffCount != 1 {
		t.Fatalf("same-time Neff update must be idempotent: %+v", state)
	}
}

func TestAssessJointPathMaturityAddsOnlyContinuousNeffSurcharge(t *testing.T) {
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		JointDistanceQuantity: JointDistanceQuantityConfig{PathMaturityMaxRelativeHalfWidth: 100},
	}
	base := JointPathPayoffStats{
		EffectiveSamples: 4,
		BuyDominant:      jointPathPayoffMoments{BuyMeanBps: 10, BuyVarBps2: 100},
		SellDominant:     jointPathPayoffMoments{SellMeanBps: 10, SellVarBps2: 100},
	}
	widened := base
	widened.EffectiveSamplesBaseline = 16
	plain := AssessJointPathMaturity(base, config, 1.645)
	low := AssessJointPathMaturity(widened, config, 1.645)
	if low.ConfidenceHalfWidthBps <= plain.ConfidenceHalfWidthBps {
		t.Fatalf("lower current Neff must widen uncertainty, plain=%v low=%v", plain.ConfidenceHalfWidthBps, low.ConfidenceHalfWidthBps)
	}
	if low.Matured != plain.Matured {
		t.Fatalf("Neff surcharge must not be a second hard gate: plain=%+v low=%+v", plain, low)
	}
}
