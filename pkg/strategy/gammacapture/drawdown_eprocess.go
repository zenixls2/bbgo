package gammacapture

import (
	"math"
	"time"
)

// DrawdownEProcessConfig defines a time-uniform sequential test in quadratic-
// variation time. BarrierWidth and Windows are existing strategy scales; the
// alternatives are mixed rather than maximized, preserving e-value validity.
type DrawdownEProcessConfig struct {
	BarrierWidth   float64
	Windows        []time.Duration
	ConfidenceZ    float64
	MinimumMinutes int
}

func (c DrawdownEProcessConfig) withDefaults() DrawdownEProcessConfig {
	if c.BarrierWidth <= 0 {
		c.BarrierWidth = .001
	}
	if len(c.Windows) == 0 {
		c.Windows = []time.Duration{10 * time.Minute, 15 * time.Minute, 30 * time.Minute}
	}
	if c.ConfidenceZ <= 0 {
		c.ConfidenceZ = 1.6448536269514722
	}
	if c.MinimumMinutes <= 0 {
		c.MinimumMinutes = 2
	}
	return c
}

type drawdownESide struct {
	anchor   float64
	previous float64
	qv       float64
	minutes  int
}

func (s *drawdownESide) reset(price float64) {
	s.anchor = price
	s.previous = price
	s.qv = 0
	s.minutes = 0
}

// DrawdownEProcessDecision is not a fitted direction forecast. DownEValue is
// time-uniform evidence against nonnegative local drift since a running high;
// RecoveryEValue is the symmetric evidence against nonpositive drift since the
// latest running low. Posterior-style probabilities use equal prior odds and
// are diagnostics conditional on the half-normal mixture alternative.
type DrawdownEProcessDecision struct {
	Healthy bool
	Reason  string
	At      time.Time
	Active  bool

	DownAlarm           bool
	RecoveryAlarm       bool
	DownEValue          float64
	RecoveryEValue      float64
	DownProbability     float64
	RecoveryProbability float64
	Threshold           float64
	EpisodeStart        time.Time
	Minutes             int
	AskScoreBps         float64
	BidScoreBps         float64
	AskQV               float64
	BidQV               float64
	// BidForecastBps is the posterior mean adverse SELL markout over the
	// requested reference horizon.  It is used only while the time-uniform
	// bid/ask agreement alarm is active.
	BidForecastBps float64
}

// DrawdownEProcess implements a two-sided executable-price state machine. A
// drawdown alarm starts a no-reentry episode; only a separate recovery e-process
// from the latest low can end it. Resetting at a new extremum discards evidence
// and therefore cannot inflate an e-value.
type DrawdownEProcess struct {
	config        DrawdownEProcessConfig
	lastAt        time.Time
	active        bool
	episodeStart  time.Time
	bid           drawdownESide
	ask           drawdownESide
	alarmBidScore float64
	alarmBidQV    float64
	alarmMinutes  int
	alarmEValue   float64
}

func NewDrawdownEProcess(c DrawdownEProcessConfig) *DrawdownEProcess {
	return &DrawdownEProcess{config: c.withDefaults()}
}

func (m *DrawdownEProcess) Reset() {
	if m == nil {
		return
	}
	c := m.config
	*m = DrawdownEProcess{config: c}
}

