package order

import (
	"context"

	"github.com/google/uuid"

	domain "order_management/domain/order"
	"order_management/domain/shared"
)

func (s *Service) HandleGet(ctx context.Context, id uuid.UUID, customer *uuid.UUID) (*domain.Order, error) {
	return s.Repo.Get(ctx, id, customer)
}
func (s *Service) HandleList(ctx context.Context, filter domain.Filter) (shared.Page[domain.Order], error) {
	return s.Repo.List(ctx, filter)
}
