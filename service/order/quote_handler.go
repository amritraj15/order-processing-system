package order

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	domain "order_management/domain/order"
	"order_management/domain/shared"
	"order_management/service/uow"
)

type CreateFromQuoteCommand struct{ CustomerID, QuoteID uuid.UUID }

func (s *Service) HandleCreateFromQuote(ctx context.Context, cmd CreateFromQuoteCommand) (*domain.Order, bool, error) {
	var result *domain.Order
	replay := false
	err := s.UOW.Do(ctx, func(repos uow.Repositories) error {
		existing, err := repos.Orders().FindByQuote(ctx, cmd.QuoteID, cmd.CustomerID)
		if err == nil {
			result = existing
			replay = true
			return nil
		}
		if !errors.Is(err, shared.ErrNotFound) {
			return err
		}
		q, err := repos.Quotes().GetForUpdate(ctx, cmd.QuoteID, cmd.CustomerID)
		if err != nil {
			return err
		}
		if q.ConsumedOrderID != nil {
			result, err = repos.Orders().Get(ctx, *q.ConsumedOrderID, &cmd.CustomerID)
			replay = true
			return err
		}
		now, err := repos.Now(ctx)
		if err != nil {
			return err
		}
		if !now.Before(q.ExpiresAt) {
			return shared.ErrQuoteExpired
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		result = &domain.Order{ID: id, CustomerID: cmd.CustomerID, QuoteID: &q.ID, Pricing: &q.Pricing, Currency: q.Currency, Status: domain.Pending, TotalMinor: q.TotalMinor, Items: q.Items, CreatedAt: now, UpdatedAt: now}
		if err := repos.Orders().Insert(ctx, result); err != nil {
			return err
		}
		return repos.Quotes().MarkConsumed(ctx, q.ID, id)
	})
	if err != nil {
		return nil, false, fmt.Errorf("create order from quote: %w", err)
	}
	return result, replay, nil
}
