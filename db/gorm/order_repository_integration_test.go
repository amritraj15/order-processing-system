//go:build integration

package gorm_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	orm "gorm.io/gorm"

	database "order_management/db/gorm"
	"order_management/domain/order"
	"order_management/domain/product"
	"order_management/domain/shared"
	"order_management/domain/user"
	"order_management/internal/testutil"
	orderservice "order_management/service/order"
	"order_management/service/uow"
)

func fixtures(t *testing.T, db *orm.DB) (uuid.UUID, product.Product) {
	t.Helper()
	ctx := context.Background()
	customer := uuid.New()
	now := time.Now().UTC()
	if err := (&database.UserRepository{DB: db}).Insert(ctx, &user.User{ID: customer, Name: "Test", Email: "test@example.com", PasswordHash: "unused", Role: user.Customer, Active: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := product.Product{ID: uuid.New(), SKU: "BOOK", Name: "Book", PriceMinor: 1299, CreatedAt: now, UpdatedAt: now}
	if err := (&database.ProductRepository{DB: db}).Insert(ctx, &p); err != nil {
		t.Fatal(err)
	}
	return customer, p
}
func TestOrderTransactionSnapshotsAndStatus(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx := context.Background()
	repo := &database.OrderRepository{DB: db}
	unit := &database.UnitOfWork{DB: db}
	s := &orderservice.Service{Repo: repo, UOW: unit, Currency: "USD"}
	o, err := s.HandleCreate(ctx, orderservice.CreateCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.TotalMinor != 2598 || o.Status != order.Pending {
		t.Fatalf("unexpected order: %+v", o)
	}
	if err := db.Exec("UPDATE products SET price_minor = 9999, name = 'Changed' WHERE id = ?", p.ID).Error; err != nil {
		t.Fatal(err)
	}
	fetched, err := repo.Get(ctx, o.ID, &customer)
	if err != nil || fetched.Items[0].UnitPriceMinor != 1299 || fetched.Items[0].Name != "Book" {
		t.Fatalf("snapshot changed: %+v %v", fetched, err)
	}
	other := uuid.New()
	if _, err := repo.Get(ctx, o.ID, &other); !errors.Is(err, shared.ErrNotFound) {
		t.Fatal("customer could access another order")
	}
	if _, err := repo.Transition(ctx, o.ID, nil, order.Processing, order.Shipped); !errors.Is(err, shared.ErrConflict) {
		t.Fatalf("skipped transition: %v", err)
	}
	for _, transition := range [][2]order.Status{{order.Pending, order.Processing}, {order.Processing, order.Shipped}, {order.Shipped, order.Delivered}} {
		if _, err := repo.Transition(ctx, o.ID, nil, transition[0], transition[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.Transition(ctx, o.ID, &customer, order.Pending, order.Cancelled); !errors.Is(err, shared.ErrConflict) {
		t.Fatal("delivered order cancelled")
	}
	var before int64
	db.Table("orders").Count(&before)
	err = unit.Do(ctx, func(repos uow.Repositories) error {
		invalid, err := order.New(customer, "USD", []order.ItemInput{{ProductID: p.ID, Quantity: 1}}, []product.Product{p}, time.Now())
		if err != nil {
			return err
		}
		// Force failure after insertion of the parent: its item violates the FK.
		invalid.Items[0].ProductID = uuid.New()
		return repos.Orders().Insert(ctx, invalid)
	})
	if err == nil {
		t.Fatal("expected item insert failure")
	}
	var after int64
	db.Table("orders").Count(&after)
	if after != before {
		t.Fatal("failed order left an orphan parent")
	}
}

func TestOrderTransitionUsesDatabaseTime(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, product := fixtures(t, db)
	ctx := context.Background()
	service := &orderservice.Service{UOW: &database.UnitOfWork{DB: db}}
	o, err := service.HandleCreate(ctx, orderservice.CreateCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: product.ID, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *orm.DB) error {
		var databaseTime time.Time
		if err := tx.Raw("SELECT now()").Scan(&databaseTime).Error; err != nil {
			return err
		}
		repo := &database.OrderRepository{DB: tx}
		updated, err := repo.Transition(ctx, o.ID, nil, order.Pending, order.Processing)
		if err != nil {
			return err
		}
		if !updated.UpdatedAt.Equal(databaseTime) {
			t.Errorf("updated_at %s differs from database transaction time %s", updated.UpdatedAt, databaseTime)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPendingBatchesConcurrencyCutoffAndQueryPlan(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, _ := fixtures(t, db)
	ctx := context.Background()
	repo := &database.OrderRepository{DB: db}
	if err := db.Exec(`INSERT INTO orders (id,customer_id,status,currency,total_minor,created_at,pricing_mode)
        SELECT gen_random_uuid(), ?, 'DELIVERED', 'USD', 100, now() - interval '1 day', 'legacy' FROM generate_series(1,20000)`, customer).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO orders (id,customer_id,status,currency,total_minor,created_at,pricing_mode)
        SELECT gen_random_uuid(), ?, 'PENDING', 'USD', 100, now() - interval '1 minute', 'legacy' FROM generate_series(1,1201)`, customer).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE orders").Error; err != nil {
		t.Fatal(err)
	}
	var plan []struct {
		QueryPlan string `gorm:"column:QUERY PLAN"`
	}
	// Explain the actual update pipeline, then roll back its measured writes.
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := tx.Raw("EXPLAIN (ANALYZE, BUFFERS) "+database.PendingBatchSQL, time.Now(), 100).Scan(&plan).Error; err != nil {
		_ = tx.Rollback().Error
		t.Fatal(err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range plan {
		lines = append(lines, line.QueryPlan)
	}
	explain := strings.Join(lines, "\n")
	t.Log(explain)
	if !strings.Contains(explain, "orders_pending_idx") || strings.Contains(explain, "Seq Scan on orders") {
		t.Fatalf("worker lookup did not use pending index:\n%s", explain)
	}
	cutoff := time.Now().UTC()
	future := uuid.New()
	if err := db.Exec("INSERT INTO orders (id,customer_id,status,currency,total_minor,created_at,pricing_mode) VALUES (?,?,'PENDING','USD',100,?,'legacy')", future, customer, cutoff.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	counts := make(chan int, 4)
	failures := make(chan error, 4)
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			sum := 0
			for {
				n, err := repo.ProcessBatch(ctx, cutoff, 100)
				if err != nil {
					failures <- err
					return
				}
				if n > 100 {
					failures <- errors.New("batch limit exceeded")
					return
				}
				sum += n
				if n == 0 {
					break
				}
			}
			counts <- sum
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	close(counts)
	for err := range failures {
		t.Fatal(err)
	}
	sum := 0
	for n := range counts {
		sum += n
	}
	if sum != 1201 {
		t.Fatalf("processed %d, expected 1201", sum)
	}
	var remaining int64
	db.Table("orders").Where("status = 'PENDING'").Count(&remaining)
	if remaining != 1 {
		t.Fatalf("wrong cutoff: %d pending", remaining)
	}
}
func TestCancellationAndProcessingRace(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx := context.Background()
	repo := &database.OrderRepository{DB: db}
	service := &orderservice.Service{Repo: repo, UOW: &database.UnitOfWork{DB: db}, Currency: "USD"}
	for i := 0; i < 20; i++ {
		o, err := service.HandleCreate(ctx, orderservice.CreateCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		var cancelErr, processErr error
		var count int
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, cancelErr = repo.Transition(ctx, o.ID, &customer, order.Pending, order.Cancelled)
		}()
		go func() { defer wg.Done(); <-start; count, processErr = repo.ProcessBatch(ctx, time.Now(), 1) }()
		close(start)
		wg.Wait()
		if processErr != nil {
			t.Fatal(processErr)
		}
		got, err := repo.Get(ctx, o.ID, &customer)
		if err != nil {
			t.Fatal(err)
		}
		if cancelErr == nil {
			if count != 0 || got.Status != order.Cancelled {
				t.Fatal("processing overwrote cancellation")
			}
		} else if !errors.Is(cancelErr, shared.ErrConflict) || count != 1 || got.Status != order.Processing {
			t.Fatalf("unexpected race: %v %d %s", cancelErr, count, got.Status)
		}
	}
}

func TestPendingSkipsLockedRowsAndRollsBackFailedBatch(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := &database.OrderRepository{DB: db}
	s := &orderservice.Service{Repo: repo, UOW: &database.UnitOfWork{DB: db}, Currency: "USD"}
	create := func() *order.Order {
		t.Helper()
		o, err := s.HandleCreate(ctx, orderservice.CreateCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	locked := create()
	create()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var id uuid.UUID
	// Use database/sql's Scanner path: GORM's top-level Scan treats UUID's
	// underlying [16]byte as an array destination instead of a scalar UUID.
	if err := tx.Raw("SELECT id FROM orders WHERE id = ? FOR UPDATE", locked.ID).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != locked.ID {
		t.Fatal("locked a different order")
	}
	if n, err := repo.ProcessBatch(ctx, time.Now(), 500); err != nil || n != 1 {
		t.Fatalf("locked row was not skipped: %d %v", n, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if n, err := repo.ProcessBatch(ctx, time.Now(), 500); err != nil || n != 1 {
		t.Fatalf("released row was not retried: %d %v", n, err)
	}
	create()
	create()
	if err := db.Exec(`CREATE FUNCTION fail_processing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'PROCESSING' THEN RAISE EXCEPTION 'injected batch failure'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER fail_processing BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION fail_processing();`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ProcessBatch(ctx, time.Now(), 500); err == nil {
		t.Fatal("expected batch failure")
	}
	var pending int64
	if err := db.Table("orders").Where("status = 'PENDING'").Count(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("failed batch changed statuses: %d pending", pending)
	}
}
