package uow

import (
	"context"
	"order_management/domain/pricing"
	"order_management/domain/quote"
	"time"

	"order_management/domain/order"
	"order_management/domain/product"
)

type Repositories interface {
	Orders() order.Repository
	Products() product.Repository
	Pricing() pricing.Repository
	Quotes() quote.Repository
	Now(context.Context) (time.Time, error)
}
type UnitOfWork interface {
	Do(context.Context, func(Repositories) error) error
}
