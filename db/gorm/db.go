package gorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	orm "gorm.io/gorm"
	"gorm.io/gorm/logger"

	"order_management/db/migrations"
	"order_management/domain/shared"
)

// PoolConfig sets limits on every connection, including replacement connections.
// Migration connections intentionally use their separate migration configuration.
type PoolConfig struct {
	MaxOpen, MaxIdle                                                                       int
	ConnectTimeout, StatementTimeout, LockTimeout, IdleTransactionTimeout, ConnMaxIdleTime time.Duration
}

func DefaultPoolConfig() PoolConfig {
	return PoolConfig{MaxOpen: 20, MaxIdle: 5, ConnectTimeout: 5 * time.Second,
		StatementTimeout: 5 * time.Second, LockTimeout: 2 * time.Second,
		IdleTransactionTimeout: 10 * time.Second, ConnMaxIdleTime: 5 * time.Minute}
}

func Open(ctx context.Context, dsn string) (*orm.DB, error) {
	return OpenWithPool(ctx, dsn, DefaultPoolConfig())
}

func OpenWithPool(ctx context.Context, dsn string, limits PoolConfig) (*orm.DB, error) {
	config, err := connectionConfig(dsn, limits)
	if err != nil {
		return nil, err
	}
	pool := stdlib.OpenDB(*config)
	pool.SetMaxOpenConns(limits.MaxOpen)
	pool.SetMaxIdleConns(limits.MaxIdle)
	pool.SetConnMaxLifetime(30 * time.Minute)
	pool.SetConnMaxIdleTime(limits.ConnMaxIdleTime)
	connectCtx, cancel := context.WithTimeout(ctx, limits.ConnectTimeout)
	defer cancel()
	if err := pool.PingContext(connectCtx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	db, err := orm.Open(postgres.New(postgres.Config{Conn: pool}), &orm.Config{
		TranslateError: true, DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

func connectionConfig(dsn string, limits PoolConfig) (*pgx.ConnConfig, error) {
	if limits.MaxOpen < 1 || limits.MaxIdle < 0 || limits.MaxIdle > limits.MaxOpen ||
		limits.ConnectTimeout <= 0 || limits.StatementTimeout < time.Millisecond ||
		limits.LockTimeout < time.Millisecond || limits.LockTimeout > limits.StatementTimeout ||
		limits.IdleTransactionTimeout < time.Millisecond || limits.ConnMaxIdleTime <= 0 {
		return nil, errors.New("invalid database pool limits")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}
	config.ConnectTimeout = limits.ConnectTimeout
	for name, duration := range map[string]time.Duration{
		"statement_timeout": limits.StatementTimeout, "lock_timeout": limits.LockTimeout,
		"idle_in_transaction_session_timeout": limits.IdleTransactionTimeout,
	} {
		config.RuntimeParams[name] = strconv.FormatInt(duration.Milliseconds(), 10)
	}
	return config, nil
}

func Migrate(ctx context.Context, dsn string, down bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", source, dsn)
	if err != nil {
		_ = source.Close()
		return fmt.Errorf("migrate database: %w", err)
	}
	defer m.Close()
	if down {
		err = m.Steps(-1)
	} else {
		err = m.Up()
	}
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}
func mapError(err error) error {
	var state interface{ SQLState() string }
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("%w: %w", shared.ErrUnavailable, err)
	}
	if errors.As(err, &state) && transientSQLState(state.SQLState()) {
		// Preserve transient failures and their underlying cause
		// for diagnostics while exposing a safe retryable application error.
		return fmt.Errorf("%w: %w", shared.ErrUnavailable, err)
	}
	if errors.Is(err, orm.ErrRecordNotFound) {
		return shared.ErrNotFound
	}
	if errors.Is(err, orm.ErrDuplicatedKey) {
		return fmt.Errorf("%w: duplicate email or SKU", shared.ErrConflict)
	}
	return err
}

// Explicit allowlist: data errors and disk/resource exhaustion are not blindly retried.
func transientSQLState(code string) bool {
	switch code {
	case "08000", "08001", "08003", "08006", "08007",
		"40001", "40P01", "53300", "55P03", "57014", "57P01", "57P02", "57P03", "57P05":
		return true
	}
	return false
}
