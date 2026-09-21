package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/leungll/Emberling/backend/migrations"
)

// Migrate applies every pending migration. It runs before readiness: a Backend that
// cannot migrate must not accept new Runs.
//
// goose needs a database/sql handle, so this opens one over the same pgx pool
// configuration for the duration of the migration only. Runtime queries continue to use
// pgx directly.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store/postgres migrate: set dialect: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("store/postgres migrate: apply: %w", err)
	}
	return nil
}
