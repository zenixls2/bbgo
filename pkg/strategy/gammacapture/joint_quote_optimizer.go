package gammacapture

import (
	"math"
	"time"
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
	Projection       ProbabilityCenteredQuoteInput
	ConfidenceZScore float64
	PairEquityJPY    float64
	RiskAversion     float64
}

type JointDistanceQuantityDecision struct {
	Enabled                   bool
	Applied                   bool
	Reason                    string
	Plan                      MarketMakerQuotePlan
	Projection                ProbabilityCenteredQuoteDecision
	Crossing                  MarketMakerHorizonDecision
	CandidateCount            int
	SelectedCandidate         int
	SelectedQuantityCandidate int
	QuantityScale             float64
	ExpectedCycleJPY          float64
	ExpectedPnLJPYHour        float64
	LowerPnLJPYHour           float64
	PathStdErrorJPYHour       float64
	KellyPenaltyJPYHour       float64
	KellyUtilityJPYHour       float64
	PathPositiveConfidence    float64
	PathEffectiveSamples      float64
	CapitalUtilization        float64
	PairCapitalUtilization    float64
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
	crossingReady, positiveEdge, pathReady, projectionReady := 0, 0, 0, 0
	positivePathMean, confidenceScaleReady := 0, 0
	for index := 0; index < count; index++ {
		fraction := float64(index) / float64(count-1)
		plan := jointDistanceCandidatePlan(
			in.BasePlan, in.BestBid, in.BestAsk, in.MidPrice,
			config.MaximumHalfSpreadBps, fraction)
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
		for quantityIndex, rawScale := range quantityScales {
			projectionInput := in.Projection
			projectionInput.Horizon = in.Horizon
			projectionInput.FastBuyNotionalJPY *= rawScale
			projectionInput.FastSellNotionalJPY *= rawScale
			projectionInput.DirectFillProbabilities = true
			projectionInput.BuyFillProbability = crossing.BuyTouchProbability
			projectionInput.SellFillProbability = crossing.SellTouchProbability
			projectionInput.BothFillProbability = crossing.BothTouchProbability
			rawProjection := ProbabilityCenteredQuoteNotionals(projectionInput)
			if !rawProjection.Enabled || rawProjection.ProjectedGrossNotionalJPY <= 0 {
				continue
			}
			projectionReady++
			rawPayoff := pathStats.Evaluate(
				rawProjection.BuyNotionalJPY, rawProjection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, 0)
			positiveConfidence := jointPathPositiveConfidence(
				rawPayoff.ExpectedPnLJPY, rawPayoff.StdErrorJPY)
			if positiveConfidence > 0 {
				positivePathMean++
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
			projectionInput.BuyFillProbability = crossing.BuyTouchProbability
			projectionInput.SellFillProbability = crossing.SellTouchProbability
			projectionInput.BothFillProbability = crossing.BothTouchProbability
			projection := ProbabilityCenteredQuoteNotionals(projectionInput)
			if !projection.Enabled || projection.ProjectedGrossNotionalJPY <= 0 {
				continue
			}
			pathPayoff := pathStats.Evaluate(
				projection.BuyNotionalJPY, projection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, 0)
			pathConfidence := pathStats.Evaluate(
				projection.BuyNotionalJPY, projection.SellNotionalJPY,
				in.PairEquityJPY, riskAversion, z)
			score := pathPayoff.CertaintyEquivalent / hours
			gross := projection.ProjectedGrossNotionalJPY
			if score > bestScore+1e-12 ||
				(math.Abs(score-bestScore) <= 1e-12 && gross > bestGross) {
				bestScore, bestGross = score, gross
				d.Enabled = true
				d.Reason = "max posterior-sign-scaled terminal-wealth kelly utility"
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
				d.PathEffectiveSamples = pathStats.EffectiveSamples
				d.PairCapitalUtilization = gross / in.PairEquityJPY
				if in.Projection.FastBuyNotionalJPY+in.Projection.FastSellNotionalJPY > 0 {
					d.CapitalUtilization = gross /
						(in.Projection.FastBuyNotionalJPY + in.Projection.FastSellNotionalJPY)
				}
			}
		}
	}
	if !d.Enabled {
		switch {
		case crossingReady == 0:
			d.Reason = "insufficient crossing samples for every distance"
		case positiveEdge == 0:
			d.Reason = "no distance has positive fee-net crossing edge"
		case pathReady == 0:
			d.Reason = "insufficient terminal path variance samples"
		case projectionReady == 0:
			d.Reason = "no executable probability projection"
		case positivePathMean == 0:
			d.Reason = "no candidate has positive posterior path mean"
		case confidenceScaleReady == 0:
			d.Reason = "posterior-supported quantity is below exchange minimum"
		default:
			d.Reason = "no evaluable joint candidate"
		}
	}
	if d.Enabled {
		if d.KellyUtilityJPYHour <= 0 {
			d.Enabled = false
			d.Applied = false
			d.Reason = "no candidate has positive posterior-scaled kelly utility"
		} else {
			d.Applied = !config.JointDistanceQuantity.ShadowOnly
		}
	}
	return d
}
