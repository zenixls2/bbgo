package gammacapture

import (
	"math"
	"testing"
	"time"
)

func withFastVarianceRisk(in NoTradeInventoryInput) NoTradeInventoryInput {
	in.FastRiskHealthy = true
	in.FastRiskHorizon = 15 * time.Minute
	in.FastRiskVarianceRatePerSecond = 2e-9
	in.FastRiskBaselineRatePerSecond = 4e-10
	return in
}

func TestFastVarianceRiskChangesRiskNotExpectedReturn(t *testing.T) {
	in := noTradeTestInput()
	baseline := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	risk := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, FastVarianceRiskEnabled: true},
		withFastVarianceRisk(in),
	)
	if !risk.FastRiskApplied || risk.ForecastVariance <= baseline.ForecastVariance {
		t.Fatalf("HAR forecast did not conservatively raise risk variance: baseline=%+v risk=%+v", baseline, risk)
	}
	if math.Abs(risk.ForecastReturn-baseline.ForecastReturn) > 1e-15 {
		t.Fatalf("HAR risk manufactured expected return: baseline=%g risk=%g", baseline.ForecastReturn, risk.ForecastReturn)
	}
	if risk.AimRatio >= baseline.AimRatio {
		t.Fatalf("larger forecast risk did not reduce the bullish Merton exposure: baseline=%g risk=%g", baseline.AimRatio, risk.AimRatio)
	}
	if risk.AimMeasurementVariance != baseline.AimMeasurementVariance {
		t.Fatalf("independent risk variance changed base-direction measurement noise: baseline=%g risk=%g", baseline.AimMeasurementVariance, risk.AimMeasurementVariance)
	}
}

func TestFastVarianceRiskBypassesOnlyPersistentKalmanAim(t *testing.T) {
	in := withFastVarianceRisk(noTradeTestInput())
	d := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, FastVarianceRiskEnabled: true}, in)
	if d.FastRiskDenominatorScale <= 0 || d.FastRiskDenominatorScale >= 1 || d.RawAimRatio != d.BaseAimRatio {
		t.Fatalf("invalid transient HAR denominator decision: %+v", d)
	}
	state := &MacroInventoryState{}
	at := time.Date(2026, 8, 3, 1, 0, 0, 0, time.UTC)
	filtered, _ := state.ApplyNoTradeState(at, at, in, d)
	want := clampRatio(d.BaseAimRatio*d.FastRiskDenominatorScale, in.PolicyMinRatio, in.PolicyMaxRatio)
	if math.Abs(filtered.AimRatio-want) > 1e-12 {
		t.Fatalf("HAR denominator was filtered instead of applied immediately: got=%g want=%g decision=%+v", filtered.AimRatio, want, filtered)
	}
	if math.Abs(state.NoTradeFilteredAimRatio-d.BaseAimRatio) > 1e-12 {
		t.Fatalf("transient HAR risk compounded into persistent Kalman state: state=%g base=%g", state.NoTradeFilteredAimRatio, d.BaseAimRatio)
	}
}

func TestFastVarianceRiskNeutralStressCapsOnlyReentry(t *testing.T) {
	cfg := NoTradeInventoryConfig{Enabled: true, FastVarianceRiskEnabled: true}
	in := withFastVarianceRisk(noTradeTestInput())
	in.CrossingUp, in.CrossingDown = 20, 20
	in.ExecutableCrossingUp, in.ExecutableCrossingDown = 20, 20
	in.CurrentRiskyWeight = 0.25
	in.FastRiskElevated = true
	stressed := EvaluateNoTradeInventory(cfg, in)
	if !stressed.FastRiskReentryCapApplied || stressed.AimRatio != in.CurrentRiskyWeight {
		t.Fatalf("confirmed neutral stress did not freeze low-weight reentry: %+v", stressed)
	}

	in.FastRiskElevated = false
	normal := EvaluateNoTradeInventory(cfg, in)
	if normal.FastRiskReentryCapApplied || normal.AimRatio != in.PriorTargetRatio {
		t.Fatalf("ordinary neutral variance must retain the strategic prior: %+v", normal)
	}

	in.FastRiskElevated = true
	in.CurrentRiskyWeight = 0.75
	above := EvaluateNoTradeInventory(cfg, in)
	if above.FastRiskReentryCapApplied || above.AimRatio != in.PriorTargetRatio {
		t.Fatalf("volatility alone must not force a sale above prior: %+v", above)
	}
}

func TestFastVarianceRiskDisabledIsBehaviorallyInert(t *testing.T) {
	in := withFastVarianceRisk(noTradeTestInput())
	in.FastRiskElevated = true
	in.CurrentRiskyWeight = 0.25
	want := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, noTradeTestInput())
	got := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	if got.FastRiskApplied || got.FastRiskReentryCapApplied ||
		got.ForecastVariance != want.ForecastVariance || got.ForecastReturn != want.ForecastReturn {
		t.Fatalf("disabled HAR path changed controller behavior: want=%+v got=%+v", want, got)
	}
}

func TestFastVarianceReentryCapSurvivesStateFilter(t *testing.T) {
	cfg := NoTradeInventoryConfig{Enabled: true, FastVarianceRiskEnabled: true}
	in := withFastVarianceRisk(noTradeTestInput())
	in.CrossingHealth = HealthDegraded
	in.CurrentRiskyWeight = 0.25
	in.FastRiskElevated = true
	d := EvaluateNoTradeInventory(cfg, in)
	state := &MacroInventoryState{NoTradeFilteredAimRatio: 0.5}
	at := time.Date(2026, 8, 3, 1, 0, 0, 0, time.UTC)
	filtered, _ := state.ApplyNoTradeState(at, at, in, d)
	if filtered.AimRatio != in.CurrentRiskyWeight || filtered.ExecutionTargetRatio > in.CurrentRiskyWeight {
		t.Fatalf("state filter erased the no-reentry projection: %+v", filtered)
	}
}
