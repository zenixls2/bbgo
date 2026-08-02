package gammacapture

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestIntensityModelUsesConfiguredVolatilityWindowForSparseEvents(t *testing.T) {
	now := time.Unix(100, 0)
	m := NewIntensityModel(IntensityConfig{
		Window:           types.Duration(time.Minute),
		VolatilityWindow: types.Duration(time.Minute),
		MinEvents:        1,
	})
	m.Update(CrossingEvent{Direction: DirectionUp, BarrierWidth: 0.001, ExchangeTime: now})
	snapshot := m.Snapshot(now.Add(time.Second))
	require.InDelta(t, math.Sqrt(1.0/60.0)*0.001, snapshot.GammaCaptureVolatility, 1e-12)
}

func TestCrossingEngineCountsEveryBarrierWithoutRevision(t *testing.T) {
	e := NewCrossingEngine(0.01, 0, 8)
	t0 := time.Unix(1, 0)
	require.Empty(t, e.Update("BTCJPY", fixedpoint.NewFromInt(100), t0, t0, 0))
	events := e.Update("BTCJPY", fixedpoint.NewFromFloat(103.1), t0.Add(time.Second), t0.Add(time.Second), 0)
	require.Len(t, events, 3)
	require.Equal(t, int64(0), events[0].FromState)
	require.Equal(t, int64(3), events[2].ToState)
	// Repeated observations at the same level never create a crossing.
	require.Empty(t, e.Update("BTCJPY", fixedpoint.NewFromFloat(103.1), t0.Add(2*time.Second), t0.Add(2*time.Second), 0))
}

func TestCrossingEngineCapsGapAndResetsUncertainPath(t *testing.T) {
	e := NewCrossingEngine(0.01, 0, 2)
	t0 := time.Unix(1, 0)
	e.Update("BTCJPY", fixedpoint.NewFromInt(100), t0, t0, 0)
	events := e.Update("BTCJPY", fixedpoint.NewFromInt(110), t0.Add(time.Second), t0.Add(time.Second), 0)
	require.Len(t, events, 2)
	require.True(t, events[0].GapAffected)
	require.Equal(t, int64(0), e.State)
}

func TestFirstPassageIsBoundedAndSymmetric(t *testing.T) {
	p := TPBeforeSL(0.5, 0.5, time.Minute, 2, 2, 0)
	require.InDelta(t, 1, p.TP+p.SL+p.Unresolved, 1e-9)
	require.InDelta(t, p.TP, p.SL, 1e-9)
	require.Greater(t, p.Unresolved, 0.0)
}

func TestGapEventsDoNotEstablishModelHealth(t *testing.T) {
	m := NewIntensityModel(IntensityConfig{Window: types.Duration(time.Hour), MinEvents: 2})
	now := time.Unix(1, 0)
	m.Update(CrossingEvent{Direction: DirectionUp, ExchangeTime: now, GapAffected: true})
	m.Update(CrossingEvent{Direction: DirectionDown, ExchangeTime: now.Add(time.Second), GapAffected: true})
	m.Update(CrossingEvent{Direction: DirectionUp, ExchangeTime: now.Add(2 * time.Second)})
	require.Equal(t, HealthInsufficient, m.Snapshot(now.Add(2*time.Second)).Health)
	m.Update(CrossingEvent{Direction: DirectionDown, ExchangeTime: now.Add(3 * time.Second)})
	m.Update(CrossingEvent{Direction: DirectionUp, ExchangeTime: now.Add(4 * time.Second)})
	m.Update(CrossingEvent{Direction: DirectionDown, ExchangeTime: now.Add(5 * time.Second)})
	require.Equal(t, HealthHealthy, m.Snapshot(now.Add(5*time.Second)).Health)
}

func TestFirstPassageWeightsAbsorptionAtTheCorrectJumpCount(t *testing.T) {
	// With one barrier on either side, the first jump absorbs the process. The
	// closed form is P(TP before t) = lambdaUp/(lambdaUp+lambdaDown) *
	// (1 - exp(-(lambdaUp+lambdaDown)t)).
	p := TPBeforeSL(.5, .5, time.Second, 1, 1, 0)
	expectedTP := .5 * (1 - math.Exp(-1))
	require.InDelta(t, expectedTP, p.TP, 1e-12)
	require.InDelta(t, expectedTP, p.SL, 1e-12)
	require.InDelta(t, math.Exp(-1), p.Unresolved, 1e-12)
}

