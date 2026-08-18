package main

import (
	"encoding/json"
	"os"
	"time"
)

type dynamicInventoryAimComparisonInput struct {
	ConfigPath                  string
	DataPath                    string
	Symbol                      string
	From, To                    time.Time
	PairEquityJPY, StartingBase float64
	QueueMultiplier             float64
	ReplayCacheDir              string
	BBOInterval                 time.Duration
	MaxDrawdownStopPct          float64
}

type dynamicInventoryAimComparisonReport struct {
	Symbol                string                 `json:"symbol"`
	From                  time.Time              `json:"from"`
	To                    time.Time              `json:"to"`
	WarmupFrom            time.Time              `json:"warmupFrom"`
	StartingPairEquityJPY float64                `json:"startingPairEquityJPY"`
	StartingBase          float64                `json:"startingBase"`
	QueueMultiplier       float64                `json:"queueMultiplier"`
	BBOInterval           string                 `json:"bboInterval,omitempty"`
	ReplayCacheHit        bool                   `json:"replayCacheHit"`
	Baseline              productionReplayResult `json:"baseline"`
	DynamicAim            productionReplayResult `json:"dynamicAim"`
	Gate                  string                 `json:"gate"`
	Warning               string                 `json:"warning"`
}

// runDynamicInventoryAimComparison is a paired component replay. All quote,
// arrival, fee, and execution code is identical; only the single inventory
// target source changes. It intentionally does not use confirmed private fills
// to tune the target, so a positive result is still a promotion candidate, not
// permission to change live YAML.
func runDynamicInventoryAimComparison(in dynamicInventoryAimComparisonInput) {
	if !in.From.Before(in.To) || in.PairEquityJPY <= 0 || in.StartingBase < 0 || in.QueueMultiplier < 0 {
		fatalf("invalid dynamic inventory aim replay interval, balances, or queue multiplier")
	}
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	warmupFrom := in.From.Add(-productionReplayWarmup(cfg))
	books, trades, cacheHit := loadMacroReplayDataset(
		in.DataPath, in.Symbol, warmupFrom, in.To, in.From,
		replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books = compactBBO(books)
	if in.BBOInterval > 0 {
		books = compactBBOAtInterval(books, in.BBOInterval)
	}
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient dynamic inventory aim replay events: bbo=%d trades=%d", len(books), len(trades))
	}

	baselineCfg := cfg
	baselineCfg.DynamicInventoryAim.Enabled = false
	baselineCfg.DynamicInventoryAim.ShadowOnly = false
	dynamicCfg := cfg
	dynamicCfg.DynamicInventoryAim.Enabled = true
	dynamicCfg.DynamicInventoryAim.ShadowOnly = false

	baseline := simulateProductionPolicyWithQuantityProjection(
		books, trades, baselineCfg, barrier, intensity, nil, replayHorizonTouch,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From,
		true, in.MaxDrawdownStopPct)
	dynamic := simulateProductionPolicyWithQuantityProjection(
		books, trades, dynamicCfg, barrier, intensity, nil, replayHorizonTouch,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From,
		true, in.MaxDrawdownStopPct)

	report := dynamicInventoryAimComparisonReport{
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmupFrom,
		StartingPairEquityJPY: in.PairEquityJPY, StartingBase: in.StartingBase,
		QueueMultiplier: in.QueueMultiplier, BBOInterval: in.BBOInterval.String(), ReplayCacheHit: cacheHit,
		Baseline: baseline, DynamicAim: dynamic,
		Gate:    "INCONCLUSIVE_COMPONENT_REPLAY_UNTIL_UNTOUCHED_SAME_SYMBOL_HOLDOUT",
		Warning: "This paired replay compares the target component only; it does not promote live YAML and does not use private fills for training.",
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode dynamic inventory aim comparison: %v", err)
	}
}
