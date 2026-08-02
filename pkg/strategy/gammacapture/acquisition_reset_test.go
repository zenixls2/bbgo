package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func statisticallyStrongAcquisitionInput(now time.Time) AcquisitionResetInput {
	return AcquisitionResetInput{
		Now: now, DeficitSince: now.Add(-12 * time.Minute),
		AnchorMidPrice: 100, MidPrice: 100.25,
		BestAsk: 100.26, BidPrice: 99.95, PlannedAskPrice: 100.65,
		MakerFeeBps: 10, TakerFeeBps: 10, AdverseSelectionBps: 2,
		MaxSlippageBps: 5,
		UpCrosses:      90, DownCrosses: 10,
		UpCrossesPerHour: 54, DownCrossesPerHour: 6,
		QuoteDistanceBps: 30, Horizon: 10 * time.Minute,
		FillIntensityHaircut: 0.25, VolatilityPerSqrtSec: 0.000001,
		EvidenceHealth: HealthHealthy, Return1mBps: 5, Return5mBps: 25,
		Return1mThresholdBps: -5, Return5mThresholdBps: 20, ReturnCalibrationReady: true,
		AdverseMoveThresholdBps: 20,
		DrawdownLimit5mBps:      10, TradeCount5m: 20, BBOCount5m: 20,
	}
}

func TestAcquisitionStartShadowDetectsCausalPatternOnly(t *testing.T) {
	cfg := AcquisitionResetConfig{ShadowStartEnabled: true, StartReturn1mMinBps: -5, StartReturn5mMinBps: 20, StartMinimumTrades5m: 10}
	in := AcquisitionStartInput{InventoryDeficit: true, EvidenceHealth: HealthHealthy, Return1mBps: -4, Return5mBps: 21, DrawdownLimit5mBps: 10, TradeCount5m: 10, BBOCount5m: 20}
	d := cfg.EvaluateStartShadow(in)
	if !d.Signal {
		t.Fatalf("expected causal shadow observation: %+v", d)
	}
	if cfg.Evaluate(statisticallyStrongAcquisitionInput(time.Now())).Trigger {
		t.Fatal("shadow start observation must not authorize IOC execution while acquisition reset is disabled")
	}
}

func TestAcquisitionStartShadowRejectsFalseBreakoutCrash(t *testing.T) {
	cfg := AcquisitionResetConfig{
		ShadowStartEnabled: true, StartReturn1mMinBps: -5, StartReturn5mMinBps: 20,
		StartMinimumTrades5m: 10, StartMinimumBBO5m: 20, StartMaxDrawdown5mBps: 10,
	}
	in := AcquisitionStartInput{
		InventoryDeficit: true, EvidenceHealth: HealthHealthy,
		Return1mBps: 2, Return5mBps: 25, Drawdown5mBps: 12, DrawdownLimit5mBps: 10,
		TradeCount5m: 20, BBOCount5m: 20,
	}
	d := cfg.EvaluateStartShadow(in)
	if d.Signal || d.Reason != "five-minute false-breakout drawdown exceeded" {
		t.Fatalf("recent crash must invalidate a positive endpoint: %+v", d)
	}
}

func TestAcquisitionStartShadowRequiresHealthyEvidence(t *testing.T) {
	cfg := AcquisitionResetConfig{ShadowStartEnabled: true, StartReturn1mMinBps: -5, StartReturn5mMinBps: 20, StartMinimumTrades5m: 10}
	d := cfg.EvaluateStartShadow(AcquisitionStartInput{InventoryDeficit: true, EvidenceHealth: HealthDegraded, Return1mBps: 10, Return5mBps: 30, TradeCount5m: 20})
	if d.Signal || d.Reason != "fast evidence not healthy" {
		t.Fatalf("degraded public evidence must remain observation-ineligible: %+v", d)
	}
}

