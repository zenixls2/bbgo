package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

func TestSummarizeHorizonExcursionsUsesFutureRange(t *testing.T) {
	start := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	mids := make([]float64, 36)
	pattern := []float64{100, 100.25, 100, 99.75}
	for i := range mids {
		mids[i] = pattern[i%len(pattern)]
	}
	bbo := make([]bboSnapshot, 0, len(mids))
	for i, mid := range mids {
		bbo = append(bbo, bboSnapshot{
			time: start.Add(time.Duration(i) * time.Minute),
			bid:  mid - 0.01, ask: mid + 0.01,
		})
	}
	stats := summarizeHorizonExcursions(bbo, 20)
	if len(stats) != 6 {
		t.Fatalf("expected six horizons, got %d", len(stats))
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
	if selected < 10 || score <= 0 {
		t.Fatalf("expected fee-adjusted horizon selection: minutes=%d score=%.2f stats=%+v", selected, score, stats)
	}
}

func TestMergeReplayTradesDeduplicatesByTradeID(t *testing.T) {
	at := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	archived := []tick{{id: 42, time: at, price: 100, size: 1, side: types.SideTypeBuy}}
	captured := []tick{{id: 42, time: at, price: 100, size: 1, side: types.SideTypeBuy}}
	got := mergeReplayTrades(archived, captured)
	if len(got) != 1 {
		t.Fatalf("expected duplicate trade ID to be merged once, got %d", len(got))
	}
}

func TestPrepareReplayInventoryUsesTargetCenteredSpotBand(t *testing.T) {
	cfg, band := prepareReplayInventory(gammacapture.MarketMakerConfig{InventoryTarget: 1, InventoryLimit: 2})
	if band.min != 0 || band.target != 1 || band.max != 2 {
		t.Fatalf("unexpected replay band: %+v", band)
	}
	if cfg.InventoryTarget != 1 || cfg.InventoryLimit != 1 {
		t.Fatalf("quote config should use target plus half-width: %+v", cfg)
	}
}

func TestPrepareReplayOrderUsesPlannedSideNotional(t *testing.T) {
	book := bboSnapshot{bid: 100, bidSize: 2, ask: 101, askSize: 3}
	plan := gammacapture.MarketMakerQuotePlan{BidPrice: 99, AskPrice: 102, BidQuoteNotional: 50, AskQuoteNotional: 80}
	band := replayInventoryBand{min: 0, target: 1, max: 2}
	bid := prepareReplayOrder(types.SideTypeBuy, plan, book, 1, 1_000, 10, band)
	ask := prepareReplayOrder(types.SideTypeSell, plan, book, 1, 1_000, 10, band)
	if !bid.active || math.Abs(bid.quantity-50.0/99.0) > 1e-12 {
		t.Fatalf("bid did not use planned notional: %+v", bid)
	}
	if !ask.active || math.Abs(ask.quantity-80.0/102.0) > 1e-12 {
		t.Fatalf("ask did not use planned notional: %+v", ask)
	}
}

func TestConsumeQueueKeepsPartiallyFilledOrderActive(t *testing.T) {
	order := replayOrder{active: true, side: types.SideTypeBuy, price: 100, quantity: 2, queueAhead: 1}
	fill, completed := consumeQueue(&order, 2)
	if fill != 1 || completed || !order.active || order.quantity != 1 {
		t.Fatalf("partial fill incorrectly completed order: fill=%f completed=%t order=%+v", fill, completed, order)
	}
	fill, completed = consumeQueue(&order, 1)
	if fill != 1 || !completed || order.active || order.quantity != 0 {
		t.Fatalf("remaining quantity did not complete: fill=%f completed=%t order=%+v", fill, completed, order)
	}
}

func TestSummarizeTickerUsesHorizonPathCrossings(t *testing.T) {
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	mids := make([]float64, 36)
	pattern := []float64{100, 100.25, 100, 99.75}
	for i := range mids {
		mids[i] = pattern[i%len(pattern)]
	}
	books := make([]bboSnapshot, 0, len(mids))
	for i, mid := range mids {
		books = append(books, bboSnapshot{time: start.Add(time.Duration(i) * time.Minute), bid: mid - .01, ask: mid + .01})
	}
	stats := summarizeTicker(nil, books, 20, 10)
	if stats.UpCrosses == 0 || stats.DownCrosses == 0 {
		t.Fatalf("horizon path should contain both crossing directions: %+v", stats)
	}
	var selected horizonExcursionStats
	for _, horizon := range stats.HorizonExcursions {
		if horizon.HorizonMinutes == stats.SelectedHorizonMinutes {
			selected = horizon
		}
	}
	if stats.UpCrosses != selected.UpCrosses || stats.DownCrosses != selected.DownCrosses {
		t.Fatalf("health counts must come from selected horizon: stats=%+v selected=%+v", stats, selected)
	}
}

func TestSummarizeHorizonVolatilityUsesNonOverlappingMidReturns(t *testing.T) {
	start := time.Date(2026, 7, 30, 0, 14, 0, 0, time.UTC)
	mids := []float64{100, 101, 99, 102}
	books := make([]bboSnapshot, 0, len(mids))
	for i, mid := range mids {
		books = append(books, bboSnapshot{
			time: start.Add(time.Duration(i) * 15 * time.Minute),
			bid:  mid - .01,
			ask:  mid + .01,
		})
	}
	stats := summarizeHorizonVolatility(books, []int{15, 30})
	if len(stats) != 2 || stats[0].Samples != 3 || stats[1].Samples != 1 {
		t.Fatalf("unexpected horizon samples: %+v", stats)
	}
	returns15 := []float64{
		math.Log(101.0/100.0) * 10_000,
		math.Log(99.0/101.0) * 10_000,
		math.Log(102.0/99.0) * 10_000,
	}
	var sumSquares float64
	for _, r := range returns15 {
		sumSquares += r * r
	}
	wantRMS := math.Sqrt(sumSquares / float64(len(returns15)))
	if math.Abs(stats[0].RMSReturnBps-wantRMS) > 1e-12 {
		t.Fatalf("15-minute RMS mismatch: got %.12f want %.12f", stats[0].RMSReturnBps, wantRMS)
	}
	want30 := math.Log(102.0/101.0) * 10_000
	if math.Abs(stats[1].RMSReturnBps-math.Abs(want30)) > 1e-12 {
		t.Fatalf("30-minute RMS mismatch: got %.12f want %.12f", stats[1].RMSReturnBps, math.Abs(want30))
	}
}
