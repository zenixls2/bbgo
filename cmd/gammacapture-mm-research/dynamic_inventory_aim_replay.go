package main

import (
	"encoding/json"
	"math"
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
	PriceBetaControlOnly        bool
	PriceBetaTarget             float64
}

type dynamicInventoryAimComparisonReport struct {
	Mode                  string                 `json:"mode"`
	Symbol                string                 `json:"symbol"`
	From                  time.Time              `json:"from"`
	To                    time.Time              `json:"to"`
	WarmupFrom            time.Time              `json:"warmupFrom"`
	StartingPairEquityJPY float64                `json:"startingPairEquityJPY"`
	StartingBase          float64                `json:"startingBase"`
	QueueMultiplier       float64                `json:"queueMultiplier"`
	BBOInterval           string                 `json:"bboInterval,omitempty"`
	ReplayCacheHit        bool                   `json:"replayCacheHit"`
	PriceBetaTarget       float64                `json:"priceBetaTarget,omitempty"`
	Baseline              productionReplayResult `json:"baseline"`
	DynamicAim            productionReplayResult `json:"dynamicAim"`
	BaselineMetrics       priceBetaReplayMetrics `json:"baselineMetrics"`
	PriceBetaMetrics      priceBetaReplayMetrics `json:"priceBetaMetrics"`
	Gate                  string                 `json:"gate"`
	Warning               string                 `json:"warning"`
}

type priceBetaReplayMetrics struct {
	NetPnLJPY               float64 `json:"netPnLJPY"`
	HoldPnLJPY              float64 `json:"holdPnLJPY"`
	ExcessVsHoldJPY         float64 `json:"excessVsHoldJPY"`
	MaximumDrawdownPct      float64 `json:"maximumDrawdownPct"`
	Fills                   int     `json:"fills"`
	StrategySharpeAnnual    float64 `json:"strategySharpeAnnualized"`
	StrategyHoldCorrelation float64 `json:"strategyHoldCorrelation"`
	StrategyHoldBeta        float64 `json:"strategyHoldBeta"`
	StrategyReturnVolBps    float64 `json:"strategyReturnVolatilityBps"`
	HoldReturnVolBps        float64 `json:"holdReturnVolatilityBps"`
	MeanRiskyWeight         float64 `json:"meanRiskyWeight"`
	MaxRiskyWeight          float64 `json:"maxRiskyWeight"`
	MeanTargetRatio         float64 `json:"meanTargetRatio"`
	MaxTargetRatio          float64 `json:"maxTargetRatio"`
}

func summarizePriceBetaReplay(replay productionReplayResult) priceBetaReplayMetrics {
	blocks, _ := relativeHoldReplayBlocks(replay.EquityCurve, time.Hour)
	sharpe, correlation, beta, strategyVolBps, holdVolBps := relativeHoldBlockRiskMetrics(blocks, time.Hour)
	metrics := priceBetaReplayMetrics{
		NetPnLJPY: replay.NetPnLJPY, HoldPnLJPY: replay.HoldPnLJPY,
		ExcessVsHoldJPY:    replay.NetPnLJPY - replay.HoldPnLJPY,
		MaximumDrawdownPct: replay.MaximumDrawdownPct, Fills: replay.FullFills,
		StrategySharpeAnnual: sharpe, StrategyHoldCorrelation: correlation,
		StrategyHoldBeta: beta, StrategyReturnVolBps: strategyVolBps,
		HoldReturnVolBps: holdVolBps,
	}
	if len(replay.EquityCurve) == 0 {
		return metrics
	}
	for _, point := range replay.EquityCurve {
		metrics.MeanRiskyWeight += point.RiskyWeight
		metrics.MaxRiskyWeight = math.Max(metrics.MaxRiskyWeight, point.RiskyWeight)
		metrics.MeanTargetRatio += point.TargetRatio
		metrics.MaxTargetRatio = math.Max(metrics.MaxTargetRatio, point.TargetRatio)
	}
	count := float64(len(replay.EquityCurve))
	metrics.MeanRiskyWeight /= count
	metrics.MeanTargetRatio /= count
	return metrics
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
	dynamicCfg := cfg
	if in.PriceBetaControlOnly {
		// Isolate the new target cap from the existing dynamic aim. Both arms
		// retain the same fee/risk gate; only the marked-inventory beta cap is
		// different.
		baselineCfg.DynamicInventoryAim.Enabled = true
		baselineCfg.DynamicInventoryAim.ShadowOnly = false
		dynamicCfg.DynamicInventoryAim.Enabled = true
		dynamicCfg.DynamicInventoryAim.ShadowOnly = false
		dynamicCfg.DynamicInventoryAim.PriceBetaTarget = in.PriceBetaTarget
	} else {
		baselineCfg.DynamicInventoryAim.Enabled = false
		baselineCfg.DynamicInventoryAim.ShadowOnly = false
		dynamicCfg.DynamicInventoryAim.Enabled = true
		dynamicCfg.DynamicInventoryAim.ShadowOnly = false
	}

	baseline := simulateProductionPolicyWithQuantityProjection(
		books, trades, baselineCfg, barrier, intensity, nil, replayHorizonTouch,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From,
		true, in.MaxDrawdownStopPct)
	dynamic := simulateProductionPolicyWithQuantityProjection(
		books, trades, dynamicCfg, barrier, intensity, nil, replayHorizonTouch,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From,
		true, in.MaxDrawdownStopPct)

	report := dynamicInventoryAimComparisonReport{
		Mode: func() string {
			if in.PriceBetaControlOnly {
				return "price-beta-target-cap"
			}
			return "dynamic-inventory-aim"
		}(),
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmupFrom,
		StartingPairEquityJPY: in.PairEquityJPY, StartingBase: in.StartingBase,
		QueueMultiplier: in.QueueMultiplier, BBOInterval: in.BBOInterval.String(), ReplayCacheHit: cacheHit,
		PriceBetaTarget: in.PriceBetaTarget, Baseline: baseline, DynamicAim: dynamic,
		BaselineMetrics:  summarizePriceBetaReplay(baseline),
		PriceBetaMetrics: summarizePriceBetaReplay(dynamic),
		Gate:             "INCONCLUSIVE_COMPONENT_REPLAY_UNTIL_UNTOUCHED_SAME_SYMBOL_HOLDOUT",
		Warning:          "This paired replay compares the target component only; it does not promote live YAML and does not use private fills for training.",
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode dynamic inventory aim comparison: %v", err)
	}
}
