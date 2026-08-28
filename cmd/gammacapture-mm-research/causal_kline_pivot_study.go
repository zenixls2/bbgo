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

type causalKlinePivotStudyInput struct {
	DataPath              string
	Symbol                string
	From                  time.Time
	TrainTo               time.Time
	ValidationTo          time.Time
	To                    time.Time
	Horizon               time.Duration
	AnchorStep            time.Duration
	CostBps               float64
	MinReversalBps        float64
	MinimumTrainingLabels int
	Intervals             []time.Duration
}

type causalKlinePivotStudyReport struct {
	Mode             string                            `json:"mode"`
	Symbol           string                            `json:"symbol"`
	From             time.Time                         `json:"from"`
	To               time.Time                         `json:"to"`
	LoadedFrom       time.Time                         `json:"loadedFrom"`
	LoadedTo         time.Time                         `json:"loadedTo"`
	Horizon          string                            `json:"horizon"`
	AnchorStep       string                            `json:"anchorStep"`
	CostBps          float64                           `json:"costBps"`
	MinReversalBps   float64                           `json:"minReversalBps"`
	BBOEvents        int                               `json:"bboEvents"`
	Candidates       []causalKlinePivotCandidateReport `json:"candidates"`
	SelectedInterval string                            `json:"selectedInterval"`
	PromotionReady   bool                              `json:"promotionReady"`
	Warnings         []string                          `json:"warnings"`
}

type causalKlinePivotCandidateReport struct {
	Interval           string                      `json:"interval"`
	Bars               int                         `json:"bars"`
	PivotConfirmations int                         `json:"pivotConfirmations"`
	SegmentsReset      int                         `json:"segmentsReset"`
	Train              causalKlinePivotSplitReport `json:"train"`
	Validation         causalKlinePivotSplitReport `json:"validation"`
	Holdout            causalKlinePivotSplitReport `json:"holdout"`
}

type causalKlinePivotSplitReport struct {
	From                  time.Time `json:"from"`
	To                    time.Time `json:"to"`
	EligiblePredictions   int       `json:"eligiblePredictions"`
	ResolvedLabels        int       `json:"resolvedLabels"`
	CensoredLabels        int       `json:"censoredLabels"`
	HighLabels            int       `json:"highLabels"`
	LowLabels             int       `json:"lowLabels"`
	NeutralLabels         int       `json:"neutralLabels"`
	CorrectPredictions    int       `json:"correctPredictions"`
	Accuracy              float64   `json:"accuracy"`
	LogLoss               float64   `json:"logLoss"`
	Brier                 float64   `json:"brier"`
	ActionMeanBps         float64   `json:"actionMeanBps"`
	ActionSEBps           float64   `json:"actionSEBps"`
	ActionLowerBps        float64   `json:"actionLowerBps"`
	BaselineActionMeanBps float64   `json:"baselineActionMeanBps"`
	IncrementalMeanBps    float64   `json:"incrementalMeanBps"`
	IncrementalSEBps      float64   `json:"incrementalSEBps"`
	IncrementalLowerBps   float64   `json:"incrementalLowerBps"`
	PositiveBlocks        int       `json:"positiveBlocks"`
	TotalBlocks           int       `json:"totalBlocks"`
	EffectiveSamples      float64   `json:"effectiveSamples"`
}

type causalKlinePivotPrediction struct {
	At                time.Time
	Probabilities     [3]float64
	PredictedKind     gammacapture.CausalKlinePivotKind
	BaselineDirection int
	StartBid          float64
	StartAsk          float64
}

type causalKlinePivotScoredSample struct {
	At                time.Time
	Label             gammacapture.CausalKlinePivotKind
	Probabilities     [3]float64
	PredictedKind     gammacapture.CausalKlinePivotKind
	ActionBps         float64
	BaselineActionBps float64
}

