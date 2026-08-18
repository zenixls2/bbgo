package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestCompetingMarginalBaselineIsAValidDistribution(t *testing.T) {
	baseline := competingMarginalBaseline{total: 8, buy: 4, sell: 5, both: 3}
	probabilities := baseline.probabilities()
	total := 0.0
	for _, probability := range probabilities {
		if probability < 0 || probability > 1 || math.IsNaN(probability) {
			t.Fatalf("invalid reconstructed probability: %+v", probabilities)
		}
		total += probability
	}
	if math.Abs(total-1) > 1e-12 {
		t.Fatalf("reconstructed probabilities do not sum to one: %+v", probabilities)
	}
}

func TestCompetingPathObservationsExecutableOutcomes(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	horizon := 15 * time.Minute
	tests := []struct {
		name    string
		books   []bboSnapshot
		outcome gammacapture.CompetingPathOutcome
	}{
		{
			name: "flat",
			books: []bboSnapshot{{time: start, bid: 100, ask: 100.1},
				{time: start.Add(horizon), bid: 100, ask: 100.1}},
			outcome: gammacapture.CompetingPathNone,
		},
		{
			name: "monotone down buy only",
			books: []bboSnapshot{{time: start, bid: 100, ask: 100.1},
				{time: start.Add(5 * time.Minute), bid: 99.7, ask: 99.8},
				{time: start.Add(horizon), bid: 99.6, ask: 99.7}},
			outcome: gammacapture.CompetingPathBuyOnly,
		},
		{
			name: "monotone up sell only",
			books: []bboSnapshot{{time: start, bid: 100, ask: 100.1},
				{time: start.Add(5 * time.Minute), bid: 100.3, ask: 100.4},
				{time: start.Add(horizon), bid: 100.4, ask: 100.5}},
			outcome: gammacapture.CompetingPathSellOnly,
		},
		{
			name: "reversal both",
			books: []bboSnapshot{{time: start, bid: 100, ask: 100.1},
				{time: start.Add(5 * time.Minute), bid: 99.7, ask: 99.8},
				{time: start.Add(10 * time.Minute), bid: 100.3, ask: 100.4},
				{time: start.Add(horizon), bid: 100, ask: 100.1}},
			outcome: gammacapture.CompetingPathBoth,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observations := competingPathObservations(tt.books, start, start.Add(horizon), horizon, 15, 10)
			if len(observations) != 1 || observations[0].outcome != tt.outcome {
				t.Fatalf("unexpected executable outcome: %+v", observations)
			}
			if tt.outcome == gammacapture.CompetingPathSellOnly {
				terminalAsk := tt.books[len(tt.books)-1].ask
				sellQuote := tt.books[0].bid * math.Exp(15.0/10_000)
				want := math.Log(sellQuote/terminalAsk)*10_000 - 10
				if math.Abs(observations[0].valueBps-want) > 1e-12 {
					t.Fatalf("SELL terminal label did not use executable ask: got=%f want=%f", observations[0].valueBps, want)
				}
			}
		})
	}
}

func TestCompetingPathObservationsGapAndDuplicateRules(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	horizon := 15 * time.Minute
	gap := []bboSnapshot{{time: start, bid: 100, ask: 100.1},
		{time: start.Add(13 * time.Minute), bid: 100, ask: 100.1}}
	if got := competingPathObservations(gap, start, start.Add(horizon), horizon, 15, 10); len(got) != 0 {
		t.Fatalf("terminal data gap must invalidate the label: %+v", got)
	}
	books := []bboSnapshot{{time: start, bid: 100, ask: 100.1},
		{time: start.Add(time.Second), bid: 100, ask: 100.1},
		{time: start.Add(horizon), bid: 100, ask: 100.1}}
	unique := []bboSnapshot{books[0], books[2]}
	withDuplicates := competingPathObservations(books, start, start.Add(horizon), horizon, 15, 10)
	withoutDuplicates := competingPathObservations(unique, start, start.Add(horizon), horizon, 15, 10)
	if len(withDuplicates) != 1 || len(withoutDuplicates) != 1 ||
		withDuplicates[0].outcome != withoutDuplicates[0].outcome ||
		withDuplicates[0].valueBps != withoutDuplicates[0].valueBps {
		t.Fatalf("unchanged duplicate BBO altered label: with=%+v without=%+v", withDuplicates, withoutDuplicates)
	}
}

func TestEventClockScoresOnlyAfterExpiryOrCrossing(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	observations := make([]competingPathObservation, 0, 31)
	for minute := 0; minute <= 30; minute++ {
		at := start.Add(time.Duration(minute) * time.Minute)
		observation := competingPathObservation{at: at, maturity: at.Add(15 * time.Minute)}
		if minute == 0 {
			observation.firstEvent = start.Add(4*time.Minute + 30*time.Second)
		}
		observations = append(observations, observation)
	}
	scores := eventClockScores(observations, start)
	for _, minute := range []int{0, 5, 20} {
		if !scores[start.Add(time.Duration(minute)*time.Minute)] {
			t.Fatalf("missing lifecycle decision at minute %d: %+v", minute, scores)
		}
	}
	if scores[start.Add(time.Minute)] || scores[start.Add(4*time.Minute)] || scores[start.Add(19*time.Minute)] {
		t.Fatalf("minute-grid observations leaked into lifecycle decisions: %+v", scores)
	}
}
