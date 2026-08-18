package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestInventoryNeutralCycleAllocationSeparatesCycleAndBuyCorrection(t *testing.T) {
	d := inventoryNeutralCycleAllocation(1_100, 80, 0.4, 0.2, 0, 1_100)
	if math.Abs(d.BuyNotionalJPY-500) > 1e-12 ||
		math.Abs(d.SellNotionalJPY-600) > 1e-12 {
		t.Fatalf("unexpected quote allocation: %+v", d)
	}
	if math.Abs(d.CycleBuyNotionalJPY-300) > 1e-12 ||
		math.Abs(d.CycleSellNotionalJPY-600) > 1e-12 ||
		math.Abs(d.TargetRestoringBuyJPY-200) > 1e-12 ||
		d.TargetRestoringSellJPY != 0 {
		t.Fatalf("cycle and BUY correction were not separated: %+v", d)
	}
	if math.Abs(0.4*d.CycleBuyNotionalJPY-0.2*d.CycleSellNotionalJPY) > 1e-12 {
		t.Fatalf("cycle flow is not expected-fill neutral: %+v", d)
	}
}

func TestProbabilityCenteredFullRiskPromotionUsesNeutralCycleCapacity(t *testing.T) {
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 1_000,
		TargetInventoryNotionalJPY:  1_000,
		LowerInventoryNotionalJPY:   0,
		UpperInventoryNotionalJPY:   2_000,
		FastBuyNotionalJPY:          400,
		FastSellNotionalJPY:         800,
		MinBuyNotionalJPY:           100,
		MinSellNotionalJPY:          100,
		MaxBuyNotionalJPY:           400,
		MaxSellNotionalJPY:          800,
		DirectFillProbabilities:     true,
		FullRiskPromotion:           true,
		BuyFillProbability:          0.4,
		SellFillProbability:         0.2,
		BothFillProbability:         0.1,
		Horizon:                     30 * time.Minute,
		ConfidenceZScore:            1,
	})
	if !d.Enabled || d.ProjectedGrossNotionalJPY <= 200 {
		t.Fatalf("identified terminal utility must be able to promote beyond venue minimums: %+v", d)
	}
	if math.Abs(d.ExpectedInventoryNotionalJPY-1_000) > 1e-9 {
		t.Fatalf("cycle promotion changed expected target inventory: %+v", d)
	}
	if d.TargetRestoringBuyJPY > 1e-9 || d.TargetRestoringSellJPY > 1e-9 ||
		d.CycleBuyNotionalJPY <= 0 || d.CycleSellNotionalJPY <= 0 {
		t.Fatalf("at-target capacity must be an inventory-neutral cycle: %+v", d)
	}
	if math.Abs(.4*d.CycleBuyNotionalJPY-.2*d.CycleSellNotionalJPY) > 1e-9 {
		t.Fatalf("promoted cycle is not fill-probability neutral: %+v", d)
	}
}

func TestInventoryNeutralCycleAllocationIsBuySellSymmetric(t *testing.T) {
	buy := inventoryNeutralCycleAllocation(1_100, 80, 0.4, 0.2, 0, 1_100)
	sell := inventoryNeutralCycleAllocation(1_100, -80, 0.2, 0.4, 0, 1_100)
	if math.Abs(buy.BuyNotionalJPY-sell.SellNotionalJPY) > 1e-12 ||
		math.Abs(buy.SellNotionalJPY-sell.BuyNotionalJPY) > 1e-12 ||
		math.Abs(buy.TargetRestoringBuyJPY-sell.TargetRestoringSellJPY) > 1e-12 ||
		math.Abs(buy.CycleBuyNotionalJPY-sell.CycleSellNotionalJPY) > 1e-12 ||
		math.Abs(buy.CycleSellNotionalJPY-sell.CycleBuyNotionalJPY) > 1e-12 {
		t.Fatalf("BUY/SELL reflection changed the allocation: buy=%+v sell=%+v", buy, sell)
	}
}

func TestInventoryNeutralCycleAllocationRespectsExecutableBoundary(t *testing.T) {
	d := inventoryNeutralCycleAllocation(500, 400, 0.2, 0.2, 100, 300)
	if d.BuyNotionalJPY != 300 || d.SellNotionalJPY != 200 {
		t.Fatalf("capacity boundary was not respected: %+v", d)
	}
	if d.ExpectedInventoryChangeJPY >= 400 {
		t.Fatalf("clipped capacity must report realized rather than desired correction: %+v", d)
	}
}

func TestInventoryNeutralCycleAllocationFailsClosedOnInvalidInput(t *testing.T) {
	for _, d := range []InventoryNeutralCycleAllocation{
		inventoryNeutralCycleAllocation(0, 0, .2, .2, 0, 0),
		inventoryNeutralCycleAllocation(100, 0, 0, .2, 0, 100),
		inventoryNeutralCycleAllocation(100, math.NaN(), .2, .2, 0, 100),
	} {
		if d.BuyNotionalJPY != 0 || d.SellNotionalJPY != 0 ||
			d.CycleBuyNotionalJPY != 0 || d.CycleSellNotionalJPY != 0 {
			t.Fatalf("invalid input produced a quote: %+v", d)
		}
	}
}

func TestInventoryNeutralCycleAllocationMatchesLegacyClosedForm(t *testing.T) {
	grossValues := []float64{100, 153.4, 1_100, 4_000}
	deltaFractions := []float64{-1.5, -1, -.5, 0, .5, 1, 1.5}
	probabilities := []float64{.01, .05, .2, .4, .9}
	boundaryFractions := [][2]float64{
		{0, 1},
		{.1, .9},
		{.4, .6},
		{.5, .5},
	}
	for _, gross := range grossValues {
		for _, deltaFraction := range deltaFractions {
			for _, pBuy := range probabilities {
				for _, pSell := range probabilities {
					for _, boundary := range boundaryFractions {
						minimumBuy := boundary[0] * gross
						maximumBuy := boundary[1] * gross
						desiredDelta := deltaFraction * gross
						legacyBuy := (desiredDelta + pSell*gross) / (pBuy + pSell)
						legacyBuy = math.Max(minimumBuy, math.Min(maximumBuy, legacyBuy))
						legacySell := math.Max(0, gross-legacyBuy)

						got := inventoryNeutralCycleAllocation(
							gross, desiredDelta, pBuy, pSell, minimumBuy, maximumBuy)
						if got.BuyNotionalJPY != legacyBuy || got.SellNotionalJPY != legacySell {
							t.Fatalf("closed-form allocation changed: gross=%v delta=%v pBuy=%v pSell=%v bounds=[%v,%v] legacy=(%v,%v) got=%+v",
								gross, desiredDelta, pBuy, pSell, minimumBuy, maximumBuy,
								legacyBuy, legacySell, got)
						}
					}
				}
			}
		}
	}
}
