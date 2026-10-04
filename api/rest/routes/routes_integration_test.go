//go:build integration

package routes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"order_management/api/rest"
	"order_management/api/rest/routes"
	"order_management/api/rest/v1"
	database "order_management/db/gorm"
	"order_management/domain/user"
	"order_management/internal/testutil"
	"order_management/service/auth/jwt"
	"order_management/service/order"
	"order_management/service/product"
	"order_management/service/quote"
)

func TestCustomerAndAdminOrderFlow(t *testing.T) {
	db := testutil.PostgreSQL(t)
	users := &database.UserRepository{DB: db}
	repo := &database.OrderRepository{DB: db}
	auth, err := jwt.NewClient(jwt.Config{Secret: strings.Repeat("s", 32), Issuer: "test", TokenTTL: time.Hour}, users, &database.TokenDenylist{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	e := rest.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	routes.Mount(e, routes.MountConfig{Auth: auth, Quotes: &v1.QuoteHandler{Service: &quote.Service{UOW: &database.UnitOfWork{DB: db}, Region: "US", TTL: time.Minute}}, Orders: &v1.OrderHandler{Service: &order.Service{Repo: repo, UOW: &database.UnitOfWork{DB: db}, Currency: "USD"}}, Products: &v1.ProductHandler{Service: &product.Service{Repo: &database.ProductRepository{DB: db}}, Currency: "USD"}, Readiness: func(context.Context) error { return nil }})
	call := func(method, path, token string, body any, want int, keys ...string) map[string]any {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		for _, key := range keys {
			request.Header.Add("Idempotency-Key", key)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, response.Code, want, response.Body.String())
		}
		result := map[string]any{}
		if response.Body.Len() > 0 {
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	register := func(email string) string {
		t.Helper()
		response := call("POST", "/api/v1/auth/register", "", map[string]any{"name": "Customer", "email": email, "password": "password-123"}, 201)
		return response["token"].(string)
	}
	customer := register("customer@example.com")
	other := register("other@example.com")
	call("POST", "/api/v1/auth/register", "", map[string]any{"name": "Customer", "email": "CUSTOMER@example.com", "password": "password-123"}, 409)
	call("POST", "/api/v1/auth/register", "", map[string]any{"name": "Customer", "email": "evil@example.com", "password": "password-123", "role": "admin"}, 400)
	call("POST", "/api/v1/auth/login", "", map[string]any{"email": "customer@example.com", "password": "wrong"}, 401)
	adminHash, err := user.HashPassword("admin-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := users.Insert(context.Background(), &user.User{ID: uuid.New(), Name: "Admin", Email: "admin@example.com", PasswordHash: adminHash, Role: user.Admin, Active: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	admin := call("POST", "/api/v1/auth/login", "", map[string]any{"email": "admin@example.com", "password": "admin-password"}, 200)["token"].(string)
	call("GET", "/api/v1/orders", "", nil, 401)
	call("POST", "/api/v1/products", customer, map[string]any{"sku": "BOOK", "name": "Book", "price_minor": 100}, 403)
	p1 := call("POST", "/api/v1/products", admin, map[string]any{"sku": "BOOK", "name": "Book", "price_minor": 1299}, 201)["id"].(string)
	p2 := call("POST", "/api/v1/products", admin, map[string]any{"sku": "PEN", "name": "Pen", "price_minor": 299}, 201)["id"].(string)
	call("POST", "/api/v1/products", admin, map[string]any{"sku": "BOOK", "name": "Book", "price_minor": 1299}, 409)
	call("GET", "/api/v1/products/"+p1, customer, nil, 200)
	call("GET", "/api/v1/products?limit=1", customer, nil, 200)
	payload := map[string]any{"items": []map[string]any{{"product_id": p1, "quantity": 2}, {"product_id": p2, "quantity": 3}}}
	// Missing keys on either request form are rejected without creating records.
	call("POST", "/api/v1/orders", customer, payload, 422)
	call("POST", "/api/v1/orders", customer, map[string]string{"quote_id": uuid.NewString()}, 422)
	for _, table := range []string{"orders", "order_items", "order_idempotency"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("missing key mutated %s: %d %v", table, count, err)
		}
	}
	created := call("POST", "/api/v1/orders", customer, payload, 201, uuid.NewString())
	id := created["id"].(string)
	if created["total_minor"] != float64(3495) || len(created["items"].([]any)) != 2 {
		t.Fatalf("incorrect totals: %+v", created)
	}
	call("GET", "/api/v1/orders/"+id, customer, nil, 200)
	call("GET", "/api/v1/orders/"+id, other, nil, 404)
	call("GET", "/api/v1/orders/"+id, admin, nil, 200)
	call("POST", "/api/v1/orders/"+id+"/cancel", other, nil, 404)
	call("PATCH", "/api/v1/orders/"+id+"/status", customer, map[string]string{"status": "PROCESSING"}, 403)
	call("PATCH", "/api/v1/orders/"+id+"/status", admin, map[string]string{"status": "SHIPPED"}, 409)
	call("POST", "/api/v1/orders", customer, map[string]any{"items": []any{}}, 422, uuid.NewString())
	call("POST", "/api/v1/orders", customer, map[string]any{"items": []map[string]any{{"product_id": uuid.NewString(), "quantity": 1}}}, 422, uuid.NewString())
	call("POST", "/api/v1/orders", customer, map[string]any{"items": []map[string]any{{"product_id": p1, "quantity": 1}, {"product_id": p1, "quantity": 1}}}, 422, uuid.NewString())
	call("POST", "/api/v1/orders", customer, map[string]any{"items": []map[string]any{{"product_id": p1, "quantity": 1, "unit_price_minor": 1}}}, 400, uuid.NewString())
	cancelID := call("POST", "/api/v1/orders", customer, payload, 201, uuid.NewString())["id"].(string)
	call("POST", "/api/v1/orders/"+cancelID+"/cancel", customer, nil, 200)
	call("POST", "/api/v1/orders/"+cancelID+"/cancel", customer, nil, 200)
	if n, err := repo.ProcessBatch(context.Background(), time.Now(), 500); err != nil || n != 1 {
		t.Fatalf("processing count: %d %v", n, err)
	}
	call("POST", "/api/v1/orders/"+id+"/cancel", customer, nil, 409)
	for _, status := range []string{"SHIPPED", "DELIVERED"} {
		call("PATCH", "/api/v1/orders/"+id+"/status", admin, map[string]string{"status": status}, 200)
	}
	page := call("GET", "/api/v1/orders?limit=1", customer, nil, 200)
	if len(page["items"].([]any)) != 1 || page["next_cursor"] == nil {
		t.Fatalf("pagination failed: %+v", page)
	}
	page = call("GET", "/api/v1/orders?limit=1&cursor="+page["next_cursor"].(string), customer, nil, 200)
	if len(page["items"].([]any)) != 1 || page["next_cursor"] != nil {
		t.Fatalf("second page failed: %+v", page)
	}
	if items := call("GET", "/api/v1/orders?status=CANCELLED", customer, nil, 200)["items"].([]any); len(items) != 1 {
		t.Fatal("status filter failed")
	}
	if items := call("GET", "/api/v1/orders", other, nil, 200)["items"].([]any); len(items) != 0 {
		t.Fatal("list leaked orders")
	}
	if items := call("GET", "/api/v1/orders", admin, nil, 200)["items"].([]any); len(items) != 2 {
		t.Fatal("admin list incomplete")
	}
	call("GET", "/api/v1/orders?status=BAD", customer, nil, 422)
	call("GET", "/api/v1/orders?limit=0", customer, nil, 422)
	quoted := call("POST", "/api/v1/order-quotes", customer, payload, 201)
	quotedID := quoted["id"].(string)
	call("POST", "/api/v1/orders", other, map[string]string{"quote_id": quotedID}, 404, uuid.NewString())
	createdQuoteOrder := call("POST", "/api/v1/orders", customer, map[string]string{"quote_id": quotedID}, 201, uuid.NewString())
	repeated := call("POST", "/api/v1/orders", customer, map[string]string{"quote_id": quotedID}, 200, uuid.NewString())
	if repeated["id"] != createdQuoteOrder["id"] {
		t.Fatal("quote retry duplicated order")
	}
	call("POST", "/api/v1/orders", customer, map[string]any{"quote_id": quotedID, "items": payload["items"]}, 422, uuid.NewString())
	call("POST", "/api/v1/order-quotes", customer, map[string]any{"region": "XX", "items": payload["items"]}, 422)
	call("POST", "/api/v1/order-quotes", customer, map[string]any{"region": "IN", "items": payload["items"]}, 409)
	// General idempotency applies to both request forms, with customer isolation.
	keyed := call("POST", "/api/v1/orders", customer, payload, 201, "checkout-1")
	// Whitespace, object property order and UUID letter case are not new payloads.
	equivalent := json.RawMessage(`{ "items": [{"quantity":2,"product_id":"` + strings.ToUpper(p1) + `"},{"quantity":3,"product_id":"` + p2 + `"}] }`)
	if retry := call("POST", "/api/v1/orders", customer, equivalent, 200, "checkout-1"); retry["id"] != keyed["id"] {
		t.Fatal("key retry duplicated order")
	}
	changed := map[string]any{"items": []map[string]any{{"product_id": p1, "quantity": 3}}}
	conflict := call("POST", "/api/v1/orders", customer, changed, 409, "checkout-1")
	if conflict["details"].(map[string]any)["reason"] != "idempotency_key_conflict" {
		t.Fatalf("wrong conflict reason: %+v", conflict)
	}
	if own := call("POST", "/api/v1/orders", other, payload, 201, "checkout-1"); own["id"] == keyed["id"] {
		t.Fatal("key leaked another customer's order")
	}
	for _, key := range []string{"", "contains spaces", strings.Repeat("a", 129)} {
		call("POST", "/api/v1/orders", customer, payload, 422, key)
	}
	call("POST", "/api/v1/orders", customer, payload, 422, "first", "second")
	keyedID := keyed["id"].(string)
	call("POST", "/api/v1/orders/"+keyedID+"/cancel", customer, nil, 200)
	call("POST", "/api/v1/orders/"+keyedID+"/cancel", customer, nil, 200)
	call("POST", "/api/v1/orders/"+keyedID+"/cancel", other, nil, 404)
	if retry := call("POST", "/api/v1/orders", customer, payload, 200, "checkout-1"); retry["status"] != "CANCELLED" || retry["id"] != keyedID {
		t.Fatal("retry must return current state of original order")
	}
	quoteBody := map[string]string{"quote_id": quotedID}
	for _, key := range []string{"quote-key-1", "quote-key-2", "quote-key-1"} {
		if retry := call("POST", "/api/v1/orders", customer, quoteBody, 200, key); retry["id"] != createdQuoteOrder["id"] {
			t.Fatal("keyed quote replay duplicated order")
		}
	}
	// Both aliases must retain their binding; neither can later create a different purchase.
	call("POST", "/api/v1/orders", customer, payload, 409, "quote-key-1")
	call("POST", "/api/v1/orders", customer, payload, 409, "quote-key-2")
	call("POST", "/api/v1/orders", customer, quoteBody, 409, "checkout-1")
	call("POST", "/api/v1/auth/logout", customer, nil, 204)
	call("GET", "/api/v1/orders", customer, nil, 401)
	if call("GET", "/api/v1/auth/session", customer, nil, 200)["authenticated"] != false {
		t.Fatal("revoked session accepted")
	}
}
