package main

import (
	"testing"
	"time"
)

func TestExecutableFirstPassageUsesExecutableSidesAndFees(t *testing.T) {
	start := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	down := []minuteRegimeClose{
		{at: start, bid: 100, ask: 100.1},
		// Mid is lower, but the ask has not cleared the 20-bps down barrier.
		{at: start.Add(time.Minute), bid: 99.85, ask: 99.91},
		{at: start.Add(2 * time.Minute), bid: 99.7, ask: 99.79},
	}
	if got := executableFirstPassage(down, 0, 2, 20); got != 1 {
		t.Fatalf("down-first outcome = %d, want 1", got)
	}

	up := []minuteRegimeClose{
		{at: start, bid: 100, ask: 100.1},
		{at: start.Add(time.Minute), bid: 100.2, ask: 100.3},
		{at: start.Add(2 * time.Minute), bid: 100.31, ask: 100.4},
	}
	if got := executableFirstPassage(up, 0, 2, 20); got != -1 {
		t.Fatalf("up-first outcome = %d, want -1", got)
	}

	censored := []minuteRegimeClose{
		{at: start, bid: 100, ask: 100.1},
		{at: start.Add(time.Minute), bid: 100.05, ask: 100.15},
		{at: start.Add(2 * time.Minute), bid: 100.08, ask: 100.18},
	}
	if got := executableFirstPassage(censored, 0, 2, 20); got != 0 {
		t.Fatalf("censored outcome = %d, want 0", got)
	}
}

func TestMinuteRegimeClosesUsesLastBBOInMinute(t *testing.T) {
	start := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{
		{time: start.Add(time.Second), bid: 100, ask: 101},
		{time: start.Add(59 * time.Second), bid: 102, ask: 103},
		{time: start.Add(time.Minute + time.Second), bid: 104, ask: 105},
	}
	closes := minuteRegimeCloses(books)
	if len(closes) != 2 {
		t.Fatalf("minute closes = %d, want 2", len(closes))
	}
	if closes[0].bid != 102 || closes[0].ask != 103 {
		t.Fatalf("first minute did not retain final BBO: %+v", closes[0])
	}
}