func TestSkellamPMFNormalizesOverPracticalSupport(t *testing.T) {
	sum := 0.0
	for k := -30; k <= 30; k++ {
		sum += SkellamPMF(k, 2, 3)
	}
	require.InDelta(t, 1, sum, 1e-8)
	require.False(t, math.IsNaN(SkellamPMF(0, 2, 3)))
}

func TestSignalRequiresResetBeforeAnotherUpcrossing(t *testing.T) {
	var s SignalState
	require.Equal(t, SignalNone, s.Update(.3, .4, .6))
	require.Equal(t, SignalUp, s.Update(.7, .4, .6))
	require.Equal(t, SignalNone, s.Update(.8, .4, .6))
	require.Equal(t, SignalDown, s.Update(.4, .4, .6))
	require.Equal(t, SignalUp, s.Update(.6, .4, .6))
	require.Equal(t, 2, s.Upcrossings)
}

func TestSignalChurnIsScopedToTheCurrentCycle(t *testing.T) {
	var s SignalState
	require.Equal(t, SignalNone, s.Update(.3, .4, .6))
	require.Equal(t, SignalUp, s.Update(.7, .4, .6))
	require.Equal(t, 1, s.Churn())
	s.ResetCycle()
	require.Zero(t, s.Churn())
	require.Equal(t, 1, s.Upcrossings)
}

func TestHealthScopedSignalDoesNotConsumeWarmupUpcrossing(t *testing.T) {
	s := &Strategy{Config: Config{Signal: SignalConfig{LowerThreshold: .4, UpperThreshold: .6}}, State: &State{}}
	_, signal := s.updateHealthScopedSignal(HealthInsufficient, .3)
	require.Equal(t, SignalNone, signal)
	raw, signal := s.updateHealthScopedSignal(HealthInsufficient, .7)
	require.Equal(t, SignalUp, raw)
	require.Equal(t, SignalNone, signal)

	// Entering HEALTHY at an already-high probability must not manufacture an
	// entry. A new healthy low-to-high transition is required.
	_, signal = s.updateHealthScopedSignal(HealthHealthy, .7)
	require.Equal(t, SignalNone, signal)
	_, signal = s.updateHealthScopedSignal(HealthHealthy, .3)
	require.Equal(t, SignalDown, signal)
	_, signal = s.updateHealthScopedSignal(HealthHealthy, .7)
	require.Equal(t, SignalUp, signal)
}

func TestLiveConfigurationIsAllowed(t *testing.T) {
	s := Config{Environment: "live", Symbol: "BTCJPY"}
	require.NoError(t, s.Validate())
}

func TestResearchForceEntryIsReplayOnly(t *testing.T) {
	require.Error(t, (&Config{Environment: "paper", Symbol: "BTCJPY", ResearchForceEntry: true}).Validate())
	require.NoError(t, (&Config{Environment: "replay", Symbol: "BTCJPY", ResearchForceEntry: true}).Validate())
}

func TestReferencePriceDefaultsToTheAvailableCausalFeed(t *testing.T) {
	backtest := Config{Environment: "backtest", Symbol: "BTCJPY"}
	require.NoError(t, backtest.Validate())
	require.Equal(t, "klineClose", backtest.ReferencePrice.Mode)
	require.False(t, (&Strategy{Config: backtest}).usesMarketTradeReference())

	paper := Config{Environment: "paper", Symbol: "BTCJPY"}
	require.NoError(t, paper.Validate())
	require.Equal(t, "lastTrade", paper.ReferencePrice.Mode)
	require.True(t, (&Strategy{Config: paper}).usesMarketTradeReference())
}

func TestBacktestRejectsUnreplayedMarketTradeReference(t *testing.T) {
	c := Config{Environment: "backtest", Symbol: "BTCJPY", ReferencePrice: ReferencePriceConfig{Mode: "lastTrade"}}
	require.Error(t, c.Validate())
	microprice := Config{Environment: "backtest", Symbol: "BTCJPY", ReferencePrice: ReferencePriceConfig{Mode: "microprice"}}
	require.Error(t, microprice.Validate())
	paperMicroprice := Config{Environment: "paper", Symbol: "BTCJPY", ReferencePrice: ReferencePriceConfig{Mode: "microprice"}}
	require.NoError(t, paperMicroprice.Validate())
}

