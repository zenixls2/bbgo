package gammacapture

import (
	"fmt"
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

type multiscaleRegimeCheckpoint struct {
	LastAt        time.Time                `json:"lastAt"`
	LastBid       float64                  `json:"lastBid"`
	LastAsk       float64                  `json:"lastAsk"`
	Samples       int                      `json:"samples"`
	AskReturns    []float64                `json:"askReturns,omitempty"`
	BidReturns    []float64                `json:"bidReturns,omitempty"`
	ReturnHead    int                      `json:"returnHead"`
	Probabilities []float64                `json:"probabilities,omitempty"`
	Runs          []multiscaleRunPosterior `json:"runs,omitempty"`
	LastDecision  MultiscaleRegimeDecision `json:"lastDecision"`
}

// MultiscaleRegimeConfig controls the optional one-minute regime fallback.
// The filter has no fitted artifact: all posterior state is learned causally
// from observations received after construction.
type MultiscaleRegimeConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// types.Duration accepts both YAML duration strings (e.g. 3h) and the
	// generated JSON representation used by the live config loader. Using
	// time.Duration directly would fail after YAML is converted to JSON.
	HazardMean       types.Duration `json:"hazardMean" yaml:"hazardMean"`
	MaximumRunLength int            `json:"maximumRunLength" yaml:"maximumRunLength"`
	VolatilityWindow int            `json:"volatilityWindow" yaml:"volatilityWindow"`
	MinimumSamples   int            `json:"minimumSamples" yaml:"minimumSamples"`
}

func (c MultiscaleRegimeConfig) withDefaults() MultiscaleRegimeConfig {
	if c.HazardMean <= 0 {
		c.HazardMean = types.Duration(3 * time.Hour)
	}
	if c.MaximumRunLength <= 0 {
		c.MaximumRunLength = int((6 * time.Hour) / time.Minute)
	}
	if c.VolatilityWindow <= 0 {
		c.VolatilityWindow = 30
	}
	if c.MinimumSamples <= 0 {
		c.MinimumSamples = c.VolatilityWindow
	}
	return c
}

// MultiscaleRegimeDecision is a posterior over the sign and age of the
// current executable-price regime. It deliberately does not produce an
// inventory target or an independent admission decision.
type MultiscaleRegimeDecision struct {
	Healthy bool
	Reason  string
	Samples int

	DownProbability         float64
	UpProbability           float64
	ChangeProbability       float64
	ExpectedRunLength       time.Duration
	MeanBpsPerMinute        float64
	MeanSEBpsPerMinute      float64
	ContinuousVolatilityBps float64
	JumpVariationFraction   float64
	MeanVarianceRatio       float64
}

type multiscaleRunPosterior struct {
	n    int
	mean float64
	m2   float64
}

func (s multiscaleRunPosterior) update(x float64) multiscaleRunPosterior {
	next := s
	next.n++
	delta := x - next.mean
	next.mean += delta / float64(next.n)
	next.m2 += delta * (x - next.mean)
	return next
}

// BayesianMultiscaleRegime implements a truncated Adams-MacKay Bayesian
// online changepoint filter. One-minute executable BBO returns are normalized
// by jump-robust bipower variation. A Normal-Inverse-Gamma posterior supplies
// the Student-t predictive density for each possible run length.
type BayesianMultiscaleRegime struct {
	config MultiscaleRegimeConfig

	lastAt  time.Time
	lastBid float64
	lastAsk float64
	samples int

	askReturns []float64
	bidReturns []float64
	returnHead int

	probabilities []float64
	runs          []multiscaleRunPosterior
	lastDecision  MultiscaleRegimeDecision
}

func NewBayesianMultiscaleRegime(c MultiscaleRegimeConfig) *BayesianMultiscaleRegime {
	c = c.withDefaults()
	return &BayesianMultiscaleRegime{
		config:        c,
		probabilities: []float64{1},
		runs:          []multiscaleRunPosterior{{}},
		lastDecision:  MultiscaleRegimeDecision{Reason: "waiting for consecutive one-minute executable BBO closes"},
	}
}

