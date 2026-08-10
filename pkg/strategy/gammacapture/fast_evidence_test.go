package gammacapture

import (
	"fmt"
	"math"
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

func TestFastEvidenceHealthAtMatchesFullSnapshot(t *testing.T) {
	now := time.Unix(1500, 0)
	m := NewFastEvidenceModel(FastEvidenceConfig{Window: time.Minute, MinTrades: 1, MinBBOUpdates: 1})
	m.ObserveTrade(now, evidenceTrade(now, 1, types.SideTypeBuy, 100, 1))
	m.ObserveBBO(now, evidenceBBO("SOLJPY", 99, 1, 101, 1))
	require.Equal(t, m.Snapshot(now).Health, m.HealthAt(now))

	afterExpiry := now.Add(2 * time.Minute)
	require.Equal(t, m.Snapshot(afterExpiry).Health, m.HealthAt(afterExpiry))
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

func TestFastEvidenceExposesCausalOneAndFiveMinuteFeatures(t *testing.T) {
	now := time.Unix(5000, 0)
	m := NewFastEvidenceModel(FastEvidenceConfig{Window: 10 * time.Minute, MinTrades: 1, MinBBOUpdates: 1})
	m.ObserveTrade(now.Add(-6*time.Minute), evidenceTrade(now.Add(-6*time.Minute), 1, types.SideTypeSell, 100, 1))
	m.ObserveTrade(now.Add(-4*time.Minute), evidenceTrade(now.Add(-4*time.Minute), 2, types.SideTypeBuy, 100, 2))
	m.ObserveBBO(now.Add(-10*time.Minute), evidenceBBO("SOLJPY", 99, 1, 101, 1))
	m.ObserveBBO(now.Add(-5*time.Minute), evidenceBBO("SOLJPY", 100, 1, 102, 1))
	m.ObserveBBO(now.Add(-time.Minute), evidenceBBO("SOLJPY", 101, 1, 103, 1))
	m.ObserveBBO(now, evidenceBBO("SOLJPY", 102, 1, 104, 1))
	s := m.Snapshot(now)
	require.Equal(t, 2, s.TradeCount)
	require.Equal(t, 1, s.TradeCount5m)
	require.Equal(t, 3, s.BBOCount5m)
	require.InDelta(t, 1, s.SignedTradeImbalance5m, 1e-9)
	require.InDelta(t, math.Log(103.0/102.0)*10_000, s.MidReturn1mBps, 1e-9)
	require.InDelta(t, math.Log(103.0/101.0)*10_000, s.MidReturn5mBps, 1e-9)
}

func TestFastEvidenceMeasuresFiveMinuteDrawdownFromHigh(t *testing.T) {
	now := time.Unix(6000, 0)
	m := NewFastEvidenceModel(FastEvidenceConfig{Window: 10 * time.Minute, MinTrades: 1, MinBBOUpdates: 1})
	m.ObserveBBO(now.Add(-5*time.Minute), evidenceBBO("SOLJPY", 99, 1, 101, 1))
	m.ObserveBBO(now.Add(-2*time.Minute), evidenceBBO("SOLJPY", 102, 1, 104, 1))
	m.ObserveBBO(now, evidenceBBO("SOLJPY", 100, 1, 102, 1))
	s := m.Snapshot(now)
	require.Equal(t, 3, s.BBOCount5m)
	require.InDelta(t, math.Log(103.0/101.0)*10_000, s.MidDrawdown5mBps, 1e-9)
	require.Equal(t, 2, s.MidVolatilitySamples5m)
	require.InDelta(t, 300, s.MidVolatilityObservedSeconds5m, 1e-9)
	wantVarianceRate := (math.Pow(math.Log(103.0/100.0), 2) + math.Pow(math.Log(101.0/103.0), 2)) / 300
	require.InDelta(t, math.Sqrt(wantVarianceRate)*10_000, s.MidVolatilityPerSqrtSecond5mBps, 1e-9)
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

func TestFastEvidenceWarmupPrefersCurrentDailyArchiveAcrossRoots(t *testing.T) {
	now := time.Date(2026, 8, 5, 5, 0, 0, 0, time.UTC)
	root := t.TempDir()
	live := filepath.Join(root, "live")
	collector := filepath.Join(root, "SOLJPY")
	require.NoError(t, os.MkdirAll(live, 0o755))
	require.NoError(t, os.MkdirAll(collector, 0o755))

	stale := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	require.NoError(t, os.WriteFile(filepath.Join(live, "SOLJPY-trades-20260715T000000Z.csv"),
		[]byte(fmt.Sprintf("event_time,received_at,id,price,quantity,side\n%s,%s,1,100,1,BUY\n", stale, stale)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(live, "SOLJPY-bookticker-20260715T000000Z.csv"),
		[]byte(fmt.Sprintf("received_at,bid,bid_quantity,ask,ask_quantity\n%s,99,1,101,1\n", stale)), 0o600))

	recent1 := now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	recent2 := now.Add(-5 * time.Second).Format(time.RFC3339Nano)
	require.NoError(t, os.WriteFile(filepath.Join(collector, "SOLJPY-trades-2026-08-05.csv"),
		[]byte(fmt.Sprintf("event_time,received_at,id,price,quantity,side\n%s,%s,11,100,1,BUY\n%s,%s,12,101,1,SELL\n",
			recent1, recent1, recent2, recent2)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(collector, "SOLJPY-bookticker-2026-08-05.csv"),
		[]byte(fmt.Sprintf("received_at,bid,bid_quantity,ask,ask_quantity\n%s,99,1,101,1\n%s,100,1,102,1\n",
			recent1, recent2)), 0o600))

	s := &Strategy{
		Config: Config{
			Symbol:         "SOLJPY",
			MarketMaker:    MarketMakerConfig{FastEvidenceWindow: types.Duration(time.Minute)},
			AggTradeWarmup: AggTradeWarmupConfig{Path: root, LivePath: live},
		},
		fastEvidence: NewFastEvidenceModel(FastEvidenceConfig{Window: time.Minute, MinTrades: 2, MinBBOUpdates: 2}),
	}
	require.NoError(t, s.warmFastEvidenceFromCapture(now))
	snapshot := s.fastEvidence.Snapshot(now)
	require.Equal(t, 2, snapshot.TradeCount)
	require.Equal(t, 2, snapshot.BBOCount)
	require.Equal(t, HealthHealthy, snapshot.Health)
}

func TestFastEvidenceMeasuresReboundOFIAndMicroprice(t *testing.T) {
	now := time.Unix(7000, 0)
	m := NewFastEvidenceModel(FastEvidenceConfig{Window: 10 * time.Minute, MinTrades: 1, MinBBOUpdates: 1})
	m.ObserveTrade(now.Add(-time.Second), evidenceTrade(now.Add(-time.Second), 1, types.SideTypeBuy, 100, 1))
	m.ObserveBBO(now.Add(-40*time.Second), evidenceBBO("SOLJPY", 99.9, 1, 100.1, 1))
	m.ObserveBBO(now.Add(-20*time.Second), evidenceBBO("SOLJPY", 99.8, 1, 100.0, 3))
	m.ObserveBBO(now.Add(-10*time.Second), evidenceBBO("SOLJPY", 99.9, 4, 100.1, 1))
	m.ObserveBBO(now, evidenceBBO("SOLJPY", 100.0, 4, 100.2, 1))

	s := m.Snapshot(now)
	require.InDelta(t, 99.9, s.MidLow30s, 1e-12)
	require.InDelta(t, math.Log(100.1/99.9)*10_000, s.MidRebound30sBps, 1e-9)
	require.Greater(t, s.OrderFlowImbalance30s, 0.0)

	// Four bid units versus one ask unit place microprice above midpoint.
	wantMicroprice := (100.2*4.0 + 100.0*1.0) / 5.0
	wantDisplacement := (wantMicroprice - 100.1) / 0.1
	require.InDelta(t, wantDisplacement, s.MicropriceDisplacement, 1e-9)
}

func TestFastEvidenceSeparatesAskAndBidVolatility(t *testing.T) {
	model := NewFastEvidenceModel(FastEvidenceConfig{
		Window: 5 * time.Minute, MinTrades: 1, MinBBOUpdates: 1,
	})
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for second := 0; second < 20; second++ {
		bid := 100 * math.Exp(float64(second)*0.00001)
		ask := 101 * math.Exp(float64(second)*0.00010)
		model.ObserveBBO(start.Add(time.Duration(second)*time.Second),
			evidenceBBO("TESTJPY", bid, 1, ask, 1))
	}
	snapshot := model.Snapshot(start.Add(20 * time.Second))
	require.Greater(t, snapshot.BuyVolatilityPerSqrtSecond5mBps,
		5*snapshot.SellVolatilityPerSqrtSecond5mBps)
	require.Equal(t, snapshot.BuyVolatilityPerSqrtSecond5mBps,
		snapshot.BuyExecutionVolatility5mBps())
	require.Equal(t, snapshot.SellVolatilityPerSqrtSecond5mBps,
		snapshot.SellExecutionVolatility5mBps())
}
