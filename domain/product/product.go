package product

import (
	"context"
	"time"

	"github.com/google/uuid"

	"order_management/domain/shared"
)

type Product struct {
	ID                   uuid.UUID
	SKU, Name            string
	PriceMinor           int64
	CreatedAt, UpdatedAt time.Time
}
type Repository interface {
	Insert(context.Context, *Product) error
	Get(context.Context, uuid.UUID) (*Product, error)
	GetMany(context.Context, []uuid.UUID) ([]Product, error)
	List(context.Context, shared.Pagination) (shared.Page[Product], error)
}
