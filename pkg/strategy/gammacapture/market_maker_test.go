package gammacapture

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestMarketMakerConfigUsesSessionFees(t *testing.T) {
	cfg := MarketMakerConfig{MakerFeeBps: 10, TakerFeeBps: 10}
	session := &bbgo.ExchangeSession{
		ExchangeSessionConfig: bbgo.ExchangeSessionConfig{
			MakerFeeRate: fixedpoint.NewFromFloat(0.00075),
			TakerFeeRate: fixedpoint.NewFromFloat(0.001),
		},
		Account: &types.Account{HasFeeRate: true},
	}
	got, source := marketMakerConfigWithSessionFees(cfg, session)
	if source != "session+yaml-floor" || math.Abs(got.MakerFeeBps-10) > 1e-12 || math.Abs(got.TakerFeeBps-10) > 1e-12 {
		t.Fatalf("unexpected effective fees: source=%s cfg=%+v", source, got)
	}
	session.MakerFeeRate = fixedpoint.NewFromFloat(0.0012)
	got, source = marketMakerConfigWithSessionFees(cfg, session)
	if source != "session" || math.Abs(got.MakerFeeBps-12) > 1e-3 || math.Abs(got.TakerFeeBps-10) > 1e-12 {
		t.Fatalf("higher authenticated fee must remain authoritative: source=%s cfg=%+v", source, got)
	}

	got, source = marketMakerConfigWithSessionFees(cfg, nil)
	if source != "yaml-fallback" || got.MakerFeeBps != 10 || got.TakerFeeBps != 10 {
		t.Fatalf("missing session must retain YAML fallback: source=%s cfg=%+v", source, got)
	}
}

func TestMarketMakerQuoteSurvivesNarrowBook(t *testing.T) {
	c := MarketMakerConfig{MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 4}
	p := c.Quote(MarketMakerQuoteInput{MidPrice: 100, BestBid: 99.99, BestAsk: 100.01, CanBuy: true, CanSell: true})
	if p.Reason != "quoted" {
		t.Fatalf("narrow BBO must not disable resting quotes: %+v", p)
	}
}

