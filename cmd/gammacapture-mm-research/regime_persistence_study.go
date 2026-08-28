package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type regimePersistenceStudyInput struct {
	DataPath         string
	Symbol           string
	From             time.Time
	To               time.Time
	SampleInterval   time.Duration
	Horizon          time.Duration
	SlowLookback     time.Duration
	VolatilityWindow time.Duration
	RoundTripCostBps float64
	FilterConfig     gammacapture.RegimePersistenceConfig
}

type regimePersistenceStudyReport struct {
	Mode                string                             `json:"mode"`
	Symbol              string                             `json:"symbol"`
	From                time.Time                          `json:"from"`
	To                  time.Time                          `json:"to"`
	LoadedFrom          time.Time                          `json:"loadedFrom"`
	LoadedTo            time.Time                          `json:"loadedTo"`
	RequiredWarmup      string                             `json:"requiredWarmup"`
	RequiredForwardTail string                             `json:"requiredForwardTail"`
	ForwardTailReady    bool                               `json:"forwardTailReady"`
	BBOEvents           int                                `json:"bboEvents"`
	SampledEvents       int                                `json:"sampledEvents"`
	EligibleAnchors     int                                `json:"eligibleAnchors"`
	SegmentResets       int                                `json:"segmentResets"`
	SampleInterval      string                             `json:"sampleInterval"`
	Horizon             string                             `json:"horizon"`
	SlowLookback        string                             `json:"slowLookback"`
	VolatilityWindow    string                             `json:"volatilityWindow"`
	RoundTripCostBps    float64                            `json:"roundTripCostBps"`
	PersistenceConfig   regimePersistenceStudyConfigReport `json:"persistenceConfig"`
	RawTag              regimePersistenceArmReport         `json:"rawTag"`
	FilteredTag         regimePersistenceArmReport         `json:"filteredTag"`
	PairedValue         regimePersistencePairedReport      `json:"pairedValue"`
	ResearchWarnings    []string                           `json:"researchWarnings"`
}

type regimePersistenceStudyConfigReport struct {
	UpdateInterval    string  `json:"updateInterval"`
	SmoothingHalfLife string  `json:"smoothingHalfLife"`
	EnterThreshold    float64 `json:"enterThreshold"`
	ExitThreshold     float64 `json:"exitThreshold"`
	MinConfirmations  int     `json:"minConfirmations"`
	MinStateDuration  string  `json:"minStateDuration"`
	MaxGap            string  `json:"maxGap"`
}

type regimePersistenceArmReport struct {
	Name                  string  `json:"name"`
	Signals               int     `json:"signals"`
	UpSignals             int     `json:"upSignals"`
	DownSignals           int     `json:"downSignals"`
	Transitions           int     `json:"transitions"`
	BullishPositive       int     `json:"bullishPositive"`
	BullishMarkoutSamples int     `json:"bullishMarkoutSamples"`
	BullishPositiveRate   float64 `json:"bullishPositiveRate"`
	BullishMeanMarkoutBps float64 `json:"bullishMeanMarkoutBps"`
	BearishPositive       int     `json:"bearishPositive"`
	BearishMarkoutSamples int     `json:"bearishMarkoutSamples"`
	BearishPositiveRate   float64 `json:"bearishPositiveRate"`
	BearishMeanMarkoutBps float64 `json:"bearishMeanMarkoutBps"`
	MeanAbsoluteTag       float64 `json:"meanAbsoluteTag"`
}

type regimePersistencePairedReport struct {
	EligiblePairs           int     `json:"eligiblePairs"`
	EffectiveSamples        float64 `json:"effectiveSamples"`
	VarianceInflationFactor float64 `json:"varianceInflationFactor"`
	AutocorrelationLag      int     `json:"autocorrelationLag"`
	DependenceWindow        string  `json:"dependenceWindow"`
	RawMeanActionValueBps   float64 `json:"rawMeanActionValueBps"`
	FilteredMeanActionBps   float64 `json:"filteredMeanActionValueBps"`
	IncrementalMeanBps      float64 `json:"incrementalMeanBps"`
	IncrementalSEBps        float64 `json:"incrementalStandardErrorBps"`
	PositiveBlocks          int     `json:"positiveBlocks"`
	TotalBlocks             int     `json:"totalBlocks"`
	BlockDuration           string  `json:"blockDuration"`
}

