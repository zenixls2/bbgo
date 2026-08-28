package gammacapture

import (
	"math"
	"testing"
)

func TestEvaluateRegimeExpectedValueSizingChargesFeesWithoutBinaryAdmission(t *testing.T) {
	config := RegimeExpectedValueSizingConfig{
		PriorEffectiveSamples: 1, RiskAversion: 1,
		MakerFeeBps: 10, AdverseSelectionBps: 2, TurnoverBufferBps: 2,
	}
	in := RegimeExpectedValueSizingInput{
		GrossExpectedValueJPY: 20, GrossStdErrorJPY: 500, EffectiveSamples: 100,
		RegimeReliability: 1, RegimeDirectionScore: 0,
		PairEquityJPY: 10_000, BuyNotionalJPY: 500, SellNotionalJPY: 500,
		BuyFillProbability: .2, SellFillProbability: .2, BothFillProbability: .1,
	}
	d := EvaluateRegimeExpectedValueSizing(config, in)
	if !d.Ready || d.Scale <= 0 || d.Scale >= 1 {
		t.Fatalf("expected a continuous non-zero scale below one: %+v", d)
	}
	if d.ExpectedFeeJPY <= 0 || d.ExpectedAdverseJPY <= 0 || d.ExpectedTurnoverJPY <= 0 {
		t.Fatalf("fee, adverse selection, and turnover costs must be charged: %+v", d)
	}
}

func TestEvaluateRegimeExpectedValueSizingShrinksToZeroWhenCostsWin(t *testing.T) {
	d := EvaluateRegimeExpectedValueSizing(
		RegimeExpectedValueSizingConfig{MakerFeeBps: 20, AdverseSelectionBps: 5, TurnoverBufferBps: 4},
		RegimeExpectedValueSizingInput{
			GrossExpectedValueJPY: 1, GrossStdErrorJPY: 0, EffectiveSamples: 100,
			RegimeReliability: 1, PairEquityJPY: 10_000,
			BuyNotionalJPY: 1_000, SellNotionalJPY: 1_000,
			BuyFillProbability: .5, SellFillProbability: .5,
		})
	if !d.Ready || d.Scale != 0 || d.ExpectedUtilityJPY != 0 {
		t.Fatalf("fee-negative action must continuously collapse to zero: %+v", d)
	}
}

func TestEvaluateRegimeExpectedValueSizingDirectionalStressAndBounds(t *testing.T) {
	base := RegimeExpectedValueSizingInput{
		GrossExpectedValueJPY: 50, GrossStdErrorJPY: 5, EffectiveSamples: 32,
		RegimeReliability: .8, PairEquityJPY: 10_000,
		BuyNotionalJPY: 500, SellNotionalJPY: 500,
		BuyFillProbability: .2, SellFillProbability: .2, BothFillProbability: .1,
	}
	neutral := EvaluateRegimeExpectedValueSizing(RegimeExpectedValueSizingConfig{
		MakerFeeBps: 1, AdverseSelectionBps: 0, TurnoverBufferBps: 0, MaxScale: .75,
	}, base)
	trendingInput := base
	trendingInput.RegimeDirectionScore = 1
	trending := EvaluateRegimeExpectedValueSizing(RegimeExpectedValueSizingConfig{
		MakerFeeBps: 1, AdverseSelectionBps: 0, TurnoverBufferBps: 0, MaxScale: .75,
	}, trendingInput)
	if trending.Scale > neutral.Scale+1e-12 {
		t.Fatalf("directional regime stress must not increase two-sided maker size: neutral=%+v trending=%+v", neutral, trending)
	}
	for _, value := range []float64{neutral.Scale, neutral.ExpectedUtilityJPY, trending.Scale, trending.ExpectedUtilityJPY} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("sizing output is not finite: %v", value)
		}
	}
}
