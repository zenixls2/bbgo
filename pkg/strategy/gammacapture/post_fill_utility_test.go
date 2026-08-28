package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func postFillUtilityTrendModel(up bool) (MarketMakerHorizonModel, time.Time, float64, float64) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	trend := 3.0
	if !up {
		trend = -3
	}
	for i := 0; i <= 7*60; i++ {
		pathBps := trend*float64(i) + 30*math.Sin(2*math.Pi*float64(i)/10)
		mid := 100 * math.Exp(pathBps/10_000)
		model.points = append(model.points, MarketMakerHorizonPoint{
			At:  start.Add(time.Duration(i) * time.Minute),
			Bid: mid * math.Exp(-0.5/10_000), Ask: mid * math.Exp(0.5/10_000), Mid: mid,
		})
	}
	last := model.points[len(model.points)-1]
	return model, last.At, last.Bid, last.Ask
}

func TestPostFillUtilityAllowsStatisticallySupportedNegativeCycleEdge(t *testing.T) {
	model, now, bid, ask := postFillUtilityTrendModel(true)
	mid := (bid + ask) / 2
	plan := MarketMakerQuotePlan{
		Reason: "quoted", AllowBid: true, AllowAsk: true,
		BidPrice: ask * math.Exp(-45.0/10_000), AskPrice: bid * math.Exp(45.0/10_000),
		BidTouchDistanceBps: 45, AskTouchDistanceBps: 45,
	}
	cfg := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		HorizonMinSamples: 6,
		PostFillUtility:   PostFillUtilityConfig{Enabled: true, MinimumSamples: 6, ConfidenceZScore: 1.0, CandidateCount: 6},
	}
	decision := cfg.ApplyPostFillUtility(&model, PostFillUtilityInput{
		Now: now, Fill: MakerPostFillState{Side: types.SideTypeSell, Price: plan.BidPrice * 0.99, Quantity: 1, At: now.Add(-time.Second)},
		Plan: plan, BestBid: bid, BestAsk: ask, Mid: mid, Horizon: 10 * time.Minute,
		InventoryBase: 50, InventoryTargetBase: 50, PairEquityJPY: 10_000,
		ExpectedFillNotionalJPY: 100, VolatilityBpsPerSqrtSec: 1, RiskAversion: 1,
	})
	if !decision.Applied {
		t.Fatalf("expected rising-path post-fill BUY utility to spend edge: %+v", decision)
	}
	if decision.SelectedDistanceBps >= decision.BaseDistanceBps {
		t.Fatalf("expected inward BUY price: %+v", decision)
	}
	if decision.CycleEdgeBps >= 0 {
		t.Fatalf("last fill must not become a hard positive-cycle gate: %+v", decision)
	}
	if decision.IncrementalLowerBps <= 0 {
		t.Fatalf("applied decision needs positive paired lower bound: %+v", decision)
	}
}

func TestPostFillUtilityRejectsAdverseBuyAfterTouch(t *testing.T) {
	model, now, bid, ask := postFillUtilityTrendModel(false)
	mid := (bid + ask) / 2
	plan := MarketMakerQuotePlan{
		Reason: "quoted", AllowBid: true, AllowAsk: true,
		BidPrice: ask * math.Exp(-45.0/10_000), AskPrice: bid * math.Exp(45.0/10_000),
		BidTouchDistanceBps: 45, AskTouchDistanceBps: 45,
	}
	cfg := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		HorizonMinSamples: 6,
		PostFillUtility:   PostFillUtilityConfig{Enabled: true, MinimumSamples: 6, ConfidenceZScore: 1.0, CandidateCount: 6},
	}
	decision := cfg.ApplyPostFillUtility(&model, PostFillUtilityInput{
		Now: now, Fill: MakerPostFillState{Side: types.SideTypeSell, Price: ask, Quantity: 1, At: now.Add(-time.Second)},
		Plan: plan, BestBid: bid, BestAsk: ask, Mid: mid, Horizon: 10 * time.Minute,
		InventoryBase: 50, InventoryTargetBase: 50, PairEquityJPY: 10_000,
		ExpectedFillNotionalJPY: 100, VolatilityBpsPerSqrtSec: 1, RiskAversion: 1,
	})
	if decision.Applied {
		t.Fatalf("continued downside after BUY touch must not be chased: %+v", decision)
	}
	if decision.SelectedPrice <= 0 || decision.CycleEdgeBps == 0 {
		t.Fatalf("rejected candidate must retain price and cycle-edge diagnostics: %+v", decision)
	}
}

