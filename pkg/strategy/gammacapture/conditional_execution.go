package gammacapture

import (
	"math"
	"time"
)

const conditionalExecutionFeatureCount = 4

// ConditionalExecutionConfig enables the side-symmetric conditional Fast
// payoff model.  It has no fitted coefficients or symbol-specific thresholds:
// the current state is compared with matured same-symbol BBO paths and shrunk
// continuously toward the unconditional path distribution.
type ConditionalExecutionConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// conditionalExecutionState contains only information observable at the start
// of a quote window. BUY uses the executable ask path; SELL uses the executable
// bid path. The SELL fields are the exact price-reflected counterparts of BUY.
type conditionalExecutionState struct {
	Valid bool

	BuyDrawdownBps    float64
	BuyRebound30Bps   float64
	BuyQVBps          float64
	SellRunupBps      float64
	SellReversal30Bps float64
	SellQVBps         float64
	SpreadBps         float64
	// Signed/absolute executable-bid path statistics are reused by the
	// asymmetric oscillation risk controller. They are causal summaries of the
	// same selected horizon; keeping them here avoids a second O(window) scan in
	// the quote loop.
	SellNetReturnBps      float64
	SellTotalVariationBps float64
	VolumeProfile         VolumeProfileState
}

func (s conditionalExecutionState) vector(buy bool, horizon time.Duration) [conditionalExecutionFeatureCount]float64 {
	if !s.Valid || horizon <= 0 {
		return [conditionalExecutionFeatureCount]float64{}
	}
	qv, excursion, reversal := s.SellQVBps, s.SellRunupBps, s.SellReversal30Bps
	if buy {
		qv, excursion, reversal = s.BuyQVBps, s.BuyDrawdownBps, s.BuyRebound30Bps
	}
	horizonSeconds := math.Max(1, horizon.Seconds())
	shortQV := qv * math.Sqrt(math.Min(1, 30/horizonSeconds))
	return [conditionalExecutionFeatureCount]float64{
		math.Tanh(excursion / math.Max(1, qv)),
		math.Tanh(reversal / math.Max(1, shortQV)),
		math.Log1p(math.Max(0, qv)),
		math.Tanh(s.SpreadBps / math.Max(1, qv)),
	}
}

func conditionalExecutionKernel(current, historical conditionalExecutionState, buy bool, horizon time.Duration) float64 {
	if !current.Valid || !historical.Valid {
		return 0
	}
	a, b := current.vector(buy, horizon), historical.vector(buy, horizon)
	distanceSquared := 0.0
	for index := range a {
		difference := a[index] - b[index]
		distanceSquared += difference * difference
	}
	// Volume Profile is a state descriptor, not a second decision gate. It
	// contributes only when both causal states have enough effective public
	// trades; otherwise the legacy conditional kernel is unchanged.
	if current.VolumeProfile.Valid && historical.VolumeProfile.Valid {
		currentProfile, historicalProfile := current.VolumeProfile.Vector(buy), historical.VolumeProfile.Vector(buy)
		weight := current.VolumeProfile.KernelWeight
		if weight <= 0 || (historical.VolumeProfile.KernelWeight > 0 && historical.VolumeProfile.KernelWeight < weight) {
			weight = historical.VolumeProfile.KernelWeight
		}
		if weight <= 0 {
			weight = 1
		}
		for index := range currentProfile {
			difference := currentProfile[index] - historicalProfile[index]
			distanceSquared += weight * difference * difference
		}
	}
	return math.Exp(-0.5 * distanceSquared)
}

type conditionalExecutionReturn struct {
	At          time.Time
	AskSquared  float64
	BidSquared  float64
	AskAbsolute float64
	BidAbsolute float64
}

// conditionalExecutionStateBuilder is the streaming form of
// buildConditionalExecutionStates.  Horizon exposures are completed one
// start point at a time as new BBO observations arrive.  Rebuilding the
// entire lookback window for every newly completed start made the incremental
// replay path O(number of observations * horizon length).  The deques and
// return accumulators below carry the exact same state across observations,
// so initialization is O(N) and steady state is amortized O(1) per point.
type conditionalExecutionStateBuilder struct {
	horizon      time.Duration
	firstAt      time.Time
	count        int
	states       []conditionalExecutionState
	lastPoint    MarketMakerHorizonPoint
	hasLastPoint bool

	maxAskH, minBidH, minAsk30, maxBid30  []int
	returns                               []conditionalExecutionReturn
	returnHead                            int
	buyQV2, sellQV2                       float64
	buyTotalVariation, sellTotalVariation float64
	segmentStart                          int
	leftH, left30                         int
}

