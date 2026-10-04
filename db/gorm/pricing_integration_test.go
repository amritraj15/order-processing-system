//go:build integration

package gorm_test

import (
	"context"
	"errors"
	"github.com/google/uuid"
	database "order_management/db/gorm"
	"order_management/domain/order"
	"order_management/domain/pricing"
	"order_management/domain/shared"
	"order_management/internal/testutil"
	orders "order_management/service/order"
	quotes "order_management/service/quote"
	"sync"
	"testing"
	"time"
)

func TestQuoteConcurrencyAndPersistentSnapshots(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx := context.Background()
	rates := &database.PricingRepository{DB: db}
	now := time.Now().UTC()
	rate := &pricing.Rate{TargetCurrency: "INR", Rate: "2.5", Source: "test-fixture", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour)}
	if err := rates.ImportRate(ctx, rate); err != nil {
		t.Fatal(err)
	}
	duplicate := *rate
	if err := rates.ImportRate(ctx, &duplicate); !errors.Is(err, shared.ErrConflict) {
		t.Fatal("overlapping rate accepted")
	}
	unit := &database.UnitOfWork{DB: db}
	qs := &quotes.Service{UOW: unit, Region: "IN", TTL: time.Minute}
	q, err := qs.HandleCreate(ctx, quotes.CreateCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if q.TotalMinor != 6496 {
		t.Fatalf("rounded total: %d", q.TotalMinor)
	}
	os := &orders.Service{Repo: &database.OrderRepository{DB: db}, UOW: unit}
	if _, _, err := os.HandleCreateFromQuote(ctx, orders.CreateFromQuoteCommand{IdempotencyKey: uuid.NewString(), CustomerID: uuid.New(), QuoteID: q.ID}); !errors.Is(err, shared.ErrNotFound) {
		t.Fatal("quote leaked")
	}
	replicas := []*orders.Service{os, {UOW: &database.UnitOfWork{DB: testutil.Reconnect(t, db)}}}
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 8)
	errs := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		replica := replicas[i%len(replicas)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			o, _, err := replica.HandleCreateFromQuote(ctx, orders.CreateFromQuoteCommand{IdempotencyKey: uuid.NewString(), CustomerID: customer, QuoteID: q.ID})
			if err != nil {
				errs <- err
				return
			}
			ids <- o.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var expected uuid.UUID
	for id := range ids {
		if expected == uuid.Nil {
			expected = id
		}
		if id != expected {
			t.Fatal("duplicate order")
		}
	}
	var count int64
	if err := db.Table("orders").Where("quote_id = ?", q.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("count %d %v", count, err)
	}
	// Remove the expired quote as cleanup would; the order retains provenance and retry identity.
	if _, err := (&database.QuoteRepository{DB: db}).DeleteExpiredBatch(ctx, q.ExpiresAt.Add(time.Hour), 500); err != nil {
		t.Fatal(err)
	}
	replay, again, err := os.HandleCreateFromQuote(ctx, orders.CreateFromQuoteCommand{IdempotencyKey: uuid.NewString(), CustomerID: customer, QuoteID: q.ID})
	if err != nil || !again || replay.TotalMinor != 6496 || replay.Pricing.Rate != "2.500000000000" {
		t.Fatalf("replay/provenance: %+v %v", replay, err)
	}
	if _, err := rates.Initialize(ctx, pricing.InitializeInput{Currency: "EUR", ConfirmExisting: true}); !errors.Is(err, shared.ErrConflict) {
		t.Fatal("currency changed")
	}
}
func TestInitializationAndRateImportsAreSerialized(t *testing.T) {
	db := testutil.PostgreSQL(t)
	ctx := context.Background()
	if err := db.Exec("DELETE FROM catalog_settings").Error; err != nil {
		t.Fatal(err)
	}
	repo := &database.PricingRepository{DB: db}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Initialize(ctx, pricing.InitializeInput{Currency: "USD"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	result := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result <- repo.ImportRate(ctx, &pricing.Rate{TargetCurrency: "INR", Rate: "2", Source: "test", ValidFrom: now, ValidUntil: now.Add(time.Hour)})
		}()
	}
	wg.Wait()
	close(result)
	success, conflict := 0, 0
	for err := range result {
		if err == nil {
			success++
		} else if errors.Is(err, shared.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("rate interval race")
	}
}
func TestLegacyCatalogRequiresExplicitAdoption(t *testing.T) {
	db := testutil.PostgreSQL(t)
	_, _ = fixtures(t, db)
	ctx := context.Background()
	if err := db.Exec("DELETE FROM catalog_settings").Error; err != nil {
		t.Fatal(err)
	}
	repo := &database.PricingRepository{DB: db}
	if _, err := repo.Initialize(ctx, pricing.InitializeInput{Currency: "USD"}); !errors.Is(err, shared.ErrConflict) {
		t.Fatal("implicit legacy adoption")
	}
	if _, err := repo.Initialize(ctx, pricing.InitializeInput{Currency: "USD", ConfirmExisting: true}); err != nil {
		t.Fatal(err)
	}
}
func TestQuoteExpiryCheckedAfterLockWait(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	unit := &database.UnitOfWork{DB: db}
	q, err := (&quotes.Service{UOW: unit, Region: "US", TTL: time.Minute}).HandleCreate(ctx, quotes.CreateCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	tx := db.WithContext(ctx).Begin()
	defer tx.Rollback()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	// Hold the row lock while changing its validity; the waiter must inspect the committed version/time.
	if err := tx.Exec("UPDATE order_quotes SET created_at=now()-interval '2 minutes', expires_at=now()-interval '1 minute' WHERE id = ?", q.ID).Error; err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, err := (&orders.Service{UOW: unit}).HandleCreateFromQuote(ctx, orders.CreateFromQuoteCommand{IdempotencyKey: uuid.NewString(), CustomerID: customer, QuoteID: q.ID})
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%order_quotes%')`).Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("quote reader never waited for the row lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, shared.ErrQuoteExpired) {
		t.Fatalf("expired quote: %v", err)
	}
}

func TestQuoteConsumptionFailureRollsBackOrder(t *testing.T) {
	db := testutil.PostgreSQL(t)
	customer, p := fixtures(t, db)
	ctx := context.Background()
	unit := &database.UnitOfWork{DB: db}
	q, err := (&quotes.Service{UOW: unit, Region: "US", TTL: time.Minute}).HandleCreate(ctx, quotes.CreateCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: p.ID, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE FUNCTION reject_quote_consumption() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected consume failure'; END $$;
 CREATE TRIGGER reject_quote_consumption BEFORE UPDATE ON order_quotes FOR EACH ROW EXECUTE FUNCTION reject_quote_consumption();`).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&orders.Service{UOW: unit}).HandleCreateFromQuote(ctx, orders.CreateFromQuoteCommand{IdempotencyKey: uuid.NewString(), CustomerID: customer, QuoteID: q.ID}); err == nil {
		t.Fatal("expected consumption failure")
	}
	var count int64
	if err := db.Table("orders").Where("quote_id = ?", q.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("orphan order: %d %v", count, err)
	}
}
