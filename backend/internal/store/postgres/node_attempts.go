package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type nodeAttemptRepository struct {
	conn pgx.Tx
}

const nodeAttemptColumns = `id, node_run_id, attempt_no, status, input, result,
	callback_token_hash, started_at, deadline_at, dispatched_at, completed_at, error`

// Create inserts a new Attempt. A retry always creates another row: an old Attempt is
// never overwritten, so UNIQUE (node_run_id, attempt_no) rejects a duplicated retry.
func (r *nodeAttemptRepository) Create(ctx context.Context, attempt domain.NodeAttempt) error {
	errPayload, err := marshalExecutionError(attempt.Error)
	if err != nil {
		return fmt.Errorf("store/postgres node_attempts.Create: attempt=%s: %w", attempt.ID, err)
	}

	const insert = `
		INSERT INTO node_attempts (` + nodeAttemptColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	if _, err := r.conn.Exec(ctx, insert,
		attempt.ID, attempt.NodeRunID, attempt.AttemptNo, string(attempt.Status),
		attempt.Input, nullableJSON(attempt.Result), attempt.CallbackTokenHash,
		attempt.StartedAt, attempt.DeadlineAt, attempt.DispatchedAt, attempt.CompletedAt, errPayload,
	); err != nil {
		return mapError("node_attempts.Create", err, attempt.ID, attempt.NodeRunID, attempt.AttemptNo)
	}
	return nil
}

func (r *nodeAttemptRepository) Get(ctx context.Context, attemptID string) (domain.NodeAttempt, error) {
	const query = `SELECT ` + nodeAttemptColumns + ` FROM node_attempts WHERE id = $1`

	rows, err := r.conn.Query(ctx, query, attemptID)
	if err != nil {
		return domain.NodeAttempt{}, mapError("node_attempts.Get", err, attemptID)
	}
	attempts, err := scanNodeAttempts(rows)
	if err != nil {
		return domain.NodeAttempt{}, mapError("node_attempts.Get", err, attemptID)
	}
	if len(attempts) == 0 {
		return domain.NodeAttempt{}, mapError("node_attempts.Get", pgx.ErrNoRows, attemptID)
	}
	return attempts[0], nil
}

func (r *nodeAttemptRepository) ListByNodeRun(ctx context.Context, nodeRunID string) ([]domain.NodeAttempt, error) {
	const query = `SELECT ` + nodeAttemptColumns + `
		  FROM node_attempts WHERE node_run_id = $1 ORDER BY attempt_no ASC`

	rows, err := r.conn.Query(ctx, query, nodeRunID)
	if err != nil {
		return nil, mapError("node_attempts.ListByNodeRun", err, nodeRunID)
	}
	attempts, err := scanNodeAttempts(rows)
	if err != nil {
		return nil, mapError("node_attempts.ListByNodeRun", err, nodeRunID)
	}
	return attempts, nil
}

// Transition conditionally moves an Attempt between two states. A stale Attempt, a
// duplicate callback and a late timeout all lose here instead of overwriting a result.
func (r *nodeAttemptRepository) Transition(ctx context.Context, attemptID string, from, to domain.NodeAttemptStatus, now time.Time) error {
	if !from.CanTransitionTo(to) {
		return &domain.InvalidStateTransitionError{
			Entity: "NodeAttempt", ID: attemptID, From: string(from), To: string(to),
		}
	}

	// completed_at is written here because it is the Attempt's own terminal timestamp;
	// dispatched_at belongs to the dispatch transaction that also commits the binding.
	const update = `
		UPDATE node_attempts
		   SET status       = $3,
		       completed_at = CASE WHEN $3 IN ('SUCCEEDED', 'FAILED') THEN $4 ELSE completed_at END
		 WHERE id = $1 AND status = $2`
	affected, err := affectedRows(ctx, r.conn, "node_attempts.Transition", update,
		attemptID, string(from), string(to), now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_attempts.Transition: attempt=%s %s -> %s: %w",
			attemptID, from, to, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_attempts.Transition", attemptID, affected)
	}
	return nil
}

// MarkDispatched conditionally moves an Attempt from STARTED to DISPATCHED and stamps
// dispatched_at. The dispatch transaction that commits the external hand-off owns this
// write; a later retry or timeout that also targets a STARTED row loses here instead.
func (r *nodeAttemptRepository) MarkDispatched(ctx context.Context, attemptID string, now time.Time) error {
	const update = `
		UPDATE node_attempts
		   SET status = 'DISPATCHED', dispatched_at = $2
		 WHERE id = $1 AND status = 'STARTED'`

	affected, err := affectedRows(ctx, r.conn, "node_attempts.MarkDispatched", update, attemptID, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_attempts.MarkDispatched: attempt=%s: %w", attemptID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_attempts.MarkDispatched", attemptID, affected)
	}
	return nil
}

