package gammacapture

import (
	"fmt"
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// JointDistanceQuantityInput contains only causal state available at quote
// time. The ordinary distance ladder moves outward from the unified Fast quote.
// When conditional execution is enabled, symmetric one-dimensional passive
// inward ladders are evaluated by the same crossing, terminal-wealth, quantity,
// and confidence model rather than by post-optimizer price skews.
type JointDistanceQuantityInput struct {
	Now     time.Time
	Horizon time.Duration
	// HorizonCandidates is the Cartesian horizon axis of the final
	// (horizon, distance, quantity) optimization.  An empty slice preserves the
	// single-horizon API used by focused research and tests.
	HorizonCandidates []time.Duration
	BestBid           float64
	BestAsk           float64
	MidPrice          float64
	BasePlan          MarketMakerQuotePlan
	FastDirection     float64
	Projection        ProbabilityCenteredQuoteInput
	ConfidenceZScore  float64
	PairEquityJPY     float64
	RiskAversion      float64
	// Raw account-backed capacities are kept separate from Projection.Max*,
	// which also contains model and inventory-risk caps. They let the Bellman
	// boundary handler distinguish an actual balance boundary from a side that
	// the ordinary quote policy intentionally suppressed.
	AvailableBuyCapitalJPY            float64
	AvailableSellInventoryNotionalJPY float64
	// CompletionSide is the opposite side selected by the conditional
	// post-fill model.  It has already been evaluated against the sunk first
	// fill and therefore must not be vetoed by the unconditional two-leg
	// admission pass below.  Exchange capacity and the hard inventory band are
	// still enforced through Projection.Min/Max*NotionalJPY.
	CompletionSide types.SideType
	// RelativeHoldRisk is the only optional integration point for the
	// same-symbol Hold-relative state. The optimizer consumes its scalar
	// utility once per candidate; it never turns it into a side gate.
	RelativeHoldRisk RelativeHoldRiskInput
}

type RelativeHoldRiskInput struct {
	Enabled               bool
	ShadowOnly            bool
	State                 RelativeHoldRiskState
	TrackingErrorAversion float64
	DownsideBetaAversion  float64
	TotalBetaAversion     float64
}

type JointDistanceQuantityDecision struct {
	Enabled                bool
	Applied                bool
	AuthoritativeRejection bool
	SideSafeFallback       bool
	// ContinuityFloorApplied distinguishes a bounded two-sided market-making
	// quote from a terminal-path-positive allocation.  It is deliberately not
	// treated as evidence that expected terminal PnL is positive.
	ContinuityFloorApplied bool
	ContinuityFloorReason  string
	FallbackBuySupported   bool
	FallbackSellSupported  bool
	// Fallback*Score/Plan retain the independent side candidate when the
	// bilateral optimizer rejects the pair.  Without these fields the caller
	// could only observe "authoritative rejection" and would incorrectly turn
	// an individually positive side into a two-sided no-order decision.
	FallbackBuyScore                    float64
	FallbackSellScore                   float64
	FallbackBuyPlan                     MarketMakerQuotePlan
	FallbackSellPlan                    MarketMakerQuotePlan
	Reason                              string
	Plan                                MarketMakerQuotePlan
	Projection                          ProbabilityCenteredQuoteDecision
	Crossing                            MarketMakerHorizonDecision
	CandidateCount                      int
	SelectedCandidate                   int
	SelectedQuantityCandidate           int
	QuantityScale                       float64
	ExpectedCycleJPY                    float64
	ExpectedPnLJPYHour                  float64
	LowerPnLJPYHour                     float64
	PathStdErrorJPYHour                 float64
	KellyPenaltyJPYHour                 float64
	KellyUtilityJPYHour                 float64
	FeeValueMeanJPY                     float64
	FeeValueDownsideRegretJPY           float64
	FeeValueNetJPY                      float64
	PathPositiveConfidence              float64
	PathEffectiveSamples                float64
	PathEffectiveSamplesBaseline        float64
	PathEffectiveSamplesBaselineStd     float64
	PathDecayHalfLifeSeconds            float64
	PathDecayAutocorrelation            float64
	PathDecayPersistenceObservations    float64
	PathMaturityReady                   bool
	PathMaturityReason                  string
	PathMaturityConfidenceHalfWidthBps  float64
	PathMaturityReferenceScaleBps       float64
	PathMaturityRelativeHalfWidth       float64
	CapitalUtilization                  float64
	PairCapitalUtilization              float64
	ExistingInventoryExpectedPnLJPY     float64
	BaselineVarianceJPY2                float64
	WholePositionVarianceJPY2           float64
	MarginalVarianceJPY2                float64
	InventoryOrderCovarianceJPY2        float64
	RiskReducing                        bool
	DownsideBuyCapApplied               bool
	DownsideInventoryReturnMeanBps      float64
	DownsideInventoryReturnSEBps        float64
	DownsideInventoryReturnUpperBps     float64
	DownsideEffectiveSamples            float64
	DownsideOriginalMaxBuyJPY           float64
	DownsideMinimumBuyEvaluated         bool
	DownsideMinimumBuyCEJPY             float64
	BuyAdmissionEvaluated               bool
	BuyAdmissionApplied                 bool
	BuyAdmissionMaximumJPY              float64
	BuyAdmissionUtilityBoundJPY         float64
	BuyAdmissionReason                  string
	SellAdmissionEvaluated              bool
	SellAdmissionApplied                bool
	SellAdmissionMaximumJPY             float64
	SellAdmissionUtilityBoundJPY        float64
	SellAdmissionReason                 string
	AdmissionJointCEJPY                 float64
	AdmissionJointRobustCEJPY           float64
	AdmissionJointComplementary         bool
	InwardBuyEligible                   bool
	InwardSellEligible                  bool
	InwardBuySelected                   bool
	InwardSellSelected                  bool
	SelectedInwardBuyDeltaBps           float64
	SelectedInwardSellDeltaBps          float64
	ConditionalBuy                      ConditionalExecutionSideDecision
	ConditionalSell                     ConditionalExecutionSideDecision
	CompletionProtected                 bool
	PairedDistanceEvaluated             bool
	PairedDistanceMeanBps               float64
	PairedDistanceStdErrorBps           float64
	PairedDistanceLowerBps              float64
	HorizonCandidateCount               int
	HorizonEffectiveSamples             float64
	HorizonReliability                  float64
	HorizonRawUtilityJPYHour            float64
	HorizonSelectionUtilityJPYHour      float64
	RelativeHoldUtilityJPYHour          float64
	RelativeHoldTrackingPenaltyJPYHour  float64
	RelativeHoldDownsidePenaltyJPYHour  float64
	RelativeHoldTotalBetaPenaltyJPYHour float64
	RelativeHoldCVaRShadowLossJPY       float64
	RelativeHoldStateReady              bool
	RelativeHoldDownsideReady           bool
}

// targetCEAuthoritativeRejection reports whether the terminal target-relative
// CE has enough completed path evidence to veto the ordinary Fast plan. A
// missing or immature CE is a data-collection state, not a no-order decision:
// suppressing the base quote there prevents the private fills needed to
// calibrate the execution model and creates a bootstrap deadlock.
func targetCEAuthoritativeRejection(d JointDistanceQuantityDecision) bool {
	return d.AuthoritativeRejection && d.PathMaturityReady
}

// targetRestoringPlanAfterJointRejection converts a mature rejection of the
// complete BUY/SELL allocation into a side-aware maker plan.  A complete-path
// rejection is not, by itself, a rejection of both individual sides: when the
// account is above target, the existing SELL is an inventory-risk action; when
// it is below target, the BUY is the corresponding action. The side-level
// fallback support flags decide whether that corrective side survives. The
// other side is deliberately disabled because it would increase the current
// target error.
//
// This helper is used only for an authoritative *rejection* where the joint
// optimizer did not return an applied plan.  An applied joint plan remains the
// source of truth and may explicitly clear SELL (or BUY) through its AllowAsk
// / AllowBid flags.
func targetRestoringPlanAfterJointRejection(
	base MarketMakerQuotePlan, currentInventoryBase, targetInventoryBase float64,
	allowBuy, allowSell bool,
) MarketMakerQuotePlan {
	plan := base
	tolerance := math.Max(1e-12, math.Abs(targetInventoryBase)*1e-12)
	switch {
	case currentInventoryBase > targetInventoryBase+tolerance:
		plan.AllowBid = false
		plan.BidQuoteNotional = 0
		if !allowSell {
			plan.AllowAsk = false
			plan.AskQuoteNotional = 0
		}
	case currentInventoryBase < targetInventoryBase-tolerance:
		plan.AllowAsk = false
		plan.AskQuoteNotional = 0
		if !allowBuy {
			plan.AllowBid = false
			plan.BidQuoteNotional = 0
		}
	default:
		plan.AllowBid = false
		plan.AllowAsk = false
		plan.BidQuoteNotional = 0
		plan.AskQuoteNotional = 0
	}
	return plan
}

func jointRelativeHoldUtility(in JointDistanceQuantityInput, buyNotionalJPY, sellNotionalJPY float64) RelativeHoldUtility {
	risk := in.RelativeHoldRisk
	if !risk.Enabled || risk.ShadowOnly || !risk.State.Ready || in.PairEquityJPY <= 0 {
		return RelativeHoldUtility{Reason: "relative-hold utility disabled or immature"}
	}
	buyProbability := math.Max(0, math.Min(1, in.Projection.BuyFillProbability))
	sellProbability := math.Max(0, math.Min(1, in.Projection.SellFillProbability))
	currentBeta := in.Projection.CurrentInventoryNotionalJPY / in.PairEquityJPY
	projectedBeta := (in.Projection.CurrentInventoryNotionalJPY +
		buyProbability*math.Max(0, buyNotionalJPY) -
		sellProbability*math.Max(0, sellNotionalJPY)) / in.PairEquityJPY
	return risk.State.EvaluateAction(RelativeHoldAction{
		RiskWeight:             math.Max(0, buyNotionalJPY+sellNotionalJPY) / in.PairEquityJPY,
		PairEquityJPY:          in.PairEquityJPY,
		TrackingErrorAversion:  risk.TrackingErrorAversion,
		DownsideBetaAversion:   risk.DownsideBetaAversion,
		TotalBetaAversion:      risk.TotalBetaAversion,
		CurrentInventoryBeta:   currentBeta,
		ProjectedInventoryBeta: projectedBeta,
		InventoryBetaSupplied:  true,
	})
}

func recordJointRelativeHoldDiagnostics(d *JointDistanceQuantityDecision, utility RelativeHoldUtility, in JointDistanceQuantityInput) {
	if d == nil {
		return
	}
	d.RelativeHoldUtilityJPYHour = utility.NetJPYPerHour
	d.RelativeHoldTrackingPenaltyJPYHour = utility.TrackingErrorPenaltyJPYPerHour
	d.RelativeHoldDownsidePenaltyJPYHour = utility.DownsideBetaPenaltyJPYPerHour
	d.RelativeHoldTotalBetaPenaltyJPYHour = utility.TotalBetaPenaltyJPYPerHour
	d.RelativeHoldCVaRShadowLossJPY = utility.CVaRShadowLossJPY
	d.RelativeHoldStateReady = in.RelativeHoldRisk.State.Ready
	d.RelativeHoldDownsideReady = in.RelativeHoldRisk.State.DownsideReady
}

func setJointPathDecayDiagnostics(d *JointDistanceQuantityDecision, stats JointPathPayoffStats) {
	if d == nil {
		return
	}
	d.PathEffectiveSamples = stats.EffectiveSamples
	d.PathEffectiveSamplesBaseline = stats.EffectiveSamplesBaseline
	d.PathEffectiveSamplesBaselineStd = stats.EffectiveSamplesBaselineStd
	d.PathDecayHalfLifeSeconds = stats.PathDecayHalfLifeSeconds
	d.PathDecayAutocorrelation = stats.PathDecayAutocorrelation
	d.PathDecayPersistenceObservations = stats.PathDecayPersistenceObservations
}

func inwardDistanceImprovementSupported(
	d ConditionalExecutionSideDecision, simultaneousZ float64,
) bool {
	return d.Evaluated && d.EffectiveSamples > 1 &&
		!math.IsNaN(d.ExpectedPairedDeltaBps) &&
		!math.IsNaN(d.PairedStdErrorBps) &&
		d.ExpectedPairedDeltaBps-
			math.Max(0, simultaneousZ)*d.PairedStdErrorBps > 0
}

// TargetRestoringSideRealignment compares a resting one-sided balance-boundary
// quote with the current inward candidate in the same target-relative terminal
// wealth model. The inactive completion leg is held fixed by the caller, so
// the difference is solely the value of renewing the executable side. Both
// candidates pay their own markout, posterior standard-error and whole-position
// covariance penalties; no fitted BPS chase threshold is required.
func TargetRestoringSideRealignment(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	now time.Time,
	horizon time.Duration,
	bestBid, bestAsk float64,
	candidateBid, candidateAsk float64,
	activeBid, activeAsk float64,
	buy bool,
	currentInventoryNotionalJPY, targetInventoryNotionalJPY,
	orderNotionalJPY, pairEquityJPY, riskAversion, confidenceZScore float64,
) (realign bool, candidateCEJPY, activeCEJPY float64) {
	if model == nil || now.IsZero() || horizon <= 0 || bestBid <= 0 || bestAsk <= bestBid ||
		candidateBid <= 0 || candidateAsk <= candidateBid || activeBid <= 0 || activeAsk <= activeBid ||
		orderNotionalJPY <= 0 || pairEquityJPY <= 0 {
		return false, 0, 0
	}
	if buy {
		if currentInventoryNotionalJPY >= targetInventoryNotionalJPY || candidateBid <= activeBid {
			return false, 0, 0
		}
	} else if currentInventoryNotionalJPY <= targetInventoryNotionalJPY || candidateAsk >= activeAsk {
		return false, 0, 0
	}
	candidateBuyDistance, candidateSellDistance, _ := MakerTouchDistances(
		bestBid, bestAsk, candidateBid, candidateAsk)
	activeBuyDistance, activeSellDistance, _ := MakerTouchDistances(
		bestBid, bestAsk, activeBid, activeAsk)
	candidateStats := model.JointPathPayoffStatistics(
		now, config, horizon, candidateBuyDistance, candidateSellDistance)
	activeStats := model.JointPathPayoffStatistics(
		now, config, horizon, activeBuyDistance, activeSellDistance)
	if candidateStats.EffectiveSamples <= 1 || activeStats.EffectiveSamples <= 1 {
		return false, 0, 0
	}
	if riskAversion <= 0 {
		riskAversion = fastRiskAversionOrDefault(config, riskAversion)
	}
	if confidenceZScore <= 0 {
		confidenceZScore = config.InventoryRiskZScore
	}
	buyNotional, sellNotional := 0.0, orderNotionalJPY
	if buy {
		buyNotional, sellNotional = orderNotionalJPY, 0
	}
	candidate := candidateStats.EvaluateTargetRelativePosition(
		currentInventoryNotionalJPY, targetInventoryNotionalJPY,
		buyNotional, sellNotional, pairEquityJPY, riskAversion, confidenceZScore)
	active := activeStats.EvaluateTargetRelativePosition(
		currentInventoryNotionalJPY, targetInventoryNotionalJPY,
		buyNotional, sellNotional, pairEquityJPY, riskAversion, confidenceZScore)
	return candidate.CertaintyEquivalent > active.CertaintyEquivalent+1e-12,
		candidate.CertaintyEquivalent, active.CertaintyEquivalent
}

func positiveExecutableUnit(values ...float64) float64 {
	unit := 0.0
	for _, value := range values {
		if value > unit && inventoryProjectionFinite(value) {
			unit = value
		}
	}
	return unit
}

func applyQuotePlanSide(target *MarketMakerQuotePlan, source MarketMakerQuotePlan, buy bool) {
	if buy {
		target.BidPrice = source.BidPrice
		target.BidDistanceBps = source.BidDistanceBps
		target.BidHalfSpreadBps = source.BidHalfSpreadBps
		target.BidTouchDistanceBps = source.BidTouchDistanceBps
		return
	}
	target.AskPrice = source.AskPrice
	target.AskDistanceBps = source.AskDistanceBps
	target.AskHalfSpreadBps = source.AskHalfSpreadBps
	target.AskTouchDistanceBps = source.AskTouchDistanceBps
}

func posteriorRiskSizedSideCap(
	stats JointPathPayoffStats,
	buy bool,
	hardCapJPY, executableUnitJPY, currentInventoryNotionalJPY, targetInventoryNotionalJPY,
	pairEquityJPY, riskAversion, confidenceZScore float64,
) float64 {
	if hardCapJPY <= 0 || executableUnitJPY <= 0 || pairEquityJPY <= 0 {
		return 0
	}
	evaluate := func(notional, zScore float64) JointPathPayoffDecision {
		if buy {
			return stats.EvaluateTargetRelativePosition(
				currentInventoryNotionalJPY, targetInventoryNotionalJPY,
				notional, 0, pairEquityJPY, riskAversion, zScore)
		}
		return stats.EvaluateTargetRelativePosition(
			currentInventoryNotionalJPY, targetInventoryNotionalJPY,
			0, notional, pairEquityJPY, riskAversion, zScore)
	}
	unit := math.Min(hardCapJPY, executableUnitJPY)
	if unit+1e-9 < executableUnitJPY ||
		evaluate(unit, confidenceZScore).CertaintyEquivalent <= 0 {
		return 0
	}

	// Expected terminal utility is concave in a one-sided order size: expected
	// payoff is linear while the whole-position variance penalty is quadratic.
	// Find the largest size whose posterior lower utility remains positive. The
	// previous implementation discarded confidenceZScore and therefore promoted
	// sparse, noisy paths using only their unadjusted sample mean.
	robustCap := hardCapJPY
	if evaluate(hardCapJPY, confidenceZScore).CertaintyEquivalent <= 0 {
		lower, upper := unit, hardCapJPY
		for iteration := 0; iteration < 40; iteration++ {
			mid := 0.5 * (lower + upper)
			if evaluate(mid, confidenceZScore).CertaintyEquivalent > 0 {
				lower = mid
			} else {
				upper = mid
			}
		}
		robustCap = lower
	}
	raw := evaluate(robustCap, 0)
	confidence := jointPathUtilityConfidence(raw)
	scale := 1.0
	if raw.KellyPenaltyJPY > 0 {
		scale = math.Min(1, raw.ExpectedPnLJPY/(2*raw.KellyPenaltyJPY))
	}
	capJPY := math.Min(robustCap, robustCap*scale*confidence)
	if capJPY+1e-9 >= executableUnitJPY {
		return capJPY
	}
	return unit
}

// jointPathPositiveConfidence is the posterior expected sign of a positive
// path mean under a locally normal mean posterior. It is zero at no directional
// evidence and approaches one continuously; unlike a significance gate, it
// does not strand every sparse-market quote at the exchange minimum.
func jointPathPositiveConfidence(mean, standardError float64) float64 {
	if mean <= 0 || math.IsNaN(mean) || math.IsNaN(standardError) {
		return 0
	}
	if standardError <= 0 {
		return 1
	}
	return math.Max(0, math.Min(1, math.Erf(mean/(standardError*math.Sqrt2))))
}

// jointPathUtilityConfidence measures support for positive marginal
// whole-position utility. A negative Kelly penalty is a genuine covariance
// benefit from reducing existing inventory risk, not a value to clamp away.
func jointPathUtilityConfidence(d JointPathPayoffDecision) float64 {
	riskAdjustedMean := d.ExpectedPnLJPY - d.KellyPenaltyJPY
	return jointPathPositiveConfidence(riskAdjustedMean, d.StdErrorJPY)
}

// fastFeeNetRegretValue is the conservative Fast admission value used away
// from a balance boundary. Fees, markout and target-relative inventory risk are
// already present in the decision; the lower partial moment prevents a noisy
// positive point estimate from authorizing ordinary churn.
func fastFeeNetRegretValue(d JointPathPayoffDecision) (mean, downsideRegret, net float64) {
	mean = d.ExpectedPnLJPY - d.KellyPenaltyJPY
	standardError := math.Max(0, d.StdErrorJPY)
	if math.IsNaN(mean) || math.IsInf(mean, 0) || math.IsNaN(standardError) ||
		math.IsInf(standardError, 0) {
		return 0, 0, math.Inf(-1)
	}
	if standardError <= 0 {
		return mean, math.Max(0, -mean), mean - math.Max(0, -mean)
	}
	z := mean / standardError
	phi := math.Exp(-0.5*z*z) / math.Sqrt(2*math.Pi)
	negativeProbability := 0.5 * math.Erfc(z/math.Sqrt2)
	downsideRegret = math.Max(0, standardError*phi-mean*negativeProbability)
	return mean, downsideRegret, mean - downsideRegret
}

// targetRelativeCEAdmissionValue is the authoritative mature terminal-path
// value for an action that is compared with submitting no new order.  The
// corrected target-relative CE uses the paired whole-position versus baseline
// confidence width.  Reusing fastFeeNetRegretValue here would substitute the
// incremental order-payoff SE and would discard the covariance benefit of a
// target-restoring action (or double-count risk already carried by inventory).
// The legacy lower-partial-moment value remains available for diagnostics and
// for explicitly incomplete/legacy callers; it must not own this gate.
func targetRelativeCEAdmissionValue(raw, confidence JointPathPayoffDecision) (mean, downside, net float64) {
	mean = raw.ExpectedPnLJPY - raw.KellyPenaltyJPY
	net = confidence.CertaintyEquivalent
	if math.IsNaN(mean) || math.IsInf(mean, 0) || math.IsNaN(net) || math.IsInf(net, 0) {
		return 0, 0, math.Inf(-1)
	}
	downside = math.Max(0, mean-net)
	return mean, downside, net
}

// sideSafeFallbackAdmission keeps a bilateral rejection from deleting a
// one-sided hedge.  Ordinary one-sided candidates still need positive
// fee-net lower-partial-moment value.  A target-restoring candidate may use
// the robust certainty equivalent instead: its negative local markout can be
// offset by the measured reduction in whole-position variance, but only when
// the confidence-adjusted terminal wealth remains positive.  This is a
// risk-budget identity, not a fixed BPS exception.
func sideSafeFallbackAdmission(feeNet float64, robust JointPathPayoffDecision) (score float64, admitted bool, riskReducing bool) {
	if feeNet > 0 && !math.IsNaN(feeNet) && !math.IsInf(feeNet, 0) {
		return feeNet, true, false
	}
	// EvaluateTargetRelativePosition already measures the action against no
	// order around the same inventory target.  Adding a separate target-progress
	// continuation here would count the same inventory deviation twice.
	robustScore := robust.CertaintyEquivalent
	if robust.RiskReducing && robustScore > 0 && !math.IsNaN(robustScore) && !math.IsInf(robustScore, 0) {
		return robustScore, true, true
	}
	return feeNet, false, false
}

func sideSafeFallbackAdmissionForReplay(
	feeNet float64, robust JointPathPayoffDecision,
	legacyContinuation float64, legacyReplay bool,
) (score float64, admitted bool, riskReducing bool) {
	if score, admitted, riskReducing = sideSafeFallbackAdmission(feeNet, robust); admitted || !legacyReplay {
		return score, admitted, riskReducing
	}
	legacyScore := robust.CertaintyEquivalent + legacyContinuation
	if robust.RiskReducing && legacyScore > 0 &&
		!math.IsNaN(legacyScore) && !math.IsInf(legacyScore, 0) {
		return legacyScore, true, true
	}
	return feeNet, false, false
}

// fastUnmatchedTouchShare is the empirical share of side touches that do not
// complete their opposite leg within the same Fast horizon:
//
//	1 - 2 P(B and A) / (P(B) + P(A)).
func fastUnmatchedTouchShare(
	buyTouchProbability, sellTouchProbability, bothTouchProbability float64,
) float64 {
	buyTouchProbability = math.Max(0, buyTouchProbability)
	sellTouchProbability = math.Max(0, sellTouchProbability)
	bothTouchProbability = math.Max(0, bothTouchProbability)
	denominator := buyTouchProbability + sellTouchProbability
	if denominator <= 0 {
		return 1
	}
	return math.Max(0, math.Min(1,
		1-2*bothTouchProbability/denominator))
}

// fastCandidateValue uses the complete Fast cycle as the unit of inference.
// For a two-sided quote, regret is charged only to the empirically unmatched
// touch share; a genuinely unilateral interior action retains the full lower
// partial-moment charge. The balance-boundary continuation floor is handled
// separately as a Bellman boundary condition, not by weakening this interior
// risk preference.
func fastCandidateValue(
	d JointPathPayoffDecision,
	buyNotionalJPY, sellNotionalJPY,
	buyTouchProbability, sellTouchProbability, bothTouchProbability float64,
) (mean, downsideRegret, net float64) {
	if buyNotionalJPY > 0 && sellNotionalJPY > 0 {
		mean, downsideRegret, _ = fastFeeNetRegretValue(d)
		unmatchedShare := fastUnmatchedTouchShare(
			buyTouchProbability, sellTouchProbability, bothTouchProbability)
		return mean, unmatchedShare * downsideRegret,
			mean - unmatchedShare*downsideRegret
	}
	return fastFeeNetRegretValue(d)
}

// targetRestoringOrderNetValue values a one-sided inventory correction without
// importing the reservation profit of a hypothetical future oscillation. A
// mature terminal-path posterior already contains target-relative inventory
// risk and is therefore the complete action value. When that posterior is
// unavailable, the Bellman prior pays the configured one-way execution cost
// exactly once. MinimumNetEdgeBps is absent by construction: it belongs only
// to matched BUY/SELL cycle notional.
func targetRestoringOrderNetValue(
	stats JointPathPayoffStats,
	currentInventoryNotionalJPY, targetInventoryNotionalJPY, pairEquityJPY,
	buyNotionalJPY, sellNotionalJPY,
	buyProbability, sellProbability, bothProbability,
	riskAversion, entryCostBps, confidenceZScore float64,
	legacyStackedTargetContinuation bool,
) float64 {
	if stats.EffectiveSamples > 1 {
		if confidenceZScore <= 0 || math.IsNaN(confidenceZScore) || math.IsInf(confidenceZScore, 0) {
			confidenceZScore = 1.645
		}
		payoff := stats.EvaluateTargetRelativePosition(
			currentInventoryNotionalJPY, targetInventoryNotionalJPY,
			buyNotionalJPY, sellNotionalJPY,
			pairEquityJPY, riskAversion, confidenceZScore)
		net := payoff.CertaintyEquivalent
		return net + matureTargetProgressContinuationValue(
			legacyStackedTargetContinuation,
			currentInventoryNotionalJPY, targetInventoryNotionalJPY,
			pairEquityJPY, buyNotionalJPY, sellNotionalJPY,
			buyProbability, sellProbability, bothProbability)
	}
	return RiskReducingContinuationNetValue(
		currentInventoryNotionalJPY, targetInventoryNotionalJPY,
		pairEquityJPY, buyNotionalJPY, sellNotionalJPY,
		buyProbability, sellProbability, bothProbability, entryCostBps)
}

// TargetProgressContinuationValue is the Bellman value of moving inventory
// toward the same-horizon target.  Let e=I-I* and D be the random signed
// inventory-notional change from the proposed fills.  The scale-free proximal
// target loss used by the continuation controller gives the exact incremental
// value
//
//	B(a) = -e E[D]/W - E[D^2]/(2 |e|).
//
// It is maximized by q*=e^2/W for a certain one-sided target-restoring fill,
// matching the existing proximal controller.  Unlike a target-side condition,
// the same expression automatically penalizes movement away from target and
// target overshoot.  Fill covariance enters through P(BUY and SELL).
func TargetProgressContinuationValue(
	currentInventoryNotionalJPY, targetInventoryNotionalJPY, pairEquityJPY,
	buyNotionalJPY, sellNotionalJPY,
	buyProbability, sellProbability, bothProbability float64,
) float64 {
	errorJPY := currentInventoryNotionalJPY - targetInventoryNotionalJPY
	gapJPY := math.Abs(errorJPY)
	if gapJPY <= 0 || pairEquityJPY <= 0 || buyNotionalJPY < 0 || sellNotionalJPY < 0 {
		return 0
	}
	buyProbability = math.Max(0, math.Min(1, buyProbability))
	sellProbability = math.Max(0, math.Min(1, sellProbability))
	bothProbability = math.Max(0, math.Min(
		math.Min(buyProbability, sellProbability), bothProbability))
	expectedDeltaJPY := buyProbability*buyNotionalJPY - sellProbability*sellNotionalJPY
	expectedDeltaSquaredJPY2 :=
		buyProbability*buyNotionalJPY*buyNotionalJPY +
			sellProbability*sellNotionalJPY*sellNotionalJPY -
			2*bothProbability*buyNotionalJPY*sellNotionalJPY
	expectedDeltaSquaredJPY2 = math.Max(0, expectedDeltaSquaredJPY2)
	return -errorJPY*expectedDeltaJPY/pairEquityJPY -
		expectedDeltaSquaredJPY2/(2*gapJPY)
}

// matureTargetProgressContinuationValue exists only to reproduce the retired
// stacked policy in paired research replay. Production configuration cannot
// enable it; mature live candidates always receive zero here.
func matureTargetProgressContinuationValue(
	legacyReplay bool,
	currentInventoryNotionalJPY, targetInventoryNotionalJPY, pairEquityJPY,
	buyNotionalJPY, sellNotionalJPY,
	buyProbability, sellProbability, bothProbability float64,
) float64 {
	if !legacyReplay {
		return 0
	}
	return TargetProgressContinuationValue(
		currentInventoryNotionalJPY, targetInventoryNotionalJPY, pairEquityJPY,
		buyNotionalJPY, sellNotionalJPY,
		buyProbability, sellProbability, bothProbability)
}

// RiskReducingContinuationNetValue prices a one-sided boundary action when a
// completed terminal path is unavailable.  The action is not treated as a
// fee-positive trading cycle: its only admissible value is the Bellman
// continuation from moving the current inventory toward target, less the
// expected maker/adverse-selection cost of the side that can actually touch.
// This is a conservative prior for data outages, not a replacement for the
// terminal-path posterior.  It must therefore only be used when the selected
// side reduces |inventory-target|.
func RiskReducingContinuationNetValue(
	currentInventoryNotionalJPY, targetInventoryNotionalJPY, pairEquityJPY,
	buyNotionalJPY, sellNotionalJPY,
	buyProbability, sellProbability, bothProbability,
	entryCostBps float64,
) float64 {
	continuation := TargetProgressContinuationValue(
		currentInventoryNotionalJPY, targetInventoryNotionalJPY, pairEquityJPY,
		buyNotionalJPY, sellNotionalJPY,
		buyProbability, sellProbability, bothProbability)
	if entryCostBps <= 0 {
		return continuation
	}
	entryCostBps = math.Max(0, entryCostBps)
	expectedTouchedNotional := math.Max(0, buyProbability)*math.Max(0, buyNotionalJPY) +
		math.Max(0, sellProbability)*math.Max(0, sellNotionalJPY)
	return continuation - expectedTouchedNotional*entryCostBps/10_000
}

// TargetRestoringContinuationNotional is the one-sided Bellman optimum for a
// target-restoring fill when the terminal path posterior is unavailable.  With
// g=|I-I*|, W=equity, and one-way cost c, the continuation objective is
//
//	U(q) = p [ gq/W - q²/(2g) - c q ],
//
// so the arrival probability p cancels from the interior optimum:
//
//	q* = g (g/W - c).
//
// The caller still clips q* to exchange/account capacity and to the venue
// minimum.  This is deliberately not used for an exposure-increasing side.
func TargetRestoringContinuationNotional(
	currentInventoryNotionalJPY, targetInventoryNotionalJPY,
	pairEquityJPY, entryCostBps float64,
) float64 {
	gap := math.Abs(currentInventoryNotionalJPY - targetInventoryNotionalJPY)
	if gap <= 0 || pairEquityJPY <= 0 ||
		!inventoryProjectionFinite(gap) || !inventoryProjectionFinite(pairEquityJPY) {
		return 0
	}
	cost := math.Max(0, entryCostBps) / 10_000
	return math.Max(0, gap*(gap/pairEquityJPY-cost))
}

// TargetRestoringTwoSidedContinuationNotionals keeps a minimum executable
// quote on the non-target side while solving the same local Bellman objective
// for the corrective side.  If the account is overweight, the BUY cell is
// fixed at its venue minimum and the SELL optimum receives the covariance
// adjustment (P[both]/P[SELL])*BUY.  The underweight case is symmetric.  This
// prevents a missing-path fallback from becoming one-sided while preserving
// the target-restoring expected correction and the existing risk caps.
func TargetRestoringTwoSidedContinuationNotionals(
	currentInventoryNotionalJPY, targetInventoryNotionalJPY,
	pairEquityJPY, buyProbability, sellProbability, bothProbability,
	entryCostBps, minimumBuyNotionalJPY, minimumSellNotionalJPY,
	maximumBuyNotionalJPY, maximumSellNotionalJPY float64,
) (buyNotionalJPY, sellNotionalJPY float64) {
	if pairEquityJPY <= 0 || minimumBuyNotionalJPY <= 0 ||
		minimumSellNotionalJPY <= 0 || maximumBuyNotionalJPY+1e-9 < minimumBuyNotionalJPY ||
		maximumSellNotionalJPY+1e-9 < minimumSellNotionalJPY {
		return 0, 0
	}
	buyProbability = math.Max(0, buyProbability)
	sellProbability = math.Max(0, sellProbability)
	bothProbability = math.Max(0, math.Min(math.Min(buyProbability, sellProbability), bothProbability))
	base := TargetRestoringContinuationNotional(
		currentInventoryNotionalJPY, targetInventoryNotionalJPY,
		pairEquityJPY, entryCostBps)
	if currentInventoryNotionalJPY > targetInventoryNotionalJPY && sellProbability > 0 {
		buyNotionalJPY = minimumBuyNotionalJPY
		sellNotionalJPY = math.Max(minimumSellNotionalJPY,
			base+bothProbability/sellProbability*buyNotionalJPY)
		sellNotionalJPY = math.Min(maximumSellNotionalJPY, sellNotionalJPY)
		if sellNotionalJPY+1e-9 < minimumSellNotionalJPY {
			return 0, 0
		}
		return buyNotionalJPY, sellNotionalJPY
	}
	if currentInventoryNotionalJPY < targetInventoryNotionalJPY && buyProbability > 0 {
		sellNotionalJPY = minimumSellNotionalJPY
		buyNotionalJPY = math.Max(minimumBuyNotionalJPY,
			base+bothProbability/buyProbability*sellNotionalJPY)
		buyNotionalJPY = math.Min(maximumBuyNotionalJPY, buyNotionalJPY)
		if buyNotionalJPY+1e-9 < minimumBuyNotionalJPY {
			return 0, 0
		}
		return buyNotionalJPY, sellNotionalJPY
	}
	return 0, 0
}

// fastValueIdentificationFloor is zero because every JointPathPayoffDecision
// is already fee-net and uncertainty/risk adjusted. A previous implementation
// divided one maker fee by lookback/horizon. That amortization is invalid: each
// realized fill pays the full fee, already present in its path payoff, while an
// unfilled evaluation pays none. Candidate multiplicity is handled by paired
// confidence bounds and quote churn by the separate switching-cost model.
// Keeping this function as the explicit no-new-order threshold avoids silently
// reintroducing a second transaction-cost gate at its existing call sites.
func fastValueIdentificationFloor(
	_ MarketMakerConfig,
	_ time.Duration,
	_, _ float64,
) float64 {
	return 0
}

func jointQuantityScales(in ProbabilityCenteredQuoteInput, count int) []float64 {
	fastGross := math.Max(0, in.FastBuyNotionalJPY) + math.Max(0, in.FastSellNotionalJPY)
	if fastGross <= 0 {
		return nil
	}
	maxGross := math.Min(fastGross,
		math.Max(0, in.MaxBuyNotionalJPY)+math.Max(0, in.MaxSellNotionalJPY))
	if maxGross <= 0 {
		return nil
	}
	minGross := math.Max(0, in.MinBuyNotionalJPY) + math.Max(0, in.MinSellNotionalJPY)
	maxScale := math.Min(1, maxGross/fastGross)
	minScale := math.Min(maxScale, minGross/fastGross)
	if minScale <= 0 {
		denominator := count
		if denominator < 2 {
			denominator = 2
		}
		minScale = maxScale / float64(denominator)
	}
	if count < 2 || maxScale <= minScale*(1+1e-12) {
		return []float64{maxScale}
	}
	scales := make([]float64, 0, count)
	ratio := maxScale / minScale
	for index := 0; index < count; index++ {
		fraction := float64(index) / float64(count-1)
		scales = append(scales, minScale*math.Pow(ratio, fraction))
	}
	return scales
}

func jointDistanceCandidatePlan(
	base MarketMakerQuotePlan,
	bestBid, bestAsk, mid, maximumHalfSpreadBps, fraction float64,
) MarketMakerQuotePlan {
	candidate := base
	if bestBid <= 0 || bestAsk <= bestBid || mid <= 0 {
		return candidate
	}
	fraction = math.Max(0, math.Min(1, fraction))
	maxBidPrice := mid * math.Exp(-maximumHalfSpreadBps/10_000)
	maxAskPrice := mid * math.Exp(maximumHalfSpreadBps/10_000)
	maxBuyDistance := math.Max(base.BidTouchDistanceBps, math.Log(bestAsk/maxBidPrice)*10_000)
	maxSellDistance := math.Max(base.AskTouchDistanceBps, math.Log(maxAskPrice/bestBid)*10_000)
	buyDistance := base.BidTouchDistanceBps + fraction*(maxBuyDistance-base.BidTouchDistanceBps)
	sellDistance := base.AskTouchDistanceBps + fraction*(maxSellDistance-base.AskTouchDistanceBps)
	candidate.BidPrice = bestAsk * math.Exp(-buyDistance/10_000)
	candidate.AskPrice = bestBid * math.Exp(sellDistance/10_000)
	if candidate.BidPrice <= 0 || candidate.AskPrice <= candidate.BidPrice {
		return base
	}
	candidate.BidTouchDistanceBps, candidate.AskTouchDistanceBps, _ =
		MakerTouchDistances(bestBid, bestAsk, candidate.BidPrice, candidate.AskPrice)
	candidate.BidDistanceBps = math.Max(0, math.Log(mid/candidate.BidPrice)*10_000)
	candidate.AskDistanceBps = math.Max(0, math.Log(candidate.AskPrice/mid)*10_000)
	candidate.BidHalfSpreadBps = candidate.BidDistanceBps
	candidate.AskHalfSpreadBps = candidate.AskDistanceBps
	candidate.HalfSpreadBps = math.Max(candidate.BidDistanceBps, candidate.AskDistanceBps)
	return candidate
}

// jointDistanceCandidatePlans preserves the established outward ladder and,
// when conditional state is available, adds one-dimensional inward BUY and
// SELL ladders. Keeping this O(K), rather than forming an O(K^2) Cartesian
// product, bounds live CPU and avoids creating unsupported joint combinations.
func jointDistanceCandidatePlans(
	base MarketMakerQuotePlan,
	bestBid, bestAsk, mid, maximumHalfSpreadBps float64,
	allowInward bool,
	count int,
) []MarketMakerQuotePlan {
	if count < 2 {
		count = 2
	}
	plans := make([]MarketMakerQuotePlan, 0, 3*count-2)
	for index := 0; index < count; index++ {
		fraction := float64(index) / float64(count-1)
		plans = append(plans, jointDistanceCandidatePlan(
			base, bestBid, bestAsk, mid, maximumHalfSpreadBps, fraction))
	}
	if !allowInward || bestBid <= 0 || bestAsk <= bestBid || mid <= 0 {
		return plans
	}
	if base.AllowBid && base.BidPrice > 0 && bestBid > base.BidPrice*(1+1e-12) {
		logRange := math.Log(bestBid / base.BidPrice)
		for index := 1; index < count; index++ {
			fraction := float64(index) / float64(count-1)
			candidate := base
			candidate.BidPrice = base.BidPrice * math.Exp(fraction*logRange)
			candidate.BidTouchDistanceBps, candidate.AskTouchDistanceBps, _ =
				MakerTouchDistances(bestBid, bestAsk, candidate.BidPrice, candidate.AskPrice)
			candidate.BidDistanceBps = math.Max(0, math.Log(mid/candidate.BidPrice)*10_000)
			candidate.BidHalfSpreadBps = candidate.BidDistanceBps
			candidate.HalfSpreadBps = math.Max(candidate.BidHalfSpreadBps, candidate.AskHalfSpreadBps)
			plans = append(plans, candidate)
		}
	}
	if base.AllowAsk && base.AskPrice > bestAsk*(1+1e-12) {
		logRange := math.Log(base.AskPrice / bestAsk)
		for index := 1; index < count; index++ {
			fraction := float64(index) / float64(count-1)
			candidate := base
			candidate.AskPrice = base.AskPrice * math.Exp(-fraction*logRange)
			candidate.BidTouchDistanceBps, candidate.AskTouchDistanceBps, _ =
				MakerTouchDistances(bestBid, bestAsk, candidate.BidPrice, candidate.AskPrice)
			candidate.AskDistanceBps = math.Max(0, math.Log(candidate.AskPrice/mid)*10_000)
			candidate.AskHalfSpreadBps = candidate.AskDistanceBps
			candidate.HalfSpreadBps = math.Max(candidate.BidHalfSpreadBps, candidate.AskHalfSpreadBps)
			plans = append(plans, candidate)
		}
	}
	return plans
}

func fastPathDownsideDecision(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
	projection ProbabilityCenteredQuoteInput,
) FastPathDownsideCapDecision {
	buyDistance, sellDistance, _ := MakerTouchDistances(
		in.BestBid, in.BestAsk, in.BasePlan.BidPrice, in.BasePlan.AskPrice)
	stats := model.JointPathPayoffStatistics(
		in.Now, config, in.Horizon, buyDistance, sellDistance)
	moments := stats.BuyDominant
	riskAversion := in.RiskAversion
	if riskAversion <= 0 {
		riskAversion = fastRiskAversionOrDefault(config, riskAversion)
	}
	z := in.ConfidenceZScore
	if z <= 0 {
		z = config.InventoryRiskZScore
	}
	buyConfidence := stats.EvaluateTargetRelativePosition(
		projection.CurrentInventoryNotionalJPY, projection.TargetInventoryNotionalJPY,
		projection.MinBuyNotionalJPY, 0,
		in.PairEquityJPY, riskAversion, z)
	cap := FastPathDownsideBuyCap(FastPathDownsideCapInput{
		Direction:                   in.FastDirection,
		InventoryReturnMeanBps:      moments.InventoryMeanBps,
		InventoryReturnVarianceBps2: moments.InventoryVarBps2,
		EffectiveSamples:            stats.EffectiveSamples,
		ConfidenceZScore:            z,
		BuyConfidenceEquivalentJPY:  buyConfidence.CertaintyEquivalent,
		MinimumBuyNotionalJPY:       projection.MinBuyNotionalJPY,
		MaximumBuyNotionalJPY:       projection.MaxBuyNotionalJPY,
	})
	return cap
}

func applyFastPathAdmissions(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
	downside FastPathDownsideCapDecision,
	d JointDistanceQuantityDecision,
) JointDistanceQuantityDecision {
	d.DownsideBuyCapApplied = false
	d.DownsideInventoryReturnMeanBps = downside.InventoryReturnMeanBps
	d.DownsideInventoryReturnSEBps = downside.InventoryReturnStdErrorBps
	d.DownsideInventoryReturnUpperBps = downside.InventoryReturnUpperBps
	d.DownsideEffectiveSamples = downside.EffectiveSamples
	d.DownsideOriginalMaxBuyJPY = downside.OriginalMaximumBuyJPY
	d.DownsideMinimumBuyEvaluated = downside.Evaluated
	d.DownsideMinimumBuyCEJPY = downside.BuyConfidenceEquivalentJPY
	if !d.Enabled || !d.Applied {
		return d
	}

	buyNotionalJPY := d.Projection.BuyNotionalJPY
	sellNotionalJPY := d.Projection.SellNotionalJPY
	if downside.Applied && buyNotionalJPY > downside.MaximumBuyNotionalJPY+1e-9 {
		d.DownsideBuyCapApplied = true
		buyNotionalJPY = downside.MaximumBuyNotionalJPY
	}
	buyDistance, sellDistance, _ := MakerTouchDistances(
		in.BestBid, in.BestAsk, d.Plan.BidPrice, d.Plan.AskPrice)
	stats := model.JointPathPayoffStatistics(
		in.Now, config, in.Horizon, buyDistance, sellDistance)
	riskAversion := in.RiskAversion
	if riskAversion <= 0 {
		riskAversion = fastRiskAversionOrDefault(config, riskAversion)
	}
	z := in.ConfidenceZScore
	if z <= 0 {
		z = config.InventoryRiskZScore
	}
	if z <= 0 {
		z = 1.645
	}
	pathEvidenceReady := stats.EffectiveSamples > 1
	proposedBuyNotionalJPY := buyNotionalJPY
	proposedSellNotionalJPY := sellNotionalJPY
	jointCEJPY := 0.0
	jointRobustCEJPY := 0.0
	jointComplementary := false
	if pathEvidenceReady && proposedBuyNotionalJPY > 0 && proposedSellNotionalJPY > 0 {
		joint := stats.EvaluateTargetRelativePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			proposedBuyNotionalJPY, proposedSellNotionalJPY,
			in.PairEquityJPY, riskAversion, 0)
		_, _, jointCEJPY = fastCandidateValue(
			joint, proposedBuyNotionalJPY, proposedSellNotionalJPY,
			d.Crossing.BuyTouchProbability,
			d.Crossing.SellTouchProbability,
			d.Crossing.BothTouchProbability)
		jointFloorJPY := fastValueIdentificationFloor(
			config, in.Horizon,
			in.Projection.MinBuyNotionalJPY,
			in.Projection.MinSellNotionalJPY)
		jointConfidence := stats.EvaluateTargetRelativePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			proposedBuyNotionalJPY, proposedSellNotionalJPY,
			in.PairEquityJPY, riskAversion, z)
		jointRobustCEJPY = jointConfidence.CertaintyEquivalent
		jointComplementary = jointCEJPY > jointFloorJPY &&
			(!config.JointDistanceQuantity.RobustAdmission ||
				jointRobustCEJPY > jointFloorJPY)
	}
	buyUtilityBoundJPY := 0.0
	if pathEvidenceReady && buyNotionalJPY > 0 && in.Projection.MinBuyNotionalJPY > 0 {
		buyValue := stats.EvaluateTargetRelativePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			in.Projection.MinBuyNotionalJPY, 0,
			in.PairEquityJPY, riskAversion, 0)
		_, _, buyUtilityBoundJPY = fastFeeNetRegretValue(buyValue)
		if config.JointDistanceQuantity.RobustAdmission {
			buyUtilityBoundJPY = stats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				in.Projection.MinBuyNotionalJPY, 0,
				in.PairEquityJPY, riskAversion, z).CertaintyEquivalent
		}
	}
	sellUtilityBoundJPY := 0.0
	if pathEvidenceReady && sellNotionalJPY > 0 && in.Projection.MinSellNotionalJPY > 0 {
		sellValue := stats.EvaluateTargetRelativePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			0, in.Projection.MinSellNotionalJPY,
			in.PairEquityJPY, riskAversion, 0)
		_, _, sellUtilityBoundJPY = fastFeeNetRegretValue(sellValue)
		if config.JointDistanceQuantity.RobustAdmission {
			sellUtilityBoundJPY = stats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				0, in.Projection.MinSellNotionalJPY,
				in.PairEquityJPY, riskAversion, z).CertaintyEquivalent
		}
	}
	d.AdmissionJointCEJPY = jointCEJPY
	d.AdmissionJointRobustCEJPY = jointRobustCEJPY
	d.AdmissionJointComplementary = jointComplementary
	buyAdmission := FastSideAdmissionDecision{
		Evaluated:          pathEvidenceReady && buyNotionalJPY > 0,
		Reason:             "positive joint Fast utility preserves complementary BUY",
		MaximumNotionalJPY: buyNotionalJPY,
		UtilityBoundJPY:    buyUtilityBoundJPY,
	}
	if !jointComplementary {
		buyAdmission = FastTargetAwareSideAdmission(
			types.SideTypeBuy,
			pathEvidenceReady && buyNotionalJPY > 0,
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			in.Projection.MinBuyNotionalJPY,
			buyNotionalJPY, buyUtilityBoundJPY)
	}
	d.BuyAdmissionEvaluated = buyAdmission.Evaluated
	d.BuyAdmissionApplied = buyAdmission.Applied
	d.BuyAdmissionMaximumJPY = buyAdmission.MaximumNotionalJPY
	d.BuyAdmissionUtilityBoundJPY = buyAdmission.UtilityBoundJPY
	d.BuyAdmissionReason = buyAdmission.Reason
	if buyAdmission.Applied {
		buyNotionalJPY = buyAdmission.MaximumNotionalJPY
	}

	sellAdmission := FastSideAdmissionDecision{
		Evaluated:          pathEvidenceReady && sellNotionalJPY > 0,
		Reason:             "positive joint Fast utility preserves complementary SELL",
		MaximumNotionalJPY: sellNotionalJPY,
		UtilityBoundJPY:    sellUtilityBoundJPY,
	}
	if !jointComplementary {
		sellAdmission = FastTargetAwareSideAdmission(
			types.SideTypeSell,
			pathEvidenceReady && sellNotionalJPY > 0,
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			in.Projection.MinSellNotionalJPY,
			sellNotionalJPY, sellUtilityBoundJPY)
	}
	d.SellAdmissionEvaluated = sellAdmission.Evaluated
	d.SellAdmissionApplied = sellAdmission.Applied
	d.SellAdmissionMaximumJPY = sellAdmission.MaximumNotionalJPY
	d.SellAdmissionUtilityBoundJPY = sellAdmission.UtilityBoundJPY
	d.SellAdmissionReason = sellAdmission.Reason
	if sellAdmission.Applied {
		sellNotionalJPY = sellAdmission.MaximumNotionalJPY
	}
	if !d.DownsideBuyCapApplied && !d.BuyAdmissionApplied && !d.SellAdmissionApplied {
		return d
	}

	// Preserve the price and order-lifetime decision made on Fast's original
	// feasible set. Restrict only additional downside exposure, then recompute
	// every inventory and terminal-wealth diagnostic at the executable size.
	target := in.Projection.TargetInventoryNotionalJPY
	d.Projection.BuyNotionalJPY = buyNotionalJPY
	d.Projection.SellNotionalJPY = sellNotionalJPY
	if buyNotionalJPY <= 0 {
		d.Plan.AllowBid = false
	}
	if sellNotionalJPY <= 0 {
		d.Plan.AllowAsk = false
	}
	d.Projection.ProjectedGrossNotionalJPY =
		d.Projection.BuyNotionalJPY + d.Projection.SellNotionalJPY
	pBuy, pSell := d.Projection.BuyFillProbability, d.Projection.SellFillProbability
	expected := in.Projection.CurrentInventoryNotionalJPY +
		pBuy*d.Projection.BuyNotionalJPY - pSell*d.Projection.SellNotionalJPY
	variance := pBuy*(1-pBuy)*d.Projection.BuyNotionalJPY*d.Projection.BuyNotionalJPY +
		pSell*(1-pSell)*d.Projection.SellNotionalJPY*d.Projection.SellNotionalJPY -
		2*d.Projection.FillCovariance*d.Projection.BuyNotionalJPY*d.Projection.SellNotionalJPY
	d.Projection.ExpectedInventoryNotionalJPY = expected
	d.Projection.InventoryVarianceJPY2 = math.Max(0, variance)
	d.Projection.InventoryStdDevJPY = math.Sqrt(d.Projection.InventoryVarianceJPY2)
	d.Projection.ConfidenceLowerNotionalJPY = expected - z*d.Projection.InventoryStdDevJPY
	d.Projection.ConfidenceUpperNotionalJPY = expected + z*d.Projection.InventoryStdDevJPY
	d.Projection.TargetErrorJPY = expected - target

	payoff := stats.EvaluateTargetRelativePosition(
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		d.Projection.BuyNotionalJPY, d.Projection.SellNotionalJPY,
		in.PairEquityJPY, riskAversion, 0)
	confidence := stats.EvaluateTargetRelativePosition(
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		d.Projection.BuyNotionalJPY, d.Projection.SellNotionalJPY,
		in.PairEquityJPY, riskAversion, z)
	hours := in.Horizon.Hours()
	if hours > 0 {
		d.ExpectedPnLJPYHour = payoff.ExpectedPnLJPY / hours
		d.LowerPnLJPYHour = confidence.CertaintyEquivalent / hours
		d.PathStdErrorJPYHour = payoff.StdErrorJPY / hours
		d.KellyPenaltyJPYHour = payoff.KellyPenaltyJPY / hours
		d.KellyUtilityJPYHour = payoff.CertaintyEquivalent / hours
	}
	d.FeeValueMeanJPY, d.FeeValueDownsideRegretJPY, d.FeeValueNetJPY =
		fastCandidateValue(
			payoff,
			d.Projection.BuyNotionalJPY,
			d.Projection.SellNotionalJPY,
			d.Crossing.BuyTouchProbability,
			d.Crossing.SellTouchProbability,
			d.Crossing.BothTouchProbability)
	d.ExpectedCycleJPY = math.Min(
		d.Crossing.BuyTouchProbability*d.Projection.BuyNotionalJPY,
		d.Crossing.SellTouchProbability*d.Projection.SellNotionalJPY)
	d.PathPositiveConfidence = jointPathUtilityConfidence(payoff)
	d.ExistingInventoryExpectedPnLJPY = payoff.ExistingInventoryExpectedPnLJPY
	d.BaselineVarianceJPY2 = payoff.BaselineVarianceJPY2
	d.WholePositionVarianceJPY2 = payoff.WholePositionVarianceJPY2
	d.MarginalVarianceJPY2 = payoff.MarginalVarianceJPY2
	d.InventoryOrderCovarianceJPY2 = payoff.InventoryOrderCovarianceJPY2
	d.RiskReducing = payoff.RiskReducing
	setJointPathDecayDiagnostics(&d, stats)
	if in.PairEquityJPY > 0 {
		d.PairCapitalUtilization = d.Projection.ProjectedGrossNotionalJPY / in.PairEquityJPY
	}
	fastGross := in.Projection.FastBuyNotionalJPY + in.Projection.FastSellNotionalJPY
	if fastGross > 0 {
		d.CapitalUtilization = d.Projection.ProjectedGrossNotionalJPY / fastGross
		d.QuantityScale = d.CapitalUtilization
	}
	if d.DownsideBuyCapApplied {
		d.Reason += "+terminal-downside BUY cap"
	}
	if d.BuyAdmissionApplied {
		d.Reason += "+target-aware marginal-BUY admission"
	}
	if d.SellAdmissionApplied {
		d.Reason += "+target-aware marginal-SELL admission"
	}
	return d
}

