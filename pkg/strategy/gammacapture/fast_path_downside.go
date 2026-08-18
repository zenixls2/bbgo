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

// FastTargetAwareSideAdmission is the exchange-lattice boundary condition for
// either Fast side. Its caller supplies the side's incremental, fee-net,
// whole-position certainty equivalent after posterior downside regret. A
// positive value preserves the optimizer's quantity; a non-positive value
// removes the side. Target restoration is already valued through the covariance
// term in that certainty equivalent, so granting an additional target-gap
// exception would spend the same inventory benefit twice. Missing evidence
// fails open.
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
	if utilityBoundJPY > 0 {
		d.Reason = "minimum side utility bound is positive"
		return d
	}
	d.MaximumNotionalJPY = 0
	d.Applied = requestedNotionalJPY > 0
	d.Reason = "non-positive fee-net side certainty equivalent removes Fast order"
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
// new BUY. A BUY is capped at one exchange cell only when both the terminal
// executable-inventory return and the exchange-cell marginal whole-position
// wealth are non-positive. The raw Fast direction is diagnostic only: using it
// as another prerequisite would apply the same directional evidence once in
// quote preprocessing and again after the terminal distribution is estimated.
//
// Thus the cap is a confidence test in terminal wealth, not a rolling-return
// stop or a fitted BPS threshold. It never removes the bid, changes quote price,
// or constrains terminally bullish/uncertain paths. SELL promotion remains owned
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
	if in.InventoryReturnMeanBps >= 0 {
		d.Reason = "terminal executable inventory return is not bearish"
		return d
	}
	if in.BuyConfidenceEquivalentJPY > 0 {
		d.Reason = "exchange-cell BUY has positive marginal terminal wealth"
		return d
	}
	d.Applied = true
	d.Reason = "terminally bearish path rejects multi-cell BUY terminal wealth"
	d.MaximumBuyNotionalJPY = in.MinimumBuyNotionalJPY
	return d
}
