package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

func TestCompactReplayEventsRemovesExactDuplicates(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	books := compactBBO([]bboSnapshot{{time: at, bid: 100, bidSize: 1, ask: 101, askSize: 2}, {time: at.Add(time.Second), bid: 100, bidSize: 1, ask: 101, askSize: 2}})
	if len(books) != 1 {
		t.Fatalf("expected one unique BBO, got %d", len(books))
	}
	trades := compactTrades([]tick{{time: at, price: 100, size: 1, side: types.SideTypeBuy}, {time: at, price: 100, size: 1, side: types.SideTypeBuy}})
	if len(trades) != 1 {
		t.Fatalf("expected one unique trade, got %d", len(trades))
	}
}

func TestCompactBBOAtIntervalKeepsLastStatePerBucket(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	books := compactBBOAtInterval([]bboSnapshot{
		{time: at.Add(100 * time.Millisecond), bid: 100, ask: 101},
		{time: at.Add(700 * time.Millisecond), bid: 100.5, ask: 101.5},
		{time: at.Add(1200 * time.Millisecond), bid: 101, ask: 102},
	}, time.Second)
	if len(books) != 2 || books[0].bid != 100.5 || books[1].bid != 101 {
		t.Fatalf("interval compaction did not preserve last state: %+v", books)
	}
}

func TestProductionReplayWarmupCoversLookbackAndPathMaturity(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{
		HorizonLookback: types.Duration(6 * time.Hour),
		FastWindows: []types.Duration{
			types.Duration(10 * time.Minute),
			types.Duration(15 * time.Minute),
			types.Duration(30 * time.Minute),
		},
		BOCPD45: gammacapture.BOCPD45Config{
			Enabled: true, CalibrationWindow: types.Duration(6 * time.Hour),
			Horizon: types.Duration(45 * time.Second),
		},
	}
	if got, want := productionReplayWarmup(cfg), 6*time.Hour+30*time.Minute; got != want {
		t.Fatalf("unexpected one-stage warmup: got %s want %s", got, want)
	}
	cfg.JointDistanceQuantity.TwoStageContinuation = true
	if got, want := productionReplayWarmup(cfg), 7*time.Hour; got != want {
		t.Fatalf("unexpected two-stage warmup: got %s want %s", got, want)
	}
	cfg.BOCPD45.CalibrationWindow = types.Duration(8 * time.Hour)
	if got, want := productionReplayWarmup(cfg), 8*time.Hour+45*time.Second; got != want {
		t.Fatalf("longer causal calibrator must own warmup: got %s want %s", got, want)
	}
}

func TestProductionReplayWarmupIgnoresDisabledMacroHistory(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{
		HorizonLookback: types.Duration(6 * time.Hour),
		FastWindows:     []types.Duration{types.Duration(30 * time.Minute)},
		MacroInventory: gammacapture.MacroInventoryConfig{
			Enabled:     false,
			BarInterval: types.Duration(10 * time.Minute),
			Lookback:    types.Duration(240 * time.Hour),
			RiskHorizons: []types.Duration{
				types.Duration(3 * time.Hour),
				types.Duration(24 * time.Hour),
			},
		},
	}
	if got, want := productionReplayWarmup(cfg), 6*time.Hour+30*time.Minute; got != want {
		t.Fatalf("disabled Macro must not own policy replay warmup: got %s want %s", got, want)
	}
	if got, want := macroReplayWarmup(cfg), 264*time.Hour+10*time.Minute; got != want {
		t.Fatalf("explicit Macro research must retain its diagnostic history: got %s want %s", got, want)
	}
	cfg.MacroInventory.Enabled = true
	if got, want := productionReplayWarmup(cfg), 264*time.Hour+10*time.Minute; got != want {
		t.Fatalf("enabled Macro must own policy replay warmup: got %s want %s", got, want)
	}
}

func TestProductionReplayLoadRangeUsesEarlierCalibrationStart(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{
		HorizonLookback: types.Duration(6 * time.Hour),
		FastWindows:     []types.Duration{types.Duration(30 * time.Minute)},
	}
	from := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	calibrationFrom := from.Add(-time.Hour)
	warmFrom, exactFrom := productionReplayLoadRange(cfg, from, calibrationFrom)
	if !exactFrom.Equal(calibrationFrom) {
		t.Fatalf("calibration must remain tick-exact: got %s want %s", exactFrom, calibrationFrom)
	}
	if want := calibrationFrom.Add(-6*time.Hour - 30*time.Minute); !warmFrom.Equal(want) {
		t.Fatalf("warmup must precede the earliest exact interval: got %s want %s", warmFrom, want)
	}
}

