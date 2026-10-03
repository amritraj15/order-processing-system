package quote

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"math"
	"order_management/domain/money"
	"order_management/domain/order"
	domain "order_management/domain/quote"
	"order_management/domain/shared"
	"order_management/service/uow"
	"time"
)

type Service struct {
	UOW    uow.UnitOfWork
	Region string
	TTL    time.Duration
}
type CreateCommand struct {
	CustomerID uuid.UUID
	Region     string
	Items      []order.ItemInput
}

func (s *Service) HandleCreate(ctx context.Context, cmd CreateCommand) (*domain.Quote, error) {
	if s.TTL <= 0 || s.TTL > time.Hour {
		return nil, shared.ErrInvalid
	}
	region := cmd.Region
	if region == "" {
		region = s.Region
	}
	currency, err := money.Currency(region)
	if err != nil {
		return nil, err
	}
	var result *domain.Quote
	err = s.UOW.Do(ctx, func(repos uow.Repositories) error {
		settings, err := repos.Pricing().Settings(ctx)
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(cmd.Items))
		for i, v := range cmd.Items {
			ids[i] = v.ProductID
		}
		products, err := repos.Products().GetMany(ctx, ids)
		if err != nil {
			return err
		}
		now, err := repos.Now(ctx)
		if err != nil {
			return err
		}
		base, err := order.New(cmd.CustomerID, settings.BaseCurrency, cmd.Items, products, now)
		if err != nil {
			return err
		}
		snapshot := *base.Pricing
		snapshot.Mode = "quote"
		snapshot.Region = region
		snapshot.MappingVersion = money.MappingVersion
		snapshot.TargetDigits, _ = money.Digits(currency)
		expires := now.Add(s.TTL)
		if currency != settings.BaseCurrency {
			rate, err := repos.Pricing().ActiveRate(ctx, settings.BaseCurrency, currency, now)
			if err != nil {
				return err
			}
			snapshot.Rate = rate.Rate
			snapshot.RateID = &rate.ID
			snapshot.RateSource = rate.Source
			snapshot.RateValidFrom = &rate.ValidFrom
			snapshot.RateValidUntil = &rate.ValidUntil
			if expires.After(rate.ValidUntil) {
				expires = rate.ValidUntil
			}
		}
		var total int64
		for i := range base.Items {
			item := &base.Items[i]
			unit, err := money.Convert(*item.SourceUnitPriceMinor, settings.BaseCurrency, currency, snapshot.Rate)
			if err != nil {
				return err
			}
			if item.Quantity > math.MaxInt64/unit {
				return shared.ErrAmountRange
			}
			line := unit * item.Quantity
			if total > math.MaxInt64-line {
				return shared.ErrAmountRange
			}
			item.UnitPriceMinor = unit
			item.LineTotalMinor = line
			total += line
		}
		result = &domain.Quote{ID: base.ID, CustomerID: cmd.CustomerID, Currency: currency, TotalMinor: total, Pricing: snapshot, Items: base.Items, CreatedAt: now, ExpiresAt: expires}
		return repos.Quotes().Insert(ctx, result)
	})
	if err != nil {
		return nil, fmt.Errorf("create quote: %w", err)
	}
	return result, nil
}
