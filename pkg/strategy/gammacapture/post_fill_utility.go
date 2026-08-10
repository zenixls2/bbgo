package gammacapture

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// MakerPostFillState is path-dependent execution state. Price is diagnostic
// model input, never a hard quote boundary.
type MakerPostFillState struct {
	Side     types.SideType `json:"side"`
	Price    float64        `json:"price"`
	Quantity float64        `json:"quantity"`
	At       time.Time      `json:"at"`
}

type PostFillUtilityInput struct {
	Now                     time.Time
	Fill                    MakerPostFillState
	Plan                    MarketMakerQuotePlan
	BestBid                 float64
	BestAsk                 float64
	Mid                     float64
	Horizon                 time.Duration
	InventoryBase           float64
	InventoryTargetBase     float64
	PairEquityJPY           float64
	ExpectedFillNotionalJPY float64
	VolatilityBpsPerSqrtSec float64
	RiskAversion            float64
}

type PostFillUtilityCandidate struct {
	DistanceBps            float64
	FillProbability        float64
	EffectiveSamples       float64
	IncrementalMeanBps     float64
	IncrementalStdErrorBps float64
	IncrementalLowerBps    float64
}

type PostFillUtilityDecision struct {
	Enabled                 bool
	Applied                 bool
	Side                    types.SideType
	Reason                  string
	BaseDistanceBps         float64
	SelectedDistanceBps     float64
	BasePrice               float64
	SelectedPrice           float64
	IncrementalMeanBps      float64
	IncrementalStdErrorBps  float64
	IncrementalLowerBps     float64
	FillProbability         float64
	EffectiveSamples        float64
	CycleEdgeBps            float64
	InventoryRiskBenefitBps float64
	Plan                    MarketMakerQuotePlan
}

