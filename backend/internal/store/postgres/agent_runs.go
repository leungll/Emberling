package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type agentRunRepository struct {
	conn pgx.Tx
}

const agentRunColumns = `id, node_run_id, instructions, model_id, model_config, allowed_tools,
	context_schema, state_schema, output_schema, max_turns, current_turn_no,
	current_context_version, current_state_version, started_at, deadline,
	terminated_at, termination, error, max_generation_calls, image_model_id, review_model_id,
	goal, paused_at, pause_reason`

// Create inserts the Agent Run with its frozen configuration. UNIQUE (node_run_id) is
// what enforces "one Agent NodeRun has at most one Agent Run": a duplicated
// initialisation transaction loses in the database, not in a process-local check.
func (r *agentRunRepository) Create(ctx context.Context, run domain.AgentRun) error {
	allowedTools, err := json.Marshal(run.AllowedTools)
	if err != nil {
		return fmt.Errorf("store/postgres agent_runs.Create: agent_run=%s: marshal allowed tools: %w", run.ID, err)
	}
	errPayload, err := marshalExecutionError(run.Error)
	if err != nil {
		return fmt.Errorf("store/postgres agent_runs.Create: agent_run=%s: %w", run.ID, err)
	}

	const insert = `
		INSERT INTO agent_runs (` + agentRunColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
		        $22, $23, $24)`
	if _, err := r.conn.Exec(ctx, insert,
		run.ID, run.NodeRunID, run.Instructions, run.ModelID, run.ModelConfig, allowedTools,
		run.ContextSchema, run.StateSchema, run.OutputSchema, run.MaxTurns, run.CurrentTurnNo,
		run.CurrentContextVersion, run.CurrentStateVersion, run.StartedAt, run.Deadline,
		run.TerminatedAt, nullableTermination(run.Termination), errPayload, run.MaxGenerationCalls,
		run.ImageModelID, run.ReviewModelID,
		nullableString((*string)(run.Goal)), run.PausedAt, nullableString((*string)(run.PauseReason)),
	); err != nil {
		return mapError("agent_runs.Create", err, run.ID, run.NodeRunID)
	}
	return nil
}

func (r *agentRunRepository) Get(ctx context.Context, agentRunID string) (domain.AgentRun, error) {
	const query = `SELECT ` + agentRunColumns + ` FROM agent_runs WHERE id = $1`
	return r.queryOne(ctx, "agent_runs.Get", query, agentRunID)
}

func (r *agentRunRepository) GetByNodeRunID(ctx context.Context, nodeRunID string) (domain.AgentRun, error) {
	const query = `SELECT ` + agentRunColumns + ` FROM agent_runs WHERE node_run_id = $1`
	return r.queryOne(ctx, "agent_runs.GetByNodeRunID", query, nodeRunID)
}

// AdvancePointers conditionally moves the recovery position. Matching the expected
// pointers in the WHERE clause is what stops a path that read the Agent Run before
// another transaction committed from rewinding the committed position.
func (r *agentRunRepository) AdvancePointers(ctx context.Context, agentRunID string, from, to store.AgentRunPointers) (bool, error) {
	const update = `
		UPDATE agent_runs
		   SET current_turn_no         = $5,
		       current_context_version = $6,
		       current_state_version   = $7
		 WHERE id = $1
		   AND current_turn_no         = $2
		   AND current_context_version = $3
		   AND current_state_version   = $4`

	affected, err := affectedRows(ctx, r.conn, "agent_runs.AdvancePointers", update,
		agentRunID, from.CurrentTurnNo, from.CurrentContextVersion, from.CurrentStateVersion,
		to.CurrentTurnNo, to.CurrentContextVersion, to.CurrentStateVersion)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("agent_runs.AdvancePointers", agentRunID, affected)
	}
	return affected == 1, nil
}

// Terminate conditionally records why the Agent Run stopped. Tool failure, timeout and
// Final completion can all reach the same Agent Run; the `termination IS NULL` predicate
// decides which one owns the reason, so a late timeout cannot relabel a committed
// TOOL_ERROR or FINAL_RESPONSE.
func (r *agentRunRepository) Terminate(ctx context.Context, agentRunID string, termination domain.AgentTermination, now time.Time, execErr *domain.ExecutionError) (bool, error) {
	if !termination.IsValid() {
		return false, fmt.Errorf("store/postgres agent_runs.Terminate: agent_run=%s: unknown termination %q",
			agentRunID, termination)
	}
	errPayload, err := marshalExecutionError(execErr)
	if err != nil {
		return false, fmt.Errorf("store/postgres agent_runs.Terminate: agent_run=%s: %w", agentRunID, err)
	}

	const update = `
		UPDATE agent_runs
		   SET termination = $2, terminated_at = $3, error = $4
		 WHERE id = $1 AND termination IS NULL`
	affected, err := affectedRows(ctx, r.conn, "agent_runs.Terminate", update,
		agentRunID, string(termination), now, errPayload)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("agent_runs.Terminate", agentRunID, affected)
	}
	return affected == 1, nil
}

// ListExpired returns the Agent Runs whose deadline has passed and that nothing has
// terminated yet -- the persisted Agent timeout work. `termination IS NULL` is the
// same predicate Terminate conditions on, so an Agent Run another path already closed is
// never rediscovered as work. A paused Agent Run is not listed: its deadline is not
// running and moves later when it resumes. The limit bounds the scan batch.
func (r *agentRunRepository) ListExpired(ctx context.Context, before time.Time, limit int) ([]domain.AgentRun, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres agent_runs.ListExpired: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + agentRunColumns + `
		  FROM agent_runs
		 WHERE termination IS NULL AND paused_at IS NULL AND deadline <= $1
		 ORDER BY id ASC
		 LIMIT $2`

	rows, err := r.conn.Query(ctx, query, before, limit)
	if err != nil {
		return nil, mapError("agent_runs.ListExpired", err)
	}
	runs, err := scanAgentRuns(rows)
	if err != nil {
		return nil, mapError("agent_runs.ListExpired", err)
	}
	return runs, nil
}

// Pause stops the clock. `paused_at IS NULL` makes an overlapping pause fail: the caller
// decides when the Agent waits, and two waits at once are its bug.
func (r *agentRunRepository) Pause(ctx context.Context, agentRunID string, reason domain.AgentPauseReason, now time.Time) error {
	if !reason.IsValid() {
		return fmt.Errorf("store/postgres agent_runs.Pause: agent_run=%s: unknown pause reason %q", agentRunID, reason)
	}
	const update = `
		UPDATE agent_runs
		   SET paused_at = $2, pause_reason = $3
		 WHERE id = $1 AND paused_at IS NULL AND termination IS NULL`
	affected, err := affectedRows(ctx, r.conn, "agent_runs.Pause", update, agentRunID, now, string(reason))
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres agent_runs.Pause: agent_run=%s is already paused or terminated: %w", agentRunID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("agent_runs.Pause", agentRunID, affected)
	}
	return nil
}

// Resume restarts the clock. The deadline moves later by exactly the paused duration, so
// the Agent keeps the execution time it had when it paused; a clock that did not advance
// (or went backwards) leaves the deadline where it was. The UPDATE reads paused_at before
// clearing it, so the shift and the clearing commit as one statement.
func (r *agentRunRepository) Resume(ctx context.Context, agentRunID string, now time.Time) (time.Time, error) {
	const update = `
		UPDATE agent_runs
		   SET deadline     = CASE WHEN $2::timestamptz > paused_at THEN deadline + ($2::timestamptz - paused_at) ELSE deadline END,
		       paused_at    = NULL,
		       pause_reason = NULL
		 WHERE id = $1 AND paused_at IS NOT NULL
		 RETURNING deadline`
	var deadline time.Time
	err := r.conn.QueryRow(ctx, update, agentRunID, now).Scan(&deadline)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, fmt.Errorf("store/postgres agent_runs.Resume: agent_run=%s is not paused: %w", agentRunID, domain.ErrStaleClaim)
		}
		return time.Time{}, mapError("agent_runs.Resume", err, agentRunID)
	}
	return deadline, nil
}

// SetPauseReason relabels a pause without restarting it; paused_at keeps the original
// time so the whole wait is credited back on resume.
func (r *agentRunRepository) SetPauseReason(ctx context.Context, agentRunID string, reason domain.AgentPauseReason) error {
	if !reason.IsValid() {
		return fmt.Errorf("store/postgres agent_runs.SetPauseReason: agent_run=%s: unknown pause reason %q", agentRunID, reason)
	}
	const update = `
		UPDATE agent_runs
		   SET pause_reason = $2
		 WHERE id = $1 AND paused_at IS NOT NULL`
	affected, err := affectedRows(ctx, r.conn, "agent_runs.SetPauseReason", update, agentRunID, string(reason))
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres agent_runs.SetPauseReason: agent_run=%s is not paused: %w", agentRunID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("agent_runs.SetPauseReason", agentRunID, affected)
	}
	return nil
}

func (r *agentRunRepository) queryOne(ctx context.Context, op, query string, arg any) (domain.AgentRun, error) {
	rows, err := r.conn.Query(ctx, query, arg)
	if err != nil {
		return domain.AgentRun{}, mapError(op, err, arg)
	}
	runs, err := scanAgentRuns(rows)
	if err != nil {
		return domain.AgentRun{}, mapError(op, err, arg)
	}
	if len(runs) == 0 {
		return domain.AgentRun{}, mapError(op, pgx.ErrNoRows, arg)
	}
	return runs[0], nil
}

func scanAgentRuns(rows pgx.Rows) ([]domain.AgentRun, error) {
	defer rows.Close()

	out := []domain.AgentRun{}
	for rows.Next() {
		var (
			run          domain.AgentRun
			allowedTools []byte
			termination  *string
			errPayload   []byte
			goal         *string
			pauseReason  *string
		)
		if err := rows.Scan(
			&run.ID, &run.NodeRunID, &run.Instructions, &run.ModelID, &run.ModelConfig, &allowedTools,
			&run.ContextSchema, &run.StateSchema, &run.OutputSchema, &run.MaxTurns, &run.CurrentTurnNo,
			&run.CurrentContextVersion, &run.CurrentStateVersion, &run.StartedAt, &run.Deadline,
			&run.TerminatedAt, &termination, &errPayload, &run.MaxGenerationCalls,
			&run.ImageModelID, &run.ReviewModelID,
			&goal, &run.PausedAt, &pauseReason,
		); err != nil {
			return nil, err
		}
		if goal != nil {
			value := domain.AgentGoal(*goal)
			run.Goal = &value
		}
		if pauseReason != nil {
			value := domain.AgentPauseReason(*pauseReason)
			run.PauseReason = &value
		}

		if err := json.Unmarshal(allowedTools, &run.AllowedTools); err != nil {
			return nil, fmt.Errorf("agent_run=%s: unmarshal allowed tools: %w", run.ID, err)
		}
		if termination != nil {
			value := domain.AgentTermination(*termination)
			run.Termination = &value
		}

		var err error
		if run.Error, err = unmarshalExecutionError(errPayload); err != nil {
			return nil, fmt.Errorf("agent_run=%s: %w", run.ID, err)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// nullableString maps an absent optional text column to SQL NULL.
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// nullableTermination maps an absent termination to SQL NULL, which is also the predicate
// Terminate conditions on.
func nullableTermination(termination *domain.AgentTermination) any {
	if termination == nil {
		return nil
	}
	return string(*termination)
}

var _ store.AgentRunRepository = (*agentRunRepository)(nil)