// MarkSucceeded conditionally moves an Attempt to SUCCEEDED and writes its result and
// completed_at. Result is written once: STARTED and DISPATCHED are the only sources the
// state machine allows, so a stale or illegal caller never overwrites a committed result.
func (r *nodeAttemptRepository) MarkSucceeded(ctx context.Context, attemptID string, from domain.NodeAttemptStatus, now time.Time, result json.RawMessage) error {
	if !from.CanTransitionTo(domain.NodeAttemptSucceeded) {
		return &domain.InvalidStateTransitionError{
			Entity: "NodeAttempt", ID: attemptID, From: string(from), To: string(domain.NodeAttemptSucceeded),
		}
	}

	const update = `
		UPDATE node_attempts
		   SET status = 'SUCCEEDED', result = $3, completed_at = $4
		 WHERE id = $1 AND status = $2`
	affected, err := affectedRows(ctx, r.conn, "node_attempts.MarkSucceeded", update,
		attemptID, string(from), nullableJSON(result), now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_attempts.MarkSucceeded: attempt=%s %s -> SUCCEEDED: %w",
			attemptID, from, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_attempts.MarkSucceeded", attemptID, affected)
	}
	return nil
}

// MarkFailed conditionally moves an Attempt to FAILED and writes its error and
// completed_at, with the same semantics as MarkSucceeded.
func (r *nodeAttemptRepository) MarkFailed(ctx context.Context, attemptID string, from domain.NodeAttemptStatus, now time.Time, execErr domain.ExecutionError) error {
	if !from.CanTransitionTo(domain.NodeAttemptFailed) {
		return &domain.InvalidStateTransitionError{
			Entity: "NodeAttempt", ID: attemptID, From: string(from), To: string(domain.NodeAttemptFailed),
		}
	}
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return fmt.Errorf("store/postgres node_attempts.MarkFailed: attempt=%s: %w", attemptID, err)
	}

	const update = `
		UPDATE node_attempts
		   SET status = 'FAILED', error = $3, completed_at = $4
		 WHERE id = $1 AND status = $2`
	affected, err := affectedRows(ctx, r.conn, "node_attempts.MarkFailed", update,
		attemptID, string(from), errPayload, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_attempts.MarkFailed: attempt=%s %s -> FAILED: %w",
			attemptID, from, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_attempts.MarkFailed", attemptID, affected)
	}
	return nil
}

// ListExpired returns non-terminal Attempts (STARTED or DISPATCHED) whose deadline_at has
// passed, using the (status, deadline_at) index. It backs Reconciler timeout rediscovery
// (06 §2: "普通 Node 的 timeout 由当前 Node Attempt 的 deadlineAt 驱动 ... Reconciler 必须
// 能够扫描过期的 STARTED 或 DISPATCHED Attempt").
func (r *nodeAttemptRepository) ListExpired(ctx context.Context, before time.Time, limit int) ([]domain.NodeAttempt, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres node_attempts.ListExpired: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + nodeAttemptColumns + `
		  FROM node_attempts
		 WHERE status IN ('STARTED', 'DISPATCHED')
		   AND deadline_at IS NOT NULL AND deadline_at <= $1
		 ORDER BY deadline_at ASC, id ASC
		 LIMIT $2`

	rows, err := r.conn.Query(ctx, query, before, limit)
	if err != nil {
		return nil, mapError("node_attempts.ListExpired", err)
	}
	attempts, err := scanNodeAttempts(rows)
	if err != nil {
		return nil, mapError("node_attempts.ListExpired", err)
	}
	return attempts, nil
}

// Latest returns the highest attempt_no Attempt of a NodeRun, or nil if none exists.
func (r *nodeAttemptRepository) Latest(ctx context.Context, nodeRunID string) (*domain.NodeAttempt, error) {
	const query = `SELECT ` + nodeAttemptColumns + `
		  FROM node_attempts
		 WHERE node_run_id = $1
		 ORDER BY attempt_no DESC
		 LIMIT 1`

	rows, err := r.conn.Query(ctx, query, nodeRunID)
	if err != nil {
		return nil, mapError("node_attempts.Latest", err, nodeRunID)
	}
	attempts, err := scanNodeAttempts(rows)
	if err != nil {
		return nil, mapError("node_attempts.Latest", err, nodeRunID)
	}
	if len(attempts) == 0 {
		return nil, nil
	}
	return &attempts[0], nil
}

func scanNodeAttempts(rows pgx.Rows) ([]domain.NodeAttempt, error) {
	defer rows.Close()

	out := []domain.NodeAttempt{}
	for rows.Next() {
		var (
			attempt    domain.NodeAttempt
			status     string
			result     []byte
			errPayload []byte
		)
		if err := rows.Scan(
			&attempt.ID, &attempt.NodeRunID, &attempt.AttemptNo, &status,
			&attempt.Input, &result, &attempt.CallbackTokenHash,
			&attempt.StartedAt, &attempt.DeadlineAt, &attempt.DispatchedAt,
			&attempt.CompletedAt, &errPayload,
		); err != nil {
			return nil, err
		}

		attempt.Status = domain.NodeAttemptStatus(status)
		attempt.Result = json.RawMessage(result)

		var err error
		if attempt.Error, err = unmarshalExecutionError(errPayload); err != nil {
			return nil, fmt.Errorf("attempt=%s: %w", attempt.ID, err)
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

var _ store.NodeAttemptRepository = (*nodeAttemptRepository)(nil)
