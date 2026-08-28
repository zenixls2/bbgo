package bbgo

import (
	"context"

	"github.com/c9s/bbgo/pkg/types"
)

// PrivateOrderFillLedger is the framework boundary for append-only private
// execution observations. The implementation lives in pkg/service so it can
// persist to the configured database without making order executors depend on
// a concrete storage backend.
type PrivateOrderFillLedger interface {
	RecordSubmitIntent(ctx context.Context, session, strategy, strategyInstanceID string, index int, submit types.SubmitOrder) error
	RecordSubmitResult(ctx context.Context, session, strategy, strategyInstanceID string, index int, submit types.SubmitOrder, order *types.Order, submitErr error) error
	RecordCancelRequest(ctx context.Context, session, strategy, strategyInstanceID, reason string, orders []types.Order) error
	RecordCancelResult(ctx context.Context, session, strategy, strategyInstanceID, reason string, orders []types.Order, cancelErr error) error
	RecordOrderUpdate(session, strategy, strategyInstanceID string, order types.Order) error
	RecordFill(session, strategy, strategyInstanceID string, trade types.Trade, order *types.Order) error
}
