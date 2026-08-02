package gammacapture

import (
	"math"
	"time"
)

// AcquisitionStartInput is a causal public-market feature vector. It is kept
// separate from AcquisitionResetInput because a historical start label is not
// sufficient authorization for an IOC order.
type AcquisitionStartInput struct {
	InventoryDeficit       bool
	EvidenceHealth         ModelHealth
	Return1mBps            float64
	Return5mBps            float64
	Return1mThresholdBps   float64
	Return5mThresholdBps   float64
	ReturnCalibrationReady bool
	Drawdown5mBps          float64
	DrawdownLimit5mBps     float64
	TradeCount5m           int
	BBOCount5m             int
}

type AcquisitionStartDecision struct {
	Signal bool
	Reason string
}

// EvaluateStartShadow detects the nonzero start pattern selected on the
// chronological training sample. It deliberately emits observation only: the
// 2/12 holdout hit rate (95% Wilson interval about 4.7%-44.8%) was nonzero but
// not yet statistically distinct from the 5% unconditional holdout rate.
func (c AcquisitionResetConfig) EvaluateStartShadow(in AcquisitionStartInput) AcquisitionStartDecision {
	d := AcquisitionStartDecision{Reason: "shadow start disabled"}
	if !c.ShadowStartEnabled {
		return d
	}
	if !in.InventoryDeficit {
		d.Reason = "no inventory deficit"
		return d
	}
	if in.EvidenceHealth != HealthHealthy {
		d.Reason = "fast evidence not healthy"
		return d
	}
	if in.TradeCount5m < c.StartMinimumTrades5m {
		d.Reason = "insufficient five-minute public trades"
		return d
	}
	if in.BBOCount5m < c.StartMinimumBBO5m {
		d.Reason = "insufficient five-minute bbo path"
		return d
	}
	if in.DrawdownLimit5mBps <= 0 {
		d.Reason = "five-minute drawdown calibration unavailable"
		return d
	}
	if in.Drawdown5mBps > in.DrawdownLimit5mBps {
		d.Reason = "five-minute false-breakout drawdown exceeded"
		return d
	}
	return1mThresholdBps, return5mThresholdBps, ready := c.returnThresholds(
		in.Return1mThresholdBps, in.Return5mThresholdBps, in.ReturnCalibrationReady)
	if !ready {
		d.Reason = "return threshold calibration unavailable"
		return d
	}
	if in.Return1mBps < return1mThresholdBps {
		d.Reason = "one-minute return below shadow threshold"
		return d
	}
	if in.Return5mBps < return5mThresholdBps {
		d.Reason = "five-minute return below shadow threshold"
		return d
	}
	d.Signal = true
	d.Reason = "historical fee-positive start pattern observed (shadow only)"
	return d
}

// AcquisitionResetInput contains only causal information available while a
// passive maker bid is resting. Crossing counts and rates must be measured at
// the maker quote distance selected by MarketMakerHorizonModel; directional
// barrier counts are not an acceptable substitute.
type AcquisitionResetInput struct {
	Now                     time.Time
	DeficitSince            time.Time
	AnchorMidPrice          float64
	MidPrice                float64
	BestAsk                 float64
	BidPrice                float64
	PlannedAskPrice         float64
	MakerFeeBps             float64
	TakerFeeBps             float64
	AdverseSelectionBps     float64
	MaxSlippageBps          float64
	UpCrosses               int
	DownCrosses             int
	UpCrossesPerHour        float64
	DownCrossesPerHour      float64
	QuoteDistanceBps        float64
	Horizon                 time.Duration
	FillIntensityHaircut    float64
	VolatilityPerSqrtSec    float64
	EvidenceHealth          ModelHealth
	Return1mBps             float64
	Return5mBps             float64
	Return1mThresholdBps    float64
	Return5mThresholdBps    float64
	ReturnCalibrationReady  bool
	AdverseMoveThresholdBps float64
	Drawdown5mBps           float64
	DrawdownLimit5mBps      float64
	TradeCount5m            int
	BBOCount5m              int
}

