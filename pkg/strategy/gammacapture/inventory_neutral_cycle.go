package gammacapture

import "math"

// InventoryNeutralCycleAllocation decomposes one executable two-sided quote
// into an expected-fill-neutral market-making cycle and a one-sided inventory
// correction.  The decomposition is in quote-currency notional at the common
// inventory mark used by ProbabilityCenteredQuoteNotionals.
type InventoryNeutralCycleAllocation struct {
	BuyNotionalJPY             float64
	SellNotionalJPY            float64
	CycleBuyNotionalJPY        float64
	CycleSellNotionalJPY       float64
	TargetRestoringBuyJPY      float64
	TargetRestoringSellJPY     float64
	ExpectedInventoryChangeJPY float64
}

// inventoryNeutralCycleAllocation solves
//
//	max qBuy + qSell (fixed here at gross)
//	s.t. pBuy*qBuy - pSell*qSell = desiredInventoryChange,
//
// on the caller's executable BUY interval. Capacity clipping can make the
// equality unattainable; in that case the closest boundary is selected. The
// realized flow is then decomposed uniquely into a neutral component satisfying
// pBuy*qCycleBuy = pSell*qCycleSell and a one-sided target-restoring residual.
func inventoryNeutralCycleAllocation(
	gross, desiredInventoryChange, pBuy, pSell,
	minimumBuy, maximumBuy float64,
) InventoryNeutralCycleAllocation {
	d := InventoryNeutralCycleAllocation{}
	values := []float64{
		gross, desiredInventoryChange, pBuy, pSell, minimumBuy, maximumBuy,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return d
		}
	}
	if gross <= 0 || pBuy <= 0 || pSell <= 0 ||
		minimumBuy < 0 || maximumBuy+1e-12 < minimumBuy {
		return d
	}
	minimumBuy = math.Max(0, math.Min(gross, minimumBuy))
	maximumBuy = math.Max(minimumBuy, math.Min(gross, maximumBuy))
	desiredBuy := (desiredInventoryChange + pSell*gross) / (pBuy + pSell)
	d.BuyNotionalJPY = math.Max(minimumBuy, math.Min(maximumBuy, desiredBuy))
	d.SellNotionalJPY = math.Max(0, gross-d.BuyNotionalJPY)
	return decomposeInventoryNeutralCycle(
		d.BuyNotionalJPY, d.SellNotionalJPY, pBuy, pSell)
}

func decomposeInventoryNeutralCycle(
	buyNotionalJPY, sellNotionalJPY, pBuy, pSell float64,
) InventoryNeutralCycleAllocation {
	d := InventoryNeutralCycleAllocation{}
	values := []float64{buyNotionalJPY, sellNotionalJPY, pBuy, pSell}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return d
		}
	}
	if buyNotionalJPY < 0 || sellNotionalJPY < 0 || pBuy <= 0 || pSell <= 0 {
		return d
	}
	d.BuyNotionalJPY = buyNotionalJPY
	d.SellNotionalJPY = sellNotionalJPY
	d.ExpectedInventoryChangeJPY = pBuy*buyNotionalJPY - pSell*sellNotionalJPY
	if d.ExpectedInventoryChangeJPY >= 0 {
		d.TargetRestoringBuyJPY = math.Min(
			buyNotionalJPY, d.ExpectedInventoryChangeJPY/pBuy)
	} else {
		d.TargetRestoringSellJPY = math.Min(
			sellNotionalJPY, -d.ExpectedInventoryChangeJPY/pSell)
	}
	d.CycleBuyNotionalJPY = math.Max(0, buyNotionalJPY-d.TargetRestoringBuyJPY)
	d.CycleSellNotionalJPY = math.Max(0, sellNotionalJPY-d.TargetRestoringSellJPY)
	return d
}
