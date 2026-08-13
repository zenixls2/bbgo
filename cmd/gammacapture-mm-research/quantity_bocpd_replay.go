//go:build ignore

package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

type quantityBOCPDComparisonInput struct {
	ConfigPath                  string
	DataPath                    string
	Symbol                      string
	From, To                    time.Time
	PairEquityJPY, StartingBase float64
	QueueMultiplier             float64
	ReplayCacheDir              string
	MaxDrawdownStopPct          float64
}

type quantityBOCPDSkillReport struct {
	Horizon                 time.Duration `json:"horizon"`
	Anchors                 int           `json:"anchors"`
	ReadyAnchors            int           `json:"readyAnchors"`
	ReadyCoveragePct        float64       `json:"readyCoveragePct"`
	DirectionalAccuracyPct  float64       `json:"directionalAccuracyPct"`
	BrierScore              float64       `json:"brierScore"`
	NeutralBrierScore       float64       `json:"neutralBrierScore"`
	BrierSkillVsNeutral     float64       `json:"brierSkillVsNeutral"`
	MeanBrierImprovement    float64       `json:"meanBrierImprovement"`
	BrierImprovementTStat   float64       `json:"brierImprovementTStatistic"`
	MeanUpProbability       float64       `json:"meanUpProbability"`
	MeanAbsTargetShiftRatio float64       `json:"meanAbsoluteTargetShiftRatio"`
	MeanChangeProbability   float64       `json:"meanChangeProbability"`
}

type quantityBOCPDComparisonReport struct {
	Symbol                         string `json:"symbol"`
	From, To                       time.Time
	WarmupFrom                     time.Time                `json:"warmupFrom"`
	StartingPairEquityJPY          float64                  `json:"startingPairEquityJPY"`
	StartingBase                   float64                  `json:"startingBase"`
	QueueMultiplier                float64                  `json:"queueMultiplier"`
	ReplayCacheHit                 bool                     `json:"replayCacheHit"`
	Hold                           replayHoldBenchmark      `json:"hold"`
	Baseline                       productionReplayResult   `json:"baseline"`
	BOCPD                          productionReplayResult   `json:"bocpd"`
	Hawkes                         productionReplayResult   `json:"hawkes"`
	HawkesSkill                    quantityBOCPDSkillReport `json:"hawkesSkill"`
	FusedSkill                     quantityBOCPDSkillReport `json:"fusedSkill"`
	BOCPDHawkes                    productionReplayResult   `json:"bocpdHawkes"`
	Skill                          quantityBOCPDSkillReport `json:"skill"`
	BaselineExcessLower95JPY       float64                  `json:"baselineExcessLower95JPYPerHour"`
	BOCPDExcessLower95JPY          float64                  `json:"bocpdExcessLower95JPYPerHour"`
	PairedMeanHourlyDeltaJPY       float64                  `json:"pairedMeanHourlyDeltaJPY"`
	PairedLower95HourlyJPY         float64                  `json:"pairedLower95HourlyJPY"`
	PairedHourlySamples            int                      `json:"pairedHourlySamples"`
	HawkesPairedMeanHourlyDeltaJPY float64                  `json:"hawkesPairedMeanHourlyDeltaJPY"`
	HawkesPairedLower95HourlyJPY   float64                  `json:"hawkesPairedLower95HourlyJPY"`
	HawkesPairedHourlySamples      int                      `json:"hawkesPairedHourlySamples"`
	FusedPairedMeanHourlyDeltaJPY  float64                  `json:"fusedPairedMeanHourlyDeltaJPY"`
	FusedPairedLower95HourlyJPY    float64                  `json:"fusedPairedLower95HourlyJPY"`
	FusedPairedHourlySamples       int                      `json:"fusedPairedHourlySamples"`
	Decision                       string                   `json:"decision"`
}

