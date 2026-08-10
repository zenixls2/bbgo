package main

import (
	"math"
	"testing"
	"time"
)

func TestCalculateVarianceWindowSeparatesExecutableSides(t *testing.T) {
	start := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	returns := []varianceMinuteReturn{{at: start, segment: 1}}
	for index := 1; index <= 4; index++ {
		returns = append(returns, varianceMinuteReturn{
			at: start.Add(time.Duration(index) * time.Minute), segment: 1,
			ask: float64(index) * .001, bid: -float64(index) * .002,
		})
	}
	ask, askOK := calculateVarianceWindow(returns, 1, 4, true)
	bid, bidOK := calculateVarianceWindow(returns, 1, 4, false)
	if !askOK || !bidOK {
		t.Fatalf("valid executable windows rejected: ask=%v bid=%v", askOK, bidOK)
	}
	if math.Abs(bid.realized/ask.realized-4) > 1e-12 {
		t.Fatalf("bid/ask realized ratio = %.12f, want 4", bid.realized/ask.realized)
	}
	if ask.downsideShare != 0 || bid.downsideShare != 1 {
		t.Fatalf("side-specific downside shares = ask %.6f bid %.6f", ask.downsideShare, bid.downsideShare)
	}
}

func TestCalculateVarianceWindowRejectsSegmentGap(t *testing.T) {
	returns := []varianceMinuteReturn{
		{}, {segment: 1, ask: .01, bid: .01}, {segment: 2, ask: .01, bid: .01},
	}
	if _, ok := calculateVarianceWindow(returns, 1, 2, true); ok {
		t.Fatal("window crossing a missing-minute segment must be rejected")
	}
}

func TestQLIKELossIsMinimizedAtActualVariance(t *testing.T) {
	actual := .004
	if got := qLikeLoss(actual, actual); math.Abs(got) > 1e-15 {
		t.Fatalf("QLIKE at truth = %g, want 0", got)
	}
	if qLikeLoss(.002, actual) <= 0 || qLikeLoss(.008, actual) <= 0 {
		t.Fatal("under- and over-forecast QLIKE must be positive")
	}
}
