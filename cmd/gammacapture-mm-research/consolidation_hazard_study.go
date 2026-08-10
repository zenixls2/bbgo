package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"
)

// consolidationHazardStudyInput defines a causal competing-risk study.  The
// short and long windows are fixed before scoring; Horizon labels are admitted
// to the training set only after the full future path has elapsed.
type consolidationHazardStudyInput struct {
	DataPath         string
	Symbol           string
	From, To         time.Time
	Horizon          time.Duration
	ShortWindow      time.Duration
	LongWindow       time.Duration
	DecisionStep     time.Duration
	RoundTripCostBps float64
	ConfidenceZ      float64
	MinimumSamples   int
}

type consolidationHazardFeature struct {
	longReturn        float64
	longZ             float64
	shortZ            float64
	efficiencyRatio   float64
	qvAccelerationLog float64
}

type consolidationHazardSample struct {
	feature             consolidationHazardFeature
	outcome             int
	upNetExcursion      float64
	downNetExcursion    float64
	firstPassageMinutes int
	distance            float64
}

type consolidationHazardPrediction struct {
	At                       time.Time `json:"at"`
	Samples                  int       `json:"samples"`
	Neighbors                int       `json:"neighbors"`
	LongReturnBps            float64   `json:"longReturnBps"`
	LongZ                    float64   `json:"longZ"`
	ShortZ                   float64   `json:"shortZ"`
	ConsolidationEfficiency  float64   `json:"consolidationEfficiencyRatio"`
	QVAccelerationLog        float64   `json:"qvAccelerationLog"`
	DownProbability          float64   `json:"downProbability"`
	UpProbability            float64   `json:"upProbability"`
	CensorProbability        float64   `json:"censorProbability"`
	DownGivenMoveProbability float64   `json:"downGivenMoveProbability"`
	DownGivenMoveLower       float64   `json:"downGivenMoveLower"`
	BaselineDownProbability  float64   `json:"baselineDownProbability"`
	ExpectedNetReturnBps     float64   `json:"expectedNetReturnBps"`
	ExpectedUpperBps         float64   `json:"expectedUpperBps"`
	Signal                   bool      `json:"signal"`
	Outcome                  string    `json:"outcome"`
	PassageMinutes           int       `json:"passageMinutes"`
}

type consolidationHazardDay struct {
	Day         string `json:"day"`
	Predictions int    `json:"predictions"`
	Signals     int    `json:"signals"`
	DownFirst   int    `json:"downFirst"`
	UpFirst     int    `json:"upFirst"`
	Censored    int    `json:"censored"`
}

type consolidationHazardStudyReport struct {
	Mode                      string                          `json:"mode"`
	Symbol                    string                          `json:"symbol"`
	From                      time.Time                       `json:"from"`
	To                        time.Time                       `json:"to"`
	HorizonMinutes            int                             `json:"horizonMinutes"`
	ShortWindowMinutes        int                             `json:"shortWindowMinutes"`
	LongWindowMinutes         int                             `json:"longWindowMinutes"`
	RoundTripCostBps          float64                         `json:"roundTripCostBps"`
	Predictions               int                             `json:"predictions"`
	Resolved                  int                             `json:"resolved"`
	DownFirst                 int                             `json:"downFirst"`
	UpFirst                   int                             `json:"upFirst"`
	Censored                  int                             `json:"censored"`
	Brier                     float64                         `json:"brier"`
	ClimatologyBrier          float64                         `json:"climatologyBrier"`
	BrierSkill                float64                         `json:"brierSkill"`
	Signals                   int                             `json:"signals"`
	SignalDownFirst           int                             `json:"signalDownFirst"`
	SignalUpFirst             int                             `json:"signalUpFirst"`
	SignalCensored            int                             `json:"signalCensored"`
	SignalDownRate            float64                         `json:"signalDownRate"`
	SignalDownRateWilsonLower float64                         `json:"signalDownRateWilsonLower95"`
	BaselineDownRate          float64                         `json:"baselineDownRate"`
	Daily                     []consolidationHazardDay        `json:"daily"`
	PredictionsDetail         []consolidationHazardPrediction `json:"predictionsDetail"`
	Acceptance                string                          `json:"acceptance"`
}

func runConsolidationHazardStudy(input consolidationHazardStudyInput) {
	books := compactBBO(readBBO(input.DataPath, input.Symbol, input.From, input.To))
	closes := minuteRegimeCloses(books)
	report := evaluateConsolidationHazard(closes, input)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode consolidation hazard report: %v", err)
	}
}

