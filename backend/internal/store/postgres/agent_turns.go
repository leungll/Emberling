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

type agentTurnRepository struct {
	conn pgx.Tx
}

const agentTurnColumns = `id, agent_run_id, turn_no, status, request, response, token_usage,
	started_at, completed_at, error`

// Create inserts a Turn. UNIQUE (agent_run_id, turn_no) is what keeps immediate
// advancement, a callback and the Reconciler from creating a duplicate round.
func (r *agentTurnRepository) Create(ctx context.Context, turn domain.AgentTurn) error {
	tokenUsage, err := marshalTokenUsage(turn.TokenUsage)
	if err != nil {
		return fmt.Errorf("store/postgres agent_turns.Create: turn=%s: %w", turn.ID, err)
	}
	errPayload, err := marshalExecutionError(turn.Error)
	if err != nil {
		return fmt.Errorf("store/postgres agent_turns.Create: turn=%s: %w", turn.ID, err)
	}

	const insert = `
		INSERT INTO agent_turns (` + agentTurnColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	if _, err := r.conn.Exec(ctx, insert,
		turn.ID, turn.AgentRunID, turn.TurnNo, string(turn.Status),
		turn.Request, nullableJSON(turn.Response), tokenUsage,
		turn.StartedAt, turn.CompletedAt, errPayload,
	); err != nil {
		return mapError("agent_turns.Create", err, turn.ID, turn.AgentRunID, turn.TurnNo)
	}
	return nil
}

func (r *agentTurnRepository) Get(ctx context.Context, turnID string) (domain.AgentTurn, error) {
	const query = `SELECT ` + agentTurnColumns + ` FROM agent_turns WHERE id = $1`

	rows, err := r.conn.Query(ctx, query, turnID)
	if err != nil {
		return domain.AgentTurn{}, mapError("agent_turns.Get", err, turnID)
	}
	turns, err := scanAgentTurns(rows)
	if err != nil {
		return domain.AgentTurn{}, mapError("agent_turns.Get", err, turnID)
	}
	if len(turns) == 0 {
		return domain.AgentTurn{}, mapError("agent_turns.Get", pgx.ErrNoRows, turnID)
	}
	return turns[0], nil
}

// ListByAgentRunID returns one Agent Run's Turns in persisted order. turn_no is unique
// per Agent Run and is allocated in the transaction that creates the round, so ordering by
// it is the persisted order of the Agent Loop, independent of when a Turn later started or
// completed. The result is bounded by the frozen max_turns of the Agent Run.
func (r *agentTurnRepository) ListByAgentRunID(ctx context.Context, agentRunID string) ([]domain.AgentTurn, error) {
	const query = `SELECT ` + agentTurnColumns + `
		  FROM agent_turns
		 WHERE agent_run_id = $1
		 ORDER BY turn_no ASC`

	rows, err := r.conn.Query(ctx, query, agentRunID)
	if err != nil {
		return nil, mapError("agent_turns.ListByAgentRunID", err, agentRunID)
	}
	turns, err := scanAgentTurns(rows)
	if err != nil {
		return nil, mapError("agent_turns.ListByAgentRunID", err, agentRunID)
	}
	return turns, nil
}

// GetByRunAndTurnNo resolves the Turn an Agent Run's current_turn_no pointer names. The
// Agent timeout transaction enters the Agent Loop from the Agent Run rather than from a
// Turn, so this is how it finds the round the deadline has to end.
func (r *agentTurnRepository) GetByRunAndTurnNo(ctx context.Context, agentRunID string, turnNo int) (domain.AgentTurn, error) {
	const query = `SELECT ` + agentTurnColumns + `
		  FROM agent_turns
		 WHERE agent_run_id = $1 AND turn_no = $2`

	rows, err := r.conn.Query(ctx, query, agentRunID, turnNo)
	if err != nil {
		return domain.AgentTurn{}, mapError("agent_turns.GetByRunAndTurnNo", err, agentRunID, turnNo)
	}
	turns, err := scanAgentTurns(rows)
	if err != nil {
		return domain.AgentTurn{}, mapError("agent_turns.GetByRunAndTurnNo", err, agentRunID, turnNo)
	}
	if len(turns) == 0 {
		return domain.AgentTurn{}, mapError("agent_turns.GetByRunAndTurnNo", pgx.ErrNoRows, agentRunID, turnNo)
	}
	return turns[0], nil
}

// ClaimReady conditionally moves READY to RUNNING and stamps started_at (06 §1.7). The
// single caller whose UPDATE affected one row owns the model call, and only after this
// transaction commits; everybody else must stop without re-issuing the request.
func (r *agentTurnRepository) ClaimReady(ctx context.Context, turnID string, now time.Time) (bool, error) {
	const claim = `
		UPDATE agent_turns
		   SET status = 'RUNNING', started_at = $2
		 WHERE id = $1 AND status = 'READY'`

	affected, err := affectedRows(ctx, r.conn, "agent_turns.ClaimReady", claim, turnID, now)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("agent_turns.ClaimReady", turnID, affected)
	}
	return affected == 1, nil
}

