package gammacapture

import "math"

// NormalFlowPressureConfig controls the conservative fallback for ordinary
// signed trade pressure. VolumeBalance historically emitted a signal only
// during a high-volume shock/rebalancing state; this component covers the
// otherwise silent, sufficiently observed flow state without becoming a hard
// side gate.
type NormalFlowPressureConfig struct {
	Enabled             bool    `json:"enabled" yaml:"enabled"`
	MinAbsImbalance     float64 `json:"minAbsImbalance" yaml:"minAbsImbalance"`
	MinTrades           int     `json:"minTrades" yaml:"minTrades"`
	PriorTrades         float64 `json:"priorTrades" yaml:"priorTrades"`
	MaximumSignalWeight float64 `json:"maximumSignalWeight" yaml:"maximumSignalWeight"`
}

type NormalFlowPressureInput struct {
	SignedTradeImbalance float64
	TradeCount           int
}

type NormalFlowPressureDecision struct {
	Ready           bool
	Applied         bool
	Reason          string
	Signal          float64
	ShrunkImbalance float64
	EvidenceWeight  float64
	SignedImbalance float64
	TradeCount      int
}

func finiteNormalFlowPressure(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clampNormalFlowPressure(value, lower, upper float64) float64 {
	return math.Max(lower, math.Min(upper, value))
}

func normalizeNormalFlowPressureConfig(config NormalFlowPressureConfig) NormalFlowPressureConfig {
	if config.MinAbsImbalance <= 0 || !finiteNormalFlowPressure(config.MinAbsImbalance) {
		config.MinAbsImbalance = 0.10
	}
	config.MinAbsImbalance = clampNormalFlowPressure(config.MinAbsImbalance, 0, 1)
	if config.MinTrades <= 0 {
		config.MinTrades = 20
	}
	if config.PriorTrades <= 0 || !finiteNormalFlowPressure(config.PriorTrades) {
		config.PriorTrades = 20
	}
	if config.MaximumSignalWeight <= 0 || !finiteNormalFlowPressure(config.MaximumSignalWeight) {
		config.MaximumSignalWeight = 0.35
	}
	config.MaximumSignalWeight = clampNormalFlowPressure(config.MaximumSignalWeight, 0, 1)
	return config
}

// EvaluateNormalFlowPressure returns a bounded auxiliary pressure signal. The
// raw imbalance is shrunk toward zero by n/(n+prior), so a small trade sample
// cannot create a full-strength quote shift. It is intentionally not a side
// admission or inventory target decision.
func EvaluateNormalFlowPressure(config NormalFlowPressureConfig, in NormalFlowPressureInput) NormalFlowPressureDecision {
	config = normalizeNormalFlowPressureConfig(config)
	decision := NormalFlowPressureDecision{
		Reason:          "normal-flow pressure is disabled",
		SignedImbalance: in.SignedTradeImbalance,
		TradeCount:      in.TradeCount,
	}
	if !config.Enabled {
		return decision
	}
	if in.TradeCount < config.MinTrades || !finiteNormalFlowPressure(in.SignedTradeImbalance) {
		decision.Reason = "normal-flow pressure has insufficient evidence"
		return decision
	}
	raw := clampNormalFlowPressure(in.SignedTradeImbalance, -1, 1)
	decision.EvidenceWeight = float64(in.TradeCount) /
		(float64(in.TradeCount) + config.PriorTrades)
	decision.ShrunkImbalance = raw * decision.EvidenceWeight
	decision.Ready = math.Abs(decision.ShrunkImbalance) >= config.MinAbsImbalance
	if !decision.Ready {
		decision.Reason = "normal-flow pressure is below the minimum imbalance"
		return decision
	}
	decision.Signal = clampNormalFlowPressure(
		decision.ShrunkImbalance, -config.MaximumSignalWeight, config.MaximumSignalWeight)
	decision.Applied = decision.Signal != 0
	decision.Reason = "normal-flow pressure fallback is active"
	return decision
}