func consolidationFeatureAt(closes []minuteRegimeClose, anchor, shortSteps, longSteps int) (consolidationHazardFeature, bool) {
	var feature consolidationHazardFeature
	if shortSteps < 2 || longSteps <= shortSteps || anchor-longSteps < 0 || anchor >= len(closes) {
		return feature, false
	}
	start := closes[anchor-longSteps]
	shortStart := closes[anchor-shortSteps]
	end := closes[anchor]
	if start.bid <= 0 || start.ask <= 0 || shortStart.bid <= 0 || shortStart.ask <= 0 || end.bid <= 0 || end.ask <= 0 {
		return feature, false
	}
	if !end.at.Equal(start.at.Add(time.Duration(longSteps)*time.Minute)) ||
		!end.at.Equal(shortStart.at.Add(time.Duration(shortSteps)*time.Minute)) {
		return feature, false
	}
	longAsk := math.Log(end.ask / start.ask)
	longBid := math.Log(end.bid / start.bid)
	// A downside state must exist in both executable paths.  Disagreement is
	// spread movement, not directional evidence.
	if longAsk >= 0 || longBid >= 0 {
		return feature, false
	}
	feature.longReturn = math.Max(longAsk, longBid)
	shortAsk := math.Log(end.ask / shortStart.ask)
	shortBid := math.Log(end.bid / shortStart.bid)
	shortReturn := 0.0
	switch {
	case shortAsk > 0 && shortBid > 0:
		shortReturn = math.Min(shortAsk, shortBid)
	case shortAsk < 0 && shortBid < 0:
		shortReturn = math.Max(shortAsk, shortBid)
	}
	var longQVAsk, longQVBid, shortQVAsk, shortQVBid float64
	var shortTVAsk, shortTVBid float64
	for i := anchor - longSteps + 1; i <= anchor; i++ {
		previous, point := closes[i-1], closes[i]
		if !point.at.Equal(previous.at.Add(time.Minute)) || previous.bid <= 0 || previous.ask <= 0 || point.bid <= 0 || point.ask <= 0 {
			return consolidationHazardFeature{}, false
		}
		askReturn := math.Log(point.ask / previous.ask)
		bidReturn := math.Log(point.bid / previous.bid)
		longQVAsk += askReturn * askReturn
		longQVBid += bidReturn * bidReturn
		if i > anchor-shortSteps {
			shortQVAsk += askReturn * askReturn
			shortQVBid += bidReturn * bidReturn
			shortTVAsk += math.Abs(askReturn)
			shortTVBid += math.Abs(bidReturn)
		}
	}
	longQV := math.Max(longQVAsk, longQVBid)
	shortQV := math.Max(shortQVAsk, shortQVBid)
	if longQV <= 0 || shortQV <= 0 || shortTVAsk <= 0 || shortTVBid <= 0 {
		return consolidationHazardFeature{}, false
	}
	efficiency := math.Max(math.Abs(shortAsk)/shortTVAsk, math.Abs(shortBid)/shortTVBid)
	// Under independent diffusion increments, E|sum r| / E sum|r| is
	// approximately 1/sqrt(n).  Dividing by this null scale makes <=1 a
	// predeclared short-term consolidation state rather than a fitted cutoff.
	nullEfficiency := 1 / math.Sqrt(float64(shortSteps))
	feature.efficiencyRatio = efficiency / nullEfficiency
	if feature.efficiencyRatio > 1 {
		return consolidationHazardFeature{}, false
	}
	feature.longZ = feature.longReturn / math.Sqrt(longQV)
	feature.shortZ = shortReturn / math.Sqrt(shortQV)
	expectedShortQV := longQV * float64(shortSteps) / float64(longSteps)
	epsilon := math.SmallestNonzeroFloat64
	feature.qvAccelerationLog = math.Log((shortQV + epsilon) / (expectedShortQV + epsilon))
	return feature, true
}

func consolidationOutcome(closes []minuteRegimeClose, anchor, steps int, costBps float64) (outcome int, upNet, downNet float64, passage int) {
	if anchor < 0 || steps <= 0 || anchor+steps >= len(closes) {
		return 0, 0, 0, 0
	}
	start := closes[anchor]
	cost := costBps / 10_000
	for step := 1; step <= steps; step++ {
		point := closes[anchor+step]
		if !point.at.Equal(start.at.Add(time.Duration(step) * time.Minute)) {
			return 0, 0, 0, 0
		}
		up := math.Log(point.bid/start.ask) - cost
		down := -math.Log(point.ask/start.bid) - cost
		upNet = math.Max(upNet, up)
		downNet = math.Max(downNet, down)
		if outcome == 0 {
			switch {
			case down >= 0 && up < 0:
				outcome, passage = 1, step
			case up >= 0 && down < 0:
				outcome, passage = -1, step
			}
		}
	}
	return outcome, math.Max(0, upNet), math.Max(0, downNet), passage
}

