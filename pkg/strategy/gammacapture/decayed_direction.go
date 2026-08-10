package gammacapture

import (
	"math"
	"time"
)

const directionWeightEpsilon = 1e-3

type decayedDirectionEvent struct {
	at        time.Time
	direction Direction
}

// DecayedDirectionSnapshot is the symmetric Beta(1,1) posterior over recent
// clean up/down crossings. Fractional event weights come from exponential
// time decay, so PosteriorDirection remains defined even in a sparse market.
type DecayedDirectionSnapshot struct {
	UpWeight           float64
	DownWeight         float64
	EffectiveSamples   float64
	PosteriorUp        float64
	PosteriorDirection float64
}

// DecayedDirectionModel is retained for replay and compatibility diagnostics.
// Live passive quote skew uses the strictly selected-window Beta posterior in
// inferFastCrossing, so events outside that window cannot leak into decisions.
type DecayedDirectionModel struct {
	halfLife time.Duration
	events   []decayedDirectionEvent
	last     time.Time
}

func NewDecayedDirectionModel(halfLife time.Duration) *DecayedDirectionModel {
	if halfLife <= 0 {
		halfLife = 10 * time.Minute
	}
	return &DecayedDirectionModel{halfLife: halfLife}
}

func (m *DecayedDirectionModel) Update(event CrossingEvent) {
	if m == nil || event.GapAffected || event.ExchangeTime.IsZero() ||
		(event.Direction != DirectionUp && event.Direction != DirectionDown) {
		return
	}
	if !m.last.IsZero() && event.ExchangeTime.Before(m.last) {
		return
	}
	m.events = append(m.events, decayedDirectionEvent{
		at: event.ExchangeTime, direction: event.Direction,
	})
	m.last = event.ExchangeTime
	m.trim(event.ExchangeTime)
}

func (m *DecayedDirectionModel) Snapshot(now time.Time) DecayedDirectionSnapshot {
	if m == nil || now.IsZero() {
		return DecayedDirectionSnapshot{PosteriorUp: 0.5}
	}
	m.trim(now)
	var upWeight, downWeight float64
	for _, event := range m.events {
		age := now.Sub(event.at)
		if age < 0 {
			continue
		}
		weight := math.Exp2(-age.Seconds() / m.halfLife.Seconds())
		if event.direction == DirectionUp {
			upWeight += weight
		} else {
			downWeight += weight
		}
	}
	effectiveSamples := upWeight + downWeight
	posteriorUp := (1 + upWeight) / (2 + effectiveSamples)
	return DecayedDirectionSnapshot{
		UpWeight:           upWeight,
		DownWeight:         downWeight,
		EffectiveSamples:   effectiveSamples,
		PosteriorUp:        posteriorUp,
		PosteriorDirection: 2*posteriorUp - 1,
	}
}

func (m *DecayedDirectionModel) trim(now time.Time) {
	if m == nil || m.halfLife <= 0 || len(m.events) == 0 {
		return
	}
	// Once an event contributes less than 0.1% of one observation, retaining it
	// cannot materially overcome the two pseudo-observations in the Beta prior.
	retention := time.Duration(math.Log2(1/directionWeightEpsilon) * float64(m.halfLife))
	cutoff := now.Add(-retention)
	first := 0
	for first < len(m.events) && m.events[first].at.Before(cutoff) {
		first++
	}
	if first > 0 {
		m.events = append([]decayedDirectionEvent(nil), m.events[first:]...)
	}
}

// fastEvidenceCoverage is a continuous [0,1] data-coverage weight. Both public
// trade and BBO streams are required; the less-complete stream is the
// statistical bottleneck and therefore sets the weight.
func fastEvidenceCoverage(snapshot FastEvidenceSnapshot, minTrades, minBBOUpdates int) float64 {
	tradeCoverage := evidenceCountCoverage(snapshot.TradeCount, minTrades)
	bboCoverage := evidenceCountCoverage(snapshot.BBOCount, minBBOUpdates)
	return math.Min(tradeCoverage, bboCoverage)
}

// FastEvidenceCoverage exposes the live public-data coverage weighting to
// deterministic replay and research tooling.
func FastEvidenceCoverage(snapshot FastEvidenceSnapshot, minTrades, minBBOUpdates int) float64 {
	return fastEvidenceCoverage(snapshot, minTrades, minBBOUpdates)
}

func evidenceCountCoverage(count, minimum int) float64 {
	if minimum <= 0 {
		return 1
	}
	return math.Max(0, math.Min(1, float64(count)/float64(minimum)))
}
