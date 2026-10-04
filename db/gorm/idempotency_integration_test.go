//go:build integration

package gorm_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	orm "gorm.io/gorm"

	database "order_management/db/gorm"
	"order_management/db/migrations"
	"order_management/domain/order"
	"order_management/domain/shared"
	"order_management/internal/testutil"
	orders "order_management/service/order"
	quotes "order_management/service/quote"
)

func countRows(t *testing.T, db *orm.DB, table string, want int64) {
	t.Helper()
	var count int64
	if err := db.Table(table).Count(&count).Error; err != nil || count != want {
		t.Fatalf("%s: got %d, want %d: %v", table, count, want, err)
	}
}

func TestIdempotentCreateConcurrentRequests(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s := &orders.Service{UOW: &database.UnitOfWork{DB: db}, Repo: &database.OrderRepository{DB: db}}
	cmd := orders.PlaceCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 2}}, IdempotencyKey: "concurrent-checkout"}
	type result struct {
		o      *order.Order
		replay bool
		err    error
	}
	// Independent pools and services model separate API replicas sharing PostgreSQL.
	other := &orders.Service{UOW: &database.UnitOfWork{DB: testutil.Reconnect(t, db)}}
	replicas := []*orders.Service{s, other}
	results := make(chan result, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 8 {
		replica := replicas[i%len(replicas)]
		wg.Go(func() {
			<-start
			o, replay, err := replica.HandlePlace(ctx, cmd)
			results <- result{o, replay, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var id uuid.UUID
	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if id == uuid.Nil {
			id = result.o.ID
		}
		if result.o.ID != id {
			t.Fatal("concurrent retry created another order")
		}
		if !result.replay {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d orders", created)
	}
	countRows(t, db, "orders", 1)
	countRows(t, db, "order_items", 1)
	countRows(t, db, "order_idempotency", 1)
	var keyCreatedAt time.Time
	if err := db.Raw("SELECT created_at FROM order_idempotency WHERE customer_id=? AND idempotency_key=?", customer, cmd.IdempotencyKey).Scan(&keyCreatedAt).Error; err != nil || keyCreatedAt.IsZero() {
		t.Fatalf("missing key timestamp: %v", err)
	}
	if err := db.Exec("UPDATE products SET price_minor=9999 WHERE id=?", p.ID).Error; err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.HandleCancel(ctx, orders.CancelCommand{ID: id, CustomerID: customer})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.HandleCancel(ctx, orders.CancelCommand{ID: id, CustomerID: customer})
	if err != nil || again.Status != order.Cancelled || !again.UpdatedAt.Equal(cancelled.UpdatedAt) {
		t.Fatalf("repeat cancel changed state/timestamp: %+v %v", again, err)
	}
	if _, err := s.HandleCancel(ctx, orders.CancelCommand{ID: id, CustomerID: uuid.New()}); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("foreign cancel: %v", err)
	}
	// A new service and pool replay durable state after a lost response.
	// This exercises reconnection, not a killed OS process or PostgreSQL failover.
	restarted := &orders.Service{UOW: &database.UnitOfWork{DB: testutil.Reconnect(t, db)}}
	o, replay, err := restarted.HandlePlace(ctx, cmd)
	if err != nil || !replay || o.ID != id || o.Status != order.Cancelled || o.TotalMinor != 2598 {
		t.Fatalf("restart replay: %+v %v", o, err)
	}
	var replayCreatedAt time.Time
	if err := db.Raw("SELECT created_at FROM order_idempotency WHERE customer_id=? AND idempotency_key=?", customer, cmd.IdempotencyKey).Scan(&replayCreatedAt).Error; err != nil || !replayCreatedAt.Equal(keyCreatedAt) {
		t.Fatalf("replay reset retention age: %v", err)
	}
	cmd.Items[0].Quantity = 3
	if _, _, err := s.HandlePlace(ctx, cmd); !errors.Is(err, shared.ErrIdempotencyConflict) {
		t.Fatalf("conflicting reuse: %v", err)
	}
	countRows(t, db, "orders", 1)
	// A down migration cannot silently erase committed retry protection.
	down, err := migrations.Files.ReadFile("000004_order_idempotency.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(down)).Error; err == nil {
		t.Fatal("unsafe idempotency rollback allowed")
	}
}

func TestIdempotencyLockTimeoutAndRecovery(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := orders.PlaceCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 1}}, IdempotencyKey: "locked"}
	holder := db.WithContext(ctx).Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	if _, err := (&database.OrderRepository{DB: holder}).LockAndFindIdempotency(ctx, customer, cmd.IdempotencyKey); !errors.Is(err, shared.ErrNotFound) {
		t.Fatal(err)
	}
	var settings struct{ LockTimeout, StatementTimeout string }
	if err := holder.Raw("SELECT current_setting('lock_timeout') AS lock_timeout, current_setting('statement_timeout') AS statement_timeout").Scan(&settings).Error; err != nil || settings.LockTimeout != "2s" || settings.StatementTimeout != "5s" {
		t.Fatalf("timeouts not installed: %+v %v", settings, err)
	}
	s := &orders.Service{UOW: &database.UnitOfWork{DB: db}}
	started := time.Now()
	if _, _, err := s.HandlePlace(ctx, cmd); !errors.Is(err, shared.ErrUnavailable) {
		t.Fatalf("lock wait: %v", err)
	}
	if time.Since(started) > 6*time.Second {
		t.Fatal("lock wait exceeded its bounded budget")
	}
	countRows(t, db, "orders", 0)
	countRows(t, db, "order_idempotency", 0)
	if err := holder.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if _, replay, err := s.HandlePlace(ctx, cmd); err != nil || replay {
		t.Fatalf("retry after lock release: %v", err)
	}
}

