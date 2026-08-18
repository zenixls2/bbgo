package main

import (
	"math"
	"testing"
	"time"
)

func TestGaussianVarianceQLIKEPrefersCorrectVariance(t *testing.T) {
	actual := 25.0
	correct := gaussianVarianceQLIKE(25, actual)
	for _, wrong := range []float64{5, 125} {
		if got := gaussianVarianceQLIKE(wrong, actual); got <= correct {
			t.Fatalf("wrong variance unexpectedly beat correct variance: wrong=%v got=%v correct=%v", wrong, got, correct)
		}
	}
}

func TestVarianceQLIKEScaleIsDelayedAndCalibrates(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	bias := varianceQLIKEScale{halfLife: 6 * time.Hour}
	raw := [3]float64{math.Log(4), math.Log(4), math.Log(4)}
	actual := [3]float64{16, 16, 16}
	for i := 0; i < 29; i++ {
		bias.update(start.Add(time.Duration(i)*time.Minute), raw, actual)
	}
	if _, ready := bias.predict(raw); ready {
		t.Fatal("variance calibration became ready before 30 matured labels")
	}
	bias.update(start.Add(29*time.Minute), raw, actual)
	prediction, ready := bias.predict(raw)
	if !ready {
		t.Fatal("variance calibration did not become ready after 30 matured labels")
	}
	for _, value := range prediction {
		if math.Abs(value-16) > 1e-9 {
			t.Fatalf("unexpected calibrated variance: got=%v want=16", value)
		}
	}
}

func TestVarianceHelpersAreFiniteAndSideSymmetric(t *testing.T) {
	for _, logVariance := range []float64{-1e9, 0, 1e9} {
		value := varianceFromLog(logVariance)
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("invalid variance for log input %v: %v", logVariance, value)
		}
	}
	observation := volumeProfileObservation{buyMeanReturnBps: 7, sellMeanReturnBps: -3}
	forward := terminalResidualSquares([3]float64{0, 2, -1}, observation)
	reflected := terminalResidualSquares([3]float64{0, 1, -2},
		volumeProfileObservation{buyMeanReturnBps: 3, sellMeanReturnBps: -7})
	if forward[0] != reflected[0] || forward[1] != reflected[2] || forward[2] != reflected[1] {
		t.Fatalf("BUY/SELL reflection changed variance targets: forward=%v reflected=%v", forward, reflected)
	}
}
