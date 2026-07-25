package gammacapture

type SignalCrossing int

const (
	SignalNone SignalCrossing = iota
	SignalUp
	SignalDown
)

type SignalState struct {
	ArmedUp       bool `json:"armedUp"`
	ArmedDown     bool `json:"armedDown"`
	Upcrossings   int  `json:"upcrossings"`
	Downcrossings int  `json:"downcrossings"`
	// CycleChurn is intentionally distinct from the lifetime diagnostic
	// counters above.  Exit logic must not become permanently stricter just
	// because a strategy has been running for a long time.
	CycleChurn int `json:"cycleChurn"`
}

func (s *SignalState) Update(value, low, high float64) SignalCrossing {
	if value <= low {
		s.ArmedUp = true
	}
	if value >= high {
		s.ArmedDown = true
	}
	if s.ArmedUp && value >= high {
		s.ArmedUp = false
		s.Upcrossings++
		s.CycleChurn++
		return SignalUp
	}
	if s.ArmedDown && value <= low {
		s.ArmedDown = false
		s.Downcrossings++
		s.CycleChurn++
		return SignalDown
	}
	return SignalNone
}

// ResetHysteresis starts a new eligible model-health epoch without erasing
// lifetime diagnostics.  A transition observed before the model is healthy
// must not arm or consume a trade signal later in that healthy epoch.
func (s *SignalState) ResetHysteresis() {
	s.ArmedUp = false
	s.ArmedDown = false
	s.CycleChurn = 0
}

// ResetCycle starts a new position/flat decision cycle while preserving the
// hysteresis arm state and lifetime metrics.
func (s *SignalState) ResetCycle() { s.CycleChurn = 0 }

func (s *SignalState) Churn() int { return s.CycleChurn }
