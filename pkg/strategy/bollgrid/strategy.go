package bollgrid

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/indicator"
	"github.com/c9s/bbgo/pkg/types"
)

const ID = "bollgrid"

var log = logrus.WithField("strategy", ID)

func init() {
	// Register the pointer of the strategy struct,
	// so that bbgo knows what struct to be used to unmarshal the configs (YAML or JSON)
	// Note: built-in strategies need to imported manually in the bbgo cmd package.
	bbgo.RegisterStrategy(ID, &Strategy{})
}

type Strategy struct {
	// The notification system will be injected into the strategy automatically.
	// This field will be injected automatically since it's a single exchange strategy.
	*bbgo.Notifiability

	// OrderExecutor is an interface for submitting order.
	// This field will be injected automatically since it's a single exchange strategy.
	bbgo.OrderExecutor

	// if Symbol string field is defined, bbgo will know it's a symbol-based strategy
	// The following embedded fields will be injected with the corresponding instances.

	// MarketDataStore is a pointer only injection field. public trades, k-lines (candlestick)
	// and order book updates are maintained in the market data store.
	// This field will be injected automatically since we defined the Symbol field.
	MarketDataStore *bbgo.MarketDataStore

	// StandardIndicatorSet contains the standard indicators of a market (symbol)
	// This field will be injected automatically since we defined the Symbol field.
	*bbgo.StandardIndicatorSet

	// Graceful let you define the graceful shutdown handler
	*bbgo.Graceful

	// Market stores the configuration of the market, for example, VolumePrecision, PricePrecision, MinLotSize... etc
	// This field will be injected automatically since we defined the Symbol field.
	types.Market

	// These fields will be filled from the config file (it translates YAML to JSON)
	Symbol string `json:"symbol"`

	// Interval is the interval used by the BOLLINGER indicator (which uses K-Line as its source price)
	Interval types.Interval `json:"interval"`

	// RepostInterval is the interval for re-posting maker orders
	RepostInterval types.Interval `json:"repostInterval"`

	// GridPips is the pips of grid
	// e.g., 0.001, so that your orders will be submitted at price like 0.127, 0.128, 0.129, 0.130
	GridPips fixedpoint.Value `json:"gridPips"`

	MinProfitSpread fixedpoint.Value `json:"minProfitSpread"`

	// GridNum is the grid number, how many orders you want to post on the orderbook.
	GridNum int `json:"gridNumber"`

	ROCProfitSpreadMultiplier float64 `json:"rocProfitMultiplier"`

	NumOfKLinesROC int `json:"numOfKLinesROC"`

	// activeOrders is the locally maintained active order book of the maker orders.
	activeOrders *bbgo.LocalActiveOrderBook

	profitOrders *bbgo.LocalActiveOrderBook

	orders *bbgo.OrderStore

	// boll is the BOLLINGER indicator we used for predicting the price.
	boll *indicator.BOLL

	CancelProfitOrdersOnShutdown bool `json: "shutdownCancelProfitOrders"`

	roc float64

	cancelDone chan struct{}

	MinBalanceForOrder float64 `json:"minBalanceForOrder"`

	QuantityUnit float64 `json:"quantityUnit"`

	CancelBadOrders bool `json:"cancelBadOrders"`

	cancelMap map[uint64]struct{}
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) Validate() error {
	if s.MinProfitSpread <= 0 {
		// If profitSpread is empty or its value is negative
		return fmt.Errorf("profit spread should bigger than 0")
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	if s.Interval == "" {
		panic("bollgrid interval can not be empty")
	}

	// currently we need the 1m kline to update the last close price and indicators
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: s.Interval.String()})

	if len(s.RepostInterval) > 0 && s.Interval != s.RepostInterval {
		session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: s.RepostInterval.String()})
	}
}