func TestPostFillInventoryRiskBenefitChangesSignAtTarget(t *testing.T) {
	base := PostFillUtilityInput{
		Mid: 100, PairEquityJPY: 10_000, ExpectedFillNotionalJPY: 1_000,
		Horizon: 10 * time.Minute, VolatilityBpsPerSqrtSec: 1, RiskAversion: 1,
		InventoryTargetBase: 50,
	}
	base.InventoryBase = 40
	if got := postFillInventoryRiskBenefitBps(types.SideTypeBuy, base); got <= 0 {
		t.Fatalf("buy toward target should reduce inventory risk, got %f", got)
	}
	base.InventoryBase = 60
	if got := postFillInventoryRiskBenefitBps(types.SideTypeBuy, base); got >= 0 {
		t.Fatalf("buy away from target should increase inventory risk, got %f", got)
	}
}

func TestPostFillInventoryRiskBenefitUsesMarginalExecutableFill(t *testing.T) {
	in := PostFillUtilityInput{
		Mid: 298_000, PairEquityJPY: 6_835, ExpectedFillNotionalJPY: 102.6,
		Horizon: 30 * time.Minute, VolatilityBpsPerSqrtSec: 0.4, RiskAversion: 1,
		InventoryBase: 0.01092201, InventoryTargetBase: 0.5 * 6_835 / 298_000,
	}
	if got := postFillInventoryRiskBenefitBps(types.SideTypeBuy, in); got <= 0 {
		t.Fatalf("one executable BUY toward the target must have positive risk benefit, got %f", got)
	}
	// The old call site passed the entire dynamic risk budget. Here that is
	// larger than pair equity, clips the hypothetical fill to 100% weight, and
	// gives the opposite answer despite the actual order being one small cell.
	in.ExpectedFillNotionalJPY = 8_939
	if got := postFillInventoryRiskBenefitBps(types.SideTypeBuy, in); got >= 0 {
		t.Fatalf("oversized whole-budget hypothetical should expose the old sign reversal, got %f", got)
	}
}

func TestPostFillExposureCacheMatchesReferencePointScan(t *testing.T) {
	distances := []float64{45, 39, 33, 27, 21, 15}
	for _, upward := range []bool{false, true} {
		model, end, _, _ := postFillUtilityTrendModel(upward)
		for _, side := range []types.SideType{types.SideTypeBuy, types.SideTypeSell} {
			for _, offset := range []time.Duration{0, -37 * time.Second, -4 * time.Minute} {
				now := end.Add(offset)
				got := model.postFillUtilityCandidates(
					now, 6*time.Hour, 10*time.Minute, side, distances, 12, 1.25, 1.645)
				want := model.postFillUtilityCandidatesReference(
					now, 6*time.Hour, 10*time.Minute, side, distances, 12, 1.25, 1.645)
				if len(got) != len(want) {
					t.Fatalf("candidate count differs: got=%d want=%d", len(got), len(want))
				}
				for index := range want {
					actual := []float64{
						got[index].DistanceBps, got[index].IncrementalMeanBps,
						got[index].IncrementalStdErrorBps, got[index].IncrementalLowerBps,
						got[index].ExpectedMeanBps, got[index].ExpectedStdErrorBps,
						got[index].ExpectedLowerBps, got[index].FillProbability,
						got[index].EffectiveSamples,
					}
					expected := []float64{
						want[index].DistanceBps, want[index].IncrementalMeanBps,
						want[index].IncrementalStdErrorBps, want[index].IncrementalLowerBps,
						want[index].ExpectedMeanBps, want[index].ExpectedStdErrorBps,
						want[index].ExpectedLowerBps, want[index].FillProbability,
						want[index].EffectiveSamples,
					}
					for field := range expected {
						if math.Abs(actual[field]-expected[field]) > 1e-10 {
							t.Fatalf("cached/reference mismatch up=%t side=%s offset=%s candidate=%d field=%d got=%.12f want=%.12f", upward, side, offset, index, field, actual[field], expected[field])
						}
					}
				}
			}
		}
	}
}