func TestReplayNoOrderReferenceMoveUsesExecutableSides(t *testing.T) {
	if replayNoOrderReferenceMoved(100, 101, 99.93, 100.93, 8) {
		t.Fatal("sub-threshold BBO movement must retain the no-order lease")
	}
	if !replayNoOrderReferenceMoved(100, 101, 99.91, 100.91, 8) {
		t.Fatal("material BBO movement must reopen the state-conditional decision")
	}
	if replayNoOrderReferenceMoved(0, 0, 90, 91, 8) {
		t.Fatal("an unanchored exchange-feasibility retry must keep its fixed clock")
	}
}

func TestReplayUsesLiveEmptyBookRefreshSemantics(t *testing.T) {
	if !gammacapture.MakerQuoteRefreshRequired(
		5*time.Minute, 10*time.Second, 30*time.Minute,
		false, true, false, false, false,
		false, true, false, false, false,
	) {
		t.Fatal("an empty replay book must wake at its retry deadline without a fictitious resting-order lease")
	}
	if gammacapture.MakerQuoteRefreshRequired(
		5*time.Minute, 10*time.Second, 30*time.Minute,
		false, true, false, false, false,
		false, false, false, false, false,
	) {
		t.Fatal("a real resting order must retain its selected 30-minute reference lease")
	}
}

func TestEarlyStatisticalRealignmentResearchOverrideDoesNotChangeLiveRefreshSemantics(t *testing.T) {
	elapsed := 5 * time.Minute
	minRefresh := 10 * time.Second
	keepDuration := 30 * time.Minute
	if gammacapture.MakerQuoteRefreshRequired(
		elapsed, minRefresh, keepDuration,
		false, false, false, false, false,
		false, false, false, true, false,
	) {
		t.Fatal("ordinary statistical improvement must not bypass the live keep-duration lease")
	}
	if !gammacapture.MakerQuoteRefreshRequired(
		elapsed, minRefresh, keepDuration,
		false, false, false, false, false,
		false, false, false, true, true,
	) {
		t.Fatal("the research replay must be able to exercise the existing explicit early-realignment hook")
	}
}

func TestReplayDatasetCacheKeyIgnoresStrategyConfigFingerprint(t *testing.T) {
	header := replayDatasetCacheHeader{Version: 1, Mode: "exact", Symbol: "ETHJPY", From: time.Unix(1, 0), To: time.Unix(2, 0), ConfigFingerprint: "one"}
	other := header
	other.ConfigFingerprint = "two"
	if replayDatasetCacheKey(header) != replayDatasetCacheKey(other) {
		t.Fatal("strategy config fingerprint must not invalidate parsed market-data cache")
	}
}

func TestProductionConfigOverridesAreOptIn(t *testing.T) {
	base := gammacapture.MarketMakerConfig{
		InventoryRiskZScore:  1.645,
		MinimumNetEdgeBps:    2,
		MinimumHalfSpreadBps: 15,
	}
	if got := (productionConfigOverrides{}).apply(base); got.InventoryRiskZScore != base.InventoryRiskZScore ||
		got.MinimumNetEdgeBps != base.MinimumNetEdgeBps ||
		got.MinimumHalfSpreadBps != base.MinimumHalfSpreadBps ||
		got.JointDistanceQuantity.PathUtilityHorizonSelection ||
		got.JointDistanceQuantity.JointHorizonSelection {
		t.Fatalf("zero-value research overrides must be a no-op: got=%+v want=%+v", got, base)
	}
	got := (productionConfigOverrides{
		InventoryRiskZScore:         1.282,
		MinimumNetEdgeBps:           0,
		MinimumNetEdgeSet:           true,
		EnablePathUtilityHorizon:    true,
		EnableJointHorizonSelection: true,
	}).apply(base)
	if got.InventoryRiskZScore != 1.282 || got.MinimumNetEdgeBps != 0 || got.MinimumHalfSpreadBps != 15 ||
		!got.JointDistanceQuantity.PathUtilityHorizonSelection ||
		!got.JointDistanceQuantity.JointHorizonSelection {
		t.Fatalf("explicit research overrides were not isolated correctly: %+v", got)
	}
}

