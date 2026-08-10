package main

import (
	"encoding/json"
	"os"
	"time"
)

type quantityProjectionComparisonInput struct {
	ConfigPath                  string
	DataPath                    string
	Symbol                      string
	From, To                    time.Time
	PairEquityJPY, StartingBase float64
	QueueMultiplier             float64
	ReplayCacheDir              string
	CurrentOnly                 bool
	MaxDrawdownStopPct          float64
}

type quantityProjectionComparisonReport struct {
	Symbol                string                 `json:"symbol"`
	From                  time.Time              `json:"from"`
	To                    time.Time              `json:"to"`
	WarmupFrom            time.Time              `json:"warmupFrom"`
	StartingPairEquityJPY float64                `json:"startingPairEquityJPY"`
	StartingBase          float64                `json:"startingBase"`
	QueueMultiplier       float64                `json:"queueMultiplier"`
	ReplayCacheHit        bool                   `json:"replayCacheHit"`
	CurrentOnly           bool                   `json:"currentOnly"`
	MaxDrawdownStopPct    float64                `json:"maxDrawdownStopPct"`
	StagedFallback        productionReplayResult `json:"stagedFallback"`
	ProbabilityCentered   productionReplayResult `json:"probabilityCentered"`
}

func runQuantityProjectionComparison(in quantityProjectionComparisonInput) {
	if !in.From.Before(in.To) || in.PairEquityJPY <= 0 || in.StartingBase < 0 || in.QueueMultiplier < 0 {
		fatalf("invalid quantity projection replay interval, balances, or queue multiplier")
	}
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	warmupFrom := in.From.Add(-macroReplayWarmup(cfg))
	books, trades, cacheHit := loadMacroReplayDataset(in.DataPath, in.Symbol, warmupFrom, in.To, in.From, replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient quantity projection replay events: bbo=%d trades=%d", len(books), len(trades))
	}

	staged := productionReplayResult{}
	if !in.CurrentOnly {
		staged = simulateProductionPolicyWithQuantityProjection(
			books, trades, cfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From, false, in.MaxDrawdownStopPct)
	}
	projected := simulateProductionPolicyWithQuantityProjection(
		books, trades, cfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From, true, in.MaxDrawdownStopPct)

	report := quantityProjectionComparisonReport{
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmupFrom,
		StartingPairEquityJPY: in.PairEquityJPY, StartingBase: in.StartingBase,
		QueueMultiplier: in.QueueMultiplier, StagedFallback: staged,
		ReplayCacheHit: cacheHit, CurrentOnly: in.CurrentOnly, MaxDrawdownStopPct: in.MaxDrawdownStopPct,
		ProbabilityCentered: projected,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode quantity projection comparison: %v", err)
	}
}
