package middleware_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"order_management/api/rest"
	"order_management/api/rest/middleware"
	"order_management/api/rest/routes"
	"order_management/service/processing"
)

func TestAdmissionPrecedesAuthenticationAndProbesBypassIt(t *testing.T) {
	e := rest.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	limits := middleware.NewRequestLimits(1, time.Second)
	e.Use(limits.Middleware)
	routes.Mount(e, routes.MountConfig{Readiness: func(context.Context) error { return nil }, WorkerStatus: processing.NewStatus(time.Now(), time.Minute), Draining: limits.Draining})
	entered, release := make(chan struct{}), make(chan struct{})
	var authCalls atomic.Int32
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error { authCalls.Add(1); return next(c) }
	}
	e.GET("/work", func(c *echo.Context) error { close(entered); <-release; return c.NoContent(204) }, auth)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/work", nil))
	}()
	<-entered
	for _, tc := range []struct {
		path string
		want int
	}{{"/work", 503}, {"/health", 200}, {"/api/v1/health", 200}, {"/api/v1/ready", 200}} {
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest("GET", tc.path, nil))
		if r.Code != tc.want {
			t.Errorf("%s: %d", tc.path, r.Code)
		}
	}
	if authCalls.Load() != 1 {
		t.Error("rejected request reached authentication")
	}
	limits.Drain()
	r := httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/ready", nil))
	if r.Code != 503 {
		t.Error("draining readiness did not fail")
	}
	close(release)
	<-done
	r = httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest("GET", "/work", nil))
	if r.Code != 503 || authCalls.Load() != 1 {
		t.Error("draining instance admitted work")
	}
}

func TestRequestBudgetIncludesAuthenticationAndReleasesSlot(t *testing.T) {
	e := rest.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	limits := middleware.NewRequestLimits(1, 20*time.Millisecond)
	e.Use(limits.Middleware)
	e.GET("/blocked", func(c *echo.Context) error { t.Error("handler ran after auth timeout"); return nil }, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error { <-c.Request().Context().Done(); return errors.New("driver cancellation") }
	})
	e.GET("/available", func(c *echo.Context) error { return c.NoContent(204) })
	r := httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest("GET", "/blocked", nil))
	if r.Code != 503 {
		t.Fatalf("timeout %d", r.Code)
	}
	r = httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest("GET", "/available", nil))
	if r.Code != 204 {
		t.Fatalf("slot leaked: %d", r.Code)
	}
}

func TestPanicReleasesAdmissionSlot(t *testing.T) {
	e := rest.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.Use(middleware.NewRequestLimits(1, time.Second).Middleware)
	e.GET("/panic", func(c *echo.Context) error { panic("private") })
	e.GET("/ok", func(c *echo.Context) error { return c.NoContent(204) })
	for _, tc := range []struct {
		path string
		code int
	}{{"/panic", 500}, {"/ok", 204}} {
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest("GET", tc.path, nil))
		if r.Code != tc.code {
			t.Fatalf("%s: %d", tc.path, r.Code)
		}
	}
}
