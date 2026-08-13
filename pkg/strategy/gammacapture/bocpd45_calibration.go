package gammacapture

import (
	"fmt"
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// BOCPD45Config controls the same-symbol, executable-BBO direction auxiliary.
// Calibration is strictly prequential: a forecast is fitted only after its
// future label has matured on the first BBO at or after Horizon.
type BOCPD45Config struct {
	Enabled           bool           `json:"enabled" yaml:"enabled"`
	Calibration       string         `json:"calibration" yaml:"calibration"`
	Horizon           types.Duration `json:"horizon" yaml:"horizon"`
	CalibrationWindow types.Duration `json:"calibrationWindow" yaml:"calibrationWindow"`
	MinimumChanges    int            `json:"minimumChanges" yaml:"minimumChanges"`
	MinimumSamples    int            `json:"minimumSamples" yaml:"minimumSamples"`
	MaximumStates     int            `json:"maximumStates" yaml:"maximumStates"`
	RefitEvery        int            `json:"refitEvery" yaml:"refitEvery"`
	PriorStrength     float64        `json:"priorStrength" yaml:"priorStrength"`
}

func (c *BOCPD45Config) setDefaults() {
	if c.Calibration == "" {
		c.Calibration = "platt"
	}
	if c.Horizon <= 0 {
		c.Horizon = types.Duration(45 * time.Second)
	}
	if c.CalibrationWindow <= 0 {
		c.CalibrationWindow = types.Duration(6 * time.Hour)
	}
	if c.MinimumChanges <= 0 {
		c.MinimumChanges = 8
	}
	if c.MinimumSamples <= 0 {
		c.MinimumSamples = 32
	}
	if c.MaximumStates <= 0 {
		c.MaximumStates = 128
	}
	if c.RefitEvery <= 0 {
		c.RefitEvery = 8
	}
	if c.PriorStrength <= 0 {
		c.PriorStrength = 8
	}
}

func (c BOCPD45Config) validate() error {
	c.setDefaults()
	if c.Calibration != "platt" && c.Calibration != "raw" {
		return fmt.Errorf("marketMaker.bocpd45.calibration must be platt or raw")
	}
	if c.Horizon <= 0 || c.CalibrationWindow < c.Horizon || c.MinimumChanges <= 0 ||
		c.MinimumSamples <= 0 || c.MaximumStates <= 0 || c.RefitEvery <= 0 || c.PriorStrength <= 0 {
		return fmt.Errorf("marketMaker.bocpd45 timing, sample, state, refit, and prior parameters must be positive")
	}
	return nil
}

type bocpd45Run struct {
	Probability float64 `json:"probability"`
	Up          float64 `json:"up"`
	Down        float64 `json:"down"`
}

type bocpd45Side struct {
	States     []bocpd45Run `json:"states,omitempty"`
	LastChange time.Time    `json:"lastChange,omitempty"`
	Changes    int          `json:"changes"`
}

type bocpd45CalibrationSample struct {
	Probability float64 `json:"probability"`
	Label       float64 `json:"label"`
}

type bocpd45PendingLabel struct {
	MaturesAt      time.Time `json:"maturesAt"`
	StartBid       float64   `json:"startBid"`
	StartAsk       float64   `json:"startAsk"`
	RawProbability float64   `json:"rawProbability"`
}

type bocpd45Checkpoint struct {
	LastBid, LastAsk float64                    `json:"lastBid,omitempty"`
	Bid, Ask         bocpd45Side                `json:"bid"`
	Samples          []bocpd45CalibrationSample `json:"samples,omitempty"`
	Updates          int                        `json:"updates"`
	Pending          *bocpd45PendingLabel       `json:"pending,omitempty"`
	NextAnchor       time.Time                  `json:"nextAnchor,omitempty"`
	Matured          int                        `json:"matured"`
}

// BOCPD45Snapshot separates the uncalibrated posterior from the probability
// actually supplied to the Fast-direction mixture.
type BOCPD45Snapshot struct {
	Enabled, Ready, CalibrationReady bool
	Calibration                      string
	RawUpProbability                 float64
	UpProbability                    float64
	Direction, Confidence            float64
	ChangeProbability                float64
	PosteriorVariance                float64
	BidChanges, AskChanges           int
	CalibrationSamples               int
	MaturedLabels                    int
	PendingMaturesAt                 time.Time
}

// BOCPD45Model combines a short-run BOCPD sign posterior and a rolling Platt
// map. It is independent of inventory targeting and order sizing.
type BOCPD45Model struct {
	config           BOCPD45Config
	lastBid, lastAsk float64
	bid, ask         bocpd45Side
	samples          []bocpd45CalibrationSample
	updates          int
	calibrationReady bool
	theta            [2]float64
	pending          *bocpd45PendingLabel
	nextAnchor       time.Time
	matured          int
}

func NewBOCPD45Model(config BOCPD45Config) *BOCPD45Model {
	config.setDefaults()
	return &BOCPD45Model{config: config, theta: [2]float64{0, 1}}
}

func (m *BOCPD45Model) resetPath() {
	m.lastBid, m.lastAsk = 0, 0
	m.bid, m.ask = bocpd45Side{}, bocpd45Side{}
	m.pending = nil
	m.nextAnchor = time.Time{}
}

func (m *BOCPD45Model) Observe(at time.Time, bid, ask float64, gap bool) {
	if m == nil || at.IsZero() || bid <= 0 || ask < bid {
		return
	}
	if gap {
		m.resetPath()
		m.nextAnchor = at
	}
	if m.lastBid > 0 && bid != m.lastBid {
		m.bid.observe(at, bid > m.lastBid, m.config)
	}
	if m.lastAsk > 0 && ask != m.lastAsk {
		m.ask.observe(at, ask > m.lastAsk, m.config)
	}
	m.lastBid, m.lastAsk = bid, ask

	// Mature first. The new anchor below may use this label; the stored forecast
	// which produced it never can.
	if m.pending != nil && !at.Before(m.pending.MaturesAt) {
		pending := m.pending
		m.pending = nil
		move := 0.5 * (math.Log(ask/pending.StartAsk) + math.Log(bid/pending.StartBid))
		if move != 0 {
			label := 0.0
			if move > 0 {
				label = 1
			}
			m.updateCalibration(pending.RawProbability, label)
			m.matured++
		}
	}
	raw := m.rawSnapshot()
	if m.pending == nil && raw.Ready && (m.nextAnchor.IsZero() || !at.Before(m.nextAnchor)) {
		m.pending = &bocpd45PendingLabel{
			MaturesAt: at.Add(time.Duration(m.config.Horizon)), StartBid: bid, StartAsk: ask,
			RawProbability: raw.RawUpProbability,
		}
		m.nextAnchor = at.Add(time.Duration(m.config.Horizon))
	}
}

func (s *bocpd45Side) observe(at time.Time, up bool, config BOCPD45Config) {
	if !s.LastChange.IsZero() && !at.After(s.LastChange) {
		return
	}
	observation := 0.0
	if up {
		observation = 1
	}
	if len(s.States) == 0 {
		s.States = []bocpd45Run{{Probability: 1, Up: 1 + observation, Down: 2 - observation}}
		s.LastChange, s.Changes = at, 1
		return
	}
	hazard := 1 - math.Exp(-float64(at.Sub(s.LastChange))/float64(config.Horizon))
	hazard = math.Max(1e-9, math.Min(1-1e-9, hazard))
	next := make([]bocpd45Run, 1, min(config.MaximumStates, len(s.States)+1))
	for _, state := range s.States {
		predictive := state.Up / (state.Up + state.Down)
		if !up {
			predictive = 1 - predictive
		}
		next[0].Probability += state.Probability * hazard * 0.5
		if len(next) < config.MaximumStates {
			next = append(next, bocpd45Run{
				Probability: state.Probability * (1 - hazard) * predictive,
				Up:          state.Up + observation, Down: state.Down + 1 - observation,
			})
		}
	}
	next[0].Up, next[0].Down = 1+observation, 2-observation
	total := 0.0
	for _, state := range next {
		total += state.Probability
	}
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		*s = bocpd45Side{}
		return
	}
	for i := range next {
		next[i].Probability /= total
	}
	s.States, s.LastChange = next, at
	s.Changes++
}

