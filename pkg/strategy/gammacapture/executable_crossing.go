package gammacapture

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
)

// ExecutableCrossingModel confirms the common microprice first-passage path
// against prices that can actually execute inventory changes. An upward event
// is retained only from the best-bid lattice (a sell can execute there); a
// downward event is retained only from the best-ask lattice (a buy can execute
// there). Spread-only moves that push the two sides apart therefore do not
// manufacture directional Macro evidence.
type ExecutableCrossingModel struct {
	symbol    string
	barrier   BarrierConfig
	intensity IntensityConfig
	bidEngine *CrossingEngine
	askEngine *CrossingEngine
	model     *IntensityModel
}

func NewExecutableCrossingModel(symbol string, barrier BarrierConfig, intensity IntensityConfig) *ExecutableCrossingModel {
	m := &ExecutableCrossingModel{symbol: symbol, barrier: barrier, intensity: intensity}
	m.reset()
	return m
}

func (m *ExecutableCrossingModel) reset() {
	if m == nil {
		return
	}
	newEngine := func() *CrossingEngine {
		return NewCrossingEngine(m.barrier.Width, time.Duration(m.barrier.MinDwell), m.barrier.MaxCrossingsPerEvent)
	}
	m.bidEngine = newEngine()
	m.askEngine = newEngine()
	m.model = NewIntensityModel(m.intensity)
}

func (m *ExecutableCrossingModel) Observe(at time.Time, bid, ask float64, gapBefore bool) {
	if m == nil || at.IsZero() || bid <= 0 || ask < bid || m.model == nil {
		return
	}
	if !gapBefore && !m.model.lastObservation.IsZero() && at.Sub(m.model.lastObservation) >= marketMakerHorizonGapThreshold {
		gapBefore = true
	}
	m.model.Observe(at, gapBefore)
	if gapBefore {
		// A missing path cannot confirm an executable first passage. Start a new
		// pair of side lattices without creating events across the outage.
		m.bidEngine = NewCrossingEngine(m.barrier.Width, time.Duration(m.barrier.MinDwell), m.barrier.MaxCrossingsPerEvent)
		m.askEngine = NewCrossingEngine(m.barrier.Width, time.Duration(m.barrier.MinDwell), m.barrier.MaxCrossingsPerEvent)
		m.bidEngine.Reset(bid)
		m.askEngine.Reset(ask)
		return
	}
	for _, event := range m.bidEngine.Update(m.symbol, fixedpoint.NewFromFloat(bid), at, at, 0) {
		if event.Direction == DirectionUp {
			m.model.Update(event)
		}
	}
	for _, event := range m.askEngine.Update(m.symbol, fixedpoint.NewFromFloat(ask), at, at, 0) {
		if event.Direction == DirectionDown {
			m.model.Update(event)
		}
	}
}

func (m *ExecutableCrossingModel) Snapshot(now time.Time) ModelSnapshot {
	if m == nil || m.model == nil {
		return ModelSnapshot{Health: HealthInsufficient}
	}
	return m.model.Snapshot(now)
}

// Rebuild reconstructs the bounded executable model from checkpointed BBO
// points. This avoids a second persisted sufficient-statistics format while
// preserving exact live/replay behavior after a checkpoint restore.
func (m *ExecutableCrossingModel) Rebuild(points []MarketMakerHorizonPoint) {
	if m == nil {
		return
	}
	m.reset()
	for _, point := range points {
		bid, ask := point.bidPrice(), point.askPrice()
		if point.At.IsZero() || bid <= 0 || ask < bid {
			continue
		}
		m.Observe(point.At, bid, ask, point.GapBefore)
	}
}

// ConservativeConfirmedDirection returns an imprecise-posterior intersection.
// The two paths are highly correlated, so their event counts must not be added
// as independent likelihoods. If they disagree, the robust identified set
// contains zero and the direction is neutral; if they agree, the weaker
// posterior deviation bounds the common signal.
func ConservativeConfirmedDirection(microUp, microDown, executableUp, executableDown int, priorSamples float64) (micro, executable, confirmed float64) {
	priorSamples = math.Max(2, priorSamples)
	signed := func(up, down int) float64 {
		return (float64(up) - float64(down)) / (float64(up+down) + priorSamples)
	}
	micro = signed(microUp, microDown)
	executable = signed(executableUp, executableDown)
	if micro*executable <= 0 {
		return micro, executable, 0
	}
	confirmed = math.Copysign(math.Min(math.Abs(micro), math.Abs(executable)), micro)
	return micro, executable, confirmed
}