func TestProductionReplayHorizonDiagnosticFinalizesDistinctDenominators(t *testing.T) {
	d := productionReplayHorizonDiagnostic{
		Evaluations: 4, PathReady: 2,
		scoreSum: 12, scoreStdErrorSum: 8,
		buyTouchProbabilitySum: 1.2, sellTouchProbabilitySum: 0.8,
		pathEffectiveSamplesSum: 18,
	}
	d.finalize()
	if math.Abs(d.MeanScoreBpsPerHour-3) > 1e-12 ||
		math.Abs(d.MeanScoreStdErrorBpsPerHour-2) > 1e-12 ||
		math.Abs(d.MeanBuyTouchProbability-0.3) > 1e-12 ||
		math.Abs(d.MeanSellTouchProbability-0.2) > 1e-12 ||
		math.Abs(d.MeanPathEffectiveSamples-9) > 1e-12 {
		t.Fatalf("horizon diagnostic mean mismatch: %+v", d)
	}
}

func TestProductionQueueRequiresCompleteOrderFill(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	origin := replayQuoteOrigin{PlacedAt: at.Add(-time.Minute), FastDirection: -0.25}
	s := &productionReplayState{
		cfg: gammacapture.MarketMakerConfig{MakerFeeBps: 10},
		bidOrder: productionReplayOrder{
			active: true, side: types.SideTypeBuy, price: 100,
			remaining: 2, queueAhead: 1, origin: origin,
		},
		fillsByDay: make(map[string]*productionReplayDay),
	}
	s.consume(&s.bidOrder, tick{time: at, price: 100, size: 2, side: types.SideTypeSell})
	if s.fills != 0 || !s.bidOrder.active || s.bidOrder.remaining != 1 {
		t.Fatalf("partial execution counted as full: %+v fills=%d", s.bidOrder, s.fills)
	}
	if !s.fillRefreshPending || s.lastMakerFill.Side != types.SideTypeBuy || s.lastMakerFill.Quantity != 1 {
		t.Fatalf("partial fill did not schedule balance-aware next-BBO refresh: pending=%t fill=%+v", s.fillRefreshPending, s.lastMakerFill)
	}
	if len(s.fillEvents) != 1 || s.fillEvents[0].Quantity != 1 || s.fillEvents[0].FeeJPY != 0.1 {
		t.Fatalf("partial private execution disappeared from replay trade history: %+v", s.fillEvents)
	}
	if s.fillEvents[0].QuoteOrigin == nil ||
		!s.fillEvents[0].QuoteOrigin.PlacedAt.Equal(origin.PlacedAt) ||
		s.fillEvents[0].QuoteOrigin.FastDirection != origin.FastDirection {
		t.Fatalf("maker fill lost its causal quote origin: %+v", s.fillEvents[0])
	}

	s.consume(&s.bidOrder, tick{time: at.Add(time.Second), price: 100, size: 1, side: types.SideTypeSell})
	if !s.fillRefreshPending || !s.lastMakerFill.At.Equal(at.Add(time.Second)) {
		t.Fatalf("terminal fill did not update post-fill state: %+v", s.lastMakerFill)
	}
	if s.fills != 1 || s.bidOrder.active {
		t.Fatalf("order did not complete: %+v fills=%d", s.bidOrder, s.fills)
	}
	if len(s.fillEvents) != 2 || s.fillEvents[1].Quantity != 1 {
		t.Fatalf("terminal execution was not recorded independently: %+v", s.fillEvents)
	}
}