type regimeSample struct {
	at             time.Time
	bid, ask       float64
	mid            float64
	slowScore      float64
	fastScore      float64
	rawTag         float64
	rawState       int
	filteredState  int
	filteredTag    float64
	filterDecision gammacapture.RegimePersistenceDecision
	eligible       bool
}

func runRegimePersistenceStudy(input regimePersistenceStudyInput) {
	config, err := prepareRegimePersistenceStudyConfig(input)
	if err != nil {
		fatalf("invalid regime-persistence study configuration: %v", err)
	}
	requiredWarmup := regimePersistenceRequiredWarmup(input, config)
	forwardTail := input.Horizon + 2*input.SampleInterval
	warmup := input.From.Add(-requiredWarmup)
	loadTo := input.To.Add(forwardTail)
	rawBooks := readBBO(input.DataPath, input.Symbol, warmup, loadTo)
	books := compactBBO(rawBooks)
	books = compactBBOAtInterval(books, input.SampleInterval)
	if len(books) < 10 {
		fatalf("insufficient BBO events for regime-persistence study: %d", len(books))
	}

	filter := gammacapture.NewRegimePersistenceFilter(config)
	var samples []regimeSample
	fromIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(input.From) })
	labelToIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(loadTo) })
	if fromIndex >= labelToIndex {
		fatalf("no BBO events in regime-persistence study interval")
	}
	segmentResets := 0
	if labelToIndex == 0 {
		fatalf("no BBO events through the forward label tail")
	}
	for i := 0; i < labelToIndex; i++ {
		book := books[i]
		slowIndex, okSlow := indexAtOrBefore(books, i, book.time.Add(-input.SlowLookback))
		volIndex, okVol := indexAtOrBefore(books, i, book.time.Add(-input.VolatilityWindow))
		fastIndex, okFast := indexAtOrBefore(books, i, book.time.Add(-input.SampleInterval))
		if !okSlow || !okVol || !okFast || book.midPrice() <= 0 || books[slowIndex].midPrice() <= 0 || books[fastIndex].midPrice() <= 0 {
			continue
		}
		volatility := rollingRegimeVolatility(books, volIndex, i)
		slowReturn := math.Log(book.midPrice() / books[slowIndex].midPrice())
		fastReturn := math.Log(book.midPrice() / books[fastIndex].midPrice())
		slowScale := math.Max(1e-6, volatility*math.Sqrt(float64(maxInt(1, i-slowIndex))))
		fastScale := math.Max(1e-6, volatility)
		slowScore := math.Tanh(slowReturn / slowScale)
		fastScore := math.Tanh(fastReturn / fastScale)
		rawTag := 0.5 * (slowScore + fastScore)
		rawState := thresholdRegimeState(rawTag, config.EnterThreshold)
		decision := filter.Observe(gammacapture.RegimePersistenceInput{
			At: book.time, SlowScore: slowScore, FastReversalScore: fastScore,
		})
		if decision.SegmentReset {
			segmentResets++
		}
		if book.time.Before(input.From) {
			continue
		}
		eligible := book.time.Before(input.To)
		samples = append(samples, regimeSample{
			at: book.time, bid: book.bid, ask: book.ask, mid: book.midPrice(),
			slowScore: slowScore, fastScore: fastScore, rawTag: rawTag, rawState: rawState,
			filteredState: decision.State, filteredTag: decision.Tag, filterDecision: decision,
			eligible: eligible,
		})
	}
	eligibleAnchors := 0
	for _, sample := range samples {
		if sample.eligible {
			eligibleAnchors++
		}
	}
	if eligibleAnchors == 0 {
		fatalf("no eligible regime anchors in study interval")
	}
	loadedFrom, loadedTo := time.Time{}, time.Time{}
	if len(books) > 0 {
		loadedFrom, loadedTo = books[0].time, books[len(books)-1].time
	}

	report := regimePersistenceStudyReport{
		Mode:                "standalone-regime-persistence-study",
		Symbol:              input.Symbol,
		From:                input.From,
		To:                  input.To,
		LoadedFrom:          loadedFrom,
		LoadedTo:            loadedTo,
		RequiredWarmup:      requiredWarmup.String(),
		RequiredForwardTail: forwardTail.String(),
		ForwardTailReady:    !loadedTo.Before(input.To.Add(input.Horizon)),
		BBOEvents:           len(rawBooks),
		SampledEvents:       len(books),
		EligibleAnchors:     eligibleAnchors,
		SegmentResets:       segmentResets,
		SampleInterval:      input.SampleInterval.String(),
		Horizon:             input.Horizon.String(),
		SlowLookback:        input.SlowLookback.String(),
		VolatilityWindow:    input.VolatilityWindow.String(),
		RoundTripCostBps:    input.RoundTripCostBps,
		PersistenceConfig: regimePersistenceStudyConfigReport{
			UpdateInterval: config.UpdateInterval.String(), SmoothingHalfLife: config.SmoothingHalfLife.String(),
			EnterThreshold: config.EnterThreshold, ExitThreshold: config.ExitThreshold,
			MinConfirmations: config.MinConfirmations, MinStateDuration: config.MinStateDuration.String(), MaxGap: config.MaxGap.String(),
		},
		ResearchWarnings: []string{
			"this is a causal tag/markout study, not a fill-faithful PnL replay",
			"public BBO has no private queue position or maker-fill calibration",
			"the raw baseline combines slow and fast scores; the filtered arm changes regime only from the slow score",
		},
	}
	report.RawTag = summarizeRegimePersistenceArm("raw-combined-tag", samples, input.Horizon, input.SampleInterval, input.RoundTripCostBps, false)
	report.FilteredTag = summarizeRegimePersistenceArm("slow-persistent-tag", samples, input.Horizon, input.SampleInterval, input.RoundTripCostBps, true)
	report.PairedValue = summarizeRegimePersistencePairedValue(samples, input.Horizon, input.SampleInterval, input.RoundTripCostBps, regimePersistenceDependenceWindow(config, input.Horizon))
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode regime-persistence study report: %v", err)
	}
}

