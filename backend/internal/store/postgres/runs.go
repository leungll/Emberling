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

type runRepository struct {
	conn pgx.Tx
}

func (r *runRepository) Create(ctx context.Context, run domain.Run) error {
	errPayload, err := marshalExecutionError(run.Error)
	if err != nil {
		return fmt.Errorf("store/postgres runs.Create: run=%s: %w", run.ID, err)
	}

	const insert = `
		INSERT INTO runs (id, workflow_id, definition_version, status, input, output, error,
		                  last_seq, started_at, completed_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	if _, err := r.conn.Exec(ctx, insert,
		run.ID, run.WorkflowID, run.DefinitionVersion, string(run.Status),
		run.Input, nullableJSON(run.Output), errPayload,
		run.LastSeq, run.StartedAt, run.CompletedAt, run.UpdatedAt,
	); err != nil {
		return mapError("runs.Create", err, run.ID)
	}
	return nil
}

func (r *runRepository) Get(ctx context.Context, runID string) (domain.Run, error) {
	const query = `
		SELECT id, workflow_id, definition_version, status, input, output, error,
		       last_seq, started_at, completed_at, updated_at
		  FROM runs WHERE id = $1`

	var (
		run        domain.Run
		status     string
		output     []byte
		errPayload []byte
	)
	err := r.conn.QueryRow(ctx, query, runID).Scan(
		&run.ID, &run.WorkflowID, &run.DefinitionVersion, &status, &run.Input, &output, &errPayload,
		&run.LastSeq, &run.StartedAt, &run.CompletedAt, &run.UpdatedAt)
	if err != nil {
		return domain.Run{}, mapError("runs.Get", err, runID)
	}

	run.Status = domain.RunStatus(status)
	run.Output = json.RawMessage(output)
	if run.Error, err = unmarshalExecutionError(errPayload); err != nil {
		return domain.Run{}, fmt.Errorf("store/postgres runs.Get: run=%s: %w", runID, err)
	}
	return run, nil
}

// LockForUpdate takes the Run aggregate lock. Every transaction that changes execution
// facts of this Run holds it before touching a NodeRun, an Attempt or Agent facts, and
// before allocating any Event seq. The caller must not invoke external code while it is
// held.
func (r *runRepository) LockForUpdate(ctx context.Context, runID string) (*store.RunLock, error) {
	const query = `SELECT id, status, last_seq FROM runs WHERE id = $1 FOR UPDATE`

	var (
		id      string
		status  string
		lastSeq int64
	)
	if err := r.conn.QueryRow(ctx, query, runID).Scan(&id, &status, &lastSeq); err != nil {
		return nil, mapError("runs.LockForUpdate", err, runID)
	}
	return store.NewRunLock(id, domain.RunStatus(status), lastSeq), nil
}

// UpdateAggregate writes the recomputed Run status and the seq watermark reached under
// the lock back to the Run row. completed_at is set the first time status becomes
// terminal (COMPLETED or FAILED) and is never moved by a later call, so a duplicated
// advancement path cannot slide the Run's terminal timestamp forward. A Run that has
// already reached a terminal status cannot be moved back to a non-terminal one: the
// WHERE clause's (completed_at IS NULL OR $4) guard rejects that write instead of
// silently resurrecting a finished Run.
func (r *runRepository) UpdateAggregate(ctx context.Context, lock *store.RunLock, status domain.RunStatus, now time.Time) error {
	if !lock.Held() {
		return fmt.Errorf("store/postgres runs.UpdateAggregate: %w", errNoRunLock)
	}
	if !status.IsValid() {
		return fmt.Errorf("store/postgres runs.UpdateAggregate: run=%s: unknown status %q", lock.RunID(), status)
	}

	const update = `
		UPDATE runs
		   SET status       = $2,
		       last_seq     = $3,
		       completed_at = CASE WHEN completed_at IS NULL AND $4 THEN $5 ELSE completed_at END,
		       updated_at   = $5
		 WHERE id = $1
		   AND (completed_at IS NULL OR $4)`
	affected, err := affectedRows(ctx, r.conn, "runs.UpdateAggregate", update,
		lock.RunID(), string(status), lock.LastSeq(), status.IsTerminal(), now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		// LockForUpdate already proved this row exists and holds it for the duration of
		// the transaction, so a 0-row result here can only mean the guard above rejected
		// a non-terminal status write onto an already-terminal Run.
		return &domain.InvalidStateTransitionError{
			Entity: "run",
			ID:     lock.RunID(),
			From:   string(lock.Status()),
			To:     string(status),
		}
	case affected > 1:
		return errUnexpectedRows("runs.UpdateAggregate", lock.RunID(), affected)
	}
	return nil
}

// SetOutput writes the Run's output exactly once. output IS NULL is the guard: the
// Output Node's NodeRun output is the single source, and a second call (a duplicated
// advancement path, or a bug upstream) must not silently overwrite it.
func (r *runRepository) SetOutput(ctx context.Context, lock *store.RunLock, output json.RawMessage) error {
	if !lock.Held() {
		return fmt.Errorf("store/postgres runs.SetOutput: %w", errNoRunLock)
	}

	const update = `
		UPDATE runs SET output = $2 WHERE id = $1 AND output IS NULL`
	affected, err := affectedRows(ctx, r.conn, "runs.SetOutput", update, lock.RunID(), nullableJSON(output))
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres runs.SetOutput: run=%s: %w", lock.RunID(), domain.ErrConflict)
	case affected > 1:
		return errUnexpectedRows("runs.SetOutput", lock.RunID(), affected)
	}
	return nil
}

// SetError records the failure summary for a Run entering FAILED.
func (r *runRepository) SetError(ctx context.Context, lock *store.RunLock, execErr domain.ExecutionError) error {
	if !lock.Held() {
		return fmt.Errorf("store/postgres runs.SetError: %w", errNoRunLock)
	}
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return fmt.Errorf("store/postgres runs.SetError: run=%s: %w", lock.RunID(), err)
	}

	const update = `UPDATE runs SET error = $2 WHERE id = $1`
	affected, err := affectedRows(ctx, r.conn, "runs.SetError", update, lock.RunID(), errPayload)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return mapError("runs.SetError", pgx.ErrNoRows, lock.RunID())
	case affected > 1:
		return errUnexpectedRows("runs.SetError", lock.RunID(), affected)
	}
	return nil
}

// LatestByWorkflow returns the most recently started Run of a Workflow, using the same
// (workflow_id, started_at DESC) index the Snapshot read path relies on. It returns nil,
// not an error, when the Workflow has never been run.
func (r *runRepository) LatestByWorkflow(ctx context.Context, workflowID string) (*domain.Run, error) {
	const query = `
		SELECT id, workflow_id, definition_version, status, input, output, error,
		       last_seq, started_at, completed_at, updated_at
		  FROM runs
		 WHERE workflow_id = $1
		 ORDER BY started_at DESC
		 LIMIT 1`

	var (
		run        domain.Run
		status     string
		output     []byte
		errPayload []byte
	)
	err := r.conn.QueryRow(ctx, query, workflowID).Scan(
		&run.ID, &run.WorkflowID, &run.DefinitionVersion, &status, &run.Input, &output, &errPayload,
		&run.LastSeq, &run.StartedAt, &run.CompletedAt, &run.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapError("runs.LatestByWorkflow", err, workflowID)
	}

	run.Status = domain.RunStatus(status)
	run.Output = json.RawMessage(output)
	if run.Error, err = unmarshalExecutionError(errPayload); err != nil {
		return nil, fmt.Errorf("store/postgres runs.LatestByWorkflow: workflow=%s: %w", workflowID, err)
	}
	return &run, nil
}

var _ store.RunRepository = (*runRepository)(nil)
