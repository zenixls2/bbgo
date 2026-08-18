package main

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/stretchr/testify/require"
)

func TestVolumeProfileObservationsRebaseEveryWindow(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC().Truncate(time.Minute)
	minutes := make([]volumeProfileMinute, 12)
	for i := range minutes {
		price := 100 + float64(i)
		minutes[i] = volumeProfileMinute{
			at: start.Add(time.Duration(i) * time.Minute), startBid: price, startAsk: price + 0.1,
			terminalBid: price, terminalAsk: price + 0.1, minAsk: price + 0.1, maxBid: price,
			meanBid: price, meanAsk: price + 0.1,
			profile: gammacapture.VolumeProfileState{Valid: true},
		}
	}
	minutes[1].minAsk = 100
	latency := fillLatencyCoverageEstimate{
		Sufficient: true, ProfileRange: 2 * time.Minute,
		BuyWindowMass: []float64{0.5, 0.3}, SellWindowMass: []float64{0.5, 0.3},
	}
	observations := volumeProfileObservations(minutes, time.Minute, 1, 0, latency)
	require.NotEmpty(t, observations)
	// A BUY event uses the quote active at that event-clock anchor and the next
	// complete window's executable bid mean.
	buy0 := math.Log(102/(101.1*math.Exp(-1.0/10_000))) * 10_000
	require.InDelta(t, buy0, observations[0].buyMeanReturnBps, 1e-9)
	require.Equal(t, start.Add(4*time.Minute), observations[0].maturity)
}

func TestSelectEventClockObservationsUsesExpiryOrCrossing(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC().Truncate(time.Minute)
	minutes := make([]volumeProfileMinute, 20)
	observations := make([]volumeProfileObservation, 20)
	for i := range minutes {
		minutes[i] = volumeProfileMinute{
			at:       start.Add(time.Duration(i) * time.Minute),
			startBid: 99.9, startAsk: 100.1, minAsk: 100.1, maxBid: 99.9,
		}
		observations[i].at = minutes[i].at
	}
	minutes[2].minAsk = 99
	selected := selectEventClockObservations(minutes, observations, 5*time.Minute, 10)
	require.GreaterOrEqual(t, len(selected), 3)
	require.Equal(t, start, selected[0].at)
	require.Equal(t, start.Add(3*time.Minute), selected[1].at)
	require.Equal(t, start.Add(8*time.Minute), selected[2].at)
}

func TestEstimateFillLatencyCoverageIsPerHorizonAndSideConservative(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC().Truncate(time.Minute)
	minutes := make([]volumeProfileMinute, 80)
	for i := range minutes {
		minutes[i] = volumeProfileMinute{
			at:       start.Add(time.Duration(i) * time.Minute),
			startBid: 99.9, startAsk: 100.1, terminalBid: 99.9, terminalAsk: 100.1,
			minAsk: 100.1, maxBid: 99.9,
		}
		if i%10 >= 2 {
			minutes[i].minAsk = 99
		}
		if i%10 >= 7 {
			minutes[i].maxBid = 101
		}
	}
	estimate := estimateFillLatencyCoverage(minutes, start.Add(60*time.Minute), 10*time.Minute, 30*time.Minute, 10, 0.8)
	require.True(t, estimate.Sufficient)
	require.GreaterOrEqual(t, estimate.Sell, estimate.Buy)
	require.Equal(t, 10*time.Minute, estimate.ProfileRange)
}

func TestEstimateFillLatencyCoverageDoesNotExtrapolateCensoredSide(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC().Truncate(time.Minute)
	minutes := make([]volumeProfileMinute, 50)
	for i := range minutes {
		minutes[i] = volumeProfileMinute{
			at:       start.Add(time.Duration(i) * time.Minute),
			startBid: 99.9, startAsk: 100.1, terminalBid: 99.9, terminalAsk: 100.1,
			minAsk: 99, maxBid: 99.9,
		}
	}
	estimate := estimateFillLatencyCoverage(minutes, start.Add(40*time.Minute), 10*time.Minute, 20*time.Minute, 10, 0.8)
	require.False(t, estimate.Sufficient)
}

func TestSmallEWRegressionLearnsVolumeInteraction(t *testing.T) {
	model := smallEWRegression{dim: 2, halfLife: time.Hour}
	start := time.Unix(1_700_000_000, 0)
	for i := 0; i < 40; i++ {
		x := [volumeProfileRegressionMaxFeatures]float64{1, float64(i%2)*2 - 1}
		model.update(start.Add(time.Duration(i)*time.Minute), x, 5*x[1], 0.5, x[1])
	}
	prediction, ready := model.predict([volumeProfileRegressionMaxFeatures]float64{1, 1})
	require.True(t, ready)
	require.Greater(t, prediction[0], 4.0)
}

func TestSmallEWRegressionDoesNotClampTerminalBpsResponses(t *testing.T) {
	model := smallEWRegression{dim: 1, halfLife: time.Hour}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	features := [volumeProfileRegressionMaxFeatures]float64{1}
	for i := 0; i < 8; i++ {
		model.update(start.Add(time.Duration(i)*time.Minute), features, 12, 25, -18)
	}
	prediction, ready := model.predict(features)
	require.True(t, ready)
	require.Greater(t, prediction[1], 1.0)
	require.Less(t, prediction[2], -1.0)
}
