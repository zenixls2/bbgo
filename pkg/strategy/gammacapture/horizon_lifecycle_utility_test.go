package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestApplyLifecycleAwareHorizonUtilitySubtractsUncertaintyAndReplacementOnce(t *testing.T) {
	decision := MarketMakerHorizonDecision{
		Horizon:                  10 * time.Minute,
		ScoreBpsPerHour:          12,
		SelectionScoreBpsPerHour: 12,
		ScoreStdErrorBpsHour:     2,
	}
	config := MarketMakerConfig{
		InventoryRiskZScore:  1.645,
		QuoteLifecycleAction: QuoteLifecycleActionConfig{ReplacementCostBps: 1},
	}
	got := applyLifecycleAwareHorizonUtility(decision, config)
	want := 12 - 1.645*2 - 1/(10.0/60.0)
	if math.Abs(got.SelectionScoreBpsPerHour-want) > 1e-12 {
		t.Fatalf("lifecycle utility=%v want=%v: %+v", got.SelectionScoreBpsPerHour, want, got)
	}
	if got.HorizonUncertaintyPenaltyBpsHour != 1.645*2 || got.HorizonReplacementCostBpsHour != 6 {
		t.Fatalf("diagnostic penalties not exposed: %+v", got)
	}
}

func TestApplyLifecycleAwareHorizonUtilityDoesNotChargeMakerFee(t *testing.T) {
	decision := MarketMakerHorizonDecision{
		Horizon: 15 * time.Minute, ScoreBpsPerHour: 5, SelectionScoreBpsPerHour: 5,
	}
	config := MarketMakerConfig{
		MakerFeeBps: 10, InventoryRiskZScore: 1.645,
		QuoteLifecycleAction: QuoteLifecycleActionConfig{},
	}
	got := applyLifecycleAwareHorizonUtility(decision, config)
	if got.SelectionScoreBpsPerHour != 5 {
		t.Fatalf("zero lifecycle replacement cost and uncertainty must preserve score: %+v", got)
	}
}
