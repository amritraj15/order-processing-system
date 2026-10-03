package routes

import (
	"context"
	"time"

	"github.com/labstack/echo/v5"

	"order_management/api/rest/middleware"
	"order_management/api/rest/v1"
	"order_management/domain/user"
	"order_management/internal/clock"
	"order_management/ports"
	"order_management/service/processing"
)

type MountConfig struct {
	Quotes                        *v1.QuoteHandler
	WorkerStatus                  *processing.Status
	LoginLimiter, RegisterLimiter *middleware.AuthLimiter
	AuthSlots                     chan struct{}
	Auth                          ports.Authenticator
	Orders                        *v1.OrderHandler
	Products                      *v1.ProductHandler
	Readiness                     func(context.Context) error
}

func Mount(e *echo.Echo, cfg MountConfig) {
	health := func(c *echo.Context) error { return c.JSON(200, map[string]string{"status": "ok"}) }
	e.GET("/health", health)
	e.GET("/api/v1/health", health)
	e.GET("/api/v1/ready", func(c *echo.Context) error {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()
		databaseState := "ok"
		if cfg.Readiness == nil || cfg.Readiness(ctx) != nil {
			databaseState = "unavailable"
		}
		snapshot := processing.Snapshot{State: "unavailable"}
		if cfg.WorkerStatus != nil {
			snapshot = cfg.WorkerStatus.Snapshot(time.Now())
		}
		status, code := "ok", 200
		if databaseState != "ok" || (snapshot.State != "ok" && snapshot.State != "starting") {
			status = "unavailable"
			code = 503
		}
		return c.JSON(code, map[string]any{"status": status, "checks": map[string]string{"database": databaseState, "worker": snapshot.State}, "worker": map[string]any{"last_attempt_at": optionalTime(snapshot.LastAttempt), "last_success_at": optionalTime(snapshot.LastSuccess), "last_failure_at": optionalTime(snapshot.LastFailure), "running": snapshot.Running}})
	})
	auth := &v1.AuthHandler{Auth: cfg.Auth}
	g := e.Group("/api/v1/auth")
	if cfg.AuthSlots == nil {
		cfg.AuthSlots = make(chan struct{}, 4)
	}
	if cfg.LoginLimiter == nil {
		cfg.LoginLimiter = middleware.NewAuthLimiter(10, 10000, time.Minute, clock.Real{})
	}
	if cfg.RegisterLimiter == nil {
		cfg.RegisterLimiter = middleware.NewAuthLimiter(5, 10000, time.Minute, clock.Real{})
	}
	g.POST("/register", auth.Register, cfg.RegisterLimiter.Middleware(cfg.AuthSlots))
	g.POST("/login", auth.Login, cfg.LoginLimiter.Middleware(cfg.AuthSlots))
	g.GET("/session", auth.Session)
	g.POST("/logout", auth.Logout)
	e.GET("/api/v1/me", auth.Session)
	if cfg.Quotes != nil {
		e.POST("/api/v1/order-quotes", cfg.Quotes.Create, middleware.RequireAuth(cfg.Auth), middleware.RequireRole(user.Customer))
	}
	orders := e.Group("/api/v1/orders", middleware.RequireAuth(cfg.Auth))
	orders.POST("", cfg.Orders.Create, middleware.RequireRole(user.Customer))
	orders.GET("", cfg.Orders.List)
	orders.GET("/:id", cfg.Orders.Get)
	orders.PATCH("/:id/status", cfg.Orders.Status, middleware.RequireRole(user.Admin))
	orders.POST("/:id/cancel", cfg.Orders.Cancel, middleware.RequireRole(user.Customer))
	products := e.Group("/api/v1/products", middleware.RequireAuth(cfg.Auth))
	products.POST("", cfg.Products.Create, middleware.RequireRole(user.Admin))
	products.GET("", cfg.Products.List)
	products.GET("/:id", cfg.Products.Get)
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
