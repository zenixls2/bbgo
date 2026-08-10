package main

import (
	"encoding/json"
	"math"
	"os"
	"time"
)

type postFillUtilityComparisonInput struct {
	ConfigPath, DataPath, Symbol        string
	From, To                            time.Time
	PairEquityJPY, StartingBase         float64
	QueueMultiplier, MaxDrawdownStopPct float64
	ReplayCacheDir                      string
}

type postFillUtilityComparisonReport struct {
	Symbol                   string `json:"symbol"`
	From, To                 time.Time
	WarmupFrom               time.Time              `json:"warmupFrom"`
	StartingPairEquityJPY    float64                `json:"startingPairEquityJPY"`
	StartingBase             float64                `json:"startingBase"`
	QueueMultiplier          float64                `json:"queueMultiplier"`
	ReplayCacheHit           bool                   `json:"replayCacheHit"`
	Hold                     replayHoldBenchmark    `json:"hold"`
	Current                  productionReplayResult `json:"current"`
	PostFillUtility          productionReplayResult `json:"postFillUtility"`
	CurrentExcessLower95JPY  float64                `json:"currentExcessLower95JPY"`
	UtilityExcessLower95JPY  float64                `json:"utilityExcessLower95JPY"`
	PairedMeanHourlyDeltaJPY float64                `json:"pairedMeanHourlyDeltaJPY"`
	PairedLower95HourlyJPY   float64                `json:"pairedLower95HourlyJPY"`
	PairedHourlySamples      int                    `json:"pairedHourlySamples"`
}

func runPostFillUtilityComparison(in postFillUtilityComparisonInput) {
	if !in.From.Before(in.To) || in.PairEquityJPY <= 0 || in.StartingBase < 0 || in.QueueMultiplier < 0 {
		fatalf("invalid post-fill utility replay interval, balances, or queue multiplier")
	}
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	warmupFrom := in.From.Add(-macroReplayWarmup(cfg))
	books, trades, cacheHit := loadMacroReplayDataset(
		in.DataPath, in.Symbol, warmupFrom, in.To, in.From,
		replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient post-fill utility replay events: bbo=%d trades=%d", len(books), len(trades))
	}
	if err := validateSpotReplayStartingBalance(books, in.From, in.PairEquityJPY, in.StartingBase); err != nil {
		fatalf("invalid post-fill utility starting balance: %v", err)
	}
	currentCfg := cfg
	currentCfg.PostFillUtility.Enabled = false
	utilityCfg := cfg
	utilityCfg.PostFillUtility.Enabled = true
	current := simulateProductionPolicyWithQuantityProjection(
		books, trades, currentCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From, true, in.MaxDrawdownStopPct)
	utility := simulateProductionPolicyWithQuantityProjection(
		books, trades, utilityCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From, true, in.MaxDrawdownStopPct)
	_, currentLower, _ := pairedExcessLower95(current)
	_, utilityLower, _ := pairedExcessLower95(utility)
	pairedMean, pairedLower, pairedN := pairedPolicyDeltaLower95(current, utility)
	report := postFillUtilityComparisonReport{
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmupFrom,
		StartingPairEquityJPY: in.PairEquityJPY, StartingBase: in.StartingBase,
		QueueMultiplier: in.QueueMultiplier, ReplayCacheHit: cacheHit,
		Hold:    holdBenchmark(books, in.From, in.PairEquityJPY, in.StartingBase),
		Current: current, PostFillUtility: utility,
		CurrentExcessLower95JPY: currentLower, UtilityExcessLower95JPY: utilityLower,
		PairedMeanHourlyDeltaJPY: pairedMean, PairedLower95HourlyJPY: pairedLower,
		PairedHourlySamples: pairedN,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode post-fill utility comparison: %v", err)
	}
}

func pairedPolicyDeltaLower95(base, candidate productionReplayResult) (mean, lower float64, samples int) {
	if len(base.EquityCurve) < 2 || len(candidate.EquityCurve) < 2 {
		return 0, 0, 0
	}
	start := base.EquityCurve[0].At
	nextBoundary := start.Add(time.Hour)
	previousDelta := 0.0
	values := make([]float64, 0)
	bi, ci := 0, 0
	for bi < len(base.EquityCurve) && ci < len(candidate.EquityCurve) {
		bp, cp := base.EquityCurve[bi], candidate.EquityCurve[ci]
		if bp.At.Before(cp.At) {
			bi++
			continue
		}
		if cp.At.Before(bp.At) {
			ci++
			continue
		}
		if !bp.At.Before(nextBoundary) {
			delta := cp.EquityJPY - bp.EquityJPY
			values = append(values, delta-previousDelta)
			previousDelta = delta
			nextBoundary = nextBoundary.Add(time.Hour)
		}
		bi++
		ci++
	}
	if len(values) == 0 {
		return 0, 0, 0
	}
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	if len(values) == 1 {
		return mean, mean, 1
	}
	var squares float64
	for _, value := range values {
		squares += math.Pow(value-mean, 2)
	}
	se := math.Sqrt(squares / float64(len(values)-1) / float64(len(values)))
	return mean, mean - 1.6448536269514722*se, len(values)
}
