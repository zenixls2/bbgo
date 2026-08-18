package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestScoreFastHorizonWithMarginalBuyUsesCommonBpsPerHourUnit(t *testing.T) {
	decision := MarketMakerHorizonDecision{
		Horizon: 10 * time.Minute, ScoreBpsPerHour: 4,
	}
	stats := JointPathPayoffStats{
		EffectiveSamples: 25,
		BuyDominant: jointPathPayoffMoments{
			BuyMeanBps: 20, BuyVarBps2: 16,
			InventoryVarBps2: 25, InventoryBuyCovBps2: -5,
		},
		SellDominant: jointPathPayoffMoments{},
	}
	in := FastHorizonMarginalBuyInput{
		CurrentInventoryNotionalJPY: 400,
		TargetInventoryNotionalJPY:  600,
		PairEquityJPY:               1000,
		MarginalBuyNotionalJPY:      100,
		AvailableBuyCapitalJPY:      600,
		RiskAversion:                1,
		ConfidenceZScore:            0,
	}
	got := scoreFastHorizonWithMarginalBuy(decision, stats, in)
	if !got.MarginalBuyEvaluated || got.MarginalBuyCertaintyEquivalentJPY <= 0 {
		t.Fatalf("expected positive marginal BUY evaluation: %+v", got)
	}
	wantUtility := got.MarginalBuyCertaintyEquivalentJPY / in.PairEquityJPY * 10_000 / decision.Horizon.Hours()
	if math.Abs(got.MarginalBuyUtilityBpsPerHour-wantUtility) > 1e-12 ||
		math.Abs(got.SelectionScoreBpsPerHour-(decision.ScoreBpsPerHour+wantUtility)) > 1e-12 {
		t.Fatalf("selection score units disagree: got=%+v wantUtility=%f", got, wantUtility)
	}
}

func TestScoreFastHorizonWithPathUtilityReplacesCrossingScore(t *testing.T) {
	decision := MarketMakerHorizonDecision{
		Horizon: 5 * time.Minute, ScoreBpsPerHour: 999,
	}
	stats := JointPathPayoffStats{
		EffectiveSamples: 25,
		BuyDominant: jointPathPayoffMoments{
			BuyMeanBps: 30, SellMeanBps: 30,
			BuyVarBps2: 4, SellVarBps2: 4,
		},
		SellDominant: jointPathPayoffMoments{
			BuyMeanBps: 30, SellMeanBps: 30,
			BuyVarBps2: 4, SellVarBps2: 4,
		},
	}
	in := FastHorizonMarginalBuyInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		HardMinInventoryNotionalJPY: 0,
		HardMaxInventoryNotionalJPY: 1_000,
		PairEquityJPY:               1_000,
		MarginalBuyNotionalJPY:      100,
		AvailableBuyCapitalJPY:      500,
		MarginalSellNotionalJPY:     100,
		AvailableSellInventoryJPY:   500,
		ConfidenceZScore:            1,
	}
	got := scoreFastHorizonWithPathUtility(decision, stats, in)
	if !got.PathUtilityEvaluated || got.PathUtilityCertaintyEquivalentJPY <= 0 ||
		got.PathUtilityBuyNotionalJPY != 100 || got.PathUtilitySellNotionalJPY != 100 {
		t.Fatalf("expected executable bilateral path utility: %+v", got)
	}
	want := got.PathUtilityCertaintyEquivalentJPY / in.PairEquityJPY * 10_000 /
		decision.Horizon.Hours()
	if math.Abs(got.SelectionScoreBpsPerHour-want) > 1e-12 ||
		math.Abs(got.PathUtilityBpsPerHour-want) > 1e-12 ||
		got.SelectionScoreBpsPerHour == decision.ScoreBpsPerHour {
		t.Fatalf("path utility must replace, not mix with, crossing score: got=%+v want=%f", got, want)
	}
}

func TestScoreFastHorizonWithPathUtilityUsesOnlyExecutableSides(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 10 * time.Minute, ScoreBpsPerHour: 5}
	stats := JointPathPayoffStats{
		EffectiveSamples: 20,
		SellDominant:     jointPathPayoffMoments{SellMeanBps: 20, SellVarBps2: 4},
	}
	in := FastHorizonMarginalBuyInput{
		CurrentInventoryNotionalJPY: 900,
		TargetInventoryNotionalJPY:  500,
		HardMinInventoryNotionalJPY: 0,
		HardMaxInventoryNotionalJPY: 900,
		PairEquityJPY:               1_000,
		MarginalBuyNotionalJPY:      100,
		AvailableBuyCapitalJPY:      100,
		MarginalSellNotionalJPY:     100,
		AvailableSellInventoryJPY:   900,
	}
	got := scoreFastHorizonWithPathUtility(decision, stats, in)
	if !got.PathUtilityEvaluated || got.PathUtilityBuyNotionalJPY != 0 ||
		got.PathUtilitySellNotionalJPY != 100 {
		t.Fatalf("hard inventory headroom must remove only the infeasible side: %+v", got)
	}
}

