package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestRollingVolumeProfilePOCAndSideReflection(t *testing.T) {
	profile := NewRollingVolumeProfile(VolumeProfileConfig{HalfLife: types.Duration(time.Hour), BinWidthBps: 1, MaxBins: 64})
	start := time.Unix(1_700_000_000, 0)
	for i := 0; i < 20; i++ {
		profile.Observe(start.Add(time.Duration(i)*time.Second), 100, 10, i%2 == 0)
	}
	for i := 0; i < 8; i++ {
		profile.Observe(start.Add(time.Duration(20+i)*time.Second), 100.20, 1, true)
	}
	state := profile.Snapshot(100.20)
	require.True(t, state.Valid)
	require.Greater(t, state.POCDistanceBps, 0.0)
	require.Less(t, state.LocalDensityRatio, 1.0)
	buy, sell := state.Vector(true), state.Vector(false)
	require.InDelta(t, buy[1], sell[1], 1e-12)
	for _, index := range []int{0, 2, 3, 4} {
		require.InDelta(t, buy[index], -sell[index], 1e-12)
	}
}

func TestRollingVolumeProfileIsBoundedAndDecaysLazily(t *testing.T) {
	profile := NewRollingVolumeProfile(VolumeProfileConfig{HalfLife: types.Duration(time.Minute), BinWidthBps: 1, MaxBins: 16})
	start := time.Unix(1_700_000_000, 0)
	for i := 0; i < 200; i++ {
		price := 100 * math.Exp(float64(i)*1.1/10_000)
		profile.Observe(start.Add(time.Duration(i)*time.Second), price, 1, i%2 == 0)
		require.LessOrEqual(t, profile.BinCount(), 16)
	}
	require.True(t, profile.Snapshot(100*math.Exp(199*1.1/10_000)).Valid)
}

func TestVolumeProfileSideTerminalRiskIsAsymmetricAroundPOC(t *testing.T) {
	state := VolumeProfileState{
		Valid:              true,
		ProfileScaleBps:    20,
		LocalDensityRatio:  1,
		LocalFlowImbalance: -1,
		POCDistanceBps:     -30, // POC overhead: resistance confirmed by sellers.
	}
	require.InDelta(t, 20.0, state.SideTerminalRiskPenaltyBps(true), 1e-12)
	require.InDelta(t, 0.0, state.SideTerminalRiskPenaltyBps(false), 1e-12)

	state.POCDistanceBps = 30 // POC below: support, but no buyer confirmation yet.
	state.LocalFlowImbalance = -1
	require.InDelta(t, 0.0, state.SideTerminalRiskPenaltyBps(true), 1e-12)
	require.InDelta(t, 0.0, state.SideTerminalRiskPenaltyBps(false), 1e-12)

	state.LocalFlowImbalance = 1
	require.InDelta(t, 0.0, state.SideTerminalRiskPenaltyBps(true), 1e-12)
	require.InDelta(t, 20.0, state.SideTerminalRiskPenaltyBps(false), 1e-12)
}

func TestVolumeProfileSideTerminalRiskScalesWithDensityAndFlow(t *testing.T) {
	state := VolumeProfileState{
		Valid:              true,
		POCDistanceBps:     -8,
		ProfileScaleBps:    20,
		LocalDensityRatio:  0.5,
		LocalFlowImbalance: -0.4,
	}
	mean, variance := state.SideTerminalRiskMoments(true)
	require.InDelta(t, 1.6, mean, 1e-12)
	require.InDelta(t, 3.84, variance, 1e-12)
	require.InDelta(t, mean, state.SideTerminalRiskPenaltyBps(true), 1e-12)
	state.LocalFlowImbalance = 0
	require.Zero(t, state.SideTerminalRiskPenaltyBps(true))
	state.LocalDensityRatio = 3
	state.LocalFlowImbalance = -2
	require.InDelta(t, 8.0, state.SideTerminalRiskPenaltyBps(true), 1e-12)
}

func BenchmarkRollingVolumeProfileObserveAndMinuteSnapshot(b *testing.B) {
	profile := NewRollingVolumeProfile(VolumeProfileConfig{HalfLife: types.Duration(45 * time.Minute), BinWidthBps: 1, MaxBins: 512})
	start := time.Unix(1_700_000_000, 0)
	for i := 0; i < 512; i++ {
		profile.Observe(start.Add(time.Duration(i)*time.Second), 100*math.Exp(float64(i%64)/10_000), 1, i%2 == 0)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		at := start.Add(time.Duration(513+i) * time.Second)
		price := 100 * math.Exp(float64(i%64)/10_000)
		profile.Observe(at, price, 1, i%2 == 0)
		if i%60 == 0 {
			_ = profile.Snapshot(price)
		}
	}
}
