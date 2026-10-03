package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/google/uuid"

	"order_management/domain/order"
)

type idempotencyRow struct {
	CustomerID     uuid.UUID `gorm:"type:uuid;primaryKey"`
	IdempotencyKey string    `gorm:"primaryKey"`
	RequestHash    string
	OrderID        uuid.UUID
	CreatedAt      time.Time `gorm:"->;autoCreateTime:false"` // Database default; never reset on replay.
}

func (idempotencyRow) TableName() string { return "order_idempotency" }

func (r *OrderRepository) LockAndFindIdempotency(ctx context.Context, customer uuid.UUID, key string) (*order.IdempotencyRecord, error) {
	// Transaction-local settings protect the pool without leaking to the next
	// borrower of this connection. The service also bounds the entire operation.
	if err := r.DB.WithContext(ctx).Exec("SELECT set_config('lock_timeout', '2s', true), set_config('statement_timeout', '5s', true)").Error; err != nil {
		return nil, wrap("set keyed order timeouts", err)
	}
	// All callers use the same transaction for the lock, lookup, order and key.
	// Hash collisions only serialize unrelated requests; the full unique key
	// below determines identity. PostgreSQL releases the lock on rollback/crash.
	hash := sha256.Sum256([]byte("order-create:" + customer.String() + ":" + key))
	lockID := int64(binary.BigEndian.Uint64(hash[:8]))
	if err := r.DB.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(?)", lockID).Error; err != nil {
		return nil, wrap("lock order idempotency key", err)
	}
	var row idempotencyRow
	if err := r.DB.WithContext(ctx).Where("customer_id = ? AND idempotency_key = ?", customer, key).First(&row).Error; err != nil {
		return nil, wrap("find order idempotency key", err)
	}
	return &order.IdempotencyRecord{CustomerID: row.CustomerID, Key: row.IdempotencyKey, RequestHash: row.RequestHash, OrderID: row.OrderID, CreatedAt: row.CreatedAt}, nil
}

func (r *OrderRepository) InsertIdempotency(ctx context.Context, record order.IdempotencyRecord) error {
	row := idempotencyRow{CustomerID: record.CustomerID, IdempotencyKey: record.Key, RequestHash: record.RequestHash, OrderID: record.OrderID}
	return wrap("insert order idempotency key", r.DB.WithContext(ctx).Create(&row).Error)
}