// ApplyPostFillUtility compares inward opposite-side prices with the ordinary
// quote on paired, completed BBO paths. A candidate is applied only when the
// lower confidence bound of its incremental terminal wealth is positive.
func (c MarketMakerConfig) ApplyPostFillUtility(model *MarketMakerHorizonModel, in PostFillUtilityInput) PostFillUtilityDecision {
	c.setDefaults()
	d := PostFillUtilityDecision{Reason: "disabled", Plan: in.Plan}
	if !c.PostFillUtility.Enabled {
		return d
	}
	d.Enabled = true
	if in.Now.IsZero() || in.Fill.At.IsZero() || in.Horizon <= 0 || in.Now.Before(in.Fill.At) || in.Now.Sub(in.Fill.At) > in.Horizon {
		d.Reason = "post-fill state is outside active horizon"
		return d
	}
	if in.BestBid <= 0 || in.BestAsk <= in.BestBid || in.Mid <= 0 || in.Plan.BidPrice <= 0 || in.Plan.AskPrice <= in.Plan.BidPrice {
		d.Reason = "invalid quote or BBO"
		return d
	}

	side := types.SideTypeBuy
	basePrice := in.Plan.BidPrice
	baseDistance := in.Plan.BidTouchDistanceBps
	if in.Fill.Side == types.SideTypeBuy {
		side = types.SideTypeSell
		basePrice = in.Plan.AskPrice
		baseDistance = in.Plan.AskTouchDistanceBps
	} else if in.Fill.Side != types.SideTypeSell {
		d.Reason = "unsupported fill side"
		return d
	}
	d.Side, d.BasePrice, d.BaseDistanceBps = side, basePrice, baseDistance
	minimumDistance := math.Max(c.MinimumHalfSpreadBps, c.MakerFeeBps+c.AdverseSelectionBps)
	if baseDistance <= minimumDistance+1e-9 {
		d.Reason = "ordinary quote already at inward economic boundary"
		return d
	}
	levels := int(math.Ceil(math.Max(2, math.Min(16, c.InventoryMaxOrderLevels))))
	distances := make([]float64, 0, levels)
	distances = append(distances, baseDistance)
	for i := 1; i < levels; i++ {
		fraction := float64(i) / float64(levels-1)
		distances = append(distances, baseDistance-fraction*(baseDistance-minimumDistance))
	}

	riskBenefit := postFillInventoryRiskBenefitBps(side, in)
	candidates := model.postFillUtilityCandidates(in.Now, time.Duration(c.HorizonLookback), in.Horizon, side, distances,
		2*c.MakerFeeBps+c.MinimumNetEdgeBps, riskBenefit, c.PostFillUtility.ConfidenceZScore)
	if len(candidates) != len(distances) || candidates[0].EffectiveSamples < float64(c.PostFillUtility.MinimumSamples) {
		d.Reason = "insufficient paired post-fill utility samples"
		if len(candidates) > 0 {
			d.EffectiveSamples = candidates[0].EffectiveSamples
		}
		return d
	}
	d.InventoryRiskBenefitBps = riskBenefit
	diagnosticIndex := 1
	for i := 1; i < len(candidates); i++ {
		if candidates[i].IncrementalLowerBps > candidates[diagnosticIndex].IncrementalLowerBps {
			diagnosticIndex = i
		}
	}
	diagnostic := candidates[diagnosticIndex]
	d.SelectedDistanceBps = diagnostic.DistanceBps
	d.IncrementalMeanBps = diagnostic.IncrementalMeanBps
	d.IncrementalStdErrorBps = diagnostic.IncrementalStdErrorBps
	d.IncrementalLowerBps = diagnostic.IncrementalLowerBps
	d.FillProbability = diagnostic.FillProbability
	d.EffectiveSamples = diagnostic.EffectiveSamples
	selectedPrice := 0.0
	if side == types.SideTypeBuy {
		selectedPrice = in.BestAsk * math.Exp(-diagnostic.DistanceBps/10_000)
		selectedPrice = math.Min(selectedPrice, math.Nextafter(in.BestAsk, 0))
	} else {
		selectedPrice = in.BestBid * math.Exp(diagnostic.DistanceBps/10_000)
		selectedPrice = math.Max(selectedPrice, math.Nextafter(in.BestBid, math.Inf(1)))
	}
	d.SelectedPrice = selectedPrice
	if side == types.SideTypeBuy && in.Fill.Price > 0 {
		d.CycleEdgeBps = math.Log(in.Fill.Price/selectedPrice) * 10_000
	} else if side == types.SideTypeSell && in.Fill.Price > 0 {
		d.CycleEdgeBps = math.Log(selectedPrice/in.Fill.Price) * 10_000
	}
	if diagnostic.EffectiveSamples < float64(c.PostFillUtility.MinimumSamples) || diagnostic.IncrementalLowerBps <= 0 {
		d.Reason = "no inward candidate has positive paired utility lower bound"
		return d
	}
	best := diagnostic

	if side == types.SideTypeBuy {
		d.Plan.BidPrice = selectedPrice
	} else {
		d.Plan.AskPrice = selectedPrice
	}
	// Preserve the economics of the newly resting pair by assigning any
	// shortfall to the non-urgent side. This does not constrain the edge versus
	// the previous fill and therefore cannot block a justified chase or exit.
	roundTripFloor := 2*c.MakerFeeBps + 2*c.AdverseSelectionBps + c.MinimumNetEdgeBps
	if gross := math.Log(d.Plan.AskPrice/d.Plan.BidPrice) * 10_000; gross < roundTripFloor {
		if side == types.SideTypeBuy {
			d.Plan.AskPrice = d.Plan.BidPrice * math.Exp(roundTripFloor/10_000)
		} else {
			d.Plan.BidPrice = d.Plan.AskPrice * math.Exp(-roundTripFloor/10_000)
		}
	}
	d.Plan.BidTouchDistanceBps, d.Plan.AskTouchDistanceBps, _ = MakerTouchDistances(in.BestBid, in.BestAsk, d.Plan.BidPrice, d.Plan.AskPrice)
	d.Plan.BidDistanceBps = math.Log(in.Mid/d.Plan.BidPrice) * 10_000
	d.Plan.AskDistanceBps = math.Log(d.Plan.AskPrice/in.Mid) * 10_000
	d.Plan.HalfSpreadBps = math.Max(d.Plan.BidDistanceBps, d.Plan.AskDistanceBps)

	d.Applied = true
	d.Reason = "positive paired terminal-wealth utility lower bound"
	d.SelectedDistanceBps = best.DistanceBps
	d.SelectedPrice = selectedPrice
	d.IncrementalMeanBps = best.IncrementalMeanBps
	d.IncrementalStdErrorBps = best.IncrementalStdErrorBps
	d.IncrementalLowerBps = best.IncrementalLowerBps
	d.FillProbability = best.FillProbability
	d.EffectiveSamples = best.EffectiveSamples
	return d
}

func postFillInventoryRiskBenefitBps(side types.SideType, in PostFillUtilityInput) float64 {
	if in.PairEquityJPY <= 0 || in.Mid <= 0 || in.ExpectedFillNotionalJPY <= 0 || in.Horizon <= 0 || in.RiskAversion <= 0 {
		return 0
	}
	currentWeight := in.InventoryBase * in.Mid / in.PairEquityJPY
	targetWeight := in.InventoryTargetBase * in.Mid / in.PairEquityJPY
	fillWeight := math.Min(1, in.ExpectedFillNotionalJPY/in.PairEquityJPY)
	afterWeight := currentWeight
	if side == types.SideTypeBuy {
		afterWeight += fillWeight
	} else {
		afterWeight -= fillWeight
	}
	sigma := math.Max(0, in.VolatilityBpsPerSqrtSec) * math.Sqrt(in.Horizon.Seconds()) / 10_000
	return 0.5 * in.RiskAversion * sigma * sigma *
		(math.Pow(currentWeight-targetWeight, 2) - math.Pow(afterWeight-targetWeight, 2)) * 10_000
}

