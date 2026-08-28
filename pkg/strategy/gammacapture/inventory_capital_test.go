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
		InventoryRiskZScore: 1.645, InventoryTargetRatio: 0.5,
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

func TestProbabilisticInventoryVariationCentersExpectedPosition(t *testing.T) {
	c := MarketMakerConfig{
		InventoryRiskZScore:         1.645,
		InventoryCapitalMinRatio:    0,
		InventoryCapitalTargetRatio: 0.5,
		InventoryCapitalMaxRatio:    1,
	}
	const (
		equityJPY       = 6808.0
		targetRatio     = 0.151431186
		buyRatePerHour  = 0.4744
		sellRatePerHour = 0.3667
		executableJPY   = 101.7
	)
	d := c.ProbabilisticInventoryVariation(
		equityJPY, targetRatio, 30*time.Minute,
		buyRatePerHour, sellRatePerHour, executableJPY)
	wantEvents := (buyRatePerHour + sellRatePerHour) * 0.5
	wantStdDev := executableJPY * math.Sqrt(wantEvents)
	wantHalfWidth := math.Max(1.5*executableJPY, c.InventoryRiskZScore*wantStdDev)
	if !d.Enabled {
		t.Fatalf("expected stochastic inventory variation: %+v", d)
	}
	if math.Abs(d.ExpectedFillEvents-wantEvents) > 1e-12 || math.Abs(d.InventoryStdDevJPY-wantStdDev) > 1e-9 {
		t.Fatalf("unexpected compound-Poisson moments: got=%+v events=%f stddev=%f", d, wantEvents, wantStdDev)
	}
	if math.Abs(d.HalfWidthJPY-wantHalfWidth) > 1e-9 {
		t.Fatalf("unexpected confidence half-width: got=%f want=%f", d.HalfWidthJPY, wantHalfWidth)
	}
	if math.Abs((d.LowerRatio+d.UpperRatio)/2-targetRatio) > 1e-12 {
		t.Fatalf("variation band must preserve macro target as its expectation: %+v", d)
	}
	if (d.UpperRatio-targetRatio)*equityJPY+1e-9 < 1.5*executableJPY ||
		(targetRatio-d.LowerRatio)*equityJPY+1e-9 < 1.5*executableJPY {
		t.Fatalf("both sides must admit one fill plus half-lattice centering error: %+v", d)
	}
}

func TestInventoryVariationHorizonTracksAdaptiveFastWindow(t *testing.T) {
	c := MarketMakerConfig{MinTradingWindow: types.Duration(10 * time.Minute)}
	horizon, source := c.InventoryVariationHorizon(15*time.Minute, 30*time.Minute)
	if horizon != 15*time.Minute || source != "adaptive-fast" {
		t.Fatalf("inventory window must follow selected fast model: horizon=%s source=%s", horizon, source)
	}
	horizon, source = c.InventoryVariationHorizon(0, 30*time.Minute)
	if horizon != 30*time.Minute || source != "maker-horizon-fallback" {
		t.Fatalf("missing fast window should use maker fallback: horizon=%s source=%s", horizon, source)
	}
	horizon, source = c.InventoryVariationHorizon(0, 0)
	if horizon != 10*time.Minute || source != "configured-minimum-fallback" {
		t.Fatalf("missing both windows should use configured minimum: horizon=%s source=%s", horizon, source)
	}
}

