package types

import (
	"strings"
	"testing"

	"github.com/c9s/bbgo/pkg/fixedpoint"
)

func TestOrderStringShowsAverageFillWhenLimitDiffers(t *testing.T) {
	o := Order{SubmitOrder: SubmitOrder{
		Symbol: "ETHJPY", Side: SideTypeBuy, Type: OrderTypeLimit,
		Price:        fixedpoint.MustNewFromString("325281"),
		Quantity:     fixedpoint.MustNewFromString("0.0012"),
		AveragePrice: fixedpoint.MustNewFromString("306112"),
	}, ExecutedQuantity: fixedpoint.MustNewFromString("0.0012")}
	if got := o.String(); !strings.Contains(got, "@ 325281 (avg-fill 306112)") {
		t.Fatalf("order log must distinguish limit from execution price: %s", got)
	}
}
