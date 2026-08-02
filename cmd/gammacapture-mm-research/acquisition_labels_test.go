package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestBuildChaseObservationsUsesNonOverlappingFeeInclusiveLabels(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	books := make([]bboSnapshot, 0, 31)
	for minute := 0; minute <= 30; minute++ {
		bid, ask := 100.0, 100.1
		if minute == 15 {
			bid, ask = 100.4, 100.5
		}
		books = append(books, bboSnapshot{time: start.Add(time.Duration(minute) * time.Minute), bid: bid, ask: ask, bidSize: 1, askSize: 1})
	}
	trades := make([]tick, 0, 10)
	for i := 0; i < 10; i++ {
		trades = append(trades, tick{time: start.Add(time.Duration(i+1) * time.Minute), price: 100, size: 1, side: types.SideTypeBuy})
	}
	in := acquisitionLabelInput{From: start, To: start.Add(31 * time.Minute), Horizon: 10 * time.Minute,
		MakerFeeBps: 10, TakerFeeBps: 10, SlippageBps: 5, AdverseBps: 2, MinimumNetEdgeBps: 2}
	observations := buildChaseObservations(books, trades, in)
	if len(observations) != 2 {
		t.Fatalf("expected two non-overlapping labels, got %d", len(observations))
	}
	if !observations[0].Hit || observations[0].HitLatencyMinutes != 5 {
		t.Fatalf("first observation should clear the full 29-bps threshold: %+v", observations[0])
	}
	if observations[1].Hit {
		t.Fatalf("second non-overlapping horizon must not reuse the earlier price move: %+v", observations[1])
	}
}

func TestStableSelectionCannotSeeFinalTestOutcomes(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	makeObservations := func(finalHit bool) []chaseObservation {
		observations := make([]chaseObservation, 0, 600)
		for i := 0; i < 600; i++ {
			hit := i < 500 && i%10 == 0
			if i >= 500 {
				hit = finalHit
			}
			observations = append(observations, chaseObservation{
				At: start.Add(time.Duration(i) * time.Minute), Hit: hit,
				Return1mBps: 5, Return5mBps: 25, Return10mBps: 25,
				TradeImbalance5m: .25, TradeCount5m: 40,
			})
		}
		return observations
	}
	boundary := start.Add(500 * time.Minute)
	allFinalHits := buildStableSelectionReport(makeObservations(true), boundary)
	noFinalHits := buildStableSelectionReport(makeObservations(false), boundary)
	if allFinalHits == nil || noFinalHits == nil {
		t.Fatal("expected stable selection reports")
	}
	if !reflect.DeepEqual(allFinalHits.Selected.Rule, noFinalHits.Selected.Rule) {
		t.Fatalf("untouched outcomes changed selected thresholds: withHits=%+v withoutHits=%+v", allFinalHits.Selected.Rule, noFinalHits.Selected.Rule)
	}
	if allFinalHits.SelectedFinalTest.Hits == noFinalHits.SelectedFinalTest.Hits {
		t.Fatal("test setup must produce different final outcomes")
	}
}
