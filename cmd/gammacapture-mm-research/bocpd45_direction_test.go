package main

import (
	"math"
	"testing"
	"time"
)

func TestBOCPD45DirectionIsCausalSymmetricAndGapReset(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	up, down := &bocpd45DirectionModel{}, &bocpd45DirectionModel{}
	bidUp, askUp := 100.0, 100.1
	bidDown, askDown := 100.0, 100.1
	up.observe(start, bidUp, askUp, false)
	down.observe(start, bidDown, askDown, false)
	for i := 1; i <= 16; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		bidUp, askUp = bidUp+0.1, askUp+0.1
		bidDown, askDown = bidDown-0.1, askDown-0.1
		up.observe(at, bidUp, askUp, false)
		down.observe(at, bidDown, askDown, false)
	}
	bull, bear := up.snapshot(), down.snapshot()
	if !bull.ready || !bear.ready || bull.direction <= 0 || bear.direction >= 0 {
		t.Fatalf("expected ready symmetric signs: bull=%+v bear=%+v", bull, bear)
	}
	if math.Abs(bull.direction+bear.direction) > 1e-12 || math.Abs(bull.confidence-bear.confidence) > 1e-12 {
		t.Fatalf("inverted executable paths must be symmetric: bull=%+v bear=%+v", bull, bear)
	}
	up.observe(start.Add(time.Minute), bidUp, askUp, true)
	if got := up.snapshot(); got.ready {
		t.Fatalf("gap must reset readiness, got %+v", got)
	}
}
