package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

// This is a research-only component screen. It does not replay orders or
// change the live optimizer. It pairs the profit-only terminal-wealth sign
// with a matured, causal pivot-to-pivot executable outcome.
type terminalWealthPivotStudyInput struct {
	ConfigPath       string
	DataPath         string
	Symbol           string
	From             time.Time
	To               time.Time
	AnchorStep       time.Duration
	PivotReversalBps float64
	PivotMaxGap      time.Duration
	Horizons         []time.Duration
	PairEquityJPY    float64
	BBOInterval      time.Duration
}

type terminalWealthPivotStudyReport struct {
	Mode                 string                             `json:"mode"`
	Symbol               string                             `json:"symbol"`
	From                 time.Time                          `json:"from"`
	To                   time.Time                          `json:"to"`
	LoadedFrom           time.Time                          `json:"loadedFrom"`
	LoadedTo             time.Time                          `json:"loadedTo"`
	AnchorStep           string                             `json:"anchorStep"`
	PivotReversalBps     float64                            `json:"pivotReversalBps"`
	PivotMaxGap          string                             `json:"pivotMaxGap"`
	TerminalEntryCostBps float64                            `json:"terminalEntryCostBps"`
	QuoteDistanceBps     float64                            `json:"quoteDistanceBps"`
	PairEquityJPY        float64                            `json:"pairEquityJPY"`
	BBOEvents            int                                `json:"bboEvents"`
	Anchors              int                                `json:"anchors"`
	PivotConfirmations   int                                `json:"pivotConfirmations"`
	HorizonReports       []terminalWealthPivotHorizonReport `json:"horizons"`
	Warnings             []string                           `json:"warnings"`
}

type terminalWealthPivotHorizonReport struct {
	Horizon    string                         `json:"horizon"`
	Train      terminalWealthPivotSplitReport `json:"train"`
	Validation terminalWealthPivotSplitReport `json:"validation"`
	Holdout    terminalWealthPivotSplitReport `json:"holdout"`
	DeltaCE    terminalWealthDeltaCEReport    `json:"deltaCEHoldout"`
}

type terminalWealthDeltaCEReport struct {
	Convention                      string                         `json:"convention"`
	Eligible                        int                            `json:"eligible"`
	ProfitOnlyRiskAccepted          int                            `json:"profitOnlyRiskAcceptedAtDefaultCell"`
	TargetRelativeAccepted          int                            `json:"targetRelativeAcceptedAtDefaultCell"`
	CorrectedTargetRelativeAccepted int                            `json:"correctedTargetRelativeAcceptedAtDefaultCell"`
	RescuedAtDefaultCell            int                            `json:"rescuedAtDefaultCell"`
	RescuedAnywhereInGrid           int                            `json:"rescuedAnywhereInGrid"`
	CorrectedRescuedAtDefaultCell   int                            `json:"correctedRescuedAtDefaultCell"`
	CorrectedRescuedAnywhereInGrid  int                            `json:"correctedRescuedAnywhereInGrid"`
	MeanDefaultReliefBps            float64                        `json:"meanDefaultReliefBps"`
	DefaultReliefSEBps              float64                        `json:"defaultReliefSEBps"`
	MeanCorrectedDefaultReliefBps   float64                        `json:"meanCorrectedDefaultReliefBps"`
	CorrectedDefaultReliefSEBps     float64                        `json:"correctedDefaultReliefSEBps"`
	MeanConfidenceCorrectionBps     float64                        `json:"meanConfidenceCorrectionBps"`
	DefaultReliefEffectiveBlocks    int                            `json:"defaultReliefEffectiveBlocks"`
	DefaultReliefPositiveBlocks     int                            `json:"defaultReliefPositiveBlocks"`
	PositiveDefaultRelief           int                            `json:"positiveDefaultRelief"`
	CorrectedPositiveDefaultRelief  int                            `json:"correctedPositiveDefaultRelief"`
	Examples                        []terminalWealthDeltaCEExample `json:"examples"`
}

type terminalWealthDeltaCEExample struct {
	At                     time.Time                  `json:"at"`
	Direction              int                        `json:"pivotDirection"`
	RiskRepairDirection    int                        `json:"riskRepairDirection"`
	StartBid               float64                    `json:"startBid"`
	StartAsk               float64                    `json:"startAsk"`
	NextPivotOutcomeBps    float64                    `json:"nextPivotOutcomeBps"`
	FixedHorizonOutcomeBps float64                    `json:"fixedHorizonOutcomeBps"`
	ProfitOnlyAlphaCEBps   float64                    `json:"profitOnlyAlphaCEBps"`
	Rows                   []terminalWealthDeltaCERow `json:"rows"`
}