func runQuantityBOCPDComparison(in quantityBOCPDComparisonInput) {
	if !in.From.Before(in.To) || in.PairEquityJPY <= 0 || in.StartingBase < 0 || in.QueueMultiplier < 0 {
		fatalf("invalid BOCPD comparison interval, balances, or queue multiplier")
	}
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	warmupFrom := in.From.Add(-macroReplayWarmup(cfg))
	books, trades, cacheHit := loadMacroReplayDataset(
		in.DataPath, in.Symbol, warmupFrom, in.To, in.From,
		replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient BOCPD replay events: bbo=%d trades=%d", len(books), len(trades))
	}
	if err := validateSpotReplayStartingBalance(books, in.From, in.PairEquityJPY, in.StartingBase); err != nil {
		fatalf("invalid BOCPD starting balance: %v", err)
	}

	baselineCfg := cfg
	baselineCfg.QuantityBOCPD.Enabled = true
	baselineCfg.QuantityBOCPD.ShadowOnly = true
	candidateCfg := baselineCfg
	candidateCfg.QuantityBOCPD.ShadowOnly = false
	previousPosteriorMode := activeProductionQuantityPosteriorMode
	defer func() { activeProductionQuantityPosteriorMode = previousPosteriorMode }()
	activeProductionQuantityPosteriorMode = productionQuantityBOCPD
	baseline := simulateProductionPolicyWithQuantityProjection(
		books, trades, baselineCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier,
		in.From, true, in.MaxDrawdownStopPct)
	candidate := simulateProductionPolicyWithQuantityProjection(
		books, trades, candidateCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier,
		in.From, true, in.MaxDrawdownStopPct)
	activeProductionQuantityPosteriorMode = productionQuantityHawkes
	hawkes := simulateProductionPolicyWithQuantityProjection(
		books, trades, candidateCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier,
		in.From, true, in.MaxDrawdownStopPct)
	activeProductionQuantityPosteriorMode = productionQuantityBOCPDHawkes
	fused := simulateProductionPolicyWithQuantityProjection(
		books, trades, candidateCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier,
		in.From, true, in.MaxDrawdownStopPct)

	_, baselineLower, _ := pairedExcessLower95(baseline)
	_, candidateLower, _ := pairedExcessLower95(candidate)
	pairedMean, pairedLower, pairedN := pairedPolicyDeltaLower95(baseline, candidate)
	hawkesMean, hawkesLower, hawkesN := pairedPolicyDeltaLower95(baseline, hawkes)
	fusedMean, fusedLower, fusedN := pairedPolicyDeltaLower95(baseline, fused)
	decision := "reject promotion: no quantity posterior candidate improves fee-net PnL with a non-negative paired hourly lower bound"
	if pairedN > 1 && candidate.NetPnLJPY > baseline.NetPnLJPY &&
		pairedLower >= 0 && candidate.MaximumDrawdownPct <= baseline.MaximumDrawdownPct {
		decision = "BOCPD-only eligible for further holdout"
	}
	if hawkesN > 1 && hawkes.NetPnLJPY > baseline.NetPnLJPY &&
		hawkesLower >= 0 && hawkes.MaximumDrawdownPct <= baseline.MaximumDrawdownPct {
		decision = "Hawkes-only eligible for further holdout"
	}
	if fusedN > 1 && fused.NetPnLJPY > baseline.NetPnLJPY &&
		fusedLower >= 0 && fused.MaximumDrawdownPct <= baseline.MaximumDrawdownPct {
		decision = "BOCPD+Hawkes eligible for further holdout"
	}
	bocpdSkill, hawkesSkill, fusedSkill := evaluateQuantityPosteriorSkills(
		books, trades, baselineCfg.QuantityBOCPD, baselineCfg.HawkesDirection,
		baselineCfg.FastModelWindows(), in.From, in.To)
	report := quantityBOCPDComparisonReport{
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmupFrom,
		StartingPairEquityJPY: in.PairEquityJPY, StartingBase: in.StartingBase,
		QueueMultiplier: in.QueueMultiplier, ReplayCacheHit: cacheHit,
		Hold:     holdBenchmark(books, in.From, in.PairEquityJPY, in.StartingBase),
		Baseline: baseline, BOCPD: candidate, Hawkes: hawkes, BOCPDHawkes: fused,
		Skill:                    bocpdSkill,
		HawkesSkill:              hawkesSkill,
		FusedSkill:               fusedSkill,
		BaselineExcessLower95JPY: baselineLower, BOCPDExcessLower95JPY: candidateLower,
		PairedMeanHourlyDeltaJPY: pairedMean, PairedLower95HourlyJPY: pairedLower,
		PairedHourlySamples:            pairedN,
		HawkesPairedMeanHourlyDeltaJPY: hawkesMean,
		HawkesPairedLower95HourlyJPY:   hawkesLower,
		HawkesPairedHourlySamples:      hawkesN,
		FusedPairedMeanHourlyDeltaJPY:  fusedMean,
		FusedPairedLower95HourlyJPY:    fusedLower,
		FusedPairedHourlySamples:       fusedN, Decision: decision,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode BOCPD comparison: %v", err)
	}
}

