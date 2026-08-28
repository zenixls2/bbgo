package gammacapture

import (
	"math"
	"time"
)

// adaptivePathDecayState is a causal, bounded persistence estimator for
// completed horizon paths.  It uses the lag-one correlation of the absolute
// executable-BBO terminal return (volatility persistence), not a signed
// midpoint return and not a future label.  The correlation is converted to a
// continuous-time half-life with
//
//	h = -log(2) * delta / log(rho).
//
// The horizon and lookback only define admissible bounds and a cold-start
// prior.  They are not a fitted decay coefficient.  The same half-life is
// also used to update the online effective-sample baseline, so the EWMA alpha
// is time-scaled for irregular observations rather than a fixed 0.1-style
// parameter.
type adaptivePathDecayState struct {
	LastMaturedAt   time.Time
	LastPairDecayAt time.Time
	LastValue       float64
	HaveValue       bool

	PairCount float64
	MeanPrev  float64
	MeanCurr  float64
	M2Prev    float64
	M2Curr    float64
	CovLag1   float64

	DeltaCount float64
	MeanDelta  float64

	NeffMean  float64
	NeffM2    float64
	NeffCount float64
	LastNeff  time.Time
}

type adaptivePathDecaySnapshot struct {
	HalfLifeSeconds          float64
	Autocorrelation          float64
	PersistenceObservations  float64
	EffectiveSamplesBaseline float64
	EffectiveSamplesStd      float64
}

