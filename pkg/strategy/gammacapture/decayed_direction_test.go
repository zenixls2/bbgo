package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestDecayedDirectionModelUsesSymmetricBetaPrior(t *testing.T) {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	model := NewDecayedDirectionModel(10 * time.Minute)

	if got := model.Snapshot(now); got.PosteriorUp != 0.5 || got.PosteriorDirection != 0 {
		t.Fatalf("empty posterior must be neutral: %+v", got)
	}
	model.Update(CrossingEvent{Direction: DirectionUp, ExchangeTime: now})
	got := model.Snapshot(now)
	assertNear(t, got.PosteriorDirection, 1.0/3.0)

	model.Update(CrossingEvent{Direction: DirectionUp, ExchangeTime: now})
	model.Update(CrossingEvent{Direction: DirectionDown, ExchangeTime: now})
	got = model.Snapshot(now)
	assertNear(t, got.PosteriorDirection, 0.2)
	if got.EffectiveSamples != 3 {
		t.Fatalf("expected three effective samples, got %+v", got)
	}
}

func TestDecayedDirectionModelHalfLife(t *testing.T) {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	model := NewDecayedDirectionModel(10 * time.Minute)
	model.Update(CrossingEvent{Direction: DirectionUp, ExchangeTime: now})

	got := model.Snapshot(now.Add(10 * time.Minute))
	assertNear(t, got.UpWeight, 0.5)
	assertNear(t, got.PosteriorDirection, 0.2)

	got = model.Snapshot(now.Add(20 * time.Minute))
	assertNear(t, got.UpWeight, 0.25)
	assertNear(t, got.PosteriorDirection, 1.0/9.0)
}

func TestDecayedDirectionModelIgnoresUncertainEvents(t *testing.T) {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	model := NewDecayedDirectionModel(10 * time.Minute)
	model.Update(CrossingEvent{Direction: DirectionUp, ExchangeTime: now, GapAffected: true})
	model.Update(CrossingEvent{Direction: DirectionNone, ExchangeTime: now})
	if got := model.Snapshot(now); got.EffectiveSamples != 0 || got.PosteriorDirection != 0 {
		t.Fatalf("uncertain events must not affect posterior: %+v", got)
	}
}

func TestFastEvidenceCoverageUsesBottleneck(t *testing.T) {
	tests := []struct {
		name     string
		snapshot FastEvidenceSnapshot
		want     float64
	}{
		{name: "none", snapshot: FastEvidenceSnapshot{}, want: 0},
		{name: "trade bottleneck", snapshot: FastEvidenceSnapshot{TradeCount: 10, BBOCount: 100}, want: 0.5},
		{name: "bbo bottleneck", snapshot: FastEvidenceSnapshot{TradeCount: 30, BBOCount: 5}, want: 0.25},
		{name: "complete", snapshot: FastEvidenceSnapshot{TradeCount: 20, BBOCount: 20}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertNear(t, fastEvidenceCoverage(tt.snapshot, 20, 20), tt.want)
		})
	}
}

func assertNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("got %.15f want %.15f", got, want)
	}
}
