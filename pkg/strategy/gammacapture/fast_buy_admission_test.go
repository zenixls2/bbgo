package gammacapture

import (
	"testing"

	"github.com/c9s/bbgo/pkg/types"
)

func TestFastTargetAwareSideAdmissionRemovesNonPositiveValueRegardlessOfTargetGap(t *testing.T) {
	tests := []struct {
		name            string
		side            types.SideType
		current, target float64
	}{
		{name: "BUY below target", side: types.SideTypeBuy, current: 3000, target: 3250},
		{name: "BUY above target", side: types.SideTypeBuy, current: 3300, target: 3250},
		{name: "SELL above target", side: types.SideTypeSell, current: 3500, target: 3250},
		{name: "SELL below target", side: types.SideTypeSell, current: 3200, target: 3250},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := FastTargetAwareSideAdmission(
				test.side, true, test.current, test.target, 100, 500, -0.01)
			if !d.Evaluated || !d.Applied || d.MaximumNotionalJPY != 0 {
				t.Fatalf("unexpected target-aware admission: %+v", d)
			}
		})
	}
}

func TestFastTargetAwareSideAdmissionDoesNotGrantSamplingCell(t *testing.T) {
	d := FastTargetAwareSideAdmission(types.SideTypeBuy, true, 2000, 4000, 100, 1500, -0.01)
	if !d.Evaluated || !d.Applied || d.MaximumNotionalJPY != 0 {
		t.Fatalf("fee-negative BUY must not receive a sampling cell: %+v", d)
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

func TestFastTargetAwareSideAdmissionRequiresStrictlyPositiveValue(t *testing.T) {
	buy := FastTargetAwareSideAdmission(types.SideTypeBuy, true, 3300, 3200, 100, 100, 0)
	if !buy.Applied || buy.MaximumNotionalJPY != 0 {
		t.Fatalf("BUY LCB=0 is not positive evidence for more risky exposure: %+v", buy)
	}
	sell := FastTargetAwareSideAdmission(types.SideTypeSell, true, 3100, 3200, 100, 100, 0)
	if !sell.Applied || sell.MaximumNotionalJPY != 0 {
		t.Fatalf("SELL LCB=0 does not pay its opportunity cost: %+v", sell)
	}
}