func TestProductionOrderIsFillEligibleOnlyAfterNextBBO(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg:        gammacapture.MarketMakerConfig{MakerFeeBps: 10},
		bidOrder:   productionReplayOrder{active: true, side: types.SideTypeBuy, price: 100, remaining: 1},
		fillsByDay: make(map[string]*productionReplayDay),
	}
	trade := tick{time: at, price: 100, size: 1, side: types.SideTypeSell}
	s.onTrade(trade)
	if s.fills != 0 || s.bidOrder.remaining != 1 {
		t.Fatalf("order filled before next BBO: %+v fills=%d", s.bidOrder, s.fills)
	}
	s.activatePendingQuotesOnNextBBO()
	s.onTrade(tick{time: at.Add(time.Millisecond), price: 100, size: 1, side: types.SideTypeSell})
	if s.fills != 1 || s.bidOrder.active {
		t.Fatalf("order did not fill after next BBO activation: %+v fills=%d", s.bidOrder, s.fills)
	}
}

func TestProductionMakerCrossingFillsOnNextExecutableBBO(t *testing.T) {
	at := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg:        gammacapture.MarketMakerConfig{MakerFeeBps: 10},
		inventory:  1,
		quote:      1_000,
		bidOrder:   productionReplayOrder{active: true, side: types.SideTypeBuy, price: 100, remaining: 2, queueAhead: 50},
		fillsByDay: make(map[string]*productionReplayDay),
	}
	// The submission BBO cannot fill the just-created order.
	s.executeCrossedMakerOrdersAtBBO(bboSnapshot{
		time: at, bid: 99, bidSize: 10, ask: 100, askSize: 10,
	})
	if s.fills != 0 || s.bidOrder.remaining != 2 {
		t.Fatalf("ineligible order filled on its submission BBO: order=%+v fills=%d", s.bidOrder, s.fills)
	}

	// At the next BBO the order becomes live; a crossed ask proves that the
	// complete resting quantity and all queue ahead have cleared. The new ask
	// size is post-event book state, not the historical executed quantity.
	s.activatePendingQuotesOnNextBBO()
	s.executeCrossedMakerOrdersAtBBO(bboSnapshot{
		time: at.Add(time.Second), bid: 98, bidSize: 10, ask: 99, askSize: 1.25,
	})
	if s.fills != 1 || s.bidOrder.active ||
		len(s.fillEvents) != 1 || math.Abs(s.fillEvents[0].Quantity-2) > 1e-12 {
		t.Fatalf("crossed BBO did not complete the resting maker order: order=%+v events=%+v", s.bidOrder, s.fillEvents)
	}
	if !s.fillRefreshPending || s.lastMakerFill.Side != types.SideTypeBuy {
		t.Fatalf("BBO fill did not schedule balance-aware replanning: pending=%v fill=%+v", s.fillRefreshPending, s.lastMakerFill)
	}
}

func TestProductionMakerCrossingAndAggregateTradeCannotDoubleFill(t *testing.T) {
	at := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg:        gammacapture.MarketMakerConfig{MakerFeeBps: 10},
		inventory:  1,
		quote:      1_000,
		bidOrder:   productionReplayOrder{active: true, eligible: true, side: types.SideTypeBuy, price: 100, quantity: 2, remaining: 2},
		fillsByDay: make(map[string]*productionReplayDay),
	}

	s.executeCrossedMakerOrdersAtBBO(bboSnapshot{
		time: at, bid: 98, bidSize: 10, ask: 99, askSize: 1,
	})
	if s.fills != 1 || s.bidOrder.active || len(s.fillEvents) != 1 {
		t.Fatalf("crossed BBO must complete exactly one maker order: order=%+v fills=%d events=%+v",
			s.bidOrder, s.fills, s.fillEvents)
	}
	inventoryAfterBBO, quoteAfterBBO, feesAfterBBO := s.inventory, s.quote, s.fees

	// A public trade at the same causal event can also satisfy the passive
	// crossing predicate, but the BBO already proved the resting order was
	// completed. The inactive order must make this second observation a no-op.
	s.onTrade(tick{time: at, price: 99, size: 10, side: types.SideTypeSell})
	if s.fills != 1 || len(s.fillEvents) != 1 ||
		s.inventory != inventoryAfterBBO || s.quote != quoteAfterBBO || s.fees != feesAfterBBO {
		t.Fatalf("BBO crossing and aggregate trade double-counted one execution: fills=%d events=%+v inventory=%v quote=%v fees=%v",
			s.fills, s.fillEvents, s.inventory, s.quote, s.fees)
	}
}