type terminalWealthDeltaCERow struct {
	QuantityRatio                     float64 `json:"quantityRatio"`
	InventoryDeltaRatio               float64 `json:"inventoryDeltaRatio"`
	ProfitOnlyRiskCEBps               float64 `json:"profitOnlyRiskCEBps"`
	TargetRelativeDeltaCEBps          float64 `json:"targetRelativeDeltaCEBps"`
	CorrectedTargetRelativeDeltaCEBps float64 `json:"correctedTargetRelativeDeltaCEBps"`
	ConfidenceCorrectionBps           float64 `json:"confidenceCorrectionBps"`
	RiskReliefImprovementBps          float64 `json:"riskReliefImprovementBps"`
	CorrectedRiskReliefImprovementBps float64 `json:"correctedRiskReliefImprovementBps"`
	Accepted                          bool    `json:"accepted"`
	CorrectedAccepted                 bool    `json:"correctedAccepted"`
}

type terminalWealthPivotSplitReport struct {
	From                         time.Time `json:"from"`
	To                           time.Time `json:"to"`
	Anchors                      int       `json:"anchors"`
	ReadyAnchors                 int       `json:"readyAnchors"`
	PivotResolved                int       `json:"pivotResolved"`
	TerminalObserved             int       `json:"terminalObserved"`
	TerminalMature               int       `json:"terminalMature"`
	TerminalAccepted             int       `json:"terminalAccepted"`
	TerminalRejected             int       `json:"terminalRejected"`
	PivotPositive                int       `json:"pivotPositive"`
	FixedHorizonPositive         int       `json:"fixedHorizonPositive"`
	ConfusionSamples             int       `json:"confusionSamples"`
	TruePositive                 int       `json:"truePositive"`
	FalsePositive                int       `json:"falsePositive"`
	TrueNegative                 int       `json:"trueNegative"`
	FalseNegative                int       `json:"falseNegative"`
	Accuracy                     float64   `json:"accuracy"`
	BalancedAccuracy             float64   `json:"balancedAccuracy"`
	PivotPositiveRate            float64   `json:"pivotPositiveRate"`
	MeanPivotBpsWhenAccepted     float64   `json:"meanPivotBpsWhenAccepted"`
	MeanPivotBpsWhenRejected     float64   `json:"meanPivotBpsWhenRejected"`
	MeanFixedHorizonBpsAccepted  float64   `json:"meanFixedHorizonBpsAccepted"`
	MeanFixedHorizonBpsRejected  float64   `json:"meanFixedHorizonBpsRejected"`
	RiskReliefObserved           int       `json:"riskReliefObserved"`
	RiskReliefAccepted           int       `json:"riskReliefAccepted"`
	RiskReliefPositiveWhenProfit int       `json:"riskReliefPositiveWhenProfit"`
	EffectiveBlocks              int       `json:"effectiveBlocks"`
	PositiveBlocks               int       `json:"positiveBlocks"`
	BlockPositiveRate            float64   `json:"blockPositiveRate"`
	MeanTerminalExpectedBps      float64   `json:"meanTerminalExpectedBps"`
	MeanTerminalLowerBps         float64   `json:"meanTerminalLowerBps"`
	MeanTerminalCEBps            float64   `json:"meanTerminalCEBps"`
	MeanRiskReliefCEBps          float64   `json:"meanRiskReliefCEBps"`
	EffectiveSamplesMean         float64   `json:"effectiveSamplesMean"`
}

type terminalWealthPivotAnchor struct {
	At                 time.Time
	StartBid, StartAsk float64
	Direction          int
	PivotReady         bool
	Stats              []gammacapture.JointPathPayoffStats
	Terminal           []terminalWealthScore
	RiskRelief         []terminalWealthScore
}

type terminalWealthScore struct {
	Observed     bool
	Mature       bool
	Accepted     bool
	ExpectedBps  float64
	LowerBps     float64
	CertaintyBps float64
	Effective    float64
}

type terminalWealthPivotEvent struct {
	At        time.Time
	PivotAt   time.Time
	Direction int
}