func (s *Strategy) GetROC() float64 {
	kLines, e := s.MarketDataStore.KLinesOfInterval(s.Interval)
	if !e {
		log.Errorf("cannot find interval")
		return 0
	}
	if len(kLines) == 0 {
		return 0
	}

	var kLine types.KLineWindow
	if len(kLines) < s.NumOfKLinesROC {
		kLine = kLines
	} else {
		kLine = kLines[len(kLines)-s.NumOfKLinesROC:]
	}
	s.roc = s.roc*float64(0.5) + kLine.GetMaxChange()/kLine.GetLow()*float64(0.5)
	return s.roc
}

func (s *Strategy) generateGridBuyOrders(session *bbgo.ExchangeSession) ([]types.SubmitOrder, error) {
	balances := session.Account.Balances()
	quote := balances[s.Market.QuoteCurrency]
	quoteBalance := quote.Available
	if quoteBalance <= 0 {
		return nil, fmt.Errorf("quote balance %s is zero: %f", s.Market.QuoteCurrency, quoteBalance.Float64())
	}
	availableAsset := quote.Available.Float64()
	totalAsset := availableAsset + quote.Locked.Float64()
	if availableAsset < s.MinBalanceForOrder {
		return nil, fmt.Errorf("Asset not enough for posting orders")
	}
	gridNum := math.Floor(float64(s.GridNum) * availableAsset / totalAsset)
	if gridNum < 1 {
		return nil, fmt.Errorf("gridNum == 0, skip")
	}

	upBand, downBand := s.boll.LastUpBand(), s.boll.LastDownBand()
	if upBand <= 0.0 {
		return nil, fmt.Errorf("up band == 0")
	}
	if downBand <= 0.0 {
		return nil, fmt.Errorf("down band == 0")
	}

	currentPrice, ok := session.LastPrice(s.Symbol)
	if !ok {
		return nil, fmt.Errorf("last price not found")
	}

	if currentPrice > upBand || currentPrice < downBand {
		return nil, fmt.Errorf("current price %f exceed the bollinger band %f <> %f", currentPrice, upBand, downBand)
	}

	ema99 := s.StandardIndicatorSet.EWMA(types.IntervalWindow{Interval: s.Interval, Window: 99})
	ema25 := s.StandardIndicatorSet.EWMA(types.IntervalWindow{Interval: s.Interval, Window: 25})
	ema7 := s.StandardIndicatorSet.EWMA(types.IntervalWindow{Interval: s.Interval, Window: 7})
	if ema7.Last() > ema25.Last()*1.001 && ema25.Last() > ema99.Last()*1.0005 {
		log.Infof("all ema lines trend up, skip buy")
		return nil, nil
	}

	priceRange := upBand - downBand
	gridSize := priceRange / float64(s.GridNum)

	var orders []types.SubmitOrder
	gridQuantity := quoteBalance.Float64() / gridNum
	if gridQuantity < s.MinBalanceForOrder {
		gridQuantity = s.MinBalanceForOrder
	}

	quantityUnit := s.QuantityUnit
	for price := upBand; price >= downBand; price -= gridSize {
		if price >= currentPrice {
			continue
		}
		newQuantity := math.Floor(gridQuantity*quantityUnit/price) / quantityUnit
		order := types.SubmitOrder{
			Symbol:      s.Symbol,
			Side:        types.SideTypeBuy,
			Type:        types.OrderTypeLimit,
			Market:      s.Market,
			Quantity:    newQuantity,
			Price:       price,
			TimeInForce: "GTC",
		}
		quoteQuantity := fixedpoint.NewFromFloat(newQuantity).MulFloat64(price)
		if quoteBalance < quoteQuantity {
			if quoteBalance.Float64() < s.MinBalanceForOrder {
				log.Infof("quote balance %f is not enough, stop generating buy orders", quoteBalance.Float64())
				break
			}
			newQuantity = math.Floor(quoteBalance.Float64()*quantityUnit/price) / quantityUnit
			order.Quantity = newQuantity
			orders = append(orders, order)
			quoteBalance = quoteBalance.Sub(fixedpoint.NewFromFloat(newQuantity).MulFloat64(price))
			log.Infof("submitting order: %s", order.String())
			break
		} else {
			quoteBalance = quoteBalance.Sub(quoteQuantity)
		}
		log.Infof("submitting order: %s", order.String())
		orders = append(orders, order)
	}
	return orders, nil
}