func TestReplayJointCompletionContractPreservesMatchedOppositeLeg(t *testing.T) {
	at := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg: gammacapture.MarketMakerConfig{
			MakerFeeBps: 10,
			JointDistanceQuantity: gammacapture.JointDistanceQuantityConfig{
				TwoStageContinuation: true, CrossHorizonContinuation: true,
			},
		},
		inventory: 1,
		quote:     1_000,
		completionContract: replayCompletionContract{
			active: true, side: types.SideTypeBuy, price: 99, quantity: 2,
			referenceHorizon: 30 * time.Minute,
			until:            at.Add(30 * time.Minute),
		},
	}
	plan := gammacapture.MarketMakerQuotePlan{
		AllowBid: true, AllowAsk: true, BidPrice: 98, AskPrice: 102,
	}
	projection := gammacapture.ProbabilityCenteredQuoteDecision{}
	crossing := gammacapture.MarketMakerHorizonDecision{}
	joint := gammacapture.JointDistanceQuantityDecision{}
	review := time.Duration(0)
	if !s.applyCompletionContract(
		bboSnapshot{time: at.Add(10 * time.Minute), bid: 100, ask: 101}, 100.5, 1, 1_000,
		&plan, &projection, &crossing, &joint, &review,
	) {
		t.Fatal("expected an active matched completion contract")
	}
	if !plan.AllowBid || plan.AllowAsk || plan.BidPrice != 99 ||
		math.Abs(projection.BuyNotionalJPY-201) > 1e-9 ||
		!joint.CompletionProtected || review != 20*time.Minute ||
		crossing.Horizon != 30*time.Minute {
		t.Fatalf("completion contract was not preserved exactly: plan=%+v projection=%+v joint=%+v review=%s",
			plan, projection, joint, review)
	}
}

func TestReplayJointCompletionContractCannotExtendOpeningAfterCompletion(t *testing.T) {
	at := time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg: gammacapture.MarketMakerConfig{
			MakerFeeBps: 10,
			JointDistanceQuantity: gammacapture.JointDistanceQuantityConfig{
				TwoStageContinuation: true, CrossHorizonContinuation: true,
			},
		},
		inventory: 1, quote: 1_000,
		completionContract: replayCompletionContract{
			active: true, side: types.SideTypeBuy, price: 100, quantity: 1,
			referenceHorizon: 30 * time.Minute,
			until:            at.Add(30 * time.Minute),
		},
		bidOrder: productionReplayOrder{
			active: true, eligible: true, side: types.SideTypeBuy,
			price: 100, quantity: 1, remaining: 1,
			origin: replayQuoteOrigin{AdmissionJointComplementary: true, AskPrice: 103, CompletionHorizon: 30 * time.Minute},
		},
		fillsByDay: make(map[string]*productionReplayDay),
	}
	s.executeCrossedMakerOrdersAtBBO(bboSnapshot{time: at, bid: 99, ask: 100})
	if s.completionContract.active {
		t.Fatalf("a completed contract must not start an endless reverse contract: %+v", s.completionContract)
	}
}

func TestProductionMacroIOCExecutesOnlyOnNextBBOAndUsesItsDepth(t *testing.T) {
	at := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg:      gammacapture.MarketMakerConfig{TakerFeeBps: 10},
		quote:    1_000,
		bidOrder: productionReplayOrder{active: true, side: types.SideTypeBuy, price: 98, remaining: 1},
		askOrder: productionReplayOrder{active: true, side: types.SideTypeSell, price: 102, remaining: 1},
	}
	bar := at.Add(-10 * time.Minute)
	s.scheduleMacroIOC(at, gammacapture.MacroActiveExecutionDecision{
		Trigger: true, Direction: 1, Quantity: 2, WorstPrice: 101,
	}, bar)
	if s.inventory != 0 || !s.pendingMacroIOC.active || s.macroActiveAttempts != 1 {
		t.Fatalf("IOC executed on its decision BBO: %+v", s)
	}
	if !s.bidOrder.active || !s.askOrder.active {
		t.Fatalf("scheduling a partial IOC canceled the surviving maker quotes: %+v", s)
	}
	s.executePendingMacroIOC(bboSnapshot{
		time: at.Add(time.Second), bid: 99.9, bidSize: 1, ask: 100, askSize: 1.5,
	})
	if s.pendingMacroIOC.active || s.macroActiveFills != 1 || s.inventory != 1.5 || s.quote != 850 {
		t.Fatalf("next-BBO IOC did not respect visible depth: inventory=%f quote=%f fills=%d pending=%+v",
			s.inventory, s.quote, s.macroActiveFills, s.pendingMacroIOC)
	}
	if !s.macroInventoryState.LastActiveExecutionBarAt.Equal(bar) || s.takerFees != 0.15 {
		t.Fatalf("IOC cadence/fee accounting mismatch: state=%+v takerFees=%f", s.macroInventoryState, s.takerFees)
	}
	if !s.bidOrder.active || !s.askOrder.active {
		t.Fatalf("IOC execution canceled maker quotes before replacement planning: %+v", s)
	}
}

