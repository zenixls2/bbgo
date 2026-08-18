package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestQuoteCycleGrossIsIndependentOfTerminalPrice(t *testing.T) {
	got := QuoteCycleGrossBps(100, 101)
	if math.Abs(got-math.Log(1.01)*10_000) > 1e-12 {
		t.Fatalf("gross quote cycle = %.12f", got)
	}
	if got != QuoteCycleGrossBps(100, 101) {
		t.Fatal("gross quote cycle must not depend on terminal mark")
	}
}

func TestQuoteCycleFeeNetUsesExactMultiplicativeFees(t *testing.T) {
	got := QuoteCycleFeeNetBps(100, 101, 10, 10)
	want := math.Log(1.01)*10_000 + 2*math.Log1p(-0.001)*10_000
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("fee-net cycle = %.12f want %.12f", got, want)
	}
}

func TestOneFillTerminalMarkoutUsesExecutableBid(t *testing.T) {
	buy := OneFillTerminalMarkoutBps(true, 100, 102)
	sell := OneFillTerminalMarkoutBps(false, 101, 102)
	if buy <= 0 || sell >= 0 {
		t.Fatalf("unexpected one-fill markouts: buy=%.6f sell=%.6f", buy, sell)
	}
	if OneFillTerminalMarkoutBps(false, 101, 102) == OneFillTerminalMarkoutBps(false, 101, 103) {
		t.Fatal("sell markout must depend on terminal executable bid")
	}
}

func TestQuoteLifecycleKeepWinsWhenContinuationPreservesQueue(t *testing.T) {
	decision := EvaluateQuoteLifecycleAction(QuoteLifecycleActionInput{
		CurrentActive: true, CandidateActive: true,
		CurrentFillProbability: 0.20, CurrentTerminalMarkoutBps: 6,
		CurrentExecutionCostBps: 1, CurrentContinuationValueBps: 7,
		CandidateFillProbability: 0.30, CandidateTerminalMarkoutBps: 6,
		CandidateExecutionCostBps: 1, CandidateContinuationValueBps: 0,
		ReplacementCostBps: 3, DiscountFactor: 1,
	})
	if !decision.Evaluated || decision.Action != QuoteLifecycleKeep {
		t.Fatalf("queue-preserving continuation was not selected: %+v", decision)
	}
	if decision.KeepValueBps <= decision.ReplaceValueBps {
		t.Fatalf("continuation value did not enter Q(KEEP): %+v", decision)
	}
}

func TestQuoteLifecycleReplaceMustPayQueueLoss(t *testing.T) {
	decision := EvaluateQuoteLifecycleAction(QuoteLifecycleActionInput{
		CurrentActive: true, CandidateActive: true,
		CurrentFillProbability: 0.20, CurrentTerminalMarkoutBps: 4,
		CurrentExecutionCostBps: 1, CurrentContinuationValueBps: 0,
		CandidateFillProbability: 0.60, CandidateTerminalMarkoutBps: 8,
		CandidateExecutionCostBps: 1, CandidateContinuationValueBps: 0,
		ReplacementCostBps: 2, DiscountFactor: 1,
	})
	if decision.Action != QuoteLifecycleReplace {
		t.Fatalf("higher value replacement was not selected: %+v", decision)
	}
	withoutCost := EvaluateQuoteLifecycleAction(QuoteLifecycleActionInput{
		CurrentActive: true, CandidateActive: true,
		CurrentFillProbability: 0.20, CurrentTerminalMarkoutBps: 4,
		CandidateFillProbability: 0.60, CandidateTerminalMarkoutBps: 8,
		CandidateExecutionCostBps: 1, ReplacementCostBps: 0, DiscountFactor: 1,
	})
	if withoutCost.ReplaceValueBps <= decision.ReplaceValueBps {
		t.Fatal("replacement queue cost was not charged")
	}
}

func TestQuoteLifecycleCancelWinsWhenBothActionsAreNegative(t *testing.T) {
	decision := EvaluateQuoteLifecycleAction(QuoteLifecycleActionInput{
		CurrentActive: true, CandidateActive: true,
		CurrentFillProbability: 0.4, CurrentTerminalMarkoutBps: -8,
		CurrentExecutionCostBps: 2, CurrentRiskPenaltyBps: 1,
		CandidateFillProbability: 0.3, CandidateTerminalMarkoutBps: -6,
		CandidateExecutionCostBps: 2, CandidateRiskPenaltyBps: 1,
		ReplacementCostBps: 1, DiscountFactor: 1,
	})
	if decision.Action != QuoteLifecycleCancel || decision.SelectedValueBps != 0 {
		t.Fatalf("negative action values must cancel: %+v", decision)
	}
}

