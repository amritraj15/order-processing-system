package middleware

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v5"
	"order_management/domain/shared"
)

// RequestLimits bounds active business requests without a waiting queue. Install
// outside route authentication. Context deadlines are cooperative; bcrypt slots
// and HTTP socket deadlines remain separate safeguards.
type RequestLimits struct {
	slots    chan struct{}
	timeout  time.Duration
	draining atomic.Bool
}

func NewRequestLimits(maxInFlight int, timeout time.Duration) *RequestLimits {
	if maxInFlight < 1 || timeout <= 0 {
		panic("invalid request limits")
	}
	return &RequestLimits{slots: make(chan struct{}, maxInFlight), timeout: timeout}
}
func (l *RequestLimits) Drain()         { l.draining.Store(true) }
func (l *RequestLimits) Draining() bool { return l.draining.Load() }

func (l *RequestLimits) Middleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		// Only the actual probe routes bypass admission; arbitrary methods/paths do not.
		if c.Request().Method == "GET" {
			switch c.Path() {
			case "/health", "/api/v1/health", "/api/v1/ready":
				return next(c)
			}
		}
		if l.Draining() {
			return shared.ErrUnavailable
		}
		select {
		case l.slots <- struct{}{}:
			defer func() { <-l.slots }()
		default:
			return shared.ErrUnavailable
		}
		if l.Draining() {
			return shared.ErrUnavailable
		}
		ctx, cancel := context.WithTimeout(c.Request().Context(), l.timeout)
		defer cancel()
		c.SetRequest(c.Request().WithContext(ctx))
		err := next(c)
		// Drivers may report SQLSTATE 57014 for a canceled caller. Preserve the
		// context cause before our deferred cancellation runs.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
}