func (b *conditionalExecutionStateBuilder) reset(horizon time.Duration, firstAt time.Time) {
	*b = conditionalExecutionStateBuilder{
		horizon: horizon, firstAt: firstAt,
		segmentStart: 0, leftH: 0, left30: 0,
	}
}

func (b *conditionalExecutionStateBuilder) ensure(points []MarketMakerHorizonPoint, horizon time.Duration) {
	if horizon <= 0 || len(points) == 0 {
		b.reset(horizon, time.Time{})
		return
	}
	if b.horizon != horizon || b.count > len(points) ||
		(b.count > 0 && !b.firstAt.Equal(points[0].At)) ||
		(b.count == len(points) && b.hasLastPoint && b.lastPoint != points[len(points)-1]) {
		b.reset(horizon, points[0].At)
	}
	if b.count == 0 {
		b.reset(horizon, points[0].At)
	}
	if cap(b.states) < len(points) {
		capacity := len(points) * 2
		if capacity < len(points) {
			capacity = len(points)
		}
		states := make([]conditionalExecutionState, len(points), capacity)
		copy(states, b.states)
		b.states = states
	} else {
		b.states = b.states[:len(points)]
	}
}

func (b *conditionalExecutionStateBuilder) append(points []MarketMakerHorizonPoint, index int) {
	point := points[index]
	if index == 0 || point.GapBefore {
		b.segmentStart = index
		b.leftH, b.left30 = index, index
		b.maxAskH, b.minBidH, b.minAsk30, b.maxBid30 = nil, nil, nil, nil
		b.returns = b.returns[:0]
		b.returnHead = 0
		b.buyQV2, b.sellQV2 = 0, 0
		b.buyTotalVariation, b.sellTotalVariation = 0, 0
	} else {
		previous := points[index-1]
		if previousAsk, currentAsk := previous.askPrice(), point.askPrice(); previousAsk > 0 && currentAsk > 0 {
			value := math.Log(currentAsk / previousAsk)
			b.buyQV2 += value * value
			b.buyTotalVariation += math.Abs(value)
			previousBid, currentBid := previous.bidPrice(), point.bidPrice()
			bidSquared, bidAbsolute := 0.0, 0.0
			if previousBid > 0 && currentBid > 0 {
				bidReturn := math.Log(currentBid / previousBid)
				bidSquared = bidReturn * bidReturn
				bidAbsolute = math.Abs(bidReturn)
				b.sellQV2 += bidSquared
				b.sellTotalVariation += bidAbsolute
			}
			b.returns = append(b.returns, conditionalExecutionReturn{
				At: point.At, AskSquared: value * value, BidSquared: bidSquared,
				AskAbsolute: math.Abs(value), BidAbsolute: bidAbsolute,
			})
		}
	}

	cutoffH := point.At.Add(-b.horizon)
	for b.leftH < index && points[b.leftH].At.Before(cutoffH) {
		b.leftH++
	}
	cutoff30 := point.At.Add(-30 * time.Second)
	for b.left30 < index && points[b.left30].At.Before(cutoff30) {
		b.left30++
	}
	for b.returnHead < len(b.returns) && b.returns[b.returnHead].At.Before(cutoffH) {
		old := b.returns[b.returnHead]
		b.buyQV2 -= old.AskSquared
		b.sellQV2 -= old.BidSquared
		b.buyTotalVariation -= old.AskAbsolute
		b.sellTotalVariation -= old.BidAbsolute
		b.returnHead++
	}
	if b.returnHead >= 1024 && b.returnHead*2 >= len(b.returns) {
		copy(b.returns, b.returns[b.returnHead:])
		b.returns = b.returns[:len(b.returns)-b.returnHead]
		b.returnHead = 0
	}
	trimFront := func(values []int, minimum int) []int {
		for len(values) > 0 && values[0] < minimum {
			values = values[1:]
		}
		return values
	}
	b.maxAskH, b.minBidH = trimFront(b.maxAskH, b.leftH), trimFront(b.minBidH, b.leftH)
	b.minAsk30, b.maxBid30 = trimFront(b.minAsk30, b.left30), trimFront(b.maxBid30, b.left30)
	ask, bid := point.askPrice(), point.bidPrice()
	for len(b.maxAskH) > 0 && points[b.maxAskH[len(b.maxAskH)-1]].askPrice() <= ask {
		b.maxAskH = b.maxAskH[:len(b.maxAskH)-1]
	}
	b.maxAskH = append(b.maxAskH, index)
	for len(b.minBidH) > 0 && points[b.minBidH[len(b.minBidH)-1]].bidPrice() >= bid {
		b.minBidH = b.minBidH[:len(b.minBidH)-1]
	}
	b.minBidH = append(b.minBidH, index)
	for len(b.minAsk30) > 0 && points[b.minAsk30[len(b.minAsk30)-1]].askPrice() >= ask {
		b.minAsk30 = b.minAsk30[:len(b.minAsk30)-1]
	}
	b.minAsk30 = append(b.minAsk30, index)
	for len(b.maxBid30) > 0 && points[b.maxBid30[len(b.maxBid30)-1]].bidPrice() <= bid {
		b.maxBid30 = b.maxBid30[:len(b.maxBid30)-1]
	}
	b.maxBid30 = append(b.maxBid30, index)

	if point.At.Sub(points[b.segmentStart].At) < b.horizon || ask <= 0 || bid <= 0 ||
		len(b.maxAskH) == 0 || len(b.minBidH) == 0 || len(b.minAsk30) == 0 || len(b.maxBid30) == 0 {
		b.states[index] = conditionalExecutionState{}
		return
	}
	b.states[index] = conditionalExecutionState{
		Valid:                 true,
		BuyDrawdownBps:        math.Max(0, math.Log(points[b.maxAskH[0]].askPrice()/ask)*10_000),
		BuyRebound30Bps:       math.Max(0, math.Log(ask/points[b.minAsk30[0]].askPrice())*10_000),
		BuyQVBps:              math.Sqrt(math.Max(0, b.buyQV2)) * 10_000,
		SellRunupBps:          math.Max(0, math.Log(bid/points[b.minBidH[0]].bidPrice())*10_000),
		SellReversal30Bps:     math.Max(0, math.Log(points[b.maxBid30[0]].bidPrice()/bid)*10_000),
		SellQVBps:             math.Sqrt(math.Max(0, b.sellQV2)) * 10_000,
		SpreadBps:             math.Max(0, math.Log(ask/bid)*10_000),
		SellNetReturnBps:      math.Log(bid/points[b.leftH].bidPrice()) * 10_000,
		SellTotalVariationBps: math.Max(0, b.sellTotalVariation) * 10_000,
		VolumeProfile:         point.volumeProfileState(b.horizon),
	}
}

