package main

import (
	"encoding/json"
	"os"
)

type symmetricHorizonReplaySummary struct {
	NetPnLJPY             float64 `json:"netPnLJPY"`
	HoldPnLJPY            float64 `json:"holdPnLJPY"`
	FullFills             int     `json:"fullFills"`
	BuyFills              int     `json:"buyFills"`
	SellFills             int     `json:"sellFills"`
	MaximumDrawdownPct    float64 `json:"maximumDrawdownPct"`
	QuoteUptimePct        float64 `json:"quoteUptimePct"`
	AverageBidDistanceBps float64 `json:"averageBidDistanceBps"`
	AverageAskDistanceBps float64 `json:"averageAskDistanceBps"`
}

type symmetricHorizonReplayReport struct {
	Symbol    string                        `json:"symbol"`
	From      string                        `json:"from"`
	To        string                        `json:"to"`
	Baseline  symmetricHorizonReplaySummary `json:"baseline"`
	Candidate symmetricHorizonReplaySummary `json:"candidate"`
}

func summarizeSymmetricHorizonReplay(result productionReplayResult) symmetricHorizonReplaySummary {
	return symmetricHorizonReplaySummary{
		NetPnLJPY: result.NetPnLJPY, HoldPnLJPY: result.HoldPnLJPY,
		FullFills: result.FullFills, BuyFills: result.BuyFills, SellFills: result.SellFills,
		MaximumDrawdownPct: result.MaximumDrawdownPct, QuoteUptimePct: result.QuoteUptimePct,
		AverageBidDistanceBps: result.AverageBidDistanceBps,
		AverageAskDistanceBps: result.AverageAskDistanceBps,
	}
}

func runSymmetricHorizonActionComparison(in quantityProjectionComparisonInput) {
	if !in.From.Before(in.To) || in.PairEquityJPY <= 0 || in.StartingBase < 0 || in.QueueMultiplier < 0 {
		fatalf("invalid symmetric horizon replay interval, balances, or queue multiplier")
	}
	barrier, intensity, baselineConfig := loadProductionConfig(in.ConfigPath, in.Symbol)
	warmupFrom := in.From.Add(-productionReplayWarmup(baselineConfig))
	books, trades, _ := loadMacroReplayDataset(
		in.DataPath, in.Symbol, warmupFrom, in.To, in.From,
		replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books, trades = compactBBO(books), compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient symmetric horizon replay events: bbo=%d trades=%d", len(books), len(trades))
	}
	candidateConfig := baselineConfig
	baselineConfig.JointDistanceQuantity.PathUtilityHorizonSelection = false
	candidateConfig.JointDistanceQuantity.PathUtilityHorizonSelection = true
	baseline := simulateProductionPolicyWithQuantityProjection(
		books, trades, baselineConfig, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From, true, in.MaxDrawdownStopPct)
	candidate := simulateProductionPolicyWithQuantityProjection(
		books, trades, candidateConfig, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From, true, in.MaxDrawdownStopPct)
	report := symmetricHorizonReplayReport{
		Symbol: in.Symbol, From: in.From.UTC().Format("2006-01-02T15:04:05Z"), To: in.To.UTC().Format("2006-01-02T15:04:05Z"),
		Baseline: summarizeSymmetricHorizonReplay(baseline), Candidate: summarizeSymmetricHorizonReplay(candidate),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode symmetric horizon replay: %v", err)
	}
}