func TestSelectInventoryControlKeepsMacroOutOfFastTradingZone(t *testing.T) {
	fast := InventoryBand{MinInventory: 1, Target: 2, MaxInventory: 3}
	macro := InventoryBand{MinInventory: 0, Target: 1.25, MaxInventory: 2.5}
	inside := SelectInventoryControl(fast, macro, true, 0)
	if !inside.FastTradingZone || inside.LongHorizonAdjustment ||
		inside.Band.Target != fast.Target {
		t.Fatalf("Macro replaced Fast inside its two-sided trading zone: %+v", inside)
	}

	bearishRegion := InventoryBand{MinInventory: 0.25, Target: 0.75, MaxInventory: 1.5}
	projected := SelectInventoryControl(fast, bearishRegion, true, -1)
	if projected.FastTradingZone || !projected.LongHorizonAdjustment ||
		projected.Band.Target != bearishRegion.Target {
		t.Fatalf("Fast target must strengthen to the active long-horizon boundary: %+v", projected)
	}
	if projected.Band.Target-projected.Band.MinInventory != fast.Target-fast.MinInventory ||
		projected.Band.MaxInventory-projected.Band.Target != fast.MaxInventory-fast.Target {
		t.Fatalf("long-horizon projection changed Fast risk-band widths: %+v", projected)
	}
}

func TestSelectInventoryControlNeverWeakensFastCorrection(t *testing.T) {
	fast := InventoryBand{MinInventory: 1, Target: 2, MaxInventory: 3}
	weakBuy := InventoryBand{MinInventory: 0.5, Target: 1.5, MaxInventory: 2.5}
	if got := SelectInventoryControl(fast, weakBuy, true, 1); got.LongHorizonAdjustment || got.Band.Target != fast.Target {
		t.Fatalf("bullish Macro weakened Fast BUY correction: %+v", got)
	}
	weakSell := InventoryBand{MinInventory: 1.5, Target: 2.5, MaxInventory: 3.5}
	if got := SelectInventoryControl(fast, weakSell, true, -1); got.LongHorizonAdjustment || got.Band.Target != fast.Target {
		t.Fatalf("bearish Macro weakened Fast SELL correction: %+v", got)
	}
	strongBuy := InventoryBand{MinInventory: 2.5, Target: 3, MaxInventory: 3.5}
	if got := SelectInventoryControl(fast, strongBuy, true, 1); !got.LongHorizonAdjustment || got.Band.Target != strongBuy.Target {
		t.Fatalf("bullish boundary did not strengthen Fast BUY correction: %+v", got)
	}
}

func TestSelectInventoryControlLeavesFastUntouchedWhenLongHorizonDisabled(t *testing.T) {
	fast := InventoryBand{MinInventory: 1, Target: 2, MaxInventory: 3}
	macro := InventoryBand{MinInventory: 3.5, Target: 4, MaxInventory: 4.5}
	if got := SelectInventoryControl(fast, macro, false, -1); got.LongHorizonAdjustment || got.Band.Target != fast.Target {
		t.Fatalf("disabled Macro must not replace Fast ownership: %+v", got)
	}
}

func TestInventoryActuationHorizonUsesShortestCausalClock(t *testing.T) {
	c := MarketMakerConfig{MinTradingWindow: types.Duration(10 * time.Minute)}
	horizon, source := c.InventoryActuationHorizon(15*time.Minute, 3*time.Hour)
	if horizon != 15*time.Minute || source != "shortest-fast-regime" {
		t.Fatalf("long Macro lease must not stretch one correction: horizon=%s source=%s", horizon, source)
	}
	horizon, source = c.InventoryActuationHorizon(30*time.Minute, 15*time.Minute)
	if horizon != 15*time.Minute || source != "shortest-regime-fast" {
		t.Fatalf("shorter regime forecast must remain the safety clock: horizon=%s source=%s", horizon, source)
	}
	horizon, source = c.InventoryActuationHorizon(0, 0)
	if horizon != 10*time.Minute || source != "configured-minimum-fallback" {
		t.Fatalf("missing clocks must use configured fallback: horizon=%s source=%s", horizon, source)
	}
}