type bocpd45SideSnapshot struct {
	Ready                  bool
	Mean, Variance, Change float64
	Changes                int
}

func (s *bocpd45Side) snapshot(minimumChanges int) bocpd45SideSnapshot {
	out := bocpd45SideSnapshot{Changes: s.Changes}
	second := 0.0
	for i, state := range s.States {
		total := state.Up + state.Down
		mean := state.Up / total
		out.Mean += state.Probability * mean
		second += state.Probability * state.Up * (state.Up + 1) / (total * (total + 1))
		if i == 0 {
			out.Change = state.Probability
		}
	}
	out.Variance = math.Max(0, second-out.Mean*out.Mean)
	out.Ready = out.Changes >= minimumChanges && len(s.States) > 0
	if len(s.States) == 0 {
		out.Mean = 0.5
	}
	return out
}

func (m *BOCPD45Model) rawSnapshot() BOCPD45Snapshot {
	out := BOCPD45Snapshot{Enabled: m != nil, Calibration: "raw", RawUpProbability: 0.5, UpProbability: 0.5}
	if m == nil {
		return out
	}
	out.Calibration = m.config.Calibration
	bid, ask := m.bid.snapshot(m.config.MinimumChanges), m.ask.snapshot(m.config.MinimumChanges)
	out.BidChanges, out.AskChanges = bid.Changes, ask.Changes
	if !bid.Ready || !ask.Ready {
		return out
	}
	out.Ready = true
	out.RawUpProbability = 0.5 * (bid.Mean + ask.Mean)
	out.UpProbability = out.RawUpProbability
	out.Direction = math.Max(-1, math.Min(1, 2*out.UpProbability-1))
	out.PosteriorVariance = 0.5 * (bid.Variance + ask.Variance)
	out.ChangeProbability = 0.5 * (bid.Change + ask.Change)
	bernoulliVariance := out.RawUpProbability * (1 - out.RawUpProbability)
	if bernoulliVariance > 1e-12 {
		precision := 1 - math.Min(1, out.PosteriorVariance/bernoulliVariance)
		out.Confidence = math.Max(0, math.Min(1, (1-out.ChangeProbability)*precision))
	}
	return out
}

