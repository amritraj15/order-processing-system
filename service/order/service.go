package order

import (
	domain "order_management/domain/order"
	"order_management/service/uow"
)

type Service struct {
	Repo     domain.Repository
	UOW      uow.UnitOfWork
	Currency string
}
