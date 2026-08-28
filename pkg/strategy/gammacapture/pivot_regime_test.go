package gammacapture

import (
	"math"
	"testing"
	"time"
)

func testPivotRegimeFilter() *PivotRegimeFilter {
	return NewPivotRegimeFilter(PivotRegimeConfig{
		ReversalBps:     20,
		MaxGap:          3 * time.Minute,
		MinLegSamples:   1,
		PriorLegSamples: 1,
	})
}

func observePivotSequence(f *PivotRegimeFilter, start time.Time, prices ...float64) []PivotRegimeDecision {
	decisions := make([]PivotRegimeDecision, 0, len(prices))
	for i, price := range prices {
		decisions = append(decisions, f.Observe(PivotRegimeInput{
			At: start.Add(time.Duration(i) * time.Minute), ReferencePrice: price,
		}))
	}
	return decisions
}

func TestPivotRegimeConfirmsEconomicLegsWithoutFutureData(t *testing.T) {
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	decisions := observePivotSequence(testPivotRegimeFilter(), start,
		100, 100.30, 100.50, 100.25, 100.00, 99.70, 99.50, 99.75, 100.00)
	var upPivot, downPivot bool
	for _, decision := range decisions {
		if decision.PivotChanged && decision.LastPivot.Direction == 1 {
			upPivot = true
		}
		if decision.PivotChanged && decision.LastPivot.Direction == -1 {
			downPivot = true
		}
	}
	if !upPivot || !downPivot {
		t.Fatalf("expected both confirmed pivot directions: up=%v down=%v decisions=%+v", upPivot, downPivot, decisions)
	}
	last := decisions[len(decisions)-1]
	if last.Direction != 1 || last.LegAmplitudeBps <= 0 || last.ExpectedLegAmplitudeBps <= 0 {
		t.Fatalf("expected a causal new up leg with empirical amplitude: %+v", last)
	}
}

func TestPivotRegimeRejectsDuplicateTimestamp(t *testing.T) {
	f := testPivotRegimeFilter()
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	first := f.Observe(PivotRegimeInput{At: start, ReferencePrice: 100})
	second := f.Observe(PivotRegimeInput{At: start, ReferencePrice: 101})
	if second.Reason != "non-monotonic pivot observation" || second.At != first.At || second.Direction != first.Direction {
		t.Fatalf("duplicate timestamp must not advance state: first=%+v second=%+v", first, second)
	}
}

func TestPivotRegimeGapResetsActiveLeg(t *testing.T) {
	f := testPivotRegimeFilter()
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	observePivotSequence(f, start, 100, 100.3, 100.5)
	decision := f.Observe(PivotRegimeInput{At: start.Add(10 * time.Minute), ReferencePrice: 99})
	if !decision.SegmentReset || decision.Direction != 0 || decision.Ready {
		t.Fatalf("gap must reset causal pivot segment: %+v", decision)
	}
}

func TestPivotRegimeSnapshotRestoresLegRisk(t *testing.T) {
	f := testPivotRegimeFilter()
	snapshot := f.Snapshot()
	snapshot.Seeded = true
	snapshot.LastAt = time.Date(2026, 8, 24, 0, 2, 0, 0, time.UTC)
	snapshot.Direction = 1
	snapshot.AnchorAt = snapshot.LastAt.Add(-time.Minute)
	snapshot.AnchorPrice = 100
	snapshot.ExtremeAt = snapshot.LastAt
	snapshot.ExtremePrice = 100.3
	snapshot.LegCount[0] = 2
	snapshot.LegSum[0] = 60
	snapshot.LegSumSquares[0] = 2_000
	snapshot.LastDecision = PivotRegimeDecision{
		At: snapshot.LastAt, Direction: 1, Ready: true,
		ExpectedLegAmplitudeBps: 30, CompletedLegSamples: 2,
	}
	if !f.Restore(snapshot) {
		t.Fatal("valid pivot regime snapshot was rejected")
	}
	if got := f.CompletedLegVarianceBps2(1); math.Abs(got-200) > 1e-12 {
		t.Fatalf("restored same-direction leg variance mismatch: got %.12f want 200", got)
	}
	if got := f.Snapshot().LastDecision; got != snapshot.LastDecision {
		t.Fatalf("restored last decision mismatch: got=%+v want=%+v", got, snapshot.LastDecision)
	}
}

func TestEvaluatePivotRegimeSizingIsContinuousAndFeeAware(t *testing.T) {
	base := PivotRegimeDecision{
		Ready: true, Direction: 1, RemainingAmplitudeBps: 60,
		Reliability: 0.5,
	}
	decision := EvaluatePivotRegimeSizing(PivotRegimeSizingInput{
		Decision: base, CostBps: 20, RiskPenaltyBps: 0,
		MaxTargetShift: 0.2, TargetScaleBps: 40,
	})
	if !decision.Applied || decision.QuantityScale <= 0 || decision.QuantityScale >= 1 || decision.TargetShiftRatio <= 0 {
		t.Fatalf("expected bounded continuous positive sizing: %+v", decision)
	}
	base.RemainingAmplitudeBps = 19
	blocked := EvaluatePivotRegimeSizing(PivotRegimeSizingInput{
		Decision: base, CostBps: 20, RiskPenaltyBps: 0,
		MaxTargetShift: 0.2, TargetScaleBps: 40,
	})
	if blocked.Applied || blocked.QuantityScale != 0 {
		t.Fatalf("sub-fee pivot remainder must have zero scale: %+v", blocked)
	}
	if math.Abs(decision.SignedQuantityScale-decision.QuantityScale) > 1e-12 {
		t.Fatalf("up leg sign mismatch: %+v", decision)
	}
}

func TestEvaluatePivotRegimeSizingFailsClosedOnNonFiniteState(t *testing.T) {
	decision := EvaluatePivotRegimeSizing(PivotRegimeSizingInput{
		Decision: PivotRegimeDecision{
			Ready: true, Direction: 1, RemainingAmplitudeBps: math.NaN(),
			ExpectedLegAmplitudeBps: 100, Reliability: 1,
		},
		CostBps: 20, MaxTargetShift: 0.2,
	})
	if decision.Applied || decision.QuantityScale != 0 || decision.TargetShiftRatio != 0 {
		t.Fatalf("non-finite pivot state must fail closed: %+v", decision)
	}
}
