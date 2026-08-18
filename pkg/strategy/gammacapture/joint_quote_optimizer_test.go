package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestChooseSideSafeFallbackNeverReturnsBothSides(t *testing.T) {
	cases := []struct {
		name                        string
		buySupported, sellSupported bool
		buyScore, sellScore         float64
		current, target             float64
		wantBuy, wantOK             bool
	}{
		{name: "buy only", buySupported: true, buyScore: 2, wantBuy: true, wantOK: true},
		{name: "sell only", sellSupported: true, sellScore: 2, wantBuy: false, wantOK: true},
		{name: "stronger buy", buySupported: true, sellSupported: true, buyScore: 2, sellScore: 1, wantBuy: true, wantOK: true},
		{name: "stronger sell", buySupported: true, sellSupported: true, buyScore: 1, sellScore: 2, wantBuy: false, wantOK: true},
		{name: "tie above target", buySupported: true, sellSupported: true, buyScore: 1, sellScore: 1, current: 6, target: 5, wantBuy: false, wantOK: true},
		{name: "tie below target", buySupported: true, sellSupported: true, buyScore: 1, sellScore: 1, current: 4, target: 5, wantBuy: true, wantOK: true},
		{name: "none", wantBuy: false, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBuy, gotOK := chooseSideSafeFallback(
				tc.buySupported, tc.sellSupported, tc.buyScore, tc.sellScore,
				tc.current, tc.target)
			if gotBuy != tc.wantBuy || gotOK != tc.wantOK {
				t.Fatalf("chooseSideSafeFallback()=(%v,%v), want (%v,%v)",
					gotBuy, gotOK, tc.wantBuy, tc.wantOK)
			}
		})
	}
}

func TestSideSafeFallbackAdmissionRequiresRobustRiskReduction(t *testing.T) {
	ordinary := JointPathPayoffDecision{CertaintyEquivalent: -0.01, RiskReducing: false}
	if _, admitted, _ := sideSafeFallbackAdmission(-0.01, ordinary, 0); admitted {
		t.Fatal("fee-negative non-risk-reducing side must remain rejected")
	}
	riskReducing := JointPathPayoffDecision{CertaintyEquivalent: 0.02, RiskReducing: true}
	score, admitted, hedge := sideSafeFallbackAdmission(-0.01, riskReducing, 0)
	if !admitted || !hedge || math.Abs(score-0.02) > 1e-12 {
		t.Fatalf("positive robust CE should admit only the risk-reducing side: score=%.12f admitted=%t hedge=%t", score, admitted, hedge)
	}
	negativeRobust := JointPathPayoffDecision{CertaintyEquivalent: -0.02, RiskReducing: true}
	if _, admitted, _ := sideSafeFallbackAdmission(-0.01, negativeRobust, 0); admitted {
		t.Fatal("risk reduction without positive robust CE must remain rejected")
	}
}

func TestFastValueIdentificationFloorDoesNotDoubleCountFee(t *testing.T) {
	config := MarketMakerConfig{
		MakerFeeBps:     10,
		HorizonLookback: types.Duration(6 * time.Hour),
	}
	got := fastValueIdentificationFloor(config, 10*time.Minute, 100, 120)
	if got != 0 {
		t.Fatalf("fee-net posterior value must be compared with no order, got floor=%g", got)
	}
	if got := fastValueIdentificationFloor(config, 10*time.Minute, 0, 0); got != 0 {
		t.Fatalf("missing executable side must not invent a value floor: %g", got)
	}
}

func TestJointDistanceCandidatePlanOnlyMovesOutward(t *testing.T) {
	base := MarketMakerQuotePlan{
		BidPrice: 99.7, AskPrice: 100.3,
		BidTouchDistanceBps: 40, AskTouchDistanceBps: 40,
		AllowBid: true, AllowAsk: true,
	}
	got := jointDistanceCandidatePlan(base, 99.9, 100.1, 100, 80, 1)
	if got.BidPrice > base.BidPrice+1e-12 || got.AskPrice < base.AskPrice-1e-12 {
		t.Fatalf("candidate moved inward: base=%+v candidate=%+v", base, got)
	}
	if got.BidTouchDistanceBps+1e-12 < base.BidTouchDistanceBps ||
		got.AskTouchDistanceBps+1e-12 < base.AskTouchDistanceBps {
		t.Fatalf("candidate reduced executable distance: base=%+v candidate=%+v", base, got)
	}
}

func TestJointDistanceCandidatePlansAddsSymmetricPassiveInwardLadders(t *testing.T) {
	base := MarketMakerQuotePlan{
		BidPrice: 99.70, AskPrice: 100.30,
		BidTouchDistanceBps: math.Log(100.10/99.70) * 10_000,
		AskTouchDistanceBps: math.Log(100.30/99.90) * 10_000,
		BidDistanceBps:      math.Log(100/99.70) * 10_000,
		AskDistanceBps:      math.Log(100.30/100) * 10_000,
		BidHalfSpreadBps:    math.Log(100/99.70) * 10_000,
		AskHalfSpreadBps:    math.Log(100.30/100) * 10_000,
		AllowBid:            true, AllowAsk: true,
	}
	withoutSignal := jointDistanceCandidatePlans(base, 99.90, 100.10, 100, 80, false, 5)
	if len(withoutSignal) != 5 {
		t.Fatalf("missing signal must preserve the exact outward search size: %d", len(withoutSignal))
	}

	plans := jointDistanceCandidatePlans(base, 99.90, 100.10, 100, 80, true, 5)
	if len(plans) != 13 {
		t.Fatalf("expected five outward plus four candidates per inward side, got %d", len(plans))
	}
	previousBid := base.BidPrice
	for index, candidate := range plans[5:9] {
		if candidate.BidPrice <= previousBid || candidate.BidPrice > 99.90+1e-12 ||
			candidate.BidPrice >= 100.10 {
			t.Fatalf("inward candidate %d is not passive and monotone: %+v", index, candidate)
		}
		if math.Abs(candidate.AskPrice-base.AskPrice) > 1e-12 ||
			math.Abs(candidate.AskTouchDistanceBps-base.AskTouchDistanceBps) > 1e-12 {
			t.Fatalf("BUY hypothesis changed SELL candidate %d: base=%+v candidate=%+v",
				index, base, candidate)
		}
		if candidate.BidTouchDistanceBps >= base.BidTouchDistanceBps {
			t.Fatalf("inward candidate did not increase BUY touch probability domain: %+v", candidate)
		}
		previousBid = candidate.BidPrice
	}
	previousAsk := base.AskPrice
	for index, candidate := range plans[9:] {
		if candidate.AskPrice >= previousAsk || candidate.AskPrice < 100.10-1e-12 ||
			candidate.AskPrice <= 99.90 {
			t.Fatalf("inward SELL candidate %d is not passive and monotone: %+v", index, candidate)
		}
		if math.Abs(candidate.BidPrice-base.BidPrice) > 1e-12 ||
			math.Abs(candidate.BidTouchDistanceBps-base.BidTouchDistanceBps) > 1e-12 {
			t.Fatalf("SELL hypothesis changed BUY candidate %d: base=%+v candidate=%+v",
				index, base, candidate)
		}
		if candidate.AskTouchDistanceBps >= base.AskTouchDistanceBps {
			t.Fatalf("inward SELL candidate did not increase SELL touch domain: %+v", candidate)
		}
		previousAsk = candidate.AskPrice
	}
}

