package gammacapture

import (
	"math"
	"time"
)

// PivotRegimeConfig defines an economic directional-change process. The
// reversal threshold is a price excursion, not a sampling-bucket threshold.
// A caller should observe this component on every valid BBO event. The state
// is defined by price excursions and confirmed reversals, not time buckets.
type PivotRegimeConfig struct {
	ReversalBps     float64
	MaxGap          time.Duration
	MinLegSamples   int
	PriorLegSamples float64
}

func (c PivotRegimeConfig) withDefaults() PivotRegimeConfig {
	if c.ReversalBps <= 0 || !finitePivotRegimeValue(c.ReversalBps) {
		c.ReversalBps = 26
	}
	if c.MaxGap <= 0 {
		c.MaxGap = 15 * time.Minute
	}
	if c.MinLegSamples <= 0 {
		c.MinLegSamples = 2
	}
	if c.PriorLegSamples <= 0 || !finitePivotRegimeValue(c.PriorLegSamples) {
		c.PriorLegSamples = 2
	}
	return c
}

// PivotRegimeInput contains only information available at the current
// observation. The reference price should be a causal mid or executable
// reference selected by the caller.
type PivotRegimeInput struct {
	At             time.Time
	ReferencePrice float64
}

type PivotRegimeEvent struct {
	At              time.Time
	PivotAt         time.Time
	Direction       int
	Price           float64
	LegAmplitudeBps float64
}

// PivotRegimeDecision is the causal state of the currently active pivot leg.
// Direction is the direction from the last confirmed pivot to the current
// price. ExpectedLegAmplitudeBps is estimated only from already completed
// legs of the same direction.
type PivotRegimeDecision struct {
	Ready                   bool
	Healthy                 bool
	Reason                  string
	At                      time.Time
	Direction               int
	PivotChanged            bool
	SegmentReset            bool
	LastPivotAt             time.Time
	LegAge                  time.Duration
	LegAmplitudeBps         float64
	ExpectedLegAmplitudeBps float64
	RemainingAmplitudeBps   float64
	Reliability             float64
	CompletedLegSamples     int
	LastPivot               PivotRegimeEvent
}

// PivotRegimeSnapshot is the bounded state needed to continue the same causal
// pivot process after a restart.  It intentionally contains no future labels
// or order state; a restored filter is equivalent to having observed the same
// BBO midpoint sequence up to LastAt.
type PivotRegimeSnapshot struct {
	Config        PivotRegimeConfig
	LastAt        time.Time
	Seeded        bool
	Direction     int
	AnchorAt      time.Time
	AnchorPrice   float64
	ExtremeAt     time.Time
	ExtremePrice  float64
	LegSum        [2]float64
	LegSumSquares [2]float64
	LegCount      [2]int
	LastDecision  PivotRegimeDecision
}

// PivotRegimeFilter is a causal directional-change detector. It confirms a
// pivot only after price reverses by ReversalBps from the running extreme.
// No future timestamp or fixed bucket count determines the regime state.
type PivotRegimeFilter struct {
	config PivotRegimeConfig

	lastAt        time.Time
	seeded        bool
	direction     int
	anchorAt      time.Time
	anchorPrice   float64
	extremeAt     time.Time
	extremePrice  float64
	legSum        [2]float64
	legSumSquares [2]float64
	legCount      [2]int
	lastDecision  PivotRegimeDecision
}

func NewPivotRegimeFilter(config PivotRegimeConfig) *PivotRegimeFilter {
	config = config.withDefaults()
	return &PivotRegimeFilter{
		config:       config,
		lastDecision: PivotRegimeDecision{Reason: "waiting for causal pivot observations"},
	}
}

func (f *PivotRegimeFilter) Reset() {
	config := f.config
	*f = *NewPivotRegimeFilter(config)
}

// Snapshot returns only the bounded sufficient state of the causal pivot
// detector.  It is safe to call on a nil filter and is used by the live model
// checkpoint path.
func (f *PivotRegimeFilter) Snapshot() PivotRegimeSnapshot {
	if f == nil {
		return PivotRegimeSnapshot{}
	}
	return PivotRegimeSnapshot{
		Config:        f.config,
		LastAt:        f.lastAt,
		Seeded:        f.seeded,
		Direction:     f.direction,
		AnchorAt:      f.anchorAt,
		AnchorPrice:   f.anchorPrice,
		ExtremeAt:     f.extremeAt,
		ExtremePrice:  f.extremePrice,
		LegSum:        f.legSum,
		LegSumSquares: f.legSumSquares,
		LegCount:      f.legCount,
		LastDecision:  f.lastDecision,
	}
}

