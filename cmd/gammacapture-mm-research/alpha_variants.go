package main

// This file contains three removable, standalone alpha screens.  None of the
// models call an order, balance, or strategy method.  They are deliberately
// kept in the research command until a component-replay gate is passed.

import (
	"encoding/json"
	"math"
	"os"
	"time"
)

const alphaFeatureDim = 12

type scalarEWRegression struct {
	dim      int
	halfLife time.Duration
	lastAt   time.Time
	count    int
	gram     [alphaFeatureDim][alphaFeatureDim]float64
	rhs      [alphaFeatureDim]float64
}

func (r *scalarEWRegression) decay(at time.Time) {
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
		r.rhs[i] *= factor
	}
	r.lastAt = at
}

func (r *scalarEWRegression) update(at time.Time, x [alphaFeatureDim]float64, y float64) {
	r.decay(at)
	for i := 0; i < r.dim; i++ {
		for j := 0; j < r.dim; j++ {
			r.gram[i][j] += x[i] * x[j]
		}
		r.rhs[i] += x[i] * y
	}
	r.count++
}

func (r *scalarEWRegression) predict(x [alphaFeatureDim]float64) (float64, bool) {
	if r.dim <= 0 || r.count < 4*r.dim {
		return 0, false
	}
	var matrix [alphaFeatureDim][alphaFeatureDim]float64
	var vector [alphaFeatureDim]float64
	for i := 0; i < r.dim; i++ {
		for j := 0; j < r.dim; j++ {
			matrix[i][j] = r.gram[i][j]
		}
		matrix[i][i]++ // fixed unit ridge; no tuned coefficient
		vector[i] = r.rhs[i]
	}
	coef, ok := solveAlphaSystem(matrix, vector, r.dim)
	if !ok {
		return 0, false
	}
	value := 0.0
	for i := 0; i < r.dim; i++ {
		value += coef[i] * x[i]
	}
	return value, math.IsNaN(value) == false && math.IsInf(value, 0) == false
}

