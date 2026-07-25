package gammacapture

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/stretchr/testify/require"
)

func evidenceTrade(at time.Time, id uint64, side types.SideType, price, quantity float64) types.Trade {
	return types.Trade{
		Symbol:        "SOLJPY",
		ID:            id,
		Side:          side,
		Price:         fixedpoint.NewFromFloat(price),
		Quantity:      fixedpoint.NewFromFloat(quantity),
		QuoteQuantity: fixedpoint.NewFromFloat(price * quantity),
		Time:          types.Time(at),
	}
}

func evidenceBBO(symbol string, bid, bidSize, ask, askSize float64) types.BookTicker {
	return types.BookTicker{
		Symbol:   symbol,
		Buy:      fixedpoint.NewFromFloat(bid),
		BuySize:  fixedpoint.NewFromFloat(bidSize),
		Sell:     fixedpoint.NewFromFloat(ask),
		SellSize: fixedpoint.NewFromFloat(askSize),
	}
}

func TestFastEvidenceRequiresCoverageBeforeHealthy(t *testing.T) {
	now := time.Unix(1000, 0)
	m := NewFastEvidenceModel(FastEvidenceConfig{Window: time.Minute, MinTrades: 2, MinBBOUpdates: 2})
	m.ObserveTrade(now, evidenceTrade(now, 1, types.SideTypeBuy, 100, 1))
	m.ObserveBBO(now, evidenceBBO("SOLJPY", 99, 2, 101, 1))
	s := m.Snapshot(now)
	require.Equal(t, HealthDegraded, s.Health)
	require.Equal(t, 1, s.TradeCount)
	require.Equal(t, 1, s.BBOCount)

	m.ObserveTrade(now.Add(10*time.Second), evidenceTrade(now.Add(10*time.Second), 2, types.SideTypeSell, 100, 2))
	m.ObserveBBO(now.Add(10*time.Second), evidenceBBO("SOLJPY", 100, 1, 102, 3))
	s = m.Snapshot(now.Add(10 * time.Second))
	require.Equal(t, HealthHealthy, s.Health)
	require.InDelta(t, -1.0/3.0, s.SignedTradeImbalance, 1e-9)
	require.InDelta(t, -0.5, s.QueueImbalance, 1e-9)
}

func TestFastEvidenceTrimsWindowAndDeduplicatesTrades(t *testing.T) {
	now := time.Unix(2000, 0)
	m := NewFastEvidenceModel(FastEvidenceConfig{Window: time.Minute, MinTrades: 1, MinBBOUpdates: 1})
	trade := evidenceTrade(now, 7, types.SideTypeBuy, 100, 1)
	m.ObserveTrade(now, trade)
	m.ObserveTrade(now.Add(time.Second), trade)
	m.ObserveBBO(now, evidenceBBO("SOLJPY", 99, 1, 101, 1))
	require.Equal(t, 1, m.Snapshot(now).TradeCount)

	m.ObserveBBO(now.Add(61*time.Second), evidenceBBO("SOLJPY", 100, 1, 102, 1))
	s := m.Snapshot(now.Add(61 * time.Second))
	require.Zero(t, s.TradeCount)
	require.Equal(t, 1, s.BBOCount)
	require.Equal(t, HealthInsufficient, s.Health)
}

func TestFastEvidenceUsesQuantityWhenQuoteQuantityMissing(t *testing.T) {
	now := time.Unix(3000, 0)
	m := NewFastEvidenceModel(FastEvidenceConfig{Window: time.Minute, MinTrades: 1, MinBBOUpdates: 1})
	trade := evidenceTrade(now, 1, types.SideTypeBuy, 100, 2)
	trade.QuoteQuantity = fixedpoint.Zero
	m.ObserveTrade(now, trade)
	m.ObserveBBO(now, evidenceBBO("SOLJPY", 99, 1, 101, 1))
	s := m.Snapshot(now)
	require.Equal(t, HealthHealthy, s.Health)
	require.InDelta(t, 1, s.SignedTradeImbalance, 1e-9)
}

func TestFastEvidenceWarmupFromCapture(t *testing.T) {
	now := time.Unix(4000, 0).UTC()
	dir := t.TempDir()
	tradeTime1 := now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	tradeTime2 := now.Add(-10 * time.Second).Format(time.RFC3339Nano)
	bookTime1 := now.Add(-25 * time.Second).Format(time.RFC3339Nano)
	bookTime2 := now.Add(-5 * time.Second).Format(time.RFC3339Nano)
	trades := fmt.Sprintf("event_time,received_at,id,price,quantity,side,aggregate_id,first_trade_id,last_trade_id,gap_before_ms,source\n"+
		"%s,%s,11,100,1,BUY,0,0,0,0,stream\n"+
		"%s,%s,12,101,1,SELL,0,0,0,0,stream\n", tradeTime1, tradeTime1, tradeTime2, tradeTime2)
	books := fmt.Sprintf("received_at,bid,bid_quantity,ask,ask_quantity,gap_before_ms\n"+
		"%s,99,2,101,1,0\n"+
		"%s,100,1,102,2,0\n", bookTime1, bookTime2)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOLJPY-trades-test.csv"), []byte(trades), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOLJPY-bookticker-test.csv"), []byte(books), 0o600))

	s := &Strategy{
		Config: Config{
			Symbol:         "SOLJPY",
			MarketMaker:    MarketMakerConfig{FastEvidenceWindow: types.Duration(time.Minute)},
			AggTradeWarmup: AggTradeWarmupConfig{LivePath: dir},
		},
		fastEvidence: NewFastEvidenceModel(FastEvidenceConfig{Window: time.Minute, MinTrades: 2, MinBBOUpdates: 2}),
	}
	require.NoError(t, s.warmFastEvidenceFromCapture(now))
	snapshot := s.fastEvidence.Snapshot(now)
	require.Equal(t, 2, snapshot.TradeCount)
	require.Equal(t, 2, snapshot.BBOCount)
	require.Equal(t, HealthHealthy, snapshot.Health)
}