func (s *Strategy) generateGridSellOrders(session *bbgo.ExchangeSession) ([]types.SubmitOrder, error) {
	balances := session.Account.Balances()
	base := balances[s.Market.BaseCurrency]
	baseBalance := base.Available
	if baseBalance <= 0 {
		return nil, fmt.Errorf("base balance %s is zero: %+v", s.Market.BaseCurrency, baseBalance.Float64())
	}
	availableAsset := base.Available.Float64()
	totalAsset := availableAsset + base.Locked.Float64()

	gridNum := math.Floor(float64(s.GridNum) * availableAsset / totalAsset)
	if gridNum < 1 {
		return nil, fmt.Errorf("gridNum == 0, skip")
	}

	upBand, downBand := s.boll.LastUpBand(), s.boll.LastDownBand()
	if upBand <= 0.0 {
		return nil, fmt.Errorf("up band == 0")
	}
	if downBand <= 0.0 {
		return nil, fmt.Errorf("down band == 0")
	}

	currentPrice, ok := session.LastPrice(s.Symbol)
	if !ok {
		return nil, fmt.Errorf("last price not found")
	}
	if availableAsset*currentPrice < s.MinBalanceForOrder {
		return nil, fmt.Errorf("Asset not enough for posting orders: %f", availableAsset)
	}

	if currentPrice > upBand || currentPrice < downBand {
		return nil, fmt.Errorf("current price exceed the bollinger band")
	}

	ema99 := s.StandardIndicatorSet.EWMA(types.IntervalWindow{Interval: s.Interval, Window: 99})
	ema25 := s.StandardIndicatorSet.EWMA(types.IntervalWindow{Interval: s.Interval, Window: 25})
	ema7 := s.StandardIndicatorSet.EWMA(types.IntervalWindow{Interval: s.Interval, Window: 7})
	if ema7.Last() < ema25.Last()*(1-0.004) && ema25.Last() < ema99.Last()*(1-0.0005) {
		log.Infof("all ema lines trend down, skip sell")
		return nil, nil
	}

	priceRange := upBand - downBand
	gridSize := priceRange / float64(s.GridNum)

	var orders []types.SubmitOrder

	gridQuantity := math.Floor(baseBalance.Float64()*s.QuantityUnit/gridNum) / s.QuantityUnit
	if gridQuantity*currentPrice < s.MinBalanceForOrder {
		gridQuantity = math.Floor(s.MinBalanceForOrder*s.QuantityUnit/currentPrice) / s.QuantityUnit
	}

	for price := downBand; price <= upBand; price += gridSize {
		if price <= currentPrice {
			continue
		}
		order := types.SubmitOrder{
			Symbol:      s.Symbol,
			Side:        types.SideTypeSell,
			Type:        types.OrderTypeLimit,
			Market:      s.Market,
			Quantity:    gridQuantity,
			Price:       price,
			TimeInForce: "GTC",
		}
		baseQuantity := fixedpoint.NewFromFloat(gridQuantity)
		if baseBalance < baseQuantity {
			log.Infof("base balance %f is not enough, stop generating sell orders", baseBalance.Float64())
			break
		} else {
			baseBalance = baseBalance.Sub(baseQuantity)
		}
		log.Infof("submitting order: %s", order.String())
		orders = append(orders, order)
	}
	return orders, nil
}

