package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
	"github.com/stretchr/testify/require"
)

func supportedEarlyBumpConfig() EarlyBumpConfig {
	return EarlyBumpConfig{
		Enabled: true, MinimumDrawdownBps: 15, MinimumRebound30sBps: 7,
		SelectedDeltaBps:    7,
		ActivationSuccesses: 21, ActivationSamples: 45,
		BaselineSuccesses: 45, BaselineSamples: 255,
		MinimumSamples: 30, ConfidenceZScore: 1.959963984540054,
		MinimumBBO: 20, LockDuration: types.Duration(30 * time.Second),
		Cooldown: types.Duration(2 * time.Minute),
	}
}

func supportedEarlyBumpInput(now time.Time) EarlyBumpInput {
	return EarlyBumpInput{
		Now: now, MidPrice: 100, BestBid: 99.95, BestAsk: 100.05,
		BaseBidPrice: 99.70, InventoryDeficit: true, CanBuy: true,
		BuyHeadroomJPY: 1000, MinimumNotional: 100,
		Evidence: FastEvidenceSnapshot{
			Health: HealthHealthy, BBOCount: 100, BBOCount5m: 100,
			MidDrawdownBps: 20, MidDrawdownWindow: 15 * time.Minute,
			MidDrawdown5mBps: 2, MidRebound30sBps: 8, MidLow30s: 99.92,
		},
	}
}

func TestEarlyBumpRequiresConfidenceBoundedLift(t *testing.T) {
	cfg := supportedEarlyBumpConfig()
	p, lower, baseline, upper, ok := cfg.statisticalSupport()
	require.True(t, ok)
	require.InDelta(t, 21.0/45.0, p, 1e-12)
	require.InDelta(t, 45.0/255.0, baseline, 1e-12)
	require.Greater(t, lower, upper)

	cfg.ActivationSuccesses = 10
	require.False(t, cfg.signal(supportedEarlyBumpInput(time.Now())).Signal)
}

func TestEarlyBumpLocksOneAbsolutePassiveBid(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := supportedEarlyBumpConfig()
	input := supportedEarlyBumpInput(now)
	state := EarlyBumpState{}

	first := state.Update(cfg, input)
	require.True(t, first.Apply)
	require.True(t, first.Refresh)
	require.True(t, first.Transition)
	require.Equal(t, EarlyBumpLocked, first.Phase)
	want := math.Min(input.BestBid, input.BaseBidPrice*math.Exp(cfg.SelectedDeltaBps/10_000))
	require.InDelta(t, want, first.BidPrice, 1e-12)

	input.Now = now.Add(10 * time.Second)
	input.BestBid = 100.15
	input.BestAsk = 100.20
	input.BaseBidPrice = 99.95
	second := state.Update(cfg, input)
	require.True(t, second.Apply)
	require.False(t, second.Refresh)
	require.InDelta(t, first.BidPrice, second.BidPrice, 1e-12)
}

func TestEarlyBumpAbortsOnRenewedDownside(t *testing.T) {
	now := time.Unix(2000, 0)
	cfg := supportedEarlyBumpConfig()
	input := supportedEarlyBumpInput(now)
	state := EarlyBumpState{}
	require.True(t, state.Update(cfg, input).Apply)

	input.Now = now.Add(5 * time.Second)
	input.MidPrice = input.Evidence.MidLow30s - 0.01
	decision := state.Update(cfg, input)
	require.Equal(t, EarlyBumpCooldown, decision.Phase)
	require.True(t, decision.Refresh)
	require.True(t, decision.Transition)
	require.False(t, decision.Apply)
	require.Equal(t, "urgency invalidated by renewed downside", decision.Reason)
}

func TestEarlyBumpShadowNeverChangesQuote(t *testing.T) {
	cfg := supportedEarlyBumpConfig()
	cfg.ShadowOnly = true
	state := EarlyBumpState{}
	decision := state.Update(cfg, supportedEarlyBumpInput(time.Now()))
	require.True(t, decision.Signal)
	require.False(t, decision.Apply)
	require.False(t, decision.Refresh)
}