// ListReady returns persisted READY Turns for Reconciler rediscovery (06 §2.1). RUNNING
// Turns are excluded on purpose: the MVP never re-issues a model request that may still
// be in flight; such a Turn ends through the Agent deadline instead.
func (r *agentTurnRepository) ListReady(ctx context.Context, limit int) ([]domain.AgentTurn, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres agent_turns.ListReady: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + agentTurnColumns + `
		  FROM agent_turns
		 WHERE status = 'READY'
		 ORDER BY id ASC
		 LIMIT $1`

	rows, err := r.conn.Query(ctx, query, limit)
	if err != nil {
		return nil, mapError("agent_turns.ListReady", err)
	}
	turns, err := scanAgentTurns(rows)
	if err != nil {
		return nil, mapError("agent_turns.ListReady", err)
	}
	return turns, nil
}

// MarkCompleted conditionally moves a RUNNING Turn to COMPLETED and writes the model
// response and token usage. Requiring RUNNING means only the transaction that won the
// model call can record its result.
func (r *agentTurnRepository) MarkCompleted(ctx context.Context, turnID string, now time.Time, response json.RawMessage, usage *domain.TokenUsage) error {
	tokenUsage, err := marshalTokenUsage(usage)
	if err != nil {
		return fmt.Errorf("store/postgres agent_turns.MarkCompleted: turn=%s: %w", turnID, err)
	}

	const update = `
		UPDATE agent_turns
		   SET status = 'COMPLETED', response = $2, token_usage = $3, completed_at = $4
		 WHERE id = $1 AND status = 'RUNNING'`
	affected, err := affectedRows(ctx, r.conn, "agent_turns.MarkCompleted", update,
		turnID, nullableJSON(response), tokenUsage, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres agent_turns.MarkCompleted: turn=%s RUNNING -> COMPLETED: %w",
			turnID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("agent_turns.MarkCompleted", turnID, affected)
	}
	return nil
}

// MarkFailed conditionally moves a RUNNING Turn to FAILED. A failed Turn carries no
// Decision and no Action; the Agent Run terminates with MODEL_ERROR or TIMEOUT.
func (r *agentTurnRepository) MarkFailed(ctx context.Context, turnID string, now time.Time, execErr domain.ExecutionError) error {
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return fmt.Errorf("store/postgres agent_turns.MarkFailed: turn=%s: %w", turnID, err)
	}

	const update = `
		UPDATE agent_turns
		   SET status = 'FAILED', error = $2, completed_at = $3
		 WHERE id = $1 AND status = 'RUNNING'`
	affected, err := affectedRows(ctx, r.conn, "agent_turns.MarkFailed", update, turnID, errPayload, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres agent_turns.MarkFailed: turn=%s RUNNING -> FAILED: %w",
			turnID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("agent_turns.MarkFailed", turnID, affected)
	}
	return nil
}

// MarkTimedOut conditionally fails a Turn that is still READY or RUNNING. The wider
// predicate belongs to the Agent timeout transaction alone: an expired deadline ends the
// current round whether the model call was ever claimed (RUNNING) or the round was only
// ever persisted as recoverable work (READY). A row that is already COMPLETED or FAILED
// affects nothing and reports false, so a Decision that committed first keeps its Turn.
func (r *agentTurnRepository) MarkTimedOut(ctx context.Context, turnID string, now time.Time, execErr domain.ExecutionError) (bool, error) {
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return false, fmt.Errorf("store/postgres agent_turns.MarkTimedOut: turn=%s: %w", turnID, err)
	}

	const update = `
		UPDATE agent_turns
		   SET status = 'FAILED', error = $2, completed_at = $3
		 WHERE id = $1 AND status IN ('READY', 'RUNNING')`
	affected, err := affectedRows(ctx, r.conn, "agent_turns.MarkTimedOut", update, turnID, errPayload, now)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("agent_turns.MarkTimedOut", turnID, affected)
	}
	return affected == 1, nil
}

func scanAgentTurns(rows pgx.Rows) ([]domain.AgentTurn, error) {
	defer rows.Close()

	out := []domain.AgentTurn{}
	for rows.Next() {
		var (
			turn       domain.AgentTurn
			status     string
			response   []byte
			tokenUsage []byte
			errPayload []byte
		)
		if err := rows.Scan(
			&turn.ID, &turn.AgentRunID, &turn.TurnNo, &status,
			&turn.Request, &response, &tokenUsage,
			&turn.StartedAt, &turn.CompletedAt, &errPayload,
		); err != nil {
			return nil, err
		}

		turn.Status = domain.AgentTurnStatus(status)
		turn.Response = json.RawMessage(response)

		var err error
		if turn.TokenUsage, err = unmarshalTokenUsage(tokenUsage); err != nil {
			return nil, fmt.Errorf("turn=%s: %w", turn.ID, err)
		}
		if turn.Error, err = unmarshalExecutionError(errPayload); err != nil {
			return nil, fmt.Errorf("turn=%s: %w", turn.ID, err)
		}
		out = append(out, turn)
	}
	return out, rows.Err()
}

var _ store.AgentTurnRepository = (*agentTurnRepository)(nil)
