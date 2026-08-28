package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func macroActiveExecutionInput(direction int) MacroActiveExecutionInput {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	return MacroActiveExecutionInput{
		Now: now, LatestClosedBarAt: now.Add(-10 * time.Minute),
		Direction: direction, AggregateNetEdgeBps: 120,
		ForecastHorizon: time.Hour, ExecutionHorizon: 15 * time.Minute, ConfidenceZScore: 1.645,
		CurrentInventoryBase: 1, TargetInventoryBase: 2,
		BaselineInventoryBase: 1,
		AvailableBase:         2, AvailableQuote: 1_000,
		BestBid: 99.9, BestBidSize: 0.20,
		BestAsk: 100, BestAskSize: 0.10,
		PassiveTouchEvents: 10, PassiveTouchRatePerHour: 1,
		MakerFeeBps: 10, TakerFeeBps: 10,
	}
}

func TestMacroActiveExecutionBuyUsesStatisticalThresholdAndDepthCap(t *testing.T) {
	in := macroActiveExecutionInput(1)
	d := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in)
	if !d.Trigger {
		t.Fatalf("expected fee-positive active BUY: %+v", d)
	}
	if d.Quantity <= 0 || d.Quantity > in.BestAskSize {
		t.Fatalf("BUY probabilistic tranche escaped visible ask depth: %+v", d)
	}
	if d.WorstPrice < in.BestAsk || d.MaximumImpactBps <= 0 {
		t.Fatalf("BUY limit is not a marketable economic worst price: %+v", d)
	}
	worstCost := math.Log(d.WorstPrice/in.BestAsk) * 10_000
	if math.Abs(worstCost-d.MaximumImpactBps) > 1e-9 {
		t.Fatalf("BUY worst-price budget mismatch: cost=%.12f decision=%+v", worstCost, d)
	}
}

func TestMacroActiveExecutionSellUsesStatisticalThresholdAndDepthCap(t *testing.T) {
	in := macroActiveExecutionInput(-1)
	in.AggregateNetEdgeBps = -120
	in.CurrentInventoryBase = 2
	in.TargetInventoryBase = 1
	in.BaselineInventoryBase = 2
	d := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in)
	if !d.Trigger {
		t.Fatalf("expected fee-positive active SELL: %+v", d)
	}
	if d.Quantity <= 0 || d.Quantity > in.BestBidSize {
		t.Fatalf("SELL probabilistic tranche escaped visible bid depth: %+v", d)
	}
	if d.WorstPrice > in.BestBid || d.MaximumImpactBps <= 0 {
		t.Fatalf("SELL limit is not a marketable economic worst price: %+v", d)
	}
	worstCost := math.Log(in.BestBid/d.WorstPrice) * 10_000
	if math.Abs(worstCost-d.MaximumImpactBps) > 1e-9 {
		t.Fatalf("SELL worst-price budget mismatch: cost=%.12f decision=%+v", worstCost, d)
	}
}

func TestMacroActiveExecutionWaitsWhenCrossingCostWins(t *testing.T) {
	in := macroActiveExecutionInput(1)
	in.AggregateNetEdgeBps = 30
	d := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "passive wait loss does not exceed crossing cost" {
		t.Fatalf("low-edge signal should remain passive: %+v", d)
	}
	if d.WaitLossBps <= 0 || d.PassiveToTouchCostBps <= d.WaitLossBps {
		t.Fatalf("test did not exercise the economic threshold: %+v", d)
	}
}

func TestMacroActiveExecutionFailsClosedWithoutStatisticsOrDepth(t *testing.T) {
	in := macroActiveExecutionInput(1)
	in.PassiveTouchEvents = 0
	if got := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in); got.Trigger || got.Reason != "insufficient passive touch statistics" {
		t.Fatalf("missing arrival data must fail closed: %+v", got)
	}
	in = macroActiveExecutionInput(1)
	in.BestAskSize = 0
	if got := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in); got.Trigger || got.Reason != "opposite-side visible depth unavailable" {
		t.Fatalf("missing L1 depth must fail closed: %+v", got)
	}
}

func TestMacroActiveExecutionRunsAtMostOncePerClosedBar(t *testing.T) {
	in := macroActiveExecutionInput(1)
	in.LastExecutionBarAt = in.LatestClosedBarAt
	got := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in)
	if got.Trigger || got.Reason != "macro bar already actively executed" {
		t.Fatalf("same Macro information set produced repeated IOC: %+v", got)
	}
}

func TestMacroActiveExecutionFeeDifferenceEntersThreshold(t *testing.T) {
	in := macroActiveExecutionInput(1)
	base := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in)
	in.TakerFeeBps = 70
	expensive := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in)
	if !base.Trigger || expensive.Trigger || expensive.FeeIncrementBps != 60 {
		t.Fatalf("maker/taker fee difference was not priced into crossing: base=%+v expensive=%+v", base, expensive)
	}
}

