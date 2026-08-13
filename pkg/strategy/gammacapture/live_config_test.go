package gammacapture

import (
	"math"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLiveETHJPYConfigDecodesInventoryControllers(t *testing.T) {
	data, err := os.ReadFile("../../../config/gammacapture-ethjpy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		ExchangeStrategies []struct {
			GammaCapture Config `yaml:"gammacapture"`
		} `yaml:"exchangeStrategies"`
		Sync *struct {
			DisableStartupSync bool `yaml:"disableStartupSync"`
			UserDataStream     *struct {
				Trades bool `yaml:"trades"`
				Orders bool `yaml:"orders"`
			} `yaml:"userDataStream"`
			Sessions []string `yaml:"sessions"`
		} `yaml:"sync"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode live ETHJPY config: %v", err)
	}
	if len(document.ExchangeStrategies) != 1 {
		t.Fatalf("expected one live strategy, got %d", len(document.ExchangeStrategies))
	}
	strategy := &document.ExchangeStrategies[0].GammaCapture
	if err := strategy.Validate(); err != nil {
		t.Fatalf("validate live ETHJPY strategy: %v", err)
	}
	if strategy.MarketMaker.MacroInventory.Enabled {
		t.Fatal("live ETHJPY config must not install a second Macro inventory target")
	}
	if !strategy.MarketMaker.ProbabilityCenteredQuantity.Enabled ||
		!strategy.MarketMaker.JointDistanceQuantity.Enabled {
		t.Fatal("live ETHJPY config must give Fast joint price/quantity ownership")
	}
	if !strategy.MarketMaker.ConditionalExecution.Enabled {
		t.Fatal("live ETHJPY config must enable symmetric conditional Fast execution")
	}
	if !strategy.MarketMaker.PosteriorInventoryTarget {
		t.Fatal("live ETHJPY config must enable posterior inventory targeting")
	}
	if !strategy.MarketMaker.BOCPD45.Enabled || strategy.MarketMaker.BOCPD45.Calibration != "platt" {
		t.Fatalf("live ETHJPY config must enable strictly-prequential Platt BOCPD45: %+v", strategy.MarketMaker.BOCPD45)
	}
	if got := strategy.MarketMaker.EffectiveInventoryTargetRatio(); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("Fast strategic inventory prior must remain 50%%, got %v", got)
	}
	if strategy.MarketMaker.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled {
		t.Fatal("live ETHJPY config must keep Macro active execution disabled")
	}
	if document.Sync == nil || document.Sync.UserDataStream == nil ||
		!document.Sync.UserDataStream.Trades || !document.Sync.UserDataStream.Orders {
		t.Fatal("live ETHJPY config must persist private trades and the complete order lifecycle")
	}
	if !document.Sync.DisableStartupSync {
		t.Fatal("live ETHJPY config must not run REST history sync during strategy startup")
	}
	if len(document.Sync.Sessions) != 1 || document.Sync.Sessions[0] != "binance" {
		t.Fatalf("live ETHJPY persistence must be restricted to binance: %+v", document.Sync.Sessions)
	}
}