func evaluateQuantityBOCPDSkill(
	books []bboSnapshot,
	config gammacapture.QuantityBOCPDConfig,
	fastWindows []time.Duration,
	from, to time.Time,
) quantityBOCPDSkillReport {
	horizon := time.Duration(config.ExpectedRunLength)
	if horizon <= 0 && len(fastWindows) > 0 {
		horizon = fastWindows[0]
	}
	if horizon <= 0 {
		horizon = 10 * time.Minute
	}
	config.Enabled = true
	config.ShadowOnly = true
	report := quantityBOCPDSkillReport{Horizon: horizon, NeutralBrierScore: 0.25}
	model := gammacapture.QuantityBOCPDModel{}
	nextAnchor := from
	previous := time.Time{}
	improvements := make([]float64, 0)
	correct := 0
	for index, book := range books {
		if book.time.After(to) {
			break
		}
		gap := !previous.IsZero() && book.time.Sub(previous) >= 15*time.Minute
		previous = book.time
		model.Observe(book.time, book.bid, book.ask, gap, config)
		if book.time.Before(from) || book.time.Before(nextAnchor) {
			continue
		}
		report.Anchors++
		nextAnchor = book.time.Add(horizon)
		decision := model.Decision(config)
		if !decision.Ready {
			continue
		}
		futureAt := book.time.Add(horizon)
		future := sort.Search(len(books)-index-1, func(offset int) bool {
			return !books[index+1+offset].time.Before(futureAt)
		})
		futureIndex := index + 1 + future
		if futureIndex >= len(books) || books[futureIndex].time.After(to) {
			continue
		}
		terminal := books[futureIndex]
		terminalReturn := 0.5 * (math.Log(terminal.ask/book.ask) + math.Log(terminal.bid/book.bid))
		label := 0.0
		if terminalReturn > 0 {
			label = 1
		}
		probability := math.Max(0, math.Min(1, decision.UpProbability))
		loss := (probability - label) * (probability - label)
		report.ReadyAnchors++
		report.BrierScore += loss
		report.MeanUpProbability += probability
		report.MeanAbsTargetShiftRatio += math.Abs(2*probability - 1)
		report.MeanChangeProbability += 0.5 * (decision.AskChangeProbability + decision.BidChangeProbability)
		improvements = append(improvements, 0.25-loss)
		if (probability >= 0.5) == (label == 1) {
			correct++
		}
	}
	if report.Anchors > 0 {
		report.ReadyCoveragePct = 100 * float64(report.ReadyAnchors) / float64(report.Anchors)
	}
	if report.ReadyAnchors > 0 {
		count := float64(report.ReadyAnchors)
		report.BrierScore /= count
		report.BrierSkillVsNeutral = 1 - report.BrierScore/report.NeutralBrierScore
		report.DirectionalAccuracyPct = 100 * float64(correct) / count
		report.MeanUpProbability /= count
		report.MeanAbsTargetShiftRatio /= count
		report.MeanChangeProbability /= count
	}
	report.MeanBrierImprovement, report.BrierImprovementTStat = pairedMeanT(improvements)
	return report
}

type quantityPosteriorSkillAccumulator struct {
	report       quantityBOCPDSkillReport
	improvements []float64
	correct      int
}

func (a *quantityPosteriorSkillAccumulator) observe(decision gammacapture.QuantityBOCPDDecision, label float64) {
	a.report.Anchors++
	if !decision.Ready {
		return
	}
	probability := math.Max(0, math.Min(1, decision.UpProbability))
	loss := (probability - label) * (probability - label)
	a.report.ReadyAnchors++
	a.report.BrierScore += loss
	a.report.MeanUpProbability += probability
	a.report.MeanAbsTargetShiftRatio += math.Abs(2*probability - 1)
	a.report.MeanChangeProbability += 0.5 * (decision.AskChangeProbability + decision.BidChangeProbability)
	a.improvements = append(a.improvements, 0.25-loss)
	if (probability >= 0.5) == (label == 1) {
		a.correct++
	}
}

func (a *quantityPosteriorSkillAccumulator) finish(horizon time.Duration) quantityBOCPDSkillReport {
	a.report.Horizon = horizon
	a.report.NeutralBrierScore = 0.25
	if a.report.Anchors > 0 {
		a.report.ReadyCoveragePct = 100 * float64(a.report.ReadyAnchors) / float64(a.report.Anchors)
	}
	if a.report.ReadyAnchors > 0 {
		count := float64(a.report.ReadyAnchors)
		a.report.BrierScore /= count
		a.report.BrierSkillVsNeutral = 1 - a.report.BrierScore/a.report.NeutralBrierScore
		a.report.DirectionalAccuracyPct = 100 * float64(a.correct) / count
		a.report.MeanUpProbability /= count
		a.report.MeanAbsTargetShiftRatio /= count
		a.report.MeanChangeProbability /= count
	}
	a.report.MeanBrierImprovement, a.report.BrierImprovementTStat =
		pairedMeanT(a.improvements)
	return a.report
}