// OptimizeUnifiedFastQuantity gives one Fast model sole ownership of the final
// decision. It selects Fast's price and order lifetime on the original
// statistically identifiable feasible set. The same terminal-path model then
// applies long-only, target-aware BUY/SELL admission at the selected prices
// without re-solving price; risk evidence therefore cannot move the quote or
// reset its lifetime a second time.
// HorizonPosteriorReliability is the empirical-Bayes precision assigned to a
// horizon-specific terminal-path estimate.  The alternative action is no new
// order, whose excess utility is zero, so shrinking an estimate with n
// effective observations and n0 prior observations gives
//
//	E[U_H | data] = n/(n+n0) * U_H.
//
// This is continuous in the amount of evidence and deliberately does not map
// categorical HEALTHY/DEGRADED labels into trading rules.
func HorizonPosteriorReliability(effectiveSamples float64, priorSamples int) float64 {
	if effectiveSamples <= 0 || math.IsNaN(effectiveSamples) || math.IsInf(effectiveSamples, 0) {
		return 0
	}
	if priorSamples <= 0 {
		priorSamples = 1
	}
	return effectiveSamples / (effectiveSamples + float64(priorSamples))
}

func jointHorizonEffectiveSamples(crossing, path float64) float64 {
	if crossing <= 0 {
		return math.Max(0, path)
	}
	if path <= 0 {
		return math.Max(0, crossing)
	}
	return math.Min(crossing, path)
}

