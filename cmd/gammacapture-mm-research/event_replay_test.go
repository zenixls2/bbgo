package main

import (
	"testing"
	"time"
)

func TestSummarizeHorizonExcursionsUsesFutureRange(t *testing.T) {
	start := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	mids := []float64{100, 100.05, 100.10, 100.25, 100.02, 99.98, 99.70}
	bbo := make([]bboSnapshot, 0, len(mids))
	for i, mid := range mids {
		bbo = append(bbo, bboSnapshot{
			time: start.Add(time.Duration(i) * time.Minute),
			bid:  mid - 0.01, ask: mid + 0.01,
		})
	}
	stats := summarizeHorizonExcursions(bbo, 20)
	if len(stats) != 5 {
		t.Fatalf("expected five horizons, got %d", len(stats))
	}
	var fiveMinute horizonExcursionStats
	for _, s := range stats {
		if s.HorizonMinutes == 5 {
			fiveMinute = s
		}
	}
	if fiveMinute.Samples == 0 || fiveMinute.UpCrossFraction == 0 || fiveMinute.DownCrossFraction == 0 {
		t.Fatalf("five-minute future range should detect both 20bps excursions: %+v", fiveMinute)
	}
	selected, score := selectBestHorizon(stats, 20, 20)
	if selected < 5 || score <= 0 {
		t.Fatalf("expected fee-adjusted horizon selection: minutes=%d score=%.2f stats=%+v", selected, score, stats)
	}
}
