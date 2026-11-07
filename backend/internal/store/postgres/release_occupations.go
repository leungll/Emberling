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
		   SET status = 'RELEASED', released_at = $2, release_reason = $3, next_query_at = NULL
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

// ScheduleQuery writes the first post-task query time onto an ACTIVE occupation.
func (r *releaseOccupationRepository) ScheduleQuery(ctx context.Context, id string, at time.Time) error {
	const update = `
		UPDATE release_occupations
		   SET next_query_at = $2
		 WHERE id = $1 AND status = 'ACTIVE'`
	affected, err := affectedRows(ctx, r.conn, "release_occupations.ScheduleQuery", update, id, at)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres release_occupations.ScheduleQuery: occupation=%s is not ACTIVE: %w", id, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("release_occupations.ScheduleQuery", id, affected)
	}
	return nil
}

// ClaimQuery takes one due post-task query. Under READ COMMITTED a second claimer that
// waited on the row lock re-evaluates the predicate against the moved next_query_at and
// affects no row, so exactly one scanner queries production per due time.
func (r *releaseOccupationRepository) ClaimQuery(ctx context.Context, id string, now, next time.Time) (bool, error) {
	const update = `
		UPDATE release_occupations
		   SET next_query_at = $3, query_count = query_count + 1
		 WHERE id = $1 AND status = 'ACTIVE' AND next_query_at IS NOT NULL AND next_query_at <= $2`
	affected, err := affectedRows(ctx, r.conn, "release_occupations.ClaimQuery", update, id, now, next)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("release_occupations.ClaimQuery", id, affected)
	}
	return affected == 1, nil
}

// ReleaseAfterRun settles an occupation whose Run already ended, from production's definite
// answer, touching nothing but the occupation row.
func (r *releaseOccupationRepository) ReleaseAfterRun(ctx context.Context, id string, reason domain.ReleaseReason, now time.Time, deployedUncheckedVersion *string) (bool, error) {
	if !reason.IsValid() {
		return false, fmt.Errorf("store/postgres release_occupations.ReleaseAfterRun: occupation=%s: unknown release reason %q", id, reason)
	}
	const update = `
		UPDATE release_occupations
		   SET status = 'RELEASED', released_at = $2, release_reason = $3, next_query_at = NULL,
		       deployed_unchecked_version = $4
		 WHERE id = $1 AND status = 'ACTIVE'`
	affected, err := affectedRows(ctx, r.conn, "release_occupations.ReleaseAfterRun", update, id, now, string(reason), nullableString(deployedUncheckedVersion))
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("release_occupations.ReleaseAfterRun", id, affected)
	}
	return affected == 1, nil
}

// ListDueQueries discovers ACTIVE occupations of ended Runs whose post-task query is due.
func (r *releaseOccupationRepository) ListDueQueries(ctx context.Context, now time.Time, limit int) ([]domain.ReleaseOccupation, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres release_occupations.ListDueQueries: limit must be positive, got %d", limit)
	}
	const query = `
		SELECT ` + qualifiedReleaseOccupationColumns + `
		  FROM release_occupations o
		  JOIN runs r ON r.id = o.run_id
		 WHERE o.status = 'ACTIVE'
		   AND o.next_query_at IS NOT NULL AND o.next_query_at <= $1
		   AND r.status IN ('COMPLETED', 'FAILED')
		 ORDER BY o.next_query_at ASC, o.id ASC
		 LIMIT $2`
	rows, err := r.conn.Query(ctx, query, now, limit)
	if err != nil {
		return nil, mapError("release_occupations.ListDueQueries", err)
	}
	occupations, err := scanReleaseOccupations(rows)
	if err != nil {
		return nil, mapError("release_occupations.ListDueQueries", err)
	}
	return occupations, nil
}

const qualifiedReleaseOccupationColumns = `o.id, o.target_key, o.run_id, o.action_id, o.operation_key, o.status, o.acquired_at,
	o.released_at, o.release_reason, o.next_query_at, o.query_count, o.deployed_unchecked_version`

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
