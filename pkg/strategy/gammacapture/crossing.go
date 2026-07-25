package gammacapture

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
)

type Direction int

const (
	DirectionDown Direction = -1
	DirectionNone Direction = 0
	DirectionUp   Direction = 1
)

type CrossingEvent struct {
	Symbol                    string
	Direction                 Direction
	FromState, ToState        int64
	BarrierWidth              float64
	ReferencePrice            fixedpoint.Value
	ExchangeTime, ReceiveTime time.Time
	GapAffected               bool
	StreamGeneration          uint64
	Epoch                     uint64
}

type CrossingEngine struct {
	Anchor       float64       `json:"anchor"`
	Width        float64       `json:"width"`
	State        int64         `json:"state"`
	Epoch        uint64        `json:"epoch"`
	LastCrossing time.Time     `json:"lastCrossing"`
	MinDwell     time.Duration `json:"minDwell"`
	MaxPerEvent  int           `json:"maxPerEvent"`
}

func NewCrossingEngine(width float64, minDwell time.Duration, maxPerEvent int) *CrossingEngine {
	return &CrossingEngine{Width: width, MinDwell: minDwell, MaxPerEvent: maxPerEvent}
}

func (e *CrossingEngine) Reset(price float64) { e.Anchor = math.Log(price); e.State = 0; e.Epoch++ }
func (e *CrossingEngine) PriceAt(state int64) float64 {
	return math.Exp(e.Anchor + float64(state)*e.Width)
}

// Update is causal: it only advances the observed grid and never revises a prior crossing.
func (e *CrossingEngine) Update(symbol string, price fixedpoint.Value, exchangeTime, receiveTime time.Time, generation uint64) []CrossingEvent {
	p := price.Float64()
	if p <= 0 || e.Width <= 0 {
		return nil
	}
	if e.Anchor == 0 {
		e.Reset(p)
		return nil
	}
	if e.MinDwell > 0 && !e.LastCrossing.IsZero() && exchangeTime.Sub(e.LastCrossing) < e.MinDwell {
		return nil
	}
	y := math.Log(p)
	next := e.State
	if y >= e.Anchor+float64(e.State+1)*e.Width {
		for y >= e.Anchor+float64(next+1)*e.Width {
			next++
		}
	} else if y <= e.Anchor+float64(e.State-1)*e.Width {
		for y <= e.Anchor+float64(next-1)*e.Width {
			next--
		}
	}
	steps := int64Abs(next - e.State)
	if steps == 0 {
		return nil
	}
	gap := steps > int64(e.MaxPerEvent)
	limit := steps
	if gap {
		limit = int64(e.MaxPerEvent)
	}
	events := make([]CrossingEvent, 0, limit)
	direction := DirectionUp
	if next < e.State {
		direction = DirectionDown
	}
	for i := int64(0); i < limit; i++ {
		from := e.State
		e.State += int64(direction)
		events = append(events, CrossingEvent{Symbol: symbol, Direction: direction, FromState: from, ToState: e.State, BarrierWidth: e.Width, ReferencePrice: price, ExchangeTime: exchangeTime, ReceiveTime: receiveTime, GapAffected: gap, StreamGeneration: generation, Epoch: e.Epoch})
	}
	// A capped discontinuity is explicitly uncertain; do not manufacture the unobserved path.
	if gap {
		e.Reset(p)
	}
	e.LastCrossing = exchangeTime
	return events
}
func int64Abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
