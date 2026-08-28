package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

// pivotThresholdStudy is a research-only calibration of the raw regime score
// against causal first-passage pivot labels.  The future pivot is used only as
// a matured label; no confirmed pivot is fed back into the score at its
// decision timestamp.
type pivotThresholdStudyInput struct {
	DataPath       string
	Symbol         string
	From           time.Time
	To             time.Time
	TrainTo        time.Time
	ValidationTo   time.Time
	SampleInterval time.Duration
	SlowLookback   time.Duration
	VolatilityWind time.Duration
	Horizon        time.Duration
	PivotBps       float64
	CostBps        float64
}

type pivotThresholdStudyReport struct {
	Mode               string                       `json:"mode"`
	Symbol             string                       `json:"symbol"`
	From               time.Time                    `json:"from"`
	To                 time.Time                    `json:"to"`
	TrainTo            time.Time                    `json:"trainTo"`
	ValidationTo       time.Time                    `json:"validationTo"`
	LoadedFrom         time.Time                    `json:"loadedFrom"`
	LoadedTo           time.Time                    `json:"loadedTo"`
	SampleInterval     string                       `json:"sampleInterval"`
	SlowLookback       string                       `json:"slowLookback"`
	VolatilityWindow   string                       `json:"volatilityWindow"`
	Horizon            string                       `json:"horizon"`
	PivotBps           float64                      `json:"pivotBps"`
	CostBps            float64                      `json:"costBps"`
	BBOEvents          int                          `json:"bboEvents"`
	SampledAnchors     int                          `json:"sampledAnchors"`
	ResolvedLabels     int                          `json:"resolvedLabels"`
	CensoredLabels     int                          `json:"censoredLabels"`
	FittedModel        pivotThresholdLogisticReport `json:"fittedModel"`
	Thresholds         []float64                    `json:"thresholds"`
	Train              pivotThresholdSplitReport    `json:"train"`
	Validation         pivotThresholdSplitReport    `json:"validation"`
	Holdout            pivotThresholdSplitReport    `json:"holdout"`
	ValidationBest     pivotThresholdArmReport      `json:"validationBest"`
	FittedBreakEven    float64                      `json:"fittedBreakEvenThreshold"`
	CurrentThreshold   float64                      `json:"currentThreshold"`
	CurrentProbability float64                      `json:"currentThresholdProbability"`
	CurrentExpectedBps float64                      `json:"currentThresholdExpectedNetBps"`
	Warnings           []string                     `json:"warnings"`
}

type pivotThresholdSplitReport struct {
	From     time.Time                 `json:"from"`
	To       time.Time                 `json:"to"`
	Anchors  int                       `json:"anchors"`
	Resolved int                       `json:"resolved"`
	Censored int                       `json:"censored"`
	Arms     []pivotThresholdArmReport `json:"arms"`
}

type pivotThresholdArmReport struct {
	Threshold          float64 `json:"threshold"`
	Signals            int     `json:"signals"`
	Resolved           int     `json:"resolved"`
	Correct            int     `json:"correct"`
	Incorrect          int     `json:"incorrect"`
	Censored           int     `json:"censored"`
	Precision          float64 `json:"precision"`
	Coverage           float64 `json:"coverage"`
	MeanNetPivotBps    float64 `json:"meanNetPivotBps"`
	ResolvedMeanNetBps float64 `json:"resolvedMeanNetBps"`
	PositiveBlocks     int     `json:"positiveBlocks"`
	TotalBlocks        int     `json:"totalBlocks"`
}

type pivotThresholdLogisticReport struct {
	Intercept     float64 `json:"intercept"`
	Slope         float64 `json:"slope"`
	Samples       int     `json:"samples"`
	PositiveRate  float64 `json:"positiveRate"`
	LogLoss       float64 `json:"logLoss"`
	BreakEvenProb float64 `json:"breakEvenProbability"`
	BreakEvenBps  float64 `json:"breakEvenPivotBps"`
}

type pivotThresholdSample struct {
	at       time.Time
	rawTag   float64
	bid, ask float64
	outcome  int
	resolved bool
}

type pivotThresholdFitPoint struct {
	x float64
	y float64
}

