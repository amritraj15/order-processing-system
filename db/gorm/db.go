package gorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"gorm.io/driver/postgres"
	orm "gorm.io/gorm"
	"gorm.io/gorm/logger"

	"order_management/db/migrations"
	"order_management/domain/shared"
)

func Open(ctx context.Context, dsn string) (*orm.DB, error) {
	db, err := orm.Open(postgres.Open(dsn), &orm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sql, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sql.SetMaxOpenConns(20)
	sql.SetMaxIdleConns(5)
	sql.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sql.PingContext(ctx); err != nil {
		_ = sql.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
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
	if errors.As(err, &state) && (state.SQLState() == "55P03" || state.SQLState() == "57014") {
		// Lock timeout or statement cancellation. Preserve the underlying cause
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