func prepareRegimePersistenceStudyConfig(input regimePersistenceStudyInput) (gammacapture.RegimePersistenceConfig, error) {
	if !input.From.Before(input.To) || input.SampleInterval <= 0 || input.Horizon <= 0 || input.SlowLookback < input.SampleInterval || input.VolatilityWindow < input.SampleInterval {
		return gammacapture.RegimePersistenceConfig{}, fmt.Errorf("invalid interval, lookback, or horizon")
	}
	if input.RoundTripCostBps < 0 || math.IsNaN(input.RoundTripCostBps) || math.IsInf(input.RoundTripCostBps, 0) {
		return gammacapture.RegimePersistenceConfig{}, fmt.Errorf("round-trip cost must be finite and non-negative")
	}
	config := input.FilterConfig
	if config.UpdateInterval <= 0 {
		config.UpdateInterval = input.SampleInterval
	}
	if config.UpdateInterval != input.SampleInterval {
		return gammacapture.RegimePersistenceConfig{}, fmt.Errorf("filter update interval %s must equal sample interval %s", config.UpdateInterval, input.SampleInterval)
	}
	if config.SmoothingHalfLife <= 0 {
		config.SmoothingHalfLife = 3 * input.SampleInterval
	}
	if config.EnterThreshold <= 0 {
		config.EnterThreshold = 0.35
	}
	if config.ExitThreshold <= 0 {
		config.ExitThreshold = 0.15
	}
	if !finiteRegimeStudyValue(config.EnterThreshold) || !finiteRegimeStudyValue(config.ExitThreshold) || config.EnterThreshold <= 0 || config.EnterThreshold > 1 || config.ExitThreshold < 0 || config.ExitThreshold >= config.EnterThreshold {
		return gammacapture.RegimePersistenceConfig{}, fmt.Errorf("thresholds must satisfy 0 <= exit < enter <= 1")
	}
	if config.MinConfirmations <= 0 {
		config.MinConfirmations = 2
	}
	if config.MinStateDuration <= 0 {
		config.MinStateDuration = 2 * input.SampleInterval
	}
	if config.MaxGap <= 0 {
		config.MaxGap = 2 * input.SampleInterval
	}
	if config.MaxGap <= config.UpdateInterval {
		return gammacapture.RegimePersistenceConfig{}, fmt.Errorf("max gap %s must exceed update interval %s", config.MaxGap, config.UpdateInterval)
	}
	return config, nil
}

func regimePersistenceRequiredWarmup(input regimePersistenceStudyInput, config gammacapture.RegimePersistenceConfig) time.Duration {
	featureWarmup := maxDurationRegimeStudy(input.SlowLookback, input.VolatilityWindow) + 2*input.SampleInterval
	filterWarmup := 4 * config.SmoothingHalfLife
	confirmationWarmup := config.MinStateDuration + time.Duration(config.MinConfirmations+1)*config.UpdateInterval
	filterWarmup = maxDurationRegimeStudy(filterWarmup, confirmationWarmup)
	// The first valid feature consumes featureWarmup before the filter can
	// observe anything. Add, rather than max, the two memories so the filter
	// truly receives its requested causal history before the evaluation start.
	return featureWarmup + filterWarmup
}

