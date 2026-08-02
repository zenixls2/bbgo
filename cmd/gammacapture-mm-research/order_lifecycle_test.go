package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestReadJournalMakerOrdersSeparatesMakerAndIOC(t *testing.T) {
	start := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	lines := []string{
		testJournalLine(start, `time="2026-07-23T08:00:00Z" level=info msg="market-maker quote evaluation"`),
		testJournalLine(start.Add(100*time.Millisecond), testCreationMessage(1, "gcmm-buy-a", start.Add(100*time.Millisecond), "NEW", "GTC", "LIMIT_MAKER", "BUY", 100, 2)),
		testJournalLine(start.Add(90*time.Second), testCreationMessage(2, "x-force", start.Add(90*time.Second), "FILLED", "IOC", "LIMIT", "SELL", 99, 1)),
		testJournalLine(start.Add(time.Minute), `time="2026-07-23T08:01:00Z" level=info msg="[ActiveOrderBook] order #1 is filled: ORDER"`),
		testJournalLine(start.Add(2*time.Minute), `time="2026-07-23T08:02:00Z" level=info msg="market-maker quote evaluation"`),
		testJournalLine(start.Add(2*time.Minute+100*time.Millisecond), testCreationMessage(3, "gcmm-sell-b", start.Add(2*time.Minute+100*time.Millisecond), "NEW", "GTC", "LIMIT_MAKER", "SELL", 102, 3)),
		testJournalLine(start.Add(3*time.Minute), `time="2026-07-23T08:03:00Z" level=info msg="market-maker quote evaluation"`),
	}
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte(joinJournalLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	orders, excluded := readJournalMakerOrders(path, "SOLJPY", start.Add(-time.Second), start.Add(4*time.Minute))
	if excluded != 1 || len(orders) != 2 {
		t.Fatalf("unexpected classification: maker=%d excluded=%d", len(orders), excluded)
	}
	if !orders[0].Filled || orders[0].EndReason != "filled" || orders[0].Side != types.SideTypeBuy {
		t.Fatalf("unexpected filled lifecycle: %+v", orders[0])
	}
	if orders[1].Filled || orders[1].EndReason != "quote-refresh-or-reconciliation" || orders[1].Side != types.SideTypeSell {
		t.Fatalf("unexpected cancelled lifecycle: %+v", orders[1])
	}
}

func TestEvaluateJournalOrdersUsesOnlyActiveCrossingVolume(t *testing.T) {
	start := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	orders := []*journalMakerOrder{
		{OrderID: 1, Side: types.SideTypeBuy, Price: 100, Quantity: 2, CreatedAt: start, EndedAt: start.Add(time.Minute), Filled: true},
		{OrderID: 2, Side: types.SideTypeSell, Price: 102, Quantity: 1, CreatedAt: start, EndedAt: start.Add(time.Minute), Filled: false},
	}
	trades := []tick{
		{time: start.Add(-time.Second), price: 99, size: 100, side: types.SideTypeSell},
		{time: start.Add(10 * time.Second), price: 100, size: 2, side: types.SideTypeSell},
		{time: start.Add(20 * time.Second), price: 102, size: 1, side: types.SideTypeBuy},
		{time: start.Add(2 * time.Minute), price: 99, size: 100, side: types.SideTypeSell},
	}
	zero := evaluateJournalOrders(orders, trades, 0)
	if zero.PredictedBuyFills != 1 || zero.PredictedSellFills != 1 || zero.TruePositive != 1 || zero.FalsePositive != 1 {
		t.Fatalf("unexpected zero-queue result: %+v", zero)
	}
	one := evaluateJournalOrders(orders, trades, 1)
	if one.PredictedBuyFills != 0 || one.PredictedSellFills != 0 || one.FalseNegative != 1 {
		t.Fatalf("unexpected one-queue result: %+v", one)
	}
}

func testCreationMessage(id uint64, client string, at time.Time, status, tif, orderType, side string, price, qty float64) string {
	return `time="` + at.Format(time.RFC3339Nano) + `" level=info msg="spot order creation response: &{Symbol:SOLJPY OrderID:` +
		formatUint(id) + ` ClientOrderID:` + client + ` TransactTime:` + formatInt(at.UnixMilli()) +
		` Price:` + formatFloat(price) + ` OrigQuantity:` + formatFloat(qty) +
		` OrigQuoteOrderQuantity:0 ExecutedQuantity:0 CummulativeQuoteQuantity:0 Status:` + status +
		` TimeInForce:` + tif + ` Type:` + orderType + ` Side:` + side + ` Fills:[]}"`
}

func testJournalLine(at time.Time, message string) string {
	row := map[string]string{"__REALTIME_TIMESTAMP": formatInt(at.UnixMicro()), "MESSAGE": message}
	data, _ := json.Marshal(row)
	return string(data)
}

func joinJournalLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

func formatUint(value uint64) string { return formatInt(int64(value)) }
func formatInt(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buffer [32]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		buffer[index] = '-'
	}
	return string(buffer[index:])
}
func formatFloat(value float64) string {
	if value == float64(int64(value)) {
		return formatInt(int64(value))
	}
	return "0.5"
}
