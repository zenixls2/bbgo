package main

import (
	"encoding/json"
	"os"
	"time"
)

type adaptivePathDecayWFOInput struct {
	ConfigPath                  string
	DataPath                    string
	Symbol                      string
	From, To                    time.Time
	PairEquityJPY, StartingBase float64
	QueueMultiplier             float64
	ReplayCacheDir              string
	Block                       time.Duration
	BBOInterval                 time.Duration
}

type adaptivePathDecayWFOReport struct {
	Mode                   string                         `json:"mode"`
	Symbol                 string                         `json:"symbol"`
	From                   time.Time                      `json:"from"`
	To                     time.Time                      `json:"to"`
	WarmupFrom             time.Time                      `json:"warmupFrom"`
	Block                  time.Duration                  `json:"block"`
	BBOInterval            time.Duration                  `json:"bboInterval"`
	PairEquityJPY          float64                        `json:"pairEquityJPY"`
	StartingBase           float64                        `json:"startingBase"`
	QueueMultiplier        float64                        `json:"queueMultiplier"`
	ReplayCacheHit         bool                           `json:"replayCacheHit"`
	BaselineAdaptive       bool                           `json:"baselineAdaptive"`
	CandidateAdaptive      bool                           `json:"candidateAdaptive"`
	Baseline               productionReplayResult         `json:"baseline"`
	Candidate              productionReplayResult         `json:"candidate"`
	Blocks                 []continuationFastOnlyWFOBlock `json:"blocks"`
	Summary                continuationFastOnlyWFOSummary `json:"summary"`
	PrivateFillCalibration string                         `json:"privateFillCalibration"`
	Decision               string                         `json:"decision"`
	Limitations            []string                       `json:"limitations"`
}

func runAdaptivePathDecayWFO(in adaptivePathDecayWFOInput) {
	if !in.From.Before(in.To) || in.Block <= 0 || in.BBOInterval <= 0 {
		fatalf("invalid adaptive path-decay WFO interval or sampling configuration")
	}
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	baselineCfg := cfg
	baselineCfg.JointDistanceQuantity.AdaptivePathDecay = false
	candidateCfg := cfg
	candidateCfg.JointDistanceQuantity.AdaptivePathDecay = true
	warmup := productionReplayWarmup(candidateCfg)
	warmFrom := in.From.Add(-warmup)
	books, trades, cacheHit := loadWarmReplayDatasetAtIntervals(
		in.DataPath, in.Symbol, warmFrom, in.To, in.From,
		replayConfigFingerprint(in.ConfigPath)+":adaptive-path-decay-wfo",
		in.ReplayCacheDir, in.BBOInterval, in.BBOInterval)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("adaptive path-decay WFO has insufficient events: bbo=%d trades=%d", len(books), len(trades))
	}
	if err := validateSpotReplayStartingBalance(books, in.From, in.PairEquityJPY, in.StartingBase); err != nil {
		fatalf("invalid adaptive path-decay WFO starting balance: %v", err)
	}
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
	decision := "reject-wfo"
	if len(blocks) >= 4 && deltaMean > 0 && deltaLower > 0 && candidate.NetPnLJPY > baseline.NetPnLJPY {
		decision = "pass-wfo-pending-private-fill-calibration"
	}
	report := adaptivePathDecayWFOReport{
		Mode: "adaptive-path-decay-paired-walk-forward", Symbol: in.Symbol,
		From: in.From, To: in.To, WarmupFrom: warmFrom, Block: in.Block,
		BBOInterval: in.BBOInterval, PairEquityJPY: in.PairEquityJPY,
		StartingBase: in.StartingBase, QueueMultiplier: in.QueueMultiplier,
		ReplayCacheHit: cacheHit, BaselineAdaptive: false, CandidateAdaptive: true,
		Baseline: baseline, Candidate: candidate, Blocks: blocks,
		Summary: continuationFastOnlyWFOSummary{
			MeanCandidateDeltaVsBaselineJPY: deltaMean,
			Lower95CandidateDeltaVsBaseline: deltaLower,
			MeanCandidateExcessVsHoldJPY:    excessMean,
			Lower95CandidateExcessVsHoldJPY: excessLower,
			PositiveDeltaBlocks:             positiveDelta,
			PositiveExcessBlocks:            positiveExcess,
			TotalBlocks:                     len(blocks),
		},
		PrivateFillCalibration: "not available: public aggregate trade replay only",
		Decision:               decision,
		Limitations: []string{
			"baseline and candidate differ only in JointDistanceQuantity.AdaptivePathDecay",
			"BBO interval applies to preload and score; use 1s for exact final replay",
			"queue multiplier is supplied, not calibrated from a private-fill ledger",
			"paired block statistics are dependent chronological blocks; lower95 is descriptive and not a multiple-testing correction",
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode adaptive path-decay WFO: %v", err)
	}
}