func TestJointPathPositiveConfidence(t *testing.T) {
	if got := jointPathPositiveConfidence(-1, 1); got != 0 {
		t.Fatalf("negative path mean must deploy no directional confidence: %v", got)
	}
	if got := jointPathPositiveConfidence(1, 0); got != 1 {
		t.Fatalf("certain positive path mean must deploy full confidence: %v", got)
	}
	want := 0.6826894921370859
	if got := jointPathPositiveConfidence(1, 1); math.Abs(got-want) > 1e-12 {
		t.Fatalf("one-standard-error posterior sign mismatch: got %v want %v", got, want)
	}
}

func TestFastFeeNetRegretValue(t *testing.T) {
	mean, downside, net := fastFeeNetRegretValue(JointPathPayoffDecision{
		ExpectedPnLJPY: 0.1, StdErrorJPY: 1,
	})
	if mean != 0.1 || downside <= 0.1 || net >= 0 {
		t.Fatalf("weak positive mean must not pay posterior downside regret: mean=%v downside=%v net=%v",
			mean, downside, net)
	}
	mean, downside, net = fastFeeNetRegretValue(JointPathPayoffDecision{
		ExpectedPnLJPY: 1, StdErrorJPY: 1,
	})
	if mean != 1 || downside <= 0 || net <= 0 {
		t.Fatalf("supported fee-net value should survive: mean=%v downside=%v net=%v",
			mean, downside, net)
	}
	// A negative marginal variance penalty is the value of reducing existing
	// inventory risk. It may rationally admit a slightly negative standalone
	// markout without using a separate target-gap exception.
	mean, downside, net = fastFeeNetRegretValue(JointPathPayoffDecision{
		ExpectedPnLJPY: -0.1, KellyPenaltyJPY: -0.5,
	})
	if math.Abs(mean-0.4) > 1e-12 || downside != 0 || math.Abs(net-0.4) > 1e-12 {
		t.Fatalf("risk-reduction credit was not included exactly once: mean=%v downside=%v net=%v",
			mean, downside, net)
	}
}

func TestFastCandidateValueChargesUncertaintyOnceForCompleteCycle(t *testing.T) {
	payoff := JointPathPayoffDecision{ExpectedPnLJPY: 0.1, StdErrorJPY: 1}
	_, pairedRegret, pairedNet := fastCandidateValue(
		payoff, 100, 100, 0.5, 0.5, 0.5)
	if pairedRegret != 0 || pairedNet != payoff.ExpectedPnLJPY {
		t.Fatalf("fully matched cycle must use confidence sizing rather than a second regret charge: regret=%v net=%v",
			pairedRegret, pairedNet)
	}
	_, partialRegret, partialNet := fastCandidateValue(
		payoff, 100, 100, 0.5, 0.5, 0.25)
	_, fullRegret, _ := fastFeeNetRegretValue(payoff)
	if math.Abs(partialRegret-0.5*fullRegret) > 1e-12 ||
		math.Abs(partialNet-(payoff.ExpectedPnLJPY-partialRegret)) > 1e-12 {
		t.Fatalf("paired regret must equal the empirical unmatched-touch share: regret=%v net=%v",
			partialRegret, partialNet)
	}
	_, unilateralRegret, unilateralNet := fastCandidateValue(
		payoff, 100, 0, 0.5, 0, 0)
	if unilateralRegret <= 0 || unilateralNet >= 0 {
		t.Fatalf("unilateral trend quote must retain downside-regret admission: regret=%v net=%v",
			unilateralRegret, unilateralNet)
	}
}

func TestFastUnmatchedTouchShareMatchesJointCrossingFormula(t *testing.T) {
	got := fastUnmatchedTouchShare(0.6, 0.4, 0.3)
	want := 1 - 2*0.3/(0.6+0.4)
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("unmatched-touch result changed: got %v want %v", got, want)
	}
}

func TestFastUnmatchedTouchShareIsSideSymmetric(t *testing.T) {
	forward := fastUnmatchedTouchShare(0.7, 0.4, 0.35)
	reflected := fastUnmatchedTouchShare(0.4, 0.7, 0.35)
	if math.Abs(forward-reflected) > 1e-12 {
		t.Fatalf("BUY/SELL reflection changed unmatched-touch share: forward=%v reflected=%v",
			forward, reflected)
	}
}

