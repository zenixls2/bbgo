package gammacapture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

const privateOrderFillLedgerSchemaVersion = 1

const (
	PrivateLedgerEventOrderSubmitIntent = "order_submit_intent"
	PrivateLedgerEventOrderSubmitResult = "order_submit_result"
	PrivateLedgerEventOrderUpdate       = "order_update"
	PrivateLedgerEventCancelRequest     = "cancel_request"
	PrivateLedgerEventCancelResult      = "cancel_result"
	PrivateLedgerEventFill              = "fill"
)

// PrivateOrderFillLedgerEvent is one immutable observation from the private
// exchange channel or the strategy's order API boundary. Values use the
// repository fixed-point JSON representation so price and quantity remain
// exact at the exchange precision instead of being rounded through float64.
//
// ObservedAt is the local receive/write clock. ExchangeAt is the exchange
// timestamp when the venue provides one. The distinction is important for
// measuring callback and transport latency without confusing it with order
// lifetime.
type PrivateOrderFillLedgerEvent struct {
	SchemaVersion     int       `json:"schemaVersion"`
	Sequence          uint64    `json:"sequence"`
	EventType         string    `json:"eventType"`
	ObservedAt        time.Time `json:"observedAt"`
	ExchangeAt        time.Time `json:"exchangeAt,omitempty"`
	ProductionVersion string    `json:"productionVersion,omitempty"`

	Symbol             string             `json:"symbol"`
	Exchange           types.ExchangeName `json:"exchange,omitempty"`
	Strategy           string             `json:"strategy,omitempty"`
	StrategyInstanceID string             `json:"strategyInstanceID,omitempty"`

	OrderID        uint64            `json:"orderID,omitempty"`
	ClientOrderID  string            `json:"clientOrderID,omitempty"`
	OrderUUID      string            `json:"orderUUID,omitempty"`
	Side           types.SideType    `json:"side,omitempty"`
	OrderType      types.OrderType   `json:"orderType,omitempty"`
	TimeInForce    types.TimeInForce `json:"timeInForce,omitempty"`
	Status         types.OrderStatus `json:"status,omitempty"`
	OriginalStatus string            `json:"originalStatus,omitempty"`
	IsWorking      bool              `json:"isWorking,omitempty"`
	OrderCreatedAt time.Time         `json:"orderCreatedAt,omitempty"`
	OrderUpdatedAt time.Time         `json:"orderUpdatedAt,omitempty"`

	Price            fixedpoint.Value `json:"price,omitempty"`
	Quantity         fixedpoint.Value `json:"quantity,omitempty"`
	AveragePrice     fixedpoint.Value `json:"averagePrice,omitempty"`
	ExecutedQuantity fixedpoint.Value `json:"executedQuantity,omitempty"`

	TradeID       uint64           `json:"tradeID,omitempty"`
	TradePrice    fixedpoint.Value `json:"tradePrice,omitempty"`
	TradeQuantity fixedpoint.Value `json:"tradeQuantity,omitempty"`
	QuoteQuantity fixedpoint.Value `json:"quoteQuantity,omitempty"`
	Fee           fixedpoint.Value `json:"fee,omitempty"`
	FeeCurrency   string           `json:"feeCurrency,omitempty"`
	IsMaker       bool             `json:"isMaker,omitempty"`
	IsBuyer       bool             `json:"isBuyer,omitempty"`

	SubmitAccepted bool   `json:"submitAccepted,omitempty"`
	SubmitError    string `json:"submitError,omitempty"`
	SubmitIndex    int    `json:"submitIndex,omitempty"`
	CancelReason   string `json:"cancelReason,omitempty"`
	CancelAccepted bool   `json:"cancelAccepted,omitempty"`
	CancelError    string `json:"cancelError,omitempty"`

	BBOAt   time.Time        `json:"bboAt,omitempty"`
	BestBid fixedpoint.Value `json:"bestBid,omitempty"`
	BestAsk fixedpoint.Value `json:"bestAsk,omitempty"`
}

// PrivateOrderFillLedgerContext is captured at the strategy boundary. It is
// intentionally limited to observable market context; model predictions are
// not copied here because private-fill calibration must not create a second
// decision path or mutate the live model.
type PrivateOrderFillLedgerContext struct {
	BBOAt   time.Time
	BestBid fixedpoint.Value
	BestAsk fixedpoint.Value
}

// PrivateOrderFillLedger is an append-only JSONL writer. A mutex preserves
// event order when user-data callbacks and fill-rebalance goroutines arrive
// concurrently. Every write is completed before the callback returns; an
// optional fsync is available for audit-critical deployments but is disabled
// by default to avoid adding disk latency to order submission callbacks.
type PrivateOrderFillLedger struct {
	mu                sync.Mutex
	file              *os.File
	path              string
	syncEachEvent     bool
	productionVersion string
	sequence          atomic.Uint64
	pendingCancelMu   sync.Mutex
	pendingCancels    map[uint64]string
	closed            bool
}

