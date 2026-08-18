package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestCompetingPathOutcomeSymmetry(t *testing.T) {
	if CompetingPathOutcomeFromTouches(true, false) != CompetingPathBuyOnly ||
		CompetingPathOutcomeFromTouches(false, true) != CompetingPathSellOnly ||
		CompetingPathOutcomeFromTouches(true, true) != CompetingPathBoth ||
		CompetingPathOutcomeFromTouches(false, false) != CompetingPathNone {
		t.Fatal("touch categories are not exhaustive and symmetric")
	}
}

func TestCompetingPathValueUsesOnlyMaturedLabels(t *testing.T) {
	start := time.Unix(1_000, 0)
	model := &CompetingPathValueModel{}
	id, before := model.Predict(start, start.Add(15*time.Minute))
	for _, probability := range before.Probabilities {
		if math.Abs(probability-0.25) > 1e-12 {
			t.Fatalf("Jeffreys prior must be neutral: %+v", before)
		}
	}
	if model.UpdateLabel(start.Add(14*time.Minute), id, CompetingPathBoth, 20, 1) {
		t.Fatal("future outcome leaked before label maturity")
	}
	if !model.UpdateLabel(start.Add(15*time.Minute), id, CompetingPathBoth, 20, 1) {
		t.Fatal("mature label was not accepted")
	}
	after := model.Snapshot()
	if after.Probabilities[CompetingPathBoth] <= 0.25 || after.ExpectedValueBps <= 0 ||
		after.EffectiveSamples != 1 {
		t.Fatalf("mature positive cycle did not update posterior: %+v", after)
	}
	if model.UpdateLabel(start.Add(15*time.Minute), id, CompetingPathBoth, 20, 1) {
		t.Fatal("a label may update the model only once")
	}
}

func TestCompetingPathValueFlatNullAndBuySellSymmetry(t *testing.T) {
	start := time.Unix(2_000, 0)
	model := &CompetingPathValueModel{}
	for i, observation := range []struct {
		outcome CompetingPathOutcome
		value   float64
	}{
		{CompetingPathBuyOnly, -12},
		{CompetingPathSellOnly, -12},
		{CompetingPathBoth, 24},
		{CompetingPathNone, 0},
	} {
		at := start.Add(time.Duration(i) * time.Hour)
		id, _ := model.Predict(at, at.Add(time.Minute))
		if !model.UpdateLabel(at.Add(time.Minute), id, observation.outcome, observation.value, 1) {
			t.Fatalf("observation %d was rejected", i)
		}
	}
	s := model.Snapshot()
	if math.Abs(s.Probabilities[CompetingPathBuyOnly]-s.Probabilities[CompetingPathSellOnly]) > 1e-12 ||
		math.Abs(s.ConditionalMeanBps[CompetingPathBuyOnly]-s.ConditionalMeanBps[CompetingPathSellOnly]) > 1e-12 ||
		math.Abs(s.ExpectedValueBps) > 1e-12 {
		t.Fatalf("symmetric zero-value path must remain neutral: %+v", s)
	}
}

func TestCompetingPathValueResetAndOrdering(t *testing.T) {
	start := time.Unix(3_000, 0)
	model := &CompetingPathValueModel{}
	id, _ := model.Predict(start, start.Add(time.Minute))
	if !model.UpdateLabel(start.Add(time.Minute), id, CompetingPathBoth, 10, 1) {
		t.Fatal("first label failed")
	}
	lateID, _ := model.Predict(start.Add(2*time.Minute), start.Add(3*time.Minute))
	if model.UpdateLabel(start, lateID, CompetingPathNone, 0, 1) {
		t.Fatal("out-of-order label must be rejected")
	}
	model.Reset()
	s := model.Snapshot()
	if s.EffectiveSamples != 0 || s.ExpectedValueBps != 0 || !s.UpdatedAt.IsZero() {
		t.Fatalf("reset retained learned state: %+v", s)
	}
}
