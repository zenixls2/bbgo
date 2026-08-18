package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestConditionalPathPayoffShrinksSparseEvidenceTowardNull(t *testing.T) {
	start := time.Unix(10_000, 0)
	model := NewConditionalPathPayoffModel(time.Hour)
	id, before := model.Predict(start, start.Add(time.Minute))
	if before.BaselineMeanBps[CompetingPathBuyOnly] != 0 || before.ShrunkMeanBps[CompetingPathBuyOnly] != 0 {
		t.Fatalf("untrained model must be neutral: %+v", before)
	}
	if model.UpdateLabel(start, id, CompetingPathBuyOnly, 100) {
		t.Fatal("label matured early")
	}
	if !model.UpdateLabel(start.Add(time.Minute), id, CompetingPathBuyOnly, 100) {
		t.Fatal("mature label rejected")
	}
	_, after := model.Predict(start.Add(2*time.Minute), start.Add(3*time.Minute))
	baseline := after.BaselineMeanBps[CompetingPathBuyOnly]
	shrunk := after.ShrunkMeanBps[CompetingPathBuyOnly]
	if baseline != 100 || !(shrunk > 0 && shrunk < baseline) {
		t.Fatalf("sparse payoff was not shrunk toward zero: %+v", after)
	}
}

func TestConditionalPathPayoffBuySellSymmetryAndStaleDecay(t *testing.T) {
	start := time.Unix(20_000, 0)
	model := NewConditionalPathPayoffModel(time.Hour)
	ids := make([]uint64, 0, 2)
	for range []CompetingPathOutcome{CompetingPathBuyOnly, CompetingPathSellOnly} {
		id, _ := model.Predict(start, start.Add(time.Minute))
		ids = append(ids, id)
	}
	for i, outcome := range []CompetingPathOutcome{CompetingPathBuyOnly, CompetingPathSellOnly} {
		if !model.UpdateLabel(start.Add(time.Minute), ids[i], outcome, -20) {
			t.Fatalf("symmetric label %d rejected", i)
		}
	}
	_, fresh := model.Predict(start.Add(2*time.Minute), start.Add(3*time.Minute))
	_, stale := model.Predict(start.Add(5*time.Hour), start.Add(5*time.Hour+time.Minute))
	if math.Abs(fresh.BaselineMeanBps[CompetingPathBuyOnly]-fresh.BaselineMeanBps[CompetingPathSellOnly]) > 1e-12 ||
		math.Abs(fresh.ShrunkMeanBps[CompetingPathBuyOnly]-fresh.ShrunkMeanBps[CompetingPathSellOnly]) > 1e-12 {
		t.Fatalf("BUY/SELL symmetry broken: %+v", fresh)
	}
	if math.Abs(stale.ShrunkMeanBps[CompetingPathBuyOnly]) >= math.Abs(fresh.ShrunkMeanBps[CompetingPathBuyOnly]) {
		t.Fatalf("stale evidence did not decay toward null: fresh=%+v stale=%+v", fresh, stale)
	}
}

func TestConditionalPathPayoffResetAndBoundarySafety(t *testing.T) {
	model := NewConditionalPathPayoffModel(time.Hour)
	start := time.Unix(30_000, 0)
	id, _ := model.Predict(start, start.Add(time.Minute))
	if model.UpdateLabel(start.Add(time.Minute), id, CompetingPathBuyOnly, math.Inf(1)) {
		t.Fatal("non-finite label accepted")
	}
	model.Reset()
	_, snapshot := model.Predict(start.Add(time.Hour), start.Add(time.Hour+time.Minute))
	if snapshot.EffectiveCounts[CompetingPathBuyOnly] != 0 || snapshot.ShrunkMeanBps[CompetingPathBuyOnly] != 0 {
		t.Fatalf("reset retained state: %+v", snapshot)
	}
}