func (m *DrawdownEProcess) ObserveMinute(at time.Time, bid, ask float64) DrawdownEProcessDecision {
	d := DrawdownEProcessDecision{Reason: "waiting for consecutive executable minutes"}
	if m == nil || at.IsZero() || bid <= 0 || ask < bid {
		return d
	}
	at = at.UTC().Truncate(time.Minute)
	if m.lastAt.IsZero() {
		m.lastAt = at
		m.bid.reset(bid)
		m.ask.reset(ask)
		return d
	}
	if at.Equal(m.lastAt) {
		// Book ticker is event-driven.  More than one update in the same minute
		// must not reset or multiply sequential evidence.
		d.Reason = "same-minute executable update retained"
		return d
	}
	if !at.Equal(m.lastAt.Add(time.Minute)) {
		m.Reset()
		m.lastAt = at
		m.bid.reset(bid)
		m.ask.reset(ask)
		d.Reason = "one-minute gap reset sequential evidence"
		return d
	}
	m.lastAt = at
	d.At = at
	d.Threshold = eProcessThreshold(m.config.ConfidenceZ)
	if !m.active {
		updateExtremumSide(&m.bid, bid, true)
		updateExtremumSide(&m.ask, ask, true)
		d.DownEValue = math.Min(
			m.multiscaleE(math.Log(m.bid.anchor/bid), m.bid.qv),
			m.multiscaleE(math.Log(m.ask.anchor/ask), m.ask.qv))
		d.DownProbability = d.DownEValue / (1 + d.DownEValue)
		d.Minutes = minInt(m.bid.minutes, m.ask.minutes)
		d.AskScoreBps = math.Log(m.ask.anchor/ask) * 10_000
		d.BidScoreBps = math.Log(m.bid.anchor/bid) * 10_000
		d.AskQV, d.BidQV = m.ask.qv, m.bid.qv
		if d.Minutes >= m.config.MinimumMinutes && d.DownEValue >= d.Threshold {
			m.alarmBidScore = math.Log(m.bid.anchor / bid)
			m.alarmBidQV = m.bid.qv
			m.alarmMinutes = m.bid.minutes
			m.alarmEValue = d.DownEValue
			m.active = true
			m.episodeStart = at
			m.bid.reset(bid)
			m.ask.reset(ask)
			d.DownAlarm = true
			d.Active = true
			d.BidForecastBps = m.DownsideForecastBps(m.config.Windows[len(m.config.Windows)-1])
			d.EpisodeStart = at
			d.Healthy = true
			d.Reason = "time-uniform executable drawdown evidence"
			return d
		}
		d.Active = false
		d.Healthy = d.Minutes >= m.config.MinimumMinutes
		d.Reason = "drawdown evidence below time-uniform threshold"
		return d
	}

	updateExtremumSide(&m.bid, bid, false)
	updateExtremumSide(&m.ask, ask, false)
	d.RecoveryEValue = math.Min(
		m.multiscaleE(math.Log(bid/m.bid.anchor), m.bid.qv),
		m.multiscaleE(math.Log(ask/m.ask.anchor), m.ask.qv))
	d.RecoveryProbability = d.RecoveryEValue / (1 + d.RecoveryEValue)
	d.Minutes = minInt(m.bid.minutes, m.ask.minutes)
	d.AskScoreBps = math.Log(ask/m.ask.anchor) * 10_000
	d.BidScoreBps = math.Log(bid/m.bid.anchor) * 10_000
	d.AskQV, d.BidQV = m.ask.qv, m.bid.qv
	d.Active = true
	d.DownEValue = m.alarmEValue
	d.DownProbability = d.DownEValue / (1 + d.DownEValue)
	d.BidForecastBps = m.DownsideForecastBps(m.config.Windows[len(m.config.Windows)-1])
	d.EpisodeStart = m.episodeStart
	d.Healthy = true
	d.Reason = "drawdown active; recovery evidence below threshold"
	if d.Minutes >= m.config.MinimumMinutes && d.RecoveryEValue >= d.Threshold {
		d.RecoveryAlarm = true
		d.Active = false
		d.Reason = "time-uniform executable recovery evidence"
		m.active = false
		m.episodeStart = time.Time{}
		m.alarmBidScore, m.alarmBidQV, m.alarmMinutes, m.alarmEValue = 0, 0, 0, 0
		m.bid.reset(bid)
		m.ask.reset(ask)
	}
	return d
}