func existingPrivateLedgerSequence(path string) (uint64, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open existing private ledger: %w", err)
	}
	defer file.Close()

	var maximum uint64
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event PrivateOrderFillLedgerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return 0, fmt.Errorf("decode existing private ledger event: %w", err)
		}
		if event.Sequence > maximum {
			maximum = event.Sequence
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan existing private ledger: %w", err)
	}
	return maximum, nil
}

// OpenPrivateOrderFillLedger creates or appends to an existing ledger. The
// parent directory is created explicitly and only the configured file is
// opened; no broad cleanup or rotation is performed here.
func OpenPrivateOrderFillLedger(config PrivateOrderFillLedgerConfig, symbol string) (*PrivateOrderFillLedger, error) {
	if !config.Enabled {
		return nil, nil
	}
	path := strings.TrimSpace(config.Path)
	if path == "" {
		path = filepath.Join("data", "gammacapture", "state", "private-ledger", symbol+".jsonl")
	}
	path = strings.ReplaceAll(path, "{symbol}", symbol)
	if symbol == "" {
		return nil, fmt.Errorf("private ledger requires a symbol")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create private ledger directory: %w", err)
	}
	sequence, err := existingPrivateLedgerSequence(path)
	if err != nil {
		return nil, fmt.Errorf("recover private ledger sequence: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open private ledger %q: %w", path, err)
	}
	ledger := &PrivateOrderFillLedger{
		file: file, path: path, syncEachEvent: config.SyncEachEvent,
		productionVersion: config.ProductionVersion,
		pendingCancels:    make(map[uint64]string),
	}
	ledger.sequence.Store(sequence)
	return ledger, nil
}

func (l *PrivateOrderFillLedger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *PrivateOrderFillLedger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if err := l.file.Sync(); err != nil {
		_ = l.file.Close()
		return err
	}
	return l.file.Close()
}

func (l *PrivateOrderFillLedger) appendEvent(event PrivateOrderFillLedgerEvent) error {
	if l == nil {
		return nil
	}
	if event.EventType == "" {
		return fmt.Errorf("private ledger event type is empty")
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return fmt.Errorf("private ledger is closed")
	}
	if event.ProductionVersion == "" {
		event.ProductionVersion = l.productionVersion
	}
	event.SchemaVersion = privateOrderFillLedgerSchemaVersion
	event.Sequence = l.sequence.Add(1)
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal private ledger event: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := l.file.Write(payload); err != nil {
		return fmt.Errorf("append private ledger event: %w", err)
	}
	if l.syncEachEvent {
		if err := l.file.Sync(); err != nil {
			return fmt.Errorf("sync private ledger event: %w", err)
		}
	}
	return nil
}

func (l *PrivateOrderFillLedger) warnOnError(err error) {
	// Callers deliberately receive no error because order callbacks must not
	// turn a logging failure into a second order decision. The strategy logs the
	// returned error at its boundary where a symbol and event type are known.
	_ = err
}

func (l *PrivateOrderFillLedger) RecordSubmitIntent(symbol, strategy, strategyInstanceID string, submit types.SubmitOrder, context PrivateOrderFillLedgerContext) error {
	if l == nil {
		return nil
	}
	return l.appendEvent(PrivateOrderFillLedgerEvent{
		EventType: PrivateLedgerEventOrderSubmitIntent,
		Symbol:    symbol, Strategy: strategy, StrategyInstanceID: strategyInstanceID,
		ClientOrderID: submit.ClientOrderID, Side: submit.Side, OrderType: submit.Type,
		TimeInForce: submit.TimeInForce, Price: submit.Price, Quantity: submit.Quantity,
		BBOAt: context.BBOAt, BestBid: context.BestBid, BestAsk: context.BestAsk,
	})
}

func (l *PrivateOrderFillLedger) RecordSubmitResult(symbol, strategy, strategyInstanceID string, index int, submit types.SubmitOrder, order *types.Order, submitErr error, context PrivateOrderFillLedgerContext) error {
	if l == nil {
		return nil
	}
	event := PrivateOrderFillLedgerEvent{
		EventType: PrivateLedgerEventOrderSubmitResult,
		Symbol:    symbol, Strategy: strategy, StrategyInstanceID: strategyInstanceID,
		ClientOrderID: submit.ClientOrderID, Side: submit.Side, OrderType: submit.Type,
		TimeInForce: submit.TimeInForce, Price: submit.Price, Quantity: submit.Quantity,
		SubmitIndex: index, SubmitAccepted: submitErr == nil,
		BBOAt: context.BBOAt, BestBid: context.BestBid, BestAsk: context.BestAsk,
	}
	if submitErr != nil {
		event.SubmitError = submitErr.Error()
	}
	if order != nil {
		applyPrivateLedgerOrder(&event, *order)
	}
	return l.appendEvent(event)
}

