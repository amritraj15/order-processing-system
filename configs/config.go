package configs

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"order_management/domain/money"
)

type Config struct {
	DatabaseURL, Address, JWTSecret, JWTIssuer, Currency    string
	StoreRegion                                             string
	QuoteTTL, AuthWindow                                    time.Duration
	LoginLimit, RegisterLimit, AuthMaxKeys, AuthMaxInFlight int
	TokenTTL, ProcessingInterval                            time.Duration
	BatchSize                                               int

	RequestTimeout, ShutdownTimeout, DrainDelay time.Duration
	MaxInFlight                                 int

	DBAPIMaxOpen, DBWorkerMaxOpen, DBReadinessMaxOpen   int
	DBConnectTimeout, DBStatementTimeout, DBLockTimeout time.Duration
	DBIdleTransactionTimeout, DBConnMaxIdleTime         time.Duration
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), Address: value("HTTP_ADDRESS", ":8080"), JWTSecret: os.Getenv("JWT_SECRET"), JWTIssuer: value("JWT_ISSUER", "order-management"), Currency: os.Getenv("CURRENCY"), StoreRegion: value("STORE_REGION", "US")}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if _, err := money.Currency(c.StoreRegion); err != nil {
		return c, fmt.Errorf("STORE_REGION: %w", err)
	}
	if c.Currency != "" {
		if _, err := money.Digits(c.Currency); err != nil {
			return c, fmt.Errorf("CURRENCY: %w", err)
		}
	}
	var err error
	c.TokenTTL, err = time.ParseDuration(value("JWT_TOKEN_TTL", "24h"))
	if err != nil || c.TokenTTL <= 0 {
		return c, errors.New("JWT_TOKEN_TTL must be a positive duration")
	}
	c.ProcessingInterval, err = time.ParseDuration(value("PROCESSING_INTERVAL", "5m"))
	if err != nil || c.ProcessingInterval < time.Second {
		return c, errors.New("PROCESSING_INTERVAL must be at least 1s")
	}
	c.BatchSize, err = strconv.Atoi(value("PROCESSING_BATCH_SIZE", "500"))
	if err != nil || c.BatchSize < 1 || c.BatchSize > 10000 {
		return c, fmt.Errorf("PROCESSING_BATCH_SIZE must be between 1 and 10000")
	}
	c.QuoteTTL, err = time.ParseDuration(value("QUOTE_TTL", "5m"))
	if err != nil || c.QuoteTTL <= 0 || c.QuoteTTL > time.Hour {
		return c, errors.New("QUOTE_TTL must be positive and at most 1h")
	}
	c.AuthWindow, err = time.ParseDuration(value("AUTH_RATE_WINDOW", "1m"))
	if err != nil || c.AuthWindow < time.Second || c.AuthWindow > time.Hour {
		return c, errors.New("AUTH_RATE_WINDOW must be 1s–1h")
	}
	for _, setting := range []struct {
		key, def string
		max      int
		dest     *int
	}{
		{"HTTP_MAX_IN_FLIGHT", "16", 1000, &c.MaxInFlight},
		{"DB_API_MAX_OPEN", "16", 1000, &c.DBAPIMaxOpen}, {"DB_WORKER_MAX_OPEN", "3", 100, &c.DBWorkerMaxOpen}, {"DB_READINESS_MAX_OPEN", "1", 10, &c.DBReadinessMaxOpen},
		{"AUTH_LOGIN_LIMIT", "10", 10000, &c.LoginLimit}, {"AUTH_REGISTER_LIMIT", "5", 10000, &c.RegisterLimit}, {"AUTH_RATE_MAX_KEYS", "10000", 100000, &c.AuthMaxKeys}, {"AUTH_MAX_IN_FLIGHT", "4", 64, &c.AuthMaxInFlight},
	} {
		v, e := strconv.Atoi(value(setting.key, setting.def))
		if e != nil || v < 1 || v > setting.max {
			return c, fmt.Errorf("%s must be 1–%d", setting.key, setting.max)
		}
		*setting.dest = v
	}
	for _, setting := range []struct {
		key, def string
		min, max time.Duration
		dest     *time.Duration
	}{
		{"HTTP_REQUEST_TIMEOUT", "10s", time.Second, 30 * time.Second, &c.RequestTimeout},
		{"HTTP_SHUTDOWN_TIMEOUT", "30s", time.Second, time.Minute, &c.ShutdownTimeout},
		{"HTTP_DRAIN_DELAY", "0s", 0, 30 * time.Second, &c.DrainDelay},
		{"DB_CONNECT_TIMEOUT", "5s", time.Second, 30 * time.Second, &c.DBConnectTimeout},
		{"DB_STATEMENT_TIMEOUT", "5s", time.Millisecond, 30 * time.Second, &c.DBStatementTimeout},
		{"DB_LOCK_TIMEOUT", "2s", time.Millisecond, 30 * time.Second, &c.DBLockTimeout},
		{"DB_IDLE_TRANSACTION_TIMEOUT", "10s", time.Second, time.Minute, &c.DBIdleTransactionTimeout},
		{"DB_CONN_MAX_IDLE_TIME", "5m", time.Second, time.Hour, &c.DBConnMaxIdleTime},
	} {
		v, e := time.ParseDuration(value(setting.key, setting.def))
		if e != nil || v < setting.min || v > setting.max {
			return c, fmt.Errorf("%s must be between %s and %s", setting.key, setting.min, setting.max)
		}
		*setting.dest = v
	}
	if c.ShutdownTimeout < c.RequestTimeout {
		return c, errors.New("HTTP_SHUTDOWN_TIMEOUT must cover HTTP_REQUEST_TIMEOUT")
	}
	if c.DBLockTimeout > c.DBStatementTimeout {
		return c, errors.New("DB_LOCK_TIMEOUT must not exceed DB_STATEMENT_TIMEOUT")
	}
	return c, nil
}
