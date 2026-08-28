package binance

import (
	"testing"

	adbinance "github.com/adshao/go-binance/v2"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/stretchr/testify/assert"
)

func TestConvertSubscription_MarkPrice(t *testing.T) {
	cases := []struct {
		name string
		sub  types.Subscription
		want string
	}{
		{
			name: "no interval",
			sub:  types.Subscription{Channel: types.MarkPriceChannel, Symbol: "BTCUSDT"},
			want: "btcusdt@markPrice",
		},
		{
			name: "1s interval",
			sub:  types.Subscription{Channel: types.MarkPriceChannel, Symbol: "BTCUSDT", Options: types.SubscribeOptions{Interval: types.Interval1s}},
			want: "btcusdt@markPrice@1s",
		},
		{
			name: "custom 3s interval string",
			sub:  types.Subscription{Channel: types.MarkPriceChannel, Symbol: "BTCUSDT", Options: types.SubscribeOptions{Interval: types.Interval("3s")}},
			want: "btcusdt@markPrice@3s",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := convertSubscription(c.sub)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestToGlobalMarketPreservesPercentPriceBySide(t *testing.T) {
	symbol := adbinance.Symbol{
		Symbol: "ETHJPY", BaseAsset: "ETH", QuoteAsset: "JPY",
		Filters: []map[string]any{{
			"filterType":        "PERCENT_PRICE_BY_SIDE",
			"avgPriceMins":      float64(5),
			"bidMultiplierUp":   "1.02",
			"bidMultiplierDown": "0.98",
			"askMultiplierUp":   "1.02",
			"askMultiplierDown": "0.98",
		}},
	}
	market := toGlobalMarket(symbol)
	if market.PercentPriceAveragePriceMins != 5 ||
		market.PercentPriceBidMultiplierUp != fixedpoint.MustNewFromString("1.02") ||
		market.PercentPriceAskMultiplierDown != fixedpoint.MustNewFromString("0.98") {
		t.Fatalf("percent-price-by-side filter was not normalized: %+v", market)
	}
}

func TestToGlobalOrderComputesAverageExecutionPrice(t *testing.T) {
	order, err := toGlobalOrder(&adbinance.Order{
		Symbol: "ETHJPY", OrderID: 7, Price: "325281", OrigQuantity: "0.0012",
		ExecutedQuantity: "0.0012", CummulativeQuoteQuantity: "367.3344",
		Status: adbinance.OrderStatusType("FILLED"), Type: adbinance.OrderTypeLimit,
		Side: adbinance.SideTypeBuy, TimeInForce: adbinance.TimeInForceType("IOC"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if order.AveragePrice != fixedpoint.MustNewFromString("306112") {
		t.Fatalf("average execution price not computed from cumulative quote: %+v", order)
	}
}
