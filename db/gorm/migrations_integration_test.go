//go:build integration

package gorm_test

import (
	"context"
	"github.com/google/uuid"
	database "order_management/db/gorm"
	"order_management/db/migrations"
	"order_management/domain/pricing"
	"order_management/internal/testutil"
	"strings"
	"testing"
	"time"
)

func TestPricingMigrationsRoundTripAndLegacyPreservation(t *testing.T) {
	db := testutil.PostgreSQL(t)
	apply := func(name string) {
		t.Helper()
		sql, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(string(sql)).Error; err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	apply("000005_idempotency_created_at.down.sql")
	apply("000004_order_idempotency.down.sql")
	apply("000003_order_quotes.down.sql")
	apply("000002_pricing.down.sql")
	apply("000002_pricing.up.sql")
	if _, err := (&database.PricingRepository{DB: db}).Initialize(context.Background(), pricing.InitializeInput{Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	customer, _ := fixtures(t, db)
	id := uuid.New()
	if err := db.Exec("INSERT INTO orders(id,customer_id,status,currency,total_minor) VALUES (?,?,'DELIVERED','USD',1299)", id, customer).Error; err != nil {
		t.Fatal(err)
	}
	apply("000003_order_quotes.up.sql")
	apply("000004_order_idempotency.up.sql")
	apply("000005_idempotency_created_at.up.sql")
	o, err := (&database.OrderRepository{DB: db}).Get(context.Background(), id, &customer)
	if err != nil || o.Currency != "USD" || o.TotalMinor != 1299 || o.Pricing != nil {
		t.Fatalf("legacy repriced: %+v %v", o, err)
	}
	// A new-format row must block a destructive down migration.
	if err := db.Exec("UPDATE orders SET pricing_mode='base', source_currency='USD', base_digits=2, target_digits=2, rate=1, rate_source='identity',source_total_minor=1299 WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	sql, err := migrations.Files.ReadFile("000003_order_quotes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(sql)).Error; err == nil {
		t.Fatal("unsafe rollback allowed")
	}
}
func TestQuoteCleanupUsesExpiryIndex(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, _ := fixtures(t, db)
	if err := db.Exec(`INSERT INTO order_quotes(id,customer_id,region,mapping_version,source_currency,currency,base_digits,target_digits,rate,rate_source,source_total_minor,total_minor,created_at,expires_at)
 SELECT gen_random_uuid(),?,'US','markets-v1','USD','USD',2,2,1,'identity',100,100,now()-interval '2 days', CASE WHEN n<=10 THEN now()-interval '1 day' ELSE now()+interval '1 day' END FROM generate_series(1,10000) n`, customer).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE order_quotes").Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	defer tx.Rollback()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	var plan []struct {
		Line string `gorm:"column:QUERY PLAN"`
	}
	if err := tx.Raw("EXPLAIN (ANALYZE, BUFFERS) "+database.QuoteCleanupSQL, time.Now(), 500).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range plan {
		t.Log(line.Line)
		if strings.Contains(line.Line, "order_quotes_expiry_idx") {
			found = true
		}
	}
	if !found {
		t.Fatal("expiry index not used")
	}
}
