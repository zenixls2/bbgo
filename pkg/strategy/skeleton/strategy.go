package skeleton

import (
	"context"

	"github.com/sirupsen/logrus"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/signals"
	"github.com/c9s/bbgo/pkg/types"
)

const ID = "skeleton"

var log = logrus.WithField("strategy", ID)
var sigs *types.Signals

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
	sigs = signals.New()
}

type Strategy struct {
	Symbol string `json:"symbol"`
	sig    types.Signal
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	log.Infof("subscribe %s", s.Symbol)
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: "1m"})
	sig, _ := sigs.FindByName("miningHamster")
	s.sig = sig

}

var Ten = fixedpoint.NewFromInt(10)

// This strategy simply spent all available quote currency to buy the symbol whenever kline gets closed
func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	market, ok := session.Market(s.Symbol)
	if !ok {
		log.Warnf("fetch market fail %s", s.Symbol)
		return nil
	}
	callback := func(kline types.KLine) {
		quoteBalance, ok := session.Account.Balance(market.QuoteCurrency)
		if !ok {
			return
		}
		quantityAmount := quoteBalance.Available
		if quantityAmount.Sign() <= 0 || quantityAmount.Compare(Ten) < 0 {
			return
		}

		currentPrice, ok := session.LastPrice(s.Symbol)
		if !ok {
			return
		}

		totalQuantity := quantityAmount.Div(currentPrice)

		_, err := orderExecutor.SubmitOrders(ctx, types.SubmitOrder{
			Symbol:   kline.Symbol,
			Side:     types.SideTypeBuy,
			Type:     types.OrderTypeMarket,
			Price:    currentPrice,
			Quantity: totalQuantity,
		})

		if err != nil {
			log.WithError(err).Error("submit order error")
		}
	}
	session.UserDataStream.OnStart(func() {
		log.Infof("connected")
	})

	session.MarketDataStream.OnKLineClosed(callback)

	return nil
}
