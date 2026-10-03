package middleware

import (
	"github.com/labstack/echo/v5"
	"math"
	"net"
	"net/netip"
	"order_management/ports"
	"strconv"
	"sync"
	"time"
)

type bucket struct {
	start time.Time
	count int
}
type AuthLimiter struct {
	mu             sync.Mutex
	buckets        map[string]bucket
	limit, maxKeys int
	window         time.Duration
	clock          ports.Clock
	nextSweep      time.Time
}

func NewAuthLimiter(limit, maxKeys int, window time.Duration, clock ports.Clock) *AuthLimiter {
	return &AuthLimiter{buckets: make(map[string]bucket), limit: limit, maxKeys: maxKeys, window: window, clock: clock}
}
func peer(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return "unknown"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	return ip.Unmap().String()
}
func (l *AuthLimiter) admit(key string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	if !now.Before(l.nextSweep) {
		for k, v := range l.buckets {
			if !now.Before(v.start.Add(l.window)) {
				delete(l.buckets, k)
			}
		}
		l.nextSweep = now.Add(min(time.Second, l.window))
	}
	b, exists := l.buckets[key]
	if exists && !now.Before(b.start.Add(l.window)) {
		delete(l.buckets, key)
		exists = false
	}
	if !exists {
		if len(l.buckets) >= l.maxKeys {
			return false, 1
		}
		b = bucket{start: now}
	}
	retry := max(1, int(math.Ceil(b.start.Add(l.window).Sub(now).Seconds())))
	if b.count >= l.limit {
		return false, retry
	}
	b.count++
	l.buckets[key] = b
	return true, 0
}
func rejectLimit(c *echo.Context, retry int) error {
	c.Response().Header().Set("Retry-After", strconv.Itoa(retry))
	return c.JSON(429, map[string]string{"error": "too many requests"})
}
func (l *AuthLimiter) Middleware(slots chan struct{}) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if ok, retry := l.admit(peer(c.Request().RemoteAddr)); !ok {
				return rejectLimit(c, retry)
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				return next(c)
			default:
				return rejectLimit(c, 1)
			}
		}
	}
}
