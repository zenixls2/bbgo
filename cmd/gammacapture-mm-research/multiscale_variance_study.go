package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type multiscaleVarianceStudyInput struct {
	DataPath   string
	Symbol     string
	From       time.Time
	To         time.Time
	Horizon    time.Duration
	AnchorStep time.Duration
}

type multiscaleVarianceStudyReport struct {
	Mode              string              `json:"mode"`
	Symbol            string              `json:"symbol"`
	From              time.Time           `json:"from"`
	To                time.Time           `json:"to"`
	MinuteCloses      int                 `json:"minuteCloses"`
	HorizonMinutes    int                 `json:"horizonMinutes"`
	AnchorStepMinutes int                 `json:"anchorStepMinutes"`
	Ask               varianceSideMetrics `json:"askBuySide"`
	Bid               varianceSideMetrics `json:"bidSellSide"`
	Conservative      varianceSideMetrics `json:"conservativeMaxSide"`
	Acceptance        string              `json:"acceptance"`
}

type varianceSideMetrics struct {
	Samples              int                  `json:"samples"`
	ElevatedSamples      int                  `json:"elevatedSamples"`
	DeescalatedSamples   int                  `json:"deescalatedSamples"`
	AmbiguousSamples     int                  `json:"ambiguousSamples"`
	ModelQLIKE           float64              `json:"modelQLIKE"`
	BaselineQLIKE        float64              `json:"baselineQLIKE"`
	QLIKESkill           float64              `json:"qLikeSkill"`
	ModelLogMSE          float64              `json:"modelLogMSE"`
	BaselineLogMSE       float64              `json:"baselineLogMSE"`
	MeanLossImprovement  float64              `json:"meanQLIKELossImprovement"`
	PairedTStatistic     float64              `json:"pairedTStatistic"`
	DaysImproved         int                  `json:"daysImproved"`
	DaysEvaluated        int                  `json:"daysEvaluated"`
	MinimumForecastRatio float64              `json:"minimumForecastRatio"`
	MaximumForecastRatio float64              `json:"maximumForecastRatio"`
	Daily                []varianceDayMetrics `json:"daily"`
}

type varianceDayMetrics struct {
	Day                string  `json:"day"`
	Samples            int     `json:"samples"`
	ModelQLIKE         float64 `json:"modelQLIKE"`
	BaselineQLIKE      float64 `json:"baselineQLIKE"`
	ElevatedSamples    int     `json:"elevatedSamples"`
	DeescalatedSamples int     `json:"deescalatedSamples"`
	AmbiguousSamples   int     `json:"ambiguousSamples"`
}

type varianceWindowStats struct {
	realized      float64
	rate          float64
	downsideShare float64
	jumpFraction  float64
}

type varianceMinuteReturn struct {
	at       time.Time
	ask, bid float64
	segment  uint64
}

type variancePendingLabel struct {
	matures     int
	askFeatures gammacapture.HARVarianceFeatures
	bidFeatures gammacapture.HARVarianceFeatures
	askCurrent  float64
	bidCurrent  float64
	askFuture   float64
	bidFuture   float64
}

type varianceScore struct {
	day                   string
	model, baseline       float64
	actual                float64
	elevated, deescalated bool
}

func runMultiscaleVarianceStudy(input multiscaleVarianceStudyInput) {
	books := compactBBO(readBBO(input.DataPath, input.Symbol, input.From, input.To))
	closes := minuteRegimeCloses(books)
	ask, bid, conservative := evaluateMultiscaleVariance(closes, input.Horizon, input.AnchorStep)
	report := multiscaleVarianceStudyReport{
		Mode: "standalone-online-har-variance-study", Symbol: input.Symbol,
		From: input.From, To: input.To, MinuteCloses: len(closes),
		HorizonMinutes: int(input.Horizon / time.Minute), AnchorStepMinutes: int(input.AnchorStep / time.Minute),
		Ask: ask, Bid: bid, Conservative: conservative,
		Acceptance: "reject: both executable sides must have positive QLIKE skill and paired t >= 1.645 with improvement on a majority of evaluated UTC days",
	}
	if ask.QLIKESkill > 0 && bid.QLIKESkill > 0 &&
		ask.PairedTStatistic >= 1.645 && bid.PairedTStatistic >= 1.645 &&
		ask.DaysImproved*2 > ask.DaysEvaluated && bid.DaysImproved*2 > bid.DaysEvaluated {
		report.Acceptance = "pass standalone variance gate; eligible for bounded Macro integration backtest"
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode multiscale variance report: %v", err)
	}
}

