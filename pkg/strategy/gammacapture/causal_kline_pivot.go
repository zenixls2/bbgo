package gammacapture

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// CausalKlinePivotKind is the delayed ground-truth class for one completed
// Kline. A LOW means that the next executable leg is expected to be upward;
// HIGH has the opposite interpretation. NEUTRAL is a valid matured label and
// is deliberately not treated as a failed observation.
type CausalKlinePivotKind int

const (
	CausalKlinePivotNeutral CausalKlinePivotKind = iota
	CausalKlinePivotHigh
	CausalKlinePivotLow
)

func (k CausalKlinePivotKind) String() string {
	switch k {
	case CausalKlinePivotHigh:
		return "high"
	case CausalKlinePivotLow:
		return "low"
	default:
		return "neutral"
	}
}

// Direction maps a confirmed pivot into the direction of the next leg. It is
// intentionally a derived convenience; the model's primary output remains
// the three-class probability distribution.
func (k CausalKlinePivotKind) Direction() int {
	switch k {
	case CausalKlinePivotHigh:
		return -1
	case CausalKlinePivotLow:
		return 1
	default:
		return 0
	}
}

// CausalKlineBar is a closed bar. At is the bar close time, not the first
// observation time. GapBefore is set by a local builder when at least one bar
// boundary had no observation; a learner must not bridge that gap.
type CausalKlineBar struct {
	At         time.Time
	ObservedAt time.Time
	Open       float64
	High       float64
	Low        float64
	Close      float64
	GapBefore  bool
}

func (b CausalKlineBar) valid() bool {
	return !b.At.IsZero() && finiteCausalKlineValue(b.Open) &&
		finiteCausalKlineValue(b.High) && finiteCausalKlineValue(b.Low) &&
		finiteCausalKlineValue(b.Close) && b.Open > 0 && b.High >= b.Low &&
		b.Low > 0 && b.Close >= b.Low && b.Close <= b.High
}

// CausalKlineBuilder builds bars from an observable price stream. It emits a
// bar only when the first observation in a later bucket arrives, so it never
// uses an incomplete bar as a closed-bar feature. It does not synthesize bars
// across a gap.
type CausalKlineBuilder struct {
	interval    time.Duration
	bucket      time.Time
	open        float64
	high        float64
	low         float64
	close       float64
	gapBefore   bool
	initialized bool
}

// CausalKlineBuilderSnapshot preserves the open bucket across a live restart.
// Without this state, delta replay would silently discard the partial bar at
// the checkpoint cursor and shift the learner's causal clock by up to one
// interval.
type CausalKlineBuilderSnapshot struct {
	Interval    types.Duration
	Bucket      time.Time
	Open        float64
	High        float64
	Low         float64
	Close       float64
	GapBefore   bool
	Initialized bool
}

func NewCausalKlineBuilder(interval time.Duration) *CausalKlineBuilder {
	if interval <= 0 {
		interval = 3 * time.Minute
	}
	return &CausalKlineBuilder{interval: interval}
}

func (b *CausalKlineBuilder) Snapshot() CausalKlineBuilderSnapshot {
	if b == nil {
		return CausalKlineBuilderSnapshot{}
	}
	return CausalKlineBuilderSnapshot{
		Interval: types.Duration(b.interval), Bucket: b.bucket,
		Open: b.open, High: b.high, Low: b.low, Close: b.close,
		GapBefore: b.gapBefore, Initialized: b.initialized,
	}
}

func (b *CausalKlineBuilder) Restore(snapshot CausalKlineBuilderSnapshot) bool {
	if b == nil || snapshot.Interval <= 0 || snapshot.Bucket.IsZero() {
		return false
	}
	interval := time.Duration(snapshot.Interval)
	if interval <= 0 || snapshot.Initialized &&
		(!finiteCausalKlineValue(snapshot.Open) || !finiteCausalKlineValue(snapshot.High) ||
			!finiteCausalKlineValue(snapshot.Low) || !finiteCausalKlineValue(snapshot.Close) ||
			snapshot.Open <= 0 || snapshot.High < snapshot.Low || snapshot.Low <= 0 ||
			snapshot.Close < snapshot.Low || snapshot.Close > snapshot.High) {
		return false
	}
	b.interval = interval
	b.bucket = snapshot.Bucket
	b.open, b.high, b.low, b.close = snapshot.Open, snapshot.High, snapshot.Low, snapshot.Close
	b.gapBefore = snapshot.GapBefore
	b.initialized = snapshot.Initialized
	return true
}

