package gammacapture

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestMarketMakerQuoteSurvivesNarrowBook(t *testing.T) {
	c := MarketMakerConfig{MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 4}
	p := c.Quote(MarketMakerQuoteInput{MidPrice: 100, BestBid: 99.99, BestAsk: 100.01, CanBuy: true, CanSell: true})
	if p.Reason != "quoted" {
		t.Fatalf("narrow BBO must not disable resting quotes: %+v", p)
	}
}

func TestMarketMakerClientOrderIDAndStaleOrderOwnership(t *testing.T) {
	clientOrderID := marketMakerClientOrderID(types.SideTypeBuy)
	if !strings.HasPrefix(clientOrderID, marketMakerClientOrderPrefix) {
		t.Fatalf("unexpected market-maker client order ID: %q", clientOrderID)
	}
	if !isOwnedMarketMakerOrder(types.Order{SubmitOrder: types.SubmitOrder{Type: types.OrderTypeLimitMaker, ClientOrderID: clientOrderID}}) {
		t.Fatalf("new market-maker order should be owned")
	}
	if !isOwnedMarketMakerOrder(types.Order{SubmitOrder: types.SubmitOrder{Type: types.OrderTypeLimitMaker, ClientOrderID: legacyBinanceBrokerClientOrderPrefix + "legacy"}}) {
		t.Fatalf("legacy BBGO maker order should be reconciled")
	}
	if !isOwnedMarketMakerOrder(types.Order{SubmitOrder: types.SubmitOrder{Type: types.OrderTypeLimit, ClientOrderID: clientOrderID}}) {
		t.Fatalf("REST-normalized market-maker order should be reconciled")
	}
	if !isOwnedMarketMakerOrder(types.Order{SubmitOrder: types.SubmitOrder{Type: types.OrderTypeLimit, ClientOrderID: legacyBinanceBrokerClientOrderPrefix + "legacy-rest"}}) {
		t.Fatalf("REST-normalized legacy maker order should be reconciled")
	}
	if isOwnedMarketMakerOrder(types.Order{SubmitOrder: types.SubmitOrder{Type: types.OrderTypeLimitMaker, ClientOrderID: "manual-order"}}) {
		t.Fatalf("manual order must not be reconciled")
	}
}

func TestMakerAdverseBBOChangeUsesReferenceBook(t *testing.T) {
	// A quote 40 bps below the current bid is not itself an adverse BBO move.
	// With an unchanged reference book, the reprice signal must remain zero.
	ask, bid := makerAdverseBBOChangeBps(12329, 12330, 12329, 12330)
	if math.Abs(ask) > 1e-9 || math.Abs(bid) > 1e-9 {
		t.Fatalf("unchanged BBO should produce zero adverse movement: ask=%f bid=%f", ask, bid)
	}

	// A bid BBO moving up by roughly 25 bps is adverse for a resting bid and
	// must be observable independently of the quote's own distance.
	ask, bid = makerAdverseBBOChangeBps(12329, 12330, 12360, 12330)
	if bid < 20 || math.Abs(ask) > 1e-9 {
		t.Fatalf("BBO movement should drive only the bid adverse signal: ask=%f bid=%f", ask, bid)
	}
}

func TestMarketMakerQuoteInventorySkewAndMakerOnly(t *testing.T) {
	c := MarketMakerConfig{MakerFeeBps: 1, AdverseSelectionBps: 1, MinimumNetEdgeBps: 0, MinimumHalfSpreadBps: 5, MaximumHalfSpreadBps: 20, InventoryLimit: 1, InventorySkewBps: 10}
	p := c.Quote(MarketMakerQuoteInput{MidPrice: 100, BestBid: 99, BestAsk: 101, Inventory: 1, CanBuy: true, CanSell: true})
	if p.Reason != "quoted" || p.AllowBid || !p.AllowAsk {
		t.Fatalf("long inventory should suppress bids: %+v", p)
	}
	if p.BidPrice >= 100 || p.AskPrice <= 100 {
		t.Fatalf("long inventory should make ask closer than mid: %+v", p)
	}
	if p.BidPrice >= 101 || p.AskPrice <= 99 {
		t.Fatalf("quote crossed observed book: %+v", p)
	}
}

