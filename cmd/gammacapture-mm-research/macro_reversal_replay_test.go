package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

func TestRecordEquityUsesNetFeesAndMinuteClose(t *testing.T) {
	at := time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC)
	state := &productionReplayState{
		quote: 200, inventory: 2, fees: 3,
		initialQuote: 100, initialInventory: 1,
	}
	state.recordEquity(at, 100, 0.6, 1, true, true)
	state.quote = 210
	state.recordEquity(at.Add(20*time.Second), 101, 0.7, -1, false, true)
	if len(state.equityCurve) != 1 {
		t.Fatalf("same-minute update must replace the open sample: %d", len(state.equityCurve))
	}
	got := state.equityCurve[0]
	if math.Abs(got.EquityJPY-409) > 1e-9 || math.Abs(got.HoldEquityJPY-201) > 1e-9 {
		t.Fatalf("unexpected minute-close equity sample: %+v", got)
	}
	if got.TargetRatio != 0.7 || got.ReversalDirection != -1 || got.EarlyReversal {
		t.Fatalf("minute-close annotations were not replaced: %+v", got)
	}
	state.recordEquity(at.Add(time.Minute), 102, 0.7, -1, false, true)
	if len(state.equityCurve) != 2 {
		t.Fatalf("next minute must append a sample: %d", len(state.equityCurve))
	}
}

func TestMacroReplayWarmupIncludesLookbackAndLongestHorizon(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{
		MacroInventory: gammacapture.MacroInventoryConfig{
			Lookback:    types.Duration(240 * time.Hour),
			BarInterval: types.Duration(10 * time.Minute),
			RiskHorizons: []types.Duration{
				types.Duration(3 * time.Hour),
				types.Duration(24 * time.Hour),
			},
		},
	}
	if got, want := macroReplayWarmup(cfg), 264*time.Hour+10*time.Minute; got != want {
		t.Fatalf("unexpected Macro warmup: got %s want %s", got, want)
	}
}
func TestReplayCappedInventoryCapacityMatchesLiveMinimumRescue(t *testing.T) {
	tests := []struct {
		name                 string
		model, hard, minimum float64
		want                 float64
	}{
		{name: "rescue executable minimum", model: 5, hard: 200, minimum: 100, want: 100},
		{name: "hard cap wins", model: 500, hard: 150, minimum: 100, want: 150},
		{name: "do not cross subminimum hard cap", model: 5, hard: 50, minimum: 100, want: 5},
		{name: "zero headroom", model: 100, hard: 0, minimum: 100, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := replayCappedInventoryCapacity(test.model, test.hard, test.minimum); got != test.want {
				t.Fatalf("unexpected capacity: got %v want %v", got, test.want)
			}
		})
	}
}
func TestReadMacroReplayBBOCompressesOnlyWarmup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ETHJPY-bookticker-test.csv")
	data := "received_at,bid,bid_quantity,ask,ask_quantity,gap_before_ms\n" +
		"2026-08-03T17:41:00.100Z,100,1,101,2,0\n" +
		"2026-08-03T17:41:00.900Z,102,1,103,2,0\n" +
		"2026-08-03T17:42:00.100Z,104,1,105,2,0\n" +
		"2026-08-03T17:42:00.200Z,106,1,107,2,0\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 8, 3, 17, 41, 0, 0, time.UTC)
	exactFrom := time.Date(2026, 8, 3, 17, 42, 0, 0, time.UTC)
	to := time.Date(2026, 8, 3, 17, 43, 0, 0, time.UTC)
	books := readMacroReplayBBO(dir, "ETHJPY", from, to, exactFrom)
	if len(books) != 3 {
		t.Fatalf("expected one warm-up close and two exact books, got %d", len(books))
	}
	if books[0].bid != 102 || books[1].bid != 104 || books[2].bid != 106 {
		t.Fatalf("unexpected warm-up/exact samples: %+v", books)
	}
}

func TestReplayDatasetCacheHitAndInvalidation(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("model: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bboPath := filepath.Join(dir, "ETHJPY-bookticker-test.csv")
	bbo := "received_at,bid,bid_quantity,ask,ask_quantity,gap_before_ms\n" +
		"2026-08-03T17:41:00.100Z,100,1,101,2,0\n" +
		"2026-08-03T17:42:00.100Z,102,1,103,2,0\n"
	if err := os.WriteFile(bboPath, []byte(bbo), 0o600); err != nil {
		t.Fatal(err)
	}
	tradesPath := filepath.Join(dir, "ETHJPY-trades-test.csv")
	trades := "received_at,event_type,trade_id,price,quantity,side\n" +
		"2026-08-03T17:41:00.100Z,trade,1,100,1,BUY\n"
	if err := os.WriteFile(tradesPath, []byte(trades), 0o600); err != nil {
		t.Fatal(err)
	}
	from := parseTime("2026-08-03T17:41:00Z")
	to := parseTime("2026-08-03T17:43:00Z")
	configFingerprint := replayConfigFingerprint(configPath)
	books, ticks, hit := loadMacroReplayDataset(dir, "ETHJPY", from, to, from, configFingerprint, cacheDir)
	if hit || len(books) != 2 || len(ticks) != 1 {
		t.Fatalf("first cache load should parse source: hit=%t books=%d trades=%d", hit, len(books), len(ticks))
	}
	_, _, hit = loadMacroReplayDataset(dir, "ETHJPY", from, to, from, configFingerprint, cacheDir)
	if !hit {
		t.Fatal("second identical replay should hit cache")
	}
	if err := os.WriteFile(bboPath, []byte(bbo+"2026-08-03T17:42:30.100Z,103,1,104,2,0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	books, _, hit = loadMacroReplayDataset(dir, "ETHJPY", from, to, from, configFingerprint, cacheDir)
	if hit || len(books) != 3 {
		t.Fatalf("source mutation should invalidate cache: hit=%t books=%d", hit, len(books))
	}
}
