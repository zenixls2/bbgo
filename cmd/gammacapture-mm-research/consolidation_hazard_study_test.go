package main

import (
	"math"
	"testing"
	"time"
)

func syntheticConsolidationCloses(scale float64) []minuteRegimeClose {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	closes := make([]minuteRegimeClose, 61)
	price := 300_000 * scale
	for i := range closes {
		if i <= 50 {
			price *= math.Exp(-0.00008)
		} else if i%2 == 0 {
			price *= math.Exp(0.00003)
		} else {
			price *= math.Exp(-0.00003)
		}
		closes[i] = minuteRegimeClose{
			at:  start.Add(time.Duration(i) * time.Minute),
			bid: price - 0.5*scale, ask: price + 0.5*scale,
		}
	}
	return closes
}

func TestConsolidationFeatureRequiresDownLegAndShortChop(t *testing.T) {
	feature, ok := consolidationFeatureAt(syntheticConsolidationCloses(1), 60, 10, 30)
	if !ok {
		t.Fatal("persistent down leg followed by alternating short chop was not recognized")
	}
	if feature.longReturn >= 0 || feature.efficiencyRatio > 1 {
		t.Fatalf("unexpected consolidation feature: %+v", feature)
	}
}

func TestConsolidationFeatureIsPriceScaleInvariant(t *testing.T) {
	a, okA := consolidationFeatureAt(syntheticConsolidationCloses(1), 60, 10, 30)
	b, okB := consolidationFeatureAt(syntheticConsolidationCloses(1000), 60, 10, 30)
	if !okA || !okB {
		t.Fatalf("scaled path lost feature eligibility: okA=%v okB=%v", okA, okB)
	}
	av, bv := hazardFeatureVector(a), hazardFeatureVector(b)
	for i := range av {
		if math.Abs(av[i]-bv[i]) > 1e-9 {
			t.Fatalf("feature %d is price-scale dependent: %.12g versus %.12g", i, av[i], bv[i])
		}
	}
}

func TestConsolidationOutcomeUsesExecutableSidesAndCompleteCost(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	closes := make([]minuteRegimeClose, 17)
	for i := range closes {
		mid := 100_000 * math.Exp(-0.0002*float64(i))
		closes[i] = minuteRegimeClose{at: start.Add(time.Duration(i) * time.Minute), bid: mid - 5, ask: mid + 5}
	}
	outcome, _, down, passage := consolidationOutcome(closes, 0, 15, 26)
	if outcome != 1 || down <= 0 || passage <= 0 {
		t.Fatalf("executable decline did not clear complete cost: outcome=%d down=%g passage=%d", outcome, down, passage)
	}
}

func TestNearestHazardKeepsCensorMassInSimplex(t *testing.T) {
	feature := consolidationHazardFeature{longZ: -1, shortZ: 0, efficiencyRatio: 0.5}
	samples := []consolidationHazardSample{
		{feature: feature, outcome: 1, downNetExcursion: 0.01},
		{feature: feature, outcome: -1, upNetExcursion: 0.01},
		{feature: feature, outcome: 0},
	}
	prediction, ok := nearestConsolidationHazards(samples, feature, 1.645)
	if !ok {
		t.Fatal("hazard posterior unavailable")
	}
	total := prediction.DownProbability + prediction.UpProbability + prediction.CensorProbability
	if math.Abs(total-1) > 1e-12 || prediction.CensorProbability <= 0 {
		t.Fatalf("competing-risk simplex lost censor mass: %+v", prediction)
	}
}
