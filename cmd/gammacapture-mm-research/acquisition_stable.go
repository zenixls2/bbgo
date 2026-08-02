package main

import (
	"math"
	"time"
)

const acquisitionStabilityFolds = 5

// stableCombinedRule deliberately contains only five thresholds over features
// already calculated by the live causal fast-evidence window. Every candidate
// has the same small complexity; no tree depth, interactions, or per-day
// parameters are fitted.
type stableCombinedRule struct {
	CurrentReturn10mMinBps float64 `json:"currentReturn10mMinBps"`
	Return1mMinBps         float64 `json:"return1mMinBps"`
	Return5mMinBps         float64 `json:"return5mMinBps"`
	TradeImbalance5mMin    float64 `json:"tradeImbalance5mMin"`
	MinimumTrades5m        int     `json:"minimumTrades5m"`
}

func (r stableCombinedRule) matches(o chaseObservation) bool {
	current := o.Return10mBps >= r.CurrentReturn10mMinBps
	early := o.Return1mBps >= r.Return1mMinBps && o.Return5mBps >= r.Return5mMinBps &&
		o.TradeImbalance5m >= r.TradeImbalance5mMin && o.TradeCount5m >= r.MinimumTrades5m
	return current || early
}

type stableCandidateEvaluation struct {
	Rule             stableCombinedRule `json:"rule"`
	Development      labelRuleStats     `json:"development"`
	DevelopmentFolds []labelRuleStats   `json:"developmentFolds"`
	ActiveFolds      int                `json:"activeFolds"`
	HitFolds         int                `json:"hitFolds"`
	FoldRateStdDev   float64            `json:"foldRateStdDev"`
	StabilityScore   float64            `json:"stabilityScore"`
}

type stableSelectionReport struct {
	TestFrom                 time.Time                 `json:"testFrom"`
	CandidateCount           int                       `json:"candidateCount"`
	SelectionConstraints     string                    `json:"selectionConstraints"`
	ReferenceRule            stableCombinedRule        `json:"referenceRule"`
	ReferenceDevelopment     labelRuleStats            `json:"referenceDevelopment"`
	ReferenceFinalTest       labelRuleStats            `json:"referenceFinalTest"`
	Selected                 stableCandidateEvaluation `json:"selected"`
	SelectedFinalTest        labelRuleStats            `json:"selectedFinalTest"`
	FinalPrecisionDelta      float64                   `json:"finalPrecisionDelta"`
	FinalRelativeImprovement float64                   `json:"finalRelativeImprovement"`
	PromoteShadow            bool                      `json:"promoteShadow"`
	Decision                 string                    `json:"decision"`
}

func buildStableSelectionReport(observations []chaseObservation, testFrom time.Time) *stableSelectionReport {
	if testFrom.IsZero() {
		return nil
	}
	split := sortObservationBoundary(observations, testFrom)
	if split < 100 || len(observations)-split < 30 {
		return &stableSelectionReport{TestFrom: testFrom, Decision: "insufficient development or untouched-test observations"}
	}
	development, finalTest := observations[:split], observations[split:]
	reference := stableCombinedRule{CurrentReturn10mMinBps: 20, Return1mMinBps: -5, Return5mMinBps: 20, TradeImbalance5mMin: -1, MinimumTrades5m: 10}
	referenceDevelopment := ruleStats(development, reference.matches)
	referenceFinal := ruleStats(finalTest, reference.matches)

	best := stableCandidateEvaluation{StabilityScore: math.Inf(-1)}
	candidateCount := 0
	for _, current10 := range []float64{20, 30, 40, 60} {
		for _, return1 := range []float64{-5, 0, 5} {
			for _, return5 := range []float64{15, 20, 25, 30} {
				for _, imbalance := range []float64{-1, 0, .25} {
					for _, trades := range []int{10, 20, 40} {
						candidateCount++
						rule := stableCombinedRule{current10, return1, return5, imbalance, trades}
						evaluation, ok := evaluateStableCandidate(development, rule)
						if !ok {
							continue
						}
						if evaluation.StabilityScore > best.StabilityScore ||
							(evaluation.StabilityScore == best.StabilityScore && evaluation.Development.PositiveCoverage > best.Development.PositiveCoverage) {
							best = evaluation
						}
					}
				}
			}
		}
	}
	report := &stableSelectionReport{
		TestFrom: testFrom, CandidateCount: candidateCount,
		SelectionConstraints: "five chronological folds; >=40 development signals, >=5 hits, >=15% positive coverage, >=4 active folds, >=3 hit folds; maximize Wilson lower bound minus 0.25*fold-rate standard deviation",
		ReferenceRule:        reference, ReferenceDevelopment: referenceDevelopment, ReferenceFinalTest: referenceFinal,
		Selected: best,
	}
	if math.IsInf(best.StabilityScore, -1) {
		report.Decision = "no candidate passed the fixed stability constraints"
		return report
	}
	report.SelectedFinalTest = ruleStats(finalTest, best.Rule.matches)
	report.FinalPrecisionDelta = report.SelectedFinalTest.HitRate - referenceFinal.HitRate
	if referenceFinal.HitRate > 0 {
		report.FinalRelativeImprovement = report.FinalPrecisionDelta / referenceFinal.HitRate
	}
	report.PromoteShadow = report.SelectedFinalTest.Samples >= 15 && report.SelectedFinalTest.Hits >= 3 &&
		report.FinalPrecisionDelta >= .02 &&
		report.SelectedFinalTest.WilsonLower95 >= referenceFinal.WilsonLower95 &&
		report.SelectedFinalTest.PositiveCoverage >= .10
	if report.PromoteShadow {
		report.Decision = "promote fixed thresholds to shadow only; untouched test improved precision without reducing Wilson lower support"
	} else {
		report.Decision = "retain existing shadow thresholds; untouched test did not satisfy all predeclared promotion gates"
	}
	return report
}

func evaluateStableCandidate(development []chaseObservation, rule stableCombinedRule) (stableCandidateEvaluation, bool) {
	stats := ruleStats(development, rule.matches)
	if stats.Samples < 40 || stats.Hits < 5 || stats.PositiveCoverage < .15 {
		return stableCandidateEvaluation{}, false
	}
	folds := make([]labelRuleStats, 0, acquisitionStabilityFolds)
	rates := make([]float64, 0, acquisitionStabilityFolds)
	activeFolds, hitFolds := 0, 0
	for fold := 0; fold < acquisitionStabilityFolds; fold++ {
		start := len(development) * fold / acquisitionStabilityFolds
		end := len(development) * (fold + 1) / acquisitionStabilityFolds
		foldStats := ruleStats(development[start:end], rule.matches)
		folds = append(folds, foldStats)
		if foldStats.Samples >= 4 {
			activeFolds++
			rates = append(rates, foldStats.HitRate)
		}
		if foldStats.Hits > 0 {
			hitFolds++
		}
	}
	if activeFolds < 4 || hitFolds < 3 {
		return stableCandidateEvaluation{}, false
	}
	stddev := sampleStdDev(rates)
	return stableCandidateEvaluation{Rule: rule, Development: stats, DevelopmentFolds: folds,
		ActiveFolds: activeFolds, HitFolds: hitFolds, FoldRateStdDev: stddev,
		StabilityScore: stats.WilsonLower95 - .25*stddev}, true
}

func sortObservationBoundary(observations []chaseObservation, boundary time.Time) int {
	for i, o := range observations {
		if !o.At.Before(boundary) {
			return i
		}
	}
	return len(observations)
}

func sampleStdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	return math.Sqrt(variance / float64(len(values)-1))
}
