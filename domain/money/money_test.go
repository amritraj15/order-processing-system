package money

import (
	"errors"
	"math"
	"order_management/domain/shared"
	"testing"
)

func TestConversion(t *testing.T) {
	for _, tc := range []struct {
		name, from, to, rate string
		amount, want         int64
	}{
		{"identity", "USD", "USD", "1", 1299, 1299},
		{"zero digits", "USD", "JPY", "150", 1299, 1948},
		{"three digits", "USD", "KWD", "0.3", 1299, 3897},
		{"half even down", "USD", "USD", "0.5", 5, 2},
		{"half even up", "USD", "USD", "0.5", 7, 4},
		{"minor scaling", "KWD", "USD", "2", 1000, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Convert(tc.amount, tc.from, tc.to, tc.rate)
			if err != nil || got != tc.want {
				t.Fatalf("got %d/%v want %d", got, err, tc.want)
			}
		})
	}
	for _, rate := range []string{"0", "-1", "1e3", "NaN", "1.0000000000001", "1000000000000", " 1"} {
		if _, err := ParseRate(rate); err == nil {
			t.Errorf("accepted %q", rate)
		}
	}
	for _, tc := range []struct {
		amount int64
		rate   string
	}{{1, "0.1"}, {math.MaxInt64, "2"}} {
		if _, err := Convert(tc.amount, "USD", "USD", tc.rate); !errors.Is(err, shared.ErrAmountRange) {
			t.Fatalf("want amount range, got %v", err)
		}
	}
}
