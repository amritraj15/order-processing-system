//go:build integration

package testutil

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	orm "gorm.io/gorm"

	database "order_management/db/gorm"
	"order_management/domain/pricing"
)

// PostgreSQL isolates each test in a unique schema in the supplied test DB.
// It never drops a database or alters an existing application's schema.
func PostgreSQL(t *testing.T) *orm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	base, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := baseSQL.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); err != nil {
		_ = baseSQL.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error(err)
		}
		_ = baseSQL.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	if err := database.Migrate(context.Background(), u.String(), false); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sql.Close() })
	if _, err := (&database.PricingRepository{DB: db}).Initialize(context.Background(), pricing.InitializeInput{Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	return db
}