// Reset starts a new causal segment after a missing one-minute close.
func (m *BayesianMultiscaleRegime) Reset() {
	if m == nil {
		return
	}
	c := m.config
	*m = *NewBayesianMultiscaleRegime(c)
}

func (m *BayesianMultiscaleRegime) checkpoint() *multiscaleRegimeCheckpoint {
	if m == nil {
		return nil
	}
	return &multiscaleRegimeCheckpoint{
		LastAt: m.lastAt, LastBid: m.lastBid, LastAsk: m.lastAsk, Samples: m.samples,
		AskReturns: append([]float64(nil), m.askReturns...),
		BidReturns: append([]float64(nil), m.bidReturns...), ReturnHead: m.returnHead,
		Probabilities: append([]float64(nil), m.probabilities...),
		Runs:          append([]multiscaleRunPosterior(nil), m.runs...), LastDecision: m.lastDecision,
	}
}

func (m *BayesianMultiscaleRegime) restore(state *multiscaleRegimeCheckpoint) error {
	if m == nil || state == nil {
		return fmt.Errorf("multiscale regime checkpoint is missing")
	}
	maximumReturns := m.config.VolatilityWindow + 1
	if state.Samples < 0 || len(state.AskReturns) != len(state.BidReturns) ||
		len(state.AskReturns) > maximumReturns || state.ReturnHead < 0 ||
		(state.ReturnHead >= len(state.AskReturns) && len(state.AskReturns) > 0) ||
		len(state.Probabilities) == 0 || len(state.Probabilities) != len(state.Runs) ||
		len(state.Probabilities) > m.config.MaximumRunLength+1 {
		return fmt.Errorf("multiscale regime checkpoint state is invalid")
	}
	var total float64
	for _, probability := range state.Probabilities {
		if probability < 0 || math.IsNaN(probability) || math.IsInf(probability, 0) {
			return fmt.Errorf("multiscale regime checkpoint probability is invalid")
		}
		total += probability
	}
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return fmt.Errorf("multiscale regime checkpoint probability mass is invalid")
	}
	*m = *NewBayesianMultiscaleRegime(m.config)
	m.lastAt, m.lastBid, m.lastAsk, m.samples = state.LastAt, state.LastBid, state.LastAsk, state.Samples
	m.askReturns = append([]float64(nil), state.AskReturns...)
	m.bidReturns = append([]float64(nil), state.BidReturns...)
	m.returnHead = state.ReturnHead
	m.probabilities = make([]float64, len(state.Probabilities))
	for index, probability := range state.Probabilities {
		m.probabilities[index] = probability / total
	}
	m.runs = append([]multiscaleRunPosterior(nil), state.Runs...)
	m.lastDecision = state.LastDecision
	return nil
}

