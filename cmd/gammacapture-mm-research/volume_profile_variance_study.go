package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

const volumeVarianceEpsilonBps2 = 1e-6

// varianceQLIKEScale estimates the exact multiplicative scale minimizing
// Gaussian QLIKE for a frozen raw variance forecast:
//
//	s* = mean(e^2 / rawVariance).
//
// Averaging log residuals would target a geometric scale and is not calibrated
// for QLIKE or the arithmetic second moment consumed by inventory risk.
type varianceQLIKEScale struct {
	halfLife time.Duration
	lastAt   time.Time
	count    int
	weight   float64
	ratioSum [3]float64
}

func (b *varianceQLIKEScale) decay(at time.Time) {
	if b.lastAt.IsZero() {
		b.lastAt = at
		return
	}
	if !at.After(b.lastAt) || b.halfLife <= 0 {
		return
	}
	factor := math.Exp(-math.Ln2 * at.Sub(b.lastAt).Seconds() / b.halfLife.Seconds())
	b.weight *= factor
	for i := range b.ratioSum {
		b.ratioSum[i] *= factor
	}
	b.lastAt = at
}

func (b *varianceQLIKEScale) update(at time.Time, rawLogVariance, squaredResidual [3]float64) {
	b.decay(at)
	var ratios [3]float64
	for i := range b.ratioSum {
		actual := math.Max(volumeVarianceEpsilonBps2, squaredResidual[i])
		rawVariance := varianceFromLog(rawLogVariance[i])
		ratios[i] = actual / rawVariance
		if math.IsNaN(ratios[i]) || math.IsInf(ratios[i], 0) {
			return
		}
	}
	for i := range b.ratioSum {
		b.ratioSum[i] += ratios[i]
	}
	b.weight++
	b.count++
}

func varianceFromLog(logVariance float64) float64 {
	return math.Exp(math.Max(-20, math.Min(20, logVariance)))
}

func (b *varianceQLIKEScale) predict(rawLogVariance [3]float64) (variance [3]float64, ready bool) {
	if b.count < 30 || b.weight <= 0 {
		return variance, false
	}
	for i := range variance {
		variance[i] = varianceFromLog(rawLogVariance[i]) * b.ratioSum[i] / b.weight
		if variance[i] <= 0 || math.IsNaN(variance[i]) || math.IsInf(variance[i], 0) {
			return [3]float64{}, false
		}
	}
	return variance, true
}

func terminalResidualSquares(
	prediction [3]float64,
	observation volumeProfileObservation,
) [3]float64 {
	buy := observation.buyMeanReturnBps - prediction[1]
	sell := observation.sellMeanReturnBps - prediction[2]
	directional := 0.5 * (buy - sell)
	return [3]float64{directional * directional, buy * buy, sell * sell}
}

func logVarianceTargets(squared [3]float64) (target [3]float64) {
	for i := range squared {
		target[i] = math.Log(math.Max(volumeVarianceEpsilonBps2, squared[i]))
	}
	return target
}

func gaussianVarianceQLIKE(variance, squaredResidual float64) float64 {
	variance = math.Max(volumeVarianceEpsilonBps2, variance)
	return math.Log(variance) + math.Max(0, squaredResidual)/variance
}

type volumeProfileVarianceHorizonReport struct {
	Horizon                         string  `json:"horizon"`
	LabelHorizon                    string  `json:"labelHorizon"`
	ScoredSamples                   int     `json:"scoredSamples"`
	Days                            int     `json:"days"`
	PositiveDays                    int     `json:"positiveDays"`
	BaselineDirectionalQLIKE        float64 `json:"baselineDirectionalQLIKE"`
	CandidateDirectionalQLIKE       float64 `json:"candidateDirectionalQLIKE"`
	IncrementalDirectionalQLIKE     float64 `json:"incrementalDirectionalQLIKE"`
	BlockMeanIncrementalQLIKE       float64 `json:"blockMeanIncrementalQLIKE"`
	BlockStandardErrorQLIKE         float64 `json:"blockStandardErrorQLIKE"`
	SimultaneousLowerBoundQLIKE     float64 `json:"simultaneousLowerBoundQLIKE"`
	BuyIncrementalQLIKE             float64 `json:"buyIncrementalQLIKE"`
	SellIncrementalQLIKE            float64 `json:"sellIncrementalQLIKE"`
	BaselineDirectionalCalibration  float64 `json:"baselineDirectionalCalibration"`
	CandidateDirectionalCalibration float64 `json:"candidateDirectionalCalibration"`
	BaselineBuyCalibration          float64 `json:"baselineBuyCalibration"`
	CandidateBuyCalibration         float64 `json:"candidateBuyCalibration"`
	BaselineSellCalibration         float64 `json:"baselineSellCalibration"`
	CandidateSellCalibration        float64 `json:"candidateSellCalibration"`
	LatencySufficient               bool    `json:"latencySufficient"`
	LatencyCalibrationSamples       int     `json:"latencyCalibrationSamples"`
}

