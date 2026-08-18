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

const volumeProfileRegressionMaxFeatures = 12

type volumeProfileStudyInput struct {
	competingPathStudyInput
	FillCoverage float64
}

type volumeProfileMinute struct {
	at                       time.Time
	startBid, startAsk       float64
	terminalBid, terminalAsk float64
	minAsk, maxBid           float64
	meanBid, meanAsk         float64
	bidArea, askArea         float64
	areaSecs                 float64
	imbalance, bboStateTag   float64
	profile                  gammacapture.VolumeProfileState
}

type volumeProfileObservation struct {
	at, maturity                                  time.Time
	baseline, volume                              [volumeProfileRegressionMaxFeatures]float64
	baselineN, volumeN                            int
	valueBps, buyMeanReturnBps, sellMeanReturnBps float64
}

type volumeProfileHorizonReport struct {
	OpeningHorizon                       string  `json:"openingHorizon"`
	CompletionHorizon                    string  `json:"completionHorizon"`
	LabelHorizon                         string  `json:"labelHorizon"`
	FillCoverage                         float64 `json:"fillCoverage"`
	BuyFillLatencyQuantile               string  `json:"buyFillLatencyQuantile"`
	SellFillLatencyQuantile              string  `json:"sellFillLatencyQuantile"`
	ProfileObservationRange              string  `json:"profileObservationRange"`
	ProfileHalfLife                      string  `json:"profileHalfLife"`
	LatencyCalibrationSamples            int     `json:"latencyCalibrationSamples"`
	LatencySufficient                    bool    `json:"latencySufficient"`
	ScoredSamples                        int     `json:"scoredSamples"`
	Days                                 int     `json:"days"`
	PositiveDays                         int     `json:"positiveDays"`
	BaselineMAEBps                       float64 `json:"baselineMAEBps"`
	CandidateMAEBps                      float64 `json:"candidateMAEBps"`
	IncrementalMAEBps                    float64 `json:"incrementalMAEBps"`
	BlockMeanIncrementalBps              float64 `json:"blockMeanIncrementalBps"`
	DayBlockStandardErrorBps             float64 `json:"dayBlockStandardErrorBps"`
	SimultaneousLowerBoundBps            float64 `json:"simultaneousLowerBoundBps"`
	RisingCandidateMAEBps                float64 `json:"risingCandidateMAEBps"`
	RisingIncrementalMAEBps              float64 `json:"risingIncrementalMAEBps"`
	RisingBlockMeanIncrementalBps        float64 `json:"risingBlockMeanIncrementalBps"`
	RisingDayBlockStandardErrorBps       float64 `json:"risingDayBlockStandardErrorBps"`
	RisingSimultaneousLowerBoundBps      float64 `json:"risingSimultaneousLowerBoundBps"`
	RisingEligibleSamples                int     `json:"risingEligibleSamples"`
	RisingPositiveDays                   int     `json:"risingPositiveDays"`
	BaselineBuyMAEBps                    float64 `json:"baselineBuyMAEBps"`
	CandidateBuyMAEBps                   float64 `json:"candidateBuyMAEBps"`
	BaselineSellMAEBps                   float64 `json:"baselineSellMAEBps"`
	CandidateSellMAEBps                  float64 `json:"candidateSellMAEBps"`
	POCEntryMeanReturnCorrelation        float64 `json:"pocEntryMeanReturnCorrelation"`
	CorridorDirectionalReturnCorrelation float64 `json:"corridorDirectionalReturnCorrelation"`
	MeanProfileBins                      float64 `json:"meanProfileBins"`
	MaxProfileBins                       int     `json:"maxProfileBins"`
}

type volumeProfileStudyReport struct {
	Name              string                       `json:"name"`
	Symbol            string                       `json:"symbol"`
	From              time.Time                    `json:"from"`
	To                time.Time                    `json:"to"`
	CalibrationTo     time.Time                    `json:"calibrationTo"`
	Causal            bool                         `json:"causal"`
	Outcome           string                       `json:"outcome"`
	ProfileUpdateCost string                       `json:"profileUpdateCost"`
	ProfileMemory     string                       `json:"profileMemory"`
	Results           []volumeProfileHorizonReport `json:"results"`
	Gate              string                       `json:"gate"`
	RisingGate        string                       `json:"risingGate"`
}

type smallEWRegression struct {
	dim      int
	halfLife time.Duration
	lastAt   time.Time
	count    int
	gram     [volumeProfileRegressionMaxFeatures][volumeProfileRegressionMaxFeatures]float64
	rhs      [3][volumeProfileRegressionMaxFeatures]float64
}

// residualCalibration is a zero-intercept, delayed-label stacking layer. It
// estimates how much of each raw volume residual forecast survives out of
// sample; unit ridge precision makes an uninformative alpha collapse to zero
// rather than displacing the BBO baseline.
type residualCalibration struct {
	halfLife time.Duration
	lastAt   time.Time
	count    int
	sxx      [3]float64
	sxy      [3]float64
}