func solveAlphaSystem(matrix [alphaFeatureDim][alphaFeatureDim]float64, vector [alphaFeatureDim]float64, dim int) ([alphaFeatureDim]float64, bool) {
	for column := 0; column < dim; column++ {
		pivot := column
		for row := column + 1; row < dim; row++ {
			if math.Abs(matrix[row][column]) > math.Abs(matrix[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(matrix[pivot][column]) < 1e-12 {
			return [alphaFeatureDim]float64{}, false
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
	return vector, true
}

func alphaFeatures(baseline, volume [volumeProfileRegressionMaxFeatures]float64) [alphaFeatureDim]float64 {
	var out [alphaFeatureDim]float64
	copy(out[:3], baseline[:3])
	// The nine VP coordinates are frozen before scoring. The final three
	// derived coordinates are reserved for a future, separately screened alpha.
	copy(out[3:], volume[:9])
	return out
}

func alphaVarianceQLIKE(variance, squared float64) float64 {
	variance = math.Max(1e-6, math.Min(1e12, variance))
	return math.Log(variance) + math.Max(0, squared)/variance
}

type alphaVariantHorizonReport struct {
	Variant            string  `json:"variant"`
	PrimaryType        string  `json:"primaryType"`
	Horizon            string  `json:"horizon"`
	EligibleSamples    int     `json:"eligibleSamples"`
	ScoredSamples      int     `json:"scoredSamples"`
	EffectiveSamples   float64 `json:"effectiveSamples"`
	MeanIncrementBps   float64 `json:"meanIncrementBps"`
	StandardErrorBps   float64 `json:"standardErrorBps"`
	Lower95Bps         float64 `json:"lower95Bps"`
	BaselineQLIKE      float64 `json:"baselineQLIKE"`
	CandidateQLIKE     float64 `json:"candidateQLIKE"`
	QLIKEImprovement   float64 `json:"qLikeImprovement"`
	PositiveBlocks     int     `json:"positiveBlocks"`
	TotalBlocks        int     `json:"totalBlocks"`
	ActionChanges      int     `json:"actionChanges"`
	SelectedUtilityBps float64 `json:"selectedUtilityBps"`
	BaselineUtilityBps float64 `json:"baselineUtilityBps"`
	Gate               string  `json:"gate"`
	Reason             string  `json:"reason"`
}

type alphaVariantStudyReport struct {
	Name     string                      `json:"name"`
	Symbol   string                      `json:"symbol"`
	From     time.Time                   `json:"from"`
	To       time.Time                   `json:"to"`
	Causal   bool                        `json:"causal"`
	FeeBps   float64                     `json:"makerFeeBps"`
	Variants []alphaVariantHorizonReport `json:"variants"`
	Gate     string                      `json:"gate"`
	DataNote string                      `json:"dataNote"`
}

// scoreMakerIOCVariant compares a BBO-only action-value surface with the
// same surface augmented by the frozen VP vector. IOC is represented by the
// same terminal executable outcome with the maker distance removed; both
// actions pay the same fee in this spot market. This is a pre-fill screen and
// does not claim private-fill evidence.
func scoreMakerIOCVariant(observations []volumeProfileObservation, horizon time.Duration, distance float64) alphaVariantHorizonReport {
	base := [4]scalarEWRegression{}
	candidate := [4]scalarEWRegression{}
	for i := range base {
		base[i] = scalarEWRegression{dim: 3, halfLife: 6 * time.Hour}
		candidate[i] = scalarEWRegression{dim: alphaFeatureDim, halfLife: 6 * time.Hour}
	}
	type pendingMaker struct {
		o                   volumeProfileObservation
		base, candidate     [4]float64
		baseOK, candidateOK bool
	}
	queue := make([]pendingMaker, 0, len(observations))
	daySum, dayCount := map[string]float64{}, map[string]int{}
	report := alphaVariantHorizonReport{Variant: "maker-ioc-lifecycle", PrimaryType: "maker/IOC execution", Horizon: horizon.String(), EligibleSamples: len(observations)}
	update := func(p pendingMaker) {
		if !p.baseOK || !p.candidateOK {
			return
		}
		actual := [4]float64{p.o.buyMeanReturnBps, p.o.buyMeanReturnBps - distance, p.o.sellMeanReturnBps, p.o.sellMeanReturnBps - distance}
		choose := func(values [4]float64) int {
			best, index := 0.0, -1
			for i, value := range values {
				if value > best {
					best, index = value, i
				}
			}
			return index
		}
		bi, ci := choose(p.base), choose(p.candidate)
		baseValue, candidateValue := 0.0, 0.0
		if bi >= 0 {
			baseValue = actual[bi]
		}
		if ci >= 0 {
			candidateValue = actual[ci]
		}
		increment := candidateValue - baseValue
		report.BaselineUtilityBps += baseValue
		report.SelectedUtilityBps += candidateValue
		if bi != ci {
			report.ActionChanges++
		}
		day := p.o.at.UTC().Format(time.DateOnly)
		daySum[day] += increment
		dayCount[day]++
		report.ScoredSamples++
	}
	for _, o := range observations {
		for len(queue) > 0 && !queue[0].o.maturity.After(o.at) {
			update(queue[0])
			// Matured labels update each action model only after scoring.
			actual := [4]float64{o.buyMeanReturnBps, o.buyMeanReturnBps - distance, o.sellMeanReturnBps, o.sellMeanReturnBps - distance}
			for i := range base {
				base[i].update(o.maturity, o.baseline, actual[i])
			}
			for i := range candidate {
				candidate[i].update(o.maturity, alphaFeatures(o.baseline, o.volume), actual[i])
			}
			queue = queue[1:]
		}
		baseX := [alphaFeatureDim]float64{o.baseline[0], o.baseline[1], o.baseline[2]}
		candidateX := alphaFeatures(o.baseline, o.volume)
		var p pendingMaker
		p.o = o
		for i := range base {
			p.base[i], _ = base[i].predict(baseX)
		}
		for i := range candidate {
			p.candidate[i], _ = candidate[i].predict(candidateX)
		}
		p.baseOK, p.candidateOK = base[0].count >= 12, candidate[0].count >= 12
		queue = append(queue, p)
	}
	for len(queue) > 0 {
		update(queue[0])
		queue = queue[1:]
	}
	report.EffectiveSamples = float64(report.ScoredSamples)
	report.BaselineUtilityBps /= math.Max(1, float64(report.ScoredSamples))
	report.SelectedUtilityBps /= math.Max(1, float64(report.ScoredSamples))
	blockValues := make([]float64, 0, len(daySum))
	for day, sum := range daySum {
		block := sum / float64(dayCount[day])
		blockValues = append(blockValues, block)
		if block > 0 {
			report.PositiveBlocks++
		}
	}
	report.TotalBlocks = len(blockValues)
	if len(blockValues) > 1 {
		mean := 0.0
		for _, v := range blockValues {
			mean += v
		}
		mean /= float64(len(blockValues))
		report.MeanIncrementBps = mean
		variance := 0.0
		for _, v := range blockValues {
			variance += (v - mean) * (v - mean)
		}
		report.StandardErrorBps = math.Sqrt(variance / float64(len(blockValues)-1) / float64(len(blockValues)))
		report.Lower95Bps = mean - 1.959963984540054*report.StandardErrorBps
	}
	report.Gate = "INCONCLUSIVE_SAMPLES"
	if report.ScoredSamples >= 24 && report.TotalBlocks >= 5 {
		if report.Lower95Bps > 0 && report.ActionChanges > 0 {
			report.Gate = "PROMOTE_COMPONENT_REPLAY"
		} else {
			report.Gate = "REJECT_NO_INCREMENTAL_VALUE"
		}
	}
	return report
}

func scoreFastVarianceResidualVariant(observations []volumeProfileObservation, horizon time.Duration) alphaVariantHorizonReport {
	baseMean := smallEWRegression{dim: 3, halfLife: 6 * time.Hour}
	baseVar := [3]scalarEWRegression{}
	residualVar := [3]scalarEWRegression{}
	for i := range baseVar {
		baseVar[i] = scalarEWRegression{dim: 3, halfLife: 6 * time.Hour}
		residualVar[i] = scalarEWRegression{dim: 9, halfLife: 6 * time.Hour}
	}
	type pendingFast struct {
		o               volumeProfileObservation
		mean            [3]float64
		baseLog         [3]float64
		base, candidate [3]float64
		varianceReady   [3]bool
		ready           bool
	}
	queue := make([]pendingFast, 0, len(observations))
	daySum, dayCount := map[string]float64{}, map[string]int{}
	var baseLoss, candidateLoss float64
	report := alphaVariantHorizonReport{Variant: "fast-terminal-variance-residual", PrimaryType: "inventory risk", Horizon: horizon.String(), EligibleSamples: len(observations)}
	for _, o := range observations {
		for len(queue) > 0 && !queue[0].o.maturity.After(o.at) {
			p := queue[0]
			if p.ready {
				sq := terminalResidualSquares(p.mean, p.o)
				increment := 0.0
				validVariance := true
				for i := 0; i < 3; i++ {
					actualLog := math.Log(math.Max(1e-6, sq[i]))
					baseVar[i].update(p.o.maturity, [alphaFeatureDim]float64{p.o.baseline[0], p.o.baseline[1], p.o.baseline[2]}, actualLog)
					if p.varianceReady[i] {
						residualVar[i].update(p.o.maturity, [alphaFeatureDim]float64{p.o.volume[0], p.o.volume[1], p.o.volume[2], p.o.volume[3], p.o.volume[4], p.o.volume[5], p.o.volume[6], p.o.volume[7], p.o.volume[8]}, actualLog-p.baseLog[i])
					}
					if p.base[i] > 0 && p.candidate[i] > 0 {
						baseLoss += alphaVarianceQLIKE(p.base[i], sq[i])
						candidateLoss += alphaVarianceQLIKE(p.candidate[i], sq[i])
						if i == 0 {
							increment = alphaVarianceQLIKE(p.base[i], sq[i]) - alphaVarianceQLIKE(p.candidate[i], sq[i])
						}
					} else {
						validVariance = false
					}
				}
				if validVariance {
					day := p.o.at.UTC().Format(time.DateOnly)
					daySum[day] += increment
					dayCount[day]++
					report.ScoredSamples++
				}
			}
			// The terminal mean is a separate causal baseline and must warm up
			// even while variance predictions are unavailable.
			baseMean.update(p.o.maturity, p.o.baseline, p.o.valueBps, p.o.buyMeanReturnBps, p.o.sellMeanReturnBps)
			queue = queue[1:]
		}
		mean, ok := baseMean.predict(o.baseline)
		var p pendingFast
		p.o = o
		p.ready = ok
		if ok {
			copy(p.mean[:], mean[:])
			for i := 0; i < 3; i++ {
				rawBase, baseOK := baseVar[i].predict([alphaFeatureDim]float64{o.baseline[0], o.baseline[1], o.baseline[2]})
				if !baseOK {
					continue
				}
				p.baseLog[i] = rawBase
				p.base[i] = math.Exp(math.Max(-20, math.Min(20, rawBase)))
				p.varianceReady[i] = true
				raw, _ := residualVar[i].predict([alphaFeatureDim]float64{o.volume[0], o.volume[1], o.volume[2], o.volume[3], o.volume[4], o.volume[5], o.volume[6], o.volume[7], o.volume[8]})
				p.candidate[i] = p.base[i] * math.Exp(math.Max(-5, math.Min(5, raw)))
			}
		}
		queue = append(queue, p)
	}
	for len(queue) > 0 {
		p := queue[0]
		if p.ready {
			sq := terminalResidualSquares(p.mean, p.o)
			for i := 0; i < 3; i++ {
				baseVar[i].update(p.o.maturity, [alphaFeatureDim]float64{p.o.baseline[0], p.o.baseline[1], p.o.baseline[2]}, math.Log(math.Max(1e-6, sq[i])))
				if p.varianceReady[i] {
					residualVar[i].update(p.o.maturity, [alphaFeatureDim]float64{p.o.volume[0], p.o.volume[1], p.o.volume[2], p.o.volume[3], p.o.volume[4], p.o.volume[5], p.o.volume[6], p.o.volume[7], p.o.volume[8]}, math.Log(math.Max(1e-6, sq[i]))-p.baseLog[i])
				}
			}
		}
		baseMean.update(p.o.maturity, p.o.baseline, p.o.valueBps, p.o.buyMeanReturnBps, p.o.sellMeanReturnBps)
		queue = queue[1:]
	}
	report.EffectiveSamples = float64(report.ScoredSamples)
	report.BaselineQLIKE = baseLoss / math.Max(1, float64(report.ScoredSamples))
	report.CandidateQLIKE = candidateLoss / math.Max(1, float64(report.ScoredSamples))
	report.QLIKEImprovement = report.BaselineQLIKE - report.CandidateQLIKE
	blocks := make([]float64, 0, len(daySum))
	for day, sum := range daySum {
		v := sum / float64(dayCount[day])
		blocks = append(blocks, v)
		if v > 0 {
			report.PositiveBlocks++
		}
	}
	report.TotalBlocks = len(blocks)
	if len(blocks) > 1 {
		m := 0.0
		for _, v := range blocks {
			m += v
		}
		m /= float64(len(blocks))
		report.MeanIncrementBps = m
		ss := 0.0
		for _, v := range blocks {
			ss += (v - m) * (v - m)
		}
		report.StandardErrorBps = math.Sqrt(ss / float64(len(blocks)-1) / float64(len(blocks)))
		report.Lower95Bps = m - 1.959963984540054*report.StandardErrorBps
	}
	report.Gate = "INCONCLUSIVE_SAMPLES"
	if report.ScoredSamples >= 24 && report.TotalBlocks >= 5 {
		if report.Lower95Bps > 0 {
			report.Gate = "PROMOTE_COMPONENT_REPLAY"
		} else {
			report.Gate = "REJECT_UNSTABLE"
		}
	}
	return report
}

type alphaHARObservation struct {
	at, maturity     time.Time
	baseline, volume [alphaFeatureDim]float64
	buyVar, sellVar  float64
}

func buildAlphaHARObservations(minutes []volumeProfileMinute, horizon time.Duration) []alphaHARObservation {
	step := int(horizon / time.Minute)
	if step <= 0 {
		return nil
	}
	out := make([]alphaHARObservation, 0, len(minutes)/step)
	for start := 3 * step; start+step < len(minutes); start += step {
		if !minutes[start].profile.Valid {
			continue
		}
		continuous := true
		for i := start - step + 1; i <= start+step; i++ {
			if i >= len(minutes) || minutes[i].at.Sub(minutes[i-1].at) != time.Minute {
				continuous = false
				break
			}
		}
		if !continuous {
			continue
		}
		rv := func(buy bool, start, end int) float64 {
			sum := 0.0
			for i := start + 1; i <= end; i++ {
				prev, curr := minutes[i-1].terminalBid, minutes[i].terminalBid
				if buy {
					prev, curr = minutes[i-1].terminalAsk, minutes[i].terminalAsk
				}
				if prev <= 0 || curr <= 0 {
					return 0
				}
				r := math.Log(curr / prev)
				sum += r * r
			}
			return sum
		}
		baseline := [alphaFeatureDim]float64{1}
		short := rv(true, start-step+1, start)
		medium := rv(true, start-2*step+1, start)
		long := rv(true, start-3*step+1, start)
		baseline[1], baseline[2], baseline[3] = short, medium, long
		prior := minutes[start-step]
		_, vol, _, _ := volumeProfileFeatures(minutes[start], prior)
		copy(baseline[4:], vol[:8])
		out = append(out, alphaHARObservation{at: minutes[start].at, maturity: minutes[start+step-1].at.Add(time.Minute), baseline: baseline, volume: alphaFeatures([volumeProfileRegressionMaxFeatures]float64{1}, vol), buyVar: rv(true, start, start+step), sellVar: rv(false, start, start+step)})
	}
	return out
}

func scoreMacroHARResidualVariant(observations []alphaHARObservation, horizon time.Duration) alphaVariantHorizonReport {
	base := [2]scalarEWRegression{
		{dim: 4, halfLife: 6 * time.Hour},
		{dim: 4, halfLife: 6 * time.Hour},
	}
	buyResidual := scalarEWRegression{dim: 8, halfLife: 6 * time.Hour}
	sellResidual := scalarEWRegression{dim: 8, halfLife: 6 * time.Hour}
	type pendingMacro struct {
		o         alphaHARObservation
		base      [2]float64
		candidate [2]float64
		ready     bool
	}
	queue := make([]pendingMacro, 0, len(observations))
	daySum, dayCount := map[string]float64{}, map[string]int{}
	report := alphaVariantHorizonReport{Variant: "macro-har-variance-residual", PrimaryType: "inventory risk", Horizon: horizon.String(), EligibleSamples: len(observations)}
	var baseLoss, candidateLoss float64
	feature := func(o alphaHARObservation) [alphaFeatureDim]float64 {
		return [alphaFeatureDim]float64{o.baseline[0], o.baseline[1], o.baseline[2], o.baseline[3]}
	}
	for _, o := range observations {
		for len(queue) > 0 && !queue[0].o.maturity.After(o.at) {
			p := queue[0]
			if p.ready {
				actual := [2]float64{p.o.buyVar, p.o.sellVar}
				increment := 0.0
				for side := 0; side < 2; side++ {
					baseLoss += alphaVarianceQLIKE(p.base[side], actual[side])
					candidateLoss += alphaVarianceQLIKE(p.candidate[side], actual[side])
					increment += alphaVarianceQLIKE(p.base[side], actual[side]) - alphaVarianceQLIKE(p.candidate[side], actual[side])
					base[side].update(p.o.maturity, feature(p.o), math.Log(math.Max(1e-6, actual[side])))
					residual := math.Log(math.Max(1e-6, actual[side])) - math.Log(math.Max(1e-6, p.base[side]))
					if side == 0 {
						buyResidual.update(p.o.maturity, p.o.volume, residual)
					} else {
						sellResidual.update(p.o.maturity, p.o.volume, residual)
					}
				}
				day := p.o.at.UTC().Format(time.DateOnly)
				daySum[day] += increment / 2
				dayCount[day]++
				report.ScoredSamples++
			}
			// Always warm the causal HAR baseline. Residual VP training remains
			// conditional on an already available baseline forecast.
			if !p.ready {
				for side, actual := range [2]float64{p.o.buyVar, p.o.sellVar} {
					base[side].update(p.o.maturity, feature(p.o), math.Log(math.Max(1e-6, actual)))
				}
			}
			queue = queue[1:]
		}
		p := pendingMacro{o: o, ready: true}
		for side := 0; side < 2; side++ {
			rawBase, baseOK := base[side].predict(feature(o))
			p.ready = baseOK
			if !p.ready {
				break
			}
			p.base[side] = math.Exp(math.Max(-20, math.Min(20, rawBase)))
			residual := 0.0
			if side == 0 {
				residual, _ = buyResidual.predict(o.volume)
			} else {
				residual, _ = sellResidual.predict(o.volume)
			}
			p.candidate[side] = p.base[side] * math.Exp(math.Max(-5, math.Min(5, residual)))
		}
		queue = append(queue, p)
	}
	for len(queue) > 0 {
		p := queue[0]
		if p.ready {
			for side, actual := range [2]float64{p.o.buyVar, p.o.sellVar} {
				base[side].update(p.o.maturity, feature(p.o), math.Log(math.Max(1e-6, actual)))
			}
		} else {
			for side, actual := range [2]float64{p.o.buyVar, p.o.sellVar} {
				base[side].update(p.o.maturity, feature(p.o), math.Log(math.Max(1e-6, actual)))
			}
		}
		queue = queue[1:]
	}
	report.EffectiveSamples = float64(report.ScoredSamples)
	report.BaselineQLIKE = baseLoss / math.Max(1, float64(report.ScoredSamples))
	report.CandidateQLIKE = candidateLoss / math.Max(1, float64(report.ScoredSamples))
	report.QLIKEImprovement = report.BaselineQLIKE - report.CandidateQLIKE
	blocks := make([]float64, 0, len(daySum))
	for day, sum := range daySum {
		value := sum / float64(dayCount[day])
		blocks = append(blocks, value)
		if value > 0 {
			report.PositiveBlocks++
		}
	}
	report.TotalBlocks = len(blocks)
	if len(blocks) > 1 {
		mean := 0.0
		for _, value := range blocks {
			mean += value
		}
		mean /= float64(len(blocks))
		report.MeanIncrementBps = mean
		variance := 0.0
		for _, value := range blocks {
			variance += (value - mean) * (value - mean)
		}
		report.StandardErrorBps = math.Sqrt(variance / float64(len(blocks)-1) / float64(len(blocks)))
		report.Lower95Bps = mean - 1.959963984540054*report.StandardErrorBps
	}
	report.Gate = "INCONCLUSIVE_SAMPLES"
	if report.ScoredSamples >= 24 && report.TotalBlocks >= 5 {
		if report.Lower95Bps > 0 {
			report.Gate = "PROMOTE_COMPONENT_REPLAY"
		} else {
			report.Gate = "REJECT_UNSTABLE"
		}
	}
	return report
}

func runAlphaVariantStudy(in volumeProfileStudyInput) {
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	books, trades, _ := loadExactReplayDataset(in.DataPath, in.Symbol, in.From, in.To, "alpha-variants-v1", in.ReplayCacheDir)
	if len(books) < 2 || len(trades) < 8 {
		fatalf("insufficient alpha-variant data: books=%d trades=%d", len(books), len(trades))
	}
	report := alphaVariantStudyReport{Name: "gamma-capture-alpha-variants", Symbol: in.Symbol, From: in.From, To: in.To, Causal: true, FeeBps: cfg.MakerFeeBps, DataNote: "ETHJPY same-symbol standalone study; Aug 1-16 was previously inspected and cannot promote a live change."}
	for _, h := range []time.Duration{15 * time.Minute, 30 * time.Minute} {
		provisional := buildVolumeProfileMinutes(books, trades, cfg, h, h, in.FillCoverage)
		latency := estimateFillLatencyCoverage(provisional, in.From.Add(in.To.Sub(in.From)/2).Truncate(time.Minute), h, time.Duration(cfg.HorizonLookback), math.Max(1, cfg.MinimumHalfSpreadBps), in.FillCoverage)
		if !latency.Sufficient {
			report.Variants = append(report.Variants, alphaVariantHorizonReport{Variant: "all", Horizon: h.String(), Gate: "INCONCLUSIVE_LATENCY", Reason: "insufficient causal fill-coverage calibration"})
			continue
		}
		minutes := buildVolumeProfileMinutes(books, trades, cfg, h, latency.ProfileRange, in.FillCoverage)
		obs := selectEventClockObservations(minutes, volumeProfileObservations(minutes, h, math.Max(1, cfg.MinimumHalfSpreadBps), cfg.MakerFeeBps, latency), h, math.Max(1, cfg.MinimumHalfSpreadBps))
		report.Variants = append(report.Variants, scoreMakerIOCVariant(obs, h, math.Max(1, cfg.MinimumHalfSpreadBps)))
		report.Variants = append(report.Variants, scoreFastVarianceResidualVariant(obs, h))
		har := buildAlphaHARObservations(minutes, h)
		report.Variants = append(report.Variants, scoreMacroHARResidualVariant(har, h))
	}
	report.Gate = "RESEARCH_ONLY"
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fatalf("encode alpha variant report: %v", err)
	}
}
