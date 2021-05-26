package signal

import (
	"context"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/indicator"
	"github.com/c9s/bbgo/pkg/types"
)

const ID = "signal"

var log = logrus.WithField("strategy", ID)

var isBackTestMode = os.Args[1] == "backtest"

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
}

type Strategy struct {
	//*bbgo.Notifiability
	//*bbgo.MarketDataStore
	//types.Market
	Symbol string `json:"symbol"`

	Interval            types.Interval `json:"interval"`
	MovingAverageWindow int            `json:"movingAverageWindow"`
	MinBalanceForOrder  float64        `json:"minBalanceForOrder"`
	StopLossRate        float64        `json:"stopLossRate"`
	prevRaise           bool
	raise               bool
	book                *types.StreamOrderBook
	activeOrders        *bbgo.LocalActiveOrderBook
	stoch               *indicator.STOCH
	price               float64
	hasPosition         bool
	jBuyCount           int
	jSellCount          int
	trendUp             bool
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: string(s.Interval)})
	if !isBackTestMode {
		session.Subscribe(types.BookChannel, s.Symbol, types.SubscribeOptions{})
	}
}

func (s *Strategy) MidPrice() (float64, bool) {
	var best types.PriceVolume
	var ok bool
	sourceBook := s.book.Copy()
	if valid, err := sourceBook.IsValid(); !valid && !isBackTestMode {
		log.WithError(err).Errorf("invalid order book")
		return 0, false
	}
	if best, ok = sourceBook.BestAsk(); !ok {
		log.Error("get best ask failed")
		return 0, false
	}
	askPrice := best.Price.Float64()
	if best, ok = sourceBook.BestBid(); !ok {
		log.Error("get best ask failed")
		return askPrice, true
	}
	bidPrice := best.Price.Float64()
	return (bidPrice + askPrice) / 2., true

}

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	s.trendUp = false
	s.raise = false
	s.prevRaise = false
	s.hasPosition = false
	s.jBuyCount = 0
	s.jSellCount = 0
	if s.StopLossRate == 0 {
		s.StopLossRate = 0.97
	}
	if s.Interval == "" {
		s.Interval = types.Interval5m
	}

	if s.MovingAverageWindow == 0 {
		s.MovingAverageWindow = 99
	}

	market, ok := session.Market(s.Symbol)
	if !ok {
		return fmt.Errorf("market %s is not defined", s.Symbol)
	}

	standardIndicatorSet, ok := session.StandardIndicatorSet(s.Symbol)
	if !ok {
		return fmt.Errorf("standardIndicatorSet is nil, symbol %s", s.Symbol)
	}

	iw := types.IntervalWindow{Interval: s.Interval, Window: s.MovingAverageWindow}
	s.stoch = standardIndicatorSet.STOCH(iw)

	s.book = types.NewStreamBook(s.Symbol)
	s.book.BindStream(session.MarketDataStream)

	s.activeOrders = bbgo.NewLocalActiveOrderBook()
	s.activeOrders.BindStream(session.UserDataStream)

	session.MarketDataStream.OnKLineClosed(func(kline types.KLine) {
		// skip k-lines from other symbols
		if kline.Symbol != s.Symbol {
			return
		}
		balances := session.Account.Balances()
		var order types.SubmitOrder
		var price float64
		var ok bool
		if isBackTestMode {
			if price, ok = session.LastPrice(s.Symbol); !ok {
				log.Error("get last price fail")
				return
			}
		} else {
			if price, ok = s.MidPrice(); !ok {
				log.Error("get mid price fail")
				return
			}
		}
		// stoploss
		if s.hasPosition && kline.Low < s.price*s.StopLossRate && balances[market.BaseCurrency].Available.Float64() > 0 {
			quantity := balances[market.BaseCurrency].Available.Float64()
			if quantity <= s.MinBalanceForOrder {
				log.Warnf("hold quantity less than least sell quantity: %.5f", quantity)
				return
			}
			order = types.SubmitOrder{
				Symbol:      s.Symbol,
				Side:        types.SideTypeSell,
				Type:        types.OrderTypeLimit,
				Market:      market,
				Quantity:    quantity,
				Price:       price,
				TimeInForce: "GTC",
			}
			s.hasPosition = true
			s.price = price
			// clear flags
			s.trendUp = true
			s.jBuyCount = 0
			s.jSellCount = 0
			log.Infof("send order losscut %v", order)
			_, err := orderExecutor.SubmitOrders(ctx, order)
			if err != nil {
				log.WithError(err).Error("submit order error")
			}
			return
		}
		activeOrders := s.activeOrders.Orders()
		if len(activeOrders) > 0 {
			log.Infof("resend orders %v in price %f", activeOrders, price)
			errs := session.Exchange.CancelOrders(context.Background(), activeOrders...)
			for i, err := range errs {
				if err != nil {
					s.activeOrders.Remove(activeOrders[i])
				}
			}
			for _, sorder := range activeOrders {
				var quantity float64
				if sorder.Side == types.SideTypeSell {
					quantity = sorder.Quantity
				} else {
					quantity = sorder.Quantity * sorder.Price / price
				}
				order = types.SubmitOrder{
					Symbol:      s.Symbol,
					Side:        sorder.Side,
					Type:        types.OrderTypeLimit,
					Market:      market,
					Quantity:    quantity,
					Price:       price,
					TimeInForce: "GTC",
				}
				createOrders, err := orderExecutor.SubmitOrders(ctx, order)
				if err != nil {
					log.WithError(err).Error("submit order error")
				} else {
					s.activeOrders.Add(createOrders...)
				}
			}
			return
		}
		var buySignal bool
		// stoploss := false
		//overMode := false

		k := s.stoch.LastK()
		d := s.stoch.LastD()
		j := 3.*k - 2.*d
		s.prevRaise = s.raise
		if k > d {
			s.raise = true
		} else if k < d {
			s.raise = false
		}
		if j > 100 || (k > 80 && d > 80 && j > 80) {
			if s.jBuyCount > 0 {
				s.jSellCount = 0
				s.jBuyCount = 0
			}
			s.jSellCount += 1
		} else if j < 0 || (k < 20 && d < 20 && j < 20) {
			if s.jSellCount > 0 {
				s.jBuyCount = 0
				s.jSellCount = 0
			}
			s.jBuyCount += 1
		} else {
			s.jBuyCount = 0
			s.jSellCount = 0
		}
		if s.jSellCount >= 3 {
			buySignal = false
			//overMode = true
			log.Infof("jsellcount >= 3, sell")
		} else if s.jBuyCount >= 3 {
			buySignal = true
			//overMode = true
			log.Infof("jbuycount >= 3, buy")
		} else if s.prevRaise == s.raise {
			buySignal = s.trendUp
			if buySignal && !(k <= 60 && d <= 60 && j <= 60 && k >= 20 && d >= 20 && j >= 20) {
				log.Infof("kdj not within buy area (trend) %f %f %f", k, d, j)
				return
			}
			if !buySignal && !(k <= 80 && d <= 80 && j <= 80 && k >= 40 && d >= 40 && j >= 40) {
				log.Infof("kdj not within sell area (trend) %f %f %f", k, d, j)
				return
			}
		} else {
			buySignal = !s.prevRaise && s.raise
			if buySignal && !(k <= 60 && d <= 60 && j <= 60 && k >= 20 && d >= 20 && j >= 20) {
				log.Infof("kdj not within buy area (cross) %f %f %f", k, d, j)
				if buySignal != s.trendUp {
					buySignal = s.trendUp
					if buySignal && !(k <= 60 && d <= 60 && j <= 60 && k >= 20 && d >= 20 && j >= 20) {
						log.Infof("kdj not within buy area (trend) %f %f %f", k, d, j)
						return
					}
					if !buySignal && !(k <= 80 && d <= 80 && j <= 80 && k >= 40 && d >= 40 && j >= 40) {
						log.Infof("kdj not within sell area (trend) %f %f %f", k, d, j)
						return
					}
				} else {
					return
				}
			}
			if !buySignal && !(k <= 80 && d <= 80 && j <= 80 && k >= 40 && d >= 40 && j >= 40) {
				log.Infof("kdj not within sell area (cross) %f %f %f", k, d, j)
				if buySignal != s.trendUp {
					buySignal = s.trendUp
					if buySignal && !(k <= 60 && d <= 60 && j <= 60 && k >= 20 && d >= 20 && j >= 20) {
						log.Infof("kdj not within buy area (trend) %f %f %f", k, d, j)
						return
					}
					if !buySignal && !(k <= 80 && d <= 80 && j <= 80 && k >= 40 && d >= 40 && j >= 40) {
						log.Infof("kdj not within sell area (trend) %f %f %f", k, d, j)
						return
					}
				} else {
					return
				}
			}
		}

		// buy
		if buySignal {
			/*if !overMode && !s.hasPosition && price > mid {
				log.Infof("buy no position and price %.5f larger than mid line %.5f, skip", price, mid)
				return
			}*/
			quote := balances[market.QuoteCurrency]
			quoteBalance := quote.Available.Float64()
			quantity := bbgo.AdjustFloatQuantityByMaxAmount(quoteBalance, price, quoteBalance)
			quoteQuantity := quantity * price
			if quoteQuantity <= s.MinBalanceForOrder {
				log.Warnf("hold quantity less than least buy quantity: %.5f", quoteQuantity)
				return
			}
			order = types.SubmitOrder{
				Symbol:      s.Symbol,
				Side:        types.SideTypeBuy,
				Type:        types.OrderTypeLimit,
				Market:      market,
				Quantity:    quantity,
				Price:       price,
				TimeInForce: "GTC",
			}
		} else { // sell
			/*if !overMode && !s.hasPosition && price < mid {
				log.Infof("sell no position and price %.5f less than mid line %.5f, skip", price, mid)
				return
			}*/
			/*if !overMode && s.hasPosition && price < s.price*1.00075 {
				log.Infof("best price %.5f less than position price %.5f, skip", price, s.price)
				return
			}*/
			quantity := balances[market.BaseCurrency].Available.Float64()
			if quantity <= s.MinBalanceForOrder {
				log.Warnf("hold quantity less than least sell quantity: %.5f", quantity)
				return
			}
			order = types.SubmitOrder{
				Symbol:      s.Symbol,
				Side:        types.SideTypeSell,
				Type:        types.OrderTypeLimit,
				Market:      market,
				Quantity:    quantity,
				Price:       price,
				TimeInForce: "GTC",
			}
		}
		s.hasPosition = true
		s.price = price
		// clear flags
		s.trendUp = !buySignal
		s.jBuyCount = 0
		s.jSellCount = 0
		log.Infof("send order %v", order)

		createOrders, err := orderExecutor.SubmitOrders(ctx, order)
		if err != nil {
			log.WithError(err).Error("submit order error")
		} else {
			s.activeOrders.Add(createOrders...)
		}
	})

	return nil
}