// ObserveMinute consumes the last executable bid/ask in a closed UTC minute.
// Consecutive timestamps must be exactly one minute apart; a gap starts a new
// segment rather than fabricating zero returns.
func (m *BayesianMultiscaleRegime) ObserveMinute(at time.Time, bid, ask float64) MultiscaleRegimeDecision {
	if m == nil {
		return MultiscaleRegimeDecision{Reason: "multiscale regime model unavailable"}
	}
	d := m.lastDecision
	if d.Reason == "" {
		d = MultiscaleRegimeDecision{Reason: "waiting for consecutive one-minute executable BBO closes"}
	}
	if at.IsZero() || bid <= 0 || ask < bid {
		return d
	}
	at = at.UTC().Truncate(time.Minute)
	if m.lastAt.IsZero() {
		m.lastAt, m.lastBid, m.lastAsk = at, bid, ask
		m.lastDecision = d
		return d
	}
	// Live BBO callbacks can arrive many times inside one minute. They are the
	// same closed-minute observation, not a missing-minute gap; return the last
	// posterior without resetting or double-counting it.
	if at.Equal(m.lastAt) {
		return m.lastDecision
	}
	if !at.Equal(m.lastAt.Add(time.Minute)) {
		m.Reset()
		m.lastAt, m.lastBid, m.lastAsk = at, bid, ask
		d = MultiscaleRegimeDecision{Reason: "one-minute BBO gap started a new causal segment"}
		m.lastDecision = d
		return d
	}

	askReturn := math.Log(ask / m.lastAsk)
	bidReturn := math.Log(bid / m.lastBid)
	m.lastAt, m.lastBid, m.lastAsk = at, bid, ask
	m.appendReturns(askReturn, bidReturn)
	m.samples++
	d.Samples = m.samples
	continuousVariance, realizedVariance := m.variation()
	if continuousVariance > 0 {
		d.ContinuousVolatilityBps = math.Sqrt(continuousVariance) * 10_000
	}
	if realizedVariance > 0 {
		d.JumpVariationFraction = clampRatio((realizedVariance-continuousVariance)/realizedVariance, 0, 1)
	}
	if m.samples < m.config.MinimumSamples || continuousVariance <= 0 {
		d.Reason = "insufficient one-minute observations for jump-robust scale"
		m.lastDecision = d
		return d
	}

	// If executable bid and ask returns disagree, spread movement is not
	// directional evidence. If they agree, retain the smaller magnitude so a
	// one-sided quote move cannot manufacture a trend.
	executableReturn := conservativeTrendMean(askReturn, bidReturn)
	standardized := executableReturn / math.Sqrt(continuousVariance)
	m.updateBOCPD(standardized)
	d = m.decision(continuousVariance, realizedVariance)
	d.Samples = m.samples
	m.lastDecision = d
	return d
}

func (m *BayesianMultiscaleRegime) appendReturns(askReturn, bidReturn float64) {
	window := m.config.VolatilityWindow + 1
	if len(m.askReturns) < window {
		m.askReturns = append(m.askReturns, askReturn)
		m.bidReturns = append(m.bidReturns, bidReturn)
		return
	}
	m.askReturns[m.returnHead] = askReturn
	m.bidReturns[m.returnHead] = bidReturn
	m.returnHead = (m.returnHead + 1) % window
}

func (m *BayesianMultiscaleRegime) orderedReturns(values []float64) []float64 {
	if len(values) == 0 || len(values) < m.config.VolatilityWindow+1 || m.returnHead == 0 {
		return values
	}
	out := make([]float64, 0, len(values))
	out = append(out, values[m.returnHead:]...)
	out = append(out, values[:m.returnHead]...)
	return out
}

func (m *BayesianMultiscaleRegime) variation() (continuous, realized float64) {
	ask := m.orderedReturns(m.askReturns)
	bid := m.orderedReturns(m.bidReturns)
	if len(ask) < 2 || len(ask) != len(bid) {
		return 0, 0
	}
	var askBPV, bidBPV float64
	for i := 0; i < len(ask); i++ {
		realized += math.Max(ask[i]*ask[i], bid[i]*bid[i])
		if i > 0 {
			askBPV += math.Abs(ask[i]) * math.Abs(ask[i-1])
			bidBPV += math.Abs(bid[i]) * math.Abs(bid[i-1])
		}
	}
	realized /= float64(len(ask))
	// pi/2 is mu_1^-2 for Gaussian increments. Use the larger executable
	// side as the conservative continuous carrying-risk scale.
	denominator := float64(len(ask) - 1)
	continuous = math.Max(askBPV, bidBPV) * (math.Pi / 2) / denominator
	return continuous, realized
}

