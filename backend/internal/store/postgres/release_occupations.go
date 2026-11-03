package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type releaseOccupationRepository struct {
	conn pgx.Tx
}

const releaseOccupationColumns = `id, target_key, run_id, action_id, operation_key, status, acquired_at,
	released_at, release_reason, next_query_at, query_count, deployed_unchecked_version`

// Acquire inserts an ACTIVE occupation unless the target is already held. The partial
// unique index on ACTIVE target keys is the only arbiter: ON CONFLICT DO NOTHING makes a
// racing approval of the same target affect zero rows, which is reported as false.
func (r *releaseOccupationRepository) Acquire(ctx context.Context, occupation domain.ReleaseOccupation) (bool, error) {
	if occupation.Status != domain.ReleaseOccupationActive {
		return false, fmt.Errorf("store/postgres release_occupations.Acquire: occupation=%s: status must be ACTIVE, got %q", occupation.ID, occupation.Status)
	}
	const insert = `
		INSERT INTO release_occupations (id, target_key, run_id, action_id, operation_key, status, acquired_at)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE', $6)
		ON CONFLICT (target_key) WHERE status = 'ACTIVE' DO NOTHING`
	affected, err := affectedRows(ctx, r.conn, "release_occupations.Acquire", insert,
		occupation.ID, occupation.TargetKey, occupation.RunID, occupation.ActionID, occupation.OperationKey, occupation.AcquiredAt)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("release_occupations.Acquire", occupation.ID, affected)
	}
	return affected == 1, nil
}

func (r *releaseOccupationRepository) Get(ctx context.Context, id string) (domain.ReleaseOccupation, error) {
	const query = `SELECT ` + releaseOccupationColumns + ` FROM release_occupations WHERE id = $1`
	return r.queryOne(ctx, "release_occupations.Get", query, id)
}

func (r *releaseOccupationRepository) GetActiveByTarget(ctx context.Context, targetKey string) (domain.ReleaseOccupation, error) {
	const query = `SELECT ` + releaseOccupationColumns + ` FROM release_occupations WHERE target_key = $1 AND status = 'ACTIVE'`
	return r.queryOne(ctx, "release_occupations.GetActiveByTarget", query, targetKey)
}

// ListByActionID returns the occupations of one Action, oldest first. An Action acquires
// at most one occupation, but the projection reads a list so that it never has to guess.
func (r *releaseOccupationRepository) ListByActionID(ctx context.Context, actionID string) ([]domain.ReleaseOccupation, error) {
	const query = `SELECT ` + releaseOccupationColumns + `
		  FROM release_occupations WHERE action_id = $1 ORDER BY acquired_at ASC, id ASC`
	rows, err := r.conn.Query(ctx, query, actionID)
	if err != nil {
		return nil, mapError("release_occupations.ListByActionID", err, actionID)
	}
	occupations, err := scanReleaseOccupations(rows)
	if err != nil {
		return nil, mapError("release_occupations.ListByActionID", err, actionID)
	}
	return occupations, nil
}

// SetOperationKey points an ACTIVE occupation at the operation that now holds the target,
// when a voided release is re-released under a new key.
func (r *releaseOccupationRepository) SetOperationKey(ctx context.Context, id, operationKey string) error {
	const update = `
		UPDATE release_occupations
		   SET operation_key = $2
		 WHERE id = $1 AND status = 'ACTIVE'`
	affected, err := affectedRows(ctx, r.conn, "release_occupations.SetOperationKey", update, id, operationKey)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres release_occupations.SetOperationKey: occupation=%s is not ACTIVE: %w", id, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("release_occupations.SetOperationKey", id, affected)
	}
	return nil
}

// Release conditionally moves an ACTIVE occupation to RELEASED with its reason. Requiring
// ACTIVE makes a second settlement of the same release lose instead of relabelling the
// committed reason.
func (r *releaseOccupationRepository) Release(ctx context.Context, id string, reason domain.ReleaseReason, now time.Time) error {
	if !reason.IsValid() {
		return fmt.Errorf("store/postgres release_occupations.Release: occupation=%s: unknown release reason %q", id, reason)
	}
	const update = `
		UPDATE release_occupations
		   SET status = 'RELEASED', released_at = $2, release_reason = $3
		 WHERE id = $1 AND status = 'ACTIVE'`
	affected, err := affectedRows(ctx, r.conn, "release_occupations.Release", update, id, now, string(reason))
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres release_occupations.Release: occupation=%s ACTIVE -> RELEASED: %w", id, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("release_occupations.Release", id, affected)
	}
	return nil
}

func (r *releaseOccupationRepository) queryOne(ctx context.Context, op, query string, arg any) (domain.ReleaseOccupation, error) {
	rows, err := r.conn.Query(ctx, query, arg)
	if err != nil {
		return domain.ReleaseOccupation{}, mapError(op, err, arg)
	}
	occupations, err := scanReleaseOccupations(rows)
	if err != nil {
		return domain.ReleaseOccupation{}, mapError(op, err, arg)
	}
	if len(occupations) == 0 {
		return domain.ReleaseOccupation{}, mapError(op, pgx.ErrNoRows, arg)
	}
	return occupations[0], nil
}

func scanReleaseOccupations(rows pgx.Rows) ([]domain.ReleaseOccupation, error) {
	defer rows.Close()

	out := []domain.ReleaseOccupation{}
	for rows.Next() {
		var (
			occupation domain.ReleaseOccupation
			status     string
			reason     *string
		)
		if err := rows.Scan(
			&occupation.ID, &occupation.TargetKey, &occupation.RunID, &occupation.ActionID, &occupation.OperationKey,
			&status, &occupation.AcquiredAt, &occupation.ReleasedAt, &reason,
			&occupation.NextQueryAt, &occupation.QueryCount, &occupation.DeployedUncheckedVersion,
		); err != nil {
			return nil, err
		}
		occupation.Status = domain.ReleaseOccupationStatus(status)
		if reason != nil {
			value := domain.ReleaseReason(*reason)
			occupation.ReleaseReason = &value
		}
		out = append(out, occupation)
	}
	return out, rows.Err()
}

var _ store.ReleaseOccupationRepository = (*releaseOccupationRepository)(nil)
