package gammacapture

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// JointDistanceQuantityInput contains only causal state available at quote
// time. The distance ladder moves outward from the unified Fast quote; it can
// therefore spend less fill probability for more fee-net edge, but can never
// turn a passive quote into a marketable one.
type JointDistanceQuantityInput struct {
	Now              time.Time
	Horizon          time.Duration
	BestBid          float64
	BestAsk          float64
	MidPrice         float64
	BasePlan         MarketMakerQuotePlan
	FastDirection    float64
	Projection       ProbabilityCenteredQuoteInput
	ConfidenceZScore float64
	PairEquityJPY    float64
	RiskAversion     float64
}

type JointDistanceQuantityDecision struct {
	Enabled                         bool
	Applied                         bool
	AuthoritativeRejection          bool
	SideSafeFallback                bool
	FallbackBuySupported            bool
	FallbackSellSupported           bool
	Reason                          string
	Plan                            MarketMakerQuotePlan
	Projection                      ProbabilityCenteredQuoteDecision
	Crossing                        MarketMakerHorizonDecision
	CandidateCount                  int
	SelectedCandidate               int
	SelectedQuantityCandidate       int
	QuantityScale                   float64
	ExpectedCycleJPY                float64
	ExpectedPnLJPYHour              float64
	LowerPnLJPYHour                 float64
	PathStdErrorJPYHour             float64
	KellyPenaltyJPYHour             float64
	KellyUtilityJPYHour             float64
	PathPositiveConfidence          float64
	PathEffectiveSamples            float64
	CapitalUtilization              float64
	PairCapitalUtilization          float64
	ExistingInventoryExpectedPnLJPY float64
	BaselineVarianceJPY2            float64
	WholePositionVarianceJPY2       float64
	MarginalVarianceJPY2            float64
	InventoryOrderCovarianceJPY2    float64
	RiskReducing                    bool
	DownsideBuyCapApplied           bool
	DownsideInventoryReturnMeanBps  float64
	DownsideInventoryReturnSEBps    float64
	DownsideInventoryReturnUpperBps float64
	DownsideEffectiveSamples        float64
	DownsideOriginalMaxBuyJPY       float64
	DownsideMinimumBuyEvaluated     bool
	DownsideMinimumBuyCEJPY         float64
	BuyAdmissionEvaluated           bool
	BuyAdmissionApplied             bool
	BuyAdmissionMaximumJPY          float64
	BuyAdmissionUtilityBoundJPY     float64
	BuyAdmissionReason              string
	SellAdmissionEvaluated          bool
	SellAdmissionApplied            bool
	SellAdmissionMaximumJPY         float64
	SellAdmissionUtilityBoundJPY    float64
	SellAdmissionReason             string
	AdmissionJointCEJPY             float64
	AdmissionJointComplementary     bool
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
	hardCapJPY, executableUnitJPY, currentInventoryNotionalJPY, pairEquityJPY, riskAversion, confidenceZScore float64,
) float64 {
	if hardCapJPY <= 0 || executableUnitJPY <= 0 || pairEquityJPY <= 0 {
		return 0
	}
	evaluate := func(notional, zScore float64) JointPathPayoffDecision {
		if buy {
			return stats.EvaluateWholePosition(currentInventoryNotionalJPY, notional, 0, pairEquityJPY, riskAversion, zScore)
		}
		return stats.EvaluateWholePosition(currentInventoryNotionalJPY, 0, notional, pairEquityJPY, riskAversion, zScore)
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
		riskAversion = config.MacroInventory.RiskAversion
	}
	z := in.ConfidenceZScore
	if z <= 0 {
		z = config.InventoryRiskZScore
	}
	buyConfidence := stats.EvaluateWholePosition(
		projection.CurrentInventoryNotionalJPY, projection.MinBuyNotionalJPY, 0,
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
	if downside.Applied && buyNotionalJPY > downside.MaximumBuyNotionalJPY+1e-9 {
		d.DownsideBuyCapApplied = true
		buyNotionalJPY = downside.MaximumBuyNotionalJPY
	}
	sellNotionalJPY := d.Projection.SellNotionalJPY
	buyDistance, sellDistance, _ := MakerTouchDistances(
		in.BestBid, in.BestAsk, d.Plan.BidPrice, d.Plan.AskPrice)
	stats := model.JointPathPayoffStatistics(
		in.Now, config, in.Horizon, buyDistance, sellDistance)
	riskAversion := in.RiskAversion
	if riskAversion <= 0 {
		riskAversion = config.MacroInventory.RiskAversion
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
	jointComplementary := false
	if pathEvidenceReady && proposedBuyNotionalJPY > 0 && proposedSellNotionalJPY > 0 {
		joint := stats.EvaluateWholePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			proposedBuyNotionalJPY, proposedSellNotionalJPY,
			in.PairEquityJPY, riskAversion, z)
		jointCEJPY = joint.CertaintyEquivalent
		jointComplementary = jointCEJPY > 0
	}
	d.AdmissionJointCEJPY = jointCEJPY
	d.AdmissionJointComplementary = jointComplementary
	buyUtilityBoundJPY := 0.0
	if pathEvidenceReady && buyNotionalJPY > 0 && in.Projection.MinBuyNotionalJPY > 0 {
		buyLower := stats.EvaluateWholePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.MinBuyNotionalJPY, 0,
			in.PairEquityJPY, riskAversion, z)
		buyUtilityBoundJPY = buyLower.CertaintyEquivalent
	}
	buyAdmission := FastSideAdmissionDecision{
		Evaluated:          pathEvidenceReady && buyNotionalJPY > 0,
		Reason:             "positive joint Fast utility preserves complementary BUY",
		MaximumNotionalJPY: buyNotionalJPY, UtilityBoundJPY: buyUtilityBoundJPY,
	}
	if !jointComplementary {
		buyAdmission = FastTargetAwareSideAdmission(
			types.SideTypeBuy,
			pathEvidenceReady && buyNotionalJPY > 0,
			in.Projection.CurrentInventoryNotionalJPY,
			in.Projection.TargetInventoryNotionalJPY,
			in.Projection.MinBuyNotionalJPY,
			buyNotionalJPY, buyUtilityBoundJPY)
		buyAdmission = FastDirectionScaledBuyAdmission(
			buyAdmission, in.FastDirection,
			in.Projection.MinBuyNotionalJPY, buyNotionalJPY)
	}
	d.BuyAdmissionEvaluated = buyAdmission.Evaluated
	d.BuyAdmissionApplied = buyAdmission.Applied
	d.BuyAdmissionMaximumJPY = buyAdmission.MaximumNotionalJPY
	d.BuyAdmissionUtilityBoundJPY = buyAdmission.UtilityBoundJPY
	d.BuyAdmissionReason = buyAdmission.Reason
	if buyAdmission.Applied {
		buyNotionalJPY = buyAdmission.MaximumNotionalJPY
	}

	sellUtilityBoundJPY := 0.0
	if pathEvidenceReady && sellNotionalJPY > 0 && in.Projection.MinSellNotionalJPY > 0 {
		withSell := stats.EvaluateWholePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			proposedBuyNotionalJPY, in.Projection.MinSellNotionalJPY,
			in.PairEquityJPY, riskAversion, 0)
		withoutSell := stats.EvaluateWholePosition(
			in.Projection.CurrentInventoryNotionalJPY,
			proposedBuyNotionalJPY, 0, in.PairEquityJPY, riskAversion, 0)
		sellUtilityBoundJPY = withSell.CertaintyEquivalent - withoutSell.CertaintyEquivalent +
			z*(withSell.StdErrorJPY+withoutSell.StdErrorJPY)
	}
	sellAdmission := FastSideAdmissionDecision{
		Evaluated:          pathEvidenceReady && sellNotionalJPY > 0,
		Reason:             "positive joint Fast utility preserves complementary SELL",
		MaximumNotionalJPY: sellNotionalJPY, UtilityBoundJPY: sellUtilityBoundJPY,
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

	payoff := stats.EvaluateWholePosition(
		in.Projection.CurrentInventoryNotionalJPY,
		d.Projection.BuyNotionalJPY, d.Projection.SellNotionalJPY,
		in.PairEquityJPY, riskAversion, 0)
	confidence := stats.EvaluateWholePosition(
		in.Projection.CurrentInventoryNotionalJPY,
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
	d.PathEffectiveSamples = stats.EffectiveSamples
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
func OptimizeUnifiedFastQuantity(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	fullInput JointDistanceQuantityInput,
	baselineProjection ProbabilityCenteredQuoteInput,
) JointDistanceQuantityDecision {
	downside := fastPathDownsideDecision(
		model, config, fullInput, fullInput.Projection)
	withAdmissions := func(d JointDistanceQuantityDecision) JointDistanceQuantityDecision {
		return applyFastPathAdmissions(model, config, fullInput, downside, d)
	}
	baseline := probabilityOnlyFastBaseline(model, config, fullInput, baselineProjection)
	full := OptimizeJointDistanceQuantity(model, config, fullInput)
	if full.Applied {
		// Terminal-path evidence may promote within the common risk capacity, but it
		// is not an independent hard-risk observation. Do not let a one-sided
		// posterior fallback delete a Fast side that the probability-centered
		// baseline, account balances, hard inventory band, and exchange lattice
		// all admit. Otherwise repeated soft SELL support can mechanically drain
		// inventory even while the unified target calls for acquisition.
		if fastDecisionDropsBaselineSide(full, baseline, fullInput.BasePlan) {
			return withAdmissions(baseline)
		}
		return withAdmissions(full)
	}
	baselineInput := fullInput
	baselineInput.Projection = baselineProjection
	optimizedBaseline := OptimizeJointDistanceQuantity(model, config, baselineInput)
	if optimizedBaseline.Applied &&
		!fastDecisionDropsBaselineSide(optimizedBaseline, baseline, fullInput.BasePlan) {
		return withAdmissions(optimizedBaseline)
	}
	if baseline.Enabled {
		return withAdmissions(baseline)
	}
	return withAdmissions(optimizedBaseline)
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
	d.CandidateCount = count
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
		riskAversion = config.MacroInventory.RiskAversion
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
	crossingReady, positiveEdge, pathReady, projectionReady := 0, 0, 0, 0
	positivePathUtility, confidenceScaleReady := 0, 0
	for index := 0; index < count; index++ {
		fraction := float64(index) / float64(count-1)
		plan := jointDistanceCandidatePlan(
			in.BasePlan, in.BestBid, in.BestAsk, in.MidPrice, config.MaximumHalfSpreadBps, fraction)
		buyDistance, sellDistance, grossEdge := MakerTouchDistances(
			in.BestBid, in.BestAsk, plan.BidPrice, plan.AskPrice)
		crossing := model.CrossingDecisionAtSideDistances(
			in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
		if !crossing.HasSufficientCrossings(config.HorizonMinSamples) {
			continue
		}
		crossingReady++
		if crossing.NetRoundTripEdgeBps <= 0 {
			continue
		}
		positiveEdge++
		pathStats := model.JointPathPayoffStatistics(
			in.Now, config, in.Horizon, buyDistance, sellDistance)
		// One independent path cannot identify dispersion. Beyond that minimum,
		// sample scarcity belongs in the standard error and confidence bound,
		// rather than a duplicate hard gate tied to crossing sample health.
		if pathStats.EffectiveSamples <= 1 {
			continue
		}
		pathReady++
		// A rejected two-sided allocation must not silently fall back to the
		// original, more inward Fast quote. Track the best conservative
		// executable point independently for each side. The one-sided terminal
		// payoff includes both untouched paths and adverse continuation after a
		// touch, so this is a conditional execution-quality test rather than a
		// nominal spread threshold.
		if in.BasePlan.AllowBid && buyUnit > 0 &&
			in.Projection.MaxBuyNotionalJPY+1e-9 >= buyUnit {
			buyUtility := pathStats.EvaluateWholePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				buyUnit, 0, in.PairEquityJPY, riskAversion, 0)
			buyConfidence := jointPathUtilityConfidence(buyUtility)
			buyScore := buyUtility.CertaintyEquivalent * buyConfidence / hours
			if buyScore > 0 && buyScore > bestFallbackBuyScore+1e-12 {
				bestFallbackBuyScore = buyScore
				bestFallbackBuyPlan = plan
			}
		}
		if in.BasePlan.AllowAsk && sellUnit > 0 &&
			in.Projection.MaxSellNotionalJPY+1e-9 >= sellUnit {
			sellUtility := pathStats.EvaluateWholePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				0, sellUnit, in.PairEquityJPY, riskAversion, 0)
			sellConfidence := jointPathUtilityConfidence(sellUtility)
			sellScore := sellUtility.CertaintyEquivalent * sellConfidence / hours
			if sellScore > 0 && sellScore > bestFallbackSellScore+1e-12 {
				bestFallbackSellScore = sellScore
				bestFallbackSellPlan = plan
			}
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
			rawPayoff := pathStats.EvaluateWholePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				rawProjection.BuyNotionalJPY, rawProjection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, 0)
			positiveConfidence := jointPathUtilityConfidence(rawPayoff)
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
			pathPayoff := pathStats.EvaluateWholePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				projection.BuyNotionalJPY, projection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, 0)
			pathConfidence := pathStats.EvaluateWholePosition(
				in.Projection.CurrentInventoryNotionalJPY,
				projection.BuyNotionalJPY, projection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, z)
			// Posterior terminal wealth chooses direction and the executable-cell
			// baseline. Promotion above one venue cell is allowed only when the
			// same path distribution also has positive confidence-adjusted utility.
			multiCell := projection.BuyNotionalJPY > buyUnit+1e-9 ||
				projection.SellNotionalJPY > sellUnit+1e-9
			if multiCell && pathConfidence.CertaintyEquivalent <= 0 {
				continue
			}
			score := pathPayoff.CertaintyEquivalent / hours
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
				d.QuantityScale = scale
				d.ExpectedCycleJPY = math.Min(
					crossing.BuyTouchProbability*projection.BuyNotionalJPY,
					crossing.SellTouchProbability*projection.SellNotionalJPY)
				d.ExpectedPnLJPYHour = pathPayoff.ExpectedPnLJPY / hours
				d.LowerPnLJPYHour = pathConfidence.CertaintyEquivalent / hours
				d.PathStdErrorJPYHour = pathPayoff.StdErrorJPY / hours
				d.KellyPenaltyJPYHour = pathPayoff.KellyPenaltyJPY / hours
				d.KellyUtilityJPYHour = score
				d.PathPositiveConfidence = positiveConfidence
				d.ExistingInventoryExpectedPnLJPY = pathPayoff.ExistingInventoryExpectedPnLJPY
				d.BaselineVarianceJPY2 = pathPayoff.BaselineVarianceJPY2
				d.WholePositionVarianceJPY2 = pathPayoff.WholePositionVarianceJPY2
				d.MarginalVarianceJPY2 = pathPayoff.MarginalVarianceJPY2
				d.InventoryOrderCovarianceJPY2 = pathPayoff.InventoryOrderCovarianceJPY2
				d.RiskReducing = pathPayoff.RiskReducing
				d.PathEffectiveSamples = pathStats.EffectiveSamples
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
		baselineBuy := in.BasePlan.AllowBid && buyUnit > 0 &&
			in.Projection.MaxBuyNotionalJPY+1e-9 >= buyUnit
		baselineSell := in.BasePlan.AllowAsk && sellUnit > 0 &&
			in.Projection.MaxSellNotionalJPY+1e-9 >= sellUnit
		d.FallbackBuySupported = buySupported
		d.FallbackSellSupported = sellSupported
		if buySupported || sellSupported {
			fallbackPlan := in.BasePlan
			fallbackPlan.AllowBid = baselineBuy
			fallbackPlan.AllowAsk = baselineSell
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
					in.Projection.CurrentInventoryNotionalJPY, in.PairEquityJPY, riskAversion, z)
			} else if baselineBuy {
				projectionInput.MaxBuyNotionalJPY = buyUnit
			}
			if sellSupported {
				projectionInput.MaxSellNotionalJPY = posteriorRiskSizedSideCap(
					pathStats, false, projectionInput.MaxSellNotionalJPY, projectionInput.MinSellNotionalJPY,
					in.Projection.CurrentInventoryNotionalJPY, in.PairEquityJPY, riskAversion, z)
			} else if baselineSell {
				projectionInput.MaxSellNotionalJPY = sellUnit
			}
			projection := ProbabilityCenteredQuoteNotionals(projectionInput)
			if projection.Enabled && projection.ProjectedGrossNotionalJPY > 0 {
				payoff := pathStats.EvaluateWholePosition(
					in.Projection.CurrentInventoryNotionalJPY,
					projection.BuyNotionalJPY, projection.SellNotionalJPY,
					in.PairEquityJPY, riskAversion, 0)
				confidence := pathStats.EvaluateWholePosition(
					in.Projection.CurrentInventoryNotionalJPY,
					projection.BuyNotionalJPY, projection.SellNotionalJPY,
					in.PairEquityJPY, riskAversion, z)
				marginalUtility := payoff.CertaintyEquivalent
				promoted := false
				if baselineProjection.Enabled {
					baselinePayoff := pathStats.EvaluateWholePosition(
						in.Projection.CurrentInventoryNotionalJPY,
						baselineProjection.BuyNotionalJPY,
						baselineProjection.SellNotionalJPY,
						in.PairEquityJPY, riskAversion, 0)
					promoted = projection.BuyNotionalJPY >
						baselineProjection.BuyNotionalJPY+1e-9 ||
						projection.SellNotionalJPY >
							baselineProjection.SellNotionalJPY+1e-9
					if promoted {
						marginalUtility -= baselinePayoff.CertaintyEquivalent
					}
				}
				if confidence.CertaintyEquivalent > 0 &&
					(payoff.CertaintyEquivalent > 0 || (promoted && marginalUtility > 0)) {
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
					d.PathPositiveConfidence = jointPathUtilityConfidence(payoff)
					d.ExistingInventoryExpectedPnLJPY = payoff.ExistingInventoryExpectedPnLJPY
					d.BaselineVarianceJPY2 = payoff.BaselineVarianceJPY2
					d.WholePositionVarianceJPY2 = payoff.WholePositionVarianceJPY2
					d.MarginalVarianceJPY2 = payoff.MarginalVarianceJPY2
					d.InventoryOrderCovarianceJPY2 = payoff.InventoryOrderCovarianceJPY2
					d.RiskReducing = payoff.RiskReducing
					d.PathEffectiveSamples = pathStats.EffectiveSamples
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
		d.AuthoritativeRejection = pathReady > 0
		switch {
		case crossingReady == 0:
			d.Reason = "insufficient crossing samples for every distance"
		case positiveEdge == 0:
			d.Reason = "no distance has positive fee-net crossing edge"
		case pathReady == 0:
			d.Reason = "insufficient terminal path variance samples"
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
