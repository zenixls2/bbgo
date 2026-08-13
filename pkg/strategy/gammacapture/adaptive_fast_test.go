package gammacapture

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func adaptiveFastTestStrategy() *Strategy {
	strategy := &Strategy{Config: Config{MarketMaker: MarketMakerConfig{
		FastWindow: types.Duration(10 * time.Minute),
		FastWindows: []types.Duration{
			types.Duration(10 * time.Minute),
			types.Duration(15 * time.Minute),
			types.Duration(30 * time.Minute),
		},
		FastEvidenceMinTrades: 2, FastEvidenceMinBBOUpdates: 2,
		MinTradingWindow: types.Duration(10 * time.Minute),
		MaxTradingWindow: types.Duration(30 * time.Minute),
	}}}
	strategy.initializeAdaptiveFastModels()
	return strategy
}

func adaptiveFastCrossing(at time.Time, direction Direction) CrossingEvent {
	return CrossingEvent{
		Symbol: "ETHJPY", Direction: direction, BarrierWidth: 0.001,
		ExchangeTime: at, ReceiveTime: at,
	}
}

func TestAdaptiveFastSnapshotFallsBackToShortestHealthyWindow(t *testing.T) {
	strategy := adaptiveFastTestStrategy()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	strategy.updateFastModels(adaptiveFastCrossing(now.Add(-25*time.Minute), DirectionUp))
	strategy.updateMakerDirectionModels(adaptiveFastCrossing(now.Add(-25*time.Minute), DirectionUp))
	strategy.updateFastModels(adaptiveFastCrossing(now.Add(-20*time.Minute), DirectionDown))
	strategy.updateMakerDirectionModels(adaptiveFastCrossing(now.Add(-20*time.Minute), DirectionDown))
	selected := strategy.adaptiveFastSnapshot(now)
	if selected.Window != 30*time.Minute || selected.Model.Health != HealthHealthy {
		t.Fatalf("30m should cover the first healthy fallback: %+v", selected)
	}

	strategy.updateFastModels(adaptiveFastCrossing(now.Add(-12*time.Minute), DirectionUp))
	strategy.updateMakerDirectionModels(adaptiveFastCrossing(now.Add(-12*time.Minute), DirectionUp))
	strategy.updateFastModels(adaptiveFastCrossing(now.Add(-11*time.Minute), DirectionDown))
	strategy.updateMakerDirectionModels(adaptiveFastCrossing(now.Add(-11*time.Minute), DirectionDown))
	selected = strategy.adaptiveFastSnapshot(now)
	if selected.Window != 15*time.Minute || selected.Model.Health != HealthHealthy {
		t.Fatalf("15m should replace 30m as the shortest healthy window: %+v", selected)
	}

	strategy.updateFastModels(adaptiveFastCrossing(now.Add(-2*time.Minute), DirectionUp))
	strategy.updateMakerDirectionModels(adaptiveFastCrossing(now.Add(-2*time.Minute), DirectionUp))
	strategy.updateFastModels(adaptiveFastCrossing(now.Add(-time.Minute), DirectionDown))
	strategy.updateMakerDirectionModels(adaptiveFastCrossing(now.Add(-time.Minute), DirectionDown))
	selected = strategy.adaptiveFastSnapshot(now)
	if selected.Window != 10*time.Minute || selected.Model.Health != HealthHealthy {
		t.Fatalf("10m should resume as soon as it is healthy: %+v", selected)
	}
	if !strings.Contains(selected.HealthSummary, "10m0s=HEALTHY") ||
		!strings.Contains(selected.HealthSummary, "15m0s=HEALTHY") ||
		!strings.Contains(selected.HealthSummary, "30m0s=HEALTHY") {
		t.Fatalf("diagnostics must expose every live window: %s", selected.HealthSummary)
	}
	direction := strategy.makerDirectionSnapshot(selected.Window, now)
	if direction.EffectiveSamples <= 0 {
		t.Fatalf("selected direction model was not updated: %+v", direction)
	}
}

func TestAdaptiveFastSnapshotPrefersHealthyQuoteHorizon(t *testing.T) {
	strategy := adaptiveFastTestStrategy()
	now := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
	strategy.updateFastModels(adaptiveFastCrossing(now.Add(-2*time.Minute), DirectionUp))
	strategy.updateFastModels(adaptiveFastCrossing(now.Add(-time.Minute), DirectionDown))

	selected := strategy.adaptiveFastSnapshotForWindow(now, 30*time.Minute)
	if selected.Window != 30*time.Minute || selected.Model.Health != HealthHealthy {
		t.Fatalf("direction must align with the healthy EV-selected quote horizon: %+v", selected)
	}
}

func TestExplicitFastWindowsDefineSelectableTradingHorizons(t *testing.T) {
	config := MarketMakerConfig{
		MinTradingWindow: types.Duration(10 * time.Minute),
		MaxTradingWindow: types.Duration(30 * time.Minute),
		FastWindows: []types.Duration{
			types.Duration(30 * time.Minute),
			types.Duration(10 * time.Minute),
			types.Duration(15 * time.Minute),
			types.Duration(15 * time.Minute),
		},
	}
	got := config.TradingHorizons()
	want := []time.Duration{10 * time.Minute, 15 * time.Minute, 30 * time.Minute}
	if len(got) != len(want) {
		t.Fatalf("unexpected explicit horizons: got=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unexpected explicit horizons: got=%v want=%v", got, want)
		}
	}
}

