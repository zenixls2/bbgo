package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

func TestBOCPD45CalibrationWaitsForMatureLabel(t *testing.T) {
	calibration := newBOCPD45PrequentialCalibration(bocpd45CalibrationPlatt)
	for i := 0; i < bocpd45CalibrationMinSamples-1; i++ {
		calibration.calibrator.update(0.8, 0)
	}
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	raw := bocpd45DirectionSnapshot{ready: true, upProbability: 0.8}
	calibration.observe(bboSnapshot{time: start, bid: 100, ask: 101}, raw, false, true)
	if got := calibration.calibrator.sampleCount(); got != bocpd45CalibrationMinSamples-1 {
		t.Fatalf("prediction must not train before maturity: samples=%d", got)
	}
	calibration.observe(bboSnapshot{time: start.Add(44 * time.Second), bid: 99, ask: 100}, raw, false, true)
	if got := calibration.calibrator.sampleCount(); got != bocpd45CalibrationMinSamples-1 {
		t.Fatalf("44-second label is not mature: samples=%d", got)
	}
	matured, label, ok := calibration.observe(
		bboSnapshot{time: start.Add(45 * time.Second), bid: 98, ask: 99}, raw, false, true)
	if !ok || label != 0 || matured == nil {
		t.Fatalf("expected mature down label, got pending=%+v label=%v ok=%v", matured, label, ok)
	}
	if math.Abs(matured.usedProbability-0.8) > 1e-12 {
		t.Fatalf("prediction was retroactively calibrated by its own label: %.12f", matured.usedProbability)
	}
	if got := calibration.calibrator.sampleCount(); got != bocpd45CalibrationMinSamples {
		t.Fatalf("mature label must update calibrator: samples=%d", got)
	}
	if calibration.pending == nil || !(calibration.pending.usedProbability < matured.usedProbability) {
		t.Fatalf("only the next prediction should see mature label: old=%.6f next=%+v", matured.usedProbability, calibration.pending)
	}
}

func TestBOCPD45CalibrationGapDiscardsUnobservableLabel(t *testing.T) {
	calibration := newBOCPD45PrequentialCalibration(bocpd45CalibrationPlatt)
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	raw := bocpd45DirectionSnapshot{ready: true, upProbability: 0.8}
	calibration.observe(bboSnapshot{time: start, bid: 100, ask: 101}, raw, false, true)
	calibration.observe(bboSnapshot{time: start.Add(time.Hour), bid: 90, ask: 91}, raw, true, true)
	if got := calibration.calibrator.sampleCount(); got != 0 {
		t.Fatalf("label spanning a data gap must be discarded: samples=%d", got)
	}
}

func TestBOCPD45PlattShrinksRepeatedOverconfidence(t *testing.T) {
	calibrator := newBOCPD45RollingCalibrator(bocpd45CalibrationPlatt)
	for i := 0; i < 200; i++ {
		label := 0.0
		if i%20 < 11 {
			label = 1
		}
		calibrator.update(0.8, label)
	}
	got := calibrator.predict(0.8)
	if got < 0.50 || got > 0.65 {
		t.Fatalf("expected 80%% raw forecast to shrink toward 55%% mature frequency, got %.6f", got)
	}
}

func TestBOCPD45CalibrationRollingWindowIsBounded(t *testing.T) {
	calibrator := newBOCPD45RollingCalibrator(bocpd45CalibrationBeta)
	for i := 0; i < bocpd45CalibrationWindow+50; i++ {
		calibrator.update(0.6, float64(i%2))
	}
	if got := calibrator.sampleCount(); got != bocpd45CalibrationWindow {
		t.Fatalf("rolling calibration samples=%d want=%d", got, bocpd45CalibrationWindow)
	}
}

func TestBOCPD45ResearchAndLivePlattArePointwiseEquivalent(t *testing.T) {
	live := gammacapture.NewBOCPD45Model(gammacapture.BOCPD45Config{
		Enabled: true, Calibration: "platt",
		Horizon: types.Duration(45 * time.Second), CalibrationWindow: types.Duration(6 * time.Hour),
		MinimumChanges: 8, MinimumSamples: 32, MaximumStates: 128, RefitEvery: 8, PriorStrength: 8,
	})
	researchDirection := bocpd45DirectionModel{}
	researchCalibration := newBOCPD45PrequentialCalibration(bocpd45CalibrationPlatt)
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	bid, ask := 300_000.0, 300_001.0
	for i := 0; i < 2_400; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		step := 1.0
		if i%137 >= 73 {
			step = -1
		}
		bid += step
		ask += step
		book := bboSnapshot{time: at, bid: bid, ask: ask}
		researchDirection.observe(at, bid, ask, false)
		raw := researchDirection.snapshot()
		researchCalibration.observe(book, raw, false, false)
		live.Observe(at, bid, ask, false)
		got := live.Snapshot()
		if raw.ready != got.Ready || math.Abs(raw.upProbability-got.RawUpProbability) > 1e-12 ||
			math.Abs(raw.confidence-got.Confidence) > 1e-12 {
			t.Fatalf("raw model diverged at %s: research=%+v live=%+v", at, raw, got)
		}
		wantProbability := researchCalibration.predict(raw.upProbability)
		if math.Abs(wantProbability-got.UpProbability) > 1e-12 {
			t.Fatalf("Platt map diverged at %s: research=%v live=%v", at, wantProbability, got.UpProbability)
		}
	}
	if got := live.Snapshot(); !got.CalibrationReady || got.CalibrationSamples < 32 {
		t.Fatalf("equivalence path did not mature Platt calibration: %+v", got)
	}
}
