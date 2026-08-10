package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestNoTradeIOCVariantsCoverFactorialWithoutMutatingInput(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{}
	cfg.MacroInventory.NoTradeRegion.Enabled = true
	cfg.MacroInventory.NoTradeRegion.TrendExcursionEnabled = true
	cfg.MacroInventory.NoTradeRegion.ContinuationEnabled = true
	cfg.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled = true
	variants := noTradeIOCVariants(cfg)
	if len(variants) != 13 {
		t.Fatalf("expected thirteen ablation variants, got %d", len(variants))
	}
	seen := make(map[[3]bool]bool)
	for _, variant := range variants {
		seen[[3]bool{
			variant.NoTradeEnabled,
			variant.TrendExcursionEnabled,
			variant.ActiveIOCEnabled,
		}] = true
	}
	combinations := [][3]bool{
		{true, true, true}, {true, false, true},
		{true, true, false}, {true, false, false},
		{false, false, true}, {false, false, false},
	}
	for _, combination := range combinations {
		if !seen[combination] {
			t.Fatalf("missing no-trade/trend/IOC combination %v", combination)
		}
	}
	fastVariance := selectNoTradeIOCVariants(variants, "qv-fast-variance+ioc", "qv-only+ioc")
	if len(fastVariance) != 2 || !fastVariance[0].FastVarianceEnabled || fastVariance[1].FastVarianceEnabled {
		t.Fatalf("unexpected fast-variance/QV targeted variants: %+v", fastVariance)
	}
	selected := selectNoTradeIOCVariants(
		variants, "qv-continuation+ioc", "qv-only+ioc")
	if len(selected) != 2 || selected[0].TrendExcursionEnabled ||
		!selected[0].ContinuationEnabled || selected[1].ContinuationEnabled {
		t.Fatalf("unexpected continuation/QV targeted variants: %+v", selected)
	}
	if !cfg.MacroInventory.NoTradeRegion.Enabled ||
		!cfg.MacroInventory.NoTradeRegion.TrendExcursionEnabled ||
		!cfg.MacroInventory.NoTradeRegion.ContinuationEnabled ||
		!cfg.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled {
		t.Fatal("variant construction mutated the live input config")
	}
}

func TestHoldBenchmarkUsesSameStartingEquityAndMarkedPath(t *testing.T) {
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{
		{time: start, bid: 99, ask: 101},
		{time: start.Add(time.Minute), bid: 89, ask: 91},
		{time: start.Add(2 * time.Minute), bid: 109, ask: 111},
	}
	got := holdBenchmark(books, start, 1_000, 1)
	if math.Abs(got.FinalEquityJPY-1_010) > 1e-12 ||
		math.Abs(got.NetPnLJPY-10) > 1e-12 ||
		math.Abs(got.ReturnPct-1) > 1e-12 ||
		math.Abs(got.MaximumDrawdownPct-1) > 1e-12 {
		t.Fatalf("unexpected hold benchmark: %+v", got)
	}
}

func TestPairedExcessLower95UsesHoldDifferenceBlocks(t *testing.T) {
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	curve := []productionEquityPoint{
		{At: start, EquityJPY: 999, HoldEquityJPY: 1000},
		{At: start.Add(30 * time.Minute), EquityJPY: 1001, HoldEquityJPY: 1000},
		{At: start.Add(90 * time.Minute), EquityJPY: 1004, HoldEquityJPY: 1000},
		{At: start.Add(150 * time.Minute), EquityJPY: 1003, HoldEquityJPY: 1000},
	}
	mean, lower, samples := pairedExcessLower95(productionReplayResult{EquityCurve: curve})
	if samples != 2 || math.Abs(mean-1.5) > 1e-12 || lower >= 0 {
		t.Fatalf("expected conservative hourly paired excess summary, mean=%v lower=%v samples=%d", mean, lower, samples)
	}
}
