package gorm

import (
	"context"
	"time"

	"github.com/google/uuid"
	orm "gorm.io/gorm"

	"order_management/domain/product"
	"order_management/domain/shared"
)

type productRow struct {
	ID                   uuid.UUID `gorm:"type:uuid;primaryKey"`
	SKU, Name            string
	PriceMinor           int64
	CreatedAt, UpdatedAt time.Time
}

func (productRow) TableName() string { return "products" }
func productFrom(p productRow) product.Product {
	return product.Product{ID: p.ID, SKU: p.SKU, Name: p.Name, PriceMinor: p.PriceMinor, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

type ProductRepository struct{ DB *orm.DB }

func (r *ProductRepository) Insert(ctx context.Context, p *product.Product) error {
	if _, err := (&PricingRepository{DB: r.DB}).Settings(ctx); err != nil {
		return wrap("product requires catalog settings", wrap("product_repository.Insert", err))
	}
	return wrap("insert resource", r.DB.WithContext(ctx).Create(&productRow{ID: p.ID, SKU: p.SKU, Name: p.Name, PriceMinor: p.PriceMinor, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}).Error)
}
func (r *ProductRepository) Get(ctx context.Context, id uuid.UUID) (*product.Product, error) {
	var row productRow
	err := r.DB.WithContext(ctx).First(&row, "id = ?", id).Error
	p := productFrom(row)
	return &p, wrap("read product", err)
}
func (r *ProductRepository) GetMany(ctx context.Context, ids []uuid.UUID) ([]product.Product, error) {
	var rows []productRow
	err := r.DB.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error
	products := make([]product.Product, len(rows))
	for i, row := range rows {
		products[i] = productFrom(row)
	}
	return products, wrap("product_repository.GetMany", err)
}
func pageQuery(db *orm.DB, p shared.Pagination) *orm.DB {
	if p.Cursor != nil {
		db = db.Where("id < ?", *p.Cursor)
	}
	return db.Order("id DESC").Limit(p.Limit + 1)
}
func (r *ProductRepository) List(ctx context.Context, p shared.Pagination) (shared.Page[product.Product], error) {
	if p.Limit < 1 || p.Limit > 100 {
		return shared.Page[product.Product]{}, shared.ErrInvalid
	}
	var rows []productRow
	err := pageQuery(r.DB.WithContext(ctx), p).Find(&rows).Error
	result := shared.Page[product.Product]{Items: make([]product.Product, 0, len(rows))}
	if len(rows) > p.Limit {
		cursor := rows[p.Limit-1].ID
		result.NextCursor = &cursor
		rows = rows[:p.Limit]
	}
	for _, row := range rows {
		result.Items = append(result.Items, productFrom(row))
	}
	return result, wrap("product_repository.List", err)
}

var _ product.Repository = (*ProductRepository)(nil)
