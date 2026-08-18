package gammacapture

import (
	"math"
	"testing"
	"time"
)

func symmetricActionStats(buyMean, sellMean float64) JointPathPayoffStats {
	moments := jointPathPayoffMoments{
		BuyMeanBps: buyMean, SellMeanBps: sellMean,
		BuyVarBps2: 4, SellVarBps2: 4,
		InventoryVarBps2:     100,
		InventoryBuyCovBps2:  100,
		InventorySellCovBps2: -100,
	}
	return JointPathPayoffStats{EffectiveSamples: 100, BuyDominant: moments, SellDominant: moments}
}

func symmetricActionInput(current, target float64) FastHorizonMarginalBuyInput {
	return FastHorizonMarginalBuyInput{
		CurrentInventoryNotionalJPY: current,
		TargetInventoryNotionalJPY:  target,
		HardMinInventoryNotionalJPY: 0,
		HardMaxInventoryNotionalJPY: 1_000,
		PairEquityJPY:               1_000,
		MarginalBuyNotionalJPY:      100,
		AvailableBuyCapitalJPY:      1_000 - current,
		MarginalSellNotionalJPY:     100,
		AvailableSellInventoryJPY:   current,
		RiskAversion:                1,
		ConfidenceZScore:            0,
	}
}

func TestSymmetricFastHorizonActionRestoresInventoryOnEitherSide(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 15 * time.Minute}
	stats := symmetricActionStats(0, 0)
	buyInput := symmetricActionInput(300, 500)
	sellInput := symmetricActionInput(700, 500)
	buyInput.RiskAversion, sellInput.RiskAversion = 1_000, 1_000
	buyInput.ConfidenceZScore, sellInput.ConfidenceZScore = 1e-9, 1e-9
	buy := evaluateSymmetricFastHorizonAction(decision, stats, buyInput)
	sell := evaluateSymmetricFastHorizonAction(decision, stats, sellInput)
	if buy.Action != FastHorizonBuy || sell.Action != FastHorizonSell {
		t.Fatalf("target-restoring actions must be side symmetric: buy=%+v sell=%+v", buy, sell)
	}
	if math.Abs(buy.CertaintyEquivalentJPY-sell.CertaintyEquivalentJPY) > 1e-12 {
		t.Fatalf("reflected states must have equal CE: buy=%+v sell=%+v", buy, sell)
	}
}

func TestSymmetricFastHorizonActionCanChooseBoth(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 15 * time.Minute}
	got := evaluateSymmetricFastHorizonAction(
		decision, symmetricActionStats(20, 20), symmetricActionInput(500, 500))
	if got.Action != FastHorizonBoth || got.BuyNotionalJPY != 100 || got.SellNotionalJPY != 100 {
		t.Fatalf("positive bilateral cycle must choose BOTH: %+v", got)
	}
}

func TestSymmetricFastHorizonActionKeepsNoOrderForNegativeCE(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 30 * time.Minute}
	got := evaluateSymmetricFastHorizonAction(
		decision, symmetricActionStats(-20, -20), symmetricActionInput(500, 500))
	if !got.Evaluated || got.Action != FastHorizonNoOrder || got.CertaintyEquivalentJPY != 0 || got.ScoreBpsPerHour != 0 {
		t.Fatalf("negative candidates must preserve zero-order option: %+v", got)
	}
}

func TestSymmetricFastHorizonActionHonorsIndependentHardBounds(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 15 * time.Minute}
	input := symmetricActionInput(900, 500)
	input.HardMaxInventoryNotionalJPY = 900
	got := evaluateSymmetricFastHorizonAction(decision, symmetricActionStats(100, 10), input)
	if got.Action != FastHorizonSell || got.BuyNotionalJPY != 0 {
		t.Fatalf("infeasible BUY must not suppress feasible SELL: %+v", got)
	}
}

func TestSymmetricFastHorizonActionUsesCommonPerHourUnit(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 15 * time.Minute}
	input := symmetricActionInput(500, 500)
	got := evaluateSymmetricFastHorizonAction(decision, symmetricActionStats(20, 20), input)
	want := got.CertaintyEquivalentJPY / input.PairEquityJPY * 10_000 / decision.Horizon.Hours()
	if math.Abs(got.ScoreBpsPerHour-want) > 1e-12 {
		t.Fatalf("score units disagree: got=%+v want=%f", got, want)
	}
}