// Observe adds one causal price observation. The returned bool is true only
// when a previously completed bar was closed by this observation.
func (b *CausalKlineBuilder) Observe(at time.Time, price float64) (CausalKlineBar, bool) {
	if b == nil || at.IsZero() || price <= 0 || !finiteCausalKlineValue(price) {
		return CausalKlineBar{}, false
	}
	bucket := at.UTC().Truncate(b.interval)
	if !b.initialized {
		b.initialized = true
		b.bucket = bucket
		b.open, b.high, b.low, b.close = price, price, price, price
		return CausalKlineBar{}, false
	}
	if bucket.Before(b.bucket) {
		return CausalKlineBar{}, false
	}
	if bucket.Equal(b.bucket) {
		if price > b.high {
			b.high = price
		}
		if price < b.low {
			b.low = price
		}
		b.close = price
		return CausalKlineBar{}, false
	}

	bar := CausalKlineBar{
		At: b.bucket.Add(b.interval), ObservedAt: at, Open: b.open,
		High: b.high, Low: b.low, Close: b.close, GapBefore: b.gapBefore,
	}
	bucketGap := bucket.Sub(b.bucket) > b.interval
	b.bucket = bucket
	b.open, b.high, b.low, b.close = price, price, price, price
	// The gap belongs to the newly started bar. It is carried until that bar
	// itself is emitted; no missing bar is synthesized.
	b.gapBefore = bucketGap
	return bar, true
}

const causalKlinePivotFeatureCount = 8

// CausalKlinePivotConfig controls a deliberately small online classifier. The
// label is the three-bar fractal class, while the prediction is made at the
// close of the middle bar, one bar before that class can be confirmed.
type CausalKlinePivotConfig struct {
	Enabled                bool           `json:"enabled" yaml:"enabled"`
	ShadowOnly             bool           `json:"shadowOnly" yaml:"shadowOnly"`
	Interval               types.Duration `json:"interval" yaml:"interval"`
	MinimumTrainingLabels  int            `json:"minimumTrainingLabels" yaml:"minimumTrainingLabels"`
	LearningRate           float64        `json:"learningRate" yaml:"learningRate"`
	L2                     float64        `json:"l2" yaml:"l2"`
	MinReversalBps         float64        `json:"minReversalBps" yaml:"minReversalBps"`
	MaxTargetShiftRatio    float64        `json:"maxTargetShiftRatio" yaml:"maxTargetShiftRatio"`
	MinimumProbabilityEdge float64        `json:"minimumProbabilityEdge" yaml:"minimumProbabilityEdge"`
	PriorLabels            float64        `json:"priorLabels" yaml:"priorLabels"`
}

func (c CausalKlinePivotConfig) withDefaults() CausalKlinePivotConfig {
	if c.Interval <= 0 {
		c.Interval = types.Duration(3 * time.Minute)
	}
	if c.MinimumTrainingLabels <= 0 {
		c.MinimumTrainingLabels = 32
	}
	if c.LearningRate <= 0 || !finiteCausalKlineValue(c.LearningRate) {
		c.LearningRate = 0.05
	}
	if c.L2 < 0 || !finiteCausalKlineValue(c.L2) {
		c.L2 = 0.001
	}
	if c.MinReversalBps < 0 || !finiteCausalKlineValue(c.MinReversalBps) {
		c.MinReversalBps = 0
	}
	if c.MaxTargetShiftRatio <= 0 || !finiteCausalKlineValue(c.MaxTargetShiftRatio) {
		// A learner that has just been restored must not be able to move the
		// whole inventory band. This is intentionally tighter than the old
		// research actuator's 20% shift.
		c.MaxTargetShiftRatio = 0.05
	}
	if c.MaxTargetShiftRatio > 1 {
		c.MaxTargetShiftRatio = 1
	}
	if c.MinimumProbabilityEdge < 0 || !finiteCausalKlineValue(c.MinimumProbabilityEdge) {
		c.MinimumProbabilityEdge = 0.10
	}
	if c.MinimumProbabilityEdge > 1 {
		c.MinimumProbabilityEdge = 1
	}
	if c.PriorLabels <= 0 || !finiteCausalKlineValue(c.PriorLabels) {
		c.PriorLabels = 32
	}
	return c
}

