package main

import (
	"math"
	"sort"
	"strconv"
)

var earlyBumpMomentumFeatureNames = []string{
	"return5sBps", "return15sBps", "return30sBps", "returnAccelerationBps",
	"rebound30sBps", "drawdown5mBps", "ofi30s", "micropriceDisplacement", "spreadBps",
}

type earlyBumpMomentumModel struct {
	FeatureNames      []string  `json:"featureNames"`
	Means             []float64 `json:"means"`
	Scales            []float64 `json:"scales"`
	Coefficients      []float64 `json:"standardizedCoefficients"`
	Lambda            float64   `json:"lambda"`
	CrossValidatedMAE float64   `json:"crossValidatedMAE"`
	ResidualStdBps    float64   `json:"residualStdBps"`
	DevelopmentMAE    float64   `json:"developmentMAE"`
	EvaluationMAE     float64   `json:"evaluationMAE"`
	DevelopmentR2     float64   `json:"developmentR2"`
	EvaluationR2      float64   `json:"evaluationR2"`
}

type earlyBumpAdaptiveMetrics struct {
	ConfidencePenaltyZ         float64        `json:"confidencePenaltyZ"`
	Signals                    int            `json:"signals"`
	Actions                    int            `json:"actions"`
	ActionRate                 float64        `json:"actionRate"`
	MeanSelectedDeltaBps       float64        `json:"meanSelectedDeltaBps"`
	DeltaCounts                map[string]int `json:"deltaCounts"`
	Fills                      int            `json:"fills"`
	CaughtEscapes              int            `json:"caughtEscapes"`
	FeePositiveTouches         int            `json:"feePositiveTouches"`
	AdverseFills               int            `json:"adverseFills"`
	MeanNetBpsPerSignal        float64        `json:"meanNetBpsPerSignal"`
	PairedNetDeltaBpsPerSignal float64        `json:"pairedNetDeltaBpsPerSignal"`
	PairedDailyBootstrap95     [2]float64     `json:"pairedDailyBootstrap95"`
}

type earlyBumpAdaptivePeriod struct {
	Name     string                     `json:"name"`
	Policies []earlyBumpAdaptiveMetrics `json:"policies"`
}

type earlyBumpAdaptiveReport struct {
	Model       earlyBumpMomentumModel  `json:"model"`
	Formula     string                  `json:"formula"`
	Development earlyBumpAdaptivePeriod `json:"development"`
	Evaluation  earlyBumpAdaptivePeriod `json:"evaluation"`
	Decision    string                  `json:"decision"`
}

type fittedEarlyBumpMomentum struct {
	means, scales, coefficients []float64
	lambda, cvMAE, residualStd  float64
}

func earlyBumpFeatureVector(event earlyBumpEvent) []float64 {
	return []float64{
		event.Return5sBps,
		event.Return15sBps,
		event.Return30sBps,
		event.Return5sBps - event.Return30sBps/6,
		event.Rebound30sBps,
		event.Drawdown5mBps,
		event.OFI30s,
		event.Microprice,
		event.SpreadBps,
	}
}

func buildEarlyBumpAdaptiveReport(events []earlyBumpEvent, in earlyBumpStudyInput) *earlyBumpAdaptiveReport {
	if in.TestFrom.IsZero() {
		return nil
	}
	var development, evaluation []earlyBumpEvent
	for _, event := range events {
		if event.At.Before(in.TestFrom) {
			development = append(development, event)
		} else {
			evaluation = append(evaluation, event)
		}
	}
	if len(development) < 80 || len(evaluation) < 40 {
		return &earlyBumpAdaptiveReport{Decision: "insufficient chronological development or evaluation events"}
	}
	lambda, cvMAE := selectEarlyBumpLambda(development)
	fitted := fitEarlyBumpMomentum(development, lambda)
	fitted.lambda, fitted.cvMAE = lambda, cvMAE
	report := &earlyBumpAdaptiveReport{
		Formula: "delta = largest discrete grid value <= min(inside-distance, max(0, predicted 30s best-bid return - z*development residual sigma)); spread is both a feature and the inside-distance cap",
		Model: earlyBumpMomentumModel{
			FeatureNames: earlyBumpMomentumFeatureNames,
			Means:        fitted.means, Scales: fitted.scales, Coefficients: fitted.coefficients,
			Lambda: lambda, CrossValidatedMAE: cvMAE, ResidualStdBps: fitted.residualStd,
		},
	}
	report.Model.DevelopmentMAE, report.Model.DevelopmentR2 = momentumFitStats(development, fitted)
	report.Model.EvaluationMAE, report.Model.EvaluationR2 = momentumFitStats(evaluation, fitted)
	report.Development = summarizeAdaptivePeriod("development", development, fitted, in)
	report.Evaluation = summarizeAdaptivePeriod("evaluation", evaluation, fitted, in)
	report.Decision = "reject live promotion unless an evaluation policy catches escapes and its paired daily-bootstrap net interval is strictly positive"
	for _, policy := range report.Evaluation.Policies {
		if policy.CaughtEscapes > 0 && policy.PairedDailyBootstrap95[0] > 0 {
			report.Decision = "evaluation supports shadow promotion only; private queue fills are still required"
			break
		}
	}
	return report
}

