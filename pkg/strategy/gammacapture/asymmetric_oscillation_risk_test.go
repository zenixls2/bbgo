package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func testAsymmetricRiskConfig() AsymmetricOscillationRiskConfig {
	return AsymmetricOscillationRiskConfig{
		DirectionStrength: 0.6,
		AsymmetryWeight:   0.5,
		MinMultiplier:     0.65,
		MaxMultiplier:     1.75,
		EWMAAlpha:         0.5,
		PriorVarianceBps2: 25,
		MinSamples:        2,
	}
}

func TestAsymmetricOscillationRiskUpDownAndFlatPaths(t *testing.T) {
	cfg := testAsymmetricRiskConfig()
	flat := EvaluateAsymmetricOscillationRisk(cfg, AsymmetricOscillationRiskFeatures{
		NetReturnBps: 0, TotalVariationBps: 40, ScaleBps: 10,
	}, AsymmetricOscillationRiskStats{})
	if flat.RiskMultiplier != 1 || flat.OscillationScore != 0 {
		t.Fatalf("flat path must be neutral: %+v", flat)
	}
	up := EvaluateAsymmetricOscillationRisk(cfg, AsymmetricOscillationRiskFeatures{
		NetReturnBps: 10, TotalVariationBps: 40, ScaleBps: 10,
	}, AsymmetricOscillationRiskStats{})
	down := EvaluateAsymmetricOscillationRisk(cfg, AsymmetricOscillationRiskFeatures{
		NetReturnBps: -10, TotalVariationBps: 40, ScaleBps: 10,
	}, AsymmetricOscillationRiskStats{})
	if !(up.RiskMultiplier < 1 && down.RiskMultiplier > 1) {
		t.Fatalf("up/down oscillating paths have wrong risk ordering: up=%+v down=%+v", up, down)
	}
	if math.Abs(up.OscillationScore+down.OscillationScore) > 1e-12 {
		t.Fatalf("path reflection must negate score: up=%g down=%g", up.OscillationScore, down.OscillationScore)
	}
	monotone := EvaluateAsymmetricOscillationRisk(cfg, AsymmetricOscillationRiskFeatures{
		NetReturnBps: 20, TotalVariationBps: 20, ScaleBps: 10,
	}, AsymmetricOscillationRiskStats{})
	if monotone.RiskMultiplier != 1 {
		t.Fatalf("monotone path is not an oscillation and must be neutral: %+v", monotone)
	}
}

func TestAsymmetricOscillationRiskMaturedLabelsChangeAsymmetry(t *testing.T) {
	m := NewAsymmetricOscillationRiskModel(testAsymmetricRiskConfig())
	features := AsymmetricOscillationRiskFeatures{NetReturnBps: 10, TotalVariationBps: 40, ScaleBps: 10}
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		now := start.Add(time.Duration(i) * 20 * time.Minute)
		m.Predict(now, 100, 10*time.Minute, features)
		if m.UpdateLabel(now.Add(10*time.Minute-1*time.Second), 101) {
			t.Fatal("label updated before horizon maturity")
		}
		if !m.UpdateLabel(now.Add(10*time.Minute), 101) {
			t.Fatal("matured upward label was not accepted")
		}
	}
	// Downward oscillation receives larger realized terminal variance.
	downFeatures := features
	downFeatures.NetReturnBps = -10
	for i := 0; i < 2; i++ {
		now := start.Add(time.Duration(i+2) * 20 * time.Minute)
		m.Predict(now, 100, 10*time.Minute, downFeatures)
		if !m.UpdateLabel(now.Add(10*time.Minute), 95) {
			t.Fatal("matured downward label was not accepted")
		}
	}
	if m.Stats.UpSamples != 2 || m.Stats.DownSamples != 2 ||
		m.Stats.DownVarianceBps2 <= m.Stats.UpVarianceBps2 {
		t.Fatalf("unexpected asymmetric variance state: %+v", m.Stats)
	}
	decision := EvaluateAsymmetricOscillationRisk(m.Config, features, m.Stats)
	if !decision.AsymmetryReady || decision.AsymmetryScore <= 0 || decision.RiskMultiplier >= 1 {
		t.Fatalf("downside-dominant asymmetry did not reduce upward risk: %+v", decision)
	}
	decision = EvaluateAsymmetricOscillationRisk(m.Config, downFeatures, m.Stats)
	if decision.RiskMultiplier <= 1 {
		t.Fatalf("downward oscillation did not increase risk: %+v", decision)
	}
}