func TestQuoteLifecycleRejectsNonCausalInputs(t *testing.T) {
	decision := EvaluateQuoteLifecycleAction(QuoteLifecycleActionInput{
		CurrentActive: true, CandidateActive: true,
		CurrentFillProbability: 1.1, DiscountFactor: 1,
	})
	if decision.Evaluated || decision.Action != QuoteLifecycleCancel {
		t.Fatalf("invalid probability was accepted: %+v", decision)
	}
}

func TestEvaluateQuoteLifecycleFromHorizonsUsesCycleEdgeAndContinuationSeparately(t *testing.T) {
	current := MarketMakerHorizonDecision{
		Horizon:              10 * time.Minute,
		BothTouchProbability: 0.4,
		NetRoundTripEdgeBps:  2,
		ScoreBpsPerHour:      6,
	}
	candidate := current
	candidate.NetRoundTripEdgeBps = 3
	candidate.ScoreBpsPerHour = 9

	decision := EvaluateQuoteLifecycleFromHorizons(
		current, candidate, true, true,
		QuoteLifecycleActionConfig{DiscountFactor: 1, ReplacementCostBps: 0.1})
	if !decision.Evaluated {
		t.Fatalf("expected evaluated decision: %+v", decision)
	}
	if decision.Action != QuoteLifecycleReplace {
		t.Fatalf("expected candidate replacement, got %+v", decision)
	}
	// The conditional payoff is p*edge; the score contributes only to the
	// no-fill continuation term, so this is not p*(score*horizon).
	wantKeep := 0.4*2 + 0.6*1
	wantReplace := 0.4*3 + 0.6*1.5 - 0.1
	if math.Abs(decision.KeepValueBps-wantKeep) > 1e-9 {
		t.Fatalf("keep value = %.12f, want %.12f", decision.KeepValueBps, wantKeep)
	}
	if math.Abs(decision.ReplaceValueBps-wantReplace) > 1e-9 {
		t.Fatalf("replace value = %.12f, want %.12f", decision.ReplaceValueBps, wantReplace)
	}
}

func TestQuoteLifecycleActionUsesUncertaintyMargin(t *testing.T) {
	withoutMargin := EvaluateQuoteLifecycleAction(QuoteLifecycleActionInput{
		CurrentActive: true, CandidateActive: true,
		CurrentFillProbability: 0.5, CurrentTerminalMarkoutBps: 4,
		CandidateFillProbability: 0.6, CandidateTerminalMarkoutBps: 5,
		ReplacementCostBps: 0,
		DiscountFactor:     1,
	})
	withMargin := EvaluateQuoteLifecycleAction(QuoteLifecycleActionInput{
		CurrentActive: true, CandidateActive: true,
		CurrentFillProbability: 0.5, CurrentTerminalMarkoutBps: 4,
		CandidateFillProbability: 0.6, CandidateTerminalMarkoutBps: 5,
		CandidateFillProbabilityStdError: 0.2, ActionMarginBps: 0.5,
		ConfidenceZScore: 1.645, ReplacementCostBps: 0, DiscountFactor: 1,
	})
	if withoutMargin.Action != QuoteLifecycleReplace {
		t.Fatalf("point estimate should replace: %+v", withoutMargin)
	}
	if withMargin.Action == QuoteLifecycleReplace {
		t.Fatalf("uncertainty margin should reject weak replacement: %+v", withMargin)
	}
}

func TestEstimateQuoteLifecycleReplacementCostIsDynamic(t *testing.T) {
	cfg := QuoteLifecycleActionConfig{
		DynamicReplacementCost: true, ReplacementCostQueueWeight: 1,
		ReplacementCostDriftWeight: 1, ReplacementCostVolatilityWeight: 1,
		ReplacementCostLatencySeconds: 4,
	}
	got := EstimateQuoteLifecycleReplacementCostBps(QuoteLifecycleReplacementCostInput{
		BaseCostBps: 1, CurrentFillProbability: 0.5, CurrentAge: 5 * time.Minute,
		Horizon: 10 * time.Minute, CurrentTerminalMarkoutBps: 4,
		BBOStalenessBps: 2, VolatilityBpsPerSqrtSec: 1,
	}, cfg)
	if got.CostBps <= 1 || got.StdErrorBps <= 0 {
		t.Fatalf("dynamic replacement cost did not respond to observables: %+v", got)
	}
}
