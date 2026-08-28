package main

import (
	"testing"
	"time"
)

func TestNormalizeProductionReplayValidationStage(t *testing.T) {
	for _, value := range []string{"", "auto", "calibration", "full"} {
		if _, err := normalizeProductionReplayValidationStage(value); err != nil {
			t.Fatalf("stage %q should be accepted: %v", value, err)
		}
	}
	if _, err := normalizeProductionReplayValidationStage("component"); err == nil {
		t.Fatal("unsupported validation stage should be rejected")
	}
}

func TestShouldStopAfterCalibration(t *testing.T) {
	if shouldStopAfterCalibration(productionReplayValidationAuto, false, false) {
		t.Fatal("auto stage must continue replay after failed calibration")
	}
	if shouldStopAfterCalibration(productionReplayValidationAuto, true, false) {
		t.Fatal("auto stage must continue after passed calibration")
	}
	if shouldStopAfterCalibration(productionReplayValidationFull, false, false) {
		t.Fatal("full stage must not be gated by calibration")
	}
	if shouldStopAfterCalibration(productionReplayValidationFull, false, true) {
		t.Fatal("full stage must continue regardless of calibration override")
	}
	if !shouldStopAfterCalibration(productionReplayValidationCalibration, true, true) {
		t.Fatal("calibration stage must always terminate after calibration")
	}
}

func TestSelectProductionReplayQueueDoesNotUseFailedFit(t *testing.T) {
	if got, source := selectProductionReplayQueue(16, false, false); got != 1 || source != "neutral-prior-uncalibrated" {
		t.Fatalf("failed calibration must use neutral prior: got=%v source=%q", got, source)
	}
	if got, source := selectProductionReplayQueue(2, true, false); got != 2 || source != "private-fill-calibrated" {
		t.Fatalf("passed calibration must use fitted queue: got=%v source=%q", got, source)
	}
	if got, source := selectProductionReplayQueue(0, false, true); got != 0 || source != "explicit-queue-assumption" {
		t.Fatalf("explicit queue must remain an assumption: got=%v source=%q", got, source)
	}
}

func TestBuildProductionReplayPreloadManifest(t *testing.T) {
	start := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	exact := start.Add(time.Hour)
	score := exact.Add(time.Hour)
	end := score.Add(time.Hour)
	books := []bboSnapshot{
		{time: start.Add(10 * time.Minute)},
		{time: exact.Add(10 * time.Minute)},
		{time: score.Add(10 * time.Minute)},
	}
	trades := []tick{
		{time: start.Add(20 * time.Minute)},
		{time: exact.Add(20 * time.Minute)},
		{time: score.Add(20 * time.Minute)},
	}
	manifest, err := buildProductionReplayPreloadManifest(
		books, trades, start, exact, score, end, 10*time.Second, true)
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	if manifest.Version != 1 || !manifest.CacheHit || !manifest.PreloadOptimizerSkipped || !manifest.ChronologicalOrderPassed {
		t.Fatalf("unexpected manifest metadata: %+v", manifest)
	}
	if manifest.WarmupBBOEvents != 1 || manifest.CalibrationBBOEvents != 1 || manifest.ScoreBBOEvents != 1 {
		t.Fatalf("unexpected BBO partition: %+v", manifest)
	}
	if manifest.WarmupTradeEvents != 1 || manifest.CalibrationTradeEvents != 1 || manifest.ScoreTradeEvents != 1 {
		t.Fatalf("unexpected trade partition: %+v", manifest)
	}
}

func TestBuildProductionReplayPreloadManifestRejectsUnorderedInput(t *testing.T) {
	start := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	exact := start.Add(time.Hour)
	score := exact.Add(time.Hour)
	end := score.Add(time.Hour)
	_, err := buildProductionReplayPreloadManifest(
		[]bboSnapshot{{time: exact}, {time: start}}, nil,
		start, exact, score, end, 0, false)
	if err == nil {
		t.Fatal("unordered BBO input should be rejected")
	}
}