type pairedCorrelation struct {
	n                   float64
	sumX, sumY          float64
	sumXX, sumYY, sumXY float64
}

func (c *pairedCorrelation) add(x, y float64) {
	c.n++
	c.sumX += x
	c.sumY += y
	c.sumXX += x * x
	c.sumYY += y * y
	c.sumXY += x * y
}

func (c pairedCorrelation) value() float64 {
	if c.n <= 1 {
		return 0
	}
	covariance := c.sumXY - c.sumX*c.sumY/c.n
	varianceX := c.sumXX - c.sumX*c.sumX/c.n
	varianceY := c.sumYY - c.sumY*c.sumY/c.n
	if varianceX <= 0 || varianceY <= 0 {
		return 0
	}
	return covariance / math.Sqrt(varianceX*varianceY)
}

func (c *residualCalibration) decay(at time.Time) {
	if c.lastAt.IsZero() {
		c.lastAt = at
		return
	}
	if !at.After(c.lastAt) || c.halfLife <= 0 {
		return
	}
	factor := math.Exp(-math.Ln2 * at.Sub(c.lastAt).Seconds() / c.halfLife.Seconds())
	for i := range c.sxx {
		c.sxx[i] *= factor
		c.sxy[i] *= factor
	}
	c.lastAt = at
}

func (c *residualCalibration) update(at time.Time, raw [3]float64, target [3]float64) {
	c.decay(at)
	for i := range raw {
		c.sxx[i] += raw[i] * raw[i]
		c.sxy[i] += raw[i] * target[i]
	}
	c.count++
}

func (c *residualCalibration) predict(raw [3]float64) (out [3]float64, ready bool) {
	if c.count < 30 {
		return out, false
	}
	for i := range raw {
		out[i] = raw[i] * c.sxy[i] / (1 + c.sxx[i])
	}
	return out, true
}

func (r *smallEWRegression) decay(at time.Time) {
	if r.lastAt.IsZero() {
		r.lastAt = at
		return
	}
	if !at.After(r.lastAt) || r.halfLife <= 0 {
		return
	}
	factor := math.Exp(-math.Ln2 * at.Sub(r.lastAt).Seconds() / r.halfLife.Seconds())
	for i := 0; i < r.dim; i++ {
		for j := 0; j < r.dim; j++ {
			r.gram[i][j] *= factor
		}
		for target := range r.rhs {
			r.rhs[target][i] *= factor
		}
	}
	r.lastAt = at
}

func (r *smallEWRegression) update(at time.Time, x [volumeProfileRegressionMaxFeatures]float64, value, rotation, direction float64) {
	r.decay(at)
	y := [3]float64{value, rotation, direction}
	for i := 0; i < r.dim; i++ {
		for j := 0; j < r.dim; j++ {
			r.gram[i][j] += x[i] * x[j]
		}
		for target := range y {
			r.rhs[target][i] += x[i] * y[target]
		}
	}
	r.count++
}

func (r *smallEWRegression) predict(x [volumeProfileRegressionMaxFeatures]float64) (out [3]float64, ready bool) {
	if r.dim <= 0 || r.count < 4*r.dim {
		return out, false
	}
	for target := range out {
		var matrix [volumeProfileRegressionMaxFeatures][volumeProfileRegressionMaxFeatures]float64
		var vector [volumeProfileRegressionMaxFeatures]float64
		for i := 0; i < r.dim; i++ {
			for j := 0; j < r.dim; j++ {
				matrix[i][j] = r.gram[i][j]
			}
			matrix[i][i]++
			vector[i] = r.rhs[target][i]
		}
		coefficients, ok := solveVolumeProfileSystem(matrix, vector, r.dim)
		if !ok {
			return [3]float64{}, false
		}
		for i := 0; i < r.dim; i++ {
			out[target] += coefficients[i] * x[i]
		}
	}
	// Every response in this study is terminal value measured in bps. An
	// earlier generic-regression prototype treated response 1 as a probability
	// and response 2 as a unit direction, truncating economically meaningful
	// terminal values. Bounds belong to the response contract, not this solver.
	return out, true
}