// CausalKlinePivotEvent is emitted only after the following bar closes. At is
// the historical extremum bar; ConfirmedAt is the first time the event is
// knowable without future data.
type CausalKlinePivotEvent struct {
	At          time.Time
	ConfirmedAt time.Time
	Kind        CausalKlinePivotKind
	Price       float64
}

// CausalKlinePivotDecision separates a current prediction from the label that
// matured while processing the current bar. A consumer may use probabilities
// only when PredictionReady and ModelReady are both true; it must never use
// ConfirmedPivot as a same-bar feature.
type CausalKlinePivotDecision struct {
	At                 time.Time
	PredictionReady    bool
	ModelReady         bool
	ProbabilityHigh    float64
	ProbabilityLow     float64
	ProbabilityNeutral float64
	PredictedKind      CausalKlinePivotKind
	PredictedDirection int

	LabelMatured     bool
	LabelSkipped     bool
	MaturedLabelKind CausalKlinePivotKind
	ConfirmedPivot   CausalKlinePivotEvent
	PivotConfirmed   bool

	SegmentReset      bool
	MaturedLabels     int
	SkippedLabels     int
	PendingPrediction bool
	Reason            string
}

type causalKlinePivotPending struct {
	BarAt     time.Time
	PredictAt time.Time
	Features  [causalKlinePivotFeatureCount]float64
}

// CausalKlinePivotSnapshot is sufficient to continue the learner without
// changing the causal order. PendingFeatures is intentionally persisted: a
// restart between prediction and label maturity must not lose that sample or
// reconstruct it from the future bar.
type CausalKlinePivotSnapshot struct {
	Config           CausalKlinePivotConfig
	Bars             []CausalKlineBar
	PendingAt        time.Time
	PendingPredictAt time.Time
	PendingFeatures  [causalKlinePivotFeatureCount]float64
	HasPending       bool
	Weights          [3][causalKlinePivotFeatureCount]float64
	MaturedLabels    int
	SkippedLabels    int
	LastDecision     CausalKlinePivotDecision
}

// CausalKlinePivotLearner predicts the next confirmed three-bar pivot while
// learning only from labels that mature one closed bar later. It is a model
// component, not a quote gate or order actuator.
type CausalKlinePivotLearner struct {
	config        CausalKlinePivotConfig
	bars          []CausalKlineBar
	pending       *causalKlinePivotPending
	weights       [3][causalKlinePivotFeatureCount]float64
	maturedLabels int
	skippedLabels int
	lastDecision  CausalKlinePivotDecision
}

func NewCausalKlinePivotLearner(config CausalKlinePivotConfig) *CausalKlinePivotLearner {
	return &CausalKlinePivotLearner{config: config.withDefaults()}
}

func (m *CausalKlinePivotLearner) Reset() {
	if m == nil {
		return
	}
	config := m.config
	*m = *NewCausalKlinePivotLearner(config)
}

