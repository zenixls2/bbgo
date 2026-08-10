package gammacapture

import (
	"testing"
	"time"
)

func applyNoTradeTestState(
	t *testing.T,
	state *MacroInventoryState,
	at, closed time.Time,
	in NoTradeInventoryInput,
) NoTradeInventoryDecision {
	t.Helper()
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	if !d.Healthy {
		t.Fatalf("test input unexpectedly unhealthy: %+v", d)
	}
	d, _ = state.ApplyNoTradeState(at, closed, in, d)
	return d
}

func TestNoTradeAimPosteriorVarianceShrinksWithEvidence(t *testing.T) {
	sparseInput := noTradeTestInput()
	sparseInput.CrossingUp, sparseInput.CrossingDown = 6, 4
	sparseInput.ExecutableCrossingUp, sparseInput.ExecutableCrossingDown = 6, 4
	sparse := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, sparseInput)

	denseInput := sparseInput
	denseInput.CrossingUp, denseInput.CrossingDown = 60, 40
	denseInput.ExecutableCrossingUp, denseInput.ExecutableCrossingDown = 60, 40
	dense := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, denseInput)
	if sparse.AimMeasurementVariance <= 0 || dense.AimMeasurementVariance <= 0 ||
		dense.AimMeasurementVariance >= sparse.AimMeasurementVariance {
		t.Fatalf("Beta posterior uncertainty did not shrink with evidence: sparse=%+v dense=%+v", sparse, dense)
	}
}

func TestNoTradeAimFilterUpdatesOnlyOnClosedMacroBar(t *testing.T) {
	state := &MacroInventoryState{}
	start := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	bullishInput := noTradeTestInput()
	first := applyNoTradeTestState(t, state, start, start, bullishInput)

	bearishInput := noTradeTestInput()
	bearishInput.CrossingUp, bearishInput.CrossingDown = 10, 30
	bearishInput.ExecutableCrossingUp, bearishInput.ExecutableCrossingDown = 12, 24
	sameBar := applyNoTradeTestState(t, state, start.Add(time.Minute), start, bearishInput)
	if sameBar.AimRatio != first.AimRatio || sameBar.AimKalmanGain != 0 {
		t.Fatalf("BBO update changed the Macro aim inside one closed bar: first=%+v same=%+v", first, sameBar)
	}

	nextBar := applyNoTradeTestState(t, state, start.Add(10*time.Minute), start.Add(10*time.Minute), bearishInput)
	if !(nextBar.RawAimRatio < nextBar.AimRatio && nextBar.AimRatio < first.AimRatio) {
		t.Fatalf("Kalman update must partially move toward the new raw aim: first=%+v next=%+v", first, nextBar)
	}
	if !(nextBar.AimKalmanGain > 0 && nextBar.AimKalmanGain < 1) {
		t.Fatalf("posterior-variance Kalman gain must be proper: %+v", nextBar)
	}
}

func TestNoTradeFilteredAimUsesSingleStatelessBoundary(t *testing.T) {
	state := &MacroInventoryState{}
	at := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	in := noTradeTestInput()
	raw := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	in.CurrentRiskyWeight = raw.AimRatio
	centered := applyNoTradeTestState(t, state, at, at, in)
	if centered.Direction != 0 {
		t.Fatalf("inventory at the filtered aim must be neutral: %+v", centered)
	}

	in.CurrentRiskyWeight = centered.LowerRatio - 0.001
	entered := applyNoTradeTestState(t, state, at.Add(time.Second), at, in)
	if entered.Direction != 1 || entered.ExecutionTargetRatio != entered.LowerRatio {
		t.Fatalf("single lower boundary did not trigger BUY correction: %+v", entered)
	}

	in.CurrentRiskyWeight = centered.LowerRatio
	released := applyNoTradeTestState(t, state, at.Add(2*time.Second), at, in)
	if released.Direction != 0 {
		t.Fatalf("stateless correction did not release at the same boundary: %+v", released)
	}
}

func TestNoTradeStateProjectsBearishContinuationConstraintOnSameBar(t *testing.T) {
	at := time.Date(2026, 8, 3, 0, 30, 0, 0, time.UTC)
	in := noTradeTestInput()
	in.CurrentRiskyWeight = 0.3
	state := &MacroInventoryState{
		NoTradeFilteredAimRatio: 0.6,
		NoTradeAimVariance:      0.01,
		NoTradeAimUpdatedAt:     at,
		NoTradeAimClosedBarAt:   at,
	}
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	d.Healthy = true
	d.ContinuationCapApplied = true
	d.ContinuationCapRatio = in.CurrentRiskyWeight
	got, changed := state.ApplyNoTradeState(at.Add(time.Second), at, in, d)
	if !changed || got.AimRatio != in.CurrentRiskyWeight ||
		state.NoTradeFilteredAimRatio != in.CurrentRiskyWeight ||
		got.Direction != 0 {
		t.Fatalf("constrained Kalman projection did not prevent same-bar re-exposure: %+v state=%+v", got, state)
	}
}

func TestNoTradeHoldProtectionProjectsStaleFilteredAimImmediately(t *testing.T) {
	at := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	in := noTradeTestInput()
	in.CurrentRiskyWeight = 0.63
	state := &MacroInventoryState{
		NoTradeFilteredAimRatio: 0.50,
		NoTradeAimVariance:      0.01,
		NoTradeAimUpdatedAt:     at,
		NoTradeAimClosedBarAt:   at,
	}
	d := EvaluateNoTradeInventory(NoTradeInventoryConfig{
		Enabled: true, HoldProtectionEnabled: true, HoldProtectionZScore: 1.645,
	}, in)
	if !d.HoldProtectionApplied {
		t.Fatalf("test setup did not reject the stale directional target: %+v", d)
	}
	got, changed := state.ApplyNoTradeState(at.Add(time.Second), at, in, d)
	if !changed || got.AimRatio != in.CurrentRiskyWeight ||
		state.NoTradeFilteredAimRatio != in.CurrentRiskyWeight ||
		state.NoTradeAimVariance != 0 || got.Direction != 0 {
		t.Fatalf("hold protection leaked through the persisted Kalman aim: %+v state=%+v", got, state)
	}
}
