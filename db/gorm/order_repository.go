package gorm

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	orm "gorm.io/gorm"

	"order_management/domain/order"
	"order_management/domain/pricing"
	"order_management/domain/product"
	"order_management/domain/quote"
	"order_management/domain/shared"
	"order_management/ports"
	"order_management/service/uow"
)

type orderRow struct {
	ID                   uuid.UUID `gorm:"type:uuid;primaryKey"`
	CustomerID           uuid.UUID `gorm:"type:uuid"`
	Status               order.Status
	QuoteID              *uuid.UUID
	PricingMode          string
	Pricing              pricingRow `gorm:"embedded"`
	Currency             string
	TotalMinor           int64
	CreatedAt, UpdatedAt time.Time
}

func (orderRow) TableName() string { return "orders" }

type itemRow struct {
	OrderID                                    uuid.UUID `gorm:"type:uuid;primaryKey"`
	Position                                   int       `gorm:"primaryKey"`
	ProductID                                  uuid.UUID `gorm:"type:uuid"`
	SKU, Name                                  string
	Quantity, UnitPriceMinor, LineTotalMinor   int64
	SourceUnitPriceMinor, SourceLineTotalMinor *int64
}

func (itemRow) TableName() string { return "order_items" }
func orderFrom(row orderRow) order.Order {
	return order.Order{ID: row.ID, QuoteID: row.QuoteID, Pricing: pricingFrom(row.Pricing, row.PricingMode), CustomerID: row.CustomerID, Status: row.Status, Currency: row.Currency, TotalMinor: row.TotalMinor, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Items: []order.Item{}}
}
func itemFrom(row itemRow) order.Item {
	return order.Item{ProductID: row.ProductID, SourceUnitPriceMinor: row.SourceUnitPriceMinor, SourceLineTotalMinor: row.SourceLineTotalMinor, SKU: row.SKU, Name: row.Name, Quantity: row.Quantity, UnitPriceMinor: row.UnitPriceMinor, LineTotalMinor: row.LineTotalMinor}
}

type OrderRepository struct{ DB *orm.DB }

func (r *OrderRepository) Insert(ctx context.Context, o *order.Order) error {
	if o.Pricing == nil {
		return shared.ErrInvalid
	}
	row := orderRow{ID: o.ID, QuoteID: o.QuoteID, PricingMode: o.Pricing.Mode, Pricing: pricingTo(o.Pricing), CustomerID: o.CustomerID, Status: o.Status, Currency: o.Currency, TotalMinor: o.TotalMinor, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
	if err := r.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return wrap("order_repository.Insert", err)
	}
	items := make([]itemRow, len(o.Items))
	for i, item := range o.Items {
		items[i] = itemRow{OrderID: o.ID, Position: i, SourceUnitPriceMinor: item.SourceUnitPriceMinor, SourceLineTotalMinor: item.SourceLineTotalMinor, ProductID: item.ProductID, SKU: item.SKU, Name: item.Name, Quantity: item.Quantity, UnitPriceMinor: item.UnitPriceMinor, LineTotalMinor: item.LineTotalMinor}
	}
	return wrap("insert resource", r.DB.WithContext(ctx).Create(&items).Error)
}
func scoped(db *orm.DB, customer *uuid.UUID) *orm.DB {
	if customer != nil {
		return db.Where("customer_id = ?", *customer)
	}
	return db
}
func (r *OrderRepository) Get(ctx context.Context, id uuid.UUID, customer *uuid.UUID) (*order.Order, error) {
	var row orderRow
	err := scoped(r.DB.WithContext(ctx), customer).First(&row, "id = ?", id).Error
	if err != nil {
		return nil, wrap("read order", err)
	}
	var items []itemRow
	if err := r.DB.WithContext(ctx).Where("order_id = ?", id).Order("position ASC").Find(&items).Error; err != nil {
		return nil, wrap("order_repository.Get", err)
	}
	o := orderFrom(row)
	for _, item := range items {
		o.Items = append(o.Items, itemFrom(item))
	}
	return &o, nil
}
func (r *OrderRepository) List(ctx context.Context, filter order.Filter) (shared.Page[order.Order], error) {
	query := scoped(r.DB.WithContext(ctx), filter.CustomerID)
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	var rows []orderRow
	err := pageQuery(query, filter.Pagination).Find(&rows).Error
	result := shared.Page[order.Order]{Items: make([]order.Order, 0, len(rows))}
	if err != nil {
		return result, wrap("order_repository.List", err)
	}
	if len(rows) > filter.Pagination.Limit {
		cursor := rows[filter.Pagination.Limit-1].ID
		result.NextCursor = &cursor
		rows = rows[:filter.Pagination.Limit]
	}
	ids := make([]uuid.UUID, len(rows))
	positions := make(map[uuid.UUID]int, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
		positions[row.ID] = i
		result.Items = append(result.Items, orderFrom(row))
	}
	if len(ids) == 0 {
		return result, nil
	}
	var items []itemRow
	err = r.DB.WithContext(ctx).Where("order_id IN ?", ids).Order("order_id, position").Find(&items).Error
	for _, item := range items {
		i := positions[item.OrderID]
		result.Items[i].Items = append(result.Items[i].Items, itemFrom(item))
	}
	return result, wrap("order_repository.List", err)
}
func (r *OrderRepository) Transition(ctx context.Context, id uuid.UUID, customer *uuid.UUID, previous, next order.Status) (*order.Order, error) {
	var result *order.Order
	err := r.DB.WithContext(ctx).Transaction(func(tx *orm.DB) error {
		res := scoped(tx.Model(&orderRow{}), customer).Where("id = ? AND status = ?", id, previous).Updates(map[string]any{"status": next, "updated_at": orm.Expr("now()")})
		if res.Error != nil {
			return res.Error
		}
		repo := &OrderRepository{DB: tx}
		var err error
		result, err = repo.Get(ctx, id, customer)
		if err != nil {
			return wrap("order_repository.Transition", err)
		}
		if res.RowsAffected == 0 {
			if previous == order.Pending && next == order.Cancelled && result.Status == order.Cancelled {
				return nil
			}
			return shared.ErrConflict
		}
		return nil
	})
	return result, wrap("order_repository.Transition", err)
}

