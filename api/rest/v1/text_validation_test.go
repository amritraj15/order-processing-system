package v1

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"order_management/api/rest"
	"strings"
	"testing"
)

func TestStoredTextRejectsNUL(t *testing.T) {
	e := rest.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Nil services ensure validation happens before any hashing/database work.
	e.POST("/products", (&ProductHandler{}).Create)
	e.POST("/register", (&AuthHandler{}).Register)
	for _, tc := range []struct{ path, body string }{
		{"/products", `{"sku":"B\u0000OOK","name":"Book","price_minor":100}`},
		{"/products", `{"sku":"BOOK","name":"B\u0000ook","price_minor":100}`},
		{"/register", `{"name":"A\u0000lice","email":"a@example.com","password":"password-123"}`},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r := httptest.NewRecorder()
		e.ServeHTTP(r, req)
		if r.Code != 422 {
			t.Fatalf("%s: %d %s", tc.path, r.Code, r.Body)
		}
	}
}
