package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestJointPathPayoffPenalizesSellingIntoContinuation(t *testing.T) {
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		HorizonLookback: types.Duration(50 * time.Minute),
	}
	for second := 0; second <= 60*60; second++ {
		mid := 100 * math.Exp(float64(second)*2.0/60.0/10_000)
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	stats := model.JointPathPayoffStatistics(
		start.Add(time.Hour), config, 10*time.Minute, 5, 5)
	if stats.EffectiveSamples < 2 {
		t.Fatalf("insufficient path samples: %+v", stats)
	}
	if stats.SellDominant.SellMeanBps >= 0 {
		t.Fatalf("continued rise must make passive SELL terminal wealth adverse: %+v", stats)
	}
	if math.Abs(stats.BuyDominant.BuyMeanBps) > 1e-9 {
		t.Fatalf("untouched BUY should contribute zero payoff: %+v", stats)
	}
}

func TestJointPathPayoffRewardsCompletedOscillation(t *testing.T) {
	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	model := MarketMakerHorizonModel{}
	config := MarketMakerConfig{
		MakerFeeBps: 10, AdverseSelectionBps: 2, MinimumNetEdgeBps: 2,
		HorizonLookback: types.Duration(40 * time.Minute),
	}
	horizon := 5 * time.Minute
	for second := 0; second <= 45*60; second++ {
		phase := 2 * math.Pi * float64(second) / horizon.Seconds()
		mid := 100 * math.Exp(0.006*math.Sin(phase))
		model.ObserveBook(start.Add(time.Duration(second)*time.Second),
			mid*math.Exp(-1.0/20_000), mid*math.Exp(1.0/20_000), config)
	}
	stats := model.JointPathPayoffStatistics(
		start.Add(45*time.Minute), config, horizon, 30, 30)
	decision := stats.Evaluate(500, 500, 7000, 1, 1.282)
	if stats.EffectiveSamples < 3 {
		t.Fatalf("insufficient path samples: %+v", stats)
	}
	if decision.ExpectedPnLJPY <= 0 || decision.CertaintyEquivalent <= 0 {
		t.Fatalf("completed fee-net oscillation should have positive wealth utility: stats=%+v decision=%+v", stats, decision)
	}
}
