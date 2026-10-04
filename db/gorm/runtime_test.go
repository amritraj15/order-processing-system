package gorm

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"order_management/domain/order"
	"order_management/domain/shared"
	"testing"
	"time"
)

func TestConnectionLimitsAndDSNForms(t *testing.T) {
	for _, dsn := range []string{"postgres://user@localhost/orders?sslmode=disable&search_path=isolated&statement_timeout=0", "host=localhost user=test dbname=orders sslmode=disable search_path=isolated statement_timeout=0"} {
		c, err := connectionConfig(dsn, DefaultPoolConfig())
		if err != nil {
			t.Fatal(err)
		}
		if c.RuntimeParams["statement_timeout"] != "5000" || c.RuntimeParams["lock_timeout"] != "2000" || c.RuntimeParams["idle_in_transaction_session_timeout"] != "10000" || c.RuntimeParams["search_path"] != "isolated" || c.ConnectTimeout != 5*time.Second {
			t.Fatalf("connection limits not applied: %+v", c.RuntimeParams)
		}
	}
	limits := DefaultPoolConfig()
	limits.MaxOpen = 0
	if _, err := connectionConfig("postgres://localhost/orders", limits); err == nil {
		t.Fatal("accepted invalid pool")
	}
}
func TestDatabaseErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		code  string
		retry bool
	}{{"55P03", true}, {"57014", true}, {"40P01", true}, {"40001", true}, {"53300", true}, {"57P01", true}, {"08006", true}, {"22012", false}, {"22021", false}, {"53100", false}, {"23514", false}, {"57P04", false}, {"08P01", false}} {
		cause := &pgconn.PgError{Code: tc.code, Message: "private values"}
		err := mapError(fmt.Errorf("query: %w", cause))
		if errors.Is(err, shared.ErrUnavailable) != tc.retry || !errors.Is(err, cause) {
			t.Fatalf("classification %s: %v", tc.code, err)
		}
	}
	cause := errors.Join(context.Canceled, &pgconn.PgError{Code: "57014"})
	if err := mapError(cause); !errors.Is(err, context.Canceled) || errors.Is(err, shared.ErrUnavailable) {
		t.Fatal("caller cancellation became retryable outage")
	}
}
func TestRepositoriesRejectInvalidPaginationBeforeDatabaseAccess(t *testing.T) {
	for _, limit := range []int{-1, 0, 101} {
		p := shared.Pagination{Limit: limit}
		if _, err := (&OrderRepository{}).List(context.Background(), order.Filter{Pagination: p}); !errors.Is(err, shared.ErrInvalid) {
			t.Fatal("order pagination", err)
		}
		if _, err := (&ProductRepository{}).List(context.Background(), p); !errors.Is(err, shared.ErrInvalid) {
			t.Fatal("product pagination", err)
		}
	}
}
