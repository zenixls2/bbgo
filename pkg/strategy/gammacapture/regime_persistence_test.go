package gammacapture

import (
	"math"
	"testing"
	"time"
)

func testRegimePersistenceFilter() *RegimePersistenceFilter {
	return NewRegimePersistenceFilter(RegimePersistenceConfig{
		UpdateInterval:    5 * time.Minute,
		SmoothingHalfLife: time.Minute,
		EnterThreshold:    0.35,
		ExitThreshold:     0.15,
		MinConfirmations:  2,
		MinStateDuration:  10 * time.Minute,
		MaxGap:            10 * time.Minute,
	})
}

func regimeInput(at time.Time, slow, fast float64) RegimePersistenceInput {
	return RegimePersistenceInput{At: at, SlowScore: slow, FastReversalScore: fast}
}

func TestRegimePersistenceRequiresConsecutiveConfirmation(t *testing.T) {
	filter := testRegimePersistenceFilter()
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)

	first := filter.Observe(regimeInput(start, 0.8, 0))
	if first.State != 0 || first.PendingState != 1 || first.PendingBuckets != 1 || first.Ready {
		t.Fatalf("first bullish observation should be pending: %+v", first)
	}
	second := filter.Observe(regimeInput(start.Add(5*time.Minute), 0.8, 0))
	if second.State != 1 || !second.Changed || second.PendingBuckets != 0 {
		t.Fatalf("second bullish observation should commit: %+v", second)
	}
	if second.Tag <= 0 || second.Tag > 1 || second.StateAge != 0 {
		t.Fatalf("committed state has invalid strength or age: %+v", second)
	}
}

func TestRegimePersistenceFastReversalDoesNotFlipSlowState(t *testing.T) {
	filter := testRegimePersistenceFilter()
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	filter.Observe(regimeInput(start, 0.8, 0))
	committed := filter.Observe(regimeInput(start.Add(5*time.Minute), 0.8, 0))
	if committed.State != 1 {
		t.Fatalf("setup did not commit bullish state: %+v", committed)
	}

	reversal := filter.Observe(regimeInput(start.Add(10*time.Minute), 0.8, -0.75))
	if reversal.State != 1 || reversal.ReversalConflict != 0.75 || reversal.Changed {
		t.Fatalf("fast reversal must remain a conflict feature: %+v", reversal)
	}

	// A genuine slow reversal still needs two observations and the minimum
	// state duration; a single slow shock cannot flip the state.
	shock := filter.Observe(regimeInput(start.Add(15*time.Minute), -0.8, -0.75))
	if shock.State != 1 || shock.PendingState != -1 || shock.PendingBuckets != 1 {
		t.Fatalf("single slow shock should be pending: %+v", shock)
	}
	flip := filter.Observe(regimeInput(start.Add(20*time.Minute), -0.8, -0.75))
	if flip.State != -1 || !flip.Changed {
		t.Fatalf("confirmed slow reversal should flip: %+v", flip)
	}
}

func TestRegimePersistenceHysteresisAndNeutralExit(t *testing.T) {
	filter := testRegimePersistenceFilter()
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	filter.Observe(regimeInput(start, 0.8, 0))
	filter.Observe(regimeInput(start.Add(5*time.Minute), 0.8, 0))

	// The score is below entry but outside the exit band, so the bullish
	// state is retained rather than chattering through neutral.
	hold := filter.Observe(regimeInput(start.Add(10*time.Minute), 0.25, 0))
	if hold.State != 1 || hold.PendingState != 0 {
		t.Fatalf("hysteresis should retain the state: %+v", hold)
	}
	firstNeutral := filter.Observe(regimeInput(start.Add(15*time.Minute), 0.1, 0))
	if firstNeutral.State != 1 || firstNeutral.PendingState != 0 || firstNeutral.PendingBuckets != 1 {
		t.Fatalf("neutral exit should require confirmation: %+v", firstNeutral)
	}
	neutral := filter.Observe(regimeInput(start.Add(20*time.Minute), 0.1, 0))
	if neutral.State != 0 || !neutral.Changed || neutral.Tag != 0 {
		t.Fatalf("confirmed neutral exit is invalid: %+v", neutral)
	}
}

