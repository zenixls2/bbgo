package gammacapture

import (
	"math"
	"time"
)

const fastDriftFeatureCount = 4

// FastDriftConfig controls the endogenous Fast reservation-price forecast.
// Training is always online from matured, non-overlapping BBO windows.  The
// switch selects whether a statistically validated forecast replaces Fast's
// heuristic direction/imbalance center shift; ShadowOnly keeps the learned
// diagnostics without changing quotes.
type FastDriftConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	ShadowOnly bool `json:"shadowOnly" yaml:"shadowOnly"`
}

// FastDriftFeatures are deliberately limited to state that startup BBO replay
// can reconstruct exactly.  The intercept is added internally.  Other public
// flow features must not be added until their capture replay shares the same
// causal event ordering in live and research.
type FastDriftFeatures struct {
	Direction     float64
	BookImbalance float64
	// BBOStateTag is a side-reflection-symmetric, scale-normalized summary of
	// the selected Fast window's drawdown/run-up and 30-second reversal state.
	// It is an input to the online regression, never an independent quote skew.
	BBOStateTag float64
}

type fastDriftBBOStateTagCache struct {
	Bucket time.Time
	Tag    float64
	Valid  bool
}

func (f FastDriftFeatures) vector() [fastDriftFeatureCount]float64 {
	finiteClamp := func(value float64) float64 {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0
		}
		return math.Max(-1, math.Min(1, value))
	}
	return [fastDriftFeatureCount]float64{
		1, finiteClamp(f.Direction), finiteClamp(f.BookImbalance), finiteClamp(f.BBOStateTag),
	}
}

// FastDriftBBOStateTag converts the continuous executable-side state into one
// bounded ML feature. A positive value means recent ask rebound/up-run evidence
// dominates bid reversal/down-drawdown evidence; swapping BUY and SELL paths
// negates the tag exactly. QV and spread normalize the magnitude so a noisy,
// wide-spread BBO burst cannot manufacture an oversized feature.
func (m *MarketMakerHorizonModel) FastDriftBBOStateTag(horizon time.Duration) (float64, bool) {
	if m == nil || horizon <= 0 || len(m.points) == 0 {
		return 0, false
	}
	bucket := m.points[len(m.points)-1].At.Truncate(time.Minute)
	if cached, ok := m.fastDriftBBOStateTags[horizon]; ok && cached.Bucket.Equal(bucket) {
		return cached.Tag, cached.Valid
	}
	state := m.conditionalExecutionState(horizon)
	tag, valid := fastDriftBBOStateTagFromState(state, horizon)
	if m.fastDriftBBOStateTags == nil {
		m.fastDriftBBOStateTags = make(map[time.Duration]fastDriftBBOStateTagCache)
	}
	m.fastDriftBBOStateTags[horizon] = fastDriftBBOStateTagCache{
		Bucket: bucket, Tag: tag, Valid: valid,
	}
	return tag, valid
}

func fastDriftBBOStateTagFromState(state conditionalExecutionState, horizon time.Duration) (float64, bool) {
	if !state.Valid || horizon <= 0 {
		return 0, false
	}
	qvScale := 0.5 * (state.BuyQVBps + state.SellQVBps)
	scale := math.Max(1, qvScale+state.SpreadBps)
	shortScale := math.Max(1, qvScale*math.Sqrt(math.Min(1, 30/horizon.Seconds()))+state.SpreadBps)
	pathLocation := math.Tanh((state.SellRunupBps - state.BuyDrawdownBps) / scale)
	shortReversal := math.Tanh((state.BuyRebound30Bps - state.SellReversal30Bps) / shortScale)
	tag := 0.5 * (pathLocation + shortReversal)
	if !finiteFastDriftValue(tag) {
		return 0, false
	}
	return math.Max(-1, math.Min(1, tag)), true
}

type fastDriftAnchor struct {
	At              time.Time
	MaturesAt       time.Time
	StartBid        float64
	StartAsk        float64
	Features        [fastDriftFeatureCount]float64
	PredictedCenter float64
	PredictionReady bool
}

type fastDriftSample struct {
	At              time.Time
	Features        [fastDriftFeatureCount]float64
	AskReturnBps    float64
	BidReturnBps    float64
	CenterReturnBps float64
	PredictedCenter float64
	PredictionReady bool
}

type fastDriftRegression struct {
	Samples []fastDriftSample
	Anchor  *fastDriftAnchor
}

