//go:build integration

package gorm_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	orm "gorm.io/gorm"
	database "order_management/db/gorm"
	"order_management/internal/testutil"
)

func runtimePool(t *testing.T, db *orm.DB, limits database.PoolConfig) *orm.DB {
	t.Helper()
	var schema string
	if err := db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	result, err := database.OpenWithPool(context.Background(), u.String(), limits)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := result.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql.Close() })
	return result
}

func TestPoolSaturationLeavesWorkerAndReadinessCapacity(t *testing.T) {
	base := testutil.PostgreSQL(t)
	limits := database.DefaultPoolConfig()
	limits.MaxOpen = 1
	limits.MaxIdle = 1
	api := runtimePool(t, base, limits)
	worker := runtimePool(t, base, limits)
	ready := runtimePool(t, base, limits)
	apiSQL, _ := api.DB()
	workerSQL, _ := worker.DB()
	readySQL, _ := ready.DB()
	held, err := apiSQL.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := apiSQL.ExecContext(ctx, "SELECT 1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool wait did not end with deadline: %v", err)
	}
	if apiSQL.Stats().WaitCount == 0 {
		t.Fatal("test did not saturate pool")
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), time.Second)
	defer probeCancel()
	if err := readySQL.PingContext(probeCtx); err != nil {
		t.Fatal("readiness starved", err)
	}
	if _, err := (&database.OrderRepository{DB: worker}).ProcessBatch(probeCtx, time.Now(), 500); err != nil {
		t.Fatal("worker starved", err)
	}
	if workerSQL.Stats().MaxOpenConnections != 1 || readySQL.Stats().MaxOpenConnections != 1 {
		t.Fatal("pool limit missing")
	}
	held.Close()
	if err := apiSQL.PingContext(probeCtx); err != nil {
		t.Fatal("pool failed to recover", err)
	}
}

func TestRuntimeSQLTimeoutsAndConnectionReplacement(t *testing.T) {
	base := testutil.PostgreSQL(t)
	limits := database.DefaultPoolConfig()
	limits.MaxOpen = 1
	limits.MaxIdle = 1
	limits.StatementTimeout = 200 * time.Millisecond
	limits.LockTimeout = 40 * time.Millisecond
	db := runtimePool(t, base, limits)
	sql, _ := db.DB()
	checkSettings := func() {
		t.Helper()
		var statement, lock, idle string
		err := sql.QueryRow("SELECT current_setting('statement_timeout'), current_setting('lock_timeout'), current_setting('idle_in_transaction_session_timeout')").Scan(&statement, &lock, &idle)
		if err != nil || statement != "200ms" || lock != "40ms" || idle != "10s" {
			t.Fatalf("session limits %s %s %s: %v", statement, lock, idle, err)
		}
	}
	checkSettings()
	// Close idle connections, then prove startup settings apply to the replacement.
	sql.SetMaxIdleConns(0)
	checkSettings()
	sql.SetMaxIdleConns(1)
	_, err := sql.Exec("SELECT pg_sleep(2)")
	var state interface{ SQLState() string }
	if !errors.As(err, &state) || state.SQLState() != "57014" {
		t.Fatalf("statement timeout: %v", err)
	}
	_, p := fixtures(t, base)
	tx := base.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("SELECT id FROM products WHERE id = ? FOR UPDATE", p.ID).Error; err != nil {
		t.Fatal(err)
	}
	_, err = sql.Exec("UPDATE products SET name = 'locked' WHERE id = $1", p.ID)
	if !errors.As(err, &state) || state.SQLState() != "55P03" {
		t.Fatalf("lock timeout: %v", err)
	}
	tx.Rollback()
	if err := sql.Ping(); err != nil {
		t.Fatal("connection failed to recover", err)
	}
}