func runTerminalWealthPivotStudy(input terminalWealthPivotStudyInput) {
	if input.From.IsZero() || !input.From.Before(input.To) || input.AnchorStep <= 0 ||
		input.PivotReversalBps <= 0 || input.PivotMaxGap <= 0 || len(input.Horizons) == 0 {
		fatalf("invalid terminal-wealth pivot study configuration")
	}
	if input.PairEquityJPY <= 0 {
		input.PairEquityJPY = 7_255
	}
	_, _, cfg := loadProductionConfig(input.ConfigPath, input.Symbol)
	lookback := time.Duration(cfg.HorizonLookback)
	if lookback <= 0 {
		lookback = 6 * time.Hour
	}
	maxHorizon := input.Horizons[0]
	for _, horizon := range input.Horizons[1:] {
		if horizon > maxHorizon {
			maxHorizon = horizon
		}
	}
	loadedFrom := input.From.Add(-lookback - maxHorizon - time.Hour)
	loadedTo := input.To.Add(maxHorizon + input.AnchorStep + time.Hour)
	// ObserveBook itself updates the model at most once per second. Retaining
	// the last executable BBO in each second preserves that model clock while
	// avoiding repeated expensive conditional-state work on every tick.
	if input.BBOInterval <= 0 {
		input.BBOInterval = time.Second
	}
	books := compactBBOAtInterval(compactBBO(readBBO(input.DataPath, input.Symbol, loadedFrom, loadedTo)), input.BBOInterval)
	if len(books) < 10 {
		fatalf("insufficient BBO events for terminal-wealth pivot study: %d", len(books))
	}

	entryCost := cfg.MakerFeeBps + cfg.AdverseSelectionBps
	if entryCost < 0 || math.IsNaN(entryCost) || math.IsInf(entryCost, 0) {
		entryCost = 0
	}
	quoteDistance := cfg.MinimumHalfSpreadBps
	if quoteDistance <= 0 {
		quoteDistance = cfg.MakerFeeBps + cfg.AdverseSelectionBps + cfg.MinimumNetEdgeBps/2
	}
	if quoteDistance <= 0 {
		quoteDistance = 15
	}
	q := cfg.QuoteNotional
	if q <= 0 {
		q = 120
	}
	z := cfg.InventoryRiskZScore
	if z <= 0 || math.IsNaN(z) || math.IsInf(z, 0) {
		z = 1.645
	}
	riskAversion := cfg.FastRiskAversion
	if riskAversion <= 0 || math.IsNaN(riskAversion) || math.IsInf(riskAversion, 0) {
		riskAversion = 1
	}

	anchors, events := collectTerminalWealthPivotAnchors(
		books, input, cfg, quoteDistance, q, input.PairEquityJPY, riskAversion, z)
	report := terminalWealthPivotStudyReport{
		Mode: "standalone-causal-terminal-wealth-pivot-study", Symbol: input.Symbol,
		From: input.From, To: input.To, LoadedFrom: loadedFrom, LoadedTo: loadedTo,
		AnchorStep: input.AnchorStep.String(), PivotReversalBps: input.PivotReversalBps,
		PivotMaxGap: input.PivotMaxGap.String(), TerminalEntryCostBps: entryCost,
		QuoteDistanceBps: quoteDistance, PairEquityJPY: input.PairEquityJPY,
		BBOEvents: len(books), Anchors: len(anchors), PivotConfirmations: len(events),
		Warnings: []string{
			"Terminal wealth uses completed historical executable-BBO paths observed strictly before each anchor; it is not a fill or queue replay.",
			"The research replay retains the last valid executable BBO per configured interval; 1s matches MarketMakerHorizonModel's observation clock, while coarser intervals are sensitivity runs.",
			"Pivot labels are matured labels: confirmation can occur after the pivot extreme, but no confirmation or future extreme enters the anchor features.",
			"Profit-only acceptance uses terminal CE with current inventory equal to target. Risk-relief CE uses a synthetic 75/25 versus 50/50 inventory state and is sensitivity analysis, not an account reconstruction.",
			"The fixed-horizon label and next pivot-to-pivot label are reported separately because they answer different questions.",
		},
	}
	for index, horizon := range input.Horizons {
		holdoutFrom := input.From.Add(3 * input.To.Sub(input.From) / 4)
		report.HorizonReports = append(report.HorizonReports, terminalWealthPivotHorizonReport{
			Horizon:    horizon.String(),
			Train:      summarizeTerminalWealthPivotSplit(anchors, input.From, input.From.Add(input.To.Sub(input.From)/2), index, horizon, books, events, entryCost),
			Validation: summarizeTerminalWealthPivotSplit(anchors, input.From.Add(input.To.Sub(input.From)/2), input.From.Add(3*input.To.Sub(input.From)/4), index, horizon, books, events, entryCost),
			Holdout:    summarizeTerminalWealthPivotSplit(anchors, holdoutFrom, input.To, index, horizon, books, events, entryCost),
			DeltaCE:    summarizeTerminalWealthDeltaCE(anchors, holdoutFrom, input.To, index, horizon, books, events, q, input.PairEquityJPY, riskAversion, z, entryCost),
		})
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode terminal-wealth pivot study: %v", err)
	}
}

