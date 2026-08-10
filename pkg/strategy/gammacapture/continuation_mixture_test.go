package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestContinuationMixtureCanOverrideStaleBullishQV(t *testing.T) {
	in := noTradeTestInput()
	qvOnly := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	in.TrendContinuation = TrendContinuationDecision{
		Healthy: true, ForecastHorizon: 3 * time.Hour,
		ExpectedReturn: -0.02, ReturnVariance: 0.0001, MeanSE: 0.001,
		ModelProbability: 0.25,
	}
	mixed := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, ContinuationMixtureEnabled: true}, in)
	if !mixed.Healthy || mixed.ForecastReturn >= 0 || mixed.AimRatio >= qvOnly.AimRatio {
		t.Fatalf("fee-aware continuation did not override stale bullish QV: qv=%+v mixed=%+v", qvOnly, mixed)
	}
	if mixed.ContinuationCapApplied {
		t.Fatalf("predictive law must create one target, not a second hard cap: %+v", mixed)
	}
}

func TestContinuationMixtureCanRecognizeRecoveryAgainstStaleBearishQV(t *testing.T) {
	in := noTradeTestInput()
	in.CrossingUp, in.CrossingDown = in.CrossingDown, in.CrossingUp
	in.ExecutableCrossingUp, in.ExecutableCrossingDown =
		in.ExecutableCrossingDown, in.ExecutableCrossingUp
	qvOnly := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	in.TrendContinuation = TrendContinuationDecision{
		Healthy: true, ForecastHorizon: 3 * time.Hour,
		ExpectedReturn: 0.02, ReturnVariance: 0.0001, MeanSE: 0.001,
		ModelProbability: 0.25,
	}
	mixed := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, ContinuationMixtureEnabled: true}, in)
	if !mixed.Healthy || mixed.ForecastReturn <= 0 || mixed.AimRatio <= qvOnly.AimRatio {
		t.Fatalf("fee-aware recovery did not update stale bearish QV: qv=%+v mixed=%+v", qvOnly, mixed)
	}
}

func TestContinuationMixtureDoesNotDoubleWeightCensorMass(t *testing.T) {
	in := noTradeTestInput()
	in.TrendContinuation = TrendContinuationDecision{
		Healthy: true, ForecastHorizon: 3 * time.Hour,
		ExpectedReturn: -0.0125, ReturnVariance: 0.0004, MeanSE: 0.002,
		ModelProbability: 0.2, CensorProbability: 0.8,
	}
	got := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, ContinuationMixtureEnabled: true}, in)
	if math.Abs(got.ForecastReturn-in.TrendContinuation.ExpectedReturn) > 1e-12 {
		t.Fatalf("censor mass was applied twice: forecast=%g posterior=%g", got.ForecastReturn, in.TrendContinuation.ExpectedReturn)
	}
	if math.Abs(got.ForecastVariance-in.TrendContinuation.ReturnVariance) > 1e-12 {
		t.Fatalf("posterior predictive variance changed: got=%g want=%g", got.ForecastVariance, in.TrendContinuation.ReturnVariance)
	}
}

func TestContinuationMixtureRemainsHealthyWhenQVIsDegraded(t *testing.T) {
	in := noTradeTestInput()
	in.CrossingHealth = HealthDegraded
	in.ExecutableCrossingHealth = HealthDegraded
	in.CrossingUp, in.CrossingDown = 0, 0
	in.ExecutableCrossingUp, in.ExecutableCrossingDown = 0, 0
	in.TrendContinuation = TrendContinuationDecision{
		Healthy: true, ForecastHorizon: 3 * time.Hour,
		ExpectedReturn: -0.01, ReturnVariance: 0.0002, MeanSE: 0.002,
		CensorProbability: 0.3, ModelProbability: 0.7,
	}
	got := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, ContinuationMixtureEnabled: true}, in)
	if !got.Healthy || !got.ContinuationMixtureApplied || got.ForecastReturn != -0.01 {
		t.Fatalf("healthy continuation posterior was disabled by unrelated QV health: %+v", got)
	}
}
