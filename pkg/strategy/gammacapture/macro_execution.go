package gammacapture

import (
	"math"
	"time"
)

// MacroActiveExecutionInput contains only causal values available at a live
// BBO. Public quote-touch arrivals are treated as an optimistic upper bound on
// passive fill intensity; this biases the decision against taking liquidity.
type MacroActiveExecutionInput struct {
	Now                     time.Time
	LatestClosedBarAt       time.Time
	LastExecutionBarAt      time.Time
	Direction               int
	AggregateNetEdgeBps     float64
	ForecastHorizon         time.Duration
	ExecutionHorizon        time.Duration
	ConfidenceZScore        float64
	CurrentInventoryBase    float64
	TargetInventoryBase     float64
	BaselineInventoryBase   float64
	AvailableBase           float64
	AvailableQuote          float64
	BestBid                 float64
	BestBidSize             float64
	BestAsk                 float64
	BestAskSize             float64
	PassiveTouchEvents      int
	PassiveTouchRatePerHour float64
	MakerFeeBps             float64
	TakerFeeBps             float64
	MinimumQuantityBase     float64
	MinimumNotionalJPY      float64
}

// MacroActiveExecutionDecision is an auditable marketable IOC limit. Quantity
// never exceeds the current opposite-side L1 depth, while WorstPrice spends no
// more than the statistically estimated value lost by continuing to wait.
type MacroActiveExecutionDecision struct {
	Trigger                      bool
	Reason                       string
	Direction                    int
	Quantity                     float64
	TargetGapBase                float64
	TacticalTargetGapBase        float64
	ResidualMakerGapBase         float64
	DepthCapBase                 float64
	PassiveTouchRateUpperPerHour float64
	ExpectedPassiveWait          time.Duration
	PassiveMissProbability       float64
	DirectionalDriftBpsPerHour   float64
	WaitLossBps                  float64
	PassiveToTouchCostBps        float64
	FeeIncrementBps              float64
	MaximumImpactBps             float64
	UrgentFraction               float64
	WorstPrice                   float64
}

func macroPoissonRateUpper(events int, ratePerHour, z float64) (float64, bool) {
	if events <= 0 || ratePerHour <= 0 {
		return 0, false
	}
	exposureHours := float64(events) / ratePerHour
	if exposureHours <= 0 || math.IsNaN(exposureHours) || math.IsInf(exposureHours, 0) {
		return 0, false
	}
	z = math.Max(0, z)
	// Score-style one-sided Poisson bound. Using the upper rate minimizes the
	// inferred waiting loss and therefore keeps the taker decision conservative.
	upperEvents := float64(events) + z*math.Sqrt(float64(events)) + z*z
	return upperEvents / exposureHours, true
}

func truncatedExponentialWait(ratePerHour, horizonHours float64) float64 {
	if horizonHours <= 0 {
		return 0
	}
	if ratePerHour <= 1e-12 {
		return horizonHours
	}
	return -math.Expm1(-ratePerHour*horizonHours) / ratePerHour
}

