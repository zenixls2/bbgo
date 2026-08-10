package main

import (
	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
	"testing"
	"time"
)

func TestCompactReplayEventsRemovesExactDuplicates(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	books := compactBBO([]bboSnapshot{{time: at, bid: 100, bidSize: 1, ask: 101, askSize: 2}, {time: at.Add(time.Second), bid: 100, bidSize: 1, ask: 101, askSize: 2}})
	if len(books) != 1 {
		t.Fatalf("expected one unique BBO, got %d", len(books))
	}
	trades := compactTrades([]tick{{time: at, price: 100, size: 1, side: types.SideTypeBuy}, {time: at, price: 100, size: 1, side: types.SideTypeBuy}})
	if len(trades) != 1 {
		t.Fatalf("expected one unique trade, got %d", len(trades))
	}
}

func TestReplayDatasetCacheKeyIgnoresStrategyConfigFingerprint(t *testing.T) {
	header := replayDatasetCacheHeader{Version: 1, Mode: "exact", Symbol: "ETHJPY", From: time.Unix(1, 0), To: time.Unix(2, 0), ConfigFingerprint: "one"}
	other := header
	other.ConfigFingerprint = "two"
	if replayDatasetCacheKey(header) != replayDatasetCacheKey(other) {
		t.Fatal("strategy config fingerprint must not invalidate parsed market-data cache")
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

func TestProductionOrderIsFillEligibleOnlyAfterNextBBO(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg:        gammacapture.MarketMakerConfig{MakerFeeBps: 10},
		bidOrder:   productionReplayOrder{active: true, side: types.SideTypeBuy, price: 100, remaining: 1},
		fillsByDay: make(map[string]*productionReplayDay),
	}
	trade := tick{time: at, price: 100, size: 1, side: types.SideTypeSell}
	s.onTrade(trade)
	if s.fills != 0 || s.bidOrder.remaining != 1 {
		t.Fatalf("order filled before next BBO: %+v fills=%d", s.bidOrder, s.fills)
	}
	s.activatePendingQuotesOnNextBBO()
	s.onTrade(tick{time: at.Add(time.Millisecond), price: 100, size: 1, side: types.SideTypeSell})
	if s.fills != 1 || s.bidOrder.active {
		t.Fatalf("order did not fill after next BBO activation: %+v fills=%d", s.bidOrder, s.fills)
	}
}

func TestProductionMacroIOCExecutesOnlyOnNextBBOAndUsesItsDepth(t *testing.T) {
	at := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg:      gammacapture.MarketMakerConfig{TakerFeeBps: 10},
		quote:    1_000,
		bidOrder: productionReplayOrder{active: true, side: types.SideTypeBuy, price: 98, remaining: 1},
		askOrder: productionReplayOrder{active: true, side: types.SideTypeSell, price: 102, remaining: 1},
	}
	bar := at.Add(-10 * time.Minute)
	s.scheduleMacroIOC(at, gammacapture.MacroActiveExecutionDecision{
		Trigger: true, Direction: 1, Quantity: 2, WorstPrice: 101,
	}, bar)
	if s.inventory != 0 || !s.pendingMacroIOC.active || s.macroActiveAttempts != 1 {
		t.Fatalf("IOC executed on its decision BBO: %+v", s)
	}
	if !s.bidOrder.active || !s.askOrder.active {
		t.Fatalf("scheduling a partial IOC canceled the surviving maker quotes: %+v", s)
	}
	s.executePendingMacroIOC(bboSnapshot{
		time: at.Add(time.Second), bid: 99.9, bidSize: 1, ask: 100, askSize: 1.5,
	})
	if s.pendingMacroIOC.active || s.macroActiveFills != 1 || s.inventory != 1.5 || s.quote != 850 {
		t.Fatalf("next-BBO IOC did not respect visible depth: inventory=%f quote=%f fills=%d pending=%+v",
			s.inventory, s.quote, s.macroActiveFills, s.pendingMacroIOC)
	}
	if !s.macroInventoryState.LastActiveExecutionBarAt.Equal(bar) || s.takerFees != 0.15 {
		t.Fatalf("IOC cadence/fee accounting mismatch: state=%+v takerFees=%f", s.macroInventoryState, s.takerFees)
	}
	if !s.bidOrder.active || !s.askOrder.active {
		t.Fatalf("IOC execution canceled maker quotes before replacement planning: %+v", s)
	}
}

func TestProductionMacroIOCCancelsWhenNextBBOEscapesLimit(t *testing.T) {
	at := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{cfg: gammacapture.MarketMakerConfig{TakerFeeBps: 10}, quote: 1_000}
	s.scheduleMacroIOC(at, gammacapture.MacroActiveExecutionDecision{
		Trigger: true, Direction: 1, Quantity: 1, WorstPrice: 101,
	}, at.Add(-10*time.Minute))
	s.executePendingMacroIOC(bboSnapshot{
		time: at.Add(time.Second), bid: 101.9, bidSize: 1, ask: 102, askSize: 1,
	})
	if s.pendingMacroIOC.active || s.macroActiveFills != 0 || s.inventory != 0 || s.quote != 1_000 {
		t.Fatalf("market moved beyond IOC limit but replay fabricated a fill: %+v", s)
	}
}

func TestProductionReplayStopsAtConfiguredDrawdown(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		quote: 94, initialQuote: 100, initialEquity: 100,
		equityPeak: 100, maxDrawdownStopPct: 5,
	}
	s.recordEquity(at, 100, 0, 0, false, false)
	if !s.stopped || !s.stopAt.Equal(at) || s.maximumDrawdownPct != 6 {
		t.Fatalf("drawdown stop did not trigger: stopped=%t at=%s max=%f", s.stopped, s.stopAt, s.maximumDrawdownPct)
	}
}

func TestProductionReplayInitializesEveryConfiguredFastWindow(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{
		FastWindows: []types.Duration{
			types.Duration(10 * time.Minute),
			types.Duration(15 * time.Minute),
			types.Duration(30 * time.Minute),
		},
	}
	s := newProductionReplayState(
		cfg, gammacapture.BarrierConfig{}, gammacapture.IntensityConfig{},
		nil, replayLegacy, "ETHJPY", 1_000, 0, 1, time.Time{}, true)
	for _, window := range []time.Duration{10 * time.Minute, 15 * time.Minute, 30 * time.Minute} {
		if s.fastModels[window] == nil || s.fastEvidenceModels[window] == nil {
			t.Fatalf("adaptive fast window %s was not initialized", window)
		}
	}
}
func TestReplayNearFillSidesPreservesApproachingBid(t *testing.T) {
	book := bboSnapshot{bid: 302_172, ask: 302_173}
	bid := productionReplayOrder{active: true, side: types.SideTypeBuy, price: 302_158}
	ask := productionReplayOrder{active: true, side: types.SideTypeSell, price: 303_427}
	plan := gammacapture.MarketMakerQuotePlan{
		AllowBid: true, AllowAsk: true, BidTouchDistanceBps: 15, AskTouchDistanceBps: 15,
	}
	retainBid, retainAsk := replayNearFillSides(bid, ask, book, plan, 26)
	if !retainBid || retainAsk {
		t.Fatalf("unexpected retention: bid=%t ask=%t", retainBid, retainAsk)
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
