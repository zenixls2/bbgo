package gammacapture

import (
	"math"
	"time"
)

// RegimePersistenceConfig controls the research-only causal filter that turns
// a noisy directional score into a persistent regime tag. It is intentionally
// not a MarketMakerConfig field yet: promotion requires an independent replay
// result first.
type RegimePersistenceConfig struct {
	// UpdateInterval is the minimum sampling bucket. Observations in the same
	// bucket are ignored so a caller cannot create persistence by sending more
	// ticks rather than more independent model updates.
	UpdateInterval time.Duration
	// SmoothingHalfLife controls the exponentially weighted slow score.
	SmoothingHalfLife time.Duration
	// EnterThreshold and ExitThreshold form the hysteresis band. Enter must be
	// strictly above Exit and both are expressed on [-1, 1].
	EnterThreshold float64
	ExitThreshold  float64
	// MinConfirmations is the number of consecutive model buckets required for
	// a pending state change, including changes to neutral.
	MinConfirmations int
	// MinStateDuration prevents a newly committed non-neutral state from being
	// replaced immediately by a single noisy reversal.
	MinStateDuration time.Duration
	// MaxGap ends the causal segment. State is not carried over an observation
	// gap larger than this duration.
	MaxGap time.Duration
}

func (c RegimePersistenceConfig) withDefaults() RegimePersistenceConfig {
	if c.UpdateInterval <= 0 {
		c.UpdateInterval = 5 * time.Minute
	}
	if c.SmoothingHalfLife <= 0 {
		c.SmoothingHalfLife = 15 * time.Minute
	}
	if c.EnterThreshold <= 0 || c.EnterThreshold > 1 {
		c.EnterThreshold = 0.35
	}
	if c.ExitThreshold < 0 || c.ExitThreshold >= c.EnterThreshold {
		c.ExitThreshold = 0.15
	}
	if c.MinConfirmations <= 0 {
		c.MinConfirmations = 2
	}
	if c.MinStateDuration <= 0 {
		c.MinStateDuration = 10 * time.Minute
	}
	if c.MaxGap <= c.UpdateInterval {
		c.MaxGap = 2 * c.UpdateInterval
	}
	return c
}

// RegimePersistenceInput contains only information available at the current
// model update. SlowScore is the economic/path score used to change regime.
// FastReversalScore is deliberately reported as a conflict feature and cannot
// by itself flip the slow regime. ChangeProbability is an optional fast
// change-risk diagnostic in [0, 1].
type RegimePersistenceInput struct {
	At                time.Time
	SlowScore         float64
	FastReversalScore float64
	ChangeProbability float64
}

// RegimePersistenceDecision is the causal output of RegimePersistenceFilter.
// State is -1, 0, or +1 for down, neutral, and up. Tag is zero while neutral
// or awaiting confirmation, and otherwise carries the smoothed state strength
// with the sign of State.
type RegimePersistenceDecision struct {
	Ready             bool
	Healthy           bool
	Reason            string
	At                time.Time
	State             int
	PreviousState     int
	Changed           bool
	StateAge          time.Duration
	PendingState      int
	PendingBuckets    int
	RawScore          float64
	FilteredScore     float64
	Tag               float64
	ReversalConflict  float64
	ChangeProbability float64
	SegmentReset      bool
}

// RegimePersistenceFilter separates a slow economic state from fast reversal
// risk. It is online and finite-memory: no future label or fitted artifact is
// consulted when producing a decision.
type RegimePersistenceFilter struct {
	config RegimePersistenceConfig

	lastAt         time.Time
	lastBucket     time.Time
	filteredScore  float64
	state          int
	stateSince     time.Time
	pendingState   int
	pendingBuckets int
	lastDecision   RegimePersistenceDecision
}

func NewRegimePersistenceFilter(config RegimePersistenceConfig) *RegimePersistenceFilter {
	config = config.withDefaults()
	return &RegimePersistenceFilter{
		config:       config,
		lastDecision: RegimePersistenceDecision{Reason: "waiting for causal regime observations"},
	}
}

// Reset starts a new causal segment. This should be called after a data gap
// when the caller knows the gap before the next observation is material.
func (f *RegimePersistenceFilter) Reset() {
	if f == nil {
		return
	}
	config := f.config
	*f = *NewRegimePersistenceFilter(config)
}

