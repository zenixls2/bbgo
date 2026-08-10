package gammacapture

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func feedRegimePath(t *testing.T, model *BayesianMultiscaleRegime, returns []float64, scale float64) []MultiscaleRegimeDecision {
	t.Helper()
	price := 300_000.0 * scale
	at := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	decisions := make([]MultiscaleRegimeDecision, 0, len(returns))
	model.ObserveMinute(at, price*.99995, price*1.00005)
	for _, value := range returns {
		price *= math.Exp(value)
		at = at.Add(time.Minute)
		decisions = append(decisions, model.ObserveMinute(at, price*.99995, price*1.00005))
	}
	return decisions
}

func TestBayesianMultiscaleRegimeLearnsPersistentDownRegime(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	returns := make([]float64, 180)
	for index := range returns {
		returns[index] = -0.00012 + rng.NormFloat64()*0.00008
	}
	model := NewBayesianMultiscaleRegime(MultiscaleRegimeConfig{
		HazardMean: 3 * time.Hour, VolatilityWindow: 30, MinimumSamples: 30,
	})
	decisions := feedRegimePath(t, model, returns, 1)
	last := decisions[len(decisions)-1]
	if !last.Healthy {
		t.Fatalf("expected healthy posterior, got %q", last.Reason)
	}
	if last.DownProbability < .95 {
		t.Fatalf("persistent down regime probability = %.6f, want >= .95", last.DownProbability)
	}
	if last.MeanBpsPerMinute >= 0 || last.MeanVarianceRatio >= 0 {
		t.Fatalf("expected negative mean and risk-scaled mean, got mean=%f ratio=%f", last.MeanBpsPerMinute, last.MeanVarianceRatio)
	}
}

func TestBayesianMultiscaleRegimeReversesWithoutLongWindowDelay(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	returns := make([]float64, 0, 180)
	for index := 0; index < 120; index++ {
		returns = append(returns, -0.00010+rng.NormFloat64()*0.00006)
	}
	for index := 0; index < 60; index++ {
		returns = append(returns, 0.00022+rng.NormFloat64()*0.00006)
	}
	model := NewBayesianMultiscaleRegime(MultiscaleRegimeConfig{
		HazardMean: 3 * time.Hour, VolatilityWindow: 30, MinimumSamples: 30,
	})
	decisions := feedRegimePath(t, model, returns, 1)
	delay := -1
	for index := 120; index < len(decisions); index++ {
		if decisions[index].Healthy && decisions[index].UpProbability >= .8 {
			delay = index - 120 + 1
			break
		}
	}
	if delay < 0 || delay > 15 {
		t.Fatalf("up-regime detection delay = %d minutes, want 1..15", delay)
	}
}

func TestBayesianMultiscaleRegimeIsPriceScaleInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	returns := make([]float64, 150)
	for index := range returns {
		returns[index] = 0.00008 + rng.NormFloat64()*0.00010
	}
	config := MultiscaleRegimeConfig{HazardMean: 3 * time.Hour, VolatilityWindow: 30, MinimumSamples: 30}
	one := feedRegimePath(t, NewBayesianMultiscaleRegime(config), returns, 1)
	hundred := feedRegimePath(t, NewBayesianMultiscaleRegime(config), returns, 100)
	left, right := one[len(one)-1], hundred[len(hundred)-1]
	if math.Abs(left.UpProbability-right.UpProbability) > 1e-9 ||
		math.Abs(left.MeanBpsPerMinute-right.MeanBpsPerMinute) > 1e-9 {
		t.Fatalf("price scaling changed posterior: %+v vs %+v", left, right)
	}
}

func TestBayesianMultiscaleRegimeRejectsSpreadOnlyDirection(t *testing.T) {
	model := NewBayesianMultiscaleRegime(MultiscaleRegimeConfig{
		HazardMean: time.Hour, VolatilityWindow: 20, MinimumSamples: 20,
	})
	at := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	model.ObserveMinute(at, 99.99, 100.01)
	var last MultiscaleRegimeDecision
	for index := 1; index <= 100; index++ {
		at = at.Add(time.Minute)
		spread := .01
		if index%2 == 0 {
			spread = .04
		}
		last = model.ObserveMinute(at, 100-spread, 100+spread)
	}
	if !last.Healthy {
		t.Fatalf("expected healthy posterior, got %q", last.Reason)
	}
	if math.Abs(last.DownProbability-.5) > .03 || math.Abs(last.MeanBpsPerMinute) > 1e-9 {
		t.Fatalf("spread-only moves created direction: %+v", last)
	}
}

func TestBayesianMultiscaleRegimeResetsOnMissingMinute(t *testing.T) {
	model := NewBayesianMultiscaleRegime(MultiscaleRegimeConfig{VolatilityWindow: 5, MinimumSamples: 5})
	at := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	model.ObserveMinute(at, 99, 101)
	for index := 0; index < 8; index++ {
		at = at.Add(time.Minute)
		model.ObserveMinute(at, 99-float64(index)*.01, 101-float64(index)*.01)
	}
	d := model.ObserveMinute(at.Add(2*time.Minute), 98, 100)
	if d.Healthy || d.Samples != 0 || model.samples != 0 {
		t.Fatalf("gap did not reset causal segment: decision=%+v samples=%d", d, model.samples)
	}
}
