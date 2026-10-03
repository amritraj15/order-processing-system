//go:build integration

package gorm_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	database "order_management/db/gorm"
	"order_management/domain/order"
	"order_management/internal/testutil"
	orders "order_management/service/order"
)

func TestIdempotencyMigrationUpgradeAndNonemptyDowngradeGuard(t *testing.T) {
	for _, missingTimestamp := range []bool{true, false} {
		name := "version4_with_timestamp"
		if missingTimestamp {
			name = "version4_without_timestamp"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.PostgreSQL(t)
			ctx := context.Background()
			customer, p := fixtures(t, db)
			service := &orders.Service{UOW: &database.UnitOfWork{DB: db}}
			cmd := orders.PlaceCommand{CustomerID: customer, IdempotencyKey: "survives-upgrade", Items: []order.ItemInput{{ProductID: p.ID, Quantity: 1}}}
			original, _, err := service.HandlePlace(ctx, cmd)
			if err != nil {
				t.Fatal(err)
			}
			var originalTimestamp time.Time
			if err := db.Raw("SELECT created_at FROM order_idempotency").Scan(&originalTimestamp).Error; err != nil {
				t.Fatal(err)
			}
			// Reconstruct the two historical version-4 variants only in this
			// test's isolated schema. Run real golang-migrate for the upgrade.
			if missingTimestamp {
				if err := db.Exec("ALTER TABLE order_idempotency DROP COLUMN created_at").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Exec("UPDATE schema_migrations SET version=4, dirty=false").Error; err != nil {
				t.Fatal(err)
			}
			var schema string
			if err := db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
				t.Fatal(err)
			}
			dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal("invalid test database URL")
			}
			query := dsn.Query()
			query.Set("search_path", schema)
			dsn.RawQuery = query.Encode()
			var beforeUpgrade time.Time
			if err := db.Raw("SELECT clock_timestamp()").Scan(&beforeUpgrade).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(ctx, dsn.String(), false); err != nil {
				t.Fatal(err)
			}
			var state struct {
				Version int
				Dirty   bool
			}
			if err := db.Table("schema_migrations").Take(&state).Error; err != nil || state.Version != 5 || state.Dirty {
				t.Fatalf("bad migration state: %+v %v", state, err)
			}
			var upgradedTimestamp time.Time
			if err := db.Raw("SELECT created_at FROM order_idempotency").Scan(&upgradedTimestamp).Error; err != nil || upgradedTimestamp.IsZero() {
				t.Fatalf("timestamp absent: %v", err)
			}
			if missingTimestamp && upgradedTimestamp.Before(beforeUpgrade) {
				t.Fatal("backfill guessed a historical timestamp")
			}
			if !missingTimestamp && !upgradedTimestamp.Equal(originalTimestamp) {
				t.Fatal("upgrade changed existing retention age")
			}
			if got, replay, err := service.HandlePlace(ctx, cmd); err != nil || !replay || got.ID != original.ID {
				t.Fatalf("upgrade lost key binding: %v", err)
			}
			// Version 5 down retains metadata; version 4 down must explicitly
			// refuse deletion because there is a durable key in the table.
			if err := database.Migrate(ctx, dsn.String(), true); err != nil {
				t.Fatal(err)
			}
			err = database.Migrate(ctx, dsn.String(), true)
			if err == nil || !strings.Contains(err.Error(), "cannot remove nonempty order idempotency records") {
				t.Fatalf("expected deliberate downgrade refusal, got %v", err)
			}
			countRows(t, db, "order_idempotency", 1)
			countRows(t, db, "orders", 1)
			if err := db.Table("schema_migrations").Take(&state).Error; err != nil || !state.Dirty {
				t.Fatalf("expected dirty marker on refused migration: %+v %v", state, err)
			}
			// Schema cleanup is owned by testutil; never force this state on a
			// real application database simply to bypass the downgrade guard.
		})
	}
}
