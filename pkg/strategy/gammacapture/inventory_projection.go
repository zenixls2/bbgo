package gammacapture

import (
	"math"
	"time"
)

// ProbabilityCenteredQuoteInput describes the final quantity projection after
// Fast has chosen a gross risk budget and after account and inventory headroom
// are known. Values are quote-currency inventory notionals at a common mark;
// callers convert them through actual execution prices.
type ProbabilityCenteredQuoteInput struct {
	CurrentInventoryNotionalJPY float64
	TargetInventoryNotionalJPY  float64
	LowerInventoryNotionalJPY   float64
	UpperInventoryNotionalJPY   float64

	FastBuyNotionalJPY  float64
	FastSellNotionalJPY float64
	MinBuyNotionalJPY   float64
	MinSellNotionalJPY  float64
	MaxBuyNotionalJPY   float64
	MaxSellNotionalJPY  float64

	BuyFillRatePerHour  float64
	SellFillRatePerHour float64
	Horizon             time.Duration
	ConfidenceZScore    float64
	// TargetContraction is the fraction of the current Macro target error that
	// one fill should remove. Live trading uses one over the configured inventory
	// order levels. The solver multiplies this by the corrective side's horizon
	// fill probability, preventing a low arrival probability from being inverted
	// into an oversized resting order.
	TargetContraction float64
	// FastBuyRestraint is posterior bearish strength in [0,1]. It attenuates
	// only the BUY notional after the coherent bid/ask split; zero preserves
	// legacy callers and one removes new BUY exposure.
	FastBuyRestraint float64
}

// InventoryActuationInput describes the reachable-set problem between a Macro
// inventory target and the passive maker-fill process. MomentumSignal is the
// signed posterior mean in [-1,1], so (1+s*m)/2 is the posterior probability
// that momentum agrees with correction side s.
type InventoryActuationInput struct {
	CurrentInventoryNotionalJPY float64
	TargetInventoryNotionalJPY  float64
	ExpectedFillNotionalJPY     float64
	BuyFillRatePerHour          float64
	SellFillRatePerHour         float64
	RegimeHorizon               time.Duration
	MaximumOrderLevels          float64
	MomentumSignal              float64
}