func (b *conditionalExecutionStateBuilder) build(points []MarketMakerHorizonPoint, horizon time.Duration) []conditionalExecutionState {
	b.reset(horizon, time.Time{})
	b.ensure(points, horizon)
	for index := range points {
		b.append(points, index)
	}
	b.count = len(points)
	return b.states
}

func (b *conditionalExecutionStateBuilder) appendThrough(points []MarketMakerHorizonPoint, horizon time.Duration) []conditionalExecutionState {
	b.ensure(points, horizon)
	for index := b.count; index < len(points); index++ {
		b.append(points, index)
	}
	b.count = len(points)
	if len(points) > 0 {
		b.lastPoint, b.hasLastPoint = points[len(points)-1], true
	}
	return b.states
}

// buildConditionalExecutionStates computes every causal start state in O(N).
// Monotone deques provide rolling extrema and a bounded return queue provides
// side-specific quadratic variation. Future prices never enter these fields.
func buildConditionalExecutionStates(points []MarketMakerHorizonPoint, horizon time.Duration) []conditionalExecutionState {
	var builder conditionalExecutionStateBuilder
	return builder.build(points, horizon)
}

func conditionalExecutionStateAtIndex(points []MarketMakerHorizonPoint, index int, horizon time.Duration) conditionalExecutionState {
	if index < 0 || index >= len(points) {
		return conditionalExecutionState{}
	}
	// Appending a newly completed exposure is infrequent relative to BBO
	// ingestion. Reusing the exact linear builder here keeps the incremental and
	// startup definitions identical; the retained horizon bounds this slice.
	lookback := horizon
	if lookback < 30*time.Second {
		lookback = 30 * time.Second
	}
	cutoff := points[index].At.Add(-lookback)
	start := index
	for start > 0 && !points[start].GapBefore && !points[start-1].At.Before(cutoff) {
		start--
	}
	// Quadratic variation over [t-H,t] includes the return ending exactly at
	// t-H. Preserve its immediately preceding observation so the incremental
	// builder is identical to the startup builder's inclusive cutoff.
	if start > 0 && !points[start].GapBefore {
		start--
	}
	states := buildConditionalExecutionStates(points[start:index+1], horizon)
	if len(states) == 0 {
		return conditionalExecutionState{}
	}
	return states[len(states)-1]
}

