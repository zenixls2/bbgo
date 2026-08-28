package main

import (
	"math"
	"testing"
	"time"
)

func TestFirstExecutablePivotOutcomeUsesFirstBarrier(t *testing.T) {
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{
		{time: start, bid: 100, ask: 100.01},
		{time: start.Add(5 * time.Minute), bid: 100.27, ask: 100.28},
		{time: start.Add(10 * time.Minute), bid: 99.80, ask: 99.81},
	}
	if got, ok := firstExecutablePivotOutcome(books, 0, 15*time.Minute, 5*time.Minute, 20); !ok || got != 1 {
		t.Fatalf("expected first up pivot, got outcome=%d resolved=%v", got, ok)
	}
}

func TestFirstExecutablePivotOutcomeCensorsMissingPath(t *testing.T) {
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{
		{time: start, bid: 100, ask: 100.01},
		{time: start.Add(20 * time.Minute), bid: 100.30, ask: 100.31},
	}
	if got, ok := firstExecutablePivotOutcome(books, 0, 30*time.Minute, 5*time.Minute, 20); ok || got != 0 {
		t.Fatalf("missing path must be censored, got outcome=%d resolved=%v", got, ok)
	}
}

func TestPivotThresholdBreakEvenProbability(t *testing.T) {
	if got := pivotBreakEvenProbability(26, 20); math.Abs(got-0.8846153846153846) > 1e-12 {
		t.Fatalf("unexpected 26 bps break-even probability: %.12f", got)
	}
	if got := pivotBreakEvenProbability(50, 20); math.Abs(got-0.7) > 1e-12 {
		t.Fatalf("unexpected 50 bps break-even probability: %.12f", got)
	}
}

func TestPivotThresholdFitRejectsFlatOrAdverseScore(t *testing.T) {
	points := make([]pivotThresholdFitPoint, 0, 100)
	for i := 0; i < 100; i++ {
		x := float64(i%5) / 4
		y := 0.0
		if i%2 == 0 {
			y = 1
		}
		points = append(points, pivotThresholdFitPoint{x: x, y: y})
	}
	model := fitPivotThresholdLogistic(points)
	if model.Samples != len(points) || !finiteRegimeStudyValue(model.LogLoss) {
		t.Fatalf("invalid fitted model: %+v", model)
	}
	if pivotThresholdForProbability(model.Intercept, model.Slope, 0.884) < 0.99 {
		t.Fatalf("flat score should not imply a low economic threshold: %+v", model)
	}
}
