package configs

import "testing"

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
