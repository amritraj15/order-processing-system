package ports

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidCredentials = errors.New("invalid credentials")
var ErrInvalidToken = errors.New("invalid token")

type AuthClaims struct {
	UserID string
	Role   string
}
type AuthUserInfo struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
	Role   string `json:"role"`
}
type AuthSession struct {
	Authenticated bool          `json:"authenticated"`
	Token         string        `json:"token,omitempty"`
	ExpiresAt     int64         `json:"expires_at,omitempty"`
	User          *AuthUserInfo `json:"user,omitempty"`
}
type Authenticator interface {
	Register(context.Context, string, string, string) (*AuthSession, error)
	Login(context.Context, string, string) (*AuthSession, error)
	ValidateToken(context.Context, string) (*AuthClaims, error)
	Session(context.Context, string) (*AuthSession, error)
	Logout(context.Context, string) error
}
type TokenDenylist interface {
	Add(context.Context, string, time.Time) error
	Exists(context.Context, string) (bool, error)
}
