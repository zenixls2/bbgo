package gammacapture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func newCheckpointTestStrategy(root string) *Strategy {
	config := Config{
		Symbol:    "TESTJPY",
		Barrier:   BarrierConfig{Width: 0.001, MaxCrossingsPerEvent: 8},
		Intensity: IntensityConfig{Window: types.Duration(2 * time.Hour), PriorAlphaUp: 1, PriorBetaUp: 60, PriorAlphaDown: 1, PriorBetaDown: 60, MinEvents: 4},
		AggTradeWarmup: AggTradeWarmupConfig{
			Path: root, LivePath: filepath.Join(root, "live"),
		},
		MarketMaker: MarketMakerConfig{Enabled: true, FastWindow: types.Duration(10 * time.Minute)},
	}
	return &Strategy{
		Config: config,
		State:  &State{Engine: NewCrossingEngine(config.Barrier.Width, 0, config.Barrier.MaxCrossingsPerEvent)},
		model:  NewIntensityModel(config.Intensity),
	}
}

func TestModelCheckpointRoundTripAndZeroDeltaWarmup(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	first := newCheckpointTestStrategy(root)
	first.State.LastReferenceTime = now.Add(-time.Minute)
	first.State.Engine.Reset(100)
	if err := first.prepareModelCheckpoint(now); err != nil {
		t.Fatal(err)
	}
	before := first.model.Snapshot(now)
	if first.State.ModelCheckpoint == nil || first.State.ModelCheckpoint.ReplayAfter != first.State.LastReferenceTime {
		t.Fatal("checkpoint did not capture the causal replay cursor")
	}

	encoded, err := json.Marshal(first.State)
	if err != nil {
		t.Fatal(err)
	}
	var restoredState State
	if err := json.Unmarshal(encoded, &restoredState); err != nil {
		t.Fatal(err)
	}
	restarted := newCheckpointTestStrategy(root)
	restarted.State = &restoredState
	cursor, restored, err := restarted.restoreModelCheckpoint(now)
	if err != nil || !restored {
		t.Fatalf("zero-delta checkpoint restore failed: restored=%v err=%v", restored, err)
	}
	after := restarted.model.Snapshot(now)
	if before.Up != after.Up || before.Down != after.Down || before.Observed != after.Observed {
		t.Fatalf("restored model differs: before=%+v after=%+v", before, after)
	}
	if cursor != now.Add(-time.Minute) {
		t.Fatalf("unexpected restored cursor: %s", cursor)
	}
}

func TestModelCheckpointRejectsChangedDataModel(t *testing.T) {
	strategy := newCheckpointTestStrategy(t.TempDir())
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	strategy.State.LastReferenceTime = now
	strategy.State.Engine.Reset(100)
	if err := strategy.prepareModelCheckpoint(now); err != nil {
		t.Fatal(err)
	}
	strategy.Barrier.Width *= 2
	if _, restored, err := strategy.restoreModelCheckpoint(now); err == nil || restored {
		t.Fatalf("changed crossing model accepted checkpoint: restored=%v err=%v", restored, err)
	}
}

func TestBacktestNeverRestoresLiveModelCheckpoint(t *testing.T) {
	strategy := newCheckpointTestStrategy(t.TempDir())
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	strategy.State.LastReferenceTime = now
	strategy.State.Engine.Reset(100)
	if err := strategy.prepareModelCheckpoint(now); err != nil {
		t.Fatal(err)
	}
	strategy.Environment = "backtest"
	strategy.model = NewIntensityModel(strategy.Intensity)
	if cursor, restored, err := strategy.restoreModelCheckpoint(now); err != nil || restored || !cursor.IsZero() {
		t.Fatalf("backtest loaded live checkpoint: cursor=%s restored=%v err=%v", cursor, restored, err)
	}
	if len(strategy.model.events) != 0 {
		t.Fatal("backtest slow model was contaminated by live events")
	}
}

func TestMakerStartupRestoresCheckpointWhenAggTradeWarmupDisabled(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 8, 0, 5, 0, 0, time.UTC)
	first := newCheckpointTestStrategy(root)
	first.Config.setDefaults()
	first.AggTradeWarmup.Enabled = false
	first.initializeAdaptiveFastModels()
	first.makerExecutableCrossingModel = NewExecutableCrossingModel(first.Symbol, first.Barrier, first.Intensity)
	first.makerHawkesDirectionModel = NewHawkesDirectionModel(first.MarketMaker.HawkesDirection)

	config := first.MarketMaker
	config.setDefaults()
	t0 := now.Add(-2 * time.Minute)
	t1 := t0.Add(time.Second)
	first.observeMakerReplayBBO(t0, evidenceBBO(first.Symbol, 99.99, 1, 100.01, 1), false, config)
	first.observeMakerReplayBBO(t1, evidenceBBO(first.Symbol, 100.19, 1, 100.21, 1), false, config)
	before := first.model.Snapshot(t1)
	if before.Up == 0 {
		t.Fatalf("test setup did not create a checkpointed upcrossing: %+v", before)
	}
	if err := first.prepareModelCheckpoint(t1); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(first.State)
	if err != nil {
		t.Fatal(err)
	}
	var restoredState State
	if err := json.Unmarshal(encoded, &restoredState); err != nil {
		t.Fatal(err)
	}

	// Simulate a top-level state sync after the bounded model checkpoint. The
	// delta between the checkpoint cursor and this value must still be replayed.
	restoredState.LastReferenceTime = now
	collector := filepath.Join(root, first.Symbol)
	if err := os.MkdirAll(collector, 0o755); err != nil {
		t.Fatal(err)
	}
	t2 := t1.Add(time.Second)
	book := fmt.Sprintf("received_at,bid,bid_quantity,ask,ask_quantity,gap_before_ms\n%s,99.99,1,100.01,1,0\n",
		t2.Format(time.RFC3339Nano))
	filename := filepath.Join(collector, fmt.Sprintf("%s-bookticker-%s.csv", first.Symbol, t2.Format(time.DateOnly)))
	if err := os.WriteFile(filename, []byte(book), 0o600); err != nil {
		t.Fatal(err)
	}

	restarted := newCheckpointTestStrategy(root)
	restarted.Config.setDefaults()
	restarted.AggTradeWarmup.Enabled = false
	restarted.State = &restoredState
	restarted.initializeAdaptiveFastModels()
	restarted.makerExecutableCrossingModel = NewExecutableCrossingModel(restarted.Symbol, restarted.Barrier, restarted.Intensity)
	restarted.makerHawkesDirectionModel = NewHawkesDirectionModel(restarted.MarketMaker.HawkesDirection)
	if err := restarted.restoreAndWarmMakerModelsFromBinanceCapture(t2.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after := restarted.model.Snapshot(t2.Add(time.Second))
	if after.Up < before.Up || after.Down == 0 {
		t.Fatalf("checkpoint or capture delta was lost: before=%+v after=%+v", before, after)
	}
	if restarted.State.LastReferenceTime != t2 {
		t.Fatalf("unexpected replay cursor: got=%s want=%s", restarted.State.LastReferenceTime, t2)
	}
}