func evaluateMultiscaleVariance(closes []minuteRegimeClose, horizon, anchorStep time.Duration) (varianceSideMetrics, varianceSideMetrics, varianceSideMetrics) {
	if len(closes) < 2 || horizon <= 0 || anchorStep < horizon {
		return varianceSideMetrics{}, varianceSideMetrics{}, varianceSideMetrics{}
	}
	returns := buildVarianceMinuteReturns(closes)
	longWindow := int(horizon / time.Minute)
	mediumWindow := int(math.Max(1, math.Min(30, float64(longWindow))))
	shortWindow := int(math.Max(1, math.Min(15, float64(mediumWindow))))
	anchorMinutes := int(anchorStep / time.Minute)
	askModel, bidModel := gammacapture.NewOnlineHARVarianceModel(), gammacapture.NewOnlineHARVarianceModel()
	var pending []variancePendingLabel
	var askScores, bidScores, conservativeScores []varianceScore
	for index, close := range closes {
		remaining := pending[:0]
		for _, label := range pending {
			if label.matures > index {
				remaining = append(remaining, label)
				continue
			}
			askModel.Update(label.askFeatures, label.askCurrent, label.askFuture)
			bidModel.Update(label.bidFeatures, label.bidCurrent, label.bidFuture)
		}
		pending = remaining
		minuteOfDay := close.at.Minute() + 60*close.at.Hour()
		if minuteOfDay%anchorMinutes != 0 || index < longWindow || index+longWindow >= len(closes) {
			continue
		}
		if !closes[index+longWindow].at.Equal(close.at.Add(horizon)) {
			continue
		}
		askFeatures, askCurrent, askFuture, askOK := varianceForecastSample(returns, index, shortWindow, mediumWindow, longWindow, true)
		bidFeatures, bidCurrent, bidFuture, bidOK := varianceForecastSample(returns, index, shortWindow, mediumWindow, longWindow, false)
		if !askOK || !bidOK {
			continue
		}
		askDecision := askModel.Predict(askFeatures, askCurrent)
		bidDecision := bidModel.Predict(bidFeatures, bidCurrent)
		if askDecision.Healthy && bidDecision.Healthy {
			day := close.at.Format(time.DateOnly)
			askScores = append(askScores, varianceScore{day: day, model: askDecision.ForecastVariance, baseline: askCurrent, actual: askFuture, elevated: askDecision.ElevatedRisk, deescalated: askDecision.DeescalatedRisk})
			bidScores = append(bidScores, varianceScore{day: day, model: bidDecision.ForecastVariance, baseline: bidCurrent, actual: bidFuture, elevated: bidDecision.ElevatedRisk, deescalated: bidDecision.DeescalatedRisk})
			conservativeScores = append(conservativeScores, varianceScore{
				day: day, model: math.Max(askDecision.ForecastVariance, bidDecision.ForecastVariance),
				baseline: math.Max(askCurrent, bidCurrent), actual: math.Max(askFuture, bidFuture),
				elevated:    askDecision.ElevatedRisk && bidDecision.ElevatedRisk,
				deescalated: askDecision.DeescalatedRisk && bidDecision.DeescalatedRisk,
			})
		}
		pending = append(pending, variancePendingLabel{
			matures: index + longWindow, askFeatures: askFeatures, bidFeatures: bidFeatures,
			askCurrent: askCurrent, bidCurrent: bidCurrent, askFuture: askFuture, bidFuture: bidFuture,
		})
	}
	return summarizeVarianceScores(askScores), summarizeVarianceScores(bidScores), summarizeVarianceScores(conservativeScores)
}

func buildVarianceMinuteReturns(closes []minuteRegimeClose) []varianceMinuteReturn {
	out := make([]varianceMinuteReturn, len(closes))
	segment := uint64(1)
	for index := range closes {
		out[index].at = closes[index].at
		out[index].segment = segment
		if index == 0 {
			continue
		}
		if !closes[index].at.Equal(closes[index-1].at.Add(time.Minute)) {
			segment++
			out[index].segment = segment
			continue
		}
		out[index].ask = math.Log(closes[index].ask / closes[index-1].ask)
		out[index].bid = math.Log(closes[index].bid / closes[index-1].bid)
	}
	return out
}

func varianceForecastSample(returns []varianceMinuteReturn, anchor, shortWindow, mediumWindow, longWindow int, ask bool) (gammacapture.HARVarianceFeatures, float64, float64, bool) {
	var features gammacapture.HARVarianceFeatures
	short, ok1 := calculateVarianceWindow(returns, anchor-shortWindow+1, anchor, ask)
	medium, ok2 := calculateVarianceWindow(returns, anchor-mediumWindow+1, anchor, ask)
	long, ok3 := calculateVarianceWindow(returns, anchor-longWindow+1, anchor, ask)
	future, ok4 := calculateVarianceWindow(returns, anchor+1, anchor+longWindow, ask)
	if !ok1 || !ok2 || !ok3 || !ok4 || long.realized <= 0 || future.realized <= 0 {
		return features, 0, 0, false
	}
	features = gammacapture.HARVarianceFeatures{
		ShortRate: short.rate, MediumRate: medium.rate, LongRate: long.rate,
		DownsideShare: long.downsideShare, JumpFraction: long.jumpFraction,
	}
	return features, long.realized, future.realized, true
}

