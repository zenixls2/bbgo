package gammacapture

import (
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
	if !strategy.MarketMaker.MacroInventory.Enabled {
		t.Fatal("live ETHJPY config must enable Macro inventory decisions")
	}
	if !strategy.MarketMaker.MacroInventory.NoTradeRegion.Enabled {
		t.Fatal("live ETHJPY config must enable the QV no-trade controller")
	}
	if strategy.MarketMaker.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled {
		t.Fatal("live ETHJPY config must keep Macro active execution disabled")
	}
	if !strategy.MarketMaker.HawkesDirection.Enabled {
		t.Fatal("live ETHJPY config must enable Hawkes direction")
	}
}