func (f *RegimePersistenceFilter) Observe(input RegimePersistenceInput) RegimePersistenceDecision {
	if f == nil {
		return RegimePersistenceDecision{Reason: "nil regime persistence filter"}
	}
	if input.At.IsZero() {
		return f.invalidDecision("observation timestamp is missing")
	}
	if !f.lastAt.IsZero() && !input.At.After(f.lastAt) {
		decision := f.lastDecision
		decision.Changed = false
		decision.Reason = "out-of-order regime observation ignored"
		return decision
	}

	slow := clampRegimeScore(input.SlowScore)
	fast := clampRegimeScore(input.FastReversalScore)
	changeProbability := clampRegimeProbability(input.ChangeProbability)
	bucket := input.At.Truncate(f.config.UpdateInterval)
	if !f.lastBucket.IsZero() && bucket.Equal(f.lastBucket) {
		decision := f.lastDecision
		decision.Changed = false
		decision.Reason = "duplicate regime update bucket ignored"
		return decision
	}

	segmentReset := false
	if !f.lastAt.IsZero() && input.At.Sub(f.lastAt) > f.config.MaxGap {
		f.Reset()
		segmentReset = true
	}
	if f.lastAt.IsZero() {
		f.filteredScore = slow
	} else {
		delta := input.At.Sub(f.lastAt)
		decay := math.Exp(-math.Ln2 * float64(delta) / float64(f.config.SmoothingHalfLife))
		if !finiteRegimeValue(decay) || decay < 0 || decay > 1 {
			decay = 0
		}
		f.filteredScore = decay*f.filteredScore + (1-decay)*slow
	}
	f.filteredScore = clampRegimeScore(f.filteredScore)

	previousState := f.state
	desired := f.desiredState()
	if desired == f.state {
		f.pendingState = 0
		f.pendingBuckets = 0
	} else {
		if desired != f.pendingState {
			f.pendingState = desired
			f.pendingBuckets = 1
		} else {
			f.pendingBuckets++
		}
		canCommit := f.pendingBuckets >= f.config.MinConfirmations
		if f.state != 0 && !f.stateSince.IsZero() && input.At.Sub(f.stateSince) < f.config.MinStateDuration {
			canCommit = false
		}
		if canCommit {
			f.state = desired
			if f.state == 0 {
				f.stateSince = time.Time{}
			} else {
				f.stateSince = input.At
			}
			f.pendingState = 0
			f.pendingBuckets = 0
		}
	}

	f.lastAt = input.At
	f.lastBucket = bucket
	decision := f.makeDecision(input.At, previousState, slow, fast, changeProbability, segmentReset)
	f.lastDecision = decision
	return decision
}

func (f *RegimePersistenceFilter) desiredState() int {
	if f.filteredScore >= f.config.EnterThreshold {
		return 1
	}
	if f.filteredScore <= -f.config.EnterThreshold {
		return -1
	}
	if math.Abs(f.filteredScore) <= f.config.ExitThreshold {
		return 0
	}
	return f.state
}

func (f *RegimePersistenceFilter) makeDecision(at time.Time, previousState int, raw, fast, changeProbability float64, segmentReset bool) RegimePersistenceDecision {
	stateAge := time.Duration(0)
	if f.state != 0 && !f.stateSince.IsZero() {
		stateAge = at.Sub(f.stateSince)
	}
	stateDirection := f.state
	if stateDirection == 0 {
		stateDirection = signRegime(f.filteredScore)
	}
	conflict := 0.0
	if stateDirection != 0 {
		conflict = math.Max(0, -float64(stateDirection)*fast)
	}
	tag := 0.0
	if f.state != 0 {
		strength := (math.Abs(f.filteredScore) - f.config.ExitThreshold) / (1 - f.config.ExitThreshold)
		tag = float64(f.state) * clampRegimeProbability(strength)
	}
	ready := !f.lastAt.IsZero() && f.pendingBuckets == 0 && !segmentReset
	reason := "persistent regime state"
	if segmentReset {
		reason = "causal segment reset after observation gap"
		ready = false
	} else if f.pendingBuckets > 0 {
		reason = "pending regime confirmation"
		ready = false
	} else if f.state == 0 {
		reason = "neutral regime state"
	}
	return RegimePersistenceDecision{
		Ready: ready, Healthy: true, Reason: reason, At: at,
		State: f.state, PreviousState: previousState, Changed: f.state != previousState,
		StateAge: stateAge, PendingState: f.pendingState, PendingBuckets: f.pendingBuckets,
		RawScore: raw, FilteredScore: f.filteredScore, Tag: tag,
		ReversalConflict: clampRegimeProbability(conflict), ChangeProbability: changeProbability,
		SegmentReset: segmentReset,
	}
}

func (f *RegimePersistenceFilter) invalidDecision(reason string) RegimePersistenceDecision {
	decision := f.lastDecision
	decision.Healthy = false
	decision.Ready = false
	decision.Changed = false
	decision.Reason = reason
	return decision
}

func clampRegimeScore(value float64) float64 {
	if !finiteRegimeValue(value) {
		return 0
	}
	return math.Max(-1, math.Min(1, value))
}

func clampRegimeProbability(value float64) float64 {
	if !finiteRegimeValue(value) {
		return 0
	}
	return math.Max(0, math.Min(1, value))
}

func signRegime(value float64) int {
	if value > 0 {
		return 1
	}
	if value < 0 {
		return -1
	}
	return 0
}

func finiteRegimeValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
