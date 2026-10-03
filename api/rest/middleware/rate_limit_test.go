package middleware

import (
	"errors"
	"github.com/labstack/echo/v5"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }
func TestLimiterBoundsAndConcurrency(t *testing.T) {
	clock := &testClock{now: time.Now()}
	l := NewAuthLimiter(10, 2, time.Minute, clock)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.admit("one"); ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 10 {
		t.Fatalf("admitted %d", accepted.Load())
	}
	if ok, _ := l.admit("two"); !ok {
		t.Fatal("second client rejected")
	}
	if ok, _ := l.admit("three"); ok {
		t.Fatal("map capacity exceeded")
	}
	clock.now = clock.now.Add(time.Minute)
	if ok, _ := l.admit("three"); !ok {
		t.Fatal("expired capacity not reclaimed")
	}
	if peer("[::ffff:127.0.0.1]:123") != "127.0.0.1" {
		t.Fatal("IP normalization")
	}
}
func TestLimiterIgnoresForwardedHeadersAndReleasesSlot(t *testing.T) {
	e := echo.New()
	clock := &testClock{now: time.Now()}
	l := NewAuthLimiter(2, 10, time.Minute, clock)
	slots := make(chan struct{}, 1)
	sentinel := errors.New("handler failed")
	handler := l.Middleware(slots)(func(c *echo.Context) error { return sentinel })
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set("X-Forwarded-For", time.Now().String())
		res := httptest.NewRecorder()
		err := handler(e.NewContext(req, res))
		if i < 2 && !errors.Is(err, sentinel) {
			t.Fatal("handler did not run")
		}
		if len(slots) != 0 {
			t.Fatal("slot leaked")
		}
		if i == 2 && (res.Code != 429 || res.Header().Get("Retry-After") == "") {
			t.Fatal("limit bypassed")
		}
	}
	slots <- struct{}{}
	l2 := NewAuthLimiter(10, 10, time.Minute, clock)
	req := httptest.NewRequest("POST", "/login", nil)
	res := httptest.NewRecorder()
	_ = l2.Middleware(slots)(func(*echo.Context) error { t.Fatal("inflight cap bypassed"); return nil })(e.NewContext(req, res))
	if res.Code != 429 {
		t.Fatal("missing inflight response")
	}
}
