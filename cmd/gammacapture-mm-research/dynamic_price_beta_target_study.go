package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type dynamicPriceBetaTargetStudyInput struct {
	DataPath, Symbol string
	From, To         time.Time
	Horizon          time.Duration
	SampleInterval   time.Duration
	History          time.Duration
	ReplayCacheDir   string
}

type dynamicPriceBetaTargetStudyReport struct {
	Name                  string                             `json:"name"`
	Symbol                string                             `json:"symbol"`
	From                  time.Time                          `json:"from"`
	To                    time.Time                          `json:"to"`
	Horizon               string                             `json:"horizon"`
	SampleInterval        string                             `json:"sampleInterval"`
	History               string                             `json:"history"`
	Causal                bool                               `json:"causal"`
	ReplayCacheHit        bool                               `json:"replayCacheHit"`
	EligibleAnchors       int                                `json:"eligibleAnchors"`
	EffectiveAnchors      int                                `json:"effectiveAnchors"`
	ActiveAnchors         int                                `json:"activeAnchors"`
	ActivePositive        int                                `json:"activePositiveFutureReturns"`
	ActiveNegative        int                                `json:"activeNegativeFutureReturns"`
	RangeAnchors          int                                `json:"rangeAnchors"`
	DeclineAnchors        int                                `json:"declineAnchors"`
	MeanActiveReturn      float64                            `json:"meanActiveFutureReturnBps"`
	MeanInactiveReturn    float64                            `json:"meanInactiveFutureReturnBps"`
	MeanActiveCap         float64                            `json:"meanActivePriceBetaTarget"`
	MeanActiveZ           float64                            `json:"meanActiveZScore"`
	ExposureReductionCost float64                            `json:"diagnosticExposureReductionCostBps"`
	OutcomeBins           []dynamicPriceBetaTargetOutcomeBin `json:"outcomeBins"`
	FitComparisons        []dynamicPriceBetaTargetFitReport  `json:"fitComparisons"`
	BestFit               string                             `json:"bestFit"`
	Gate                  string                             `json:"gate"`
	Warning               string                             `json:"warning"`
}

type dynamicPriceBetaTargetObservation struct {
	z      float64
	active bool
	y      float64
}

type dynamicPriceBetaTargetOutcomeBin struct {
	Label      string  `json:"label"`
	Count      int     `json:"count"`
	MeanReturn float64 `json:"meanFutureReturnBps"`
	StdReturn  float64 `json:"stdFutureReturnBps"`
}

type dynamicPriceBetaTargetFitReport struct {
	Model              string  `json:"model"`
	TrainSamples       int     `json:"trainSamples"`
	OOSSamples         int     `json:"oosSamples"`
	OOSRMSE            float64 `json:"oosRMSEBps"`
	OOSMAE             float64 `json:"oosMAEBps"`
	OOSR2              float64 `json:"oosR2"`
	OOSCorrelation     float64 `json:"oosCorrelation"`
	OOSSignAccuracy    float64 `json:"oosSignAccuracy"`
	PositiveTestBlocks int     `json:"positiveTestBlocks"`
	TestBlocks         int     `json:"testBlocks"`
}

type dynamicPriceBetaTargetFitModel struct {
	name         string
	mean         float64
	activeMean   float64
	inactiveMean float64
	coefficients []float64
	blocks       []dynamicPriceBetaTargetIsotonicBlock
	neighbors    []dynamicPriceBetaTargetObservation
	nearest      int
	bandwidth    float64
}

type dynamicPriceBetaTargetIsotonicBlock struct {
	upper float64
	mean  float64
}

type dynamicPriceBetaTargetNeighbor struct {
	distance float64
	value    float64
}

func dynamicPriceBetaTargetMeanAndStd(values []float64) (float64, float64) {
	mean := meanDynamicPriceBetaTarget(values)
	if len(values) < 2 {
		return mean, 0
	}
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	return mean, math.Sqrt(variance / float64(len(values)-1))
}

