package gammacapture

import "testing"

func TestEvaluateNormalFlowPressureShrinksAndBoundsSignal(t *testing.T) {
	decision := EvaluateNormalFlowPressure(NormalFlowPressureConfig{Enabled: true}, NormalFlowPressureInput{
		SignedTradeImbalance: 0.8, TradeCount: 20,
	})
	if !decision.Ready || !decision.Applied || decision.Signal <= 0 || decision.Signal > 0.35 {
		t.Fatalf("expected bounded positive flow pressure, got %+v", decision)
	}
	if decision.EvidenceWeight >= 1 || decision.ShrunkImbalance >= 0.8 {
		t.Fatalf("sparse evidence was not shrunk: %+v", decision)
	}
}

func TestEvaluateNormalFlowPressureRequiresEvidence(t *testing.T) {
	for _, input := range []NormalFlowPressureInput{
		{SignedTradeImbalance: 0.8, TradeCount: 19},
		{SignedTradeImbalance: 0.01, TradeCount: 100},
	} {
		decision := EvaluateNormalFlowPressure(NormalFlowPressureConfig{Enabled: true}, input)
		if decision.Ready || decision.Applied || decision.Signal != 0 {
			t.Fatalf("insufficient flow evidence must fail closed: input=%+v decision=%+v", input, decision)
		}
	}
}

func TestEvaluateNormalFlowPressurePreservesSign(t *testing.T) {
	positive := EvaluateNormalFlowPressure(NormalFlowPressureConfig{Enabled: true}, NormalFlowPressureInput{
		SignedTradeImbalance: 0.4, TradeCount: 100,
	})
	negative := EvaluateNormalFlowPressure(NormalFlowPressureConfig{Enabled: true}, NormalFlowPressureInput{
		SignedTradeImbalance: -0.4, TradeCount: 100,
	})
	if positive.Signal <= 0 || negative.Signal >= 0 || positive.Signal != -negative.Signal {
		t.Fatalf("flow pressure must be side symmetric: positive=%+v negative=%+v", positive, negative)
	}
}
