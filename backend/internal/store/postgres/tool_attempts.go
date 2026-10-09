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

type toolAttemptRepository struct {
	conn pgx.Tx
}

const toolAttemptColumns = `id, action_id, attempt_no, tool_name, status, input, result,
	callback_token_hash, started_at, dispatched_at, completed_at, error`

// Create inserts the STARTED Attempt in the transaction that claimed the Action; the Tool
// is called only after that transaction commits. UNIQUE (action_id, attempt_no) rejects a
// duplicated dispatch of the same call.
func (r *toolAttemptRepository) Create(ctx context.Context, attempt domain.ToolAttempt) error {
	errPayload, err := marshalExecutionError(attempt.Error)
	if err != nil {
		return fmt.Errorf("store/postgres tool_attempts.Create: tool_attempt=%s: %w", attempt.ID, err)
	}

	const insert = `
		INSERT INTO tool_attempts (` + toolAttemptColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	if _, err := r.conn.Exec(ctx, insert,
		attempt.ID, attempt.ActionID, attempt.AttemptNo, attempt.ToolName, string(attempt.Status),
		attempt.Input, nullableJSON(attempt.Result), attempt.CallbackTokenHash,
		attempt.StartedAt, attempt.DispatchedAt, attempt.CompletedAt, errPayload,
	); err != nil {
		return mapError("tool_attempts.Create", err, attempt.ID, attempt.ActionID, attempt.AttemptNo)
	}
	return nil
}

func (r *toolAttemptRepository) Get(ctx context.Context, attemptID string) (domain.ToolAttempt, error) {
	const query = `SELECT ` + toolAttemptColumns + ` FROM tool_attempts WHERE id = $1`

	rows, err := r.conn.Query(ctx, query, attemptID)
	if err != nil {
		return domain.ToolAttempt{}, mapError("tool_attempts.Get", err, attemptID)
	}
	attempts, err := scanToolAttempts(rows)
	if err != nil {
		return domain.ToolAttempt{}, mapError("tool_attempts.Get", err, attemptID)
	}
	if len(attempts) == 0 {
		return domain.ToolAttempt{}, mapError("tool_attempts.Get", pgx.ErrNoRows, attemptID)
	}
	return attempts[0], nil
}

func (r *toolAttemptRepository) ListByActionID(ctx context.Context, actionID string) ([]domain.ToolAttempt, error) {
	const query = `SELECT ` + toolAttemptColumns + `
		  FROM tool_attempts WHERE action_id = $1 ORDER BY attempt_no ASC`

	rows, err := r.conn.Query(ctx, query, actionID)
	if err != nil {
		return nil, mapError("tool_attempts.ListByActionID", err, actionID)
	}
	attempts, err := scanToolAttempts(rows)
	if err != nil {
		return nil, mapError("tool_attempts.ListByActionID", err, actionID)
	}
	return attempts, nil
}

// MarkDispatched conditionally moves a STARTED Attempt to DISPATCHED. Requiring STARTED
// makes the dispatch record lose to an Agent timeout that already closed the Attempt.
func (r *toolAttemptRepository) MarkDispatched(ctx context.Context, attemptID string, now time.Time) error {
	const update = `
		UPDATE tool_attempts
		   SET status = 'DISPATCHED', dispatched_at = $2
		 WHERE id = $1 AND status = 'STARTED'`

	affected, err := affectedRows(ctx, r.conn, "tool_attempts.MarkDispatched", update, attemptID, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres tool_attempts.MarkDispatched: tool_attempt=%s STARTED -> DISPATCHED: %w",
			attemptID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("tool_attempts.MarkDispatched", attemptID, affected)
	}
	return nil
}

// MarkSucceeded conditionally moves an Attempt from the given status to SUCCEEDED and
// writes its result: STARTED for a synchronous call, DISPATCHED for an asynchronous one
// resumed by its callback. A duplicated or late result transaction finds the row no
// longer in that status and loses here instead of overwriting the committed result.
func (r *toolAttemptRepository) MarkSucceeded(ctx context.Context, attemptID string, from domain.ToolAttemptStatus, now time.Time, result json.RawMessage) error {
	if !from.CanTransitionTo(domain.ToolAttemptSucceeded) {
		return &domain.InvalidStateTransitionError{
			Entity: "ToolAttempt", ID: attemptID, From: string(from), To: string(domain.ToolAttemptSucceeded),
		}
	}

	const update = `
		UPDATE tool_attempts
		   SET status = 'SUCCEEDED', result = $3, completed_at = $4
		 WHERE id = $1 AND status = $2`

	affected, err := affectedRows(ctx, r.conn, "tool_attempts.MarkSucceeded", update,
		attemptID, string(from), nullableJSON(result), now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres tool_attempts.MarkSucceeded: tool_attempt=%s %s -> SUCCEEDED: %w",
			attemptID, from, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("tool_attempts.MarkSucceeded", attemptID, affected)
	}
	return nil
}

