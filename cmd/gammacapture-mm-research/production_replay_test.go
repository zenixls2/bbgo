package main

import (
	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
	"testing"
	"time"
)

func TestCompactReplayEventsRemovesExactDuplicates(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	books := compactBBO([]bboSnapshot{{time: at, bid: 100, bidSize: 1, ask: 101, askSize: 2}, {time: at, bid: 100, bidSize: 1, ask: 101, askSize: 2}})
	if len(books) != 1 {
		t.Fatalf("expected one unique BBO, got %d", len(books))
	}
	trades := compactTrades([]tick{{time: at, price: 100, size: 1, side: types.SideTypeBuy}, {time: at, price: 100, size: 1, side: types.SideTypeBuy}})
	if len(trades) != 1 {
		t.Fatalf("expected one unique trade, got %d", len(trades))
	}
}
func TestProductionQueueRequiresCompleteOrderFill(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{cfg: gammacapture.MarketMakerConfig{MakerFeeBps: 10}, bidOrder: productionReplayOrder{active: true, side: types.SideTypeBuy, price: 100, remaining: 2, queueAhead: 1}, fillsByDay: make(map[string]*productionReplayDay)}
	s.consume(&s.bidOrder, tick{time: at, price: 100, size: 2, side: types.SideTypeSell})
	if s.fills != 0 || !s.bidOrder.active || s.bidOrder.remaining != 1 {
		t.Fatalf("partial execution counted as full: %+v fills=%d", s.bidOrder, s.fills)
	}
	s.consume(&s.bidOrder, tick{time: at.Add(time.Second), price: 100, size: 1, side: types.SideTypeSell})
	if s.fills != 1 || s.bidOrder.active {
		t.Fatalf("order did not complete: %+v fills=%d", s.bidOrder, s.fills)
	}
}
func TestMeanFillMarkoutUsesSideSign(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{{time: at, bid: 99, ask: 101}, {time: at.Add(time.Minute), bid: 109, ask: 111}}
	buy := meanFillMarkout([]replayFill{{at: at, side: types.SideTypeBuy, price: 100}}, books, time.Minute)
	sell := meanFillMarkout([]replayFill{{at: at, side: types.SideTypeSell, price: 100}}, books, time.Minute)
	if buy <= 0 || sell >= 0 {
		t.Fatalf("unexpected markouts buy=%f sell=%f", buy, sell)
	}
}

func TestCompareReplayPoliciesFailsClosedWhenCalibrationMisses(t *testing.T) {
	old := productionReplayResult{FillsPerDay: 4, RoundTripsPerDay: 2}
	current := productionReplayResult{FillsPerDay: 8, RoundTripsPerDay: 4}
	got := compareReplayPolicies(old, current, false)
	want := "calibration failed: do not use synthetic fill comparison to promote or tune live policy"
	if got != want {
		t.Fatalf("unexpected decision: %q", got)
	}
}