func finiteAdaptiveDecay(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func (s *adaptivePathDecayState) observePath(at time.Time, value float64, horizon, lookback time.Duration) {
	if s == nil || at.IsZero() || !finiteAdaptiveDecay(value) {
		return
	}
	if !s.LastMaturedAt.IsZero() && at.Before(s.LastMaturedAt) {
		return
	}
	if !s.LastPairDecayAt.IsZero() && at.After(s.LastPairDecayAt) {
		s.decayPersistence(s.persistenceDecayFactor(at.Sub(s.LastPairDecayAt), horizon, lookback))
	}
	s.LastPairDecayAt = at
	if s.HaveValue {
		delta := at.Sub(s.LastMaturedAt).Seconds()
		if delta > 0 && finiteAdaptiveDecay(delta) {
			oldWeight := s.DeltaCount
			newWeight := oldWeight + 1
			if newWeight > 0 {
				s.MeanDelta += (delta - s.MeanDelta) / newWeight
			}
			s.DeltaCount = newWeight
		}
		// Exponentially weighted Welford updates for the lagged pair
		// (previous, current). Using absolute returns makes this a volatility-
		// persistence estimate and prevents a sign flip from being mistaken for
		// a new regime by itself. The weight is decayed before this pair is added.
		n := s.PairCount + 1
		prev := s.LastValue
		dx := prev - s.MeanPrev
		s.MeanPrev += dx / n
		dy := value - s.MeanCurr
		s.MeanCurr += dy / n
		s.M2Prev += dx * (prev - s.MeanPrev)
		s.M2Curr += dy * (value - s.MeanCurr)
		s.CovLag1 += dx * (value - s.MeanCurr)
		s.PairCount = n
	}
	s.LastValue = value
	s.HaveValue = true
	s.LastMaturedAt = at
}

func (s *adaptivePathDecayState) decayPersistence(factor float64) {
	if s == nil || factor >= 1 {
		return
	}
	if factor <= 0 || !finiteAdaptiveDecay(factor) {
		s.PairCount = 0
		s.M2Prev, s.M2Curr, s.CovLag1 = 0, 0, 0
		s.DeltaCount, s.MeanDelta = 0, 0
		return
	}
	s.PairCount *= factor
	s.M2Prev *= factor
	s.M2Curr *= factor
	s.CovLag1 *= factor
	s.DeltaCount *= factor
}

func (s adaptivePathDecayState) persistenceDecayFactor(delta, horizon, lookback time.Duration) float64 {
	if delta <= 0 {
		return 1
	}
	if horizon <= 0 {
		horizon = time.Minute
	}
	if lookback < horizon {
		lookback = horizon
	}
	// Use the old causal scale only as the forgetting clock for estimating
	// persistence itself. It is not the fitted path half-life and is bounded
	// by the same horizon/lookback interval.
	halfLife := math.Sqrt(horizon.Seconds() * lookback.Seconds())
	if halfLife < horizon.Seconds() || !finiteAdaptiveDecay(halfLife) {
		halfLife = horizon.Seconds()
	}
	return math.Exp(-math.Ln2 * delta.Seconds() / halfLife)
}

// resetSegment prevents an outage from creating an artificial lag-one pair.
// Learned persistence and the matured Neff baseline are retained, but their
// EW weights are decayed on the next timestamped observation.
func (s *adaptivePathDecayState) resetSegment() {
	if s != nil {
		s.HaveValue = false
	}
}

func (s adaptivePathDecayState) autocorrelation() float64 {
	if s.PairCount < 8 || s.M2Prev <= 0 || s.M2Curr <= 0 {
		return 0
	}
	rho := s.CovLag1 / math.Sqrt(s.M2Prev*s.M2Curr)
	if !finiteAdaptiveDecay(rho) {
		return 0
	}
	return math.Max(0, math.Min(0.995, rho))
}

func (s adaptivePathDecayState) halfLife(horizon, lookback time.Duration) float64 {
	if horizon <= 0 {
		horizon = time.Minute
	}
	if lookback < horizon {
		lookback = horizon
	}
	// The cold-start prior is the old scale-derived value.  It is used only
	// until enough matured pairs exist to estimate persistence, so sparse
	// startup does not silently produce an infinite or zero half-life.
	prior := math.Sqrt(horizon.Seconds() * lookback.Seconds())
	if prior <= 0 || !finiteAdaptiveDecay(prior) {
		prior = horizon.Seconds()
	}
	if s.PairCount < 8 || s.MeanDelta <= 0 {
		return prior
	}
	rho := s.autocorrelation()
	if rho <= 0 {
		// A zero/negative persistence estimate is not evidence that old paths
		// should be discarded faster; retain the causal cold-start scale until
		// a positive persistence signal is identified.
		return prior
	}
	// Consecutive starts overlap for H.  Using the raw one-second callback
	// spacing would count near-duplicate windows as independent persistence
	// clocks and collapse the half-life to the horizon floor.  The renewal
	// clock is therefore the larger of observed spacing and H, matching the
	// overlap correction already used by weightedJointMoments.
	decayDelta := math.Max(horizon.Seconds(), s.MeanDelta)
	halfLife := -math.Ln2 * decayDelta / math.Log(rho)
	minHalfLife := math.Max(horizon.Seconds(), s.MeanDelta)
	maxHalfLife := math.Max(minHalfLife, lookback.Seconds())
	if !finiteAdaptiveDecay(halfLife) || halfLife <= 0 {
		return prior
	}
	return math.Max(minHalfLife, math.Min(maxHalfLife, halfLife))
}

func (s adaptivePathDecayState) decayFactor(delta time.Duration, horizon, lookback time.Duration) float64 {
	if delta <= 0 {
		return 1
	}
	halfLife := s.halfLife(horizon, lookback)
	if halfLife <= 0 || !finiteAdaptiveDecay(halfLife) {
		return 1
	}
	return math.Exp(-math.Ln2 * delta.Seconds() / halfLife)
}

// observeNeff updates the causal stability baseline.  Neff is not replaced by
// its EWMA in the estimator; the current Neff remains authoritative and the
// baseline only measures whether today's information mass is unusually low.
func (s *adaptivePathDecayState) observeNeff(
	at time.Time, neff float64, horizon, lookback time.Duration,
) {
	if s == nil || at.IsZero() || !finiteAdaptiveDecay(neff) || neff < 0 {
		return
	}
	if !s.LastNeff.IsZero() && at.Before(s.LastNeff) {
		return
	}
	if s.NeffCount <= 0 {
		s.NeffMean = neff
		s.NeffM2 = 0
		s.NeffCount = 1
		s.LastNeff = at
		return
	}
	if at.Equal(s.LastNeff) {
		return
	}
	decay := s.decayFactor(at.Sub(s.LastNeff), horizon, lookback)
	alpha := 1 - decay
	if alpha <= 0 {
		return
	}
	previousMean := s.NeffMean
	s.NeffMean = decay*s.NeffMean + alpha*neff
	// EWMA second central moment, evaluated against the pre-update mean.
	s.NeffM2 = decay * (s.NeffM2 + alpha*(neff-previousMean)*(neff-previousMean))
	s.NeffCount += alpha
	s.LastNeff = at
}

func (s adaptivePathDecayState) snapshot(horizon, lookback time.Duration) adaptivePathDecaySnapshot {
	return adaptivePathDecaySnapshot{
		HalfLifeSeconds:          s.halfLife(horizon, lookback),
		Autocorrelation:          s.autocorrelation(),
		PersistenceObservations:  s.PairCount,
		EffectiveSamplesBaseline: s.NeffMean,
		EffectiveSamplesStd:      math.Sqrt(math.Max(0, s.NeffM2)),
	}
}