func runPivotThresholdStudy(input pivotThresholdStudyInput) {
	if err := validatePivotThresholdStudyInput(input); err != nil {
		fatalf("invalid pivot-threshold study configuration: %v", err)
	}
	warmup := input.SlowLookback
	if input.VolatilityWind > warmup {
		warmup = input.VolatilityWind
	}
	loadFrom := input.From.Add(-warmup)
	loadTo := input.To.Add(input.Horizon + 2*input.SampleInterval)
	rawBooks := readBBO(input.DataPath, input.Symbol, loadFrom, loadTo)
	books := compactBBO(rawBooks)
	books = compactBBOAtInterval(books, input.SampleInterval)
	if len(books) < 10 {
		fatalf("insufficient BBO events for pivot-threshold study: %d", len(books))
	}

	samples := buildPivotThresholdSamples(books, input)
	if len(samples) == 0 {
		fatalf("no causal pivot-threshold anchors")
	}
	fitPoints := make([]pivotThresholdFitPoint, 0, len(samples))
	for _, sample := range samples {
		if sample.resolved && math.Abs(sample.rawTag) > 1e-12 {
			correct := 0.0
			if sample.rawTag*float64(sample.outcome) > 0 {
				correct = 1
			}
			fitPoints = append(fitPoints, pivotThresholdFitPoint{x: math.Abs(sample.rawTag), y: correct})
		}
	}
	model := fitPivotThresholdLogistic(fitPoints)
	model.BreakEvenProb = pivotBreakEvenProbability(input.PivotBps, input.CostBps)
	model.BreakEvenBps = input.PivotBps
	breakEvenThreshold := pivotThresholdForProbability(model.Intercept, model.Slope, model.BreakEvenProb)

	thresholds := []float64{0.15, 0.25, 0.35, 0.45, 0.55, 0.65}
	train := summarizePivotThresholdSplit(samples, input.From, input.TrainTo, input, thresholds)
	validation := summarizePivotThresholdSplit(samples, input.TrainTo, input.ValidationTo, input, thresholds)
	holdout := summarizePivotThresholdSplit(samples, input.ValidationTo, input.To, input, thresholds)
	validationBest := selectBestPivotThresholdArm(validation.Arms)
	currentProbability := sigmoid(model.Intercept + model.Slope*0.35)
	currentExpected := 2*currentProbability*input.PivotBps - input.PivotBps - input.CostBps

	report := pivotThresholdStudyReport{
		Mode: "standalone-causal-pivot-threshold-study", Symbol: input.Symbol,
		From: input.From, To: input.To, TrainTo: input.TrainTo, ValidationTo: input.ValidationTo,
		LoadedFrom: loadFrom, LoadedTo: loadTo,
		SampleInterval: input.SampleInterval.String(), SlowLookback: input.SlowLookback.String(),
		VolatilityWindow: input.VolatilityWind.String(), Horizon: input.Horizon.String(),
		PivotBps: input.PivotBps, CostBps: input.CostBps, BBOEvents: len(rawBooks),
		SampledAnchors: len(samples), FittedModel: model, Thresholds: thresholds,
		Train: train, Validation: validation, Holdout: holdout, ValidationBest: validationBest,
		FittedBreakEven: breakEvenThreshold, CurrentThreshold: 0.35,
		CurrentProbability: currentProbability, CurrentExpectedBps: currentExpected,
		Warnings: []string{
			"Pivot labels are first-passage executable-BBO labels and are matured after the decision; they are not available online at the decision timestamp.",
			"The fitted break-even threshold is estimated on the training split only; validation and holdout are reported without refitting.",
		},
	}
	for _, sample := range samples {
		if sample.resolved {
			report.ResolvedLabels++
		} else {
			report.CensoredLabels++
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode pivot-threshold study: %v", err)
	}
}

func validatePivotThresholdStudyInput(input pivotThresholdStudyInput) error {
	if !input.From.Before(input.TrainTo) || !input.TrainTo.Before(input.ValidationTo) || !input.ValidationTo.Before(input.To) {
		return fmt.Errorf("require from < train-to < validation-to < to")
	}
	if input.SampleInterval <= 0 || input.SlowLookback <= 0 || input.VolatilityWind <= 0 || input.Horizon <= 0 {
		return fmt.Errorf("intervals and horizon must be positive")
	}
	if input.PivotBps <= 0 || input.CostBps < 0 || !finiteRegimeStudyValue(input.PivotBps) || !finiteRegimeStudyValue(input.CostBps) {
		return fmt.Errorf("pivot and cost must be finite, with positive pivot and non-negative cost")
	}
	return nil
}

func buildPivotThresholdSamples(books []bboSnapshot, input pivotThresholdStudyInput) []pivotThresholdSample {
	fromIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(input.From) })
	labelToIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(input.To) })
	samples := make([]pivotThresholdSample, 0, labelToIndex-fromIndex)
	for i := fromIndex; i < labelToIndex; i++ {
		book := books[i]
		slowIndex, okSlow := indexAtOrBefore(books, i, book.time.Add(-input.SlowLookback))
		volIndex, okVol := indexAtOrBefore(books, i, book.time.Add(-input.VolatilityWind))
		fastIndex, okFast := indexAtOrBefore(books, i, book.time.Add(-input.SampleInterval))
		if !okSlow || !okVol || !okFast || book.midPrice() <= 0 || books[slowIndex].midPrice() <= 0 || books[fastIndex].midPrice() <= 0 {
			continue
		}
		volatility := rollingRegimeVolatility(books, volIndex, i)
		slowReturn := math.Log(book.midPrice() / books[slowIndex].midPrice())
		fastReturn := math.Log(book.midPrice() / books[fastIndex].midPrice())
		slowScale := math.Max(1e-6, volatility*math.Sqrt(float64(maxInt(1, i-slowIndex))))
		fastScale := math.Max(1e-6, volatility)
		rawTag := 0.5 * (math.Tanh(slowReturn/slowScale) + math.Tanh(fastReturn/fastScale))
		outcome, resolved := firstExecutablePivotOutcome(books, i, input.Horizon, input.SampleInterval, input.PivotBps)
		samples = append(samples, pivotThresholdSample{at: book.time, rawTag: rawTag, bid: book.bid, ask: book.ask, outcome: outcome, resolved: resolved})
	}
	return samples
}

