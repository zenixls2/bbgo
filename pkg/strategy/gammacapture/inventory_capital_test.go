package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
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
		InventoryCapitalMinRatio: 0.25, InventoryCapitalTargetRatio: 0.50, InventoryCapitalMaxRatio: 0.75,
	}
	band := c.DynamicInventoryBandWithCapital(12_200, 10, 10*time.Minute, 10_000)
	if band.MinInventory <= 0 || band.Target <= band.MinInventory || band.MaxInventory <= band.Target || band.Limit <= 0 {
		t.Fatalf("expected ordered target-centered inventory band: %+v", band)
	}
	if math.Abs(band.Target*12_200-5_000) > 1e-9 {
		t.Fatalf("capital target must remain centered at 50%% of pair equity: %+v", band)
	}
	if band.MinInventory*12_200 < 10_000*0.25-1e-9 || band.MaxInventory*12_200 > 10_000*0.75+1e-9 {
		t.Fatalf("effective band must remain inside capital guardrails: %+v", band)
	}
	if band.Target*12_200-band.MinInventory*12_200 > band.RiskBandHalfWidthNotionalJPY+1e-9 ||
		band.MaxInventory*12_200-band.Target*12_200 > band.RiskBandHalfWidthNotionalJPY+1e-9 {
		t.Fatalf("effective band exceeded statistical half-width: %+v", band)
	}
}

func TestInventoryOrderHeadroomCapsBothSides(t *testing.T) {
	band := InventoryBand{MinInventory: 0.15, Target: 0.30, MaxInventory: 0.45}
	if got := inventoryBuyHeadroomNotional(band, 0.287, 12_000); math.Abs(got-1_956) > 1e-9 {
		t.Fatalf("unexpected buy headroom: %.8f", got)
	}
	if got := inventorySellHeadroomQuantity(band, 0.287); math.Abs(got-0.137) > 1e-12 {
		t.Fatalf("unexpected sell headroom: %.8f", got)
	}
	if got := inventoryBuyHeadroomNotional(band, 0.45, 12_000); got != 0 {
		t.Fatalf("buy headroom must close at upper edge: %.8f", got)
	}
	if got := inventorySellHeadroomQuantity(band, 0.15); got != 0 {
		t.Fatalf("sell headroom must close at lower edge: %.8f", got)
	}
}

func TestTargetCenteredOrderCapsPreventFullBandTraversal(t *testing.T) {
	const price = 10_000.0
	band := InventoryBand{
		MinInventory: 0, Target: 0.5, MaxInventory: 1,
		RiskBandHalfWidthNotionalJPY: 5_000,
	}

	atCash := targetCenteredInventoryOrderCaps(band, 0, price, 10)
	if math.Abs(atCash.TrancheNotional-500) > 1e-9 || math.Abs(atCash.BuyNotional-5_000) > 1e-9 {
		t.Fatalf("cash-edge cap should stop at target: %+v", atCash)
	}
	if ending := atCash.BuyNotional / price; math.Abs(ending-band.Target) > 1e-12 {
		t.Fatalf("one corrective buy crossed target: ending=%f target=%f", ending, band.Target)
	}

	atBase := targetCenteredInventoryOrderCaps(band, 1, price, 10)
	if math.Abs(atBase.TrancheNotional-500) > 1e-9 || math.Abs(atBase.SellQuantity-0.5) > 1e-12 {
		t.Fatalf("base-edge cap should stop at target: %+v", atBase)
	}
	if ending := 1 - atBase.SellQuantity; math.Abs(ending-band.Target) > 1e-12 {
		t.Fatalf("one corrective sell crossed target: ending=%f target=%f", ending, band.Target)
	}

	atTarget := targetCenteredInventoryOrderCaps(band, band.Target, price, 10)
	if math.Abs(atTarget.BuyNotional-500) > 1e-9 || math.Abs(atTarget.SellQuantity-0.05) > 1e-12 {
		t.Fatalf("target should use one equity-scaled tranche per side: %+v", atTarget)
	}
}