func TestProductionMacroIOCCancelsWhenNextBBOEscapesLimit(t *testing.T) {
	at := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{cfg: gammacapture.MarketMakerConfig{TakerFeeBps: 10}, quote: 1_000}
	s.scheduleMacroIOC(at, gammacapture.MacroActiveExecutionDecision{
		Trigger: true, Direction: 1, Quantity: 1, WorstPrice: 101,
	}, at.Add(-10*time.Minute))
	s.executePendingMacroIOC(bboSnapshot{
		time: at.Add(time.Second), bid: 101.9, bidSize: 1, ask: 102, askSize: 1,
	})
	if s.pendingMacroIOC.active || s.macroActiveFills != 0 || s.inventory != 0 || s.quote != 1_000 {
		t.Fatalf("market moved beyond IOC limit but replay fabricated a fill: %+v", s)
	}
}

func TestProductionFastTargetIOCExecutesAtNextBBOAndCapsDepth(t *testing.T) {
	at := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		cfg:   gammacapture.MarketMakerConfig{TakerFeeBps: 10},
		quote: 1_000, fillsByDay: make(map[string]*productionReplayDay),
	}
	s.scheduleFastTargetIOC(gammacapture.FastTargetExecutionDecision{
		Trigger: true, Direction: 1, Quantity: 2, WorstPrice: 101,
	}, at)
	if s.inventory != 0 || !s.pendingFastTargetIOC.active || s.fastTargetActiveAttempts != 1 {
		t.Fatalf("Fast IOC executed on decision BBO: %+v", s)
	}
	filled := s.executePendingFastTargetIOC(bboSnapshot{
		time: at.Add(time.Second), bid: 99.9, bidSize: 1, ask: 100, askSize: 1.5,
	})
	if !filled || s.fastTargetActiveFills != 1 || s.inventory != 1.5 || s.quote != 850 {
		t.Fatalf("next-BBO Fast IOC did not respect visible depth: inventory=%f quote=%f fills=%d",
			s.inventory, s.quote, s.fastTargetActiveFills)
	}
	if s.fills != 1 || s.buys != 1 || s.takerFees != 0.15 ||
		!s.lastFastTargetExecutionModelAt.Equal(at) {
		t.Fatalf("Fast IOC accounting mismatch: %+v", s)
	}
}

func TestProductionReplayStopsAtConfiguredDrawdown(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		quote: 94, initialQuote: 100, initialEquity: 100,
		equityPeak: 100, maxDrawdownStopPct: 5,
	}
	s.recordEquity(at, 100, 0, 0, false, false)
	if !s.stopped || !s.stopAt.Equal(at) || s.maximumDrawdownPct != 6 {
		t.Fatalf("drawdown stop did not trigger: stopped=%t at=%s max=%f", s.stopped, s.stopAt, s.maximumDrawdownPct)
	}
}

func TestProductionReplayHoldPnLUsesExactTerminalBBO(t *testing.T) {
	at := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	s := &productionReplayState{
		inventory: 1, quote: 500, initialInventory: 1, initialQuote: 500,
		initialEquity: 600, tradingFrom: at,
	}
	books := []bboSnapshot{
		{time: at, bid: 99, ask: 101},
		{time: at.Add(time.Minute), bid: 109, ask: 111},
	}
	r := s.result(books)
	if math.Abs(r.NetPnLJPY-10) > 1e-12 || math.Abs(r.HoldPnLJPY-10) > 1e-12 {
		t.Fatalf("no-trade strategy and hold must use the same terminal BBO: %+v", r)
	}
}