func collectTerminalWealthPivotAnchors(books []bboSnapshot, input terminalWealthPivotStudyInput, cfg gammacapture.MarketMakerConfig, quoteDistance, q, pairEquity, riskAversion, z float64) ([]terminalWealthPivotAnchor, []terminalWealthPivotEvent) {
	filter := gammacapture.NewPivotRegimeFilter(gammacapture.PivotRegimeConfig{
		ReversalBps: input.PivotReversalBps, MaxGap: input.PivotMaxGap,
		MinLegSamples: 2, PriorLegSamples: 2,
	})
	var model gammacapture.MarketMakerHorizonModel
	anchors := make([]terminalWealthPivotAnchor, 0)
	events := make([]terminalWealthPivotEvent, 0)
	anchorAt := input.From
	for _, book := range books {
		model.ObserveBook(book.time, book.bid, book.ask, cfg)
		decision := filter.Observe(gammacapture.PivotRegimeInput{At: book.time, ReferencePrice: book.midPrice()})
		if decision.PivotChanged {
			events = append(events, terminalWealthPivotEvent{At: decision.LastPivot.At, PivotAt: decision.LastPivot.PivotAt, Direction: decision.LastPivot.Direction})
		}
		if book.time.Before(anchorAt) || !anchorAt.Before(input.To) || decision.Direction == 0 || book.ask <= book.bid {
			continue
		}
		for anchorAt.Before(input.To) && !book.time.Before(anchorAt) {
			anchor := terminalWealthPivotAnchor{
				At: book.time, StartBid: book.bid, StartAsk: book.ask,
				Direction: decision.Direction, PivotReady: decision.Ready,
				Stats:      make([]gammacapture.JointPathPayoffStats, 0, len(input.Horizons)),
				Terminal:   make([]terminalWealthScore, 0, len(input.Horizons)),
				RiskRelief: make([]terminalWealthScore, 0, len(input.Horizons)),
			}
			for _, horizon := range input.Horizons {
				stats := model.JointPathPayoffStatistics(book.time, cfg, horizon, quoteDistance, quoteDistance)
				anchor.Stats = append(anchor.Stats, stats)
				anchor.Terminal = append(anchor.Terminal, terminalWealthSideScore(stats, decision.Direction, q, pairEquity, riskAversion, z, cfg))
				anchor.RiskRelief = append(anchor.RiskRelief, terminalWealthRiskReliefScore(stats, -decision.Direction, decision.Direction, q, pairEquity, riskAversion, z, cfg))
			}
			anchors = append(anchors, anchor)
			anchorAt = anchorAt.Add(input.AnchorStep)
		}
	}
	return anchors, events
}

func terminalWealthSideScore(stats gammacapture.JointPathPayoffStats, direction int, q, pairEquity, riskAversion, z float64, cfg gammacapture.MarketMakerConfig) terminalWealthScore {
	if direction == 0 || q <= 0 || pairEquity <= 0 || stats.EffectiveSamples <= 0 {
		return terminalWealthScore{}
	}
	buy, sell := 0.0, 0.0
	if direction > 0 {
		buy = q
	} else {
		sell = q
	}
	decision := stats.EvaluateTargetRelativePosition(0, 0, buy, sell, pairEquity, riskAversion, z)
	maturity := gammacapture.AssessJointPathMaturity(stats, cfg, z)
	return terminalWealthScore{
		Observed: true, Mature: maturity.Matured,
		Accepted:     maturity.Matured && decision.CertaintyEquivalent > 0,
		ExpectedBps:  decision.ExpectedPnLJPY / q * 10_000,
		LowerBps:     decision.LowerPnLJPY / q * 10_000,
		CertaintyBps: decision.CertaintyEquivalent / q * 10_000,
		Effective:    stats.EffectiveSamples,
	}
}

func terminalWealthRiskReliefScore(stats gammacapture.JointPathPayoffStats, actionDirection, pivotDirection int, q, pairEquity, riskAversion, z float64, cfg gammacapture.MarketMakerConfig) terminalWealthScore {
	if actionDirection == 0 || pivotDirection == 0 || q <= 0 || pairEquity <= 0 || stats.EffectiveSamples <= 0 {
		return terminalWealthScore{}
	}
	buy, sell := 0.0, 0.0
	if actionDirection > 0 {
		buy = q
	} else {
		sell = q
	}
	current := 0.5 * pairEquity
	if pivotDirection > 0 {
		current = 0.75 * pairEquity
	} else {
		current = 0.25 * pairEquity
	}
	decision := stats.EvaluateTargetRelativePosition(current, 0.5*pairEquity, buy, sell, pairEquity, riskAversion, z)
	maturity := gammacapture.AssessJointPathMaturity(stats, cfg, z)
	return terminalWealthScore{
		Observed: true, Mature: maturity.Matured,
		Accepted:     maturity.Matured && decision.CertaintyEquivalent > 0,
		ExpectedBps:  decision.ExpectedPnLJPY / q * 10_000,
		LowerBps:     decision.LowerPnLJPY / q * 10_000,
		CertaintyBps: decision.CertaintyEquivalent / q * 10_000,
		Effective:    stats.EffectiveSamples,
	}
}