// AcquisitionResetDecision exposes every probability and value term used by
// the fail-closed IOC BUY gate so a live decision can be audited from logs.
type AcquisitionResetDecision struct {
	Trigger                   bool
	Reason                    string
	Age                       time.Duration
	AdverseMoveBps            float64
	AdverseMoveThresholdBps   float64
	Return1mThresholdBps      float64
	Return5mThresholdBps      float64
	DrawdownLimit5mBps        float64
	UpProbabilityLower        float64
	PassiveBidFillProbability float64
	UpRateLowerPerHour        float64
	DownRateUpperPerHour      float64
	MakerExitFillProbability  float64
	ExpectedDriftLowerBps     float64
	RiskBps                   float64
	PassiveWaitValueBps       float64
	IOCValueBps               float64
	IOCImprovementBps         float64
}

// Evaluate compares a slippage-capped IOC acquisition with continuing to wait
// for the passive bid. It deliberately uses a lower confidence bound for
// upward crossings and an upper bound for downward crossings. The reset is
// therefore possible only when the quote-distance sample supports an upward
// regime after fees, adverse selection, unresolved-inventory risk, and the
// configured minimum edge.
func (c AcquisitionResetConfig) Evaluate(in AcquisitionResetInput) AcquisitionResetDecision {
	d := AcquisitionResetDecision{Reason: "disabled"}
	if !c.Enabled {
		return d
	}
	if in.Now.IsZero() || in.DeficitSince.IsZero() || in.Now.Before(in.DeficitSince) {
		d.Reason = "invalid time"
		return d
	}
	if in.AnchorMidPrice <= 0 || in.MidPrice <= 0 || in.BestAsk <= 0 || in.BidPrice <= 0 || in.PlannedAskPrice <= 0 {
		d.Reason = "invalid prices"
		return d
	}
	d.Age = in.Now.Sub(in.DeficitSince)
	d.AdverseMoveBps = math.Log(in.MidPrice/in.AnchorMidPrice) * 10_000
	if d.Age < time.Duration(c.MinDeficitAge) {
		d.Reason = "inventory deficit not stale"
		return d
	}
	d.AdverseMoveThresholdBps = in.AdverseMoveThresholdBps
	if d.AdverseMoveThresholdBps <= 0 {
		d.AdverseMoveThresholdBps = c.AdverseMoveBps
	}
	if d.AdverseMoveThresholdBps <= 0 {
		d.Reason = "adverse move calibration unavailable"
		return d
	}
	if d.AdverseMoveBps < d.AdverseMoveThresholdBps {
		d.Reason = "upward move threshold not reached"
		return d
	}
	d.DrawdownLimit5mBps = in.DrawdownLimit5mBps
	if in.EvidenceHealth != HealthHealthy {
		d.Reason = "fast evidence not healthy"
		return d
	}
	if in.TradeCount5m < c.StartMinimumTrades5m {
		d.Reason = "insufficient five-minute public trades"
		return d
	}
	if in.BBOCount5m < c.StartMinimumBBO5m {
		d.Reason = "insufficient five-minute bbo path"
		return d
	}
	if in.DrawdownLimit5mBps <= 0 {
		d.Reason = "five-minute drawdown calibration unavailable"
		return d
	}
	if in.Drawdown5mBps > in.DrawdownLimit5mBps {
		d.Reason = "five-minute false-breakout drawdown exceeded"
		return d
	}
	return1mThresholdBps, return5mThresholdBps, ready := c.returnThresholds(
		in.Return1mThresholdBps, in.Return5mThresholdBps, in.ReturnCalibrationReady)
	d.Return1mThresholdBps = return1mThresholdBps
	d.Return5mThresholdBps = return5mThresholdBps
	if !ready {
		d.Reason = "return threshold calibration unavailable"
		return d
	}
	if in.Return1mBps < d.Return1mThresholdBps {
		d.Reason = "one-minute return below confirmation threshold"
		return d
	}
	if in.Return5mBps < d.Return5mThresholdBps {
		d.Reason = "five-minute return below confirmation threshold"
		return d
	}

	samples := in.UpCrosses + in.DownCrosses
	if samples < c.MinSamples || in.UpCrosses <= 0 || in.DownCrosses <= 0 ||
		in.UpCrossesPerHour <= 0 || in.DownCrossesPerHour <= 0 ||
		in.QuoteDistanceBps <= 0 || in.Horizon <= 0 {
		d.Reason = "insufficient quote-distance crossing statistics"
		return d
	}

	d.UpProbabilityLower = wilsonLowerBound(in.UpCrosses, samples, c.ConfidenceZScore)
	if d.UpProbabilityLower <= 0.5 {
		d.Reason = "upward crossing advantage not statistically significant"
		return d
	}

	upObservedHours := float64(in.UpCrosses) / in.UpCrossesPerHour
	downObservedHours := float64(in.DownCrosses) / in.DownCrossesPerHour
	if upObservedHours <= 0 || downObservedHours <= 0 {
		d.Reason = "invalid quote-distance exposure"
		return d
	}
	z := math.Max(0, c.ConfidenceZScore)
	d.UpRateLowerPerHour = math.Max(0, (float64(in.UpCrosses)-z*math.Sqrt(float64(in.UpCrosses)))/upObservedHours)
	d.DownRateUpperPerHour = (float64(in.DownCrosses) + z*math.Sqrt(float64(in.DownCrosses)) + z*z) / downObservedHours
	if d.UpRateLowerPerHour <= d.DownRateUpperPerHour {
		d.Reason = "upward crossing intensity not statistically significant"
		return d
	}
	haircut := math.Min(1, math.Max(0, in.FillIntensityHaircut))
	hours := in.Horizon.Hours()
	d.PassiveBidFillProbability = 1 - math.Exp(-d.DownRateUpperPerHour*haircut*hours)
	d.MakerExitFillProbability = 1 - math.Exp(-d.UpRateLowerPerHour*haircut*hours)
	d.ExpectedDriftLowerBps = (d.UpRateLowerPerHour - d.DownRateUpperPerHour) * in.QuoteDistanceBps * hours

	volBpsPerSqrtSec := math.Max(0, in.VolatilityPerSqrtSec) * 10_000
	d.RiskBps = math.Max(0, c.RiskZScore) * volBpsPerSqrtSec * math.Sqrt(in.Horizon.Seconds())

	iocPrice := in.BestAsk * (1 + math.Max(0, in.MaxSlippageBps)/10_000)
	iocCostBps := math.Log(iocPrice/in.MidPrice)*10_000 + in.TakerFeeBps
	passiveCostBps := math.Log(in.BidPrice/in.MidPrice)*10_000 + in.MakerFeeBps
	exitRevenueBps := math.Log(in.PlannedAskPrice/in.MidPrice)*10_000 - in.MakerFeeBps - in.AdverseSelectionBps

	iocCycleBps := exitRevenueBps - iocCostBps
	passiveCycleBps := exitRevenueBps - passiveCostBps
	iocUnresolvedBps := d.ExpectedDriftLowerBps - iocCostBps - d.RiskBps
	passiveUnresolvedBps := d.ExpectedDriftLowerBps - passiveCostBps - d.RiskBps

	d.IOCValueBps = d.MakerExitFillProbability*iocCycleBps +
		(1-d.MakerExitFillProbability)*iocUnresolvedBps
	// If the passive bid does not fill, no base inventory is acquired and its
	// value is zero. Giving a later passive fill the full remaining horizon is
	// optimistic for waiting and makes the IOC promotion test conservative.
	d.PassiveWaitValueBps = d.PassiveBidFillProbability *
		(d.MakerExitFillProbability*passiveCycleBps +
			(1-d.MakerExitFillProbability)*passiveUnresolvedBps)
	d.IOCImprovementBps = d.IOCValueBps - d.PassiveWaitValueBps

	if d.IOCValueBps < c.MinimumExpectedValueBps {
		d.Reason = "ioc expected value below minimum"
		return d
	}
	if d.IOCImprovementBps < c.MinimumImprovementBps {
		d.Reason = "ioc improvement over passive wait below minimum"
		return d
	}
	d.Trigger = true
	d.Reason = "confidence-bounded ioc value exceeds passive wait"
	return d
}