// jointHorizonRawUtility evaluates the final physical orders, rather than the
// neutral quote used by the preliminary Fast-window selector.  It is the same
// fee-net, whole-position terminal-wealth objective as the distance/quantity
// optimizer and is normalized by H via the renewal-reward theorem.
func jointHorizonRawUtility(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
	d *JointDistanceQuantityDecision,
) float64 {
	if model == nil || d == nil || in.Horizon <= 0 || in.PairEquityJPY <= 0 ||
		d.Plan.BidPrice <= 0 || d.Plan.AskPrice <= d.Plan.BidPrice ||
		d.Projection.ProjectedGrossNotionalJPY <= 0 {
		return math.Inf(-1)
	}
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(
		in.BestBid, in.BestAsk, d.Plan.BidPrice, d.Plan.AskPrice)
	crossing := model.CrossingDecisionAtSideDistances(
		in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
	stats := model.JointPathPayoffStatistics(
		in.Now, config, in.Horizon, buyDistance, sellDistance)
	inwardBuy := d.Plan.BidPrice > in.BasePlan.BidPrice*(1+1e-12)
	inwardSell := d.Plan.AskPrice < in.BasePlan.AskPrice*(1-1e-12)
	if conditionalState := model.conditionalExecutionState(in.Horizon); config.ConditionalExecution.Enabled && conditionalState.Valid &&
		(inwardBuy || inwardSell) {
		crossing = model.conditionalCrossingDecision(
			in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge,
			conditionalState)
		stats = model.conditionalJointPathPayoffStatistics(
			in.Now, config, in.Horizon, buyDistance, sellDistance,
			conditionalState)
	}
	if stats.EffectiveSamples <= 0 {
		return math.Inf(-1)
	}
	riskAversion := in.RiskAversion
	if riskAversion <= 0 {
		riskAversion = fastRiskAversionOrDefault(config, riskAversion)
	}
	payoff := stats.EvaluateTargetRelativePosition(
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		d.Projection.BuyNotionalJPY, d.Projection.SellNotionalJPY,
		in.PairEquityJPY, riskAversion, 0)
	_, _, feeValueNet := fastCandidateValue(
		payoff,
		d.Projection.BuyNotionalJPY, d.Projection.SellNotionalJPY,
		crossing.BuyTouchProbability, crossing.SellTouchProbability,
		crossing.BothTouchProbability)
	feeValueNet += matureTargetProgressContinuationValue(
		config.JointDistanceQuantity.LegacyStackedTargetContinuation,
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		in.PairEquityJPY,
		d.Projection.BuyNotionalJPY, d.Projection.SellNotionalJPY,
		crossing.BuyTouchProbability, crossing.SellTouchProbability,
		crossing.BothTouchProbability)
	relativeHoldUtility := jointRelativeHoldUtility(in,
		d.Projection.BuyNotionalJPY, d.Projection.SellNotionalJPY)
	feeValueNet += relativeHoldUtility.NetJPYPerHour * in.Horizon.Hours()
	floor := fastValueIdentificationFloor(
		config, in.Horizon,
		in.Projection.MinBuyNotionalJPY, in.Projection.MinSellNotionalJPY)
	d.Crossing = crossing
	setJointPathDecayDiagnostics(d, stats)
	d.FeeValueNetJPY = feeValueNet
	d.HorizonRawUtilityJPYHour = (feeValueNet-floor)/in.Horizon.Hours() + relativeHoldUtility.NetJPYPerHour
	recordJointRelativeHoldDiagnostics(d, relativeHoldUtility, in)
	return d.HorizonRawUtilityJPYHour
}

// OptimizeUnifiedFastQuantity maximizes one posterior objective over the
// Cartesian candidate set (H, bid distance, ask distance, bid quantity, ask
// quantity). Feasibility still comes from exchange/account constraints, but
// horizon preference is never expressed as an if/else health policy.
func OptimizeUnifiedFastQuantity(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	fullInput JointDistanceQuantityInput,
	baselineProjection ProbabilityCenteredQuoteInput,
) JointDistanceQuantityDecision {
	if !config.JointDistanceQuantity.JointHorizonSelection ||
		len(fullInput.HorizonCandidates) == 0 {
		return optimizeUnifiedFastQuantityAtHorizon(
			model, config, fullInput, baselineProjection)
	}
	horizons := make([]time.Duration, 0, len(fullInput.HorizonCandidates))
	seen := make(map[time.Duration]struct{}, len(fullInput.HorizonCandidates))
	for _, horizon := range fullInput.HorizonCandidates {
		if horizon <= 0 {
			continue
		}
		if _, ok := seen[horizon]; ok {
			continue
		}
		seen[horizon] = struct{}{}
		horizons = append(horizons, horizon)
	}
	if len(horizons) == 0 {
		return optimizeUnifiedFastQuantityAtHorizon(
			model, config, fullInput, baselineProjection)
	}

	// Posterior uncertainty, rather than the legacy categorical health gate,
	// controls sparse windows. One completed path remains the technical minimum
	// needed to identify dispersion inside the existing optimizer.
	researchConfig := config
	researchConfig.HorizonMinSamples = 1
	bestScore := 0.0 // the explicit no-new-order candidate
	best := JointDistanceQuantityDecision{
		Enabled: true, AuthoritativeRejection: true,
		Reason:                "no horizon has positive reliability-shrunk terminal-wealth utility",
		Plan:                  fullInput.BasePlan,
		HorizonCandidateCount: len(horizons),
	}
	for _, horizon := range horizons {
		candidateInput := fullInput
		candidateInput.Horizon = horizon
		candidateInput.HorizonCandidates = nil
		candidateInput.Projection.Horizon = horizon
		candidateBaseline := baselineProjection
		candidateBaseline.Horizon = horizon
		candidate := optimizeUnifiedFastQuantityAtHorizon(
			model, researchConfig, candidateInput, candidateBaseline)
		candidate.HorizonCandidateCount = len(horizons)
		raw := jointHorizonRawUtility(
			model, researchConfig, candidateInput, &candidate)
		effective := jointHorizonEffectiveSamples(
			candidate.Crossing.EffectiveSamples, candidate.PathEffectiveSamples)
		reliability := HorizonPosteriorReliability(
			effective, config.HorizonMinSamples)
		score := reliability * raw
		candidate.HorizonEffectiveSamples = effective
		candidate.HorizonReliability = reliability
		candidate.HorizonRawUtilityJPYHour = raw
		candidate.HorizonSelectionUtilityJPYHour = score
		if candidate.Enabled && !candidate.AuthoritativeRejection &&
			candidate.Projection.ProjectedGrossNotionalJPY > 0 &&
			!math.IsNaN(score) && !math.IsInf(score, 0) && score > bestScore+1e-12 {
			bestScore = score
			best = candidate
			best.Enabled = true
			best.Applied = !config.JointDistanceQuantity.ShadowOnly
			best.AuthoritativeRejection = false
			best.Reason += "; joint horizon-distance-quantity posterior optimum"
		}
	}
	return best
}

// optimizeUnifiedFastQuantityAtHorizon applies the same relative-Hold scalar
// to a fallback decision that is returned after the terminal-path optimizer
// rejects its bilateral candidate. The ordinary candidate path already adds
// the scalar while ranking quantities, so this wrapper only fills the
// diagnostic/objective field when a continuity or side-safe fallback was
// selected; it never introduces a second side gate.
func optimizeUnifiedFastQuantityAtHorizon(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	fullInput JointDistanceQuantityInput,
	baselineProjection ProbabilityCenteredQuoteInput,
) JointDistanceQuantityDecision {
	d := optimizeUnifiedFastQuantityAtHorizonRaw(
		model, config, fullInput, baselineProjection)
	if !d.RelativeHoldStateReady && d.Projection.ProjectedGrossNotionalJPY > 0 {
		u := jointRelativeHoldUtility(fullInput,
			d.Projection.BuyNotionalJPY, d.Projection.SellNotionalJPY)
		if u.Ready {
			recordJointRelativeHoldDiagnostics(&d, u, fullInput)
			d.FeeValueNetJPY += u.NetJPYPerHour * fullInput.Horizon.Hours()
			d.KellyUtilityJPYHour += u.NetJPYPerHour
		}
	}
	return d
}

func optimizeUnifiedFastQuantityAtHorizonRaw(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	fullInput JointDistanceQuantityInput,
	baselineProjection ProbabilityCenteredQuoteInput,
) JointDistanceQuantityDecision {
	downside := fastPathDownsideDecision(
		model, config, fullInput, fullInput.Projection)
	// Downside capacity is part of the optimizer's feasible set, not a second
	// controller that resizes a price/quantity decision after selection.
	// Applying it before both the full and baseline searches keeps distance and
	// quantity mutually consistent at the executable lattice.
	fullInput.Projection, baselineProjection = fastDownsideConstrainedInputs(
		fullInput.Projection, baselineProjection, downside)
	withDownsideDiagnostics := func(d JointDistanceQuantityDecision) JointDistanceQuantityDecision {
		d.DownsideBuyCapApplied = downside.Applied
		d.DownsideInventoryReturnMeanBps = downside.InventoryReturnMeanBps
		d.DownsideInventoryReturnSEBps = downside.InventoryReturnStdErrorBps
		d.DownsideInventoryReturnUpperBps = downside.InventoryReturnUpperBps
		d.DownsideEffectiveSamples = downside.EffectiveSamples
		d.DownsideOriginalMaxBuyJPY = downside.OriginalMaximumBuyJPY
		d.DownsideMinimumBuyEvaluated = downside.Evaluated
		d.DownsideMinimumBuyCEJPY = downside.BuyConfidenceEquivalentJPY
		if downside.Applied {
			d.Reason += "; terminal-downside BUY capacity included in joint feasible set"
		}
		return d
	}
	baseline := probabilityOnlyFastBaseline(model, config, fullInput, baselineProjection)
	full := OptimizeJointDistanceQuantity(model, config, fullInput)
	if full.AuthoritativeRejection {
		if protected := protectPostFillCompletion(
			model, config, fullInput, baselineProjection); protected.Applied {
			return withDownsideDiagnostics(protected)
		}
		// When terminal paths are absent, preserve both executable sides at the
		// target-restoring Bellman quantities. This is a continuity fallback for
		// a data gap, not a resurrection of a measured negative payoff.
		if fallback := twoSidedContinuationFallbackAfterPathGap(
			model, config, fullInput); fallback.Applied || fallback.Enabled {
			return withDownsideDiagnostics(fallback)
		}
		if fallback := twoSidedQuoteContinuityFloorAfterJointRejection(
			model, config, fullInput, full); fallback.Applied || fallback.Enabled {
			return withDownsideDiagnostics(fallback)
		} else if config.JointDistanceQuantity.PreserveTwoSidedQuotes {
			full.ContinuityFloorReason = fallback.ContinuityFloorReason
		}
		continuation := targetRestoringFastContinuation(
			model, config, fullInput, baselineProjection)
		if continuation.Applied {
			continuation.ContinuityFloorReason = full.ContinuityFloorReason
			return withDownsideDiagnostics(continuation)
		}
		// A sparse/reconnected BBO stream can leave the completed terminal-path
		// posterior empty even though the side-crossing posterior is usable.  Do
		// not confuse that missing path with a measured negative terminal payoff:
		// admit only the venue-minimum side that reduces the current target error,
		// and charge its one-way maker/adverse cost from the Bellman continuation
		// value.  This cannot create a BUY while the account is already above its
		// target; it only prevents a data gap from suppressing a risk-reducing side.
		if fallback := targetContinuationFallbackAfterPathGap(
			model, config, fullInput); fallback.Applied || fallback.Enabled {
			fallback.ContinuityFloorReason = full.ContinuityFloorReason
			return withDownsideDiagnostics(fallback)
		}
		// A bilateral rejection is not evidence that both independent side
		// actions are unsafe. Preserve the strongest side-safe venue cell after
		// boundary/conditional continuations have had first claim on the state.
		if fallback := sideSafeFallbackAfterJointRejection(
			model, config, fullInput, full); fallback.Applied || fallback.Enabled {
			fallback.ContinuityFloorReason = full.ContinuityFloorReason
			return withDownsideDiagnostics(fallback)
		}
		full.Reason += "; " + continuation.Reason
		// Once completed same-horizon paths identify a non-positive fee-net
		// opportunity, probability-only fallback may not resurrect it. The
		// missing-path case was handled explicitly above; reaching here means the
		// path was either measured negative or the continuation prior was not
		// executable, so fail closed.
		return withDownsideDiagnostics(full)
	}
	if full.Applied {
		// Terminal-path evidence may promote within the common risk capacity, but it
		// is not an independent hard-risk observation. Do not let a one-sided
		// posterior fallback delete a Fast side that the probability-centered
		// baseline, account balances, hard inventory band, and exchange lattice
		// all admit. Otherwise repeated soft SELL support can mechanically drain
		// inventory even while the unified target calls for acquisition.
		if fastDecisionDropsBaselineSide(full, baseline, fullInput.BasePlan) {
			return withDownsideDiagnostics(
				retainJointEvaluationDiagnostics(baseline, full))
		}
		return withDownsideDiagnostics(full)
	}
	baselineInput := fullInput
	baselineInput.Projection = baselineProjection
	optimizedBaseline := OptimizeJointDistanceQuantity(model, config, baselineInput)
	if optimizedBaseline.AuthoritativeRejection {
		return withDownsideDiagnostics(optimizedBaseline)
	}
	if optimizedBaseline.Applied &&
		!fastDecisionDropsBaselineSide(optimizedBaseline, baseline, fullInput.BasePlan) {
		return withDownsideDiagnostics(optimizedBaseline)
	}
	if baseline.Enabled {
		baseline = retainJointEvaluationDiagnostics(baseline, full)
		baseline = retainJointEvaluationDiagnostics(baseline, optimizedBaseline)
		return withDownsideDiagnostics(baseline)
	}
	return withDownsideDiagnostics(optimizedBaseline)
}

// retainJointEvaluationDiagnostics keeps evidence metadata from an evaluated
// terminal-path candidate when the executable probability-only baseline wins
// for a separate policy reason (for example, preserving a bilateral quote).
// The selected baseline's prices, quantities, utility and reason remain its
// own; only facts about how much evidence and how many candidates were
// evaluated are carried forward.
func retainJointEvaluationDiagnostics(
	selected, evaluated JointDistanceQuantityDecision,
) JointDistanceQuantityDecision {
	if evaluated.CandidateCount > selected.CandidateCount {
		selected.CandidateCount = evaluated.CandidateCount
	}
	if evaluated.PathEffectiveSamples > selected.PathEffectiveSamples {
		selected.PathEffectiveSamples = evaluated.PathEffectiveSamples
	}
	// The relative-Hold contribution is part of the same joint scalar even
	// when the executable baseline is retained for a separate Fast-side
	// continuity rule. Preserve its evidence/utility diagnostics so the caller
	// can distinguish "scalar evaluated but baseline retained" from "scalar
	// never reached the optimizer". This does not copy prices or quantities.
	if evaluated.RelativeHoldStateReady {
		selected.RelativeHoldStateReady = true
		selected.RelativeHoldDownsideReady = evaluated.RelativeHoldDownsideReady
		selected.RelativeHoldUtilityJPYHour = evaluated.RelativeHoldUtilityJPYHour
		selected.RelativeHoldTrackingPenaltyJPYHour = evaluated.RelativeHoldTrackingPenaltyJPYHour
		selected.RelativeHoldDownsidePenaltyJPYHour = evaluated.RelativeHoldDownsidePenaltyJPYHour
		selected.RelativeHoldCVaRShadowLossJPY = evaluated.RelativeHoldCVaRShadowLossJPY
	}
	return selected
}

func fastDownsideConstrainedInputs(
	full, baseline ProbabilityCenteredQuoteInput,
	downside FastPathDownsideCapDecision,
) (ProbabilityCenteredQuoteInput, ProbabilityCenteredQuoteInput) {
	if !downside.Applied {
		return full, baseline
	}
	full.MaxBuyNotionalJPY = math.Min(
		full.MaxBuyNotionalJPY, downside.MaximumBuyNotionalJPY)
	baseline.MaxBuyNotionalJPY = math.Min(
		baseline.MaxBuyNotionalJPY, downside.MaximumBuyNotionalJPY)
	return full, baseline
}

// protectPostFillCompletion keeps the statistically approved opposite leg of
// an already-started cycle.  The first fill is sunk, while the unconditional
// joint optimizer compares a fresh zero-fill action with a new two-leg quote;
// allowing the latter to veto the former is a conditioning error.  This helper
// preserves only one exchange-minimum completion cell and never exceeds the
// live balance/hard-band capacity carried by the projection input.
func protectPostFillCompletion(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
	projectionInput ProbabilityCenteredQuoteInput,
) JointDistanceQuantityDecision {
	d := JointDistanceQuantityDecision{
		Reason: "no statistically approved post-fill completion",
		Plan:   in.BasePlan,
	}
	buy := in.CompletionSide == types.SideTypeBuy
	sell := in.CompletionSide == types.SideTypeSell
	if (!buy && !sell) || model == nil {
		return d
	}
	notionalJPY, maximumJPY := projectionInput.MinSellNotionalJPY, projectionInput.MaxSellNotionalJPY
	if buy {
		notionalJPY, maximumJPY = projectionInput.MinBuyNotionalJPY, projectionInput.MaxBuyNotionalJPY
	}
	if notionalJPY <= 0 || maximumJPY+1e-9 < notionalJPY ||
		(buy && !in.BasePlan.AllowBid) || (sell && !in.BasePlan.AllowAsk) {
		return d
	}
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(
		in.BestBid, in.BestAsk, in.BasePlan.BidPrice, in.BasePlan.AskPrice)
	crossing := model.CrossingDecisionAtSideDistances(
		in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
	pBuy, pSell := 0.0, 0.0
	if buy {
		pBuy = crossing.BuyTouchProbability
	} else {
		pSell = crossing.SellTouchProbability
	}
	projection := ProbabilityCenteredQuoteDecision{
		Enabled:             true,
		Reason:              "conditional post-fill completion cell",
		BuyFillProbability:  pBuy,
		SellFillProbability: pSell,
		BuyNotionalJPY:      0,
		SellNotionalJPY:     0,
	}
	if buy {
		projection.BuyNotionalJPY = notionalJPY
		d.Plan.AllowAsk = false
	} else {
		projection.SellNotionalJPY = notionalJPY
		d.Plan.AllowBid = false
	}
	projection.ProjectedGrossNotionalJPY = notionalJPY
	projection.ExpectedInventoryNotionalJPY =
		projectionInput.CurrentInventoryNotionalJPY +
			pBuy*projection.BuyNotionalJPY - pSell*projection.SellNotionalJPY
	projection.InventoryVarianceJPY2 =
		pBuy*(1-pBuy)*projection.BuyNotionalJPY*projection.BuyNotionalJPY +
			pSell*(1-pSell)*projection.SellNotionalJPY*projection.SellNotionalJPY
	projection.InventoryStdDevJPY = math.Sqrt(math.Max(0, projection.InventoryVarianceJPY2))
	z := projectionInput.ConfidenceZScore
	if z <= 0 {
		z = config.InventoryRiskZScore
	}
	projection.ConfidenceLowerNotionalJPY =
		projection.ExpectedInventoryNotionalJPY - z*projection.InventoryStdDevJPY
	projection.ConfidenceUpperNotionalJPY =
		projection.ExpectedInventoryNotionalJPY + z*projection.InventoryStdDevJPY
	projection.TargetErrorJPY =
		projection.ExpectedInventoryNotionalJPY - projectionInput.TargetInventoryNotionalJPY
	d.Enabled = true
	d.Applied = !config.JointDistanceQuantity.ShadowOnly
	d.SideSafeFallback = true
	d.CompletionProtected = true
	d.Reason = "conditional post-fill cycle completion protected from unconditional rejection"
	d.Projection = projection
	d.Crossing = crossing
	return d
}

// probabilityOnlyFastBaseline is the smallest statistically controlled Fast
// feasible set. It uses the same Bernoulli inventory chance constraint as the
// promoted model, but no terminal-path direction veto. Path evidence may size
// above this set; only balances, the hard inventory band, or exchange filters
// may remove a side from it.
func probabilityOnlyFastBaseline(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
	projectionInput ProbabilityCenteredQuoteInput,
) JointDistanceQuantityDecision {
	d := JointDistanceQuantityDecision{
		Reason: "probability-only Fast baseline unavailable",
		Plan:   in.BasePlan,
	}
	projection := ProbabilityCenteredQuoteNotionals(projectionInput)
	if !projection.Enabled || projection.ProjectedGrossNotionalJPY <= 0 {
		return d
	}
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(
		in.BestBid, in.BestAsk, in.BasePlan.BidPrice, in.BasePlan.AskPrice)
	d.Enabled = true
	d.Applied = !config.JointDistanceQuantity.ShadowOnly
	d.Reason = "probability-only executable Fast baseline preserved"
	d.Projection = projection
	d.Crossing = model.CrossingDecisionAtSideDistances(
		in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
	if in.PairEquityJPY > 0 {
		d.PairCapitalUtilization = projection.ProjectedGrossNotionalJPY / in.PairEquityJPY
	}
	fastGross := projectionInput.FastBuyNotionalJPY + projectionInput.FastSellNotionalJPY
	if fastGross > 0 {
		d.CapitalUtilization = projection.ProjectedGrossNotionalJPY / fastGross
		d.QuantityScale = d.CapitalUtilization
	}
	return d
}

// twoSidedContinuationFallbackAfterPathGap retains both executable sides when
// the terminal path is missing but the side-crossing posterior is usable.  It
// is intentionally inactive after two or more completed terminal paths: a
// measured negative terminal CE must continue to veto an ordinary side.
func twoSidedContinuationFallbackAfterPathGap(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
) JointDistanceQuantityDecision {
	d := JointDistanceQuantityDecision{
		Reason: "no two-sided continuation after terminal-path gap",
		Plan:   in.BasePlan,
	}
	if model == nil || in.Horizon <= 0 || in.PairEquityJPY <= 0 ||
		!in.BasePlan.AllowBid || !in.BasePlan.AllowAsk {
		return d
	}
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(
		in.BestBid, in.BestAsk, in.BasePlan.BidPrice, in.BasePlan.AskPrice)
	basePath := model.JointPathPayoffStatistics(
		in.Now, config, in.Horizon, buyDistance, sellDistance)
	if basePath.EffectiveSamples > 1 {
		return d
	}
	crossing := model.CrossingDecisionAtSideDistances(
		in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
	if !crossing.HasSufficientCrossings(config.HorizonMinSamples) ||
		crossing.NetRoundTripEdgeBps <= 0 {
		return d
	}
	projectionInput := in.Projection
	projectionInput.Horizon = in.Horizon
	projectionInput.DirectFillProbabilities = true
	projectionInput.FullRiskPromotion = true
	projectionInput.BuyFillProbability = crossing.BuyTouchProbability
	projectionInput.SellFillProbability = crossing.SellTouchProbability
	projectionInput.BothFillProbability = crossing.BothTouchProbability
	buy, sell := TargetRestoringTwoSidedContinuationNotionals(
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		in.PairEquityJPY,
		crossing.BuyTouchProbability, crossing.SellTouchProbability,
		crossing.BothTouchProbability,
		config.MakerFeeBps+config.AdverseSelectionBps,
		projectionInput.MinBuyNotionalJPY, projectionInput.MinSellNotionalJPY,
		projectionInput.MaxBuyNotionalJPY, projectionInput.MaxSellNotionalJPY)
	if buy <= 0 || sell <= 0 {
		return d
	}
	projectionInput.FastBuyNotionalJPY = buy
	projectionInput.FastSellNotionalJPY = sell
	projectionInput.MaxBuyNotionalJPY = buy
	projectionInput.MaxSellNotionalJPY = sell
	projection := ProbabilityCenteredQuoteNotionals(projectionInput)
	if !projection.Enabled || projection.BuyNotionalJPY <= 0 || projection.SellNotionalJPY <= 0 {
		return d
	}
	d.Enabled = true
	d.Applied = !config.JointDistanceQuantity.ShadowOnly
	d.SideSafeFallback = true
	d.Reason = "two-sided Fast continuation fallback after terminal-path gap"
	d.Plan.AllowBid = true
	d.Plan.AllowAsk = true
	d.Projection = projection
	d.Crossing = crossing
	setJointPathDecayDiagnostics(&d, basePath)
	d.ExpectedCycleJPY = crossing.BothTouchProbability *
		projection.ProjectedGrossNotionalJPY * crossing.NetRoundTripEdgeBps / 10_000
	d.KellyUtilityJPYHour = d.ExpectedCycleJPY / in.Horizon.Hours()
	d.LowerPnLJPYHour = d.KellyUtilityJPYHour
	d.RiskReducing = (in.Projection.CurrentInventoryNotionalJPY-in.Projection.TargetInventoryNotionalJPY)*
		(crossing.BuyTouchProbability*projection.BuyNotionalJPY-
			crossing.SellTouchProbability*projection.SellNotionalJPY) < 0
	if in.PairEquityJPY > 0 {
		d.PairCapitalUtilization = projection.ProjectedGrossNotionalJPY / in.PairEquityJPY
	}
	return d
}

// twoSidedQuoteContinuityFloorAfterJointRejection preserves the market-maker
// invariant that a valid quote state does not become an empty book merely
// because the completed terminal-path CE is non-positive.  This is narrower
// than a probability-only fallback:
//
//   - both sides must already be allowed by the Fast reservation and balance
//     policy;
//   - both sides must be exchange executable and inside hard inventory
//     headroom;
//   - the same-horizon BBO crossing posterior must have enough observations and
//     a positive fee-net round-trip edge;
//   - the corrective side uses the cost-adjusted Bellman quantity q*=g(g/W-c),
//     while the opposite side remains at its minimum executable cell.
//
// If the terminal posterior is measured adverse, this function does not claim
// positive terminal wealth and does not promote either side above the bounded
// continuation quantity.  It only prevents the two-sided quote from
// disappearing; all fills remain subject to the existing hard caps.
func twoSidedQuoteContinuityFloorAfterJointRejection(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
	rejected JointDistanceQuantityDecision,
) JointDistanceQuantityDecision {
	d := JointDistanceQuantityDecision{
		Reason: "no two-sided quote continuity floor",
		Plan:   in.BasePlan,
	}
	reject := func(reason string) JointDistanceQuantityDecision {
		d.ContinuityFloorReason = reason
		return d
	}
	if !config.JointDistanceQuantity.PreserveTwoSidedQuotes ||
		model == nil || in.Horizon <= 0 || in.PairEquityJPY <= 0 ||
		!in.BasePlan.AllowBid || !in.BasePlan.AllowAsk {
		return reject("disabled, invalid input, or base plan is not bilateral")
	}
	if in.BestBid <= 0 || in.BestAsk <= in.BestBid || in.MidPrice <= 0 {
		return reject("invalid BBO")
	}
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(
		in.BestBid, in.BestAsk, in.BasePlan.BidPrice, in.BasePlan.AskPrice)
	crossing := model.CrossingDecisionAtSideDistances(
		in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
	if !crossing.HasSufficientCrossings(config.HorizonMinSamples) ||
		crossing.NetRoundTripEdgeBps <= 0 {
		return reject("crossing samples or fee-net round-trip edge unavailable")
	}
	minBuy := positiveExecutableUnit(in.Projection.MinBuyNotionalJPY)
	minSell := positiveExecutableUnit(in.Projection.MinSellNotionalJPY)
	if minBuy <= 0 || minSell <= 0 {
		return reject("exchange minimum unavailable")
	}
	// A terminal downside cap is allowed to suppress multi-cell BUYs, but it
	// must not turn the quote loop into an empty book when the account still has
	// hard headroom for the venue minimum.  Re-admit exactly one minimum cell in
	// that case; this is not a quantity promotion and cannot exceed the raw
	// balance/headroom carried by the input.
	maxBuy := in.Projection.MaxBuyNotionalJPY
	maxSell := in.Projection.MaxSellNotionalJPY
	if maxBuy+1e-9 < minBuy {
		buyHeadroom := math.Max(0, in.Projection.UpperInventoryNotionalJPY-
			in.Projection.CurrentInventoryNotionalJPY)
		if in.AvailableBuyCapitalJPY+1e-9 < minBuy || buyHeadroom+1e-9 < minBuy {
			return reject("BUY minimum exceeds raw balance or hard headroom")
		}
		maxBuy = minBuy
	}
	if maxSell+1e-9 < minSell {
		sellHeadroom := math.Max(0, in.Projection.CurrentInventoryNotionalJPY-
			in.Projection.LowerInventoryNotionalJPY)
		if in.AvailableSellInventoryNotionalJPY+1e-9 < minSell || sellHeadroom+1e-9 < minSell {
			return reject("SELL minimum exceeds raw balance or hard headroom")
		}
		maxSell = minSell
	}
	// Fixed-point BUY/SELL conversions may leave a sub-ulp cap below the same
	// exchange minimum.  Align that lattice point before the Bellman quantity
	// check instead of turning a valid minimum into a false capacity rejection.
	maxBuy = math.Max(maxBuy, minBuy)
	maxSell = math.Max(maxSell, minSell)
	// The correction quantity is target-relative and cost-adjusted.  If the
	// posterior target is already close to current inventory, q* falls below
	// the venue minimum and both sides intentionally remain at one cell.
	corrective := TargetRestoringContinuationNotional(
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		in.PairEquityJPY,
		config.MakerFeeBps+config.AdverseSelectionBps)
	buy, sell := minBuy, minSell
	if in.Projection.CurrentInventoryNotionalJPY > in.Projection.TargetInventoryNotionalJPY {
		sell = math.Max(minSell, math.Min(maxSell, corrective))
	} else if in.Projection.CurrentInventoryNotionalJPY < in.Projection.TargetInventoryNotionalJPY {
		buy = math.Max(minBuy, math.Min(maxBuy, corrective))
	}
	capacityEpsilon := 1e-7 * math.Max(1, math.Max(maxBuy, maxSell))
	if buy <= 0 || sell <= 0 || buy > maxBuy+capacityEpsilon || sell > maxSell+capacityEpsilon {
		return reject(fmt.Sprintf("corrective quantity exceeds executable capacity (buy=%.15g sell=%.15g maxBuy=%.15g maxSell=%.15g minBuy=%.15g minSell=%.15g corrective=%.15g)", buy, sell, maxBuy, maxSell, minBuy, minSell, corrective))
	}

	projectionInput := in.Projection
	projectionInput.Horizon = in.Horizon
	projectionInput.FastBuyNotionalJPY = buy
	projectionInput.FastSellNotionalJPY = sell
	projectionInput.MinBuyNotionalJPY = minBuy
	projectionInput.MinSellNotionalJPY = minSell
	projectionInput.MaxBuyNotionalJPY = maxBuy
	projectionInput.MaxSellNotionalJPY = maxSell
	projectionInput.DirectFillProbabilities = true
	projectionInput.FullRiskPromotion = true
	projectionInput.FastBuyRestraint = 0
	projectionInput.FastSellRestraint = 0
	projectionInput.TargetContraction = 0
	projectionInput.BuyFillProbability = crossing.BuyTouchProbability
	projectionInput.SellFillProbability = crossing.SellTouchProbability
	projectionInput.BothFillProbability = crossing.BothTouchProbability
	projection := ProbabilityCenteredQuoteNotionals(projectionInput)
	if !projection.Enabled || projection.BuyNotionalJPY <= 0 ||
		projection.SellNotionalJPY <= 0 {
		return reject("probability projection cannot retain both minimum sides")
	}

	// Use only the observable fee-net round-trip edge for the continuity
	// score.  The terminal path was the reason the ordinary optimizer rejected
	// the pair, so reporting a positive terminal CE here would be false.  The
	// score is used solely to compare this bounded floor against NO_ORDER when
	// joint horizon selection is enabled.
	cycleNotional := math.Min(
		crossing.BuyTouchProbability*projection.BuyNotionalJPY,
		crossing.SellTouchProbability*projection.SellNotionalJPY)
	cyclePnL := cycleNotional * crossing.NetRoundTripEdgeBps / 10_000
	if cycleNotional <= 0 || cyclePnL <= 0 || math.IsNaN(cyclePnL) || math.IsInf(cyclePnL, 0) {
		return reject("crossing cycle probability or fee-net score is non-positive")
	}
	beforeError := in.Projection.CurrentInventoryNotionalJPY -
		in.Projection.TargetInventoryNotionalJPY
	afterError := beforeError +
		crossing.BuyTouchProbability*projection.BuyNotionalJPY -
		crossing.SellTouchProbability*projection.SellNotionalJPY
	d.Enabled = true
	d.Applied = !config.JointDistanceQuantity.ShadowOnly
	d.AuthoritativeRejection = false
	d.SideSafeFallback = true
	d.ContinuityFloorApplied = true
	d.Reason = "two-sided quote continuity floor after terminal-wealth rejection"
	if rejected.Reason != "" {
		d.Reason += "; prior=" + rejected.Reason
	}
	d.Plan = in.BasePlan
	d.Plan.AllowBid = true
	d.Plan.AllowAsk = true
	d.Projection = projection
	d.Crossing = crossing
	d.CandidateCount = rejected.CandidateCount
	d.PathEffectiveSamples = rejected.PathEffectiveSamples
	d.ExpectedCycleJPY = cycleNotional
	d.ExpectedPnLJPYHour = cyclePnL / in.Horizon.Hours()
	// No lower terminal-PnL bound is asserted by a continuity floor.
	d.LowerPnLJPYHour = 0
	d.KellyUtilityJPYHour = cyclePnL / in.Horizon.Hours()
	d.FeeValueMeanJPY = cyclePnL
	d.FeeValueNetJPY = cyclePnL
	d.RiskReducing = math.Abs(afterError) < math.Abs(beforeError)
	d.PairCapitalUtilization = projection.ProjectedGrossNotionalJPY / in.PairEquityJPY
	if in.Projection.FastBuyNotionalJPY+in.Projection.FastSellNotionalJPY > 0 {
		d.CapitalUtilization = projection.ProjectedGrossNotionalJPY /
			(in.Projection.FastBuyNotionalJPY + in.Projection.FastSellNotionalJPY)
		d.QuantityScale = d.CapitalUtilization
	}
	return d
}

// targetContinuationFallbackAfterPathGap keeps the quote loop from treating a
// missing completed path as a negative terminal-wealth posterior.  It admits
// only the side that reduces the current target error, at the venue minimum,
// and charges the expected maker/adverse cost.  If the base-price path has at
// least two effective observations this helper is deliberately inactive: a
// measured negative terminal CE must continue to veto the side.
func targetContinuationFallbackAfterPathGap(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
) JointDistanceQuantityDecision {
	d := JointDistanceQuantityDecision{
		Reason: "no target-restoring continuation after terminal-path gap",
		Plan:   in.BasePlan,
	}
	if model == nil || in.Horizon <= 0 || in.PairEquityJPY <= 0 {
		return d
	}
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(
		in.BestBid, in.BestAsk, in.BasePlan.BidPrice, in.BasePlan.AskPrice)
	basePath := model.JointPathPayoffStatistics(
		in.Now, config, in.Horizon, buyDistance, sellDistance)
	if basePath.EffectiveSamples > 1 {
		return d
	}
	crossing := model.CrossingDecisionAtSideDistances(
		in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
	if !crossing.HasSufficientCrossings(config.HorizonMinSamples) {
		return d
	}
	buy := in.Projection.CurrentInventoryNotionalJPY <
		in.Projection.TargetInventoryNotionalJPY
	sell := in.Projection.CurrentInventoryNotionalJPY >
		in.Projection.TargetInventoryNotionalJPY
	if (!buy && !sell) || (buy && !in.BasePlan.AllowBid) || (sell && !in.BasePlan.AllowAsk) {
		return d
	}
	projectionInput := in.Projection
	projectionInput.Horizon = in.Horizon
	projectionInput.DirectFillProbabilities = true
	projectionInput.FullRiskPromotion = true
	projectionInput.BuyFillProbability = crossing.BuyTouchProbability
	projectionInput.SellFillProbability = crossing.SellTouchProbability
	projectionInput.BothFillProbability = 0
	continuationCostBps := config.MakerFeeBps + config.AdverseSelectionBps
	continuationNotional := TargetRestoringContinuationNotional(
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		in.PairEquityJPY, continuationCostBps)
	if buy {
		unit := positiveExecutableUnit(projectionInput.MinBuyNotionalJPY)
		if unit <= 0 || projectionInput.FastBuyNotionalJPY+1e-9 < unit ||
			projectionInput.MaxBuyNotionalJPY+1e-9 < unit {
			return d
		}
		projectionInput.FastSellNotionalJPY = 0
		projectionInput.MinSellNotionalJPY = 0
		projectionInput.MaxSellNotionalJPY = 0
		projectionInput.MaxBuyNotionalJPY = math.Max(unit,
			math.Min(projectionInput.MaxBuyNotionalJPY, continuationNotional))
		projectionInput.FastBuyNotionalJPY = projectionInput.MaxBuyNotionalJPY
	} else {
		unit := positiveExecutableUnit(projectionInput.MinSellNotionalJPY)
		if unit <= 0 || projectionInput.FastSellNotionalJPY+1e-9 < unit ||
			projectionInput.MaxSellNotionalJPY+1e-9 < unit {
			return d
		}
		projectionInput.FastBuyNotionalJPY = 0
		projectionInput.MinBuyNotionalJPY = 0
		projectionInput.MaxBuyNotionalJPY = 0
		projectionInput.MaxSellNotionalJPY = math.Max(unit,
			math.Min(projectionInput.MaxSellNotionalJPY, continuationNotional))
		projectionInput.FastSellNotionalJPY = projectionInput.MaxSellNotionalJPY
	}
	projection := ProbabilityCenteredQuoteNotionals(projectionInput)
	if !projection.Enabled || projection.ProjectedGrossNotionalJPY <= 0 {
		return d
	}
	net := RiskReducingContinuationNetValue(
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		in.PairEquityJPY,
		projection.BuyNotionalJPY, projection.SellNotionalJPY,
		crossing.BuyTouchProbability, crossing.SellTouchProbability,
		crossing.BothTouchProbability,
		config.MakerFeeBps+config.AdverseSelectionBps)
	if net <= 0 || math.IsNaN(net) || math.IsInf(net, 0) {
		return d
	}
	d.Enabled = true
	d.Applied = !config.JointDistanceQuantity.ShadowOnly
	d.SideSafeFallback = true
	d.Reason = "risk-reducing Fast fallback from target continuation prior after terminal-path gap"
	d.Plan.AllowBid = buy
	d.Plan.AllowAsk = sell
	d.Projection = projection
	d.Crossing = crossing
	d.RiskReducing = true
	d.KellyUtilityJPYHour = net / in.Horizon.Hours()
	d.LowerPnLJPYHour = d.KellyUtilityJPYHour
	setJointPathDecayDiagnostics(&d, basePath)
	d.ExpectedCycleJPY = projection.ProjectedGrossNotionalJPY *
		math.Max(crossing.BuyTouchProbability, crossing.SellTouchProbability)
	if in.PairEquityJPY > 0 {
		d.PairCapitalUtilization = projection.ProjectedGrossNotionalJPY / in.PairEquityJPY
	}
	return d
}

// sideSafeFallbackAfterJointRejection repairs a subtle admission bug in the
// unified controller.  The bilateral search can quite correctly reject a
// pair because its covariance/terminal-wealth utility is non-positive while
// one side, evaluated on its own exchange cell, still has positive utility.
// Treating that pair rejection as "cancel both sides" loses a risk-reducing
// SELL (or a fee-positive BUY) and leaves the quote loop with no executable
// action.  The fallback below keeps exactly the higher-scoring supported side,
// re-runs the same crossing, fee-regret, inventory and hard-cap checks at the
// retained price, and uses a target-restoring Bellman quantity when the
// terminal path is missing. It never resurrects a side whose measured
// independent posterior score was non-positive.
func sideSafeFallbackAfterJointRejection(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
	rejected JointDistanceQuantityDecision,
) JointDistanceQuantityDecision {
	d := JointDistanceQuantityDecision{
		Reason: "no independently supported side after joint rejection",
		Plan:   in.BasePlan,
	}
	buySupported := rejected.FallbackBuySupported &&
		rejected.FallbackBuyScore > 0 && !math.IsNaN(rejected.FallbackBuyScore)
	sellSupported := rejected.FallbackSellSupported &&
		rejected.FallbackSellScore > 0 && !math.IsNaN(rejected.FallbackSellScore)
	if !buySupported && !sellSupported || model == nil || in.Horizon <= 0 {
		return d
	}
	// A paired rejection must not turn into a two-sided quote. Select the
	// stronger independent posterior deterministically; ties prefer SELL when
	// inventory is above target and BUY otherwise.
	buy, ok := chooseSideSafeFallback(
		buySupported, sellSupported,
		rejected.FallbackBuyScore, rejected.FallbackSellScore,
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY)
	if !ok {
		return d
	}
	sell := !buy
	plan := in.BasePlan
	if buy {
		applyQuotePlanSide(&plan, rejected.FallbackBuyPlan, true)
		plan.AllowBid = true
		plan.AllowAsk = false
	} else {
		applyQuotePlanSide(&plan, rejected.FallbackSellPlan, false)
		plan.AllowBid = false
		plan.AllowAsk = true
	}
	if plan.BidPrice <= 0 || plan.AskPrice <= plan.BidPrice {
		return d
	}
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(
		in.BestBid, in.BestAsk, plan.BidPrice, plan.AskPrice)
	crossing := model.CrossingDecisionAtSideDistances(
		in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
	if !crossing.HasSufficientCrossings(config.HorizonMinSamples) {
		return d
	}
	riskReducingSide := (buy && in.Projection.CurrentInventoryNotionalJPY <
		in.Projection.TargetInventoryNotionalJPY) ||
		(sell && in.Projection.CurrentInventoryNotionalJPY >
			in.Projection.TargetInventoryNotionalJPY)
	// A fee-positive completed cycle remains mandatory for an ordinary side.
	// A target-restoring side is different: its continuation value can pay the
	// one maker/adverse-selection cost even when the opposite completion leg has
	// no positive cycle edge.  The terminal-path check below still decides this
	// from posterior wealth whenever path data exists.
	if crossing.NetRoundTripEdgeBps <= 0 && !riskReducingSide {
		return d
	}
	projectionInput := in.Projection
	projectionInput.Horizon = in.Horizon
	projectionInput.DirectFillProbabilities = true
	projectionInput.FullRiskPromotion = true
	projectionInput.BuyFillProbability = crossing.BuyTouchProbability
	projectionInput.SellFillProbability = crossing.SellTouchProbability
	projectionInput.BothFillProbability = 0
	if buy {
		unit := positiveExecutableUnit(projectionInput.MinBuyNotionalJPY)
		if unit <= 0 || projectionInput.FastBuyNotionalJPY+1e-9 < unit ||
			projectionInput.MaxBuyNotionalJPY+1e-9 < unit {
			return d
		}
		projectionInput.FastSellNotionalJPY = 0
		projectionInput.MinSellNotionalJPY = 0
		projectionInput.MaxSellNotionalJPY = 0
		// The bilateral score was rejected, but terminal-path data is absent. Use
		// the target-restoring continuation optimum rather than hard-coding one
		// exchange cell; the quantity remains clipped by the same account/risk
		// capacity and cannot recreate the rejected bilateral covariance.
		continuationNotional := TargetRestoringContinuationNotional(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			in.PairEquityJPY,
			config.MakerFeeBps+config.AdverseSelectionBps)
		projectionInput.MaxBuyNotionalJPY = math.Max(unit,
			math.Min(projectionInput.MaxBuyNotionalJPY, continuationNotional))
		projectionInput.FastBuyNotionalJPY = projectionInput.MaxBuyNotionalJPY
	} else {
		unit := positiveExecutableUnit(projectionInput.MinSellNotionalJPY)
		if unit <= 0 || projectionInput.FastSellNotionalJPY+1e-9 < unit ||
			projectionInput.MaxSellNotionalJPY+1e-9 < unit {
			return d
		}
		projectionInput.FastBuyNotionalJPY = 0
		projectionInput.MinBuyNotionalJPY = 0
		projectionInput.MaxBuyNotionalJPY = 0
		continuationNotional := TargetRestoringContinuationNotional(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			in.PairEquityJPY,
			config.MakerFeeBps+config.AdverseSelectionBps)
		projectionInput.MaxSellNotionalJPY = math.Max(unit,
			math.Min(projectionInput.MaxSellNotionalJPY, continuationNotional))
		projectionInput.FastSellNotionalJPY = projectionInput.MaxSellNotionalJPY
	}
	projection := ProbabilityCenteredQuoteNotionals(projectionInput)
	if !projection.Enabled || projection.ProjectedGrossNotionalJPY <= 0 ||
		(buy && projection.BuyNotionalJPY <= 0) ||
		(sell && projection.SellNotionalJPY <= 0) {
		return d
	}
	stats := model.JointPathPayoffStatistics(
		in.Now, config, in.Horizon, buyDistance, sellDistance)
	riskAversion := in.RiskAversion
	if riskAversion <= 0 {
		riskAversion = fastRiskAversionOrDefault(config, riskAversion)
	}
	mean, regret, net := 0.0, 0.0, 0.0
	admittedUtility := 0.0
	riskReducingAdmission := false
	continuationOnly := stats.EffectiveSamples <= 1
	var payoff JointPathPayoffDecision
	if continuationOnly {
		if !riskReducingSide {
			return d
		}
		// No terminal path is available at this price.  Do not fabricate a
		// markout mean; use only target continuation and the expected cost of the
		// side that can touch.  This keeps the fallback finite and one-sided.
		net = RiskReducingContinuationNetValue(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			in.PairEquityJPY,
			projection.BuyNotionalJPY, projection.SellNotionalJPY,
			crossing.BuyTouchProbability, crossing.SellTouchProbability,
			crossing.BothTouchProbability,
			config.MakerFeeBps+config.AdverseSelectionBps)
		if net <= 0 || math.IsNaN(net) || math.IsInf(net, 0) {
			return d
		}
		admittedUtility = net
		riskReducingAdmission = true
	} else {
		payoff = stats.EvaluateTargetRelativePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			projection.BuyNotionalJPY, projection.SellNotionalJPY,
			in.PairEquityJPY, riskAversion, 0)
		mean, regret, net = fastFeeNetRegretValue(payoff)
		legacyContinuation := matureTargetProgressContinuationValue(
			config.JointDistanceQuantity.LegacyStackedTargetContinuation,
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			in.PairEquityJPY, projection.BuyNotionalJPY, projection.SellNotionalJPY,
			crossing.BuyTouchProbability, crossing.SellTouchProbability,
			crossing.BothTouchProbability)
		net += legacyContinuation
		admittedUtility = net
		if net <= 0 || math.IsNaN(net) || math.IsInf(net, 0) {
			// A one-sided target-restoring hedge is admitted only by its
			// confidence-adjusted whole-position CE. This preserves the old
			// rejection for an ordinary fee-negative trade while avoiding an empty
			// book when the only supported action is variance reducing.
			confidenceZ := in.ConfidenceZScore
			if confidenceZ <= 0 {
				confidenceZ = config.InventoryRiskZScore
			}
			robust := stats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				projection.BuyNotionalJPY, projection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, confidenceZ)
			admittedScore, admitted, riskReducing := sideSafeFallbackAdmissionForReplay(
				net, robust, legacyContinuation,
				config.JointDistanceQuantity.LegacyStackedTargetContinuation)
			if !admitted {
				return d
			}
			admittedUtility = admittedScore
			riskReducingAdmission = riskReducing
		}
	}
	confidenceZ := in.ConfidenceZScore
	if confidenceZ <= 0 {
		confidenceZ = config.InventoryRiskZScore
	}
	confidence := stats.EvaluateTargetRelativePosition(
		in.Projection.CurrentInventoryNotionalJPY,
		in.Projection.TargetInventoryNotionalJPY,
		projection.BuyNotionalJPY, projection.SellNotionalJPY,
		in.PairEquityJPY, riskAversion, confidenceZ)
	d.Enabled = true
	d.Applied = !config.JointDistanceQuantity.ShadowOnly
	d.SideSafeFallback = true
	d.AuthoritativeRejection = false
	d.Reason = "independent side-safe Fast fallback after bilateral rejection"
	if riskReducingAdmission {
		d.Reason = "risk-reducing side-safe Fast fallback after bilateral rejection"
	}
	if continuationOnly {
		d.Reason = "risk-reducing side-safe Fast fallback from target continuation prior"
	}
	d.Plan = plan
	d.Projection = projection
	d.Crossing = crossing
	d.SelectedCandidate = -1
	d.SelectedQuantityCandidate = -1
	d.ExpectedCycleJPY = projection.ProjectedGrossNotionalJPY *
		math.Max(crossing.BuyTouchProbability, crossing.SellTouchProbability)
	if continuationOnly {
		d.ExpectedPnLJPYHour = 0
		d.LowerPnLJPYHour = admittedUtility / in.Horizon.Hours()
		d.PathStdErrorJPYHour = 0
		d.KellyPenaltyJPYHour = 0
	} else {
		d.ExpectedPnLJPYHour = payoff.ExpectedPnLJPY / in.Horizon.Hours()
		d.LowerPnLJPYHour = confidence.CertaintyEquivalent / in.Horizon.Hours()
		d.PathStdErrorJPYHour = payoff.StdErrorJPY / in.Horizon.Hours()
		d.KellyPenaltyJPYHour = payoff.KellyPenaltyJPY / in.Horizon.Hours()
	}
	d.KellyUtilityJPYHour = admittedUtility / in.Horizon.Hours()
	d.FeeValueMeanJPY = mean
	d.FeeValueDownsideRegretJPY = regret
	d.FeeValueNetJPY = net
	if continuationOnly {
		d.PathPositiveConfidence = 0
		d.RiskReducing = true
	} else {
		d.PathPositiveConfidence = jointPathUtilityConfidence(payoff)
		d.ExistingInventoryExpectedPnLJPY = payoff.ExistingInventoryExpectedPnLJPY
		d.BaselineVarianceJPY2 = payoff.BaselineVarianceJPY2
		d.WholePositionVarianceJPY2 = payoff.WholePositionVarianceJPY2
		d.MarginalVarianceJPY2 = payoff.MarginalVarianceJPY2
		d.InventoryOrderCovarianceJPY2 = payoff.InventoryOrderCovarianceJPY2
		d.RiskReducing = payoff.RiskReducing
	}
	setJointPathDecayDiagnostics(&d, stats)
	if in.PairEquityJPY > 0 {
		d.PairCapitalUtilization = projection.ProjectedGrossNotionalJPY / in.PairEquityJPY
	}
	return d
}

func chooseSideSafeFallback(
	buySupported, sellSupported bool, buyScore, sellScore,
	currentInventoryNotionalJPY, targetInventoryNotionalJPY float64,
) (buy, ok bool) {
	if !buySupported && !sellSupported {
		return false, false
	}
	if buySupported && !sellSupported {
		return true, true
	}
	if sellSupported && !buySupported {
		return false, true
	}
	if buyScore > sellScore+1e-12 {
		return true, true
	}
	if sellScore > buyScore+1e-12 {
		return false, true
	}
	return currentInventoryNotionalJPY <= targetInventoryNotionalJPY, true
}

// targetRestoringFastContinuation supplies the Bellman continuation condition
// for a long-only maker. When balances make exactly one side executable,
// treating "no new order" as a terminal state assigns zero continuation value
// to an absorbing inventory boundary: a full-base account can never free quote
// to place the next BUY, and a full-quote account can never acquire base for
// the next SELL. After a partial boundary fill, both sides can become executable
// while a material target error remains; the same scale-free proximal control
// keeps only the target-restoring side alive. Small interior errors remain in
// the endogenous no-trade region below the exchange minimum. A correction must
// pay its own one-way fee, posterior markout and whole-position risk, but it
// does not have to pay the reservation profit of a hypothetical later cycle.
func targetRestoringFastContinuation(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
	projectionInput ProbabilityCenteredQuoteInput,
) JointDistanceQuantityDecision {
	d := JointDistanceQuantityDecision{
		Reason: "no target-restoring boundary continuation",
		Plan:   in.BasePlan,
	}
	if model == nil || in.PairEquityJPY <= 0 {
		d.Reason = "boundary continuation has invalid model or equity"
		return d
	}
	// Feasibility is tested with the venue minimum, not Fast's desired ticket.
	// The desired ticket can exceed available balance at the exact boundary this
	// helper is meant to release; comparing it with Max* would incorrectly mark
	// both sides infeasible and recreate the absorbing state. Once the feasible
	// side is known, however, quantity is a scale-free proximal partial control,
	// not an exchange-minimum probe. A full q=g correction is optimal only if the
	// estimated target is fixed. Fast's target is re-estimated online, so close a
	// fraction of the gap equal to the current portfolio-weight error:
	//
	//   q* = argmin_q 1/2(q-g)^2 + 1/2(W/g-1)q^2 = g^2/W
	//      = g |w-w*|.
	//
	// The target-revision regularizer is strongest near target and vanishes at a
	// full-equity displacement. This suppresses churn from small target changes
	// while increasing size smoothly at a genuine account boundary, without a
	// fitted threshold. The probability-centered projection below retains its
	// confidence constraint and directional restraint.
	buyUnit := math.Max(0, projectionInput.MinBuyNotionalJPY)
	sellUnit := math.Max(0, projectionInput.MinSellNotionalJPY)
	buyCapacityFeasible := buyUnit > 0 &&
		projectionInput.MaxBuyNotionalJPY+1e-9 >= buyUnit
	sellCapacityFeasible := sellUnit > 0 &&
		projectionInput.MaxSellNotionalJPY+1e-9 >= sellUnit
	buyFeasible := in.BasePlan.AllowBid && buyCapacityFeasible
	sellFeasible := in.BasePlan.AllowAsk && sellCapacityFeasible
	if !buyFeasible && !sellFeasible {
		d.Reason = "target-restoring continuation has no feasible side"
		return d
	}
	current, target := projectionInput.CurrentInventoryNotionalJPY,
		projectionInput.TargetInventoryNotionalJPY
	interiorProximalControl := false
	if buyFeasible && sellFeasible {
		// An authoritative terminal-path rejection can otherwise create an
		// absorbing no-order state immediately after a partial fill: both balances
		// become executable, so the exact-boundary continuation disappears even
		// though inventory remains materially away from the same-horizon target.
		// Reuse the scale-free proximal control derived below and admit only its
		// target-restoring side. Requiring the one-way-cost-adjusted Bellman q* to
		// reach the venue minimum is the endogenous no-trade region; it preserves an authoritative rejection
		// for small target errors without introducing a fitted inventory threshold.
		proximalControl := TargetRestoringContinuationNotional(
			current, target, in.PairEquityJPY,
			config.MakerFeeBps+config.AdverseSelectionBps)
		switch {
		case current < target && proximalControl+1e-9 >= buyUnit:
			sellFeasible = false
			interiorProximalControl = true
		case current > target && proximalControl+1e-9 >= sellUnit:
			buyFeasible = false
			interiorProximalControl = true
		default:
			d.Reason = "interior target-restoring proximal control is below executable minimum"
			return d
		}
	}
	if (buyFeasible && current >= target-1e-9) ||
		(sellFeasible && current <= target+1e-9) {
		d.Reason = "boundary continuation side does not restore target"
		return d
	}
	// Only a genuine account/exchange-capacity boundary receives the smooth
	// partial adjustment. If both sides have capacity but the quote policy
	// suppresses one, the terminal optimizer has already declined promotion;
	// preserve only the minimum continuation cell instead of laundering that
	// policy decision into a large "balance-boundary" order.
	buyBalanceFeasible := buyUnit > 0 && in.AvailableBuyCapitalJPY+1e-9 >= buyUnit
	sellBalanceFeasible := sellUnit > 0 && in.AvailableSellInventoryNotionalJPY+1e-9 >= sellUnit
	if in.AvailableBuyCapitalJPY <= 0 && in.AvailableSellInventoryNotionalJPY <= 0 {
		// Compatibility for focused unit fixtures that predate explicit raw
		// balance capacities. Production and replay always provide both fields.
		buyBalanceFeasible = buyCapacityFeasible
		sellBalanceFeasible = sellCapacityFeasible
	}
	accountBoundary := buyBalanceFeasible != sellBalanceFeasible
	partialTargetControl := 0.0
	if accountBoundary || interiorProximalControl {
		partialTargetControl = TargetRestoringContinuationNotional(
			current, target, in.PairEquityJPY,
			config.MakerFeeBps+config.AdverseSelectionBps)
	}
	controlNotional := 0.0
	if buyFeasible {
		controlNotional = math.Min(projectionInput.MaxBuyNotionalJPY, partialTargetControl)
		if controlNotional+1e-9 < buyUnit {
			controlNotional = buyUnit
		}
	} else {
		controlNotional = math.Min(projectionInput.MaxSellNotionalJPY, partialTargetControl)
		if controlNotional+1e-9 < sellUnit {
			controlNotional = sellUnit
		}
	}
	// At a genuine account boundary, price and quantity must solve the same
	// target-relative problem. Inheriting Fast's interior distance would size a
	// larger corrective order while leaving it at the same low-arrival edge.
	// Search only the O(K) one-sided inward ladder and keep the opposite
	// reference fixed. This candidate is a correction, not a completed cycle:
	// maximize one-sided posterior terminal wealth plus target continuation
	// without charging MinimumNetEdgeBps.
	if accountBoundary || interiorProximalControl {
		candidateCount := config.JointDistanceQuantity.CandidateCount
		if candidateCount < 2 {
			candidateCount = 2
		}
		baseBuyDistance, baseSellDistance, _ := MakerTouchDistances(
			in.BestBid, in.BestAsk, in.BasePlan.BidPrice, in.BasePlan.AskPrice)
		conditionalState := model.conditionalExecutionState(in.Horizon)
		conditionalReady := config.ConditionalExecution.Enabled && conditionalState.Valid
		z := in.ConfidenceZScore
		if z <= 0 {
			z = config.InventoryRiskZScore
		}
		// The inward ladder contains candidateCount-1 correlated concessions.
		// Family-wise one-sided error is controlled with the same simultaneous
		// Gaussian bound used by the ordinary distance optimizer.
		inwardZ := simultaneousOneSidedZ(z, candidateCount-1)
		bestCE := math.Inf(-1)
		selectedInwardBuy, selectedInwardSell := false, false
		selectedConditionalBuy, selectedConditionalSell :=
			ConditionalExecutionSideDecision{}, ConditionalExecutionSideDecision{}
		for _, candidate := range jointDistanceCandidatePlans(
			in.BasePlan, in.BestBid, in.BestAsk, in.MidPrice,
			config.MaximumHalfSpreadBps, conditionalReady, candidateCount) {
			if buyFeasible {
				if math.Abs(candidate.AskPrice-in.BasePlan.AskPrice) > 1e-9 ||
					candidate.BidPrice+1e-9 < in.BasePlan.BidPrice || candidate.BidPrice > in.BestBid+1e-9 {
					continue
				}
			} else if math.Abs(candidate.BidPrice-in.BasePlan.BidPrice) > 1e-9 ||
				candidate.AskPrice+1e-9 < in.BestAsk || candidate.AskPrice > in.BasePlan.AskPrice+1e-9 {
				continue
			}
			candidateBuyDistance, candidateSellDistance, candidateGrossEdge := MakerTouchDistances(
				in.BestBid, in.BestAsk, candidate.BidPrice, candidate.AskPrice)
			inwardBuy := buyFeasible && candidate.BidPrice > in.BasePlan.BidPrice*(1+1e-12)
			inwardSell := sellFeasible && candidate.AskPrice < in.BasePlan.AskPrice*(1-1e-12)
			conditionalBuy, conditionalSell :=
				ConditionalExecutionSideDecision{}, ConditionalExecutionSideDecision{}
			if inwardBuy {
				conditionalBuy = model.conditionalExecutionSideDecision(
					in.Now, config, in.Horizon, conditionalState, true,
					baseBuyDistance, candidateBuyDistance)
				if !inwardDistanceImprovementSupported(conditionalBuy, inwardZ) {
					continue
				}
			}
			if inwardSell {
				conditionalSell = model.conditionalExecutionSideDecision(
					in.Now, config, in.Horizon, conditionalState, false,
					baseSellDistance, candidateSellDistance)
				if !inwardDistanceImprovementSupported(conditionalSell, inwardZ) {
					continue
				}
			}
			stats := model.JointPathPayoffStatistics(
				in.Now, config, in.Horizon, candidateBuyDistance, candidateSellDistance)
			if inwardBuy || inwardSell {
				stats = model.conditionalJointPathPayoffStatistics(
					in.Now, config, in.Horizon, candidateBuyDistance, candidateSellDistance,
					conditionalState)
			}
			if stats.EffectiveSamples <= 1 {
				continue
			}
			buyNotional, sellNotional := 0.0, controlNotional
			if buyFeasible {
				buyNotional, sellNotional = controlNotional, 0
			}
			riskAversion := in.RiskAversion
			if riskAversion <= 0 {
				riskAversion = fastRiskAversionOrDefault(config, riskAversion)
			}
			crossing := model.CrossingDecisionAtSideDistances(
				in.Now, config, in.Horizon,
				candidateBuyDistance, candidateSellDistance, candidateGrossEdge)
			if inwardBuy || inwardSell {
				crossing = model.conditionalCrossingDecision(
					in.Now, config, in.Horizon,
					candidateBuyDistance, candidateSellDistance,
					candidateGrossEdge, conditionalState)
			}
			value := targetRestoringOrderNetValue(
				stats, current, target, in.PairEquityJPY,
				buyNotional, sellNotional,
				crossing.BuyTouchProbability,
				crossing.SellTouchProbability,
				0, riskAversion,
				config.MakerFeeBps+config.AdverseSelectionBps,
				z,
				config.JointDistanceQuantity.LegacyStackedTargetContinuation)
			if value > bestCE {
				bestCE = value
				d.Plan = candidate
				selectedInwardBuy, selectedInwardSell = inwardBuy, inwardSell
				selectedConditionalBuy, selectedConditionalSell = conditionalBuy, conditionalSell
			}
		}
		d.InwardBuyEligible = conditionalReady && buyFeasible &&
			in.BestBid > in.BasePlan.BidPrice*(1+1e-12)
		d.InwardSellEligible = conditionalReady && sellFeasible &&
			in.BasePlan.AskPrice > in.BestAsk*(1+1e-12)
		d.InwardBuySelected, d.InwardSellSelected = selectedInwardBuy, selectedInwardSell
		d.ConditionalBuy, d.ConditionalSell = selectedConditionalBuy, selectedConditionalSell
		if selectedInwardBuy {
			d.SelectedInwardBuyDeltaBps = math.Log(d.Plan.BidPrice/in.BasePlan.BidPrice) * 10_000
		}
		if selectedInwardSell {
			d.SelectedInwardSellDeltaBps = math.Log(in.BasePlan.AskPrice/d.Plan.AskPrice) * 10_000
		}
	}
	buyDistance, sellDistance, grossEdge := MakerTouchDistances(
		in.BestBid, in.BestAsk, d.Plan.BidPrice, d.Plan.AskPrice)
	crossing := model.CrossingDecisionAtSideDistances(
		in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
	if conditionalState := model.conditionalExecutionState(in.Horizon); config.ConditionalExecution.Enabled && conditionalState.Valid &&
		(d.InwardBuySelected || d.InwardSellSelected) {
		crossing = model.conditionalCrossingDecision(
			in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge,
			conditionalState)
	}
	if (buyFeasible && crossing.BuyTouchProbability <= 0) ||
		(sellFeasible && crossing.SellTouchProbability <= 0) {
		d.Reason = "boundary continuation opening touch is unsupported"
		return d
	}
	floorInput := projectionInput
	floorInput.DirectFillProbabilities = true
	floorInput.FullRiskPromotion = false
	floorInput.BuyFillProbability = crossing.BuyTouchProbability
	floorInput.SellFillProbability = crossing.SellTouchProbability
	floorInput.BothFillProbability = 0
	if buyFeasible {
		floorInput.FastBuyNotionalJPY = controlNotional
		floorInput.MinBuyNotionalJPY = buyUnit
		floorInput.MaxBuyNotionalJPY = controlNotional
		floorInput.FastSellNotionalJPY = 0
		floorInput.MinSellNotionalJPY = 0
		floorInput.MaxSellNotionalJPY = 0
		d.Plan.AllowAsk = false
	} else {
		floorInput.FastSellNotionalJPY = controlNotional
		floorInput.MinSellNotionalJPY = sellUnit
		floorInput.MaxSellNotionalJPY = controlNotional
		floorInput.FastBuyNotionalJPY = 0
		floorInput.MinBuyNotionalJPY = 0
		floorInput.MaxBuyNotionalJPY = 0
		d.Plan.AllowBid = false
	}
	projection := ProbabilityCenteredQuoteNotionals(floorInput)
	if !projection.Enabled || projection.ProjectedGrossNotionalJPY <= 0 {
		d.Reason = "boundary continuation projection rejected: " + projection.Reason
		return d
	}
	riskAversion := in.RiskAversion
	if riskAversion <= 0 {
		riskAversion = fastRiskAversionOrDefault(config, riskAversion)
	}
	stats := model.JointPathPayoffStatistics(
		in.Now, config, in.Horizon, buyDistance, sellDistance)
	if conditionalState := model.conditionalExecutionState(in.Horizon); config.ConditionalExecution.Enabled && conditionalState.Valid &&
		(d.InwardBuySelected || d.InwardSellSelected) {
		stats = model.conditionalJointPathPayoffStatistics(
			in.Now, config, in.Horizon, buyDistance, sellDistance,
			conditionalState)
	}
	continuationNet := targetRestoringOrderNetValue(
		stats, current, target, in.PairEquityJPY,
		projection.BuyNotionalJPY, projection.SellNotionalJPY,
		crossing.BuyTouchProbability, crossing.SellTouchProbability,
		0, riskAversion, config.MakerFeeBps+config.AdverseSelectionBps,
		in.ConfidenceZScore,
		config.JointDistanceQuantity.LegacyStackedTargetContinuation)
	if continuationNet <= 0 || math.IsNaN(continuationNet) || math.IsInf(continuationNet, 0) {
		d.Reason = "target-restoring order has non-positive posterior continuation value"
		return d
	}
	d.Enabled = true
	d.Applied = !config.JointDistanceQuantity.ShadowOnly
	d.SideSafeFallback = true
	d.Reason = "target-restoring one-sided policy continuation floor"
	if accountBoundary {
		d.Reason = "target-restoring account-boundary continuation control"
	} else if interiorProximalControl {
		d.Reason = "target-restoring interior proximal continuation control"
	}
	d.Projection = projection
	d.Crossing = crossing
	d.ExpectedCycleJPY = 0
	d.FeeValueNetJPY = continuationNet
	d.KellyUtilityJPYHour = continuationNet / in.Horizon.Hours()
	d.LowerPnLJPYHour = d.KellyUtilityJPYHour
	d.PairCapitalUtilization = projection.ProjectedGrossNotionalJPY / in.PairEquityJPY
	beforeError := current - target
	afterFillError := beforeError
	if buyFeasible {
		afterFillError += projection.BuyNotionalJPY
	} else {
		afterFillError -= projection.SellNotionalJPY
	}
	d.RiskReducing = afterFillError*afterFillError < beforeError*beforeError
	return d
}

func fastDecisionDropsBaselineSide(
	decision, baseline JointDistanceQuantityDecision,
	basePlan MarketMakerQuotePlan,
) bool {
	if !baseline.Enabled {
		return false
	}
	return (basePlan.AllowBid && baseline.Projection.BuyNotionalJPY > 0 &&
		decision.Projection.BuyNotionalJPY <= 0) ||
		(basePlan.AllowAsk && baseline.Projection.SellNotionalJPY > 0 &&
			decision.Projection.SellNotionalJPY <= 0)
}

// OptimizeJointDistanceQuantity selects one physical order level per side from
// an outward distance ladder, and sizes both orders in one Bernoulli inventory
// model. Multiple candidate levels are alternatives, not simultaneous orders;
// after a fill, the normal quote loop recomputes the full allocation.
func OptimizeJointDistanceQuantity(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	in JointDistanceQuantityInput,
) JointDistanceQuantityDecision {
	d := JointDistanceQuantityDecision{
		Reason: "invalid input",
		Plan:   in.BasePlan,
	}
	config.setDefaults()
	if model == nil || !config.JointDistanceQuantity.Enabled ||
		in.Horizon <= 0 || in.BestBid <= 0 || in.BestAsk <= in.BestBid ||
		in.MidPrice <= 0 || in.BasePlan.BidPrice <= 0 ||
		in.BasePlan.AskPrice <= in.BasePlan.BidPrice {
		return d
	}
	count := config.JointDistanceQuantity.CandidateCount
	if count < 2 {
		count = 2
	}
	conditionalState := model.conditionalExecutionState(in.Horizon)
	conditionalEnabled := config.ConditionalExecution.Enabled && conditionalState.Valid
	plans := jointDistanceCandidatePlans(
		in.BasePlan, in.BestBid, in.BestAsk, in.MidPrice,
		config.MaximumHalfSpreadBps, conditionalEnabled, count)
	d.CandidateCount = len(plans)
	d.InwardBuyEligible = conditionalEnabled && in.BasePlan.AllowBid && in.BestBid > in.BasePlan.BidPrice*(1+1e-12)
	d.InwardSellEligible = conditionalEnabled && in.BasePlan.AllowAsk && in.BasePlan.AskPrice > in.BestAsk*(1+1e-12)
	z := in.ConfidenceZScore
	if z <= 0 {
		z = config.InventoryRiskZScore
	}
	if z <= 0 {
		z = 1.645
	}
	hours := in.Horizon.Hours()
	if in.PairEquityJPY <= 0 {
		d.Reason = "pair equity unavailable for path utility"
		return d
	}
	riskAversion := in.RiskAversion
	if riskAversion <= 0 {
		riskAversion = fastRiskAversionOrDefault(config, riskAversion)
	}
	quantityScales := jointQuantityScales(in.Projection, count)
	if len(quantityScales) == 0 {
		d.Reason = "no executable quantity scales"
		return d
	}
	bestScore := math.Inf(-1)
	bestGross := -1.0
	bestFallbackBuyScore, bestFallbackSellScore := math.Inf(-1), math.Inf(-1)
	bestFallbackBuyPlan, bestFallbackSellPlan := in.BasePlan, in.BasePlan
	buyUnit := positiveExecutableUnit(in.Projection.MinBuyNotionalJPY)
	sellUnit := positiveExecutableUnit(in.Projection.MinSellNotionalJPY)
	jointIdentificationFloorJPY := fastValueIdentificationFloor(
		config, in.Horizon,
		in.Projection.MinBuyNotionalJPY,
		in.Projection.MinSellNotionalJPY)
	crossingReady, positiveEdge, pathReady, projectionReady := 0, 0, 0, 0
	pathObserved, pathImmature := 0, false
	positivePathUtility, confidenceScaleReady := 0, 0
	baseBuyDistance, baseSellDistance, _ := MakerTouchDistances(
		in.BestBid, in.BestAsk, in.BasePlan.BidPrice, in.BasePlan.AskPrice)
	pairedDistanceZ := simultaneousOneSidedZ(z, count-1)
	for index, plan := range plans {
		buyDistance, sellDistance, grossEdge := MakerTouchDistances(
			in.BestBid, in.BestAsk, plan.BidPrice, plan.AskPrice)
		inwardBuy := plan.BidPrice > in.BasePlan.BidPrice*(1+1e-12)
		inwardSell := plan.AskPrice < in.BasePlan.AskPrice*(1-1e-12)
		conditionalBuy, conditionalSell := ConditionalExecutionSideDecision{}, ConditionalExecutionSideDecision{}
		if inwardBuy {
			conditionalBuy = model.conditionalExecutionSideDecision(
				in.Now, config, in.Horizon, conditionalState, true,
				in.BasePlan.BidTouchDistanceBps, buyDistance)
			if !inwardDistanceImprovementSupported(conditionalBuy, pairedDistanceZ) {
				continue
			}
		}
		if inwardSell {
			conditionalSell = model.conditionalExecutionSideDecision(
				in.Now, config, in.Horizon, conditionalState, false,
				in.BasePlan.AskTouchDistanceBps, sellDistance)
			if !inwardDistanceImprovementSupported(conditionalSell, pairedDistanceZ) {
				continue
			}
		}
		pairedDistance := JointPathPayoffDifferenceDecision{}
		if config.JointDistanceQuantity.PairedDistanceImprovement &&
			!inwardBuy && !inwardSell && index > 0 {
			pairedDistance = model.jointBalancedPathPayoffDifference(
				in.Now, config, in.Horizon,
				baseBuyDistance, baseSellDistance,
				buyDistance, sellDistance,
				conditionalState)
			if !pairedDistance.Evaluated ||
				pairedDistance.MeanBps-pairedDistanceZ*pairedDistance.StdErrorBps <= 0 {
				continue
			}
		}
		crossing := model.CrossingDecisionAtSideDistances(
			in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
		if inwardBuy || inwardSell {
			crossing = model.conditionalCrossingDecision(
				in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge, conditionalState)
		}
		if !crossing.HasSufficientCrossings(config.HorizonMinSamples) {
			continue
		}
		crossingReady++
		cycleEdgePositive := crossing.NetRoundTripEdgeBps > 0
		if cycleEdgePositive {
			positiveEdge++
		}
		pathStats := model.JointPathPayoffStatistics(
			in.Now, config, in.Horizon, buyDistance, sellDistance)
		if inwardBuy || inwardSell {
			pathStats = model.conditionalJointPathPayoffStatistics(
				in.Now, config, in.Horizon, buyDistance, sellDistance, conditionalState)
		}
		pathObserved++
		setJointPathDecayDiagnostics(&d, pathStats)
		maturity := AssessJointPathMaturity(pathStats, config, z)
		// Keep the diagnostics from the best available candidate even when no
		// candidate is mature. This is what lets the caller distinguish a data
		// gap from a measured negative terminal-wealth posterior.
		if d.PathMaturityReason == "" || maturity.EffectiveSamples > d.PathEffectiveSamples ||
			(maturity.Matured && !d.PathMaturityReady) {
			d.PathMaturityReason = maturity.Reason
			d.PathMaturityConfidenceHalfWidthBps = maturity.ConfidenceHalfWidthBps
			d.PathMaturityReferenceScaleBps = maturity.ReferenceScaleBps
			d.PathMaturityRelativeHalfWidth = maturity.RelativeHalfWidth
		}
		if d.PathEffectiveSamples < maturity.EffectiveSamples {
			d.PathEffectiveSamples = maturity.EffectiveSamples
		}
		if !maturity.Matured {
			pathImmature = true
			continue
		}
		pathReady++
		d.PathMaturityReady = true
		// Preserve estimator diagnostics even when every terminal-wealth
		// candidate is later rejected. PathEffectiveSamples describes the
		// evidence evaluated, not only evidence attached to a winning allocation;
		// otherwise measured negative posterior utility is logged like a genuine
		// path-data gap.
		// A rejected two-sided allocation must not silently fall back to the
		// original, more inward Fast quote. Track the best conservative
		// executable point independently for each side. The one-sided terminal
		// payoff includes both untouched paths and adverse continuation after a
		// touch, so this is a conditional execution-quality test rather than a
		// nominal spread threshold.
		if !inwardBuy && !inwardSell && in.BasePlan.AllowBid && buyUnit > 0 &&
			in.Projection.MaxBuyNotionalJPY+1e-9 >= buyUnit {
			buyUtility := pathStats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				buyUnit, 0, in.PairEquityJPY, riskAversion, 0)
			buyContinuation := matureTargetProgressContinuationValue(
				config.JointDistanceQuantity.LegacyStackedTargetContinuation,
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				in.PairEquityJPY, buyUnit, 0,
				crossing.BuyTouchProbability, 0, 0)
			buyRobust := pathStats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				buyUnit, 0, in.PairEquityJPY, riskAversion, z)
			_, _, buyNetValue := targetRelativeCEAdmissionValue(buyUtility, buyRobust)
			buyNetValue += buyContinuation
			buyFloorJPY := fastValueIdentificationFloor(
				config, in.Horizon, in.Projection.MinBuyNotionalJPY, 0)
			buyScore, buyAdmitted, _ := sideSafeFallbackAdmissionForReplay(
				buyNetValue-buyFloorJPY, buyRobust, buyContinuation,
				config.JointDistanceQuantity.LegacyStackedTargetContinuation)
			// A non-target BUY is still an oscillation leg and must pay the
			// completed-cycle reservation edge.  A BUY that reduces |I-I*| is
			// admitted by its one-sided posterior/continuation value instead.
			if !cycleEdgePositive &&
				in.Projection.CurrentInventoryNotionalJPY >= in.Projection.TargetInventoryNotionalJPY {
				buyAdmitted = false
			}
			buyScore /= hours
			if buyAdmitted && buyScore > bestFallbackBuyScore+1e-12 {
				bestFallbackBuyScore = buyScore
				bestFallbackBuyPlan = plan
			}
		}
		if !inwardBuy && !inwardSell && in.BasePlan.AllowAsk && sellUnit > 0 &&
			in.Projection.MaxSellNotionalJPY+1e-9 >= sellUnit {
			sellUtility := pathStats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				0, sellUnit, in.PairEquityJPY, riskAversion, 0)
			sellContinuation := matureTargetProgressContinuationValue(
				config.JointDistanceQuantity.LegacyStackedTargetContinuation,
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				in.PairEquityJPY, 0, sellUnit,
				0, crossing.SellTouchProbability, 0)
			sellRobust := pathStats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				0, sellUnit, in.PairEquityJPY, riskAversion, z)
			_, _, sellNetValue := targetRelativeCEAdmissionValue(sellUtility, sellRobust)
			sellNetValue += sellContinuation
			sellFloorJPY := fastValueIdentificationFloor(
				config, in.Horizon, 0, in.Projection.MinSellNotionalJPY)
			sellScore, sellAdmitted, _ := sideSafeFallbackAdmissionForReplay(
				sellNetValue-sellFloorJPY, sellRobust, sellContinuation,
				config.JointDistanceQuantity.LegacyStackedTargetContinuation)
			if !cycleEdgePositive &&
				in.Projection.CurrentInventoryNotionalJPY <= in.Projection.TargetInventoryNotionalJPY {
				sellAdmitted = false
			}
			sellScore /= hours
			if sellAdmitted && sellScore > bestFallbackSellScore+1e-12 {
				bestFallbackSellScore = sellScore
				bestFallbackSellPlan = plan
			}
		}
		// No matched oscillation quantity may bypass its hard profit hurdle. The
		// target-restoring side evidence collected above remains available to the
		// one-sided continuation path.
		if !cycleEdgePositive {
			continue
		}
		for quantityIndex, rawScale := range quantityScales {
			projectionInput := in.Projection
			projectionInput.Horizon = in.Horizon
			projectionInput.FastBuyNotionalJPY *= rawScale
			projectionInput.FastSellNotionalJPY *= rawScale
			projectionInput.DirectFillProbabilities = true
			projectionInput.FullRiskPromotion = true
			projectionInput.BuyFillProbability = crossing.BuyTouchProbability
			projectionInput.SellFillProbability = crossing.SellTouchProbability
			projectionInput.BothFillProbability = crossing.BothTouchProbability
			rawProjection := ProbabilityCenteredQuoteNotionals(projectionInput)
			if !rawProjection.Enabled || rawProjection.ProjectedGrossNotionalJPY <= 0 {
				continue
			}
			projectionReady++
			rawConfidence := pathStats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				rawProjection.BuyNotionalJPY, rawProjection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, z)
			// The corrected paired CE already contains the confidence adjustment.
			// Do not apply the old incremental-SE confidence factor a second time;
			// it was the source of the false terminal rejection for target repair.
			positiveConfidence := 0.0
			if rawConfidence.CertaintyEquivalent > 0 {
				positiveConfidence = 1
			}
			if positiveConfidence > 0 {
				positivePathUtility++
			}
			scale := rawScale * positiveConfidence
			if scale+1e-12 < quantityScales[0] {
				continue
			}
			confidenceScaleReady++
			projectionInput = in.Projection
			projectionInput.Horizon = in.Horizon
			projectionInput.FastBuyNotionalJPY *= scale
			projectionInput.FastSellNotionalJPY *= scale
			projectionInput.DirectFillProbabilities = true
			projectionInput.FullRiskPromotion = true
			projectionInput.BuyFillProbability = crossing.BuyTouchProbability
			projectionInput.SellFillProbability = crossing.SellTouchProbability
			projectionInput.BothFillProbability = crossing.BothTouchProbability
			projection := ProbabilityCenteredQuoteNotionals(projectionInput)
			if !projection.Enabled || projection.ProjectedGrossNotionalJPY <= 0 {
				continue
			}
			pathPayoff := pathStats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				projection.BuyNotionalJPY, projection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, 0)
			pathConfidence := pathStats.EvaluateTargetRelativePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				projection.BuyNotionalJPY, projection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, z)
			feeValueMean, feeValueDownsideRegret, feeValueNet :=
				targetRelativeCEAdmissionValue(pathPayoff, pathConfidence)
			feeValueNet += matureTargetProgressContinuationValue(
				config.JointDistanceQuantity.LegacyStackedTargetContinuation,
				in.Projection.CurrentInventoryNotionalJPY,
				in.Projection.TargetInventoryNotionalJPY,
				in.PairEquityJPY,
				projection.BuyNotionalJPY, projection.SellNotionalJPY,
				crossing.BuyTouchProbability, crossing.SellTouchProbability,
				crossing.BothTouchProbability)
			relativeHoldUtility := jointRelativeHoldUtility(in,
				projection.BuyNotionalJPY, projection.SellNotionalJPY)
			feeValueNet += relativeHoldUtility.NetJPYPerHour * hours
			// Every executable candidate, including the venue-minimum cell, must
			// pay its fee-net posterior downside regret relative to no new order.
			if feeValueNet <= jointIdentificationFloorJPY {
				continue
			}
			// Relative-Hold is already expressed as the same JPY/hour scalar as
			// the Fast terminal-wealth objective. It is added once above, after
			// fee/path feasibility, and never controls a side independently.
			score := (feeValueNet - jointIdentificationFloorJPY) / hours
			gross := projection.ProjectedGrossNotionalJPY
			if score > bestScore+1e-12 ||
				(math.Abs(score-bestScore) <= 1e-12 && gross > bestGross) {
				bestScore, bestGross = score, gross
				d.Enabled = true
				d.Reason = "max posterior terminal-wealth utility with robust multi-cell constraint"
				d.Plan = plan
				d.Projection = projection
				d.Crossing = crossing
				d.SelectedCandidate = index
				d.SelectedQuantityCandidate = quantityIndex
				d.InwardBuySelected = inwardBuy
				d.InwardSellSelected = inwardSell
				d.PairedDistanceEvaluated = pairedDistance.Evaluated
				d.PairedDistanceMeanBps = pairedDistance.MeanBps
				d.PairedDistanceStdErrorBps = pairedDistance.StdErrorBps
				d.PairedDistanceLowerBps = pairedDistance.MeanBps -
					pairedDistanceZ*pairedDistance.StdErrorBps
				if d.InwardBuySelected {
					d.SelectedInwardBuyDeltaBps = math.Log(plan.BidPrice/in.BasePlan.BidPrice) * 10_000
					d.ConditionalBuy = conditionalBuy
				}
				if d.InwardSellSelected {
					d.SelectedInwardSellDeltaBps = math.Log(in.BasePlan.AskPrice/plan.AskPrice) * 10_000
					d.ConditionalSell = conditionalSell
				}
				d.QuantityScale = scale
				d.ExpectedCycleJPY = math.Min(
					crossing.BuyTouchProbability*projection.BuyNotionalJPY,
					crossing.SellTouchProbability*projection.SellNotionalJPY)
				d.ExpectedPnLJPYHour = pathPayoff.ExpectedPnLJPY / hours
				d.LowerPnLJPYHour = pathConfidence.CertaintyEquivalent / hours
				d.PathStdErrorJPYHour = pathPayoff.StdErrorJPY / hours
				d.KellyPenaltyJPYHour = pathPayoff.KellyPenaltyJPY / hours
				d.KellyUtilityJPYHour = score
				d.FeeValueMeanJPY = feeValueMean
				d.FeeValueDownsideRegretJPY = feeValueDownsideRegret
				d.FeeValueNetJPY = feeValueNet
				recordJointRelativeHoldDiagnostics(&d, relativeHoldUtility, in)
				d.PathPositiveConfidence = positiveConfidence
				d.ExistingInventoryExpectedPnLJPY = pathPayoff.ExistingInventoryExpectedPnLJPY
				d.BaselineVarianceJPY2 = pathPayoff.BaselineVarianceJPY2
				d.WholePositionVarianceJPY2 = pathPayoff.WholePositionVarianceJPY2
				d.MarginalVarianceJPY2 = pathPayoff.MarginalVarianceJPY2
				d.InventoryOrderCovarianceJPY2 = pathPayoff.InventoryOrderCovarianceJPY2
				d.RiskReducing = pathPayoff.RiskReducing
				setJointPathDecayDiagnostics(&d, pathStats)
				d.PairCapitalUtilization = gross / in.PairEquityJPY
				if in.Projection.FastBuyNotionalJPY+in.Projection.FastSellNotionalJPY > 0 {
					d.CapitalUtilization = gross /
						(in.Projection.FastBuyNotionalJPY + in.Projection.FastSellNotionalJPY)
				}
			}
		}
	}
	if d.Enabled {
		if d.KellyUtilityJPYHour <= 0 {
			d.Enabled = false
			d.Applied = false
			d.Reason = "no candidate has positive posterior terminal-wealth utility"
		} else {
			d.Applied = !config.JointDistanceQuantity.ShadowOnly
		}
	}

	// If no complete two-sided allocation survives, keep one executable Fast
	// cell on every side admitted by balances and hard inventory capacity. A
	// side whose terminal wealth is supported may expand above that bilateral
	// floor using the whole-position risk cap. The promotion is accepted only
	// when it improves certainty-equivalent wealth relative to the same
	// bilateral floor.
	if !d.Enabled && pathReady > 0 {
		buySupported := bestFallbackBuyScore > 0 && !math.IsInf(bestFallbackBuyScore, -1)
		sellSupported := bestFallbackSellScore > 0 && !math.IsInf(bestFallbackSellScore, -1)
		fallbackMinimumBuyJPY, fallbackMinimumSellJPY := 0.0, 0.0
		if buySupported {
			fallbackMinimumBuyJPY = in.Projection.MinBuyNotionalJPY
		}
		if sellSupported {
			fallbackMinimumSellJPY = in.Projection.MinSellNotionalJPY
		}
		baselineBuy := in.BasePlan.AllowBid && buyUnit > 0 &&
			in.Projection.MaxBuyNotionalJPY+1e-9 >= buyUnit
		baselineSell := in.BasePlan.AllowAsk && sellUnit > 0 &&
			in.Projection.MaxSellNotionalJPY+1e-9 >= sellUnit
		d.FallbackBuySupported = buySupported
		d.FallbackSellSupported = sellSupported
		d.FallbackBuyScore = bestFallbackBuyScore
		d.FallbackSellScore = bestFallbackSellScore
		d.FallbackBuyPlan = bestFallbackBuyPlan
		d.FallbackSellPlan = bestFallbackSellPlan
		if buySupported || sellSupported {
			fallbackPlan := in.BasePlan
			fallbackPlan.AllowBid = baselineBuy && buySupported
			fallbackPlan.AllowAsk = baselineSell && sellSupported
			if buySupported {
				applyQuotePlanSide(&fallbackPlan, bestFallbackBuyPlan, true)
			}
			if sellSupported {
				applyQuotePlanSide(&fallbackPlan, bestFallbackSellPlan, false)
			}
			buyDistance, sellDistance, grossEdge := MakerTouchDistances(
				in.BestBid, in.BestAsk, fallbackPlan.BidPrice, fallbackPlan.AskPrice)
			fallbackPlan.BidTouchDistanceBps = buyDistance
			fallbackPlan.AskTouchDistanceBps = sellDistance
			fallbackPlan.BidDistanceBps = math.Max(0, math.Log(in.MidPrice/fallbackPlan.BidPrice)*10_000)
			fallbackPlan.AskDistanceBps = math.Max(0, math.Log(fallbackPlan.AskPrice/in.MidPrice)*10_000)
			fallbackPlan.BidHalfSpreadBps = fallbackPlan.BidDistanceBps
			fallbackPlan.AskHalfSpreadBps = fallbackPlan.AskDistanceBps
			fallbackPlan.HalfSpreadBps = math.Max(
				fallbackPlan.BidHalfSpreadBps, fallbackPlan.AskHalfSpreadBps)

			crossing := model.CrossingDecisionAtSideDistances(
				in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
			pathStats := model.JointPathPayoffStatistics(
				in.Now, config, in.Horizon, buyDistance, sellDistance)
			projectionInput := in.Projection
			projectionInput.Horizon = in.Horizon
			projectionInput.DirectFillProbabilities = true
			projectionInput.FullRiskPromotion = true
			projectionInput.BuyFillProbability = crossing.BuyTouchProbability
			projectionInput.SellFillProbability = crossing.SellTouchProbability
			projectionInput.BothFillProbability = crossing.BothTouchProbability
			baselineInput := projectionInput
			if baselineBuy {
				baselineInput.MaxBuyNotionalJPY = buyUnit
			} else {
				baselineInput.FastBuyNotionalJPY = 0
				baselineInput.MinBuyNotionalJPY = 0
				baselineInput.MaxBuyNotionalJPY = 0
			}
			if baselineSell {
				baselineInput.MaxSellNotionalJPY = sellUnit
			} else {
				baselineInput.FastSellNotionalJPY = 0
				baselineInput.MinSellNotionalJPY = 0
				baselineInput.MaxSellNotionalJPY = 0
			}
			baselineProjection := ProbabilityCenteredQuoteNotionals(baselineInput)
			if buySupported {
				projectionInput.MaxBuyNotionalJPY = posteriorRiskSizedSideCap(
					pathStats, true, projectionInput.MaxBuyNotionalJPY, projectionInput.MinBuyNotionalJPY,
					in.Projection.CurrentInventoryNotionalJPY, in.Projection.TargetInventoryNotionalJPY,
					in.PairEquityJPY, riskAversion, z)
			} else {
				projectionInput.FastBuyNotionalJPY = 0
				projectionInput.MinBuyNotionalJPY = 0
				projectionInput.MaxBuyNotionalJPY = 0
			}
			if sellSupported {
				projectionInput.MaxSellNotionalJPY = posteriorRiskSizedSideCap(
					pathStats, false, projectionInput.MaxSellNotionalJPY, projectionInput.MinSellNotionalJPY,
					in.Projection.CurrentInventoryNotionalJPY, in.Projection.TargetInventoryNotionalJPY,
					in.PairEquityJPY, riskAversion, z)
			} else {
				projectionInput.FastSellNotionalJPY = 0
				projectionInput.MinSellNotionalJPY = 0
				projectionInput.MaxSellNotionalJPY = 0
			}
			projection := ProbabilityCenteredQuoteNotionals(projectionInput)
			if projection.Enabled && projection.ProjectedGrossNotionalJPY > 0 {
				payoff := pathStats.EvaluateTargetRelativePosition(
					in.Projection.CurrentInventoryNotionalJPY,
					in.Projection.TargetInventoryNotionalJPY,
					projection.BuyNotionalJPY, projection.SellNotionalJPY,
					in.PairEquityJPY, riskAversion, 0)
				confidence := pathStats.EvaluateTargetRelativePosition(
					in.Projection.CurrentInventoryNotionalJPY,
					in.Projection.TargetInventoryNotionalJPY,
					projection.BuyNotionalJPY, projection.SellNotionalJPY,
					in.PairEquityJPY, riskAversion, z)
				feeValueMean, feeValueDownsideRegret, feeValueNet :=
					targetRelativeCEAdmissionValue(payoff, confidence)
				feeValueNet += matureTargetProgressContinuationValue(
					config.JointDistanceQuantity.LegacyStackedTargetContinuation,
					in.Projection.CurrentInventoryNotionalJPY,
					in.Projection.TargetInventoryNotionalJPY,
					in.PairEquityJPY,
					projection.BuyNotionalJPY, projection.SellNotionalJPY,
					crossing.BuyTouchProbability, crossing.SellTouchProbability,
					crossing.BothTouchProbability)
				fallbackIdentificationFloorJPY := fastValueIdentificationFloor(
					config, in.Horizon, fallbackMinimumBuyJPY, fallbackMinimumSellJPY)
				marginalUtility := feeValueNet - fallbackIdentificationFloorJPY
				promoted := false
				if baselineProjection.Enabled {
					baselinePayoff := pathStats.EvaluateTargetRelativePosition(
						in.Projection.CurrentInventoryNotionalJPY,
						in.Projection.TargetInventoryNotionalJPY,
						baselineProjection.BuyNotionalJPY,
						baselineProjection.SellNotionalJPY,
						in.PairEquityJPY, riskAversion, 0)
					baselineConfidence := pathStats.EvaluateTargetRelativePosition(
						in.Projection.CurrentInventoryNotionalJPY,
						in.Projection.TargetInventoryNotionalJPY,
						baselineProjection.BuyNotionalJPY,
						baselineProjection.SellNotionalJPY,
						in.PairEquityJPY, riskAversion, z)
					promoted = projection.BuyNotionalJPY >
						baselineProjection.BuyNotionalJPY+1e-9 ||
						projection.SellNotionalJPY >
							baselineProjection.SellNotionalJPY+1e-9
					if promoted {
						_, _, baselineFeeValueNet := targetRelativeCEAdmissionValue(
							baselinePayoff, baselineConfidence)
						baselineFeeValueNet += matureTargetProgressContinuationValue(
							config.JointDistanceQuantity.LegacyStackedTargetContinuation,
							in.Projection.CurrentInventoryNotionalJPY,
							in.Projection.TargetInventoryNotionalJPY,
							in.PairEquityJPY,
							baselineProjection.BuyNotionalJPY,
							baselineProjection.SellNotionalJPY,
							crossing.BuyTouchProbability,
							crossing.SellTouchProbability,
							crossing.BothTouchProbability)
						marginalUtility -= baselineFeeValueNet
					}
				}
				if feeValueNet > fallbackIdentificationFloorJPY &&
					(!promoted || marginalUtility > 0) {
					d.Enabled = true
					d.Applied = !config.JointDistanceQuantity.ShadowOnly
					d.SideSafeFallback = true
					d.Reason = "bilateral Fast floor with marginal whole-position promotion"
					d.Plan = fallbackPlan
					d.Projection = projection
					d.Crossing = crossing
					d.SelectedCandidate = -1
					d.SelectedQuantityCandidate = -1
					d.ExpectedCycleJPY = math.Min(
						crossing.BuyTouchProbability*projection.BuyNotionalJPY,
						crossing.SellTouchProbability*projection.SellNotionalJPY)
					d.ExpectedPnLJPYHour = payoff.ExpectedPnLJPY / hours
					d.LowerPnLJPYHour = confidence.CertaintyEquivalent / hours
					d.PathStdErrorJPYHour = payoff.StdErrorJPY / hours
					d.KellyPenaltyJPYHour = payoff.KellyPenaltyJPY / hours
					d.KellyUtilityJPYHour = marginalUtility / hours
					d.FeeValueMeanJPY = feeValueMean
					d.FeeValueDownsideRegretJPY = feeValueDownsideRegret
					d.FeeValueNetJPY = feeValueNet
					d.PathPositiveConfidence = jointPathUtilityConfidence(payoff)
					d.ExistingInventoryExpectedPnLJPY = payoff.ExistingInventoryExpectedPnLJPY
					d.BaselineVarianceJPY2 = payoff.BaselineVarianceJPY2
					d.WholePositionVarianceJPY2 = payoff.WholePositionVarianceJPY2
					d.MarginalVarianceJPY2 = payoff.MarginalVarianceJPY2
					d.InventoryOrderCovarianceJPY2 = payoff.InventoryOrderCovarianceJPY2
					d.RiskReducing = payoff.RiskReducing
					setJointPathDecayDiagnostics(&d, pathStats)
					d.PairCapitalUtilization = projection.ProjectedGrossNotionalJPY / in.PairEquityJPY
					fastGross := in.Projection.FastBuyNotionalJPY + in.Projection.FastSellNotionalJPY
					if fastGross > 0 {
						d.CapitalUtilization = projection.ProjectedGrossNotionalJPY / fastGross
						d.QuantityScale = d.CapitalUtilization
					}
				}
			}
		}
	}
	if !d.Enabled {
		// An immature path is inconclusive, not an authoritative terminal-wealth
		// rejection. The caller therefore retains the already computed Fast plan
		// while the path estimator accumulates more completed evidence.
		d.AuthoritativeRejection = pathReady > 0
		switch {
		case crossingReady == 0:
			d.Reason = "insufficient crossing samples for every distance"
		case positiveEdge == 0:
			d.Reason = "no distance has positive fee-net crossing edge"
		case pathReady == 0:
			if pathObserved > 0 && pathImmature {
				d.Reason = "terminal path evidence is immature"
			} else {
				d.Reason = "insufficient terminal path variance samples"
			}
		case bestFallbackBuyScore <= 0 && bestFallbackSellScore <= 0:
			d.Reason = "no side distance has positive posterior-risk terminal wealth"
		case projectionReady == 0:
			d.Reason = "no executable probability projection"
		case positivePathUtility == 0:
			d.Reason = "no candidate has positive posterior marginal whole-position utility"
		case confidenceScaleReady == 0:
			d.Reason = "posterior-supported quantity is below exchange minimum"
		default:
			d.Reason = "side-safe candidate violates joint inventory or utility constraint"
		}
	}
	return d
}
