package order

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"order_management/domain/money"
	"order_management/domain/pricing"
	"order_management/domain/product"
	"order_management/domain/shared"
)

type Status string

const (
	Pending    Status = "PENDING"
	Processing Status = "PROCESSING"
	Shipped    Status = "SHIPPED"
	Delivered  Status = "DELIVERED"
	Cancelled  Status = "CANCELLED"
)

func (s Status) Valid() bool {
	switch s {
	case Pending, Processing, Shipped, Delivered, Cancelled:
		return true
	}
	return false
}
func PreviousStatus(next Status) (Status, error) {
	switch next {
	case Processing:
		return Pending, nil
	case Shipped:
		return Processing, nil
	case Delivered:
		return Shipped, nil
	default:
		return "", fmt.Errorf("%w: unsupported status transition", shared.ErrInvalid)
	}
}

type ItemInput struct {
	ProductID uuid.UUID
	Quantity  int64
}
type Item struct {
	ProductID                                  uuid.UUID
	SKU, Name                                  string
	Quantity, UnitPriceMinor, LineTotalMinor   int64
	SourceUnitPriceMinor, SourceLineTotalMinor *int64
}
type Order struct {
	ID, CustomerID       uuid.UUID
	QuoteID              *uuid.UUID
	Pricing              *pricing.Snapshot
	Status               Status
	Currency             string
	TotalMinor           int64
	Items                []Item
	CreatedAt, UpdatedAt time.Time
}

func New(customer uuid.UUID, currency string, inputs []ItemInput, products []product.Product, now time.Time) (*Order, error) {
	if customer == uuid.Nil || len(inputs) == 0 || len(inputs) > 100 {
		return nil, fmt.Errorf("%w: expected 1–100 items", shared.ErrInvalid)
	}
	digits, err := money.Digits(currency)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]product.Product, len(products))
	for _, p := range products {
		byID[p.ID] = p
	}
	seen := make(map[uuid.UUID]bool, len(inputs))
	o := &Order{CustomerID: customer, Currency: currency, Status: Pending, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), Items: make([]Item, 0, len(inputs))}
	for _, input := range inputs {
		if input.ProductID == uuid.Nil || input.Quantity <= 0 || seen[input.ProductID] {
			return nil, fmt.Errorf("%w: invalid quantity or duplicate product", shared.ErrInvalid)
		}
		seen[input.ProductID] = true
		p, ok := byID[input.ProductID]
		if !ok {
			return nil, fmt.Errorf("%w: unknown product %s", shared.ErrInvalid, input.ProductID)
		}
		if p.PriceMinor <= 0 || input.Quantity > math.MaxInt64/p.PriceMinor {
			return nil, fmt.Errorf("%w: item total overflow", shared.ErrInvalid)
		}
		line := input.Quantity * p.PriceMinor
		if o.TotalMinor > math.MaxInt64-line {
			return nil, fmt.Errorf("%w: order total overflow", shared.ErrInvalid)
		}
		o.TotalMinor += line
		o.Items = append(o.Items, Item{ProductID: p.ID, SKU: p.SKU, Name: p.Name, Quantity: input.Quantity, UnitPriceMinor: p.PriceMinor, LineTotalMinor: line, SourceUnitPriceMinor: &p.PriceMinor, SourceLineTotalMinor: &line})
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	o.ID = id
	o.Pricing = &pricing.Snapshot{Mode: "base", SourceCurrency: currency, BaseDigits: digits, TargetDigits: digits, Rate: "1", RateSource: "identity", SourceTotalMinor: o.TotalMinor}
	return o, nil
}

type Filter struct {
	CustomerID *uuid.UUID
	Status     Status
	Pagination shared.Pagination
}
type Repository interface {
	Insert(context.Context, *Order) error
	Get(context.Context, uuid.UUID, *uuid.UUID) (*Order, error)
	FindByQuote(context.Context, uuid.UUID, uuid.UUID) (*Order, error)
	List(context.Context, Filter) (shared.Page[Order], error)
	Transition(context.Context, uuid.UUID, *uuid.UUID, Status, Status) (*Order, error)
	// LockAndFindIdempotency must run inside the creation transaction. The lock
	// lasts until commit/rollback, including when the key does not exist yet.
	LockAndFindIdempotency(context.Context, uuid.UUID, string) (*IdempotencyRecord, error)
	InsertIdempotency(context.Context, IdempotencyRecord) error
}

type IdempotencyRecord struct {
	CustomerID  uuid.UUID
	Key         string
	RequestHash string
	OrderID     uuid.UUID
	CreatedAt   time.Time
}
