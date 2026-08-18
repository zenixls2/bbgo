package gammacapture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestLocalCaptureStartupSmoke is opt-in because it reads the device's real
// capture archive. It exercises the complete pre-order BBO/trade warmup for
// multiple symbols without creating an exchange session or order executor.
func TestLocalCaptureStartupSmoke(t *testing.T) {
	configPath := os.Getenv("GAMMACAPTURE_SMOKE_CONFIG")
	captureRoot := os.Getenv("GAMMACAPTURE_SMOKE_CAPTURE_ROOT")
	if configPath == "" || captureRoot == "" {
		t.Skip("set GAMMACAPTURE_SMOKE_CONFIG and GAMMACAPTURE_SMOKE_CAPTURE_ROOT")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		ExchangeStrategies []struct {
			GammaCapture Config `yaml:"gammacapture"`
		} `yaml:"exchangeStrategies"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.ExchangeStrategies) != 1 {
		t.Fatalf("expected one Gamma strategy, got %d", len(document.ExchangeStrategies))
	}

	symbols := []string{"BTCJPY", "XRPJPY", "SOLJPY"}
	if configured := os.Getenv("GAMMACAPTURE_SMOKE_SYMBOLS"); configured != "" {
		symbols = strings.FieldsFunc(configured, func(r rune) bool { return r == ',' || r == ' ' })
	}
	now := time.Now().UTC()
	for _, symbol := range symbols {
		t.Run(symbol, func(t *testing.T) {
			config := document.ExchangeStrategies[0].GammaCapture
			config.Symbol = symbol
			config.SymbolSelection.Symbols = []string{symbol}
			config.AggTradeWarmup.Path = captureRoot
			config.AggTradeWarmup.LivePath = filepath.Join(captureRoot, "live")
			config.setDefaults()
			if err := config.Validate(); err != nil {
				t.Fatalf("configuration rejected: %v", err)
			}

			strategy := &Strategy{
				Config: config,
				State: &State{
					Engine:         NewCrossingEngine(config.Barrier.Width, time.Duration(config.Barrier.MinDwell), config.Barrier.MaxCrossingsPerEvent),
					MacroInventory: &MacroInventoryState{},
				},
			}
			strategy.model = NewIntensityModel(config.Intensity)
			strategy.initializeAdaptiveFastModels()
			strategy.makerExecutableCrossingModel = NewExecutableCrossingModel(strategy.Symbol, strategy.Barrier, strategy.Intensity)

			started := time.Now()
			if err := strategy.restoreAndWarmMakerModelsFromBinanceCapture(now); err != nil {
				t.Fatalf("complete startup replay failed: %v", err)
			}
			selected := strategy.adaptiveFastSnapshot(now)
			if strategy.State.ModelCheckpoint == nil {
				t.Fatal("startup replay did not produce an in-memory checkpoint")
			}
			beforeBOCPD := strategy.makerBOCPD45.Snapshot()
			t.Logf("startup warmup=%s selectedWindow=%s slowHealth=%s fastHealth=%s evidenceHealth=%s checkpointBBO=%s checkpointTrade=%s",
				time.Since(started), selected.Window, strategy.model.Snapshot(now).Health,
				selected.Model.Health, selected.Evidence.Health,
				strategy.State.ModelCheckpoint.ReplayAfter,
				strategy.State.ModelCheckpoint.TradeReplayAfter)

			persisted, err := json.Marshal(strategy.State)
			if err != nil {
				t.Fatal(err)
			}
			var restoredState State
			if err := json.Unmarshal(persisted, &restoredState); err != nil {
				t.Fatal(err)
			}
			if beforeBOCPD.Ready && (restoredState.ModelCheckpoint.BOCPD45 == nil ||
				len(restoredState.ModelCheckpoint.BOCPD45.Bid.States) == 0 ||
				len(restoredState.ModelCheckpoint.BOCPD45.Ask.States) == 0) {
				beforeCheckpoint := strategy.State.ModelCheckpoint.BOCPD45
				t.Fatalf("serialized checkpoint lost BOCPD run states: before bid=%d ask=%d after=%+v",
					len(beforeCheckpoint.Bid.States), len(beforeCheckpoint.Ask.States), restoredState.ModelCheckpoint.BOCPD45)
			}
			restarted := &Strategy{Config: config, State: &restoredState}
			restarted.model = NewIntensityModel(config.Intensity)
			restarted.initializeAdaptiveFastModels()
			restarted.makerExecutableCrossingModel = NewExecutableCrossingModel(restarted.Symbol, restarted.Barrier, restarted.Intensity)
			restoreStarted := time.Now()
			if err := restarted.restoreAndWarmMakerModelsFromBinanceCapture(now); err != nil {
				t.Fatalf("checkpoint delta startup failed: %v", err)
			}
			restoredSnapshot := restarted.adaptiveFastSnapshot(now)
			afterBOCPD := restarted.makerBOCPD45.Snapshot()
			if beforeBOCPD.Ready && !afterBOCPD.Ready {
				t.Fatalf("checkpoint restart lost BOCPD readiness: before=%+v after=%+v", beforeBOCPD, afterBOCPD)
			}
			t.Logf("checkpoint restart=%s selectedWindow=%s slowHealth=%s fastHealth=%s evidenceHealth=%s",
				time.Since(restoreStarted), restoredSnapshot.Window,
				restarted.model.Snapshot(now).Health,
				restoredSnapshot.Model.Health, restoredSnapshot.Evidence.Health)
		})
	}
}
