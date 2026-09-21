package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// PostgreSQL SQLSTATE codes the Store translates into domain errors.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
)

// mapError translates driver failures into domain errors and wraps them with the
// operation and stable identifiers. Values, payloads and credentials are never included.
func mapError(op string, err error, identifiers ...any) error {
	if err == nil {
		return nil
	}

	context := fmt.Sprintf("store/postgres %s", op)
	if len(identifiers) > 0 {
		context = fmt.Sprintf("%s %v", context, identifiers)
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", context, domain.ErrNotFound)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlStateUniqueViolation, sqlStateForeignKeyViolation, sqlStateCheckViolation:
			return fmt.Errorf("%s: constraint %s: %w", context, pgErr.ConstraintName, domain.ErrConflict)
		}
	}

	return fmt.Errorf("%s: %w", context, err)
}

// isUniqueViolation reports whether err is a PostgreSQL unique constraint violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolation
}
