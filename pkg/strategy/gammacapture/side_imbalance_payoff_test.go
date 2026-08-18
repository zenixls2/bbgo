package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestSideImbalancePayoffLearnsSeparateReflectedSlopes(t *testing.T) {
	start := time.Unix(40_000, 0)
	model := NewSideImbalancePayoffModel(time.Hour)
	type sample struct {
		imbalance float64
		outcome   CompetingPathOutcome
		value     float64
	}
	for i, sample := range []sample{
		{imbalance: -1, outcome: CompetingPathBuyOnly, value: -20},
		{imbalance: 1, outcome: CompetingPathBuyOnly, value: 20},
		{imbalance: 1, outcome: CompetingPathSellOnly, value: -20},
		{imbalance: -1, outcome: CompetingPathSellOnly, value: 20},
	} {
		at := start.Add(time.Duration(i) * 2 * time.Minute)
		id, _ := model.Predict(at, at.Add(time.Minute), sample.imbalance)
		if !model.UpdateWeightedLabel(at.Add(time.Minute), id, sample.outcome, sample.value, 1) {
			t.Fatalf("sample %d rejected", i)
		}
	}
	_, positive := model.Predict(start.Add(10*time.Minute), start.Add(11*time.Minute), 1)
	_, negative := model.Predict(start.Add(10*time.Minute), start.Add(11*time.Minute), -1)
	if positive.ConditionalBuyBps <= positive.BaselineBuyBps ||
		negative.ConditionalSellBps <= negative.BaselineSellBps {
		t.Fatalf("side-reflected favorable imbalance was not learned: positive=%+v negative=%+v", positive, negative)
	}
}

func TestSideImbalancePayoffFlatNullDelayAndBounds(t *testing.T) {
	start := time.Unix(50_000, 0)
	model := NewSideImbalancePayoffModel(time.Hour)
	id, before := model.Predict(start, start.Add(time.Minute), math.Inf(1))
	if before.Imbalance != 0 || before.ConditionalBuyBps != 0 || before.ConditionalSellBps != 0 {
		t.Fatalf("invalid/flat state must be neutral: %+v", before)
	}
	if model.UpdateWeightedLabel(start, id, CompetingPathBuyOnly, 10, 1) {
		t.Fatal("future label leaked")
	}
	if !model.UpdateWeightedLabel(start.Add(time.Minute), id, CompetingPathBuyOnly, 10, 1) {
		t.Fatal("mature label rejected")
	}
	_, bounded := model.Predict(start.Add(2*time.Minute), start.Add(3*time.Minute), 5)
	if bounded.Imbalance != 1 || math.IsNaN(bounded.ConditionalBuyBps) || math.IsInf(bounded.ConditionalBuyBps, 0) {
		t.Fatalf("extreme imbalance was not bounded safely: %+v", bounded)
	}
	model.Reset()
	_, reset := model.Predict(start.Add(time.Hour), start.Add(time.Hour+time.Minute), 0)
	if reset.BuyEffectiveSamples != 0 || reset.SellEffectiveSamples != 0 || reset.ConditionalBuyBps != 0 {
		t.Fatalf("reset retained state: %+v", reset)
	}
}