// ObserveBar first matures the label for the preceding bar, then predicts for
// the newly closed bar. This ordering is the central causal guarantee: the
// just-confirmed label can affect subsequent forecasts, never its own forecast.
func (m *CausalKlinePivotLearner) ObserveBar(bar CausalKlineBar) CausalKlinePivotDecision {
	if m == nil {
		return CausalKlinePivotDecision{At: bar.At, Reason: "nil causal Kline pivot learner"}
	}
	predictAt := bar.ObservedAt
	if predictAt.IsZero() {
		predictAt = bar.At
	}
	d := CausalKlinePivotDecision{At: predictAt, Reason: "waiting for two closed Kline bars"}
	if !bar.valid() {
		d.Reason = "invalid closed Kline bar"
		return d
	}
	if !m.lastBarAt().IsZero() && !bar.At.After(m.lastBarAt()) {
		d.Reason = "non-monotonic closed Kline bar"
		return d
	}
	segmentReset := bar.GapBefore
	if previous := m.lastBarAt(); !previous.IsZero() && bar.At.Sub(previous) >= 2*time.Duration(m.config.Interval) {
		segmentReset = true
	}
	if segmentReset {
		m.bars = nil
		m.pending = nil
		d.SegmentReset = true
		d.Reason = "Kline gap reset causal pivot segment"
	}

	if len(m.bars) >= 2 && !segmentReset {
		previousPrevious := m.bars[len(m.bars)-2]
		previous := m.bars[len(m.bars)-1]
		kind, ambiguous, price := causalKlinePivotLabel(previousPrevious, previous, bar, m.config.MinReversalBps)
		d.LabelMatured = true
		d.MaturedLabelKind = kind
		if ambiguous {
			d.LabelSkipped = true
			m.skippedLabels++
		} else {
			m.maturedLabels++
			if m.pending != nil && m.pending.BarAt.Equal(previous.At) {
				m.update(m.pending.Features, kind)
			}
			if kind != CausalKlinePivotNeutral {
				d.PivotConfirmed = true
				d.ConfirmedPivot = CausalKlinePivotEvent{
					At: previous.At, ConfirmedAt: predictAt, Kind: kind, Price: price,
				}
			}
		}
		m.pending = nil
	}

	m.bars = append(m.bars, bar)
	if len(m.bars) > 8 {
		m.bars = append([]CausalKlineBar(nil), m.bars[len(m.bars)-8:]...)
	}
	features, ready := causalKlinePivotFeatures(m.bars)
	if ready {
		probabilities := m.predict(features)
		d.PredictionReady = true
		d.ModelReady = m.maturedLabels >= m.config.MinimumTrainingLabels
		d.ProbabilityHigh = probabilities[causalKlinePivotHighIndex]
		d.ProbabilityLow = probabilities[causalKlinePivotLowIndex]
		d.ProbabilityNeutral = probabilities[causalKlinePivotNeutralIndex]
		d.PredictedKind = causalKlinePivotKindFromProbabilities(probabilities)
		d.PredictedDirection = d.PredictedKind.Direction()
		m.pending = &causalKlinePivotPending{BarAt: bar.At, PredictAt: predictAt, Features: features}
		if d.ModelReady {
			d.Reason = "causal Kline pivot forecast ready"
		} else {
			d.Reason = "causal Kline features ready; waiting for matured labels"
		}
	}
	d.MaturedLabels = m.maturedLabels
	d.SkippedLabels = m.skippedLabels
	d.PendingPrediction = m.pending != nil
	m.lastDecision = d
	return d
}

func (m *CausalKlinePivotLearner) lastBarAt() time.Time {
	if m == nil || len(m.bars) == 0 {
		return time.Time{}
	}
	return m.bars[len(m.bars)-1].At
}

func (m *CausalKlinePivotLearner) predict(features [causalKlinePivotFeatureCount]float64) [3]float64 {
	var logits [3]float64
	maxLogit := math.Inf(-1)
	for class := 0; class < len(logits); class++ {
		for i, feature := range features {
			logits[class] += m.weights[class][i] * feature
		}
		if logits[class] > maxLogit {
			maxLogit = logits[class]
		}
	}
	var probabilities [3]float64
	total := 0.0
	for class, logit := range logits {
		probabilities[class] = math.Exp(math.Max(-50, math.Min(50, logit-maxLogit)))
		total += probabilities[class]
	}
	if total <= 0 || !finiteCausalKlineValue(total) {
		return [3]float64{1.0 / 3, 1.0 / 3, 1.0 / 3}
	}
	for class := range probabilities {
		probabilities[class] /= total
	}
	return probabilities
}

