package gammacapture

import (
	"math"
	"sync"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// HawkesDirectionConfig controls the bounded, online marked-Hawkes direction
// layer. Excitation values are branching ratios: their sum is the expected
// number of offspring events per parent event. Keeping the sum below one
// makes the process subcritical and prevents a burst from becoming a
// permanent directional bias.
type HawkesDirectionConfig struct {
	Enabled         bool           `json:"enabled" yaml:"enabled"`
	HalfLife        types.Duration `json:"halfLife" yaml:"halfLife"`
	SelfExcitation  float64        `json:"selfExcitation" yaml:"selfExcitation"`
	CrossExcitation float64        `json:"crossExcitation" yaml:"crossExcitation"`
	MinEvents       int            `json:"minEvents" yaml:"minEvents"`
}

func (c *HawkesDirectionConfig) setDefaults() {
	if c.HalfLife <= 0 {
		c.HalfLife = types.Duration(45 * time.Second)
	}
	if c.SelfExcitation <= 0 {
		c.SelfExcitation = 0.35
	}
	if c.CrossExcitation < 0 {
		c.CrossExcitation = 0
	}
	if c.SelfExcitation+c.CrossExcitation >= 1 {
		c.SelfExcitation = 0.35
		c.CrossExcitation = 0.05
	}
	if c.MinEvents <= 0 {
		c.MinEvents = 8
	}
}

// HawkesDirectionSnapshot is the causal prediction consumed by the fast
// quote. Direction is normalized, while Confidence is derived from the
// integrated event intensity and observed event count rather than a manual
// directional weight.
type HawkesDirectionSnapshot struct {
	Ready      bool
	Events     int
	LambdaUp   float64
	LambdaDown float64
	Total      float64
	Direction  float64
	Confidence float64
	Observed   time.Duration
}

// HawkesDirectionModel is intentionally small: only two exponentially
// decaying excitation states and causal event counters are retained. This is
// suitable for live updates and checkpoint replay without rescanning history.
type HawkesDirectionModel struct {
	mu  sync.Mutex
	cfg HawkesDirectionConfig

	lastAt         time.Time
	firstAt        time.Time
	upExcitation   float64
	downExcitation float64
	upEvents       int
	downEvents     int
	totalNotional  float64
	notionalEWMA   float64
}

func NewHawkesDirectionModel(cfg HawkesDirectionConfig) *HawkesDirectionModel {
	cfg.setDefaults()
	return &HawkesDirectionModel{cfg: cfg}
}

func (m *HawkesDirectionModel) ObserveTrade(at time.Time, trade types.Trade) {
	if m == nil || at.IsZero() || trade.Price.Sign() <= 0 || trade.Quantity.Sign() <= 0 {
		return
	}
	up := trade.Side != types.SideTypeSell
	if trade.Side == "" {
		up = trade.IsBuyer
	}
	notional := trade.QuoteQuantity.Float64()
	if notional <= 0 {
		notional = trade.Price.Float64() * trade.Quantity.Float64()
	}
	m.observe(at, up, notional)
}

// ObserveDirection is useful to deterministic replay and tests that already
// classify a public event as an up/down price-moving event.
func (m *HawkesDirectionModel) ObserveDirection(at time.Time, up bool, notional float64) {
	if m == nil || at.IsZero() || notional <= 0 || math.IsNaN(notional) || math.IsInf(notional, 0) {
		return
	}
	m.observe(at, up, notional)
}

func (m *HawkesDirectionModel) observe(at time.Time, up bool, notional float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.Enabled {
		return
	}
	if !m.firstAt.IsZero() && at.Before(m.lastAt) {
		return
	}
	if m.firstAt.IsZero() {
		m.firstAt = at
	}
	m.decayLocked(at)
	if m.notionalEWMA <= 0 {
		m.notionalEWMA = notional
	}
	// The mark changes excitation only within a bounded range. It captures
	// unusually large public prints without allowing one bad quantity row to
	// dominate the intensity.
	mark := notional / math.Max(1, m.notionalEWMA)
	mark = math.Max(0.5, math.Min(3, mark))
	m.notionalEWMA = 0.95*m.notionalEWMA + 0.05*notional
	branchRate := 1 / math.Max(time.Second.Seconds(), time.Duration(m.cfg.HalfLife).Seconds())
	if up {
		m.upExcitation += m.cfg.SelfExcitation * branchRate * mark
		m.downExcitation += m.cfg.CrossExcitation * branchRate * mark
		m.upEvents++
	} else {
		m.downExcitation += m.cfg.SelfExcitation * branchRate * mark
		m.upExcitation += m.cfg.CrossExcitation * branchRate * mark
		m.downEvents++
	}
	m.totalNotional += notional
	m.lastAt = at
}

func (m *HawkesDirectionModel) decayLocked(now time.Time) {
	if m.lastAt.IsZero() || !now.After(m.lastAt) {
		return
	}
	halfLife := math.Max(time.Second.Seconds(), time.Duration(m.cfg.HalfLife).Seconds())
	decay := math.Exp(-math.Ln2 * now.Sub(m.lastAt).Seconds() / halfLife)
	m.upExcitation *= decay
	m.downExcitation *= decay
	m.lastAt = now
}

func (m *HawkesDirectionModel) Snapshot(now time.Time) HawkesDirectionSnapshot {
	if m == nil {
		return HawkesDirectionSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.Enabled || m.firstAt.IsZero() {
		return HawkesDirectionSnapshot{}
	}
	m.decayLocked(now)
	observed := now.Sub(m.firstAt)
	if observed < 0 {
		observed = 0
	}
	priorSeconds := math.Max(time.Second.Seconds(), time.Duration(m.cfg.HalfLife).Seconds())
	baseUp := (1 + float64(m.upEvents)) / (priorSeconds + observed.Seconds())
	baseDown := (1 + float64(m.downEvents)) / (priorSeconds + observed.Seconds())
	lambdaUp := baseUp + m.upExcitation
	lambdaDown := baseDown + m.downExcitation
	total := math.Max(0, lambdaUp+lambdaDown)
	direction := 0.0
	if total > 0 {
		direction = (lambdaUp - lambdaDown) / total
	}
	eventCount := m.upEvents + m.downEvents
	confidence := 1 - math.Exp(-total*priorSeconds)
	confidence *= math.Min(1, float64(eventCount)/float64(m.cfg.MinEvents))
	return HawkesDirectionSnapshot{
		Ready: eventCount >= m.cfg.MinEvents, Events: eventCount,
		LambdaUp: lambdaUp, LambdaDown: lambdaDown, Total: total,
		Direction: math.Max(-1, math.Min(1, direction)), Confidence: math.Max(0, math.Min(1, confidence)),
		Observed: observed,
	}
}