// PendingBatchSQL keeps the partial-index predicate literal even in prepared
// plans. Locks and updates share one statement and transaction; committed rows
// leave the queue index immediately. No historical rows or order items are read.
const PendingBatchSQL = `WITH batch AS (
    SELECT id FROM orders
    WHERE status = 'PENDING' AND created_at <= ?
    ORDER BY created_at, id
    LIMIT ? FOR UPDATE SKIP LOCKED
), updated AS (
    UPDATE orders AS o SET status = 'PROCESSING', updated_at = now()
    FROM batch WHERE o.id = batch.id AND o.status = 'PENDING'
    RETURNING o.id
)
SELECT count(*) FROM updated`

func (r *OrderRepository) ProcessBatch(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if limit < 1 || limit > 10000 {
		return 0, shared.ErrInvalid
	}
	var count int64
	err := r.DB.WithContext(ctx).Transaction(func(tx *orm.DB) error { return tx.Raw(PendingBatchSQL, cutoff, limit).Scan(&count).Error })
	return int(count), wrap("order_repository.ProcessBatch", err)
}

type UnitOfWork struct{ DB *orm.DB }
type repositories struct{ db *orm.DB }

func (r repositories) Orders() order.Repository     { return &OrderRepository{DB: r.db} }
func (r repositories) Products() product.Repository { return &ProductRepository{DB: r.db} }
func (u *UnitOfWork) Do(ctx context.Context, fn func(uow.Repositories) error) error {
	// A key/quote lookup after waiting for a competing transaction must see its
	// commit, even if the database's default isolation is configured differently.
	return wrap("unit of work", u.DB.WithContext(ctx).Transaction(func(tx *orm.DB) error { return fn(repositories{db: tx}) }, &sql.TxOptions{Isolation: sql.LevelReadCommitted}))
}

var _ order.Repository = (*OrderRepository)(nil)
var _ ports.PendingProcessor = (*OrderRepository)(nil)
var _ uow.UnitOfWork = (*UnitOfWork)(nil)
var _ uow.Repositories = repositories{}

func (r *OrderRepository) FindByQuote(ctx context.Context, id, customer uuid.UUID) (*order.Order, error) {
	var row orderRow
	if err := r.DB.WithContext(ctx).Where("quote_id = ? AND customer_id = ?", id, customer).First(&row).Error; err != nil {
		return nil, wrap("find quoted order", err)
	}
	return r.Get(ctx, row.ID, &customer)
}
func (r repositories) Pricing() pricing.Repository { return &PricingRepository{DB: r.db} }
func (r repositories) Quotes() quote.Repository    { return &QuoteRepository{DB: r.db} }
func (r repositories) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := r.db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error
	return now, wrap("read database time", err)
}
