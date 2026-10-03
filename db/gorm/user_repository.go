package gorm

import (
	"context"
	"time"

	"github.com/google/uuid"
	orm "gorm.io/gorm"
	"gorm.io/gorm/clause"

	"order_management/domain/user"
	"order_management/ports"
)

type userRow struct {
	ID                        uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name, Email, PasswordHash string
	Role                      user.Role
	Active                    bool
	CreatedAt, UpdatedAt      time.Time
}

func (userRow) TableName() string { return "users" }
func userFrom(row userRow) *user.User {
	return &user.User{ID: row.ID, Name: row.Name, Email: row.Email, PasswordHash: row.PasswordHash, Role: row.Role, Active: row.Active, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

type UserRepository struct{ DB *orm.DB }

func (r *UserRepository) Insert(ctx context.Context, u *user.User) error {
	return wrap("insert resource", r.DB.WithContext(ctx).Create(&userRow{ID: u.ID, Name: u.Name, Email: u.Email, PasswordHash: u.PasswordHash, Role: u.Role, Active: u.Active, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}).Error)
}
func (r *UserRepository) Get(ctx context.Context, id uuid.UUID) (*user.User, error) {
	var row userRow
	err := r.DB.WithContext(ctx).First(&row, "id = ?", id).Error
	return userFrom(row), wrap("read user", err)
}
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	var row userRow
	err := r.DB.WithContext(ctx).First(&row, "email = ?", email).Error
	return userFrom(row), wrap("read user", err)
}

type tokenRow struct {
	TokenHash string `gorm:"primaryKey"`
	ExpiresAt time.Time
}

func (tokenRow) TableName() string { return "denylisted_tokens" }

type TokenDenylist struct{ DB *orm.DB }

func (r *TokenDenylist) Add(ctx context.Context, hash string, expiry time.Time) error {
	return wrap("revoke token", r.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&tokenRow{TokenHash: hash, ExpiresAt: expiry}).Error)
}
func (r *TokenDenylist) Exists(ctx context.Context, hash string) (bool, error) {
	var count int64
	err := r.DB.WithContext(ctx).Model(&tokenRow{}).Where("token_hash = ? AND expires_at > now()", hash).Count(&count).Error
	return count > 0, wrap("user_repository.Exists", err)
}
func (r *TokenDenylist) Purge(ctx context.Context) error {
	return wrap("purge revoked tokens", r.DB.WithContext(ctx).Where("expires_at <= now()").Delete(&tokenRow{}).Error)
}

var _ user.Repository = (*UserRepository)(nil)
var _ ports.TokenDenylist = (*TokenDenylist)(nil)
