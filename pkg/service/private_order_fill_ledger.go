package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/c9s/bbgo/pkg/types"
)

const PrivateOrderFillLedgerSchemaVersion = 1

const (
	PrivateOrderFillEventSubmitIntent  = "order_submit_intent"
	PrivateOrderFillEventSubmitResult  = "order_submit_result"
	PrivateOrderFillEventCancelRequest = "cancel_request"
	PrivateOrderFillEventCancelResult  = "cancel_result"
	PrivateOrderFillEventOrderUpdate   = "order_update"
	PrivateOrderFillEventFill          = "fill"
)

// PrivateOrderFillEvent is the query-friendly projection of one immutable
// private execution observation. Payload retains the original framework
// object(s), while the scalar columns make lifecycle and calibration queries
// inexpensive on both SQLite and MySQL.
type PrivateOrderFillEvent struct {
	SchemaVersion int        `db:"schema_version"`
	EventType     string     `db:"event_type"`
	ObservedAt    time.Time  `db:"observed_at"`
	ExchangeAt    *time.Time `db:"exchange_at"`

	ProductionVersion  string `db:"production_version"`
	Session            string `db:"session"`
	Exchange           string `db:"exchange"`
	Strategy           string `db:"strategy"`
	StrategyInstanceID string `db:"strategy_instance_id"`
	Symbol             string `db:"symbol"`

	OrderID       uint64 `db:"order_id"`
	ClientOrderID string `db:"client_order_id"`
	OrderUUID     string `db:"order_uuid"`
	TradeID       uint64 `db:"trade_id"`

	Side          string `db:"side"`
	OrderType     string `db:"order_type"`
	TimeInForce   string `db:"time_in_force"`
	Status        string `db:"status"`
	IsWorking     bool   `db:"is_working"`
	Price         string `db:"price"`
	Quantity      string `db:"quantity"`
	AveragePrice  string `db:"average_price"`
	ExecutedQty   string `db:"executed_quantity"`
	TradePrice    string `db:"trade_price"`
	TradeQuantity string `db:"trade_quantity"`
	TradeQuoteQty string `db:"trade_quote_quantity"`
	Fee           string `db:"fee"`
	FeeCurrency   string `db:"fee_currency"`
	IsMaker       bool   `db:"is_maker"`
	IsBuyer       bool   `db:"is_buyer"`

	SubmitIndex    int    `db:"submit_index"`
	SubmitAccepted bool   `db:"submit_accepted"`
	Error          string `db:"error"`
	CancelReason   string `db:"cancel_reason"`
	CancelAccepted bool   `db:"cancel_accepted"`
	CancelError    string `db:"cancel_error"`

	Payload string `db:"payload"`
}

// PrivateOrderFillLedgerService persists the append-only event table. It is
// intentionally observation-only: a storage failure is returned to the
// caller for logging, never converted into an order decision.
type PrivateOrderFillLedgerService struct {
	DB                *sqlx.DB
	ProductionVersion string
}

func NewPrivateOrderFillLedgerService(db *sqlx.DB, productionVersion string) *PrivateOrderFillLedgerService {
	return &PrivateOrderFillLedgerService{DB: db, ProductionVersion: productionVersion}
}

