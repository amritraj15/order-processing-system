package user

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"order_management/domain/shared"
)

type Role string

const (
	Customer Role = "customer"
	Admin    Role = "admin"
)

type User struct {
	ID                        uuid.UUID
	Name, Email, PasswordHash string
	Role                      Role
	Active                    bool
	CreatedAt, UpdatedAt      time.Time
}

func HashPassword(password string) (string, error) {
	// bcrypt limits input to 72 bytes, including multibyte UTF-8 characters.
	if len(password) < 8 || len(password) > 72 {
		return "", fmt.Errorf("%w: password must be 8–72 bytes", shared.ErrInvalid)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}
func (u *User) Authenticate(password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password))
	return err == nil && u.Active
}

type Repository interface {
	Insert(context.Context, *User) error
	Get(context.Context, uuid.UUID) (*User, error)
	GetByEmail(context.Context, string) (*User, error)
}