func finiteDynamicPriceBetaTargetStudy(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func dynamicPriceBetaTargetOutcomeBinLabel(z float64) string {
	switch {
	case z < -1:
		return "z_lt_-1"
	case z < 0:
		return "z_-1_to_0"
	case z < 1:
		return "z_0_to_1"
	case z < 2:
		return "z_1_to_2"
	default:
		return "z_ge_2"
	}
}

func dynamicPriceBetaTargetOutcomeBins(observations []dynamicPriceBetaTargetObservation) []dynamicPriceBetaTargetOutcomeBin {
	labels := []string{"z_lt_-1", "z_-1_to_0", "z_0_to_1", "z_1_to_2", "z_ge_2"}
	values := make(map[string][]float64, len(labels))
	for _, label := range labels {
		values[label] = nil
	}
	for _, observation := range observations {
		label := dynamicPriceBetaTargetOutcomeBinLabel(observation.z)
		values[label] = append(values[label], observation.y)
	}
	report := make([]dynamicPriceBetaTargetOutcomeBin, 0, len(labels))
	for _, label := range labels {
		mean, std := dynamicPriceBetaTargetMeanAndStd(values[label])
		report = append(report, dynamicPriceBetaTargetOutcomeBin{
			Label: label, Count: len(values[label]), MeanReturn: mean, StdReturn: std,
		})
	}
	return report
}

func dynamicPriceBetaTargetSolveLinearSystem(matrix [][]float64, vector []float64) ([]float64, bool) {
	n := len(vector)
	augmented := make([][]float64, n)
	for row := 0; row < n; row++ {
		augmented[row] = make([]float64, n+1)
		copy(augmented[row], matrix[row])
		augmented[row][n] = vector[row]
	}
	for column := 0; column < n; column++ {
		pivot := column
		for row := column + 1; row < n; row++ {
			if math.Abs(augmented[row][column]) > math.Abs(augmented[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(augmented[pivot][column]) < 1e-10 {
			return nil, false
		}
		augmented[column], augmented[pivot] = augmented[pivot], augmented[column]
		pivotValue := augmented[column][column]
		for value := column; value <= n; value++ {
			augmented[column][value] /= pivotValue
		}
		for row := 0; row < n; row++ {
			if row == column {
				continue
			}
			factor := augmented[row][column]
			for value := column; value <= n; value++ {
				augmented[row][value] -= factor * augmented[column][value]
			}
		}
	}
	coefficients := make([]float64, n)
	for row := range coefficients {
		coefficients[row] = augmented[row][n]
	}
	return coefficients, true
}

func dynamicPriceBetaTargetFitFeatures(model string, z float64) []float64 {
	switch model {
	case "linear":
		return []float64{1, z}
	case "quadratic":
		return []float64{1, z, z * z}
	case "piecewise-linear":
		return []float64{1, z, math.Max(0, z-1), math.Max(0, z-2)}
	default:
		return nil
	}
}

func dynamicPriceBetaTargetFitOLS(name string, observations []dynamicPriceBetaTargetObservation) dynamicPriceBetaTargetFitModel {
	model := dynamicPriceBetaTargetFitModel{name: name}
	if strings.HasPrefix(name, "knn-") {
		nearest, err := strconv.Atoi(strings.TrimPrefix(name, "knn-"))
		if err == nil && nearest > 0 {
			model.neighbors = append([]dynamicPriceBetaTargetObservation(nil), observations...)
			model.nearest = nearest
		}
	}
	if strings.HasPrefix(name, "kernel-") {
		bandwidth, err := strconv.ParseFloat(strings.TrimPrefix(name, "kernel-"), 64)
		if err == nil && bandwidth > 0 {
			model.neighbors = append([]dynamicPriceBetaTargetObservation(nil), observations...)
			model.bandwidth = bandwidth
		}
	}
	allReturns := make([]float64, 0, len(observations))
	activeReturns := make([]float64, 0, len(observations))
	inactiveReturns := make([]float64, 0, len(observations))
	for _, observation := range observations {
		allReturns = append(allReturns, observation.y)
		if observation.active {
			activeReturns = append(activeReturns, observation.y)
		} else {
			inactiveReturns = append(inactiveReturns, observation.y)
		}
	}
	model.mean = meanDynamicPriceBetaTarget(allReturns)
	model.activeMean = meanDynamicPriceBetaTarget(activeReturns)
	model.inactiveMean = meanDynamicPriceBetaTarget(inactiveReturns)
	if len(activeReturns) == 0 {
		model.activeMean = model.mean
	}
	if len(inactiveReturns) == 0 {
		model.inactiveMean = model.mean
	}
	features := dynamicPriceBetaTargetFitFeatures(name, 0)
	if len(features) == 0 || len(observations) < len(features) {
		return model
	}
	gram := make([][]float64, len(features))
	for row := range gram {
		gram[row] = make([]float64, len(features))
	}
	target := make([]float64, len(features))
	for _, observation := range observations {
		features := dynamicPriceBetaTargetFitFeatures(name, observation.z)
		for row := range features {
			target[row] += features[row] * observation.y
			for column := range features {
				gram[row][column] += features[row] * features[column]
			}
		}
	}
	// A tiny ridge only stabilizes the polynomial basis; it does not materially
	// regularize the fitted response at this sample size.
	for diagonal := 1; diagonal < len(gram); diagonal++ {
		gram[diagonal][diagonal] += 1e-8
	}
	if coefficients, ok := dynamicPriceBetaTargetSolveLinearSystem(gram, target); ok {
		model.coefficients = coefficients
	}
	return model
}

func dynamicPriceBetaTargetFitIsotonic(name string, observations []dynamicPriceBetaTargetObservation) dynamicPriceBetaTargetFitModel {
	model := dynamicPriceBetaTargetFitOLS(name, observations)
	if len(observations) == 0 {
		return model
	}
	sorted := append([]dynamicPriceBetaTargetObservation(nil), observations...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].z < sorted[j].z })
	type pavaBlock struct {
		upper, sum float64
		count      int
	}
	blocks := make([]pavaBlock, 0, len(sorted))
	for _, observation := range sorted {
		blocks = append(blocks, pavaBlock{upper: observation.z, sum: observation.y, count: 1})
		for len(blocks) >= 2 {
			left, right := blocks[len(blocks)-2], blocks[len(blocks)-1]
			leftMean := left.sum / float64(left.count)
			rightMean := right.sum / float64(right.count)
			violates := rightMean < leftMean
			if name == "isotonic-decreasing" {
				violates = rightMean > leftMean
			}
			if !violates {
				break
			}
			blocks[len(blocks)-2] = pavaBlock{
				upper: right.upper, sum: left.sum + right.sum, count: left.count + right.count,
			}
			blocks = blocks[:len(blocks)-1]
		}
	}
	model.blocks = make([]dynamicPriceBetaTargetIsotonicBlock, 0, len(blocks))
	for _, block := range blocks {
		model.blocks = append(model.blocks, dynamicPriceBetaTargetIsotonicBlock{
			upper: block.upper, mean: block.sum / float64(block.count),
		})
	}
	return model
}

func (model dynamicPriceBetaTargetFitModel) predict(z float64) float64 {
	switch model.name {
	case "mean":
		return model.mean
	case "binary":
		if z > 1 {
			return model.activeMean
		}
		return model.inactiveMean
	case "isotonic-increasing", "isotonic-decreasing":
		if len(model.blocks) == 0 {
			return model.mean
		}
		for _, block := range model.blocks {
			if z <= block.upper {
				return block.mean
			}
		}
		return model.blocks[len(model.blocks)-1].mean
	default:
		if model.nearest > 0 && len(model.neighbors) > 0 {
			neighbors := make([]dynamicPriceBetaTargetNeighbor, 0, len(model.neighbors))
			for _, observation := range model.neighbors {
				neighbors = append(neighbors, dynamicPriceBetaTargetNeighbor{
					distance: math.Abs(observation.z - z), value: observation.y,
				})
			}
			sort.SliceStable(neighbors, func(i, j int) bool {
				return neighbors[i].distance < neighbors[j].distance
			})
			count := model.nearest
			if count > len(neighbors) {
				count = len(neighbors)
			}
			return meanDynamicPriceBetaTargetObservationValues(neighbors[:count])
		}
		if model.bandwidth > 0 && len(model.neighbors) > 0 {
			weighted, totalWeight := 0.0, 0.0
			for _, observation := range model.neighbors {
				distance := (observation.z - z) / model.bandwidth
				weight := math.Exp(-0.5 * distance * distance)
				weighted += weight * observation.y
				totalWeight += weight
			}
			if totalWeight > 0 {
				return weighted / totalWeight
			}
			return model.mean
		}
		features := dynamicPriceBetaTargetFitFeatures(model.name, z)
		if len(features) != len(model.coefficients) {
			return model.mean
		}
		prediction := 0.0
		for index, feature := range features {
			prediction += feature * model.coefficients[index]
		}
		if !finiteDynamicPriceBetaTargetStudy(prediction) {
			return model.mean
		}
		return prediction
	}
}

func meanDynamicPriceBetaTargetObservationValues(values []dynamicPriceBetaTargetNeighbor) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value.value
	}
	return total / float64(len(values))
}