func selectEarlyBumpLambda(events []earlyBumpEvent) (float64, float64) {
	lambdas := []float64{0.1, 1, 10, 100}
	bestLambda, bestMAE := lambdas[0], math.Inf(1)
	for _, lambda := range lambdas {
		totalError, samples := 0.0, 0
		for fold := 0; fold < 4; fold++ {
			trainEnd := len(events) * (4 + fold) / 8
			testEnd := len(events) * (5 + fold) / 8
			if trainEnd < 30 || testEnd <= trainEnd {
				continue
			}
			model := fitEarlyBumpMomentum(events[:trainEnd], lambda)
			for _, event := range events[trainEnd:testEnd] {
				totalError += math.Abs(model.predict(event) - event.FutureReturn30sBps)
				samples++
			}
		}
		if samples == 0 {
			continue
		}
		mae := totalError / float64(samples)
		if mae < bestMAE {
			bestLambda, bestMAE = lambda, mae
		}
	}
	return bestLambda, bestMAE
}

func fitEarlyBumpMomentum(events []earlyBumpEvent, lambda float64) fittedEarlyBumpMomentum {
	featureCount := len(earlyBumpMomentumFeatureNames)
	means := make([]float64, featureCount)
	scales := make([]float64, featureCount)
	for _, event := range events {
		for j, value := range earlyBumpFeatureVector(event) {
			means[j] += value
		}
	}
	for j := range means {
		means[j] /= float64(len(events))
	}
	for _, event := range events {
		for j, value := range earlyBumpFeatureVector(event) {
			diff := value - means[j]
			scales[j] += diff * diff
		}
	}
	for j := range scales {
		scales[j] = math.Sqrt(scales[j] / math.Max(1, float64(len(events)-1)))
		if scales[j] < 1e-9 {
			scales[j] = 1
		}
	}
	dimension := featureCount + 1
	normal := make([][]float64, dimension)
	for i := range normal {
		normal[i] = make([]float64, dimension)
	}
	target := make([]float64, dimension)
	for _, event := range events {
		row := make([]float64, dimension)
		row[0] = 1
		for j, value := range earlyBumpFeatureVector(event) {
			row[j+1] = (value - means[j]) / scales[j]
		}
		for i := range row {
			target[i] += row[i] * event.FutureReturn30sBps
			for j := range row {
				normal[i][j] += row[i] * row[j]
			}
		}
	}
	for j := 1; j < dimension; j++ {
		normal[j][j] += lambda
	}
	coefficients := solveEarlyBumpLinearSystem(normal, target)
	model := fittedEarlyBumpMomentum{means: means, scales: scales, coefficients: coefficients, lambda: lambda}
	residualSum := 0.0
	for _, event := range events {
		residual := event.FutureReturn30sBps - model.predict(event)
		residualSum += residual * residual
	}
	degrees := math.Max(1, float64(len(events)-dimension))
	model.residualStd = math.Sqrt(residualSum / degrees)
	return model
}

func solveEarlyBumpLinearSystem(matrix [][]float64, target []float64) []float64 {
	n := len(target)
	augmented := make([][]float64, n)
	for i := range augmented {
		augmented[i] = append(append([]float64(nil), matrix[i]...), target[i])
	}
	for column := 0; column < n; column++ {
		pivot := column
		for row := column + 1; row < n; row++ {
			if math.Abs(augmented[row][column]) > math.Abs(augmented[pivot][column]) {
				pivot = row
			}
		}
		augmented[column], augmented[pivot] = augmented[pivot], augmented[column]
		if math.Abs(augmented[column][column]) < 1e-12 {
			continue
		}
		divisor := augmented[column][column]
		for j := column; j <= n; j++ {
			augmented[column][j] /= divisor
		}
		for row := 0; row < n; row++ {
			if row == column {
				continue
			}
			factor := augmented[row][column]
			for j := column; j <= n; j++ {
				augmented[row][j] -= factor * augmented[column][j]
			}
		}
	}
	solution := make([]float64, n)
	for i := range solution {
		solution[i] = augmented[i][n]
	}
	return solution
}

