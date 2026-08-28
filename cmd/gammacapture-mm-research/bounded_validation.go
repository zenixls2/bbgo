package main

import (
	"fmt"
	"os"
	"time"
)

// productionReplayValidationStage describes the permitted stage of a
// production-policy comparison. Calibration is a validity label for the
// resulting fill/P&L evidence, not a prerequisite for executing the replay.
type productionReplayValidationStage string

const (
	productionReplayValidationAuto        productionReplayValidationStage = "auto"
	productionReplayValidationCalibration productionReplayValidationStage = "calibration"
	productionReplayValidationFull        productionReplayValidationStage = "full"
)

func normalizeProductionReplayValidationStage(value string) (productionReplayValidationStage, error) {
	stage := productionReplayValidationStage(value)
	if stage == "" {
		stage = productionReplayValidationAuto
	}
	switch stage {
	case productionReplayValidationAuto, productionReplayValidationCalibration, productionReplayValidationFull:
		return stage, nil
	default:
		return "", fmt.Errorf("unsupported production replay validation stage %q (want auto, calibration, or full)", value)
	}
}

// shouldStopAfterCalibration is deliberately pure so the stage boundary can
// be tested without starting a replay. Only an explicitly requested
// calibration-only run stops here. An uncalibrated replay is still required to
// test the signal; it is simply marked ineligible for promotion below.
func shouldStopAfterCalibration(stage productionReplayValidationStage, _, _ bool) bool {
	return stage == productionReplayValidationCalibration
}

// selectProductionReplayQueue separates the queue estimate used to fit the
// calibration interval from the queue assumption used to execute a full
// replay. A failed fit must not leak its best-fit value into P&L results; an
// explicit queue is a declared model assumption and is therefore retained.
func selectProductionReplayQueue(calibrated float64, calibrationPassed, explicit bool) (float64, string) {
	if explicit {
		return calibrated, "explicit-queue-assumption"
	}
	if calibrationPassed {
		return calibrated, "private-fill-calibrated"
	}
	return 1, "neutral-prior-uncalibrated"
}

// productionReplayPreloadManifest is emitted with a replay report. It makes
// the preload/scoring boundary auditable instead of inferring it from a large
// JSON equity curve. Counts use half-open intervals:
// [warmupFrom, exactFrom), [exactFrom, scoreFrom), [scoreFrom, scoreTo).
type productionReplayPreloadManifest struct {
	Version                  int       `json:"version"`
	SimulationFrom           time.Time `json:"simulationFrom"`
	ExactFrom                time.Time `json:"exactFrom"`
	ScoreFrom                time.Time `json:"scoreFrom"`
	ScoreTo                  time.Time `json:"scoreTo"`
	WarmupBBOEvents          int       `json:"warmupBBOEvents"`
	CalibrationBBOEvents     int       `json:"calibrationBBOEvents"`
	ScoreBBOEvents           int       `json:"scoreBBOEvents"`
	WarmupTradeEvents        int       `json:"warmupTradeEvents"`
	CalibrationTradeEvents   int       `json:"calibrationTradeEvents"`
	ScoreTradeEvents         int       `json:"scoreTradeEvents"`
	BBOInterval              string    `json:"bboInterval"`
	CacheHit                 bool      `json:"cacheHit"`
	PreloadOptimizerSkipped  bool      `json:"preloadOptimizerSkipped"`
	ChronologicalOrderPassed bool      `json:"chronologicalOrderPassed"`
}

func buildProductionReplayPreloadManifest(books []bboSnapshot, trades []tick, simulationFrom, exactFrom, scoreFrom, scoreTo time.Time, bboInterval time.Duration, cacheHit bool) (productionReplayPreloadManifest, error) {
	if !simulationFrom.Before(exactFrom) || !exactFrom.Before(scoreTo) || scoreFrom.Before(exactFrom) || !scoreFrom.Before(scoreTo) {
		return productionReplayPreloadManifest{}, fmt.Errorf("invalid preload boundary: simulation=%s exact=%s score=%s--%s", simulationFrom, exactFrom, scoreFrom, scoreTo)
	}
	if !replaySnapshotsChronological(books) || !replayTradesChronological(trades) {
		return productionReplayPreloadManifest{}, fmt.Errorf("replay input is not chronological")
	}
	for _, book := range books {
		if book.time.Before(simulationFrom) || !book.time.Before(scoreTo) {
			return productionReplayPreloadManifest{}, fmt.Errorf("BBO event %s lies outside preload/score range", book.time)
		}
	}
	for _, trade := range trades {
		if trade.time.Before(simulationFrom) || !trade.time.Before(scoreTo) {
			return productionReplayPreloadManifest{}, fmt.Errorf("trade event %s lies outside preload/score range", trade.time)
		}
	}

	manifest := productionReplayPreloadManifest{
		Version: 1, SimulationFrom: simulationFrom, ExactFrom: exactFrom,
		ScoreFrom: scoreFrom, ScoreTo: scoreTo, BBOInterval: bboInterval.String(),
		CacheHit: cacheHit, PreloadOptimizerSkipped: true,
		ChronologicalOrderPassed: true,
	}
	for _, book := range books {
		switch {
		case book.time.Before(exactFrom):
			manifest.WarmupBBOEvents++
		case book.time.Before(scoreFrom):
			manifest.CalibrationBBOEvents++
		default:
			manifest.ScoreBBOEvents++
		}
	}
	for _, trade := range trades {
		switch {
		case trade.time.Before(exactFrom):
			manifest.WarmupTradeEvents++
		case trade.time.Before(scoreFrom):
			manifest.CalibrationTradeEvents++
		default:
			manifest.ScoreTradeEvents++
		}
	}
	return manifest, nil
}

func replaySnapshotsChronological(values []bboSnapshot) bool {
	for i := 1; i < len(values); i++ {
		if values[i].time.Before(values[i-1].time) {
			return false
		}
	}
	return true
}

func replayTradesChronological(values []tick) bool {
	for i := 1; i < len(values); i++ {
		if values[i].time.Before(values[i-1].time) {
			return false
		}
	}
	return true
}

// runWithWallClockLimit is a process-level safety net around the existing
// synchronous simulator. It avoids threading context cancellation through the
// 230KB replay kernel. On timeout the research process exits, so a stuck arm
// cannot continue consuming CPU in the background or produce a partial report.
func runWithWallClockLimit(limit time.Duration, fn func()) {
	if limit <= 0 {
		fn()
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		fmt.Fprintf(os.Stderr, "production replay exceeded wall-clock limit %s; terminating without a partial result\n", limit)
		os.Exit(124)
	}
}
