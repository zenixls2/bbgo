package backtest

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/exchange/binance"
	"github.com/c9s/bbgo/pkg/exchange/max"
	"github.com/c9s/bbgo/pkg/service"
	"github.com/c9s/bbgo/pkg/types"
)

var ErrUnimplemented = errors.New("unimplemented method")

type Exchange struct {
	sourceName         types.ExchangeName
	publicExchange     types.Exchange
	srv                *service.BacktestService
	startTime, endTime time.Time

	account *types.Account
	config  *bbgo.Backtest

	userDataStream *Stream

	trades        map[string][]types.Trade
	closedOrders  map[string][]types.Order
	matchingBooks map[string]*SimplePriceMatching
	markets       types.MarketMap
	doneC         chan struct{}
}

func NewExchange(sourceName types.ExchangeName, srv *service.BacktestService, config *bbgo.Backtest) *Exchange {
	ex, err := newPublicExchange(sourceName)
	if err != nil {
		panic(err)
	}

	if config == nil {
		panic(errors.New("backtest config can not be nil"))
	}

	markets, err := bbgo.LoadExchangeMarketsWithCache(context.Background(), ex)
	if err != nil {
		panic(err)
	}

	startTime, err := config.ParseStartTime()
	if err != nil {
		panic(err)
	}

	endTime, err := config.ParseEndTime()
	if err != nil {
		panic(err)
	}

	account := &types.Account{
		MakerCommission: config.Account.MakerCommission,
		TakerCommission: config.Account.TakerCommission,
		AccountType:     "SPOT", // currently not used
	}

	balances := config.Account.Balances.BalanceMap()
	account.UpdateBalances(balances)

	e := &Exchange{
		sourceName:     sourceName,
		publicExchange: ex,
		markets:        markets,
		srv:            srv,
		config:         config,
		account:        account,
		startTime:      startTime,
		endTime:        endTime,
		matchingBooks:  make(map[string]*SimplePriceMatching),
		closedOrders:   make(map[string][]types.Order),
		trades:         make(map[string][]types.Trade),
		doneC:          make(chan struct{}),
	}

	return e
}

func (e *Exchange) Done() chan struct{} {
	return e.doneC
}

func (e *Exchange) NewStream() types.Stream {
	return &Stream{exchange: e}
}

func (e Exchange) SubmitOrders(ctx context.Context, orders ...types.SubmitOrder) (createdOrders types.OrderSlice, err error) {
	for _, order := range orders {
		symbol := order.Symbol
		matching, ok := e.matchingBooks[symbol]
		if !ok {
			return nil, fmt.Errorf("matching engine is not initialized for symbol %s", symbol)
		}

		createdOrder, trade, err := matching.PlaceOrder(order)
		if err != nil {
			return nil, err
		}

		if createdOrder != nil {
			createdOrders = append(createdOrders, *createdOrder)

			// market order can be closed immediately.
			switch createdOrder.Status {
			case types.OrderStatusFilled, types.OrderStatusCanceled, types.OrderStatusRejected:
				e.closedOrders[symbol] = append(e.closedOrders[symbol], *createdOrder)
			}

			e.userDataStream.EmitOrderUpdate(*createdOrder)
		}

		if trade != nil {
			e.userDataStream.EmitTradeUpdate(*trade)
		}
	}

	return createdOrders, nil
}

func (e Exchange) QueryOpenOrders(ctx context.Context, symbol string) (orders []types.Order, err error) {
	matching, ok := e.matchingBooks[symbol]
	if !ok {
		return nil, fmt.Errorf("matching engine is not initialized for symbol %s", symbol)
	}

	return append(matching.bidOrders, matching.askOrders...), nil
}

func (e Exchange) QueryClosedOrders(ctx context.Context, symbol string, since, until time.Time, lastOrderID uint64) (orders []types.Order, err error) {
	orders, ok := e.closedOrders[symbol]
	if !ok {
		return orders, fmt.Errorf("matching engine is not initialized for symbol %s", symbol)
	}

	return orders, nil
}

func (e Exchange) CancelOrders(ctx context.Context, orders ...types.Order) (errs []error) {
	for _, order := range orders {
		matching, ok := e.matchingBooks[order.Symbol]
		if !ok {
			e := fmt.Errorf("matching engine is not initialized for symbol %s", order.Symbol)
			errs = append(errs, e)
			continue
		}
		canceledOrder, err := matching.CancelOrder(order)
		errs = append(errs, err)
		if err != nil {
			continue
		}

		e.userDataStream.EmitOrderUpdate(canceledOrder)
	}

	return errs
}

func (e Exchange) QueryAccount(ctx context.Context) (*types.Account, error) {
	return e.account, nil
}

func (e *Exchange) QueryAccountBalances(ctx context.Context) (types.BalanceMap, error) {
	return e.account.Balances(), nil
}

func (e Exchange) QueryKLines(ctx context.Context, symbol string, interval types.Interval, options types.KLineQueryOptions) ([]types.KLine, error) {
	if options.EndTime != nil {
		return e.srv.QueryKLinesBackward(e.sourceName, symbol, interval, *options.EndTime, 1000)
	}

	if options.StartTime != nil {
		return e.srv.QueryKLinesForward(e.sourceName, symbol, interval, *options.StartTime, 1000)
	}

	return nil, errors.New("endTime or startTime can not be nil")
}

func (e Exchange) QueryTrades(ctx context.Context, symbol string, options *types.TradeQueryOptions) ([]types.Trade, error) {
	// we don't need query trades for backtest
	return nil, nil
}

func (e Exchange) QueryTicker(ctx context.Context, symbol string) (*types.Ticker, error) {
	matching, ok := e.matchingBooks[symbol]
	if !ok {
		return nil, fmt.Errorf("matching engine is not initialized for symbol %s", symbol)
	}

	kline := matching.LastKLine
	return &types.Ticker{
		Time:   kline.EndTime,
		Volume: kline.Volume,
		Last:   kline.Close,
		Open:   kline.Open,
		High:   kline.High,
		Low:    kline.Low,
		Buy:    kline.Close,
		Sell:   kline.Close,
	}, nil
}

func (e Exchange) QueryTickers(ctx context.Context, symbol ...string) (map[string]types.Ticker, error) {
	// Not using Tickers in back test (yet)
	return nil, ErrUnimplemented
}

func (e Exchange) Name() types.ExchangeName {
	return e.publicExchange.Name()
}

func (e Exchange) PlatformFeeCurrency() string {
	return e.publicExchange.PlatformFeeCurrency()
}

func (e Exchange) QueryMarkets(ctx context.Context) (types.MarketMap, error) {
	return e.publicExchange.QueryMarkets(ctx)
}

func (e Exchange) QueryDepositHistory(ctx context.Context, asset string, since, until time.Time) (allDeposits []types.Deposit, err error) {
	return nil, nil
}

func (e Exchange) QueryWithdrawHistory(ctx context.Context, asset string, since, until time.Time) (allWithdraws []types.Withdraw, err error) {
	return nil, nil
}

func newPublicExchange(sourceExchange types.ExchangeName) (types.Exchange, error) {
	switch sourceExchange {
	case types.ExchangeBinance:
		return binance.New("", ""), nil
	case types.ExchangeMax:
		return max.New("", ""), nil
	}

	return nil, fmt.Errorf("exchange %s is not supported", sourceExchange)
}