type volumeProfileVarianceReport struct {
	Name          string                               `json:"name"`
	Symbol        string                               `json:"symbol"`
	From          time.Time                            `json:"from"`
	To            time.Time                            `json:"to"`
	CalibrationTo time.Time                            `json:"calibrationTo"`
	Causal        bool                                 `json:"causal"`
	PrimaryMetric string                               `json:"primaryMetric"`
	Results       []volumeProfileVarianceHorizonReport `json:"results"`
	Gate          string                               `json:"gate"`
}

func scoreVolumeProfileVarianceHorizon(
	minutes []volumeProfileMinute,
	cfg gammacapture.MarketMakerConfig,
	horizon time.Duration,
	distanceBps, coverage float64,
	latency fillLatencyCoverageEstimate,
	evaluationAt time.Time,
) volumeProfileVarianceHorizonReport {
	observations := selectEventClockObservations(minutes,
		volumeProfileObservations(minutes, horizon, distanceBps, cfg.MakerFeeBps, latency),
		horizon, distanceBps)
	halfLife := time.Duration(math.Sqrt(horizon.Seconds()*time.Duration(cfg.HorizonLookback).Seconds())) * time.Second
	meanModel := smallEWRegression{dim: 3, halfLife: halfLife}
	baselineVariance := smallEWRegression{dim: 1, halfLife: halfLife}
	varianceResidual := smallEWRegression{dim: 12, halfLife: halfLife}
	varianceResidualCalibration := residualCalibration{halfLife: halfLife}
	baselineBias := varianceQLIKEScale{halfLife: halfLife}
	candidateBias := varianceQLIKEScale{halfLife: halfLife}

	type pendingVariancePrediction struct {
		observation                                                              volumeProfileObservation
		mean, rawBaselineLog, rawCandidateLog, rawVarianceResidual               [3]float64
		baselinePrediction, candidatePrediction                                  [3]float64
		meanReady, rawBaselineReady, rawCandidateReady, rawVarianceResidualReady bool
		score                                                                    bool
	}
	pending := make([]pendingVariancePrediction, 0, 128)
	daySums, dayCounts := make(map[string]float64), make(map[string]int)
	var baselineLoss, candidateLoss, baselineBuyLoss, candidateBuyLoss float64
	var baselineSellLoss, candidateSellLoss float64
	var actualSum, baselineVarianceSum, candidateVarianceSum [3]float64
	scored := 0
	mature := func(p pendingVariancePrediction) {
		o := p.observation
		if p.meanReady {
			squared := terminalResidualSquares(p.mean, o)
			if p.score {
				baseDirectional := gaussianVarianceQLIKE(p.baselinePrediction[0], squared[0])
				candidateDirectional := gaussianVarianceQLIKE(p.candidatePrediction[0], squared[0])
				increment := baseDirectional - candidateDirectional
				baselineLoss += baseDirectional
				candidateLoss += candidateDirectional
				baselineBuyLoss += gaussianVarianceQLIKE(p.baselinePrediction[1], squared[1])
				candidateBuyLoss += gaussianVarianceQLIKE(p.candidatePrediction[1], squared[1])
				baselineSellLoss += gaussianVarianceQLIKE(p.baselinePrediction[2], squared[2])
				candidateSellLoss += gaussianVarianceQLIKE(p.candidatePrediction[2], squared[2])
				for i := range squared {
					actualSum[i] += squared[i]
					baselineVarianceSum[i] += p.baselinePrediction[i]
					candidateVarianceSum[i] += p.candidatePrediction[i]
				}
				day := o.at.UTC().Format(time.DateOnly)
				daySums[day] += increment
				dayCounts[day]++
				scored++
			}
			target := logVarianceTargets(squared)
			baselineVariance.update(o.maturity,
				[volumeProfileRegressionMaxFeatures]float64{1}, target[0], target[1], target[2])
			if p.rawBaselineReady {
				baselineBias.update(o.maturity, p.rawBaselineLog, squared)
				varianceResidual.update(o.maturity, o.volume,
					target[0]-p.rawBaselineLog[0],
					target[1]-p.rawBaselineLog[1],
					target[2]-p.rawBaselineLog[2])
				if p.rawVarianceResidualReady {
					varianceResidualCalibration.update(o.maturity, p.rawVarianceResidual, [3]float64{
						target[0] - p.rawBaselineLog[0],
						target[1] - p.rawBaselineLog[1],
						target[2] - p.rawBaselineLog[2],
					})
				}
			}
			if p.rawCandidateReady {
				candidateBias.update(o.maturity, p.rawCandidateLog, squared)
			}
		}
		meanModel.update(o.maturity, o.baseline,
			o.valueBps, o.buyMeanReturnBps, o.sellMeanReturnBps)
	}

	for _, observation := range observations {
		for len(pending) > 0 && !pending[0].observation.maturity.After(observation.at) {
			mature(pending[0])
			pending = pending[1:]
		}
		meanPrediction, meanReady := meanModel.predict(observation.baseline)
		rawBaselineLog, rawBaselineReady := baselineVariance.predict(
			[volumeProfileRegressionMaxFeatures]float64{1})
		rawVarianceResidual, rawVarianceResidualReady := varianceResidual.predict(observation.volume)
		calibratedVarianceResidual, varianceResidualReady := varianceResidualCalibration.predict(rawVarianceResidual)
		rawCandidateLog := rawBaselineLog
		if rawBaselineReady && rawVarianceResidualReady && varianceResidualReady {
			for i := range rawCandidateLog {
				rawCandidateLog[i] += calibratedVarianceResidual[i]
			}
		}
		rawCandidateReady := rawBaselineReady && rawVarianceResidualReady && varianceResidualReady
		baselinePrediction, baselineReady := baselineBias.predict(rawBaselineLog)
		candidatePrediction, candidateReady := candidateBias.predict(rawCandidateLog)
		candidateReady = candidateReady && rawCandidateReady
		pending = append(pending, pendingVariancePrediction{
			observation: observation, mean: meanPrediction,
			rawBaselineLog: rawBaselineLog, rawCandidateLog: rawCandidateLog,
			rawVarianceResidual: rawVarianceResidual,
			baselinePrediction:  baselinePrediction, candidatePrediction: candidatePrediction,
			meanReady: meanReady, rawBaselineReady: rawBaselineReady,
			rawCandidateReady: rawCandidateReady, rawVarianceResidualReady: rawVarianceResidualReady,
			score: meanReady && baselineReady && candidateReady && !observation.at.Before(evaluationAt),
		})
	}
	for _, prediction := range pending {
		mature(prediction)
	}

	report := volumeProfileVarianceHorizonReport{
		Horizon:       horizon.String(),
		LabelHorizon:  (time.Duration(len(latency.BuyWindowMass)+1) * horizon).String(),
		ScoredSamples: scored, LatencySufficient: latency.Sufficient,
		LatencyCalibrationSamples: latency.Eligible,
	}
	if scored == 0 {
		return report
	}
	n := float64(scored)
	report.BaselineDirectionalQLIKE = baselineLoss / n
	report.CandidateDirectionalQLIKE = candidateLoss / n
	report.IncrementalDirectionalQLIKE = report.BaselineDirectionalQLIKE - report.CandidateDirectionalQLIKE
	report.BuyIncrementalQLIKE = (baselineBuyLoss - candidateBuyLoss) / n
	report.SellIncrementalQLIKE = (baselineSellLoss - candidateSellLoss) / n
	calibration := func(actual, predicted float64) float64 {
		if predicted <= 0 {
			return 0
		}
		return actual / predicted
	}
	report.BaselineDirectionalCalibration = calibration(actualSum[0], baselineVarianceSum[0])
	report.CandidateDirectionalCalibration = calibration(actualSum[0], candidateVarianceSum[0])
	report.BaselineBuyCalibration = calibration(actualSum[1], baselineVarianceSum[1])
	report.CandidateBuyCalibration = calibration(actualSum[1], candidateVarianceSum[1])
	report.BaselineSellCalibration = calibration(actualSum[2], baselineVarianceSum[2])
	report.CandidateSellCalibration = calibration(actualSum[2], candidateVarianceSum[2])
	dayMeans := make([]float64, 0, len(daySums))
	for day, sum := range daySums {
		mean := sum / float64(dayCounts[day])
		dayMeans = append(dayMeans, mean)
		if mean > 0 {
			report.PositiveDays++
		}
	}
	report.Days = len(dayMeans)
	if len(dayMeans) > 1 {
		for _, value := range dayMeans {
			report.BlockMeanIncrementalQLIKE += value
		}
		report.BlockMeanIncrementalQLIKE /= float64(len(dayMeans))
		variance := 0.0
		for _, value := range dayMeans {
			variance += math.Pow(value-report.BlockMeanIncrementalQLIKE, 2)
		}
		variance /= float64(len(dayMeans) - 1)
		report.BlockStandardErrorQLIKE = math.Sqrt(variance / float64(len(dayMeans)))
		report.SimultaneousLowerBoundQLIKE = report.BlockMeanIncrementalQLIKE -
			1.959963984540054*report.BlockStandardErrorQLIKE
	}
	return report
}

