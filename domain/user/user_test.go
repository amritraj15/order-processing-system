package user

import (
	"errors"
	"strings"
	"testing"

	"order_management/domain/shared"
)

func TestPasswordLimitsAndActiveUser(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("a", 73), strings.Repeat("é", 37)} {
		if _, err := HashPassword(password); !errors.Is(err, shared.ErrInvalid) {
			t.Fatalf("expected invalid password length, got %v", err)
		}
	}
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	u := &User{PasswordHash: hash, Active: true}
	if !u.Authenticate("correct-password") || u.Authenticate("wrong-password") || hash == "correct-password" {
		t.Fatal("password verification failed")
	}
	u.Active = false
	if u.Authenticate("correct-password") {
		t.Fatal("inactive user authenticated")
	}
}
