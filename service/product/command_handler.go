package product

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	domain "order_management/domain/product"
	"order_management/domain/shared"
)

func (s *Service) HandleCreate(ctx context.Context, cmd CreateCommand) (*domain.Product, error) {
	cmd.SKU = strings.TrimSpace(cmd.SKU)
	cmd.Name = strings.TrimSpace(cmd.Name)
	if cmd.SKU == "" || len(cmd.SKU) > 64 || cmd.Name == "" || len(cmd.Name) > 255 || cmd.PriceMinor <= 0 {
		return nil, fmt.Errorf("%w: invalid product", shared.ErrInvalid)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	p := &domain.Product{ID: id, SKU: cmd.SKU, Name: cmd.Name, PriceMinor: cmd.PriceMinor, CreatedAt: now, UpdatedAt: now}
	if err := s.Repo.Insert(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}