// EvaluateMacroActiveExecution compares the expected opportunity loss during
// a passive wait with the incremental cost of crossing from that quote. Equal
// maker/taker fee schedules cancel naturally; no fixed BPS trigger is needed.
func EvaluateMacroActiveExecution(c MacroActiveExecutionConfig, in MacroActiveExecutionInput) MacroActiveExecutionDecision {
	d := MacroActiveExecutionDecision{Reason: "disabled", Direction: in.Direction}
	if !c.Enabled {
		return d
	}
	if in.Now.IsZero() || in.LatestClosedBarAt.IsZero() || in.LatestClosedBarAt.After(in.Now) {
		d.Reason = "invalid macro bar time"
		return d
	}
	if !in.LastExecutionBarAt.IsZero() && !in.LatestClosedBarAt.After(in.LastExecutionBarAt) {
		d.Reason = "macro bar already actively executed"
		return d
	}
	if in.ForecastHorizon <= 0 || in.ExecutionHorizon <= 0 {
		d.Reason = "missing macro forecast horizon"
		return d
	}
	if in.BestBid <= 0 || in.BestAsk <= in.BestBid {
		d.Reason = "invalid executable prices"
		return d
	}

	edgeBps := 0.0
	passivePrice, touchPrice, visibleDepth := 0.0, 0.0, 0.0
	switch {
	case in.Direction > 0:
		d.TargetGapBase = in.TargetInventoryBase - in.CurrentInventoryBase
		if d.TargetGapBase <= 0 || in.AggregateNetEdgeBps <= 0 {
			d.Reason = "bullish target gap or edge is absent"
			return d
		}
		edgeBps = in.AggregateNetEdgeBps
		d.TacticalTargetGapBase = math.Min(d.TargetGapBase,
			math.Max(0, in.TargetInventoryBase-in.BaselineInventoryBase))
		passivePrice, touchPrice, visibleDepth = in.BestBid, in.BestAsk, in.BestAskSize
	case in.Direction < 0:
		d.TargetGapBase = in.CurrentInventoryBase - in.TargetInventoryBase
		if d.TargetGapBase <= 0 || in.AggregateNetEdgeBps >= 0 {
			d.Reason = "bearish target gap or edge is absent"
			return d
		}
		edgeBps = -in.AggregateNetEdgeBps
		d.TacticalTargetGapBase = math.Min(d.TargetGapBase,
			math.Max(0, in.BaselineInventoryBase-in.TargetInventoryBase))
		passivePrice, touchPrice, visibleDepth = in.BestAsk, in.BestBid, in.BestBidSize
	default:
		d.Reason = "macro direction is neutral"
		return d
	}
	if d.TacticalTargetGapBase <= 0 {
		d.Reason = "macro tactical target gap is absent"
		return d
	}
	if visibleDepth <= 0 {
		d.Reason = "opposite-side visible depth unavailable"
		return d
	}

	upperRate, ok := macroPoissonRateUpper(
		in.PassiveTouchEvents, in.PassiveTouchRatePerHour, in.ConfidenceZScore)
	if !ok {
		d.Reason = "insufficient passive touch statistics"
		return d
	}
	d.PassiveTouchRateUpperPerHour = upperRate
	forecastHours := in.ForecastHorizon.Hours()
	executionHours := math.Min(in.ExecutionHorizon.Hours(), forecastHours)
	waitHours := truncatedExponentialWait(upperRate, executionHours)
	d.ExpectedPassiveWait = time.Duration(waitHours * float64(time.Hour))
	d.PassiveMissProbability = math.Exp(-upperRate * executionHours)
	d.DirectionalDriftBpsPerHour = edgeBps / forecastHours
	d.WaitLossBps = d.DirectionalDriftBpsPerHour * waitHours
	if in.Direction > 0 {
		d.PassiveToTouchCostBps = math.Log(touchPrice/passivePrice) * 10_000
	} else {
		d.PassiveToTouchCostBps = math.Log(passivePrice/touchPrice) * 10_000
	}
	d.FeeIncrementBps = in.TakerFeeBps - in.MakerFeeBps
	crossingCostBps := d.PassiveToTouchCostBps + d.FeeIncrementBps
	d.MaximumImpactBps = d.WaitLossBps - crossingCostBps
	if d.MaximumImpactBps <= 0 {
		d.Reason = "passive wait loss does not exceed crossing cost"
		return d
	}
	d.UrgentFraction = d.PassiveMissProbability * math.Min(1, d.MaximumImpactBps/d.WaitLossBps)
	urgentQuantity := d.TacticalTargetGapBase * d.UrgentFraction

	if in.Direction > 0 {
		d.WorstPrice = touchPrice * math.Exp(d.MaximumImpactBps/10_000)
		balanceCap := in.AvailableQuote / d.WorstPrice
		d.DepthCapBase = visibleDepth
		d.Quantity = math.Min(urgentQuantity, math.Min(visibleDepth, balanceCap))
	} else {
		d.WorstPrice = touchPrice * math.Exp(-d.MaximumImpactBps/10_000)
		d.DepthCapBase = visibleDepth
		d.Quantity = math.Min(urgentQuantity, math.Min(visibleDepth, in.AvailableBase))
	}
	d.ResidualMakerGapBase = math.Max(0, d.TargetGapBase-d.Quantity)
	if d.Quantity <= 0 || d.WorstPrice <= 0 || math.IsNaN(d.Quantity) || math.IsNaN(d.WorstPrice) {
		d.Reason = "depth or balance leaves no executable quantity"
		return d
	}
	if (in.MinimumQuantityBase > 0 && d.Quantity < in.MinimumQuantityBase) ||
		(in.MinimumNotionalJPY > 0 && d.Quantity*touchPrice < in.MinimumNotionalJPY) {
		d.Reason = "probabilistic IOC tranche below exchange minimum"
		return d
	}
	d.Trigger = true
	d.Reason = "confidence-adjusted wait loss exceeds bounded IOC cost"
	return d
}