func TestExchangeFeasibleInventoryCapsFloorSubMinimumTargetTranche(t *testing.T) {
	market := types.Market{
		MinNotional: fixedpoint.MustNewFromString("100"),
		MinQuantity: fixedpoint.MustNewFromString("0.00001"),
		StepSize:    fixedpoint.MustNewFromString("0.00001"),
		TickSize:    fixedpoint.MustNewFromString("1"),
	}
	bidPrice := fixedpoint.MustNewFromString("290364")
	askPrice := fixedpoint.MustNewFromString("293223")
	modelBuyCap := fixedpoint.MustNewFromString("66.83646190")
	modelSellCap := modelBuyCap.Div(askPrice)
	hardBuyCap := fixedpoint.MustNewFromString("2137.20935710")
	hardSellCap := fixedpoint.MustNewFromString("0.0073")

	buyCapacity := makerBuyInventoryCapacity(market, bidPrice, modelBuyCap, hardBuyCap)
	buyQuantity, buyOK := market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, bidPrice, buyCapacity)
	if !buyOK || buyQuantity.String() != "0.00035" {
		t.Fatalf("sub-minimum BUY tranche should floor to an executable lattice quantity: capacity=%s quantity=%s ok=%v",
			buyCapacity, buyQuantity, buyOK)
	}

	sellCapacity := makerSellInventoryCapacity(market, askPrice, modelSellCap, hardSellCap)
	strategy := Strategy{Market: market}
	sellQuantity, sellOK := strategy.makerAskQuantity(
		askPrice, sellCapacity, fixedpoint.MustNewFromString("2138.76678095"))
	if !sellOK || sellQuantity.String() != "0.00035" {
		t.Fatalf("sub-minimum SELL tranche should floor to an executable lattice quantity: capacity=%s quantity=%s ok=%v",
			sellCapacity, sellQuantity, sellOK)
	}
}

func TestExchangeFeasibleInventoryCapsNeverCrossHardBand(t *testing.T) {
	market := types.Market{
		MinNotional: fixedpoint.MustNewFromString("100"),
		MinQuantity: fixedpoint.MustNewFromString("0.00001"),
		StepSize:    fixedpoint.MustNewFromString("0.00001"),
		TickSize:    fixedpoint.MustNewFromString("1"),
	}
	price := fixedpoint.MustNewFromString("293223")
	modelBuyCap := fixedpoint.MustNewFromString("66")
	modelSellCap := fixedpoint.MustNewFromString("0.00022")
	hardBuyCap := fixedpoint.MustNewFromString("80")
	hardSellCap := fixedpoint.MustNewFromString("0.00030")

	if got := makerBuyInventoryCapacity(market, price, modelBuyCap, hardBuyCap); got.Compare(hardBuyCap) > 0 {
		t.Fatalf("BUY minimum floor crossed hard inventory band: got=%s hard=%s", got, hardBuyCap)
	}
	if _, ok := market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price,
		makerBuyInventoryCapacity(market, price, modelBuyCap, hardBuyCap)); ok {
		t.Fatal("BUY side must remain omitted when hard headroom cannot fit exchange minimum")
	}

	if got := makerSellInventoryCapacity(market, price, modelSellCap, hardSellCap); got.Compare(hardSellCap) > 0 {
		t.Fatalf("SELL minimum floor crossed hard inventory band: got=%s hard=%s", got, hardSellCap)
	}
	if _, ok := market.GreaterThanMinimalOrderQuantity(types.SideTypeSell, price,
		makerSellInventoryCapacity(market, price, modelSellCap, hardSellCap)); ok {
		t.Fatal("SELL side must remain omitted when hard headroom cannot fit exchange minimum")
	}
}

func TestTargetCenteredOrderCapsRemainInsideHardHeadroom(t *testing.T) {
	band := InventoryBand{MinInventory: 0.25, Target: 0.5, MaxInventory: 0.75}
	caps := targetCenteredInventoryOrderCaps(band, 0.49, 10_000, 10)
	hardBuy := inventoryBuyHeadroomNotional(band, 0.49, 10_000)
	hardSell := inventorySellHeadroomQuantity(band, 0.49)
	if caps.BuyNotional > hardBuy+1e-9 || caps.SellQuantity > hardSell+1e-12 {
		t.Fatalf("ticket caps must remain inside hard band headroom: caps=%+v hardBuy=%f hardSell=%f", caps, hardBuy, hardSell)
	}
}

