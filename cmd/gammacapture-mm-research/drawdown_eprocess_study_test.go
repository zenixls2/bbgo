package main

import (
	"math"
	"testing"
	"time"
)

func TestEvaluateDrawdownEProcessLabelsCausalExecutablePassage(t *testing.T) {
	start := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	price := 300_000.0
	closes := make([]minuteRegimeClose, 0, 180)
	for minute := 0; minute < 180; minute++ {
		switch {
		case minute < 70:
			price *= math.Exp(-.00009)
		case minute < 130:
			price *= math.Exp(.00011)
		default:
			price *= math.Exp(.00001 * math.Sin(float64(minute)))
		}
		closes = append(closes, minuteRegimeClose{
			at:  start.Add(time.Duration(minute) * time.Minute),
			bid: price - 5, ask: price + 5,
		})
	}
	report := evaluateDrawdownEProcess(closes, drawdownEProcessStudyInput{
		Symbol: "ETHJPY", From: start, To: start.Add(180 * time.Minute),
		Horizon: 30 * time.Minute, RoundTripCostBps: 20,
		BarrierWidth: .001, ConfidenceZ: 1.645,
		Windows: []time.Duration{10 * time.Minute, 15 * time.Minute, 30 * time.Minute},
	})
	if report.Signals == 0 || report.DownFirst == 0 {
		t.Fatalf("persistent decline did not create a down-first signal: %+v", report)
	}
	if report.Recoveries == 0 {
		t.Fatalf("persistent recovery did not resolve the drawdown episode: %+v", report)
	}
	for _, signal := range report.SignalsDetail {
		if signal.At.Before(start) || signal.PosteriorDiagnostic <= .5 || signal.EValue < report.Threshold {
			t.Fatalf("invalid causal signal: %+v", signal)
		}
	}
}

func TestExecutableFirstPassageWithStepRejectsGap(t *testing.T) {
	start := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	closes := []minuteRegimeClose{
		{at: start, bid: 100, ask: 101},
		{at: start.Add(2 * time.Minute), bid: 90, ask: 91},
	}
	if outcome, step := executableFirstPassageWithStep(closes, 0, 1, 20); outcome != 0 || step != 0 {
		t.Fatalf("gap produced a first-passage label: outcome=%d step=%d", outcome, step)
	}
}
