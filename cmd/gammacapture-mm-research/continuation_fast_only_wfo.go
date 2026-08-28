package main

import (
	"encoding/json"
	"math"
	"os"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

// continuationFastOnlyWFOInput describes a research-only paired replay.  The
// baseline keeps the live Fast quote path and its fixed 50/50 capital target;
// the candidate keeps that exact quote/execution path but lets the causal
// continuation posterior supply the single inventory target.  No live YAML or
// strategy wiring is changed by this command.
type continuationFastOnlyWFOInput struct {
	ConfigPath                  string
	DataPath                    string
	Symbol                      string
	From, To                    time.Time
	PairEquityJPY, StartingBase float64
	QueueMultiplier             float64
	ContinuationPriorStrength   float64
	ReplayCacheDir              string
	Block                       time.Duration
	BBOInterval                 time.Duration
}

type continuationFastOnlyWFOBlock struct {
	From                        time.Time `json:"from"`
	To                          time.Time `json:"to"`
	BaselineNetPnLJPY           float64   `json:"baselineNetPnLJPY"`
	CandidateNetPnLJPY          float64   `json:"candidateNetPnLJPY"`
	HoldPnLJPY                  float64   `json:"holdPnLJPY"`
	BaselineExcessVsHoldJPY     float64   `json:"baselineExcessVsHoldJPY"`
	CandidateExcessVsHoldJPY    float64   `json:"candidateExcessVsHoldJPY"`
	CandidateDeltaVsBaselineJPY float64   `json:"candidateDeltaVsBaselineJPY"`
	BaselineFills               int       `json:"baselineFills"`
	CandidateFills              int       `json:"candidateFills"`
	BaselineMaximumDrawdownPct  float64   `json:"baselineMaximumDrawdownPct"`
	CandidateMaximumDrawdownPct float64   `json:"candidateMaximumDrawdownPct"`
}

type continuationFastOnlyWFOSummary struct {
	MeanCandidateDeltaVsBaselineJPY float64 `json:"meanCandidateDeltaVsBaselineJPY"`
	Lower95CandidateDeltaVsBaseline float64 `json:"lower95CandidateDeltaVsBaselineJPY"`
	MeanCandidateExcessVsHoldJPY    float64 `json:"meanCandidateExcessVsHoldJPY"`
	Lower95CandidateExcessVsHoldJPY float64 `json:"lower95CandidateExcessVsHoldJPY"`
	PositiveDeltaBlocks             int     `json:"positiveDeltaBlocks"`
	PositiveExcessBlocks            int     `json:"positiveExcessBlocks"`
	TotalBlocks                     int     `json:"totalBlocks"`
}

type continuationFastOnlyWFOReport struct {
	Mode                      string                            `json:"mode"`
	Symbol                    string                            `json:"symbol"`
	From                      time.Time                         `json:"from"`
	To                        time.Time                         `json:"to"`
	WarmupFrom                time.Time                         `json:"warmupFrom"`
	Block                     time.Duration                     `json:"block"`
	BBOInterval               time.Duration                     `json:"bboInterval"`
	PairEquityJPY             float64                           `json:"pairEquityJPY"`
	StartingBase              float64                           `json:"startingBase"`
	QueueMultiplier           float64                           `json:"queueMultiplier"`
	ContinuationPriorStrength float64                           `json:"continuationPriorStrength"`
	ReplayCacheHit            bool                              `json:"replayCacheHit"`
	BaselineConfig            continuationFastOnlyWFOConfigView `json:"baselineConfig"`
	CandidateConfig           continuationFastOnlyWFOConfigView `json:"candidateConfig"`
	Baseline                  productionReplayResult            `json:"baseline"`
	Candidate                 productionReplayResult            `json:"candidate"`
	Blocks                    []continuationFastOnlyWFOBlock    `json:"blocks"`
	Summary                   continuationFastOnlyWFOSummary    `json:"summary"`
	PrivateFillCalibration    string                            `json:"privateFillCalibration"`
	Decision                  string                            `json:"decision"`
	Limitations               []string                          `json:"limitations"`
}

type continuationFastOnlyWFOConfigView struct {
	MacroInventoryEnabled bool `json:"macroInventoryEnabled"`
	NoTradeEnabled        bool `json:"noTradeEnabled"`
	ContinuationMixture   bool `json:"continuationMixture"`
	ActiveIOCEnabled      bool `json:"activeIOCEnabled"`
	JointDistanceEnabled  bool `json:"jointDistanceEnabled"`
}

func continuationFastOnlyBaselineConfig(cfg gammacapture.MarketMakerConfig) gammacapture.MarketMakerConfig {
	baseline := cfg
	baseline.MacroInventory.Enabled = false
	baseline.MacroInventory.NoTradeRegion.Enabled = false
	baseline.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled = false
	baseline.InventoryTargetRatio = 0.5
	baseline.InventoryCapitalMinRatio = 0
	baseline.InventoryCapitalTargetRatio = 0.5
	baseline.InventoryCapitalMaxRatio = 1
	return baseline
}

func continuationFastOnlyCandidateConfig(cfg gammacapture.MarketMakerConfig) gammacapture.MarketMakerConfig {
	candidate := continuationFastOnlyBaselineConfig(cfg)
	// This is deliberately the only additional controller.  Trend excursion,
	// legacy continuation caps, and active Macro IOC stay off, so the paired
	// comparison isolates the complete continuation posterior as a target.
	candidate.MacroInventory.Enabled = true
	candidate.MacroInventory.NoTradeRegion.Enabled = true
	// Isolate the continuation posterior from the other Macro/no-trade
	// controllers. In particular, do not let inherited live hold protection,
	// downside-risk mode, or HAR variance turn this paired arm into a bundled
	// Macro experiment and create an unexplained fill collapse.
	candidate.MacroInventory.NoTradeRegion.DownsideRiskControlEnabled = false
	candidate.MacroInventory.NoTradeRegion.HoldProtectionEnabled = false
	candidate.MacroInventory.NoTradeRegion.FastVarianceRiskEnabled = false
	candidate.MacroInventory.NoTradeRegion.TrendExcursionEnabled = false
	candidate.MacroInventory.NoTradeRegion.ContinuationEnabled = false
	candidate.MacroInventory.NoTradeRegion.ContinuationMixtureEnabled = true
	return candidate
}

func continuationFastOnlyCandidateConfigWithPriorStrength(cfg gammacapture.MarketMakerConfig, priorStrength float64) gammacapture.MarketMakerConfig {
	candidate := continuationFastOnlyCandidateConfig(cfg)
	if priorStrength > 0 {
		candidate.MacroInventory.PriorStrength = priorStrength
	}
	return candidate
}

func continuationFastOnlyConfigView(cfg gammacapture.MarketMakerConfig) continuationFastOnlyWFOConfigView {
	return continuationFastOnlyWFOConfigView{
		MacroInventoryEnabled: cfg.MacroInventory.Enabled,
		NoTradeEnabled:        cfg.MacroInventory.NoTradeRegion.Enabled,
		ContinuationMixture:   cfg.MacroInventory.Enabled && cfg.MacroInventory.NoTradeRegion.ContinuationMixtureEnabled,
		ActiveIOCEnabled:      cfg.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled,
		JointDistanceEnabled:  cfg.JointDistanceQuantity.Enabled && !cfg.JointDistanceQuantity.ShadowOnly,
	}
}

type replayBlockWindow struct {
	from, to time.Time
}

func continuationReplayBlockWindows(from, to time.Time, block time.Duration) []replayBlockWindow {
	if block <= 0 || !from.Before(to) {
		return nil
	}
	var windows []replayBlockWindow
	for cursor := from; cursor.Before(to); {
		end := cursor.Add(block)
		if end.After(to) {
			end = to
		}
		windows = append(windows, replayBlockWindow{from: cursor, to: end})
		cursor = end
	}
	return windows
}

func continuationReplayPointRange(curve []productionEquityPoint, from, to time.Time) (start, end productionEquityPoint, ok bool) {
	for _, point := range curve {
		if point.At.Before(from) {
			continue
		}
		if !point.At.Before(to) {
			break
		}
		if !ok {
			start = point
			ok = true
		}
		end = point
	}
	return start, end, ok
}

func continuationReplayBlockDrawdown(curve []productionEquityPoint, from, to time.Time) float64 {
	var peak float64
	var drawdown float64
	for _, point := range curve {
		if point.At.Before(from) || !point.At.Before(to) {
			continue
		}
		if peak == 0 || point.EquityJPY > peak {
			peak = point.EquityJPY
		}
		if peak > 0 {
			drawdown = math.Max(drawdown, 100*(peak-point.EquityJPY)/peak)
		}
	}
	return drawdown
}

func continuationReplayBlockFills(result productionReplayResult, from, to time.Time) int {
	fills := 0
	for _, fill := range result.FillEvents {
		if !fill.At.Before(from) && fill.At.Before(to) {
			fills++
		}
	}
	return fills
}

func continuationReplayBlocks(baseline, candidate productionReplayResult, from, to time.Time, block time.Duration) []continuationFastOnlyWFOBlock {
	windows := continuationReplayBlockWindows(from, to, block)
	blocks := make([]continuationFastOnlyWFOBlock, 0, len(windows))
	for _, window := range windows {
		baselineStart, baselineEnd, baselineOK := continuationReplayPointRange(baseline.EquityCurve, window.from, window.to)
		candidateStart, candidateEnd, candidateOK := continuationReplayPointRange(candidate.EquityCurve, window.from, window.to)
		if !baselineOK || !candidateOK {
			continue
		}
		baselinePnL := baselineEnd.EquityJPY - baselineStart.EquityJPY
		candidatePnL := candidateEnd.EquityJPY - candidateStart.EquityJPY
		holdPnL := baselineEnd.HoldEquityJPY - baselineStart.HoldEquityJPY
		blocks = append(blocks, continuationFastOnlyWFOBlock{
			From: window.from, To: window.to,
			BaselineNetPnLJPY:           baselinePnL,
			CandidateNetPnLJPY:          candidatePnL,
			HoldPnLJPY:                  holdPnL,
			BaselineExcessVsHoldJPY:     baselinePnL - holdPnL,
			CandidateExcessVsHoldJPY:    candidatePnL - holdPnL,
			CandidateDeltaVsBaselineJPY: candidatePnL - baselinePnL,
			BaselineFills:               continuationReplayBlockFills(baseline, window.from, window.to),
			CandidateFills:              continuationReplayBlockFills(candidate, window.from, window.to),
			BaselineMaximumDrawdownPct:  continuationReplayBlockDrawdown(baseline.EquityCurve, window.from, window.to),
			CandidateMaximumDrawdownPct: continuationReplayBlockDrawdown(candidate.EquityCurve, window.from, window.to),
		})
	}
	return blocks
}

func continuationMeanLower95(values []float64) (mean, lower float64) {
	if len(values) == 0 {
		return 0, 0
	}
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	if len(values) == 1 {
		return mean, mean
	}
	var sumSquares float64
	for _, value := range values {
		sumSquares += (value - mean) * (value - mean)
	}
	se := math.Sqrt(sumSquares / float64(len(values)-1) / float64(len(values)))
	return mean, mean - 1.6448536269514722*se
}

func runContinuationFastOnlyWFO(in continuationFastOnlyWFOInput) {
	if !in.From.Before(in.To) || in.Block <= 0 || in.BBOInterval <= 0 {
		fatalf("invalid continuation Fast-only WFO interval or sampling configuration")
	}
	barrier, intensity, productionCfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	baselineCfg := continuationFastOnlyBaselineConfig(productionCfg)
	candidateCfg := continuationFastOnlyCandidateConfigWithPriorStrength(productionCfg, in.ContinuationPriorStrength)
	// Candidate startup warmup is authoritative because it includes the
	// 240-hour continuation history. Both arms consume the same observations and
	// start scoring at the same timestamp.
	warmup := productionReplayWarmup(candidateCfg)
	warmFrom := in.From.Add(-warmup)
	books, trades, cacheHit := loadWarmReplayDatasetAtIntervals(
		in.DataPath, in.Symbol, warmFrom, in.To, in.From,
		replayConfigFingerprint(in.ConfigPath)+":continuation-fast-only-wfo",
		in.ReplayCacheDir, in.BBOInterval, in.BBOInterval)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("continuation Fast-only WFO has insufficient events: bbo=%d trades=%d", len(books), len(trades))
	}
	if err := validateSpotReplayStartingBalance(books, in.From, in.PairEquityJPY, in.StartingBase); err != nil {
		fatalf("invalid continuation Fast-only WFO starting balance: %v", err)
	}

	// Separate states run concurrently but never share estimators or account
	// state. This is the paired replay contract: identical market path, queue,
	// balances, warmup, and execution model.
	type armResult struct {
		name   string
		result productionReplayResult
	}
	results := make(chan armResult, 2)
	go func() {
		results <- armResult{name: "baseline", result: simulateProductionPolicyForComparisonWithPreloadOnly(
			books, trades, baselineCfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, warmFrom, in.From)}
	}()
	go func() {
		results <- armResult{name: "candidate", result: simulateProductionPolicyForComparisonWithPreloadOnly(
			books, trades, candidateCfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, warmFrom, in.From)}
	}()
	var baseline, candidate productionReplayResult
	for range 2 {
		result := <-results
		if result.name == "baseline" {
			baseline = result.result
		} else {
			candidate = result.result
		}
	}

	blocks := continuationReplayBlocks(baseline, candidate, in.From, in.To, in.Block)
	deltaValues := make([]float64, 0, len(blocks))
	excessValues := make([]float64, 0, len(blocks))
	positiveDelta, positiveExcess := 0, 0
	for _, block := range blocks {
		deltaValues = append(deltaValues, block.CandidateDeltaVsBaselineJPY)
		excessValues = append(excessValues, block.CandidateExcessVsHoldJPY)
		if block.CandidateDeltaVsBaselineJPY > 0 {
			positiveDelta++
		}
		if block.CandidateExcessVsHoldJPY > 0 {
			positiveExcess++
		}
	}
	deltaMean, deltaLower := continuationMeanLower95(deltaValues)
	excessMean, excessLower := continuationMeanLower95(excessValues)
	summary := continuationFastOnlyWFOSummary{
		MeanCandidateDeltaVsBaselineJPY: deltaMean,
		Lower95CandidateDeltaVsBaseline: deltaLower,
		MeanCandidateExcessVsHoldJPY:    excessMean,
		Lower95CandidateExcessVsHoldJPY: excessLower,
		PositiveDeltaBlocks:             positiveDelta,
		PositiveExcessBlocks:            positiveExcess,
		TotalBlocks:                     len(blocks),
	}
	decision := "reject-wfo"
	if len(blocks) >= 4 && deltaMean > 0 && deltaLower > 0 &&
		excessMean > 0 && excessLower > 0 &&
		candidate.NetPnLJPY > baseline.NetPnLJPY &&
		candidate.MaximumDrawdownPct <= baseline.MaximumDrawdownPct {
		decision = "pass-wfo-pending-private-fill-calibration"
	}
	report := continuationFastOnlyWFOReport{
		Mode:   "continuation-posterior-fast-only-paired-walk-forward",
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmFrom,
		Block: in.Block, BBOInterval: in.BBOInterval,
		PairEquityJPY: in.PairEquityJPY, StartingBase: in.StartingBase,
		QueueMultiplier: in.QueueMultiplier, ContinuationPriorStrength: in.ContinuationPriorStrength, ReplayCacheHit: cacheHit,
		BaselineConfig:  continuationFastOnlyConfigView(baselineCfg),
		CandidateConfig: continuationFastOnlyConfigView(candidateCfg),
		Baseline:        baseline, Candidate: candidate, Blocks: blocks, Summary: summary,
		PrivateFillCalibration: "not available: public aggregate trade replay only",
		Decision:               decision,
		Limitations: []string{
			"candidate enables the existing Macro target owner only inside this isolated replay; live Macro YAML remains disabled",
			"BBO interval applies to preload and score; use 1s for exact final replay, while the default 5s run is a computational screen",
			"queue multiplier is supplied, not calibrated from a private-fill ledger",
			"paired block statistics are dependent chronological blocks; lower95 is descriptive and not a multiple-testing correction",
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode continuation Fast-only WFO: %v", err)
	}
}
