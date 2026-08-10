package service

import (
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestOrderServiceInsertUpsertsSQLiteLifecycle(t *testing.T) {
	db, err := prepareDB(t)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	xdb := sqlx.NewDb(db.DB, "sqlite3")
	service := &OrderService{DB: xdb}
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	order := types.Order{
		SubmitOrder: types.SubmitOrder{
			ClientOrderID: "gcmm-buy-test", Symbol: "ETHJPY",
			Side: types.SideTypeBuy, Type: types.OrderTypeLimitMaker,
			Price: fixedpoint.NewFromInt(305000), Quantity: fixedpoint.NewFromFloat(0.00033),
			TimeInForce: types.TimeInForceGTC,
		},
		Exchange: types.ExchangeBinance, OrderID: 12345,
		Status: types.OrderStatusNew, IsWorking: true,
		CreationTime: types.Time(now), UpdateTime: types.Time(now),
	}
	require.NoError(t, service.Insert(order))

	order.Status = types.OrderStatusFilled
	order.IsWorking = false
	order.ExecutedQuantity = order.Quantity
	order.UpdateTime = types.Time(now.Add(time.Minute))
	require.NoError(t, service.Insert(order))

	var count int
	require.NoError(t, xdb.Get(&count, "SELECT COUNT(*) FROM orders WHERE exchange = ? AND order_id = ?", order.Exchange, order.OrderID))
	assert.Equal(t, 1, count)
	var stored types.Order
	require.NoError(t, xdb.Get(&stored, "SELECT * FROM orders WHERE exchange = ? AND order_id = ?", order.Exchange, order.OrderID))
	assert.Equal(t, types.OrderStatusFilled, stored.Status)
	assert.Equal(t, order.Quantity, stored.ExecutedQuantity)
	assert.False(t, stored.IsWorking)
}

func Test_genOrderSQL(t *testing.T) {
	t.Run("accept empty options", func(t *testing.T) {
		o := QueryOrdersOptions{}
		assert.Equal(t, "SELECT orders.*, IFNULL(SUM(t.price * t.quantity)/SUM(t.quantity), orders.price) AS average_price FROM orders LEFT JOIN trades AS t ON (t.order_id = orders.order_id) GROUP BY orders.gid  ORDER BY orders.gid ASC LIMIT 500", genOrderSQL("sqlite", o))
	})

	t.Run("different ordering ", func(t *testing.T) {
		o := QueryOrdersOptions{}
		assert.Equal(t, "SELECT orders.*, IFNULL(SUM(t.price * t.quantity)/SUM(t.quantity), orders.price) AS average_price FROM orders LEFT JOIN trades AS t ON (t.order_id = orders.order_id) GROUP BY orders.gid  ORDER BY orders.gid ASC LIMIT 500", genOrderSQL("sqlite", o))
		o.Ordering = "ASC"
		assert.Equal(t, "SELECT orders.*, IFNULL(SUM(t.price * t.quantity)/SUM(t.quantity), orders.price) AS average_price FROM orders LEFT JOIN trades AS t ON (t.order_id = orders.order_id) GROUP BY orders.gid  ORDER BY orders.gid ASC LIMIT 500", genOrderSQL("sqlite", o))
		o.Ordering = "DESC"
		assert.Equal(t, "SELECT orders.*, IFNULL(SUM(t.price * t.quantity)/SUM(t.quantity), orders.price) AS average_price FROM orders LEFT JOIN trades AS t ON (t.order_id = orders.order_id) GROUP BY orders.gid  ORDER BY orders.gid DESC LIMIT 500", genOrderSQL("sqlite", o))
	})

	t.Run("with since and until", func(t *testing.T) {
		since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		until := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
		o := QueryOrdersOptions{
			Exchange: "binance",
			Symbol:   "BTCUSDT",
			Since:    &since,
			Until:    &until,
		}
		sql := genOrderSQL("sqlite", o)
		assert.Contains(t, sql, "orders.created_at >= :since")
		assert.Contains(t, sql, "orders.created_at < :until")
		assert.Contains(t, sql, "orders.exchange = :exchange")
		assert.Contains(t, sql, "orders.symbol = :symbol")
	})

	t.Run("with only since", func(t *testing.T) {
		since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		o := QueryOrdersOptions{
			Since: &since,
		}
		sql := genOrderSQL("sqlite", o)
		assert.Contains(t, sql, "orders.created_at >= :since")
		assert.NotContains(t, sql, ":until")
	})

	t.Run("custom limit", func(t *testing.T) {
		o := QueryOrdersOptions{Limit: 100}
		sql := genOrderSQL("sqlite", o)
		assert.Contains(t, sql, "LIMIT 100")
	})

}
