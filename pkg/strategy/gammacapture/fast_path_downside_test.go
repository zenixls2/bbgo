package gammacapture

import (
	"math"
	"testing"
)

func TestFastPathDownsideBuyCapUsesTerminalPosteriorNotRawDirection(t *testing.T) {
	for _, direction := range []float64{0, 0.8} {
		d := FastPathDownsideBuyCap(FastPathDownsideCapInput{
			Direction: direction, InventoryReturnMeanBps: -40,
			InventoryReturnVarianceBps2: 25, EffectiveSamples: 9,
			ConfidenceZScore: 1.645, BuyConfidenceEquivalentJPY: -0.1,
			MinimumBuyNotionalJPY: 100, MaximumBuyNotionalJPY: 1000,
		})
		if !d.Applied || d.MaximumBuyNotionalJPY != 100 {
			t.Fatalf("terminally bearish path must cap BUY independent of duplicate raw direction: %+v", d)
		}
		if d.InventoryReturnStdErrorBps <= 0 ||
			d.InventoryReturnUpperBps == 0 {
			t.Fatalf("valid path diagnostics must survive an early gate: %+v", d)
		}
	}
}

func TestFastPathDownsideBuyCapUsesUpperConfidenceBound(t *testing.T) {
	in := FastPathDownsideCapInput{
		Direction: -0.5, InventoryReturnMeanBps: -20,
		InventoryReturnVarianceBps2: 36, EffectiveSamples: 9,
		ConfidenceZScore: 1.645, BuyConfidenceEquivalentJPY: -0.1,
		MinimumBuyNotionalJPY: 100, MaximumBuyNotionalJPY: 1000,
	}
	d := FastPathDownsideBuyCap(in)
	wantSE := 2.0
	wantUpper := -20 + 1.645*wantSE
	if !d.Applied || d.MaximumBuyNotionalJPY != 100 ||
		math.Abs(d.InventoryReturnStdErrorBps-wantSE) > 1e-12 ||
		math.Abs(d.InventoryReturnUpperBps-wantUpper) > 1e-12 {
		t.Fatalf("unexpected downside cap: %+v", d)
	}
}

func TestFastPathDownsideBuyCapPreservesPositiveBuyUtility(t *testing.T) {
	d := FastPathDownsideBuyCap(FastPathDownsideCapInput{
		Direction: -1, InventoryReturnMeanBps: -13,
		InventoryReturnVarianceBps2: 100, EffectiveSamples: 4,
		ConfidenceZScore: 1.645, BuyConfidenceEquivalentJPY: 0.01,
		MinimumBuyNotionalJPY: 100, MaximumBuyNotionalJPY: 1000,
	})
	if d.Applied || d.MaximumBuyNotionalJPY != 1000 {
		t.Fatalf("positive BUY terminal wealth must preserve Fast baseline: %+v", d)
	}
}

func TestFastPathDownsideBuyCapFailsClosedWithoutVarianceSamples(t *testing.T) {
	for _, samples := range []float64{0, 1} {
		d := FastPathDownsideBuyCap(FastPathDownsideCapInput{
			Direction: -1, InventoryReturnMeanBps: -100,
			InventoryReturnVarianceBps2: 1, EffectiveSamples: samples,
			ConfidenceZScore: 1.645, BuyConfidenceEquivalentJPY: -0.1,
			MinimumBuyNotionalJPY: 100, MaximumBuyNotionalJPY: 1000,
		})
		if d.Applied || d.MaximumBuyNotionalJPY != 1000 {
			t.Fatalf("unidentified terminal variance must preserve Fast baseline: %+v", d)
		}
	}
}