func (l *PrivateOrderFillLedger) RecordCancelRequest(symbol, strategy, strategyInstanceID, reason string, orders []types.Order, context PrivateOrderFillLedgerContext) error {
	if l == nil {
		return nil
	}
	var firstErr error
	for _, order := range orders {
		l.pendingCancelMu.Lock()
		l.pendingCancels[order.OrderID] = reason
		l.pendingCancelMu.Unlock()
		event := PrivateOrderFillLedgerEvent{
			EventType: PrivateLedgerEventCancelRequest,
			Symbol:    symbol, Strategy: strategy, StrategyInstanceID: strategyInstanceID,
			CancelReason: reason,
			BBOAt:        context.BBOAt, BestBid: context.BestBid, BestAsk: context.BestAsk,
		}
		applyPrivateLedgerOrder(&event, order)
		if err := l.appendEvent(event); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (l *PrivateOrderFillLedger) RecordCancelResult(symbol, strategy, strategyInstanceID, reason string, orders []types.Order, cancelErr error, context PrivateOrderFillLedgerContext) error {
	if l == nil {
		return nil
	}
	var firstErr error
	for _, order := range orders {
		event := PrivateOrderFillLedgerEvent{
			EventType: PrivateLedgerEventCancelResult,
			Symbol:    symbol, Strategy: strategy, StrategyInstanceID: strategyInstanceID,
			CancelReason: reason, CancelAccepted: cancelErr == nil,
			BBOAt: context.BBOAt, BestBid: context.BestBid, BestAsk: context.BestAsk,
		}
		if cancelErr != nil {
			event.CancelError = cancelErr.Error()
		}
		applyPrivateLedgerOrder(&event, order)
		if err := l.appendEvent(event); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (l *PrivateOrderFillLedger) RecordOrderUpdate(symbol, strategy, strategyInstanceID string, order types.Order, context PrivateOrderFillLedgerContext) error {
	if l == nil {
		return nil
	}
	event := PrivateOrderFillLedgerEvent{
		EventType: PrivateLedgerEventOrderUpdate,
		Symbol:    symbol, Strategy: strategy, StrategyInstanceID: strategyInstanceID,
		BBOAt: context.BBOAt, BestBid: context.BestBid, BestAsk: context.BestAsk,
	}
	applyPrivateLedgerOrder(&event, order)
	l.pendingCancelMu.Lock()
	event.CancelReason = l.pendingCancels[order.OrderID]
	if order.Status.Closed() {
		delete(l.pendingCancels, order.OrderID)
	}
	l.pendingCancelMu.Unlock()
	return l.appendEvent(event)
}

func (l *PrivateOrderFillLedger) RecordFill(symbol, strategy, strategyInstanceID string, trade types.Trade, order *types.Order, context PrivateOrderFillLedgerContext) error {
	if l == nil {
		return nil
	}
	event := PrivateOrderFillLedgerEvent{
		EventType: PrivateLedgerEventFill,
		Symbol:    symbol, Strategy: strategy, StrategyInstanceID: strategyInstanceID,
		ExchangeAt: trade.Time.Time(), TradeID: trade.ID, TradePrice: trade.Price,
		TradeQuantity: trade.Quantity, QuoteQuantity: trade.QuoteQuantity,
		Fee: trade.Fee, FeeCurrency: trade.FeeCurrency, IsMaker: trade.IsMaker,
		IsBuyer: trade.IsBuyer,
		BBOAt:   context.BBOAt, BestBid: context.BestBid, BestAsk: context.BestAsk,
	}
	if order != nil {
		applyPrivateLedgerOrder(&event, *order)
	}
	return l.appendEvent(event)
}

func applyPrivateLedgerOrder(event *PrivateOrderFillLedgerEvent, order types.Order) {
	if event == nil {
		return
	}
	event.OrderID = order.OrderID
	event.ClientOrderID = order.ClientOrderID
	event.OrderUUID = order.UUID
	event.Side = order.Side
	event.OrderType = order.Type
	event.TimeInForce = order.TimeInForce
	event.Status = order.Status
	event.OriginalStatus = order.OriginalStatus
	event.IsWorking = order.IsWorking
	event.OrderCreatedAt = order.CreationTime.Time()
	event.OrderUpdatedAt = order.UpdateTime.Time()
	event.Price = order.Price
	event.Quantity = order.Quantity
	event.AveragePrice = order.AveragePrice
	event.ExecutedQuantity = order.ExecutedQuantity
	if event.Symbol == "" {
		event.Symbol = order.Symbol
	}
	if event.Exchange == "" {
		event.Exchange = order.Exchange
	}
}