func TestAcquisitionResetRejectsFalseBreakoutCrash(t *testing.T) {
	now := time.Now()
	in := statisticallyStrongAcquisitionInput(now)
	in.Drawdown5mBps = 12
	cfg := AcquisitionResetConfig{
		Enabled: true, MinDeficitAge: types.Duration(10 * time.Minute), AdverseMoveBps: 20,
		StartReturn1mMinBps: -5, StartReturn5mMinBps: 20,
		StartMinimumTrades5m: 10, StartMinimumBBO5m: 20, StartMaxDrawdown5mBps: 10,
		MinSamples: 30, ConfidenceZScore: 1.645, FillIntensityHaircut: .25, RiskZScore: 1.645,
	}
	d := cfg.Evaluate(in)
	if d.Trigger || d.Reason != "five-minute false-breakout drawdown exceeded" {
		t.Fatalf("anchor gain must not hide a recent false-breakout crash: %+v", d)
	}
}

func TestAcquisitionReturnMoveUsesOneSidedQuantile(t *testing.T) {
	cfg := AcquisitionResetConfig{
		StartMinimumVolatilitySamples5m: 20,
		StartReturnTailProbability:      0.10,
	}
	evidence := FastEvidenceSnapshot{
		MidVolatilityPerSqrtSecond5mBps: 0.5,
		MidVolatilitySamples5m:          20,
		MidVolatilityObservedSeconds5m:  300,
	}
	got, ok := cfg.CalibratedReturnMoveBps(evidence, time.Minute)
	want := math.Sqrt2 * math.Erfcinv(0.20) * 0.5 * math.Sqrt(60)
	if !ok || math.Abs(got-want) > 1e-12 {
		t.Fatalf("unexpected calibrated return move: got=%v ok=%v want=%v", got, ok, want)
	}
	evidence.MidVolatilitySamples5m = 19
	if _, ok := cfg.CalibratedReturnMoveBps(evidence, time.Minute); ok {
		t.Fatal("insufficient real-time variance samples must fail closed")
	}
}

func TestAcquisitionStartFailsClosedWithoutReturnCalibration(t *testing.T) {
	cfg := AcquisitionResetConfig{ShadowStartEnabled: true, StartMinimumTrades5m: 10, StartMinimumBBO5m: 20}
	in := AcquisitionStartInput{
		InventoryDeficit: true, EvidenceHealth: HealthHealthy,
		Return1mBps: 10, Return5mBps: 30, DrawdownLimit5mBps: 10,
		TradeCount5m: 20, BBOCount5m: 20,
	}
	d := cfg.EvaluateStartShadow(in)
	if d.Signal || d.Reason != "return threshold calibration unavailable" {
		t.Fatalf("missing live return calibration must fail closed: %+v", d)
	}
	in.ReturnCalibrationReady = true
	in.Return1mThresholdBps = -4
	in.Return5mThresholdBps = 20
	if d = cfg.EvaluateStartShadow(in); !d.Signal {
		t.Fatalf("calibrated thresholds should authorize shadow observation: %+v", d)
	}
}

func TestAcquisitionDrawdownLimitUsesReflectionPrinciple(t *testing.T) {
	cfg := AcquisitionResetConfig{
		StartMinimumVolatilitySamples5m: 20,
		StartDrawdownTailProbability:    0.10,
	}
	evidence := FastEvidenceSnapshot{
		MidVolatilityPerSqrtSecond5mBps: 0.5,
		MidVolatilitySamples5m:          20,
		MidVolatilityObservedSeconds5m:  300,
	}
	got, ok := cfg.CalibratedDrawdownLimit5mBps(evidence)
	want := math.Sqrt2 * math.Erfcinv(0.10) * 0.5 * math.Sqrt(300)
	if !ok || math.Abs(got-want) > 1e-12 {
		t.Fatalf("unexpected calibrated limit: got=%v ok=%v want=%v", got, ok, want)
	}
	evidence.MidVolatilitySamples5m = 19
	if _, ok := cfg.CalibratedDrawdownLimit5mBps(evidence); ok {
		t.Fatal("insufficient real-time variance samples must fail closed")
	}
}