func TestRegimePersistenceDuplicateAndGapReset(t *testing.T) {
	filter := testRegimePersistenceFilter()
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	filter.Observe(regimeInput(start, 0.8, 0))
	committed := filter.Observe(regimeInput(start.Add(5*time.Minute), 0.8, 0))

	duplicate := filter.Observe(regimeInput(start.Add(6*time.Minute), -0.8, 0))
	if duplicate.State != committed.State || duplicate.PendingBuckets != committed.PendingBuckets || duplicate.Reason != "duplicate regime update bucket ignored" {
		t.Fatalf("same update bucket must not create a transition: %+v", duplicate)
	}

	reset := filter.Observe(regimeInput(start.Add(20*time.Minute), -0.8, 0))
	if !reset.SegmentReset || reset.State != 0 || reset.PendingState != -1 || reset.PendingBuckets != 1 || reset.Ready {
		t.Fatalf("gap must start a fresh causal segment: %+v", reset)
	}
	resumed := filter.Observe(regimeInput(start.Add(25*time.Minute), -0.8, 0))
	if resumed.State != -1 || !resumed.Changed {
		t.Fatalf("fresh segment should commit only after confirmation: %+v", resumed)
	}
}

func TestRegimePersistenceBoundsNonFiniteAndSymmetry(t *testing.T) {
	positive := testRegimePersistenceFilter()
	negative := testRegimePersistenceFilter()
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	positive.Observe(regimeInput(start, 1, -1))
	positiveDecision := positive.Observe(RegimePersistenceInput{
		At: start.Add(5 * time.Minute), SlowScore: 1, FastReversalScore: -1, ChangeProbability: math.Inf(1),
	})
	negative.Observe(regimeInput(start, -1, 1))
	negativeDecision := negative.Observe(RegimePersistenceInput{
		At: start.Add(5 * time.Minute), SlowScore: -1, FastReversalScore: 1, ChangeProbability: math.Inf(1),
	})
	if positiveDecision.State != 1 || negativeDecision.State != -1 {
		t.Fatalf("bounded infinite scores should preserve directional symmetry: positive=%+v negative=%+v", positiveDecision, negativeDecision)
	}
	if positiveDecision.ReversalConflict != negativeDecision.ReversalConflict || positiveDecision.ReversalConflict != 1 {
		t.Fatalf("reversal conflict is not symmetric: positive=%+v negative=%+v", positiveDecision, negativeDecision)
	}
	if positiveDecision.ChangeProbability != 0 || negativeDecision.ChangeProbability != 0 {
		t.Fatalf("non-finite change probability must be safe: positive=%+v negative=%+v", positiveDecision, negativeDecision)
	}
	for _, decision := range []RegimePersistenceDecision{positiveDecision, negativeDecision} {
		if decision.Tag < -1 || decision.Tag > 1 || decision.ReversalConflict < 0 || decision.ReversalConflict > 1 {
			t.Fatalf("decision escaped bounds: %+v", decision)
		}
	}
	if got := positive.Observe(RegimePersistenceInput{At: start.Add(10 * time.Minute), SlowScore: math.NaN()}); got.RawScore != 0 || got.FilteredScore < -1 || got.FilteredScore > 1 {
		t.Fatalf("NaN score must be neutral and bounded: %+v", got)
	}
}

func TestRegimePersistenceThresholdBoundariesAreInclusive(t *testing.T) {
	config := RegimePersistenceConfig{
		UpdateInterval:    time.Minute,
		SmoothingHalfLife: time.Nanosecond,
		EnterThreshold:    .5,
		ExitThreshold:     .2,
		MinConfirmations:  1,
		MinStateDuration:  time.Minute,
		MaxGap:            2 * time.Minute,
	}
	up := NewRegimePersistenceFilter(config)
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	entered := up.Observe(regimeInput(start, .5, 0))
	if entered.State != 1 || !entered.Changed {
		t.Fatalf("entry threshold must be inclusive: %+v", entered)
	}
	exited := up.Observe(regimeInput(start.Add(time.Minute), .2, 0))
	if exited.State != 0 || !exited.Changed {
		t.Fatalf("exit threshold must be inclusive: %+v", exited)
	}

	down := NewRegimePersistenceFilter(config)
	enteredDown := down.Observe(regimeInput(start, -.5, 0))
	if enteredDown.State != -1 || !enteredDown.Changed {
		t.Fatalf("negative entry threshold must be symmetric: %+v", enteredDown)
	}
}