func (s *PrivateOrderFillLedgerService) insert(ctx context.Context, event PrivateOrderFillEvent, payload interface{}) error {
	if s == nil || s.DB == nil {
		return ErrPersistenceNotExists
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = PrivateOrderFillLedgerSchemaVersion
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = time.Now().UTC()
	}
	if event.ProductionVersion == "" {
		event.ProductionVersion = s.ProductionVersion
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	event.Payload = string(data)

	_, err = s.DB.NamedExecContext(ctx, `
		INSERT INTO private_order_fill_events (
			schema_version, event_type, observed_at, exchange_at,
			production_version, session, exchange, strategy, strategy_instance_id, symbol,
			order_id, client_order_id, order_uuid, trade_id,
			side, order_type, time_in_force, status, is_working,
			price, quantity, average_price, executed_quantity,
			trade_price, trade_quantity, trade_quote_quantity, fee, fee_currency,
			is_maker, is_buyer, submit_index, submit_accepted, error,
			cancel_reason, cancel_accepted, cancel_error, payload
		) VALUES (
			:schema_version, :event_type, :observed_at, :exchange_at,
			:production_version, :session, :exchange, :strategy, :strategy_instance_id, :symbol,
			:order_id, :client_order_id, :order_uuid, :trade_id,
			:side, :order_type, :time_in_force, :status, :is_working,
			:price, :quantity, :average_price, :executed_quantity,
			:trade_price, :trade_quantity, :trade_quote_quantity, :fee, :fee_currency,
			:is_maker, :is_buyer, :submit_index, :submit_accepted, :error,
			:cancel_reason, :cancel_accepted, :cancel_error, :payload
		)`, event)
	return err
}

func orderEvent(eventType, session, strategy, strategyInstanceID string, order types.Order) PrivateOrderFillEvent {
	exchangeAt := order.UpdateTime.Time()
	if exchangeAt.IsZero() {
		exchangeAt = order.CreationTime.Time()
	}
	var exchangeAtPtr *time.Time
	if !exchangeAt.IsZero() {
		exchangeAt = exchangeAt.UTC()
		exchangeAtPtr = &exchangeAt
	}
	return PrivateOrderFillEvent{
		EventType: eventType, ExchangeAt: exchangeAtPtr,
		Session: session, Exchange: order.Exchange.String(), Strategy: strategy,
		StrategyInstanceID: strategyInstanceID, Symbol: order.Symbol,
		OrderID: order.OrderID, ClientOrderID: order.ClientOrderID, OrderUUID: order.UUID,
		Side: order.Side.String(), OrderType: string(order.Type), TimeInForce: string(order.TimeInForce),
		Status: string(order.Status), IsWorking: order.IsWorking,
		Price: order.Price.String(), Quantity: order.Quantity.String(),
		AveragePrice: order.AveragePrice.String(), ExecutedQty: order.ExecutedQuantity.String(),
	}
}

func (s *PrivateOrderFillLedgerService) RecordOrderUpdate(session, strategy, strategyInstanceID string, order types.Order) error {
	return s.insert(context.Background(), orderEvent(PrivateOrderFillEventOrderUpdate, session, strategy, strategyInstanceID, order), order)
}

func (s *PrivateOrderFillLedgerService) RecordFill(session, strategy, strategyInstanceID string, trade types.Trade, order *types.Order) error {
	event := PrivateOrderFillEvent{
		EventType: PrivateOrderFillEventFill, ObservedAt: time.Now().UTC(),
		Session: session, Exchange: trade.Exchange.String(), Strategy: strategy,
		StrategyInstanceID: strategyInstanceID, Symbol: trade.Symbol,
		OrderID: trade.OrderID, TradeID: trade.ID,
		Side: trade.Side.String(), TradePrice: trade.Price.String(),
		TradeQuantity: trade.Quantity.String(), TradeQuoteQty: trade.QuoteQuantity.String(),
		Fee: trade.Fee.String(), FeeCurrency: trade.FeeCurrency,
		IsMaker: trade.IsMaker, IsBuyer: trade.IsBuyer,
	}
	if !trade.Time.Time().IsZero() {
		t := trade.Time.Time().UTC()
		event.ExchangeAt = &t
	}
	if order != nil {
		event.OrderID = order.OrderID
		event.ClientOrderID = order.ClientOrderID
		event.OrderUUID = order.UUID
		event.Side = order.Side.String()
		event.OrderType = string(order.Type)
		event.TimeInForce = string(order.TimeInForce)
		event.Status = string(order.Status)
		event.IsWorking = order.IsWorking
		event.Price = order.Price.String()
		event.Quantity = order.Quantity.String()
		event.AveragePrice = order.AveragePrice.String()
		event.ExecutedQty = order.ExecutedQuantity.String()
	}
	return s.insert(context.Background(), event, struct {
		Trade types.Trade  `json:"trade"`
		Order *types.Order `json:"order,omitempty"`
	}{trade, order})
}

func (s *PrivateOrderFillLedgerService) RecordSubmitIntent(ctx context.Context, session, strategy, strategyInstanceID string, index int, submit types.SubmitOrder) error {
	event := PrivateOrderFillEvent{
		EventType: PrivateOrderFillEventSubmitIntent, Session: session, Strategy: strategy,
		StrategyInstanceID: strategyInstanceID, Symbol: submit.Symbol, Side: submit.Side.String(),
		OrderType: string(submit.Type), TimeInForce: string(submit.TimeInForce), Price: submit.Price.String(),
		Quantity: submit.Quantity.String(), ClientOrderID: submit.ClientOrderID, SubmitIndex: index,
	}
	return s.insert(ctx, event, submit)
}

func (s *PrivateOrderFillLedgerService) RecordSubmitResult(ctx context.Context, session, strategy, strategyInstanceID string, index int, submit types.SubmitOrder, order *types.Order, submitErr error) error {
	event := PrivateOrderFillEvent{
		EventType: PrivateOrderFillEventSubmitResult, Session: session, Strategy: strategy,
		StrategyInstanceID: strategyInstanceID, Symbol: submit.Symbol, Side: submit.Side.String(),
		OrderType: string(submit.Type), TimeInForce: string(submit.TimeInForce), Price: submit.Price.String(),
		Quantity: submit.Quantity.String(), ClientOrderID: submit.ClientOrderID, SubmitIndex: index,
		SubmitAccepted: submitErr == nil, Error: errorString(submitErr),
	}
	if order != nil {
		projection := orderEvent(PrivateOrderFillEventSubmitResult, session, strategy, strategyInstanceID, *order)
		event.OrderID = projection.OrderID
		event.ClientOrderID = projection.ClientOrderID
		event.OrderUUID = projection.OrderUUID
		event.Symbol = projection.Symbol
		event.Side = projection.Side
		event.OrderType = projection.OrderType
		event.TimeInForce = projection.TimeInForce
		event.Status = projection.Status
		event.IsWorking = projection.IsWorking
		event.Price = projection.Price
		event.Quantity = projection.Quantity
		event.AveragePrice = projection.AveragePrice
		event.ExecutedQty = projection.ExecutedQty
	}
	return s.insert(ctx, event, struct {
		Submit types.SubmitOrder `json:"submit"`
		Order  *types.Order      `json:"order,omitempty"`
		Error  string            `json:"error,omitempty"`
	}{submit, order, errorString(submitErr)})
}

func (s *PrivateOrderFillLedgerService) RecordCancelRequest(ctx context.Context, session, strategy, strategyInstanceID, reason string, orders []types.Order) error {
	if len(orders) == 0 {
		event := PrivateOrderFillEvent{EventType: PrivateOrderFillEventCancelRequest, Session: session, Strategy: strategy, StrategyInstanceID: strategyInstanceID, CancelReason: reason}
		return s.insert(ctx, event, struct {
			Reason string `json:"reason"`
		}{reason})
	}
	for _, order := range orders {
		event := orderEvent(PrivateOrderFillEventCancelRequest, session, strategy, strategyInstanceID, order)
		event.CancelReason = reason
		if err := s.insert(ctx, event, struct {
			Reason string      `json:"reason"`
			Order  types.Order `json:"order"`
		}{reason, order}); err != nil {
			return err
		}
	}
	return nil
}

func (s *PrivateOrderFillLedgerService) RecordCancelResult(ctx context.Context, session, strategy, strategyInstanceID, reason string, orders []types.Order, cancelErr error) error {
	if len(orders) == 0 {
		event := PrivateOrderFillEvent{EventType: PrivateOrderFillEventCancelResult, Session: session, Strategy: strategy, StrategyInstanceID: strategyInstanceID, CancelReason: reason, CancelAccepted: cancelErr == nil, CancelError: errorString(cancelErr)}
		return s.insert(ctx, event, struct {
			Reason string `json:"reason"`
			Error  string `json:"error,omitempty"`
		}{reason, errorString(cancelErr)})
	}
	for _, order := range orders {
		event := orderEvent(PrivateOrderFillEventCancelResult, session, strategy, strategyInstanceID, order)
		event.CancelReason = reason
		event.CancelAccepted = cancelErr == nil
		event.CancelError = errorString(cancelErr)
		if err := s.insert(ctx, event, struct {
			Reason string      `json:"reason"`
			Order  types.Order `json:"order"`
			Error  string      `json:"error,omitempty"`
		}{reason, order, errorString(cancelErr)}); err != nil {
			return err
		}
	}
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