func TestApplyEarlyBumpBidPreservesAskAndUpdatesDistance(t *testing.T) {
	plan := MarketMakerQuotePlan{AllowBid: true, BidPrice: 99.70, BidDistanceBps: 30}
	decision := EarlyBumpDecision{Apply: true, BidPrice: 99.80}
	got := applyEarlyBumpBid(plan, decision, 100, 100.05)
	require.InDelta(t, 99.80, got.BidPrice, 1e-12)
	require.InDelta(t, math.Log(100.0/99.80)*10_000, got.BidDistanceBps, 1e-9)

	decision.BidPrice = 100.05
	require.Equal(t, got, applyEarlyBumpBid(got, decision, 100, 100.05))
}

func TestEarlyBumpPreservesFeeAdjustedBidFloor(t *testing.T) {
	cfg := supportedEarlyBumpConfig()
	input := supportedEarlyBumpInput(time.Now())
	input.MinimumBidDistanceBps = 25
	decision := cfg.signal(input)
	require.True(t, decision.Signal)
	require.LessOrEqual(t, decision.BidPrice, input.MidPrice*math.Exp(-25.0/10_000))

	input.MinimumBidDistanceBps = 40
	decision = cfg.signal(input)
	require.False(t, decision.Signal)
	require.Equal(t, "selected delta cannot improve passive bid", decision.Reason)
}

func TestEarlyBumpUsesSelectedWindowForSlowDecline(t *testing.T) {
	now := time.Unix(4000, 0)
	model := NewFastEvidenceModel(FastEvidenceConfig{
		Window: 15 * time.Minute, MinTrades: 1, MinBBOUpdates: 1,
	})
	model.ObserveTrade(now.Add(-time.Minute),
		evidenceTrade(now.Add(-time.Minute), 1, types.SideTypeBuy, 99.83, 1))
	model.ObserveBBO(now.Add(-12*time.Minute), evidenceBBO("ETHJPY", 99.99, 1, 100.01, 1))
	model.ObserveBBO(now.Add(-6*time.Minute), evidenceBBO("ETHJPY", 99.74, 1, 99.76, 1))
	model.ObserveBBO(now.Add(-20*time.Second), evidenceBBO("ETHJPY", 99.74, 1, 99.76, 1))
	model.ObserveBBO(now, evidenceBBO("ETHJPY", 99.82, 1, 99.84, 1))

	evidence := model.Snapshot(now)
	require.Equal(t, 15*time.Minute, evidence.MidDrawdownWindow)
	require.Greater(t, evidence.MidDrawdownBps, 15.0)
	require.Less(t, evidence.MidDrawdown5mBps, 15.0)
	require.Greater(t, evidence.MidRebound30sBps, 7.0)

	cfg := supportedEarlyBumpConfig()
	cfg.MinimumBBO = 1
	cfg.ShadowOnly = true
	input := supportedEarlyBumpInput(now)
	input.MidPrice = 99.83
	input.BestBid = 99.82
	input.BestAsk = 99.84
	input.BaseBidPrice = 99.50
	input.Evidence = evidence

	decision := (&EarlyBumpState{}).Update(cfg, input)
	require.True(t, decision.Signal)
	require.True(t, decision.Transition)
	require.Equal(t, EarlyBumpLocked, decision.Phase)
	require.False(t, decision.Apply)
	require.False(t, decision.Refresh)
	require.Equal(t, "shadow confidence-bounded escape lift", decision.Reason)
}

func TestEarlyBumpLegacyFiveMinuteThresholdFallback(t *testing.T) {
	cfg := supportedEarlyBumpConfig()
	cfg.MinimumDrawdownBps = 0
	cfg.MinimumDrawdown5mBps = 15
	cfg.MinimumBBO = 0
	cfg.MinimumBBO5m = 20
	input := supportedEarlyBumpInput(time.Now())
	input.Evidence.MidDrawdownWindow = 0
	input.Evidence.MidDrawdownBps = 0
	input.Evidence.MidDrawdown5mBps = 20
	require.True(t, cfg.signal(input).Signal)
}

func TestEarlyBumpAbortsWhenInventoryDeficitCloses(t *testing.T) {
	now := time.Unix(3000, 0)
	cfg := supportedEarlyBumpConfig()
	input := supportedEarlyBumpInput(now)
	state := EarlyBumpState{}
	require.True(t, state.Update(cfg, input).Apply)

	input.Now = now.Add(time.Second)
	input.InventoryDeficit = false
	decision := state.Update(cfg, input)
	require.Equal(t, EarlyBumpCooldown, decision.Phase)
	require.True(t, decision.Refresh)
	require.True(t, decision.Transition)
	require.Equal(t, "urgency eligibility lost", decision.Reason)
}
