package gammacapture

import (
	"math"
	"sort"
)

// NormalFlowPressureDistributionVariant identifies an online, feature-only
// transformation of ordinary signed trade pressure. These variants are kept
// separate from the live NormalFlowPressure fallback until a causal screen
// shows both directional value and execution benefit.
type NormalFlowPressureDistributionVariant string

const (
	NormalFlowPressureDistributionCurrent      NormalFlowPressureDistributionVariant = "current"
	NormalFlowPressureDistributionWinsorized   NormalFlowPressureDistributionVariant = "winsorized"
	NormalFlowPressureDistributionRobustTanh   NormalFlowPressureDistributionVariant = "robust-tanh"
	NormalFlowPressureDistributionBalancedRank NormalFlowPressureDistributionVariant = "balanced-rank"
)

// NormalFlowPressureDistributionConfig controls the distribution correction.
// The state is updated only with observations available at the prediction
// timestamp; no future labels are used by this model.
type NormalFlowPressureDistributionConfig struct {
	Variant             NormalFlowPressureDistributionVariant
	MinAbsImbalance     float64
	MinTrades           int
	PriorTrades         float64
	MaximumSignalWeight float64
	EWMAAlpha           float64
	RankWindow          int
	WinsorCap           float64
	RobustScaleFloor    float64
}

type NormalFlowPressureDistributionDecision struct {
	Variant                NormalFlowPressureDistributionVariant
	Ready                  bool
	Applied                bool
	Reason                 string
	Raw                    float64
	Transformed            float64
	ShrunkImbalance        float64
	Signal                 float64
	EvidenceWeight         float64
	Center                 float64
	Scale                  float64
	HistoricalObservations int
	TradeCount             int
}

type NormalFlowPressureDistributionModel struct {
	config      NormalFlowPressureDistributionConfig
	mean        float64
	absDev      float64
	initialized bool
	history     []float64
}

func normalizeNormalFlowPressureDistributionConfig(config NormalFlowPressureDistributionConfig) NormalFlowPressureDistributionConfig {
	if config.Variant == "" {
		config.Variant = NormalFlowPressureDistributionCurrent
	}
	switch config.Variant {
	case NormalFlowPressureDistributionCurrent,
		NormalFlowPressureDistributionWinsorized,
		NormalFlowPressureDistributionRobustTanh,
		NormalFlowPressureDistributionBalancedRank:
	default:
		config.Variant = NormalFlowPressureDistributionCurrent
	}
	base := normalizeNormalFlowPressureConfig(NormalFlowPressureConfig{
		MinAbsImbalance:     config.MinAbsImbalance,
		MinTrades:           config.MinTrades,
		PriorTrades:         config.PriorTrades,
		MaximumSignalWeight: config.MaximumSignalWeight,
	})
	config.MinAbsImbalance = base.MinAbsImbalance
	config.MinTrades = base.MinTrades
	config.PriorTrades = base.PriorTrades
	config.MaximumSignalWeight = base.MaximumSignalWeight
	if config.EWMAAlpha <= 0 || config.EWMAAlpha > 1 || !finiteNormalFlowPressure(config.EWMAAlpha) {
		config.EWMAAlpha = 0.10
	}
	if config.RankWindow <= 0 {
		config.RankWindow = 96
	}
	if config.WinsorCap <= 0 || config.WinsorCap > 1 || !finiteNormalFlowPressure(config.WinsorCap) {
		config.WinsorCap = 0.25
	}
	if config.RobustScaleFloor <= 0 || config.RobustScaleFloor > 1 || !finiteNormalFlowPressure(config.RobustScaleFloor) {
		config.RobustScaleFloor = 0.05
	}
	return config
}

func NewNormalFlowPressureDistributionModel(config NormalFlowPressureDistributionConfig) *NormalFlowPressureDistributionModel {
	config = normalizeNormalFlowPressureDistributionConfig(config)
	return &NormalFlowPressureDistributionModel{
		config:  config,
		history: make([]float64, 0, config.RankWindow),
	}
}

