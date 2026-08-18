package gammacapture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
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

func TestModelCheckpointHashCoversFastDecisionConfiguration(t *testing.T) {
	strategy := newCheckpointTestStrategy(t.TempDir())
	base, err := strategy.modelCheckpointHash()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*MarketMakerConfig)
	}{
		{"fast risk aversion", func(c *MarketMakerConfig) { c.FastRiskAversion = 2 }},
		{"dynamic inventory aim", func(c *MarketMakerConfig) { c.DynamicInventoryAim.Enabled = true }},
		{"fast target execution", func(c *MarketMakerConfig) { c.FastTargetExecution.Enabled = true }},
		{"fast target switching", func(c *MarketMakerConfig) { c.FastTargetSwitching.Enabled = true }},
		{"posterior inventory target", func(c *MarketMakerConfig) { c.PosteriorInventoryTarget = true }},
		{"probability centered quantity", func(c *MarketMakerConfig) { c.ProbabilityCenteredQuantity.Enabled = true }},
		{"joint distance quantity", func(c *MarketMakerConfig) { c.JointDistanceQuantity.Enabled = true }},
		{"conditional execution", func(c *MarketMakerConfig) { c.ConditionalExecution.Enabled = true }},
		{"post fill utility", func(c *MarketMakerConfig) { c.PostFillUtility.Enabled = true }},
		{"maker fee", func(c *MarketMakerConfig) { c.MakerFeeBps += 1 }},
		{"inventory risk", func(c *MarketMakerConfig) { c.InventoryRiskZScore += 0.1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := newCheckpointTestStrategy(t.TempDir())
			tc.mutate(&candidate.MarketMaker)
			got, err := candidate.modelCheckpointHash()
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatalf("decision configuration change did not change checkpoint hash: %s", got)
			}
		})
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

