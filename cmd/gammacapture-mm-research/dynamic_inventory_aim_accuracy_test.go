package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestSummarizeDynamicInventoryAimAccuracyUsesMatureFutureMid(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{
		{time: start, bid: 100, ask: 102},
		{time: start.Add(time.Minute), bid: 101, ask: 103},
		{time: start.Add(2 * time.Minute), bid: 102, ask: 104},
	}
	report := summarizeDynamicInventoryAimAccuracy(books, []replayDynamicInventoryAimObservation{
		{At: start, Horizon: 2 * time.Minute, GrossForecastBps: 100, CurrentRatio: .5, AdjustedTargetRatio: .6, GatePassed: true, Applied: true, EffectiveSamples: 10},
	}, start)
	if report.Matured != 1 || report.GatePassed != 1 {
		t.Fatalf("unexpected maturity/gate counts: %+v", report)
	}
	if report.MeanActualMidReturnBps <= 0 || report.SignAccuracy != 1 || report.TargetActionSignAccuracy != 1 {
		t.Fatalf("positive forecast should match positive future return: %+v", report)
	}
	if math.Abs(report.MeanAbsoluteTargetMove-.1) > 1e-12 {
		t.Fatalf("unexpected target movement: %+v", report)
	}
}

func TestDynamicInventoryAimNextPivotUsesOnlyFutureConfirmedPivot(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prices := []float64{100, 110, 105, 100}
	books := make([]bboSnapshot, 0, len(prices))
	for i, price := range prices {
		at := start.Add(time.Duration(i) * dynamicInventoryAimPivotInterval).Add(time.Second)
		books = append(books, bboSnapshot{time: at, bid: price - .1, ask: price + .1})
	}
	pivots := buildDynamicInventoryAimPivots(books)
	if len(pivots) != 1 {
		t.Fatalf("expected one confirmed high pivot, got %+v", pivots)
	}
	if pivots[0].Kind != gammacapture.CausalKlinePivotHigh || !pivots[0].At.Equal(start.Add(6*time.Minute)) {
		t.Fatalf("unexpected next pivot: %+v", pivots[0])
	}
	if !pivots[0].ConfirmedAt.After(pivots[0].At) {
		t.Fatalf("pivot must mature after its extremum: %+v", pivots[0])
	}
	future, ok := nextDynamicInventoryAimPivot(pivots, start.Add(1*time.Minute))
	if !ok || !future.At.Equal(pivots[0].At) {
		t.Fatalf("prediction before the pivot should resolve to the future pivot: %+v ok=%v", future, ok)
	}
	if _, ok := nextDynamicInventoryAimPivot(pivots, start.Add(7*time.Minute)); ok {
		t.Fatal("a pivot whose extremum is already in the past must not be reused as the next pivot")
	}
	if pnl, ok := dynamicInventoryAimPivotActionPnl(99.9, 100.1, future.Price, 1); !ok || pnl <= 0 {
		t.Fatalf("upward action should have positive gross PnL to the high pivot: pnl=%v ok=%v", pnl, ok)
	}
	if pnl, ok := dynamicInventoryAimPivotActionPnl(99.9, 100.1, future.Price, -1); !ok || pnl >= 0 {
		t.Fatalf("downward action should have negative gross PnL to the high pivot: pnl=%v ok=%v", pnl, ok)
	}
}

func TestSummarizeDynamicInventoryAimAccuracyUsesNextPivotPnl(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []struct {
		offset time.Duration
		price  float64
	}{
		{time.Minute, 100},
		{2 * time.Minute, 100},
		{3*time.Minute + time.Second, 100},
		{4*time.Minute + time.Second, 110},
		{6*time.Minute + time.Second, 105},
		{7*time.Minute + time.Second, 105},
		{9*time.Minute + time.Second, 100},
	}
	books := make([]bboSnapshot, 0, len(points))
	for _, point := range points {
		books = append(books, bboSnapshot{time: start.Add(point.offset), bid: point.price - .1, ask: point.price + .1})
	}
	report := summarizeDynamicInventoryAimAccuracy(books, []replayDynamicInventoryAimObservation{
		{At: start.Add(time.Minute), Horizon: time.Minute, GrossForecastBps: 100, CurrentRatio: .5, AdjustedTargetRatio: .6, GatePassed: true},
	}, start)
	if report.NextPivotEligible != 1 || report.NextPivotResolved != 1 || report.NextPivotCensored != 0 {
		t.Fatalf("next pivot should resolve exactly once: %+v", report)
	}
	if report.NextPivotForecastActions != 1 || report.NextPivotForecastHitRate != 1 || report.NextPivotForecastMeanPnlBps <= 0 {
		t.Fatalf("upward forecast should be a positive next-pivot PnL action: %+v", report)
	}
	if report.NextPivotTargetActions != 1 || report.NextPivotTargetHitRate != 1 || report.NextPivotTargetMeanPnlBps <= 0 {
		t.Fatalf("upward target action should be a positive next-pivot PnL action: %+v", report)
	}
}