func (s *Strategy) placeGridOrders(orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) {
	sellOrders, err := s.generateGridSellOrders(session)
	if err != nil {
		log.Warn(err.Error())
	}

	buyOrders, err := s.generateGridBuyOrders(session)
	if err != nil {
		log.Warn(err.Error())
	}

	allOrders := append(buyOrders, sellOrders...)

	if len(allOrders) > 0 {
		log.Infof("all orders %v", allOrders)
		createdOrders, err := orderExecutor.SubmitOrders(context.Background(), allOrders...)
		if err != nil {
			log.WithError(err).Errorf("can not place orders")
		}

		s.activeOrders.Add(createdOrders...)
		s.orders.Add(createdOrders...)
	}
}

func (s *Strategy) cancelWait(session *bbgo.ExchangeSession, orders ...types.Order) {
	for _, order := range orders {
		s.cancelMap[order.OrderID] = struct{}{}
	}
	errs := session.Exchange.CancelOrders(context.Background(), orders...)
	for i, err := range errs {
		if err != nil {
			delete(s.cancelMap, orders[i].OrderID)
			log.WithError(err).Errorf("cancel order error")
		} else {
			<-s.cancelDone
		}
	}
}

func (s *Strategy) updateOrders(orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) {
	activeOrders := s.activeOrders.Orders()
	if len(activeOrders) > 0 {
		log.Infof("before update: start cancelling orders %v", activeOrders)
		s.cancelWait(session, activeOrders...)
		log.Infof("before update: canceled all orders")
	}

	// skip order updates if up-band - down-band < min profit spread
	upBand := s.boll.LastUpBand()
	downBand := s.boll.LastDownBand()
	currentPrice, ok := session.LastPrice(s.Symbol)
	if !ok {
		log.Errorf("last price not found")
		return
	}
	spread := (upBand - downBand) / currentPrice
	roc := s.GetROC()
	dynamicSpread := math.Max(s.MinProfitSpread.Float64(), roc*s.ROCProfitSpreadMultiplier)
	log.Infof("dynamicSpread %f", dynamicSpread)
	if spread <= dynamicSpread {
		log.Infof("boll: band spread %f too small, skipping %f...", spread, dynamicSpread)
		return
	}
	if s.CancelBadOrders {
		profitOrders := s.cancelBadOrders(session)
		if len(profitOrders) > 0 {
			log.Infof("cancel bad orders %v", profitOrders)
			s.cancelWait(session, profitOrders...)
			log.Infof("cancel bad orders done")
		}
	}

	s.placeGridOrders(orderExecutor, session)

	s.activeOrders.Print()
}

func (s *Strategy) cancelBadOrders(session *bbgo.ExchangeSession) types.OrderSlice {
	needCancelOrders := make(types.OrderSlice, 0)
	/*bandWidth := 7.
		sma := s.boll.LastSMA()
		std := s.boll.LastStdDev()
	    band := std * bandWidth*/
	currentPrice, ok := session.LastPrice(s.Symbol)
	if !ok {
		log.Errorf("last price not found")
		return needCancelOrders
	}
	bigUpBand, bigDownBand := currentPrice*1.1, currentPrice*0.91

	for _, order := range s.profitOrders.Orders() {
		if order.Price > bigUpBand || order.Price < bigDownBand {
			needCancelOrders = append(needCancelOrders, order)
		}
	}
	return needCancelOrders
}