func TestMarketMakerQuoteAdaptsToDirectionAndImbalance(t *testing.T) {
	c := MarketMakerConfig{MakerFeeBps: 1, AdverseSelectionBps: 1, MinimumHalfSpreadBps: 10, MaximumHalfSpreadBps: 50, InventoryLimit: 1, DirectionSkewBps: 10, ImbalanceSkewBps: 10}
	neutral := c.Quote(MarketMakerQuoteInput{MidPrice: 100, BestBid: 99, BestAsk: 101, CanBuy: true, CanSell: true})
	up := c.Quote(MarketMakerQuoteInput{MidPrice: 100, BestBid: 99, BestAsk: 101, DirectionSignal: 1, BookImbalance: 1, CanBuy: true, CanSell: true})
	if up.BidPrice < neutral.BidPrice || up.AskPrice <= neutral.AskPrice {
		t.Fatalf("upward pressure should not weaken the bid floor and should move ask upward: neutral=%+v up=%+v", neutral, up)
	}
	down := c.Quote(MarketMakerQuoteInput{MidPrice: 100, BestBid: 99, BestAsk: 101, DirectionSignal: -1, BookImbalance: -1, CanBuy: true, CanSell: true})
	if down.BidPrice >= neutral.BidPrice || down.AskPrice > neutral.AskPrice {
		t.Fatalf("downward pressure should move bid downward without weakening ask floor: neutral=%+v down=%+v", neutral, down)
	}
}

func TestMarketMakerQuoteSkewPreservesFeeFloor(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		InventoryLimit: 1, InventorySkewBps: 20, DirectionSkewBps: 10, ImbalanceSkewBps: 10,
	}
	p := c.Quote(MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99, BestAsk: 101, Inventory: 1,
		DirectionSignal: 1, BookImbalance: 1, CanBuy: true, CanSell: true,
	})
	if got := math.Log(100/p.BidPrice) * 10_000; got < 15-1e-9 {
		t.Fatalf("bid crossed fee floor after skew: distance=%.4f plan=%+v", got, p)
	}
	if got := math.Log(p.AskPrice/100) * 10_000; got < 15-1e-9 {
		t.Fatalf("ask crossed fee floor after skew: distance=%.4f plan=%+v", got, p)
	}
}

func TestMarketMakerRefreshIntervalsUseFirstPassageScale(t *testing.T) {
	c := MarketMakerConfig{MinRefreshInterval: types.Duration(20 * time.Second), RefreshInterval: types.Duration(time.Minute), MaxRefreshInterval: types.Duration(15 * time.Minute)}
	minRefresh, maxRefresh := c.RefreshIntervals(15, 0.58)
	if minRefresh <= 20*time.Second || maxRefresh <= time.Minute {
		t.Fatalf("expected low volatility to lengthen quote lifetime: min=%s max=%s", minRefresh, maxRefresh)
	}
	if maxRefresh > 15*time.Minute || maxRefresh < minRefresh {
		t.Fatalf("refresh interval bounds invalid: min=%s max=%s", minRefresh, maxRefresh)
	}
	nearMin, nearMax := c.RefreshIntervals(15, 0.58)
	farMin, farMax := c.RefreshIntervals(30, 0.58)
	if farMin <= nearMin || farMax <= nearMax {
		t.Fatalf("larger quote distance must receive a longer statistical horizon: near=(%s,%s) far=(%s,%s)", nearMin, nearMax, farMin, farMax)
	}
	minRefresh, maxRefresh = c.RefreshIntervals(80, 0.01)
	if minRefresh > 15*time.Minute || maxRefresh > 15*time.Minute || maxRefresh < minRefresh {
		t.Fatalf("refresh cap must apply to both adaptive bounds: min=%s max=%s", minRefresh, maxRefresh)
	}
}

func TestBoundRefreshIntervalsRespectTradingWindow(t *testing.T) {
	minRefresh, maxRefresh := BoundRefreshIntervals(15*time.Minute, 15*time.Minute, 10*time.Minute)
	if minRefresh != 10*time.Minute || maxRefresh != 10*time.Minute {
		t.Fatalf("quote lifetime must not exceed selected window: min=%s max=%s", minRefresh, maxRefresh)
	}

	minRefresh, maxRefresh = BoundRefreshIntervals(20*time.Second, time.Minute, 10*time.Minute)
	if minRefresh != 20*time.Second || maxRefresh != time.Minute {
		t.Fatalf("window cap should not shorten normal refresh bounds: min=%s max=%s", minRefresh, maxRefresh)
	}
}

