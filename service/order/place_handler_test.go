package order

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	domain "order_management/domain/order"
	"order_management/domain/pricing"
	"order_management/domain/product"
	"order_management/domain/shared"
	"order_management/service/uow"
)

type testOrders struct {
	domain.Repository
	orders  map[uuid.UUID]*domain.Order
	records map[string]domain.IdempotencyRecord
}

func (r *testOrders) Insert(_ context.Context, o *domain.Order) error {
	r.orders[o.ID] = o
	return nil
}
func (r *testOrders) Get(_ context.Context, id uuid.UUID, customer *uuid.UUID) (*domain.Order, error) {
	o, ok := r.orders[id]
	if !ok || customer != nil && o.CustomerID != *customer {
		return nil, shared.ErrNotFound
	}
	return o, nil
}
func (r *testOrders) LockAndFindIdempotency(_ context.Context, customer uuid.UUID, key string) (*domain.IdempotencyRecord, error) {
	record, ok := r.records[customer.String()+":"+key]
	if !ok {
		return nil, shared.ErrNotFound
	}
	return &record, nil
}
func (r *testOrders) InsertIdempotency(_ context.Context, record domain.IdempotencyRecord) error {
	r.records[record.CustomerID.String()+":"+record.Key] = record
	return nil
}

type testProducts struct {
	product.Repository
	product product.Product
	reads   int
}

func (p *testProducts) GetMany(context.Context, []uuid.UUID) ([]product.Product, error) {
	p.reads++
	return []product.Product{p.product}, nil
}

type testPricing struct{ pricing.Repository }

func (testPricing) Settings(context.Context) (*pricing.Settings, error) {
	return &pricing.Settings{BaseCurrency: "USD"}, nil
}

type testUnit struct {
	uow.Repositories
	orders   *testOrders
	products *testProducts
}

func (u *testUnit) Do(ctx context.Context, fn func(uow.Repositories) error) error { return fn(u) }
func (u *testUnit) Orders() domain.Repository                                     { return u.orders }
func (u *testUnit) Products() product.Repository                                  { return u.products }
func (u *testUnit) Pricing() pricing.Repository                                   { return testPricing{} }
func (u *testUnit) Now(context.Context) (time.Time, error)                        { return time.Now(), nil }