func summarizeTerminalWealthPivotSplit(anchors []terminalWealthPivotAnchor, from, to time.Time, horizonIndex int, horizon time.Duration, books []bboSnapshot, events []terminalWealthPivotEvent, entryCost float64) terminalWealthPivotSplitReport {
	report := terminalWealthPivotSplitReport{From: from, To: to}
	blockValues := make(map[int][]float64)
	acceptedPivot, rejectedPivot := make([]float64, 0), make([]float64, 0)
	acceptedFixed, rejectedFixed := make([]float64, 0), make([]float64, 0)
	for _, anchor := range anchors {
		if anchor.At.Before(from) || !anchor.At.Before(to) {
			continue
		}
		report.Anchors++
		if !anchor.PivotReady || horizonIndex < 0 || horizonIndex >= len(anchor.Terminal) {
			continue
		}
		report.ReadyAnchors++
		score, riskScore := anchor.Terminal[horizonIndex], anchor.RiskRelief[horizonIndex]
		pivot, pivotOK := nextPivotOutcome(books, events, anchor, entryCost)
		fixed, fixedOK := fixedHorizonOutcome(books, anchor, horizon, entryCost)
		if pivotOK {
			report.PivotResolved++
			if pivot > 0 {
				report.PivotPositive++
			}
		}
		if fixedOK && fixed > 0 {
			report.FixedHorizonPositive++
		}
		if score.Observed {
			report.TerminalObserved++
			if score.Mature {
				report.TerminalMature++
			}
			if score.Accepted {
				report.TerminalAccepted++
			} else {
				report.TerminalRejected++
			}
			report.MeanTerminalExpectedBps += score.ExpectedBps
			report.MeanTerminalLowerBps += score.LowerBps
			report.MeanTerminalCEBps += score.CertaintyBps
			report.EffectiveSamplesMean += score.Effective
		}
		if riskScore.Observed {
			report.RiskReliefObserved++
			if riskScore.Accepted {
				report.RiskReliefAccepted++
			}
			if !score.Accepted && riskScore.Accepted {
				report.RiskReliefPositiveWhenProfit++
			}
			report.MeanRiskReliefCEBps += riskScore.CertaintyBps
		}
		if pivotOK && score.Observed {
			if score.Accepted {
				acceptedPivot = append(acceptedPivot, pivot)
			} else {
				rejectedPivot = append(rejectedPivot, pivot)
			}
			if fixedOK {
				if score.Accepted {
					acceptedFixed = append(acceptedFixed, fixed)
				} else {
					rejectedFixed = append(rejectedFixed, fixed)
				}
			}
			if pivot > 0 && score.Accepted {
				report.TruePositive++
			} else if pivot <= 0 && score.Accepted {
				report.FalsePositive++
			} else if pivot <= 0 {
				report.TrueNegative++
			} else {
				report.FalseNegative++
			}
			report.ConfusionSamples++
			block := int(anchor.At.Sub(from) / (6 * time.Hour))
			blockValues[block] = append(blockValues[block], pivot)
		}
	}
	if report.PivotResolved > 0 {
		report.PivotPositiveRate = float64(report.PivotPositive) / float64(report.PivotResolved)
	}
	if report.ConfusionSamples > 0 {
		report.Accuracy = float64(report.TruePositive+report.TrueNegative) / float64(report.ConfusionSamples)
	}
	positive := report.TruePositive + report.FalseNegative
	negative := report.TrueNegative + report.FalsePositive
	tpr, tnr := 0.0, 0.0
	if positive > 0 {
		tpr = float64(report.TruePositive) / float64(positive)
	}
	if negative > 0 {
		tnr = float64(report.TrueNegative) / float64(negative)
	}
	report.BalancedAccuracy = (tpr + tnr) / 2
	report.MeanPivotBpsWhenAccepted = meanFloat64(acceptedPivot)
	report.MeanPivotBpsWhenRejected = meanFloat64(rejectedPivot)
	report.MeanFixedHorizonBpsAccepted = meanFloat64(acceptedFixed)
	report.MeanFixedHorizonBpsRejected = meanFloat64(rejectedFixed)
	if report.TerminalObserved > 0 {
		report.MeanTerminalExpectedBps /= float64(report.TerminalObserved)
		report.MeanTerminalLowerBps /= float64(report.TerminalObserved)
		report.MeanTerminalCEBps /= float64(report.TerminalObserved)
		report.EffectiveSamplesMean /= float64(report.TerminalObserved)
	}
	if report.RiskReliefObserved > 0 {
		report.MeanRiskReliefCEBps /= float64(report.RiskReliefObserved)
	}
	for _, values := range blockValues {
		if len(values) == 0 {
			continue
		}
		report.EffectiveBlocks++
		if meanFloat64(values) > 0 {
			report.PositiveBlocks++
		}
	}
	if report.EffectiveBlocks > 0 {
		report.BlockPositiveRate = float64(report.PositiveBlocks) / float64(report.EffectiveBlocks)
	}
	return report
}