func (m *MarketMakerHorizonModel) conditionalExecutionState(horizon time.Duration) conditionalExecutionState {
	if m == nil || len(m.points) == 0 {
		return conditionalExecutionState{}
	}
	if cached, ok := m.conditionalStates[horizon]; ok {
		return cached
	}
	if len(m.points) > 0 {
		if m.crossingExposureCaches == nil {
			m.crossingExposureCaches = make(map[time.Duration]*marketMakerHorizonExposureCache)
		}
		cache := m.crossingExposureCaches[horizon]
		if cache == nil {
			cache = &marketMakerHorizonExposureCache{}
			m.crossingExposureCaches[horizon] = cache
		}
		builder := &cache.ConditionalStateBuilder
		latest := m.points[len(m.points)-1]
		// A same-second BBO replacement is mutable until the next sampled
		// second. Keep the old exact fallback for that rare case; ordinary new
		// seconds use the carried rolling state and do not rescan the horizon.
		if builder.count < len(m.points) ||
			(builder.count == len(m.points) && builder.hasLastPoint && builder.lastPoint == latest) {
			states := cache.conditionalStates(m.points, horizon)
			if len(states) == len(m.points) {
				state := states[len(states)-1]
				if m.conditionalStates == nil {
					m.conditionalStates = make(map[time.Duration]conditionalExecutionState)
				}
				m.conditionalStates[horizon] = state
				return state
			}
		}
	}
	state := conditionalExecutionStateAtIndex(m.points, len(m.points)-1, horizon)
	if m.conditionalStates == nil {
		m.conditionalStates = make(map[time.Duration]conditionalExecutionState)
	}
	m.conditionalStates[horizon] = state
	return state
}

// VolumeProfileState exposes only the latest causal profile snapshot for
// research diagnostics. Quote decisions continue to consume it through the
// conditional kernel, not through this accessor.
func (m *MarketMakerHorizonModel) VolumeProfileState(horizon time.Duration) (VolumeProfileState, bool) {
	if m == nil || horizon <= 0 || len(m.points) == 0 {
		return VolumeProfileState{}, false
	}
	// Volume profile is already captured causally on every horizon point. Do
	// not rebuild the O(horizon) conditional path merely to retrieve this O(1)
	// snapshot; conditionalExecutionState consumes the identical point field.
	state := m.points[len(m.points)-1].volumeProfileState(horizon)
	return state, state.Valid
}

// AsymmetricOscillationRiskFeatures returns the causal executable-bid path
// summary used by the inventory-risk alpha. It shares the conditional
// execution state cache/definition, so the risk controller cannot silently
// use a different window or a midpoint label than the quote optimizer.
func (m *MarketMakerHorizonModel) AsymmetricOscillationRiskFeatures(horizon time.Duration) (AsymmetricOscillationRiskFeatures, bool) {
	if m == nil || horizon <= 0 || len(m.points) == 0 {
		return AsymmetricOscillationRiskFeatures{}, false
	}
	bucket := m.points[len(m.points)-1].At.Truncate(time.Minute)
	if cached, ok := m.asymmetricRiskFeatures[horizon]; ok && cached.Bucket.Equal(bucket) {
		return cached.Features, cached.Valid
	}
	state := m.conditionalExecutionState(horizon)
	if !state.Valid || horizon <= 0 {
		if m.asymmetricRiskFeatures == nil {
			m.asymmetricRiskFeatures = make(map[time.Duration]asymmetricOscillationRiskFeatureCache)
		}
		m.asymmetricRiskFeatures[horizon] = asymmetricOscillationRiskFeatureCache{Bucket: bucket}
		return AsymmetricOscillationRiskFeatures{}, false
	}
	features := AsymmetricOscillationRiskFeatures{
		NetReturnBps:      state.SellNetReturnBps,
		TotalVariationBps: state.SellTotalVariationBps,
		ScaleBps:          state.SellQVBps,
		SpreadBps:         state.SpreadBps,
	}
	if m.asymmetricRiskFeatures == nil {
		m.asymmetricRiskFeatures = make(map[time.Duration]asymmetricOscillationRiskFeatureCache)
	}
	m.asymmetricRiskFeatures[horizon] = asymmetricOscillationRiskFeatureCache{
		Bucket: bucket, Features: features, Valid: true,
	}
	return features, true
}

