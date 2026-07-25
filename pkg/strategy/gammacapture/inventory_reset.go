package gammacapture

import (
	"math"
	"time"
)

// InventoryResetInput is expressed in log-return basis points. FillIntensity
// is per second and VolatilityPerSqrtSecond is a log-return volatility.
type InventoryResetInput struct {
	Now                  time.Time
	AskSince             time.Time
	AnchorMidPrice       float64
	MidPrice             float64
	BestBid              float64
	AskPrice             float64
	MakerFeeBps          float64
	TakerFeeBps          float64
	MaxSlippageBps       float64
	FillIntensity        float64
	FillIntensityHaircut float64
	VolatilityPerSqrtSec float64
	FastDirectionSignal  float64 // [-1,1], negative means short-term downside pressure
}

type InventoryResetDecision struct {
	Trigger         bool
	Reason          string
	Age             time.Duration
	AdverseMoveBps  float64
	FillProbability float64
	WaitValueBps    float64
	IOCValueBps     float64
	RiskBps         float64
}

// Evaluate compares the expected net value of waiting for a passive ask with
// a slippage-capped IOC sell. It only triggers after both a stale ask and an
// observed adverse move, preventing unnecessary taker fees in quiet markets.
func (c InventoryResetConfig) Evaluate(in InventoryResetInput) InventoryResetDecision {
	d := InventoryResetDecision{Reason: "disabled"}
	if !c.Enabled {
		return d
	}
	if in.Now.IsZero() || in.AskSince.IsZero() || in.Now.Before(in.AskSince) {
		d.Reason = "invalid time"
		return d
	}
	if in.AnchorMidPrice <= 0 || in.MidPrice <= 0 || in.BestBid <= 0 || in.AskPrice <= 0 {
		d.Reason = "invalid prices"
		return d
	}
	d.Age = in.Now.Sub(in.AskSince)
	d.AdverseMoveBps = math.Log(in.MidPrice/in.AnchorMidPrice) * 10_000
	staleReset := d.Age >= time.Duration(c.MaxAskAge) && d.AdverseMoveBps <= -c.AdverseMoveBps
	fastReset := c.FastAskAge > 0 && c.FastAdverseMoveBps > 0 && c.FastDirectionThreshold > 0 &&
		d.Age >= time.Duration(c.FastAskAge) &&
		in.FastDirectionSignal <= -c.FastDirectionThreshold &&
		d.AdverseMoveBps <= -c.FastAdverseMoveBps
	if !staleReset && !fastReset {
		if d.Age < time.Duration(c.FastAskAge) {
			d.Reason = "ask not stale"
		} else if in.FastDirectionSignal > -math.Abs(c.FastDirectionThreshold) {
			d.Reason = "short-term downside threshold not reached"
		} else {
			d.Reason = "adverse move threshold not reached"
		}
		return d
	}

	seconds := d.Age.Seconds()
	lambda := math.Max(0, in.FillIntensity) * math.Min(1, math.Max(0, in.FillIntensityHaircut))
	d.FillProbability = 1 - math.Exp(-lambda*seconds)
	volBpsPerSqrtSec := math.Max(0, in.VolatilityPerSqrtSec) * 10_000
	d.RiskBps = math.Max(0, c.RiskZScore) * volBpsPerSqrtSec * math.Sqrt(seconds)

	// Prices are compared relative to the current mid, including the relevant
	// execution fee. If the ask does not fill, the observed drift is used as a
	// conservative mark for the remaining inventory.
	askNetBps := math.Log(in.AskPrice/in.MidPrice)*10_000 - in.MakerFeeBps
	iocPrice := in.BestBid * (1 - math.Max(0, in.MaxSlippageBps)/10_000)
	iocNetBps := math.Log(iocPrice/in.MidPrice)*10_000 - in.TakerFeeBps
	driftBps := d.AdverseMoveBps / seconds
	futureMarkBps := driftBps*seconds - in.TakerFeeBps
	d.WaitValueBps = d.FillProbability*askNetBps + (1-d.FillProbability)*futureMarkBps - d.RiskBps
	d.IOCValueBps = iocNetBps
	d.Trigger = d.IOCValueBps >= d.WaitValueBps
	if d.Trigger {
		d.Reason = "ioc value exceeds passive wait value"
	} else {
		d.Reason = "passive wait value remains higher"
	}
	return d
}