func solveVolumeProfileSystem(matrix [volumeProfileRegressionMaxFeatures][volumeProfileRegressionMaxFeatures]float64, vector [volumeProfileRegressionMaxFeatures]float64, dim int) ([volumeProfileRegressionMaxFeatures]float64, bool) {
	var result [volumeProfileRegressionMaxFeatures]float64
	for column := 0; column < dim; column++ {
		pivot := column
		for row := column + 1; row < dim; row++ {
			if math.Abs(matrix[row][column]) > math.Abs(matrix[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(matrix[pivot][column]) < 1e-12 {
			return result, false
		}
		matrix[column], matrix[pivot] = matrix[pivot], matrix[column]
		vector[column], vector[pivot] = vector[pivot], vector[column]
		inverse := 1 / matrix[column][column]
		for j := column; j < dim; j++ {
			matrix[column][j] *= inverse
		}
		vector[column] *= inverse
		for row := 0; row < dim; row++ {
			if row == column {
				continue
			}
			factor := matrix[row][column]
			for j := column; j < dim; j++ {
				matrix[row][j] -= factor * matrix[column][j]
			}
			vector[row] -= factor * vector[column]
		}
	}
	copy(result[:], vector[:dim])
	return result, true
}

func buildVolumeProfileMinutes(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, horizon, profileRange time.Duration, fillCoverage float64) []volumeProfileMinute {
	if len(books) == 0 {
		return nil
	}
	if profileRange <= 0 {
		profileRange = horizon
	}
	if fillCoverage <= 0 || fillCoverage >= 1 {
		fillCoverage = 0.8
	}
	profileHalfLife := time.Duration(profileRange.Seconds()*math.Ln2/-math.Log1p(-fillCoverage)) * time.Second
	profile := gammacapture.NewRollingVolumeProfile(gammacapture.VolumeProfileConfig{
		HalfLife: types.Duration(profileHalfLife), BinWidthBps: 1, MaxBins: 512,
	})
	model := &gammacapture.MarketMakerHorizonModel{}
	minutes := make([]volumeProfileMinute, 0, int(books[len(books)-1].time.Sub(books[0].time)/time.Minute)+1)
	tradeIndex := 0
	var current *volumeProfileMinute
	var lastBookAt time.Time
	finalize := func(minute *volumeProfileMinute, until time.Time) {
		if minute == nil {
			return
		}
		end := minute.at.Add(time.Minute)
		if until.After(end) {
			until = end
		}
		if !lastBookAt.IsZero() && until.After(lastBookAt) {
			seconds := until.Sub(lastBookAt).Seconds()
			minute.bidArea += minute.terminalBid * seconds
			minute.askArea += minute.terminalAsk * seconds
			minute.areaSecs += seconds
		}
		if minute.areaSecs > 0 {
			minute.meanBid = minute.bidArea / minute.areaSecs
			minute.meanAsk = minute.askArea / minute.areaSecs
		} else {
			minute.meanBid, minute.meanAsk = minute.terminalBid, minute.terminalAsk
		}
	}
	for _, book := range books {
		for tradeIndex < len(trades) && !trades[tradeIndex].time.After(book.time) {
			trade := trades[tradeIndex]
			profile.Observe(trade.time, trade.price, trade.size, trade.side == types.SideTypeBuy)
			tradeIndex++
		}
		model.ObserveBookWithSizes(book.time, book.bid, book.bidSize, book.ask, book.askSize, cfg)
		bucket := book.time.Truncate(time.Minute)
		if current == nil || !current.at.Equal(bucket) {
			previousBid, previousAsk := 0.0, 0.0
			if current != nil {
				previousBid, previousAsk = current.terminalBid, current.terminalAsk
				finalize(current, current.at.Add(time.Minute))
				minutes = append(minutes, *current)
			}
			imbalance := 0.0
			if total := book.bidSize + book.askSize; total > 0 {
				imbalance = math.Max(-1, math.Min(1, (book.bidSize-book.askSize)/total))
			}
			tag, _ := model.FastDriftBBOStateTag(horizon)
			current = &volumeProfileMinute{
				at: bucket, startBid: book.bid, startAsk: book.ask,
				terminalBid: book.bid, terminalAsk: book.ask,
				minAsk: book.ask, maxBid: book.bid,
				imbalance: imbalance, bboStateTag: tag,
				profile: profile.Snapshot(math.Sqrt(book.bid * book.ask)),
			}
			if previousBid > 0 && bucket.Sub(minutes[len(minutes)-1].at) == time.Minute && book.time.After(bucket) {
				seconds := book.time.Sub(bucket).Seconds()
				current.bidArea = previousBid * seconds
				current.askArea = previousAsk * seconds
				current.areaSecs = seconds
			}
			lastBookAt = book.time
			continue
		}
		if !lastBookAt.IsZero() && book.time.After(lastBookAt) {
			seconds := book.time.Sub(lastBookAt).Seconds()
			current.bidArea += current.terminalBid * seconds
			current.askArea += current.terminalAsk * seconds
			current.areaSecs += seconds
		}
		current.terminalBid, current.terminalAsk = book.bid, book.ask
		current.minAsk = math.Min(current.minAsk, book.ask)
		current.maxBid = math.Max(current.maxBid, book.bid)
		lastBookAt = book.time
	}
	if current != nil {
		finalize(current, current.at.Add(time.Minute))
		minutes = append(minutes, *current)
	}
	return minutes
}

type fillLatencyCoverageEstimate struct {
	Buy, Sell, ProfileRange time.Duration
	Eligible                int
	Sufficient              bool
	BuyWindowMass           []float64
	SellWindowMass          []float64
}

type simulatedSideFill struct {
	Index   int
	Elapsed time.Duration
	Quote   float64
}

// simulateQuoteLifecycle re-bases both quotes only when the selected Fast
// window expires or a BBO crossing event occurs. Minute records are an
// integration grid; they are never treated as unconditional quote decisions.
func simulateQuoteLifecycle(minutes []volumeProfileMinute, start int, horizon, maxLookback time.Duration, distanceBps float64) (buy, sell simulatedSideFill, valid bool) {
	step := int(horizon / time.Minute)
	limit := int(maxLookback / time.Minute)
	if step <= 0 || limit <= 0 || start < 0 || start+limit > len(minutes) {
		return buy, sell, false
	}
	end := start + limit
	for anchor := start; anchor < end && (buy.Index == 0 || sell.Index == 0); {
		if anchor > start && minutes[anchor].at.Sub(minutes[anchor-1].at) != time.Minute {
			return buy, sell, false
		}
		right := anchor + step
		if right > end {
			right = end
		}
		buyQuote := minutes[anchor].startAsk * math.Exp(-distanceBps/10_000)
		sellQuote := minutes[anchor].startBid * math.Exp(distanceBps/10_000)
		eventIndex := -1
		for index := anchor; index < right; index++ {
			if index > start && minutes[index].at.Sub(minutes[index-1].at) != time.Minute {
				return buy, sell, false
			}
			buyTouched := buy.Index == 0 && minutes[index].minAsk <= buyQuote
			sellTouched := sell.Index == 0 && minutes[index].maxBid >= sellQuote
			if !buyTouched && !sellTouched {
				continue
			}
			eventIndex = index
			elapsed := time.Duration(index-start+1) * time.Minute
			if buyTouched {
				buy = simulatedSideFill{Index: index + 1, Elapsed: elapsed, Quote: buyQuote}
			}
			if sellTouched {
				sell = simulatedSideFill{Index: index + 1, Elapsed: elapsed, Quote: sellQuote}
			}
			break
		}
		if eventIndex >= 0 {
			anchor = eventIndex + 1
		} else {
			anchor = right
		}
	}
	return buy, sell, true
}

// estimateFillLatencyCoverage uses only anchors whose full censoring window
// matures before calibrationTo. The empirical quantile is unconditional:
// censored paths remain in the denominator, so a side that does not reach the
// requested coverage is insufficient instead of receiving an extrapolated
// independent-window latency.
func estimateFillLatencyCoverage(minutes []volumeProfileMinute, calibrationTo time.Time, horizon, maxLookback time.Duration, distanceBps, coverage float64) fillLatencyCoverageEstimate {
	var estimate fillLatencyCoverageEstimate
	if len(minutes) == 0 || horizon <= 0 || maxLookback < horizon || distanceBps <= 0 || coverage <= 0 || coverage >= 1 {
		return estimate
	}
	step := int(horizon / time.Minute)
	limit := int(maxLookback / time.Minute)
	if step <= 0 || limit <= 0 {
		return estimate
	}
	maxWindows := limit / step
	buyCounts := make([]int, maxWindows)
	sellCounts := make([]int, maxWindows)
	for start := 0; start+limit < len(minutes) && minutes[start].at.Add(maxLookback).Before(calibrationTo.Add(time.Nanosecond)); start += step {
		if start > 0 && minutes[start].at.Sub(minutes[start-1].at) != time.Minute {
			continue
		}
		buyFill, sellFill, valid := simulateQuoteLifecycle(minutes, start, horizon, maxLookback, distanceBps)
		if !valid {
			continue
		}
		estimate.Eligible++
		if buyFill.Elapsed > 0 {
			window := int(math.Ceil(float64(buyFill.Elapsed)/float64(horizon))) - 1
			buyCounts[window]++
		}
		if sellFill.Elapsed > 0 {
			window := int(math.Ceil(float64(sellFill.Elapsed)/float64(horizon))) - 1
			sellCounts[window]++
		}
	}
	required := int(math.Ceil(coverage * float64(estimate.Eligible)))
	if required <= 0 {
		return estimate
	}
	quantileWindow := func(counts []int) int {
		cumulative := 0
		for window, count := range counts {
			cumulative += count
			if cumulative >= required {
				return window + 1
			}
		}
		return 0
	}
	buyWindows, sellWindows := quantileWindow(buyCounts), quantileWindow(sellCounts)
	if buyWindows == 0 || sellWindows == 0 {
		return estimate
	}
	estimate.Buy = time.Duration(buyWindows) * horizon
	estimate.Sell = time.Duration(sellWindows) * horizon
	profileWindows := buyWindows
	if sellWindows > profileWindows {
		profileWindows = sellWindows
	}
	estimate.ProfileRange = time.Duration(profileWindows) * horizon
	estimate.BuyWindowMass = make([]float64, profileWindows)
	estimate.SellWindowMass = make([]float64, profileWindows)
	for window := 0; window < profileWindows; window++ {
		estimate.BuyWindowMass[window] = float64(buyCounts[window]) / float64(estimate.Eligible)
		estimate.SellWindowMass[window] = float64(sellCounts[window]) / float64(estimate.Eligible)
	}
	estimate.Sufficient = true
	return estimate
}

func volumeProfileFeatures(minute, prior volumeProfileMinute) (baseline, volume [volumeProfileRegressionMaxFeatures]float64, baselineN, volumeN int) {
	baseline[0], baseline[1], baseline[2] = 1, minute.bboStateTag, minute.imbalance
	baselineN = 3
	profile := minute.profile.Vector(true)
	for i, value := range profile {
		volume[i] = value
	}
	density, flow, poc := profile[1], profile[2], profile[0]
	volume[5] = density * (1 - math.Abs(flow))
	volume[6] = (1 - density) * flow
	volume[7] = poc * flow
	priorProfile := prior.profile.Vector(true)
	volume[8] = math.Max(-1, math.Min(1, math.Abs(priorProfile[0])-math.Abs(profile[0])))
	volume[9] = math.Max(-1, math.Min(1, density-priorProfile[1]))
	volume[10] = math.Max(-1, math.Min(1, profile[4]-priorProfile[4]))
	volume[11] = volume[8] * density * (1 - math.Abs(flow))
	return baseline, volume, baselineN, 12
}

func volumeProfileCompletionHorizon(cfg gammacapture.MarketMakerConfig, opening time.Duration) time.Duration {
	completion := opening
	for _, candidate := range cfg.FastModelWindows() {
		if candidate > completion {
			completion = candidate
		}
	}
	return completion
}

func volumeProfileObservations(minutes []volumeProfileMinute, horizon time.Duration, distanceBps, feeBps float64, latency fillLatencyCoverageEstimate) []volumeProfileObservation {
	if horizon <= 0 || len(minutes) == 0 || !latency.Sufficient || latency.ProfileRange < horizon {
		return nil
	}
	segmentMinutes := int(horizon / time.Minute)
	totalMinutes := int((latency.ProfileRange + horizon) / time.Minute)
	out := make([]volumeProfileObservation, 0, len(minutes))
	for start := 0; start+totalMinutes <= len(minutes); start++ {
		if !minutes[start].profile.Valid {
			continue
		}
		buyFill, sellFill, valid := simulateQuoteLifecycle(minutes, start, horizon, latency.ProfileRange, distanceBps)
		if !valid {
			continue
		}
		futureMean := func(fill simulatedSideFill, buy bool) (float64, bool) {
			if fill.Elapsed <= 0 {
				return 0, true
			}
			meanLeft, meanRight := fill.Index, fill.Index+segmentMinutes
			if meanRight > len(minutes) {
				return 0, false
			}
			meanBid, meanAsk := 0.0, 0.0
			for index := meanLeft; index < meanRight; index++ {
				if (index > meanLeft && minutes[index].at.Sub(minutes[index-1].at) != time.Minute) || minutes[index].meanBid <= 0 || minutes[index].meanAsk < minutes[index].meanBid {
					return 0, false
				}
				meanBid += minutes[index].meanBid
				meanAsk += minutes[index].meanAsk
			}
			meanBid /= float64(segmentMinutes)
			meanAsk /= float64(segmentMinutes)
			if buy {
				return math.Log(meanBid/fill.Quote)*10_000 - feeBps, true
			}
			return math.Log(fill.Quote/meanAsk)*10_000 - feeBps, true
		}
		buyReturn, buyValid := futureMean(buyFill, true)
		sellReturn, sellValid := futureMean(sellFill, false)
		if !buyValid || !sellValid {
			continue
		}
		priorIndex := start - segmentMinutes
		if priorIndex < 0 || minutes[start].at.Sub(minutes[priorIndex].at) != horizon || !minutes[priorIndex].profile.Valid {
			continue
		}
		baseline, volume, baselineN, volumeN := volumeProfileFeatures(minutes[start], minutes[priorIndex])
		end := minutes[start+totalMinutes-1]
		out = append(out, volumeProfileObservation{
			at: minutes[start].at, maturity: end.at.Add(time.Minute),
			baseline: baseline, volume: volume, baselineN: baselineN, volumeN: volumeN,
			valueBps: 0.5 * (buyReturn + sellReturn), buyMeanReturnBps: buyReturn, sellMeanReturnBps: sellReturn,
		})
	}
	return out
}

func selectEventClockObservations(minutes []volumeProfileMinute, observations []volumeProfileObservation, horizon time.Duration, distanceBps float64) []volumeProfileObservation {
	if len(minutes) == 0 || len(observations) == 0 || horizon <= 0 {
		return nil
	}
	step := int(horizon / time.Minute)
	out := make([]volumeProfileObservation, 0, len(observations)/max(1, step))
	nextEligible := observations[0].at
	for _, observation := range observations {
		if observation.at.Before(nextEligible) {
			continue
		}
		start := sort.Search(len(minutes), func(index int) bool { return !minutes[index].at.Before(observation.at) })
		if start >= len(minutes) || !minutes[start].at.Equal(observation.at) {
			continue
		}
		out = append(out, observation)
		buyQuote := minutes[start].startAsk * math.Exp(-distanceBps/10_000)
		sellQuote := minutes[start].startBid * math.Exp(distanceBps/10_000)
		eventIndex := -1
		for index := start; index < len(minutes) && index < start+step; index++ {
			if index > start && minutes[index].at.Sub(minutes[index-1].at) != time.Minute {
				break
			}
			if minutes[index].minAsk <= buyQuote || minutes[index].maxBid >= sellQuote {
				eventIndex = index
				break
			}
		}
		if eventIndex >= 0 {
			nextEligible = minutes[eventIndex].at.Add(time.Minute)
		} else {
			nextEligible = observation.at.Add(horizon)
		}
	}
	return out
}

func scoreVolumeProfileHorizon(minutes []volumeProfileMinute, cfg gammacapture.MarketMakerConfig, horizon time.Duration, distanceBps, fillCoverage float64, latency fillLatencyCoverageEstimate, evaluationFrom time.Time) volumeProfileHorizonReport {
	completion := volumeProfileCompletionHorizon(cfg, horizon)
	observations := selectEventClockObservations(minutes,
		volumeProfileObservations(minutes, horizon, distanceBps, cfg.MakerFeeBps, latency),
		horizon, distanceBps)
	var pocEntryMean, corridorDirectional pairedCorrelation
	for _, observation := range observations {
		pocEntryMean.add(observation.volume[11], observation.valueBps)
		corridorDirectional.add(observation.volume[6], observation.buyMeanReturnBps-observation.sellMeanReturnBps)
	}
	halfLife := time.Duration(math.Sqrt(horizon.Seconds()*time.Duration(cfg.HorizonLookback).Seconds())) * time.Second
	baseline := smallEWRegression{dim: 3, halfLife: halfLife}
	residual := smallEWRegression{dim: 12, halfLife: halfLife}
	calibration := residualCalibration{halfLife: halfLife}
	risingResidual := smallEWRegression{dim: 12, halfLife: halfLife}
	risingCalibration := residualCalibration{halfLife: halfLife}
	type pendingPrediction struct {
		observation                                                    volumeProfileObservation
		base, rawResidual, residual, risingRawResidual, risingResidual [3]float64
		score, rising, baseReady, rawResidualReady, risingRawReady     bool
	}
	pending := make([]pendingPrediction, 0, (len(latency.BuyWindowMass)+1)*int(horizon/time.Minute)+2)
	daySums, dayCounts := make(map[string]float64), make(map[string]int)
	risingDaySums, risingDayCounts := make(map[string]float64), make(map[string]int)
	baseAbs, candidateAbs, risingCandidateAbs := 0.0, 0.0, 0.0
	baseBuyAbs, candidateBuyAbs, baseSellAbs, candidateSellAbs := 0.0, 0.0, 0.0, 0.0
	scored, risingEligible := 0, 0
	mature := func(p pendingPrediction) {
		o := p.observation
		candidatePrediction := [3]float64{
			p.base[0] + p.residual[0],
			p.base[1] + p.residual[1],
			p.base[2] + p.residual[2],
		}
		baseBuyError := math.Abs(p.base[1] - o.buyMeanReturnBps)
		baseSellError := math.Abs(p.base[2] - o.sellMeanReturnBps)
		baseError := 0.5 * (baseBuyError + baseSellError)
		if p.score {
			candidateBuyError := math.Abs(candidatePrediction[1] - o.buyMeanReturnBps)
			candidateSellError := math.Abs(candidatePrediction[2] - o.sellMeanReturnBps)
			candidateError := 0.5 * (candidateBuyError + candidateSellError)
			risingPrediction := p.base
			if p.rising {
				risingPrediction = [3]float64{
					p.base[0] + p.risingResidual[0],
					p.base[1] + p.risingResidual[1],
					p.base[2] + p.risingResidual[2],
				}
				risingEligible++
			}
			risingError := 0.5 * (math.Abs(risingPrediction[1]-o.buyMeanReturnBps) +
				math.Abs(risingPrediction[2]-o.sellMeanReturnBps))
			increment := baseError - candidateError
			risingIncrement := baseError - risingError
			baseAbs += baseError
			candidateAbs += candidateError
			risingCandidateAbs += risingError
			baseBuyAbs += baseBuyError
			candidateBuyAbs += candidateBuyError
			baseSellAbs += baseSellError
			candidateSellAbs += candidateSellError
			day := o.at.UTC().Format(time.DateOnly)
			daySums[day] += increment
			dayCounts[day]++
			risingDaySums[day] += risingIncrement
			risingDayCounts[day]++
			scored++
		}
		baseline.update(o.maturity, o.baseline, o.valueBps, o.buyMeanReturnBps, o.sellMeanReturnBps)
		// The volume model learns only the strictly-prequential BBO forecast's
		// matured residual. With no incremental information it shrinks to zero
		// and therefore cannot refit or displace the baseline itself.
		residual.update(o.maturity, o.volume,
			o.valueBps-p.base[0], o.buyMeanReturnBps-p.base[1], o.sellMeanReturnBps-p.base[2])
		if p.baseReady && p.rawResidualReady {
			calibration.update(o.maturity, p.rawResidual, [3]float64{
				o.valueBps - p.base[0], o.buyMeanReturnBps - p.base[1], o.sellMeanReturnBps - p.base[2],
			})
		}
		// The rising specialist is tagged at prediction time and learns only
		// after that exact label matures. Recomputing the sign here would leak
		// the later model state into historical regime membership.
		if p.rising && p.baseReady {
			risingResidual.update(o.maturity, o.volume,
				o.valueBps-p.base[0], o.buyMeanReturnBps-p.base[1], o.sellMeanReturnBps-p.base[2])
			if p.risingRawReady {
				risingCalibration.update(o.maturity, p.risingRawResidual, [3]float64{
					o.valueBps - p.base[0], o.buyMeanReturnBps - p.base[1], o.sellMeanReturnBps - p.base[2],
				})
			}
		}
	}
	for _, observation := range observations {
		for len(pending) > 0 && !pending[0].observation.maturity.After(observation.at) {
			mature(pending[0])
			pending = pending[1:]
		}
		basePrediction, baseReady := baseline.predict(observation.baseline)
		rawResidual, rawResidualReady := residual.predict(observation.volume)
		residualPrediction, calibrationReady := calibration.predict(rawResidual)
		risingRawResidual, risingRawReady := risingResidual.predict(observation.volume)
		risingResidualPrediction, risingCalibrationReady := risingCalibration.predict(risingRawResidual)
		rising := baseReady && 0.5*(basePrediction[1]-basePrediction[2]) > 0
		commonReady := baseReady && rawResidualReady && calibrationReady
		// Always queue the label until maturity. The previous warmup shortcut
		// updated models with future labels at prediction time.
		pending = append(pending, pendingPrediction{
			observation: observation, base: basePrediction,
			rawResidual: rawResidual, residual: residualPrediction,
			risingRawResidual: risingRawResidual, risingResidual: risingResidualPrediction,
			rising: rising, baseReady: baseReady, rawResidualReady: rawResidualReady,
			risingRawReady: risingRawReady,
			score: commonReady && (!rising || (risingRawReady && risingCalibrationReady)) &&
				!observation.at.Before(evaluationFrom),
		})
	}
	for _, prediction := range pending {
		mature(prediction)
	}
	profileHalfLife := time.Duration(latency.ProfileRange.Seconds()*math.Ln2/-math.Log1p(-fillCoverage)) * time.Second
	report := volumeProfileHorizonReport{
		OpeningHorizon: horizon.String(), CompletionHorizon: completion.String(),
		LabelHorizon: (time.Duration(len(latency.BuyWindowMass)+1) * horizon).String(), ScoredSamples: scored,
		FillCoverage: fillCoverage, BuyFillLatencyQuantile: latency.Buy.String(), SellFillLatencyQuantile: latency.Sell.String(),
		ProfileObservationRange: latency.ProfileRange.String(), ProfileHalfLife: profileHalfLife.String(),
		LatencyCalibrationSamples: latency.Eligible, LatencySufficient: latency.Sufficient,
	}
	report.RisingEligibleSamples = risingEligible
	report.POCEntryMeanReturnCorrelation = pocEntryMean.value()
	report.CorridorDirectionalReturnCorrelation = corridorDirectional.value()
	if scored == 0 {
		return report
	}
	n := float64(scored)
	report.BaselineMAEBps, report.CandidateMAEBps = baseAbs/n, candidateAbs/n
	report.IncrementalMAEBps = report.BaselineMAEBps - report.CandidateMAEBps
	report.RisingCandidateMAEBps = risingCandidateAbs / n
	report.RisingIncrementalMAEBps = report.BaselineMAEBps - report.RisingCandidateMAEBps
	report.BaselineBuyMAEBps, report.CandidateBuyMAEBps = baseBuyAbs/n, candidateBuyAbs/n
	report.BaselineSellMAEBps, report.CandidateSellMAEBps = baseSellAbs/n, candidateSellAbs/n
	dayMeans := make([]float64, 0, len(daySums))
	for day, sum := range daySums {
		mean := sum / float64(dayCounts[day])
		dayMeans = append(dayMeans, mean)
		if mean > 0 {
			report.PositiveDays++
		}
	}
	risingDayMeans := make([]float64, 0, len(risingDaySums))
	for day, sum := range risingDaySums {
		mean := sum / float64(risingDayCounts[day])
		risingDayMeans = append(risingDayMeans, mean)
		if mean > 0 {
			report.RisingPositiveDays++
		}
	}
	report.Days = len(dayMeans)
	if len(dayMeans) > 1 {
		mean := 0.0
		for _, value := range dayMeans {
			mean += value
		}
		mean /= float64(len(dayMeans))
		report.BlockMeanIncrementalBps = mean
		variance := 0.0
		for _, value := range dayMeans {
			variance += math.Pow(value-mean, 2)
		}
		variance /= float64(len(dayMeans) - 1)
		report.DayBlockStandardErrorBps = math.Sqrt(variance / float64(len(dayMeans)))
		report.SimultaneousLowerBoundBps = mean - 1.959963984540054*report.DayBlockStandardErrorBps
	}
	if len(risingDayMeans) > 1 {
		mean := 0.0
		for _, value := range risingDayMeans {
			mean += value
		}
		mean /= float64(len(risingDayMeans))
		report.RisingBlockMeanIncrementalBps = mean
		variance := 0.0
		for _, value := range risingDayMeans {
			variance += math.Pow(value-mean, 2)
		}
		variance /= float64(len(risingDayMeans) - 1)
		report.RisingDayBlockStandardErrorBps = math.Sqrt(variance / float64(len(risingDayMeans)))
		report.RisingSimultaneousLowerBoundBps = mean - 1.959963984540054*report.RisingDayBlockStandardErrorBps
	}
	for _, minute := range minutes {
		if minute.profile.Valid {
			report.MeanProfileBins += float64(minute.profile.Bins)
			if minute.profile.Bins > report.MaxProfileBins {
				report.MaxProfileBins = minute.profile.Bins
			}
		}
	}
	report.MeanProfileBins /= math.Max(1, float64(len(minutes)))
	return report
}

func runVolumeProfileStudy(in volumeProfileStudyInput) {
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	books, trades, _ := loadExactReplayDataset(in.DataPath, in.Symbol, in.From, in.To, "volume-profile-v1", in.ReplayCacheDir)
	if len(books) < 2 || len(trades) < 8 {
		fatalf("insufficient volume-profile data: books=%d trades=%d", len(books), len(trades))
	}
	distance := math.Max(1, cfg.MinimumHalfSpreadBps)
	coverage := in.FillCoverage
	if coverage <= 0 || coverage >= 1 {
		fatalf("volume-profile fill coverage must be in (0,1): %.6f", coverage)
	}
	calibrationTo := in.From.Add(in.To.Sub(in.From) / 2).Truncate(time.Minute)
	maxLookback := time.Duration(cfg.HorizonLookback)
	if maxLookback <= 0 {
		maxLookback = 6 * time.Hour
	}
	horizons := []time.Duration{15 * time.Minute, 30 * time.Minute}
	results := make([]volumeProfileHorizonReport, 0, len(horizons))
	latencyInsufficient := false
	for _, horizon := range horizons {
		provisional := buildVolumeProfileMinutes(books, trades, cfg, horizon, horizon, coverage)
		latency := estimateFillLatencyCoverage(provisional, calibrationTo, horizon, maxLookback, distance, coverage)
		if !latency.Sufficient {
			latencyInsufficient = true
			results = append(results, volumeProfileHorizonReport{
				OpeningHorizon: horizon.String(), CompletionHorizon: volumeProfileCompletionHorizon(cfg, horizon).String(),
				FillCoverage: coverage, LatencyCalibrationSamples: latency.Eligible, LatencySufficient: false,
			})
			continue
		}
		minutes := buildVolumeProfileMinutes(books, trades, cfg, horizon, latency.ProfileRange, coverage)
		results = append(results, scoreVolumeProfileHorizon(minutes, cfg, horizon, distance, coverage, latency, calibrationTo))
	}
	gate := "PROMOTE_COMPONENT_REPLAY"
	risingGate := "PROMOTE_COMPONENT_REPLAY"
	if latencyInsufficient {
		gate = "INCONCLUSIVE_LATENCY"
		risingGate = "INCONCLUSIVE_LATENCY"
	} else if len(results) != 2 || results[0].SimultaneousLowerBoundBps <= 0 || results[1].SimultaneousLowerBoundBps < 0 {
		gate = "REJECT_UNSTABLE"
	}
	if !latencyInsufficient && (len(results) != 2 || results[0].RisingSimultaneousLowerBoundBps <= 0 || results[1].RisingSimultaneousLowerBoundBps < 0) {
		risingGate = "REJECT_UNSTABLE"
	}
	report := volumeProfileStudyReport{
		Name: "multi-window-volume-profile-path-value", Symbol: in.Symbol, From: in.From, To: in.To, CalibrationTo: calibrationTo, Causal: true,
		Outcome:           "event-clock side-specific fee-net return to the next complete executable BBO mean; unfilled paths are zero",
		ProfileUpdateCost: "amortized O(1) per public trade; O(B) one-minute snapshot",
		ProfileMemory:     "O(W*B), B<=512 per Fast window, no raw-trade queue", Results: results, Gate: gate, RisingGate: risingGate,
	}
	sort.Slice(report.Results, func(i, j int) bool { return report.Results[i].OpeningHorizon < report.Results[j].OpeningHorizon })
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode volume-profile study: %v", err)
	}
}