func TestProbabilisticInventoryVariationUsesOneFillBootstrapAndPolicyBoundary(t *testing.T) {
	c := MarketMakerConfig{
		InventoryRiskZScore:         1.645,
		InventoryCapitalMinRatio:    0,
		InventoryCapitalTargetRatio: 0.5,
		InventoryCapitalMaxRatio:    1,
	}
	d := c.ProbabilisticInventoryVariation(10_000, 0.25, 30*time.Minute, 0, 0, 100)
	if !d.Enabled || math.Abs(d.HalfWidthJPY-150) > 1e-9 {
		t.Fatalf("missing fill-rate data should retain one jump plus half-lattice centering error: %+v", d)
	}
	atBoundary := c.ProbabilisticInventoryVariation(10_000, 0, 30*time.Minute, 2, 2, 100)
	if atBoundary.Enabled || atBoundary.LowerRatio != 0 || atBoundary.UpperRatio != 0 {
		t.Fatalf("absolute outer policy boundary must collapse the symmetric band: %+v", atBoundary)
	}
}

func TestDynamicInventoryBandWithRuntimeBoundsPreservesExpectedTarget(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, InventoryRiskBudgetJPY: 100,
		InventoryRiskZScore:      1.645,
		InventoryCapitalMinRatio: 0, InventoryCapitalTargetRatio: 0.5, InventoryCapitalMaxRatio: 1,
	}
	band := c.DynamicInventoryBandWithCapitalPolicyBounds(
		290_000, 0.1, 30*time.Minute, 6808,
		0.1355, 0.1514, 0.1673)
	if math.Abs(band.CapitalTargetNotionalJPY-6808*0.1514) > 1e-9 {
		t.Fatalf("runtime band shifted the expected target: %+v", band)
	}
	if band.CapitalMinNotionalJPY < 6808*0.1355-1e-9 || band.CapitalCapNotionalJPY > 6808*0.1673+1e-9 {
		t.Fatalf("runtime band escaped supplied stochastic bounds: %+v", band)
	}
	if math.Abs((band.MinInventory+band.MaxInventory)/2-band.Target) > 1e-12 {
		t.Fatalf("effective inventory region must remain centered: %+v", band)
	}
}

