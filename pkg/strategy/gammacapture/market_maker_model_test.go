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
		makerDirectionModel: NewDecayedDirectionModel(10 * time.Minute),
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
	if got := s.makerDirectionModel.Snapshot(now.Add(time.Second)); got.PosteriorDirection <= 0 {
		t.Fatalf("live crossing did not advance direction posterior: %+v", got)
	}
}

func TestUpdateMarketMakerModelResetsReferenceAcrossDataGap(t *testing.T) {
	s := &Strategy{
		Config: Config{Symbol: "SOLJPY"},
		State:  &State{Engine: NewCrossingEngine(0.001, 0, 8)},
		model: NewIntensityModel(IntensityConfig{
			Window: types.Duration(time.Hour), PriorAlphaUp: 1, PriorBetaUp: 60,
			PriorAlphaDown: 1, PriorBetaDown: 60, MinEvents: 1,
		}),
	}
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	book := func(price float64) types.BookTicker {
		return types.BookTicker{Symbol: "SOLJPY", Buy: fixedpoint.NewFromFloat(price), Sell: fixedpoint.NewFromFloat(price + 0.01)}
	}
	s.updateMarketMakerModel(now, book(100))
	afterGap := s.updateMarketMakerModel(now.Add(3*time.Minute), book(102))
	if afterGap.Up != 0 || afterGap.Down != 0 {
		t.Fatalf("missing websocket path created phantom crossings: %+v", afterGap)
	}
	afterLiveMove := s.updateMarketMakerModel(now.Add(3*time.Minute+time.Second), book(102.11))
	if afterLiveMove.Up != 1 {
		t.Fatalf("post-gap live path did not resume from the new reference: %+v", afterLiveMove)
	}
}

func TestMakerSubmittedSidePricesDoesNotCreatePhantomSide(t *testing.T) {
	askOnly := []types.SubmitOrder{{Side: types.SideTypeSell, Price: fixedpoint.NewFromInt(307_788)}}
	bid, ask := makerSubmittedSidePrices(askOnly)
	if bid.Sign() != 0 {
		t.Fatalf("ask-only submission created phantom bid %s", bid)
	}
	if ask.Compare(fixedpoint.NewFromInt(307_788)) != 0 {
		t.Fatalf("ask-only submission lost actual ask price: %s", ask)
	}

	bidOnly := []types.SubmitOrder{{Side: types.SideTypeBuy, Price: fixedpoint.NewFromInt(295_000)}}
	bid, ask = makerSubmittedSidePrices(bidOnly)
	if ask.Sign() != 0 {
		t.Fatalf("bid-only submission created phantom ask %s", ask)
	}
	if bid.Compare(fixedpoint.NewFromInt(295_000)) != 0 {
		t.Fatalf("bid-only submission lost actual bid price: %s", bid)
	}
}