func TestScoreFastHorizonWithMarginalBuyRequiresExecutableTargetDeficit(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 15 * time.Minute, ScoreBpsPerHour: 7}
	stats := JointPathPayoffStats{
		EffectiveSamples: 20,
		BuyDominant:      jointPathPayoffMoments{BuyMeanBps: 100},
	}
	for name, in := range map[string]FastHorizonMarginalBuyInput{
		"at target": {
			CurrentInventoryNotionalJPY: 500, TargetInventoryNotionalJPY: 500,
			PairEquityJPY: 1000, MarginalBuyNotionalJPY: 100, AvailableBuyCapitalJPY: 500,
		},
		"sub-lattice deficit": {
			CurrentInventoryNotionalJPY: 450, TargetInventoryNotionalJPY: 500,
			PairEquityJPY: 1000, MarginalBuyNotionalJPY: 100, AvailableBuyCapitalJPY: 500,
		},
		"insufficient quote": {
			CurrentInventoryNotionalJPY: 300, TargetInventoryNotionalJPY: 500,
			PairEquityJPY: 1000, MarginalBuyNotionalJPY: 100, AvailableBuyCapitalJPY: 99,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := scoreFastHorizonWithMarginalBuy(decision, stats, in)
			if got.MarginalBuyEvaluated || got.SelectionScoreBpsPerHour != decision.ScoreBpsPerHour {
				t.Fatalf("non-executable BUY changed horizon score: %+v", got)
			}
		})
	}
}

func TestScoreFastHorizonWithMarginalBuyDeclinesNegativeOptionWithoutPenalizingHorizon(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 30 * time.Minute, ScoreBpsPerHour: 5}
	in := FastHorizonMarginalBuyInput{
		CurrentInventoryNotionalJPY: 800, TargetInventoryNotionalJPY: 1000,
		PairEquityJPY: 2000, MarginalBuyNotionalJPY: 100, AvailableBuyCapitalJPY: 1200,
		RiskAversion: 2, ConfidenceZScore: 1.645,
	}
	stats := JointPathPayoffStats{
		EffectiveSamples: 16,
		BuyDominant: jointPathPayoffMoments{
			BuyMeanBps: -12, BuyVarBps2: 64,
			InventoryVarBps2: 100, InventoryBuyCovBps2: 40,
		},
	}
	got := scoreFastHorizonWithMarginalBuy(decision, stats, in)
	if !got.MarginalBuyEvaluated || got.MarginalBuyCertaintyEquivalentJPY >= 0 ||
		got.MarginalBuyUtilityBpsPerHour != 0 ||
		got.SelectionScoreBpsPerHour != decision.ScoreBpsPerHour {
		t.Fatalf("negative BUY option must be declined without penalizing the horizon: %+v", got)
	}
}

func TestScoreFastHorizonWithMarginalBuyUsesSameHorizonPosteriorTarget(t *testing.T) {
	decision := MarketMakerHorizonDecision{Horizon: 10 * time.Minute, ScoreBpsPerHour: 5}
	stats := JointPathPayoffStats{
		EffectiveSamples: 25,
		BuyDominant: jointPathPayoffMoments{
			InventoryMeanBps: 20, InventoryVarBps2: 100,
			InventoryDirectionalMeanBps: 20, InventoryDirectionalVarBps2: 100,
			BuyMeanBps: 20, BuyVarBps2: 100, InventoryBuyCovBps2: 100,
		},
		SellDominant: jointPathPayoffMoments{
			InventoryMeanBps: 20, InventoryVarBps2: 100,
			InventoryDirectionalMeanBps: 20, InventoryDirectionalVarBps2: 100,
			BuyMeanBps: 20, BuyVarBps2: 100, InventoryBuyCovBps2: 100,
		},
	}
	in := FastHorizonMarginalBuyInput{
		CurrentInventoryNotionalJPY: 600,
		TargetInventoryNotionalJPY:  500,
		HardMinInventoryNotionalJPY: 0,
		HardMaxInventoryNotionalJPY: 1_000,
		PosteriorInventoryTarget:    true,
		PairEquityJPY:               1_000,
		MarginalBuyNotionalJPY:      100,
		AvailableBuyCapitalJPY:      400,
		RiskAversion:                1,
	}
	got := scoreFastHorizonWithMarginalBuy(decision, stats, in)
	if !got.MarginalBuyEvaluated || got.MarginalBuyTargetNotionalJPY <= 600 ||
		got.MarginalBuyTargetUpProbability <= 0.5 {
		t.Fatalf("supported upside must move the horizon target before valuing BUY: %+v", got)
	}
}