// FastDriftDecision is the causal posterior-predictive forecast for one Fast
// horizon. Ask and bid models are kept separate; Center combines their log
// returns only after both executable-side regressions have been fitted.
type FastDriftDecision struct {
	Enabled               bool
	Fitted                bool
	Healthy               bool
	Reason                string
	Horizon               time.Duration
	Samples               int
	ValidationSamples     int
	PrequentialSkill      float64
	ValidationGainBps2    float64
	ValidationGainSEBps2  float64
	ValidationProbability float64
	Strength              float64
	AskMeanBps            float64
	BidMeanBps            float64
	RawCenterMeanBps      float64
	CenterMeanBps         float64
	AskVarianceBps2       float64
	BidVarianceBps2       float64
	CenterVarianceBps2    float64
	CenterStdErrorBps     float64
	CenterPredictiveBps2  float64
}

func rawFastDirection(snapshot ModelSnapshot) float64 {
	events := snapshot.Up + snapshot.Down
	return float64(snapshot.Up-snapshot.Down) / float64(events+2)
}

// RawFastDirection is the replay-safe crossing feature used by the online
// drift learner. It intentionally excludes evidence coverage.
func RawFastDirection(snapshot ModelSnapshot) float64 {
	return rawFastDirection(snapshot)
}

// ObserveFastDrift matures at most one non-overlapping forecast label and then
// starts the next horizon. A gap invalidates only the pending label; already
// matured rolling samples remain valid. The first BBO at or just after expiry
// is the executable terminal observation, matching next-BBO replay semantics.
func (m *MarketMakerHorizonModel) ObserveFastDrift(
	now time.Time,
	bestBid, bestAsk float64,
	horizon, lookback time.Duration,
	features FastDriftFeatures,
	gapBefore bool,
) {
	if m == nil || now.IsZero() || bestBid <= 0 || bestAsk < bestBid || horizon <= 0 {
		return
	}
	if m.fastDrift == nil {
		m.fastDrift = make(map[time.Duration]*fastDriftRegression)
	}
	model := m.fastDrift[horizon]
	if model == nil {
		model = &fastDriftRegression{}
		m.fastDrift[horizon] = model
	}
	if gapBefore {
		model.Anchor = nil
		model.trim(now, lookback)
		return
	}
	if anchor := model.Anchor; anchor != nil && !now.Before(anchor.MaturesAt) {
		maximumLag := horizon / 10
		if maximumLag > 2*time.Minute {
			maximumLag = 2 * time.Minute
		}
		if maximumLag < time.Second {
			maximumLag = time.Second
		}
		if now.Sub(anchor.MaturesAt) <= maximumLag {
			askReturn := math.Log(bestAsk/anchor.StartAsk) * 10_000
			bidReturn := math.Log(bestBid/anchor.StartBid) * 10_000
			if finiteFastDriftValue(askReturn) && finiteFastDriftValue(bidReturn) {
				model.Samples = append(model.Samples, fastDriftSample{
					At: anchor.MaturesAt, Features: anchor.Features,
					AskReturnBps: askReturn, BidReturnBps: bidReturn,
					CenterReturnBps: 0.5 * (askReturn + bidReturn),
					PredictedCenter: anchor.PredictedCenter,
					PredictionReady: anchor.PredictionReady,
				})
			}
		}
		model.Anchor = nil
	}
	model.trim(now, lookback)
	if model.Anchor == nil {
		decision := model.predict(horizon, features.vector())
		model.Anchor = &fastDriftAnchor{
			At: now, MaturesAt: now.Add(horizon), StartBid: bestBid, StartAsk: bestAsk,
			// Prequential model selection must score the candidate regression,
			// not its evidence-weighted mixture with the zero-drift fallback.
			// Scoring the shrunk output would make a weak forecast look like zero
			// and create a self-validating positive-feedback loop.
			Features: features.vector(), PredictedCenter: decision.RawCenterMeanBps,
			PredictionReady: decision.Fitted,
		}
	}
}

func (m *MarketMakerHorizonModel) FastDriftDecision(
	horizon time.Duration,
	features FastDriftFeatures,
) FastDriftDecision {
	if m == nil || m.fastDrift == nil || m.fastDrift[horizon] == nil {
		return FastDriftDecision{Horizon: horizon, Reason: "online Fast drift has no matured samples"}
	}
	return m.fastDrift[horizon].predict(horizon, features.vector())
}

func (m *fastDriftRegression) trim(now time.Time, lookback time.Duration) {
	if m == nil || lookback <= 0 || len(m.Samples) == 0 {
		return
	}
	cutoff := now.Add(-lookback)
	first := 0
	for first < len(m.Samples) && m.Samples[first].At.Before(cutoff) {
		first++
	}
	if first > 0 {
		m.Samples = append([]fastDriftSample(nil), m.Samples[first:]...)
	}
}