func TestProtectPostFillCompletionKeepsOnlyApprovedOppositeLeg(t *testing.T) {
	config := MarketMakerConfig{
		InventoryRiskZScore: 1.282,
		JointDistanceQuantity: JointDistanceQuantityConfig{
			Enabled: true,
		},
	}
	input := JointDistanceQuantityInput{
		Now: time.Unix(1_000, 0), Horizon: 10 * time.Minute,
		BestBid: 99.9, BestAsk: 100.1, MidPrice: 100,
		CompletionSide: types.SideTypeSell,
		BasePlan: MarketMakerQuotePlan{
			BidPrice: 99.5, AskPrice: 100.5, AllowBid: true, AllowAsk: true,
		},
	}
	projection := ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 3_500,
		TargetInventoryNotionalJPY:  3_500,
		MinBuyNotionalJPY:           100,
		MinSellNotionalJPY:          120,
		MaxBuyNotionalJPY:           1_000,
		MaxSellNotionalJPY:          1_000,
		ConfidenceZScore:            1.282,
	}
	got := protectPostFillCompletion(
		&MarketMakerHorizonModel{}, config, input, projection)
	if !got.Applied || !got.CompletionProtected || got.AuthoritativeRejection ||
		got.Plan.AllowBid || !got.Plan.AllowAsk ||
		got.Projection.BuyNotionalJPY != 0 ||
		got.Projection.SellNotionalJPY != projection.MinSellNotionalJPY {
		t.Fatalf("approved post-fill SELL was not preserved as one safe cell: %+v", got)
	}

	projection.MaxSellNotionalJPY = 100
	blocked := protectPostFillCompletion(
		&MarketMakerHorizonModel{}, config, input, projection)
	if blocked.Applied || blocked.CompletionProtected {
		t.Fatalf("completion must not bypass hard/exchange capacity: %+v", blocked)
	}
}

func TestPosteriorRiskSizedSideCapRespectsHardCapacityAndPosteriorRisk(t *testing.T) {
	strong := JointPathPayoffStats{
		EffectiveSamples: 100,
		BuyDominant: jointPathPayoffMoments{
			BuyMeanBps: 50, BuyVarBps2: 10_000,
		},
		SellDominant: jointPathPayoffMoments{
			SellMeanBps: -10, SellVarBps2: 10_000,
		},
	}
	if got := posteriorRiskSizedSideCap(strong, true, 250, 100, 0, 0, 10_000, 1, 1.282); got <= 100 || got > 250 {
		t.Fatalf("strong BUY evidence must use more than one cell without exceeding current hard cap: %v", got)
	}
	if got := posteriorRiskSizedSideCap(strong, false, 250, 100, 0, 0, 10_000, 1, 1.282); got != 0 {
		t.Fatalf("negative SELL posterior must receive no risk capacity: %v", got)
	}
	if got := posteriorRiskSizedSideCap(strong, true, 250, 100, 0, 0, 10_000, 1, 1e6); got != 0 {
		t.Fatalf("a posterior whose confidence lower bound is negative must receive no promoted capacity: %v", got)
	}

}

func TestHorizonPosteriorReliabilityIsContinuousPrecisionShrinkage(t *testing.T) {
	prior := 6
	cases := []struct {
		samples float64
		want    float64
	}{
		{0, 0},
		{1, 1.0 / 7.0},
		{6, 0.5},
		{12, 2.0 / 3.0},
		{60, 10.0 / 11.0},
	}
	previous := -1.0
	for _, tc := range cases {
		got := HorizonPosteriorReliability(tc.samples, prior)
		if math.Abs(got-tc.want) > 1e-12 {
			t.Fatalf("samples=%v: got %v want %v", tc.samples, got, tc.want)
		}
		if got <= previous {
			t.Fatalf("precision shrinkage must increase continuously with evidence: %v <= %v", got, previous)
		}
		previous = got
	}
	if got := HorizonPosteriorReliability(math.Inf(1), prior); got != 0 {
		t.Fatalf("non-finite evidence must not receive weight: %v", got)
	}
}

func TestTargetProgressContinuationValueHasProximalOptimum(t *testing.T) {
	current, target, equity := 600.0, 500.0, 1_000.0
	gap := current - target
	optimalSell := gap * gap / equity
	value := func(sell float64) float64 {
		return TargetProgressContinuationValue(
			current, target, equity, 0, sell, 0, 1, 0)
	}
	if got, want := value(optimalSell), gap*gap*gap/(2*equity*equity); math.Abs(got-want) > 1e-12 {
		t.Fatalf("proximal Bellman value mismatch: got %v want %v", got, want)
	}
	if value(optimalSell*.5) >= value(optimalSell) ||
		value(optimalSell*1.5) >= value(optimalSell) {
		t.Fatalf("q*=gap^2/equity must maximize the one-sided continuation value")
	}
	if away := TargetProgressContinuationValue(
		current, target, equity, optimalSell, 0, 1, 0, 0); away >= 0 {
		t.Fatalf("moving away from target must have negative Bellman value: %v", away)
	}
}

func TestRiskReducingContinuationNetValueRequiresTargetProgressAfterCost(t *testing.T) {
	// A one-sided SELL from an overweight inventory has positive Bellman value
	// even when no completed terminal path is available, while the symmetric
	// BUY away from target must remain negative.  The fee term is charged only
	// on the side that actually touches.
	sell := RiskReducingContinuationNetValue(
		7_000, 3_500, 10_000,
		0, 100,
		0, 0.8, 0,
		12)
	if sell <= 0 {
		t.Fatalf("overweight SELL continuation should pay its expected one-side cost: %v", sell)
	}
	buy := RiskReducingContinuationNetValue(
		7_000, 3_500, 10_000,
		100, 0,
		0.8, 0, 0,
		12)
	if buy >= 0 {
		t.Fatalf("BUY away from target must not be admitted by missing-path prior: %v", buy)
	}
}