func TestMarketTradeReferenceRejectsDuplicateTradeID(t *testing.T) {
	now := time.Unix(1, 0)
	s := &Strategy{
		Config: Config{
			Symbol:         "BTCJPY",
			Barrier:        BarrierConfig{Width: .001, MaxCrossingsPerEvent: 1},
			Intensity:      IntensityConfig{Window: types.Duration(time.Hour), MinEvents: 10},
			Horizon:        HorizonConfig{Prediction: types.Duration(15 * time.Minute)},
			Signal:         SignalConfig{LowerThreshold: .4, UpperThreshold: .6, EntryProbability: .62},
			TakeProfit:     TakeProfitConfig{InitialTargetBarriers: 6},
			StopLoss:       StopLossConfig{HardStopBarriers: 3},
			ReferencePrice: ReferencePriceConfig{Mode: "lastTrade"},
		},
		State:     &State{Engine: NewCrossingEngine(.001, 0, 1)},
		GateStats: &GateStats{},
		Position:  types.NewPositionFromMarket(types.Market{Symbol: "BTCJPY", BaseCurrency: "BTC", QuoteCurrency: "JPY"}),
	}
	s.model = NewIntensityModel(s.Intensity)
	s.Status = types.StrategyStatusRunning
	trade := types.Trade{ID: 42, Symbol: "BTCJPY", Price: fixedpoint.NewFromInt(100000), Time: types.Time(now)}
	s.onMarketTradeReference(context.Background(), trade)
	s.onMarketTradeReference(context.Background(), trade)

	require.Equal(t, 1, s.GateStats.FlatObservations)
	require.Equal(t, uint64(42), s.State.LastMarketTradeID)
}

func TestPaperEntryRequiresFreshTightBook(t *testing.T) {
	s := &Strategy{Config: Config{
		Symbol:         "BTCJPY",
		ReferencePrice: ReferencePriceConfig{Mode: "lastTrade"},
		Risk:           RiskConfig{MaxSpreadBps: 10, MaxBookAge: types.Duration(5 * time.Second)},
	}}
	require.False(t, s.entryBookPasses())

	s.onBookTicker(types.BookTicker{Symbol: "BTCJPY", Buy: fixedpoint.NewFromInt(100000), Sell: fixedpoint.NewFromInt(100050)})
	require.True(t, s.entryBookPasses())

	s.onBookTicker(types.BookTicker{Symbol: "BTCJPY", Buy: fixedpoint.NewFromInt(100000), Sell: fixedpoint.NewFromInt(100200)})
	require.False(t, s.entryBookPasses())

	s.bestBookAt = time.Now().Add(-6 * time.Second)
	require.False(t, s.entryBookPasses())
}

func TestMicropriceUsesOppositeSideLiquidity(t *testing.T) {
	price, ok := microprice(types.BookTicker{
		Buy:      fixedpoint.NewFromInt(100),
		BuySize:  fixedpoint.NewFromInt(3),
		Sell:     fixedpoint.NewFromInt(102),
		SellSize: fixedpoint.NewFromInt(1),
	})
	require.True(t, ok)
	require.Equal(t, "101.5", price.String())

	_, ok = microprice(types.BookTicker{Buy: fixedpoint.NewFromInt(100), Sell: fixedpoint.NewFromInt(101)})
	require.False(t, ok)
}

func TestMinimumTakeProfitBarriersCoversCosts(t *testing.T) {
	s := &Strategy{Config: Config{
		Barrier:    BarrierConfig{Width: .001},
		TakeProfit: TakeProfitConfig{ActivationBarriers: 2},
		Risk:       RiskConfig{EstimatedCostBps: 25, MinimumNetEdgeBps: 15},
	}}
	require.Equal(t, int64(4), s.minimumTakeProfitBarriers())
}

func TestExpectedNetEdgeBpsDoesNotCreditUnresolvedPaths(t *testing.T) {
	s := &Strategy{Config: Config{
		Barrier:    BarrierConfig{Width: .001},
		TakeProfit: TakeProfitConfig{InitialTargetBarriers: 6},
		StopLoss:   StopLossConfig{HardStopBarriers: 3},
		Risk:       RiskConfig{EstimatedCostBps: 25},
	}}
	// 0.6 * 60 bps - 0.1 * 30 bps - 25 bps = 8 bps. The unresolved
	// 0.3 deliberately receives no positive contribution.
	require.InDelta(t, 8, s.expectedNetEdgeBps(FirstPassage{TP: .6, SL: .1, Unresolved: .3}), 1e-12)
}