func TestProductionReplayInitializesEveryConfiguredFastWindow(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{
		FastWindows: []types.Duration{
			types.Duration(10 * time.Minute),
			types.Duration(15 * time.Minute),
			types.Duration(30 * time.Minute),
		},
	}
	s := newProductionReplayState(
		cfg, gammacapture.BarrierConfig{}, gammacapture.IntensityConfig{},
		nil, replayLegacy, "ETHJPY", 1_000, 0, 1, time.Time{}, true)
	for _, window := range []time.Duration{10 * time.Minute, 15 * time.Minute, 30 * time.Minute} {
		if s.fastModels[window] == nil || s.fastEvidenceModels[window] == nil {
			t.Fatalf("adaptive fast window %s was not initialized", window)
		}
	}
}

func TestProductionReplayHonorsLiveBOCPD45Config(t *testing.T) {
	cfg := gammacapture.MarketMakerConfig{
		BOCPD45: gammacapture.BOCPD45Config{Enabled: true, Calibration: "platt"},
	}
	s := newProductionReplayState(
		cfg, gammacapture.BarrierConfig{}, gammacapture.IntensityConfig{},
		nil, replayLegacy, "ETHJPY", 1_000, 0, 1, time.Time{}, true)
	if !s.bocpd45DirectionEnabled || s.bocpd45Calibration == nil ||
		s.bocpd45Calibration.calibrator.method != bocpd45CalibrationPlatt {
		t.Fatalf("replay did not honor live BOCPD45 config: %+v", s)
	}
}

func TestReplayNearFillSidesPreservesApproachingBid(t *testing.T) {
	book := bboSnapshot{bid: 302_172, ask: 302_173}
	bid := productionReplayOrder{active: true, side: types.SideTypeBuy, price: 302_158}
	ask := productionReplayOrder{active: true, side: types.SideTypeSell, price: 303_427}
	plan := gammacapture.MarketMakerQuotePlan{
		AllowBid: true, AllowAsk: true, BidTouchDistanceBps: 15, AskTouchDistanceBps: 15,
	}
	retainBid, retainAsk := replayNearFillSides(bid, ask, book, plan, 26)
	if !retainBid || retainAsk {
		t.Fatalf("unexpected retention: bid=%t ask=%t", retainBid, retainAsk)
	}
}

func TestReplayNearFillSidesPreservesOneSidedApproachingBid(t *testing.T) {
	book := bboSnapshot{bid: 302_172, ask: 302_173}
	bid := productionReplayOrder{active: true, side: types.SideTypeBuy, price: 302_158}
	plan := gammacapture.MarketMakerQuotePlan{
		AllowBid: true, BidPrice: 302_158, AskPrice: 303_427,
		BidTouchDistanceBps: 15, AskTouchDistanceBps: 15,
	}
	retainBid, retainAsk := replayNearFillSides(
		bid, productionReplayOrder{}, book, plan, 26)
	if !retainBid || retainAsk {
		t.Fatalf("unexpected one-sided retention: bid=%t ask=%t", retainBid, retainAsk)
	}
}

func TestMeanFillMarkoutUsesSideSign(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{{time: at, bid: 99, ask: 101}, {time: at.Add(time.Minute), bid: 109, ask: 111}}
	buy := meanFillMarkout([]replayFill{{At: at, Side: types.SideTypeBuy, Price: 100}}, books, time.Minute)
	sell := meanFillMarkout([]replayFill{{At: at, Side: types.SideTypeSell, Price: 100}}, books, time.Minute)
	if buy <= 0 || sell >= 0 {
		t.Fatalf("unexpected markouts buy=%f sell=%f", buy, sell)
	}
}

func TestCompareReplayPoliciesFailsClosedWhenCalibrationMisses(t *testing.T) {
	old := productionReplayResult{FillsPerDay: 4, RoundTripsPerDay: 2}
	current := productionReplayResult{FillsPerDay: 8, RoundTripsPerDay: 4}
	got := compareReplayPolicies(old, current, false)
	want := "calibration failed: do not use synthetic fill comparison to promote or tune live policy"
	if got != want {
		t.Fatalf("unexpected decision: %q", got)
	}
}
