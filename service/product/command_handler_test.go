package product

import (
	"context"
	"errors"
	"order_management/domain/shared"
	"testing"
)

func TestProductRejectsNULBeforePersistence(t *testing.T) {
	for _, cmd := range []CreateCommand{{SKU: "bad\x00sku", Name: "Book", PriceMinor: 100}, {SKU: "BOOK", Name: "bad\x00name", PriceMinor: 100}} {
		if _, err := (&Service{}).HandleCreate(context.Background(), cmd); !errors.Is(err, shared.ErrInvalid) {
			t.Fatal(err)
		}
	}
}
