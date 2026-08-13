package main

import (
	"math"
	"time"
)

const (
	bocpd45ExpectedRunLength = 45 * time.Second
	bocpd45MinimumChanges    = 8
	bocpd45MaximumStates     = 128
)

type bocpd45Run struct {
	probability float64
	up, down    float64
}

type bocpd45Side struct {
	states     []bocpd45Run
	lastChange time.Time
	changes    int
}

type bocpd45DirectionModel struct {
	lastBid, lastAsk float64
	bid, ask         bocpd45Side
}

type bocpd45DirectionSnapshot struct {
	ready                  bool
	upProbability          float64
	direction, confidence  float64
	bidChanges, askChanges int
	changeProbability      float64
	posteriorVariance      float64
}

func (m *bocpd45DirectionModel) reset() { *m = bocpd45DirectionModel{} }

func (m *bocpd45DirectionModel) observe(at time.Time, bid, ask float64, gap bool) {
	if at.IsZero() || bid <= 0 || ask < bid {
		return
	}
	if gap {
		m.reset()
	}
	if m.lastBid > 0 && bid != m.lastBid {
		m.bid.observe(at, bid > m.lastBid)
	}
	if m.lastAsk > 0 && ask != m.lastAsk {
		m.ask.observe(at, ask > m.lastAsk)
	}
	m.lastBid, m.lastAsk = bid, ask
}

func (s *bocpd45Side) observe(at time.Time, up bool) {
	if !s.lastChange.IsZero() && !at.After(s.lastChange) {
		return
	}
	observation := 0.0
	if up {
		observation = 1
	}
	if len(s.states) == 0 {
		s.states = []bocpd45Run{{probability: 1, up: 1 + observation, down: 2 - observation}}
		s.lastChange, s.changes = at, 1
		return
	}
	hazard := 1 - math.Exp(-float64(at.Sub(s.lastChange))/float64(bocpd45ExpectedRunLength))
	hazard = math.Max(1e-9, math.Min(1-1e-9, hazard))
	next := make([]bocpd45Run, 1, minInt(bocpd45MaximumStates, len(s.states)+1))
	for _, state := range s.states {
		predictive := state.up / (state.up + state.down)
		if !up {
			predictive = 1 - predictive
		}
		next[0].probability += state.probability * hazard * 0.5
		if len(next) < bocpd45MaximumStates {
			next = append(next, bocpd45Run{
				probability: state.probability * (1 - hazard) * predictive,
				up:          state.up + observation, down: state.down + 1 - observation,
			})
		}
	}
	next[0].up, next[0].down = 1+observation, 2-observation
	total := 0.0
	for _, state := range next {
		total += state.probability
	}
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		*s = bocpd45Side{}
		return
	}
	for i := range next {
		next[i].probability /= total
	}
	s.states, s.lastChange = next, at
	s.changes++
}

type bocpd45SideSnapshot struct {
	ready                  bool
	mean, variance, change float64
	changes                int
}

func (s *bocpd45Side) snapshot() bocpd45SideSnapshot {
	out := bocpd45SideSnapshot{changes: s.changes}
	second := 0.0
	for i, state := range s.states {
		total := state.up + state.down
		mean := state.up / total
		out.mean += state.probability * mean
		second += state.probability * state.up * (state.up + 1) / (total * (total + 1))
		if i == 0 {
			out.change = state.probability
		}
	}
	out.variance = math.Max(0, second-out.mean*out.mean)
	out.ready = out.changes >= bocpd45MinimumChanges && len(s.states) > 0
	if len(s.states) == 0 {
		out.mean = 0.5
	}
	return out
}

func (m *bocpd45DirectionModel) snapshot() bocpd45DirectionSnapshot {
	bid, ask := m.bid.snapshot(), m.ask.snapshot()
	out := bocpd45DirectionSnapshot{
		upProbability: 0.5, bidChanges: bid.changes, askChanges: ask.changes,
	}
	if !bid.ready || !ask.ready {
		return out
	}
	out.ready = true
	out.upProbability = 0.5 * (bid.mean + ask.mean)
	out.direction = math.Max(-1, math.Min(1, 2*out.upProbability-1))
	out.posteriorVariance = 0.5 * (bid.variance + ask.variance)
	out.changeProbability = 0.5 * (bid.change + ask.change)
	bernoulliVariance := out.upProbability * (1 - out.upProbability)
	precision := 0.0
	if bernoulliVariance > 1e-12 {
		precision = 1 - math.Min(1, out.posteriorVariance/bernoulliVariance)
	}
	out.confidence = math.Max(0, math.Min(1, (1-out.changeProbability)*precision))
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
