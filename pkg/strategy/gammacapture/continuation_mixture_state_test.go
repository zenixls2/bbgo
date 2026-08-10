package gammacapture

import (
	"testing"
	"time"
)

func TestContinuationMixtureModeChangeResetsIncompatibleKalmanAim(t *testing.T) {
	at := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	state := &MacroInventoryState{
		NoTradeFilteredAimRatio: 0.9,
		NoTradeAimVariance:      0.001,
		NoTradeAimUpdatedAt:     at.Add(-10 * time.Minute),
		NoTradeAimClosedBarAt:   at.Add(-10 * time.Minute),
	}
	in := noTradeTestInput()
	in.TrendContinuation = TrendContinuationDecision{
		Healthy: true, ForecastHorizon: 3 * time.Hour,
		ExpectedReturn: -0.02, ReturnVariance: 0.0001,
		MeanSE: 0.001, ModelProbability: 0.9,
	}
	d := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, ContinuationMixtureEnabled: true}, in)
	if !d.ContinuationMixtureApplied {
		t.Fatal("mixture mode was not marked on the decision")
	}
	got, changed := state.ApplyNoTradeState(at, at, in, d)
	if !changed || !state.NoTradeContinuationMixture || got.AimKalmanGain != 1 {
		t.Fatalf("model-mode migration did not initialize from the new raw aim: got=%+v state=%+v", got, state)
	}
	if got.AimRatio != got.RawAimRatio || state.NoTradeFilteredAimRatio != got.RawAimRatio {
		t.Fatalf("old QV-only aim contaminated the mixture state: got=%+v state=%+v", got, state)
	}
}
