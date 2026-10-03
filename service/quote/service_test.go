package quote_test

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"order_management/domain/order"
	"order_management/domain/pricing"
	"order_management/domain/product"
	domain "order_management/domain/quote"
	"order_management/domain/shared"
	orders "order_management/service/order"
	quotes "order_management/service/quote"
	"order_management/service/uow"
	"testing"
	"time"
)

type pricingFake struct {
	pricing.Repository
	rate *pricing.Rate
}

func (p *pricingFake) Settings(context.Context) (*pricing.Settings, error) {
	return &pricing.Settings{BaseCurrency: "USD"}, nil
}
func (p *pricingFake) ActiveRate(context.Context, string, string, time.Time) (*pricing.Rate, error) {
	if p.rate == nil {
		return nil, shared.ErrRateUnavailable
	}
	return p.rate, nil
}

type productsFake struct {
	product.Repository
	p product.Product
}

func (p productsFake) GetMany(context.Context, []uuid.UUID) ([]product.Product, error) {
	return []product.Product{p.p}, nil
}

type quotesFake struct {
	domain.Repository
	q *domain.Quote
}

func (q *quotesFake) Insert(_ context.Context, v *domain.Quote) error { q.q = v; return nil }
func (q *quotesFake) GetForUpdate(_ context.Context, id, customer uuid.UUID) (*domain.Quote, error) {
	if q.q == nil || id != q.q.ID || customer != q.q.CustomerID {
		return nil, shared.ErrNotFound
	}
	return q.q, nil
}
func (q *quotesFake) MarkConsumed(_ context.Context, id, orderID uuid.UUID) error {
	q.q.ConsumedOrderID = &orderID
	return nil
}

type ordersFake struct {
	order.Repository
	o *order.Order
}

func (o *ordersFake) FindByQuote(_ context.Context, id, customer uuid.UUID) (*order.Order, error) {
	if o.o == nil || *o.o.QuoteID != id || o.o.CustomerID != customer {
		return nil, shared.ErrNotFound
	}
	return o.o, nil
}
func (o *ordersFake) Insert(_ context.Context, v *order.Order) error { o.o = v; return nil }
func (o *ordersFake) Get(_ context.Context, id uuid.UUID, customer *uuid.UUID) (*order.Order, error) {
	return o.o, nil
}

type unit struct {
	now      time.Time
	p        *pricingFake
	products productsFake
	q        *quotesFake
	o        *ordersFake
}

func (u *unit) Do(ctx context.Context, fn func(uow.Repositories) error) error { return fn(u) }
func (u *unit) Now(context.Context) (time.Time, error)                        { return u.now, nil }
func (u *unit) Pricing() pricing.Repository                                   { return u.p }
func (u *unit) Products() product.Repository                                  { return u.products }
func (u *unit) Quotes() domain.Repository                                     { return u.q }
func (u *unit) Orders() order.Repository                                      { return u.o }
func TestQuoteSnapshotsExpiryOwnershipAndReplay(t *testing.T) {
	now := time.Now().UTC()
	rate := &pricing.Rate{ID: uuid.New(), Rate: "2", Source: "test", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Minute)}
	u := &unit{now: now, p: &pricingFake{rate: rate}, products: productsFake{p: product.Product{ID: uuid.New(), SKU: "book", Name: "Book", PriceMinor: 1299}}, q: &quotesFake{}, o: &ordersFake{}}
	service := &quotes.Service{UOW: u, Region: "IN", TTL: 5 * time.Minute}
	customer := uuid.New()
	cmd := quotes.CreateCommand{CustomerID: customer, Items: []order.ItemInput{{ProductID: u.products.p.ID, Quantity: 2}}}
	q, err := service.HandleCreate(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if q.Currency != "INR" || q.TotalMinor != 5196 || q.Pricing.SourceTotalMinor != 2598 || !q.ExpiresAt.Equal(rate.ValidUntil) {
		t.Fatalf("bad quote: %+v", q)
	}
	rate.Rate = "3"
	u.products.p.PriceMinor = 9999
	orderService := &orders.Service{UOW: u}
	if _, _, err := orderService.HandleCreateFromQuote(context.Background(), orders.CreateFromQuoteCommand{CustomerID: uuid.New(), QuoteID: q.ID}); !errors.Is(err, shared.ErrNotFound) {
		t.Fatal("ownership not enforced")
	}
	o, replay, err := orderService.HandleCreateFromQuote(context.Background(), orders.CreateFromQuoteCommand{CustomerID: customer, QuoteID: q.ID})
	if err != nil || replay || o.TotalMinor != 5196 || o.Pricing.Rate != "2" {
		t.Fatalf("snapshot changed: %+v %v", o, err)
	}
	u.now = q.ExpiresAt.Add(time.Hour)
	u.q.q = nil
	repeated, replay, err := orderService.HandleCreateFromQuote(context.Background(), orders.CreateFromQuoteCommand{CustomerID: customer, QuoteID: q.ID})
	if err != nil || !replay || repeated.ID != o.ID {
		t.Fatal("replay after cleanup failed")
	}
	u.o.o = nil
	u.q.q = q
	q.ConsumedOrderID = nil
	if _, _, err := orderService.HandleCreateFromQuote(context.Background(), orders.CreateFromQuoteCommand{CustomerID: customer, QuoteID: q.ID}); !errors.Is(err, shared.ErrQuoteExpired) {
		t.Fatal("expired quote accepted")
	}
	u.p.rate = nil
	if _, err := service.HandleCreate(context.Background(), cmd); !errors.Is(err, shared.ErrRateUnavailable) {
		t.Fatal("missing rate accepted")
	}
}