func TestTargetRestoringContinuationNotionalIsCostAdjustedBellmanOptimum(t *testing.T) {
	// For a 1,558 JPY target gap in a 6,829 JPY account, the one-way 12 bps
	// cost-adjusted optimum is materially larger than the 100 JPY exchange cell
	// but remains below the full gap.
	got := TargetRestoringContinuationNotional(4_972, 3_414, 6_829, 12)
	want := 1_558 * (1_558.0/6_829 - 12.0/10_000)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("continuation notional mismatch: got=%v want=%v", got, want)
	}
	if got <= 100 || got >= 1_558 {
		t.Fatalf("continuation optimum must be between venue minimum and full target gap: %v", got)
	}
	if got := TargetRestoringContinuationNotional(3_414, 4_972, 6_829, 12); got <= 0 {
		t.Fatalf("symmetric BUY target correction should remain positive: %v", got)
	}
	if got := TargetRestoringContinuationNotional(4_972, 4_972, 6_829, 12); got != 0 {
		t.Fatalf("zero target gap must not create an order: %v", got)
	}
}

func TestTargetRestoringTwoSidedContinuationKeepsMinimumOppositeQuote(t *testing.T) {
	buy, sell := TargetRestoringTwoSidedContinuationNotionals(
		4_972, 3_414, 6_829,
		0.28, 0.08, 0.02, 12,
		100, 100, 1_856, 4_950)
	if buy != 100 {
		t.Fatalf("overweight fallback must retain the minimum BUY cell: %v", buy)
	}
	if sell <= 100 || sell >= 4_950 {
		t.Fatalf("SELL must use the target-restoring continuation quantity: %v", sell)
	}
	buy, sell = TargetRestoringTwoSidedContinuationNotionals(
		3_414, 4_972, 6_829,
		0.08, 0.28, 0.02, 12,
		100, 100, 1_856, 4_950)
	if sell != 100 || buy <= 100 {
		t.Fatalf("underweight fallback must be symmetric: buy=%v sell=%v", buy, sell)
	}
}

func TestPosteriorRiskSizedSideCapCreditsTargetRestoringBuy(t *testing.T) {
	stats := JointPathPayoffStats{
		EffectiveSamples: 100,
		BuyDominant: jointPathPayoffMoments{
			BuyMeanBps: -1, BuyVarBps2: 100,
			InventoryVarBps2: 100, InventoryBuyCovBps2: 100,
		},
		SellDominant: jointPathPayoffMoments{
			BuyMeanBps: -1, BuyVarBps2: 100,
			InventoryVarBps2: 100, InventoryBuyCovBps2: 100,
		},
	}
	towardTarget := posteriorRiskSizedSideCap(
		stats, true, 250, 100, 200, 600, 1_000, 1_000, 0)
	absoluteExposure := posteriorRiskSizedSideCap(
		stats, true, 250, 100, 200, 0, 1_000, 1_000, 0)
	if towardTarget <= 100 {
		t.Fatalf("target-restoring BUY should receive more than one risk-funded cell: %v", towardTarget)
	}
	if absoluteExposure != 0 {
		t.Fatalf("the same negative-edge BUY away from a target must remain rejected: %v", absoluteExposure)
	}
}

func TestOptimizeJointDistanceQuantityProducesExecutableRiskProjection(t *testing.T) {
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		HorizonLookback:       types.Duration(40 * time.Minute),
		HorizonMinSamples:     3,
		JointDistanceQuantity: JointDistanceQuantityConfig{Enabled: true, CandidateCount: 5},
	}
	for second := 0; second <= 45*60; second++ {
		// One complete oscillation per holding horizon returns terminal wealth
		// to the start while touching both sides, so a fee-net cycle should have
		// a genuinely positive path-payoff lower bound.
		phase := 2 * math.Pi * float64(second) / float64(5*60)
		mid := 100 * math.Exp(0.006*math.Sin(phase))
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-2.0/20_000), mid*math.Exp(2.0/20_000), config)
	}
	now := start.Add(45 * time.Minute)
	bestBid, bestAsk, mid := 99.99, 100.01, 100.0
	base := MarketMakerQuotePlan{
		BidPrice: 99.65, AskPrice: 100.35,
		BidTouchDistanceBps: math.Log(bestAsk/99.65) * 10_000,
		AskTouchDistanceBps: math.Log(100.35/bestBid) * 10_000,
		AllowBid:            true, AllowAsk: true,
	}
	projection := ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 3500,
		TargetInventoryNotionalJPY:  3500,
		LowerInventoryNotionalJPY:   3000,
		UpperInventoryNotionalJPY:   4000,
		FastBuyNotionalJPY:          3000,
		FastSellNotionalJPY:         3000,
		MinBuyNotionalJPY:           100,
		MinSellNotionalJPY:          100,
		MaxBuyNotionalJPY:           3000,
		MaxSellNotionalJPY:          3000,
		Horizon:                     5 * time.Minute,
		ConfidenceZScore:            1.282,
	}
	decision := OptimizeJointDistanceQuantity(&model, config, JointDistanceQuantityInput{
		Now: now, Horizon: 5 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: base,
		ConfidenceZScore: 1.282,
		PairEquityJPY:    7000,
		RiskAversion:     1,
		Projection:       projection,
	})
	if !decision.Enabled || !decision.Applied {
		t.Fatalf("expected live joint decision, got %+v", decision)
	}
	if !decision.Projection.Enabled ||
		decision.Projection.ProjectedGrossNotionalJPY < 200 {
		t.Fatalf("expected two executable sides, got %+v", decision.Projection)
	}
	if decision.Crossing.Horizon != 5*time.Minute ||
		!decision.Crossing.HasSufficientCrossings(config.HorizonMinSamples) {
		t.Fatalf("selected crossing window is inconsistent: %+v", decision.Crossing)
	}
	if decision.KellyUtilityJPYHour <= 0 {
		t.Fatalf("promoted decision must have positive confidence-adjusted utility: %+v", decision)
	}
	preserved := applyFastPathAdmissions(
		&model, config, JointDistanceQuantityInput{
			Now: now, Horizon: 5 * time.Minute,
			BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid,
			BasePlan: base, Projection: projection,
			ConfidenceZScore: 1.282, PairEquityJPY: 7_000, RiskAversion: 1,
		}, FastPathDownsideCapDecision{}, decision)
	if !preserved.AdmissionJointComplementary || preserved.DownsideBuyCapApplied ||
		preserved.Projection.BuyNotionalJPY != decision.Projection.BuyNotionalJPY ||
		preserved.Projection.SellNotionalJPY != decision.Projection.SellNotionalJPY {
		t.Fatalf("one-sided cap broke an accepted complete Fast cycle: before=%+v after=%+v",
			decision, preserved)
	}

	// Joint covariance uncertainty blocks a two-sided multi-cell promotion.
	// A one-sided terminal-return posterior must not delete the independently
	// feasible probability-centered Fast baseline on the opposite side.
	input := JointDistanceQuantityInput{
		Now: now, Horizon: 5 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: base,
		ConfidenceZScore: 1.282, PairEquityJPY: 7000, RiskAversion: 1,
		Projection: projection,
	}
	baseline := projection
	baseline.MaxBuyNotionalJPY = 100
	baseline.MaxSellNotionalJPY = 100
	hierarchical := OptimizeUnifiedFastQuantity(&model, config, input, baseline)
	if !hierarchical.Applied ||
		hierarchical.Projection.BuyNotionalJPY <= 0 ||
		hierarchical.Projection.SellNotionalJPY <= 0 ||
		hierarchical.Projection.ProjectedGrossNotionalJPY <=
			baseline.MaxBuyNotionalJPY+baseline.MaxSellNotionalJPY {
		t.Fatalf("normal-confidence bilateral Fast floor must retain supported promotion: %+v",
			hierarchical)
	}

	// The production selector compares the final distance and quantity action
	// across horizons using one reliability-shrunk terminal-wealth objective.
	// There is no categorical health branch in this choice.
	config.JointDistanceQuantity.JointHorizonSelection = true
	input.HorizonCandidates = []time.Duration{5 * time.Minute, 10 * time.Minute}
	multi := OptimizeUnifiedFastQuantity(&model, config, input, baseline)
	if !multi.Applied || multi.HorizonCandidateCount != 2 ||
		(multi.Crossing.Horizon != 5*time.Minute && multi.Crossing.Horizon != 10*time.Minute) {
		t.Fatalf("expected a joint horizon-distance-quantity optimum: %+v", multi)
	}
	if multi.HorizonReliability <= 0 || multi.HorizonReliability >= 1 ||
		math.Abs(multi.HorizonSelectionUtilityJPYHour-
			multi.HorizonReliability*multi.HorizonRawUtilityJPYHour) > 1e-12 {
		t.Fatalf("joint horizon objective is not precision-shrunk terminal wealth: %+v", multi)
	}
}