func summarizeTerminalWealthDeltaCE(anchors []terminalWealthPivotAnchor, from, to time.Time, horizonIndex int, horizon time.Duration, books []bboSnapshot, events []terminalWealthPivotEvent, quoteNotional, pairEquity, riskAversion, z, entryCost float64) terminalWealthDeltaCEReport {
	report := terminalWealthDeltaCEReport{
		Convention: "oldDeltaCE uses the current incremental-order SE; correctedDeltaCE subtracts the no-order baseline lower-bound term: mean - z*(sqrt(wholeVariance/N)-sqrt(baselineVariance/N)) - Kelly marginal variance",
		Examples:   make([]terminalWealthDeltaCEExample, 0, 3),
	}
	if quoteNotional <= 0 || pairEquity <= 0 {
		return report
	}
	quantityRatios := []float64{0.5, 1, 2}
	inventoryRatios := []float64{0, 0.25, 0.5}
	defaultReliefs := make([]float64, 0)
	correctedDefaultReliefs := make([]float64, 0)
	blockReliefs := make(map[int][]float64)
	type candidate struct {
		example terminalWealthDeltaCEExample
		score   float64
		relief  float64
	}
	candidates := make([]candidate, 0)
	for _, anchor := range anchors {
		if anchor.At.Before(from) || !anchor.At.Before(to) || !anchor.PivotReady ||
			horizonIndex < 0 || horizonIndex >= len(anchor.Stats) || horizonIndex >= len(anchor.Terminal) {
			continue
		}
		terminal := anchor.Terminal[horizonIndex]
		if !terminal.Observed || !terminal.Mature {
			continue
		}
		// Keep the old false-negative cohort fixed after the production CE
		// changes. Otherwise the corrected replay would remove its own newly
		// accepted rows before comparing old versus new.
		if terminalWealthRiskCEBps(
			anchor.Stats[horizonIndex], anchor.Direction,
			0.5*pairEquity, 0.5*pairEquity, quoteNotional,
			pairEquity, riskAversion, z) > 0 {
			continue
		}
		pivot, pivotOK := nextPivotOutcome(books, events, anchor, entryCost)
		if !pivotOK || pivot <= 0 {
			continue
		}
		fixed, _ := fixedHorizonOutcome(books, anchor, horizon, entryCost)
		stats := anchor.Stats[horizonIndex]
		riskDirection := -anchor.Direction
		example := terminalWealthDeltaCEExample{
			At: anchor.At, Direction: anchor.Direction, RiskRepairDirection: riskDirection,
			StartBid: anchor.StartBid, StartAsk: anchor.StartAsk,
			NextPivotOutcomeBps: pivot, FixedHorizonOutcomeBps: fixed,
			ProfitOnlyAlphaCEBps: terminal.CertaintyBps,
			Rows:                 make([]terminalWealthDeltaCERow, 0, len(quantityRatios)*len(inventoryRatios)),
		}
		maxTargetCE := math.Inf(-1)
		maxRelief := math.Inf(-1)
		defaultProfit, defaultTarget := 0.0, 0.0
		defaultCorrected := 0.0
		for _, quantityRatio := range quantityRatios {
			for _, inventoryRatio := range inventoryRatios {
				quantity := quoteNotional * quantityRatio
				profitOnly := terminalWealthRiskCEBps(stats, riskDirection, 0.5*pairEquity, 0.5*pairEquity, quantity, pairEquity, riskAversion, z)
				current := 0.5*pairEquity + float64(anchor.Direction)*inventoryRatio*pairEquity
				targetRelative := terminalWealthRiskCEBps(stats, riskDirection, current, 0.5*pairEquity, quantity, pairEquity, riskAversion, z)
				correctedTargetRelative := terminalWealthCorrectedRiskCEBps(stats, riskDirection, current, 0.5*pairEquity, quantity, pairEquity, riskAversion, z)
				improvement := targetRelative - profitOnly
				correctedImprovement := correctedTargetRelative - profitOnly
				row := terminalWealthDeltaCERow{
					QuantityRatio: quantityRatio, InventoryDeltaRatio: inventoryRatio,
					ProfitOnlyRiskCEBps: profitOnly, TargetRelativeDeltaCEBps: targetRelative,
					CorrectedTargetRelativeDeltaCEBps: correctedTargetRelative,
					ConfidenceCorrectionBps:           correctedTargetRelative - targetRelative,
					RiskReliefImprovementBps:          improvement,
					CorrectedRiskReliefImprovementBps: correctedImprovement,
					Accepted:                          targetRelative > 0, CorrectedAccepted: correctedTargetRelative > 0,
				}
				example.Rows = append(example.Rows, row)
				if inventoryRatio == 0.25 && quantityRatio == 1 {
					defaultProfit, defaultTarget, defaultCorrected = profitOnly, targetRelative, correctedTargetRelative
				}
				if correctedTargetRelative > maxTargetCE {
					maxTargetCE = correctedTargetRelative
				}
				if correctedImprovement > maxRelief {
					maxRelief = correctedImprovement
				}
			}
		}
		report.Eligible++
		report.ProfitOnlyRiskAccepted += boolToInt(defaultProfit > 0)
		report.TargetRelativeAccepted += boolToInt(defaultTarget > 0)
		report.CorrectedTargetRelativeAccepted += boolToInt(defaultCorrected > 0)
		report.RescuedAtDefaultCell += boolToInt(defaultProfit <= 0 && defaultTarget > 0)
		report.RescuedAnywhereInGrid += boolToInt(anyDeltaCERowRescue(example.Rows))
		report.CorrectedRescuedAtDefaultCell += boolToInt(defaultProfit <= 0 && defaultCorrected > 0)
		report.CorrectedRescuedAnywhereInGrid += boolToInt(anyCorrectedDeltaCERowRescue(example.Rows))
		report.MeanDefaultReliefBps += defaultTarget - defaultProfit
		report.MeanCorrectedDefaultReliefBps += defaultCorrected - defaultProfit
		report.MeanConfidenceCorrectionBps += defaultCorrected - defaultTarget
		report.PositiveDefaultRelief += boolToInt(defaultTarget-defaultProfit > 0)
		report.CorrectedPositiveDefaultRelief += boolToInt(defaultCorrected-defaultProfit > 0)
		defaultRelief := defaultTarget - defaultProfit
		defaultReliefs = append(defaultReliefs, defaultRelief)
		correctedDefaultReliefs = append(correctedDefaultReliefs, defaultCorrected-defaultProfit)
		block := int(anchor.At.Sub(from) / (6 * time.Hour))
		blockReliefs[block] = append(blockReliefs[block], defaultRelief)
		candidates = append(candidates, candidate{example: example, score: maxTargetCE, relief: maxRelief})
	}
	if report.Eligible > 0 {
		report.MeanDefaultReliefBps /= float64(report.Eligible)
		report.MeanCorrectedDefaultReliefBps /= float64(report.Eligible)
		report.MeanConfidenceCorrectionBps /= float64(report.Eligible)
	}
	if len(defaultReliefs) > 1 {
		mean := meanFloat64(defaultReliefs)
		variance := 0.0
		for _, value := range defaultReliefs {
			variance += (value - mean) * (value - mean)
		}
		variance /= float64(len(defaultReliefs) - 1)
		report.DefaultReliefSEBps = math.Sqrt(variance / float64(len(defaultReliefs)))
	}
	report.CorrectedDefaultReliefSEBps = sampleMeanSE(correctedDefaultReliefs)
	for _, values := range blockReliefs {
		if len(values) == 0 {
			continue
		}
		report.DefaultReliefEffectiveBlocks++
		if meanFloat64(values) > 0 {
			report.DefaultReliefPositiveBlocks++
		}
	}
	seen := make(map[time.Time]struct{})
	appendExample := func(item candidate) {
		if len(report.Examples) >= 3 {
			return
		}
		if _, exists := seen[item.example.At]; exists {
			return
		}
		seen[item.example.At] = struct{}{}
		report.Examples = append(report.Examples, item.example)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	for _, item := range candidates {
		appendExample(item)
		if len(report.Examples) >= 1 {
			break
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].relief > candidates[j].relief })
	for _, item := range candidates {
		appendExample(item)
		if len(report.Examples) >= 2 {
			break
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score < candidates[j].score })
	for _, item := range candidates {
		appendExample(item)
		if len(report.Examples) >= 3 {
			break
		}
	}
	return report
}

