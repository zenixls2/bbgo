package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestEffectiveInventoryRiskBudgetScalesWithPairEquity(t *testing.T) {
	c := MarketMakerConfig{InventoryRiskBudgetJPY: 10, InventoryRiskBudgetRatio: 0.0025}
	if got := c.EffectiveInventoryRiskBudgetJPY(1_000); math.Abs(got-10) > 1e-9 {
		t.Fatalf("absolute floor should apply to small equity, got %.4f", got)
	}
	if got := c.EffectiveInventoryRiskBudgetJPY(10_000); math.Abs(got-25) > 1e-9 {
		t.Fatalf("risk budget should scale with pair equity, got %.4f", got)
	}
}

func TestDynamicInventoryBandUsesPairCapital(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, InventoryRiskBudgetJPY: 10, InventoryRiskBudgetRatio: 0.0025,
		InventoryRiskZScore: 1.645, InventoryMaxOrderLevels: 32, InventoryTargetRatio: 0.5,
		InventoryCapitalTargetRatio: 0.25, InventoryCapitalMaxRatio: 0.50,
	}
	band := c.DynamicInventoryBandWithCapital(12_200, 10, 10*time.Minute, 10_000)
	if band.MaxInventory <= 0 || band.Target <= 0 || band.Limit <= 0 {
		t.Fatalf("expected positive dynamic inventory band: %+v", band)
	}
	if band.TargetRatio < 0.499999 || band.TargetRatio > 0.500001 {
		t.Fatalf("capital target/max ratios should produce a 0.5 target ratio, got %.6f", band.TargetRatio)
	}
	if band.MaxInventory*12_200 > 10_000*0.50+1e-9 {
		t.Fatalf("inventory cap must not exceed pair-equity cap: %+v", band)
	}
	if math.Abs((band.Target+band.Limit)-band.MaxInventory) > 1e-12 {
		t.Fatalf("target and limit must form the upper cap: %+v", band)
	}
}