func calculateVarianceWindow(returns []varianceMinuteReturn, start, end int, ask bool) (varianceWindowStats, bool) {
	var out varianceWindowStats
	if start < 1 || end < start || end >= len(returns) {
		return out, false
	}
	segment := returns[start].segment
	var bipower float64
	previous := 0.0
	for index := start; index <= end; index++ {
		if returns[index].segment != segment {
			return varianceWindowStats{}, false
		}
		value := returns[index].bid
		if ask {
			value = returns[index].ask
		}
		out.realized += value * value
		if value < 0 {
			out.downsideShare += value * value
		}
		if index > start {
			bipower += math.Abs(value) * math.Abs(previous)
		}
		previous = value
	}
	count := end - start + 1
	if out.realized <= 0 || count < 2 {
		return varianceWindowStats{}, false
	}
	out.rate = out.realized / float64(count)
	out.downsideShare /= out.realized
	bipower *= math.Pi / 2
	out.jumpFraction = clampRatioResearch((out.realized-math.Min(out.realized, bipower))/out.realized, 0, 1)
	return out, true
}

func summarizeVarianceScores(scores []varianceScore) varianceSideMetrics {
	out := varianceSideMetrics{Samples: len(scores), MinimumForecastRatio: math.Inf(1)}
	if len(scores) == 0 {
		out.MinimumForecastRatio = 0
		return out
	}
	type dailyLoss struct {
		count                            int
		elevated, deescalated, ambiguous int
		model, baseline                  float64
	}
	days := make(map[string]*dailyLoss)
	differences := make([]float64, 0, len(scores))
	for _, score := range scores {
		modelLoss := qLikeLoss(score.model, score.actual)
		baselineLoss := qLikeLoss(score.baseline, score.actual)
		out.ModelQLIKE += modelLoss
		out.BaselineQLIKE += baselineLoss
		modelLogError := math.Log(score.model / score.actual)
		baselineLogError := math.Log(score.baseline / score.actual)
		out.ModelLogMSE += modelLogError * modelLogError
		out.BaselineLogMSE += baselineLogError * baselineLogError
		differences = append(differences, baselineLoss-modelLoss)
		switch {
		case score.elevated:
			out.ElevatedSamples++
		case score.deescalated:
			out.DeescalatedSamples++
		default:
			out.AmbiguousSamples++
		}
		ratio := score.model / score.baseline
		out.MinimumForecastRatio = math.Min(out.MinimumForecastRatio, ratio)
		out.MaximumForecastRatio = math.Max(out.MaximumForecastRatio, ratio)
		day := days[score.day]
		if day == nil {
			day = &dailyLoss{}
			days[score.day] = day
		}
		day.count++
		switch {
		case score.elevated:
			day.elevated++
		case score.deescalated:
			day.deescalated++
		default:
			day.ambiguous++
		}
		day.model += modelLoss
		day.baseline += baselineLoss
	}
	n := float64(len(scores))
	out.ModelQLIKE /= n
	out.BaselineQLIKE /= n
	out.ModelLogMSE /= n
	out.BaselineLogMSE /= n
	if out.BaselineQLIKE > 0 {
		out.QLIKESkill = 1 - out.ModelQLIKE/out.BaselineQLIKE
	}
	out.MeanLossImprovement, out.PairedTStatistic = pairedMeanT(differences)
	keys := make([]string, 0, len(days))
	for day := range days {
		keys = append(keys, day)
	}
	sort.Strings(keys)
	for _, key := range keys {
		day := days[key]
		modelLoss := day.model / float64(day.count)
		baselineLoss := day.baseline / float64(day.count)
		if modelLoss < baselineLoss {
			out.DaysImproved++
		}
		out.Daily = append(out.Daily, varianceDayMetrics{Day: key, Samples: day.count, ModelQLIKE: modelLoss, BaselineQLIKE: baselineLoss, ElevatedSamples: day.elevated, DeescalatedSamples: day.deescalated, AmbiguousSamples: day.ambiguous})
	}
	out.DaysEvaluated = len(out.Daily)
	return out
}

func qLikeLoss(forecast, actual float64) float64 {
	if forecast <= 0 || actual <= 0 {
		return math.Inf(1)
	}
	ratio := actual / forecast
	return ratio - math.Log(ratio) - 1
}

func pairedMeanT(values []float64) (mean, statistic float64) {
	if len(values) == 0 {
		return 0, 0
	}
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	if len(values) < 2 {
		return mean, 0
	}
	var variance float64
	for _, value := range values {
		variance += math.Pow(value-mean, 2)
	}
	variance /= float64(len(values) - 1)
	if variance > 0 {
		statistic = mean / math.Sqrt(variance/float64(len(values)))
	}
	return mean, statistic
}

func clampRatioResearch(value, minimum, maximum float64) float64 {
	return math.Max(minimum, math.Min(maximum, value))
}