func TestInferFastCrossingQuietWindowIsRateOnly(t *testing.T) {
	fast := ModelSnapshot{Health: HealthInsufficient}
	evidence := FastEvidenceSnapshot{Health: HealthHealthy, Observed: 30 * time.Minute}
	slow := ModelSnapshot{Health: HealthHealthy, Total: 1.0 / 900.0, Observed: 6 * time.Hour}

	got := inferFastCrossing(30*time.Minute, fast, evidence, slow)
	if got.Activity != FastCrossingQuiet || !got.RateUsable || got.DirectionalActions {
		t.Fatalf("quiet observed window must be rate-only: %+v", got)
	}
	if got.Direction != 0 || got.DirectionConfidence != 0 {
		t.Fatalf("zero crossings must have neutral direction: %+v", got)
	}
	if got.PriorExposure != 15*time.Minute {
		t.Fatalf("one slow pseudo-crossing should imply 15m exposure: %+v", got)
	}
	wantTotal := 1.0 / 2700.0
	if math.Abs(got.Total-wantTotal) > 1e-12 || math.Abs(got.LambdaUp-wantTotal/2) > 1e-12 || math.Abs(got.LambdaDown-wantTotal/2) > 1e-12 {
		t.Fatalf("unexpected quiet posterior rate: got=%+v wantTotal=%g", got, wantTotal)
	}
}

func TestInferFastCrossingSparseDirectionUsesCurrentWindowOnly(t *testing.T) {
	fast := ModelSnapshot{Up: 1, Health: HealthDegraded}
	evidence := FastEvidenceSnapshot{Health: HealthHealthy, Observed: 15 * time.Minute}
	slow := ModelSnapshot{Health: HealthHealthy, Total: 1.0 / 900.0, Observed: 6 * time.Hour}

	got := inferFastCrossing(15*time.Minute, fast, evidence, slow)
	if got.Activity != FastCrossingSparse || !got.RateUsable || got.DirectionalActions {
		t.Fatalf("one crossing must remain sparse and direction-only: %+v", got)
	}
	if math.Abs(got.Direction-1.0/3.0) > 1e-12 || math.Abs(got.DirectionConfidence-1.0/3.0) > 1e-12 {
		t.Fatalf("Beta(1,1) posterior mismatch: %+v", got)
	}
	if math.Abs(got.Total-1.0/900.0) > 1e-12 {
		t.Fatalf("unexpected sparse posterior total: %+v", got)
	}
}

func TestInferFastCrossingHealthyEnablesDirectionalActions(t *testing.T) {
	fast := ModelSnapshot{Up: 2, Health: HealthHealthy}
	evidence := FastEvidenceSnapshot{Health: HealthHealthy, Observed: 10 * time.Minute}
	slow := ModelSnapshot{Health: HealthHealthy, Total: 1.0 / 900.0, Observed: 6 * time.Hour}

	got := inferFastCrossing(10*time.Minute, fast, evidence, slow)
	if got.Activity != FastCrossingActive || !got.DirectionalActions {
		t.Fatalf("healthy crossing posterior should enable directional actions: %+v", got)
	}
	if math.Abs(got.Direction-0.5) > 1e-12 || math.Abs(got.DirectionConfidence-0.5) > 1e-12 {
		t.Fatalf("unexpected healthy direction posterior: %+v", got)
	}
}

func TestInferFastCrossingMissingEvidenceFailsClosed(t *testing.T) {
	fast := ModelSnapshot{Up: 2, Health: HealthHealthy}
	evidence := FastEvidenceSnapshot{Health: HealthDegraded, Observed: 10 * time.Minute}
	slow := ModelSnapshot{Health: HealthHealthy, Total: 1.0 / 900.0, Observed: 6 * time.Hour}

	got := inferFastCrossing(10*time.Minute, fast, evidence, slow)
	if got.Activity != FastCrossingUnobserved || got.RateUsable || got.DirectionalActions || got.Direction != 0 {
		t.Fatalf("missing raw coverage must fail closed: %+v", got)
	}
}

func TestInferFastCrossingUsesObservedSlowPosteriorBeforeCountHealth(t *testing.T) {
	fast := ModelSnapshot{Health: HealthInsufficient}
	evidence := FastEvidenceSnapshot{Health: HealthHealthy, Observed: 30 * time.Minute}
	slow := ModelSnapshot{
		Health: HealthInsufficient, Total: 1.0 / 2400.0,
		Observed: 6 * time.Hour, Up: 3, Down: 4,
	}

	got := inferFastCrossing(30*time.Minute, fast, evidence, slow)
	if got.Activity != FastCrossingQuiet || !got.RateUsable || got.RateSource != "slow-empirical-bayes" {
		t.Fatalf("real slow exposure must support quiet rate before count health: %+v", got)
	}
}