func (c AcquisitionResetConfig) returnThresholds(return1m, return5m float64, calibrated bool) (float64, float64, bool) {
	if calibrated {
		return return1m, return5m, true
	}
	if c.StartReturn1mMinBps == 0 || c.StartReturn5mMinBps == 0 {
		return 0, 0, false
	}
	return c.StartReturn1mMinBps, c.StartReturn5mMinBps, true
}

// CalibratedReturnMoveBps converts a one-sided probability policy into a live
// price-move threshold. For driftless log-ask X(t)=sigma*W(t), the upper
// alpha-tail threshold is Phi^-1(1-alpha)*sigma*sqrt(t).
func (c AcquisitionResetConfig) CalibratedReturnMoveBps(e FastEvidenceSnapshot, horizon time.Duration) (float64, bool) {
	if e.MidVolatilitySamples5m < c.StartMinimumVolatilitySamples5m ||
		e.MidVolatilityObservedSeconds5m <= 0 ||
		e.BuyExecutionVolatility5mBps() <= 0 ||
		horizon <= 0 {
		return 0, false
	}
	alpha := c.StartReturnTailProbability
	if alpha <= 0 || alpha >= 0.5 {
		return 0, false
	}
	z := math.Sqrt2 * math.Erfcinv(2*alpha)
	seconds := math.Min(horizon.Seconds(), e.MidVolatilityObservedSeconds5m)
	move := z * e.BuyExecutionVolatility5mBps() * math.Sqrt(seconds)
	if move <= 0 || math.IsNaN(move) || math.IsInf(move, 0) {
		return 0, false
	}
	return move, true
}

