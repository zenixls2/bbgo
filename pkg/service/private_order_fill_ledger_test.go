package service

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestPrivateOrderFillLedgerPersistsFrameworkEvents(t *testing.T) {
	db, err := prepareDB(t)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	xdb := sqlx.NewDb(db.DB, "sqlite3")
	ledger := NewPrivateOrderFillLedgerService(xdb, "gammacapture-ethjpy-v1")
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	order := types.Order{
		SubmitOrder: types.SubmitOrder{
			ClientOrderID: "gcmm-buy-test", Symbol: "ETHJPY", Side: types.SideTypeBuy,
			Type: types.OrderTypeLimitMaker, Price: fixedpoint.NewFromInt(305000),
			Quantity: fixedpoint.NewFromFloat(0.001), TimeInForce: types.TimeInForceGTC,
		},
		Exchange: types.ExchangeBinance, OrderID: 123, Status: types.OrderStatusNew,
		IsWorking: true, CreationTime: types.Time(createdAt), UpdateTime: types.Time(createdAt),
	}
	trade := types.Trade{
		ID: 456, OrderID: order.OrderID, Exchange: types.ExchangeBinance, Symbol: order.Symbol,
		Side: types.SideTypeBuy, Price: order.Price, Quantity: fixedpoint.NewFromFloat(0.0004),
		QuoteQuantity: fixedpoint.NewFromFloat(122), IsBuyer: true, IsMaker: true,
		Time: types.Time(createdAt.Add(time.Second)),
	}

	require.NoError(t, ledger.RecordSubmitIntent(ctx, "binance", "gammacapture", "gammacapture:ETHJPY", 0, order.SubmitOrder))
	require.NoError(t, ledger.RecordSubmitResult(ctx, "binance", "gammacapture", "gammacapture:ETHJPY", 0, order.SubmitOrder, &order, nil))
	require.NoError(t, ledger.RecordOrderUpdate("binance", "", "", order))
	require.NoError(t, ledger.RecordFill("binance", "", "", trade, &order))
	require.NoError(t, ledger.RecordCancelRequest(ctx, "binance", "gammacapture", "gammacapture:ETHJPY", "quote-refresh", []types.Order{order}))
	require.NoError(t, ledger.RecordCancelResult(ctx, "binance", "gammacapture", "gammacapture:ETHJPY", "quote-refresh", []types.Order{order}, nil))

	var count int
	require.NoError(t, xdb.Get(&count, "SELECT COUNT(*) FROM private_order_fill_events"))
	require.Equal(t, 6, count)
	var stored struct {
		ProductionVersion string `db:"production_version"`
		EventType         string `db:"event_type"`
		OrderID           uint64 `db:"order_id"`
		TradeID           uint64 `db:"trade_id"`
		Payload           string `db:"payload"`
	}
	require.NoError(t, xdb.Get(&stored, "SELECT production_version, event_type, order_id, trade_id, payload FROM private_order_fill_events WHERE event_type = ? ORDER BY gid DESC LIMIT 1", PrivateOrderFillEventFill))
	require.Equal(t, "gammacapture-ethjpy-v1", stored.ProductionVersion)
	require.Equal(t, uint64(order.OrderID), stored.OrderID)
	require.Equal(t, uint64(trade.ID), stored.TradeID)
	require.Contains(t, stored.Payload, "gcmm-buy-test")
}