func TestApplyFastPathDownsideCapPreservesPriceAndRecomputesRisk(t *testing.T) {
	input := JointDistanceQuantityInput{
		Now: time.Unix(1_000, 0), Horizon: 10 * time.Minute,
		BestBid: 99.9, BestAsk: 100.1, PairEquityJPY: 7_000, RiskAversion: 1,
		Projection: ProbabilityCenteredQuoteInput{
			CurrentInventoryNotionalJPY: 3_500,
			TargetInventoryNotionalJPY:  3_500,
			FastBuyNotionalJPY:          1_000, FastSellNotionalJPY: 1_000,
		},
	}
	plan := MarketMakerQuotePlan{
		BidPrice: 99.5, AskPrice: 100.5, AllowBid: true, AllowAsk: true,
	}
	decision := JointDistanceQuantityDecision{
		Enabled: true, Applied: true, Plan: plan,
		Crossing: MarketMakerHorizonDecision{
			BuyTouchProbability: 0.4, SellTouchProbability: 0.3,
		},
		Projection: ProbabilityCenteredQuoteDecision{
			Enabled:            true,
			BuyFillProbability: 0.4, SellFillProbability: 0.3,
			BothFillProbability: 0.1, FillCovariance: -0.02,
			BuyNotionalJPY: 600, SellNotionalJPY: 300,
			ProjectedGrossNotionalJPY:    900,
			ExpectedInventoryNotionalJPY: 3_650,
			TargetErrorJPY:               150,
		},
	}
	got := applyFastPathAdmissions(
		&MarketMakerHorizonModel{},
		MarketMakerConfig{InventoryRiskZScore: 1.282},
		input,
		FastPathDownsideCapDecision{
			Applied: true, MaximumBuyNotionalJPY: 100,
			OriginalMaximumBuyJPY: 1_000,
		},
		decision,
	)
	if got.Plan.BidPrice != plan.BidPrice || got.Plan.AskPrice != plan.AskPrice {
		t.Fatalf("downside size cap changed the Fast-selected price: %+v", got.Plan)
	}
	if !got.DownsideBuyCapApplied ||
		got.Projection.BuyNotionalJPY != 100 ||
		got.Projection.SellNotionalJPY != 300 ||
		got.Projection.ProjectedGrossNotionalJPY != 400 {
		t.Fatalf("unexpected post-selection quantity cap: %+v", got)
	}
	wantExpected := 3_500.0 + 0.4*100 - 0.3*300
	wantVariance := 0.4*0.6*100*100 + 0.3*0.7*300*300 -
		2*(-0.02)*100*300
	if math.Abs(got.Projection.ExpectedInventoryNotionalJPY-wantExpected) > 1e-12 ||
		math.Abs(got.Projection.InventoryVarianceJPY2-wantVariance) > 1e-12 {
		t.Fatalf("capped inventory moments were not recomputed: %+v", got.Projection)
	}
	if got.DownsideOriginalMaxBuyJPY != 1_000 ||
		got.Reason != "+terminal-downside BUY cap" {
		t.Fatalf("downside diagnostics do not describe the applied cap: %+v", got)
	}
}

