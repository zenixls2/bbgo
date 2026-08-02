package main

import "testing"

func TestAdaptiveEarlyBumpOutcomeIndexUsesForecastAndSpreadCap(t *testing.T) {
	deltas := []float64{0, 3, 5, 7, 10, 15, 20, 25, 30}
	model := fittedEarlyBumpMomentum{
		means:        make([]float64, len(earlyBumpMomentumFeatureNames)),
		scales:       makeUnitSlice(len(earlyBumpMomentumFeatureNames)),
		coefficients: append([]float64{20}, make([]float64, len(earlyBumpMomentumFeatureNames))...),
	}

	event := earlyBumpEvent{InsideDeltaBps: 7}
	if got := adaptiveEarlyBumpOutcomeIndex(event, model, 0, deltas); got != 3 {
		t.Fatalf("spread cap should select +7 bps, got index %d (+%g bps)", got, deltas[got])
	}

	event.InsideDeltaBps = 30
	model.residualStd = 10
	if got := adaptiveEarlyBumpOutcomeIndex(event, model, 1, deltas); got != 4 {
		t.Fatalf("confidence-adjusted forecast should select +10 bps, got index %d (+%g bps)", got, deltas[got])
	}
}

func TestAdaptiveEarlyBumpOutcomeIndexFailsClosedOnNegativeForecast(t *testing.T) {
	deltas := []float64{0, 3, 5, 7, 10}
	model := fittedEarlyBumpMomentum{
		means:        make([]float64, len(earlyBumpMomentumFeatureNames)),
		scales:       makeUnitSlice(len(earlyBumpMomentumFeatureNames)),
		coefficients: append([]float64{-5}, make([]float64, len(earlyBumpMomentumFeatureNames))...),
	}

	if got := adaptiveEarlyBumpOutcomeIndex(earlyBumpEvent{InsideDeltaBps: 10}, model, 0, deltas); got != 0 {
		t.Fatalf("negative momentum forecast should keep baseline delta, got index %d", got)
	}
}

func makeUnitSlice(n int) []float64 {
	values := make([]float64, n)
	for i := range values {
		values[i] = 1
	}
	return values
}