func TestMakerMinimumExecutableNotionalUsesBothSideLattices(t *testing.T) {
	market := types.Market{
		MinNotional: fixedpoint.MustNewFromString("100"),
		MinQuantity: fixedpoint.MustNewFromString("0.00001"),
		StepSize:    fixedpoint.MustNewFromString("0.00001"),
		TickSize:    fixedpoint.MustNewFromString("1"),
	}
	got := makerMinimumExecutableNotional(
		market,
		fixedpoint.MustNewFromString("290364"),
		fixedpoint.MustNewFromString("293223"))
	if got < 100 {
		t.Fatalf("minimum executable notional fell below exchange minimum: %.8f", got)
	}
	buyCapacity, buyOK := makerMinimumExecutableBuyCapacity(market, fixedpoint.MustNewFromString("290364"))
	if !buyOK || got+1e-9 < buyCapacity.Float64() {
		t.Fatalf("minimum notional does not cover executable BUY lattice: got=%f buy=%s", got, buyCapacity)
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

func TestTargetCenteredOrderCapsStageCorrectionAcrossLevels(t *testing.T) {
	const price = 10_000.0
	band := InventoryBand{
		MinInventory: 0, Target: 0.5, MaxInventory: 1,
		RiskBandHalfWidthNotionalJPY: 5_000,
	}

	atCash := targetCenteredInventoryOrderCaps(band, 0, price, 10)
	if math.Abs(atCash.TrancheNotional-500) > 1e-9 || math.Abs(atCash.BuyNotional-500) > 1e-9 {
		t.Fatalf("cash-edge correction should use one staged tranche: %+v", atCash)
	}
	if ending := atCash.BuyNotional / price; math.Abs(ending-0.05) > 1e-12 {
		t.Fatalf("one corrective buy should close one tenth of the target error: ending=%f", ending)
	}

	atBase := targetCenteredInventoryOrderCaps(band, 1, price, 10)
	if math.Abs(atBase.TrancheNotional-500) > 1e-9 || math.Abs(atBase.SellQuantity-0.05) > 1e-12 {
		t.Fatalf("base-edge correction should use one staged tranche: %+v", atBase)
	}
	if ending := 1 - atBase.SellQuantity; math.Abs(ending-0.95) > 1e-12 {
		t.Fatalf("one corrective sell should close one tenth of the target error: ending=%f", ending)
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

func TestExchangeFeasibleInventoryCapsPreserveAuthoritativeZero(t *testing.T) {
	market := types.Market{
		MinNotional: fixedpoint.MustNewFromString("100"),
		MinQuantity: fixedpoint.MustNewFromString("0.00001"),
		StepSize:    fixedpoint.MustNewFromString("0.00001"),
		TickSize:    fixedpoint.MustNewFromString("1"),
	}
	price := fixedpoint.MustNewFromString("298000")
	hardBuyCap := fixedpoint.MustNewFromString("1000")
	hardSellCap := fixedpoint.MustNewFromString("0.01")
	if got := makerBuyInventoryCapacity(market, price, fixedpoint.Zero, hardBuyCap); got.Sign() != 0 {
		t.Fatalf("statistically rejected BUY must remain zero, got %s", got)
	}
	if got := makerSellInventoryCapacity(market, price, fixedpoint.Zero, hardSellCap); got.Sign() != 0 {
		t.Fatalf("statistically rejected SELL must remain zero, got %s", got)
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

func TestBuyInventoryCapacityAcceptsValidHardHeadroomBelowPaddedMinimum(t *testing.T) {
	market := types.Market{
		MinNotional: fixedpoint.MustNewFromString("100"),
		MinQuantity: fixedpoint.MustNewFromString("0.00001"),
		StepSize:    fixedpoint.MustNewFromString("0.00001"),
		TickSize:    fixedpoint.MustNewFromString("1"),
	}
	price := fixedpoint.MustNewFromString("290238")
	modelCap := fixedpoint.MustNewFromString("3.21149218")
	hardCap := fixedpoint.MustNewFromString("102.34")
	paddedMinimum, minimumOK := makerMinimumExecutableBuyCapacity(market, price)
	if !minimumOK || hardCap.Compare(paddedMinimum) >= 0 {
		t.Fatalf("test requires valid headroom below padded minimum: hard=%s padded=%s", hardCap, paddedMinimum)
	}
	if _, hardOK := market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, hardCap); !hardOK {
		t.Fatalf("test hard headroom must be directly exchange-valid: %s", hardCap)
	}
	capacity := makerBuyInventoryCapacity(market, price, modelCap, hardCap)
	quantity, ok := market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, capacity)
	if !ok || quantity.String() != "0.00035" {
		t.Fatalf("valid hard headroom should floor to one executable BUY: capacity=%s quantity=%s ok=%v",
			capacity, quantity, ok)
	}
	if capacity.Compare(hardCap) > 0 {
		t.Fatalf("BUY capacity crossed the inventory band: capacity=%s hard=%s", capacity, hardCap)
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

func TestInventoryBandCancellationKeepsCorrectiveSide(t *testing.T) {
	band := InventoryBand{MinInventory: 0.15, Target: 0.30, MaxInventory: 0.45}
	orders := types.OrderSlice{
		{SubmitOrder: types.SubmitOrder{Side: types.SideTypeBuy, Quantity: fixedpoint.NewFromFloat(0.02)}},
		{SubmitOrder: types.SubmitOrder{Side: types.SideTypeSell, Quantity: fixedpoint.NewFromFloat(0.10)}},
	}
	buyViolation, sellViolation := makerOrderInventoryBandViolations(orders, 0.50, band)
	if !buyViolation || sellViolation {
		t.Fatalf("overweight inventory must flag only the inventory-increasing BUY: buy=%v sell=%v", buyViolation, sellViolation)
	}
	cancel := makerOrdersForInventoryBandViolations(orders, 0.50, band)
	if len(cancel) != 1 || cancel[0].Side != types.SideTypeBuy {
		t.Fatalf("overweight inventory must cancel only BUY: %+v", cancel)
	}

	buyViolation, sellViolation = makerOrderInventoryBandViolations(orders, 0.10, band)
	if buyViolation || !sellViolation {
		t.Fatalf("underweight inventory must flag only the inventory-increasing SELL: buy=%v sell=%v", buyViolation, sellViolation)
	}
	cancel = makerOrdersForInventoryBandViolations(orders, 0.10, band)
	if len(cancel) != 1 || cancel[0].Side != types.SideTypeSell {
		t.Fatalf("underweight inventory must cancel only SELL: %+v", cancel)
	}
}

func TestRestingOrdersIgnoreSubStepDynamicBandNoise(t *testing.T) {
	// This reproduces the live ETHJPY failure: the resting bid plus current
	// inventory exceeds the recalculated cap by 4.47e-8 base, well below the
	// venue's 1e-5 quantity step. Such numerical drift must not trigger a
	// cancellation-only transition.
	band := InventoryBand{MinInventory: 0, MaxInventory: 0.019805945308849715}
	orders := types.OrderSlice{{
		SubmitOrder: types.SubmitOrder{
			Side:     types.SideTypeBuy,
			Quantity: fixedpoint.MustNewFromString("0.0171"),
		},
	}}
	if makerOrdersExceedInventoryBand(orders, 0.00270599, band) {
		t.Fatalf("sub-step cap drift must not cancel a valid resting bid")
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
	assertFixedpointEqual(t, "total base", got.TotalBase, "0.1292253")
	assertFixedpointEqual(t, "total quote", got.TotalQuote, "580")
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

func TestDynamicInventoryBandAllowsTargetAtMacroCap(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, InventoryRiskBudgetJPY: 10, InventoryRiskBudgetRatio: 0.0025,
		InventoryRiskZScore:      1.645,
		InventoryCapitalMinRatio: 0, InventoryCapitalTargetRatio: 0.5, InventoryCapitalMaxRatio: 1,
	}
	band := c.DynamicInventoryBandWithCapitalPolicy(10_000, 0.5, 10*time.Minute, 10_000, 0.20, 0.20)
	if math.Abs(band.Target*10_000-2_000) > 1e-9 || math.Abs(band.MaxInventory-band.Target) > 1e-12 {
		t.Fatalf("macro target at cap must close new inventory headroom: %+v", band)
	}
	if band.MinInventory >= band.Target {
		t.Fatalf("corrective asks must retain a lower-side band: %+v", band)
	}
	if got := inventoryBuyHeadroomNotional(band, band.Target, 10_000); got != 0 {
		t.Fatalf("buy headroom must be zero at the carrying cap, got %.8f", got)
	}
	if got := inventorySellHeadroomQuantity(band, band.Target); got <= 0 {
		t.Fatalf("sell headroom must remain positive at the carrying cap, got %.8f", got)
	}
}

func TestMakerMinimumExecutableQuantityHandlesFloatStepBoundary(t *testing.T) {
	market := types.Market{
		Symbol:      "ETHJPY",
		MinNotional: fixedpoint.MustNewFromString("100"),
		MinQuantity: fixedpoint.MustNewFromString("0.00001"),
		StepSize:    fixedpoint.MustNewFromString("0.00001"),
		TickSize:    fixedpoint.MustNewFromString("1"),
	}

	price := fixedpoint.MustNewFromString("391970")
	quantity, ok := makerMinimumExecutableQuantity(market, price)
	if !ok || quantity.Sign() <= 0 {
		t.Fatalf("minimum quantity should survive the exchange validator: quantity=%s ok=%v", quantity, ok)
	}
	if quantity.Mul(price).Compare(market.MinNotional) < 0 {
		t.Fatalf("minimum quantity must satisfy min-notional: quantity=%s notional=%s", quantity, quantity.Mul(price))
	}
	if quantity.String() != "0.00027" {
		t.Fatalf("expected one-lot float-boundary safety margin, got %s", quantity)
	}
}