func runCausalKlinePivotStudy(input causalKlinePivotStudyInput) {
	if input.From.IsZero() || !input.From.Before(input.To) || input.Horizon <= 0 || input.AnchorStep <= 0 ||
		input.CostBps < 0 || input.MinReversalBps < 0 || len(input.Intervals) == 0 {
		fatalf("invalid causal Kline pivot study configuration")
	}
	if input.TrainTo.IsZero() || input.ValidationTo.IsZero() {
		duration := input.To.Sub(input.From)
		input.TrainTo = input.From.Add(duration / 2)
		input.ValidationTo = input.From.Add(3 * duration / 4)
	}
	if !input.From.Before(input.TrainTo) || !input.TrainTo.Before(input.ValidationTo) || !input.ValidationTo.Before(input.To) {
		fatalf("causal Kline pivot study requires from < train-to < validation-to < to")
	}
	if input.MinimumTrainingLabels <= 0 {
		input.MinimumTrainingLabels = 32
	}
	minInterval := input.Intervals[0]
	for _, interval := range input.Intervals[1:] {
		if interval > 0 && interval < minInterval {
			minInterval = interval
		}
	}
	loadedFrom := input.From.Add(-24 * time.Hour)
	loadedTo := input.To.Add(input.Horizon + minInterval + input.AnchorStep)
	books := compactBBO(readBBO(input.DataPath, input.Symbol, loadedFrom, loadedTo))
	if len(books) < 10 {
		fatalf("insufficient BBO events for causal Kline pivot study: %d", len(books))
	}

	report := causalKlinePivotStudyReport{
		Mode: "standalone-causal-kline-pivot-study", Symbol: input.Symbol,
		From: input.From, To: input.To, LoadedFrom: loadedFrom, LoadedTo: loadedTo,
		Horizon: input.Horizon.String(), AnchorStep: input.AnchorStep.String(),
		CostBps: input.CostBps, MinReversalBps: input.MinReversalBps,
		BBOEvents: len(books), Warnings: []string{
			"Bars are built from the observable BBO mid stream; prediction time is the first observed event after the bar closes.",
			"The next bar confirms the previous label. No future bar is used in current-bar features or prediction.",
			"Action values use executable ask for upward predictions, executable bid for downward predictions, and the complete configured fee/cost floor once.",
			"Samples are scored only at non-overlapping anchor times; uncertainty is summarized by chronological six-hour blocks.",
		},
	}

	for _, interval := range input.Intervals {
		candidate := runCausalKlinePivotCandidate(books, input, interval)
		report.Candidates = append(report.Candidates, candidate)
	}
	if selected := selectCausalKlinePivotCandidate(report.Candidates); selected >= 0 {
		report.SelectedInterval = report.Candidates[selected].Interval
		report.PromotionReady = causalKlinePivotPromotionGate(report.Candidates[selected])
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode causal Kline pivot study: %v", err)
	}
}

func runCausalKlinePivotCandidate(books []bboSnapshot, input causalKlinePivotStudyInput, interval time.Duration) causalKlinePivotCandidateReport {
	report := causalKlinePivotCandidateReport{Interval: interval.String()}
	if interval <= 0 {
		return report
	}
	learner := gammacapture.NewCausalKlinePivotLearner(gammacapture.CausalKlinePivotConfig{
		Interval: types.Duration(interval), MinimumTrainingLabels: input.MinimumTrainingLabels,
	})
	builder := gammacapture.NewCausalKlineBuilder(interval)
	baseline := gammacapture.NewPivotRegimeFilter(gammacapture.PivotRegimeConfig{
		ReversalBps: 26, MaxGap: 15 * time.Minute, MinLegSamples: 2, PriorLegSamples: 2,
	})
	var pending *causalKlinePivotPrediction
	nextAnchor := input.From
	samples := make([]causalKlinePivotScoredSample, 0)
	for _, book := range books {
		baselineDecision := baseline.Observe(gammacapture.PivotRegimeInput{At: book.time, ReferencePrice: book.midPrice()})
		bar, closed := builder.Observe(book.time, book.midPrice())
		if !closed {
			continue
		}
		report.Bars++
		decision := learner.ObserveBar(bar)
		if decision.SegmentReset {
			report.SegmentsReset++
		}
		if decision.PivotConfirmed {
			report.PivotConfirmations++
		}
		if pending != nil && decision.LabelMatured && !decision.LabelSkipped &&
			!pending.At.Before(input.From) && pending.At.Before(input.To) && !pending.At.Before(nextAnchor) {
			if sample, ok := causalKlinePivotScore(books, pending, decision.MaturedLabelKind, input.Horizon, input.CostBps); ok {
				samples = append(samples, sample)
				nextAnchor = pending.At.Add(input.AnchorStep)
			}
		}
		if decision.PredictionReady && decision.ModelReady {
			pending = &causalKlinePivotPrediction{
				At:                decision.At,
				Probabilities:     [3]float64{decision.ProbabilityHigh, decision.ProbabilityLow, decision.ProbabilityNeutral},
				PredictedKind:     decision.PredictedKind,
				BaselineDirection: baselineDecision.Direction,
				StartBid:          book.bid, StartAsk: book.ask,
			}
		} else {
			pending = nil
		}
	}
	report.Train = summarizeCausalKlinePivotSplit(samples, input.From, input.TrainTo)
	report.Validation = summarizeCausalKlinePivotSplit(samples, input.TrainTo, input.ValidationTo)
	report.Holdout = summarizeCausalKlinePivotSplit(samples, input.ValidationTo, input.To)
	return report
}