func TestFastDownsideCapacityConstrainsBothSearchesBeforeOptimization(t *testing.T) {
	full := ProbabilityCenteredQuoteInput{MaxBuyNotionalJPY: 1_000, MaxSellNotionalJPY: 800}
	baseline := ProbabilityCenteredQuoteInput{MaxBuyNotionalJPY: 300, MaxSellNotionalJPY: 200}
	gotFull, gotBaseline := fastDownsideConstrainedInputs(
		full, baseline, FastPathDownsideCapDecision{Applied: true, MaximumBuyNotionalJPY: 100})
	if gotFull.MaxBuyNotionalJPY != 100 || gotBaseline.MaxBuyNotionalJPY != 100 {
		t.Fatalf("downside capacity was not shared by both candidate searches: full=%+v baseline=%+v",
			gotFull, gotBaseline)
	}
	if gotFull.MaxSellNotionalJPY != full.MaxSellNotionalJPY ||
		gotBaseline.MaxSellNotionalJPY != baseline.MaxSellNotionalJPY {
		t.Fatalf("BUY downside capacity changed SELL feasibility: full=%+v baseline=%+v",
			gotFull, gotBaseline)
	}
	unchangedFull, unchangedBaseline := fastDownsideConstrainedInputs(
		full, baseline, FastPathDownsideCapDecision{})
	if unchangedFull.MaxBuyNotionalJPY != full.MaxBuyNotionalJPY ||
		unchangedBaseline.MaxBuyNotionalJPY != baseline.MaxBuyNotionalJPY {
		t.Fatalf("inactive downside posterior changed candidate capacity")
	}
}

func TestOptimizeJointDistanceQuantityRejectsZeroGrossProjection(t *testing.T) {
	config := MarketMakerConfig{
		HorizonLookback:   types.Duration(time.Hour),
		HorizonMinSamples: 2,
		MakerFeeBps:       10,
		MinimumNetEdgeBps: 1,
		JointDistanceQuantity: JointDistanceQuantityConfig{
			Enabled:        true,
			CandidateCount: 3,
		},
	}
	decision := OptimizeJointDistanceQuantity(
		&MarketMakerHorizonModel{}, config, JointDistanceQuantityInput{
			Now:           time.Unix(1_000, 0),
			Horizon:       5 * time.Minute,
			BestBid:       99,
			BestAsk:       101,
			MidPrice:      100,
			PairEquityJPY: 10_000,
			BasePlan: MarketMakerQuotePlan{
				BidPrice: 98, AskPrice: 102,
			},
			Projection: ProbabilityCenteredQuoteInput{
				FastBuyNotionalJPY:  100,
				FastSellNotionalJPY: 100,
			},
		})
	if decision.Enabled || decision.Applied {
		t.Fatalf("zero-cap projection must not be executable: %+v", decision)
	}
}