func hazardFeatureVector(feature consolidationHazardFeature) [4]float64 {
	return [4]float64{feature.longZ, feature.shortZ, feature.efficiencyRatio, feature.qvAccelerationLog}
}

func nearestConsolidationHazards(samples []consolidationHazardSample, current consolidationHazardFeature, confidenceZ float64) (consolidationHazardPrediction, bool) {
	prediction := consolidationHazardPrediction{Samples: len(samples)}
	if len(samples) == 0 {
		return prediction, false
	}
	means, scales := [4]float64{}, [4]float64{}
	for _, sample := range samples {
		values := hazardFeatureVector(sample.feature)
		for i, value := range values {
			means[i] += value
		}
	}
	for i := range means {
		means[i] /= float64(len(samples))
	}
	for _, sample := range samples {
		values := hazardFeatureVector(sample.feature)
		for i, value := range values {
			scales[i] += math.Pow(value-means[i], 2)
		}
	}
	for i := range scales {
		scales[i] = math.Sqrt(scales[i] / math.Max(1, float64(len(samples)-1)))
	}
	currentValues := hazardFeatureVector(current)
	neighbors := append([]consolidationHazardSample(nil), samples...)
	for i := range neighbors {
		values := hazardFeatureVector(neighbors[i].feature)
		for j, value := range values {
			if scales[j] > 0 {
				neighbors[i].distance += math.Pow((value-currentValues[j])/scales[j], 2)
			}
		}
	}
	sort.Slice(neighbors, func(i, j int) bool { return neighbors[i].distance < neighbors[j].distance })
	k := int(math.Ceil(math.Sqrt(float64(len(neighbors)))))
	if k > len(neighbors) {
		k = len(neighbors)
	}
	prediction.Neighbors = k
	selected := neighbors[:k]
	down, up, censored := 0, 0, 0
	values := make([]float64, 0, k)
	for _, sample := range selected {
		switch sample.outcome {
		case 1:
			down++
			values = append(values, -sample.downNetExcursion)
		case -1:
			up++
			values = append(values, sample.upNetExcursion)
		default:
			censored++
			values = append(values, 0)
		}
	}
	denominator := float64(k + 3)
	prediction.DownProbability = float64(down+1) / denominator
	prediction.UpProbability = float64(up+1) / denominator
	prediction.CensorProbability = float64(censored+1) / denominator
	resolved := down + up
	prediction.DownGivenMoveProbability = float64(down+1) / float64(resolved+2)
	prediction.DownGivenMoveLower = wilsonLower(down+1, resolved+2, confidenceZ)
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	if len(values) > 1 {
		for _, value := range values {
			variance += math.Pow(value-mean, 2)
		}
		variance /= float64(len(values) - 1)
	}
	prediction.ExpectedNetReturnBps = mean * 10_000
	prediction.ExpectedUpperBps = (mean + math.Max(0, confidenceZ)*math.Sqrt(variance/float64(len(values)))) * 10_000
	return prediction, true
}