func TestModelCheckpointRoundTripsFastDriftState(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	first := newCheckpointTestStrategy(root)
	first.MarketMaker.FastDrift.Enabled = true
	first.State.LastReferenceTime = now.Add(-time.Minute)
	first.State.Engine.Reset(100)
	first.makerHorizonModel.fastDrift = map[time.Duration]*fastDriftRegression{
		window: {
			Samples: []fastDriftSample{{
				At: now.Add(-window), Features: [fastDriftFeatureCount]float64{1, 0.4, -0.2},
				AskReturnBps: 3.2, BidReturnBps: 3.0, CenterReturnBps: 3.1,
				PredictedCenter: 2.7, PredictionReady: true,
			}},
			Anchor: &fastDriftAnchor{
				At: now, MaturesAt: now.Add(window), StartBid: 99.9, StartAsk: 100.1,
				Features:        [fastDriftFeatureCount]float64{1, -0.3, 0.5},
				PredictedCenter: -1.4, PredictionReady: true,
			},
		},
	}
	if err := first.prepareModelCheckpoint(now); err != nil {
		t.Fatal(err)
	}
	restarted := newCheckpointTestStrategy(root)
	restarted.MarketMaker.FastDrift.Enabled = true
	restarted.State = &State{
		Engine: first.State.Engine, LastReferenceTime: first.State.LastReferenceTime,
		ModelCheckpoint: first.State.ModelCheckpoint,
	}
	if _, restored, err := restarted.restoreModelCheckpoint(now); err != nil || !restored {
		t.Fatalf("Fast drift checkpoint restore failed: restored=%v err=%v", restored, err)
	}
	model := restarted.makerHorizonModel.fastDrift[window]
	if model == nil || len(model.Samples) != 1 || model.Anchor == nil {
		t.Fatalf("Fast drift checkpoint state was lost: %+v", model)
	}
	if !model.Samples[0].PredictionReady || model.Samples[0].PredictedCenter != 2.7 ||
		!model.Anchor.PredictionReady || model.Anchor.PredictedCenter != -1.4 {
		t.Fatalf("Fast drift causal validation state changed: %+v", model)
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

func TestModelCheckpointRoundTripsVolumeProfileState(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	first := newCheckpointTestStrategy(root)
	first.MarketMaker.VolumeProfile = VolumeProfileConfig{
		Enabled: true, HalfLife: types.Duration(45 * time.Minute),
		BinWidthBps: 1, MaxBins: 64, MinEffectiveTrades: 1,
	}
	first.initializeAdaptiveFastModels()
	config := first.MarketMaker
	config.setDefaults()
	first.observeMakerReplayTrade(makerStartupTrade{when: now.Add(-3 * time.Second), trade: types.Trade{
		ID: 1, Symbol: first.Symbol, Price: fixedpoint.NewFromFloat(99.99),
		Quantity: fixedpoint.One, Side: types.SideTypeSell,
	}}, config)
	first.observeMakerReplayTrade(makerStartupTrade{when: now.Add(-2 * time.Second), trade: types.Trade{
		ID: 2, Symbol: first.Symbol, Price: fixedpoint.NewFromFloat(100.01),
		Quantity: fixedpoint.One, Side: types.SideTypeBuy,
	}}, config)
	first.observeMakerReplayBBO(now.Add(-time.Second), evidenceBBO(first.Symbol, 99.99, 1, 100.01, 1), false, config)
	window := config.FastModelWindows()[0]
	before := first.makerHorizonModel.volumeProfiles[window].Snapshot(100)
	if !before.Valid {
		t.Fatalf("test setup did not build a valid volume profile: %+v", before)
	}
	if err := first.prepareModelCheckpoint(now); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(first.State)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}

	restarted := newCheckpointTestStrategy(root)
	restarted.MarketMaker.VolumeProfile = first.MarketMaker.VolumeProfile
	restarted.State = &state
	restarted.initializeAdaptiveFastModels()
	if _, restored, err := restarted.restoreModelCheckpoint(now); err != nil || !restored {
		t.Fatalf("volume-profile checkpoint restore failed: restored=%v err=%v", restored, err)
	}
	after := restarted.makerHorizonModel.volumeProfiles[window].Snapshot(100)
	if !after.Valid || after.EffectiveTrades != before.EffectiveTrades || after.POCDistanceBps != before.POCDistanceBps {
		t.Fatalf("restored volume profile differs: before=%+v after=%+v", before, after)
	}
	point := restarted.makerHorizonModel.points[len(restarted.makerHorizonModel.points)-1]
	if historical := point.volumeProfileState(window); !historical.Valid {
		t.Fatalf("historical BBO point lost its volume-profile snapshot: %+v", historical)
	}
}

func TestMakerStartupCausallyLoadsTradesAndRecordsCheckpoint(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 17, 8, 0, 5, 0, time.UTC)
	strategy := newCheckpointTestStrategy(root)
	strategy.Config.setDefaults()
	strategy.MarketMaker.VolumeProfile = VolumeProfileConfig{
		Enabled: true, HalfLife: types.Duration(45 * time.Minute),
		BinWidthBps: 1, MaxBins: 64, MinEffectiveTrades: 1,
	}
	strategy.initializeAdaptiveFastModels()
	strategy.makerExecutableCrossingModel = NewExecutableCrossingModel(strategy.Symbol, strategy.Barrier, strategy.Intensity)
	collector := filepath.Join(root, strategy.Symbol)
	if err := os.MkdirAll(collector, 0o755); err != nil {
		t.Fatal(err)
	}
	day := now.Format(time.DateOnly)
	book := "received_at,bid,bid_quantity,ask,ask_quantity,gap_before_ms\n" +
		fmt.Sprintf("%s,99.99,1,100.01,1,0\n", now.Add(-3*time.Second).Format(time.RFC3339Nano)) +
		fmt.Sprintf("%s,100.00,1,100.02,1,0\n", now.Add(-time.Second).Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(collector, fmt.Sprintf("%s-bookticker-%s.csv", strategy.Symbol, day)), []byte(book), 0o600); err != nil {
		t.Fatal(err)
	}
	trades := "received_at,event_time_ms,trade_id,price,quantity,side\n" +
		fmt.Sprintf("%s,0,1,100.00,1,SELL\n", now.Add(-4*time.Second).Format(time.RFC3339Nano)) +
		fmt.Sprintf("%s,0,2,100.01,1,BUY\n", now.Add(-2*time.Second).Format(time.RFC3339Nano)) +
		fmt.Sprintf("%s,0,3,100.02,1,BUY\n", now.Add(-500*time.Millisecond).Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(collector, fmt.Sprintf("%s-trades-%s.csv", strategy.Symbol, day)), []byte(trades), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := strategy.restoreAndWarmMakerModelsFromBinanceCapture(now); err != nil {
		t.Fatal(err)
	}
	if strategy.State.ModelCheckpoint == nil {
		t.Fatal("startup did not record a model checkpoint")
	}
	if strategy.State.ModelCheckpoint.ReplayAfter != now.Add(-time.Second) ||
		strategy.State.ModelCheckpoint.TradeReplayAfter != now.Add(-2*time.Second) {
		t.Fatalf("unexpected checkpoint cursors: bbo=%s trade=%s",
			strategy.State.ModelCheckpoint.ReplayAfter, strategy.State.ModelCheckpoint.TradeReplayAfter)
	}
	window := strategy.MarketMaker.FastModelWindows()[0]
	state := strategy.makerHorizonModel.volumeProfiles[window].Snapshot(100.01)
	if !state.Valid || state.EffectiveTrades < 1.9 {
		t.Fatalf("startup did not causally warm volume profile: %+v", state)
	}
	lastPoint := strategy.makerHorizonModel.points[len(strategy.makerHorizonModel.points)-1]
	if historical := lastPoint.volumeProfileState(window); !historical.Valid {
		t.Fatalf("last BBO was observed before its prior trades: %+v", historical)
	}
	if len(strategy.makerStartupPendingTrades) != 1 {
		t.Fatalf("trade newer than the last captured BBO must wait for the first live BBO: %d", len(strategy.makerStartupPendingTrades))
	}
	strategy.drainMakerStartupTrades(now, strategy.MarketMaker)
	if len(strategy.makerStartupPendingTrades) != 0 || strategy.makerLastPublicTradeAt != now.Add(-500*time.Millisecond) {
		t.Fatalf("first live BBO did not consume pending causal trades: pending=%d cursor=%s",
			len(strategy.makerStartupPendingTrades), strategy.makerLastPublicTradeAt)
	}
}