func terminalWealthRiskCEBps(stats gammacapture.JointPathPayoffStats, actionDirection int, current, target, quantity, pairEquity, riskAversion, z float64) float64 {
	decision, ok := terminalWealthRiskDecision(stats, actionDirection, current, target, quantity, pairEquity, riskAversion, z)
	if !ok {
		return 0
	}
	return decision.CertaintyEquivalent / quantity * 10_000
}

// terminalWealthCorrectedRiskCEBps computes the candidate-vs-no-order lower-
// bound difference for the historical formula audit. Production currently
// uses terminalWealthRiskCEBps, which intentionally retains the incremental
// lower-bound contract after the rollback.
func terminalWealthCorrectedRiskCEBps(stats gammacapture.JointPathPayoffStats, actionDirection int, current, target, quantity, pairEquity, riskAversion, z float64) float64 {
	decision, ok := terminalWealthRiskDecision(stats, actionDirection, current, target, quantity, pairEquity, riskAversion, z)
	if !ok {
		return 0
	}
	effective := math.Max(1, stats.EffectiveSamples)
	baselineSE := math.Sqrt(math.Max(0, decision.BaselineVarianceJPY2) / effective)
	wholeSE := math.Sqrt(math.Max(0, decision.WholePositionVarianceJPY2) / effective)
	correctedJPY := decision.ExpectedPnLJPY - math.Max(0, z)*(wholeSE-baselineSE) - decision.KellyPenaltyJPY
	return correctedJPY / quantity * 10_000
}

