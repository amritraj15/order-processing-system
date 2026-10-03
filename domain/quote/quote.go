package quote

import (
	"context"
	"github.com/google/uuid"
	"order_management/domain/order"
	"order_management/domain/pricing"
	"time"
)

type Quote struct {
	ID, CustomerID       uuid.UUID
	Currency             string
	TotalMinor           int64
	Pricing              pricing.Snapshot
	Items                []order.Item
	CreatedAt, ExpiresAt time.Time
	ConsumedOrderID      *uuid.UUID
}
type Repository interface {
	Insert(context.Context, *Quote) error
	GetForUpdate(context.Context, uuid.UUID, uuid.UUID) (*Quote, error)
	MarkConsumed(context.Context, uuid.UUID, uuid.UUID) error
	DeleteExpiredBatch(context.Context, time.Time, int) (int, error)
}
