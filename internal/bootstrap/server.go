package bootstrap

import (
	"context"
	"errors"
	"log/slog"
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
	db, err := database.OpenWithPool(ctx, cfg.DatabaseURL, poolConfig(cfg, cfg.DBAPIMaxOpen))
	if err != nil {
		return err
	}
	sql, err := db.DB()
	if err != nil {
		return err
	}
	defer sql.Close()
	workerDB, err := database.OpenWithPool(ctx, cfg.DatabaseURL, poolConfig(cfg, cfg.DBWorkerMaxOpen))
	if err != nil {
		return err
	}
	workerSQL, err := workerDB.DB()
	if err != nil {
		return err
	}
	defer workerSQL.Close()
	readyDB, err := database.OpenWithPool(ctx, cfg.DatabaseURL, poolConfig(cfg, cfg.DBReadinessMaxOpen))
	if err != nil {
		return err
	}
	readySQL, err := readyDB.DB()
	if err != nil {
		return err
	}
	defer readySQL.Close()
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
	observation := processing.NewStatus(time.Now(), cfg.ProcessingInterval)
	quoteRepo := &database.QuoteRepository{DB: workerDB}
	workerDenylist := &database.TokenDenylist{DB: workerDB}
	worker := &processing.Worker{Processor: &database.OrderRepository{DB: workerDB}, Interval: cfg.ProcessingInterval, BatchSize: cfg.BatchSize, Logger: logger, Clock: clock.Real{}, Status: observation, Purge: func(ctx context.Context) error {
		tokenErr := workerDenylist.Purge(ctx)
		_, quoteErr := quoteRepo.DeleteExpiredBatch(ctx, time.Now().Add(-24*time.Hour), 500)
		return errors.Join(tokenErr, quoteErr)
	}}
	limits := middleware.NewRequestLimits(cfg.MaxInFlight, cfg.RequestTimeout)
	e := rest.NewServer(logger)
	e.Use(limits.Middleware)
	routes.Mount(e, routes.MountConfig{Auth: auth,
		Orders:          &v1.OrderHandler{Service: &order.Service{Repo: orders, UOW: &database.UnitOfWork{DB: db}, Currency: settings.BaseCurrency}},
		Products:        &v1.ProductHandler{Service: &product.Service{Repo: products}, Currency: settings.BaseCurrency},
		Quotes:          &v1.QuoteHandler{Service: &quote.Service{UOW: &database.UnitOfWork{DB: db}, Region: cfg.StoreRegion, TTL: cfg.QuoteTTL}},
		WorkerStatus:    observation,
		Draining:        limits.Draining,
		LoginLimiter:    middleware.NewAuthLimiter(cfg.LoginLimit, cfg.AuthMaxKeys, cfg.AuthWindow, clock.Real{}),
		RegisterLimiter: middleware.NewAuthLimiter(cfg.RegisterLimit, cfg.AuthMaxKeys, cfg.AuthWindow, clock.Real{}),
		AuthSlots:       make(chan struct{}, cfg.AuthMaxInFlight),
		Readiness: func(ctx context.Context) error {
			var version int
			var dirty bool
			if err := readySQL.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
				return err
			}
			if version != 5 || dirty {
				return errors.New("schema unavailable")
			}
			var base string
			return readySQL.QueryRowContext(ctx, "SELECT base_currency FROM catalog_settings WHERE singleton_id = 1").Scan(&base)
		},
	})
	docs.Mount(e)
	server := &http.Server{Addr: cfg.Address, Handler: e, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: cfg.RequestTimeout + 5*time.Second, IdleTimeout: 60 * time.Second}
	return serve(ctx, server, limits, worker.Run, cfg.DrainDelay, cfg.ShutdownTimeout, logger)
}

func poolConfig(cfg configs.Config, maxOpen int) database.PoolConfig {
	return database.PoolConfig{MaxOpen: maxOpen, MaxIdle: min(5, maxOpen),
		ConnectTimeout: cfg.DBConnectTimeout, StatementTimeout: cfg.DBStatementTimeout,
		LockTimeout: cfg.DBLockTimeout, IdleTransactionTimeout: cfg.DBIdleTransactionTimeout,
		ConnMaxIdleTime: cfg.DBConnMaxIdleTime}
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