func evaluateConsolidationHazard(closes []minuteRegimeClose, input consolidationHazardStudyInput) consolidationHazardStudyReport {
	report := consolidationHazardStudyReport{
		Mode: "standalone-bayesian-logistic-consolidation-competing-risk", Symbol: input.Symbol,
		From: input.From, To: input.To, HorizonMinutes: int(input.Horizon / time.Minute),
		ShortWindowMinutes: int(input.ShortWindow / time.Minute), LongWindowMinutes: int(input.LongWindow / time.Minute),
		RoundTripCostBps: input.RoundTripCostBps,
		Acceptance:       "reject: requires positive resolved Brier skill and fee-positive downside signals with a Wilson lower bound above the causal baseline",
	}
	horizonSteps := int(input.Horizon / time.Minute)
	shortSteps := int(input.ShortWindow / time.Minute)
	longSteps := int(input.LongWindow / time.Minute)
	decisionSteps := int(input.DecisionStep / time.Minute)
	if horizonSteps <= 0 || shortSteps < 2 || longSteps <= shortSteps || decisionSteps <= 0 || len(closes) <= longSteps+horizonSteps {
		return report
	}
	minimumSamples := input.MinimumSamples
	if minimumSamples < 8 {
		minimumSamples = 8
	}
	confidenceZ := input.ConfidenceZ
	if confidenceZ <= 0 {
		confidenceZ = 1.6448536269514722
	}
	var training []consolidationHazardSample
	days := make(map[string]*consolidationHazardDay)
	squaredError, climateSquaredError := 0.0, 0.0
	for index := longSteps; index+horizonSteps < len(closes); index++ {
		// Mature a non-overlapping training anchor before producing the current
		// prediction.  The current label can therefore never leak into its model.
		matureAnchor := index - horizonSteps
		if matureAnchor >= longSteps && matureAnchor%horizonSteps == 0 {
			if feature, ok := consolidationFeatureAt(closes, matureAnchor, shortSteps, longSteps); ok {
				outcome, upNet, downNet, passage := consolidationOutcome(closes, matureAnchor, horizonSteps, input.RoundTripCostBps)
				training = append(training, consolidationHazardSample{feature: feature, outcome: outcome, upNetExcursion: upNet, downNetExcursion: downNet, firstPassageMinutes: passage})
			}
		}
		if index%decisionSteps != 0 || len(training) < minimumSamples {
			continue
		}
		feature, ok := consolidationFeatureAt(closes, index, shortSteps, longSteps)
		if !ok || !closes[index+horizonSteps].at.Equal(closes[index].at.Add(input.Horizon)) {
			continue
		}
		prediction, ok := bayesianLogisticConsolidationHazard(training, feature, confidenceZ)
		if !ok {
			continue
		}
		prediction.At = closes[index].at
		prediction.LongReturnBps = feature.longReturn * 10_000
		prediction.LongZ = feature.longZ
		prediction.ShortZ = feature.shortZ
		prediction.ConsolidationEfficiency = feature.efficiencyRatio
		prediction.QVAccelerationLog = feature.qvAccelerationLog
		baseDown, baseUp := 0, 0
		for _, sample := range training {
			if sample.outcome == 1 {
				baseDown++
			} else if sample.outcome == -1 {
				baseUp++
			}
		}
		prediction.BaselineDownProbability = float64(baseDown+1) / float64(baseDown+baseUp+2)
		prediction.Signal = prediction.DownGivenMoveLower > prediction.BaselineDownProbability && prediction.ExpectedUpperBps < 0
		outcome, _, _, passage := consolidationOutcome(closes, index, horizonSteps, input.RoundTripCostBps)
		prediction.PassageMinutes = passage
		switch outcome {
		case 1:
			prediction.Outcome = "down-first"
			report.DownFirst++
		case -1:
			prediction.Outcome = "up-first"
			report.UpFirst++
		default:
			prediction.Outcome = "censored"
			report.Censored++
		}
		report.Predictions++
		dayKey := closes[index].at.Format(time.DateOnly)
		day := days[dayKey]
		if day == nil {
			day = &consolidationHazardDay{Day: dayKey}
			days[dayKey] = day
		}
		day.Predictions++
		switch outcome {
		case 1:
			day.DownFirst++
		case -1:
			day.UpFirst++
		default:
			day.Censored++
		}
		if outcome != 0 {
			y := 0.0
			if outcome == 1 {
				y = 1
			}
			error := prediction.DownGivenMoveProbability - y
			climateError := prediction.BaselineDownProbability - y
			squaredError += error * error
			climateSquaredError += climateError * climateError
			report.Resolved++
		}
		if prediction.Signal {
			report.Signals++
			day.Signals++
			switch outcome {
			case 1:
				report.SignalDownFirst++
			case -1:
				report.SignalUpFirst++
			default:
				report.SignalCensored++
			}
		}
		report.PredictionsDetail = append(report.PredictionsDetail, prediction)
	}
	if report.Resolved > 0 {
		report.Brier = squaredError / float64(report.Resolved)
		report.ClimatologyBrier = climateSquaredError / float64(report.Resolved)
		if report.ClimatologyBrier > 0 {
			report.BrierSkill = 1 - report.Brier/report.ClimatologyBrier
		}
	}
	resolvedSignals := report.SignalDownFirst + report.SignalUpFirst
	if resolvedSignals > 0 {
		report.SignalDownRate = float64(report.SignalDownFirst) / float64(resolvedSignals)
		report.SignalDownRateWilsonLower = wilsonLower(report.SignalDownFirst, resolvedSignals, confidenceZ)
	}
	allDown, allUp := 0, 0
	for _, sample := range training {
		if sample.outcome == 1 {
			allDown++
		} else if sample.outcome == -1 {
			allUp++
		}
	}
	if allDown+allUp > 0 {
		report.BaselineDownRate = float64(allDown) / float64(allDown+allUp)
	}
	keys := make([]string, 0, len(days))
	for key := range days {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		report.Daily = append(report.Daily, *days[key])
	}
	if report.BrierSkill > 0 && report.Signals > 0 && report.SignalDownRateWilsonLower > report.BaselineDownRate {
		report.Acceptance = "statistical direction gate passes; production Macro replay and turnover attribution remain required"
	}
	return report
}