func (m *fastDriftRegression) predict(
	horizon time.Duration,
	x [fastDriftFeatureCount]float64,
) FastDriftDecision {
	d := FastDriftDecision{Horizon: horizon, Reason: "insufficient causal regression degrees of freedom"}
	if m == nil {
		return d
	}
	d.Samples = len(m.Samples)
	// Two observations per fitted coefficient leave residual degrees of
	// freedom without introducing a symbol-specific minimum-sample knob.
	if len(m.Samples) < 2*fastDriftFeatureCount {
		return d
	}
	var xtx [fastDriftFeatureCount][fastDriftFeatureCount]float64
	var xtAsk, xtBid, xtCenter [fastDriftFeatureCount]float64
	var yyAsk, yyBid, yyCenter float64
	for _, sample := range m.Samples {
		for row := 0; row < fastDriftFeatureCount; row++ {
			xtAsk[row] += sample.Features[row] * sample.AskReturnBps
			xtBid[row] += sample.Features[row] * sample.BidReturnBps
			xtCenter[row] += sample.Features[row] * sample.CenterReturnBps
			for column := 0; column < fastDriftFeatureCount; column++ {
				xtx[row][column] += sample.Features[row] * sample.Features[column]
			}
		}
		yyAsk += sample.AskReturnBps * sample.AskReturnBps
		yyBid += sample.BidReturnBps * sample.BidReturnBps
		yyCenter += sample.CenterReturnBps * sample.CenterReturnBps
	}
	inverse, ok := invertFastDriftMatrix(xtx)
	if !ok {
		d.Reason = "Fast drift feature matrix is singular"
		return d
	}
	betaAsk := fastDriftMatrixVector(inverse, xtAsk)
	betaBid := fastDriftMatrixVector(inverse, xtBid)
	betaCenter := fastDriftMatrixVector(inverse, xtCenter)
	d.AskMeanBps = fastDriftDot(x, betaAsk)
	d.BidMeanBps = fastDriftDot(x, betaBid)
	d.CenterMeanBps = fastDriftDot(x, betaCenter)
	degrees := float64(len(m.Samples) - fastDriftFeatureCount)
	askResidual := math.Max(0, yyAsk-fastDriftDot(betaAsk, xtAsk)) / degrees
	bidResidual := math.Max(0, yyBid-fastDriftDot(betaBid, xtBid)) / degrees
	centerResidual := math.Max(0, yyCenter-fastDriftDot(betaCenter, xtCenter)) / degrees
	leverage := math.Max(0, fastDriftQuadratic(x, inverse))
	d.AskVarianceBps2 = askResidual * (1 + leverage)
	d.BidVarianceBps2 = bidResidual * (1 + leverage)
	d.CenterPredictiveBps2 = centerResidual * (1 + leverage)
	// Side QV already prices the irreducible return innovation into the quote
	// spread. CenterVariance is therefore only uncertainty in the conditional
	// mean, not a second copy of the complete posterior-predictive variance.
	d.CenterVarianceBps2 = math.Max(0, centerResidual*leverage)
	d.CenterStdErrorBps = math.Sqrt(d.CenterVarianceBps2)
	d.Fitted = finiteFastDriftValue(d.CenterMeanBps) &&
		d.CenterVarianceBps2 > 0 && finiteFastDriftValue(d.CenterVarianceBps2)
	if !d.Fitted {
		d.Reason = "invalid Fast drift posterior predictive distribution"
		return d
	}
	rawCenterMean := d.CenterMeanBps
	d.RawCenterMeanBps = rawCenterMean
	// The public applied mean is fail-closed until causal validation assigns a
	// non-zero model weight. RawCenterMean remains available for diagnostics and
	// for scoring the next prequential anchor.
	d.CenterMeanBps = 0
	var modelSquaredError, zeroSquaredError, gainSum, gainSquaredSum float64
	for _, sample := range m.Samples {
		if !sample.PredictionReady {
			continue
		}
		d.ValidationSamples++
		residual := sample.CenterReturnBps - sample.PredictedCenter
		modelLoss := residual * residual
		zeroLoss := sample.CenterReturnBps * sample.CenterReturnBps
		gain := zeroLoss - modelLoss
		modelSquaredError += modelLoss
		zeroSquaredError += zeroLoss
		gainSum += gain
		gainSquaredSum += gain * gain
	}
	if d.ValidationSamples < fastDriftFeatureCount || zeroSquaredError <= 0 {
		d.Reason = "insufficient prequential Fast drift validation"
		return d
	}
	d.PrequentialSkill = 1 - modelSquaredError/zeroSquaredError
	n := float64(d.ValidationSamples)
	d.ValidationGainBps2 = gainSum / n
	if d.ValidationSamples > 1 {
		gainVariance := math.Max(0, (gainSquaredSum-gainSum*gainSum/n)/(n-1))
		d.ValidationGainSEBps2 = math.Sqrt(gainVariance / n)
	}
	if d.ValidationGainSEBps2 > 0 {
		d.ValidationProbability = standardNormalCDF(d.ValidationGainBps2 / d.ValidationGainSEBps2)
	} else if d.ValidationGainBps2 > 0 {
		d.ValidationProbability = 1
	}
	d.Strength = math.Max(0, math.Min(1, 2*d.ValidationProbability-1))
	if d.ValidationGainBps2 <= 0 || d.Strength <= 0 {
		d.Reason = "Fast drift does not beat the zero-drift forecast"
		return d
	}
	// Bayesian model averaging shrinks a weakly supported drift forecast toward
	// the zero-drift model. The extra variance is the model-selection risk that
	// remains after shrinkage; it widens quotes instead of pretending the raw
	// point estimate is known with certainty.
	d.CenterMeanBps = rawCenterMean * d.Strength
	d.CenterVarianceBps2 += rawCenterMean * rawCenterMean * (1 - d.Strength*d.Strength)
	d.Enabled = true
	d.Healthy = true
	d.Reason = "evidence-weighted causal side-BBO drift beats the zero-drift forecast"
	return d
}

func finiteFastDriftValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func invertFastDriftMatrix(
	matrix [fastDriftFeatureCount][fastDriftFeatureCount]float64,
) ([fastDriftFeatureCount][fastDriftFeatureCount]float64, bool) {
	var augmented [fastDriftFeatureCount][2 * fastDriftFeatureCount]float64
	trace := 0.0
	for row := 0; row < fastDriftFeatureCount; row++ {
		trace += matrix[row][row]
		for column := 0; column < fastDriftFeatureCount; column++ {
			augmented[row][column] = matrix[row][column]
		}
		augmented[row][fastDriftFeatureCount+row] = 1
	}
	tolerance := math.Max(1, trace) * 1e-12
	for column := 0; column < fastDriftFeatureCount; column++ {
		pivot := column
		for row := column + 1; row < fastDriftFeatureCount; row++ {
			if math.Abs(augmented[row][column]) > math.Abs(augmented[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(augmented[pivot][column]) <= tolerance {
			return [fastDriftFeatureCount][fastDriftFeatureCount]float64{}, false
		}
		augmented[pivot], augmented[column] = augmented[column], augmented[pivot]
		scale := augmented[column][column]
		for index := 0; index < 2*fastDriftFeatureCount; index++ {
			augmented[column][index] /= scale
		}
		for row := 0; row < fastDriftFeatureCount; row++ {
			if row == column {
				continue
			}
			factor := augmented[row][column]
			for index := 0; index < 2*fastDriftFeatureCount; index++ {
				augmented[row][index] -= factor * augmented[column][index]
			}
		}
	}
	var inverse [fastDriftFeatureCount][fastDriftFeatureCount]float64
	for row := 0; row < fastDriftFeatureCount; row++ {
		for column := 0; column < fastDriftFeatureCount; column++ {
			inverse[row][column] = augmented[row][fastDriftFeatureCount+column]
		}
	}
	return inverse, true
}

func fastDriftMatrixVector(
	matrix [fastDriftFeatureCount][fastDriftFeatureCount]float64,
	vector [fastDriftFeatureCount]float64,
) [fastDriftFeatureCount]float64 {
	var result [fastDriftFeatureCount]float64
	for row := 0; row < fastDriftFeatureCount; row++ {
		for column := 0; column < fastDriftFeatureCount; column++ {
			result[row] += matrix[row][column] * vector[column]
		}
	}
	return result
}

func fastDriftDot(left, right [fastDriftFeatureCount]float64) float64 {
	result := 0.0
	for index := 0; index < fastDriftFeatureCount; index++ {
		result += left[index] * right[index]
	}
	return result
}

func fastDriftQuadratic(
	vector [fastDriftFeatureCount]float64,
	matrix [fastDriftFeatureCount][fastDriftFeatureCount]float64,
) float64 {
	return fastDriftDot(vector, fastDriftMatrixVector(matrix, vector))
}