func (m *CausalKlinePivotLearner) update(features [causalKlinePivotFeatureCount]float64, kind CausalKlinePivotKind) {
	if kind < CausalKlinePivotNeutral || kind > CausalKlinePivotLow {
		return
	}
	probabilities := m.predict(features)
	target := [3]float64{}
	target[causalKlinePivotClassIndex(kind)] = 1
	for class := 0; class < len(m.weights); class++ {
		gradient := target[class] - probabilities[class]
		for i, feature := range features {
			weight := m.weights[class][i]
			weight += m.config.LearningRate * (gradient*feature - m.config.L2*weight)
			m.weights[class][i] = math.Max(-12, math.Min(12, weight))
		}
	}
}

func (m *CausalKlinePivotLearner) Snapshot() CausalKlinePivotSnapshot {
	if m == nil {
		return CausalKlinePivotSnapshot{}
	}
	snapshot := CausalKlinePivotSnapshot{
		Config: m.config, Weights: m.weights,
		MaturedLabels: m.maturedLabels, SkippedLabels: m.skippedLabels,
		LastDecision: m.lastDecision,
		Bars:         append([]CausalKlineBar(nil), m.bars...),
	}
	if m.pending != nil {
		snapshot.HasPending = true
		snapshot.PendingAt = m.pending.BarAt
		snapshot.PendingPredictAt = m.pending.PredictAt
		snapshot.PendingFeatures = m.pending.Features
	}
	return snapshot
}

func (m *CausalKlinePivotLearner) Restore(snapshot CausalKlinePivotSnapshot) bool {
	if m == nil || len(snapshot.Bars) > 8 || snapshot.MaturedLabels < 0 || snapshot.SkippedLabels < 0 {
		return false
	}
	config := snapshot.Config.withDefaults()
	var previousAt time.Time
	for _, bar := range snapshot.Bars {
		if !bar.valid() || (!previousAt.IsZero() && !bar.At.After(previousAt)) {
			return false
		}
		previousAt = bar.At
	}
	for class := range snapshot.Weights {
		for _, weight := range snapshot.Weights[class] {
			if !finiteCausalKlineValue(weight) {
				return false
			}
		}
	}
	for _, feature := range snapshot.PendingFeatures {
		if !finiteCausalKlineValue(feature) {
			return false
		}
	}
	if snapshot.HasPending && (snapshot.PendingAt.IsZero() || snapshot.PendingPredictAt.IsZero() || len(snapshot.Bars) == 0 ||
		!snapshot.PendingAt.Equal(snapshot.Bars[len(snapshot.Bars)-1].At)) {
		return false
	}
	m.config = config
	m.bars = append([]CausalKlineBar(nil), snapshot.Bars...)
	m.weights = snapshot.Weights
	m.maturedLabels = snapshot.MaturedLabels
	m.skippedLabels = snapshot.SkippedLabels
	m.lastDecision = snapshot.LastDecision
	m.pending = nil
	if snapshot.HasPending {
		m.pending = &causalKlinePivotPending{BarAt: snapshot.PendingAt, PredictAt: snapshot.PendingPredictAt, Features: snapshot.PendingFeatures}
	}
	return true
}

// CausalKlinePivotTargetDecision is the only live policy output of the
// learner. It is deliberately a bounded target adjustment, never a price,
// quantity, quote, cancellation, or order-admission decision.
type CausalKlinePivotTargetDecision struct {
	Enabled            bool
	ShadowOnly         bool
	PredictionReady    bool
	ModelReady         bool
	Applied            bool
	Ready              bool
	BaseTargetRatio    float64
	TargetRatio        float64
	ShiftRatio         float64
	Signal             float64
	Shrinkage          float64
	ProbabilityHigh    float64
	ProbabilityLow     float64
	ProbabilityNeutral float64
	MaturedLabels      int
	Reason             string
}

