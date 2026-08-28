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
	// DirectFillProbabilities identifies non-Poisson completed-window inputs.
	// Zero is a valid measured probability, so an explicit switch is required.
	DirectFillProbabilities bool
	// FullRiskPromotion is set only by the terminal-path utility optimizer when
	// confidence-supported evidence permits search beyond the target-sufficient
	// baseline. Hard risk, inventory, and balance capacities still apply.
	FullRiskPromotion   bool
	BuyFillProbability  float64
	SellFillProbability float64
	BothFillProbability float64
	Horizon             time.Duration
	ConfidenceZScore    float64
	// TargetContraction is the fraction of the current unified Fast target error that
	// one fill should remove. Live trading derives it from the number of
	// statistically reachable, exchange-sized correction fills. The solver
	// multiplies this by the corrective side's horizon
	// fill probability, preventing a low arrival probability from being inverted
	// into an oversized resting order.
	TargetContraction float64
	// FastBuyRestraint is posterior bearish strength in [0,1]. It attenuates
	// only the BUY notional after the coherent bid/ask split; zero preserves
	// legacy callers and one removes new BUY exposure.
	FastBuyRestraint float64
	// FastSellRestraint is symmetric bullish posterior strength. Both restraints
	// preserve an executable opposite-side cell instead of creating the removed
	// one-sided hard gate.
	FastSellRestraint float64
}

// ExposureUtilizationSizingInput converts one exchange-executable notional
// cell into side-specific risk capacity. RiskSizedNotionalJPY is the Fast
// adverse-move budget before balances and inventory policy are applied.
type ExposureUtilizationSizingInput struct {
	ExecutableUnitJPY                 float64
	RiskSizedNotionalJPY              float64
	PairEquityJPY                     float64
	CurrentInventoryNotionalJPY       float64
	HardLowerInventoryNotionalJPY     float64
	HardUpperInventoryNotionalJPY     float64
	AvailableBuyCapitalJPY            float64
	AvailableSellInventoryNotionalJPY float64
}

// ExposureUtilizationSizingDecision reports the dimensionless multiplier and
// the resulting side capacities. A multiplier below one is retained rather
// than rounded up: the exchange filter downstream decides whether the remaining
// risk capacity can support an executable order.
type ExposureUtilizationSizingDecision struct {
	Enabled               bool
	Reason                string
	RiskMultiplier        float64
	BuyMultiplier         float64
	SellMultiplier        float64
	BuyNotionalCapJPY     float64
	SellNotionalCapJPY    float64
	CurrentExposureRatio  float64
	BuyHeadroomRatio      float64
	SellHeadroomRatio     float64
	GrossUtilizationRatio float64
}

// FastQuantityCapacityDecision gives the single Fast quantity controller a
// common risk, inventory-headroom, and balance-backed feasible set. The
// probability-only baseline risks one exchange-executable fill when completed
// path payoffs are unidentifiable. Statistically supported path utility may
// promote above that floor, bounded by the whole-position risk capacity. No
// downstream module may exceed the selected hard capacity.
type FastQuantityCapacityDecision struct {
	Exposure            ExposureUtilizationSizingDecision
	BaselineBuyCapJPY   float64
	BaselineSellCapJPY  float64
	PathModelBuyCapJPY  float64
	PathModelSellCapJPY float64
}

// FastQuantityCapacity defines the feasible set for the unified Fast quantity
// problem:
//
//	C_risk,s = min(Q_risk, H_s, B_s)
//	C_baseline,s = min(q_min, C_risk,s), C_path,s = C_risk,s.
//
// This distinguishes a hard risk ceiling from the amount justified without an
// identifiable terminal-path distribution.
func FastQuantityCapacity(in ExposureUtilizationSizingInput) FastQuantityCapacityDecision {
	exposure := ExposureUtilizationQuoteSizing(in)
	return FastQuantityCapacityDecision{
		Exposure:            exposure,
		BaselineBuyCapJPY:   exposure.BuyNotionalCapJPY,
		BaselineSellCapJPY:  exposure.SellNotionalCapJPY,
		PathModelBuyCapJPY:  exposure.BuyNotionalCapJPY,
		PathModelSellCapJPY: exposure.SellNotionalCapJPY,
	}
}