func evaluateQuantityPosteriorSkills(
	books []bboSnapshot,
	trades []tick,
	config gammacapture.QuantityBOCPDConfig,
	hawkesConfig gammacapture.HawkesDirectionConfig,
	fastWindows []time.Duration,
	from, to time.Time,
) (quantityBOCPDSkillReport, quantityBOCPDSkillReport, quantityBOCPDSkillReport) {
	horizon := time.Duration(config.ExpectedRunLength)
	if horizon <= 0 && len(fastWindows) > 0 {
		horizon = fastWindows[0]
	}
	if horizon <= 0 {
		horizon = 10 * time.Minute
	}
	config.Enabled = true
	config.ShadowOnly = true
	hawkesConfig.Enabled = true
	bocpd := gammacapture.QuantityBOCPDModel{}
	hawkes := gammacapture.NewHawkesDirectionModel(hawkesConfig)
	bocpdSkill := quantityPosteriorSkillAccumulator{}
	hawkesSkill := quantityPosteriorSkillAccumulator{}
	fusedSkill := quantityPosteriorSkillAccumulator{}
	nextAnchor := from
	previous := time.Time{}
	tradeIndex := 0
	for index, book := range books {
		if book.time.After(to) {
			break
		}
		// Match production replay ordering: a BBO wins an exact timestamp tie.
		for tradeIndex < len(trades) && trades[tradeIndex].time.Before(book.time) {
			trade := trades[tradeIndex]
			hawkes.ObserveDirection(
				trade.time, trade.side != types.SideTypeSell, trade.price*trade.size)
			tradeIndex++
		}
		gap := !previous.IsZero() && book.time.Sub(previous) >= 15*time.Minute
		previous = book.time
		bocpd.Observe(book.time, book.bid, book.ask, gap, config)
		if book.time.Before(from) || book.time.Before(nextAnchor) {
			continue
		}
		nextAnchor = book.time.Add(horizon)
		futureAt := book.time.Add(horizon)
		futureOffset := sort.Search(len(books)-index-1, func(offset int) bool {
			return !books[index+1+offset].time.Before(futureAt)
		})
		futureIndex := index + 1 + futureOffset
		if futureIndex >= len(books) || books[futureIndex].time.After(to) {
			continue
		}
		terminal := books[futureIndex]
		terminalReturn := 0.5 * (math.Log(terminal.ask/book.ask) + math.Log(terminal.bid/book.bid))
		label := 0.0
		if terminalReturn > 0 {
			label = 1
		}
		bocpdDecision := bocpd.Decision(config)
		hawkesSnapshot := hawkes.Snapshot(book.time)
		hawkesDecision := gammacapture.QuantityPosteriorFromHawkes(hawkesSnapshot)
		fusedDecision := gammacapture.FuseQuantityBOCPDWithHawkes(
			bocpdDecision, hawkesSnapshot)
		bocpdSkill.observe(bocpdDecision, label)
		hawkesSkill.observe(hawkesDecision, label)
		fusedSkill.observe(fusedDecision, label)
	}
	return bocpdSkill.finish(horizon), hawkesSkill.finish(horizon), fusedSkill.finish(horizon)
}

type quantityPosteriorSkillStudyReport struct {
	Symbol         string `json:"symbol"`
	From, To       time.Time
	WarmupFrom     time.Time                `json:"warmupFrom"`
	ReplayCacheHit bool                     `json:"replayCacheHit"`
	BOCPD          quantityBOCPDSkillReport `json:"bocpd"`
	Hawkes         quantityBOCPDSkillReport `json:"hawkes"`
	Fused          quantityBOCPDSkillReport `json:"fused"`
}

func runQuantityPosteriorSkillStudy(in quantityBOCPDComparisonInput) {
	if !in.From.Before(in.To) {
		fatalf("invalid quantity posterior skill interval")
	}
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	warmupFrom := in.From.Add(-macroReplayWarmup(cfg))
	books, trades, cacheHit := loadMacroReplayDataset(
		in.DataPath, in.Symbol, warmupFrom, in.To, in.From,
		replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books = compactBBO(books)
	trades = compactTrades(trades)
	bocpd, hawkes, fused := evaluateQuantityPosteriorSkills(
		books, trades, cfg.QuantityBOCPD, cfg.HawkesDirection,
		cfg.FastModelWindows(), in.From, in.To)
	report := quantityPosteriorSkillStudyReport{
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmupFrom,
		ReplayCacheHit: cacheHit, BOCPD: bocpd, Hawkes: hawkes, Fused: fused,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode quantity posterior skill study: %v", err)
	}
}
