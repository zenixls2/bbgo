package gammacapture

import (
	"math"
	"time"
)

type sideImbalanceSufficientStats struct {
	weight, sumX, sumY, sumXX, sumXY float64
}

func (s *sideImbalanceSufficientStats) decay(factor float64) {
	s.weight *= factor
	s.sumX *= factor
	s.sumY *= factor
	s.sumXX *= factor
	s.sumXY *= factor
}

func (s *sideImbalanceSufficientStats) add(x, y, weight float64) {
	s.weight += weight
	s.sumX += weight * x
	s.sumY += weight * y
	s.sumXX += weight * x * x
	s.sumXY += weight * x * y
}

func (s sideImbalanceSufficientStats) predict(x float64) (baseline, conditional float64) {
	if s.weight <= 0 {
		return 0, 0
	}
	baseline = s.sumY / s.weight
	if s.weight <= 1 {
		return baseline, baseline
	}
	meanX := s.sumX / s.weight
	centeredXX := math.Max(0, s.sumXX-s.sumX*s.sumX/s.weight)
	centeredXY := s.sumXY - s.sumX*s.sumY/s.weight
	// Unit ridge precision is one side-aligned imbalance pseudo-observation.
	slope := centeredXY / (centeredXX + 1)
	conditional = baseline + slope*(x-meanX)
	if math.IsNaN(conditional) || math.IsInf(conditional, 0) {
		conditional = baseline
	}
	return baseline, conditional
}

type SideImbalancePayoffSnapshot struct {
	BaselineBuyBps, ConditionalBuyBps   float64
	BaselineSellBps, ConditionalSellBps float64
	BuyEffectiveSamples                 float64
	SellEffectiveSamples                float64
	Imbalance                           float64
	UpdatedAt                           time.Time
}

type sideImbalancePending struct {
	MaturesAt   time.Time
	BuyX, SellX float64
}

// SideImbalancePayoffModel is an online side-specific EW ridge model.  BUY
// sees raw top-of-book imbalance; SELL sees its price-reflected counterpart.
type SideImbalancePayoffModel struct {
	halfLife   time.Duration
	buy, sell  sideImbalanceSufficientStats
	lastUpdate time.Time
	nextID     uint64
	pending    map[uint64]sideImbalancePending
}

func NewSideImbalancePayoffModel(halfLife time.Duration) *SideImbalancePayoffModel {
	return &SideImbalancePayoffModel{halfLife: halfLife}
}

func clampBookImbalance(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(-1, math.Min(1, value))
}

func (m *SideImbalancePayoffModel) decayFactor(at time.Time) float64 {
	if m == nil || m.halfLife <= 0 || m.lastUpdate.IsZero() || !at.After(m.lastUpdate) {
		return 1
	}
	return math.Exp(-math.Ln2 * at.Sub(m.lastUpdate).Seconds() / m.halfLife.Seconds())
}

func (m *SideImbalancePayoffModel) Predict(at, maturesAt time.Time, imbalance float64) (uint64, SideImbalancePayoffSnapshot) {
	if m == nil || m.halfLife <= 0 || at.IsZero() || !maturesAt.After(at) ||
		(!m.lastUpdate.IsZero() && at.Before(m.lastUpdate)) {
		return 0, SideImbalancePayoffSnapshot{}
	}
	imbalance = clampBookImbalance(imbalance)
	decay := m.decayFactor(at)
	buy, sell := m.buy, m.sell
	buy.decay(decay)
	sell.decay(decay)
	baselineBuy, conditionalBuy := buy.predict(imbalance)
	baselineSell, conditionalSell := sell.predict(-imbalance)
	if m.pending == nil {
		m.pending = make(map[uint64]sideImbalancePending)
	}
	m.nextID++
	m.pending[m.nextID] = sideImbalancePending{
		MaturesAt: maturesAt, BuyX: imbalance, SellX: -imbalance,
	}
	return m.nextID, SideImbalancePayoffSnapshot{
		BaselineBuyBps: baselineBuy, ConditionalBuyBps: conditionalBuy,
		BaselineSellBps: baselineSell, ConditionalSellBps: conditionalSell,
		BuyEffectiveSamples: buy.weight, SellEffectiveSamples: sell.weight,
		Imbalance: imbalance, UpdatedAt: m.lastUpdate,
	}
}

func (m *SideImbalancePayoffModel) UpdateWeightedLabel(
	now time.Time,
	id uint64,
	outcome CompetingPathOutcome,
	valueBps, weight float64,
) bool {
	if m == nil || id == 0 || outcome >= competingPathOutcomeCount ||
		math.IsNaN(valueBps) || math.IsInf(valueBps, 0) || weight <= 0 ||
		math.IsNaN(weight) || math.IsInf(weight, 0) {
		return false
	}
	pending, ok := m.pending[id]
	if !ok || now.Before(pending.MaturesAt) ||
		(!m.lastUpdate.IsZero() && now.Before(m.lastUpdate)) {
		return false
	}
	delete(m.pending, id)
	decay := m.decayFactor(now)
	m.buy.decay(decay)
	m.sell.decay(decay)
	switch outcome {
	case CompetingPathBuyOnly:
		m.buy.add(pending.BuyX, valueBps, weight)
	case CompetingPathSellOnly:
		m.sell.add(pending.SellX, valueBps, weight)
	}
	m.lastUpdate = now
	return true
}

func (m *SideImbalancePayoffModel) Reset() {
	if m == nil {
		return
	}
	halfLife := m.halfLife
	*m = SideImbalancePayoffModel{halfLife: halfLife}
}
