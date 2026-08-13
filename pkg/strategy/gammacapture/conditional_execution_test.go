package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestConditionalExecutionStateIsCausalAndSideSpecific(t *testing.T) {
	start := time.Unix(1_000, 0)
	points := make([]MarketMakerHorizonPoint, 0, 16)
	for second := 0; second < 16; second++ {
		// Ask first rises and then rebounds from its short low; bid follows a
		// deliberately different path so the two executable-side QVs differ.
		ask := 100.20 + .03*float64(second)
		if second >= 8 {
			ask = 100.44 - .02*float64(second-8)
		}
		bid := 100.00 + .005*float64(second)
		points = append(points, MarketMakerHorizonPoint{
			At: start.Add(time.Duration(second) * time.Second), Bid: bid, Ask: ask,
		})
	}
	horizon := 10 * time.Second
	prefix := buildConditionalExecutionStates(points[:13], horizon)
	full := buildConditionalExecutionStates(points, horizon)
	got, want := full[12], prefix[12]
	if !got.Valid || got != want {
		t.Fatalf("future observations changed causal state: got=%+v want=%+v", got, want)
	}
	if got.BuyQVBps <= got.SellQVBps || got.BuyDrawdownBps <= 0 {
		t.Fatalf("ask and bid paths were collapsed: %+v", got)
	}
}

func TestConditionalExposureCacheStartupMatchesIncremental(t *testing.T) {
	start := time.Unix(2_000, 0)
	cfg := MarketMakerConfig{
		HorizonLookback:  types.Duration(2 * time.Minute),
		MaxTradingWindow: types.Duration(10 * time.Second),
	}
	observe := func(model *MarketMakerHorizonModel, from, to int) {
		for second := from; second < to; second++ {
			mid := 100 + .08*math.Sin(float64(second)/4)
			model.ObserveBookWithGap(
				start.Add(time.Duration(second)*time.Second), mid-.02, mid+.03, cfg, false)
		}
	}
	horizon := 10 * time.Second
	var startup, incremental MarketMakerHorizonModel
	observe(&startup, 0, 50)
	want := startup.crossingExposures(horizon)
	observe(&incremental, 0, 30)
	_ = incremental.crossingExposures(horizon)
	observe(&incremental, 30, 50)
	got := incremental.crossingExposures(horizon)
	if len(got) != len(want) {
		t.Fatalf("exposure count mismatch: incremental=%d startup=%d", len(got), len(want))
	}
	for index := range want {
		gotState, wantState := got[index].ConditionalState, want[index].ConditionalState
		close := func(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }
		if gotState.Valid != wantState.Valid ||
			!close(gotState.BuyDrawdownBps, wantState.BuyDrawdownBps) ||
			!close(gotState.BuyRebound30Bps, wantState.BuyRebound30Bps) ||
			!close(gotState.BuyQVBps, wantState.BuyQVBps) ||
			!close(gotState.SellRunupBps, wantState.SellRunupBps) ||
			!close(gotState.SellReversal30Bps, wantState.SellReversal30Bps) ||
			!close(gotState.SellQVBps, wantState.SellQVBps) ||
			!close(gotState.SpreadBps, wantState.SpreadBps) {
			t.Fatalf("conditional state mismatch at %d: got=%+v want=%+v",
				index, gotState, wantState)
		}
	}
}

func TestConditionalExecutionPairedValueIsBuySellSymmetric(t *testing.T) {
	horizon := 10 * time.Minute
	start := time.Unix(10_000, 0)
	state := conditionalExecutionState{Valid: true, BuyQVBps: 5, SellQVBps: 5, SpreadBps: 4}
	decision := func(buy bool) ConditionalExecutionSideDecision {
		exposures := make([]marketMakerHorizonExposure, 30)
		for index := range exposures {
			exposure := marketMakerHorizonExposure{
				At:       start.Add(time.Duration(index) * time.Minute),
				EndAt:    start.Add(time.Duration(index)*time.Minute + horizon),
				StartBid: 100, StartAsk: 100.04,
				ConditionalState: state, NextMinute: index + 1,
			}
			if buy {
				exposure.BuyExcursionBps = 15
				quote := exposure.StartAsk * math.Exp(-10.0/10_000)
				exposure.TerminalBid = quote * math.Exp(30.0/10_000)
				exposure.TerminalAsk = exposure.TerminalBid * math.Exp(1.0/10_000)
			} else {
				exposure.SellExcursionBps = 15
				quote := exposure.StartBid * math.Exp(10.0/10_000)
				exposure.TerminalAsk = quote * math.Exp(-30.0/10_000)
				exposure.TerminalBid = exposure.TerminalAsk * math.Exp(-1.0/10_000)
			}
			exposures[index] = exposure
		}
		lastPointAt := start.Add(time.Hour)
		model := MarketMakerHorizonModel{
			points: []MarketMakerHorizonPoint{{At: lastPointAt, Bid: 100, Ask: 100.04}},
			crossingExposureCaches: map[time.Duration]*marketMakerHorizonExposureCache{
				horizon: {Initialized: true, LastPointAt: lastPointAt, Exposures: exposures},
			},
		}
		return model.conditionalExecutionSideDecision(
			start.Add(time.Hour), MarketMakerConfig{
				HorizonLookback: types.Duration(6 * time.Hour), MakerFeeBps: 10, AdverseSelectionBps: 2,
			}, horizon, state, buy, 20, 10)
	}
	buy, sell := decision(true), decision(false)
	if !buy.Evaluated || !sell.Evaluated ||
		buy.ExpectedPairedDeltaBps-buy.PairedStdErrorBps <= 0 ||
		sell.ExpectedPairedDeltaBps-sell.PairedStdErrorBps <= 0 {
		t.Fatalf("synthetic supported inward action was rejected: buy=%+v sell=%+v", buy, sell)
	}
	if math.Abs(buy.ExpectedPairedDeltaBps-sell.ExpectedPairedDeltaBps) > 1e-9 {
		t.Fatalf("reflected BUY/SELL payoffs differ: buy=%+v sell=%+v", buy, sell)
	}
}
