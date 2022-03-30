package vegaschannel

import (
	"context"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/c9s/bbgo/pkg/indicator"
)

const ID = "vegaschannel"
var log = logrus.WithField("strategy", ID)

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
}

type Strategy struct {
	*bbgo.Graceful
	*bbgo.Persistence

	Position *types.Position `json:"position,omitempty"`
	Environment *bbgo.Environment
	StandardIndicatorSet *bbgo.StandardIndicatorSet
	Market types.Market

	Symbol string `json:"symbol"`

	Interval types.Interval `json:"interval"`

	ema12 *indicator.EWMA
	ema144 *indicator.EWMA
	ema169 *indicator.EWMA
	ema576 *indicator.EWMA
	ema676 *indicator.EWMA

	lastTrend indicator.Trend

	activeOrders *bbgo.LocalActiveOrderBook
	tradeCollector *bbgo.TradeCollector
	bbgo.SmartStops
	orderStore *bbgo.OrderStore
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) Initialize() error {
	s.lastTrend = indicator.TrendNeutral
	if s.Position == nil {
		s.Position = types.NewPositionFromMarket(s.Market)
	}
	return s.SmartStops.InitializeStopControllers(s.Symbol)
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{
		Interval: string(s.Interval),
	})

	s.SmartStops.Subscribe(session)
}

var two = fixedpoint.NewFromInt(2)

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {

	instanceID := fmt.Sprintf("%s-%s", ID, s.Symbol)
	s.Position.Strategy = ID
	s.Position.StrategyInstanceID = instanceID

	s.activeOrders = bbgo.NewLocalActiveOrderBook(s.Symbol)
	s.activeOrders.BindStream(session.UserDataStream)
	s.orderStore = bbgo.NewOrderStore(s.Symbol)
	s.orderStore.BindStream(session.UserDataStream)
	s.tradeCollector = bbgo.NewTradeCollector(s.Symbol, s.Position, s.orderStore)
	s.tradeCollector.BindStream(session.UserDataStream)
	s.tradeCollector.OnTrade(func(trade types.Trade, profit, netprofit fixedpoint.Value) {
		if profit.IsZero() {
			s.Environment.RecordPosition(s.Position, trade, nil)
		} else {
			log.Infof("%s generated profit: %v", s.Symbol, profit)
			p := s.Position.NewProfit(trade, profit, netprofit)
			p.Strategy = ID
			p.StrategyInstanceID = instanceID
			s.Environment.RecordPosition(s.Position, trade, &p)
		}
	})
	s.SmartStops.RunStopControllers(ctx, session, s.tradeCollector)
	s.ema12 = s.StandardIndicatorSet.EWMA(types.IntervalWindow{
		Interval: s.Interval, Window: 12})
	s.ema144 = s.StandardIndicatorSet.EWMA(types.IntervalWindow{
		Interval: s.Interval, Window: 144})
	s.ema169 = s.StandardIndicatorSet.EWMA(types.IntervalWindow{
		Interval: s.Interval, Window: 169})
	s.ema576 = s.StandardIndicatorSet.EWMA(types.IntervalWindow{
		Interval: s.Interval, Window: 576})
	s.ema676 = s.StandardIndicatorSet.EWMA(types.IntervalWindow{
		Interval: s.Interval, Window: 676})


	session.MarketDataStream.OnKLineClosed(func(kline types.KLine) {
		s.tradeCollector.Process()
		//ticker, err := s.session.Exchange.QueryTicker(ctx, s.Symbol)
		//midPrice := ticker.Buy.Add(ticker.Sell).Div(two)
		midPrice, ok := session.LastPrice(s.Symbol)
		if !ok {
			log.Errorf("cannot get last price")
			return
		}

		p12 := s.ema12.Last()
		p144 := s.ema144.Last()
		p169 := s.ema169.Last()
		mp := midPrice.Float64()
		if  mp > p144 && mp > p169 && p12 > p144 && p12 > p169 &&
			s.ema576.Trend() == indicator.TrendUp &&
			s.ema676.Trend() == indicator.TrendUp {
			if s.lastTrend == indicator.TrendDown || s.lastTrend == indicator.TrendNeutral { // buy now
				if err := s.activeOrders.GracefulCancel(ctx, session.Exchange); err != nil {
					log.WithError(err).Errorf("graceful cancel order error")
				}
				balances := session.Account.Balances()
				quoteBalance, hasQuoteBalance :=  balances[s.Market.QuoteCurrency]
				if !hasQuoteBalance {
					s.lastTrend = indicator.TrendUp
					return
				}
				totalQuantity := quoteBalance.Available.Div(midPrice)
				if quoteBalance.Available.Compare(s.Market.MinNotional) < 0 {
					s.lastTrend = indicator.TrendUp
					return
				}
				if totalQuantity.Compare(s.Market.MinQuantity) < 0 {
					s.lastTrend = indicator.TrendUp
					return
				}
				buyOrder := types.SubmitOrder{
					Symbol: s.Symbol,
					Side: types.SideTypeBuy,
					Type: types.OrderTypeMarket,
					Quantity: totalQuantity,
					Price: midPrice,
					Market: s.Market,
				}
				createdOrders, err := orderExecutor.SubmitOrders(ctx, buyOrder)
				if err != nil {
					log.WithError(err).Errorf("cannot place order")
					return
				}
				s.orderStore.Add(createdOrders...)
				s.activeOrders.Add(createdOrders...)
				s.lastTrend = indicator.TrendUp
			}
		} else if mp < p144 && mp < p169 && p12 < p144 && p12 < p169 &&
			s.ema576.Trend() == indicator.TrendDown &&
			s.ema676.Trend() == indicator.TrendDown {
			if s.lastTrend == indicator.TrendUp || s.lastTrend == indicator.TrendNeutral { // sell now
				if err := s.activeOrders.GracefulCancel(ctx, session.Exchange); err != nil {
					log.WithError(err).Errorf("graceful cancel order error")
				}
				balances := session.Account.Balances()
				baseBalance, hasBaseBalance := balances[s.Market.BaseCurrency]
				if !hasBaseBalance {
					s.lastTrend = indicator.TrendDown
					return
				}
				totalQuantity := baseBalance.Available.Div(midPrice)
				if totalQuantity.Compare(s.Market.MinQuantity) < 0 {
					s.lastTrend = indicator.TrendDown
					return
				}
				if baseBalance.Available.Compare(s.Market.MinNotional) < 0 {
					s.lastTrend = indicator.TrendDown
					return
				}
				sellOrder := types.SubmitOrder {
					Symbol: s.Symbol,
					Side: types.SideTypeSell,
					Type: types.OrderTypeMarket,
					Quantity: totalQuantity,
					Price: midPrice,
					Market: s.Market,
				}
				createdOrders, err := orderExecutor.SubmitOrders(ctx, sellOrder)
				if err != nil {
					log.WithError(err).Errorf("cannot place order")
					return
				}
				s.orderStore.Add(createdOrders...)
				s.activeOrders.Add(createdOrders...)
				s.lastTrend = indicator.TrendDown
			}
		} else {
			s.lastTrend = indicator.TrendNeutral
		}
	})

	s.Graceful.OnShutdown(func(ctx context.Context, wg *sync.WaitGroup) {
		defer wg.Done()
		if err := s.activeOrders.GracefulCancel(ctx, session.Exchange); err != nil {
			log.WithError(err).Errorf("graceful cancel order error")
		}

		s.tradeCollector.Process()
	})

	return nil
}
