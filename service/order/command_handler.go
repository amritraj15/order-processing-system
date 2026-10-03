package order

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	domain "order_management/domain/order"
	"order_management/service/uow"
)

func (s *Service) HandleCreate(ctx context.Context, cmd CreateCommand) (*domain.Order, error) {
	var result *domain.Order
	err := s.UOW.Do(ctx, func(repos uow.Repositories) error {
		ids := make([]uuid.UUID, len(cmd.Items))
		for i, item := range cmd.Items {
			ids[i] = item.ProductID
		}
		products, err := repos.Products().GetMany(ctx, ids)
		if err != nil {
			return err
		}
		settings, err := repos.Pricing().Settings(ctx)
		if err != nil {
			return err
		}
		now, err := repos.Now(ctx)
		if err != nil {
			return err
		}
		result, err = domain.New(cmd.CustomerID, settings.BaseCurrency, cmd.Items, products, now)
		if err != nil {
			return err
		}
		return repos.Orders().Insert(ctx, result)
	})
	if err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}
	return result, nil
}
func (s *Service) HandleStatus(ctx context.Context, cmd StatusCommand) (*domain.Order, error) {
	previous, err := domain.PreviousStatus(cmd.Status)
	if err != nil {
		return nil, fmt.Errorf("update order status: %w", err)
	}
	return s.Repo.Transition(ctx, cmd.ID, nil, previous, cmd.Status)
}
func (s *Service) HandleCancel(ctx context.Context, cmd CancelCommand) (*domain.Order, error) {
	return s.Repo.Transition(ctx, cmd.ID, &cmd.CustomerID, domain.Pending, domain.Cancelled)
}
