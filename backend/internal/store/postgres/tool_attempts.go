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
	callback_token_hash, started_at, dispatched_at, completed_at, error, runner_id, uncertain`

// toolAttemptPollColumns are the polling and external-operation facts. They are never part
// of the INSERT list: a new Attempt has no poll scheduled, no operation key and a known
// outcome, which the column defaults express.
const toolAttemptPollColumns = `next_poll_at, poll_count, operation_key, outcome_unknown_since`

// toolAttemptSelectColumns is toolAttemptColumns followed by the cost columns, which are
// written only by MarkSucceeded, and the poll columns.
const toolAttemptSelectColumns = toolAttemptColumns + `, ` + costColumns + `, ` + toolAttemptPollColumns

// qualifiedToolAttemptColumns is toolAttemptSelectColumns for a query that joins
// tool_attempts as "ta".
const qualifiedToolAttemptColumns = `ta.id, ta.action_id, ta.attempt_no, ta.tool_name, ta.status, ta.input,
	ta.result, ta.callback_token_hash, ta.started_at, ta.dispatched_at, ta.completed_at, ta.error,
	ta.runner_id, ta.uncertain, ta.cost_amount::text, ta.cost_currency, ta.cost_source,
	ta.next_poll_at, ta.poll_count, ta.operation_key, ta.outcome_unknown_since`

// Create inserts the STARTED Attempt in the transaction that claimed the Action; the Tool
// is called only after that transaction commits. UNIQUE (action_id, attempt_no) rejects a
// duplicated dispatch of the same call. The runner_id of the claiming process is written
// by this same INSERT, so no committed STARTED Attempt ever lacks the process that owns it.
func (r *toolAttemptRepository) Create(ctx context.Context, attempt domain.ToolAttempt) error {
	errPayload, err := marshalExecutionError(attempt.Error)
	if err != nil {
		return fmt.Errorf("store/postgres tool_attempts.Create: tool_attempt=%s: %w", attempt.ID, err)
	}

	const insert = `
		INSERT INTO tool_attempts (` + toolAttemptColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	if _, err := r.conn.Exec(ctx, insert,
		attempt.ID, attempt.ActionID, attempt.AttemptNo, attempt.ToolName, string(attempt.Status),
		attempt.Input, nullableJSON(attempt.Result), attempt.CallbackTokenHash,
		attempt.StartedAt, attempt.DispatchedAt, attempt.CompletedAt, errPayload,
		attempt.RunnerID, attempt.Uncertain,
	); err != nil {
		return mapError("tool_attempts.Create", err, attempt.ID, attempt.ActionID, attempt.AttemptNo)
	}
	return nil
}

func (r *toolAttemptRepository) Get(ctx context.Context, attemptID string) (domain.ToolAttempt, error) {
	const query = `SELECT ` + toolAttemptSelectColumns + ` FROM tool_attempts WHERE id = $1`

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
	const query = `SELECT ` + toolAttemptSelectColumns + `
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
// makes the dispatch record lose to an Agent timeout that already closed the Attempt. The
// first poll time is written by the same UPDATE, so a later process can derive the due
// poll from the committed dispatch alone; firstPollAt is nil when no poll policy is
// registered.
func (r *toolAttemptRepository) MarkDispatched(ctx context.Context, attemptID string, now time.Time, firstPollAt *time.Time) error {
	const update = `
		UPDATE tool_attempts
		   SET status = 'DISPATCHED', dispatched_at = $2, next_poll_at = $3
		 WHERE id = $1 AND status = 'STARTED'`

	affected, err := affectedRows(ctx, r.conn, "tool_attempts.MarkDispatched", update, attemptID, now, firstPollAt)
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
// writes its result and the Cost the Provider reported for the call, nil when it reported
// none: STARTED for a synchronous call, DISPATCHED for an asynchronous one resumed by its
// callback. A duplicated or late result transaction finds the row no longer in that
// status and loses here instead of overwriting the committed result or charge.
func (r *toolAttemptRepository) MarkSucceeded(ctx context.Context, attemptID string, from domain.ToolAttemptStatus, now time.Time, result json.RawMessage, cost *domain.Cost) error {
	if !from.CanTransitionTo(domain.ToolAttemptSucceeded) {
		return &domain.InvalidStateTransitionError{
			Entity: "ToolAttempt", ID: attemptID, From: string(from), To: string(domain.ToolAttemptSucceeded),
		}
	}
	amount, currency, source, err := costParams(cost)
	if err != nil {
		return fmt.Errorf("store/postgres tool_attempts.MarkSucceeded: tool_attempt=%s: %w", attemptID, err)
	}

	const update = `
		UPDATE tool_attempts
		   SET status = 'SUCCEEDED', result = $3, completed_at = $4,
		       cost_amount = $5::numeric, cost_currency = $6, cost_source = $7
		 WHERE id = $1 AND status = $2`

	affected, err := affectedRows(ctx, r.conn, "tool_attempts.MarkSucceeded", update,
		attemptID, string(from), nullableJSON(result), now, amount, currency, source)
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

// MarkInterrupted conditionally moves a STARTED Attempt whose process stopped to FAILED,
// writing its error, completed_at and the uncertain determination once. Requiring STARTED
// makes it lose to a result, a timeout or another handling of the same Attempt that
// committed first.
func (r *toolAttemptRepository) MarkInterrupted(ctx context.Context, attemptID string, now time.Time, execErr domain.ExecutionError, uncertain bool) error {
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return fmt.Errorf("store/postgres tool_attempts.MarkInterrupted: tool_attempt=%s: %w", attemptID, err)
	}

	const update = `
		UPDATE tool_attempts
		   SET status = 'FAILED', error = $2, completed_at = $3, uncertain = $4
		 WHERE id = $1 AND status = 'STARTED'`
	affected, err := affectedRows(ctx, r.conn, "tool_attempts.MarkInterrupted", update, attemptID, errPayload, now, uncertain)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres tool_attempts.MarkInterrupted: tool_attempt=%s STARTED -> FAILED: %w",
			attemptID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("tool_attempts.MarkInterrupted", attemptID, affected)
	}
	return nil
}

// MarkLost conditionally moves a DISPATCHED Attempt whose Provider no longer knows the
// dispatched task to FAILED, writing its error, completed_at and the uncertain
// determination once. Requiring DISPATCHED makes it lose to a callback, a timeout or
// another poll that committed first.
func (r *toolAttemptRepository) MarkLost(ctx context.Context, attemptID string, now time.Time, execErr domain.ExecutionError, uncertain bool) error {
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return fmt.Errorf("store/postgres tool_attempts.MarkLost: tool_attempt=%s: %w", attemptID, err)
	}

	const update = `
		UPDATE tool_attempts
		   SET status = 'FAILED', error = $2, completed_at = $3, uncertain = $4
		 WHERE id = $1 AND status = 'DISPATCHED'`
	affected, err := affectedRows(ctx, r.conn, "tool_attempts.MarkLost", update, attemptID, errPayload, now, uncertain)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres tool_attempts.MarkLost: tool_attempt=%s DISPATCHED -> FAILED: %w",
			attemptID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("tool_attempts.MarkLost", attemptID, affected)
	}
	return nil
}

