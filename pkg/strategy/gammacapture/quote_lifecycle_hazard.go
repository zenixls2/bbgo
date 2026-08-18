package gammacapture

import (
	"math"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// QuoteLifecycleHazardConfig controls the small, online discrete-hazard model
// used by the Bellman lifecycle component.  The model is deliberately
// discrete rather than Poisson: each review interval contributes one
// Bernoulli exposure, and the age bucket conditions the next-window hazard.
// The default update interval is five minutes, matching the model snapshot
// cadence; no future label has to mature before a new snapshot can be used.
type QuoteLifecycleHazardConfig struct {
	Enabled           bool           `json:"enabled" yaml:"enabled"`
	UpdateInterval    types.Duration `json:"updateInterval" yaml:"updateInterval"`
	AgeBucket         types.Duration `json:"ageBucket" yaml:"ageBucket"`
	DistanceBucketBps float64        `json:"distanceBucketBps" yaml:"distanceBucketBps"`
	PriorAlpha        float64        `json:"priorAlpha" yaml:"priorAlpha"`
	PriorBeta         float64        `json:"priorBeta" yaml:"priorBeta"`
	MaxAgeBuckets     int            `json:"maxAgeBuckets" yaml:"maxAgeBuckets"`
}

func (c *QuoteLifecycleHazardConfig) setDefaults() {
	if c.UpdateInterval <= 0 {
		c.UpdateInterval = types.Duration(5 * time.Minute)
	}
	if c.AgeBucket <= 0 {
		c.AgeBucket = types.Duration(5 * time.Minute)
	}
	if c.DistanceBucketBps <= 0 || !finiteLifecycleValue(c.DistanceBucketBps) {
		c.DistanceBucketBps = 5
	}
	if c.PriorAlpha <= 0 || !finiteLifecycleValue(c.PriorAlpha) {
		c.PriorAlpha = 1
	}
	if c.PriorBeta <= 0 || !finiteLifecycleValue(c.PriorBeta) {
		c.PriorBeta = 1
	}
	if c.MaxAgeBuckets <= 0 {
		c.MaxAgeBuckets = 12
	}
}

// QuoteLifecycleHazardSide names the executable side whose public-BBO touch
// is being observed. BUY uses the ask path; SELL uses the bid path.
type QuoteLifecycleHazardSide string

const (
	QuoteLifecycleHazardBuy  QuoteLifecycleHazardSide = "BUY"
	QuoteLifecycleHazardSell QuoteLifecycleHazardSide = "SELL"
)

type quoteLifecycleHazardKey struct {
	side     QuoteLifecycleHazardSide
	distance int
	age      int
}

type quoteLifecycleHazardPosterior struct {
	alpha, beta float64
	exposures   float64
	events      float64
}

// QuoteLifecycleHazardSnapshot is the causal probability of at least one
// public executable-BBO touch over the requested horizon. StdError is a
// delta-method uncertainty estimate for that probability.
type QuoteLifecycleHazardSnapshot struct {
	Ready            bool
	Probability      float64
	Survival         float64
	StdError         float64
	EffectiveSamples float64
	HazardPerReview  float64
	AgeBucket        int
	DistanceBucket   int
}

// QuoteLifecycleHazardPairSnapshot combines side-specific hazards. It does
// not assume independent private fills; independence is only used as a
// conservative public-touch proxy when no paired covariance is available.
type QuoteLifecycleHazardPairSnapshot struct {
	Ready            bool
	BuyProbability   float64
	SellProbability  float64
	BothProbability  float64
	BuyStdError      float64
	SellStdError     float64
	BothStdError     float64
	EffectiveSamples float64
}

// QuoteLifecycleHazardModel is an online Beta-Bernoulli table. Observe must be
// called with a review-boundary public-BBO touch label; repeated calls for the
// same side/distance/age bucket inside UpdateInterval are coalesced. This
// makes the estimator cheap on second-level BBO streams and naturally aligns
// it with five-minute continuation updates.
type QuoteLifecycleHazardModel struct {
	cfg          QuoteLifecycleHazardConfig
	posteriors   map[quoteLifecycleHazardKey]*quoteLifecycleHazardPosterior
	lastAt       map[quoteLifecycleHazardKey]time.Time
	observations int
}

func NewQuoteLifecycleHazardModel(config QuoteLifecycleHazardConfig) *QuoteLifecycleHazardModel {
	config.setDefaults()
	return &QuoteLifecycleHazardModel{
		cfg: config, posteriors: make(map[quoteLifecycleHazardKey]*quoteLifecycleHazardPosterior),
		lastAt: make(map[quoteLifecycleHazardKey]time.Time),
	}
}

func (m *QuoteLifecycleHazardModel) Config() QuoteLifecycleHazardConfig {
	if m == nil {
		return QuoteLifecycleHazardConfig{}
	}
	return m.cfg
}

func normalizeQuoteLifecycleHazardSide(side QuoteLifecycleHazardSide) QuoteLifecycleHazardSide {
	switch QuoteLifecycleHazardSide(strings.ToUpper(string(side))) {
	case QuoteLifecycleHazardBuy:
		return QuoteLifecycleHazardBuy
	case QuoteLifecycleHazardSell:
		return QuoteLifecycleHazardSell
	default:
		return ""
	}
}

func (m *QuoteLifecycleHazardModel) key(side QuoteLifecycleHazardSide, distanceBps float64, age time.Duration) (quoteLifecycleHazardKey, bool) {
	if m == nil {
		return quoteLifecycleHazardKey{}, false
	}
	side = normalizeQuoteLifecycleHazardSide(side)
	if side == "" || !finiteLifecycleValue(distanceBps) || distanceBps < 0 {
		return quoteLifecycleHazardKey{}, false
	}
	if age < 0 {
		age = 0
	}
	distance := int(math.Floor(distanceBps / m.cfg.DistanceBucketBps))
	ageBucket := int(age / time.Duration(m.cfg.AgeBucket))
	if ageBucket < 0 {
		ageBucket = 0
	}
	if ageBucket >= m.cfg.MaxAgeBuckets {
		ageBucket = m.cfg.MaxAgeBuckets - 1
	}
	return quoteLifecycleHazardKey{side: side, distance: distance, age: ageBucket}, true
}

// Observe adds one exposure and, when touched is true, one event. It returns
// false when the observation is disabled, invalid, or coalesced by the
// five-minute review interval.
func (m *QuoteLifecycleHazardModel) Observe(at time.Time, side QuoteLifecycleHazardSide, distanceBps float64, age time.Duration, touched bool) bool {
	if m == nil || !m.cfg.Enabled || at.IsZero() {
		return false
	}
	key, ok := m.key(side, distanceBps, age)
	if !ok {
		return false
	}
	if previous := m.lastAt[key]; !previous.IsZero() && at.Before(previous.Add(time.Duration(m.cfg.UpdateInterval))) {
		return false
	}
	p := m.posteriors[key]
	if p == nil {
		p = &quoteLifecycleHazardPosterior{alpha: m.cfg.PriorAlpha, beta: m.cfg.PriorBeta}
		m.posteriors[key] = p
	}
	p.exposures++
	if touched {
		p.events++
		p.alpha++
	} else {
		p.beta++
	}
	m.lastAt[key] = at
	m.observations++
	return true
}

func (m *QuoteLifecycleHazardModel) ObservationCount() int {
	if m == nil {
		return 0
	}
	return m.observations
}

func (m *QuoteLifecycleHazardModel) posterior(key quoteLifecycleHazardKey) (*quoteLifecycleHazardPosterior, bool) {
	if m == nil {
		return nil, false
	}
	if p := m.posteriors[key]; p != nil {
		return p, true
	}
	return nil, false
}

func (m *QuoteLifecycleHazardModel) Snapshot(side QuoteLifecycleHazardSide, distanceBps float64, age time.Duration, horizon time.Duration) QuoteLifecycleHazardSnapshot {
	snapshot := QuoteLifecycleHazardSnapshot{}
	if m == nil || !m.cfg.Enabled || horizon <= 0 {
		return snapshot
	}
	key, ok := m.key(side, distanceBps, age)
	if !ok {
		return snapshot
	}
	snapshot.AgeBucket, snapshot.DistanceBucket = key.age, key.distance
	p, ready := m.posterior(key)
	if !ready {
		// Age-specific cells are sparse early in a session. Falling back to the
		// youngest distance-matched cell avoids a false zero while retaining an
		// explicit Ready=false marker for the caller's confidence gate.
		for candidateAge := key.age - 1; candidateAge >= 0; candidateAge-- {
			candidate := key
			candidate.age = candidateAge
			if p, ready = m.posterior(candidate); ready {
				break
			}
		}
	}
	if !ready {
		p = &quoteLifecycleHazardPosterior{alpha: m.cfg.PriorAlpha, beta: m.cfg.PriorBeta}
	}
	if p.alpha <= 0 || p.beta <= 0 || p.alpha+p.beta <= 0 {
		return snapshot
	}
	hazard := p.alpha / (p.alpha + p.beta)
	// A review is a Bernoulli exposure. The ceiling keeps a partial final
	// review conservative and does not impose a continuous-time Poisson law.
	reviews := int(math.Ceil(float64(horizon) / float64(time.Duration(m.cfg.UpdateInterval))))
	if reviews < 1 {
		reviews = 1
	}
	survival := math.Pow(1-hazard, float64(reviews))
	probability := 1 - survival
	n := p.alpha + p.beta
	hazardSE := math.Sqrt(math.Max(0, hazard*(1-hazard)/(n+1)))
	probabilitySE := float64(reviews) * math.Pow(math.Max(0, 1-hazard), float64(reviews-1)) * hazardSE
	probabilitySE = math.Min(0.5, math.Max(0, probabilitySE))
	snapshot.Ready = ready && p.exposures > 0
	snapshot.Probability = math.Min(1, math.Max(0, probability))
	snapshot.Survival = math.Min(1, math.Max(0, survival))
	snapshot.StdError = probabilitySE
	snapshot.EffectiveSamples = p.exposures
	snapshot.HazardPerReview = hazard
	return snapshot
}

func (m *QuoteLifecycleHazardModel) PairSnapshot(horizon time.Duration, buyDistanceBps, sellDistanceBps float64, buyAge, sellAge time.Duration) QuoteLifecycleHazardPairSnapshot {
	buy := m.Snapshot(QuoteLifecycleHazardBuy, buyDistanceBps, buyAge, horizon)
	sell := m.Snapshot(QuoteLifecycleHazardSell, sellDistanceBps, sellAge, horizon)
	pair := QuoteLifecycleHazardPairSnapshot{
		BuyProbability: buy.Probability, SellProbability: sell.Probability,
		BuyStdError: buy.StdError, SellStdError: sell.StdError,
		EffectiveSamples: math.Min(buy.EffectiveSamples, sell.EffectiveSamples),
	}
	pair.BothProbability = buy.Probability * sell.Probability
	pair.BothStdError = math.Sqrt(
		math.Pow(sell.Probability*buy.StdError, 2) +
			math.Pow(buy.Probability*sell.StdError, 2))
	pair.BothStdError = math.Min(0.5, math.Max(0, pair.BothStdError))
	pair.Ready = buy.Ready && sell.Ready
	return pair
}
