package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"fmt"
	"order_management/api/rest"
	"order_management/api/rest/middleware"
	"order_management/api/rest/routes"
	"order_management/api/rest/v1"
	"order_management/configs"
	database "order_management/db/gorm"
	"order_management/docs"
	"order_management/domain/money"
	"order_management/domain/pricing"
	"order_management/domain/shared"
	"order_management/internal/clock"
	"order_management/service/auth/jwt"
	"order_management/service/order"
	"order_management/service/processing"
	"order_management/service/product"
	"order_management/service/quote"
)

func RunServer(ctx context.Context, cfg configs.Config, logger *slog.Logger) error {
	db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	sql, err := db.DB()
	if err != nil {
		return err
	}
	defer sql.Close()
	users := &database.UserRepository{DB: db}
	denylist := &database.TokenDenylist{DB: db}
	orders := &database.OrderRepository{DB: db}
	products := &database.ProductRepository{DB: db}
	auth, err := jwt.NewClient(jwt.Config{Secret: cfg.JWTSecret, Issuer: cfg.JWTIssuer, TokenTTL: cfg.TokenTTL}, users, denylist)
	if err != nil {
		return err
	}
	settings, err := Catalog(ctx, &database.PricingRepository{DB: db}, cfg)
	if err != nil {
		return err
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	observation := processing.NewStatus(time.Now(), cfg.ProcessingInterval)
	quoteRepo := &database.QuoteRepository{DB: db}
	worker := &processing.Worker{Processor: orders, Interval: cfg.ProcessingInterval, BatchSize: cfg.BatchSize, Logger: logger, Clock: clock.Real{}, Status: observation, Purge: func(ctx context.Context) error {
		tokenErr := denylist.Purge(ctx)
		_, quoteErr := quoteRepo.DeleteExpiredBatch(ctx, time.Now().Add(-24*time.Hour), 500)
		return errors.Join(tokenErr, quoteErr)
	}}
	e := rest.NewServer(logger)
	routes.Mount(e, routes.MountConfig{Auth: auth,
		Orders:          &v1.OrderHandler{Service: &order.Service{Repo: orders, UOW: &database.UnitOfWork{DB: db}, Currency: settings.BaseCurrency}},
		Products:        &v1.ProductHandler{Service: &product.Service{Repo: products}, Currency: settings.BaseCurrency},
		Quotes:          &v1.QuoteHandler{Service: &quote.Service{UOW: &database.UnitOfWork{DB: db}, Region: cfg.StoreRegion, TTL: cfg.QuoteTTL}},
		WorkerStatus:    observation,
		LoginLimiter:    middleware.NewAuthLimiter(cfg.LoginLimit, cfg.AuthMaxKeys, cfg.AuthWindow, clock.Real{}),
		RegisterLimiter: middleware.NewAuthLimiter(cfg.RegisterLimit, cfg.AuthMaxKeys, cfg.AuthWindow, clock.Real{}),
		AuthSlots:       make(chan struct{}, cfg.AuthMaxInFlight),
		Readiness: func(ctx context.Context) error {
			var version int
			var dirty bool
			if err := sql.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
				return err
			}
			if version != 3 || dirty {
				return errors.New("schema unavailable")
			}
			var base string
			return sql.QueryRowContext(ctx, "SELECT base_currency FROM catalog_settings WHERE singleton_id = 1").Scan(&base)
		},
	})
	docs.Mount(e)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); worker.Run(workerCtx) }()
	server := &http.Server{Addr: cfg.Address, Handler: e, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return workerCtx },
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	logger.InfoContext(ctx, "server started", "address", cfg.Address, "processing_interval", cfg.ProcessingInterval, "batch_size", cfg.BatchSize)
	select {
	case <-ctx.Done():
	case err = <-serverErr:
	}
	cancelWorker()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		logger.ErrorContext(ctx, "server shutdown failed", "error_kind", "shutdown")
	}
	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
		return shutdownCtx.Err()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Catalog resolves existing settings without interpreting a changed store region as a base-currency migration.
func Catalog(ctx context.Context, repo pricing.Repository, cfg configs.Config) (*pricing.Settings, error) {
	setting, err := repo.Settings(ctx)
	if err == nil {
		if cfg.Currency != "" && cfg.Currency != setting.BaseCurrency {
			return nil, fmt.Errorf("%w: configured currency differs from catalog", shared.ErrConflict)
		}
		return setting, nil
	}
	if !errors.Is(err, shared.ErrNotFound) {
		return nil, err
	}
	currency := cfg.Currency
	if currency == "" {
		currency, err = money.Currency(cfg.StoreRegion)
		if err != nil {
			return nil, err
		}
	}
	return repo.Initialize(ctx, pricing.InitializeInput{Currency: currency})
}