func (m *BOCPD45Model) Snapshot() BOCPD45Snapshot {
	out := m.rawSnapshot()
	if m == nil {
		return out
	}
	out.CalibrationReady = m.calibrationReady
	out.CalibrationSamples = len(m.samples)
	out.MaturedLabels = m.matured
	if m.pending != nil {
		out.PendingMaturesAt = m.pending.MaturesAt
	}
	if out.Ready && m.config.Calibration == "platt" && m.calibrationReady {
		out.UpProbability = m.predictCalibration(out.RawUpProbability)
		out.Direction = math.Max(-1, math.Min(1, 2*out.UpProbability-1))
	}
	return out
}

func (m *BOCPD45Model) updateCalibration(probability, label float64) {
	if m.config.Calibration != "platt" || (label != 0 && label != 1) {
		return
	}
	m.samples = append(m.samples, bocpd45CalibrationSample{Probability: clampBOCPD45Probability(probability), Label: label})
	maximum := int(time.Duration(m.config.CalibrationWindow) / time.Duration(m.config.Horizon))
	if maximum < m.config.MinimumSamples {
		maximum = m.config.MinimumSamples
	}
	if len(m.samples) > maximum {
		copy(m.samples, m.samples[len(m.samples)-maximum:])
		m.samples = m.samples[:maximum]
	}
	m.updates++
	if len(m.samples) >= m.config.MinimumSamples && (!m.calibrationReady || m.updates%m.config.RefitEvery == 0) {
		m.refitCalibration()
	}
}