func TestMarketMakerAskPreservesMarkToMarketEquityBeforeFill(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 1, MaximumHalfSpreadBps: 80,
		InventoryLimit: 1,
	}
	plan := c.Quote(MarketMakerQuoteInput{
		MidPrice: 99, BestBid: 98.99, BestAsk: 99.01,
		Inventory: 0.5, CanBuy: true, CanSell: true,
	})
	if !plan.EquityProtected || plan.AskPrice < plan.AskEquityFloor {
		t.Fatalf("expected exact mark-to-market equity protection: %+v", plan)
	}
	minimumOneSideEdge := c.MinimumNetEdgeBps / 2
	if plan.AskNetMarkEdgeBps < minimumOneSideEdge-1e-9 {
		t.Fatalf("protected ask reduces risk-adjusted marked equity: net=%.4f plan=%+v", plan.AskNetMarkEdgeBps, plan)
	}
	market := types.Market{TickSize: fixedpoint.MustNewFromString("0.01")}
	price := makerProtectedAskPrice(market, plan.AskPrice, plan.AskEquityFloor)
	if price.Float64() < plan.AskEquityFloor {
		t.Fatalf("tick formatting crossed equity floor: price=%s floor=%.8f", price, plan.AskEquityFloor)
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

func TestMarketMakerQuoteSoftBandDoesNotSuppressFastSide(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 1, AdverseSelectionBps: 1,
		MinimumHalfSpreadBps: 5, MaximumHalfSpreadBps: 20,
		InventoryTarget: 0.5, InventoryLimit: 0.1,
	}
	p := c.Quote(MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99, BestAsk: 101,
		Inventory: 0.7, InventoryMin: 0.4, InventoryMax: 0.6,
		HardInventoryMin: 0, HardInventoryMax: 1,
		CanBuy: true, CanSell: true,
	})
	if p.Reason != "quoted" || !p.AllowBid || !p.AllowAsk {
		t.Fatalf("soft Macro variation must size, not hard-gate, Fast sides: %+v", p)
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

func TestQuotePressureDoesNotDuplicateSignalsIntoQuantity(t *testing.T) {
	c := MarketMakerConfig{MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2, MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80, InventoryTarget: 0.5, InventoryLimit: 0.5}
	plan := c.Quote(MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99.99, BestAsk: 100.01, Inventory: 0.8, InventoryMin: 0.25, InventoryMax: 0.75,
		DirectionSignal: -0.25, BookImbalance: -0.20, BuyFillRate: 12, SellFillRate: 1,
		VolatilityPerSqrtSec: 0.2, TradingHorizonSeconds: 600, QuoteNotionalBase: 1000, CanBuy: true, CanSell: true,
	})
	if plan.SidePressure <= 0 || plan.BidDistanceBps <= plan.AskDistanceBps {
		t.Fatalf("long inventory and bid-heavy hazard must make the bid the riskier side: %+v", plan)
	}
	if plan.BidQuoteFactor != 1 || plan.AskQuoteFactor != 1 ||
		math.Abs(plan.BidQuoteNotional-1000) > 1e-9 || math.Abs(plan.AskQuoteNotional-1000) > 1e-9 {
		t.Fatalf("price pressure must not duplicate the same evidence through quantity: %+v", plan)
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

func TestOrderKeepDistanceUsesActualExecutableDistance(t *testing.T) {
	c := MarketMakerConfig{MakerFeeBps: 7.5, MinimumNetEdgeBps: 2, MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80}
	if got := c.OrderKeepDistanceBps(25); got != 25 {
		t.Fatalf("lifecycle must use the actual side distance, not the configured maximum: got %.2f", got)
	}
	if got := c.OrderKeepDistanceBps(95); got != 95 {
		t.Fatalf("selected distance beyond configured side maximum must be retained: got %.2f", got)
	}
	if got := c.OrderKeepDistanceBps(0); got != 15 {
		t.Fatalf("invalid distance must use the economic minimum fallback: got %.2f", got)
	}
}

func TestDynamicOrderKeepDecisionRoundsUpToMeasuredHorizon(t *testing.T) {
	c := MarketMakerConfig{
		MinTradingWindow: types.Duration(10 * time.Minute),
		MaxTradingWindow: types.Duration(30 * time.Minute),
	}
	decision := c.DynamicOrderKeepDecision(10*time.Minute, 15, 0.58)
	if decision.Duration != 15*time.Minute {
		t.Fatalf("11-minute first-passage scale should round up to 15m: %+v", decision)
	}
	if decision.CharacteristicFirstPassageTime <= 10*time.Minute || decision.CharacteristicFirstPassageTime >= 15*time.Minute {
		t.Fatalf("unexpected characteristic passage time: %+v", decision)
	}

	decision = c.DynamicOrderKeepDecision(10*time.Minute, 30, 0.58)
	if decision.Duration != 30*time.Minute {
		t.Fatalf("long passage scale should use the configured 30m cap: %+v", decision)
	}
	decision = c.DynamicOrderKeepDecision(30*time.Minute, 15, 5)
	if decision.Duration != 30*time.Minute {
		t.Fatalf("order keep time must never be shorter than the selected statistical horizon: %+v", decision)
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

func TestMakerQuoteRefreshSignalsRespectDynamicKeepDuration(t *testing.T) {
	keep := 10 * time.Minute
	if makerQuoteRefreshRequired(19*time.Second, 20*time.Second, keep, false, false, false, true, true, true, false, true) {
		t.Fatal("signals must not bypass the minimum transport resting interval")
	}
	if !makerQuoteRefreshRequired(20*time.Second, 20*time.Second, keep, true, false, false, false, false, false, false, false) {
		t.Fatal("a crossed quote must remain a hard lifecycle transition")
	}
	if !makerQuoteRefreshRequired(20*time.Second, 20*time.Second, keep, false, false, false, false, false, true, false, false) {
		t.Fatal("a missing or policy-mismatched side must remain actionable")
	}
	for name, signal := range map[string][4]bool{
		"window expiry":      {true, false, false, false},
		"adverse move":       {false, true, false, false},
		"material move":      {false, false, true, false},
		"material imbalance": {false, false, false, true},
	} {
		if makerQuoteRefreshRequired(time.Minute, 20*time.Second, keep, false, signal[0], signal[1], signal[2], signal[3], false, false, false) {
			t.Fatalf("%s must not destroy queue age before the modeled keep duration", name)
		}
		if !makerQuoteRefreshRequired(keep, 20*time.Second, keep, false, signal[0], signal[1], signal[2], signal[3], false, false, false) {
			t.Fatalf("%s should refresh once the modeled keep duration resolves", name)
		}
	}
	if !makerQuoteRefreshRequired(time.Minute, 20*time.Second, keep, false, false, false, false, false, false, true, false) {
		t.Fatal("a healthy fast-edge evidence lease should permit one bounded reprice before the generic keep duration")
	}
	if !makerQuoteRefreshRequired(time.Minute, 20*time.Second, keep, false, false, false, false, false, false, false, true) {
		t.Fatal("a statistically significant edge improvement should re-align inside the generic keep duration")
	}
	if makerQuoteRefreshRequired(keep, 20*time.Second, keep, false, false, false, false, false, false, false, false) {
		t.Fatal("no refresh signal should retain the existing quote")
	}
}

func TestMakerQuoteStatisticalRealignmentRequiresSignificantImprovement(t *testing.T) {
	active := MarketMakerHorizonDecision{
		EstimatorSource: "online-bbo", ScoreBpsPerHour: 5, ScoreStdErrorBpsHour: 1,
	}
	candidate := MarketMakerHorizonDecision{
		EstimatorSource: "online-bbo", ScoreBpsPerHour: 10, ScoreStdErrorBpsHour: 1,
	}
	refresh, improvement, threshold := makerQuoteStatisticalRealignment(candidate, active, 1.645)
	if !refresh || math.Abs(improvement-5) > 1e-12 || threshold <= 2 || threshold >= 3 {
		t.Fatalf("expected significant improvement: refresh=%v improvement=%f threshold=%f", refresh, improvement, threshold)
	}
	candidate.ScoreBpsPerHour = 7
	if refresh, _, _ := makerQuoteStatisticalRealignment(candidate, active, 1.645); refresh {
		t.Fatal("uncertain two-score improvement must preserve queue age")
	}
	active.ScoreStdErrorBpsHour = 0
	candidate.ScoreBpsPerHour = 10
	if refresh, _, threshold := makerQuoteStatisticalRealignment(candidate, active, 1.645); !refresh || threshold <= 0 {
		t.Fatal("a zero-edge active quote should use candidate uncertainty rather than disable re-alignment")
	}
}

func TestMakerBidEligibilityUsesExchangeMinimumAndPlannedCapacity(t *testing.T) {
	market := types.Market{
		MinNotional: fixedpoint.NewFromInt(100),
		MinQuantity: fixedpoint.MustNewFromString("0.00001"),
		StepSize:    fixedpoint.MustNewFromString("0.00001"),
		TickSize:    fixedpoint.NewFromInt(1),
	}
	price := fixedpoint.NewFromInt(300_000)
	if makerBidEligible(market, price, fixedpoint.NewFromFloat(8.77), fixedpoint.NewFromInt(500)) {
		t.Fatal("sub-minimum JPY must not create an impossible expected bid side")
	}
	if makerBidEligible(market, price, fixedpoint.NewFromInt(500), fixedpoint.NewFromInt(50)) {
		t.Fatal("joint quantity shrinkage below minimum must suppress the expected bid side")
	}
	if !makerBidEligible(market, price, fixedpoint.NewFromInt(500), fixedpoint.NewFromInt(120)) {
		t.Fatal("an exchange-valid planned bid should remain eligible")
	}
}

func TestMakerHeadroomCancelCooldown(t *testing.T) {
	now := time.Unix(100, 0)
	if !makerHeadroomCancelDue(now, time.Time{}, 20*time.Second) {
		t.Fatal("first headroom cancellation must be allowed")
	}
	if makerHeadroomCancelDue(now.Add(19*time.Second), now, 20*time.Second) {
		t.Fatal("headroom cancellation must be rate-limited while cancel updates drain")
	}
	if !makerHeadroomCancelDue(now.Add(20*time.Second), now, 20*time.Second) {
		t.Fatal("headroom cancellation should be allowed after the cooldown")
	}
}

func TestOwnedMakerTradeAcceptsPartialExecution(t *testing.T) {
	order := types.Order{
		SubmitOrder: types.SubmitOrder{
			Symbol: "ETHJPY", Type: types.OrderTypeLimitMaker,
			ClientOrderID: marketMakerClientOrderPrefix + "buy-test",
		},
		OrderID: 42,
		Status:  types.OrderStatusPartiallyFilled,
	}
	trade := types.Trade{
		ID: 7, OrderID: order.OrderID, Symbol: order.Symbol,
		Side: types.SideTypeBuy, Quantity: fixedpoint.MustNewFromString("0.001"),
	}
	if !isOwnedMarketMakerTrade(trade, order, true) {
		t.Fatal("every partial maker execution must trigger balance-aware requoting")
	}
	if isOwnedMarketMakerTrade(trade, order, false) {
		t.Fatal("a trade without an executor-owned order must be ignored")
	}
	trade.OrderID++
	if isOwnedMarketMakerTrade(trade, order, true) {
		t.Fatal("an execution for a different order must be ignored")
	}
}

func TestMakerTerminalFillDefersNormalReplacement(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if !makerTerminalFillDefersReplacement(0, 4, 5, time.Time{}, now) {
		t.Fatal("a fill observed during planning must defer the normal replacement")
	}
	if !makerTerminalFillDefersReplacement(0, 5, 5, now.Add(-time.Second), now) {
		t.Fatal("a recent fill must defer a normal refresh whose planning started afterward")
	}
	if makerTerminalFillDefersReplacement(0, 5, 5, now.Add(-3*time.Second), now) {
		t.Fatal("an old terminal fill must not suppress unrelated refreshes")
	}
	if makerTerminalFillDefersReplacement(7, 5, 6, now, now) {
		t.Fatal("the generation-matched fill worker must be allowed to replace quotes")
	}
}

func TestMakerFillRebalanceQuoteGenerationGate(t *testing.T) {
	if !makerFillRebalanceQuoteAllowed(false, 0, 0) {
		t.Fatal("ordinary BBO evaluation should run when no fill rebalance is scheduled")
	}
	if !makerFillRebalanceQuoteAllowed(true, 4, 0) {
		t.Fatal("ordinary BBO evidence evaluation must continue during a fill rebalance")
	}
	if !makerFillRebalanceQuoteAllowed(true, 4, 4) {
		t.Fatal("the synchronized execution generation should be allowed to quote")
	}
	if makerFillRebalanceQuoteAllowed(true, 5, 4) {
		t.Fatal("a stale synchronized generation must not quote after a newer fill")
	}
	if makerFillRebalanceQuoteAllowed(false, 4, 4) {
		t.Fatal("a forced quote must not run after its worker has finished")
	}
}

func TestMakerFillRebalanceFailureInvalidatesGeneration(t *testing.T) {
	strategy := &Strategy{
		makerFillRefreshScheduled:  true,
		makerFillRefreshGeneration: 4,
	}
	strategy.retryMakerFillRebalanceLocked(4)
	if strategy.makerFillRefreshGeneration != 5 || !strategy.makerFillRefreshPending {
		t.Fatalf("failed atomic swap must force another synchronized generation: generation=%d pending=%t",
			strategy.makerFillRefreshGeneration, strategy.makerFillRefreshPending)
	}
	strategy.retryMakerFillRebalanceLocked(4)
	if strategy.makerFillRefreshGeneration != 5 {
		t.Fatal("a stale failure callback must not invalidate the next generation")
	}
}

func TestMakerQuoteNearFillProtectsETHJPYQueueAtWindowExpiry(t *testing.T) {
	plan := MarketMakerQuotePlan{
		AllowBid: true, AllowAsk: true,
		BidTouchDistanceBps: 15, AskTouchDistanceBps: 15,
	}
	// The 302158 bid was about 0.5 bps below the later 302173 best ask. The
	// opposite side is farther away, but the pair still has enough gross edge;
	// refreshing both sides here would destroy the near-fill queue position.
	if !makerQuoteNearFill(302158, 303427, 302172, 302173, plan, 26) {
		t.Fatal("fee-safe ETHJPY bid approaching best ask should retain queue priority")
	}
	retainBid, retainAsk := makerQuoteNearFillSides(302158, 303427, 302172, 302173, plan, 26)
	if !retainBid || retainAsk {
		t.Fatalf("expected to retain only the approaching bid: retainBid=%t retainAsk=%t", retainBid, retainAsk)
	}
	if makerQuoteNearFill(302158, 303427, 302700, 302710, plan, 26) {
		t.Fatal("pair with neither side near the proposed executable distance should be reviewed for replacement")
	}
	if makerQuoteNearFill(302158, 302500, 302172, 302173, plan, 26) {
		t.Fatal("pair inside the round-trip fee/adverse-selection floor must not be retained")
	}
	if makerQuoteNearFill(302180, 303427, 302172, 302173, plan, 26) {
		t.Fatal("marketable/crossed bid must not be retained")
	}
}

func TestMakerOrdersToCancelPreservesApproachingSide(t *testing.T) {
	active := []types.Order{
		{OrderID: 1, SubmitOrder: types.SubmitOrder{Side: types.SideTypeBuy}},
		{OrderID: 2, SubmitOrder: types.SubmitOrder{Side: types.SideTypeSell}},
	}
	orders := makerOrdersToCancelForReplacement(active, true, false)
	if len(orders) != 1 || orders[0].OrderID != 2 {
		t.Fatalf("retained bid must not be cancelled: %+v", orders)
	}
	orders = makerOrdersToCancelForReplacement(active, false, true)
	if len(orders) != 1 || orders[0].OrderID != 1 {
		t.Fatalf("retained ask must not be cancelled: %+v", orders)
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

func TestCrossingDecisionAtDistanceUsesActualQuoteDistance(t *testing.T) {
	start := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	for i := 0; i < 8*60; i++ {
		mid := 100 + 0.5*math.Sin(float64(i)*math.Pi/4)
		model.points = append(model.points, MarketMakerHorizonPoint{At: start.Add(time.Duration(i) * time.Minute), Mid: mid})
	}
	c := MarketMakerConfig{HorizonLookback: types.Duration(6 * time.Hour)}
	near := model.CrossingDecisionAtDistance(start.Add(8*time.Hour), c, 10*time.Minute, 20)
	far := model.CrossingDecisionAtDistance(start.Add(8*time.Hour), c, 10*time.Minute, 150)
	if near.UpCrosses == 0 || near.UpCrossesPerHour <= 0 || near.ObservedHours <= 0 {
		t.Fatalf("expected actual-distance upward crossings: %+v", near)
	}
	if far.UpCrosses != 0 || far.UpCrossesPerHour != 0 {
		t.Fatalf("farther ask must not inherit the near-distance crossing rate: near=%+v far=%+v", near, far)
	}
}

func TestCrossingDecisionTreatsZeroTouchesAsExposure(t *testing.T) {
	start := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	for minute := 0; minute <= 390; minute++ {
		model.points = append(model.points, MarketMakerHorizonPoint{
			At: start.Add(time.Duration(minute) * time.Minute), Bid: 100, Ask: 100.01,
		})
	}
	config := MarketMakerConfig{
		HorizonLookback: types.Duration(6 * time.Hour), HorizonMinSamples: 6,
		MakerFeeBps: 1, AdverseSelectionBps: 0.5, MinimumNetEdgeBps: 1,
	}
	decision := model.CrossingDecisionAtSideDistances(
		start.Add(390*time.Minute), config, 30*time.Minute, 15, 15, 30)
	if decision.UpCrosses != 0 || decision.DownCrosses != 0 {
		t.Fatalf("flat book should have zero raw touches: %+v", decision)
	}
	if decision.EffectiveSamples < 11 || decision.EffectiveSamples > 13 {
		t.Fatalf("six hours should contain about twelve overlap-adjusted 30m exposures: %+v", decision)
	}
	if decision.BuyTouchProbability <= 0 || decision.SellTouchProbability <= 0 ||
		decision.BuyTouchProbability >= 0.1 || decision.SellTouchProbability >= 0.1 {
		t.Fatalf("zero touches must produce a small finite Jeffreys posterior: %+v", decision)
	}
	if !decision.HasSufficientCrossings(config.HorizonMinSamples) {
		t.Fatalf("valid zero-touch exposure must not be classified as missing data: %+v", decision)
	}
}

func TestTouchRateUsesDiscreteWindowPosteriorWithoutPoissonTransform(t *testing.T) {
	decision := MarketMakerHorizonDecision{
		Horizon: 30 * time.Minute, BuyTouchProbability: 0.2, SellTouchProbability: 0.3,
	}
	if got := decision.BuyTouchRatePerHour(); math.Abs(got-0.4) > 1e-12 {
		t.Fatalf("buy renewal rate must equal p/H: got %f", got)
	}
	if got := decision.SellTouchRatePerHour(); math.Abs(got-0.6) > 1e-12 {
		t.Fatalf("sell renewal rate must equal p/H: got %f", got)
	}
}

func TestHorizonDecisionRejectsMismatchedDirectionalFallback(t *testing.T) {
	decision := MarketMakerHorizonDecision{
		Horizon:            10 * time.Minute,
		UpCrosses:          14,
		DownCrosses:        13,
		UpCrossesPerHour:   2.3,
		DownCrossesPerHour: 2.1,
		Reason:             "insufficient completed horizon samples",
	}
	if decision.HasSufficientCrossings(20) {
		t.Fatalf("directional barrier fallback must not be treated as quote-distance evidence: %+v", decision)
	}

	decision.Reason = "max fee-adjusted two-sided edge per hour"
	if !decision.HasSufficientCrossings(20) {
		t.Fatalf("valid quote-distance crossing decision should be accepted: %+v", decision)
	}

	decision.UpCrosses = 9
	decision.DownCrosses = 10
	if decision.HasSufficientCrossings(20) {
		t.Fatalf("decision below the configured sample threshold must remain invalid: %+v", decision)
	}
}

func TestDynamicInventoryBandUsesRiskAndOrderLevels(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, InventoryRiskBudgetJPY: 10, InventoryRiskZScore: 1.645,
		InventoryTargetRatio: 0.5,
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

func TestQuoteUsesExplicitCenteredInventoryEdges(t *testing.T) {
	c := MarketMakerConfig{
		InventoryTarget: 0.30, InventoryLimit: 0.15,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
	}
	quote := func(inventory float64) MarketMakerQuotePlan {
		return c.Quote(MarketMakerQuoteInput{
			MidPrice: 12_000, BestBid: 11_999, BestAsk: 12_001,
			Inventory: inventory, InventoryMin: 0.15, InventoryMax: 0.45,
			CanBuy: true, CanSell: true,
		})
	}
	inside := quote(0.287)
	if !inside.AllowBid || !inside.AllowAsk {
		t.Fatalf("inventory inside centered band must quote both sides: %+v", inside)
	}
	if upper := quote(0.45); upper.AllowBid || !upper.AllowAsk {
		t.Fatalf("upper edge must close only the bid: %+v", upper)
	}
	if lower := quote(0.15); !lower.AllowBid || lower.AllowAsk {
		t.Fatalf("lower edge must close only the ask: %+v", lower)
	}
}

func TestDynamicQuoteNotionalUsesRiskBudget(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, MinimumQuoteNotional: 100, MaximumQuoteNotional: 480,
		InventoryRiskBudgetJPY: 10, InventoryRiskZScore: 1.645,
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
		InventoryRiskBudgetJPY: 10, InventoryRiskZScore: 1.645,
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

func TestShrinkVolatilityUsesSampleWeightedVariance(t *testing.T) {
	got, weight := ShrinkVolatility(0.5, 1.5, 20, 20)
	want := math.Sqrt(0.5*0.5*0.5 + 0.5*1.5*1.5)
	if math.Abs(got-want) > 1e-9 || math.Abs(weight-0.5) > 1e-9 {
		t.Fatalf("unexpected shrinkage: got=%.6f weight=%.4f want=%.6f", got, weight, want)
	}
	if got <= 0.5 || got >= 1.5 {
		t.Fatalf("shrinkage must blend rather than impose the prior as a hard floor: %.6f", got)
	}
}

func TestDynamicQuoteNotionalUsesTwoSidedFillLoad(t *testing.T) {
	c := MarketMakerConfig{
		QuoteNotional: 120, MinimumQuoteNotional: 100, MaximumQuoteNotional: 480,
		InventoryRiskBudgetJPY: 10, InventoryRiskZScore: 1.645,
	}
	coldStart := c.DynamicQuoteNotionalWithFillRates(0.48, 10*time.Minute, 0, 0)
	balanced := c.DynamicQuoteNotionalWithFillRates(0.48, 10*time.Minute, 12, 12)
	oneSided := c.DynamicQuoteNotionalWithFillRates(0.48, 10*time.Minute, 12, 0)
	if coldStart <= balanced {
		t.Fatalf("cold-start prior should allocate one expected ticket, while observed balanced flow allocates more tickets: coldStart=%.2f balanced=%.2f", coldStart, balanced)
	}
	if math.Abs(oneSided-coldStart) > 1e-9 || oneSided <= balanced {
		t.Fatalf("one-sided intensity should retain the bootstrap ticket without a 1-to-max discontinuity: coldStart=%.2f balanced=%.2f oneSided=%.2f", coldStart, balanced, oneSided)
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
	upBias := c.SideAllocationBias(SideQuoteAllocationInput{DirectionSignal: 1, BookImbalance: 1, InventoryLimit: 1})
	if math.Abs(downBias) > 1e-9 || math.Abs(upBias) > 1e-9 {
		t.Fatalf("direction and imbalance must not be applied twice through quantity allocation: down=%.4f up=%.4f", downBias, upBias)
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
		BuyFillRate: 0, SellFillRate: 10, CanBuy: true, CanSell: true,
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

func TestReconcileMakerPositionBase(t *testing.T) {
	market := types.Market{Symbol: "SOLJPY", BaseCurrency: "SOL", QuoteCurrency: "JPY"}
	position := types.NewPositionFromMarket(market)
	if err := position.ModifyBase(fixedpoint.MustNewFromString("0.449")); err != nil {
		t.Fatal(err)
	}
	tolerance := fixedpoint.NewFromFloat(1e-9)
	delta, changed, err := reconcileMakerPositionBase(position, fixedpoint.MustNewFromString("0.4490000005"), tolerance)
	if err != nil || changed || delta.Sign() != 0 {
		t.Fatalf("sub-floating-point drift should not reconcile: delta=%s changed=%v err=%v", delta, changed, err)
	}
	delta, changed, err = reconcileMakerPositionBase(position, fixedpoint.MustNewFromString("0.451"), tolerance)
	if err != nil || !changed || delta.Float64() != 0.002 {
		t.Fatalf("material account drift should reconcile: delta=%s changed=%v err=%v", delta, changed, err)
	}
	if got := position.GetBase().Float64(); got != 0.451 {
		t.Fatalf("position base was not reconciled: %.6f", got)
	}
}

func TestHealthyDirectionSignalFailsClosed(t *testing.T) {
	snapshot := ModelSnapshot{LambdaUp: 3, LambdaDown: 1, Health: HealthHealthy}
	if got := healthyDirectionSignal(snapshot, HealthHealthy); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("healthy direction mismatch: %.4f", got)
	}
	if got := healthyDirectionSignal(snapshot, HealthDegraded); got != 0 {
		t.Fatalf("degraded raw evidence must suppress direction: %.4f", got)
	}
	snapshot.Health = HealthDegraded
	if got := healthyDirectionSignal(snapshot, HealthHealthy); got != 0 {
		t.Fatalf("degraded crossing model must suppress direction: %.4f", got)
	}
}

func TestMarketMakerHorizonDistinguishesQuietBBOFromOutage(t *testing.T) {
	c := MarketMakerConfig{}
	start := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	model.Observe(start, 100, c)
	model.Observe(start.Add(time.Minute), 100, c)
	if len(model.points) != 2 || model.points[1].GapBefore {
		t.Fatalf("one-minute change-driven BBO silence must be forward-filled: %+v", model.points)
	}
	model.Observe(start.Add(3*time.Minute), 100, c)
	if len(model.points) != 3 || !model.points[2].GapBefore {
		t.Fatalf("two-minute BBO outage must mark the path discontinuous: %+v", model.points)
	}
}

func TestEquityProtectedAskFloorUsesExactFeeMath(t *testing.T) {
	c := MarketMakerConfig{MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2}
	want := 80 * math.Exp((c.AdverseSelectionBps+c.MinimumNetEdgeBps/2)/10_000) / (1 - c.MakerFeeBps/10_000)
	if got := equityProtectedAskFloor(80, c.MakerFeeBps, c.AdverseSelectionBps, c.MinimumNetEdgeBps); math.Abs(got-want) > 1e-12 {
		t.Fatalf("equity floor fee math is incorrect: got %.12f want %.12f", got, want)
	}
}

func TestMarketMakerAskChasesMarkBeforeFirstFill(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80, InventoryLimit: 10,
	}
	quote := func(mid float64) MarketMakerQuotePlan {
		return c.Quote(MarketMakerQuoteInput{
			MidPrice: mid, BestBid: mid - 0.01, BestAsk: mid + 0.01,
			Inventory: 10, CanBuy: true, CanSell: true,
		})
	}
	high, low := quote(100), quote(95)
	if !(low.AskPrice < high.AskPrice) {
		t.Fatalf("ask must chase a lower live mark without waiting for a fill: high=%+v low=%+v", high, low)
	}
	if math.Abs(low.AskPrice/high.AskPrice-0.95) > 1e-12 {
		t.Fatalf("mark-relative ask should move proportionally: high=%.12f low=%.12f", high.AskPrice, low.AskPrice)
	}
}

func TestMarketMakerAskIncreasesTotalMarkedEquity(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 1, MaximumHalfSpreadBps: 80, InventoryLimit: 10,
	}
	const quoteBalance, baseBalance, sellQuantity, mark = 1000.0, 10.0, 2.0, 95.0
	plan := c.Quote(MarketMakerQuoteInput{
		MidPrice: mark, BestBid: 94.99, BestAsk: 95.01,
		Inventory: baseBalance, CanBuy: true, CanSell: true,
	})
	feeRate := c.MakerFeeBps / 10_000
	equityBefore := quoteBalance + baseBalance*mark
	equityAfter := quoteBalance + sellQuantity*plan.AskPrice*(1-feeRate) + (baseBalance-sellQuantity)*mark
	minimumGain := sellQuantity * mark * (math.Exp((c.AdverseSelectionBps+c.MinimumNetEdgeBps/2)/10_000) - 1)
	if equityAfter+1e-12 < equityBefore+minimumGain {
		t.Fatalf("sell reduced protected total marked equity: before=%.12f after=%.12f minGain=%.12f plan=%+v", equityBefore, equityAfter, minimumGain, plan)
	}
}

func TestEmpiricalSideVolatilityUsesAskForBuyAndBidForSell(t *testing.T) {
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{HorizonLookback: types.Duration(time.Hour)}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for second := 0; second < 30; second++ {
		bid := 100 * math.Exp(float64(second)*0.00001)
		ask := 101 * math.Exp(float64(second)*0.00010)
		model.ObserveBookWithGap(start.Add(time.Duration(second)*time.Second), bid, ask, config, false)
	}
	estimate := model.EmpiricalSideVolatilityEstimate(start.Add(30*time.Second), time.Minute)
	if estimate.BuySamples < 8 || estimate.SellSamples < 8 {
		t.Fatalf("insufficient side samples: %+v", estimate)
	}
	if estimate.BuyBps <= 5*estimate.SellBps {
		t.Fatalf("ask-path buy volatility must remain distinct from bid-path sell volatility: %+v", estimate)
	}
}

func TestMakerTouchDistanceIncludesObservedSpread(t *testing.T) {
	bestBid, bestAsk := 99.0, 101.0
	bidQuote, askQuote := 99.8, 100.2
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(bestBid, bestAsk, bidQuote, askQuote)
	if buyDistance <= 100 || sellDistance <= 100 {
		t.Fatalf("touch distances omitted the wide BBO spread: buy=%f sell=%f", buyDistance, sellDistance)
	}
	if math.Abs(grossEdge-math.Log(askQuote/bidQuote)*10_000) > 1e-12 {
		t.Fatalf("strategy edge must exclude the observed market spread: %f", grossEdge)
	}

	config := MarketMakerConfig{
		HorizonLookback: types.Duration(time.Hour), HorizonMinSamples: 1,
		MakerFeeBps: 1, MaximumHalfSpreadBps: 200,
	}
	model := MarketMakerHorizonModel{}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for minute := 0; minute <= 5; minute++ {
		// Mid rises by more than 20 bps, but best bid never reaches the ask quote.
		model.ObserveBookWithGap(
			start.Add(time.Duration(minute)*time.Minute),
			bestBid+0.04*float64(minute), bestAsk+0.04*float64(minute), config, false)
	}
	decision := model.CrossingDecisionAtSideDistances(
		start.Add(5*time.Minute), config, time.Minute, buyDistance, sellDistance, grossEdge)
	if decision.UpCrosses != 0 || decision.DownCrosses != 0 {
		t.Fatalf("midpoint movement was incorrectly counted as an executable touch: %+v", decision)
	}
}

func TestMarketMakerQuoteUsesSeparateExecutionVolatility(t *testing.T) {
	config := MarketMakerConfig{
		MakerFeeBps: 1, AdverseSelectionBps: 1,
		MinimumHalfSpreadBps: 5, MaximumHalfSpreadBps: 100,
		VolatilityMultiplier: 1, InventoryLimit: 1,
	}
	plan := config.Quote(MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99.99, BestAsk: 100.01,
		BuyVolatilityPerSqrtSec: 1.0, SellVolatilityPerSqrtSec: 0.25,
		TradingHorizonSeconds: 600, CanBuy: true, CanSell: true,
	})
	if plan.BidHalfSpreadBps <= plan.AskHalfSpreadBps {
		t.Fatalf("higher ask-path buy volatility must widen the bid side independently: %+v", plan)
	}
	if plan.BidDistanceBps <= plan.AskDistanceBps {
		t.Fatalf("side volatility did not reach final quote prices: %+v", plan)
	}
}

func TestBrownianAcquisitionTouchProbabilityFallsWithPositiveDrift(t *testing.T) {
	withoutDrift := brownianLowerBarrierTouchProbability(30, 0, 0.5, 15*60)
	withDrift := brownianLowerBarrierTouchProbability(30, 15, 0.5, 15*60)
	if withoutDrift <= withDrift || withoutDrift < 0 || withoutDrift > 1 || withDrift < 0 || withDrift > 1 {
		t.Fatalf("unexpected first-passage probabilities: zero=%.8f positive=%.8f", withoutDrift, withDrift)
	}
}

func TestAcquisitionQuoteDeltaUsesConfidenceBoundAndShadowMode(t *testing.T) {
	cfg := AcquisitionQuoteConfig{Enabled: true, ShadowOnly: true, MaxDeltaBps: 15, MinDriftBps: 2, DriftConfidenceZScore: 1, MinDirection: 0.25}
	delta, probability := acquisitionQuoteDeltaBps(35, 20, 30, 0.5, 15*60, cfg)
	if math.Abs(delta-15) > 1e-9 || probability <= 0 || probability > 1 {
		t.Fatalf("unexpected confidence-bounded acquisition delta: delta=%.8f probability=%.8f", delta, probability)
	}
	noConfidence, _ := acquisitionQuoteDeltaBps(35, 20, 10, 0.5, 15*60, cfg)
	if noConfidence != 0 {
		t.Fatalf("delta must fail closed when the lower confidence drift is below the minimum: %.8f", noConfidence)
	}

	base := MarketMakerConfig{MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2, MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80, InventoryLimit: 1}
	input := MarketMakerQuoteInput{MidPrice: 100, BestBid: 99.9, BestAsk: 100.1, TradingHorizonSeconds: 15 * 60,
		BuyVolatilityPerSqrtSec: 0.5, SellVolatilityPerSqrtSec: 0.5, AcquisitionDriftBps: 30,
		AcquisitionVolatilityPerSqrtSecBps: 0.5, AcquisitionHorizonSeconds: 15 * 60, AcquisitionDirection: 1,
		CanBuy: true, CanSell: true}
	shadow := base
	shadow.AcquisitionQuote = cfg
	shadowPlan := shadow.Quote(input)
	if shadowPlan.AcquisitionDeltaBps <= 0 || shadowPlan.AcquisitionApplied || shadowPlan.BidPrice <= 0 {
		t.Fatalf("shadow plan should report but not apply the delta: %+v", shadowPlan)
	}
	live := base
	cfg.ShadowOnly = false
	live.AcquisitionQuote = cfg
	livePlan := live.Quote(input)
	if !livePlan.AcquisitionApplied || livePlan.BidPrice <= shadowPlan.BidPrice || livePlan.BidPrice >= input.BestAsk {
		t.Fatalf("live acquisition plan must improve a maker bid without crossing: shadow=%+v live=%+v", shadowPlan, livePlan)
	}
	if livePlan.BidDistanceBps < live.MinimumHalfSpreadBps-1e-9 {
		t.Fatalf("live acquisition plan crossed the fee floor: %+v", livePlan)
	}
}

func TestInventoryActuationMovesTargetSideInwardWithoutSpendingRoundTripEdge(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		InventoryTarget: 1, InventoryLimit: 1,
	}
	in := MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99.99, BestAsk: 100.01,
		Inventory: 1, CanBuy: true, CanSell: true,
	}
	neutral := c.Quote(in)
	in.InventoryActuationDirection = 1
	in.InventoryActuationStrength = 1
	actuated := c.Quote(in)
	if !actuated.AllowBid || !actuated.AllowAsk {
		t.Fatalf("actuation must retain both quote sides: %+v", actuated)
	}
	if actuated.BidPrice <= neutral.BidPrice || math.Abs(actuated.AskPrice-neutral.AskPrice) > 1e-12 {
		t.Fatalf("buy actuation must move only the target bid inward: neutral=%+v actuated=%+v", neutral, actuated)
	}
	_, _, grossEdge := MakerTouchDistances(in.BestBid, in.BestAsk, actuated.BidPrice, actuated.AskPrice)
	wantFloor := 2*c.MakerFeeBps + 2*c.AdverseSelectionBps + c.MinimumNetEdgeBps
	if grossEdge+1e-9 < wantFloor || actuated.InventoryActuationInwardBps <= 0 {
		t.Fatalf("actuated quote spent required round-trip edge: edge=%.8f floor=%.8f plan=%+v", grossEdge, wantFloor, actuated)
	}
}

func TestInventoryActuationMovesSellTargetInwardAndKeepsBid(t *testing.T) {
	c := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		InventoryTarget: 1, InventoryLimit: 1,
	}
	in := MarketMakerQuoteInput{
		MidPrice: 100, BestBid: 99.99, BestAsk: 100.01,
		Inventory: 1, CanBuy: true, CanSell: true,
	}
	neutral := c.Quote(in)
	in.InventoryActuationDirection = -1
	in.InventoryActuationStrength = 1
	actuated := c.Quote(in)
	if !actuated.AllowBid || !actuated.AllowAsk {
		t.Fatalf("sell actuation must retain both quote sides: %+v", actuated)
	}
	if actuated.AskPrice >= neutral.AskPrice || math.Abs(actuated.BidPrice-neutral.BidPrice) > 1e-12 {
		t.Fatalf("sell actuation must move only the target ask inward: neutral=%+v actuated=%+v", neutral, actuated)
	}
	_, _, grossEdge := MakerTouchDistances(in.BestBid, in.BestAsk, actuated.BidPrice, actuated.AskPrice)
	wantFloor := 2*c.MakerFeeBps + 2*c.AdverseSelectionBps + c.MinimumNetEdgeBps
	if grossEdge+1e-9 < wantFloor || actuated.InventoryActuationInwardBps <= 0 {
		t.Fatalf("sell actuation spent required round-trip edge: edge=%.8f floor=%.8f plan=%+v", grossEdge, wantFloor, actuated)
	}
}
