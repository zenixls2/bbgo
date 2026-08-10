package gammacapture

import "time"

var validatedHARVarianceHorizons = [...]time.Duration{15 * time.Minute, 30 * time.Minute}

// observeSideHARVarianceRisk shares the causal BBO stream already retained by
// the horizon model. Training is online and side-specific; no artifact or
// pre-trained coefficients are loaded.
func (m *MarketMakerHorizonModel) observeSideHARVarianceRisk(
	at time.Time,
	bid, ask float64,
	c MarketMakerConfig,
	gapBefore bool,
) {
	if m == nil || !c.MacroInventory.NoTradeRegion.FastVarianceRiskEnabled {
		return
	}
	if m.sideHARVarianceRisk == nil {
		m.sideHARVarianceRisk = make(map[time.Duration]*OnlineSideHARVarianceRisk, len(validatedHARVarianceHorizons))
		for _, horizon := range validatedHARVarianceHorizons {
			m.sideHARVarianceRisk[horizon] = NewOnlineSideHARVarianceRisk(horizon)
		}
	}
	for _, model := range m.sideHARVarianceRisk {
		model.ObserveBBO(at, bid, ask, gapBefore)
	}
}

// SideHARVarianceRisk returns the shortest statistically validated horizon
// that is not shorter than the active crossing window. Thus a 10m fast window
// conservatively uses 15m HAR risk, while 15m and 30m remain aligned exactly.
func (m *MarketMakerHorizonModel) SideHARVarianceRisk(window time.Duration) SideHARVarianceRiskDecision {
	if m == nil || len(m.sideHARVarianceRisk) == 0 {
		return SideHARVarianceRiskDecision{Reason: "side HAR variance risk disabled"}
	}
	selected := validatedHARVarianceHorizons[len(validatedHARVarianceHorizons)-1]
	for _, horizon := range validatedHARVarianceHorizons {
		if window <= horizon {
			selected = horizon
			break
		}
	}
	model := m.sideHARVarianceRisk[selected]
	if model == nil {
		return SideHARVarianceRiskDecision{Horizon: selected, Reason: "side HAR variance horizon unavailable"}
	}
	return model.Snapshot()
}

// rebuildSideHARVarianceRisk reconstructs recursive state from checkpoint BBO
// points. It is intentionally deterministic and uses the same observation
// method as live/replay startup warmup.
func (m *MarketMakerHorizonModel) rebuildSideHARVarianceRisk(c MarketMakerConfig) {
	if m == nil || !c.MacroInventory.NoTradeRegion.FastVarianceRiskEnabled {
		m.sideHARVarianceRisk = nil
		return
	}
	points := append([]MarketMakerHorizonPoint(nil), m.points...)
	m.sideHARVarianceRisk = nil
	for _, point := range points {
		m.observeSideHARVarianceRisk(point.At, point.bidPrice(), point.askPrice(), c, point.GapBefore)
	}
}