func causalKlinePivotScore(books []bboSnapshot, prediction *causalKlinePivotPrediction, label gammacapture.CausalKlinePivotKind, horizon time.Duration, costBps float64) (causalKlinePivotScoredSample, bool) {
	if prediction == nil || prediction.At.IsZero() || prediction.StartBid <= 0 || prediction.StartAsk <= prediction.StartBid {
		return causalKlinePivotScoredSample{}, false
	}
	future, ok := causalKlinePivotFutureBook(books, prediction.At.Add(horizon))
	if !ok {
		return causalKlinePivotScoredSample{}, false
	}
	action := causalKlinePivotAction(prediction.StartBid, prediction.StartAsk, future.bid, future.ask, prediction.PredictedKind, costBps)
	baselineKind := gammacapture.CausalKlinePivotNeutral
	if prediction.BaselineDirection > 0 {
		baselineKind = gammacapture.CausalKlinePivotLow
	} else if prediction.BaselineDirection < 0 {
		baselineKind = gammacapture.CausalKlinePivotHigh
	}
	return causalKlinePivotScoredSample{
		At: prediction.At, Label: label, Probabilities: prediction.Probabilities,
		PredictedKind: prediction.PredictedKind, ActionBps: action,
		BaselineActionBps: causalKlinePivotAction(prediction.StartBid, prediction.StartAsk, future.bid, future.ask, baselineKind, costBps),
	}, true
}

func causalKlinePivotAction(startBid, startAsk, futureBid, futureAsk float64, kind gammacapture.CausalKlinePivotKind, costBps float64) float64 {
	if kind == gammacapture.CausalKlinePivotLow {
		return math.Log(futureBid/startAsk)*10_000 - costBps
	}
	if kind == gammacapture.CausalKlinePivotHigh {
		return -math.Log(futureAsk/startBid)*10_000 - costBps
	}
	return 0
}

func causalKlinePivotFutureBook(books []bboSnapshot, target time.Time) (bboSnapshot, bool) {
	index := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(target) })
	if index >= len(books) || books[index].bid <= 0 || books[index].ask <= books[index].bid {
		return bboSnapshot{}, false
	}
	return books[index], true
}

