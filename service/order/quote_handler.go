package order

import (
	"context"
	"errors"
	"github.com/google/uuid"
	domain "order_management/domain/order"
	"order_management/domain/shared"
	"order_management/service/uow"
)

type CreateFromQuoteCommand struct {
	CustomerID, QuoteID uuid.UUID
	IdempotencyKey      string
}

func (s *Service) HandleCreateFromQuote(ctx context.Context, cmd CreateFromQuoteCommand) (*domain.Order, bool, error) {
	return s.HandlePlace(ctx, PlaceCommand{CustomerID: cmd.CustomerID, QuoteID: &cmd.QuoteID, IdempotencyKey: cmd.IdempotencyKey})
}

func createFromQuote(ctx context.Context, repos uow.Repositories, customer, quoteID uuid.UUID) (*domain.Order, bool, error) {
	existing, err := repos.Orders().FindByQuote(ctx, quoteID, customer)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, shared.ErrNotFound) {
		return nil, false, err
	}
	q, err := repos.Quotes().GetForUpdate(ctx, quoteID, customer)
	if err != nil {
		return nil, false, err
	}
	if q.ConsumedOrderID != nil {
		result, err := repos.Orders().Get(ctx, *q.ConsumedOrderID, &customer)
		return result, true, err
	}
	now, err := repos.Now(ctx)
	if err != nil {
		return nil, false, err
	}
	if !now.Before(q.ExpiresAt) {
		return nil, false, shared.ErrQuoteExpired
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, false, err
	}
	result := &domain.Order{ID: id, CustomerID: customer, QuoteID: &q.ID, Pricing: &q.Pricing, Currency: q.Currency, Status: domain.Pending, TotalMinor: q.TotalMinor, Items: q.Items, CreatedAt: now, UpdatedAt: now}
	if err := repos.Orders().Insert(ctx, result); err != nil {
		return nil, false, err
	}
	if err := repos.Quotes().MarkConsumed(ctx, q.ID, id); err != nil {
		return nil, false, err
	}
	return result, false, nil
}