func TestMacroActiveExecutionCaptured0130BullishUsesPartialIOC(t *testing.T) {
	now := time.Date(2026, 8, 5, 16, 30, 0, 0, time.UTC)
	const (
		equity = 6848.526720955
		mid    = 298769.5
	)
	in := MacroActiveExecutionInput{
		Now: now, LatestClosedBarAt: now,
		Direction: 1, AggregateNetEdgeBps: 9.178356918662397 / 3,
		ForecastHorizon:  13*time.Hour + 20*time.Minute,
		ExecutionHorizon: 15 * time.Minute, ConfidenceZScore: 1.645,
		CurrentInventoryBase:  0.01111817,
		TargetInventoryBase:   0.55035 * equity / mid,
		BaselineInventoryBase: 0.4827141503172136 * equity / mid,
		AvailableQuote:        3_526, AvailableBase: 0.01111817,
		BestBid: 298769, BestBidSize: 0.3,
		BestAsk: 298770, BestAskSize: 0.3,
		PassiveTouchEvents: 61, PassiveTouchRatePerHour: 0.17029304777722487,
		MakerFeeBps: 10, TakerFeeBps: 10,
		MinimumQuantityBase: 0.00001, MinimumNotionalJPY: 100,
	}
	d := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in)
	if !d.Trigger {
		t.Fatalf("01:30 bullish snapshot should authorize a partial IOC: %+v", d)
	}
	if d.Quantity <= 0 || d.Quantity >= d.TargetGapBase ||
		d.ResidualMakerGapBase <= 0 || d.Quantity*in.BestAsk < in.MinimumNotionalJPY {
		t.Fatalf("01:30 bullish IOC was not a valid partial tranche: %+v", d)
	}
}

func TestMacroActiveExecutionCaptured0728BearishStaysPassive(t *testing.T) {
	now := time.Date(2026, 8, 5, 22, 28, 3, 0, time.UTC)
	const (
		equity = 6873.61005727
		mid    = 301143.5
	)
	in := MacroActiveExecutionInput{
		Now: now, LatestClosedBarAt: now,
		Direction: -1, AggregateNetEdgeBps: -1.2195784543266464 / 3,
		ForecastHorizon:  3*time.Hour + 40*time.Minute,
		ExecutionHorizon: 30 * time.Minute, ConfidenceZScore: 1.645,
		CurrentInventoryBase:  0.01280546,
		TargetInventoryBase:   0.42779 * equity / mid,
		BaselineInventoryBase: 0.48450340152972615 * equity / mid,
		AvailableBase:         0.01099546, AvailableQuote: 3_017,
		BestBid: 301143, BestBidSize: 0.3,
		BestAsk: 301144, BestAskSize: 0.3,
		PassiveTouchEvents: 51, PassiveTouchRatePerHour: 0.9054052928439442,
		MakerFeeBps: 10, TakerFeeBps: 10,
		MinimumQuantityBase: 0.00001, MinimumNotionalJPY: 100,
	}
	d := EvaluateMacroActiveExecution(MacroActiveExecutionConfig{Enabled: true}, in)
	if d.Trigger || d.Reason != "probabilistic IOC tranche below exchange minimum" {
		t.Fatalf("07:28 bearish snapshot should remain passive: %+v", d)
	}
}

func TestMacroMarketableIOCPriceUsesSideAwareEconomicRounding(t *testing.T) {
	market := types.Market{TickSize: fixedpoint.MustNewFromString("0.1")}
	touch := fixedpoint.MustNewFromString("100")
	buy, ok := macroMarketableIOCPrice(market, types.SideTypeBuy, touch, 100.19)
	if !ok || buy.String() != "100.1" || buy.Float64() > 100.19 || buy.Compare(touch) < 0 {
		t.Fatalf("BUY worst price did not floor safely: price=%s ok=%t", buy, ok)
	}
	sell, ok := macroMarketableIOCPrice(market, types.SideTypeSell, touch, 99.81)
	if !ok || sell.String() != "99.9" || sell.Float64() < 99.81 || sell.Compare(touch) > 0 {
		t.Fatalf("SELL worst price did not ceil safely: price=%s ok=%t", sell, ok)
	}
}

func TestMarketableIOCPriceClampsPercentPriceBySide(t *testing.T) {
	market := types.Market{
		TickSize:                      fixedpoint.MustNewFromString("0.1"),
		PercentPriceBidMultiplierDown: fixedpoint.MustNewFromString("0.98"),
		PercentPriceBidMultiplierUp:   fixedpoint.MustNewFromString("1.02"),
		PercentPriceAskMultiplierDown: fixedpoint.MustNewFromString("0.98"),
		PercentPriceAskMultiplierUp:   fixedpoint.MustNewFromString("1.02"),
	}
	ref := fixedpoint.MustNewFromString("100")
	touch := fixedpoint.MustNewFromString("100")
	buy, ok, clamped := marketableIOCPriceWithReference(market, types.SideTypeBuy, touch, 105, ref)
	if !ok || !clamped || buy.String() != "102" {
		t.Fatalf("BUY IOC should be clamped to side-aware upper bound: price=%s ok=%t clamped=%t", buy, ok, clamped)
	}
	sell, ok, clamped := marketableIOCPriceWithReference(market, types.SideTypeSell, touch, 95, ref)
	if !ok || !clamped || sell.String() != "98" {
		t.Fatalf("SELL IOC should be clamped to side-aware lower bound: price=%s ok=%t clamped=%t", sell, ok, clamped)
	}
	market.PercentPriceBidMultiplierUp = fixedpoint.MustNewFromString("0.99")
	if _, ok, _ := marketableIOCPriceWithReference(market, types.SideTypeBuy, touch, 100, ref); ok {
		t.Fatalf("BUY should fail closed when exchange upper bound is below the ask touch")
	}
}