func TestAdaptiveBarrierSelectionPersistsAValidPlan(t *testing.T) {
	s := &Strategy{Config: Config{
		Barrier:    BarrierConfig{Width: .001},
		Horizon:    HorizonConfig{Prediction: types.Duration(time.Hour)},
		Signal:     SignalConfig{EntryProbability: .50},
		TakeProfit: TakeProfitConfig{InitialTargetBarriers: 6},
		StopLoss:   StopLossConfig{HardStopBarriers: 3, SoftStopBarriers: 2},
		BarrierSelection: BarrierSelectionConfig{
			Enabled: true, MinTargetBarriers: 2, MaxTargetBarriers: 4,
			MinStopBarriers: 2, MaxStopBarriers: 3,
		},
		Risk: RiskConfig{EstimatedCostBps: 1},
	}}
	plan := s.selectBarrierPlan(ModelSnapshot{LambdaUp: .01, LambdaDown: .001}, FirstPassage{})
	require.GreaterOrEqual(t, plan.Target, 2)
	require.LessOrEqual(t, plan.Target, 4)
	require.GreaterOrEqual(t, plan.HardStop, 2)
	require.LessOrEqual(t, plan.HardStop, 3)
	require.GreaterOrEqual(t, plan.SoftStop, 1)
	require.Less(t, plan.SoftStop, plan.HardStop)
	require.NotEqual(t, math.Inf(-1), plan.EdgeBps)
}

func TestTargetRangeCoversRoundTripCosts(t *testing.T) {
	s := &Strategy{Config: Config{
		Barrier: BarrierConfig{Width: .001},
		Risk:    RiskConfig{EstimatedCostBps: 25, MinimumNetEdgeBps: 15},
	}}
	require.False(t, s.rangePasses(3)) // 30 bps does not cover the 40 bps floor.
	require.True(t, s.rangePasses(4))
}

func TestFeedbackRecordsFeeAdjustedExit(t *testing.T) {
	s := &Strategy{
		Config: Config{Risk: RiskConfig{EstimatedCostBps: 25}},
		State: &State{
			EntryPrice:       fixedpoint.NewFromInt(100000),
			EntryPredictedTP: .65,
		},
	}
	s.recordFeedback("trailing take profit", fixedpoint.NewFromInt(100500))
	require.Equal(t, 1, s.State.Feedback.ResolvedExits)
	require.Equal(t, 1, s.State.Feedback.PositiveNetExits)
	require.InDelta(t, .65, s.State.Feedback.PredictedTPSum, 1e-12)
	require.InDelta(t, 24.8754151104, s.State.Feedback.RealizedNetBpsSum, 1e-9)
}

func TestSoftStopMustBeTighterThanHardStop(t *testing.T) {
	require.Error(t, (&Config{Environment: "backtest", Symbol: "BTCJPY", StopLoss: StopLossConfig{HardStopBarriers: 3, SoftStopBarriers: 3}}).Validate())
}

func TestEntryBarIsStable(t *testing.T) {
	s := &Strategy{Config: Config{Risk: RiskConfig{MaxEntryBarRangeBps: 25, MaxEntryBarReturnBps: 15}}}
	require.True(t, s.entryBarIsStable(types.KLine{
		Open: fixedpoint.NewFromInt(100000), Close: fixedpoint.NewFromInt(100100), Low: fixedpoint.NewFromInt(99990), High: fixedpoint.NewFromInt(100120),
	}))
	require.False(t, s.entryBarIsStable(types.KLine{
		Open: fixedpoint.NewFromInt(100000), Close: fixedpoint.NewFromInt(100300), Low: fixedpoint.NewFromInt(99980), High: fixedpoint.NewFromInt(100320),
	}))
	require.False(t, s.entryBarIsStable(types.KLine{
		Open: fixedpoint.NewFromInt(100000), Close: fixedpoint.NewFromInt(100100), Low: fixedpoint.NewFromInt(99800), High: fixedpoint.NewFromInt(100200),
	}))
}

func TestStretchedSignalRequiresRetrace(t *testing.T) {
	s := &Strategy{
		Config: Config{Signal: SignalConfig{StretchedSignalRetraceBps: 10}},
		State:  &State{EntrySignalPrice: fixedpoint.NewFromInt(100000), EntryNeedsRetrace: true},
	}
	require.False(t, s.entryRetraceConfirmed(fixedpoint.NewFromInt(99950)))
	require.True(t, s.entryRetraceConfirmed(fixedpoint.NewFromInt(99900)))
}

func TestStretchedSignalRetraceIsExplicitOptIn(t *testing.T) {
	c := &Config{Environment: "backtest", Symbol: "BTCJPY"}
	require.NoError(t, c.Validate())
	require.Zero(t, c.Signal.StretchedSignalRetraceBps)
}

