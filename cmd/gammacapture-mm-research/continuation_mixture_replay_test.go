package main

import (
	"testing"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestContinuationMixtureReplayVariantUsesOneTargetPath(t *testing.T) {
	variants := selectNoTradeIOCVariants(
		noTradeIOCVariants(gammacapture.MarketMakerConfig{}),
		"qv-continuation-mixture+ioc")
	if len(variants) != 1 {
		t.Fatalf("unexpected mixture variant count: %d", len(variants))
	}
	variant := variants[0]
	if !variant.NoTradeEnabled || !variant.ActiveIOCEnabled ||
		!variant.ContinuationMixtureEnabled || variant.ContinuationEnabled ||
		variant.TrendExcursionEnabled || variant.FastVarianceEnabled {
		t.Fatalf("mixture replay includes a duplicate target or unrelated factor: %+v", variant)
	}
	config := variant.Config.MacroInventory.NoTradeRegion
	if !config.Enabled || !config.ContinuationMixtureEnabled ||
		config.ContinuationEnabled || config.TrendExcursionEnabled ||
		config.FastVarianceRiskEnabled {
		t.Fatalf("mixture config is not an isolated one-target ablation: %+v", config)
	}
}
