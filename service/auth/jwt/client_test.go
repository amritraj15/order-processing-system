package jwt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"order_management/domain/shared"
	"order_management/domain/user"
	"order_management/ports"
)

type usersFake struct{ users map[uuid.UUID]*user.User }

func (r *usersFake) Insert(_ context.Context, u *user.User) error {
	for _, existing := range r.users {
		if existing.Email == u.Email {
			return shared.ErrConflict
		}
	}
	r.users[u.ID] = u
	return nil
}
func (r *usersFake) Get(_ context.Context, id uuid.UUID) (*user.User, error) {
	if u, ok := r.users[id]; ok {
		return u, nil
	}
	return nil, shared.ErrNotFound
}
func (r *usersFake) GetByEmail(_ context.Context, email string) (*user.User, error) {
	for _, u := range r.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, shared.ErrNotFound
}

type denylistFake struct {
	hashes map[string]time.Time
	fail   bool
}

func (r *denylistFake) Add(_ context.Context, hash string, expiry time.Time) error {
	r.hashes[hash] = expiry
	return nil
}
func (r *denylistFake) Exists(_ context.Context, hash string) (bool, error) {
	if r.fail {
		return false, errors.New("database unavailable")
	}
	expiry, ok := r.hashes[hash]
	return ok && time.Now().Before(expiry), nil
}
func newFixture(t *testing.T) (*Client, *usersFake, *denylistFake) {
	t.Helper()
	repo := &usersFake{users: map[uuid.UUID]*user.User{}}
	denied := &denylistFake{hashes: map[string]time.Time{}}
	c, err := NewClient(Config{Secret: strings.Repeat("s", 32), Issuer: "test", TokenTTL: time.Hour}, repo, denied)
	if err != nil {
		t.Fatal(err)
	}
	return c, repo, denied
}
func TestRegisterLoginSessionLogout(t *testing.T) {
	c, repo, denied := newFixture(t)
	ctx := context.Background()
	session, err := c.Register(ctx, "Customer", "CUSTOMER@example.com", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	if session.User.Role != string(user.Customer) || session.User.Email != "customer@example.com" || session.Token == "" || !session.Authenticated {
		t.Fatalf("unexpected session: %+v", session)
	}
	if _, err := c.Register(ctx, "Customer", "customer@example.com", "password-123"); !errors.Is(err, shared.ErrConflict) {
		t.Fatalf("duplicate email: %v", err)
	}
	for _, email := range []string{"missing@example.com", "customer@example.com"} {
		if _, err := c.Login(ctx, email, "wrong"); !errors.Is(err, ports.ErrInvalidCredentials) {
			t.Fatalf("bad credentials: %v", err)
		}
	}
	login, err := c.Login(ctx, "CUSTOMER@example.com", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	if login.Token == session.Token {
		t.Fatal("separate logins must have distinct tokens")
	}
	if current, err := c.Session(ctx, login.Token); err != nil || !current.Authenticated {
		t.Fatalf("session: %+v %v", current, err)
	}
	if err := c.Logout(ctx, login.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ValidateToken(ctx, login.Token); !errors.Is(err, ports.ErrInvalidToken) {
		t.Fatalf("revoked token accepted: %v", err)
	}
	if _, ok := denied.hashes[login.Token]; ok {
		t.Fatal("raw token stored in denylist")
	}
	if _, err := c.ValidateToken(ctx, session.Token); err != nil {
		t.Fatalf("logout revoked another token: %v", err)
	}
	u := repo.users[uuid.MustParse(session.User.ID)]
	u.Active = false
	if _, err := c.ValidateToken(ctx, session.Token); !errors.Is(err, ports.ErrInvalidToken) {
		t.Fatal("inactive user accepted")
	}
}
func TestRejectsInvalidJWTAndChecksCurrentRole(t *testing.T) {
	c, repo, denied := newFixture(t)
	ctx := context.Background()
	session, err := c.Register(ctx, "Customer", "c@example.com", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	u := repo.users[uuid.MustParse(session.User.ID)]
	u.Role = user.Admin
	validated, err := c.ValidateToken(ctx, session.Token)
	if err != nil || validated.Role != string(user.Admin) {
		t.Fatalf("role was not loaded from database: %+v %v", validated, err)
	}
	for _, raw := range []string{"", "invalid", session.Token + "x"} {
		if _, err := c.ValidateToken(ctx, raw); !errors.Is(err, ports.ErrInvalidToken) {
			t.Fatalf("accepted invalid token: %v", err)
		}
	}
	for name, mutate := range map[string]func(*claims){
		"expired":   func(v *claims) { v.ExpiresAt = jwtv5.NewNumericDate(time.Now().Add(-time.Hour)) },
		"no expiry": func(v *claims) { v.ExpiresAt = nil },
		"issuer":    func(v *claims) { v.Issuer = "other" },
		"subject":   func(v *claims) { v.Subject = uuid.NewString() },
	} {
		t.Run(name, func(t *testing.T) {
			v := claims{UserID: u.ID.String(), RegisteredClaims: jwtv5.RegisteredClaims{Subject: u.ID.String(), Issuer: "test", ExpiresAt: jwtv5.NewNumericDate(time.Now().Add(time.Hour))}}
			mutate(&v)
			raw, err := jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, v).SignedString([]byte(c.cfg.Secret))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.ValidateToken(ctx, raw); !errors.Is(err, ports.ErrInvalidToken) {
				t.Fatalf("accepted %s: %v", name, err)
			}
		})
	}
	v := claims{UserID: u.ID.String(), RegisteredClaims: jwtv5.RegisteredClaims{Subject: u.ID.String(), Issuer: "test", ExpiresAt: jwtv5.NewNumericDate(time.Now().Add(time.Hour))}}
	raw, _ := jwtv5.NewWithClaims(jwtv5.SigningMethodHS384, v).SignedString([]byte(c.cfg.Secret))
	if _, err := c.ValidateToken(ctx, raw); !errors.Is(err, ports.ErrInvalidToken) {
		t.Fatal("accepted unexpected signing algorithm")
	}
	denied.fail = true
	if _, err := c.ValidateToken(ctx, session.Token); err == nil {
		t.Fatal("auth failed open on denylist failure")
	}
}

func TestLoginVerifiesMissingAndInactiveAccounts(t *testing.T) {
	c, repo, _ := newFixture(t)
	ctx := context.Background()
	id := uuid.New()
	repo.users[id] = &user.User{ID: id, Email: "known@example.com", PasswordHash: "known-hash", Active: false}
	calls := 0
	c.verify = func(hash, password []byte) error {
		calls++
		if len(hash) == 0 {
			t.Fatal("empty dummy")
		}
		return nil
	}
	for _, email := range []string{"missing@example.com", "known@example.com"} {
		if _, err := c.Login(ctx, email, "password"); !errors.Is(err, ports.ErrInvalidCredentials) {
			t.Fatalf("accepted %s", email)
		}
	}
	if calls != 2 {
		t.Fatalf("verifier called %d times", calls)
	}
	repo.users[id].Active = true
	c.verify = func([]byte, []byte) error { calls++; return errors.New("wrong password") }
	if _, err := c.Login(ctx, "known@example.com", "wrong"); !errors.Is(err, ports.ErrInvalidCredentials) || calls != 3 {
		t.Fatal("known failure path")
	}
}

func TestRegistrationRejectsNULBeforeHashingOrPersistence(t *testing.T) {
	for _, fields := range [][2]string{{"bad\x00name", "user@example.com"}, {"Customer", "bad\x00email"}} {
		if _, err := (&Client{}).Register(context.Background(), fields[0], fields[1], "password-123"); !errors.Is(err, shared.ErrInvalid) {
			t.Fatal(err)
		}
	}
}
