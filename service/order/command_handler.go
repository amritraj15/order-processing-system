package order

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	domain "order_management/domain/order"
	"order_management/service/uow"
)

func (s *Service) HandleCreate(ctx context.Context, cmd CreateCommand) (*domain.Order, error) {
	o, _, err := s.HandlePlace(ctx, PlaceCommand{CustomerID: cmd.CustomerID, Items: cmd.Items})
	return o, err
}

func createItems(ctx context.Context, repos uow.Repositories, cmd PlaceCommand) (*domain.Order, error) {
	ids := make([]uuid.UUID, len(cmd.Items))
	for i, item := range cmd.Items {
		ids[i] = item.ProductID
	}
	products, err := repos.Products().GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	settings, err := repos.Pricing().Settings(ctx)
	if err != nil {
		return nil, err
	}
	now, err := repos.Now(ctx)
	if err != nil {
		return nil, err
	}
	result, err := domain.New(cmd.CustomerID, settings.BaseCurrency, cmd.Items, products, now)
	if err != nil {
		return nil, err
	}
	if err := repos.Orders().Insert(ctx, result); err != nil {
		return nil, err
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