func (m fittedEarlyBumpMomentum) predict(event earlyBumpEvent) float64 {
	if len(m.coefficients) == 0 {
		return 0
	}
	value := m.coefficients[0]
	for j, feature := range earlyBumpFeatureVector(event) {
		value += m.coefficients[j+1] * (feature - m.means[j]) / m.scales[j]
	}
	return value
}

func momentumFitStats(events []earlyBumpEvent, model fittedEarlyBumpMomentum) (mae, r2 float64) {
	if len(events) == 0 {
		return 0, 0
	}
	mean := 0.0
	for _, event := range events {
		mean += event.FutureReturn30sBps
	}
	mean /= float64(len(events))
	residual, total := 0.0, 0.0
	for _, event := range events {
		diff := model.predict(event) - event.FutureReturn30sBps
		mae += math.Abs(diff)
		residual += diff * diff
		centered := event.FutureReturn30sBps - mean
		total += centered * centered
	}
	mae /= float64(len(events))
	if total > 0 {
		r2 = 1 - residual/total
	}
	return mae, r2
}

func summarizeAdaptivePeriod(name string, events []earlyBumpEvent, model fittedEarlyBumpMomentum, in earlyBumpStudyInput) earlyBumpAdaptivePeriod {
	period := earlyBumpAdaptivePeriod{Name: name}
	for _, z := range []float64{0, 0.5, 1} {
		metric := earlyBumpAdaptiveMetrics{ConfidencePenaltyZ: z, Signals: len(events), DeltaCounts: make(map[string]int)}
		netSum, pairedSum, selectedDeltaSum := 0.0, 0.0, 0.0
		selected := make([]earlyBumpOutcome, len(events))
		for i, event := range events {
			index := adaptiveEarlyBumpOutcomeIndex(event, model, z, in.DeltasBps)
			outcome := event.Outcomes[index]
			selected[i] = outcome
			metric.DeltaCounts[formatDelta(outcome.DeltaBps)]++
			if index > 0 {
				metric.Actions++
				selectedDeltaSum += outcome.DeltaBps
			}
			if outcome.Filled {
				metric.Fills++
				netSum += outcome.TerminalNetMarkoutBps
			}
			if outcome.CaughtEscape {
				metric.CaughtEscapes++
			}
			if outcome.FeePositiveTouch {
				metric.FeePositiveTouches++
			}
			if outcome.AdverseAfterFill {
				metric.AdverseFills++
			}
			pairedSum += outcomeValue(outcome) - outcomeValue(event.Outcomes[0])
		}
		if metric.Signals > 0 {
			metric.ActionRate = float64(metric.Actions) / float64(metric.Signals)
			metric.MeanNetBpsPerSignal = netSum / float64(metric.Signals)
			metric.PairedNetDeltaBpsPerSignal = pairedSum / float64(metric.Signals)
		}
		if metric.Actions > 0 {
			metric.MeanSelectedDeltaBps = selectedDeltaSum / float64(metric.Actions)
		}
		metric.PairedDailyBootstrap95 = bootstrapAdaptiveDailyCI(events, selected)
		period.Policies = append(period.Policies, metric)
	}
	return period
}

func adaptiveEarlyBumpOutcomeIndex(event earlyBumpEvent, model fittedEarlyBumpMomentum, z float64, deltas []float64) int {
	forecast := math.Max(0, model.predict(event)-z*model.residualStd)
	capBps := math.Min(event.InsideDeltaBps, forecast)
	selected := 0
	for i, delta := range deltas {
		if delta <= capBps+1e-9 {
			selected = i
		}
	}
	return selected
}

func formatDelta(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func bootstrapAdaptiveDailyCI(events []earlyBumpEvent, selected []earlyBumpOutcome) [2]float64 {
	type block struct {
		sum float64
		n   int
	}
	byDay := make(map[string]block)
	for i, event := range events {
		key := event.At.UTC().Format("2006-01-02")
		value := outcomeValue(selected[i]) - outcomeValue(event.Outcomes[0])
		block := byDay[key]
		block.sum += value
		block.n++
		byDay[key] = block
	}
	if len(byDay) < 2 {
		return [2]float64{}
	}
	blocks := make([]block, 0, len(byDay))
	for _, block := range byDay {
		blocks = append(blocks, block)
	}
	// Deterministic xorshift avoids adding stochastic test output.
	state := uint64(42)
	values := make([]float64, 5000)
	for sample := range values {
		sum, count := 0.0, 0
		for range blocks {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			block := blocks[int(state%uint64(len(blocks)))]
			sum += block.sum
			count += block.n
		}
		if count > 0 {
			values[sample] = sum / float64(count)
		}
	}
	sort.Float64s(values)
	return [2]float64{values[int(.025*float64(len(values)-1))], values[int(.975*float64(len(values)-1))]}
}
