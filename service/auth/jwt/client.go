// Package jwt adapts Surge's JWT provider to customer/admin identities.
package jwt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"order_management/domain/shared"
	"order_management/domain/user"
	"order_management/ports"
)

type Config struct {
	Secret, Issuer string
	TokenTTL       time.Duration
}
type Client struct {
	dummyHash []byte
	verify    func([]byte, []byte) error
	cfg       Config
	repo      user.Repository
	denylist  ports.TokenDenylist
}
type claims struct {
	UserID string    `json:"user_id"`
	Role   user.Role `json:"role"`
	jwtv5.RegisteredClaims
}

func NewClient(cfg Config, repo user.Repository, denylist ports.TokenDenylist) (*Client, error) {
	if len(cfg.Secret) < 32 || cfg.Issuer == "" || cfg.TokenTTL <= 0 || repo == nil || denylist == nil {
		return nil, errors.New("invalid JWT configuration")
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(uuid.NewString()), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("prepare password verification: %w", err)
	}
	return &Client{cfg: cfg, repo: repo, denylist: denylist, dummyHash: dummy, verify: bcrypt.CompareHashAndPassword}, nil
}
func (c *Client) Register(ctx context.Context, name, email, password string) (*ports.AuthSession, error) {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", shared.ErrInvalid)
	}
	hash, err := user.HashPassword(password)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	u := &user.User{ID: id, Name: name, Email: email, PasswordHash: hash, Role: user.Customer, Active: true, CreatedAt: now, UpdatedAt: now}
	if err := c.repo.Insert(ctx, u); err != nil {
		return nil, err
	}
	return c.issueSession(u)
}
func (c *Client) Login(ctx context.Context, email, password string) (*ports.AuthSession, error) {
	u, err := c.repo.GetByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	missing := errors.Is(err, shared.ErrNotFound)
	if err != nil && !missing {
		return nil, fmt.Errorf("lookup login account: %w", err)
	}
	hash := c.dummyHash
	if !missing {
		hash = []byte(u.PasswordHash)
	}
	verifyErr := c.verify(hash, []byte(password))
	if missing || verifyErr != nil || !u.Active {
		return nil, ports.ErrInvalidCredentials
	}
	return c.issueSession(u)
}
func (c *Client) parse(raw string) (*claims, error) {
	v := new(claims)
	token, err := jwtv5.ParseWithClaims(raw, v, func(*jwtv5.Token) (any, error) { return []byte(c.cfg.Secret), nil }, jwtv5.WithValidMethods([]string{"HS256"}), jwtv5.WithIssuer(c.cfg.Issuer), jwtv5.WithExpirationRequired(), jwtv5.WithIssuedAt())
	if err != nil || !token.Valid || v.UserID == "" || v.Subject != v.UserID {
		return nil, ports.ErrInvalidToken
	}
	if _, err := uuid.Parse(v.UserID); err != nil {
		return nil, ports.ErrInvalidToken
	}
	return v, nil
}
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func (c *Client) authenticatedUser(ctx context.Context, raw string) (*user.User, error) {
	v, err := c.parse(raw)
	if err != nil {
		return nil, err
	}
	denied, err := c.denylist.Exists(ctx, hashToken(raw))
	if err != nil {
		return nil, err
	}
	if denied {
		return nil, ports.ErrInvalidToken
	}
	id, _ := uuid.Parse(v.UserID)
	u, err := c.repo.Get(ctx, id)
	if errors.Is(err, shared.ErrNotFound) {
		return nil, ports.ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	if !u.Active || (u.Role != user.Customer && u.Role != user.Admin) {
		return nil, ports.ErrInvalidToken
	}
	return u, nil
}
func (c *Client) ValidateToken(ctx context.Context, raw string) (*ports.AuthClaims, error) {
	u, err := c.authenticatedUser(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &ports.AuthClaims{UserID: u.ID.String(), Role: string(u.Role)}, nil
}
func info(u *user.User) *ports.AuthUserInfo {
	return &ports.AuthUserInfo{ID: u.ID.String(), Email: u.Email, Status: "active", Role: string(u.Role)}
}
func (c *Client) Session(ctx context.Context, raw string) (*ports.AuthSession, error) {
	u, err := c.authenticatedUser(ctx, raw)
	if errors.Is(err, ports.ErrInvalidToken) {
		return &ports.AuthSession{Authenticated: false}, nil
	}
	if err != nil {
		return nil, err
	}
	return &ports.AuthSession{Authenticated: true, User: info(u)}, nil
}
func (c *Client) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	v, err := c.parse(raw)
	if err != nil {
		return err
	}
	return c.denylist.Add(ctx, hashToken(raw), v.ExpiresAt.Time)
}
func (c *Client) issueSession(u *user.User) (*ports.AuthSession, error) {
	now := time.Now().UTC()
	expiry := now.Add(c.cfg.TokenTTL)
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	v := claims{UserID: u.ID.String(), Role: u.Role, RegisteredClaims: jwtv5.RegisteredClaims{Issuer: c.cfg.Issuer, Subject: u.ID.String(), ID: id.String(), IssuedAt: jwtv5.NewNumericDate(now), ExpiresAt: jwtv5.NewNumericDate(expiry)}}
	raw, err := jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, v).SignedString([]byte(c.cfg.Secret))
	if err != nil {
		return nil, err
	}
	return &ports.AuthSession{Authenticated: true, Token: raw, ExpiresAt: expiry.Unix(), User: info(u)}, nil
}

var _ ports.Authenticator = (*Client)(nil)
