package product

import (
	"context"

	"github.com/google/uuid"

	domain "order_management/domain/product"
	"order_management/domain/shared"
)

func (s *Service) HandleGet(ctx context.Context, id uuid.UUID) (*domain.Product, error) {
	return s.Repo.Get(ctx, id)
}
func (s *Service) HandleList(ctx context.Context, pagination shared.Pagination) (shared.Page[domain.Product], error) {
	return s.Repo.List(ctx, pagination)
}
