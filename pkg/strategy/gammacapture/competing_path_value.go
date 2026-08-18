package gammacapture

import (
	"math"
	"time"
)

// CompetingPathOutcome is the mutually exclusive outcome of one completed
// Fast quote window.  The categories are exhaustive, so their posterior can
// be estimated directly instead of reconstructing it from three separately
// smoothed marginal touch probabilities.
type CompetingPathOutcome uint8

const (
	CompetingPathNone CompetingPathOutcome = iota
	CompetingPathBuyOnly
	CompetingPathSellOnly
	CompetingPathBoth
	competingPathOutcomeCount
)

func CompetingPathOutcomeFromTouches(buyTouched, sellTouched bool) CompetingPathOutcome {
	switch {
	case buyTouched && sellTouched:
		return CompetingPathBoth
	case buyTouched:
		return CompetingPathBuyOnly
	case sellTouched:
		return CompetingPathSellOnly
	default:
		return CompetingPathNone
	}
}

type CompetingPathValueSnapshot struct {
	Probabilities      [competingPathOutcomeCount]float64
	ConditionalMeanBps [competingPathOutcomeCount]float64
	ExpectedValueBps   float64
	EffectiveSamples   float64
	UpdatedAt          time.Time
}

type competingPathPending struct {
	MaturesAt time.Time
}

// CompetingPathValueModel is an online Dirichlet-multinomial outcome model
// with delayed labels.  Jeffreys' 1/2 prior is applied once to the four-way
// outcome, rather than independently to overlapping marginal events.
type CompetingPathValueModel struct {
	counts       [competingPathOutcomeCount]float64
	valueSums    [competingPathOutcomeCount]float64
	weightSquare float64
	lastUpdate   time.Time
	nextID       uint64
	pending      map[uint64]competingPathPending
}

func (m *CompetingPathValueModel) Predict(at, maturesAt time.Time) (uint64, CompetingPathValueSnapshot) {
	if m == nil || at.IsZero() || !maturesAt.After(at) {
		return 0, CompetingPathValueSnapshot{}
	}
	if m.pending == nil {
		m.pending = make(map[uint64]competingPathPending)
	}
	m.nextID++
	m.pending[m.nextID] = competingPathPending{MaturesAt: maturesAt}
	return m.nextID, m.Snapshot()
}

func (m *CompetingPathValueModel) UpdateLabel(
	now time.Time,
	id uint64,
	outcome CompetingPathOutcome,
	terminalValueBps, weight float64,
) bool {
	if m == nil || id == 0 || outcome >= competingPathOutcomeCount ||
		math.IsNaN(terminalValueBps) || math.IsInf(terminalValueBps, 0) ||
		weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
		return false
	}
	pending, ok := m.pending[id]
	if !ok || now.Before(pending.MaturesAt) ||
		(!m.lastUpdate.IsZero() && now.Before(m.lastUpdate)) {
		return false
	}
	delete(m.pending, id)
	m.counts[outcome] += weight
	m.valueSums[outcome] += weight * terminalValueBps
	m.weightSquare += weight * weight
	m.lastUpdate = now
	return true
}

func (m *CompetingPathValueModel) Snapshot() CompetingPathValueSnapshot {
	if m == nil {
		return CompetingPathValueSnapshot{}
	}
	total := 0.0
	for _, count := range m.counts {
		total += count
	}
	s := CompetingPathValueSnapshot{UpdatedAt: m.lastUpdate}
	posteriorTotal := total + 0.5*float64(competingPathOutcomeCount)
	for outcome := CompetingPathOutcome(0); outcome < competingPathOutcomeCount; outcome++ {
		s.Probabilities[outcome] = (m.counts[outcome] + 0.5) / posteriorTotal
		if m.counts[outcome] > 0 {
			s.ConditionalMeanBps[outcome] = m.valueSums[outcome] / m.counts[outcome]
		}
		s.ExpectedValueBps += s.Probabilities[outcome] * s.ConditionalMeanBps[outcome]
	}
	if m.weightSquare > 0 {
		s.EffectiveSamples = total * total / m.weightSquare
	}
	return s
}

func (m *CompetingPathValueModel) Reset() {
	if m != nil {
		*m = CompetingPathValueModel{}
	}
}
