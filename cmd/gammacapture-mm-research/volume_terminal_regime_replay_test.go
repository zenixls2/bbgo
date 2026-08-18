package main

import (
	"testing"
	"time"
)

func TestVolumeTerminalTargetProviderNeverUsesFuturePoint(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	horizon := 15 * time.Minute
	provider := &volumeTerminalTargetProvider{
		Policy: volumeTerminalAlways,
		Series: map[time.Duration]volumeTerminalTargetSeries{
			horizon: {
				Horizon: horizon, MaximumAge: time.Hour,
				Points: []volumeTerminalTargetPoint{
					{At: start, BaselineBps: 1, CombinedBps: 2},
					{At: start.Add(10 * time.Minute), BaselineBps: 3, CombinedBps: 4},
				},
			},
		},
	}
	if _, ready := provider.MeanAt(start.Add(-time.Second), horizon); ready {
		t.Fatal("provider leaked a future target")
	}
	if got, ready := provider.MeanAt(start.Add(9*time.Minute), horizon); !ready || got != 2 {
		t.Fatalf("unexpected causal target: got=%v ready=%t", got, ready)
	}
	if got, ready := provider.MeanAt(start.Add(10*time.Minute), horizon); !ready || got != 4 {
		t.Fatalf("new target was not published at its timestamp: got=%v ready=%t", got, ready)
	}
	if _, ready := provider.MeanAt(start.Add(2*time.Hour), horizon); ready {
		t.Fatal("stale target remained active beyond its lifecycle range")
	}
}

func TestRisingConditionedTerminalTarget(t *testing.T) {
	if got, applied := risingConditionedTerminalTarget(2, 7, true); !applied || got != 7 {
		t.Fatalf("rising baseline must admit Volume Profile residual: got=%v applied=%t", got, applied)
	}
	for _, baseline := range []float64{0, -2} {
		if got, applied := risingConditionedTerminalTarget(baseline, 7, true); applied || got != baseline {
			t.Fatalf("non-rising baseline must remain terminal-only: baseline=%v got=%v applied=%t", baseline, got, applied)
		}
	}
	if got, applied := risingConditionedTerminalTarget(2, 7, false); applied || got != 2 {
		t.Fatalf("unready rising specialist must fall back to terminal-only: got=%v applied=%t", got, applied)
	}
}

func TestVolumeTerminalTargetProviderAppliesVolumeProfileOnlyWhileRising(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	horizon := 15 * time.Minute
	provider := &volumeTerminalTargetProvider{
		Policy: volumeTerminalRisingOnly,
		Series: map[time.Duration]volumeTerminalTargetSeries{horizon: {
			MaximumAge: time.Hour,
			Points: []volumeTerminalTargetPoint{
				{At: start, BaselineBps: -1, CombinedBps: 10, RisingBps: 9, RisingSpecialistReady: true},
				{At: start.Add(time.Minute), BaselineBps: 1, CombinedBps: 8, RisingBps: 7, RisingSpecialistReady: true},
			},
		}},
	}
	if got, ready := provider.MeanAt(start, horizon); !ready || got != -1 {
		t.Fatalf("declining target used Volume Profile: got=%v ready=%t", got, ready)
	}
	if got, ready := provider.MeanAt(start.Add(time.Minute), horizon); !ready || got != 7 {
		t.Fatalf("rising target did not use Volume Profile: got=%v ready=%t", got, ready)
	}
	if provider.Queries != 2 || provider.VolumeProfileApplications != 1 {
		t.Fatalf("unexpected gate diagnostics: queries=%d applications=%d", provider.Queries, provider.VolumeProfileApplications)
	}
}

func TestVolumeTerminalTargetUsesAntisymmetricSideValue(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	horizon := 15 * time.Minute
	observations := make([]volumeProfileObservation, 0, 180)
	for i := 0; i < 180; i++ {
		at := start.Add(time.Duration(i) * horizon)
		baseline := [volumeProfileRegressionMaxFeatures]float64{1, 0.1, -0.1}
		volume := [volumeProfileRegressionMaxFeatures]float64{1, float64(i%2)*2 - 1}
		observations = append(observations, volumeProfileObservation{
			at: at, maturity: at.Add(horizon), baseline: baseline, volume: volume,
			baselineN: 3, volumeN: 2,
			buyMeanReturnBps: 20, sellMeanReturnBps: -10, valueBps: 5,
		})
	}
	points := buildVolumeTerminalTargetPoints(observations, horizon, 6*time.Hour)
	if len(points) == 0 {
		t.Fatal("causal terminal target did not become ready")
	}
	last := points[len(points)-1]
	if last.BaselineBps <= 0 || last.CombinedBps <= 0 {
		t.Fatalf("BUY-dominant terminal value must imply positive inventory drift: %+v", last)
	}
	if !last.RisingSpecialistReady || last.RisingBps <= 0 {
		t.Fatalf("mature rising observations must train the rising specialist: %+v", last)
	}
}

func TestVolumeTerminalRisingSpecialistDoesNotTrainOnDecliningTags(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	horizon := 15 * time.Minute
	observations := make([]volumeProfileObservation, 0, 180)
	for i := 0; i < 180; i++ {
		at := start.Add(time.Duration(i) * horizon)
		observations = append(observations, volumeProfileObservation{
			at: at, maturity: at.Add(horizon),
			baseline:  [volumeProfileRegressionMaxFeatures]float64{1, 0.1, -0.1},
			volume:    [volumeProfileRegressionMaxFeatures]float64{1, float64(i%2)*2 - 1},
			baselineN: 3, volumeN: 2,
			buyMeanReturnBps: -20, sellMeanReturnBps: 10, valueBps: -5,
		})
	}
	points := buildVolumeTerminalTargetPoints(observations, horizon, 6*time.Hour)
	if len(points) == 0 {
		t.Fatal("terminal-only baseline did not become ready")
	}
	for _, point := range points {
		if point.BaselineBps >= 0 {
			t.Fatalf("synthetic decline unexpectedly produced a rising tag: %+v", point)
		}
		if point.RisingSpecialistReady || point.RisingBps != point.BaselineBps {
			t.Fatalf("declining labels contaminated the rising specialist: %+v", point)
		}
	}
}