// EvaluateCausalKlinePivotTarget converts the prequential probability
// difference P(next leg up)-P(next leg down) into a small target shift. The
// matured-label shrinkage makes the transition from cold to trained state
// continuous; the probability edge avoids reacting to near ties. The base
// target is returned unchanged for every unavailable or invalid state.
func EvaluateCausalKlinePivotTarget(
	config CausalKlinePivotConfig,
	decision CausalKlinePivotDecision,
	baseTarget, hardMinimum, hardMaximum float64,
) CausalKlinePivotTargetDecision {
	config = config.withDefaults()
	d := CausalKlinePivotTargetDecision{
		Enabled: config.Enabled, ShadowOnly: config.ShadowOnly,
		PredictionReady: decision.PredictionReady, ModelReady: decision.ModelReady,
		BaseTargetRatio: baseTarget, TargetRatio: baseTarget,
		ProbabilityHigh:    decision.ProbabilityHigh,
		ProbabilityLow:     decision.ProbabilityLow,
		ProbabilityNeutral: decision.ProbabilityNeutral,
		MaturedLabels:      decision.MaturedLabels,
		Reason:             "causal Kline pivot target disabled",
	}
	if !config.Enabled {
		return d
	}
	d.Reason = "causal Kline pivot target is warming"
	if !finiteCausalKlineValue(baseTarget) || !finiteCausalKlineValue(hardMinimum) ||
		!finiteCausalKlineValue(hardMaximum) || hardMinimum > hardMaximum {
		d.Reason = "invalid causal Kline target bounds"
		return d
	}
	minimum := causalKlineClamp(hardMinimum, 0, 1)
	maximum := causalKlineClamp(hardMaximum, minimum, 1)
	d.BaseTargetRatio = causalKlineClamp(baseTarget, minimum, maximum)
	d.TargetRatio = d.BaseTargetRatio
	if !decision.PredictionReady || !decision.ModelReady {
		return d
	}
	probabilities := [3]float64{decision.ProbabilityHigh, decision.ProbabilityLow, decision.ProbabilityNeutral}
	probabilitySum := 0.0
	for _, probability := range probabilities {
		if !finiteCausalKlineValue(probability) || probability < 0 {
			d.Reason = "invalid causal Kline probability"
			return d
		}
		probabilitySum += probability
	}
	if probabilitySum <= 0 {
		d.Reason = "empty causal Kline probability"
		return d
	}
	for i := range probabilities {
		probabilities[i] /= probabilitySum
	}
	d.ProbabilityHigh, d.ProbabilityLow, d.ProbabilityNeutral = probabilities[0], probabilities[1], probabilities[2]
	d.Signal = probabilities[1] - probabilities[0]
	if math.Abs(d.Signal) < config.MinimumProbabilityEdge {
		d.Reason = "causal Kline pivot probability edge is below target threshold"
		return d
	}
	directionalProbability := math.Max(probabilities[1], probabilities[0])
	if directionalProbability-probabilities[2] < config.MinimumProbabilityEdge {
		// A directional difference is not enough when neutral remains the
		// modal class. Treating that case as a buy/sell target would convert
		// uncertainty into inventory risk.
		d.Reason = "causal Kline pivot directional probability does not clear neutral"
		return d
	}
	labels := float64(decision.MaturedLabels)
	if labels <= 0 {
		d.Reason = "causal Kline pivot has no matured labels"
		return d
	}
	d.Shrinkage = causalKlineClamp(labels/(labels+config.PriorLabels), 0, 1)
	d.ShiftRatio = causalKlineClamp(
		config.MaxTargetShiftRatio*d.Signal*d.Shrinkage,
		-config.MaxTargetShiftRatio, config.MaxTargetShiftRatio)
	d.TargetRatio = causalKlineClamp(d.BaseTargetRatio+d.ShiftRatio, minimum, maximum)
	d.Ready = true
	d.Applied = !config.ShadowOnly && math.Abs(d.TargetRatio-d.BaseTargetRatio) > 1e-12
	if d.Applied {
		d.Reason = "causal Kline pivot target applied as bounded overlay"
	} else if config.ShadowOnly {
		d.Reason = "causal Kline pivot target is shadow-only"
	} else {
		d.Reason = "causal Kline pivot target is bounded at inventory limit"
	}
	return d
}

