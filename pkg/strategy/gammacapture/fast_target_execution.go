package gammacapture

import (
	"math"
	"time"
)

// FastTargetExecutionConfig enables statistically justified Fast-horizon
// maker-to-IOC conversion. It deliberately contains no fitted BPS threshold:
// the selected Fast horizon, its empirical touch posterior, and its terminal
// inventory-return posterior determine whether waiting is more expensive than
// crossing.
type FastTargetExecutionConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// FastTargetExecutionInput contains only causal state available at the quote
// decision. TouchProbability is the completed-window empirical posterior, not
// a Poisson arrival intensity.
type FastTargetExecutionInput struct {
	Now                    time.Time
	ModelUpdatedAt         time.Time
	LastExecutionModelAt   time.Time
	LastExecutionDirection int
	Horizon                time.Duration
	Direction              int
	CurrentInventoryBase   float64
	TargetInventoryBase    float64
	AvailableBase          float64
	AvailableQuote         float64
	BestBid                float64
	BestBidSize            float64
	BestAsk                float64
	BestAskSize            float64
	PassiveQuotePrice      float64
	// PassiveAvailable distinguishes a valid resting-maker alternative from an
	// authoritative maker rejection.  In the latter case the counterfactual is
	// to keep the inventory until H, not to pretend that a rejected quote fills.
	PassiveAvailable       bool
	TouchProbability       float64
	TouchStdError          float64
	InventoryReturnMeanBps float64
	// InventoryReturnSEBps is the causal standard error of the executable
	// directional return forecast. It is used only for the one-sided CE lower
	// bound; it is not folded into the inventory target itself.
	InventoryReturnSEBps          float64
	InventoryPredictiveSDBps      float64
	DirectionConfidence           float64
	PersistentDownsideActive      bool
	PersistentDownsideEValue      float64
	PersistentDownsideForecastBps float64
	PersistentUpsideActive        bool
	PersistentUpsideEValue        float64
	PersistentUpsideForecastBps   float64
	PairEquityJPY                 float64
	RiskAversion                  float64
	ConfidenceZScore              float64
	MakerFeeBps                   float64
	TakerFeeBps                   float64
	MinimumQuantityBase           float64
	MinimumNotionalJPY            float64
}

// FastTargetExecutionDecision is an auditable, depth-capped marketable IOC
// limit. Remaining target inventory is left to the ordinary Fast maker quote.
type FastTargetExecutionDecision struct {
	Trigger   bool
	Reason    string
	Direction int
	// ReferenceHorizonReady describes the anti-overlap maturity check for the
	// previous IOC decision. It is intentionally separate from evidence
	// readiness: a valid forecast can exist while a prior same-side decision is
	// still being observed.
	ReferenceHorizonReady bool
	ReferenceMaturityAt   time.Time
	// DecisionEvaluated is true only after the reference-horizon gate has
	// passed. False means the remaining CE/quantity fields were not evaluated;
	// their zero values must not be interpreted as measured zeros.
	DecisionEvaluated                 bool
	Quantity                          float64
	TargetGapBase                     float64
	ResidualMakerGapBase              float64
	DepthCapBase                      float64
	PassiveTouchProbability           float64
	PassiveTouchProbabilityUpper      float64
	PassiveMissProbabilityLower       float64
	ExpectedAdverseMoveBps            float64
	WaitLossBps                       float64
	PassiveToTouchCostBps             float64
	ProbabilityWeightedPassiveCostBps float64
	ExpectedExecutionFeeBps           float64
	ExecutionCostBps                  float64
	// FeeIncrementBps is retained as an audit field for older logs.  The
	// decision uses ExpectedExecutionFeeBps, which also handles no-maker/hold.
	FeeIncrementBps                  float64
	PersistentDownsideActive         bool
	PersistentDownsideEValue         float64
	PersistentDownsideForecastBps    float64
	PersistentUpsideActive           bool
	PersistentUpsideEValue           float64
	PersistentUpsideForecastBps      float64
	InventoryVariancePenaltyBps      float64
	ActiveCertaintyEquivalentMeanBps float64
	// ActiveCertaintyEquivalentBps is the conservative lower bound after the
	// confidence haircut, not an unbounded risk gradient.
	ActiveCertaintyEquivalentBps float64
	MaximumImpactBps             float64
	UrgentFraction               float64
	WorstPrice                   float64
}

