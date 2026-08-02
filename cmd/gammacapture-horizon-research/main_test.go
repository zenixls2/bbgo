package main

import (
	"math"
	"testing"
	"time"
)

func TestFitLogisticLearnsCausalFeatureDirection(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]minuteBar, 1000)
	features := make([]featureRow, len(bars))
	labels := make([]float64, len(bars))
	for i := range bars {
		bars[i] = minuteBar{at: start.Add(time.Duration(i) * time.Minute), high: 100, low: 100, close: 100}
		features[i].valid = true
		if (i/evaluationStep)%2 == 0 {
			features[i].values[0] = -2
			labels[i] = 0
		} else {
			features[i].values[0] = 2
			labels[i] = 1
		}
	}
	model := fitLogistic(features, labels, bars, start, start.Add(1000*time.Minute), 0)
	low := predictLogistic(model, featureRow{valid: true, values: [featureCount]float64{-2}})
	high := predictLogistic(model, featureRow{valid: true, values: [featureCount]float64{2}})
	if low >= 0.1 || high <= 0.9 {
		t.Fatalf("model did not learn separation: low=%.6f high=%.6f", low, high)
	}
}

func TestPathLabelsRejectCaptureGap(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]minuteBar, 20)
	for i := range bars {
		price := 100 * math.Exp(float64(i)/10_000)
		bars[i] = minuteBar{at: start.Add(time.Duration(i) * time.Minute), high: price, low: price, close: price}
	}
	bars[5] = minuteBar{at: bars[5].at}
	up, down := pathLabels(bars, 10)
	if finite(up[0]) || finite(down[0]) {
		t.Fatal("window crossing a missing BBO minute must be censored")
	}
	if !finite(up[6]) || !finite(down[6]) {
		t.Fatal("continuous window after the gap should remain usable")
	}
}

func TestSolveLinear(t *testing.T) {
	solution, ok := solveLinear([][]float64{{2, 1}, {1, 3}}, []float64{5, 6})
	if !ok || math.Abs(solution[0]-1.8) > 1e-12 || math.Abs(solution[1]-1.4) > 1e-12 {
		t.Fatalf("unexpected solution: %v ok=%v", solution, ok)
	}
}
