//go:build ignore

package main

import "testing"

func TestOnlineBayesianOutcomeLearnsContinuationSign(t *testing.T) {
	model := newOnlineBayesianOutcome()
	down := [multiscaleOutcomeFeatures]float64{1, 2, 0, 0}
	up := [multiscaleOutcomeFeatures]float64{1, -2, 0, 0}
	for index := 0; index < 40; index++ {
		model.update(down, 1)
		model.update(up, 0)
	}
	if probability := model.predict(down); probability < .8 {
		t.Fatalf("learned continuation probability = %.6f, want >= .8", probability)
	}
	if probability := model.predict(up); probability > .2 {
		t.Fatalf("learned up-state down probability = %.6f, want <= .2", probability)
	}
}

func TestOnlineBayesianOutcomeCanLearnMeanReversion(t *testing.T) {
	model := newOnlineBayesianOutcome()
	downRegime := [multiscaleOutcomeFeatures]float64{1, 2, 0, 0}
	upRegime := [multiscaleOutcomeFeatures]float64{1, -2, 0, 0}
	for index := 0; index < 40; index++ {
		model.update(downRegime, 0)
		model.update(upRegime, 1)
	}
	if probability := model.predict(downRegime); probability > .2 {
		t.Fatalf("mean-reversion down probability = %.6f, want <= .2", probability)
	}
	if probability := model.predict(upRegime); probability < .8 {
		t.Fatalf("mean-reversion up-regime probability = %.6f, want >= .8", probability)
	}
}

func TestOnlineBayesianOutcomeStartsSymmetric(t *testing.T) {
	model := newOnlineBayesianOutcome()
	features := [multiscaleOutcomeFeatures]float64{1, 3, 1, 3}
	if probability := model.predict(features); probability != .5 {
		t.Fatalf("zero-mean prior probability = %.6f, want .5", probability)
	}
}
