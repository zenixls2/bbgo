package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func causalKlineTestConfig() CausalKlinePivotConfig {
	return CausalKlinePivotConfig{
		Interval:              types.Duration(3 * time.Minute),
		MinimumTrainingLabels: 1,
		LearningRate:          0.25,
		L2:                    0,
	}
}

func causalKlineBar(at time.Time, open, high, low, close float64) CausalKlineBar {
	return CausalKlineBar{At: at, Open: open, High: high, Low: low, Close: close}
}

func TestCausalKlineBuilderEmitsOnlyClosedBarsAndPreservesGap(t *testing.T) {
	start := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	builder := NewCausalKlineBuilder(3 * time.Minute)
	if _, ok := builder.Observe(start.Add(10*time.Second), 100); ok {
		t.Fatal("first observation cannot close a bar")
	}
	if _, ok := builder.Observe(start.Add(1*time.Minute), 102); ok {
		t.Fatal("observation inside the bucket cannot close a bar")
	}
	if _, ok := builder.Observe(start.Add(2*time.Minute), 99); ok {
		t.Fatal("observation inside the bucket cannot close a bar")
	}
	bar, ok := builder.Observe(start.Add(3*time.Minute+time.Second), 101)
	if !ok {
		t.Fatal("first observation in the next bucket must close the prior bar")
	}
	if !bar.At.Equal(start.Add(3*time.Minute)) || bar.Open != 100 || bar.High != 102 || bar.Low != 99 || bar.Close != 99 {
		t.Fatalf("unexpected causal bar: %+v", bar)
	}
	if bar.GapBefore {
		t.Fatal("first closed bar must not be marked as gapped")
	}
	if !bar.ObservedAt.Equal(start.Add(3*time.Minute + time.Second)) {
		t.Fatalf("bar must retain the actual close-observation time: %+v", bar)
	}
	if _, ok := builder.Observe(start.Add(9*time.Minute+time.Second), 103); !ok {
		t.Fatal("later bucket must close the intervening bar")
	}
	bar, ok = builder.Observe(start.Add(12*time.Minute+time.Second), 104)
	if !ok || !bar.GapBefore {
		t.Fatalf("missing bucket must be visible on the next emitted bar: bar=%+v ok=%v", bar, ok)
	}
}

func TestCausalKlinePivotLabelMaturesAfterNextBarAndNotBefore(t *testing.T) {
	start := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	m := NewCausalKlinePivotLearner(causalKlineTestConfig())

	first := m.ObserveBar(causalKlineBar(start.Add(3*time.Minute), 100, 101, 99, 100))
	if first.PredictionReady || first.LabelMatured {
		t.Fatalf("first bar must not predict or mature a label: %+v", first)
	}
	second := m.ObserveBar(causalKlineBar(start.Add(6*time.Minute), 100, 110, 99, 108))
	if !second.PredictionReady || second.LabelMatured || !second.PendingPrediction {
		t.Fatalf("second bar should create an unresolved prediction: %+v", second)
	}
	third := m.ObserveBar(causalKlineBar(start.Add(9*time.Minute), 108, 109, 104, 105))
	if !third.LabelMatured || third.LabelSkipped || !third.PivotConfirmed || third.MaturedLabelKind != CausalKlinePivotHigh {
		t.Fatalf("third bar should causally confirm the prior high: %+v", third)
	}
	if !third.ConfirmedPivot.At.Equal(start.Add(6*time.Minute)) || !third.ConfirmedPivot.ConfirmedAt.Equal(start.Add(9*time.Minute)) {
		t.Fatalf("pivot time and confirmation time must remain distinct: %+v", third.ConfirmedPivot)
	}
	if third.MaturedLabels != 1 || !third.ModelReady {
		t.Fatalf("matured label must update model readiness only after confirmation: %+v", third)
	}
}

func TestCausalKlinePivotConfirmsLowWithSymmetricRule(t *testing.T) {
	start := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	m := NewCausalKlinePivotLearner(causalKlineTestConfig())
	m.ObserveBar(causalKlineBar(start.Add(3*time.Minute), 100, 101, 99, 100))
	m.ObserveBar(causalKlineBar(start.Add(6*time.Minute), 92, 94, 90, 91))
	d := m.ObserveBar(causalKlineBar(start.Add(9*time.Minute), 91, 96, 91, 95))
	if !d.PivotConfirmed || d.MaturedLabelKind != CausalKlinePivotLow {
		t.Fatalf("causal low pivot must be confirmed symmetrically: %+v", d)
	}
	if d.ConfirmedPivot.Price != 90 || d.ConfirmedPivot.Kind.Direction() != 1 {
		t.Fatalf("low pivot must map to an upward next-leg direction: %+v", d.ConfirmedPivot)
	}
}

func TestCausalKlinePredictionUsesPreLabelWeights(t *testing.T) {
	start := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	m := NewCausalKlinePivotLearner(causalKlineTestConfig())
	m.ObserveBar(causalKlineBar(start.Add(3*time.Minute), 100, 101, 99, 100))
	beforeLabel := m.ObserveBar(causalKlineBar(start.Add(6*time.Minute), 100, 110, 99, 108))
	if beforeLabel.MaturedLabels != 0 {
		t.Fatalf("prediction bar must not have a mature label: %+v", beforeLabel)
	}
	weightsBefore := m.Snapshot().Weights
	afterLabel := m.ObserveBar(causalKlineBar(start.Add(9*time.Minute), 108, 109, 104, 105))
	weightsAfter := m.Snapshot().Weights
	if afterLabel.MaturedLabels != 1 {
		t.Fatalf("expected one matured label: %+v", afterLabel)
	}
	if weightsBefore == weightsAfter {
		t.Fatal("matured pivot label should update the model after, not before, confirmation")
	}
	if beforeLabel.ProbabilityHigh <= 0 || beforeLabel.ProbabilityLow <= 0 || beforeLabel.ProbabilityNeutral <= 0 {
		t.Fatalf("pre-label prediction must still expose a valid distribution: %+v", beforeLabel)
	}
	probabilitySum := afterLabel.ProbabilityHigh + afterLabel.ProbabilityLow + afterLabel.ProbabilityNeutral
	if math.Abs(probabilitySum-1) > 1e-12 {
		t.Fatalf("post-label probabilities must sum to one: %.15f", probabilitySum)
	}
}