// ConditionalExecutionSideDecision exposes the nested passage decomposition
// used to audit an inward quote against the ordinary Fast quote.
type ConditionalExecutionSideDecision struct {
	Evaluated                   bool
	EffectiveSamples            float64
	CandidateTouchProbability   float64
	BaseTouchProbability        float64
	IncrementalTouchProbability float64
	IncrementalMarkoutMeanBps   float64
	ConcessionBps               float64
	ExpectedPairedDeltaBps      float64
	PairedStdErrorBps           float64
	PairedPositiveConfidence    float64
}

type weightedScalarMoments struct {
	weight, weightSquared float64
	sum, squared          float64
}

func (m *weightedScalarMoments) add(weight, value float64) {
	if weight <= 0 {
		return
	}
	m.weight += weight
	m.weightSquared += weight * weight
	m.sum += weight * value
	m.squared += weight * value * value
}

func (m weightedScalarMoments) result() (mean, variance, effective float64) {
	if m.weight <= 0 {
		return 0, 0, 0
	}
	mean = m.sum / m.weight
	effective = m.weight
	if m.weightSquared > 0 {
		effective = math.Min(effective, m.weight*m.weight/m.weightSquared)
	}
	variance = math.Max(0, m.squared/m.weight-mean*mean)
	if effective > 1 {
		variance *= effective / (effective - 1)
	}
	return
}

func (m *MarketMakerHorizonModel) conditionalExecutionSideDecision(
	now time.Time,
	config MarketMakerConfig,
	horizon time.Duration,
	current conditionalExecutionState,
	buy bool,
	baseDistanceBps, candidateDistanceBps float64,
) ConditionalExecutionSideDecision {
	d := ConditionalExecutionSideDecision{}
	if m == nil || !current.Valid || now.IsZero() || horizon <= 0 ||
		baseDistanceBps <= 0 || candidateDistanceBps <= 0 ||
		candidateDistanceBps >= baseDistanceBps-1e-12 {
		return d
	}
	exposures := m.crossingExposures(horizon)
	cutoff := now.Add(-time.Duration(config.HorizonLookback))
	globalCount := 0
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		if exposures[index].EndAt.After(now) {
			break
		}
		globalCount++
		if exposures[index].NextMinute <= index {
			break
		}
		index = exposures[index].NextMinute
	}
	if globalCount == 0 {
		return d
	}
	priorPerPath := 1 / math.Sqrt(float64(globalCount))
	entryCostBps := config.MakerFeeBps + config.AdverseSelectionBps
	concession := baseDistanceBps - candidateDistanceBps
	var paired, incrementalMarkout weightedScalarMoments
	weightedCandidate, weightedBase, weightedIncremental, totalWeight := 0.0, 0.0, 0.0, 0.0
	var lastExposure time.Time
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		exposure := exposures[index]
		if exposure.EndAt.After(now) {
			break
		}
		weight := 1.0
		if !lastExposure.IsZero() {
			weight = math.Min(1, exposure.At.Sub(lastExposure).Seconds()/horizon.Seconds())
		}
		kernel := conditionalExecutionKernel(current, exposure.ConditionalState, buy, horizon)
		weight *= (priorPerPath + kernel) / (1 + priorPerPath)
		if weight > 0 {
			excursion := exposure.SellExcursionBps
			if buy {
				excursion = exposure.BuyExcursionBps
			}
			candidateTouched := excursion >= candidateDistanceBps
			baseTouched := excursion >= baseDistanceBps
			value := 0.0
			if baseTouched {
				value = -concession
			} else if candidateTouched {
				if buy {
					quote := exposure.StartAsk * math.Exp(-candidateDistanceBps/10_000)
					value = makerFillTerminalWealthBps(
						true, quote, exposure.TerminalBid, entryCostBps)
				} else {
					quote := exposure.StartBid * math.Exp(candidateDistanceBps/10_000)
					value = makerFillTerminalWealthBps(
						false, quote, exposure.TerminalBid, entryCostBps)
				}
				incrementalMarkout.add(weight, value)
				weightedIncremental += weight
			}
			if candidateTouched {
				weightedCandidate += weight
			}
			if baseTouched {
				weightedBase += weight
			}
			totalWeight += weight
			paired.add(weight, value)
		}
		lastExposure = exposure.At
		if exposure.NextMinute <= index {
			break
		}
		index = exposure.NextMinute
	}
	mean, variance, effective := paired.result()
	if effective <= 1 || totalWeight <= 0 {
		return d
	}
	markoutMean, _, _ := incrementalMarkout.result()
	d.Evaluated = true
	d.EffectiveSamples = effective
	d.CandidateTouchProbability = weightedCandidate / totalWeight
	d.BaseTouchProbability = weightedBase / totalWeight
	d.IncrementalTouchProbability = weightedIncremental / totalWeight
	d.IncrementalMarkoutMeanBps = markoutMean
	d.ConcessionBps = concession
	d.ExpectedPairedDeltaBps = mean
	d.PairedStdErrorBps = math.Sqrt(variance / effective)
	d.PairedPositiveConfidence = jointPathPositiveConfidence(mean, d.PairedStdErrorBps)
	return d
}

