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
}

type JointDistanceQuantityDecision struct {
	Enabled            bool
	Applied            bool
	Reason             string
	Plan               MarketMakerQuotePlan
	Projection         ProbabilityCenteredQuoteDecision
	Crossing           MarketMakerHorizonDecision
	CandidateCount     int
	SelectedCandidate  int
	ExpectedCycleJPY   float64
	ExpectedPnLJPYHour float64
	LowerPnLJPYHour    float64
	CapitalUtilization float64
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
	bestScore := math.Inf(-1)
	bestGross := -1.0
	for index := 0; index < count; index++ {
		fraction := float64(index) / float64(count-1)
		plan := jointDistanceCandidatePlan(
			in.BasePlan, in.BestBid, in.BestAsk, in.MidPrice,
			config.MaximumHalfSpreadBps, fraction)
		buyDistance, sellDistance, grossEdge := MakerTouchDistances(
			in.BestBid, in.BestAsk, plan.BidPrice, plan.AskPrice)
		crossing := model.CrossingDecisionAtSideDistances(
			in.Now, config, in.Horizon, buyDistance, sellDistance, grossEdge)
		if !crossing.HasSufficientCrossings(config.HorizonMinSamples) ||
			crossing.NetRoundTripEdgeBps <= 0 {
			continue
		}
		projectionInput := in.Projection
		projectionInput.Horizon = in.Horizon
		projectionInput.DirectFillProbabilities = true
		projectionInput.BuyFillProbability = crossing.BuyTouchProbability
		projectionInput.SellFillProbability = crossing.SellTouchProbability
		projectionInput.BothFillProbability = crossing.BothTouchProbability
		projection := ProbabilityCenteredQuoteNotionals(projectionInput)
		if !projection.Enabled {
			continue
		}
		lowerBuy := math.Max(0, crossing.BuyTouchProbability-z*crossing.BuyTouchStdError)
		lowerSell := math.Max(0, crossing.SellTouchProbability-z*crossing.SellTouchStdError)
		expectedCycle := math.Min(
			crossing.BuyTouchProbability*projection.BuyNotionalJPY,
			crossing.SellTouchProbability*projection.SellNotionalJPY)
		lowerCycle := math.Min(
			lowerBuy*projection.BuyNotionalJPY,
			lowerSell*projection.SellNotionalJPY)
		expectedPnL := expectedCycle * crossing.NetRoundTripEdgeBps / 10_000 / hours
		lowerPnL := lowerCycle * crossing.NetRoundTripEdgeBps / 10_000 / hours
		gross := projection.ProjectedGrossNotionalJPY
		if lowerPnL > bestScore+1e-12 ||
			(math.Abs(lowerPnL-bestScore) <= 1e-12 && gross > bestGross) {
			bestScore, bestGross = lowerPnL, gross
			d.Enabled = true
			d.Reason = "max confidence-adjusted fee-net cycle pnl per hour"
			d.Plan = plan
			d.Projection = projection
			d.Crossing = crossing
			d.SelectedCandidate = index
			d.ExpectedCycleJPY = expectedCycle
			d.ExpectedPnLJPYHour = expectedPnL
			d.LowerPnLJPYHour = lowerPnL
			if in.Projection.FastBuyNotionalJPY+in.Projection.FastSellNotionalJPY > 0 {
				d.CapitalUtilization = gross /
					(in.Projection.FastBuyNotionalJPY + in.Projection.FastSellNotionalJPY)
			}
		}
	}
	if d.Enabled {
		// Gross notional is only a tie-break among statistically profitable
		// candidates. When every lower confidence bound is zero, selecting the
		// largest order would convert absence of evidence into maximum capital
		// deployment and reproduce the replay underperformance this optimizer
		// is intended to prevent.
		if d.LowerPnLJPYHour <= 0 {
			d.Enabled = false
			d.Applied = false
			d.Reason = "no candidate has positive confidence-adjusted fee-net pnl"
		} else {
			d.Applied = !config.JointDistanceQuantity.ShadowOnly
		}
	}
	return d
}