func (m *NormalFlowPressureDistributionModel) Config() NormalFlowPressureDistributionConfig {
	if m == nil {
		return normalizeNormalFlowPressureDistributionConfig(NormalFlowPressureDistributionConfig{})
	}
	return m.config
}

// Evaluate applies the transform using state strictly before the current
// observation. Observe must be called after Evaluate for each timestamp.
func (m *NormalFlowPressureDistributionModel) Evaluate(raw float64, tradeCount int) NormalFlowPressureDistributionDecision {
	if m == nil {
		return NormalFlowPressureDistributionDecision{Reason: "distribution model is nil"}
	}
	d := NormalFlowPressureDistributionDecision{
		Variant:                m.config.Variant,
		Raw:                    raw,
		TradeCount:             tradeCount,
		Center:                 m.mean,
		Scale:                  math.Max(m.absDev, m.config.RobustScaleFloor),
		HistoricalObservations: len(m.history),
		Reason:                 "ordinary flow has insufficient evidence",
	}
	if tradeCount < m.config.MinTrades || !finiteNormalFlowPressure(raw) {
		return d
	}
	raw = clampNormalFlowPressure(raw, -1, 1)
	d.Raw = raw
	d.Transformed = m.transform(raw)
	d.EvidenceWeight = float64(tradeCount) / (float64(tradeCount) + m.config.PriorTrades)
	d.ShrunkImbalance = d.Transformed * d.EvidenceWeight
	if math.Abs(d.ShrunkImbalance) < m.config.MinAbsImbalance {
		d.Reason = "distribution-adjusted flow is below the minimum imbalance"
		return d
	}
	d.Ready = true
	d.Signal = clampNormalFlowPressure(d.ShrunkImbalance, -m.config.MaximumSignalWeight, m.config.MaximumSignalWeight)
	d.Applied = d.Signal != 0
	d.Reason = "distribution-adjusted ordinary flow is active"
	return d
}

// Observe advances only the feature-distribution state. It is deliberately
// separate from Evaluate so callers cannot accidentally train on a forward
// return before making the current decision.
func (m *NormalFlowPressureDistributionModel) Observe(raw float64) {
	if m == nil || !finiteNormalFlowPressure(raw) {
		return
	}
	raw = clampNormalFlowPressure(raw, -1, 1)
	if !m.initialized {
		m.mean = raw
		m.absDev = 0
		m.initialized = true
	} else {
		deviation := raw - m.mean
		m.mean += m.config.EWMAAlpha * deviation
		m.absDev += m.config.EWMAAlpha * (math.Abs(deviation) - m.absDev)
	}
	if m.config.Variant == NormalFlowPressureDistributionBalancedRank {
		m.history = append(m.history, raw)
		if len(m.history) > m.config.RankWindow {
			m.history = append([]float64(nil), m.history[len(m.history)-m.config.RankWindow:]...)
		}
	}
}

func (m *NormalFlowPressureDistributionModel) transform(raw float64) float64 {
	switch m.config.Variant {
	case NormalFlowPressureDistributionWinsorized:
		return clampNormalFlowPressure(raw, -m.config.WinsorCap, m.config.WinsorCap)
	case NormalFlowPressureDistributionRobustTanh:
		if !m.initialized {
			return raw
		}
		scale := math.Max(m.absDev, m.config.RobustScaleFloor)
		return math.Tanh((raw - m.mean) / (2 * scale))
	case NormalFlowPressureDistributionBalancedRank:
		if len(m.history) == 0 {
			return raw
		}
		ordered := append([]float64(nil), m.history...)
		sort.Float64s(ordered)
		less, equal := 0, 0
		for _, value := range ordered {
			if value < raw {
				less++
			} else if value == raw {
				equal++
			}
		}
		midRank := float64(less) + 0.5*float64(equal)
		return clampNormalFlowPressure(2*(midRank/float64(len(ordered)))-1, -1, 1)
	default:
		return raw
	}
}
