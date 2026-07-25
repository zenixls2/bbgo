package gammacapture

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestUpdateMarketMakerModelConsumesBookTickerEvents(t *testing.T) {
	s := &Strategy{
		Config: Config{Symbol: "SOLJPY"},
		State:  &State{Engine: NewCrossingEngine(0.001, 0, 1)},
		model: NewIntensityModel(IntensityConfig{
			Window:         types.Duration(time.Hour),
			PriorAlphaUp:   1,
			PriorBetaUp:    60,
			PriorAlphaDown: 1,
			PriorBetaDown:  60,
			MinEvents:      1,
		}),
	}
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	ticker := types.BookTicker{
		Symbol: "SOLJPY", Buy: fixedpoint.NewFromFloat(100), Sell: fixedpoint.NewFromFloat(100.01),
		BuySize: fixedpoint.NewFromFloat(1), SellSize: fixedpoint.NewFromFloat(1),
	}

	s.updateMarketMakerModel(now, ticker)
	if got := s.updateMarketMakerModel(now.Add(time.Second), types.BookTicker{
		Symbol: "SOLJPY", Buy: fixedpoint.NewFromFloat(100.11), Sell: fixedpoint.NewFromFloat(100.12),
		BuySize: fixedpoint.NewFromFloat(1), SellSize: fixedpoint.NewFromFloat(1),
	}); got.Up != 1 {
		t.Fatalf("expected one live upward crossing, got snapshot=%+v", got)
	}
	if s.State.LastReferenceTime != now.Add(time.Second) {
		t.Fatalf("model reference timestamp was not advanced: %s", s.State.LastReferenceTime)
	}
}