// conditionalCrossingDecision uses one common state kernel for the paired
// BUY/SELL indicators, preserving a realizable joint Bernoulli distribution.
// The geometric mean makes either executable side relevant without letting a
// one-sided similarity score independently distort the same joint sample.
func (m *MarketMakerHorizonModel) conditionalCrossingDecision(
	now time.Time,
	config MarketMakerConfig,
	horizon time.Duration,
	buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps float64,
	current conditionalExecutionState,
) MarketMakerHorizonDecision {
	d := m.CrossingDecisionAtSideDistances(
		now, config, horizon, buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps)
	if !current.Valid || horizon <= 0 {
		return d
	}
	exposures := m.crossingExposures(horizon)
	cutoff := now.Add(-time.Duration(config.HorizonLookback))
	globalCount := 0
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		if exposures[index].EndAt.After(now) {
			break
		}
		globalCount++
		if exposures[index].NextMinute <= index {
			break
		}
		index = exposures[index].NextMinute
	}
	if globalCount == 0 {
		return d
	}
	prior := 1 / math.Sqrt(float64(globalCount))
	weightedBuy, weightedSell, weightedBoth, effective := 0.0, 0.0, 0.0, 0.0
	var lastExposure time.Time
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		exposure := exposures[index]
		if exposure.EndAt.After(now) {
			break
		}
		weight := 1.0
		if !lastExposure.IsZero() {
			weight = math.Min(1, exposure.At.Sub(lastExposure).Seconds()/horizon.Seconds())
		}
		buyKernel := conditionalExecutionKernel(current, exposure.ConditionalState, true, horizon)
		sellKernel := conditionalExecutionKernel(current, exposure.ConditionalState, false, horizon)
		weight *= (prior + math.Sqrt(buyKernel*sellKernel)) / (1 + prior)
		buyTouched := exposure.BuyExcursionBps >= buyDistanceBps
		sellTouched := exposure.SellExcursionBps >= sellDistanceBps
		effective += weight
		if buyTouched {
			weightedBuy += weight
		}
		if sellTouched {
			weightedSell += weight
		}
		if buyTouched && sellTouched {
			weightedBoth += weight
		}
		lastExposure = exposure.At
		if exposure.NextMinute <= index {
			break
		}
		index = exposure.NextMinute
	}
	if effective <= 0 {
		return d
	}
	d.EffectiveSamples = effective
	d.BuyTouchProbability, d.BuyTouchStdError = jeffreysBernoulliPosterior(weightedBuy, effective)
	d.SellTouchProbability, d.SellTouchStdError = jeffreysBernoulliPosterior(weightedSell, effective)
	d.BothTouchProbability, d.BothTouchStdError = jeffreysBernoulliPosterior(weightedBoth, effective)
	lowerJoint := math.Max(0, d.BuyTouchProbability+d.SellTouchProbability-1)
	upperJoint := math.Min(d.BuyTouchProbability, d.SellTouchProbability)
	d.BothTouchProbability = math.Max(lowerJoint, math.Min(upperJoint, d.BothTouchProbability))
	d.TouchCovariance = d.BothTouchProbability - d.BuyTouchProbability*d.SellTouchProbability
	edge := math.Max(0, d.NetRoundTripEdgeBps)
	if hours := horizon.Hours(); hours > 0 {
		d.ScoreBpsPerHour = math.Min(d.BuyTouchProbability, d.SellTouchProbability) / hours * edge
		d.ScoreStdErrorBpsHour = math.Max(d.BuyTouchStdError, d.SellTouchStdError) / hours * edge
	}
	d.EstimatorSource = "bbo-side-conditional"
	d.Reason = "conditional same-symbol fee-adjusted two-sided edge per hour"
	return d
}
