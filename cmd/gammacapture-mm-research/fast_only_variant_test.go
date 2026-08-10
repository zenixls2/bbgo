package main

import (
	"testing"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestFastOnlyFixedHalfVariantDisablesMacroAndIOC(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{
		InventoryTargetRatio:        0.2,
		InventoryCapitalMinRatio:    0.1,
		InventoryCapitalTargetRatio: 0.3,
		InventoryCapitalMaxRatio:    0.7,
	}
	cfg.MacroInventory.Enabled = true
	cfg.MacroInventory.NoTradeRegion.Enabled = true
	cfg.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled = true

	selected := selectNoTradeIOCVariants(noTradeIOCVariants(cfg), "fast-only-fixed-half")
	if len(selected) != 1 {
		t.Fatalf("expected one fast-only variant, got %d", len(selected))
	}
	got := selected[0]
	if got.MacroInventoryEnabled || got.ActiveIOCEnabled || got.Config.MacroInventory.Enabled ||
		got.Config.MacroInventory.NoTradeRegion.Enabled ||
		got.Config.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled {
		t.Fatalf("fast-only variant retained Macro behavior: %+v", got)
	}
	if got.Config.InventoryTargetRatio != 0.5 || got.Config.InventoryCapitalMinRatio != 0 ||
		got.Config.InventoryCapitalTargetRatio != 0.5 || got.Config.InventoryCapitalMaxRatio != 1 {
		t.Fatalf("fast-only variant is not centered on a full-range 50/50 target: %+v", got.Config)
	}
	if !cfg.MacroInventory.Enabled || !cfg.MacroInventory.NoTradeRegion.Enabled ||
		!cfg.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled {
		t.Fatal("fast-only variant construction mutated input config")
	}
}