// Restore replaces the filter state only when the snapshot is finite and
// internally consistent.  A failed restore lets startup fall back to the
// bounded BBO prefill instead of quoting from an ambiguous partial state.
func (f *PivotRegimeFilter) Restore(snapshot PivotRegimeSnapshot) bool {
	if f == nil {
		return false
	}
	config := snapshot.Config.withDefaults()
	if snapshot.Direction < -1 || snapshot.Direction > 1 ||
		(snapshot.Seeded && (snapshot.AnchorPrice <= 0 || snapshot.ExtremePrice <= 0)) {
		return false
	}
	for index := range snapshot.LegCount {
		if snapshot.LegCount[index] < 0 ||
			!finitePivotRegimeValue(snapshot.LegSum[index]) ||
			!finitePivotRegimeValue(snapshot.LegSumSquares[index]) ||
			snapshot.LegSum[index] < 0 || snapshot.LegSumSquares[index] < 0 {
			return false
		}
	}
	*f = PivotRegimeFilter{
		config:        config,
		lastAt:        snapshot.LastAt,
		seeded:        snapshot.Seeded,
		direction:     snapshot.Direction,
		anchorAt:      snapshot.AnchorAt,
		anchorPrice:   snapshot.AnchorPrice,
		extremeAt:     snapshot.ExtremeAt,
		extremePrice:  snapshot.ExtremePrice,
		legSum:        snapshot.LegSum,
		legSumSquares: snapshot.LegSumSquares,
		legCount:      snapshot.LegCount,
		lastDecision:  snapshot.LastDecision,
	}
	return true
}

// CompletedLegVarianceBps2 is the sample variance of completed legs in the
// requested direction.  It is a causal predictive-risk input for the CE
// target; with fewer than two observations the variance is zero and the CE
// prior/fee terms still determine whether a position change is worthwhile.
func (f *PivotRegimeFilter) CompletedLegVarianceBps2(direction int) float64 {
	if f == nil || (direction != 1 && direction != -1) {
		return 0
	}
	index := 0
	if direction < 0 {
		index = 1
	}
	count := f.legCount[index]
	if count < 2 {
		return 0
	}
	mean := f.legSum[index] / float64(count)
	variance := (f.legSumSquares[index] - f.legSum[index]*mean) / float64(count-1)
	if variance < 0 {
		return 0
	}
	return variance
}

func (f *PivotRegimeFilter) Observe(input PivotRegimeInput) PivotRegimeDecision {
	if f == nil {
		return PivotRegimeDecision{Reason: "nil pivot regime filter"}
	}
	if input.At.IsZero() || input.ReferencePrice <= 0 || !finitePivotRegimeValue(input.ReferencePrice) {
		return f.invalidDecision("invalid pivot observation")
	}
	if !f.lastAt.IsZero() && !input.At.After(f.lastAt) {
		return f.invalidDecision("non-monotonic pivot observation")
	}
	segmentReset := false
	if !f.lastAt.IsZero() && input.At.Sub(f.lastAt) > f.config.MaxGap {
		f.seeded = false
		f.direction = 0
		f.anchorAt = time.Time{}
		f.anchorPrice = 0
		f.extremeAt = time.Time{}
		f.extremePrice = 0
		segmentReset = true
	}
	f.lastAt = input.At
	if !f.seeded {
		f.seeded = true
		f.anchorAt, f.extremeAt = input.At, input.At
		f.anchorPrice, f.extremePrice = input.ReferencePrice, input.ReferencePrice
		return f.makeDecision(input.At, segmentReset, PivotRegimeEvent{})
	}

	threshold := f.config.ReversalBps / 10_000
	pivotEvent := PivotRegimeEvent{}
	switch f.direction {
	case 0:
		switch {
		case math.Log(input.ReferencePrice/f.extremePrice) >= threshold:
			f.direction = 1
			f.anchorAt, f.anchorPrice = f.extremeAt, f.extremePrice
			f.extremeAt, f.extremePrice = input.At, input.ReferencePrice
		case math.Log(f.extremePrice/input.ReferencePrice) >= threshold:
			f.direction = -1
			f.anchorAt, f.anchorPrice = f.extremeAt, f.extremePrice
			f.extremeAt, f.extremePrice = input.At, input.ReferencePrice
		}
	case 1:
		if input.ReferencePrice >= f.extremePrice {
			f.extremeAt, f.extremePrice = input.At, input.ReferencePrice
		} else if math.Log(f.extremePrice/input.ReferencePrice) >= threshold {
			pivotEvent = f.confirmPivot(-1, input.At, input.ReferencePrice)
		}
	case -1:
		if input.ReferencePrice <= f.extremePrice {
			f.extremeAt, f.extremePrice = input.At, input.ReferencePrice
		} else if math.Log(input.ReferencePrice/f.extremePrice) >= threshold {
			pivotEvent = f.confirmPivot(1, input.At, input.ReferencePrice)
		}
	}
	return f.makeDecision(input.At, segmentReset, pivotEvent)
}

func (f *PivotRegimeFilter) confirmPivot(nextDirection int, at time.Time, price float64) PivotRegimeEvent {
	completedDirection := f.direction
	completedAmplitude := math.Abs(math.Log(f.extremePrice/f.anchorPrice)) * 10_000
	if completedDirection != 0 && completedAmplitude > 0 {
		index := 0
		if completedDirection < 0 {
			index = 1
		}
		f.legSum[index] += completedAmplitude
		f.legSumSquares[index] += completedAmplitude * completedAmplitude
		f.legCount[index]++
	}
	event := PivotRegimeEvent{
		At: at, PivotAt: f.extremeAt, Direction: completedDirection,
		Price: f.extremePrice, LegAmplitudeBps: completedAmplitude,
	}
	f.direction = nextDirection
	f.anchorAt, f.anchorPrice = f.extremeAt, f.extremePrice
	f.extremeAt, f.extremePrice = at, price
	return event
}