func TestIdempotencyStatementTimeoutRollsBack(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.Exec(`CREATE FUNCTION stall_key_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_sleep(6); RETURN NEW; END $$;
CREATE TRIGGER stall_key_insert BEFORE INSERT ON order_idempotency FOR EACH ROW EXECUTE FUNCTION stall_key_insert();`).Error; err != nil {
		t.Fatal(err)
	}
	s := &orders.Service{UOW: &database.UnitOfWork{DB: db}}
	cmd := orders.PlaceCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 1}}, IdempotencyKey: "statement-timeout"}
	if _, _, err := s.HandlePlace(ctx, cmd); !errors.Is(err, shared.ErrUnavailable) {
		t.Fatalf("statement timeout: %v", err)
	}
	countRows(t, db, "orders", 0)
	countRows(t, db, "order_items", 0)
	countRows(t, db, "order_idempotency", 0)
	if err := db.Exec("DROP TRIGGER stall_key_insert ON order_idempotency").Error; err != nil {
		t.Fatal(err)
	}
	if _, replay, err := s.HandlePlace(ctx, cmd); err != nil || replay {
		t.Fatalf("retry after timeout: %v", err)
	}
}

func TestIdempotencyFailureRollsBackOrderAndQuote(t *testing.T) {
	for _, quoted := range []bool{false, true} {
		name := "items"
		if quoted {
			name = "quote"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.PostgreSQL(t)
			customer, p := fixtures(t, db)
			ctx := context.Background()
			unit := &database.UnitOfWork{DB: db}
			s := &orders.Service{UOW: unit}
			cmd := orders.PlaceCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 1}}, IdempotencyKey: "retry-after-rollback"}
			if quoted {
				q, err := (&quotes.Service{UOW: unit, Region: "US", TTL: time.Minute}).HandleCreate(ctx, quotes.CreateCommand{CustomerID: customer, Items: cmd.Items})
				if err != nil {
					t.Fatal(err)
				}
				cmd.QuoteID, cmd.Items = &q.ID, nil
			}
			if err := db.Exec(`CREATE FUNCTION fail_key_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected idempotency failure'; END $$;
CREATE TRIGGER fail_key_insert BEFORE INSERT ON order_idempotency FOR EACH ROW EXECUTE FUNCTION fail_key_insert();`).Error; err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.HandlePlace(ctx, cmd); err == nil {
				t.Fatal("expected key insertion failure")
			}
			countRows(t, db, "orders", 0)
			countRows(t, db, "order_items", 0)
			countRows(t, db, "order_idempotency", 0)
			if quoted {
				var count int64
				if err := db.Table("order_quotes").Where("consumed_order_id IS NOT NULL").Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("quote consumed on rollback: %d %v", count, err)
				}
			}
			if err := db.Exec("DROP TRIGGER fail_key_insert ON order_idempotency").Error; err != nil {
				t.Fatal(err)
			}
			o, replay, err := s.HandlePlace(ctx, cmd)
			if err != nil || replay {
				t.Fatalf("retry after rollback: %v", err)
			}
			if quoted {
				cmd.IdempotencyKey = "another-key-for-same-quote"
				again, replay, err := s.HandlePlace(ctx, cmd)
				if err != nil || !replay || again.ID != o.ID {
					t.Fatalf("quote replay with new key: %v", err)
				}
				countRows(t, db, "order_idempotency", 2)
			}
			countRows(t, db, "orders", 1)
		})
	}
}

func TestConcurrentIdempotencyPayloadConflict(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s := &orders.Service{UOW: &database.UnitOfWork{DB: db}}
	start := make(chan struct{})
	failures := make(chan error, 2)
	for _, quantity := range []int64{1, 2} {
		go func() {
			<-start
			_, _, err := s.HandlePlace(ctx, orders.PlaceCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: quantity}}, IdempotencyKey: "competing-payloads"})
			failures <- err
		}()
	}
	close(start)
	created, conflicts := 0, 0
	for range 2 {
		err := <-failures
		switch {
		case err == nil:
			created++
		case errors.Is(err, shared.ErrIdempotencyConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("got %d creates and %d conflicts", created, conflicts)
	}
	countRows(t, db, "orders", 1)
	countRows(t, db, "order_idempotency", 1)
}