func (m *BayesianMultiscaleRegime) updateBOCPD(x float64) {
	oldProbabilities, oldRuns := m.probabilities, m.runs
	maxLength := m.config.MaximumRunLength
	newLength := len(oldProbabilities) + 1
	if newLength > maxLength+1 {
		newLength = maxLength + 1
	}
	newProbabilities := make([]float64, newLength)
	newRuns := make([]multiscaleRunPosterior, newLength)

	hazardMinutes := m.config.HazardMean.Duration().Minutes()
	hazard := 1 / math.Max(1, hazardMinutes)
	priorPredictive := multiscaleStudentLogPDF(multiscaleRunPosterior{}, x)
	changeMass := 0.0
	for _, probability := range oldProbabilities {
		changeMass += probability * hazard
	}
	newProbabilities[0] = changeMass * math.Exp(priorPredictive)
	newRuns[0] = (multiscaleRunPosterior{}).update(x)
	for index, probability := range oldProbabilities {
		growth := index + 1
		if growth >= newLength {
			continue
		}
		newProbabilities[growth] = probability * (1 - hazard) * math.Exp(multiscaleStudentLogPDF(oldRuns[index], x))
		newRuns[growth] = oldRuns[index].update(x)
	}
	var total float64
	for _, probability := range newProbabilities {
		total += probability
	}
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		m.probabilities = []float64{1}
		m.runs = []multiscaleRunPosterior{newRuns[0]}
		return
	}
	for index := range newProbabilities {
		newProbabilities[index] /= total
	}
	m.probabilities, m.runs = newProbabilities, newRuns
}

func multiscalePosterior(run multiscaleRunPosterior) (mean, meanVariance, alpha, beta, kappa float64) {
	const (
		priorMean  = 0.0
		priorKappa = 1.0
		priorAlpha = 2.0
		priorBeta  = 1.0
	)
	n := float64(run.n)
	kappa = priorKappa + n
	mean = (priorKappa*priorMean + n*run.mean) / kappa
	alpha = priorAlpha + n/2
	beta = priorBeta + .5*run.m2
	if run.n > 0 {
		beta += priorKappa * n * math.Pow(run.mean-priorMean, 2) / (2 * kappa)
	}
	meanVariance = beta / (alpha * kappa)
	return
}

func multiscaleStudentLogPDF(run multiscaleRunPosterior, x float64) float64 {
	mean, _, alpha, beta, kappa := multiscalePosterior(run)
	df := 2 * alpha
	scaleSquared := beta * (kappa + 1) / (alpha * kappa)
	if scaleSquared <= 0 {
		return math.Inf(-1)
	}
	left, _ := math.Lgamma((df + 1) / 2)
	right, _ := math.Lgamma(df / 2)
	zSquared := math.Pow(x-mean, 2) / scaleSquared
	return left - right - .5*math.Log(df*math.Pi*scaleSquared) - .5*(df+1)*math.Log1p(zSquared/df)
}

func (m *BayesianMultiscaleRegime) decision(continuousVariance, realizedVariance float64) MultiscaleRegimeDecision {
	d := MultiscaleRegimeDecision{Healthy: true, Reason: "online jump-robust run-length posterior"}
	scale := math.Sqrt(continuousVariance)
	var standardizedMean, mixtureSecond, expectedRun float64
	for index, probability := range m.probabilities {
		mean, meanVariance, _, _, _ := multiscalePosterior(m.runs[index])
		standardError := math.Sqrt(math.Max(0, meanVariance))
		pDown := 0.5
		if standardError > 0 {
			pDown = .5 * math.Erfc(mean/(math.Sqrt2*standardError))
		}
		d.DownProbability += probability * pDown
		standardizedMean += probability * mean
		mixtureSecond += probability * (meanVariance + mean*mean)
		expectedRun += probability * float64(index)
	}
	d.DownProbability = clampRatio(d.DownProbability, 0, 1)
	d.UpProbability = 1 - d.DownProbability
	if len(m.probabilities) > 0 {
		d.ChangeProbability = clampRatio(m.probabilities[0], 0, 1)
	}
	d.ExpectedRunLength = time.Duration(expectedRun * float64(time.Minute))
	d.MeanBpsPerMinute = standardizedMean * scale * 10_000
	mixtureVariance := math.Max(0, mixtureSecond-standardizedMean*standardizedMean)
	d.MeanSEBpsPerMinute = math.Sqrt(mixtureVariance) * scale * 10_000
	d.ContinuousVolatilityBps = scale * 10_000
	if realizedVariance > 0 {
		d.JumpVariationFraction = clampRatio((realizedVariance-continuousVariance)/realizedVariance, 0, 1)
	}
	varianceBps := continuousVariance * 100_000_000
	if varianceBps > 0 {
		d.MeanVarianceRatio = d.MeanBpsPerMinute / varianceBps
	}
	return d
}
