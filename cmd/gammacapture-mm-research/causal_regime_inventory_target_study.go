package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

// This study is deliberately an action-level screen.  It uses the causal
// pivot state to choose a continuous inventory target and scores the target
// against the next confirmed pivot.  It does not pretend to be a private-fill
// backtest; the production replay remains the required next stage.
type causalRegimeInventoryTargetStudyInput struct {
	DataPath         string
	Symbol           string
	From, TrainTo    time.Time
	ValidationTo, To time.Time
	AnchorStep       time.Duration
	PivotReversalBps float64
	PivotMaxGap      time.Duration
	OneWayCostBps    float64
	RiskAversion     float64
	PriorStrengthBps float64
}

type causalRegimeInventoryTargetStudyReport struct {
	Mode               string                                 `json:"mode"`
	Symbol             string                                 `json:"symbol"`
	From               time.Time                              `json:"from"`
	To                 time.Time                              `json:"to"`
	LoadedFrom         time.Time                              `json:"loadedFrom"`
	LoadedTo           time.Time                              `json:"loadedTo"`
	AnchorStep         string                                 `json:"anchorStep"`
	PivotReversalBps   float64                                `json:"pivotReversalBps"`
	PivotMaxGap        string                                 `json:"pivotMaxGap"`
	OneWayCostBps      float64                                `json:"oneWayCostBps"`
	RiskAversion       float64                                `json:"riskAversion"`
	PriorStrengthBps   float64                                `json:"priorStrengthBps"`
	BBOEvents          int                                    `json:"bboEvents"`
	PivotConfirmations int                                    `json:"pivotConfirmations"`
	Anchors            int                                    `json:"anchors"`
	Train              causalRegimeInventoryTargetSplitReport `json:"train"`
	Validation         causalRegimeInventoryTargetSplitReport `json:"validation"`
	Holdout            causalRegimeInventoryTargetSplitReport `json:"holdout"`
	Warnings           []string                               `json:"warnings"`
}

type causalRegimeInventoryTargetSplitReport struct {
	From                      time.Time `json:"from"`
	To                        time.Time `json:"to"`
	Anchors                   int       `json:"anchors"`
	ReadyAnchors              int       `json:"readyAnchors"`
	OutcomeAnchors            int       `json:"outcomeAnchors"`
	ActiveAnchors             int       `json:"activeAnchors"`
	FullLongTargets           int       `json:"fullLongTargets"`
	FullFlatTargets           int       `json:"fullFlatTargets"`
	MeanTargetWeight          float64   `json:"meanTargetWeight"`
	MeanTargetDeltaWeight     float64   `json:"meanTargetDeltaWeight"`
	MeanIncrementalGrossBps   float64   `json:"meanIncrementalGrossBps"`
	MeanIncrementalNetBps     float64   `json:"meanIncrementalNetBps"`
	ActiveMeanNetBps          float64   `json:"activeMeanNetBps"`
	LegacyActiveAnchors       int       `json:"legacyActiveAnchors"`
	LegacyMeanNetBps          float64   `json:"legacyMeanNetBps"`
	LegacyActiveMeanNetBps    float64   `json:"legacyActiveMeanNetBps"`
	LegacyPositiveBlocks      int       `json:"legacyPositiveBlocks"`
	CandidateDeltaVsLegacyBps float64   `json:"candidateDeltaVsLegacyBps"`
	PositiveAnchors           int       `json:"positiveAnchors"`
	PositiveBlocks            int       `json:"positiveBlocks"`
	TotalBlocks               int       `json:"totalBlocks"`
	MaxCumulativeDrawdownBps  float64   `json:"maxCumulativeDrawdownBps"`
	CumulativeNetBps          float64   `json:"cumulativeNetBps"`
	LegacyMaxDrawdownBps      float64   `json:"legacyMaxCumulativeDrawdownBps"`
	LegacyCumulativeNetBps    float64   `json:"legacyCumulativeNetBps"`
}

type causalRegimeTargetStudyAnchor struct {
	at                                    time.Time
	bid, ask                              float64
	decision                              gammacapture.PivotRegimeDecision
	target                                gammacapture.CausalRegimeInventoryTargetDecision
	legacyTarget                          float64
	futureLongReturn, futureReducedReturn float64
	validOutcome                          bool
}