func TestRestingOrdersCannotCrossDynamicInventoryBand(t *testing.T) {
	band := InventoryBand{MinInventory: 0.15, Target: 0.30, MaxInventory: 0.45}
	orders := types.OrderSlice{
		{SubmitOrder: types.SubmitOrder{Side: types.SideTypeBuy, Quantity: fixedpoint.NewFromFloat(0.20)}, ExecutedQuantity: fixedpoint.NewFromFloat(0.04)},
		{SubmitOrder: types.SubmitOrder{Side: types.SideTypeSell, Quantity: fixedpoint.NewFromFloat(0.13)}},
	}
	if makerOrdersExceedInventoryBand(orders, 0.287, band) {
		t.Fatalf("remaining quantities are inside both band edges")
	}
	orders[0].Quantity = fixedpoint.NewFromFloat(0.22)
	if !makerOrdersExceedInventoryBand(orders, 0.287, band) {
		t.Fatalf("remaining bid quantity must be rejected above the upper edge")
	}
	orders[0].Quantity = fixedpoint.NewFromFloat(0.20)
	orders[1].Quantity = fixedpoint.NewFromFloat(0.14)
	if !makerOrdersExceedInventoryBand(orders, 0.287, band) {
		t.Fatalf("remaining ask quantity must be rejected below the lower edge")
	}
}

func TestInventoryOutsideBandWithoutOrdersAllowsCorrectiveQuote(t *testing.T) {
	band := InventoryBand{MinInventory: 0.15, Target: 0.30, MaxInventory: 0.45}
	if makerOrdersExceedInventoryBand(nil, 0.50, band) {
		t.Fatalf("inventory above the dynamic maximum without resting orders must allow a corrective ask")
	}
	if makerOrdersExceedInventoryBand(nil, 0.10, band) {
		t.Fatalf("inventory below the dynamic minimum without resting orders must allow a corrective bid")
	}
}

func TestInventoryOutsideBandRejectsOnlyRiskIncreasingOrders(t *testing.T) {
	band := InventoryBand{MinInventory: 0.15, Target: 0.30, MaxInventory: 0.45}
	aboveMaxAsk := types.OrderSlice{{SubmitOrder: types.SubmitOrder{Side: types.SideTypeSell, Quantity: fixedpoint.NewFromFloat(0.10)}}}
	if makerOrdersExceedInventoryBand(aboveMaxAsk, 0.50, band) {
		t.Fatalf("corrective ask from above the dynamic maximum must remain eligible")
	}
	aboveMaxBid := types.OrderSlice{{SubmitOrder: types.SubmitOrder{Side: types.SideTypeBuy, Quantity: fixedpoint.NewFromFloat(0.01)}}}
	if !makerOrdersExceedInventoryBand(aboveMaxBid, 0.50, band) {
		t.Fatalf("bid that worsens inventory above the dynamic maximum must be rejected")
	}
	belowMinBid := types.OrderSlice{{SubmitOrder: types.SubmitOrder{Side: types.SideTypeBuy, Quantity: fixedpoint.NewFromFloat(0.10)}}}
	if makerOrdersExceedInventoryBand(belowMinBid, 0.10, band) {
		t.Fatalf("corrective bid from below the dynamic minimum must remain eligible")
	}
	belowMinAsk := types.OrderSlice{{SubmitOrder: types.SubmitOrder{Side: types.SideTypeSell, Quantity: fixedpoint.NewFromFloat(0.01)}}}
	if !makerOrdersExceedInventoryBand(belowMinAsk, 0.10, band) {
		t.Fatalf("ask that worsens inventory below the dynamic minimum must be rejected")
	}
}