func TestPositionExitUsesPricesNotResetGrid(t *testing.T) {
	s := &Strategy{
		Config: Config{
			Barrier:    BarrierConfig{Width: .001},
			Horizon:    HorizonConfig{MaximumHolding: types.Duration(time.Hour)},
			Signal:     SignalConfig{ExitProbability: .42, MaxChurn: 4},
			TakeProfit: TakeProfitConfig{ActivationBarriers: 2, MinTrailingBarriers: 1},
			StopLoss:   StopLossConfig{HardStopBarriers: 2, SoftStopBarriers: 1},
			Risk:       RiskConfig{EstimatedCostBps: 25, MinimumNetEdgeBps: 15, SoftExitMinHolding: types.Duration(time.Minute)},
		},
		State: &State{
			EntryGrid:      3, // A gap reset can make the engine's state zero.
			EntryPrice:     fixedpoint.NewFromInt(100000),
			HighWaterPrice: fixedpoint.NewFromInt(100000),
			EnteredAt:      time.Unix(0, 0),
		},
		Position: types.NewPositionFromMarket(types.Market{Symbol: "BTCJPY", BaseCurrency: "BTC", QuoteCurrency: "JPY"}),
	}
	// This is a profitable price despite an implied grid movement of -3 after
	// reset. It must not trigger a synthetic hard stop.
	require.Empty(t, s.positionExitReason(time.Unix(0, 0).Add(2*time.Minute), fixedpoint.NewFromInt(100500), FirstPassage{TP: .7}, SignalNone))
	require.Equal(t, "hard stop", s.positionExitReason(time.Unix(0, 0).Add(2*time.Minute), fixedpoint.NewFromInt(99700), FirstPassage{TP: .7}, SignalNone))
}

func TestTrendGateUsesOnlySamplesAtOrBeforeLookback(t *testing.T) {
	s := &Strategy{
		Config: Config{Trend: TrendConfig{Lookback: types.Duration(5 * time.Minute), MinReturnBps: 10}},
		State:  &State{},
	}
	t0 := time.Unix(0, 0)
	s.observeTrend(t0, fixedpoint.NewFromInt(100000))
	s.observeTrend(t0.Add(4*time.Minute), fixedpoint.NewFromInt(100500))
	// The newer observation is within the lookback and must not be used as
	// the reference. At t0+5m, the only eligible reference is 100000.
	require.True(t, s.trendPasses(t0.Add(5*time.Minute), fixedpoint.NewFromInt(100200)))
	require.False(t, s.trendPasses(t0.Add(5*time.Minute), fixedpoint.NewFromInt(100050)))
}

func TestBacktestRuntimeStateIsReset(t *testing.T) {
	s := &Strategy{
		Config:    Config{Environment: "backtest", Barrier: BarrierConfig{Width: .001, MaxCrossingsPerEvent: 8}},
		State:     &State{Engine: NewCrossingEngine(.01, 0, 1), CooldownUntil: time.Now().Add(time.Hour)},
		GateStats: &GateStats{Entries: 2},
	}
	market := types.Market{Symbol: "BTCJPY", BaseCurrency: "BTC", QuoteCurrency: "JPY"}
	require.True(t, s.resetDeterministicRuntimeState(market))
	require.Equal(t, int64(0), s.State.Engine.State)
	require.True(t, s.State.CooldownUntil.IsZero())
	require.Zero(t, s.GateStats.Entries)
	require.True(t, s.Position.GetBase().IsZero())
}

func TestIntensityModelTracksObservationExposureWithoutCrossings(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	model := NewIntensityModel(IntensityConfig{
		Window:       types.Duration(6 * time.Hour),
		PriorAlphaUp: 1, PriorBetaUp: 60,
		PriorAlphaDown: 1, PriorBetaDown: 60,
		MinEvents: 10,
	})
	for offset := 8 * time.Hour; offset >= 0; offset -= 2 * time.Hour {
		model.Observe(now.Add(-offset), false)
	}

	got := model.Snapshot(now)
	if got.Observed != 6*time.Hour {
		t.Fatalf("observation exposure should cap at configured window: %+v", got)
	}
	wantTotal := 2.0 / (60.0 + (6 * time.Hour).Seconds())
	if math.Abs(got.Total-wantTotal) > 1e-12 {
		t.Fatalf("zero-crossing posterior must use observed market-data exposure: got=%g want=%g", got.Total, wantTotal)
	}
	if got.Health != HealthInsufficient {
		t.Fatalf("activity health remains count-based: %+v", got)
	}
}

func TestIntensityModelGapResetsObservationExposure(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	model := NewIntensityModel(IntensityConfig{Window: types.Duration(30 * time.Minute)})
	model.Observe(now.Add(-20*time.Minute), false)
	model.Observe(now.Add(-10*time.Minute), true)
	model.Observe(now, false)

	if got := model.Snapshot(now).Observed; got != 10*time.Minute {
		t.Fatalf("post-gap exposure mismatch: got=%s want=10m", got)
	}
}