func TestTradingHorizonsIncludeFiveToThirtyMinutes(t *testing.T) {
	c := MarketMakerConfig{
		MinTradingWindow: types.Duration(5 * time.Minute),
		MaxTradingWindow: types.Duration(30 * time.Minute),
	}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 20 * time.Minute, 30 * time.Minute}
	got := c.TradingHorizons()
	if len(got) != len(want) {
		t.Fatalf("unexpected trading horizons: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected trading horizons: got=%v want=%v", got, want)
		}
	}
}

func TestMakerQuoteRefreshSignalsRespectMinimumRestingInterval(t *testing.T) {
	if makerQuoteRefreshRequired(19*time.Second, 20*time.Second, false, false, false, true, true, true) {
		t.Fatal("material signals must not bypass the minimum resting interval")
	}
	for name, signal := range map[string][6]bool{
		"quote crossed":      {true, false, false, false, false, false},
		"window expired":     {false, true, false, false, false, false},
		"adverse move":       {false, false, true, false, false, false},
		"material move":      {false, false, false, true, false, false},
		"material imbalance": {false, false, false, false, true, false},
		"missing side":       {false, false, false, false, false, true},
	} {
		if !makerQuoteRefreshRequired(20*time.Second, 20*time.Second, signal[0], signal[1], signal[2], signal[3], signal[4], signal[5]) {
			t.Fatalf("%s should trigger a refresh after the resting interval", name)
		}
	}
	if makerQuoteRefreshRequired(time.Minute, 20*time.Second, false, false, false, false, false, false) {
		t.Fatal("no refresh signal should retain the existing quote")
	}
}

func TestMakerQuoteNearFillProtectsQueueAtWindowExpiry(t *testing.T) {
	plan := MarketMakerQuotePlan{AllowBid: true, AllowAsk: true, BidDistanceBps: 30, AskDistanceBps: 30}
	if !makerQuoteNearFill(99.8, 100.2, 99, 101, 100, plan, 10) {
		t.Fatal("quotes closer than the replacement target should retain queue priority")
	}
	if makerQuoteNearFill(99.5, 100.5, 99, 101, 100, plan, 10) {
		t.Fatal("quotes farther than the replacement target should be repriced")
	}
	if makerQuoteNearFill(99.99, 100.01, 99, 101, 100, plan, 10) {
		t.Fatal("quotes inside the fee/adverse-selection floor must not be retained")
	}
	plan.AllowAsk = false
	if !makerQuoteNearFill(99.8, 100.2, 99, 101, 100, plan, 10) {
		t.Fatal("a valid one-sided quote should still receive near-fill protection")
	}
}

func TestInventoryRiskVolatilityIgnoresSparseFastSpike(t *testing.T) {
	if got := inventoryRiskVolatility(0.00003, 0.001, HealthDegraded); got != 0.00003 {
		t.Fatalf("degraded fast model must not tighten inventory cap: got=%g", got)
	}
	if got := inventoryRiskVolatility(0.00003, 0.001, HealthHealthy); got != 0.001 {
		t.Fatalf("healthy fast model should participate in inventory risk: got=%g", got)
	}
}

func TestQuoteRiskVolatilityRequiresHealthyFastEvidence(t *testing.T) {
	const slow = 0.00003
	const fast = 0.001
	if got := quoteRiskVolatility(slow, fast, HealthDegraded, HealthDegraded); got != slow {
		t.Fatalf("degraded fast model must not widen quote: got=%g", got)
	}
	if got := quoteRiskVolatility(slow, fast, HealthHealthy, HealthDegraded); got != slow {
		t.Fatalf("degraded fast evidence must not widen quote: got=%g", got)
	}
	if got := quoteRiskVolatility(slow, fast, HealthHealthy, HealthHealthy); got != fast {
		t.Fatalf("healthy fast model and evidence should widen quote when indicated: got=%g", got)
	}
}

