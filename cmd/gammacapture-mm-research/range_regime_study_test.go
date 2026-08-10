package main

import (
	"math"
	"testing"
	"time"
)

func TestMeasureRangeRegimeSeparatesOscillationFromTrend(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	oscillating := make([]rangeRegimePoint, 49)
	trending := make([]rangeRegimePoint, 49)
	for i := range oscillating {
		at := start.Add(time.Duration(i) * 15 * time.Minute)
		oscillating[i] = rangeRegimePoint{At: at, Mid: 100 * math.Exp(0.01*math.Sin(float64(i)*8*math.Pi/47))}
		trending[i] = rangeRegimePoint{At: at, Mid: 100 * math.Exp(0.0005*float64(i))}
	}
	rangeWindow, ok := measureRangeRegime(oscillating, start, start.Add(12*time.Hour))
	if !ok || rangeWindow.EfficiencyRatio > 0.25 || rangeWindow.CenterCrossings < 2 {
		t.Fatalf("oscillating path was not selected: ok=%v window=%+v", ok, rangeWindow)
	}
	trendWindow, ok := measureRangeRegime(trending, start, start.Add(12*time.Hour))
	if ok || trendWindow.EfficiencyRatio < 0.9 {
		t.Fatalf("monotone trend was classified as ranging: ok=%v window=%+v", ok, trendWindow)
	}
}

func TestSelectNonOverlappingRangeWindows(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	candidates := []rangeRegimeWindow{
		{From: start, To: start.Add(12 * time.Hour), OscillationScore: 10},
		{From: start.Add(6 * time.Hour), To: start.Add(18 * time.Hour), OscillationScore: 20},
		{From: start.Add(18 * time.Hour), To: start.Add(30 * time.Hour), OscillationScore: 5},
	}
	got := selectNonOverlappingRangeWindows(candidates, 3)
	if len(got) != 2 || !got[0].From.Equal(start.Add(6*time.Hour)) ||
		!got[1].From.Equal(start.Add(18*time.Hour)) {
		t.Fatalf("unexpected non-overlapping selection: %+v", got)
	}
}
