package gammacapture

import (
	"math"

	"github.com/c9s/bbgo/pkg/types"
)

type FastSideAdmissionDecision struct {
	Evaluated          bool
	Applied            bool
	Reason             string
	MaximumNotionalJPY float64
	UtilityBoundJPY    float64
}

// FastDirectionScaledBuyAdmission converts the selected-window Beta posterior
// direction into a continuous exchange-lattice cap when a BUY's confidence-
// adjusted marginal utility is not yet positive.  Direction is already
// 2*P(up)-1, hence max(0,direction) is the posterior advantage over a neutral
// coin.  It controls only the additional target-acquisition cells; one minimum
// executable cell remains available to keep Fast sampling alive.
func FastDirectionScaledBuyAdmission(
	d FastSideAdmissionDecision,
	direction, minimumNotionalJPY, requestedNotionalJPY float64,
) FastSideAdmissionDecision {
	finite := func(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
	if !d.Evaluated || d.UtilityBoundJPY > 0 ||
		!finite(direction) || !finite(minimumNotionalJPY) || !finite(requestedNotionalJPY) ||
		minimumNotionalJPY <= 0 || requestedNotionalJPY <= 0 ||
		d.MaximumNotionalJPY <= minimumNotionalJPY {
		return d
	}
	advantage := math.Max(0, math.Min(1, direction))
	maximum := minimumNotionalJPY +
		advantage*(d.MaximumNotionalJPY-minimumNotionalJPY)
	maximum = math.Min(requestedNotionalJPY, maximum)
	d.MaximumNotionalJPY = maximum
	d.Applied = maximum+1e-9 < requestedNotionalJPY
	d.Reason = "non-positive BUY utility scales extra target cells by Fast posterior advantage"
	return d
}

// FastTargetAwareSideAdmission is the exchange-lattice boundary condition for
// either Fast side. Its caller supplies the decision-relevant utility bound:
// a lower confidence bound for risk-increasing BUY and an upper confidence
// bound for risk-reducing SELL. A positive bound preserves the optimizer's
// quantity. A non-positive bound may still restore inventory to the posterior
// target. The separate FastPathDownsideBuyCap owns the one-cell boundary for
// a bearish terminal path; applying it here as well would double-count the
// same risk evidence and suppress statistically supported target acquisition.
// Missing evidence fails open.
func FastTargetAwareSideAdmission(
	side types.SideType,
	marginalUtilityEvaluated bool,
	currentInventoryJPY, targetInventoryJPY, minimumNotionalJPY,
	requestedNotionalJPY, utilityBoundJPY float64,
) FastSideAdmissionDecision {
	d := FastSideAdmissionDecision{
		Reason:             "minimum side utility unavailable",
		MaximumNotionalJPY: requestedNotionalJPY,
		UtilityBoundJPY:    utilityBoundJPY,
	}
	finite := func(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
	if !marginalUtilityEvaluated ||
		(side != types.SideTypeBuy && side != types.SideTypeSell) ||
		!finite(currentInventoryJPY) || !finite(targetInventoryJPY) ||
		!finite(minimumNotionalJPY) || !finite(requestedNotionalJPY) || !finite(utilityBoundJPY) ||
		minimumNotionalJPY <= 0 || requestedNotionalJPY < 0 {
		return d
	}
	d.Evaluated = true
	// BUY increases long-only risky exposure and therefore needs strictly
	// positive lower-bound evidence. SELL reduces exposure and is restrained
	// only with strictly negative upper-bound evidence; an exact zero is absence
	// of evidence, not evidence of harm.
	if utilityBoundJPY > 0 || (side == types.SideTypeSell && utilityBoundJPY == 0) {
		d.Reason = "minimum side utility bound is positive"
		if side == types.SideTypeSell && utilityBoundJPY == 0 {
			d.Reason = "SELL utility upper bound is not negative"
		}
		return d
	}
	maximum := math.Max(0, currentInventoryJPY-targetInventoryJPY)
	if side == types.SideTypeBuy {
		maximum = math.Max(0, targetInventoryJPY-currentInventoryJPY)
	}
	maximum = math.Min(requestedNotionalJPY, maximum)
	if maximum+1e-9 < minimumNotionalJPY {
		maximum = 0
	}
	d.MaximumNotionalJPY = maximum
	d.Applied = maximum+1e-9 < requestedNotionalJPY
	if side == types.SideTypeBuy {
		d.Reason = "non-positive BUY marginal utility restricts acquisition to target deficit"
	} else {
		d.Reason = "non-positive SELL marginal utility restricts reduction to target excess"
	}
	return d
}

type FastPathDownsideCapInput struct {
	Direction                   float64
	InventoryReturnMeanBps      float64
	InventoryReturnVarianceBps2 float64
	EffectiveSamples            float64
	ConfidenceZScore            float64
	BuyConfidenceEquivalentJPY  float64
	MinimumBuyNotionalJPY       float64
	MaximumBuyNotionalJPY       float64
}

type FastPathDownsideCapDecision struct {
	Evaluated                  bool
	Applied                    bool
	Reason                     string
	InventoryReturnMeanBps     float64
	InventoryReturnStdErrorBps float64
	InventoryReturnUpperBps    float64
	BuyConfidenceEquivalentJPY float64
	EffectiveSamples           float64
	OriginalMaximumBuyJPY      float64
	MaximumBuyNotionalJPY      float64
}

// FastPathDownsideBuyCap protects the probability-only Fast baseline from
// averaging down when completed terminal executable-bid paths contradict a
// new BUY. A BUY is capped at one exchange cell only when both conditions hold:
//
//  1. the live Fast crossing direction is bearish; and
//
//  2. the exchange-cell BUY has non-positive confidence-adjusted marginal
//     whole-position terminal wealth.
//
// Thus the cap is a confidence test in terminal wealth, not a rolling-return
// stop or a fitted BPS threshold. It never removes the bid, changes quote price,
// or constrains bullish/neutral/uncertain paths. SELL promotion remains owned
// by the same marginal whole-position utility optimizer. The terminal return
// confidence interval is reported as a diagnostic, not used as a second gate.
func FastPathDownsideBuyCap(in FastPathDownsideCapInput) FastPathDownsideCapDecision {
	d := FastPathDownsideCapDecision{
		Reason:                     "terminal downside evidence unavailable",
		InventoryReturnMeanBps:     in.InventoryReturnMeanBps,
		BuyConfidenceEquivalentJPY: in.BuyConfidenceEquivalentJPY,
		EffectiveSamples:           in.EffectiveSamples,
		OriginalMaximumBuyJPY:      in.MaximumBuyNotionalJPY,
		MaximumBuyNotionalJPY:      in.MaximumBuyNotionalJPY,
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if !finite(in.Direction) || !finite(in.InventoryReturnMeanBps) ||
		!finite(in.InventoryReturnVarianceBps2) || !finite(in.EffectiveSamples) ||
		!finite(in.ConfidenceZScore) || !finite(in.BuyConfidenceEquivalentJPY) ||
		!finite(in.MinimumBuyNotionalJPY) || !finite(in.MaximumBuyNotionalJPY) ||
		in.InventoryReturnVarianceBps2 < 0 || in.EffectiveSamples <= 1 ||
		in.MinimumBuyNotionalJPY <= 0 ||
		in.MaximumBuyNotionalJPY+1e-9 < in.MinimumBuyNotionalJPY {
		return d
	}
	d.Evaluated = true
	d.InventoryReturnStdErrorBps = math.Sqrt(in.InventoryReturnVarianceBps2 / in.EffectiveSamples)
	z := math.Max(0, in.ConfidenceZScore)
	d.InventoryReturnUpperBps = in.InventoryReturnMeanBps + z*d.InventoryReturnStdErrorBps
	if in.Direction >= 0 {
		d.Reason = "Fast direction is not bearish"
		return d
	}
	if in.InventoryReturnMeanBps >= 0 {
		d.Reason = "terminal executable inventory return is not bearish"
		return d
	}
	if in.BuyConfidenceEquivalentJPY > 0 {
		d.Reason = "exchange-cell BUY has positive marginal terminal wealth"
		return d
	}
	d.Applied = true
	d.Reason = "bearish Fast path rejects multi-cell BUY terminal wealth"
	d.MaximumBuyNotionalJPY = in.MinimumBuyNotionalJPY
	return d
}