// EvaluateFastTargetExecution compares a conservative lower bound on the
// same-horizon cost of missing the passive quote with the incremental cost of
// crossing. The one-sided upper confidence bound on maker-touch probability
// makes taking liquidity harder. Terminal-return uncertainty is represented
// by the existing posterior-predictive distribution; its direction confidence
// already shrinks the dynamic target, so a second mean/SE hypothesis gate would
// double-count the same uncertainty. Waiting regret is the positive part of
// the same-horizon predictive return: BUY loses when price rises before filling
// and SELL loses when price falls before filling.
func EvaluateFastTargetExecution(c FastTargetExecutionConfig, in FastTargetExecutionInput) FastTargetExecutionDecision {
	d := FastTargetExecutionDecision{
		Reason:                "disabled",
		Direction:             in.Direction,
		ReferenceHorizonReady: true,
	}
	if !c.Enabled {
		return d
	}
	if in.Now.IsZero() || in.ModelUpdatedAt.IsZero() || in.ModelUpdatedAt.After(in.Now) {
		d.Reason = "invalid Fast model time"
		return d
	}
	if in.Horizon <= 0 || in.BestBid <= 0 || in.BestAsk <= in.BestBid ||
		(in.PassiveAvailable && in.PassiveQuotePrice <= 0) {
		d.Reason = "invalid Fast execution prices or horizon"
		return d
	}
	// Populate the directional gap before the maturity gate. This keeps the
	// diagnostic useful when an otherwise valid decision is waiting for a
	// prior reference horizon.
	switch {
	case in.Direction > 0:
		d.TargetGapBase = in.TargetInventoryBase - in.CurrentInventoryBase
	case in.Direction < 0:
		d.TargetGapBase = in.CurrentInventoryBase - in.TargetInventoryBase
	}
	if !in.LastExecutionModelAt.IsZero() {
		maturity := in.LastExecutionModelAt.Add(in.Horizon)
		if in.Direction != 0 && in.Direction == in.LastExecutionDirection {
			// The first H observes the result of the prior impulse. A repeated
			// same-side impulse additionally needs one fresh, disjoint H; otherwise
			// overlapping windows repeatedly bet on nearly the same signal. An
			// opposite-side risk exit retains the original one-H maturity rule.
			maturity = maturity.Add(in.Horizon)
		}
		d.ReferenceMaturityAt = maturity
		if in.ModelUpdatedAt.Before(maturity) {
			d.ReferenceHorizonReady = false
			d.Reason = "prior Fast IOC reference horizon is unresolved"
			return d
		}
	}
	d.DecisionEvaluated = true
	if in.TouchProbability < 0 || in.TouchProbability > 1 || in.TouchStdError < 0 ||
		math.IsNaN(in.TouchProbability) || math.IsNaN(in.TouchStdError) {
		d.Reason = "invalid empirical touch posterior"
		return d
	}
	z := math.Max(0, in.ConfidenceZScore)
	d.PassiveTouchProbability = in.TouchProbability
	if in.PassiveAvailable {
		d.PassiveTouchProbabilityUpper = math.Min(1, in.TouchProbability+z*in.TouchStdError)
	}
	d.PassiveMissProbabilityLower = math.Max(0, 1-d.PassiveTouchProbabilityUpper)
	d.PersistentDownsideActive = in.PersistentDownsideActive
	d.PersistentDownsideEValue = in.PersistentDownsideEValue
	d.PersistentDownsideForecastBps = math.Max(0, in.PersistentDownsideForecastBps)
	d.PersistentUpsideActive = in.PersistentUpsideActive
	d.PersistentUpsideEValue = in.PersistentUpsideEValue
	d.PersistentUpsideForecastBps = math.Max(0, in.PersistentUpsideForecastBps)

	visibleDepth, touchPrice := 0.0, 0.0
	directionalMean := float64(in.Direction) * in.InventoryReturnMeanBps
	switch {
	case in.Direction > 0:
		d.TargetGapBase = in.TargetInventoryBase - in.CurrentInventoryBase
		visibleDepth, touchPrice = in.BestAskSize, in.BestAsk
		if d.TargetGapBase <= 0 {
			d.Reason = "bullish Fast target gap is absent"
			return d
		}
		if in.PassiveAvailable && in.PassiveQuotePrice > in.BestBid {
			d.Reason = "Fast BUY benchmark is not a passive quote"
			return d
		}
		if in.PassiveAvailable {
			d.PassiveToTouchCostBps = math.Log(touchPrice/in.PassiveQuotePrice) * 10_000
		}
	case in.Direction < 0:
		d.TargetGapBase = in.CurrentInventoryBase - in.TargetInventoryBase
		visibleDepth, touchPrice = in.BestBidSize, in.BestBid
		if d.TargetGapBase <= 0 {
			d.Reason = "bearish Fast target gap is absent"
			return d
		}
		if in.PassiveAvailable && in.PassiveQuotePrice < in.BestAsk {
			d.Reason = "Fast SELL benchmark is not a passive quote"
			return d
		}
		if in.PassiveAvailable {
			d.PassiveToTouchCostBps = math.Log(in.PassiveQuotePrice/touchPrice) * 10_000
		}
	default:
		d.Reason = "Fast target direction is neutral"
		return d
	}
	if visibleDepth <= 0 {
		d.Reason = "opposite-side visible depth unavailable"
		return d
	}
	if math.IsNaN(in.DirectionConfidence) || math.Abs(in.DirectionConfidence) > 1 {
		d.Reason = "invalid Fast posterior direction confidence"
		return d
	}
	if in.InventoryPredictiveSDBps < 0 || math.IsNaN(in.InventoryPredictiveSDBps) {
		d.Reason = "invalid Fast predictive dispersion"
		return d
	}
	if in.InventoryReturnSEBps < 0 || math.IsNaN(in.InventoryReturnSEBps) || math.IsInf(in.InventoryReturnSEBps, 0) {
		d.Reason = "invalid Fast return standard error"
		return d
	}
	if in.InventoryPredictiveSDBps > 0 {
		zReturn := directionalMean / in.InventoryPredictiveSDBps
		phi := math.Exp(-0.5*zReturn*zReturn) / math.Sqrt(2*math.Pi)
		cdf := 0.5 * (1 + math.Erf(zReturn/math.Sqrt2))
		d.ExpectedAdverseMoveBps = in.InventoryPredictiveSDBps*phi + directionalMean*cdf
	} else {
		d.ExpectedAdverseMoveBps = math.Max(0, directionalMean)
	}
	// Persistent slow-decline evidence is estimated from executable bids in
	// QV time.  It may increase the SELL-side adverse-move forecast, but it
	// never creates a target gap or changes BUY state, so inventory ownership
	// remains in the single Fast posterior model.
	if in.Direction < 0 && in.PersistentDownsideActive {
		d.ExpectedAdverseMoveBps = math.Max(
			d.ExpectedAdverseMoveBps, d.PersistentDownsideForecastBps)
	}
	if in.Direction > 0 && in.PersistentUpsideActive {
		d.ExpectedAdverseMoveBps = math.Max(
			d.ExpectedAdverseMoveBps, d.PersistentUpsideForecastBps)
	}
	if in.PassiveAvailable && d.ExpectedAdverseMoveBps <= 0 {
		d.Reason = "Fast expected adverse move is absent"
		return d
	}
	d.WaitLossBps = d.PassiveMissProbabilityLower * d.ExpectedAdverseMoveBps
	d.FeeIncrementBps = in.TakerFeeBps - in.MakerFeeBps
	// IOC-now is compared with the complete maker-or-hold counterfactual.  A
	// passive price improvement and maker fee exist only on the pTouch branch;
	// the miss branch pays no execution fee and keeps the inventory to H.
	d.ProbabilityWeightedPassiveCostBps =
		d.PassiveTouchProbabilityUpper * d.PassiveToTouchCostBps
	d.ExpectedExecutionFeeBps = in.TakerFeeBps -
		d.PassiveTouchProbabilityUpper*in.MakerFeeBps
	d.ExecutionCostBps = d.ProbabilityWeightedPassiveCostBps + d.ExpectedExecutionFeeBps
	d.MaximumImpactBps = d.WaitLossBps - d.ExecutionCostBps
	if in.PassiveAvailable && (d.MaximumImpactBps <= 0 || d.WaitLossBps <= 0) {
		d.Reason = "Fast passive wait loss does not exceed crossing cost"
		return d
	}
	if in.PassiveAvailable {
		d.UrgentFraction = d.PassiveMissProbabilityLower *
			math.Min(1, d.MaximumImpactBps/d.WaitLossBps)
	} else {
		// With no admissible maker quote, compare IOC directly with holding the
		// current inventory.  Do not require the target-correction direction to
		// repeat the sign of the return forecast: that forecast already moved the
		// target and enters the certainty equivalent below.  Requiring both is a
		// duplicate hard gate that can strand an extreme inventory position.
		d.UrgentFraction = 1
	}
	d.DepthCapBase = visibleDepth
	// Estimate the marginal portfolio CE for every impulse, including when a
	// passive quote exists. Previously passive actions bypassed this calculation,
	// so a risk-gradient value could trigger an IOC with no positive execution
	// value. The signed variance term is intentional: reducing inventory risk is
	// a benefit (negative penalty), while adding risk is a cost.
	if in.PairEquityJPY <= 0 || math.IsNaN(in.PairEquityJPY) || math.IsInf(in.PairEquityJPY, 0) {
		d.Reason = "Fast active certainty equivalent lacks pair equity"
		return d
	}
	setQuantity := func(impactBps float64) {
		impactBps = math.Max(0, impactBps)
		if in.Direction > 0 {
			d.WorstPrice = touchPrice * math.Exp(impactBps/10_000)
			urgentQuantity := d.TargetGapBase * d.UrgentFraction
			balanceCap := in.AvailableQuote / d.WorstPrice
			d.Quantity = math.Min(urgentQuantity, math.Min(visibleDepth, balanceCap))
		} else {
			d.WorstPrice = touchPrice * math.Exp(-impactBps/10_000)
			urgentQuantity := d.TargetGapBase * d.UrgentFraction
			d.Quantity = math.Min(urgentQuantity, math.Min(visibleDepth, in.AvailableBase))
		}
		d.ResidualMakerGapBase = math.Max(0, d.TargetGapBase-d.Quantity)
	}
	computeCE := func() float64 {
		notionalJPY := d.Quantity * touchPrice
		if notionalJPY <= 0 || math.IsNaN(notionalJPY) || math.IsInf(notionalJPY, 0) {
			return math.Inf(-1)
		}
		inventoryDeviationJPY := (in.CurrentInventoryBase - in.TargetInventoryBase) * touchPrice
		inventoryDeltaJPY := float64(in.Direction) * notionalJPY
		marginalVarianceJPY2 :=
			((inventoryDeviationJPY+inventoryDeltaJPY)*(inventoryDeviationJPY+inventoryDeltaJPY) -
				inventoryDeviationJPY*inventoryDeviationJPY) *
				in.InventoryPredictiveSDBps * in.InventoryPredictiveSDBps / 100_000_000
		d.InventoryVariancePenaltyBps = math.Max(0, in.RiskAversion) *
			marginalVarianceJPY2 / (2 * in.PairEquityJPY) / notionalJPY * 10_000
		d.ActiveCertaintyEquivalentMeanBps = directionalMean - in.TakerFeeBps - d.InventoryVariancePenaltyBps
		return d.ActiveCertaintyEquivalentMeanBps - math.Max(0, in.ConfidenceZScore)*in.InventoryReturnSEBps
	}
	setQuantity(d.MaximumImpactBps)
	if d.Quantity <= 0 || d.Quantity < in.MinimumQuantityBase ||
		d.Quantity*touchPrice < in.MinimumNotionalJPY {
		d.Reason = "Fast active quantity is below exchange minimum"
		return d
	}
	ceLower := computeCE()
	d.ActiveCertaintyEquivalentBps = ceLower
	if ceLower <= 0 || math.IsNaN(ceLower) || math.IsInf(ceLower, 0) {
		d.Reason = "Fast active certainty equivalent is nonpositive"
		return d
	}
	if in.PassiveAvailable {
		// A passive quote can justify a smaller crossing budget, but it cannot
		// bypass the same terminal-wealth lower bound. Recompute quantity after
		// capping impact and then recompute CE at that final quantity.
		d.MaximumImpactBps = math.Min(math.Max(0, d.MaximumImpactBps), ceLower)
		if d.WaitLossBps > 0 {
			d.UrgentFraction = d.PassiveMissProbabilityLower *
				math.Min(1, d.MaximumImpactBps/d.WaitLossBps)
		}
		setQuantity(d.MaximumImpactBps)
		if d.Quantity <= 0 || d.Quantity < in.MinimumQuantityBase ||
			d.Quantity*touchPrice < in.MinimumNotionalJPY {
			d.Reason = "Fast active quantity is below exchange minimum"
			return d
		}
		ceLower = computeCE()
		d.ActiveCertaintyEquivalentBps = ceLower
		if ceLower <= 0 || math.IsNaN(ceLower) || math.IsInf(ceLower, 0) {
			d.Reason = "Fast active certainty equivalent is nonpositive"
			return d
		}
	} else {
		// With no admissible maker quote, the CE lower bound is the maximum
		// adverse execution impact the impulse can pay relative to holding.
		d.MaximumImpactBps = ceLower
		setQuantity(d.MaximumImpactBps)
		if d.Quantity <= 0 || d.Quantity < in.MinimumQuantityBase ||
			d.Quantity*touchPrice < in.MinimumNotionalJPY {
			d.Reason = "Fast active quantity is below exchange minimum"
			return d
		}
		ceLower = computeCE()
		d.ActiveCertaintyEquivalentBps = ceLower
		if ceLower <= 0 || math.IsNaN(ceLower) || math.IsInf(ceLower, 0) {
			d.Reason = "Fast active certainty equivalent is nonpositive"
			return d
		}
	}
	d.Trigger = true
	if in.PassiveAvailable {
		d.Reason = "Fast expected wait loss exceeds probability-weighted maker-or-hold cost"
	} else {
		d.Reason = "Fast portfolio certainty equivalent favors IOC over hold after maker rejection"
	}
	return d
}