// ExposureUtilizationQuoteSizing implements q_s=q0*m_s with
//
//	m_s = min(qRisk/q0, exposureHeadroom_s/q0, availableCapital_s/q0).
//
// The risk budget therefore chooses the desired scale while current exposure
// and actually deployable capital provide independent, side-specific caps.
// No configured order-level divisor or arbitrary maximum multiplier is used.
func ExposureUtilizationQuoteSizing(in ExposureUtilizationSizingInput) ExposureUtilizationSizingDecision {
	d := ExposureUtilizationSizingDecision{Reason: "invalid input"}
	values := []float64{
		in.ExecutableUnitJPY, in.RiskSizedNotionalJPY, in.PairEquityJPY,
		in.CurrentInventoryNotionalJPY, in.HardLowerInventoryNotionalJPY,
		in.HardUpperInventoryNotionalJPY, in.AvailableBuyCapitalJPY,
		in.AvailableSellInventoryNotionalJPY,
	}
	for _, value := range values {
		if !inventoryProjectionFinite(value) {
			return d
		}
	}
	if in.ExecutableUnitJPY <= 0 || in.RiskSizedNotionalJPY <= 0 ||
		in.PairEquityJPY <= 0 ||
		in.HardUpperInventoryNotionalJPY <= in.HardLowerInventoryNotionalJPY {
		return d
	}

	current := in.CurrentInventoryNotionalJPY
	buyHeadroom := math.Max(0, in.HardUpperInventoryNotionalJPY-current)
	sellHeadroom := math.Max(0, current-in.HardLowerInventoryNotionalJPY)
	buyCapital := math.Max(0, in.AvailableBuyCapitalJPY)
	sellCapital := math.Max(0, in.AvailableSellInventoryNotionalJPY)

	d.RiskMultiplier = in.RiskSizedNotionalJPY / in.ExecutableUnitJPY
	d.BuyNotionalCapJPY = math.Min(in.RiskSizedNotionalJPY,
		math.Min(buyHeadroom, buyCapital))
	d.SellNotionalCapJPY = math.Min(in.RiskSizedNotionalJPY,
		math.Min(sellHeadroom, sellCapital))
	d.BuyMultiplier = d.BuyNotionalCapJPY / in.ExecutableUnitJPY
	d.SellMultiplier = d.SellNotionalCapJPY / in.ExecutableUnitJPY
	d.CurrentExposureRatio = current / in.PairEquityJPY
	d.BuyHeadroomRatio = buyHeadroom / in.PairEquityJPY
	d.SellHeadroomRatio = sellHeadroom / in.PairEquityJPY
	d.GrossUtilizationRatio = (d.BuyNotionalCapJPY + d.SellNotionalCapJPY) / in.PairEquityJPY
	d.Enabled = d.BuyNotionalCapJPY > 0 || d.SellNotionalCapJPY > 0
	if d.Enabled {
		d.Reason = "risk-exposure-capital minimum"
	} else {
		d.Reason = "no side capacity"
	}
	return d
}

// SymmetricInventoryProjectionBounds returns the widest target-centered
// interval contained by the hard inventory band. The quantity solver uses one
// second-moment radius, so asymmetric hard headroom must be reduced to its
// narrower side instead of borrowing capacity from the wider side.
func SymmetricInventoryProjectionBounds(target, hardLower, hardUpper float64) (lower, upper float64) {
	if !inventoryProjectionFinite(target) || !inventoryProjectionFinite(hardLower) ||
		!inventoryProjectionFinite(hardUpper) || hardUpper <= hardLower {
		return target, target
	}
	center := math.Max(hardLower, math.Min(hardUpper, target))
	halfWidth := math.Min(center-hardLower, hardUpper-center)
	return center - halfWidth, center + halfWidth
}

// TargetAwareFallbackQuoteInput describes the quantity fallback used when the
// second-moment inventory projection is unavailable. All values share the same
// mid-marked quote-currency notional. The fallback is deliberately target
// monotone: it may reduce the current target error, but it may never create a
// quote that increases that error.
type TargetAwareFallbackQuoteInput struct {
	CurrentInventoryNotionalJPY   float64
	TargetInventoryNotionalJPY    float64
	HardLowerInventoryNotionalJPY float64
	HardUpperInventoryNotionalJPY float64
	BuyNotionalCapJPY             float64
	SellNotionalCapJPY            float64
	MinBuyNotionalJPY             float64
	MinSellNotionalJPY            float64
}

type TargetAwareFallbackQuoteDecision struct {
	Enabled             bool
	Reason              string
	CorrectiveDirection int
	BuyNotionalJPY      float64
	SellNotionalJPY     float64
}

