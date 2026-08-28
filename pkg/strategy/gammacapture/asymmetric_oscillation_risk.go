package gammacapture

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// AsymmetricOscillationRiskConfig controls the inventory-risk alpha.
// It changes only the risk-aversion multiplier; it does not decide a side,
// price, quantity, or order gate. ShadowOnly keeps the causal diagnostics
// running without changing the unified quote objective.
type AsymmetricOscillationRiskConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	ShadowOnly bool `json:"shadowOnly" yaml:"shadowOnly"`
	// DirectionStrength controls the maximum response to an oscillating path.
	// A value of zero leaves the baseline risk aversion unchanged.
	DirectionStrength float64 `json:"directionStrength" yaml:"directionStrength"`
	// AsymmetryWeight controls how much the matured downside/upside variance
	// difference changes the path response.  It is bounded internally so a
	// noisy early estimate cannot reverse the requested risk ordering.
	AsymmetryWeight   float64 `json:"asymmetryWeight" yaml:"asymmetryWeight"`
	MinMultiplier     float64 `json:"minMultiplier" yaml:"minMultiplier"`
	MaxMultiplier     float64 `json:"maxMultiplier" yaml:"maxMultiplier"`
	EWMAAlpha         float64 `json:"ewmaAlpha" yaml:"ewmaAlpha"`
	PriorVarianceBps2 float64 `json:"priorVarianceBps2" yaml:"priorVarianceBps2"`
	MinSamples        int     `json:"minSamples" yaml:"minSamples"`
}

func (c AsymmetricOscillationRiskConfig) normalized() AsymmetricOscillationRiskConfig {
	if c.DirectionStrength < 0 || math.IsNaN(c.DirectionStrength) || math.IsInf(c.DirectionStrength, 0) {
		c.DirectionStrength = 0
	}
	if c.DirectionStrength > 2 {
		c.DirectionStrength = 2
	}
	if c.AsymmetryWeight < 0 || math.IsNaN(c.AsymmetryWeight) || math.IsInf(c.AsymmetryWeight, 0) {
		c.AsymmetryWeight = 0
	}
	if c.AsymmetryWeight > 1 {
		c.AsymmetryWeight = 1
	}
	if c.MinMultiplier <= 0 || math.IsNaN(c.MinMultiplier) || math.IsInf(c.MinMultiplier, 0) {
		c.MinMultiplier = 0.65
	}
	if c.MaxMultiplier < c.MinMultiplier || math.IsNaN(c.MaxMultiplier) || math.IsInf(c.MaxMultiplier, 0) {
		c.MaxMultiplier = 1.75
	}
	if c.EWMAAlpha <= 0 || c.EWMAAlpha > 1 || math.IsNaN(c.EWMAAlpha) || math.IsInf(c.EWMAAlpha, 0) {
		c.EWMAAlpha = 0.1
	}
	if c.PriorVarianceBps2 <= 0 || math.IsNaN(c.PriorVarianceBps2) || math.IsInf(c.PriorVarianceBps2, 0) {
		c.PriorVarianceBps2 = 25
	}
	if c.MinSamples < 1 {
		c.MinSamples = 12
	}
	return c
}

// AsymmetricOscillationRiskFeatures describe only information available at the
// prediction timestamp. TotalVariationBps is the sum of absolute executable
// log returns over the selected Fast window; NetReturnBps is its signed
// endpoint return. ScaleBps should be a causal volatility/spread scale.
type AsymmetricOscillationRiskFeatures struct {
	NetReturnBps      float64
	TotalVariationBps float64
	ScaleBps          float64
	SpreadBps         float64
}

// AsymmetricOscillationRiskStats are updated only with matured terminal
// executable-bid labels. Variances are EWMA second moments, so the model does
// not need a stored historical sample vector.
type AsymmetricOscillationRiskStats struct {
	UpVarianceBps2   float64
	DownVarianceBps2 float64
	UpSamples        int
	DownSamples      int
}

type AsymmetricOscillationRiskDecision struct {
	Enabled          bool
	RiskMultiplier   float64
	OscillationScore float64
	AsymmetryScore   float64
	AsymmetryReady   bool
	UpVarianceBps2   float64
	DownVarianceBps2 float64
	UpSamples        int
	DownSamples      int
	Reason           string
}