func TestAcquisitionResetRequiresQuoteDistanceStatistics(t *testing.T) {
	now := time.Now()
	in := statisticallyStrongAcquisitionInput(now)
	in.UpCrosses, in.DownCrosses = 12, 4
	cfg := AcquisitionResetConfig{Enabled: true, MinDeficitAge: types.Duration(10 * time.Minute), AdverseMoveBps: 20, MinSamples: 30, ConfidenceZScore: 1.645, FillIntensityHaircut: .25, RiskZScore: 1.645}
	d := cfg.Evaluate(in)
	if d.Trigger || d.Reason != "insufficient quote-distance crossing statistics" {
		t.Fatalf("unsupported acquisition must fail closed: %+v", d)
	}
}

func TestAcquisitionResetRequiresSignificantUpwardAdvantage(t *testing.T) {
	now := time.Now()
	in := statisticallyStrongAcquisitionInput(now)
	in.UpCrosses, in.DownCrosses = 54, 46
	in.UpCrossesPerHour, in.DownCrossesPerHour = 32.4, 27.6
	cfg := AcquisitionResetConfig{Enabled: true, MinDeficitAge: types.Duration(10 * time.Minute), AdverseMoveBps: 20, MinSamples: 30, ConfidenceZScore: 1.645, FillIntensityHaircut: .25, RiskZScore: 1.645}
	d := cfg.Evaluate(in)
	if d.Trigger || d.Reason != "upward crossing advantage not statistically significant" {
		t.Fatalf("weak directional sample must fail closed: %+v", d)
	}
}

func TestAcquisitionResetRequiresSignificantUpwardIntensity(t *testing.T) {
	now := time.Now()
	in := statisticallyStrongAcquisitionInput(now)
	in.UpCrosses, in.DownCrosses = 62, 38
	in.UpCrossesPerHour, in.DownCrossesPerHour = 37.2, 22.8
	cfg := AcquisitionResetConfig{Enabled: true, MinDeficitAge: types.Duration(10 * time.Minute), AdverseMoveBps: 20, MinSamples: 30, ConfidenceZScore: 1.645, FillIntensityHaircut: .25, RiskZScore: 1.645}
	d := cfg.Evaluate(in)
	if d.Trigger || d.Reason != "upward crossing intensity not statistically significant" {
		t.Fatalf("overlapping Poisson rate bounds must fail closed: %+v", d)
	}
}

func TestAcquisitionResetChargesRoundTripFees(t *testing.T) {
	now := time.Now()
	in := statisticallyStrongAcquisitionInput(now)
	in.PlannedAskPrice = 100.42
	cfg := AcquisitionResetConfig{
		Enabled: true, MinDeficitAge: types.Duration(10 * time.Minute), AdverseMoveBps: 20,
		MinSamples: 30, ConfidenceZScore: 1.645, FillIntensityHaircut: .25,
		RiskZScore: 1.645, MinimumExpectedValueBps: 5, MinimumImprovementBps: 2,
	}
	withFees := cfg.Evaluate(in)
	in.MakerFeeBps, in.TakerFeeBps, in.AdverseSelectionBps = 0, 0, 0
	withoutFees := cfg.Evaluate(in)
	if withFees.Trigger {
		t.Fatalf("fee-negative acquisition must not trigger: %+v", withFees)
	}
	if withoutFees.IOCValueBps-withFees.IOCValueBps < 15 {
		t.Fatalf("round-trip fees must materially reduce expected value: with=%+v without=%+v", withFees, withoutFees)
	}
}

func TestAcquisitionResetTriggersOnConfidenceBoundedPositiveValue(t *testing.T) {
	now := time.Now()
	in := statisticallyStrongAcquisitionInput(now)
	cfg := AcquisitionResetConfig{
		Enabled: true, MinDeficitAge: types.Duration(10 * time.Minute), AdverseMoveBps: 20,
		MinSamples: 30, ConfidenceZScore: 1.645, FillIntensityHaircut: .25,
		RiskZScore: 1.645, MinimumExpectedValueBps: 2, MinimumImprovementBps: 2,
	}
	d := cfg.Evaluate(in)
	if !d.Trigger {
		t.Fatalf("strong fee-positive sample should trigger: %+v", d)
	}
	if d.UpProbabilityLower <= .5 || d.MakerExitFillProbability <= 0 || d.IOCImprovementBps < 2 {
		t.Fatalf("decision must expose positive statistical support: %+v", d)
	}
}
