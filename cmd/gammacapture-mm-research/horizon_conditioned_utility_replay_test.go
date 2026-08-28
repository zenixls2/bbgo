package main

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestHorizonUtilityReplayUpdatesContinuationAtConfirmationOnly(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := newHorizonConditionedUtilityReplaySizer(gammacapture.MarketMakerConfig{})
	s.books[base.Add(4*time.Minute).UnixNano()] = bboSnapshot{
		time: base.Add(4 * time.Minute), bid: 105, ask: 106,
	}
	s.pending = []*horizonConditionedUtilityReplayPending{{
		at: base, shortAt: base.Add(2 * time.Minute), direction: 1,
		phase: 0, quote: 101, startBid: 100, startAsk: 101,
		touched: true,
	}}
	s.mature(bboSnapshot{time: base.Add(2 * time.Minute), bid: 102, ask: 103})
	if s.shortLabels != 1 || s.continuationLabels != 0 || len(s.pending) != 1 {
		t.Fatalf("continuation updated before confirmation: short=%d continuation=%d pending=%d", s.shortLabels, s.continuationLabels, len(s.pending))
	}
	s.assignPivot(gammacapture.PivotRegimeEvent{
		At: base.Add(5 * time.Minute), PivotAt: base.Add(4 * time.Minute), Direction: 1,
	}, base.Add(5*time.Minute))
	s.mature(bboSnapshot{time: base.Add(5 * time.Minute), bid: 104, ask: 105})
	if s.continuationLabels != 1 || len(s.pending) != 0 {
		t.Fatalf("continuation did not update at confirmation: continuation=%d pending=%d", s.continuationLabels, len(s.pending))
	}
}

func TestHorizonUtilityReplayScaleUsesOnlyAlignedSide(t *testing.T) {
	snapshot := horizonUtilitySideSnapshot{
		Ready: true, FillProbability: 1, EffectiveSamples: 100,
		ContinuationMeanBps: 10, TotalVarianceBps2: 0,
	}
	projection := gammacapture.ProbabilityCenteredQuoteDecision{
		Enabled: true, BuyNotionalJPY: 200, SellNotionalJPY: 200,
	}
	plan := gammacapture.MarketMakerQuotePlan{
		AllowBid: true, AllowAsk: true, BidPrice: 100, AskPrice: 101,
	}
	scale, approved := horizonUtilityReplayScale(snapshot, projection, plan, 10_000, 1, 0, 1)
	if !approved || scale <= 0 {
		t.Fatalf("aligned BUY utility was not accepted: scale=%.6f approved=%v", scale, approved)
	}
	if sellScale, sellApproved := horizonUtilityReplayScale(snapshot, projection, plan, 10_000, 1, 0, -1); !sellApproved || sellScale <= 0 {
		t.Fatalf("aligned SELL utility was not accepted: scale=%.6f approved=%v", sellScale, sellApproved)
	}
}

func TestHorizonUtilityReplayGapResetsPendingLabels(t *testing.T) {
	s := newHorizonConditionedUtilityReplaySizer(gammacapture.MarketMakerConfig{})
	s.pending = []*horizonConditionedUtilityReplayPending{{at: time.Now()}}
	s.observeBook(bboSnapshot{time: time.Now().Add(time.Second), bid: 100, ask: 101}, true)
	if len(s.pending) != 0 || s.lastDecision.Ready {
		t.Fatalf("gap did not reset HCU state: pending=%d decision=%+v", len(s.pending), s.lastDecision)
	}
}

func TestHorizonUtilityReplayBoundsBookRetention(t *testing.T) {
	s := newHorizonConditionedUtilityReplaySizer(gammacapture.MarketMakerConfig{})
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 8000; index++ {
		s.observeBook(bboSnapshot{
			time: start.Add(time.Duration(index) * 5 * time.Second),
			bid:  100, ask: 101,
		}, false)
	}
	if len(s.books) >= 8000 || len(s.books) > int((s.continuationHorizon+s.shortHorizon)/(5*time.Second))+1 {
		t.Fatalf("book retention is not bounded: books=%d times=%d", len(s.books), len(s.bookTimes))
	}
}