func finiteAsymmetricRisk(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// EvaluateAsymmetricOscillationRisk maps a signed oscillation into a bounded
// risk-aversion multiplier. Let O=1-|R|/TV be the path's oscillation fraction
// and D=tanh(R/s) its signed endpoint direction. The score is O*D. With the
// usual downside-volatility asymmetry, a positive oscillating path reduces
// inventory risk aversion and a negative oscillating path increases it:
//
//	lambda_t/lambda_0 = clip(exp(-eta * O * D * (1+w*A)), m_min, m_max),
//	A=(sigma_down^2-sigma_up^2)/(sigma_down^2+sigma_up^2).
//
// A<0 (the inverted cryptocurrency asymmetry documented in the literature)
// attenuates the response rather than silently assuming that every symbol has
// an equity-style leverage effect.
func EvaluateAsymmetricOscillationRisk(
	cfg AsymmetricOscillationRiskConfig,
	features AsymmetricOscillationRiskFeatures,
	stats AsymmetricOscillationRiskStats,
) AsymmetricOscillationRiskDecision {
	cfg = cfg.normalized()
	d := AsymmetricOscillationRiskDecision{
		Enabled:          true,
		RiskMultiplier:   1,
		UpVarianceBps2:   stats.UpVarianceBps2,
		DownVarianceBps2: stats.DownVarianceBps2,
		UpSamples:        stats.UpSamples,
		DownSamples:      stats.DownSamples,
		Reason:           "neutral path or insufficient oscillation",
	}
	if !finiteAsymmetricRisk(features.NetReturnBps) ||
		!finiteAsymmetricRisk(features.TotalVariationBps) ||
		!finiteAsymmetricRisk(features.ScaleBps) ||
		!finiteAsymmetricRisk(features.SpreadBps) ||
		features.TotalVariationBps < 0 || features.ScaleBps < 0 || features.SpreadBps < 0 ||
		!finiteAsymmetricRisk(stats.UpVarianceBps2) || !finiteAsymmetricRisk(stats.DownVarianceBps2) ||
		stats.UpVarianceBps2 < 0 || stats.DownVarianceBps2 < 0 {
		d.Enabled = false
		d.Reason = "non-finite or invalid risk input"
		return d
	}
	totalVariation := math.Max(features.TotalVariationBps, math.Abs(features.NetReturnBps))
	if totalVariation <= 1e-12 || math.Abs(features.NetReturnBps) <= 1e-12 {
		return d
	}
	oscillation := 1 - math.Abs(features.NetReturnBps)/totalVariation
	if oscillation <= 1e-12 {
		return d
	}
	oscillation = math.Max(0, math.Min(1, oscillation))
	scale := math.Max(1, features.ScaleBps+features.SpreadBps)
	direction := math.Tanh(features.NetReturnBps / scale)
	d.OscillationScore = oscillation * direction

	upVariance := math.Max(cfg.PriorVarianceBps2, stats.UpVarianceBps2)
	downVariance := math.Max(cfg.PriorVarianceBps2, stats.DownVarianceBps2)
	if stats.UpSamples >= cfg.MinSamples && stats.DownSamples >= cfg.MinSamples {
		d.AsymmetryReady = true
	}
	if d.AsymmetryReady {
		d.AsymmetryScore = (downVariance - upVariance) / (downVariance + upVariance)
	}
	// Keep the learned asymmetry from reversing the baseline path ordering;
	// an inverted asymmetry attenuates the response and is visible in logs.
	amplifier := 1 + cfg.AsymmetryWeight*d.AsymmetryScore
	amplifier = math.Max(0.25, math.Min(1.75, amplifier))
	multiplier := math.Exp(-cfg.DirectionStrength * d.OscillationScore * amplifier)
	d.RiskMultiplier = math.Max(cfg.MinMultiplier, math.Min(cfg.MaxMultiplier, multiplier))
	if d.OscillationScore > 0 {
		d.Reason = "oscillating upward path reduces inventory risk aversion"
	} else {
		d.Reason = "oscillating downward path increases inventory risk aversion"
	}
	return d
}

type asymmetricOscillationRiskPending struct {
	MaturesAt time.Time
	Horizon   time.Duration
	StartBid  float64
	Regime    float64
}

type asymmetricOscillationRiskHorizonState struct {
	Stats   AsymmetricOscillationRiskStats
	Pending *asymmetricOscillationRiskPending
}

type asymmetricOscillationRiskHorizonCheckpoint struct {
	Stats            AsymmetricOscillationRiskStats `json:"stats"`
	PendingMaturesAt time.Time                      `json:"pendingMaturesAt,omitempty"`
	PendingHorizon   time.Duration                  `json:"pendingHorizon,omitempty"`
	PendingStartBid  float64                        `json:"pendingStartBid,omitempty"`
	PendingRegime    float64                        `json:"pendingRegime,omitempty"`
}

type asymmetricOscillationRiskCheckpoint struct {
	// Horizon-specific state prevents terminal variance labels from 10m, 15m,
	// and 30m paths being pooled into one EWMA. ModelCheckpoint's version bump
	// rejects the former single-state representation instead of restoring it
	// with incompatible statistics.
	Horizons map[string]asymmetricOscillationRiskHorizonCheckpoint `json:"horizons,omitempty"`
}

// AsymmetricOscillationRiskModel is an online prequential wrapper around the
// pure evaluator. It keeps one non-overlapping pending label and one matured
// variance state per horizon. The caller must call UpdateLabel only when the
// future executable bid is observed at or after the horizon; no future value
// is used by Predict.
type AsymmetricOscillationRiskModel struct {
	Config   AsymmetricOscillationRiskConfig
	horizons map[time.Duration]*asymmetricOscillationRiskHorizonState
}

func NewAsymmetricOscillationRiskModel(cfg AsymmetricOscillationRiskConfig) *AsymmetricOscillationRiskModel {
	cfg = cfg.normalized()
	return &AsymmetricOscillationRiskModel{Config: cfg, horizons: make(map[time.Duration]*asymmetricOscillationRiskHorizonState)}
}

func normalizeAsymmetricRiskHorizon(horizon time.Duration) time.Duration {
	if horizon <= 0 {
		return 0
	}
	return horizon.Round(time.Second)
}

func (m *AsymmetricOscillationRiskModel) horizonState(horizon time.Duration) *asymmetricOscillationRiskHorizonState {
	if m == nil {
		return nil
	}
	if m.horizons == nil {
		m.horizons = make(map[time.Duration]*asymmetricOscillationRiskHorizonState)
	}
	horizon = normalizeAsymmetricRiskHorizon(horizon)
	if horizon <= 0 {
		return nil
	}
	state := m.horizons[horizon]
	if state == nil {
		state = &asymmetricOscillationRiskHorizonState{}
		m.horizons[horizon] = state
	}
	return state
}

// StatsForHorizon returns only the matured state for the requested horizon.
// It is intentionally read-only so diagnostics cannot mutate the learner or
// accidentally pool windows.
func (m *AsymmetricOscillationRiskModel) StatsForHorizon(horizon time.Duration) (AsymmetricOscillationRiskStats, bool) {
	if m == nil {
		return AsymmetricOscillationRiskStats{}, false
	}
	state := m.horizons[normalizeAsymmetricRiskHorizon(horizon)]
	if state == nil {
		return AsymmetricOscillationRiskStats{}, false
	}
	return state.Stats, true
}

func (m *AsymmetricOscillationRiskModel) Predict(
	now time.Time, bestBid float64, horizon time.Duration,
	features AsymmetricOscillationRiskFeatures,
) AsymmetricOscillationRiskDecision {
	if m == nil {
		return AsymmetricOscillationRiskDecision{Reason: "model unavailable"}
	}
	horizon = normalizeAsymmetricRiskHorizon(horizon)
	state := m.horizonState(horizon)
	if state == nil {
		return AsymmetricOscillationRiskDecision{Reason: "asymmetric oscillation risk horizon unavailable"}
	}
	d := EvaluateAsymmetricOscillationRisk(m.Config, features, state.Stats)
	// The pure evaluator remains useful for mathematical diagnostics before
	// maturity. The online model must not let that provisional path-direction
	// score alter Fast risk aversion until both directional variance posteriors
	// for this exact horizon have matured.
	if !d.AsymmetryReady && math.Abs(d.OscillationScore) > 1e-12 {
		d.RiskMultiplier = 1
		d.Reason = "asymmetry statistics not mature"
	}
	if now.IsZero() || bestBid <= 0 || horizon <= 0 || !finiteAsymmetricRisk(bestBid) ||
		!finiteAsymmetricRisk(d.OscillationScore) {
		return d
	}
	// Do not overlap labels. A caller can mature the existing prediction and
	// start a new one on the next eligible decision timestamp.
	if state.Pending == nil && math.Abs(d.OscillationScore) > 1e-12 {
		state.Pending = &asymmetricOscillationRiskPending{
			MaturesAt: now.Add(horizon), Horizon: horizon,
			StartBid: bestBid, Regime: d.OscillationScore,
		}
	}
	return d
}

// UpdateLabel updates the EWMA only after maturity. A delayed observation past
// the bounded event lag is discarded; existing statistics remain valid.
func (m *AsymmetricOscillationRiskModel) UpdateLabel(now time.Time, bestBid float64) bool {
	if m == nil || now.IsZero() || bestBid <= 0 || !finiteAsymmetricRisk(bestBid) {
		return false
	}
	updated := false
	alpha := m.Config.normalized().EWMAAlpha
	for _, state := range m.horizons {
		if state == nil || state.Pending == nil || now.Before(state.Pending.MaturesAt) {
			continue
		}
		pending := state.Pending
		state.Pending = nil
		maxLag := pending.Horizon / 10
		if maxLag > 2*time.Minute {
			maxLag = 2 * time.Minute
		}
		if maxLag < time.Second {
			maxLag = time.Second
		}
		if now.Sub(pending.MaturesAt) > maxLag {
			continue
		}
		returnBps := math.Log(bestBid/pending.StartBid) * 10_000
		if !finiteAsymmetricRisk(returnBps) {
			continue
		}
		if pending.Regime > 0 {
			state.Stats.UpVarianceBps2 = (1-alpha)*state.Stats.UpVarianceBps2 + alpha*returnBps*returnBps
			state.Stats.UpSamples++
			updated = true
		} else if pending.Regime < 0 {
			state.Stats.DownVarianceBps2 = (1-alpha)*state.Stats.DownVarianceBps2 + alpha*returnBps*returnBps
			state.Stats.DownSamples++
			updated = true
		}
	}
	return updated
}

// ResetPending invalidates only the unlabelled forecast after a market-data
// gap or restart; matured EWMA risk statistics are retained.
func (m *AsymmetricOscillationRiskModel) ResetPending() {
	if m != nil {
		for _, state := range m.horizons {
			if state != nil {
				state.Pending = nil
			}
		}
	}
}

func (m *AsymmetricOscillationRiskModel) checkpoint() *asymmetricOscillationRiskCheckpoint {
	if m == nil {
		return nil
	}
	checkpoint := &asymmetricOscillationRiskCheckpoint{
		Horizons: make(map[string]asymmetricOscillationRiskHorizonCheckpoint, len(m.horizons)),
	}
	for horizon, state := range m.horizons {
		if state == nil {
			continue
		}
		entry := asymmetricOscillationRiskHorizonCheckpoint{Stats: state.Stats}
		if state.Pending != nil {
			entry.PendingMaturesAt = state.Pending.MaturesAt
			entry.PendingHorizon = state.Pending.Horizon
			entry.PendingStartBid = state.Pending.StartBid
			entry.PendingRegime = state.Pending.Regime
		}
		checkpoint.Horizons[checkpointWindowKey(horizon)] = entry
	}
	return checkpoint
}

func (m *AsymmetricOscillationRiskModel) restore(checkpoint *asymmetricOscillationRiskCheckpoint) error {
	if m == nil || checkpoint == nil {
		return fmt.Errorf("asymmetric oscillation risk checkpoint is missing")
	}
	m.horizons = make(map[time.Duration]*asymmetricOscillationRiskHorizonState, len(checkpoint.Horizons))
	for key, entry := range checkpoint.Horizons {
		seconds, err := strconv.ParseInt(key, 10, 64)
		if err != nil || seconds <= 0 {
			return fmt.Errorf("asymmetric oscillation risk checkpoint horizon %q is invalid", key)
		}
		horizon := time.Duration(seconds) * time.Second
		if horizon <= 0 || int64(horizon/time.Second) != seconds {
			return fmt.Errorf("asymmetric oscillation risk checkpoint horizon %q overflows duration", key)
		}
		if !finiteAsymmetricRisk(entry.Stats.UpVarianceBps2) ||
			!finiteAsymmetricRisk(entry.Stats.DownVarianceBps2) ||
			entry.Stats.UpVarianceBps2 < 0 || entry.Stats.DownVarianceBps2 < 0 ||
			entry.Stats.UpSamples < 0 || entry.Stats.DownSamples < 0 {
			return fmt.Errorf("asymmetric oscillation risk checkpoint horizon %q has invalid statistics", key)
		}
		state := &asymmetricOscillationRiskHorizonState{Stats: entry.Stats}
		pendingPresent := !entry.PendingMaturesAt.IsZero() || entry.PendingHorizon != 0 ||
			entry.PendingStartBid != 0 || entry.PendingRegime != 0
		if pendingPresent {
			if entry.PendingMaturesAt.IsZero() || entry.PendingHorizon <= 0 ||
				!finiteAsymmetricRisk(entry.PendingStartBid) || entry.PendingStartBid <= 0 ||
				!finiteAsymmetricRisk(entry.PendingRegime) || math.Abs(entry.PendingRegime) <= 1e-12 {
				return fmt.Errorf("asymmetric oscillation risk checkpoint horizon %q has invalid pending label", key)
			}
			pendingHorizon := normalizeAsymmetricRiskHorizon(entry.PendingHorizon)
			if pendingHorizon != horizon {
				return fmt.Errorf("asymmetric oscillation risk checkpoint horizon mismatch: key=%s pending=%s", key, pendingHorizon)
			}
			state.Pending = &asymmetricOscillationRiskPending{
				MaturesAt: entry.PendingMaturesAt, Horizon: pendingHorizon,
				StartBid: entry.PendingStartBid, Regime: entry.PendingRegime,
			}
		}
		m.horizons[horizon] = state
	}
	return nil
}
