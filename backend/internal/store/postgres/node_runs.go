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

type nodeRunRepository struct {
	conn pgx.Tx
}

const nodeRunColumns = `id, run_id, node_id, node_type, status, input, output, error,
	attempt_count, next_attempt_at, ready_at, started_at, waiting_at, completed_at,
	latency_ms, token_usage, updated_at`

// Create inserts a NodeRun. UNIQUE (run_id, node_id) is what makes repeated scheduler
// computation by immediate advancement and the Reconciler converge on one row.
func (r *nodeRunRepository) Create(ctx context.Context, nr domain.NodeRun) error {
	errPayload, err := marshalExecutionError(nr.Error)
	if err != nil {
		return fmt.Errorf("store/postgres node_runs.Create: node_run=%s: %w", nr.ID, err)
	}
	tokenUsage, err := marshalTokenUsage(nr.TokenUsage)
	if err != nil {
		return fmt.Errorf("store/postgres node_runs.Create: node_run=%s: %w", nr.ID, err)
	}

	const insert = `
		INSERT INTO node_runs (` + nodeRunColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`
	if _, err := r.conn.Exec(ctx, insert,
		nr.ID, nr.RunID, nr.NodeID, nr.NodeType, string(nr.Status),
		nr.Input, nullableJSON(nr.Output), errPayload,
		nr.AttemptCount, nr.NextAttemptAt, nr.ReadyAt, nr.StartedAt, nr.WaitingAt,
		nr.CompletedAt, nr.LatencyMs, tokenUsage, nr.UpdatedAt,
	); err != nil {
		return mapError("node_runs.Create", err, nr.ID, nr.RunID, nr.NodeID)
	}
	return nil
}

func (r *nodeRunRepository) Get(ctx context.Context, nodeRunID string) (domain.NodeRun, error) {
	const query = `SELECT ` + nodeRunColumns + ` FROM node_runs WHERE id = $1`

	rows, err := r.conn.Query(ctx, query, nodeRunID)
	if err != nil {
		return domain.NodeRun{}, mapError("node_runs.Get", err, nodeRunID)
	}
	nodeRuns, err := scanNodeRuns(rows)
	if err != nil {
		return domain.NodeRun{}, mapError("node_runs.Get", err, nodeRunID)
	}
	if len(nodeRuns) == 0 {
		return domain.NodeRun{}, mapError("node_runs.Get", pgx.ErrNoRows, nodeRunID)
	}
	return nodeRuns[0], nil
}

func (r *nodeRunRepository) ListByRun(ctx context.Context, runID string) ([]domain.NodeRun, error) {
	const query = `SELECT ` + nodeRunColumns + `
		  FROM node_runs WHERE run_id = $1 ORDER BY ready_at ASC, node_id ASC`

	rows, err := r.conn.Query(ctx, query, runID)
	if err != nil {
		return nil, mapError("node_runs.ListByRun", err, runID)
	}
	nodeRuns, err := scanNodeRuns(rows)
	if err != nil {
		return nil, mapError("node_runs.ListByRun", err, runID)
	}
	return nodeRuns, nil
}

// ClaimReady conditionally moves READY to RUNNING. Exactly one concurrent caller sees an
// affected-row count of one and therefore owns the execution slot; everyone else must
// stop. A false result is an ordinary outcome, not an error.
func (r *nodeRunRepository) ClaimReady(ctx context.Context, nodeRunID string, now time.Time) (bool, error) {
	const claim = `
		UPDATE node_runs
		   SET status = 'RUNNING', started_at = $2, updated_at = $2
		 WHERE id = $1 AND status = 'READY'`

	affected, err := affectedRows(ctx, r.conn, "node_runs.ClaimReady", claim, nodeRunID, now)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("node_runs.ClaimReady", nodeRunID, affected)
	}
	return affected == 1, nil
}

// Transition conditionally moves a NodeRun between two states. It writes status and
// updated_at only; outcome fields such as output, latency and error belong to the
// completion use cases that own those transactions.
func (r *nodeRunRepository) Transition(ctx context.Context, nodeRunID string, from, to domain.NodeRunStatus, now time.Time) error {
	if !from.CanTransitionTo(to) {
		return &domain.InvalidStateTransitionError{
			Entity: "NodeRun", ID: nodeRunID, From: string(from), To: string(to),
		}
	}

	const update = `
		UPDATE node_runs
		   SET status = $3, updated_at = $4
		 WHERE id = $1 AND status = $2`
	affected, err := affectedRows(ctx, r.conn, "node_runs.Transition", update,
		nodeRunID, string(from), string(to), now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_runs.Transition: node_run=%s %s -> %s: %w",
			nodeRunID, from, to, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_runs.Transition", nodeRunID, affected)
	}
	return nil
}

// ListReadyOrRetryable returns persisted work the Reconciler can rediscover after a
// crash: READY NodeRuns that nobody claimed, and RUNNING NodeRuns whose retry backoff
// has elapsed. WAITING_CALLBACK NodeRuns are recovered through their Callback Binding,
// so they are deliberately excluded here.
func (r *nodeRunRepository) ListReadyOrRetryable(ctx context.Context, before time.Time, limit int) ([]domain.NodeRun, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres node_runs.ListReadyOrRetryable: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + nodeRunColumns + `
		  FROM node_runs
		 WHERE status = 'READY'
		    OR (status = 'RUNNING' AND next_attempt_at IS NOT NULL AND next_attempt_at <= $1)
		 ORDER BY ready_at ASC, id ASC
		 LIMIT $2`

	rows, err := r.conn.Query(ctx, query, before, limit)
	if err != nil {
		return nil, mapError("node_runs.ListReadyOrRetryable", err)
	}
	nodeRuns, err := scanNodeRuns(rows)
	if err != nil {
		return nil, mapError("node_runs.ListReadyOrRetryable", err)
	}
	return nodeRuns, nil
}

// ClaimRetry conditionally clears next_attempt_at on a RUNNING NodeRun whose backoff has
// elapsed, so exactly one caller owns dispatching the next Attempt. It reuses the
// ClaimReady idiom: affected rows decides the single winner, and false is an ordinary,
// non-error outcome (not yet due, or already claimed by another advancement path).
func (r *nodeRunRepository) ClaimRetry(ctx context.Context, nodeRunID string, now time.Time) (bool, error) {
	const claim = `
		UPDATE node_runs
		   SET next_attempt_at = NULL, updated_at = $2
		 WHERE id = $1 AND status = 'RUNNING'
		   AND next_attempt_at IS NOT NULL AND next_attempt_at <= $2`

	affected, err := affectedRows(ctx, r.conn, "node_runs.ClaimRetry", claim, nodeRunID, now)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("node_runs.ClaimRetry", nodeRunID, affected)
	}
	return affected == 1, nil
}

// ScheduleRetry records the next backoff deadline for a NodeRun that stays RUNNING after
// a retryable Attempt failure (retry allowed: the Attempt is FAILED, the NodeRun stays
// RUNNING with next_attempt_at set, and NODE_RETRYING is written). It requires the
// NodeRun to still be RUNNING.
func (r *nodeRunRepository) ScheduleRetry(ctx context.Context, nodeRunID string, nextAttemptAt, now time.Time) error {
	const update = `
		UPDATE node_runs
		   SET next_attempt_at = $2, updated_at = $3
		 WHERE id = $1 AND status = 'RUNNING'`

	affected, err := affectedRows(ctx, r.conn, "node_runs.ScheduleRetry", update, nodeRunID, nextAttemptAt, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_runs.ScheduleRetry: node_run=%s: %w", nodeRunID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_runs.ScheduleRetry", nodeRunID, affected)
	}
	return nil
}

// IncrementAttemptCount bumps attempt_count and returns the new value, so the caller can
// stamp the Attempt it is about to create with the right attempt_no.
func (r *nodeRunRepository) IncrementAttemptCount(ctx context.Context, nodeRunID string) (int, error) {
	const update = `
		UPDATE node_runs SET attempt_count = attempt_count + 1
		 WHERE id = $1
		 RETURNING attempt_count`

	var count int
	if err := r.conn.QueryRow(ctx, update, nodeRunID).Scan(&count); err != nil {
		return 0, mapError("node_runs.IncrementAttemptCount", err, nodeRunID)
	}
	return count, nil
}

// SetInput overwrites the NodeRun's recorded input, used when scheduling resolves the
// node's actual input ahead of dispatch.
func (r *nodeRunRepository) SetInput(ctx context.Context, nodeRunID string, input json.RawMessage) error {
	const update = `UPDATE node_runs SET input = $2 WHERE id = $1`

	affected, err := affectedRows(ctx, r.conn, "node_runs.SetInput", update, nodeRunID, input)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return mapError("node_runs.SetInput", pgx.ErrNoRows, nodeRunID)
	case affected > 1:
		return errUnexpectedRows("node_runs.SetInput", nodeRunID, affected)
	}
	return nil
}

// MarkSucceeded conditionally moves a NodeRun to SUCCEEDED and writes its outcome
// (output, token usage, latency) and completed_at in the same statement.
func (r *nodeRunRepository) MarkSucceeded(ctx context.Context, nodeRunID string, from domain.NodeRunStatus, now time.Time, outcome store.NodeRunOutcome) error {
	if !from.CanTransitionTo(domain.NodeRunSucceeded) {
		return &domain.InvalidStateTransitionError{
			Entity: "NodeRun", ID: nodeRunID, From: string(from), To: string(domain.NodeRunSucceeded),
		}
	}
	tokenUsage, err := marshalTokenUsage(outcome.TokenUsage)
	if err != nil {
		return fmt.Errorf("store/postgres node_runs.MarkSucceeded: node_run=%s: %w", nodeRunID, err)
	}

	const update = `
		UPDATE node_runs
		   SET status       = 'SUCCEEDED',
		       output       = $4,
		       token_usage  = $5,
		       latency_ms   = COALESCE($6, CASE WHEN started_at IS NOT NULL
		                                        THEN ROUND(EXTRACT(EPOCH FROM ($3 - started_at)) * 1000)::BIGINT
		                                        ELSE NULL END),
		       completed_at = $3,
		       updated_at   = $3
		 WHERE id = $1 AND status = $2`
	affected, err := affectedRows(ctx, r.conn, "node_runs.MarkSucceeded", update,
		nodeRunID, string(from), now, nullableJSON(outcome.Output), tokenUsage, outcome.LatencyMs)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_runs.MarkSucceeded: node_run=%s %s -> SUCCEEDED: %w",
			nodeRunID, from, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_runs.MarkSucceeded", nodeRunID, affected)
	}
	return nil
}

// MarkFailed conditionally moves a NodeRun to FAILED and writes its error, token usage,
// latency and completed_at in the same statement. latency_ms is always derived from
// started_at here: an unrecoverable failure carries no separate outcome to override it
// with.
func (r *nodeRunRepository) MarkFailed(ctx context.Context, nodeRunID string, from domain.NodeRunStatus, now time.Time, execErr domain.ExecutionError, usage *domain.TokenUsage) error {
	if !from.CanTransitionTo(domain.NodeRunFailed) {
		return &domain.InvalidStateTransitionError{
			Entity: "NodeRun", ID: nodeRunID, From: string(from), To: string(domain.NodeRunFailed),
		}
	}
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return fmt.Errorf("store/postgres node_runs.MarkFailed: node_run=%s: %w", nodeRunID, err)
	}
	tokenUsage, err := marshalTokenUsage(usage)
	if err != nil {
		return fmt.Errorf("store/postgres node_runs.MarkFailed: node_run=%s: %w", nodeRunID, err)
	}

	const update = `
		UPDATE node_runs
		   SET status       = 'FAILED',
		       error        = $4,
		       token_usage  = $5,
		       latency_ms   = CASE WHEN started_at IS NOT NULL
		                           THEN ROUND(EXTRACT(EPOCH FROM ($3 - started_at)) * 1000)::BIGINT
		                           ELSE latency_ms END,
		       completed_at = $3,
		       updated_at   = $3
		 WHERE id = $1 AND status = $2`
	affected, err := affectedRows(ctx, r.conn, "node_runs.MarkFailed", update,
		nodeRunID, string(from), now, errPayload, tokenUsage)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_runs.MarkFailed: node_run=%s %s -> FAILED: %w",
			nodeRunID, from, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_runs.MarkFailed", nodeRunID, affected)
	}
	return nil
}

// MarkWaiting conditionally moves a RUNNING NodeRun to WAITING_CALLBACK and stamps
// waiting_at. WAITING_CALLBACK is reachable only from RUNNING (domain.nodeRunTransitions),
// so the source state is fixed rather than taken as a parameter.
func (r *nodeRunRepository) MarkWaiting(ctx context.Context, nodeRunID string, now time.Time) error {
	const update = `
		UPDATE node_runs
		   SET status = 'WAITING_CALLBACK', waiting_at = $2, updated_at = $2
		 WHERE id = $1 AND status = 'RUNNING'`

	affected, err := affectedRows(ctx, r.conn, "node_runs.MarkWaiting", update, nodeRunID, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres node_runs.MarkWaiting: node_run=%s: %w", nodeRunID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("node_runs.MarkWaiting", nodeRunID, affected)
	}
	return nil
}

func scanNodeRuns(rows pgx.Rows) ([]domain.NodeRun, error) {
	defer rows.Close()

	out := []domain.NodeRun{}
	for rows.Next() {
		var (
			nr         domain.NodeRun
			status     string
			output     []byte
			errPayload []byte
			tokenUsage []byte
		)
		if err := rows.Scan(
			&nr.ID, &nr.RunID, &nr.NodeID, &nr.NodeType, &status,
			&nr.Input, &output, &errPayload,
			&nr.AttemptCount, &nr.NextAttemptAt, &nr.ReadyAt, &nr.StartedAt, &nr.WaitingAt,
			&nr.CompletedAt, &nr.LatencyMs, &tokenUsage, &nr.UpdatedAt,
		); err != nil {
			return nil, err
		}

		nr.Status = domain.NodeRunStatus(status)
		nr.Output = json.RawMessage(output)

		var err error
		if nr.Error, err = unmarshalExecutionError(errPayload); err != nil {
			return nil, fmt.Errorf("node_run=%s: %w", nr.ID, err)
		}
		if nr.TokenUsage, err = unmarshalTokenUsage(tokenUsage); err != nil {
			return nil, fmt.Errorf("node_run=%s: %w", nr.ID, err)
		}
		out = append(out, nr)
	}
	return out, rows.Err()
}

var _ store.NodeRunRepository = (*nodeRunRepository)(nil)