func (m *BOCPD45Model) predictCalibration(probability float64) float64 {
	p := clampBOCPD45Probability(probability)
	logit := math.Log(p / (1 - p))
	return clampBOCPD45Probability(stableLogistic(m.theta[0] + m.theta[1]*logit))
}

func (m *BOCPD45Model) refitCalibration() {
	theta := [2]float64{0, 1}
	prior := theta
	for iteration := 0; iteration < 12; iteration++ {
		gradient := [2]float64{
			m.config.PriorStrength * (theta[0] - prior[0]),
			m.config.PriorStrength * (theta[1] - prior[1]),
		}
		hessian := [2][2]float64{{m.config.PriorStrength, 0}, {0, m.config.PriorStrength}}
		for _, sample := range m.samples {
			p := clampBOCPD45Probability(sample.Probability)
			x := [2]float64{1, math.Log(p / (1 - p))}
			q := stableLogistic(theta[0] + theta[1]*x[1])
			weight := math.Max(1e-6, q*(1-q))
			for i := 0; i < 2; i++ {
				gradient[i] += (q - sample.Label) * x[i]
				for j := 0; j < 2; j++ {
					hessian[i][j] += weight * x[i] * x[j]
				}
			}
		}
		determinant := hessian[0][0]*hessian[1][1] - hessian[0][1]*hessian[1][0]
		if math.Abs(determinant) < 1e-12 {
			return
		}
		step := [2]float64{
			(gradient[0]*hessian[1][1] - gradient[1]*hessian[0][1]) / determinant,
			(hessian[0][0]*gradient[1] - hessian[1][0]*gradient[0]) / determinant,
		}
		theta[0] -= step[0]
		theta[1] -= step[1]
		if math.Max(math.Abs(step[0]), math.Abs(step[1])) < 1e-8 {
			break
		}
	}
	if math.IsNaN(theta[0]) || math.IsNaN(theta[1]) || math.IsInf(theta[0], 0) || math.IsInf(theta[1], 0) {
		return
	}
	m.theta, m.calibrationReady = theta, true
}

func stableLogistic(value float64) float64 {
	if value >= 0 {
		z := math.Exp(-value)
		return 1 / (1 + z)
	}
	z := math.Exp(value)
	return z / (1 + z)
}

func clampBOCPD45Probability(value float64) float64 {
	return math.Max(1e-9, math.Min(1-1e-9, value))
}

func (m *BOCPD45Model) checkpoint() *bocpd45Checkpoint {
	if m == nil {
		return nil
	}
	state := &bocpd45Checkpoint{
		LastBid: m.lastBid, LastAsk: m.lastAsk, Bid: m.bid, Ask: m.ask,
		Samples: append([]bocpd45CalibrationSample(nil), m.samples...), Updates: m.updates,
		NextAnchor: m.nextAnchor, Matured: m.matured,
	}
	if m.pending != nil {
		pending := *m.pending
		state.Pending = &pending
	}
	return state
}

func (m *BOCPD45Model) restore(state *bocpd45Checkpoint) error {
	if m == nil || state == nil {
		return fmt.Errorf("BOCPD45 checkpoint is missing")
	}
	m.lastBid, m.lastAsk = state.LastBid, state.LastAsk
	m.bid, m.ask = state.Bid, state.Ask
	m.samples = append([]bocpd45CalibrationSample(nil), state.Samples...)
	m.updates, m.nextAnchor, m.matured = state.Updates, state.NextAnchor, state.Matured
	if state.Pending != nil {
		pending := *state.Pending
		m.pending = &pending
	}
	if len(m.samples) >= m.config.MinimumSamples && m.config.Calibration == "platt" {
		m.refitCalibration()
	}
	return nil
}
