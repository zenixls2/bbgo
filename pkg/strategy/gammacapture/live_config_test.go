package gammacapture

import (
	"math"
	"os"
	"testing"
	"time"

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
	if strategy.MarketMaker.JointDistanceQuantity.JointHorizonSelection {
		t.Fatal("live ETHJPY config must select one complete horizon evidence bundle before joint price/quantity optimization")
	}
	if !strategy.MarketMaker.ConditionalExecution.Enabled {
		t.Fatal("live ETHJPY config must enable symmetric conditional Fast execution")
	}
	if !strategy.MarketMaker.PosteriorInventoryTarget {
		t.Fatal("live ETHJPY config must enable posterior inventory targeting")
	}
	if strategy.MarketMaker.DynamicInventoryAim.Enabled || strategy.MarketMaker.DynamicInventoryAim.ShadowOnly {
		t.Fatalf("live ETHJPY config must keep the retired DynamicInventoryAim inactive: %+v", strategy.MarketMaker.DynamicInventoryAim)
	}
	if strategy.MarketMaker.DynamicInventoryAim.RegimeConditionedTarget.Enabled ||
		math.Abs(strategy.MarketMaker.DynamicInventoryAim.RegimeConditionedTarget.Kappa-0.20) > 1e-12 ||
		math.Abs(strategy.MarketMaker.DynamicInventoryAim.RegimeConditionedTarget.MaxShiftRatio-0.20) > 1e-12 {
		t.Fatalf("live ETHJPY config must retain but disable the rejected regime-conditioned target: %+v", strategy.MarketMaker.DynamicInventoryAim.RegimeConditionedTarget)
	}
	pivotTarget := strategy.MarketMaker.DynamicInventoryAim.PivotRegimeTarget
	if pivotTarget.Enabled || math.Abs(pivotTarget.ReversalBps-26) > 1e-12 ||
		time.Duration(pivotTarget.MaxGap) != 15*time.Minute || pivotTarget.MinLegSamples != 2 ||
		math.Abs(pivotTarget.PriorLegSamples-2) > 1e-12 || math.Abs(pivotTarget.MaxShiftRatio-0.20) > 1e-12 {
		t.Fatalf("live ETHJPY config must retain but disable the pivot-first target pending private-fill calibration: %+v", pivotTarget)
	}
	if !strategy.MarketMaker.BOCPD45.Enabled || strategy.MarketMaker.BOCPD45.Calibration != "platt" {
		t.Fatalf("live ETHJPY config must enable strictly-prequential Platt BOCPD45: %+v", strategy.MarketMaker.BOCPD45)
	}
	if !strategy.MarketMaker.RelativeHoldRisk.Enabled || strategy.MarketMaker.RelativeHoldRisk.ShadowOnly {
		t.Fatalf("live ETHJPY config must enable the Relative-Hold canary as an active Fast utility scalar: %+v", strategy.MarketMaker.RelativeHoldRisk)
	}
	if strategy.MarketMaker.RelativeHoldRisk.Horizon != time.Hour ||
		strategy.MarketMaker.RelativeHoldRisk.HalfLife != 6*time.Hour ||
		strategy.MarketMaker.RelativeHoldRisk.MinimumEffectiveSamples != 4 {
		t.Fatalf("live Relative-Hold canary must match the five-day replay parameters: %+v", strategy.MarketMaker.RelativeHoldRisk)
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
