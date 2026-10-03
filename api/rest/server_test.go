package rest

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestJSONBinderAndErrorEnvelope(t *testing.T) {
	e := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.POST("/input", func(c *echo.Context) error {
		var req struct {
			Name string `json:"name" validate:"required"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		return c.JSON(200, req)
	})
	for _, test := range []struct {
		name, body, content string
		status              int
	}{
		{"valid", `{"name":"Customer"}`, "application/json", 200},
		{"malformed", `{`, "application/json", 400}, {"unknown field", `{"name":"Customer","role":"admin"}`, "application/json", 400},
		{"multiple values", `{"name":"Customer"} {}`, "application/json", 400}, {"validation", `{}`, "application/json", 422},
		{"missing content type", `{}`, "", 415}, {"oversized", `{"name":"` + strings.Repeat("x", 1<<20) + `"}`, "application/json", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/input", strings.NewReader(test.body))
			req.Header.Set("Content-Type", test.content)
			response := httptest.NewRecorder()
			e.ServeHTTP(response, req)
			if response.Code != test.status {
				t.Fatalf("got %d: %s", response.Code, response.Body)
			}
			if test.status >= 400 {
				var apiErr APIError
				if err := json.Unmarshal(response.Body.Bytes(), &apiErr); err != nil || apiErr.Error == "" {
					t.Fatalf("invalid error envelope: %s", response.Body)
				}
			}
		})
	}
}

func TestRequestLogsDoNotExposeBodyOrErrors(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	e := NewServer(logger)
	e.POST("/secret", func(c *echo.Context) error { return errors.New("postgres://user:password@private") })
	req := httptest.NewRequest("POST", "/secret?token=sensitive", strings.NewReader(`{"password":"private-body"}`))
	req.Header.Set("X-Request-ID", "untrusted")
	res := httptest.NewRecorder()
	e.ServeHTTP(res, req)
	if res.Code != 500 || res.Header().Get("X-Request-ID") == "" || res.Header().Get("X-Request-ID") == "untrusted" {
		t.Fatal("request ID/status incorrect")
	}
	for _, secret := range []string{"private-body", "sensitive", "postgres://", "password@"} {
		if strings.Contains(buf.String(), secret) || strings.Contains(res.Body.String(), secret) {
			t.Fatal("secret exposed")
		}
	}
}