func TestCalculateMakerQuoteBalancesRecoversOwnReservations(t *testing.T) {
	base := types.Balance{
		Available: fixedpoint.MustNewFromString("0.0002253"),
		Locked:    fixedpoint.MustNewFromString("0.129"),
	}
	quote := types.Balance{
		Available: fixedpoint.MustNewFromString("100"),
		Locked:    fixedpoint.MustNewFromString("480"),
	}
	orders := types.OrderSlice{
		{
			SubmitOrder: types.SubmitOrder{
				Symbol: "SOLJPY", Side: types.SideTypeSell,
				Price: fixedpoint.MustNewFromString("12686"), Quantity: fixedpoint.MustNewFromString("0.129"),
			},
			Status: types.OrderStatusNew,
		},
		{
			SubmitOrder: types.SubmitOrder{
				Symbol: "SOLJPY", Side: types.SideTypeBuy,
				Price: fixedpoint.MustNewFromString("12000"), Quantity: fixedpoint.MustNewFromString("0.04"),
			},
			Status: types.OrderStatusNew,
		},
	}

	got := calculateMakerQuoteBalances(base, quote, orders)
	assertFixedpointEqual(t, "available base", got.AvailableBase, "0.0002253")
	assertFixedpointEqual(t, "quoteable base", got.QuoteableBase, "0.1292253")
	assertFixedpointEqual(t, "available quote", got.AvailableQuote, "100")
	assertFixedpointEqual(t, "quoteable quote", got.QuoteableQuote, "580")
}

func TestCalculateMakerQuoteBalancesUsesRemainingQuantityAndLockedCap(t *testing.T) {
	base := types.Balance{
		Available: fixedpoint.MustNewFromString("0.01"),
		Locked:    fixedpoint.MustNewFromString("0.10"),
	}
	quote := types.Balance{
		Available: fixedpoint.MustNewFromString("200"),
		Locked:    fixedpoint.MustNewFromString("50"),
	}
	orders := types.OrderSlice{
		{
			SubmitOrder: types.SubmitOrder{
				Symbol: "SOLJPY", Side: types.SideTypeSell,
				Price: fixedpoint.MustNewFromString("12686"), Quantity: fixedpoint.MustNewFromString("0.20"),
			},
			Status: types.OrderStatusPartiallyFilled, ExecutedQuantity: fixedpoint.MustNewFromString("0.08"),
		},
		{
			SubmitOrder: types.SubmitOrder{
				Symbol: "SOLJPY", Side: types.SideTypeBuy,
				Price: fixedpoint.MustNewFromString("12000"), Quantity: fixedpoint.MustNewFromString("0.01"),
			},
			Status: types.OrderStatusNew,
		},
		{
			SubmitOrder: types.SubmitOrder{
				Symbol: "SOLJPY", Side: types.SideTypeSell,
				Price: fixedpoint.MustNewFromString("12600"), Quantity: fixedpoint.MustNewFromString("1"),
			},
			Status: types.OrderStatusCanceled,
		},
	}

	got := calculateMakerQuoteBalances(base, quote, orders)
	// The active ask has 0.12 SOL remaining, but the exchange reports only
	// 0.10 SOL locked, so the reclaimable reservation is capped at 0.10.
	assertFixedpointEqual(t, "capped quoteable base", got.QuoteableBase, "0.11")
	// The active bid reserves 120 JPY, but only 50 JPY is reported locked.
	assertFixedpointEqual(t, "capped quoteable quote", got.QuoteableQuote, "250")
}

func TestCalculateMakerQuoteBalancesDoesNotReclaimUnownedLocks(t *testing.T) {
	base := types.Balance{
		Available: fixedpoint.MustNewFromString("0.01"), Locked: fixedpoint.MustNewFromString("0.50"),
	}
	quote := types.Balance{
		Available: fixedpoint.MustNewFromString("200"), Locked: fixedpoint.MustNewFromString("500"),
	}

	got := calculateMakerQuoteBalances(base, quote, nil)
	assertFixedpointEqual(t, "unowned base lock", got.QuoteableBase, "0.01")
	assertFixedpointEqual(t, "unowned quote lock", got.QuoteableQuote, "200")
}

func assertFixedpointEqual(t *testing.T, name string, got fixedpoint.Value, want string) {
	t.Helper()
	expected := fixedpoint.MustNewFromString(want)
	if got.Compare(expected) != 0 {
		t.Fatalf("%s: got %s, want %s", name, got, expected)
	}
}
