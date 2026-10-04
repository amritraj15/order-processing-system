package configs

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndValidation(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "HTTP_ADDRESS", "JWT_SECRET", "JWT_ISSUER", "CURRENCY", "JWT_TOKEN_TTL", "PROCESSING_INTERVAL", "PROCESSING_BATCH_SIZE", "STORE_REGION", "QUOTE_TTL", "AUTH_LOGIN_LIMIT", "AUTH_REGISTER_LIMIT", "AUTH_RATE_WINDOW", "AUTH_RATE_MAX_KEYS", "AUTH_MAX_IN_FLIGHT"} {
		t.Setenv(key, "")
	}
	if _, err := Load(); err == nil {
		t.Fatal("accepted missing database URL")
	}
	t.Setenv("DATABASE_URL", "postgres://example/orders")
	c, err := Load()
	if err != nil || c.BatchSize != 500 || c.Currency != "" || c.StoreRegion != "US" || c.ProcessingInterval.String() != "5m0s" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for _, test := range []struct{ key, value string }{{"CURRENCY", "usd"}, {"JWT_TOKEN_TTL", "0s"}, {"PROCESSING_INTERVAL", "bad"}, {"PROCESSING_BATCH_SIZE", "0"}, {"PROCESSING_BATCH_SIZE", "10001"}} {
		t.Run(test.key+test.value, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("accepted %s=%s", test.key, test.value)
			}
		})
	}
}

func TestHardeningConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example/orders")
	t.Setenv("CURRENCY", "")
	t.Setenv("STORE_REGION", "IN")
	c, err := Load()
	if err != nil || c.Currency != "" || c.StoreRegion != "IN" || c.LoginLimit != 10 || c.RegisterLimit != 5 {
		t.Fatalf("region defaults: %+v %v", c, err)
	}
	for _, tc := range []struct{ key, value string }{{"STORE_REGION", "XX"}, {"QUOTE_TTL", "0s"}, {"QUOTE_TTL", "2h"}, {"AUTH_RATE_WINDOW", "0s"}, {"AUTH_LOGIN_LIMIT", "0"}, {"AUTH_REGISTER_LIMIT", "-1"}, {"AUTH_RATE_MAX_KEYS", "100001"}, {"AUTH_MAX_IN_FLIGHT", "65"}} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("accepted %s", tc.key)
			}
		})
	}
}

func TestRuntimeLimits(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example/orders")
	for _, key := range []string{"HTTP_MAX_IN_FLIGHT", "HTTP_REQUEST_TIMEOUT", "HTTP_SHUTDOWN_TIMEOUT", "HTTP_DRAIN_DELAY", "DB_API_MAX_OPEN", "DB_WORKER_MAX_OPEN", "DB_READINESS_MAX_OPEN", "DB_CONNECT_TIMEOUT", "DB_STATEMENT_TIMEOUT", "DB_LOCK_TIMEOUT", "DB_IDLE_TRANSACTION_TIMEOUT", "DB_CONN_MAX_IDLE_TIME", "PROCESSING_INTERVAL"} {
		t.Setenv(key, "")
	}
	c, err := Load()
	if err != nil || c.MaxInFlight != 16 || c.DBAPIMaxOpen+c.DBWorkerMaxOpen+c.DBReadinessMaxOpen != 20 || c.RequestTimeout != 10*time.Second || c.ShutdownTimeout != 30*time.Second {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for _, tc := range []struct{ key, value string }{
		{"PROCESSING_INTERVAL", "1ns"}, {"HTTP_MAX_IN_FLIGHT", "0"}, {"HTTP_REQUEST_TIMEOUT", "0s"}, {"HTTP_SHUTDOWN_TIMEOUT", "1s"}, {"HTTP_DRAIN_DELAY", "-1s"}, {"DB_API_MAX_OPEN", "0"}, {"DB_WORKER_MAX_OPEN", "0"}, {"DB_READINESS_MAX_OPEN", "0"}, {"DB_CONNECT_TIMEOUT", "0s"}, {"DB_STATEMENT_TIMEOUT", "1s"}, {"DB_LOCK_TIMEOUT", "6s"}, {"DB_IDLE_TRANSACTION_TIMEOUT", "0s"}, {"DB_CONN_MAX_IDLE_TIME", "0s"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("accepted %s=%s", tc.key, tc.value)
			}
		})
	}
	t.Setenv("PROCESSING_INTERVAL", "5s")
	if _, err := Load(); err != nil {
		t.Fatal("fast smoke interval rejected", err)
	}
}
