package order

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"order_management/domain/product"
	"order_management/domain/shared"
)

func TestNewSnapshotsCatalogPricesAndTotals(t *testing.T) {
	a, b, customer := uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	products := []product.Product{{ID: a, SKU: "A", Name: "Book", PriceMinor: 1299}, {ID: b, SKU: "B", Name: "Pen", PriceMinor: 299}}
	o, err := New(customer, "USD", []ItemInput{{a, 2}, {b, 3}}, products, now)
	if err != nil {
		t.Fatal(err)
	}
	if o.TotalMinor != 3495 || o.Status != Pending || o.CustomerID != customer || o.ID.Version() != 7 || len(o.Items) != 2 {
		t.Fatalf("unexpected order: %+v", o)
	}
	products[0].PriceMinor = 1
	products[0].Name = "Changed"
	if o.Items[0].UnitPriceMinor != 1299 || o.Items[0].Name != "Book" || !o.CreatedAt.Equal(now) || o.CreatedAt.Location() != time.UTC {
		t.Fatal("order snapshot or timestamps changed")
	}
}
func TestNewRejectsInvalidOrders(t *testing.T) {
	id := uuid.New()
	products := []product.Product{{ID: id, PriceMinor: 10}}
	for name, items := range map[string][]ItemInput{
		"empty": {}, "zero quantity": {{id, 0}}, "negative quantity": {{id, -1}},
		"unknown": {{uuid.New(), 1}}, "duplicate": {{id, 1}, {id, 2}}, "line overflow": {{id, math.MaxInt64}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(uuid.New(), "USD", items, products, time.Now())
			if !errors.Is(err, shared.ErrInvalid) {
				t.Fatalf("expected invalid input, got %v", err)
			}
		})
	}
	other := uuid.New()
	products = []product.Product{{ID: id, PriceMinor: math.MaxInt64}, {ID: other, PriceMinor: 1}}
	if _, err := New(uuid.New(), "USD", []ItemInput{{id, 1}, {other, 1}}, products, time.Now()); !errors.Is(err, shared.ErrInvalid) {
		t.Fatalf("expected total overflow, got %v", err)
	}
}
func TestStatusTransitions(t *testing.T) {
	for next, previous := range map[Status]Status{Processing: Pending, Shipped: Processing, Delivered: Shipped} {
		got, err := PreviousStatus(next)
		if err != nil || got != previous {
			t.Fatalf("transition %s: %s %v", next, got, err)
		}
	}
	for _, status := range []Status{Pending, Cancelled, "INVALID"} {
		if _, err := PreviousStatus(status); !errors.Is(err, shared.ErrInvalid) {
			t.Fatalf("accepted %s", status)
		}
	}
}
