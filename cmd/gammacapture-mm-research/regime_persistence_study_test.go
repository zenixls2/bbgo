package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestRegimeFutureIndexUsesFirstObservableAfterMaturity(t *testing.T) {
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	samples := make([]regimeSample, 4)
	for i := range samples {
		samples[i] = regimeSample{
			at: start.Add(time.Duration(i) * 5 * time.Minute), bid: 100, ask: 101,
		}
	}
	// A later observable close is valid for a causal label; it must not be
	// fabricated at exactly the nominal maturity timestamp.
	samples[3].at = start.Add(16 * time.Minute)
	if future, ok := regimeFutureIndex(samples, 0, 15*time.Minute, 5*time.Minute); !ok || future != 3 {
		t.Fatalf("expected first observable after maturity, got index=%d ok=%v", future, ok)
	}
	samples[3].at = start.Add(26 * time.Minute)
	if _, ok := regimeFutureIndex(samples, 0, 15*time.Minute, 5*time.Minute); ok {
		t.Fatal("label delayed beyond the predeclared tolerance must be censored")
	}
}

func TestRegimeActionValueUsesExecutableSidesAndSpread(t *testing.T) {
	start := regimeSample{bid: 100, ask: 101}
	future := regimeSample{bid: 102, ask: 103}
	narrowStart := regimeSample{bid: 100, ask: 100.5}
	wideStart := regimeSample{bid: 100, ask: 102}
	narrow := regimeActionValue(narrowStart, future, 1, 0)
	wide := regimeActionValue(wideStart, future, 1, 0)
	if !(narrow > wide) {
		t.Fatalf("wider entry spread must reduce executable BUY markout: narrow=%v wide=%v", narrow, wide)
	}
	if got := regimeActionValue(start, future, 0, 20); got != 0 {
		t.Fatalf("neutral tag must have zero action value: %v", got)
	}
	long := regimeActionValue(start, future, 1, 0)
	if math.IsNaN(long) || math.IsInf(long, 0) {
		t.Fatalf("executable markout must be finite: %v", long)
	}
}

func TestSummarizeRegimePersistencePairedValue(t *testing.T) {
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	samples := make([]regimeSample, 8)
	for i := range samples {
		samples[i] = regimeSample{
			at: start.Add(time.Duration(i) * 5 * time.Minute), bid: 100 + float64(i), ask: 101 + float64(i),
			rawState: 1, filteredState: 1, rawTag: .8, filteredTag: .6,
			eligible: true,
		}
	}
	report := summarizeRegimePersistencePairedValue(samples, 15*time.Minute, 5*time.Minute, 0, time.Hour)
	if report.EligiblePairs != 5 || report.IncrementalMeanBps != 0 || report.IncrementalSEBps != 0 || report.TotalBlocks != 1 {
		t.Fatalf("unexpected paired synthetic report: %+v", report)
	}
}

func TestPrepareRegimePersistenceStudyConfigRejectsThresholdAndCadenceErrors(t *testing.T) {
	base := regimePersistenceStudyInput{
		From: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		SampleInterval: 5 * time.Minute, Horizon: 15 * time.Minute,
		SlowLookback: 30 * time.Minute, VolatilityWindow: 30 * time.Minute,
	}
	if config, err := prepareRegimePersistenceStudyConfig(base); err != nil || config.EnterThreshold != .35 || config.ExitThreshold != .15 {
		t.Fatalf("zero config should receive explicit study defaults: config=%+v err=%v", config, err)
	}
	for _, invalid := range []gammacapture.RegimePersistenceConfig{
		{EnterThreshold: .2, ExitThreshold: .2},
		{EnterThreshold: .2, ExitThreshold: .3},
		{EnterThreshold: math.NaN(), ExitThreshold: .1},
		{UpdateInterval: time.Minute},
	} {
		input := base
		input.FilterConfig = invalid
		if _, err := prepareRegimePersistenceStudyConfig(input); err == nil {
			t.Fatalf("invalid study config was accepted: %+v", invalid)
		}
	}
}

func TestRegimePersistenceRequiredWarmupCoversFeaturesAndFilterMemory(t *testing.T) {
	input := regimePersistenceStudyInput{
		SampleInterval: 5 * time.Minute, SlowLookback: 30 * time.Minute, VolatilityWindow: 45 * time.Minute,
	}
	config := gammacapture.RegimePersistenceConfig{
		UpdateInterval: 5 * time.Minute, SmoothingHalfLife: 30 * time.Minute,
		MinConfirmations: 3, MinStateDuration: 20 * time.Minute,
	}
	warmup := regimePersistenceRequiredWarmup(input, config)
	if warmup != 2*time.Hour+55*time.Minute {
		t.Fatalf("warmup must add feature and persistence memory: %s", warmup)
	}
}

func TestRegimePersistenceSummaryUsesForwardTailWithoutScoringTailAnchors(t *testing.T) {
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	samples := make([]regimeSample, 5)
	for i := range samples {
		samples[i] = regimeSample{
			at: start.Add(time.Duration(i) * 5 * time.Minute), bid: 100 + float64(i), ask: 101 + float64(i),
			rawState: 1, filteredState: 1, rawTag: .8, filteredTag: .6, eligible: i < 3,
		}
	}
	report := summarizeRegimePersistencePairedValue(samples, 5*time.Minute, 5*time.Minute, 0, time.Hour)
	if report.EligiblePairs != 3 || report.TotalBlocks != 1 {
		t.Fatalf("forward tail should label eligible anchors but not become anchors: %+v", report)
	}
}

func TestEstimateRegimeEffectiveSamplesPenalizesSerialDependence(t *testing.T) {
	independent := []float64{-.3, .2, -.1, .4, -.2, .1, .3, -.4, .2, -.1, .4, -.2}
	effective, vif, lag, se := estimateRegimeEffectiveSamples(independent, 5*time.Minute, 15*time.Minute)
	if effective <= 0 || effective > float64(len(independent)) || vif < 1 || lag != 3 || se <= 0 {
		t.Fatalf("invalid HAC estimate for independent-like series: n=%v vif=%v lag=%d se=%v", effective, vif, lag, se)
	}

	clustered := []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, -1, -1, -1, -1}
	clusteredEffective, clusteredVIF, _, _ := estimateRegimeEffectiveSamples(clustered, 5*time.Minute, 30*time.Minute)
	if clusteredEffective >= float64(len(clustered)) || clusteredVIF <= 1 {
		t.Fatalf("serial clusters must reduce effective samples: n=%v vif=%v", clusteredEffective, clusteredVIF)
	}
}

func TestEstimateRegimeEffectiveSamplesHandlesNullSeries(t *testing.T) {
	effective, vif, lag, se := estimateRegimeEffectiveSamples([]float64{2, 2, 2, 2}, 5*time.Minute, time.Hour)
	if effective != 4 || vif != 1 || lag != 0 || se != 0 {
		t.Fatalf("constant null series should be deterministic: n=%v vif=%v lag=%d se=%v", effective, vif, lag, se)
	}
}