func TestMakerQuoteWindowOpen(t *testing.T) {
	now := time.Unix(100, 0)
	if !makerQuoteWindowOpen(now, now.Add(-time.Minute), now.Add(time.Minute)) {
		t.Fatalf("active quote should remain eligible for a data-gap grace period")
	}
	if makerQuoteWindowOpen(now.Add(2*time.Minute), now, now.Add(time.Minute)) {
		t.Fatalf("expired quote window must not be retained")
	}
	if makerQuoteWindowOpen(now, now, time.Time{}) {
		t.Fatalf("quote without a selected window must not be retained")
	}
}

func TestMarketMakerQuoteUsesDistanceDependentHorizon(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		MinRefreshInterval:   types.Duration(20 * time.Second),
		RefreshInterval:      types.Duration(time.Minute),
		MaxRefreshInterval:   types.Duration(15 * time.Minute),
		VolatilityMultiplier: 0.75,
	}
	p := c.Quote(MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99.99, BestAsk: 100.01,
		VolatilityPerSqrtSec: 0.58, CanBuy: true, CanSell: true,
	})
	if p.Reason != "quoted" {
		t.Fatalf("expected quote, got %+v", p)
	}
	if p.HalfSpreadBps <= 15 {
		t.Fatalf("expected volatility buffer over the static floor: %+v", p)
	}
	if p.HalfSpreadBps > c.MaximumHalfSpreadBps {
		t.Fatalf("distance-dependent spread exceeded cap: %+v", p)
	}
}

func TestMarketMakerHorizonModelUsesCrossingRateAndSpacing(t *testing.T) {
	start := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	for i := 0; i < 8*60; i++ {
		// A repeating excursion creates completed crossings at every candidate
		// horizon, allowing the score to compare frequency rather than only size.
		mid := 100 + 0.5*math.Sin(float64(i)*math.Pi/4)
		model.points = append(model.points, MarketMakerHorizonPoint{At: start.Add(time.Duration(i) * time.Minute), Mid: mid})
	}
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		MinTradingWindow: types.Duration(5 * time.Minute), MaxTradingWindow: types.Duration(15 * time.Minute),
		HorizonLookback: types.Duration(6 * time.Hour), HorizonMinSamples: 5,
	}
	decision := model.Update(start.Add(8*time.Hour), c, 0)
	if decision.Horizon < 5*time.Minute || decision.Horizon > 15*time.Minute {
		t.Fatalf("selected horizon outside configured trading window: %+v", decision)
	}
	if decision.ScoreBpsPerHour <= 0 || decision.UpCrosses == 0 || decision.DownCrosses == 0 {
		t.Fatalf("expected fee-adjusted two-sided score from crossings: %+v", decision)
	}
	if decision.MeanUpSpacing <= 0 || decision.MeanDownSpacing <= 0 {
		t.Fatalf("expected crossing spacing estimates: %+v", decision)
	}
}

func TestDynamicInventoryBandUsesRiskAndOrderLevels(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, InventoryRiskBudgetJPY: 10, InventoryRiskZScore: 1.645,
		InventoryMaxOrderLevels: 32, InventoryTargetRatio: 0.5,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
	}
	band := c.DynamicInventoryBand(12_200, 0.48, 10*time.Minute)
	if band.MaxInventory <= 0 || band.Target <= 0 || band.Limit <= 0 {
		t.Fatalf("expected positive dynamic inventory band: %+v", band)
	}
	if band.Target+band.Limit != band.MaxInventory {
		t.Fatalf("target and limit must form the upper cap: %+v", band)
	}
	if band.MaxInventory < 0.24 {
		t.Fatalf("low-volatility account should support current-sized inventory: %+v", band)
	}
	p := c.Quote(MarketMakerQuoteInput{
		MidPrice: 12_200, BestBid: 12_199, BestAsk: 12_201,
		Inventory: 0.24, CanBuy: true, CanSell: true,
	})
	if !p.AllowBid {
		t.Fatalf("static config should still quote when within its own band: %+v", p)
	}
}

func TestDynamicQuoteNotionalUsesRiskBudget(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, MinimumQuoteNotional: 100, MaximumQuoteNotional: 480,
		InventoryRiskBudgetJPY: 10, InventoryRiskZScore: 1.645, InventoryMaxOrderLevels: 32,
	}
	quiet := c.DynamicQuoteNotional(0.10, 10*time.Minute)
	normal := c.DynamicQuoteNotional(0.48, 10*time.Minute)
	volatile := c.DynamicQuoteNotional(1.50, 10*time.Minute)
	if quiet <= 480 {
		t.Fatalf("quiet regime should not be clipped by the legacy ceiling, got %.2f", quiet)
	}
	if normal <= 120 || normal >= quiet {
		t.Fatalf("normal regime should produce a risk-sized ticket between reference and ceiling, got %.2f", normal)
	}
	if volatile <= 0 || volatile >= normal {
		t.Fatalf("volatile regime should remain a positive, smaller risk-sized ticket, got %.2f", volatile)
	}
}