func dynamicPriceBetaTargetFitComparison(observations []dynamicPriceBetaTargetObservation) []dynamicPriceBetaTargetFitReport {
	modelNames := []string{
		"mean", "binary", "linear", "quadratic", "piecewise-linear",
		"isotonic-increasing", "isotonic-decreasing",
		"knn-8", "knn-16", "knn-32", "kernel-0.5", "kernel-1", "kernel-2",
	}
	if len(observations) < 16 {
		return nil
	}
	foldSize := len(observations) / 4
	comparison := make([]dynamicPriceBetaTargetFitReport, 0, len(modelNames))
	for _, name := range modelNames {
		var sumSquared, sumAbsolute, sumY, sumY2, sumPrediction, sumPrediction2, sumJoint float64
		var total, signs int
		positiveBlocks := 0
		testBlocks := 0
		for fold := 1; fold < 4; fold++ {
			trainEnd := fold * foldSize
			testEnd := trainEnd + foldSize
			if fold == 3 {
				testEnd = len(observations)
			}
			train := observations[:trainEnd]
			test := observations[trainEnd:testEnd]
			model := dynamicPriceBetaTargetFitOLS(name, train)
			baseline := dynamicPriceBetaTargetFitOLS("mean", train)
			if name == "isotonic-increasing" || name == "isotonic-decreasing" {
				model = dynamicPriceBetaTargetFitIsotonic(name, train)
			}
			blockError := 0.0
			baselineBlockError := 0.0
			blockCount := 0
			for _, observation := range test {
				prediction := model.predict(observation.z)
				baselinePrediction := baseline.predict(observation.z)
				error := prediction - observation.y
				baselineError := baselinePrediction - observation.y
				sumSquared += error * error
				sumAbsolute += math.Abs(error)
				sumY += observation.y
				sumY2 += observation.y * observation.y
				sumPrediction += prediction
				sumPrediction2 += prediction * prediction
				sumJoint += prediction * observation.y
				total++
				blockError += error * error
				baselineBlockError += baselineError * baselineError
				blockCount++
				if (prediction >= 0) == (observation.y >= 0) {
					signs++
				}
			}
			if blockCount > 0 {
				testBlocks++
				if blockError < baselineBlockError {
					positiveBlocks++
				}
			}
		}
		if total == 0 {
			continue
		}
		meanY := sumY / float64(total)
		meanPrediction := sumPrediction / float64(total)
		totalVariance := sumY2 - 2*meanY*sumY + float64(total)*meanY*meanY
		sse := sumSquared
		oosR2 := 0.0
		if totalVariance > 0 {
			oosR2 = 1 - sse/totalVariance
		}
		correlationDenominator := math.Sqrt((sumY2 - float64(total)*meanY*meanY) *
			(sumPrediction2 - float64(total)*meanPrediction*meanPrediction))
		correlation := 0.0
		if correlationDenominator > 0 {
			correlation = (sumJoint - float64(total)*meanY*meanPrediction) / correlationDenominator
		}
		comparison = append(comparison, dynamicPriceBetaTargetFitReport{
			Model: name, TrainSamples: foldSize, OOSSamples: total,
			OOSRMSE: math.Sqrt(sse / float64(total)), OOSMAE: sumAbsolute / float64(total),
			OOSR2: oosR2, OOSCorrelation: correlation,
			OOSSignAccuracy:    float64(signs) / float64(total),
			PositiveTestBlocks: positiveBlocks, TestBlocks: testBlocks,
		})
	}
	return comparison
}

