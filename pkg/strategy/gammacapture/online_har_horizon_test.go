package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestMarketMakerHorizonModelTrainsAndSelectsValidatedHARRisk(t *testing.T) {
	cfg := MarketMakerConfig{}
	cfg.MacroInventory.NoTradeRegion.FastVarianceRiskEnabled = true
	model := &MarketMakerHorizonModel{}
	at := time.Date(2026, 8, 3, 0, 0, 10, 0, time.UTC)
	price := 300_000.0
	for minute := 0; minute < 13*60; minute++ {
		volatility := .00005
		if (minute/45)%2 == 1 {
			volatility = .00020
		}
		price *= math.Exp(volatility * math.Sin(float64(minute)*1.7))
		model.ObserveBookWithGap(at, price*.99995, price*1.00005, cfg, false)
		at = at.Add(time.Minute)
	}
	for _, test := range []struct {
		window, want time.Duration
	}{
		{10 * time.Minute, 15 * time.Minute},
		{15 * time.Minute, 15 * time.Minute},
		{30 * time.Minute, 30 * time.Minute},
	} {
		decision := model.SideHARVarianceRisk(test.window)
		if !decision.Healthy || decision.Horizon != test.want {
			t.Fatalf("window %s selected invalid HAR risk: %+v", test.window, decision)
		}
	}
}

func TestMarketMakerHorizonModelLeavesHARRiskDisabledByDefault(t *testing.T) {
	model := &MarketMakerHorizonModel{}
	cfg := MarketMakerConfig{}
	model.ObserveBookWithGap(time.Now(), 99, 101, cfg, false)
	decision := model.SideHARVarianceRisk(15 * time.Minute)
	if decision.Healthy || len(model.sideHARVarianceRisk) != 0 {
		t.Fatalf("disabled risk path unexpectedly allocated or trained models: %+v", decision)
	}
}