func TestDynamicQuoteNotionalIgnoresLegacyBounds(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, MinimumQuoteNotional: 10_000, MaximumQuoteNotional: 1,
		InventoryRiskBudgetJPY: 10, InventoryRiskZScore: 1.645, InventoryMaxOrderLevels: 32,
	}
	got := c.DynamicQuoteNotional(0.48, 10*time.Minute)
	if got <= 0 || got >= 10_000 {
		t.Fatalf("legacy min/max fields must not override statistical sizing, got %.2f", got)
	}
}

func TestEmpiricalVolatilityFloor(t *testing.T) {
	model := MarketMakerHorizonModel{}
	start := time.Unix(0, 0)
	price := 12_000.0
	for i := 0; i < 40; i++ {
		// Alternate small non-zero one-second returns so the robust estimator has
		// enough SOLJPY-specific observations without relying on a fixed config.
		if i%2 == 0 {
			price *= 1.0001
		} else {
			price *= 0.9999
		}
		model.Observe(start.Add(time.Duration(i+1)*time.Second), price, MarketMakerConfig{HorizonLookback: types.Duration(time.Minute)})
	}
	floor := model.EmpiricalVolatilityFloor(start.Add(time.Minute), time.Minute)
	if floor <= 0 {
		t.Fatalf("expected positive empirical volatility floor")
	}
}

func TestDynamicQuoteNotionalUsesTwoSidedFillLoad(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, MinimumQuoteNotional: 100, MaximumQuoteNotional: 480,
		InventoryRiskBudgetJPY: 10, InventoryRiskZScore: 1.645, InventoryMaxOrderLevels: 32,
	}
	coldStart := c.DynamicQuoteNotionalWithFillRates(0.48, 10*time.Minute, 0, 0)
	balanced := c.DynamicQuoteNotionalWithFillRates(0.48, 10*time.Minute, 12, 12)
	oneSided := c.DynamicQuoteNotionalWithFillRates(0.48, 10*time.Minute, 12, 0)
	if coldStart <= balanced {
		t.Fatalf("cold-start prior should allocate one expected ticket, while observed balanced flow allocates more tickets: coldStart=%.2f balanced=%.2f", coldStart, balanced)
	}
	if oneSided >= balanced || oneSided <= 0 {
		t.Fatalf("one-sided intensity must retain the worst-case ticket count: coldStart=%.2f balanced=%.2f oneSided=%.2f", coldStart, balanced, oneSided)
	}
	if got := c.EffectiveOrderLevels(10*time.Minute, 0, 0); math.Abs(got-1) > 1e-9 {
		t.Fatalf("cold-start prior should use one expected ticket, got %.4f", got)
	}
	if got := c.EffectiveOrderLevels(10*time.Minute, 12, 12); math.Abs(got-2) > 1e-9 {
		t.Fatalf("expected two effective tickets over the horizon, got %.4f", got)
	}
}