// ClaimPoll conditionally claims one Provider poll. The WHERE clause is the single arbiter
// between concurrent scans and a concurrent completion: only a DISPATCHED Attempt that is
// due and below the bound is claimed, and the row lock taken by the UPDATE serialises
// racing claimers so exactly one sees an affected row. In SET, poll_count still reads the
// pre-update value, so the claim that reaches maxPolls clears next_poll_at instead of
// scheduling another poll.
func (r *toolAttemptRepository) ClaimPoll(ctx context.Context, attemptID string, now time.Time, interval time.Duration, maxPolls int) (bool, error) {
	if interval <= 0 {
		return false, fmt.Errorf("store/postgres tool_attempts.ClaimPoll: tool_attempt=%s: interval must be positive, got %s", attemptID, interval)
	}
	if maxPolls <= 0 {
		return false, fmt.Errorf("store/postgres tool_attempts.ClaimPoll: tool_attempt=%s: maxPolls must be positive, got %d", attemptID, maxPolls)
	}

	const update = `
		UPDATE tool_attempts
		   SET poll_count   = poll_count + 1,
		       next_poll_at = CASE WHEN poll_count + 1 >= $3 THEN NULL ELSE $4::timestamptz END
		 WHERE id = $1
		   AND status = 'DISPATCHED'
		   AND next_poll_at IS NOT NULL
		   AND next_poll_at <= $2
		   AND poll_count < $3`
	affected, err := affectedRows(ctx, r.conn, "tool_attempts.ClaimPoll", update,
		attemptID, now, maxPolls, now.Add(interval))
	if err != nil {
		return false, err
	}
	switch {
	case affected == 0:
		return false, nil
	case affected > 1:
		return false, errUnexpectedRows("tool_attempts.ClaimPoll", attemptID, affected)
	}
	return true, nil
}

// ClearPoll unschedules polling of a DISPATCHED Attempt. A terminal Attempt is left
// untouched: its next_poll_at no longer matters because only DISPATCHED rows are due.
func (r *toolAttemptRepository) ClearPoll(ctx context.Context, attemptID string) (bool, error) {
	const update = `
		UPDATE tool_attempts
		   SET next_poll_at = NULL
		 WHERE id = $1 AND status = 'DISPATCHED'`
	affected, err := affectedRows(ctx, r.conn, "tool_attempts.ClearPoll", update, attemptID)
	if err != nil {
		return false, err
	}
	switch {
	case affected == 0:
		return false, nil
	case affected > 1:
		return false, errUnexpectedRows("tool_attempts.ClearPoll", attemptID, affected)
	}
	return true, nil
}

