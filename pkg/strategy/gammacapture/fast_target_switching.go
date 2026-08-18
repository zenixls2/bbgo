package gammacapture

import "math"

// FastTargetSwitchingConfig keeps proportional switching cost inside the Fast
// target model. ShadowOnly exposes the decision without changing allocation.
type FastTargetSwitchingConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	ShadowOnly bool `json:"shadowOnly" yaml:"shadowOnly"`
}

type FastTargetSwitchingInput struct {
	CandidateTargetBase         float64
	PreviousTargetBase          float64
	CurrentInventoryBase        float64
	HardMinimumBase             float64
	HardMaximumBase             float64
	MidPrice                    float64
	PairEquityJPY               float64
	InventoryReturnMeanBps      float64
	InventoryReturnPredictiveSD float64
	RiskAversion                float64
	OneWayExecutionCostBps      float64
}

type FastTargetSwitchingDecision struct {
	Enabled                           bool
	Applied                           bool
	Reason                            string
	CandidateTargetBase               float64
	PreviousTargetBase                float64
	SelectedTargetBase                float64
	TargetTurnoverJPY                 float64
	CandidateCertaintyEquivalentJPY   float64
	PreviousCertaintyEquivalentJPY    float64
	SelectedCertaintyEquivalentJPY    float64
	IncrementalCertaintyEquivalentJPY float64
	// NetCertaintyEquivalent fields include the proportional execution cost
	// charged by the impulse controller.  The existing CE fields intentionally
	// remain the raw mean-variance utility for backwards-compatible reports.
	PreviousNetCertaintyEquivalentJPY float64
	SelectedNetCertaintyEquivalentJPY float64
	SwitchingCostJPY                  float64
	NetSwitchValueJPY                 float64
}