type causalRegimeTargetStudyPivot struct {
	at      time.Time
	pivotAt time.Time
	price   float64
}

type causalRegimeTargetMagnitudeStats struct {
	count      int
	sum        float64
	sumSquares float64
}

func (s *causalRegimeTargetMagnitudeStats) add(value float64) {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	s.count++
	s.sum += value
	s.sumSquares += value * value
}

func (s causalRegimeTargetMagnitudeStats) variance() float64 {
	if s.count < 2 {
		return 0
	}
	mean := s.sum / float64(s.count)
	return math.Max(0, (s.sumSquares-s.sum*mean)/float64(s.count-1))
}

func runCausalRegimeInventoryTargetStudy(in causalRegimeInventoryTargetStudyInput) {
	if !in.From.Before(in.To) || in.AnchorStep <= 0 || in.PivotReversalBps <= 0 ||
		in.PivotMaxGap <= 0 || in.OneWayCostBps < 0 || in.RiskAversion <= 0 ||
		in.PriorStrengthBps <= 0 {
		fatalf("invalid causal regime inventory target study configuration")
	}
	if in.TrainTo.IsZero() || in.ValidationTo.IsZero() {
		duration := in.To.Sub(in.From)
		in.TrainTo = in.From.Add(duration / 2)
		in.ValidationTo = in.From.Add(3 * duration / 4)
	}
	if !in.From.Before(in.TrainTo) || !in.TrainTo.Before(in.ValidationTo) ||
		!in.ValidationTo.Before(in.To) {
		fatalf("causal regime target study requires from < train-to < validation-to < to")
	}

	loadedFrom := in.From.Add(-24 * time.Hour)
	loadedTo := in.To.Add(24 * time.Hour)
	books := compactBBO(readBBO(in.DataPath, in.Symbol, loadedFrom, loadedTo))
	if len(books) < 10 {
		fatalf("insufficient BBO events for causal regime target study: %d", len(books))
	}
	sampled := compactBBOAtInterval(append([]bboSnapshot(nil), books...), in.AnchorStep)
	filter := gammacapture.NewPivotRegimeFilter(gammacapture.PivotRegimeConfig{
		ReversalBps:     in.PivotReversalBps,
		MaxGap:          in.PivotMaxGap,
		MinLegSamples:   2,
		PriorLegSamples: 2,
	})
	config := gammacapture.CausalRegimeInventoryTargetConfig{
		RiskAversion: in.RiskAversion, PriorStrengthBps: in.PriorStrengthBps,
	}
	stats := [2]causalRegimeTargetMagnitudeStats{}
	events := make([]causalRegimeTargetStudyPivot, 0)
	anchors := make([]causalRegimeTargetStudyAnchor, 0, len(sampled))
	sampledIndex := 0
	for _, book := range books {
		decision := filter.Observe(gammacapture.PivotRegimeInput{
			At: book.time, ReferencePrice: book.midPrice(),
		})
		if decision.PivotChanged {
			event := decision.LastPivot
			events = append(events, causalRegimeTargetStudyPivot{
				at: event.At, pivotAt: event.PivotAt, price: event.Price,
			})
			index := 0
			if event.Direction < 0 {
				index = 1
			}
			stats[index].add(event.LegAmplitudeBps)
		}
		if sampledIndex >= len(sampled) || !book.time.Equal(sampled[sampledIndex].time) {
			continue
		}
		if !book.time.Before(in.From) && book.time.Before(in.To) {
			variance := 0.0
			index := 0
			if decision.Direction < 0 {
				index = 1
			}
			variance = stats[index].variance()
			pivotInput, ready := gammacapture.BuildCausalRegimeInventoryTargetInputFromPivot(
				decision, 0.5, 0.5, 0, 1, variance, in.OneWayCostBps,
			)
			target := gammacapture.CausalRegimeInventoryTargetDecision{
				TargetWeight: 0.5,
				Reason:       "pivot target input not ready",
			}
			if ready {
				target = gammacapture.EvaluateCausalRegimeInventoryTarget(config, pivotInput)
			}
			legacyTarget := 0.5
			if decision.Ready {
				legacySizing := gammacapture.EvaluatePivotRegimeSizing(gammacapture.PivotRegimeSizingInput{
					Decision: decision, CostBps: in.OneWayCostBps,
					MaxTargetShift: 0.20,
				})
				if legacySizing.Applied {
					legacyTarget = 0.5 + legacySizing.TargetShiftRatio
				}
			}
			anchor := causalRegimeTargetStudyAnchor{
				at: book.time, bid: book.bid, ask: book.ask,
				decision: decision, target: target, legacyTarget: legacyTarget,
			}
			anchors = append(anchors, anchor)
		}
		sampledIndex++
	}
	// Attach labels only after the causal pass has finished.  The target was
	// already computed online above; this second pass is evaluation-only and
	// therefore cannot leak a future pivot into the forecast.
	for index := range anchors {
		if !anchors[index].target.Ready {
			continue
		}
		anchors[index].futureLongReturn, anchors[index].futureReducedReturn, anchors[index].validOutcome = causalRegimeTargetFutureReturn(
			books, events, anchors[index].at,
		)
	}
	if len(anchors) == 0 {
		fatalf("causal regime target study produced no anchors")
	}
	report := causalRegimeInventoryTargetStudyReport{
		Mode:   "standalone-causal-pivot-regime-inventory-target-study",
		Symbol: in.Symbol, From: in.From, To: in.To,
		LoadedFrom: loadedFrom, LoadedTo: loadedTo,
		AnchorStep: in.AnchorStep.String(), PivotReversalBps: in.PivotReversalBps,
		PivotMaxGap: in.PivotMaxGap.String(), OneWayCostBps: in.OneWayCostBps,
		RiskAversion: in.RiskAversion, PriorStrengthBps: in.PriorStrengthBps,
		BBOEvents: len(books), PivotConfirmations: len(events), Anchors: len(anchors),
		Warnings: []string{
			"The pivot filter observes every compacted BBO event; AnchorStep only samples decisions and does not define the regime.",
			"50% is used as a soft prior. The target optimizer has hard bounds [0,1] and no fixed 20% shift cap.",
			"Outcome is the next confirmed pivot executable markout. There is no private queue/fill calibration, so this is not production evidence.",
			"Legacy comparison is the same pivot geometry with the former fixed +/-20 percentage-point target shift, scored on identical outcome anchors.",
			"The split statistics are prequential action-level diagnostics; use component replay before any live promotion.",
		},
	}
	report.Train = summarizeCausalRegimeTargetSplit(anchors, in.From, in.TrainTo)
	report.Validation = summarizeCausalRegimeTargetSplit(anchors, in.TrainTo, in.ValidationTo)
	report.Holdout = summarizeCausalRegimeTargetSplit(anchors, in.ValidationTo, in.To)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode causal regime inventory target study: %v", err)
	}
}