func TestSideQuoteAllocationDeRisksExposedSide(t *testing.T) {
	c := MarketMakerConfig{
		InventoryTarget: 0, InventoryLimit: 1,
		SideAllocationSensitivity: 1, SideAllocationFillRateWeight: 0.25,
	}
	base := 1_000.0
	neutralBias := c.SideAllocationBias(SideQuoteAllocationInput{InventoryLimit: 1})
	neutral := c.SideQuoteNotionals(base, neutralBias)
	if math.Abs(neutral.Buy-base) > 1e-9 || math.Abs(neutral.Sell-base) > 1e-9 {
		t.Fatalf("neutral allocation must preserve the common baseline: bias=%.4f notionals=%+v", neutralBias, neutral)
	}

	longBias := c.SideAllocationBias(SideQuoteAllocationInput{Inventory: 1, InventoryLimit: 1})
	long := c.SideQuoteNotionals(base, longBias)
	if longBias <= 0 || long.Buy >= long.Sell || long.Sell > base || long.Buy <= 0 {
		t.Fatalf("long inventory must reduce bids without increasing ask risk: bias=%.4f notionals=%+v", longBias, long)
	}

	downBias := c.SideAllocationBias(SideQuoteAllocationInput{DirectionSignal: -1, BookImbalance: -1, InventoryLimit: 1})
	if downBias <= 0 {
		t.Fatalf("downward pressure must reduce bid allocation: bias=%.4f", downBias)
	}
	upBias := c.SideAllocationBias(SideQuoteAllocationInput{DirectionSignal: 1, BookImbalance: 1, InventoryLimit: 1})
	if upBias >= 0 {
		t.Fatalf("upward pressure must reduce ask allocation: bias=%.4f", upBias)
	}

	fastBuyBias := c.SideAllocationBias(SideQuoteAllocationInput{InventoryLimit: 1, BuyFillRate: 20, SellFillRate: 0})
	if fastBuyBias <= 0 {
		t.Fatalf("higher bid crossing intensity must reduce bid allocation: bias=%.4f", fastBuyBias)
	}
}

func TestSideQuoteAllocationNeverExceedsBaseline(t *testing.T) {
	c := MarketMakerConfig{}
	for _, bias := range []float64{-1, -0.5, 0, 0.5, 1} {
		n := c.SideQuoteNotionals(120, bias)
		if n.Buy > 120+1e-9 || n.Sell > 120+1e-9 || n.Buy <= 0 || n.Sell <= 0 {
			t.Fatalf("side allocation must stay positive and within shared baseline: bias=%.2f notionals=%+v", bias, n)
		}
	}
}

func TestSideQuoteDistanceUsesRateAsymmetryAndFeeFloor(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		SideDistanceSensitivity: 1,
	}
	if got := c.SideQuoteDistanceBias(0, 10); got >= 0 {
		t.Fatalf("under-filling bids should receive a negative distance bias: %.4f", got)
	}
	if got := c.SideQuoteDistanceBias(10, 0); got <= 0 {
		t.Fatalf("fast bid fills should receive a positive distance bias: %.4f", got)
	}

	neutral := c.Quote(MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99.99, BestAsk: 100.01,
		VolatilityPerSqrtSec: 0.58, TradingHorizonSeconds: 600,
		CanBuy: true, CanSell: true,
	})
	closerBid := c.Quote(MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99.99, BestAsk: 100.01,
		VolatilityPerSqrtSec: 0.58, TradingHorizonSeconds: 600,
		SideDistanceBias: -1, CanBuy: true, CanSell: true,
	})
	if closerBid.BidDistanceBps >= neutral.BidDistanceBps || closerBid.AskDistanceBps <= neutral.AskDistanceBps {
		t.Fatalf("negative distance bias should bring bid closer and ask farther: neutral=%+v adjusted=%+v", neutral, closerBid)
	}
	if closerBid.BidDistanceBps < c.MinimumHalfSpreadBps-1e-9 || closerBid.AskDistanceBps < c.MinimumHalfSpreadBps-1e-9 {
		t.Fatalf("side distance must preserve fee floor: %+v", closerBid)
	}
}

func TestMakerAskQuantityConsumesDustResidual(t *testing.T) {
	s := Strategy{Market: types.Market{
		MinNotional: fixedpoint.MustNewFromString("100"), MinQuantity: fixedpoint.MustNewFromString("0.001"),
		StepSize: fixedpoint.MustNewFromString("0.001"), TickSize: fixedpoint.MustNewFromString("0.01"),
	}}
	price := fixedpoint.MustNewFromString("12200")
	quote := fixedpoint.MustNewFromString("120")
	qty, ok := s.makerAskQuantity(price, fixedpoint.MustNewFromString("0.013"), quote)
	if !ok || qty.Float64() != 0.013 {
		t.Fatalf("expected full balance when capped order strands dust, got qty=%s ok=%v", qty, ok)
	}
	qty, ok = s.makerAskQuantity(price, fixedpoint.MustNewFromString("0.022"), quote)
	if !ok || qty.Float64() != 0.009 {
		t.Fatalf("expected notional cap when residual is tradeable, got qty=%s ok=%v", qty, ok)
	}
}
