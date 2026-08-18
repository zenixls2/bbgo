package gammacapture

import (
	"math"
)

// FastHorizonAction is the single-cell inventory action considered while
// comparing Fast horizons.  The downstream quote optimizer still owns the
// final distance and quantity; this action only puts BUY and SELL on the same
// horizon-selection footing.
type FastHorizonAction string

const (
	FastHorizonNoOrder FastHorizonAction = "NO_ORDER"
	FastHorizonBuy     FastHorizonAction = "BUY"
	FastHorizonSell    FastHorizonAction = "SELL"
	FastHorizonBoth    FastHorizonAction = "BOTH"
)

type FastHorizonActionValue struct {
	Evaluated                  bool
	Action                     FastHorizonAction
	BuyNotionalJPY             float64
	SellNotionalJPY            float64
	TargetInventoryNotionalJPY float64
	CertaintyEquivalentJPY     float64
	ScoreBpsPerHour            float64
}

// evaluateSymmetricFastHorizonAction chooses the best feasible action from
// {NO_ORDER, BUY, SELL, BOTH}.  NO_ORDER has exactly zero incremental value,
// which prevents an unfavorable horizon from being selected merely because a
// crossing-frequency score is positive.  Every non-zero action uses the same
// account state, horizon-specific target and JointPathPayoffStats instance.
func evaluateSymmetricFastHorizonAction(
	decision MarketMakerHorizonDecision,
	stats JointPathPayoffStats,
	in FastHorizonMarginalBuyInput,
) FastHorizonActionValue {
	result := FastHorizonActionValue{Action: FastHorizonNoOrder}
	if decision.Horizon <= 0 || in.PairEquityJPY <= 0 || stats.EffectiveSamples <= 1 {
		return result
	}
	target := in.TargetInventoryNotionalJPY
	if in.PosteriorInventoryTarget {
		posterior := PosteriorInventoryRiskTarget(
			in.TargetInventoryNotionalJPY,
			in.HardMinInventoryNotionalJPY,
			in.HardMaxInventoryNotionalJPY,
			stats)
		if posterior.Enabled {
			target = posterior.TargetBase
		}
	}
	result.Evaluated = true
	result.TargetInventoryNotionalJPY = target

	buy := math.Min(in.MarginalBuyNotionalJPY, in.AvailableBuyCapitalJPY)
	if buy <= 0 || in.CurrentInventoryNotionalJPY+buy > in.HardMaxInventoryNotionalJPY+1e-9 {
		buy = 0
	}
	sell := math.Min(in.MarginalSellNotionalJPY, in.AvailableSellInventoryJPY)
	if sell <= 0 || in.CurrentInventoryNotionalJPY-sell < in.HardMinInventoryNotionalJPY-1e-9 {
		sell = 0
	}
	z := in.ConfidenceZScore
	if z <= 0 {
		z = 1.645
	}
	type candidate struct {
		action    FastHorizonAction
		buy, sell float64
	}
	candidates := make([]candidate, 0, 3)
	if buy > 0 {
		candidates = append(candidates, candidate{action: FastHorizonBuy, buy: buy})
	}
	if sell > 0 {
		candidates = append(candidates, candidate{action: FastHorizonSell, sell: sell})
	}
	if buy > 0 && sell > 0 {
		candidates = append(candidates, candidate{action: FastHorizonBoth, buy: buy, sell: sell})
	}
	for _, candidate := range candidates {
		value := stats.EvaluateTargetRelativePosition(
			in.CurrentInventoryNotionalJPY, target,
			candidate.buy, candidate.sell,
			in.PairEquityJPY, in.RiskAversion, z)
		// Strict improvement preserves NO_ORDER on exact ties.
		if value.CertaintyEquivalent > result.CertaintyEquivalentJPY+1e-12 {
			result.Action = candidate.action
			result.BuyNotionalJPY = candidate.buy
			result.SellNotionalJPY = candidate.sell
			result.CertaintyEquivalentJPY = value.CertaintyEquivalent
		}
	}
	result.ScoreBpsPerHour = result.CertaintyEquivalentJPY /
		in.PairEquityJPY * 10_000 / decision.Horizon.Hours()
	return result
}
