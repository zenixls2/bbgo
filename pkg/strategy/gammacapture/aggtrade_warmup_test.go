package gammacapture

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestWarmModelFromAggTrades(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "binance", "TESTJPY", "aggTrades")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	file, err := os.Create(filepath.Join(dir, "TESTJPY-2026-07-16.csv"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		price := 100.0
		if i%2 == 1 {
			price = 100.2
		}
		_, _ = fmt.Fprintf(file, "%d,buy,1,%.4f,%d\n", i+1, price, start.Add(time.Duration(i)*time.Minute).UnixMilli())
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Strategy{
		Config: Config{
			Symbol:         "TESTJPY",
			Barrier:        BarrierConfig{Width: .001, MaxCrossingsPerEvent: 1},
			Intensity:      IntensityConfig{Window: types.Duration(2 * time.Hour), PriorAlphaUp: 1, PriorBetaUp: 60, PriorAlphaDown: 1, PriorBetaDown: 60, MinEvents: 10},
			AggTradeWarmup: AggTradeWarmupConfig{Enabled: true, Path: root, Lookback: types.Duration(2 * time.Hour), MaxAge: types.Duration(10 * time.Minute), RequireHealthy: true},
			MarketMaker:    MarketMakerConfig{Enabled: true, HorizonLookback: types.Duration(2 * time.Hour)},
		},
		State: &State{Engine: NewCrossingEngine(.001, 0, 1)},
	}
	s.model = NewIntensityModel(s.Intensity)
	s.makerDirectionModel = NewDecayedDirectionModel(10 * time.Minute)
	if err := s.warmModelFromAggTrades(start.Add(50 * time.Minute)); err != nil {
		t.Fatalf("warmup failed: %v", err)
	}
	if s.State.Runtime != StateArmedLong || s.State.LastDecision == "" {
		t.Fatalf("warmup did not arm strategy: state=%+v", s.State)
	}
	if len(s.makerHorizonModel.points) == 0 || s.makerHorizonModel.EmpiricalVolatilityFloor(start.Add(50*time.Minute), 2*time.Hour) <= 0 {
		t.Fatalf("public aggregate-trade warmup did not seed maker horizon statistics: points=%d", len(s.makerHorizonModel.points))
	}
	if got := s.makerDirectionModel.Snapshot(start.Add(50 * time.Minute)); got.EffectiveSamples <= 0 {
		t.Fatalf("public aggregate-trade warmup did not seed direction posterior: %+v", got)
	}
}

func TestReadLiveWarmupFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "trades-*.csv")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	_, _ = fmt.Fprintln(file, "event_time,received_at,id,price,quantity,side")
	_, _ = fmt.Fprintf(file, "%s,%s,42,100.5,1,buy\n", when.Format(time.RFC3339Nano), when.Format(time.RFC3339Nano))
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var trades []warmupTrade
	if err := readLiveWarmupFile(file.Name(), when.Add(-time.Minute), when.Add(time.Minute), &trades); err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 || trades[0].id != 42 || trades[0].price != 100.5 {
		t.Fatalf("unexpected live trade: %+v", trades)
	}
}

func TestReadLiveWarmupFileReadsCaptureGapMarker(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "trades-*.csv")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	_, _ = fmt.Fprintln(file, "event_time,received_at,id,price,quantity,side,aggregate_id,first_trade_id,last_trade_id,gap_before_ms")
	_, _ = fmt.Fprintf(file, "%s,%s,42,100.5,1,buy,9,40,42,6000\n", when.Format(time.RFC3339Nano), when.Format(time.RFC3339Nano))
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var trades []warmupTrade
	if err := readLiveWarmupFile(file.Name(), when.Add(-time.Minute), when.Add(time.Minute), &trades); err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 || trades[0].gapBefore != 6*time.Second {
		t.Fatalf("capture gap marker was not decoded: %+v", trades)
	}
}
