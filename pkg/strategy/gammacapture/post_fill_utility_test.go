package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func postFillUtilityTrendModel(up bool) (MarketMakerHorizonModel, time.Time, float64, float64) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	trend := 3.0
	if !up {
		trend = -3
	}
	for i := 0; i <= 7*60; i++ {
		pathBps := trend*float64(i) + 30*math.Sin(2*math.Pi*float64(i)/10)
		mid := 100 * math.Exp(pathBps/10_000)
		model.points = append(model.points, MarketMakerHorizonPoint{
			At:  start.Add(time.Duration(i) * time.Minute),
			Bid: mid * math.Exp(-0.5/10_000), Ask: mid * math.Exp(0.5/10_000), Mid: mid,
		})
	}
	last := model.points[len(model.points)-1]
	return model, last.At, last.Bid, last.Ask
}

func TestPostFillUtilityAllowsStatisticallySupportedNegativeCycleEdge(t *testing.T) {
	model, now, bid, ask := postFillUtilityTrendModel(true)
	mid := (bid + ask) / 2
	plan := MarketMakerQuotePlan{
		Reason: "quoted", AllowBid: true, AllowAsk: true,
		BidPrice: ask * math.Exp(-45.0/10_000), AskPrice: bid * math.Exp(45.0/10_000),
		BidTouchDistanceBps: 45, AskTouchDistanceBps: 45,
	}
	cfg := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		InventoryMaxOrderLevels: 6, HorizonMinSamples: 6,
		PostFillUtility: PostFillUtilityConfig{Enabled: true, MinimumSamples: 6, ConfidenceZScore: 1.0},
	}
	decision := cfg.ApplyPostFillUtility(&model, PostFillUtilityInput{
		Now: now, Fill: MakerPostFillState{Side: types.SideTypeSell, Price: plan.BidPrice * 0.99, Quantity: 1, At: now.Add(-time.Second)},
		Plan: plan, BestBid: bid, BestAsk: ask, Mid: mid, Horizon: 10 * time.Minute,
		InventoryBase: 50, InventoryTargetBase: 50, PairEquityJPY: 10_000,
		ExpectedFillNotionalJPY: 100, VolatilityBpsPerSqrtSec: 1, RiskAversion: 1,
	})
	if !decision.Applied {
		t.Fatalf("expected rising-path post-fill BUY utility to spend edge: %+v", decision)
	}
	if decision.SelectedDistanceBps >= decision.BaseDistanceBps {
		t.Fatalf("expected inward BUY price: %+v", decision)
	}
	if decision.CycleEdgeBps >= 0 {
		t.Fatalf("last fill must not become a hard positive-cycle gate: %+v", decision)
	}
	if decision.IncrementalLowerBps <= 0 {
		t.Fatalf("applied decision needs positive paired lower bound: %+v", decision)
	}
}

func TestPostFillUtilityRejectsAdverseBuyAfterTouch(t *testing.T) {
	model, now, bid, ask := postFillUtilityTrendModel(false)
	mid := (bid + ask) / 2
	plan := MarketMakerQuotePlan{
		Reason: "quoted", AllowBid: true, AllowAsk: true,
		BidPrice: ask * math.Exp(-45.0/10_000), AskPrice: bid * math.Exp(45.0/10_000),
		BidTouchDistanceBps: 45, AskTouchDistanceBps: 45,
	}
	cfg := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		InventoryMaxOrderLevels: 6, HorizonMinSamples: 6,
		PostFillUtility: PostFillUtilityConfig{Enabled: true, MinimumSamples: 6, ConfidenceZScore: 1.0},
	}
	decision := cfg.ApplyPostFillUtility(&model, PostFillUtilityInput{
		Now: now, Fill: MakerPostFillState{Side: types.SideTypeSell, Price: ask, Quantity: 1, At: now.Add(-time.Second)},
		Plan: plan, BestBid: bid, BestAsk: ask, Mid: mid, Horizon: 10 * time.Minute,
		InventoryBase: 50, InventoryTargetBase: 50, PairEquityJPY: 10_000,
		ExpectedFillNotionalJPY: 100, VolatilityBpsPerSqrtSec: 1, RiskAversion: 1,
	})
	if decision.Applied {
		t.Fatalf("continued downside after BUY touch must not be chased: %+v", decision)
	}
	if decision.SelectedPrice <= 0 || decision.CycleEdgeBps == 0 {
		t.Fatalf("rejected candidate must retain price and cycle-edge diagnostics: %+v", decision)
	}
}

func TestPostFillInventoryRiskBenefitChangesSignAtTarget(t *testing.T) {
	base := PostFillUtilityInput{
		Mid: 100, PairEquityJPY: 10_000, ExpectedFillNotionalJPY: 1_000,
		Horizon: 10 * time.Minute, VolatilityBpsPerSqrtSec: 1, RiskAversion: 1,
		InventoryTargetBase: 50,
	}
	base.InventoryBase = 40
	if got := postFillInventoryRiskBenefitBps(types.SideTypeBuy, base); got <= 0 {
		t.Fatalf("buy toward target should reduce inventory risk, got %f", got)
	}
	base.InventoryBase = 60
	if got := postFillInventoryRiskBenefitBps(types.SideTypeBuy, base); got >= 0 {
		t.Fatalf("buy away from target should increase inventory risk, got %f", got)
	}
}
