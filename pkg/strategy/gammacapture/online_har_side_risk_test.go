package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestOnlineSideHARVarianceRiskWarmsWithoutPretraining(t *testing.T) {
	model := NewOnlineSideHARVarianceRisk(15 * time.Minute)
	at := time.Date(2026, 8, 3, 0, 0, 10, 0, time.UTC)
	price := 300_000.0
	for minute := 0; minute < 8*60; minute++ {
		volatility := .00005
		if (minute/45)%2 == 1 {
			volatility = .00020
		}
		price *= math.Exp(volatility * math.Sin(float64(minute)*1.7))
		model.ObserveBBO(at, price*.99995, price*1.00005, false)
		at = at.Add(time.Minute)
	}
	decision := model.Snapshot()
	if !decision.Healthy {
		t.Fatalf("online side HAR failed to warm from live observations: %+v", decision)
	}
	if decision.Buy.Samples < 20 || decision.Sell.Samples < 20 {
		t.Fatalf("matured samples buy=%d sell=%d, want >=20", decision.Buy.Samples, decision.Sell.Samples)
	}
	if decision.VarianceRatePerSecond <= 0 || decision.BaselineRatePerSecond <= 0 {
		t.Fatalf("invalid variance rates: %+v", decision)
	}
}

func TestOnlineSideHARVarianceRiskRejectsWindowAcrossGap(t *testing.T) {
	model := NewOnlineSideHARVarianceRisk(15 * time.Minute)
	at := time.Date(2026, 8, 3, 0, 0, 10, 0, time.UTC)
	price := 300_000.0
	for minute := 0; minute < 4*60; minute++ {
		price *= math.Exp(.0001 * math.Sin(float64(minute)))
		model.ObserveBBO(at, price*.99995, price*1.00005, false)
		at = at.Add(time.Minute)
	}
	before := model.Snapshot()
	model.ObserveBBO(at.Add(3*time.Minute), price*.99995, price*1.00005, true)
	model.ObserveBBO(at.Add(4*time.Minute), price*.99995, price*1.00005, false)
	after := model.Snapshot()
	if after.At.After(before.At) && after.At.Sub(before.At) < 15*time.Minute && after.Healthy && after.At.Equal(at.Add(3*time.Minute).Truncate(time.Minute)) {
		t.Fatalf("gap-crossing window unexpectedly produced a fresh healthy decision: before=%+v after=%+v", before, after)
	}
}

func TestCombineSideHARRequiresBothStressPosteriors(t *testing.T) {
	buy := HARVarianceDecision{Healthy: true, CurrentVariance: 1, ForecastVariance: 2, ElevatedRisk: true}
	sell := HARVarianceDecision{Healthy: true, CurrentVariance: 1, ForecastVariance: 3, ElevatedRisk: false}
	decision := combineSideHARDecision(time.Now(), 15*time.Minute, buy, sell)
	if !decision.Healthy {
		t.Fatal("healthy side forecasts should combine")
	}
	if decision.ElevatedRisk {
		t.Fatal("one-sided stress must not trigger common inventory risk")
	}
	if decision.ConservativeForecastVariance != 3 {
		t.Fatalf("conservative variance = %f, want 3", decision.ConservativeForecastVariance)
	}
}