// EvaluateFastTargetSwitching compares the same-horizon mean-variance value of
// the new Fast target with the previous quoted Fast target, then charges the
// proportional cost of changing the intended inventory. This is a utility
// comparison, not a fixed BPS hysteresis band.
func EvaluateFastTargetSwitching(
	config FastTargetSwitchingConfig,
	in FastTargetSwitchingInput,
) FastTargetSwitchingDecision {
	d := FastTargetSwitchingDecision{
		Reason: "disabled", CandidateTargetBase: in.CandidateTargetBase,
		PreviousTargetBase: in.PreviousTargetBase,
		SelectedTargetBase: in.CandidateTargetBase,
	}
	if !config.Enabled {
		return d
	}
	d.Enabled = true
	finiteValue := func(value float64) bool {
		return !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	if in.MidPrice <= 0 || in.PairEquityJPY <= 0 || in.HardMinimumBase > in.HardMaximumBase ||
		!finiteValue(in.CandidateTargetBase) || !finiteValue(in.PreviousTargetBase) ||
		!finiteValue(in.CurrentInventoryBase) ||
		!finiteValue(in.InventoryReturnMeanBps) || !finiteValue(in.InventoryReturnPredictiveSD) ||
		in.InventoryReturnPredictiveSD < 0 || in.RiskAversion < 0 || in.OneWayExecutionCostBps < 0 {
		d.Reason = "invalid Fast target switching input"
		return d
	}
	candidate := math.Max(in.HardMinimumBase, math.Min(in.HardMaximumBase, in.CandidateTargetBase))
	previous := math.Max(in.HardMinimumBase, math.Min(in.HardMaximumBase, in.PreviousTargetBase))
	d.CandidateTargetBase, d.PreviousTargetBase = candidate, previous
	d.SelectedTargetBase = previous
	varianceBps2 := in.InventoryReturnPredictiveSD * in.InventoryReturnPredictiveSD
	utility := func(targetBase float64) float64 {
		notional := targetBase * in.MidPrice
		meanValue := notional * in.InventoryReturnMeanBps / 10_000
		variancePenalty := 0.5 * in.RiskAversion / in.PairEquityJPY *
			notional * notional * varianceBps2 / (10_000 * 10_000)
		return meanValue - variancePenalty
	}
	d.CandidateCertaintyEquivalentJPY = utility(candidate)
	d.PreviousCertaintyEquivalentJPY = utility(previous)

	// Solve the one-dimensional concave impulse problem exactly on the line
	// segment from the previous target to the posterior candidate:
	//
	//   max_x  a*x - b*x^2/2 - k*|x-I_t|.
	//
	// I_t is executable current inventory, not the previous abstract target.
	// Changing an unfilled target is free; transaction cost is paid only by an
	// eventual fill from current inventory. Comparing |x-x_previous| instead
	// double-counts cost when a stale target was never reached and misprices a
	// target reversal. The posterior chooses the admissible segment; this layer
	// determines the exact partial adjustment after actual execution cost.
	previousNotional := previous * in.MidPrice
	candidateNotional := candidate * in.MidPrice
	currentNotional := math.Max(
		in.HardMinimumBase*in.MidPrice,
		math.Min(in.HardMaximumBase*in.MidPrice, in.CurrentInventoryBase*in.MidPrice))
	meanPerJPY := in.InventoryReturnMeanBps / 10_000
	variancePenaltyPerJPY2 := in.RiskAversion / in.PairEquityJPY * varianceBps2 / (10_000 * 10_000)
	costPerJPY := in.OneWayExecutionCostBps / 10_000
	lower, upper := math.Min(previousNotional, candidateNotional), math.Max(previousNotional, candidateNotional)
	clamp := func(value float64) float64 { return math.Max(lower, math.Min(upper, value)) }
	netActionValue := func(notional float64) float64 {
		return utility(notional/in.MidPrice) - costPerJPY*math.Abs(notional-currentNotional)
	}
	candidates := []float64{previousNotional, candidateNotional, clamp(currentNotional)}
	if variancePenaltyPerJPY2 > 0 {
		candidates = append(candidates,
			clamp((meanPerJPY-costPerJPY)/variancePenaltyPerJPY2),
			clamp((meanPerJPY+costPerJPY)/variancePenaltyPerJPY2))
	}
	previousActionValue := netActionValue(previousNotional)
	selectedNotional := previousNotional
	selectedActionValue := previousActionValue
	for _, point := range candidates {
		value := netActionValue(point)
		if value > selectedActionValue+1e-12 {
			selectedNotional, selectedActionValue = point, value
		}
	}
	selected := selectedNotional / in.MidPrice
	d.SelectedTargetBase = selected
	d.SelectedCertaintyEquivalentJPY = utility(selected)
	d.IncrementalCertaintyEquivalentJPY =
		d.SelectedCertaintyEquivalentJPY - d.PreviousCertaintyEquivalentJPY
	d.PreviousNetCertaintyEquivalentJPY = previousActionValue
	d.SelectedNetCertaintyEquivalentJPY = selectedActionValue
	d.TargetTurnoverJPY = math.Abs(selected-previous) * in.MidPrice
	selectedExecutionCostJPY := math.Abs(selectedNotional-currentNotional) * costPerJPY
	d.SwitchingCostJPY = selectedExecutionCostJPY
	d.NetSwitchValueJPY = selectedActionValue - previousActionValue
	if math.Abs(candidate-previous)*in.MidPrice <= 1e-9 {
		d.SelectedTargetBase = candidate
		d.Applied = true
		d.Reason = "Fast target is unchanged"
		return d
	}
	if math.Abs(selected-previous)*in.MidPrice <= 1e-9 || d.NetSwitchValueJPY <= 0 {
		d.SelectedTargetBase = previous
		d.SelectedCertaintyEquivalentJPY = d.PreviousCertaintyEquivalentJPY
		d.Reason = "Fast target improvement does not pay switching cost"
		return d
	}
	d.Applied = true
	if math.Abs(selected-candidate)*in.MidPrice <= 1e-9 {
		d.Reason = "full Fast target certainty-equivalent improvement pays switching cost"
	} else {
		d.Reason = "partial Fast target adjustment maximizes certainty equivalent after switching cost"
	}
	return d
}
