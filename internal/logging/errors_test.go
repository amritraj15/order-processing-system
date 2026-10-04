package logging

import (
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
)

func TestSafeErrorDiagnostics(t *testing.T) {
	cause := &pgconn.PgError{Code: "40P01", Message: "private-password", Detail: "private-token"}
	attrs := fmt.Sprint(ErrorAttrs(fmt.Errorf("private-dsn: %w", errors.Join(cause, errors.New("private-body")))))
	if !strings.Contains(attrs, "40P01") || strings.Contains(attrs, "private") {
		t.Fatal("missing classification or leaked values", attrs)
	}
	cause.Code = "private-password"
	if strings.Contains(fmt.Sprint(ErrorAttrs(cause)), "private") {
		t.Fatal("unvalidated SQLSTATE leaked")
	}
}