// TargetAwareFallbackQuoteNotionals preserves the Macro/causal target when the
// probability projection cannot solve (notably when a target at a hard 0% or
// 100% boundary collapses its symmetric interval). A material target error is
// corrected on one side only. When inventory is already aligned with a
// strictly interior target, one minimum executable cell per side is retained
// for private-fill learning; hard-boundary targets do not add exposure merely
// to manufacture observations.
func TargetAwareFallbackQuoteNotionals(in TargetAwareFallbackQuoteInput) TargetAwareFallbackQuoteDecision {
	d := TargetAwareFallbackQuoteDecision{Reason: "invalid target-aware fallback input"}
	values := []float64{
		in.CurrentInventoryNotionalJPY, in.TargetInventoryNotionalJPY,
		in.HardLowerInventoryNotionalJPY, in.HardUpperInventoryNotionalJPY,
		in.BuyNotionalCapJPY, in.SellNotionalCapJPY,
		in.MinBuyNotionalJPY, in.MinSellNotionalJPY,
	}
	for _, value := range values {
		if !inventoryProjectionFinite(value) {
			return d
		}
	}
	if in.HardUpperInventoryNotionalJPY <= in.HardLowerInventoryNotionalJPY ||
		in.BuyNotionalCapJPY < 0 || in.SellNotionalCapJPY < 0 ||
		in.MinBuyNotionalJPY < 0 || in.MinSellNotionalJPY < 0 {
		return d
	}

	lower := in.HardLowerInventoryNotionalJPY
	upper := in.HardUpperInventoryNotionalJPY
	current := math.Max(lower, math.Min(upper, in.CurrentInventoryNotionalJPY))
	target := math.Max(lower, math.Min(upper, in.TargetInventoryNotionalJPY))
	buyCap := math.Max(0, math.Min(in.BuyNotionalCapJPY, upper-current))
	sellCap := math.Max(0, math.Min(in.SellNotionalCapJPY, current-lower))
	gap := target - current
	tolerance := 1e-9 * math.Max(1, math.Max(math.Abs(lower), math.Abs(upper)))

	if gap > tolerance {
		d.CorrectiveDirection = 1
		d.BuyNotionalJPY = math.Min(gap, buyCap)
		if d.BuyNotionalJPY+1e-9 < in.MinBuyNotionalJPY {
			d.BuyNotionalJPY = 0
		}
		d.Enabled = d.BuyNotionalJPY > 0
		d.Reason = "buy-only target correction"
		if !d.Enabled {
			d.Reason = "buy target gap is not executable"
		}
		return d
	}
	if gap < -tolerance {
		d.CorrectiveDirection = -1
		d.SellNotionalJPY = math.Min(-gap, sellCap)
		if d.SellNotionalJPY+1e-9 < in.MinSellNotionalJPY {
			d.SellNotionalJPY = 0
		}
		d.Enabled = d.SellNotionalJPY > 0
		d.Reason = "sell-only target correction"
		if !d.Enabled {
			d.Reason = "sell target gap is not executable"
		}
		return d
	}

	if target <= lower+tolerance || target >= upper-tolerance {
		d.Reason = "aligned hard-boundary target"
		return d
	}
	if buyCap+1e-9 >= in.MinBuyNotionalJPY && in.MinBuyNotionalJPY > 0 {
		d.BuyNotionalJPY = in.MinBuyNotionalJPY
	}
	if sellCap+1e-9 >= in.MinSellNotionalJPY && in.MinSellNotionalJPY > 0 {
		d.SellNotionalJPY = in.MinSellNotionalJPY
	}
	d.Enabled = d.BuyNotionalJPY > 0 || d.SellNotionalJPY > 0
	d.Reason = "interior target minimum learning cells"
	if !d.Enabled {
		d.Reason = "interior target has no executable learning cell"
	}
	return d
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
// The required number of exchange-sized fills bounds the reachable fill count,
// so a fixed configured divisor cannot create sub-minimum tranches. Price
// urgency is the unreachable fraction, weighted by the posterior
// probability that short-horizon momentum agrees with the correction.
func InventoryActuation(in InventoryActuationInput) InventoryActuationDecision {
	d := InventoryActuationDecision{Reason: "invalid input"}
	errorJPY := in.TargetInventoryNotionalJPY - in.CurrentInventoryNotionalJPY
	if math.IsNaN(errorJPY) || math.IsInf(errorJPY, 0) || math.Abs(errorJPY) <= 1e-9 ||
		inventoryActuationFiniteNonNegative(in.ExpectedFillNotionalJPY) <= 0 ||
		in.RegimeHorizon <= 0 {
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
	// A corrupted rate or accidentally huge duration must not produce an
	// infinite level count.
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
	// Never subdivide the correction into more pieces than the exchange-sized
	// target gap can contain. This keeps every inferred tranche executable while
	// allowing liquid regimes to stage genuinely larger corrections.
	d.EffectiveOrderLevels = math.Max(1,
		math.Min(d.RequiredCorrectionFills, d.ExpectedCorrectiveFills))
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
// available balances and the unified Fast confidence interval, while choosing
// the split closest to the unified Fast expected target.
type ProbabilityCenteredQuoteDecision struct {
	Enabled bool
	Reason  string

	BuyFillProbability        float64
	SellFillProbability       float64
	BothFillProbability       float64
	FillCovariance            float64
	FastGrossNotionalJPY      float64
	ProjectedGrossNotionalJPY float64
	BuyNotionalJPY            float64
	SellNotionalJPY           float64
	CycleBuyNotionalJPY       float64
	CycleSellNotionalJPY      float64
	TargetRestoringBuyJPY     float64
	TargetRestoringSellJPY    float64

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
	FastSellRestraint            float64
	SellRetention                float64
	UnrestrainedSellNotionalJPY  float64
}

func horizonFillProbability(ratePerHour float64, horizon time.Duration) float64 {
	if ratePerHour <= 0 || !inventoryProjectionFinite(ratePerHour) || horizon <= 0 {
		return 0
	}
	p := 1 - math.Exp(-ratePerHour*horizon.Hours())
	return math.Max(0, math.Min(1, p))
}

// probabilityCenteredTargetGross is the smallest two-sided executable gross
// that can deliver the requested expected inventory change. The baseline Fast
// policy spends capacity on target correction, not merely because a wide hard
// risk band exists. Terminal-path utility may promote above this baseline.
func probabilityCenteredTargetGross(
	current, desired, pBuy, pSell,
	minBuy, minSell, maxBuy, maxSell float64,
) float64 {
	if minBuy <= 0 && minSell <= 0 {
		return math.Max(0, maxBuy) + math.Max(0, maxSell)
	}
	buy := math.Min(maxBuy, minBuy)
	sell := math.Min(maxSell, minSell)
	delta := desired - current
	baselineDelta := pBuy*buy - pSell*sell
	if baselineDelta < delta && pBuy > 0 {
		buy = math.Min(maxBuy, buy+(delta-baselineDelta)/pBuy)
	} else if baselineDelta > delta && pSell > 0 {
		sell = math.Min(maxSell, sell+(baselineDelta-delta)/pSell)
	}
	return math.Max(0, buy) + math.Max(0, sell)
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
		!inventoryProjectionFinite(in.BuyFillProbability) ||
		!inventoryProjectionFinite(in.SellFillProbability) ||
		!inventoryProjectionFinite(in.BothFillProbability) ||
		!inventoryProjectionFinite(in.ConfidenceZScore) ||
		!inventoryProjectionFinite(in.TargetContraction) ||
		!inventoryProjectionFinite(in.FastBuyRestraint) ||
		!inventoryProjectionFinite(in.FastSellRestraint) {
		return d
	}

	d.BuyFillProbability = horizonFillProbability(in.BuyFillRatePerHour, in.Horizon)
	d.SellFillProbability = horizonFillProbability(in.SellFillRatePerHour, in.Horizon)
	if in.DirectFillProbabilities {
		d.BuyFillProbability = math.Max(0, math.Min(1, in.BuyFillProbability))
		d.SellFillProbability = math.Max(0, math.Min(1, in.SellFillProbability))
		lowerJoint := math.Max(0, d.BuyFillProbability+d.SellFillProbability-1)
		upperJoint := math.Min(d.BuyFillProbability, d.SellFillProbability)
		d.BothFillProbability = math.Max(lowerJoint, math.Min(upperJoint, in.BothFillProbability))
		d.FillCovariance = d.BothFillProbability - d.BuyFillProbability*d.SellFillProbability
	}
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
	if !in.FullRiskPromotion {
		maxGross = math.Min(maxGross, probabilityCenteredTargetGross(
			in.CurrentInventoryNotionalJPY, d.DesiredInventoryNotionalJPY,
			d.BuyFillProbability, d.SellFillProbability,
			minBuy, minSell, maxBuy, maxSell))
	}
	if maxGross <= 0 {
		return ProbabilityCenteredQuoteDecision{Reason: "no target-correcting gross capacity"}
	}
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
		allocation := inventoryNeutralCycleAllocation(
			gross,
			d.DesiredInventoryNotionalJPY-in.CurrentInventoryNotionalJPY,
			pBuy, pSell, minimumBuy, maximumBuy)
		buy, sell := allocation.BuyNotionalJPY, allocation.SellNotionalJPY
		expected := in.CurrentInventoryNotionalJPY + pBuy*buy - pSell*sell
		variance := pBuy*(1-pBuy)*buy*buy + pSell*(1-pSell)*sell*sell -
			2*d.FillCovariance*buy*sell
		stddev := math.Sqrt(math.Max(0, variance))

		c.ProjectedGrossNotionalJPY = gross
		c.BuyNotionalJPY = buy
		c.SellNotionalJPY = sell
		c.CycleBuyNotionalJPY = allocation.CycleBuyNotionalJPY
		c.CycleSellNotionalJPY = allocation.CycleSellNotionalJPY
		c.TargetRestoringBuyJPY = allocation.TargetRestoringBuyJPY
		c.TargetRestoringSellJPY = allocation.TargetRestoringSellJPY
		c.ExpectedInventoryNotionalJPY = expected
		c.InventoryVarianceJPY2 = variance
		c.InventoryStdDevJPY = stddev
		c.ConfidenceLowerNotionalJPY = expected - z*stddev
		c.ConfidenceUpperNotionalJPY = expected + z*stddev
		c.TargetErrorJPY = expected - target
		return c
	}
	constraintViolation := func(c ProbabilityCenteredQuoteDecision) float64 {
		const tolerance = 1e-7
		if !c.Enabled ||
			(c.BuyNotionalJPY > tolerance && c.BuyNotionalJPY+tolerance < minBuy) ||
			(c.SellNotionalJPY > tolerance && c.SellNotionalJPY+tolerance < minSell) {
			return math.Inf(1)
		}
		currentError := math.Abs(in.CurrentInventoryNotionalJPY - target)
		expectedError := math.Abs(c.ExpectedInventoryNotionalJPY - target)
		softHalfWidth := math.Max(target-in.LowerInventoryNotionalJPY,
			in.UpperInventoryNotionalJPY-target)
		softHalfWidth = math.Max(0, softHalfWidth)
		// One coherent second-moment budget jointly controls mean displacement
		// and Bernoulli fill variance:
		// E[(N_T-M)^2] <= (N-M)^2 + (softWidth/z)^2.
		// A separate |E[N_T]-M| <= |N-M| gate is intentionally absent: at the
		// target, asymmetric arrivals plus exchange minimums would otherwise make
		// every nonzero two-sided quote infeasible and activate an unmodelled
		// fallback.
		mseBudget := currentError*currentError + math.Pow(softHalfWidth/z, 2)
		mse := expectedError*expectedError + c.InventoryVarianceJPY2
		notionalScale := math.Max(1, math.Max(currentError, softHalfWidth))
		mseScale := math.Max(1, mseBudget)
		return math.Max(
			(math.Abs(c.ExpectedInventoryNotionalJPY-d.DesiredInventoryNotionalJPY)-softHalfWidth-tolerance)/notionalScale,
			(mse-mseBudget-tolerance)/mseScale,
		)
	}
	feasible := func(c ProbabilityCenteredQuoteDecision) bool { return constraintViolation(c) <= 0 }

	// Apply the directional posterior only after the coherent quantity split.
	// It attenuates exposure-increasing BUYs in bearish states and inventory-
	// reducing SELLs in bullish states. It does not restore the removed hard
	// one-sided gate: an affordable executable cell remains on each side.
	finalize := func(c ProbabilityCenteredQuoteDecision) ProbabilityCenteredQuoteDecision {
		if !c.Enabled {
			return c
		}
		buyRestraint := math.Max(0, math.Min(1, in.FastBuyRestraint))
		sellRestraint := math.Max(0, math.Min(1, in.FastSellRestraint))
		c.FastBuyRestraint = buyRestraint
		c.FastSellRestraint = sellRestraint
		c.BuyRetention = 1 - buyRestraint
		c.SellRetention = 1 - sellRestraint
		c.UnrestrainedBuyNotionalJPY = c.BuyNotionalJPY
		c.UnrestrainedSellNotionalJPY = c.SellNotionalJPY
		if buyRestraint <= 0 && sellRestraint <= 0 {
			return c
		}
		c.BuyNotionalJPY *= c.BuyRetention
		if c.BuyNotionalJPY > 0 && c.BuyNotionalJPY+1e-9 < minBuy {
			c.BuyNotionalJPY = minBuy
		}
		c.SellNotionalJPY *= c.SellRetention
		if c.SellNotionalJPY > 0 && c.SellNotionalJPY+1e-9 < minSell {
			c.SellNotionalJPY = minSell
		}
		c.ProjectedGrossNotionalJPY = c.BuyNotionalJPY + c.SellNotionalJPY
		allocation := decomposeInventoryNeutralCycle(
			c.BuyNotionalJPY, c.SellNotionalJPY, pBuy, pSell)
		c.CycleBuyNotionalJPY = allocation.CycleBuyNotionalJPY
		c.CycleSellNotionalJPY = allocation.CycleSellNotionalJPY
		c.TargetRestoringBuyJPY = allocation.TargetRestoringBuyJPY
		c.TargetRestoringSellJPY = allocation.TargetRestoringSellJPY
		expected := in.CurrentInventoryNotionalJPY +
			pBuy*c.BuyNotionalJPY - pSell*c.SellNotionalJPY
		variance := pBuy*(1-pBuy)*c.BuyNotionalJPY*c.BuyNotionalJPY +
			pSell*(1-pSell)*c.SellNotionalJPY*c.SellNotionalJPY -
			2*d.FillCovariance*c.BuyNotionalJPY*c.SellNotionalJPY
		stddev := math.Sqrt(math.Max(0, variance))
		c.ExpectedInventoryNotionalJPY = expected
		c.InventoryVarianceJPY2 = variance
		c.InventoryStdDevJPY = stddev
		c.ConfidenceLowerNotionalJPY = expected - z*stddev
		c.ConfidenceUpperNotionalJPY = expected + z*stddev
		c.TargetErrorJPY = expected - target
		c.Reason = "probability-centered+directional-side-restraint"
		return c
	}

	best := d
	full := candidate(maxGross)
	if feasible(full) {
		return finalize(full)
	}

	// Capacity clipping means feasibility need not begin at zero gross when the
	// current inventory lies outside the soft band. The former 32-point grid
	// could skip a narrow interval near the exchange minimum. Minimize the
	// continuous normalized violation, seed every capacity kink, then bisect the
	// upper feasible boundary. Work remains constant per quote.
	left := 0.0
	if minBuy > 0 && minSell > 0 && maxBuy >= minBuy && maxSell >= minSell {
		left = math.Min(maxGross, minBuy+minSell)
	}
	critical := []float64{
		left, maxGross, minBuy, minSell, minBuy + minSell,
		maxBuy, maxSell, maxBuy + minSell, maxSell + minBuy,
	}
	bestViolation := math.Inf(1)
	bestGross := left
	consider := func(gross float64) {
		gross = math.Max(left, math.Min(maxGross, gross))
		c := candidate(gross)
		violation := constraintViolation(c)
		if violation < bestViolation || (violation == bestViolation && gross > bestGross) {
			bestViolation, bestGross = violation, gross
			if feasible(c) {
				best = c
			}
		}
	}
	for _, gross := range critical {
		consider(gross)
	}
	a, b := left, maxGross
	const goldenRatio = 0.6180339887498948482
	x1 := b - goldenRatio*(b-a)
	x2 := a + goldenRatio*(b-a)
	v1 := constraintViolation(candidate(x1))
	v2 := constraintViolation(candidate(x2))
	for i := 0; i < 48; i++ {
		consider(x1)
		consider(x2)
		if v1 <= v2 {
			b, x2, v2 = x2, x1, v1
			x1 = b - goldenRatio*(b-a)
			v1 = constraintViolation(candidate(x1))
		} else {
			a, x1, v1 = x1, x2, v2
			x2 = a + goldenRatio*(b-a)
			v2 = constraintViolation(candidate(x2))
		}
	}
	consider((a + b) / 2)
	if bestViolation > 0 || !best.Enabled {
		d.Reason = "no feasible two-sided macro contraction"
		return d
	}
	low, high := bestGross, maxGross
	for i := 0; i < 48; i++ {
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