func causalKlinePivotLabel(previousPrevious, previous, next CausalKlineBar, minReversalBps float64) (CausalKlinePivotKind, bool, float64) {
	high := previous.High > previousPrevious.High && previous.High >= next.High
	low := previous.Low < previousPrevious.Low && previous.Low <= next.Low
	if high && low {
		return CausalKlinePivotNeutral, true, 0
	}
	if high && (minReversalBps <= 0 || math.Log(previous.High/next.Close)*10_000 >= minReversalBps) {
		return CausalKlinePivotHigh, false, previous.High
	}
	if low && (minReversalBps <= 0 || math.Log(next.Close/previous.Low)*10_000 >= minReversalBps) {
		return CausalKlinePivotLow, false, previous.Low
	}
	return CausalKlinePivotNeutral, false, 0
}

func causalKlinePivotFeatures(bars []CausalKlineBar) ([causalKlinePivotFeatureCount]float64, bool) {
	var features [causalKlinePivotFeatureCount]float64
	if len(bars) < 2 {
		return features, false
	}
	current := bars[len(bars)-1]
	previous := bars[len(bars)-2]
	scale := 0.0
	count := 0
	start := len(bars) - 5
	if start < 0 {
		start = 0
	}
	for _, bar := range bars[start:] {
		scale += math.Log(bar.High/bar.Low) * 10_000
		count++
	}
	scale = math.Max(1, scale/float64(count))
	features[0] = 1
	features[1] = causalKlineNormalize(math.Log(current.Close/previous.Close)*10_000, scale)
	lookback := len(bars) - 1 - 3
	if lookback < 0 {
		lookback = 0
	}
	features[2] = causalKlineNormalize(math.Log(current.Close/bars[lookback].Close)*10_000, scale*math.Sqrt(float64(len(bars)-lookback)))
	rangeBps := math.Log(current.High/current.Low) * 10_000
	features[3] = causalKlineNormalize(rangeBps, scale)
	barRange := math.Max(1e-9, current.High-current.Low)
	body := current.Close - current.Open
	features[4] = causalKlineClamp(body/barRange*2, -1, 1)
	features[5] = causalKlineClamp((current.High-math.Max(current.Open, current.Close))/barRange*2, 0, 1)
	features[6] = causalKlineClamp((math.Min(current.Open, current.Close)-current.Low)/barRange*2, 0, 1)
	features[7] = causalKlineClamp((current.Close-current.Low)/barRange*2-1, -1, 1)
	return features, true
}

func causalKlineNormalize(value, scale float64) float64 {
	if scale <= 0 || !finiteCausalKlineValue(value) || !finiteCausalKlineValue(scale) {
		return 0
	}
	return causalKlineClamp(value/scale, -1, 1)
}

func causalKlineClamp(value, lower, upper float64) float64 {
	if !finiteCausalKlineValue(value) {
		return 0
	}
	return math.Max(lower, math.Min(upper, value))
}

const (
	causalKlinePivotHighIndex    = 0
	causalKlinePivotLowIndex     = 1
	causalKlinePivotNeutralIndex = 2
)

func causalKlinePivotClassIndex(kind CausalKlinePivotKind) int {
	switch kind {
	case CausalKlinePivotHigh:
		return causalKlinePivotHighIndex
	case CausalKlinePivotLow:
		return causalKlinePivotLowIndex
	default:
		return causalKlinePivotNeutralIndex
	}
}

func causalKlinePivotKindFromProbabilities(probabilities [3]float64) CausalKlinePivotKind {
	best := causalKlinePivotNeutralIndex
	for i := 0; i < len(probabilities); i++ {
		if probabilities[i] > probabilities[best] {
			best = i
		}
	}
	switch best {
	case causalKlinePivotHighIndex:
		return CausalKlinePivotHigh
	case causalKlinePivotLowIndex:
		return CausalKlinePivotLow
	default:
		return CausalKlinePivotNeutral
	}
}

func finiteCausalKlineValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
