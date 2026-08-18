package main

import (
	"math"
	"testing"
	"time"
)

func TestScalarEWRegressionLearnsOnlyAfterEnoughLabels(t *testing.T) {
	model := scalarEWRegression{dim: 1, halfLife: time.Hour}
	x := [alphaFeatureDim]float64{1}
	if _, ok := model.predict(x); ok {
		t.Fatal("empty model must not predict")
	}
	for i := 0; i < 3; i++ {
		model.update(time.Unix(int64(i), 0), x, 2)
	}
	if _, ok := model.predict(x); ok {
		t.Fatal("model must remain cold before four effective samples")
	}
	model.update(time.Unix(3, 0), x, 2)
	value, ok := model.predict(x)
	if !ok || math.Abs(value-1.6) > 0.5 {
		t.Fatalf("unexpected ridge prediction: value=%v ready=%v", value, ok)
	}
}

func TestAlphaFeaturesPreserveBaselineAndBoundedVP(t *testing.T) {
	var baseline, volume [volumeProfileRegressionMaxFeatures]float64
	baseline[0], baseline[1], baseline[2] = 1, 0.25, -0.5
	for i := range volume {
		volume[i] = float64(i)
	}
	features := alphaFeatures(baseline, volume)
	if features[0] != 1 || features[1] != 0.25 || features[2] != -0.5 {
		t.Fatalf("baseline coordinates were not preserved: %+v", features)
	}
	for i := 3; i < alphaFeatureDim; i++ {
		if features[i] != volume[i-3] {
			t.Fatalf("unexpected VP coordinate at %d: got=%v want=%v", i, features[i], volume[i-3])
		}
	}
}

func TestAlphaVarianceQLIKEIsMinimizedAtActualVariance(t *testing.T) {
	actual := 25.0
	truth := alphaVarianceQLIKE(actual, actual)
	if alphaVarianceQLIKE(5, actual) <= truth || alphaVarianceQLIKE(100, actual) <= truth {
		t.Fatalf("QLIKE must prefer the actual variance: truth=%v low=%v high=%v", truth, alphaVarianceQLIKE(5, actual), alphaVarianceQLIKE(100, actual))
	}
}

func TestMakerIOCVariantDoesNotCreateAnActionOnNeutralColdData(t *testing.T) {
	observations := make([]volumeProfileObservation, 0, 24)
	for i := 0; i < cap(observations); i++ {
		at := time.Date(2026, time.August, 1, 0, i, 0, 0, time.UTC)
		observations = append(observations, volumeProfileObservation{
			at: at, maturity: at.Add(15 * time.Minute),
			baseline: [volumeProfileRegressionMaxFeatures]float64{1},
			volume:   [volumeProfileRegressionMaxFeatures]float64{},
		})
	}
	report := scoreMakerIOCVariant(observations, 15*time.Minute, 15)
	if report.ActionChanges != 0 || report.SelectedUtilityBps != 0 {
		t.Fatalf("neutral data must not invent actions: %+v", report)
	}
}
