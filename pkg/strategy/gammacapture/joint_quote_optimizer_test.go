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

func TestOptimizeJointDistanceQuantityProducesExecutableRiskProjection(t *testing.T) {
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		MinimumHalfSpreadBps: 15, MaximumHalfSpreadBps: 80,
		HorizonLookback:       types.Duration(40 * time.Minute),
		HorizonMinSamples:     6,
		JointDistanceQuantity: JointDistanceQuantityConfig{Enabled: true, CandidateCount: 5},
	}
	for second := 0; second <= 45*60; second++ {
		phase := 2 * math.Pi * float64(second) / float64(8*60)
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
	decision := OptimizeJointDistanceQuantity(&model, config, JointDistanceQuantityInput{
		Now: now, Horizon: 5 * time.Minute,
		BestBid: bestBid, BestAsk: bestAsk, MidPrice: mid, BasePlan: base,
		ConfidenceZScore: 1.282,
		Projection: ProbabilityCenteredQuoteInput{
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
		},
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
	if decision.LowerPnLJPYHour <= 0 {
		t.Fatalf("promoted decision must have positive confidence-adjusted pnl: %+v", decision)
	}
}
