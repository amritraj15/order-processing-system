package gorm

import (
	"context"
	"github.com/google/uuid"
	orm "gorm.io/gorm"
	"gorm.io/gorm/clause"
	"order_management/domain/order"
	"order_management/domain/pricing"
	"order_management/domain/quote"
	"order_management/domain/shared"
	"time"
)

type pricingRow struct {
	Region, MappingVersion, SourceCurrency, Rate, RateSource *string
	BaseDigits, TargetDigits                                 *int
	RateID                                                   *uuid.UUID
	RateValidFrom, RateValidUntil                            *time.Time
	SourceTotalMinor                                         *int64
}

func pricingTo(p *pricing.Snapshot) pricingRow {
	if p == nil {
		return pricingRow{}
	}
	return pricingRow{Region: &p.Region, MappingVersion: &p.MappingVersion, SourceCurrency: &p.SourceCurrency, Rate: &p.Rate, RateSource: &p.RateSource, BaseDigits: &p.BaseDigits, TargetDigits: &p.TargetDigits, RateID: p.RateID, RateValidFrom: p.RateValidFrom, RateValidUntil: p.RateValidUntil, SourceTotalMinor: &p.SourceTotalMinor}
}
func pricingFrom(p pricingRow, mode string) *pricing.Snapshot {
	if mode == "legacy" {
		return nil
	}
	v := &pricing.Snapshot{Mode: mode, RateID: p.RateID, RateValidFrom: p.RateValidFrom, RateValidUntil: p.RateValidUntil}
	if p.Region != nil {
		v.Region = *p.Region
	}
	if p.MappingVersion != nil {
		v.MappingVersion = *p.MappingVersion
	}
	if p.SourceCurrency != nil {
		v.SourceCurrency = *p.SourceCurrency
	}
	if p.Rate != nil {
		v.Rate = *p.Rate
	}
	if p.RateSource != nil {
		v.RateSource = *p.RateSource
	}
	if p.BaseDigits != nil {
		v.BaseDigits = *p.BaseDigits
	}
	if p.TargetDigits != nil {
		v.TargetDigits = *p.TargetDigits
	}
	if p.SourceTotalMinor != nil {
		v.SourceTotalMinor = *p.SourceTotalMinor
	}
	return v
}

type quoteRow struct {
	ID, CustomerID       uuid.UUID
	Currency             string
	TotalMinor           int64
	Pricing              pricingRow `gorm:"embedded"`
	CreatedAt, ExpiresAt time.Time
	ConsumedOrderID      *uuid.UUID
}

func (quoteRow) TableName() string { return "order_quotes" }

type quoteItemRow struct {
	QuoteID                                                                              uuid.UUID `gorm:"primaryKey"`
	Position                                                                             int       `gorm:"primaryKey"`
	ProductID                                                                            uuid.UUID
	SKU, Name                                                                            string
	Quantity, SourceUnitPriceMinor, SourceLineTotalMinor, UnitPriceMinor, LineTotalMinor int64
}

func (quoteItemRow) TableName() string { return "quote_items" }

type QuoteRepository struct{ DB *orm.DB }

var _ quote.Repository = (*QuoteRepository)(nil)

func (r *QuoteRepository) Insert(ctx context.Context, q *quote.Quote) error {
	row := quoteRow{ID: q.ID, CustomerID: q.CustomerID, Currency: q.Currency, TotalMinor: q.TotalMinor, Pricing: pricingTo(&q.Pricing), CreatedAt: q.CreatedAt, ExpiresAt: q.ExpiresAt}
	if err := r.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return wrap("insert quote", err)
	}
	items := make([]quoteItemRow, len(q.Items))
	for i, v := range q.Items {
		items[i] = quoteItemRow{QuoteID: q.ID, Position: i, ProductID: v.ProductID, SKU: v.SKU, Name: v.Name, Quantity: v.Quantity, SourceUnitPriceMinor: *v.SourceUnitPriceMinor, SourceLineTotalMinor: *v.SourceLineTotalMinor, UnitPriceMinor: v.UnitPriceMinor, LineTotalMinor: v.LineTotalMinor}
	}
	return wrap("insert quote items", r.DB.WithContext(ctx).Create(&items).Error)
}
func (r *QuoteRepository) GetForUpdate(ctx context.Context, id, customer uuid.UUID) (*quote.Quote, error) {
	var row quoteRow
	if err := r.DB.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND customer_id = ?", id, customer).First(&row).Error; err != nil {
		return nil, wrap("lock quote", err)
	}
	var rows []quoteItemRow
	if err := r.DB.WithContext(ctx).Where("quote_id = ?", id).Order("position ASC").Find(&rows).Error; err != nil {
		return nil, wrap("read quote items", err)
	}
	q := &quote.Quote{ID: row.ID, CustomerID: row.CustomerID, Currency: row.Currency, TotalMinor: row.TotalMinor, Pricing: *pricingFrom(row.Pricing, "quote"), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, ConsumedOrderID: row.ConsumedOrderID}
	for _, v := range rows {
		q.Items = append(q.Items, order.Item{ProductID: v.ProductID, SKU: v.SKU, Name: v.Name, Quantity: v.Quantity, SourceUnitPriceMinor: &v.SourceUnitPriceMinor, SourceLineTotalMinor: &v.SourceLineTotalMinor, UnitPriceMinor: v.UnitPriceMinor, LineTotalMinor: v.LineTotalMinor})
	}
	return q, nil
}
func (r *QuoteRepository) MarkConsumed(ctx context.Context, id, orderID uuid.UUID) error {
	res := r.DB.WithContext(ctx).Model(&quoteRow{}).Where("id = ? AND consumed_order_id IS NULL", id).Update("consumed_order_id", orderID)
	if res.Error != nil {
		return wrap("consume quote", res.Error)
	}
	if res.RowsAffected != 1 {
		return shared.ErrConflict
	}
	return nil
}

const QuoteCleanupSQL = `WITH expired AS (SELECT id FROM order_quotes WHERE expires_at <= ? ORDER BY expires_at LIMIT ? FOR UPDATE SKIP LOCKED), removed AS (DELETE FROM order_quotes q USING expired e WHERE q.id=e.id RETURNING q.id) SELECT count(*) FROM removed`

func (r *QuoteRepository) DeleteExpiredBatch(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit < 1 || limit > 10000 {
		return 0, shared.ErrInvalid
	}
	var count int64
	err := r.DB.WithContext(ctx).Raw(QuoteCleanupSQL, before, limit).Scan(&count).Error
	return int(count), wrap("clean expired quotes", err)
}