func summarizeCausalKlinePivotSplit(samples []causalKlinePivotScoredSample, from, to time.Time) causalKlinePivotSplitReport {
	report := causalKlinePivotSplitReport{From: from, To: to}
	blockValues := make(map[int][]float64)
	blockBaselineValues := make(map[int][]float64)
	for _, sample := range samples {
		if sample.At.Before(from) || !sample.At.Before(to) {
			continue
		}
		report.EligiblePredictions++
		report.ResolvedLabels++
		switch sample.Label {
		case gammacapture.CausalKlinePivotHigh:
			report.HighLabels++
		case gammacapture.CausalKlinePivotLow:
			report.LowLabels++
		default:
			report.NeutralLabels++
		}
		labelIndex := causalKlinePivotStudyClassIndex(sample.Label)
		probability := math.Max(1e-12, math.Min(1, sample.Probabilities[labelIndex]))
		report.LogLoss -= math.Log(probability)
		for i, value := range sample.Probabilities {
			target := 0.0
			if i == labelIndex {
				target = 1
			}
			report.Brier += (value - target) * (value - target)
		}
		if sample.PredictedKind == sample.Label {
			report.CorrectPredictions++
		}
		report.ActionMeanBps += sample.ActionBps
		report.BaselineActionMeanBps += sample.BaselineActionBps
		block := int(sample.At.Sub(from) / (6 * time.Hour))
		blockValues[block] = append(blockValues[block], sample.ActionBps)
		blockBaselineValues[block] = append(blockBaselineValues[block], sample.BaselineActionBps)
	}
	if report.ResolvedLabels > 0 {
		report.Accuracy = float64(report.CorrectPredictions) / float64(report.ResolvedLabels)
		report.LogLoss /= float64(report.ResolvedLabels)
		report.Brier /= float64(report.ResolvedLabels)
		report.ActionMeanBps /= float64(report.ResolvedLabels)
		report.BaselineActionMeanBps /= float64(report.ResolvedLabels)
		report.IncrementalMeanBps = report.ActionMeanBps - report.BaselineActionMeanBps
	}
	blockMeans := make([]float64, 0, len(blockValues))
	actionBlockMeans := make([]float64, 0, len(blockValues))
	for block, values := range blockValues {
		if len(values) == 0 {
			continue
		}
		mean := meanFloat64(values)
		baselineMean := meanFloat64(blockBaselineValues[block])
		actionBlockMeans = append(actionBlockMeans, mean)
		blockMeans = append(blockMeans, mean-baselineMean)
		report.TotalBlocks++
		if mean-baselineMean > 0 {
			report.PositiveBlocks++
		}
	}
	report.EffectiveSamples = float64(len(blockMeans))
	if len(actionBlockMeans) > 1 {
		mean := meanFloat64(actionBlockMeans)
		variance := 0.0
		for _, value := range actionBlockMeans {
			variance += (value - mean) * (value - mean)
		}
		variance /= float64(len(actionBlockMeans) - 1)
		report.ActionSEBps = math.Sqrt(variance / float64(len(actionBlockMeans)))
		report.ActionLowerBps = report.ActionMeanBps - 1.645*report.ActionSEBps
	}
	if len(blockMeans) > 1 {
		mean := meanFloat64(blockMeans)
		variance := 0.0
		for _, value := range blockMeans {
			variance += (value - mean) * (value - mean)
		}
		variance /= float64(len(blockMeans) - 1)
		report.IncrementalSEBps = math.Sqrt(variance / float64(len(blockMeans)))
		report.IncrementalLowerBps = report.IncrementalMeanBps - 1.645*report.IncrementalSEBps
	}
	return report
}

func causalKlinePivotStudyClassIndex(kind gammacapture.CausalKlinePivotKind) int {
	switch kind {
	case gammacapture.CausalKlinePivotHigh:
		return 0
	case gammacapture.CausalKlinePivotLow:
		return 1
	default:
		return 2
	}
}

func meanFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func selectCausalKlinePivotCandidate(candidates []causalKlinePivotCandidateReport) int {
	selected := -1
	for i := range candidates {
		if selected < 0 || candidates[i].Validation.IncrementalLowerBps > candidates[selected].Validation.IncrementalLowerBps {
			selected = i
		}
	}
	return selected
}

func causalKlinePivotPromotionGate(candidate causalKlinePivotCandidateReport) bool {
	validation := candidate.Validation
	holdout := candidate.Holdout
	return validation.EffectiveSamples >= 24 && holdout.EffectiveSamples >= 24 &&
		validation.PositiveBlocks >= 3 && holdout.PositiveBlocks >= 3 &&
		validation.ActionLowerBps > 0 && holdout.ActionLowerBps > 0 &&
		validation.IncrementalLowerBps > 0 && holdout.IncrementalLowerBps > 0
}
