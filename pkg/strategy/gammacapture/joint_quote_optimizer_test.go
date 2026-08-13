package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

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
	if got := posteriorRiskSizedSideCap(strong, true, 250, 100, 0, 10_000, 1, 1.282); got <= 100 || got > 250 {
		t.Fatalf("strong BUY evidence must use more than one cell without exceeding current hard cap: %v", got)
	}
	if got := posteriorRiskSizedSideCap(strong, false, 250, 100, 0, 10_000, 1, 1.282); got != 0 {
		t.Fatalf("negative SELL posterior must receive no risk capacity: %v", got)
	}
	if got := posteriorRiskSizedSideCap(strong, true, 250, 100, 0, 10_000, 1, 1e6); got != 0 {
		t.Fatalf("a posterior whose confidence lower bound is negative must receive no promoted capacity: %v", got)
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
}

func TestApplyFastPathDownsideCapPreservesPriceAndRecomputesRisk(t *testing.T) {
	input := JointDistanceQuantityInput{
		Now: time.Unix(1_000, 0), Horizon: 10 * time.Minute,
		BestBid: 99.9, BestAsk: 100.1, PairEquityJPY: 7_000, RiskAversion: 1,
		Projection: ProbabilityCenteredQuoteInput{
			CurrentInventoryNotionalJPY: 3_500,
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

	// The lower-level path optimizer remains authoritative for promotion. At the
	// final fallback distance this deterministic fixture has zero SELL payoff
	// and zero uncertainty rather than a strictly negative upper bound. The
	// unified Fast controller must therefore preserve both sides: absence of a
	// touched payoff is not evidence that SELL is harmful.
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
	if !unified.Enabled || !unified.Applied ||
		unified.Projection.BuyNotionalJPY <= 0 ||
		unified.Projection.SellNotionalJPY <= 0 ||
		unified.SellAdmissionApplied || unified.BuyAdmissionApplied ||
		!unified.SellAdmissionEvaluated ||
		unified.SellAdmissionUtilityBoundJPY != 0 ||
		!unified.Plan.AllowBid || !unified.Plan.AllowAsk {
		t.Fatalf("zero SELL evidence at final price must preserve both sides: %+v", unified)
	}
	if unified.AuthoritativeRejection {
		t.Fatalf("soft path rejection must not become a hard inventory gate: %+v", unified)
	}
}