func meanDynamicPriceBetaTarget(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func rollingBidReturnVarianceBps(books []bboSnapshot, from, to int) float64 {
	if to-from < 3 {
		return 0
	}
	returns := make([]float64, 0, to-from-1)
	for i := from + 1; i < to; i++ {
		if books[i-1].bid <= 0 || books[i].bid <= 0 {
			continue
		}
		returns = append(returns, math.Log(books[i].bid/books[i-1].bid)*10_000)
	}
	if len(returns) < 2 {
		return 0
	}
	mean := meanDynamicPriceBetaTarget(returns)
	variance := 0.0
	for _, value := range returns {
		delta := value - mean
		variance += delta * delta
	}
	return variance / float64(len(returns)-1)
}

// runDynamicPriceBetaTargetStudy is a standalone causal behavior screen. It
// deliberately has no orders or inventory feedback: it checks whether the
// predeclared chase state is observable and whether the future executable-bid
// outcome is directionally consistent with activating a cap. It is not a PnL
// promotion gate; the paired replay remains a later stage.
func runDynamicPriceBetaTargetStudy(in dynamicPriceBetaTargetStudyInput) {
	if !in.From.Before(in.To) || in.Horizon <= 0 || in.SampleInterval <= 0 || in.History < in.Horizon {
		fatalf("invalid dynamic price-beta target study interval or horizon")
	}
	warmupFrom := in.From.Add(-in.History)
	books, _, cacheHit := loadMacroReplayDataset(
		in.DataPath, in.Symbol, warmupFrom, in.To.Add(in.Horizon), in.From,
		"dynamic-price-beta-target-study", in.ReplayCacheDir)
	books = compactBBOAtInterval(compactBBO(books), in.SampleInterval)
	if len(books) < 4 {
		fatalf("insufficient BBO events for dynamic price-beta target study: %d", len(books))
	}
	config := gammacapture.DynamicPriceBetaTargetConfig{
		Enabled: true, ChaseTarget: .20, ChaseZStart: 1, ChaseZFull: 2,
		PriorEffectiveSamples: 1,
	}
	fromIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(in.From) })
	toIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(in.To) })
	activeReturns, inactiveReturns, activeCaps, activeZ := []float64{}, []float64{}, []float64{}, []float64{}
	observations := make([]dynamicPriceBetaTargetObservation, 0)
	positive, negative, rangeCount, declineCount := 0, 0, 0, 0
	eligible := 0
	for i := fromIndex; i < toIndex; i++ {
		at := books[i].time
		if i == 0 || books[i].bid <= 0 {
			continue
		}
		lagAt := at.Add(-in.Horizon)
		lagIndex := sort.Search(i, func(j int) bool { return !books[j].time.Before(lagAt) })
		if lagIndex >= i || books[lagIndex].bid <= 0 {
			continue
		}
		futureAt := at.Add(in.Horizon)
		futureIndex := sort.Search(len(books), func(j int) bool { return !books[j].time.Before(futureAt) })
		if futureIndex >= len(books) || books[futureIndex].bid <= 0 {
			continue
		}
		if i-fromIndex > 0 && (i-fromIndex)%maxDynamicPriceBetaTargetStep(in.SampleInterval, in.Horizon) != 0 {
			continue
		}
		varianceFrom := sort.Search(i, func(j int) bool { return !books[j].time.Before(at.Add(-in.History)) })
		variance := rollingBidReturnVarianceBps(books, varianceFrom, i+1)
		pastReturn := math.Log(books[i].bid/books[lagIndex].bid) * 10_000
		decision := gammacapture.EvaluateDynamicPriceBetaTarget(config, gammacapture.DynamicPriceBetaTargetInput{
			CurrentInventoryRatio: .80, PolicyTargetRatio: .50,
			HardMinimumRatio: 0, HardMaximumRatio: 1,
			GrossInventoryReturnBps: pastReturn, PredictiveVarianceBps2: variance,
			EffectiveSamples: float64(i - varianceFrom),
		})
		futureReturn := math.Log(books[futureIndex].bid/books[i].bid) * 10_000
		eligible++
		observations = append(observations, dynamicPriceBetaTargetObservation{
			z: decision.ZScore, active: decision.Active, y: futureReturn,
		})
		if math.Abs(decision.ZScore) < 1 {
			rangeCount++
		}
		if decision.ZScore < 0 {
			declineCount++
		}
		if decision.Active {
			activeReturns = append(activeReturns, futureReturn)
			activeCaps = append(activeCaps, decision.Target)
			activeZ = append(activeZ, decision.ZScore)
			if futureReturn >= 0 {
				positive++
			} else {
				negative++
			}
		} else {
			inactiveReturns = append(inactiveReturns, futureReturn)
		}
	}
	activeTarget := meanDynamicPriceBetaTarget(activeCaps)
	proxyValue := 0.0
	if len(activeReturns) > 0 {
		// This is only a diagnostic inventory-mark proxy: it ignores partial
		// adjustment, fees, spread, and queue, so it cannot promote the alpha.
		proxyValue = meanDynamicPriceBetaTarget(activeReturns) * (.80 - activeTarget)
	}
	fitComparisons := dynamicPriceBetaTargetFitComparison(observations)
	bestFit := ""
	bestRMSE := math.Inf(1)
	for _, fit := range fitComparisons {
		if fit.Model == "mean" {
			continue
		}
		if fit.OOSRMSE < bestRMSE {
			bestRMSE = fit.OOSRMSE
			bestFit = fit.Model
		}
	}
	report := dynamicPriceBetaTargetStudyReport{
		Name: "dynamic-price-beta-target-chase-state", Symbol: in.Symbol,
		From: in.From, To: in.To, Horizon: in.Horizon.String(),
		SampleInterval: in.SampleInterval.String(), History: in.History.String(), Causal: true,
		EligibleAnchors: eligible, EffectiveAnchors: eligible, ActiveAnchors: len(activeReturns),
		ActivePositive: positive, ActiveNegative: negative, RangeAnchors: rangeCount,
		DeclineAnchors: declineCount, MeanActiveReturn: meanDynamicPriceBetaTarget(activeReturns),
		MeanInactiveReturn: meanDynamicPriceBetaTarget(inactiveReturns), MeanActiveCap: activeTarget,
		MeanActiveZ: meanDynamicPriceBetaTarget(activeZ), ExposureReductionCost: proxyValue,
		OutcomeBins: dynamicPriceBetaTargetOutcomeBins(observations), FitComparisons: fitComparisons,
		BestFit: bestFit,
		Gate:    "INCONCLUSIVE_STANDALONE_BEHAVIOR_SCREEN", ReplayCacheHit: cacheHit,
		Warning: "No orders, fees, partial adjustment, or private queue fills are modeled; use only to decide whether a component replay is warranted.",
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode dynamic price-beta target study: %v", err)
	}
}

func maxDynamicPriceBetaTargetStep(sampleInterval, horizon time.Duration) int {
	step := int(horizon / sampleInterval)
	if step < 1 {
		return 1
	}
	return step
}
