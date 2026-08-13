package gammacapture

import (
	"math"
	"testing"

	"github.com/c9s/bbgo/pkg/types"
)

func TestFastTargetAwareSideAdmissionCannotCrossTarget(t *testing.T) {
	tests := []struct {
		name                     string
		side                     types.SideType
		current, target, wantMax float64
	}{
		{name: "BUY is capped at target deficit", side: types.SideTypeBuy, current: 3000, target: 3250, wantMax: 250},
		{name: "BUY above target is zero", side: types.SideTypeBuy, current: 3300, target: 3250, wantMax: 0},
		{name: "SELL caps at excess", side: types.SideTypeSell, current: 3500, target: 3250, wantMax: 250},
		{name: "SELL below target is zero", side: types.SideTypeSell, current: 3200, target: 3250, wantMax: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := FastTargetAwareSideAdmission(
				test.side, true, test.current, test.target, 100, 500, -0.01)
			if !d.Evaluated || !d.Applied || d.MaximumNotionalJPY != test.wantMax {
				t.Fatalf("unexpected target-aware admission: %+v", d)
			}
		})
	}
}

func TestFastTargetAwareSideAdmissionLeavesBearishOneCellCapToPathModel(t *testing.T) {
	d := FastTargetAwareSideAdmission(types.SideTypeBuy, true, 2000, 4000, 100, 1500, -0.01)
	if !d.Evaluated || d.Applied || d.MaximumNotionalJPY != 1500 {
		t.Fatalf("target admission must not duplicate the bearish path cap: %+v", d)
	}
}

func TestFastTargetAwareSideAdmissionRejectsSubMinimumRemainder(t *testing.T) {
	for _, test := range []struct {
		side            types.SideType
		current, target float64
	}{
		{side: types.SideTypeBuy, current: 3180, target: 3250},
		{side: types.SideTypeSell, current: 3320, target: 3250},
	} {
		d := FastTargetAwareSideAdmission(
			test.side, true, test.current, test.target, 100, 500, -0.01)
		if !d.Applied || d.MaximumNotionalJPY != 0 {
			t.Fatalf("sub-minimum target remainder must not leak an order: %+v", d)
		}
	}
}

func TestFastTargetAwareSideAdmissionPreservesPositiveMarginalUtility(t *testing.T) {
	for _, side := range []types.SideType{types.SideTypeBuy, types.SideTypeSell} {
		d := FastTargetAwareSideAdmission(side, true, 3300, 3200, 100, 300, 0.01)
		if !d.Evaluated || d.Applied || d.MaximumNotionalJPY != 300 {
			t.Fatalf("positive marginal utility changed on %s: %+v", side, d)
		}
	}
}

func TestFastTargetAwareSideAdmissionFailsOpenWhenUtilityUnavailable(t *testing.T) {
	for _, side := range []types.SideType{types.SideTypeBuy, types.SideTypeSell} {
		d := FastTargetAwareSideAdmission(side, false, 3300, 3200, 100, 300, 0)
		if d.Evaluated || d.Applied || d.MaximumNotionalJPY != 300 {
			t.Fatalf("unavailable utility must preserve %s: %+v", side, d)
		}
	}
}

func TestFastTargetAwareSideAdmissionUsesLongOnlyZeroBoundary(t *testing.T) {
	buy := FastTargetAwareSideAdmission(types.SideTypeBuy, true, 3300, 3200, 100, 100, 0)
	if !buy.Applied || buy.MaximumNotionalJPY != 0 {
		t.Fatalf("BUY LCB=0 is not positive evidence for more risky exposure: %+v", buy)
	}
	sell := FastTargetAwareSideAdmission(types.SideTypeSell, true, 3100, 3200, 100, 100, 0)
	if sell.Applied || sell.MaximumNotionalJPY != 100 {
		t.Fatalf("SELL UCB=0 is not negative evidence of harm: %+v", sell)
	}
}

func TestFastDirectionScaledBuyAdmissionUsesPosteriorAdvantage(t *testing.T) {
	base := FastTargetAwareSideAdmission(
		types.SideTypeBuy, true, 2000, 2500, 100, 500, -0.01)
	for _, test := range []struct {
		name      string
		direction float64
		want      float64
	}{
		{name: "neutral keeps one cell", direction: 0, want: 100},
		{name: "forty percent advantage", direction: 0.4, want: 260},
		{name: "certain up preserves target", direction: 1, want: 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := FastDirectionScaledBuyAdmission(base, test.direction, 100, 500)
			if math.Abs(got.MaximumNotionalJPY-test.want) > 1e-12 {
				t.Fatalf("unexpected posterior-scaled BUY cap: got=%+v want=%v", got, test.want)
			}
		})
	}
}
