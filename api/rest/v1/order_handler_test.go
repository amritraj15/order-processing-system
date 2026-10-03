package v1_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"order_management/api/rest"
	"order_management/api/rest/middleware"
	"order_management/api/rest/v1"
	"order_management/ports"
)

// Exercise handler guards even if a future binder only decodes JSON.
type decodingOnlyBinder struct{}

func (decodingOnlyBinder) Bind(c *echo.Context, value any) error {
	return json.NewDecoder(c.Request().Body).Decode(value)
}

func TestCreateRejectsInvalidUUIDs(t *testing.T) {
	for _, validate := range []bool{true, false} {
		name := "production binder"
		if !validate {
			name = "decoding-only binder"
		}
		t.Run(name, func(t *testing.T) {
			e := rest.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if !validate {
				e.Binder = decodingOnlyBinder{}
			}
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c *echo.Context) error {
					c.Set(middleware.AuthClaimsKey, &ports.AuthClaims{UserID: uuid.NewString(), Role: "customer"})
					return next(c)
				}
			})
			// Invalid input must be rejected before accessing any service or DB.
			e.POST("/orders", (&v1.OrderHandler{}).Create)
			e.POST("/order-quotes", (&v1.QuoteHandler{}).Create)
			for _, test := range []struct{ name, path, body string }{
				{"malformed quote", "/orders", `{"quote_id":"invalid"}`},
				{"nil quote", "/orders", `{"quote_id":"00000000-0000-0000-0000-000000000000"}`},
				{"malformed order item", "/orders", `{"items":[{"product_id":"invalid","quantity":1}]}`},
				{"missing order item ID", "/orders", `{"items":[{"quantity":1}]}`},
				{"nil order item", "/orders", `{"items":[{"product_id":"00000000-0000-0000-0000-000000000000","quantity":1}]}`},
				{"malformed quote item", "/order-quotes", `{"items":[{"product_id":"invalid","quantity":1}]}`},
				{"missing quote item ID", "/order-quotes", `{"items":[{"quantity":1}]}`},
				{"nil quote item", "/order-quotes", `{"items":[{"product_id":"00000000-0000-0000-0000-000000000000","quantity":1}]}`},
			} {
				t.Run(test.name, func(t *testing.T) {
					req := httptest.NewRequest("POST", test.path, strings.NewReader(test.body))
					req.Header.Set("Content-Type", "application/json")
					res := httptest.NewRecorder()
					e.ServeHTTP(res, req)
					if res.Code != 422 {
						t.Fatalf("expected 422 without a panic/service call, got %d: %s", res.Code, res.Body)
					}
				})
			}
		})
	}
}

func TestCreateRejectsInvalidIdempotencyHeaders(t *testing.T) {
	e := rest.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Set(middleware.AuthClaimsKey, &ports.AuthClaims{UserID: uuid.NewString(), Role: "customer"})
			return next(c)
		}
	})
	e.POST("/orders", (&v1.OrderHandler{}).Create)
	for _, keys := range [][]string{{""}, {"a b"}, {"a,b"}, {strings.Repeat("a", 129)}, {"one", "two"}} {
		req := httptest.NewRequest("POST", "/orders", strings.NewReader(`{"items":[{"product_id":"`+uuid.NewString()+`","quantity":1}]}`))
		req.Header.Set("Content-Type", "application/json")
		for _, key := range keys {
			req.Header.Add("Idempotency-Key", key)
		}
		res := httptest.NewRecorder()
		e.ServeHTTP(res, req)
		if res.Code != 422 {
			t.Fatalf("invalid header %q should fail before service access: %d %s", keys, res.Code, res.Body)
		}
	}
}
