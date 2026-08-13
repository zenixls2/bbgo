package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestJointPathPayoffPenalizesSellingIntoContinuation(t *testing.T) {
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		HorizonLookback: types.Duration(50 * time.Minute),
	}
	for second := 0; second <= 60*60; second++ {
		mid := 100 * math.Exp(float64(second)*2.0/60.0/10_000)
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	stats := model.JointPathPayoffStatistics(
		start.Add(time.Hour), config, 10*time.Minute, 5, 5)
	if stats.EffectiveSamples < 2 {
		t.Fatalf("insufficient path samples: %+v", stats)
	}
	if stats.SellDominant.SellMeanBps >= 0 {
		t.Fatalf("continued rise must make passive SELL terminal wealth adverse: %+v", stats)
	}
	if math.Abs(stats.BuyDominant.BuyMeanBps) > 1e-9 {
		t.Fatalf("untouched BUY should contribute zero payoff: %+v", stats)
	}
}

func TestJointPathPayoffRewardsCompletedOscillation(t *testing.T) {
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		HorizonLookback: types.Duration(40 * time.Minute),
	}
	horizon := 5 * time.Minute
	for second := 0; second <= 45*60; second++ {
		phase := 2 * math.Pi * float64(second) / horizon.Seconds()
		mid := 100 * math.Exp(0.006*math.Sin(phase))
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	stats := model.JointPathPayoffStatistics(
		start.Add(45*time.Minute), config, horizon, 30, 30)
	decision := stats.Evaluate(500, 500, 7000, 1, 1.282)
	if stats.EffectiveSamples < 3 {
		t.Fatalf("insufficient path samples: %+v", stats)
	}
	if decision.ExpectedPnLJPY <= 0 || decision.CertaintyEquivalent <= 0 {
		t.Fatalf("completed fee-net oscillation should have positive wealth utility: stats=%+v decision=%+v", stats, decision)
	}
}

func TestWholePositionUtilityRewardsRiskReducingSellAndRestrainsBuy(t *testing.T) {
	moments := jointPathPayoffMoments{
		BuyMeanBps:           2,
		SellMeanBps:          -1,
		InventoryMeanBps:     -40,
		BuyVarBps2:           400,
		SellVarBps2:          400,
		InventoryVarBps2:     1_600,
		InventoryBuyCovBps2:  700,
		InventorySellCovBps2: -700,
	}
	stats := JointPathPayoffStats{
		EffectiveSamples: 100,
		BuyDominant:      moments,
		SellDominant:     moments,
	}
	const (
		inventoryJPY = 8_000
		orderJPY     = 1_000
		equityJPY    = 10_000
		riskAversion = 50
	)

	sell := stats.EvaluateWholePosition(
		inventoryJPY, 0, orderJPY, equityJPY, riskAversion, 0)
	if !sell.RiskReducing || sell.MarginalVarianceJPY2 >= 0 || sell.KellyPenaltyJPY >= 0 {
		t.Fatalf("SELL hedge must reduce whole-position variance: %+v", sell)
	}
	if sell.ExpectedPnLJPY >= 0 || sell.CertaintyEquivalent <= 0 {
		t.Fatalf("risk benefit should support a small negative-edge SELL when total wealth improves: %+v", sell)
	}

	buy := stats.EvaluateWholePosition(
		inventoryJPY, orderJPY, 0, equityJPY, riskAversion, 0)
	if buy.RiskReducing || buy.MarginalVarianceJPY2 <= 0 || buy.KellyPenaltyJPY <= 0 {
		t.Fatalf("BUY that compounds long exposure must add whole-position risk: %+v", buy)
	}
	if buy.ExpectedPnLJPY <= 0 || buy.CertaintyEquivalent >= 0 {
		t.Fatalf("local BUY edge must not override larger whole-position risk: %+v", buy)
	}
}

func TestWholePositionUtilityAllowsPositiveReversalBuyAtLowInventory(t *testing.T) {
	moments := jointPathPayoffMoments{
		BuyMeanBps: 40, SellMeanBps: -10,
		BuyVarBps2: 400, SellVarBps2: 400,
		InventoryVarBps2:    900,
		InventoryBuyCovBps2: 50, InventorySellCovBps2: -50,
	}
	stats := JointPathPayoffStats{EffectiveSamples: 100, BuyDominant: moments, SellDominant: moments}
	buy := stats.EvaluateWholePosition(1_000, 1_000, 0, 10_000, 1, 1.282)
	if buy.ExpectedPnLJPY <= 0 || buy.CertaintyEquivalent <= 0 {
		t.Fatalf("strong executable-price reversal evidence should support BUY at low inventory: %+v", buy)
	}
}

func TestWholePositionZeroInventoryMatchesIncrementalModel(t *testing.T) {
	moments := jointPathPayoffMoments{
		BuyMeanBps: 20, SellMeanBps: 15,
		BuyVarBps2: 500, SellVarBps2: 600, CovBps2: -100,
		InventoryMeanBps: 5, InventoryVarBps2: 900,
		InventoryBuyCovBps2: 200, InventorySellCovBps2: -150,
	}
	stats := JointPathPayoffStats{EffectiveSamples: 50, BuyDominant: moments, SellDominant: moments}
	want := stats.Evaluate(400, 300, 10_000, 2, 1.282)
	got := stats.EvaluateWholePosition(0, 400, 300, 10_000, 2, 1.282)
	if got != want {
		t.Fatalf("zero inventory must preserve the incremental model: got=%+v want=%+v", got, want)
	}
}