func TestPlaceReplayConflictOwnershipAndUnkeyed(t *testing.T) {
	ctx := context.Background()
	products := &testProducts{product: product.Product{ID: uuid.New(), Name: "Book", SKU: "BOOK", PriceMinor: 1299}}
	repo := &testOrders{orders: map[uuid.UUID]*domain.Order{}, records: map[string]domain.IdempotencyRecord{}}
	s := Service{UOW: &testUnit{orders: repo, products: products}}
	cmd := PlaceCommand{CustomerID: uuid.New(), Items: []domain.ItemInput{{ProductID: products.product.ID, Quantity: 2}}, IdempotencyKey: "checkout-1"}
	original, replay, err := s.HandlePlace(ctx, cmd)
	if err != nil || replay {
		t.Fatalf("create: %v, replay=%v", err, replay)
	}
	products.product.PriceMinor = 9999
	original.Status = domain.Cancelled
	again, replay, err := s.HandlePlace(ctx, cmd)
	if err != nil || !replay || again.ID != original.ID || again.TotalMinor != 2598 || again.Status != domain.Cancelled || products.reads != 1 {
		t.Fatalf("replay must preserve ID/prices and return current status without catalog reads: %+v %v", again, err)
	}
	changed := cmd
	changed.Items = []domain.ItemInput{{ProductID: products.product.ID, Quantity: 3}}
	if _, _, err := s.HandlePlace(ctx, changed); !errors.Is(err, shared.ErrIdempotencyConflict) {
		t.Fatalf("expected payload conflict: %v", err)
	}
	other := cmd
	other.CustomerID = uuid.New()
	o, replay, err := s.HandlePlace(ctx, other)
	if err != nil || replay || o.ID == original.ID || o.CustomerID != other.CustomerID {
		t.Fatalf("key must be scoped to customer: %+v %v", o, err)
	}
	cmd.IdempotencyKey = ""
	a, _, err := s.HandlePlace(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	b, replay, err := s.HandlePlace(ctx, cmd)
	if err != nil || replay || a.ID == b.ID {
		t.Fatalf("unkeyed behavior changed: %v", err)
	}
}

func TestPlaceValidationBeforeTransaction(t *testing.T) {
	valid := PlaceCommand{CustomerID: uuid.New(), Items: []domain.ItemInput{{ProductID: uuid.New(), Quantity: 1}}}
	for _, key := range []string{"has space", "with,comma", "é", strings.Repeat("x", 129)} {
		cmd := valid
		cmd.IdempotencyKey = key
		if _, _, err := (&Service{}).HandlePlace(context.Background(), cmd); !errors.Is(err, shared.ErrInvalid) {
			t.Fatalf("invalid key accepted: %q %v", key, err)
		}
	}
	for _, key := range []string{"abc:DEF-123._", strings.Repeat("a", 128)} {
		if !ValidIdempotencyKey(key) {
			t.Fatalf("valid key rejected: %q", key)
		}
	}
	if ValidIdempotencyKey("") {
		t.Fatal("explicit empty header should be invalid")
	}
	quoteID := uuid.New()
	valid.QuoteID = &quoteID
	if _, _, err := (&Service{}).HandlePlace(context.Background(), valid); !errors.Is(err, shared.ErrInvalid) {
		t.Fatal("quote and items accepted together")
	}
}

func TestRequestFingerprintContract(t *testing.T) {
	cmd := PlaceCommand{CustomerID: uuid.New(), Items: []domain.ItemInput{{ProductID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Quantity: 2}}}
	hash, err := requestHash(cmd)
	if err != nil {
		t.Fatal(err)
	}
	// A fixed digest guards durable keys against accidental encoding changes.
	const expected = "2bb082f77401884458a7b10e8466a916c77a252674c73d8e43cf0737e33ab778"
	if hash != expected {
		t.Fatalf("fingerprint changed: %s", hash)
	}
	cmd.IdempotencyKey = "another-key"
	cmd.CustomerID = uuid.New()
	other, _ := requestHash(cmd)
	if other != hash {
		t.Fatal("request hash must exclude key and owner (owner is in unique scope)")
	}
}

type unitFunc func(context.Context, func(uow.Repositories) error) error

func (fn unitFunc) Do(ctx context.Context, work func(uow.Repositories) error) error {
	return fn(ctx, work)
}

func TestKeyedCreateDeadline(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		ctx := context.Background()
		callerDeadline := time.Now().Add(3 * time.Second)
		if earlier {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, callerDeadline)
			defer cancel()
		}
		var workCtx context.Context
		started := time.Now()
		s := &Service{UOW: unitFunc(func(ctx context.Context, _ func(uow.Repositories) error) error {
			workCtx = ctx
			deadline, ok := ctx.Deadline()
			if !ok || deadline.After(started.Add(10*time.Second+100*time.Millisecond)) {
				t.Fatalf("missing/too long deadline: %v", deadline)
			}
			if earlier && !deadline.Equal(callerDeadline) {
				t.Fatal("extended caller deadline")
			}
			return context.DeadlineExceeded
		})}
		cmd := PlaceCommand{CustomerID: uuid.New(), IdempotencyKey: "bounded", Items: []domain.ItemInput{{ProductID: uuid.New(), Quantity: 1}}}
		if o, replay, err := s.HandlePlace(ctx, cmd); o != nil || replay || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline failure misreported: %+v %v %v", o, replay, err)
		}
		if workCtx.Err() == nil {
			t.Fatal("child context not released after completion")
		}
	}
}