func causalRegimeTargetFutureReturn(
	books []bboSnapshot,
	events []causalRegimeTargetStudyPivot,
	at time.Time,
) (longReturn, reducedReturn float64, ok bool) {
	if len(events) == 0 {
		return 0, 0, false
	}
	eventIndex := sort.Search(len(events), func(i int) bool { return events[i].at.After(at) })
	if eventIndex >= len(events) {
		return 0, 0, false
	}
	event := events[eventIndex]
	bookIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(event.pivotAt) })
	if bookIndex >= len(books) {
		return 0, 0, false
	}
	entryIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(at) })
	if entryIndex >= len(books) || books[entryIndex].ask <= 0 || books[entryIndex].bid <= 0 {
		return 0, 0, false
	}
	entry := books[entryIndex]
	exit := books[bookIndex]
	if exit.bid <= 0 || exit.ask <= exit.bid {
		return 0, 0, false
	}
	longReturn = math.Log(exit.bid/entry.ask) * 10_000
	// A lower target removes risky inventory at the current bid.  The
	// long-position return is marked at the future ask so a negative asset
	// return correctly creates positive value for a negative target delta.
	reducedReturn = math.Log(exit.ask/entry.bid) * 10_000
	return longReturn, reducedReturn, true
}

func summarizeCausalRegimeTargetSplit(
	anchors []causalRegimeTargetStudyAnchor,
	from, to time.Time,
) causalRegimeInventoryTargetSplitReport {
	report := causalRegimeInventoryTargetSplitReport{From: from, To: to}
	blockSums := make(map[int]float64)
	legacyBlockSums := make(map[int]float64)
	blockCounts := make(map[int]int)
	var cumulative, peak float64
	var legacyCumulative, legacyPeak float64
	for _, anchor := range anchors {
		if anchor.at.Before(from) || !anchor.at.Before(to) {
			continue
		}
		report.Anchors++
		if !anchor.target.Ready {
			continue
		}
		report.ReadyAnchors++
		report.MeanTargetWeight += anchor.target.TargetWeight
		report.MeanTargetDeltaWeight += anchor.target.TargetWeight - 0.5
		if anchor.target.TargetWeight >= 1-1e-9 {
			report.FullLongTargets++
		}
		if anchor.target.TargetWeight <= 1e-9 {
			report.FullFlatTargets++
		}
		if !anchor.validOutcome {
			continue
		}
		report.OutcomeAnchors++
		delta := anchor.target.TargetWeight - 0.5
		gross := causalRegimeTargetGrossValue(delta, anchor.futureLongReturn, anchor.futureReducedReturn)
		net := gross - anchor.target.OneWayCostBps*math.Abs(delta)
		legacyDelta := anchor.legacyTarget - 0.5
		legacyGross := causalRegimeTargetGrossValue(legacyDelta, anchor.futureLongReturn, anchor.futureReducedReturn)
		legacyNet := legacyGross - anchor.target.OneWayCostBps*math.Abs(legacyDelta)
		report.MeanIncrementalGrossBps += gross
		report.MeanIncrementalNetBps += net
		if math.Abs(delta) > 1e-9 {
			report.ActiveAnchors++
			report.ActiveMeanNetBps += net
		}
		if math.Abs(legacyDelta) > 1e-9 {
			report.LegacyActiveAnchors++
			report.LegacyActiveMeanNetBps += legacyNet
		}
		if net > 0 {
			report.PositiveAnchors++
		}
		cumulative += net
		if cumulative > peak {
			peak = cumulative
		}
		report.MaxCumulativeDrawdownBps = math.Max(
			report.MaxCumulativeDrawdownBps, peak-cumulative,
		)
		block := int(anchor.at.Sub(from) / (6 * time.Hour))
		blockSums[block] += net
		legacyCumulative += legacyNet
		if legacyCumulative > legacyPeak {
			legacyPeak = legacyCumulative
		}
		report.LegacyMaxDrawdownBps = math.Max(
			report.LegacyMaxDrawdownBps, legacyPeak-legacyCumulative,
		)
		legacyBlockSums[block] += legacyNet
		blockCounts[block]++
	}
	if report.ReadyAnchors > 0 {
		report.MeanTargetWeight /= float64(report.ReadyAnchors)
		report.MeanTargetDeltaWeight /= float64(report.ReadyAnchors)
	}
	if report.OutcomeAnchors > 0 {
		report.MeanIncrementalGrossBps /= float64(report.OutcomeAnchors)
		report.MeanIncrementalNetBps /= float64(report.OutcomeAnchors)
	}
	if report.ActiveAnchors > 0 {
		report.ActiveMeanNetBps /= float64(report.ActiveAnchors)
	}
	if report.LegacyActiveAnchors > 0 {
		report.LegacyActiveMeanNetBps /= float64(report.LegacyActiveAnchors)
	}
	for block, sum := range blockSums {
		if blockCounts[block] == 0 {
			continue
		}
		report.TotalBlocks++
		if sum > 0 {
			report.PositiveBlocks++
		}
		if legacyBlockSums[block] > 0 {
			report.LegacyPositiveBlocks++
		}
	}
	report.CumulativeNetBps = cumulative
	report.LegacyCumulativeNetBps = legacyCumulative
	if report.OutcomeAnchors > 0 {
		report.LegacyMeanNetBps = legacyCumulative / float64(report.OutcomeAnchors)
		report.CandidateDeltaVsLegacyBps =
			(cumulative - legacyCumulative) / float64(report.OutcomeAnchors)
	}
	return report
}

func causalRegimeTargetGrossValue(delta, longReturn, reducedReturn float64) float64 {
	if delta > 0 {
		return delta * longReturn
	}
	if delta < 0 {
		return delta * reducedReturn
	}
	return 0
}