func terminalWealthRiskDecision(stats gammacapture.JointPathPayoffStats, actionDirection int, current, target, quantity, pairEquity, riskAversion, z float64) (gammacapture.JointPathPayoffDecision, bool) {
	if actionDirection == 0 || quantity <= 0 || pairEquity <= 0 || stats.EffectiveSamples <= 0 {
		return gammacapture.JointPathPayoffDecision{}, false
	}
	buy, sell := 0.0, 0.0
	if actionDirection > 0 {
		buy = quantity
	} else {
		sell = quantity
	}
	decision := stats.EvaluateTargetRelativePosition(current, target, buy, sell, pairEquity, riskAversion, z)
	return decision, true
}

func anyDeltaCERowRescue(rows []terminalWealthDeltaCERow) bool {
	for _, row := range rows {
		if row.ProfitOnlyRiskCEBps <= 0 && row.TargetRelativeDeltaCEBps > 0 {
			return true
		}
	}
	return false
}

func anyCorrectedDeltaCERowRescue(rows []terminalWealthDeltaCERow) bool {
	for _, row := range rows {
		if row.ProfitOnlyRiskCEBps <= 0 && row.CorrectedTargetRelativeDeltaCEBps > 0 {
			return true
		}
	}
	return false
}

func sampleMeanSE(values []float64) float64 {
	if len(values) <= 1 {
		return 0
	}
	mean := meanFloat64(values)
	variance := 0.0
	for _, value := range values {
		variance += (value - mean) * (value - mean)
	}
	variance /= float64(len(values) - 1)
	return math.Sqrt(variance / float64(len(values)))
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nextPivotOutcome(books []bboSnapshot, events []terminalWealthPivotEvent, anchor terminalWealthPivotAnchor, entryCost float64) (float64, bool) {
	for _, event := range events {
		if !event.At.After(anchor.At) || event.Direction != anchor.Direction || !event.PivotAt.After(anchor.At) {
			continue
		}
		index := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(event.PivotAt) })
		if index >= len(books) || books[index].bid <= 0 || books[index].ask <= books[index].bid {
			return 0, false
		}
		book := books[index]
		return directionalExecutableOutcome(anchor.StartBid, anchor.StartAsk, book.bid, book.ask, anchor.Direction, entryCost), true
	}
	return 0, false
}

func fixedHorizonOutcome(books []bboSnapshot, anchor terminalWealthPivotAnchor, horizon time.Duration, entryCost float64) (float64, bool) {
	index := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(anchor.At.Add(horizon)) })
	if index >= len(books) || books[index].bid <= 0 || books[index].ask <= books[index].bid {
		return 0, false
	}
	book := books[index]
	return directionalExecutableOutcome(anchor.StartBid, anchor.StartAsk, book.bid, book.ask, anchor.Direction, entryCost), true
}

func directionalExecutableOutcome(startBid, startAsk, endBid, endAsk float64, direction int, costBps float64) float64 {
	if direction > 0 && startAsk > 0 && endBid > 0 {
		return math.Log(endBid/startAsk)*10_000 - costBps
	}
	if direction < 0 && startBid > 0 && endAsk > 0 {
		return -math.Log(endAsk/startBid)*10_000 - costBps
	}
	return 0
}

func parseDurationList(value string) []time.Duration {
	result := make([]time.Duration, 0)
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		duration, err := time.ParseDuration(token)
		if err != nil || duration <= 0 {
			fatalf("invalid duration %q in --terminal-wealth-pivot-horizons", token)
		}
		result = append(result, duration)
	}
	return result
}
