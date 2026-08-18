package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestTerminalTailTargetLearnsReversionAndContinuationWithoutFixedSign(t *testing.T) {
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	model := NewTerminalTailTargetModel(time.Hour)
	for index := 0; index < 80; index++ {
		at := start.Add(time.Duration(index) * time.Minute)
		feature := -1.0
		label := 12.0
		if index%2 == 1 {
			feature = 1
			label = -12
		}
		id, _ := model.Predict(at, at.Add(time.Minute), feature)
		if !model.UpdateWeightedLabel(at.Add(time.Minute), id, label, 1) {
			t.Fatalf("reversion label %d did not mature", index)
		}
	}
	at := start.Add(2 * time.Hour)
	_, high := model.Predict(at, at.Add(time.Minute), 1)
	_, low := model.Predict(at, at.Add(time.Minute), -1)
	if high.ConditionalMeanBps >= low.ConditionalMeanBps ||
		high.ConditionalMeanBps >= high.BaselineMeanBps {
		t.Fatalf("model did not learn mean reversion: high=%+v low=%+v", high, low)
	}

	model.Reset()
	for index := 0; index < 80; index++ {
		when := at.Add(time.Duration(index) * time.Minute)
		feature := -1.0
		label := -8.0
		if index%2 == 1 {
			feature = 1
			label = 8
		}
		id, _ := model.Predict(when, when.Add(time.Minute), feature)
		if !model.UpdateWeightedLabel(when.Add(time.Minute), id, label, 1) {
			t.Fatalf("continuation label %d did not mature", index)
		}
	}
	_, continuation := model.Predict(at.Add(3*time.Hour), at.Add(3*time.Hour+time.Minute), 1)
	if continuation.ConditionalMeanBps <= continuation.BaselineMeanBps {
		t.Fatalf("model imposed reversion instead of learning continuation: %+v", continuation)
	}
}

func TestTerminalTailTargetDelayNullBoundsResetAndOrdering(t *testing.T) {
	start := time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)
	model := NewTerminalTailTargetModel(time.Hour)
	id, cold := model.Predict(start, start.Add(15*time.Minute), math.Inf(1))
	if id == 0 || cold.Feature != 0 || cold.ConditionalMeanBps != 0 {
		t.Fatalf("invalid feature must be neutral and bounded: %+v", cold)
	}
	if model.UpdateWeightedLabel(start.Add(14*time.Minute), id, 0, 1) {
		t.Fatal("label updated before maturity")
	}
	if badID, _ := model.Predict(start.Add(-time.Second), start.Add(time.Hour), 0); badID != 0 {
		t.Fatal("prediction older than a pending prediction was accepted")
	}
	if !model.UpdateWeightedLabel(start.Add(15*time.Minute), id, 0, 1) {
		t.Fatal("mature zero label rejected")
	}
	_, flat := model.Predict(start.Add(16*time.Minute), start.Add(31*time.Minute), 5)
	if flat.BaselineMeanBps != 0 || flat.ConditionalMeanBps != 0 ||
		math.IsNaN(flat.ConditionalMeanBps) || math.IsInf(flat.ConditionalMeanBps, 0) {
		t.Fatalf("flat null must remain neutral: %+v", flat)
	}
	if badID, _ := model.Predict(start, start.Add(time.Hour), 0); badID != 0 {
		t.Fatal("out-of-order prediction accepted")
	}
	model.Reset()
	_, reset := model.Predict(start.Add(time.Hour), start.Add(2*time.Hour), -5)
	if reset.EffectiveSamples != 0 || reset.ConditionalMeanBps != 0 {
		t.Fatalf("reset retained learned state: %+v", reset)
	}
}
