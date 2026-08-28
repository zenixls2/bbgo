package gammacapture

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestPrivateOrderFillLedgerPersistsOrderedLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ETHJPY.jsonl")
	ledger, err := OpenPrivateOrderFillLedger(PrivateOrderFillLedgerConfig{
		Enabled: true, Path: path, SyncEachEvent: true,
	}, "ETHJPY")
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	context := PrivateOrderFillLedgerContext{
		BBOAt:   time.Unix(100, 0),
		BestBid: fixedpoint.NewFromFloat(500_000),
		BestAsk: fixedpoint.NewFromFloat(500_100),
	}
	submit := types.SubmitOrder{
		ClientOrderID: "gcmm-test-buy",
		Symbol:        "ETHJPY",
		Side:          types.SideTypeBuy,
		Type:          types.OrderTypeLimitMaker,
		TimeInForce:   types.TimeInForceGTC,
		Price:         fixedpoint.NewFromFloat(500_000),
		Quantity:      fixedpoint.NewFromFloat(0.01),
	}
	if err := ledger.RecordSubmitIntent("ETHJPY", "gammacapture", "gammacapture:ETHJPY", submit, context); err != nil {
		t.Fatalf("record submit intent: %v", err)
	}
	created := types.Order{
		SubmitOrder:  submit,
		OrderID:      42,
		Status:       types.OrderStatusNew,
		IsWorking:    true,
		CreationTime: types.NewTimeFromUnix(101, 0),
		UpdateTime:   types.NewTimeFromUnix(101, 0),
	}
	if err := ledger.RecordSubmitResult("ETHJPY", "gammacapture", "gammacapture:ETHJPY", 0, submit, &created, nil, context); err != nil {
		t.Fatalf("record submit result: %v", err)
	}
	if err := ledger.RecordCancelRequest("ETHJPY", "gammacapture", "gammacapture:ETHJPY", "quote-refresh-required", []types.Order{created}, context); err != nil {
		t.Fatalf("record cancel request: %v", err)
	}
	if err := ledger.RecordCancelResult("ETHJPY", "gammacapture", "gammacapture:ETHJPY", "quote-refresh-required", []types.Order{created}, nil, context); err != nil {
		t.Fatalf("record cancel result: %v", err)
	}
	created.Status = types.OrderStatusCanceled
	created.IsWorking = false
	created.UpdateTime = types.NewTimeFromUnix(102, 0)
	if err := ledger.RecordOrderUpdate("ETHJPY", "gammacapture", "gammacapture:ETHJPY", created, context); err != nil {
		t.Fatalf("record order update: %v", err)
	}
	trade := types.Trade{
		ID:            99,
		OrderID:       42,
		Symbol:        "ETHJPY",
		Side:          types.SideTypeBuy,
		IsBuyer:       true,
		IsMaker:       true,
		Price:         fixedpoint.NewFromFloat(500_000),
		Quantity:      fixedpoint.NewFromFloat(0.01),
		QuoteQuantity: fixedpoint.NewFromFloat(5_000),
		Time:          types.NewTimeFromUnix(103, 0),
		Fee:           fixedpoint.NewFromFloat(0.00001),
		FeeCurrency:   "ETH",
	}
	if err := ledger.RecordFill("ETHJPY", "gammacapture", "gammacapture:ETHJPY", trade, &created, context); err != nil {
		t.Fatalf("record fill: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open ledger output: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var events []PrivateOrderFillLedgerEvent
	for scanner.Scan() {
		var event PrivateOrderFillLedgerEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode ledger line: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan ledger output: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("event count mismatch: got %d want 6", len(events))
	}
	for index, event := range events {
		if event.SchemaVersion != privateOrderFillLedgerSchemaVersion || event.Sequence != uint64(index+1) {
			t.Fatalf("event sequencing mismatch at %d: %+v", index, event)
		}
	}
	if events[0].EventType != PrivateLedgerEventOrderSubmitIntent || events[0].BestBid.Float64() != 500_000 {
		t.Fatalf("submit intent lost causal BBO: %+v", events[0])
	}
	if events[2].EventType != PrivateLedgerEventCancelRequest || events[2].CancelReason != "quote-refresh-required" {
		t.Fatalf("cancel reason mismatch: %+v", events[2])
	}
	if events[3].EventType != PrivateLedgerEventCancelResult || !events[3].CancelAccepted {
		t.Fatalf("cancel result mismatch: %+v", events[3])
	}
	if events[4].Status != types.OrderStatusCanceled || events[4].CancelReason != "quote-refresh-required" {
		t.Fatalf("cancel update mismatch: %+v", events[4])
	}
	if events[5].EventType != PrivateLedgerEventFill || events[5].TradeID != 99 || !events[5].IsMaker {
		t.Fatalf("fill fields mismatch: %+v", events[5])
	}
}

func TestOpenPrivateOrderFillLedgerExpandsSymbolPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger", "{symbol}.jsonl")
	ledger, err := OpenPrivateOrderFillLedger(PrivateOrderFillLedgerConfig{
		Enabled: true, Path: path,
	}, "ETHJPY")
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	if want := filepath.Join(filepath.Dir(path), "ETHJPY.jsonl"); ledger.Path() != want {
		t.Fatalf("expanded path mismatch: got %q want %q", ledger.Path(), want)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
}

func TestOpenPrivateOrderFillLedgerResumesSequenceAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ETHJPY.jsonl")
	first, err := OpenPrivateOrderFillLedger(PrivateOrderFillLedgerConfig{Enabled: true, Path: path}, "ETHJPY")
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	if err := first.appendEvent(PrivateOrderFillLedgerEvent{EventType: PrivateLedgerEventOrderSubmitIntent, Symbol: "ETHJPY"}); err != nil {
		t.Fatalf("append first event: %v", err)
	}
	if err := first.appendEvent(PrivateOrderFillLedgerEvent{EventType: PrivateLedgerEventOrderUpdate, Symbol: "ETHJPY"}); err != nil {
		t.Fatalf("append second event: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	second, err := OpenPrivateOrderFillLedger(PrivateOrderFillLedgerConfig{Enabled: true, Path: path}, "ETHJPY")
	if err != nil {
		t.Fatalf("open restarted ledger: %v", err)
	}
	defer second.Close()
	if err := second.appendEvent(PrivateOrderFillLedgerEvent{EventType: PrivateLedgerEventFill, Symbol: "ETHJPY"}); err != nil {
		t.Fatalf("append restarted event: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open ledger output: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var events []PrivateOrderFillLedgerEvent
	for scanner.Scan() {
		var event PrivateOrderFillLedgerEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode ledger line: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan ledger output: %v", err)
	}
	if len(events) != 3 || events[2].Sequence != 3 {
		t.Fatalf("restart sequence mismatch: %+v", events)
	}
}
