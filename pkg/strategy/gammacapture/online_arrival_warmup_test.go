package gammacapture

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func writeOnlineArrivalWarmupBBO(t *testing.T, root string, start time.Time, minutes int) time.Time {
	t.Helper()
	dir := filepath.Join(root, "TESTJPY")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "TESTJPY-bookticker-test.csv"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(file, "received_at,bid,bid_quantity,ask,ask_quantity,gap_before_ms")
	for minute := 0; minute <= minutes; minute++ {
		at := start.Add(time.Duration(minute) * time.Minute)
		phase := 2 * math.Pi * float64(minute%10) / 10
		mid := 100 + 0.6*math.Sin(phase)
		_, _ = fmt.Fprintf(file, "%s,%.8f,1,%.8f,1,60000\n", at.Format(time.RFC3339Nano), mid-0.01, mid+0.01)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return start.Add(time.Duration(minutes+1) * time.Minute)
}

func newOnlineArrivalWarmupStrategy(root string, state *OnlineArrivalState) *Strategy {
	config := Config{
		Symbol:    "TESTJPY",
		Barrier:   BarrierConfig{Width: 0.001, MaxCrossingsPerEvent: 8},
		Intensity: IntensityConfig{Window: types.Duration(2 * time.Hour), PriorAlphaUp: 1, PriorBetaUp: 60, PriorAlphaDown: 1, PriorBetaDown: 60, MinEvents: 4},
		AggTradeWarmup: AggTradeWarmupConfig{
			Path: root, LivePath: filepath.Join(root, "live"),
		},
		MarketMaker: MarketMakerConfig{
			Enabled:              true,
			MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 50,
			MinTradingWindow: types.Duration(10 * time.Minute), MaxTradingWindow: types.Duration(10 * time.Minute),
			HorizonLookback: types.Duration(2 * time.Hour), HorizonMinSamples: 4,
			FastWindow: types.Duration(10 * time.Minute),
			OnlineArrival: OnlineArrivalConfig{
				Enabled: true, DistanceStepBps: 5,
				FastHalfLife: types.Duration(time.Hour), SlowHalfLife: types.Duration(24 * time.Hour),
				StartupLookback: types.Duration(2 * time.Hour), StartupMaxAge: types.Duration(2 * time.Minute),
				RequireStartupHistory: true,
			},
		},
	}
	strategy := &Strategy{
		Config: config,
		State: &State{
			Engine:        NewCrossingEngine(config.Barrier.Width, 0, config.Barrier.MaxCrossingsPerEvent),
			OnlineArrival: state,
		},
	}
	strategy.model = NewIntensityModel(config.Intensity)
	strategy.fastModel = NewIntensityModel(config.MarketMaker.fastIntensityConfig())
	strategy.makerDirectionModel = NewDecayedDirectionModel(10 * time.Minute)
	strategy.makerHorizonModel.bindOnlineArrival(state)
	return strategy
}

func TestWarmOnlineArrivalFromBinanceBBOTrainsColdProcessAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	now := writeOnlineArrivalWarmupBBO(t, root, start, 90)
	state := NewOnlineArrivalState()

	first := newOnlineArrivalWarmupStrategy(root, state)
	if err := first.warmOnlineArrivalFromBinanceBBO(now); err != nil {
		t.Fatalf("first startup replay failed: %v", err)
	}
	decision := first.makerHorizonModel.CrossingDecisionAtDistance(now, first.MarketMaker, 10*time.Minute, 20)
	if !decision.HasSufficientCrossings(first.MarketMaker.HorizonMinSamples) {
		t.Fatalf("startup replay did not finish online arrival training: %+v", decision)
	}
	if snapshot := first.model.Snapshot(now); snapshot.Health != HealthHealthy {
		t.Fatalf("startup replay did not reconstruct the rolling crossing model: %+v", snapshot)
	}
	if direction := first.makerDirectionModel.Snapshot(now); direction.EffectiveSamples <= 0 {
		t.Fatalf("startup replay did not reconstruct the direction model: %+v", direction)
	}
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}

	restarted := newOnlineArrivalWarmupStrategy(root, state)
	if err := restarted.warmOnlineArrivalFromBinanceBBO(now); err != nil {
		t.Fatalf("restart replay failed: %v", err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("overlapping startup replay double-counted persisted online windows\nbefore=%s\nafter=%s", before, after)
	}
	if snapshot := restarted.model.Snapshot(now); snapshot.Health != HealthHealthy {
		t.Fatalf("restart replay did not rebuild volatile crossing state: %+v", snapshot)
	}
}

func TestWarmOnlineArrivalFromBinanceBBORejectsMissingAndStaleHistory(t *testing.T) {
	state := NewOnlineArrivalState()
	missing := newOnlineArrivalWarmupStrategy(t.TempDir(), state)
	now := time.Date(2026, 7, 31, 2, 0, 0, 0, time.UTC)
	if err := missing.warmOnlineArrivalFromBinanceBBO(now); err == nil {
		t.Fatal("required startup replay accepted a missing Binance BBO capture")
	}

	root := t.TempDir()
	start := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	captureEnd := writeOnlineArrivalWarmupBBO(t, root, start, 90)
	stale := newOnlineArrivalWarmupStrategy(root, NewOnlineArrivalState())
	if err := stale.warmOnlineArrivalFromBinanceBBO(captureEnd.Add(3 * time.Minute)); err == nil {
		t.Fatal("required startup replay accepted stale Binance BBO history")
	}
}

func TestWarmOnlineArrivalFromBinanceBBOCompletesAllAdaptiveWindows(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	now := writeOnlineArrivalWarmupBBO(t, root, start, 240)
	strategy := newOnlineArrivalWarmupStrategy(root, NewOnlineArrivalState())
	strategy.MarketMaker.MaxTradingWindow = types.Duration(30 * time.Minute)
	strategy.MarketMaker.OnlineArrival.StartupLookback = types.Duration(5 * time.Hour)
	strategy.MarketMaker.FastWindows = []types.Duration{
		types.Duration(10 * time.Minute),
		types.Duration(15 * time.Minute),
		types.Duration(30 * time.Minute),
	}
	strategy.initializeAdaptiveFastModels()

	if err := strategy.warmOnlineArrivalFromBinanceBBO(now); err != nil {
		t.Fatalf("adaptive-window startup replay failed: %v", err)
	}
	for _, window := range strategy.MarketMaker.FastModelWindows() {
		decision := strategy.makerHorizonModel.CrossingDecisionAtDistance(now, strategy.MarketMaker, window, 20)
		if !decision.HasSufficientCrossings(strategy.MarketMaker.HorizonMinSamples) {
			t.Fatalf("%s arrival model did not finish startup training: %+v", window, decision)
		}
		if snapshot := strategy.fastModels[window].Snapshot(now); snapshot.Health != HealthHealthy {
			t.Fatalf("%s fast model did not finish startup training: %+v", window, snapshot)
		}
	}
	selected := strategy.adaptiveFastSnapshot(now)
	if selected.Window != 10*time.Minute || selected.Model.Health != HealthHealthy {
		t.Fatalf("startup should select the shortest healthy adaptive window: %+v", selected)
	}
}