// postFillUtilityCandidates uses completed, overlap-adjusted executable-BBO
// paths. BUY is touched on ask and marked at terminal bid; SELL is touched on
// bid and marked at terminal ask, so adverse selection is observed directly.
func (m MarketMakerHorizonModel) postFillUtilityCandidates(now time.Time, lookback, horizon time.Duration, side types.SideType, distances []float64, roundTripFeeAndEdgeBps, inventoryRiskBenefitBps, zScore float64) []PostFillUtilityCandidate {
	out := make([]PostFillUtilityCandidate, len(distances))
	for i, distance := range distances {
		out[i].DistanceBps = distance
	}
	if now.IsZero() || horizon <= 0 || len(distances) < 2 || len(m.points) < 3 || (side != types.SideTypeBuy && side != types.SideTypeSell) {
		return out
	}
	if lookback <= 0 {
		lookback = 6 * time.Hour
	}
	cutoff := now.Add(-lookback)
	badPrefix := make([]int, len(m.points)+1)
	for i := range m.points {
		badPrefix[i+1] = badPrefix[i]
		bid, ask := m.points[i].bidPrice(), m.points[i].askPrice()
		if m.points[i].GapBefore || bid <= 0 || ask < bid {
			badPrefix[i+1]++
		}
	}
	maxDeque, minDeque := make([]int, 0), make([]int, 0)
	right := 1
	var lastExposure time.Time
	var sumWeight, sumWeightSquared float64
	fillWeight := make([]float64, len(distances))
	diffSum := make([]float64, len(distances))
	diffSquareSum := make([]float64, len(distances))
	pushWindow := func(index int) {
		bid, ask := m.points[index].bidPrice(), m.points[index].askPrice()
		for len(maxDeque) > 0 && m.points[maxDeque[len(maxDeque)-1]].bidPrice() <= bid {
			maxDeque = maxDeque[:len(maxDeque)-1]
		}
		maxDeque = append(maxDeque, index)
		for len(minDeque) > 0 && m.points[minDeque[len(minDeque)-1]].askPrice() >= ask {
			minDeque = minDeque[:len(minDeque)-1]
		}
		minDeque = append(minDeque, index)
	}
	for i, start := range m.points {
		if start.At.Before(cutoff) || start.GapBefore {
			continue
		}
		endAt := start.At.Add(horizon)
		if endAt.After(now) {
			break
		}
		if right < i+1 {
			right = i + 1
		}
		for right < len(m.points) && m.points[right].At.Before(endAt) {
			pushWindow(right)
			right++
		}
		for len(maxDeque) > 0 && maxDeque[0] <= i {
			maxDeque = maxDeque[1:]
		}
		for len(minDeque) > 0 && minDeque[0] <= i {
			minDeque = minDeque[1:]
		}
		if right <= i+1 || len(maxDeque) == 0 || len(minDeque) == 0 || badPrefix[right]-badPrefix[i+1] > 0 {
			continue
		}
		if !lastExposure.IsZero() && start.At.Sub(lastExposure) < time.Minute {
			continue
		}
		weight := 1.0
		if !lastExposure.IsZero() {
			weight = math.Min(1, start.At.Sub(lastExposure).Seconds()/horizon.Seconds())
		}
		if weight <= 0 {
			continue
		}
		startBid, startAsk := start.bidPrice(), start.askPrice()
		terminalBid, terminalAsk := m.points[right-1].bidPrice(), m.points[right-1].askPrice()
		maxBid := m.points[maxDeque[0]].bidPrice()
		minAsk := m.points[minDeque[0]].askPrice()
		outcomes := make([]float64, len(distances))
		for j, distance := range distances {
			if side == types.SideTypeBuy {
				quote := startAsk * math.Exp(-distance/10_000)
				if minAsk <= quote && terminalBid > 0 {
					fillWeight[j] += weight
					outcomes[j] = math.Log(terminalBid/quote)*10_000 - roundTripFeeAndEdgeBps + inventoryRiskBenefitBps
				}
			} else {
				quote := startBid * math.Exp(distance/10_000)
				if maxBid >= quote && terminalAsk > 0 {
					fillWeight[j] += weight
					outcomes[j] = math.Log(quote/terminalAsk)*10_000 - roundTripFeeAndEdgeBps + inventoryRiskBenefitBps
				}
			}
		}
		base := outcomes[0]
		for j := range distances {
			diff := outcomes[j] - base
			diffSum[j] += weight * diff
			diffSquareSum[j] += weight * diff * diff
		}
		sumWeight += weight
		sumWeightSquared += weight * weight
		lastExposure = start.At
	}
	if sumWeight <= 0 {
		return out
	}
	effectiveN := sumWeight
	if sumWeightSquared > 0 {
		effectiveN = math.Min(sumWeight, sumWeight*sumWeight/sumWeightSquared)
	}
	for i := range out {
		out[i].EffectiveSamples = effectiveN
		out[i].FillProbability, _ = jeffreysBernoulliPosterior(fillWeight[i], sumWeight)
		mean := diffSum[i] / sumWeight
		variance := math.Max(0, diffSquareSum[i]/sumWeight-mean*mean)
		se := math.Sqrt(variance / math.Max(1, effectiveN))
		out[i].IncrementalMeanBps = mean
		out[i].IncrementalStdErrorBps = se
		out[i].IncrementalLowerBps = mean - math.Max(0, zScore)*se
	}
	return out
}
