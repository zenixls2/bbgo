package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestNextHorizonUtilityPivotLabelMaturesOnConfirmation(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{
		{time: base, bid: 100, ask: 101},
		{time: base.Add(4 * time.Minute), bid: 105, ask: 106},
		{time: base.Add(5 * time.Minute), bid: 104, ask: 105},
	}
	events := []horizonUtilityPivotEvent{{
		At: base.Add(5 * time.Minute), PivotAt: base.Add(4 * time.Minute), Direction: 1,
	}}
	pivotAt, maturesAt, outcome, ok := nextHorizonUtilityPivotLabel(
		books, events, base, 1, 101, 100, 101, 12, time.Hour)
	if !ok || !pivotAt.Equal(base.Add(4*time.Minute)) ||
		!maturesAt.Equal(base.Add(5*time.Minute)) || !maturesAt.After(pivotAt) {
		t.Fatalf("pivot label did not preserve confirmation maturity: ok=%v pivot=%v maturity=%v", ok, pivotAt, maturesAt)
	}
	expected := math.Log(105.0/101.0)*10_000 - 12
	if math.Abs(outcome-expected) > 1e-12 {
		t.Fatalf("unexpected executable pivot outcome: got %.12f want %.12f", outcome, expected)
	}
}

func TestNextHorizonUtilityPivotLabelRejectsUnmaturedMaxHorizon(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{
		{time: base, bid: 100, ask: 101},
		{time: base.Add(20 * time.Minute), bid: 105, ask: 106},
	}
	events := []horizonUtilityPivotEvent{{
		At: base.Add(21 * time.Minute), PivotAt: base.Add(20 * time.Minute), Direction: 1,
	}}
	if _, _, _, ok := nextHorizonUtilityPivotLabel(
		books, events, base, 1, 101, 100, 101, 12, 15*time.Minute); ok {
		t.Fatal("pivot confirmation beyond the predeclared maximum horizon was accepted")
	}
}

func TestSummarizeHorizonUtilityReportsPairedIncrement(t *testing.T) {
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	rows := []horizonUtilityStudyRow{{
		At: at, Block: 0, PublicFillProbability: 1, Touched: true,
		ShortOutcomeBps: 1, TotalOutcomeBps: 3, ContinuationKnown: true,
		EffectiveSamples: 8,
		Legacy: gammacapture.HorizonConditionedUtilityDecision{
			Evaluated: true, Approved: true, NotionalJPY: 100,
		},
		Enhanced: gammacapture.HorizonConditionedUtilityDecision{
			Evaluated: true, Approved: true, NotionalJPY: 100,
		},
	}}
	report := summarizeHorizonUtilityRows(rows, at, at.Add(time.Hour))
	if math.Abs(report.LegacyIncrementalVsBaselineBps-1) > 1e-12 ||
		math.Abs(report.ContinuationIncrementalBps-2) > 1e-12 {
		t.Fatalf("paired incremental values were not reported: %+v", report)
	}
}