// inventoryActuationFiniteNonNegative converts a data-derived rate or notional
// into a safe value for the controller. A missing estimate must never turn
// into an infinite correction or into a NaN that bypasses the hard inventory
// gates downstream.
func inventoryActuationFiniteNonNegative(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func inventoryProjectionFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

type InventoryActuationDecision struct {
	Enabled                      bool
	Direction                    int
	CorrectiveFillRatePerHour    float64
	ExpectedCorrectiveFills      float64
	RequiredCorrectionFills      float64
	EffectiveOrderLevels         float64
	TargetContraction            float64
	ReachabilityShortfall        float64
	MomentumAlignmentProbability float64
	InwardStrength               float64
	Reason                       string
}

// InventoryActuation computes the number of correction tranches that are
// statistically reachable during the expected regime lifetime. If K=lambda*T
// corrective fills are expected, splitting the error into K tranches gives an
// expected inventory drift that closes one target error over T (before caps).
// InventoryMaxOrderLevels remains an upper risk bound, not a fixed response
// time. Price urgency is the unreachable fraction, weighted by the posterior
// probability that short-horizon momentum agrees with the correction.
func InventoryActuation(in InventoryActuationInput) InventoryActuationDecision {
	d := InventoryActuationDecision{Reason: "invalid input"}
	errorJPY := in.TargetInventoryNotionalJPY - in.CurrentInventoryNotionalJPY
	if math.IsNaN(errorJPY) || math.IsInf(errorJPY, 0) || math.Abs(errorJPY) <= 1e-9 ||
		inventoryActuationFiniteNonNegative(in.ExpectedFillNotionalJPY) <= 0 ||
		in.RegimeHorizon <= 0 || in.MaximumOrderLevels <= 0 ||
		math.IsNaN(in.MaximumOrderLevels) || math.IsInf(in.MaximumOrderLevels, 0) {
		return d
	}
	if errorJPY > 0 {
		d.Direction = 1
		d.CorrectiveFillRatePerHour = inventoryActuationFiniteNonNegative(in.BuyFillRatePerHour)
	} else {
		d.Direction = -1
		d.CorrectiveFillRatePerHour = inventoryActuationFiniteNonNegative(in.SellFillRatePerHour)
	}
	if d.CorrectiveFillRatePerHour <= 0 {
		d.Reason = "corrective arrival rate unavailable"
		return d
	}
	hours := in.RegimeHorizon.Hours()
	if hours <= 0 || math.IsNaN(hours) || math.IsInf(hours, 0) {
		d.Reason = "invalid actuation horizon"
		return d
	}
	// A corrupted rate or an accidentally huge duration must not produce an
	// infinite level count. Once the configured maximum is reached, larger
	// expected fill counts have no further effect on the staged policy.
	d.ExpectedCorrectiveFills = d.CorrectiveFillRatePerHour * hours
	if d.ExpectedCorrectiveFills <= 0 || math.IsNaN(d.ExpectedCorrectiveFills) || math.IsInf(d.ExpectedCorrectiveFills, 0) {
		d.Reason = "invalid expected corrective fills"
		return d
	}
	expectedFillNotional := inventoryActuationFiniteNonNegative(in.ExpectedFillNotionalJPY)
	d.RequiredCorrectionFills = math.Abs(errorJPY) / expectedFillNotional
	if math.IsNaN(d.RequiredCorrectionFills) || math.IsInf(d.RequiredCorrectionFills, 0) {
		d.Reason = "invalid required correction fills"
		return d
	}
	d.EffectiveOrderLevels = math.Max(1, math.Min(in.MaximumOrderLevels, d.ExpectedCorrectiveFills))
	if math.IsNaN(d.EffectiveOrderLevels) || math.IsInf(d.EffectiveOrderLevels, 0) {
		d.Reason = "invalid effective order levels"
		return d
	}
	d.TargetContraction = 1 / d.EffectiveOrderLevels
	d.ReachabilityShortfall = math.Max(0, math.Min(1, d.RequiredCorrectionFills/d.ExpectedCorrectiveFills))
	if math.IsNaN(d.ReachabilityShortfall) || math.IsInf(d.ReachabilityShortfall, 0) {
		d.Reason = "invalid reachability load"
		return d
	}
	momentum := math.Max(-1, math.Min(1, in.MomentumSignal))
	if math.IsNaN(momentum) || math.IsInf(momentum, 0) {
		momentum = 0
	}
	d.MomentumAlignmentProbability = math.Max(0, math.Min(1,
		(1+float64(d.Direction)*momentum)/2))
	d.InwardStrength = d.ReachabilityShortfall * d.MomentumAlignmentProbability
	d.Enabled = true
	d.Reason = "arrival-reachable macro correction"
	return d
}

// InventoryTargetRealignmentRequired detects a target change large enough to
// alter an exchange-executable quantity cell. A correction-side sign change is
// always material because keeping the old allocation would push inventory in
// the newly wrong direction.
func InventoryTargetRealignmentRequired(currentTargetRatio, quotedTargetRatio, currentRiskyWeight, pairEquityJPY, executableCellJPY float64) bool {
	if pairEquityJPY <= 0 || executableCellJPY <= 0 ||
		math.IsNaN(currentTargetRatio) || math.IsNaN(quotedTargetRatio) || math.IsNaN(currentRiskyWeight) {
		return false
	}
	currentError := currentTargetRatio - currentRiskyWeight
	quotedError := quotedTargetRatio - currentRiskyWeight
	if currentError*quotedError < 0 {
		return true
	}
	return math.Abs(currentTargetRatio-quotedTargetRatio)*pairEquityJPY+1e-9 >= executableCellJPY
}

// ProbabilityCenteredQuoteDecision is the Bernoulli one-order approximation
// used to allocate Fast's gross quote budget between bid and ask.
//
// If B and S are the resting quote notionals and pB/pS are their horizon fill
// probabilities, the projected risky notional has
//
//	E[N_T] = N + pB*B - pS*S
//	Var[N_T] = pB(1-pB)B^2 + pS(1-pS)S^2.
//
// The solver preserves as much of Fast's requested gross B+S as fits the
// available balances and the Macro confidence interval, while choosing the
// split closest to the Macro expected target.
type ProbabilityCenteredQuoteDecision struct {
	Enabled bool
	Reason  string

	BuyFillProbability        float64
	SellFillProbability       float64
	FastGrossNotionalJPY      float64
	ProjectedGrossNotionalJPY float64
	BuyNotionalJPY            float64
	SellNotionalJPY           float64

	ExpectedInventoryNotionalJPY float64
	InventoryVarianceJPY2        float64
	InventoryStdDevJPY           float64
	ConfidenceLowerNotionalJPY   float64
	ConfidenceUpperNotionalJPY   float64
	TargetErrorJPY               float64
	DesiredInventoryNotionalJPY  float64
	TargetContraction            float64
	FastBuyRestraint             float64
	BuyRetention                 float64
	UnrestrainedBuyNotionalJPY   float64
}

func horizonFillProbability(ratePerHour float64, horizon time.Duration) float64 {
	if ratePerHour <= 0 || !inventoryProjectionFinite(ratePerHour) || horizon <= 0 {
		return 0
	}
	p := 1 - math.Exp(-ratePerHour*horizon.Hours())
	return math.Max(0, math.Min(1, p))
}

// ProbabilityCenteredQuoteNotionals applies one coherent quantity model after
// price formation. Price signals therefore cannot be multiplied a second time
// through ad-hoc side-size factors.
func ProbabilityCenteredQuoteNotionals(in ProbabilityCenteredQuoteInput) ProbabilityCenteredQuoteDecision {
	d := ProbabilityCenteredQuoteDecision{Reason: "invalid input"}
	if in.Horizon <= 0 ||
		in.UpperInventoryNotionalJPY <= in.LowerInventoryNotionalJPY ||
		!inventoryProjectionFinite(in.CurrentInventoryNotionalJPY) ||
		!inventoryProjectionFinite(in.TargetInventoryNotionalJPY) ||
		!inventoryProjectionFinite(in.LowerInventoryNotionalJPY) ||
		!inventoryProjectionFinite(in.UpperInventoryNotionalJPY) ||
		!inventoryProjectionFinite(in.FastBuyNotionalJPY) ||
		!inventoryProjectionFinite(in.FastSellNotionalJPY) ||
		!inventoryProjectionFinite(in.MinBuyNotionalJPY) ||
		!inventoryProjectionFinite(in.MinSellNotionalJPY) ||
		!inventoryProjectionFinite(in.MaxBuyNotionalJPY) ||
		!inventoryProjectionFinite(in.MaxSellNotionalJPY) ||
		!inventoryProjectionFinite(in.BuyFillRatePerHour) ||
		!inventoryProjectionFinite(in.SellFillRatePerHour) ||
		!inventoryProjectionFinite(in.ConfidenceZScore) ||
		!inventoryProjectionFinite(in.TargetContraction) ||
		!inventoryProjectionFinite(in.FastBuyRestraint) {
		return d
	}

	d.BuyFillProbability = horizonFillProbability(in.BuyFillRatePerHour, in.Horizon)
	d.SellFillProbability = horizonFillProbability(in.SellFillRatePerHour, in.Horizon)
	if d.BuyFillProbability <= 0 || d.SellFillProbability <= 0 {
		d.Reason = "two-sided fill probabilities unavailable"
		return d
	}

	fastBuy := math.Max(0, in.FastBuyNotionalJPY)
	fastSell := math.Max(0, in.FastSellNotionalJPY)
	maxBuy := math.Max(0, in.MaxBuyNotionalJPY)
	maxSell := math.Max(0, in.MaxSellNotionalJPY)
	minBuy := math.Max(0, in.MinBuyNotionalJPY)
	minSell := math.Max(0, in.MinSellNotionalJPY)
	if maxBuy+1e-9 < minBuy {
		maxBuy, minBuy = 0, 0
	}
	if maxSell+1e-9 < minSell {
		maxSell, minSell = 0, 0
	}
	d.FastGrossNotionalJPY = fastBuy + fastSell
	maxGross := math.Min(d.FastGrossNotionalJPY, maxBuy+maxSell)
	if maxGross <= 0 {
		d.Reason = "no executable gross capacity"
		return d
	}

	target := math.Max(in.LowerInventoryNotionalJPY,
		math.Min(in.UpperInventoryNotionalJPY, in.TargetInventoryNotionalJPY))
	contraction := in.TargetContraction
	explicitContraction := contraction > 0 && contraction <= 1
	if !explicitContraction {
		contraction = 1
	}
	if explicitContraction {
		if target > in.CurrentInventoryNotionalJPY {
			contraction *= d.BuyFillProbability
		} else if target < in.CurrentInventoryNotionalJPY {
			contraction *= d.SellFillProbability
		}
	}
	d.TargetContraction = contraction
	d.DesiredInventoryNotionalJPY = in.CurrentInventoryNotionalJPY +
		contraction*(target-in.CurrentInventoryNotionalJPY)
	z := in.ConfidenceZScore
	if z <= 0 {
		z = 1.645
	}
	pBuy, pSell := d.BuyFillProbability, d.SellFillProbability

	candidate := func(gross float64) ProbabilityCenteredQuoteDecision {
		c := d
		c.Enabled = true
		c.Reason = "probability-centered"
		gross = math.Max(0, math.Min(maxGross, gross))
		minimumBuy := math.Max(0, gross-maxSell)
		maximumBuy := math.Min(maxBuy, gross)
		// Keep both exchange-executable sides whenever Fast's gross budget and
		// the account's hard capacities can carry them.
		if gross+1e-9 >= minBuy+minSell && maxBuy >= minBuy && maxSell >= minSell {
			minimumBuy = math.Max(minimumBuy, minBuy)
			maximumBuy = math.Min(maximumBuy, gross-minSell)
		}
		if maximumBuy+1e-9 < minimumBuy {
			c.Enabled = false
			c.Reason = "exchange minimums exceed gross capacity"
			return c
		}
		desiredBuy := (d.DesiredInventoryNotionalJPY - in.CurrentInventoryNotionalJPY + pSell*gross) / (pBuy + pSell)
		buy := math.Max(minimumBuy, math.Min(maximumBuy, desiredBuy))
		sell := gross - buy
		expected := in.CurrentInventoryNotionalJPY + pBuy*buy - pSell*sell
		variance := pBuy*(1-pBuy)*buy*buy + pSell*(1-pSell)*sell*sell
		stddev := math.Sqrt(math.Max(0, variance))

		c.ProjectedGrossNotionalJPY = gross
		c.BuyNotionalJPY = buy
		c.SellNotionalJPY = sell
		c.ExpectedInventoryNotionalJPY = expected
		c.InventoryVarianceJPY2 = variance
		c.InventoryStdDevJPY = stddev
		c.ConfidenceLowerNotionalJPY = expected - z*stddev
		c.ConfidenceUpperNotionalJPY = expected + z*stddev
		c.TargetErrorJPY = expected - target
		return c
	}
	feasible := func(c ProbabilityCenteredQuoteDecision) bool {
		const tolerance = 1e-7
		if !c.Enabled ||
			(c.BuyNotionalJPY > tolerance && c.BuyNotionalJPY+tolerance < minBuy) ||
			(c.SellNotionalJPY > tolerance && c.SellNotionalJPY+tolerance < minSell) {
			return false
		}
		currentError := math.Abs(in.CurrentInventoryNotionalJPY - target)
		expectedError := math.Abs(c.ExpectedInventoryNotionalJPY - target)
		softHalfWidth := math.Max(target-in.LowerInventoryNotionalJPY,
			in.UpperInventoryNotionalJPY-target)
		softHalfWidth = math.Max(0, softHalfWidth)
		// Expected Macro error must not increase. Progress toward the target
		// earns an equal amount of Bernoulli variance budget, while the soft
		// stochastic band remains available even when inventory is on target:
		// E[(N_T-M)^2] <= (N-M)^2 + (softWidth/z)^2.
		mseBudget := currentError*currentError + math.Pow(softHalfWidth/z, 2)
		mse := expectedError*expectedError + c.InventoryVarianceJPY2
		return expectedError <= currentError+tolerance &&
			math.Abs(c.ExpectedInventoryNotionalJPY-d.DesiredInventoryNotionalJPY) <= softHalfWidth+tolerance &&
			mse <= mseBudget+tolerance
	}

	// Apply the posterior restraint only after the coherent quantity split. The
	// ask is deliberately unchanged: this is a restraint on adding exposure, not
	// the removed legacy gate that forced one-sided inventory liquidation.
	finalize := func(c ProbabilityCenteredQuoteDecision) ProbabilityCenteredQuoteDecision {
		if !c.Enabled {
			return c
		}
		restraint := math.Max(0, math.Min(1, in.FastBuyRestraint))
		c.FastBuyRestraint = restraint
		c.BuyRetention = 1 - restraint
		c.UnrestrainedBuyNotionalJPY = c.BuyNotionalJPY
		if restraint <= 0 || c.BuyNotionalJPY <= 0 {
			return c
		}
		c.BuyNotionalJPY *= c.BuyRetention
		// The posterior mapping is continuous; exchange minimums are discrete. A
		// sub-minimum remainder is represented as no BUY rather than rounded up,
		// which would erase strong bearish evidence.
		if c.BuyNotionalJPY > 0 && c.BuyNotionalJPY+1e-9 < minBuy {
			c.BuyNotionalJPY = 0
		}
		c.ProjectedGrossNotionalJPY = c.BuyNotionalJPY + c.SellNotionalJPY
		expected := in.CurrentInventoryNotionalJPY +
			pBuy*c.BuyNotionalJPY - pSell*c.SellNotionalJPY
		variance := pBuy*(1-pBuy)*c.BuyNotionalJPY*c.BuyNotionalJPY +
			pSell*(1-pSell)*c.SellNotionalJPY*c.SellNotionalJPY
		stddev := math.Sqrt(math.Max(0, variance))
		c.ExpectedInventoryNotionalJPY = expected
		c.InventoryVarianceJPY2 = variance
		c.InventoryStdDevJPY = stddev
		c.ConfidenceLowerNotionalJPY = expected - z*stddev
		c.ConfidenceUpperNotionalJPY = expected + z*stddev
		c.TargetErrorJPY = expected - target
		c.Reason = "probability-centered+bearish-buy-restraint"
		return c
	}

	best := d
	full := candidate(maxGross)
	if feasible(full) {
		return finalize(full)
	}

	// Capacity clipping means feasibility need not begin at zero gross when the
	// current inventory lies outside the soft band. Find the highest feasible
	// interval with a bounded scan, then refine its upper edge. This is constant
	// work per quote and does not touch historical data.
	const scanSteps = 32
	bestGross := 0.0
	for i := 1; i <= scanSteps; i++ {
		gross := maxGross * float64(i) / scanSteps
		c := candidate(gross)
		if feasible(c) && gross > bestGross {
			best, bestGross = c, gross
		}
	}
	if !best.Enabled {
		d.Reason = "no feasible two-sided macro contraction"
		return d
	}
	low, high := bestGross, math.Min(maxGross, bestGross+maxGross/scanSteps)
	for i := 0; i < 16; i++ {
		mid := (low + high) / 2
		c := candidate(mid)
		if feasible(c) {
			low, best = mid, c
		} else {
			high = mid
		}
	}
	return finalize(best)
}
