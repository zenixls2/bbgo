package main

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestSimulateEarlyBumpChaseReevaluatesWithinEpisode(t *testing.T) {
	start := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	books := make([]bboSnapshot, 901)
	for i := range books {
		books[i] = bboSnapshot{time: start.Add(time.Duration(i) * time.Second), bid: 100, ask: 100.02, bidSize: 1, askSize: 1}
	}
	episodeAt := start.Add(5 * time.Minute)
	trades := []tick{{time: episodeAt.Add(12 * time.Second), price: 99.95, size: 1, side: types.SideTypeSell}}
	model := fittedEarlyBumpMomentum{
		means:        make([]float64, len(earlyBumpMomentumFeatureNames)),
		scales:       makeUnitSlice(len(earlyBumpMomentumFeatureNames)),
		coefficients: append([]float64{10}, make([]float64, len(earlyBumpMomentumFeatureNames))...),
	}
	input := earlyBumpStudyInput{
		BaseDistanceBps: 30, MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		LockDuration: 30 * time.Second, EscapeHorizon: 2 * time.Minute, MarkoutHorizon: 10 * time.Minute,
		EscapeBarrierBps: 10, AdverseBarrierBps: 10,
	}
	result := simulateEarlyBumpChase(books, trades, earlyBumpEvent{At: episodeAt}, input, model,
		earlyBumpChasePolicy{CadenceSeconds: 5, MinimumAmendBps: 1})

	if result.BaselineFilled {
		t.Fatal("baseline bid should remain below the public sell")
	}
	if !result.Filled || result.Amendments != 3 {
		t.Fatalf("expected the third cadence-aligned amendment to fill, got filled=%v amendments=%d", result.Filled, result.Amendments)
	}
	if result.FillAt != trades[0].time {
		t.Fatalf("unexpected fill time %s", result.FillAt)
	}
}

func TestChaseFirstSellFillHonorsActiveInterval(t *testing.T) {
	start := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	trades := []tick{
		{time: start.Add(time.Second), price: 99, side: types.SideTypeSell},
		{time: start.Add(3 * time.Second), price: 99, side: types.SideTypeSell},
	}
	fillAt, filled := chaseFirstSellFill(trades, start.Add(2*time.Second), start.Add(4*time.Second), 100)
	if !filled || fillAt != trades[1].time {
		t.Fatalf("queue reset must ignore pre-amend trades, got filled=%v at=%s", filled, fillAt)
	}
}
