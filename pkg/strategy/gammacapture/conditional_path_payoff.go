package gammacapture

import (
	"math"
	"time"
)

type ConditionalPathPayoffSnapshot struct {
	BaselineMeanBps [competingPathOutcomeCount]float64
	ShrunkMeanBps   [competingPathOutcomeCount]float64
	EffectiveCounts [competingPathOutcomeCount]float64
	UpdatedAt       time.Time
}

type conditionalPathPayoffPending struct {
	MaturesAt time.Time
}

// ConditionalPathPayoffModel estimates only the payoff magnitude conditional
// on a one-sided fill.  The empirical-Bayes output shrinks sparse or stale
// evidence toward the no-new-order excess payoff of zero by one pseudo-sample.
type ConditionalPathPayoffModel struct {
	halfLife   time.Duration
	counts     [competingPathOutcomeCount]float64
	valueSums  [competingPathOutcomeCount]float64
	lastUpdate time.Time
	nextID     uint64
	pending    map[uint64]conditionalPathPayoffPending
}

func NewConditionalPathPayoffModel(halfLife time.Duration) *ConditionalPathPayoffModel {
	return &ConditionalPathPayoffModel{halfLife: halfLife}
}

func (m *ConditionalPathPayoffModel) decayFactor(at time.Time) float64 {
	if m == nil || m.halfLife <= 0 || m.lastUpdate.IsZero() || !at.After(m.lastUpdate) {
		return 1
	}
	return math.Exp(-math.Ln2 * at.Sub(m.lastUpdate).Seconds() / m.halfLife.Seconds())
}

func (m *ConditionalPathPayoffModel) snapshotAt(at time.Time) ConditionalPathPayoffSnapshot {
	s := ConditionalPathPayoffSnapshot{}
	if m == nil || m.halfLife <= 0 {
		return s
	}
	decay := m.decayFactor(at)
	s.UpdatedAt = m.lastUpdate
	for _, outcome := range []CompetingPathOutcome{CompetingPathBuyOnly, CompetingPathSellOnly} {
		count := m.counts[outcome] * decay
		sum := m.valueSums[outcome] * decay
		s.EffectiveCounts[outcome] = count
		if count > 0 {
			s.BaselineMeanBps[outcome] = sum / count
			s.ShrunkMeanBps[outcome] = count / (count + 1) * s.BaselineMeanBps[outcome]
		}
	}
	return s
}

func (m *ConditionalPathPayoffModel) Predict(at, maturesAt time.Time) (uint64, ConditionalPathPayoffSnapshot) {
	if m == nil || m.halfLife <= 0 || at.IsZero() || !maturesAt.After(at) ||
		(!m.lastUpdate.IsZero() && at.Before(m.lastUpdate)) {
		return 0, ConditionalPathPayoffSnapshot{}
	}
	if m.pending == nil {
		m.pending = make(map[uint64]conditionalPathPayoffPending)
	}
	m.nextID++
	m.pending[m.nextID] = conditionalPathPayoffPending{MaturesAt: maturesAt}
	return m.nextID, m.snapshotAt(at)
}

func (m *ConditionalPathPayoffModel) UpdateLabel(now time.Time, id uint64, outcome CompetingPathOutcome, valueBps float64) bool {
	return m.UpdateWeightedLabel(now, id, outcome, valueBps, 1)
}

func (m *ConditionalPathPayoffModel) UpdateWeightedLabel(now time.Time, id uint64, outcome CompetingPathOutcome, valueBps, weight float64) bool {
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
	for i := range m.counts {
		m.counts[i] *= decay
		m.valueSums[i] *= decay
	}
	if outcome == CompetingPathBuyOnly || outcome == CompetingPathSellOnly {
		m.counts[outcome] += weight
		m.valueSums[outcome] += weight * valueBps
	}
	m.lastUpdate = now
	return true
}

func (m *ConditionalPathPayoffModel) Reset() {
	if m == nil {
		return
	}
	halfLife := m.halfLife
	*m = ConditionalPathPayoffModel{halfLife: halfLife}
}