// ListDuePolls discovers DISPATCHED Tool Attempts whose next poll is due, using the
// partial due-poll index. It returns identities only; it claims nothing.
func (r *toolAttemptRepository) ListDuePolls(ctx context.Context, now time.Time, limit int) ([]store.DueToolPoll, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres tool_attempts.ListDuePolls: limit must be positive, got %d", limit)
	}

	const query = `
		SELECT id, action_id
		  FROM tool_attempts
		 WHERE status = 'DISPATCHED'
		   AND next_poll_at IS NOT NULL AND next_poll_at <= $1
		 ORDER BY next_poll_at ASC, id ASC
		 LIMIT $2`

	rows, err := r.conn.Query(ctx, query, now, limit)
	if err != nil {
		return nil, mapError("tool_attempts.ListDuePolls", err)
	}
	defer rows.Close()

	out := []store.DueToolPoll{}
	for rows.Next() {
		var due store.DueToolPoll
		if err := rows.Scan(&due.AttemptID, &due.ActionID); err != nil {
			return nil, mapError("tool_attempts.ListDuePolls", err)
		}
		out = append(out, due)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("tool_attempts.ListDuePolls", err)
	}
	return out, nil
}

// ListOrphanStarted lists the STARTED synchronous Tool Attempts another process created.
// A NULL runner_id is never an orphan: which process wrote it is unknown. A callback
// credential hash marks an asynchronous Tool's Attempt, whose claim always stores one, and
// those are left to the deadline. Only Agent Runs that have not terminated and whose
// deadline has not passed are scanned; an expired one belongs to the Agent timeout. A
// paused Agent Run's deadline is not running, so its STARTED Attempts are listed whatever
// the clock says: a release that resumes the Agent must find them.
func (r *toolAttemptRepository) ListOrphanStarted(ctx context.Context, currentRunnerID string, now time.Time, limit int) ([]domain.ToolAttempt, error) {
	const query = `
		SELECT ` + qualifiedToolAttemptColumns + `
		  FROM tool_attempts ta
		  JOIN agent_actions aa ON aa.id = ta.action_id
		  JOIN agent_turns   t  ON t.id  = aa.turn_id
		  JOIN agent_runs    ar ON ar.id = t.agent_run_id
		 WHERE ta.status = 'STARTED'
		   AND ta.runner_id IS NOT NULL
		   AND ta.runner_id <> $1
		   AND ta.callback_token_hash IS NULL
		   AND ar.termination IS NULL
		   AND (ar.deadline > $2 OR ar.paused_at IS NOT NULL)
		 ORDER BY ta.started_at, ta.id
		 LIMIT $3`

	rows, err := r.conn.Query(ctx, query, currentRunnerID, now, limit)
	if err != nil {
		return nil, mapError("tool_attempts.ListOrphanStarted", err)
	}
	attempts, err := scanToolAttempts(rows)
	if err != nil {
		return nil, mapError("tool_attempts.ListOrphanStarted", err)
	}
	return attempts, nil
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
			attempt      domain.ToolAttempt
			status       string
			result       []byte
			errPayload   []byte
			costAmount   *string
			costCurrency *string
			costSource   *string
		)
		if err := rows.Scan(
			&attempt.ID, &attempt.ActionID, &attempt.AttemptNo, &attempt.ToolName, &status,
			&attempt.Input, &result, &attempt.CallbackTokenHash,
			&attempt.StartedAt, &attempt.DispatchedAt, &attempt.CompletedAt, &errPayload,
			&attempt.RunnerID, &attempt.Uncertain,
			&costAmount, &costCurrency, &costSource,
			&attempt.NextPollAt, &attempt.PollCount, &attempt.OperationKey, &attempt.OutcomeUnknownSince,
		); err != nil {
			return nil, err
		}

		attempt.Status = domain.ToolAttemptStatus(status)
		attempt.Result = json.RawMessage(result)

		var err error
		if attempt.Error, err = unmarshalExecutionError(errPayload); err != nil {
			return nil, fmt.Errorf("tool_attempt=%s: %w", attempt.ID, err)
		}
		if attempt.Cost, err = scanCost(costAmount, costCurrency, costSource); err != nil {
			return nil, fmt.Errorf("tool_attempt=%s: %w", attempt.ID, err)
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

var _ store.ToolAttemptRepository = (*toolAttemptRepository)(nil)
