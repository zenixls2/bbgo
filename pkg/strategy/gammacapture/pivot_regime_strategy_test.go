package gammacapture

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestStrategyPivotAdapterUsesEveryRealBBOEvent(t *testing.T) {
	s := &Strategy{}
	config := MarketMakerConfig{
		DynamicInventoryAim: DynamicInventoryAimConfig{
			Enabled: true,
			PivotRegimeTarget: PivotRegimeTargetConfig{
				Enabled: true, ReversalBps: 20,
				MaxGap: types.Duration(3 * time.Minute), MinLegSamples: 1, PriorLegSamples: 1,
			},
		},
	}
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	prices := []float64{100, 100.30, 100.50, 100.25, 100.00, 99.70}
	for i, price := range prices {
		decision := s.observeMakerPivotRegime(start.Add(time.Duration(i)*time.Minute), price, config)
		if decision.At != start.Add(time.Duration(i)*time.Minute) {
			t.Fatalf("pivot adapter did not advance on BBO event %d: %+v", i, decision)
		}
	}
	if s.makerPivotRegimeFilter == nil {
		t.Fatal("strategy did not install the configured pivot filter")
	}
	if s.makerPivotRegimeDecision.At != start.Add(5*time.Minute) {
		t.Fatalf("strategy did not retain the latest causal pivot decision: %+v", s.makerPivotRegimeDecision)
	}
}

func TestStrategyPivotAdapterDoesNotAdvanceDisabledTarget(t *testing.T) {
	s := &Strategy{}
	at := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	decision := s.observeMakerPivotRegime(at, 100, MarketMakerConfig{})
	if decision.Reason != "pivot regime target disabled" || s.makerPivotRegimeFilter != nil {
		t.Fatalf("disabled pivot target must not install or advance state: %+v", decision)
	}
}