// CalibratedDrawdownLimit5mBps converts a probability policy into a live BPS
// threshold. For driftless log-ask X(t)=sigma*W(t), the reflection principle
// gives P(max(X)-X(T) <= d) = 2*Phi(d/(sigma*sqrt(T)))-1. Therefore a tail
// probability alpha implies d = Phi^-1(1-alpha/2)*sigma*sqrt(T).
//
// Sigma is estimated causally from one-second BBO ask returns. An optional
// legacy fixed cap may only tighten the statistically generated threshold.
func (c AcquisitionResetConfig) CalibratedDrawdownLimit5mBps(e FastEvidenceSnapshot) (float64, bool) {
	if e.MidVolatilitySamples5m < c.StartMinimumVolatilitySamples5m ||
		e.MidVolatilityObservedSeconds5m <= 0 ||
		e.BuyExecutionVolatility5mBps() <= 0 {
		return 0, false
	}
	alpha := c.StartDrawdownTailProbability
	if alpha <= 0 || alpha >= 1 {
		return 0, false
	}
	z := math.Sqrt2 * math.Erfcinv(alpha)
	seconds := math.Min((5 * time.Minute).Seconds(), e.MidVolatilityObservedSeconds5m)
	limit := z * e.BuyExecutionVolatility5mBps() * math.Sqrt(seconds)
	if c.StartMaxDrawdown5mBps > 0 {
		limit = math.Min(limit, c.StartMaxDrawdown5mBps)
	}
	if limit <= 0 || math.IsNaN(limit) || math.IsInf(limit, 0) {
		return 0, false
	}
	return limit, true
}

func wilsonLowerBound(successes, samples int, z float64) float64 {
	if samples <= 0 || successes < 0 || successes > samples {
		return 0
	}
	z = math.Max(0, z)
	p := float64(successes) / float64(samples)
	n := float64(samples)
	denominator := 1 + z*z/n
	center := p + z*z/(2*n)
	margin := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n))
	return math.Max(0, (center-margin)/denominator)
}