// MarkFailed conditionally moves an Attempt from the given status to FAILED and writes
// its error, with the same single-winner semantics as MarkSucceeded.
func (r *toolAttemptRepository) MarkFailed(ctx context.Context, attemptID string, from domain.ToolAttemptStatus, now time.Time, execErr domain.ExecutionError) error {
	if !from.CanTransitionTo(domain.ToolAttemptFailed) {
		return &domain.InvalidStateTransitionError{
			Entity: "ToolAttempt", ID: attemptID, From: string(from), To: string(domain.ToolAttemptFailed),
		}
	}
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return fmt.Errorf("store/postgres tool_attempts.MarkFailed: tool_attempt=%s: %w", attemptID, err)
	}

	const update = `
		UPDATE tool_attempts
		   SET status = 'FAILED', error = $3, completed_at = $4
		 WHERE id = $1 AND status = $2`
	affected, err := affectedRows(ctx, r.conn, "tool_attempts.MarkFailed", update,
		attemptID, string(from), errPayload, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres tool_attempts.MarkFailed: tool_attempt=%s %s -> FAILED: %w",
			attemptID, from, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("tool_attempts.MarkFailed", attemptID, affected)
	}
	return nil
}

// MarkTimedOut conditionally fails an Attempt that is still STARTED or DISPATCHED. The
// Agent deadline bounds the synchronous call and the wait for an asynchronous Tool's
// callback alike, so the Agent timeout transaction closes both; a result that
// committed first leaves the row no longer in either state, and this reports false rather
// than relabelling it.
func (r *toolAttemptRepository) MarkTimedOut(ctx context.Context, attemptID string, now time.Time, execErr domain.ExecutionError) (bool, error) {
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return false, fmt.Errorf("store/postgres tool_attempts.MarkTimedOut: tool_attempt=%s: %w", attemptID, err)
	}

	const update = `
		UPDATE tool_attempts
		   SET status = 'FAILED', error = $2, completed_at = $3
		 WHERE id = $1 AND status IN ('STARTED', 'DISPATCHED')`
	affected, err := affectedRows(ctx, r.conn, "tool_attempts.MarkTimedOut", update, attemptID, errPayload, now)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("tool_attempts.MarkTimedOut", attemptID, affected)
	}
	return affected == 1, nil
}

// CountByAgentRunForTools counts every Tool Attempt of the Agent Run whose Tool is in
// toolNames, whatever its status. Attempts reach the Agent Run through their Action and
// Turn. Counting Attempts rather than Actions is deliberate: an Action rejected at claim
// never gets an Attempt and so never consumes the generation limit, while a failed
// Attempt may already have reached the Provider and does.
func (r *toolAttemptRepository) CountByAgentRunForTools(ctx context.Context, agentRunID string, toolNames []string) (int, error) {
	if len(toolNames) == 0 {
		return 0, nil
	}

	const query = `
		SELECT count(*)
		  FROM tool_attempts ta
		  JOIN agent_actions aa ON aa.id = ta.action_id
		  JOIN agent_turns   t  ON t.id  = aa.turn_id
		 WHERE t.agent_run_id = $1
		   AND ta.tool_name = ANY($2)`

	var count int
	if err := r.conn.QueryRow(ctx, query, agentRunID, toolNames).Scan(&count); err != nil {
		return 0, mapError("tool_attempts.CountByAgentRunForTools", err, agentRunID)
	}
	return count, nil
}

func scanToolAttempts(rows pgx.Rows) ([]domain.ToolAttempt, error) {
	defer rows.Close()

	out := []domain.ToolAttempt{}
	for rows.Next() {
		var (
			attempt    domain.ToolAttempt
			status     string
			result     []byte
			errPayload []byte
		)
		if err := rows.Scan(
			&attempt.ID, &attempt.ActionID, &attempt.AttemptNo, &attempt.ToolName, &status,
			&attempt.Input, &result, &attempt.CallbackTokenHash,
			&attempt.StartedAt, &attempt.DispatchedAt, &attempt.CompletedAt, &errPayload,
		); err != nil {
			return nil, err
		}

		attempt.Status = domain.ToolAttemptStatus(status)
		attempt.Result = json.RawMessage(result)

		var err error
		if attempt.Error, err = unmarshalExecutionError(errPayload); err != nil {
			return nil, fmt.Errorf("tool_attempt=%s: %w", attempt.ID, err)
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

var _ store.ToolAttemptRepository = (*toolAttemptRepository)(nil)