func (f *PivotRegimeFilter) makeDecision(at time.Time, segmentReset bool, event PivotRegimeEvent) PivotRegimeDecision {
	d := PivotRegimeDecision{
		At: at, Direction: f.direction, SegmentReset: segmentReset,
		PivotChanged: event.Direction != 0, LastPivot: event,
		Healthy: f.seeded, Reason: "pivot leg has insufficient completed-leg evidence",
	}
	if !f.seeded || f.direction == 0 {
		d.Reason = "waiting for first economic pivot leg"
		f.lastDecision = d
		return d
	}
	d.LastPivotAt = f.anchorAt
	d.LegAge = at.Sub(f.anchorAt)
	d.LegAmplitudeBps = math.Abs(math.Log(f.extremePrice/f.anchorPrice)) * 10_000
	index := 0
	if f.direction < 0 {
		index = 1
	}
	d.CompletedLegSamples = f.legCount[index]
	if d.CompletedLegSamples > 0 {
		d.ExpectedLegAmplitudeBps = f.legSum[index] / float64(d.CompletedLegSamples)
	}
	if d.ExpectedLegAmplitudeBps > 0 {
		d.RemainingAmplitudeBps = math.Max(0, d.ExpectedLegAmplitudeBps-d.LegAmplitudeBps)
		d.Reliability = float64(d.CompletedLegSamples) /
			(float64(d.CompletedLegSamples) + f.config.PriorLegSamples)
	}
	d.Ready = d.CompletedLegSamples >= f.config.MinLegSamples && d.ExpectedLegAmplitudeBps > 0
	if d.Ready {
		d.Reason = "causal pivot leg with empirical same-direction amplitude"
	}
	f.lastDecision = d
	return d
}

func (f *PivotRegimeFilter) invalidDecision(reason string) PivotRegimeDecision {
	d := f.lastDecision
	d.Ready = false
	d.Healthy = false
	d.Reason = reason
	f.lastDecision = d
	return d
}

type PivotRegimeSizingInput struct {
	Decision       PivotRegimeDecision
	CostBps        float64
	RiskPenaltyBps float64
	MaxTargetShift float64
	TargetScaleBps float64
}

type PivotRegimeSizingDecision struct {
	Ready               bool
	Applied             bool
	Direction           int
	NetRemainingBps     float64
	QuantityScale       float64
	SignedQuantityScale float64
	TargetShiftRatio    float64
	Reason              string
}

// EvaluatePivotRegimeSizing converts pivot geometry into a bounded continuous
// actuator. It never acts as a binary trade gate: insufficient remaining
// amplitude returns zero scale, while a strong, fee-cleared active leg returns
// a proportionally larger target shift.
func EvaluatePivotRegimeSizing(input PivotRegimeSizingInput) PivotRegimeSizingDecision {
	d := PivotRegimeSizingDecision{Reason: "pivot evidence is not ready"}
	if !input.Decision.Ready || (input.Decision.Direction != 1 && input.Decision.Direction != -1) ||
		!finitePivotRegimeValue(input.Decision.RemainingAmplitudeBps) ||
		!finitePivotRegimeValue(input.Decision.ExpectedLegAmplitudeBps) ||
		!finitePivotRegimeValue(input.Decision.Reliability) ||
		!finitePivotRegimeValue(input.CostBps) || !finitePivotRegimeValue(input.RiskPenaltyBps) ||
		!finitePivotRegimeValue(input.MaxTargetShift) || input.CostBps < 0 ||
		input.RiskPenaltyBps < 0 || input.MaxTargetShift <= 0 || input.Decision.Reliability <= 0 {
		return d
	}
	net := input.Decision.RemainingAmplitudeBps - input.CostBps - input.RiskPenaltyBps
	d.Direction = input.Decision.Direction
	d.NetRemainingBps = net
	if net <= 0 {
		d.Reason = "pivot remaining amplitude is not fee/risk positive"
		return d
	}
	denominator := input.TargetScaleBps
	if !finitePivotRegimeValue(denominator) || denominator <= 0 {
		denominator = input.Decision.ExpectedLegAmplitudeBps
	}
	if denominator <= 0 {
		return d
	}
	scale := net / denominator
	if scale > 1 {
		scale = 1
	}
	if scale < 0 {
		scale = 0
	}
	reliability := math.Max(0, math.Min(1, input.Decision.Reliability))
	scale *= reliability
	d.Ready, d.Applied = true, true
	d.QuantityScale = scale
	d.SignedQuantityScale = float64(d.Direction) * scale
	d.TargetShiftRatio = float64(d.Direction) * input.MaxTargetShift * scale
	d.Reason = "fee/risk-cleared pivot remaining amplitude"
	return d
}

func finitePivotRegimeValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
