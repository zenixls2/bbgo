package gammacapture

import (
	"math"
	"time"
)

type terminalTailTargetPending struct {
	MaturesAt time.Time
	Feature   float64
}

// TerminalTailTargetModel is a delayed-label EW ridge model for the
// same-horizon terminal-tail inventory return.  The supplied feature is a
// causal standardized deviation from the current equilibrium; the fitted
// slope is free to learn either mean reversion or continuation.
type TerminalTailTargetModel struct {
	halfLife    time.Duration
	stats       sideImbalanceSufficientStats
	lastUpdate  time.Time
	lastPredict time.Time
	nextID      uint64
	pending     map[uint64]terminalTailTargetPending
}

type TerminalTailTargetSnapshot struct {
	BaselineMeanBps    float64
	ConditionalMeanBps float64
	EffectiveSamples   float64
	Feature            float64
	UpdatedAt          time.Time
}

func NewTerminalTailTargetModel(halfLife time.Duration) *TerminalTailTargetModel {
	return &TerminalTailTargetModel{halfLife: halfLife}
}

func clampTerminalTailFeature(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(-5, math.Min(5, value))
}

func (m *TerminalTailTargetModel) decayFactor(at time.Time) float64 {
	if m == nil || m.halfLife <= 0 || m.lastUpdate.IsZero() || !at.After(m.lastUpdate) {
		return 1
	}
	return math.Exp(-math.Ln2 * at.Sub(m.lastUpdate).Seconds() / m.halfLife.Seconds())
}

func (m *TerminalTailTargetModel) Predict(
	at, maturesAt time.Time,
	standardizedDeviation float64,
) (uint64, TerminalTailTargetSnapshot) {
	if m == nil || m.halfLife <= 0 || at.IsZero() || !maturesAt.After(at) ||
		(!m.lastUpdate.IsZero() && at.Before(m.lastUpdate)) ||
		(!m.lastPredict.IsZero() && at.Before(m.lastPredict)) {
		return 0, TerminalTailTargetSnapshot{}
	}
	feature := clampTerminalTailFeature(standardizedDeviation)
	stats := m.stats
	stats.decay(m.decayFactor(at))
	baseline, conditional := stats.predict(feature)
	if m.pending == nil {
		m.pending = make(map[uint64]terminalTailTargetPending)
	}
	m.nextID++
	m.lastPredict = at
	m.pending[m.nextID] = terminalTailTargetPending{MaturesAt: maturesAt, Feature: feature}
	return m.nextID, TerminalTailTargetSnapshot{
		BaselineMeanBps: baseline, ConditionalMeanBps: conditional,
		EffectiveSamples: stats.weight, Feature: feature, UpdatedAt: m.lastUpdate,
	}
}

func (m *TerminalTailTargetModel) UpdateWeightedLabel(
	now time.Time, id uint64, terminalTailReturnBps, weight float64,
) bool {
	if m == nil || id == 0 || math.IsNaN(terminalTailReturnBps) ||
		math.IsInf(terminalTailReturnBps, 0) || weight <= 0 ||
		math.IsNaN(weight) || math.IsInf(weight, 0) {
		return false
	}
	pending, ok := m.pending[id]
	if !ok || now.Before(pending.MaturesAt) ||
		(!m.lastUpdate.IsZero() && now.Before(m.lastUpdate)) {
		return false
	}
	delete(m.pending, id)
	m.stats.decay(m.decayFactor(now))
	m.stats.add(pending.Feature, terminalTailReturnBps, weight)
	m.lastUpdate = now
	return true
}

func (m *TerminalTailTargetModel) Reset() {
	if m == nil {
		return
	}
	halfLife := m.halfLife
	*m = TerminalTailTargetModel{halfLife: halfLife}
}
