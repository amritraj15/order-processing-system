package gorm

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	orm "gorm.io/gorm"
	"gorm.io/gorm/clause"
	"order_management/domain/money"
	"order_management/domain/pricing"
	"order_management/domain/shared"
	"strings"
	"time"
)

type settingsRow struct {
	SingletonID   int
	BaseCurrency  string
	InitializedAt time.Time
}

func (settingsRow) TableName() string { return "catalog_settings" }

type rateRow struct {
	ID                                         uuid.UUID
	BaseCurrency, TargetCurrency, Rate, Source string
	ValidFrom, ValidUntil, CreatedAt           time.Time
}

func (rateRow) TableName() string { return "fx_rates" }

type PricingRepository struct{ DB *orm.DB }

var _ pricing.Repository = (*PricingRepository)(nil)

func (r *PricingRepository) Settings(ctx context.Context) (*pricing.Settings, error) {
	var row settingsRow
	if err := r.DB.WithContext(ctx).Where("singleton_id = 1").First(&row).Error; err != nil {
		return nil, fmt.Errorf("read catalog settings: %w", mapError(err))
	}
	return &pricing.Settings{BaseCurrency: row.BaseCurrency, InitializedAt: row.InitializedAt}, nil
}
func (r *PricingRepository) Initialize(ctx context.Context, in pricing.InitializeInput) (*pricing.Settings, error) {
	if _, err := money.Digits(in.Currency); err != nil {
		return nil, err
	}
	var result *pricing.Settings
	err := r.DB.WithContext(ctx).Transaction(func(tx *orm.DB) error {
		if err := tx.Exec("LOCK TABLE catalog_settings, products, orders IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		repo := &PricingRepository{DB: tx}
		s, err := repo.Settings(ctx)
		if err == nil {
			if s.BaseCurrency != in.Currency {
				return fmt.Errorf("%w: catalog currency mismatch", shared.ErrConflict)
			}
			result = s
			return nil
		}
		if !errors.Is(err, shared.ErrNotFound) {
			return err
		}
		var populated bool
		if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM products) OR EXISTS(SELECT 1 FROM orders)").Scan(&populated).Error; err != nil {
			return err
		}
		if populated && !in.ConfirmExisting {
			return fmt.Errorf("%w: run catalog init --currency CODE --confirm-existing", shared.ErrConflict)
		}
		var conflicting bool
		if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM orders WHERE currency <> ?)", in.Currency).Scan(&conflicting).Error; err != nil {
			return err
		}
		if conflicting {
			return fmt.Errorf("%w: historical currencies require investigation", shared.ErrConflict)
		}
		row := settingsRow{SingletonID: 1, BaseCurrency: in.Currency, InitializedAt: time.Now().UTC()}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		result = &pricing.Settings{BaseCurrency: row.BaseCurrency, InitializedAt: row.InitializedAt}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("initialize catalog: %w", err)
	}
	return result, nil
}
func (r *PricingRepository) ActiveRate(ctx context.Context, base, target string, now time.Time) (*pricing.Rate, error) {
	var row rateRow
	err := r.DB.WithContext(ctx).Where("base_currency = ? AND target_currency = ? AND valid_from <= ? AND valid_until > ?", base, target, now, now).First(&row).Error
	if errors.Is(err, orm.ErrRecordNotFound) {
		return nil, shared.ErrRateUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("read active rate: %w", err)
	}
	return &pricing.Rate{ID: row.ID, BaseCurrency: row.BaseCurrency, TargetCurrency: row.TargetCurrency, Rate: row.Rate, Source: row.Source, ValidFrom: row.ValidFrom, ValidUntil: row.ValidUntil, CreatedAt: row.CreatedAt}, nil
}
func (r *PricingRepository) ImportRate(ctx context.Context, in *pricing.Rate) error {
	if _, err := money.Digits(in.TargetCurrency); err != nil {
		return err
	}
	if _, err := money.ParseRate(in.Rate); err != nil {
		return err
	}
	if !in.ValidUntil.After(in.ValidFrom) || strings.TrimSpace(in.Source) == "" || len(in.Source) > 128 {
		return fmt.Errorf("%w: invalid rate validity/source", shared.ErrInvalid)
	}
	return wrap("import rate", r.DB.WithContext(ctx).Transaction(func(tx *orm.DB) error {
		var setting settingsRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("singleton_id = 1").First(&setting).Error; err != nil {
			return mapError(err)
		}
		if in.TargetCurrency == setting.BaseCurrency {
			return fmt.Errorf("%w: base conversion is always 1", shared.ErrInvalid)
		}
		var count int64
		if err := tx.Model(&rateRow{}).Where("base_currency = ? AND target_currency = ? AND valid_from < ? AND valid_until > ?", setting.BaseCurrency, in.TargetCurrency, in.ValidUntil, in.ValidFrom).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: overlapping rate intervals", shared.ErrConflict)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		in.ID = id
		in.BaseCurrency = setting.BaseCurrency
		in.CreatedAt = time.Now().UTC()
		return tx.Create(&rateRow{ID: in.ID, BaseCurrency: in.BaseCurrency, TargetCurrency: in.TargetCurrency, Rate: in.Rate, Source: in.Source, ValidFrom: in.ValidFrom, ValidUntil: in.ValidUntil, CreatedAt: in.CreatedAt}).Error
	}))
}
func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, mapError(err))
}