func (s *Strategy) submitReverseOrder(order types.Order, session *bbgo.ExchangeSession) {
	balances := session.Account.Balances()
	var side = order.Side.Reverse()
	var price = order.Price
	var quantity = order.Quantity

	currentPrice, ok := session.LastPrice(s.Symbol)
	if !ok {
		log.Errorf("last price not found")
		return
	}
	roc := s.GetROC()
	dynamicSpread := math.Max(s.MinProfitSpread.Float64(), roc*s.ROCProfitSpreadMultiplier)
	switch side {
	case types.SideTypeSell:
		price += dynamicSpread * currentPrice
		maxQuantity := balances[s.Market.BaseCurrency].Available.Float64()
		quantity = math.Min(quantity, maxQuantity)

	case types.SideTypeBuy:
		price -= dynamicSpread * currentPrice
		maxQuantity := balances[s.Market.QuoteCurrency].Available.Float64() / price
		quantity = math.Min(quantity, maxQuantity)
	}

	submitOrder := types.SubmitOrder{
		Symbol:      s.Symbol,
		Side:        side,
		Type:        types.OrderTypeLimit,
		Quantity:    quantity,
		Price:       price,
		TimeInForce: "GTC",
	}

	log.Infof("submitting reverse order: %s against %s", submitOrder.String(), order.String())

	createdOrders, err := s.OrderExecutor.SubmitOrders(context.Background(), submitOrder)
	if err != nil {
		log.WithError(err).Errorf("can not place orders")
		return
	}

	s.profitOrders.Add(createdOrders...)
	s.orders.Add(createdOrders...)
}

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	if s.GridNum == 0 {
		s.GridNum = 2
	}

	s.boll = s.StandardIndicatorSet.BOLL(types.IntervalWindow{
		Interval: s.Interval,
		Window:   21,
	}, 2.0)

	s.orders = bbgo.NewOrderStore(s.Symbol)
	s.orders.BindStream(session.UserDataStream)
	s.cancelMap = make(map[uint64]struct{})

	// we don't persist orders so that we can not clear the previous orders for now. just need time to support this.
	s.activeOrders = bbgo.NewLocalActiveOrderBook()
	s.activeOrders.OnFilled(func(o types.Order) {
		//s.submitReverseOrder(o, session)
	})
	s.activeOrders.BindStream(session.UserDataStream)

	s.profitOrders = bbgo.NewLocalActiveOrderBook()
	s.profitOrders.OnFilled(func(o types.Order) {
		// we made profit here!
	})
	s.profitOrders.BindStream(session.UserDataStream)

	c := make(chan struct{}, 1000)
	// setup graceful shutting down handler
	s.Graceful.OnShutdown(func(ctx context.Context, wg *sync.WaitGroup) {
		// call Done to notify the main process.
		defer wg.Done()

		activeOrders := s.activeOrders.Orders()

		log.Infof("on_shutdown: canceling active orders... %v", activeOrders)

		s.cancelWait(session, activeOrders...)

		log.Infof("on_shutdown: cancel active orders done")

		if s.CancelProfitOrdersOnShutdown {
			profitOrders := s.profitOrders.Orders()

			log.Infof("canceling profit orders...%v", profitOrders)

			s.cancelWait(session, profitOrders...)

			log.Infof("on_shutdown: cancel profit orders done")
		}
	})

	session.UserDataStream.OnStart(func() {
		log.Infof("connected, submitting the first round of the orders")
		s.updateOrders(orderExecutor, session)
	})

	session.Stream.OnOrderUpdate(func(o types.Order) {
		if _, ok := s.cancelMap[o.OrderID]; !ok {
			return
		}
		log.Infof("order update OrderID: %v %v", o.OrderID, o)
		switch o.Status {
		case types.OrderStatusCanceled:
			delete(s.cancelMap, o.OrderID)
			c <- struct{}{}
		case types.OrderStatusFilled:
			delete(s.cancelMap, o.OrderID)
		case types.OrderStatusRejected:
			if _, ok := s.cancelMap[o.OrderID]; ok {
				delete(s.cancelMap, o.OrderID)
				c <- struct{}{}
			}
		}
	})
	s.cancelDone = c

	// avoid using time ticker since we will need back testing here
	session.MarketDataStream.OnKLineClosed(func(kline types.KLine) {
		// skip kline events that does not belong to this symbol
		if kline.Symbol != s.Symbol {
			log.Infof("%s != %s", kline.Symbol, s.Symbol)
			return
		}

		if s.RepostInterval != "" {
			// see if we have enough balances and then we create limit orders on the up band and the down band.
			if s.RepostInterval == kline.Interval {
				s.updateOrders(orderExecutor, session)
			}

		} else if s.Interval == kline.Interval {
			s.updateOrders(orderExecutor, session)
		}
	})

	return nil
}
