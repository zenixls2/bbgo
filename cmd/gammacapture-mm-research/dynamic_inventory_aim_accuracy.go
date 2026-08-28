package main

import (
	"math"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

const dynamicInventoryAimPivotInterval = 3 * time.Minute

// replayDynamicInventoryAimObservation is emitted by the existing production
// replay at the exact point where EvaluateDynamicInventoryAim receives its
// causal forecast. It contains no future information. The future mid label is
// attached only after replay has reached the label maturity time.
type replayDynamicInventoryAimObservation struct {
	At                   time.Time
	Horizon              time.Duration
	GrossForecastBps     float64
	ExecutionForecastBps float64
	CurrentRatio         float64
	AdjustedTargetRatio  float64
	GatePassed           bool
	Applied              bool
	EffectiveSamples     float64
}

type dynamicInventoryAimAccuracyReport struct {
	Evaluations                 int     `json:"evaluations"`
	Eligible                    int     `json:"eligible"`
	Matured                     int     `json:"matured"`
	GatePassed                  int     `json:"gatePassed"`
	Applied                     int     `json:"applied"`
	MeanGrossForecastBps        float64 `json:"meanGrossForecastBps"`
	MeanActualMidReturnBps      float64 `json:"meanActualMidReturnBps"`
	MeanForecastErrorBps        float64 `json:"meanForecastErrorBps"`
	MAEBps                      float64 `json:"maeBps"`
	RMSEBps                     float64 `json:"rmseBps"`
	SignAccuracy                float64 `json:"signAccuracy"`
	TargetActionDecisions       int     `json:"targetActionDecisions"`
	TargetActionSignAccuracy    float64 `json:"targetActionSignAccuracy"`
	ForecastCorrelation         float64 `json:"forecastCorrelation"`
	MeanGatedActualBps          float64 `json:"meanGatedActualBps"`
	GatedPositiveRate           float64 `json:"gatedPositiveRate"`
	MeanAbsoluteTargetMove      float64 `json:"meanAbsoluteTargetMove"`
	MeanEffectiveSamples        float64 `json:"meanEffectiveSamples"`
	NextPivotEligible           int     `json:"nextPivotEligible"`
	NextPivotResolved           int     `json:"nextPivotResolved"`
	NextPivotCensored           int     `json:"nextPivotCensored"`
	NextPivotHighLabels         int     `json:"nextPivotHighLabels"`
	NextPivotLowLabels          int     `json:"nextPivotLowLabels"`
	NextPivotMeanMoveBps        float64 `json:"nextPivotMeanMoveBps"`
	NextPivotForecastActions    int     `json:"nextPivotForecastActions"`
	NextPivotForecastHitRate    float64 `json:"nextPivotForecastHitRate"`
	NextPivotForecastMeanPnlBps float64 `json:"nextPivotForecastMeanPnlBps"`
	NextPivotTargetActions      int     `json:"nextPivotTargetActions"`
	NextPivotTargetHitRate      float64 `json:"nextPivotTargetHitRate"`
	NextPivotTargetMeanPnlBps   float64 `json:"nextPivotTargetMeanPnlBps"`
	NextPivotDefinition         string  `json:"nextPivotDefinition"`
	Horizon                     string  `json:"horizon"`
	Label                       string  `json:"label"`
	Warning                     string  `json:"warning"`
}

// buildDynamicInventoryAimPivots constructs the same delayed, three-minute
// Kline pivot definition used by the research learner. The learner is used
// only as a causal fractal detector here; its prediction model is deliberately
// not involved in the label.
func buildDynamicInventoryAimPivots(books []bboSnapshot) []gammacapture.CausalKlinePivotEvent {
	builder := gammacapture.NewCausalKlineBuilder(dynamicInventoryAimPivotInterval)
	learner := gammacapture.NewCausalKlinePivotLearner(gammacapture.CausalKlinePivotConfig{
		Interval: types.Duration(dynamicInventoryAimPivotInterval),
		// Pivot detection does not depend on model readiness. A very large
		// training threshold keeps this helper from being confused with the
		// forecast learner while retaining its causal label maturation.
		MinimumTrainingLabels: int(^uint(0) >> 1),
	})
	pivots := make([]gammacapture.CausalKlinePivotEvent, 0)
	for _, book := range books {
		mid := book.midPrice()
		if mid <= 0 || math.IsNaN(mid) || math.IsInf(mid, 0) {
			continue
		}
		bar, closed := builder.Observe(book.time, mid)
		if !closed {
			continue
		}
		decision := learner.ObserveBar(bar)
		if decision.PivotConfirmed && decision.ConfirmedPivot.Kind != gammacapture.CausalKlinePivotNeutral {
			pivots = append(pivots, decision.ConfirmedPivot)
		}
	}
	sort.SliceStable(pivots, func(i, j int) bool {
		return pivots[i].At.Before(pivots[j].At)
	})
	return pivots
}

func nextDynamicInventoryAimPivot(pivots []gammacapture.CausalKlinePivotEvent, at time.Time) (gammacapture.CausalKlinePivotEvent, bool) {
	index := sort.Search(len(pivots), func(i int) bool { return pivots[i].At.After(at) })
	if index >= len(pivots) {
		return gammacapture.CausalKlinePivotEvent{}, false
	}
	pivot := pivots[index]
	if pivot.At.IsZero() || pivot.ConfirmedAt.IsZero() || !pivot.ConfirmedAt.After(at) || pivot.Price <= 0 {
		return gammacapture.CausalKlinePivotEvent{}, false
	}
	return pivot, true
}

// dynamicInventoryAimPivotActionPnl is the gross mark-to-pivot PnL of the
// proposed directional action. Entry uses the executable current ask/bid;
// the terminal value is the causal pivot price. Fees and adverse-selection
// deductions are intentionally excluded because this is a direction-label
// diagnostic, not a promotion PnL.
func dynamicInventoryAimPivotActionPnl(startBid, startAsk, pivotPrice float64, direction int) (float64, bool) {
	if startBid <= 0 || startAsk <= startBid || pivotPrice <= 0 {
		return 0, false
	}
	switch {
	case direction > 0:
		return math.Log(pivotPrice/startAsk) * 10_000, true
	case direction < 0:
		return -math.Log(pivotPrice/startBid) * 10_000, true
	default:
		return 0, false
	}
}

func dynamicInventoryAimActualMidReturn(books []bboSnapshot, observation replayDynamicInventoryAimObservation) (float64, bool) {
	if observation.At.IsZero() || observation.Horizon <= 0 {
		return 0, false
	}
	start := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(observation.At) })
	if start >= len(books) {
		return 0, false
	}
	end := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(observation.At.Add(observation.Horizon)) })
	if end >= len(books) {
		return 0, false
	}
	startMid := (books[start].bid + books[start].ask) / 2
	endMid := (books[end].bid + books[end].ask) / 2
	if startMid <= 0 || endMid <= 0 {
		return 0, false
	}
	return math.Log(endMid/startMid) * 10_000, true
}

