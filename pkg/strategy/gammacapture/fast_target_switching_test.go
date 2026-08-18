package gammacapture

import (
	"math"
	"testing"
)

func fastTargetSwitchingInput() FastTargetSwitchingInput {
	return FastTargetSwitchingInput{
		CandidateTargetBase: 0.02, PreviousTargetBase: 0.01,
		CurrentInventoryBase: 0.01, HardMinimumBase: 0, HardMaximumBase: 0.03,
		MidPrice: 300_000, PairEquityJPY: 6_000,
		InventoryReturnMeanBps: 40, InventoryReturnPredictiveSD: 30,
		RiskAversion: 1, OneWayExecutionCostBps: 12,
	}
}

func TestFastTargetSwitchingAppliesPositiveNetUtilityChange(t *testing.T) {
	d := EvaluateFastTargetSwitching(
		FastTargetSwitchingConfig{Enabled: true}, fastTargetSwitchingInput())
	if !d.Enabled || !d.Applied || d.SelectedTargetBase != 0.02 || d.NetSwitchValueJPY <= 0 {
		t.Fatalf("expected positive Fast target switch, got %+v", d)
	}
	if d.SelectedNetCertaintyEquivalentJPY <= d.PreviousNetCertaintyEquivalentJPY {
		t.Fatalf("applied switch must improve after-cost CE: got previous=%v selected=%v", d.PreviousNetCertaintyEquivalentJPY, d.SelectedNetCertaintyEquivalentJPY)
	}
	if math.Abs(d.NetSwitchValueJPY-(d.SelectedNetCertaintyEquivalentJPY-d.PreviousNetCertaintyEquivalentJPY)) > 1e-12 {
		t.Fatalf("net switch value must equal after-cost CE delta: %+v", d)
	}
}

func TestFastTargetSwitchingRetainsPreviousTargetWhenCostDominates(t *testing.T) {
	in := fastTargetSwitchingInput()
	in.InventoryReturnMeanBps = 1
	d := EvaluateFastTargetSwitching(FastTargetSwitchingConfig{Enabled: true}, in)
	if d.Applied || d.SelectedTargetBase != in.PreviousTargetBase || d.NetSwitchValueJPY > 0 {
		t.Fatalf("expected cost-aware target retention, got %+v", d)
	}
}

func TestFastTargetSwitchingUsesOptimalPartialAdjustment(t *testing.T) {
	in := fastTargetSwitchingInput()
	in.InventoryReturnMeanBps = 25
	in.InventoryReturnPredictiveSD = 400
	d := EvaluateFastTargetSwitching(FastTargetSwitchingConfig{Enabled: true}, in)
	if !d.Applied || d.SelectedTargetBase <= in.PreviousTargetBase ||
		d.SelectedTargetBase >= in.CandidateTargetBase || d.NetSwitchValueJPY <= 0 {
		t.Fatalf("expected a positive interior Fast target adjustment, got %+v", d)
	}
	if d.Reason != "partial Fast target adjustment maximizes certainty equivalent after switching cost" {
		t.Fatalf("unexpected partial-adjustment reason: %q", d.Reason)
	}
}

func TestFastTargetSwitchingIsSymmetricForRiskReduction(t *testing.T) {
	in := fastTargetSwitchingInput()
	in.CandidateTargetBase, in.PreviousTargetBase = 0.005, 0.02
	in.InventoryReturnMeanBps = -40
	d := EvaluateFastTargetSwitching(FastTargetSwitchingConfig{Enabled: true}, in)
	if !d.Applied || d.SelectedTargetBase != in.CandidateTargetBase || d.NetSwitchValueJPY <= 0 {
		t.Fatalf("expected profitable Fast risk-reduction switch, got %+v", d)
	}
}

func TestFastTargetSwitchingChargesExecutionFromCurrentInventory(t *testing.T) {
	in := fastTargetSwitchingInput()
	// Both targets are below actual inventory. Moving the target upward reduces
	// the required SELL turnover; it must not be charged as a new BUY from the
	// previous unfilled target.
	in.CurrentInventoryBase = 0.03
	in.PreviousTargetBase = 0.005
	in.CandidateTargetBase = 0.015
	in.InventoryReturnMeanBps = -5
	in.InventoryReturnPredictiveSD = 0
	d := EvaluateFastTargetSwitching(FastTargetSwitchingConfig{Enabled: true}, in)
	if !d.Applied || d.SelectedTargetBase != in.CandidateTargetBase || d.NetSwitchValueJPY <= 0 {
		t.Fatalf("actual-inventory execution cost must value reduced SELL turnover: %+v", d)
	}
	wantCost := (in.CurrentInventoryBase - in.CandidateTargetBase) * in.MidPrice *
		in.OneWayExecutionCostBps / 10_000
	if math.Abs(d.SwitchingCostJPY-wantCost) > 1e-12 {
		t.Fatalf("selected execution cost mismatch: got %v want %v", d.SwitchingCostJPY, wantCost)
	}
}

func TestFastTargetSwitchingFailsClosedOnInvalidDistribution(t *testing.T) {
	in := fastTargetSwitchingInput()
	in.InventoryReturnPredictiveSD = -1
	d := EvaluateFastTargetSwitching(FastTargetSwitchingConfig{Enabled: true}, in)
	if d.Applied || d.Reason != "invalid Fast target switching input" {
		t.Fatalf("expected invalid input rejection, got %+v", d)
	}
}
