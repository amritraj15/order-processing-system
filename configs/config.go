package configs

import (
	"errors"
	"fmt"
	"order_management/domain/money"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL, Address, JWTSecret, JWTIssuer, Currency    string
	StoreRegion                                             string
	QuoteTTL, AuthWindow                                    time.Duration
	LoginLimit, RegisterLimit, AuthMaxKeys, AuthMaxInFlight int
	TokenTTL, ProcessingInterval                            time.Duration
	BatchSize                                               int
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
	if err != nil || c.ProcessingInterval <= 0 {
		return c, errors.New("PROCESSING_INTERVAL must be a positive duration")
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
		{"AUTH_LOGIN_LIMIT", "10", 10000, &c.LoginLimit}, {"AUTH_REGISTER_LIMIT", "5", 10000, &c.RegisterLimit}, {"AUTH_RATE_MAX_KEYS", "10000", 100000, &c.AuthMaxKeys}, {"AUTH_MAX_IN_FLIGHT", "4", 64, &c.AuthMaxInFlight},
	} {
		v, e := strconv.Atoi(value(setting.key, setting.def))
		if e != nil || v < 1 || v > setting.max {
			return c, fmt.Errorf("%s must be 1–%d", setting.key, setting.max)
		}
		*setting.dest = v
	}
	return c, nil
}