// firstExecutablePivotOutcome returns the first direction whose executable
// markout reaches the economic pivot. A missing BBO gap is censored rather
// than bridged, because the unobserved path could contain the pivot.
func firstExecutablePivotOutcome(books []bboSnapshot, index int, horizon, interval time.Duration, pivotBps float64) (int, bool) {
	if index < 0 || index >= len(books) || books[index].ask <= books[index].bid {
		return 0, false
	}
	deadline := books[index].time.Add(horizon)
	for j := index + 1; j < len(books) && books[j].time.Before(deadline); j++ {
		if books[j].time.Sub(books[j-1].time) > 2*interval {
			return 0, false
		}
		up := math.Log(books[j].bid/books[index].ask*1) * 10_000
		down := -math.Log(books[j].ask/books[index].bid*1) * 10_000
		upHit := up >= pivotBps
		downHit := down >= pivotBps
		if upHit && downHit {
			if up >= down {
				return 1, true
			}
			return -1, true
		}
		if upHit {
			return 1, true
		}
		if downHit {
			return -1, true
		}
	}
	return 0, false
}

func summarizePivotThresholdSplit(samples []pivotThresholdSample, from, to time.Time, input pivotThresholdStudyInput, thresholds []float64) pivotThresholdSplitReport {
	report := pivotThresholdSplitReport{From: from, To: to}
	for _, sample := range samples {
		if sample.at.Before(from) || !sample.at.Before(to) {
			continue
		}
		report.Anchors++
		if sample.resolved {
			report.Resolved++
		} else {
			report.Censored++
		}
	}
	report.Arms = make([]pivotThresholdArmReport, 0, len(thresholds))
	for _, threshold := range thresholds {
		report.Arms = append(report.Arms, summarizePivotThresholdArm(samples, from, to, input, threshold))
	}
	return report
}