func TestOptimizeJointDistanceQuantityMakesSupportedTrendRejectionAuthoritative(t *testing.T) {
	start := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		HorizonLookback:       types.Duration(50 * time.Minute),
		HorizonMinSamples:     3,
		JointDistanceQuantity: JointDistanceQuantityConfig{Enabled: true, CandidateCount: 7},
	}
	for second := 0; second <= 60*60; second++ {
		// A persistent rise makes SELL touches adversely selected while every
		// untouched BUY contributes zero terminal wealth. Sufficient completed
		// paths must reject the base quote authoritatively rather than treating
		// the absence of a positive candidate as missing data.
		mid := 100 * math.Exp(float64(second)*2.0/60.0/10_000)
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	bestBid, bestAsk, mid := 112.74, 112.76, 112.75
	base := MarketMakerQuotePlan{
		BidPrice: bestAsk * math.Exp(-30.0/10_000),
		AskPrice: bestBid * math.Exp(30.0/10_000),
		AllowBid: true, AllowAsk: true,
	}
	base.BidTouchDistanceBps, base.AskTouchDistanceBps, _ =
		MakerTouchDistances(bestBid, bestAsk, base.BidPrice, base.AskPrice)
	decision := OptimizeJointDistanceQuantity(&model, config, JointDistanceQuantityInput{
		Now: start.Add(time.Hour), Horizon: 10 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: base,
		ConfidenceZScore: 1.282, PairEquityJPY: 7_000, RiskAversion: 1,
		Projection: ProbabilityCenteredQuoteInput{
			CurrentInventoryNotionalJPY: 3_500,
			TargetInventoryNotionalJPY:  3_500,
			LowerInventoryNotionalJPY:   2_000,
			UpperInventoryNotionalJPY:   5_000,
			FastBuyNotionalJPY:          1_000,
			FastSellNotionalJPY:         1_000,
			MinBuyNotionalJPY:           100,
			MinSellNotionalJPY:          100,
			MaxBuyNotionalJPY:           1_000,
			MaxSellNotionalJPY:          1_000,
			Horizon:                     10 * time.Minute,
			ConfidenceZScore:            1.282,
		},
	})
	if decision.Enabled || decision.Applied || !decision.AuthoritativeRejection {
		t.Fatalf("supported adverse trend must block the inward base fallback: %+v", decision)
	}

	// The unified controller must preserve the authoritative no-order action.
	// Zero terminal value does not pay fees or opportunity cost and therefore
	// cannot be resurrected as a probability-only sampling order.
	projection := ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 3_000,
		TargetInventoryNotionalJPY:  3_500,
		LowerInventoryNotionalJPY:   2_000,
		UpperInventoryNotionalJPY:   5_000,
		FastBuyNotionalJPY:          1_000,
		FastSellNotionalJPY:         1_000,
		MinBuyNotionalJPY:           100,
		MinSellNotionalJPY:          100,
		MaxBuyNotionalJPY:           1_000,
		MaxSellNotionalJPY:          1_000,
		BuyFillRatePerHour:          2,
		SellFillRatePerHour:         2,
		Horizon:                     10 * time.Minute,
		ConfidenceZScore:            1.282,
	}
	baseline := projection
	baseline.MaxBuyNotionalJPY = 100
	baseline.MaxSellNotionalJPY = 100
	unified := OptimizeUnifiedFastQuantity(&model, config, JointDistanceQuantityInput{
		Now: start.Add(time.Hour), Horizon: 10 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: base,
		ConfidenceZScore: 1.282, PairEquityJPY: 7_000, RiskAversion: 1,
		Projection: projection,
	}, baseline)
	if unified.Enabled || unified.Applied || !unified.AuthoritativeRejection ||
		unified.Projection.BuyNotionalJPY != 0 || unified.Projection.SellNotionalJPY != 0 {
		t.Fatalf("non-positive fee-net Fast value must remain rejected: %+v", unified)
	}

	// The identical adverse terminal posterior must not turn a full-base account
	// into an absorbing state. With no quote balance, a target-restoring passive
	// SELL moves toward target and unlocks the next BUY; the complete sequential
	// spread still has to pay both fees and the configured residual edge.
	boundaryPlan := base
	boundaryPlan.BidPrice = bestAsk * math.Exp(-15.0/10_000)
	boundaryPlan.AskPrice = bestBid * math.Exp(15.0/10_000)
	boundaryPlan.AllowBid = false
	boundaryProjection := projection
	boundaryProjection.CurrentInventoryNotionalJPY = 6_000
	boundaryProjection.TargetInventoryNotionalJPY = 3_500
	boundaryProjection.UpperInventoryNotionalJPY = 7_000
	boundaryProjection.FastBuyNotionalJPY = 0
	boundaryProjection.MinBuyNotionalJPY = 100
	boundaryProjection.MaxBuyNotionalJPY = 0
	boundaryProjection.FastSellNotionalJPY = 100
	boundaryProjection.MinSellNotionalJPY = 100
	boundaryProjection.MaxSellNotionalJPY = 1_000
	boundary := OptimizeUnifiedFastQuantity(&model, config, JointDistanceQuantityInput{
		Now: start.Add(time.Hour), Horizon: 10 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: boundaryPlan,
		ConfidenceZScore: 1.282, PairEquityJPY: 7_000, RiskAversion: 1,
		AvailableBuyCapitalJPY: 0, AvailableSellInventoryNotionalJPY: 6_000,
		Projection: boundaryProjection,
	}, boundaryProjection)
	wantBoundarySell := TargetRestoringContinuationNotional(
		6_000, 3_500, 7_000, config.MakerFeeBps+config.AdverseSelectionBps)
	if !boundary.Enabled || !boundary.Applied || !boundary.SideSafeFallback ||
		boundary.Plan.AllowBid || !boundary.Plan.AllowAsk ||
		math.Abs(boundary.Projection.SellNotionalJPY-wantBoundarySell) > 1e-9 ||
		!boundary.RiskReducing ||
		boundary.Reason != "target-restoring account-boundary continuation control" {
		t.Fatalf("target-restoring balance boundary was not kept viable: %+v", boundary)
	}

	// minimumNetEdge is the reservation profit of a completed oscillation, not
	// a cost of reducing inventory risk.  Raising that cycle hurdle above the
	// entire quoted spread must not suppress a one-sided target-restoring SELL
	// whose Bellman continuation value pays its own maker/adverse cost.
	rebalanceConfig := config
	rebalanceConfig.MinimumNetEdgeBps = 1_000
	rebalance := OptimizeUnifiedFastQuantity(&model, rebalanceConfig, JointDistanceQuantityInput{
		Now: start.Add(time.Hour), Horizon: 10 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: boundaryPlan,
		ConfidenceZScore: 1.282, PairEquityJPY: 7_000, RiskAversion: 1,
		AvailableBuyCapitalJPY: 0, AvailableSellInventoryNotionalJPY: 6_000,
		Projection: boundaryProjection,
	}, boundaryProjection)
	if !rebalance.Enabled || !rebalance.Applied || rebalance.Plan.AllowBid ||
		!rebalance.Plan.AllowAsk || !rebalance.RiskReducing ||
		rebalance.Projection.SellNotionalJPY <= 100 {
		t.Fatalf("cycle profit hurdle leaked into target-restoring SELL: %+v", rebalance)
	}

	// A policy-disabled BUY is not a balance boundary when both sides retain
	// exchange capacity. The terminal optimizer already rejected promotion, so
	// this path may preserve one cell but must not turn the policy decision into
	// a target-gap-sized SELL.
	policyProjection := boundaryProjection
	policyProjection.MinBuyNotionalJPY = 100
	policyProjection.MaxBuyNotionalJPY = 1_000
	policy := OptimizeUnifiedFastQuantity(&model, config, JointDistanceQuantityInput{
		Now: start.Add(time.Hour), Horizon: 10 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: boundaryPlan,
		ConfidenceZScore: 1.282, PairEquityJPY: 7_000, RiskAversion: 1,
		AvailableBuyCapitalJPY: 1_000, AvailableSellInventoryNotionalJPY: 6_000,
		Projection: policyProjection,
	}, policyProjection)
	if !policy.Applied || math.Abs(policy.Projection.SellNotionalJPY-100) > 1e-9 ||
		policy.Reason != "target-restoring one-sided policy continuation floor" {
		t.Fatalf("policy-only one-sided continuation must remain one cell: %+v", policy)
	}

	// After a partial boundary fill both balances become executable.  A large
	// remaining target error must not turn that interior account state into an
	// absorbing no-order state.  The same scale-free proximal control supplies
	// one target-restoring side, while its endogenous exchange-minimum threshold
	// leaves the small-error authoritative rejection above unchanged.
	interiorPlan := base
	interiorProjection := projection
	interiorProjection.CurrentInventoryNotionalJPY = 5_200
	interiorProjection.TargetInventoryNotionalJPY = 4_000
	interiorProjection.LowerInventoryNotionalJPY = 0
	interiorProjection.UpperInventoryNotionalJPY = 7_000
	interiorProjection.FastBuyNotionalJPY = 1_000
	interiorProjection.FastSellNotionalJPY = 1_000
	interiorProjection.MinBuyNotionalJPY = 100
	interiorProjection.MinSellNotionalJPY = 100
	interiorProjection.MaxBuyNotionalJPY = 1_800
	interiorProjection.MaxSellNotionalJPY = 5_200
	interior := OptimizeUnifiedFastQuantity(&model, config, JointDistanceQuantityInput{
		Now: start.Add(time.Hour), Horizon: 10 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: interiorPlan,
		ConfidenceZScore: 1.282, PairEquityJPY: 7_000, RiskAversion: 1,
		AvailableBuyCapitalJPY: 1_800, AvailableSellInventoryNotionalJPY: 5_200,
		Projection: interiorProjection,
	}, interiorProjection)
	wantInteriorSell := TargetRestoringContinuationNotional(
		5_200, 4_000, 7_000, config.MakerFeeBps+config.AdverseSelectionBps)
	if !interior.Enabled || !interior.Applied || !interior.SideSafeFallback ||
		interior.Plan.AllowBid || !interior.Plan.AllowAsk ||
		math.Abs(interior.Projection.SellNotionalJPY-wantInteriorSell) > 1e-9 ||
		!interior.RiskReducing ||
		interior.Reason != "target-restoring interior proximal continuation control" {
		t.Fatalf("interior target-restoring continuation was not kept viable: %+v", interior)
	}
}