func TestAsymmetricOscillationRiskGapResetAndBounds(t *testing.T) {
	m := NewAsymmetricOscillationRiskModel(testAsymmetricRiskConfig())
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	features := AsymmetricOscillationRiskFeatures{NetReturnBps: -12, TotalVariationBps: 30, ScaleBps: 5, SpreadBps: 100}
	m.Predict(start, 100, 10*time.Minute, features)
	m.ResetPending()
	if m.UpdateLabel(start.Add(10*time.Minute), 90) {
		t.Fatal("a gap-reset pending label must not update statistics")
	}
	decision := EvaluateAsymmetricOscillationRisk(m.Config, features, m.Stats)
	if decision.RiskMultiplier < m.Config.MinMultiplier || decision.RiskMultiplier > m.Config.MaxMultiplier ||
		!finiteAsymmetricRisk(decision.RiskMultiplier) {
		t.Fatalf("risk multiplier escaped bounds: %+v", decision)
	}
	if invalid := EvaluateAsymmetricOscillationRisk(m.Config, AsymmetricOscillationRiskFeatures{NetReturnBps: math.NaN()}, m.Stats); invalid.Enabled {
		t.Fatalf("invalid feature should fail closed: %+v", invalid)
	}
}

func TestAsymmetricOscillationRiskReflectionIsNotSpreadSensitiveToSign(t *testing.T) {
	cfg := testAsymmetricRiskConfig()
	stats := AsymmetricOscillationRiskStats{UpVarianceBps2: 9, DownVarianceBps2: 9, UpSamples: 2, DownSamples: 2}
	up := EvaluateAsymmetricOscillationRisk(cfg, AsymmetricOscillationRiskFeatures{
		NetReturnBps: 8, TotalVariationBps: 32, ScaleBps: 8, SpreadBps: 2,
	}, stats)
	down := EvaluateAsymmetricOscillationRisk(cfg, AsymmetricOscillationRiskFeatures{
		NetReturnBps: -8, TotalVariationBps: 32, ScaleBps: 8, SpreadBps: 2,
	}, stats)
	if math.Abs(up.RiskMultiplier*down.RiskMultiplier-1) > 1e-9 {
		t.Fatalf("symmetric variance should make reflected multipliers reciprocal: up=%g down=%g", up.RiskMultiplier, down.RiskMultiplier)
	}
	wide := EvaluateAsymmetricOscillationRisk(cfg, AsymmetricOscillationRiskFeatures{
		NetReturnBps: 8, TotalVariationBps: 32, ScaleBps: 8, SpreadBps: 30,
	}, stats)
	if wide.RiskMultiplier <= up.RiskMultiplier || wide.RiskMultiplier >= 1 {
		t.Fatalf("wide spread should damp the upward risk reduction: narrow=%g wide=%g", up.RiskMultiplier, wide.RiskMultiplier)
	}
}

func TestAsymmetricOscillationRiskUsesCausalBidPathFeatures(t *testing.T) {
	config := MarketMakerConfig{
		HorizonLookback: types.Duration(2 * time.Hour), MinTradingWindow: types.Duration(10 * time.Minute),
		MaxTradingWindow: types.Duration(30 * time.Minute),
	}
	config.setDefaults()
	model := MarketMakerHorizonModel{}
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20*60; i++ {
		// A bounded oscillating bid path with a small negative endpoint drift.
		bid := 100.0 + []float64{0, 1, -1, 2, -2, 1, -1}[i%7] - 0.005*float64(i)
		model.ObserveBook(start.Add(time.Duration(i)*time.Second), bid, bid+0.01, config)
	}
	features, ready := model.AsymmetricOscillationRiskFeatures(10 * time.Minute)
	if !ready || features.TotalVariationBps <= math.Abs(features.NetReturnBps) {
		t.Fatalf("expected causal oscillation features, ready=%t features=%+v", ready, features)
	}
	decision := EvaluateAsymmetricOscillationRisk(testAsymmetricRiskConfig(), features, AsymmetricOscillationRiskStats{})
	if decision.RiskMultiplier <= 1 {
		t.Fatalf("negative oscillating bid path should increase risk aversion: features=%+v decision=%+v", features, decision)
	}
}