func regimePersistenceDependenceWindow(config gammacapture.RegimePersistenceConfig, horizon time.Duration) time.Duration {
	filterMemory := maxDurationRegimeStudy(4*config.SmoothingHalfLife, config.MinStateDuration+time.Duration(config.MinConfirmations+1)*config.UpdateInterval)
	return maxDurationRegimeStudy(horizon, filterMemory)
}

func maxDurationRegimeStudy(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func finiteRegimeStudyValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (b bboSnapshot) midPrice() float64 { return 0.5 * (b.bid + b.ask) }

func indexAtOrBefore(books []bboSnapshot, end int, target time.Time) (int, bool) {
	if end <= 0 {
		return 0, false
	}
	index := sort.Search(end+1, func(i int) bool { return !books[i].time.Before(target) })
	if index > end {
		index = end
	}
	if books[index].time.After(target) {
		index--
	}
	return index, index >= 0
}

func rollingRegimeVolatility(books []bboSnapshot, from, to int) float64 {
	if from < 0 || to <= from || to >= len(books) {
		return 1e-4
	}
	returns := make([]float64, 0, to-from)
	for i := from + 1; i <= to; i++ {
		if books[i-1].midPrice() <= 0 || books[i].midPrice() <= 0 {
			continue
		}
		returns = append(returns, math.Log(books[i].midPrice()/books[i-1].midPrice()))
	}
	if len(returns) < 2 {
		return 1e-4
	}
	mean := 0.0
	for _, value := range returns {
		mean += value
	}
	mean /= float64(len(returns))
	variance := 0.0
	for _, value := range returns {
		delta := value - mean
		variance += delta * delta
	}
	return math.Max(1e-6, math.Sqrt(variance/float64(len(returns)-1)))
}

func thresholdRegimeState(score, threshold float64) int {
	if score >= threshold {
		return 1
	}
	if score <= -threshold {
		return -1
	}
	return 0
}

func summarizeRegimePersistenceArm(name string, samples []regimeSample, horizon, sampleInterval time.Duration, costBps float64, filtered bool) regimePersistenceArmReport {
	report := regimePersistenceArmReport{Name: name}
	previousState := 0
	var bullishMarkoutSum, bearishMarkoutSum float64
	for i := range samples {
		if !samples[i].eligible {
			continue
		}
		state := samples[i].rawState
		tag := samples[i].rawTag
		if filtered {
			state = samples[i].filteredState
			tag = samples[i].filteredTag
		}
		if state != previousState {
			report.Transitions++
		}
		previousState = state
		if state == 0 {
			continue
		}
		report.Signals++
		report.MeanAbsoluteTag += math.Abs(tag)
		future, ok := regimeFutureIndex(samples, i, horizon, sampleInterval)
		if !ok {
			continue
		}
		if state > 0 {
			report.UpSignals++
			markout := math.Log(samples[future].bid/samples[i].ask)*10_000 - costBps
			report.BullishMarkoutSamples++
			bullishMarkoutSum += markout
			if markout > 0 {
				report.BullishPositive++
			}
		} else {
			report.DownSignals++
			markout := -math.Log(samples[future].ask/samples[i].bid)*10_000 - costBps
			report.BearishMarkoutSamples++
			bearishMarkoutSum += markout
			if markout > 0 {
				report.BearishPositive++
			}
		}
	}
	if report.Signals > 0 {
		report.MeanAbsoluteTag /= float64(report.Signals)
	}
	if report.BullishMarkoutSamples > 0 {
		report.BullishMeanMarkoutBps = bullishMarkoutSum / float64(report.BullishMarkoutSamples)
		report.BullishPositiveRate = float64(report.BullishPositive) / float64(report.BullishMarkoutSamples)
	}
	if report.BearishMarkoutSamples > 0 {
		report.BearishMeanMarkoutBps = bearishMarkoutSum / float64(report.BearishMarkoutSamples)
		report.BearishPositiveRate = float64(report.BearishPositive) / float64(report.BearishMarkoutSamples)
	}
	return report
}

func summarizeRegimePersistencePairedValue(samples []regimeSample, horizon, sampleInterval time.Duration, costBps float64, dependenceWindow time.Duration) regimePersistencePairedReport {
	const blockDuration = 6 * time.Hour
	report := regimePersistencePairedReport{BlockDuration: blockDuration.String(), DependenceWindow: dependenceWindow.String()}
	differences := make([]float64, 0, len(samples))
	blockSums := make(map[int]float64)
	blockCounts := make(map[int]int)
	for i := range samples {
		if !samples[i].eligible {
			continue
		}
		future, ok := regimeFutureIndex(samples, i, horizon, sampleInterval)
		if !ok {
			continue
		}
		rawValue := regimeActionValue(samples[i], samples[future], samples[i].rawState, costBps)
		filteredValue := regimeActionValue(samples[i], samples[future], samples[i].filteredState, costBps)
		difference := filteredValue - rawValue
		report.EligiblePairs++
		report.RawMeanActionValueBps += rawValue
		report.FilteredMeanActionBps += filteredValue
		differences = append(differences, difference)
		block := int(samples[i].at.Sub(samples[0].at) / blockDuration)
		blockSums[block] += difference
		blockCounts[block]++
	}
	if report.EligiblePairs > 0 {
		report.RawMeanActionValueBps /= float64(report.EligiblePairs)
		report.FilteredMeanActionBps /= float64(report.EligiblePairs)
		report.IncrementalMeanBps = report.FilteredMeanActionBps - report.RawMeanActionValueBps
	}
	report.EffectiveSamples, report.VarianceInflationFactor, report.AutocorrelationLag, report.IncrementalSEBps = estimateRegimeEffectiveSamples(differences, sampleInterval, dependenceWindow)
	for block, sum := range blockSums {
		if blockCounts[block] == 0 {
			continue
		}
		report.TotalBlocks++
		if sum > 0 {
			report.PositiveBlocks++
		}
	}
	return report
}

// estimateRegimeEffectiveSamples applies a predeclared Bartlett/Newey-West
// dependence window to the paired incremental series. The raw anchor count is
// not an independent sample count because the label horizon overlaps and the
// persistent state intentionally carries information across buckets.
func estimateRegimeEffectiveSamples(values []float64, sampleInterval, dependenceWindow time.Duration) (effective, varianceInflation float64, lag int, standardError float64) {
	n := len(values)
	if n == 0 {
		return 0, 1, 0, 0
	}
	if n == 1 {
		return 1, 1, 0, 0
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(n)
	gamma0 := 0.0
	for _, value := range values {
		delta := value - mean
		gamma0 += delta * delta
	}
	gamma0 /= float64(n)
	if gamma0 <= 0 || math.IsNaN(gamma0) || math.IsInf(gamma0, 0) {
		return float64(n), 1, 0, 0
	}
	if sampleInterval > 0 && dependenceWindow > 0 {
		lag = int(dependenceWindow / sampleInterval)
	}
	if lag < 1 {
		lag = 1
	}
	if lag > n-1 {
		lag = n - 1
	}
	varianceInflation = 1
	for k := 1; k <= lag; k++ {
		covariance := 0.0
		for i := k; i < n; i++ {
			covariance += (values[i] - mean) * (values[i-k] - mean)
		}
		covariance /= float64(n)
		rho := covariance / gamma0
		weight := 1 - float64(k)/float64(lag+1)
		varianceInflation += 2 * weight * rho
	}
	if !finiteRegimeStudyValue(varianceInflation) || varianceInflation < 1 {
		varianceInflation = 1
	}
	effective = float64(n) / varianceInflation
	standardError = math.Sqrt(gamma0 * varianceInflation / float64(n))
	return effective, varianceInflation, lag, standardError
}

func regimeFutureIndex(samples []regimeSample, index int, horizon, sampleInterval time.Duration) (int, bool) {
	maturity := samples[index].at.Add(horizon)
	future := index + 1
	for future < len(samples) && samples[future].at.Before(maturity) {
		future++
	}
	if future >= len(samples) || samples[future].at.Sub(maturity) > 2*sampleInterval {
		return 0, false
	}
	if samples[index].ask <= samples[index].bid || samples[future].bid <= 0 || samples[future].ask <= samples[future].bid {
		return 0, false
	}
	return future, true
}

func regimeActionValue(start, future regimeSample, state int, costBps float64) float64 {
	switch {
	case state > 0:
		return math.Log(future.bid/start.ask)*10_000 - costBps
	case state < 0:
		return -math.Log(future.ask/start.bid)*10_000 - costBps
	default:
		return 0
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