func dynamicInventoryAimAccuracyCorrelation(predictions, actuals []float64) float64 {
	if len(predictions) != len(actuals) || len(predictions) < 2 {
		return 0
	}
	meanPrediction, meanActual := 0.0, 0.0
	for i := range predictions {
		meanPrediction += predictions[i]
		meanActual += actuals[i]
	}
	meanPrediction /= float64(len(predictions))
	meanActual /= float64(len(actuals))
	varPrediction, varActual, covariance := 0.0, 0.0, 0.0
	for i := range predictions {
		dp := predictions[i] - meanPrediction
		da := actuals[i] - meanActual
		varPrediction += dp * dp
		varActual += da * da
		covariance += dp * da
	}
	if varPrediction <= 0 || varActual <= 0 {
		return 0
	}
	return covariance / math.Sqrt(varPrediction*varActual)
}

func summarizeDynamicInventoryAimAccuracy(books []bboSnapshot, observations []replayDynamicInventoryAimObservation, from time.Time) dynamicInventoryAimAccuracyReport {
	report := dynamicInventoryAimAccuracyReport{
		Label:               "future mid log-return after the model horizon",
		NextPivotDefinition: "next confirmed 3m Kline fractal pivot; confirmation occurs after the following bar closes",
		Warning:             "Mid-horizon metrics are forecast accuracy only. Next-pivot PnL uses current executable ask/bid to the mid-based pivot price and excludes fees, fills, and adverse-selection deductions.",
	}
	if len(observations) == 0 {
		return report
	}
	pivots := buildDynamicInventoryAimPivots(books)
	report.Horizon = observations[0].Horizon.String()
	for _, observation := range observations[1:] {
		if observation.Horizon != observations[0].Horizon {
			report.Horizon = "variable"
			break
		}
	}
	allPredictions, allActuals := make([]float64, 0), make([]float64, 0)
	gatedActuals := make([]float64, 0)
	for _, observation := range observations {
		if !from.IsZero() && observation.At.Before(from) {
			continue
		}
		report.Evaluations++
		actual, ok := dynamicInventoryAimActualMidReturn(books, observation)
		if !ok || !finiteDynamicInventoryAimAccuracy(observation.GrossForecastBps) {
			continue
		}
		report.Eligible++
		report.Matured++
		allPredictions = append(allPredictions, observation.GrossForecastBps)
		allActuals = append(allActuals, actual)
		report.MeanGrossForecastBps += observation.GrossForecastBps
		report.MeanActualMidReturnBps += actual
		report.MeanForecastErrorBps += observation.GrossForecastBps - actual
		report.MAEBps += math.Abs(observation.GrossForecastBps - actual)
		report.RMSEBps += (observation.GrossForecastBps - actual) * (observation.GrossForecastBps - actual)
		if (observation.GrossForecastBps >= 0) == (actual >= 0) {
			report.SignAccuracy++
		}
		delta := observation.AdjustedTargetRatio - observation.CurrentRatio
		if observation.GatePassed {
			report.GatePassed++
			gatedActuals = append(gatedActuals, actual)
			report.MeanGatedActualBps += actual
			report.MeanAbsoluteTargetMove += math.Abs(delta)
			if math.Abs(delta) > 1e-12 {
				report.TargetActionDecisions++
				if (delta >= 0) == (actual >= 0) {
					report.TargetActionSignAccuracy++
				}
			}
		}
		if observation.Applied {
			report.Applied++
		}
		report.MeanEffectiveSamples += observation.EffectiveSamples

		pivot, pivotOK := nextDynamicInventoryAimPivot(pivots, observation.At)
		if !pivotOK {
			report.NextPivotCensored++
			continue
		}
		startIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(observation.At) })
		if startIndex >= len(books) {
			report.NextPivotCensored++
			continue
		}
		start := books[startIndex]
		startMid := start.midPrice()
		if startMid <= 0 || pivot.Price <= 0 {
			report.NextPivotCensored++
			continue
		}
		report.NextPivotEligible++
		report.NextPivotResolved++
		if pivot.Kind == gammacapture.CausalKlinePivotHigh {
			report.NextPivotHighLabels++
		} else if pivot.Kind == gammacapture.CausalKlinePivotLow {
			report.NextPivotLowLabels++
		}
		pivotMoveBps := math.Log(pivot.Price/startMid) * 10_000
		report.NextPivotMeanMoveBps += pivotMoveBps
		forecastDirection := signDynamicInventoryAimAccuracy(observation.GrossForecastBps)
		if forecastDirection != 0 {
			if pnl, ok := dynamicInventoryAimPivotActionPnl(start.bid, start.ask, pivot.Price, forecastDirection); ok {
				report.NextPivotForecastActions++
				report.NextPivotForecastMeanPnlBps += pnl
				if pnl > 0 {
					report.NextPivotForecastHitRate++
				}
			}
		}
		if observation.GatePassed && math.Abs(delta) > 1e-12 {
			targetDirection := signDynamicInventoryAimAccuracy(delta)
			if pnl, ok := dynamicInventoryAimPivotActionPnl(start.bid, start.ask, pivot.Price, targetDirection); ok {
				report.NextPivotTargetActions++
				report.NextPivotTargetMeanPnlBps += pnl
				if pnl > 0 {
					report.NextPivotTargetHitRate++
				}
			}
		}
	}
	if report.Eligible > 0 {
		count := float64(report.Eligible)
		report.MeanGrossForecastBps /= count
		report.MeanActualMidReturnBps /= count
		report.MeanForecastErrorBps /= count
		report.MAEBps /= count
		report.RMSEBps = math.Sqrt(report.RMSEBps / count)
		report.SignAccuracy /= count
		report.MeanEffectiveSamples /= count
		report.ForecastCorrelation = dynamicInventoryAimAccuracyCorrelation(allPredictions, allActuals)
	}
	if report.GatePassed > 0 {
		report.MeanAbsoluteTargetMove /= float64(report.GatePassed)
	}
	if report.TargetActionDecisions > 0 {
		report.TargetActionSignAccuracy /= float64(report.TargetActionDecisions)
	}
	if report.GatePassed > 0 {
		report.MeanGatedActualBps /= float64(report.GatePassed)
		for _, actual := range gatedActuals {
			if actual > 0 {
				report.GatedPositiveRate++
			}
		}
		report.GatedPositiveRate /= float64(report.GatePassed)
	}
	if report.NextPivotEligible > 0 {
		report.NextPivotMeanMoveBps /= float64(report.NextPivotEligible)
	}
	if report.NextPivotForecastActions > 0 {
		report.NextPivotForecastHitRate /= float64(report.NextPivotForecastActions)
		report.NextPivotForecastMeanPnlBps /= float64(report.NextPivotForecastActions)
	}
	if report.NextPivotTargetActions > 0 {
		report.NextPivotTargetHitRate /= float64(report.NextPivotTargetActions)
		report.NextPivotTargetMeanPnlBps /= float64(report.NextPivotTargetActions)
	}
	return report
}

func signDynamicInventoryAimAccuracy(value float64) int {
	if value > 0 {
		return 1
	}
	if value < 0 {
		return -1
	}
	return 0
}

func finiteDynamicInventoryAimAccuracy(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