func TestUnifiedFastQuantityPreservesBoundedTwoSidedQuoteAfterTerminalRejection(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		HorizonLookback: types.Duration(50 * time.Minute), HorizonMinSamples: 3,
		JointDistanceQuantity: JointDistanceQuantityConfig{
			Enabled: true, CandidateCount: 7, PreserveTwoSidedQuotes: true,
		},
	}
	for second := 0; second <= 60*60; second++ {
		// The monotone path supplies enough BBO observations for the crossing
		// posterior but a negative terminal markout for the ordinary pair.
		mid := 100 * math.Exp(float64(second)*2.0/60.0/10_000)
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	bestBid, bestAsk, mid := 112.74, 112.76, 112.75
	base := MarketMakerQuotePlan{
		BidPrice: bestAsk * math.Exp(-30.0/10_000),
		AskPrice: bestBid * math.Exp(30.0/10_000), AllowBid: true, AllowAsk: true,
	}
	base.BidTouchDistanceBps, base.AskTouchDistanceBps, _ =
		MakerTouchDistances(bestBid, bestAsk, base.BidPrice, base.AskPrice)
	projection := ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 5_000, TargetInventoryNotionalJPY: 3_500,
		LowerInventoryNotionalJPY: 2_000, UpperInventoryNotionalJPY: 7_000,
		FastBuyNotionalJPY: 1_000, FastSellNotionalJPY: 1_000,
		MinBuyNotionalJPY: 100, MinSellNotionalJPY: 100,
		MaxBuyNotionalJPY: 1_000, MaxSellNotionalJPY: 1_000,
		Horizon: 10 * time.Minute, ConfidenceZScore: 1.282,
	}
	input := JointDistanceQuantityInput{
		Now: start.Add(time.Hour), Horizon: 10 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: base,
		ConfidenceZScore: 1.282, PairEquityJPY: 7_000, RiskAversion: 1,
		Projection: projection,
	}
	decision := OptimizeUnifiedFastQuantity(&model, config, input, projection)
	if !decision.Applied || !decision.ContinuityFloorApplied || decision.AuthoritativeRejection ||
		!decision.Plan.AllowBid || !decision.Plan.AllowAsk ||
		decision.Projection.BuyNotionalJPY < projection.MinBuyNotionalJPY ||
		decision.Projection.SellNotionalJPY < projection.MinSellNotionalJPY {
		t.Fatalf("continuity floor did not preserve both executable sides: %+v", decision)
	}
	if decision.Projection.SellNotionalJPY <= projection.MinSellNotionalJPY {
		t.Fatalf("target-restoring side should use more than one minimum cell: %+v", decision.Projection)
	}
	if decision.LowerPnLJPYHour != 0 || decision.KellyUtilityJPYHour <= 0 {
		t.Fatalf("continuity floor must not claim a terminal lower bound: %+v", decision)
	}
	if decision.PathEffectiveSamples <= 1 {
		t.Fatalf("terminal rejection fallback lost evaluated path samples: %+v", decision)
	}
	if decision.CandidateCount < config.JointDistanceQuantity.CandidateCount {
		t.Fatalf("terminal rejection fallback lost evaluated candidate count: %+v", decision)
	}
}

func TestRetainJointEvaluationDiagnosticsDoesNotRewriteBaselineDecision(t *testing.T) {
	selected := JointDistanceQuantityDecision{
		Enabled: true, Applied: true,
		Reason: "probability-only executable Fast baseline preserved",
		Projection: ProbabilityCenteredQuoteDecision{
			Enabled: true, BuyNotionalJPY: 100, SellNotionalJPY: 100,
		},
	}
	evaluated := JointDistanceQuantityDecision{
		Reason:         "terminal candidate rejected",
		CandidateCount: 13, PathEffectiveSamples: 7.5,
		Projection: ProbabilityCenteredQuoteDecision{
			Enabled: true, BuyNotionalJPY: 400, SellNotionalJPY: 0,
		},
	}
	got := retainJointEvaluationDiagnostics(selected, evaluated)
	if got.CandidateCount != 13 || got.PathEffectiveSamples != 7.5 {
		t.Fatalf("evaluated diagnostics were not retained: %+v", got)
	}
	if got.Reason != selected.Reason ||
		got.Projection.BuyNotionalJPY != selected.Projection.BuyNotionalJPY ||
		got.Projection.SellNotionalJPY != selected.Projection.SellNotionalJPY {
		t.Fatalf("diagnostic propagation rewrote the selected baseline: %+v", got)
	}
}
