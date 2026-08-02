package gammacapture

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestInventoryResetWaitsForStaleAskAndAdverseMove(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 20, 0, 0, time.UTC)
	cfg := InventoryResetConfig{
		Enabled: true, MaxAskAge: types.Duration(10 * time.Minute), AdverseMoveBps: 75,
		MaxSlippageBps: 25, FillIntensityHaircut: 0.25, RiskZScore: 1.645,
	}
	base := InventoryResetInput{
		Now: now, AskSince: now.Add(-12 * time.Minute), AnchorMidPrice: 100,
		MidPrice: 99.1, BestBid: 99, AskPrice: 99.2, MakerFeeBps: 7.5,
		TakerFeeBps: 7.5, FillIntensity: 0.00001, FillIntensityValid: true, VolatilityPerSqrtSec: 0.0001,
		AverageCost: 98,
	}

	decision := cfg.Evaluate(base)
	if !decision.Trigger {
		t.Fatalf("expected reset trigger, got reason=%s wait=%.2f ioc=%.2f", decision.Reason, decision.WaitValueBps, decision.IOCValueBps)
	}

	base.Now = now.Add(-1 * time.Minute)
	base.AskSince = base.Now.Add(-9 * time.Minute)
	if got := cfg.Evaluate(base); got.Trigger {
		t.Fatalf("triggered before max ask age: %+v", got)
	}

	base.Now = now
	base.AskSince = now.Add(-9 * time.Minute)
	base.MidPrice = 99.5
	if got := cfg.Evaluate(base); got.Trigger {
		t.Fatalf("triggered before adverse move threshold: %+v", got)
	}
}

func TestInventoryResetKeepsPassiveAskWhenFillValueIsHigher(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 20, 0, 0, time.UTC)
	cfg := InventoryResetConfig{
		Enabled: true, MaxAskAge: types.Duration(10 * time.Minute), AdverseMoveBps: 75,
		MaxSlippageBps: 25, FillIntensityHaircut: 1, RiskZScore: 0,
	}
	decision := cfg.Evaluate(InventoryResetInput{
		Now: now, AskSince: now.Add(-12 * time.Minute), AnchorMidPrice: 100,
		MidPrice: 99.2, BestBid: 99, AskPrice: 100.5, MakerFeeBps: 1,
		TakerFeeBps: 10, FillIntensityHaircut: 1, FillIntensity: 1, FillIntensityValid: true, VolatilityPerSqrtSec: 0,
		AverageCost: 99,
	})
	if decision.Trigger {
		t.Fatalf("expected passive ask to win, got %+v", decision)
	}
}

func TestInventoryResetFastDownsideTrigger(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 20, 0, 0, time.UTC)
	cfg := InventoryResetConfig{
		Enabled: true, MaxAskAge: types.Duration(10 * time.Minute), FastAskAge: types.Duration(time.Minute),
		AdverseMoveBps: 75, FastAdverseMoveBps: 20, FastDirectionThreshold: 0.25,
		MaxSlippageBps: 25, FillIntensityHaircut: 0.25, RiskZScore: 1.645,
	}
	decision := cfg.Evaluate(InventoryResetInput{
		Now: now, AskSince: now.Add(-90 * time.Second), AnchorMidPrice: 100,
		MidPrice: 99.7, BestBid: 99.6, AskPrice: 99.8, MakerFeeBps: 7.5,
		TakerFeeBps: 7.5, FillIntensity: 0.00001, FillIntensityValid: true, FillIntensityHaircut: 0.25,
		VolatilityPerSqrtSec: 0.0001, FastDirectionSignal: -0.8,
		FastSignalHealthy: true, AverageCost: 98,
	})
	if !decision.Trigger {
		t.Fatalf("expected fast downside reset trigger, got reason=%s wait=%.2f ioc=%.2f", decision.Reason, decision.WaitValueBps, decision.IOCValueBps)
	}
}

func TestInventoryResetRejectsFeeNegativeRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 29, 1, 23, 18, 0, time.FixedZone("JST", 9*60*60))
	cfg := InventoryResetConfig{
		Enabled: true, MaxAskAge: types.Duration(3 * time.Minute), FastAskAge: types.Duration(time.Minute),
		AdverseMoveBps: 40, FastAdverseMoveBps: 20, FastDirectionThreshold: 0.25,
		MaxSlippageBps: 25, FillIntensityHaircut: 0.25, RiskZScore: 1.645,
		MinimumRoundTripValueBps: 2, MinimumImprovementBps: 2,
	}
	decision := cfg.Evaluate(InventoryResetInput{
		Now: now, AskSince: now.Add(-102 * time.Second), AnchorMidPrice: 12159,
		MidPrice: 12134, BestBid: 12121, AskPrice: 12191,
		MakerFeeBps: 10, TakerFeeBps: 10, FillIntensity: 0.0045, FillIntensityValid: true,
		FillIntensityHaircut: 0.25, VolatilityPerSqrtSec: 0.00001,
		FastDirectionSignal: -0.5, FastSignalHealthy: true,
		AverageCost: 12144 / 0.999,
	})
	if decision.Trigger {
		t.Fatalf("fee-negative round trip must not reset: %+v", decision)
	}
	if decision.Reason != "ioc round-trip value below minimum" {
		t.Fatalf("unexpected rejection reason: %+v", decision)
	}
	if decision.ExpectedFutureDriftBps != 0 {
		t.Fatalf("martingale baseline must not repeat the observed decline: %+v", decision)
	}
}

func TestInventoryResetFastPathRequiresHealthyEvidence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 20, 0, 0, time.UTC)
	cfg := InventoryResetConfig{
		Enabled: true, MaxAskAge: types.Duration(10 * time.Minute), FastAskAge: types.Duration(time.Minute),
		AdverseMoveBps: 75, FastAdverseMoveBps: 20, FastDirectionThreshold: 0.25,
	}
	decision := cfg.Evaluate(InventoryResetInput{
		Now: now, AskSince: now.Add(-90 * time.Second), AnchorMidPrice: 100,
		MidPrice: 99.7, BestBid: 99.6, AskPrice: 99.8,
		FastDirectionSignal: -0.8, FastSignalHealthy: false, AverageCost: 98,
	})
	if decision.Trigger || decision.Reason != "fast signal unhealthy" {
		t.Fatalf("unhealthy fast evidence must fail closed: %+v", decision)
	}
}

func TestInventoryResetRequiresQuoteDistanceStatistics(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 20, 0, 0, time.UTC)
	cfg := InventoryResetConfig{
		Enabled: true, MaxAskAge: types.Duration(10 * time.Minute), AdverseMoveBps: 75,
	}
	decision := cfg.Evaluate(InventoryResetInput{
		Now: now, AskSince: now.Add(-12 * time.Minute), AnchorMidPrice: 100,
		MidPrice: 99.1, BestBid: 99, AskPrice: 99.2, AverageCost: 98,
	})
	if decision.Trigger || decision.Reason != "insufficient ask-distance crossing statistics" {
		t.Fatalf("reset must fail closed without actual-distance statistics: %+v", decision)
	}
}
