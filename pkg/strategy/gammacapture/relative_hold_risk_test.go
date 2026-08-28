package gammacapture

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestRelativeHoldRiskJSONAcceptsHumanReadableDurations(t *testing.T) {
	var cfg RelativeHoldRiskConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"horizon":"1h","halfLife":"6h"}`), &cfg); err != nil {
		t.Fatalf("duration-string JSON must decode: %v", err)
	}
	if cfg.Horizon != time.Hour || cfg.HalfLife != 6*time.Hour {
		t.Fatalf("unexpected durations: horizon=%s halfLife=%s", cfg.Horizon, cfg.HalfLife)
	}
}

func TestRelativeHoldPrivateFillReplayReconcilesFeeCurrencies(t *testing.T) {
	trades := []types.Trade{
		{
			Symbol: "ETHJPY", Side: types.SideTypeBuy, IsBuyer: true,
			Price: fixedpoint.NewFromFloat(100), Quantity: fixedpoint.NewFromFloat(1),
			QuoteQuantity: fixedpoint.NewFromFloat(100), Fee: fixedpoint.NewFromFloat(.001),
			FeeCurrency: "ETH",
		},
		{
			Symbol: "ETHJPY", Side: types.SideTypeSell,
			Price: fixedpoint.NewFromFloat(110), Quantity: fixedpoint.NewFromFloat(.5),
			QuoteQuantity: fixedpoint.NewFromFloat(55), Fee: fixedpoint.NewFromFloat(.055),
			FeeCurrency: "JPY",
		},
	}
	baseStart, quoteStart := .25, 200.0
	baseEnd, quoteEnd := applyRelativeHoldPrivateTrades(baseStart, quoteStart, trades, "ETH", "JPY")
	if math.Abs(baseEnd-.749) > 1e-12 || math.Abs(quoteEnd-154.945) > 1e-12 {
		t.Fatalf("forward private-fill replay mismatch: base=%v quote=%v", baseEnd, quoteEnd)
	}
	recoveredBase, recoveredQuote, ok := reverseRelativeHoldPrivateTrades(baseEnd, quoteEnd, trades, "ETH", "JPY")
	if !ok || math.Abs(recoveredBase-baseStart) > 1e-12 || math.Abs(recoveredQuote-quoteStart) > 1e-12 {
		t.Fatalf("reverse private-fill replay mismatch: ok=%v base=%v quote=%v", ok, recoveredBase, recoveredQuote)
	}
}

func relativeHoldTestModel() *RelativeHoldRiskModel {
	return NewRelativeHoldRiskModel(RelativeHoldRiskConfig{
		Horizon:                         time.Hour,
		HalfLife:                        4 * time.Hour,
		MinimumEffectiveSamples:         2,
		MinimumDownsideEffectiveSamples: 1.5,
		PriorEffectiveSamples:           4,
		PriorVarianceFraction:           1e-8,
		PriorDownsideVarianceFraction:   1e-8,
		TargetDownsideBeta:              1,
		ConfidenceZ:                     1.645,
		TailQuantile:                    .75,
		MaxTailSamples:                  8,
	})
}

func addRelativeHoldLabel(m *RelativeHoldRiskModel, decision, matured time.Time, strategy, hold float64) bool {
	return m.UpdateLabel(RelativeHoldRiskLabel{
		DecisionAt: decision, MaturedAt: matured,
		StrategyReturn: strategy, HoldReturn: hold,
	})
}

func TestRelativeHoldRiskRequiresMaturedCausalLabel(t *testing.T) {
	m := relativeHoldTestModel()
	decision := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if addRelativeHoldLabel(m, decision, decision.Add(59*time.Minute), .001, .001) {
		t.Fatal("label before the one-hour outcome horizon was accepted")
	}
	if got := m.Snapshot(); got.MaturedLabels != 0 || got.Ready {
		t.Fatalf("premature update changed state: %+v", got)
	}
	if !addRelativeHoldLabel(m, decision, decision.Add(time.Hour), .001, .001) {
		t.Fatal("matured label was rejected")
	}
	if got := m.Snapshot(); got.MaturedLabels != 1 || got.Ready {
		t.Fatalf("one label should not be ready: %+v", got)
	}
}

func TestRelativeHoldRiskTracksExcessAndDownsideBeta(t *testing.T) {
	m := relativeHoldTestModel()
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	labels := []struct{ strategy, hold float64 }{
		{-.020, -.010},
		{-.040, -.020},
		{.006, .004},
		{.008, .004},
	}
	for i, label := range labels {
		decision := start.Add(time.Duration(i) * time.Hour)
		if !addRelativeHoldLabel(m, decision, decision.Add(time.Hour), label.strategy, label.hold) {
			t.Fatalf("label %d rejected", i)
		}
	}
	state := m.Snapshot()
	if !state.Ready || !state.DownsideReady {
		t.Fatalf("expected ready relative state: %+v", state)
	}
	if state.MeanExcessReturn >= 0 {
		t.Fatalf("mean excess should reflect the two adverse labels: %+v", state)
	}
	if state.DownsideBeta <= 1.5 {
		t.Fatalf("downside beta = %f, want materially above one", state.DownsideBeta)
	}
	if state.DownsideCVaR <= 0 {
		t.Fatalf("shadow downside CVaR = %f, want positive loss", state.DownsideCVaR)
	}
	if state.TrackingErrorBps <= 0 || state.Shrinkage <= 0 || state.Shrinkage >= 1 {
		t.Fatalf("invalid tracking/shrinkage diagnostics: %+v", state)
	}
}

func TestRelativeHoldRiskTracksTotalBetaAndAppliesOptInPenalty(t *testing.T) {
	m := NewRelativeHoldRiskModel(RelativeHoldRiskConfig{
		Horizon: time.Hour, HalfLife: 4 * time.Hour,
		MinimumEffectiveSamples: 2, PriorEffectiveSamples: 0,
		PriorVarianceFraction: 1e-8, TargetTotalBeta: .5,
		ConfidenceZ: 1.645,
	})
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	for i, hold := range []float64{-.020, .010, -.010, .030} {
		if !addRelativeHoldLabel(m, start.Add(time.Duration(i)*time.Hour),
			start.Add(time.Duration(i+1)*time.Hour), 2*hold, hold) {
			t.Fatalf("label %d rejected", i)
		}
	}
	state := m.Snapshot()
	if !state.TotalBetaReady || state.TotalBeta <= 1.5 || state.TotalBetaUpper < state.TotalBeta {
		t.Fatalf("expected total beta near two with an upper bound: %+v", state)
	}
	utility := state.EvaluateAction(RelativeHoldAction{
		RiskWeight: .25, PairEquityJPY: 100_000, TotalBetaAversion: 2,
	})
	if utility.TotalBetaPenaltyJPYPerHour <= 0 || utility.NetJPYPerHour >= utility.MeanExcessJPYPerHour {
		t.Fatalf("total beta penalty was not applied as one scalar: %+v", utility)
	}
}

func TestRelativeHoldRiskRejectsDuplicateAndOutOfOrderLabels(t *testing.T) {
	m := relativeHoldTestModel()
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	label := RelativeHoldRiskLabel{DecisionAt: start, MaturedAt: start.Add(time.Hour), StrategyReturn: .001, HoldReturn: 0}
	if !m.UpdateLabel(label) {
		t.Fatal("first label rejected")
	}
	if m.UpdateLabel(label) {
		t.Fatal("duplicate maturity timestamp accepted")
	}
	if m.UpdateLabel(RelativeHoldRiskLabel{
		DecisionAt: start.Add(-time.Hour), MaturedAt: start.Add(30 * time.Minute), StrategyReturn: .1,
	}) {
		t.Fatal("out-of-order label accepted")
	}
	if got := m.Snapshot(); got.MaturedLabels != 1 {
		t.Fatalf("duplicate/out-of-order update changed labels: %+v", got)
	}
}

func TestRelativeHoldRiskGapDecaysMomentsAndClearsTail(t *testing.T) {
	m := relativeHoldTestModel()
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if !addRelativeHoldLabel(m, start, start.Add(time.Hour), -.10, -.01) {
		t.Fatal("first label rejected")
	}
	before := m.Snapshot()
	if !addRelativeHoldLabel(m, start.Add(12*time.Hour), start.Add(13*time.Hour), 0, 0) {
		t.Fatal("post-gap label rejected")
	}
	after := m.Snapshot()
	if math.Abs(after.MeanExcessReturn) >= math.Abs(before.MeanExcessReturn) {
		t.Fatalf("EWMA contribution did not decay across gap: before=%f after=%f", before.MeanExcessReturn, after.MeanExcessReturn)
	}
	if after.DownsideCVaR != 0 {
		t.Fatalf("stale CVaR tail survived a gap: %+v", after)
	}
}

func TestRelativeHoldRiskUtilityIsSingleScalarAndCVaRShadowOnly(t *testing.T) {
	m := relativeHoldTestModel()
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	labels := []struct{ strategy, hold float64 }{
		{-.020, -.010}, {-.040, -.020}, {.006, .004}, {.008, .004},
	}
	for i, label := range labels {
		addRelativeHoldLabel(m, start.Add(time.Duration(i)*time.Hour), start.Add(time.Duration(i+1)*time.Hour), label.strategy, label.hold)
	}
	state := m.Snapshot()
	utility := state.EvaluateAction(RelativeHoldAction{
		RiskWeight: 0.5, PairEquityJPY: 100_000,
		TrackingErrorAversion: 2, DownsideBetaAversion: 3,
	})
	if !utility.Ready || utility.NetJPYPerHour >= utility.MeanExcessJPYPerHour {
		t.Fatalf("risk penalties were not included in one utility scalar: %+v", utility)
	}
	if utility.CVaRShadowLossJPY <= 0 {
		t.Fatalf("expected shadow CVaR loss: %+v", utility)
	}
	if math.IsNaN(utility.NetJPYPerHour) || math.IsInf(utility.NetJPYPerHour, 0) {
		t.Fatalf("non-finite utility: %+v", utility)
	}
}

func TestRelativeHoldRiskPrecisionUsesDynamicRequiredEffectiveSamples(t *testing.T) {
	state := RelativeHoldRiskState{
		MeanExcessReturn: 0.001, TrackingVariance: 0.000004,
		EffectiveSamples: 16,
	}
	precision := state.Precision(1.645)
	if precision.StandardError <= 0 || precision.OneSidedLower >= state.MeanExcessReturn {
		t.Fatalf("invalid precision output: %+v", precision)
	}
	wantRequired := 1.645 * 1.645 * state.TrackingVariance /
		(state.MeanExcessReturn * state.MeanExcessReturn)
	if math.Abs(precision.RequiredEffectiveSamples-wantRequired) > 1e-12 {
		t.Fatalf("required effective samples used a fixed gate: got %v want %v", precision.RequiredEffectiveSamples, wantRequired)
	}
	state.MeanExcessReturn = 0
	if got := state.Precision(1.645).RequiredEffectiveSamples; !math.IsInf(got, 1) {
		t.Fatalf("non-positive mean should require infinite evidence: %v", got)
	}
}

func TestRelativeHoldRiskNoNaNOnFlatAndInvalidLabels(t *testing.T) {
	m := relativeHoldTestModel()
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if m.UpdateLabel(RelativeHoldRiskLabel{
		DecisionAt: start, MaturedAt: start.Add(time.Hour), StrategyReturn: math.NaN(), HoldReturn: 0,
	}) {
		t.Fatal("NaN label accepted")
	}
	for i := 0; i < 4; i++ {
		if !addRelativeHoldLabel(m, start.Add(time.Duration(i)*time.Hour), start.Add(time.Duration(i+1)*time.Hour), 0, 0) {
			t.Fatalf("flat label %d rejected", i)
		}
	}
	state := m.Snapshot()
	for _, value := range []float64{state.MeanExcessReturn, state.TrackingVariance, state.TrackingError, state.DownsideBeta, state.DownsideCVaR} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("flat path produced non-finite state: %+v", state)
		}
	}
}

func TestRelativeHoldRiskCheckpointRoundTripPreservesCausalState(t *testing.T) {
	m := relativeHoldTestModel()
	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	for i, label := range []struct{ strategy, hold float64 }{
		{.004, .001}, {-.010, -.006}, {.008, .002},
	} {
		if !addRelativeHoldLabel(m, start.Add(time.Duration(i)*time.Hour), start.Add(time.Duration(i+1)*time.Hour), label.strategy, label.hold) {
			t.Fatalf("label %d rejected", i)
		}
	}
	cp := m.Checkpoint()
	restored := NewRelativeHoldRiskModel(m.config)
	if err := restored.Restore(cp); err != nil {
		t.Fatalf("checkpoint restore failed: %v", err)
	}
	before, after := m.Snapshot(), restored.Snapshot()
	if before != after {
		t.Fatalf("checkpoint changed derived state:\nbefore=%+v\nafter=%+v", before, after)
	}
	next := RelativeHoldRiskLabel{
		DecisionAt: start.Add(3 * time.Hour), MaturedAt: start.Add(4 * time.Hour),
		StrategyReturn: .002, HoldReturn: .001,
	}
	if !m.UpdateLabel(next) || !restored.UpdateLabel(next) {
		t.Fatal("restored model did not accept the same next matured label")
	}
	if got, want := restored.Snapshot(), m.Snapshot(); got != want {
		t.Fatalf("checkpoint continuation diverged:\nrestored=%+v\noriginal=%+v", got, want)
	}
}

func TestRelativeHoldRiskCheckpointRejectsChangedHorizon(t *testing.T) {
	m := relativeHoldTestModel()
	cp := m.Checkpoint()
	changed := NewRelativeHoldRiskModel(RelativeHoldRiskConfig{Horizon: 30 * time.Minute, HalfLife: 4 * time.Hour})
	if err := changed.Restore(cp); err == nil {
		t.Fatal("checkpoint with changed label horizon was accepted")
	}
}

func TestRelativeHoldRiskWarmupRequirementUsesEffectiveSamples(t *testing.T) {
	requirement := RelativeHoldRiskConfig{
		Enabled: true, Horizon: time.Hour, HalfLife: 6 * time.Hour,
		MinimumEffectiveSamples: 4,
	}.WarmupRequirement(5 * time.Minute)
	if !requirement.Feasible || requirement.RequiredLabels != 5 {
		t.Fatalf("unexpected causal warmup requirement: %+v", requirement)
	}
	if math.Abs(requirement.EffectiveSamples-4.870876895870648) > 1e-9 {
		t.Fatalf("unexpected effective samples: %v", requirement.EffectiveSamples)
	}
	if requirement.RequiredDuration != 5*time.Hour+5*time.Minute {
		t.Fatalf("unexpected warmup duration: %s", requirement.RequiredDuration)
	}
	if got := (RelativeHoldRiskConfig{
		Enabled: true, Horizon: time.Hour, HalfLife: time.Hour,
		MinimumEffectiveSamples: 4,
	}).WarmupRequirement(time.Minute); got.Feasible {
		t.Fatalf("impossible EWMA N_eff target was reported feasible: %+v", got)
	}
}

func TestRelativeHoldRiskLiveBridgeUsesExecutableBidAndMaturesLabels(t *testing.T) {
	config := RelativeHoldRiskConfig{
		Enabled: true, Horizon: time.Hour, HalfLife: 6 * time.Hour,
		MinimumEffectiveSamples: 1, MinimumDownsideEffectiveSamples: 1,
	}
	strategy := &Strategy{Config: Config{MarketMaker: MarketMakerConfig{RelativeHoldRisk: config}}}
	strategy.makerRelativeHoldRisk = NewRelativeHoldRiskModel(config)
	t0 := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	strategy.observeRelativeHoldRiskEquity(t0, 100, 1, 100)
	strategy.observeRelativeHoldRiskEquity(t0.Add(time.Hour), 101, .5, 150)
	state := strategy.makerRelativeHoldRisk.Snapshot()
	if state.MaturedLabels != 1 {
		t.Fatalf("expected one matured executable-BBO label: %+v", state)
	}
	// Initial Hold wealth is 100 + 1*101 = 201. Strategy wealth is
	// 150 + .5*101 = 200.5; the negative excess confirms the bridge used the
	// executable bid, not a midpoint or the next ask.
	if state.MeanExcessReturn >= 0 {
		t.Fatalf("expected negative fee-net excess from executable-bid mark: %+v", state)
	}
}

func TestRequiredStartupWarmupIncludesRelativeHoldRequirement(t *testing.T) {
	base := MarketMakerConfig{
		HorizonLookback:       types.Duration(time.Hour),
		FastWindows:           []types.Duration{types.Duration(30 * time.Minute)},
		HorizonUpdateInterval: types.Duration(5 * time.Minute),
	}
	without := base.RequiredStartupWarmup()
	base.RelativeHoldRisk = RelativeHoldRiskConfig{
		Enabled: true, Horizon: time.Hour, HalfLife: 6 * time.Hour,
		MinimumEffectiveSamples: 4,
	}
	with := base.RequiredStartupWarmup()
	if with <= without || with != 5*time.Hour+5*time.Minute {
		t.Fatalf("Relative-Hold warmup was not incorporated: without=%s with=%s", without, with)
	}
}

func TestJointRelativeHoldUtilityIsOneScalarAndShadowSafe(t *testing.T) {
	state := RelativeHoldRiskState{
		Ready: true, DownsideReady: true, MeanExcessReturn: 0.0001,
		TrackingVariance: 0.000004, DownsideHoldVariance: 0.000009,
		DownsideBeta: 1.4, TargetDownsideBeta: 1,
		DownsideCVaR: 0.002,
	}
	in := JointDistanceQuantityInput{
		PairEquityJPY: 100_000,
		RelativeHoldRisk: RelativeHoldRiskInput{
			Enabled: true, TrackingErrorAversion: 1, DownsideBetaAversion: 1, State: state,
		},
	}
	utility := jointRelativeHoldUtility(in, 10_000, 0)
	if utility.NetJPYPerHour >= utility.MeanExcessJPYPerHour || utility.CVaRShadowLossJPY <= 0 {
		t.Fatalf("joint scalar omitted risk or CVaR shadow: %+v", utility)
	}
	in.RelativeHoldRisk.ShadowOnly = true
	if got := jointRelativeHoldUtility(in, 10_000, 0); got.NetJPYPerHour != 0 {
		t.Fatalf("shadow-only state changed joint utility: %+v", got)
	}
}

func TestJointRelativeHoldTotalBetaUsesIncrementalInventoryRisk(t *testing.T) {
	state := RelativeHoldRiskState{
		Ready: true, TotalBetaReady: true, MeanExcessReturn: 0,
		TrackingVariance: 1e-8, TotalHoldVariance: 1e-4,
		TotalBeta: .8, TargetTotalBeta: .5,
	}
	base := JointDistanceQuantityInput{
		PairEquityJPY: 100_000,
		Projection: ProbabilityCenteredQuoteInput{
			CurrentInventoryNotionalJPY: 80_000,
			BuyFillProbability:          .5,
			SellFillProbability:         .5,
		},
		RelativeHoldRisk: RelativeHoldRiskInput{
			Enabled: true, State: state, TotalBetaAversion: 25,
		},
	}
	deRisking := jointRelativeHoldUtility(base, 0, 60_000)
	adding := jointRelativeHoldUtility(base, 60_000, 0)
	if deRisking.TotalBetaPenaltyJPYPerHour >= 0 || adding.TotalBetaPenaltyJPYPerHour <= 0 {
		t.Fatalf("expected incremental beta risk to reward de-risking and penalize adding: de-risking=%+v adding=%+v", deRisking, adding)
	}
}