func runVolumeProfileVarianceStudy(in volumeProfileStudyInput) {
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	books, trades, _ := loadExactReplayDataset(
		in.DataPath, in.Symbol, in.From, in.To, "volume-profile-variance-v1", in.ReplayCacheDir)
	if len(books) < 2 || len(trades) < 8 {
		fatalf("insufficient volume-profile variance data: books=%d trades=%d", len(books), len(trades))
	}
	calibrationTo := in.From.Add(in.To.Sub(in.From) / 2).Truncate(time.Minute)
	distance := math.Max(1, cfg.MinimumHalfSpreadBps)
	lookback := time.Duration(cfg.HorizonLookback)
	if lookback <= 0 {
		lookback = 6 * time.Hour
	}
	coverage := in.FillCoverage
	if coverage <= 0 || coverage >= 1 {
		fatalf("volume-profile variance fill coverage must be in (0,1): %.6f", coverage)
	}
	report := volumeProfileVarianceReport{
		Name: "volume-profile-conditional-terminal-variance", Symbol: in.Symbol,
		From: in.From, To: in.To, CalibrationTo: calibrationTo, Causal: true,
		PrimaryMetric: "Gaussian QLIKE improvement for directional executable-terminal squared residual",
	}
	latencyInsufficient := false
	for _, horizon := range []time.Duration{15 * time.Minute, 30 * time.Minute} {
		provisional := buildVolumeProfileMinutes(books, trades, cfg, horizon, horizon, coverage)
		latency := estimateFillLatencyCoverage(provisional, calibrationTo, horizon, lookback, distance, coverage)
		if !latency.Sufficient {
			latencyInsufficient = true
			report.Results = append(report.Results, volumeProfileVarianceHorizonReport{
				Horizon: horizon.String(), LatencySufficient: false,
				LatencyCalibrationSamples: latency.Eligible,
			})
			continue
		}
		minutes := buildVolumeProfileMinutes(books, trades, cfg, horizon, latency.ProfileRange, coverage)
		report.Results = append(report.Results, scoreVolumeProfileVarianceHorizon(
			minutes, cfg, horizon, distance, coverage, latency, calibrationTo))
	}
	sort.Slice(report.Results, func(i, j int) bool { return report.Results[i].Horizon < report.Results[j].Horizon })
	report.Gate = "PROMOTE_COMPONENT_REPLAY"
	if latencyInsufficient {
		report.Gate = "INCONCLUSIVE_LATENCY"
	} else if len(report.Results) != 2 || report.Results[0].SimultaneousLowerBoundQLIKE <= 0 ||
		report.Results[1].SimultaneousLowerBoundQLIKE < 0 || report.Results[0].PositiveDays < 5 {
		report.Gate = "REJECT_UNSTABLE"
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode volume-profile variance study: %v", err)
	}
}