func TestCausalKlineGapDiscardsUnmaturedLabelAndResetsFeatures(t *testing.T) {
	start := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	m := NewCausalKlinePivotLearner(causalKlineTestConfig())
	m.ObserveBar(causalKlineBar(start.Add(3*time.Minute), 100, 101, 99, 100))
	m.ObserveBar(causalKlineBar(start.Add(6*time.Minute), 100, 110, 99, 108))
	gap := m.ObserveBar(CausalKlineBar{
		At: start.Add(15 * time.Minute), Open: 105, High: 106, Low: 104, Close: 105,
		GapBefore: true,
	})
	if !gap.SegmentReset || gap.LabelMatured || gap.MaturedLabels != 0 || gap.PendingPrediction {
		t.Fatalf("gap must not bridge or mature a label: %+v", gap)
	}
}

func TestCausalKlineSnapshotRestoreIsDeterministic(t *testing.T) {
	start := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	config := causalKlineTestConfig()
	first := NewCausalKlinePivotLearner(config)
	first.ObserveBar(causalKlineBar(start.Add(3*time.Minute), 100, 101, 99, 100))
	first.ObserveBar(causalKlineBar(start.Add(6*time.Minute), 100, 110, 99, 108))
	snapshot := first.Snapshot()
	second := NewCausalKlinePivotLearner(CausalKlinePivotConfig{})
	if !second.Restore(snapshot) {
		t.Fatal("valid causal Kline snapshot must restore")
	}
	bar := causalKlineBar(start.Add(9*time.Minute), 108, 109, 104, 105)
	gotFirst := first.ObserveBar(bar)
	gotSecond := second.ObserveBar(bar)
	if gotFirst != gotSecond {
		t.Fatalf("restored learner diverged: first=%+v second=%+v", gotFirst, gotSecond)
	}
	if first.Snapshot().Weights != second.Snapshot().Weights {
		t.Fatal("restored learner weights diverged")
	}
}

func TestCausalKlineFlatBarsMatureNeutralLabels(t *testing.T) {
	start := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	m := NewCausalKlinePivotLearner(causalKlineTestConfig())
	for i := 0; i < 4; i++ {
		d := m.ObserveBar(causalKlineBar(start.Add(time.Duration(i+1)*3*time.Minute), 100, 100, 100, 100))
		if i >= 2 && (!d.LabelMatured || d.MaturedLabelKind != CausalKlinePivotNeutral || d.PivotConfirmed) {
			t.Fatalf("flat path must produce neutral matured labels: %+v", d)
		}
	}
}

func TestCausalKlinePivotTargetIsBoundedAndNonBlocking(t *testing.T) {
	config := CausalKlinePivotConfig{
		Enabled: true, MaxTargetShiftRatio: 0.05,
		MinimumProbabilityEdge: 0.10, PriorLabels: 32,
	}
	decision := CausalKlinePivotDecision{
		PredictionReady: true, ModelReady: true,
		ProbabilityLow: 0.70, ProbabilityHigh: 0.10, ProbabilityNeutral: 0.20,
		MaturedLabels: 32,
	}
	got := EvaluateCausalKlinePivotTarget(config, decision, 0.50, 0.20, 0.80)
	if !got.Ready || !got.Applied || got.TargetRatio <= got.BaseTargetRatio {
		t.Fatalf("bullish pivot should apply a positive target overlay: %+v", got)
	}
	if got.ShiftRatio > 0.05+1e-12 || got.TargetRatio > 0.80+1e-12 {
		t.Fatalf("target overlay exceeded configured/hard bounds: %+v", got)
	}

	decision.ProbabilityLow, decision.ProbabilityHigh = 0.54, 0.46
	notReady := EvaluateCausalKlinePivotTarget(config, decision, 0.50, 0.20, 0.80)
	if notReady.Applied || notReady.TargetRatio != notReady.BaseTargetRatio {
		t.Fatalf("near-tie pivot must be non-blocking and leave target unchanged: %+v", notReady)
	}

	decision.ProbabilityLow, decision.ProbabilityHigh, decision.ProbabilityNeutral = 0.45, 0.20, 0.50
	neutralDominant := EvaluateCausalKlinePivotTarget(config, decision, 0.50, 0.20, 0.80)
	if neutralDominant.Applied || neutralDominant.TargetRatio != neutralDominant.BaseTargetRatio {
		t.Fatalf("directional probability must clear neutral before changing target: %+v", neutralDominant)
	}

	config.ShadowOnly = true
	decision.ProbabilityLow, decision.ProbabilityHigh = 0.70, 0.10
	shadow := EvaluateCausalKlinePivotTarget(config, decision, 0.50, 0.20, 0.80)
	if !shadow.Ready || shadow.Applied || shadow.TargetRatio == shadow.BaseTargetRatio {
		t.Fatalf("shadow-only pivot must expose a candidate without applying it: %+v", shadow)
	}
}
