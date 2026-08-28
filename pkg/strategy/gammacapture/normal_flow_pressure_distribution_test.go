package gammacapture

import (
	"math"
	"testing"
)

func TestNormalFlowPressureDistributionVariantsAreBounded(t *testing.T) {
	variants := []NormalFlowPressureDistributionVariant{
		NormalFlowPressureDistributionCurrent,
		NormalFlowPressureDistributionWinsorized,
		NormalFlowPressureDistributionRobustTanh,
		NormalFlowPressureDistributionBalancedRank,
	}
	for _, variant := range variants {
		model := NewNormalFlowPressureDistributionModel(NormalFlowPressureDistributionConfig{
			Variant: variant, MinTrades: 1, MinAbsImbalance: 0,
		})
		for _, raw := range []float64{-1, -.8, -.1, 0, .1, .8, 1} {
			decision := model.Evaluate(raw, 10)
			if math.IsNaN(decision.Signal) || math.IsInf(decision.Signal, 0) || math.Abs(decision.Signal) > .35 {
				t.Fatalf("variant=%s emitted invalid bounded signal: %+v", variant, decision)
			}
			model.Observe(raw)
		}
	}
}

func TestNormalFlowPressureDistributionIsPrequential(t *testing.T) {
	model := NewNormalFlowPressureDistributionModel(NormalFlowPressureDistributionConfig{
		Variant: NormalFlowPressureDistributionRobustTanh, MinTrades: 1, MinAbsImbalance: 0,
	})
	first := model.Evaluate(.8, 10)
	model.Observe(.8)
	second := model.Evaluate(.8, 10)
	if first.Transformed != .8 {
		t.Fatalf("cold-start robust transform must not use current observation as prior state: %+v", first)
	}
	if second.Transformed == first.Transformed {
		t.Fatalf("robust transform did not use only prior observed distribution: first=%+v second=%+v", first, second)
	}
}

func TestNormalFlowPressureDistributionRankUsesPastOnly(t *testing.T) {
	model := NewNormalFlowPressureDistributionModel(NormalFlowPressureDistributionConfig{
		Variant: NormalFlowPressureDistributionBalancedRank, MinTrades: 1, MinAbsImbalance: 0,
	})
	first := model.Evaluate(.9, 10)
	if first.Transformed != .9 {
		t.Fatalf("rank transform must have a causal cold start: %+v", first)
	}
	model.Observe(-.8)
	second := model.Evaluate(.9, 10)
	if second.Transformed <= 0 {
		t.Fatalf("high current flow should rank above a negative past observation: %+v", second)
	}
}