func summarizePivotThresholdArm(samples []pivotThresholdSample, from, to time.Time, input pivotThresholdStudyInput, threshold float64) pivotThresholdArmReport {
	arm := pivotThresholdArmReport{Threshold: threshold}
	blockSums := make(map[int]float64)
	blockCounts := make(map[int]int)
	for _, sample := range samples {
		if sample.at.Before(from) || !sample.at.Before(to) || math.Abs(sample.rawTag) < threshold {
			continue
		}
		arm.Signals++
		if !sample.resolved {
			arm.Censored++
			continue
		}
		arm.Resolved++
		correct := sample.rawTag*float64(sample.outcome) > 0
		value := -input.PivotBps - input.CostBps
		if correct {
			arm.Correct++
			value = input.PivotBps - input.CostBps
		} else {
			arm.Incorrect++
		}
		arm.MeanNetPivotBps += value
		arm.ResolvedMeanNetBps += value
		block := int(sample.at.Sub(from) / (6 * time.Hour))
		blockSums[block] += value
		blockCounts[block]++
	}
	if arm.Signals > 0 {
		arm.MeanNetPivotBps /= float64(arm.Signals)
		arm.Coverage = float64(arm.Resolved) / float64(arm.Signals)
	}
	if arm.Resolved > 0 {
		arm.Precision = float64(arm.Correct) / float64(arm.Resolved)
		arm.ResolvedMeanNetBps /= float64(arm.Resolved)
	}
	for block, sum := range blockSums {
		if blockCounts[block] == 0 {
			continue
		}
		arm.TotalBlocks++
		if sum > 0 {
			arm.PositiveBlocks++
		}
	}
	return arm
}

func fitPivotThresholdLogistic(points []pivotThresholdFitPoint) pivotThresholdLogisticReport {
	report := pivotThresholdLogisticReport{Samples: len(points)}
	if len(points) == 0 {
		return report
	}
	positive := 0.0
	for _, point := range points {
		positive += point.y
	}
	report.PositiveRate = positive / float64(len(points))
	report.Intercept = logit(clampPivotProbability((positive + 0.5) / (float64(len(points)) + 1)))
	report.Slope = 0
	for iteration := 0; iteration < 40; iteration++ {
		g0, g1, h00, h01, h11 := 0.0, 0.0, 1e-8, 0.0, 1e-8
		for _, point := range points {
			p := sigmoid(report.Intercept + report.Slope*point.x)
			residual := point.y - p
			weight := math.Max(1e-8, p*(1-p))
			g0 += residual
			g1 += residual * point.x
			h00 += weight
			h01 += weight * point.x
			h11 += weight * point.x * point.x
		}
		determinant := h00*h11 - h01*h01
		if determinant <= 1e-12 {
			break
		}
		delta0 := (g0*h11 - g1*h01) / determinant
		delta1 := (g1*h00 - g0*h01) / determinant
		report.Intercept += delta0
		report.Slope += delta1
		if math.Abs(delta0)+math.Abs(delta1) < 1e-8 {
			break
		}
	}
	for _, point := range points {
		p := clampPivotProbability(sigmoid(report.Intercept + report.Slope*point.x))
		report.LogLoss += -(point.y*math.Log(p) + (1-point.y)*math.Log(1-p))
	}
	report.LogLoss /= float64(len(points))
	return report
}

func selectBestPivotThresholdArm(arms []pivotThresholdArmReport) pivotThresholdArmReport {
	best := pivotThresholdArmReport{}
	for _, arm := range arms {
		if arm.Resolved == 0 {
			continue
		}
		if best.Resolved == 0 || arm.MeanNetPivotBps > best.MeanNetPivotBps ||
			(arm.MeanNetPivotBps == best.MeanNetPivotBps && arm.Threshold > best.Threshold) {
			best = arm
		}
	}
	return best
}

func pivotBreakEvenProbability(pivotBps, costBps float64) float64 {
	return clampPivotProbability((pivotBps + costBps) / (2 * pivotBps))
}

func pivotThresholdForProbability(intercept, slope, probability float64) float64 {
	if slope <= 1e-9 {
		return 1
	}
	threshold := (logit(clampPivotProbability(probability)) - intercept) / slope
	return math.Max(0, math.Min(1, threshold))
}

func sigmoid(value float64) float64 {
	if value >= 0 {
		e := math.Exp(-value)
		return 1 / (1 + e)
	}
	e := math.Exp(value)
	return e / (1 + e)
}

func logit(probability float64) float64 {
	probability = clampPivotProbability(probability)
	return math.Log(probability / (1 - probability))
}

func clampPivotProbability(probability float64) float64 {
	return math.Max(1e-6, math.Min(1-1e-6, probability))
}