// DownsideForecastBps converts the half-normal drift-mixture posterior from
// executable-bid QV time back to a same-horizon adverse markout.  The QV rate
// is estimated causally from the alarm episode; a jump raises QV and therefore
// lowers the inferred drift rather than being mistaken for persistent trend.
func (m *DrawdownEProcess) DownsideForecastBps(horizon time.Duration) float64 {
	if m == nil || !m.active || horizon <= 0 || m.alarmBidScore <= 0 ||
		m.alarmBidQV <= 0 || m.alarmMinutes <= 0 {
		return 0
	}
	futureQV := m.alarmBidQV * horizon.Minutes() / float64(m.alarmMinutes)
	minimum := m.config.Windows[0]
	for _, window := range m.config.Windows[1:] {
		if window > 0 && window < minimum {
			minimum = window
		}
	}
	var forecast float64
	var count int
	for _, window := range m.config.Windows {
		if window <= 0 {
			continue
		}
		scale := m.config.BarrierWidth * math.Sqrt(window.Seconds()/minimum.Seconds())
		theta := halfNormalDriftPosteriorMean(m.alarmBidScore, m.alarmBidQV, 1/scale)
		forecast += theta * futureQV * 10_000
		count++
	}
	if count == 0 {
		return 0
	}
	return forecast / float64(count)
}

func updateExtremumSide(side *drawdownESide, price float64, high bool) {
	if side == nil || price <= 0 {
		return
	}
	if side.anchor <= 0 || high && price >= side.anchor || !high && price <= side.anchor {
		side.reset(price)
		return
	}
	if side.previous > 0 {
		step := math.Log(price / side.previous)
		side.qv += step * step
	}
	side.previous = price
	side.minutes++
}

func (m *DrawdownEProcess) multiscaleE(score, qv float64) float64 {
	if m == nil || score < 0 || qv < 0 {
		return 0
	}
	minimum := m.config.Windows[0]
	for _, window := range m.config.Windows[1:] {
		if window > 0 && window < minimum {
			minimum = window
		}
	}
	var total float64
	var count int
	for _, window := range m.config.Windows {
		if window <= 0 {
			continue
		}
		scale := m.config.BarrierWidth * math.Sqrt(window.Seconds()/minimum.Seconds())
		total += halfNormalDriftMixtureE(score, qv, 1/scale)
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

// halfNormalDriftMixtureE integrates exp(theta*x-theta^2*A/2) over a
// half-normal prior theta>=0. For X_A=B_A under the no-drift QV clock it is a
// nonnegative martingale with initial value one.
func halfNormalDriftMixtureE(score, qv, tau float64) float64 {
	if score < 0 || qv < 0 || tau <= 0 {
		return 0
	}
	b := qv + 1/(tau*tau)
	if b <= 0 {
		return 0
	}
	z := score / math.Sqrt(b)
	phi := .5 * math.Erfc(-z/math.Sqrt2)
	logE := math.Log(2) - math.Log(tau) - .5*math.Log(b) +
		.5*score*score/b + math.Log(math.Max(math.SmallestNonzeroFloat64, phi))
	if logE >= math.Log(math.MaxFloat64) {
		return math.MaxFloat64
	}
	return math.Exp(logE)
}

func halfNormalDriftPosteriorMean(score, qv, tau float64) float64 {
	if score < 0 || qv <= 0 || tau <= 0 {
		return 0
	}
	b := qv + 1/(tau*tau)
	mean := score / b
	sd := 1 / math.Sqrt(b)
	z := mean / sd
	cdf := .5 * math.Erfc(-z/math.Sqrt2)
	if cdf <= math.SmallestNonzeroFloat64 {
		return math.Max(0, mean)
	}
	density := math.Exp(-.5*z*z) / math.Sqrt(2*math.Pi)
	return math.Max(0, mean+sd*density/cdf)
}

func eProcessThreshold(z float64) float64 {
	alpha := .5 * math.Erfc(math.Max(0, z)/math.Sqrt2)
	if alpha <= 0 {
		return math.MaxFloat64
	}
	return 1 / alpha
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
