package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type agentActionRepository struct {
	conn pgx.Tx
}

const agentActionColumns = `id, turn_id, decision_id, type, status, created_at, started_at,
	waiting_at, completed_at, error`

// Create inserts the READY Action in the same transaction as its Decision. UNIQUE
// (turn_id) and UNIQUE (decision_id) allow at most one Action per Turn and per Decision,
// so a second advancement path cannot create a competing work item.
func (r *agentActionRepository) Create(ctx context.Context, action domain.AgentAction) error {
	errPayload, err := marshalExecutionError(action.Error)
	if err != nil {
		return fmt.Errorf("store/postgres agent_actions.Create: action=%s: %w", action.ID, err)
	}

	const insert = `
		INSERT INTO agent_actions (` + agentActionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	if _, err := r.conn.Exec(ctx, insert,
		action.ID, action.TurnID, action.DecisionID, string(action.Type), string(action.Status),
		action.CreatedAt, action.StartedAt, action.WaitingAt, action.CompletedAt, errPayload,
	); err != nil {
		return mapError("agent_actions.Create", err, action.ID, action.TurnID, action.DecisionID)
	}
	return nil
}

func (r *agentActionRepository) Get(ctx context.Context, actionID string) (domain.AgentAction, error) {
	const query = `SELECT ` + agentActionColumns + ` FROM agent_actions WHERE id = $1`
	return r.queryOne(ctx, "agent_actions.Get", query, actionID)
}

func (r *agentActionRepository) GetByTurnID(ctx context.Context, turnID string) (domain.AgentAction, error) {
	const query = `SELECT ` + agentActionColumns + ` FROM agent_actions WHERE turn_id = $1`
	return r.queryOne(ctx, "agent_actions.GetByTurnID", query, turnID)
}

// ClaimReady conditionally moves READY to RUNNING (06 §1.7) and stamps started_at, which
// 05 §1.8 keeps empty until an executor takes the Action. The winner owns the execution
// right: a TOOL_CALL calls the Tool only after this transaction commits, and a FINAL is
// closed out inside the same transaction.
func (r *agentActionRepository) ClaimReady(ctx context.Context, actionID string, now time.Time) (bool, error) {
	const claim = `
		UPDATE agent_actions
		   SET status = 'RUNNING', started_at = $2
		 WHERE id = $1 AND status = 'READY'`

	affected, err := affectedRows(ctx, r.conn, "agent_actions.ClaimReady", claim, actionID, now)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("agent_actions.ClaimReady", actionID, affected)
	}
	return affected == 1, nil
}

// ListReady returns persisted READY Actions for Reconciler rediscovery (06 §2.1). The
// Reconciler advances the original Action; it never re-requests the model or creates a
// second Decision.
func (r *agentActionRepository) ListReady(ctx context.Context, limit int) ([]domain.AgentAction, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres agent_actions.ListReady: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + agentActionColumns + `
		  FROM agent_actions
		 WHERE status = 'READY'
		 ORDER BY id ASC
		 LIMIT $1`

	rows, err := r.conn.Query(ctx, query, limit)
	if err != nil {
		return nil, mapError("agent_actions.ListReady", err)
	}
	actions, err := scanAgentActions(rows)
	if err != nil {
		return nil, mapError("agent_actions.ListReady", err)
	}
	return actions, nil
}

// MarkWaiting conditionally moves a RUNNING Action to WAITING_CALLBACK once its ASYNC Tool
// has dispatched. Requiring RUNNING ties the move to the holder of the execution right.
func (r *agentActionRepository) MarkWaiting(ctx context.Context, actionID string, now time.Time) error {
	const update = `
		UPDATE agent_actions
		   SET status = 'WAITING_CALLBACK', waiting_at = $2
		 WHERE id = $1 AND status = 'RUNNING'`

	affected, err := affectedRows(ctx, r.conn, "agent_actions.MarkWaiting", update, actionID, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres agent_actions.MarkWaiting: action=%s RUNNING -> WAITING_CALLBACK: %w",
			actionID, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("agent_actions.MarkWaiting", actionID, affected)
	}
	return nil
}

// MarkSucceeded conditionally moves an Action from the given status to SUCCEEDED: RUNNING
// for the caller that holds the execution right of a synchronous call, WAITING_CALLBACK
// for the resume of an asynchronous one. Requiring the expected status is what ties
// completion to the single winner.
func (r *agentActionRepository) MarkSucceeded(ctx context.Context, actionID string, from domain.AgentActionStatus, now time.Time) error {
	if !from.CanTransitionTo(domain.AgentActionSucceeded) {
		return &domain.InvalidStateTransitionError{
			Entity: "AgentAction", ID: actionID, From: string(from), To: string(domain.AgentActionSucceeded),
		}
	}

	const update = `
		UPDATE agent_actions
		   SET status = 'SUCCEEDED', completed_at = $3
		 WHERE id = $1 AND status = $2`

	affected, err := affectedRows(ctx, r.conn, "agent_actions.MarkSucceeded", update, actionID, string(from), now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres agent_actions.MarkSucceeded: action=%s %s -> SUCCEEDED: %w",
			actionID, from, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("agent_actions.MarkSucceeded", actionID, affected)
	}
	return nil
}

// MarkFailed conditionally moves a RUNNING or WAITING_CALLBACK Action to FAILED and
// records the failure the Agent Run terminates on. READY -> FAILED is reserved for the
// Agent timeout transaction (MarkTimedOut), so it is rejected here as well.
func (r *agentActionRepository) MarkFailed(ctx context.Context, actionID string, from domain.AgentActionStatus, now time.Time, execErr domain.ExecutionError) error {
	if from == domain.AgentActionReady || !from.CanTransitionTo(domain.AgentActionFailed) {
		return &domain.InvalidStateTransitionError{
			Entity: "AgentAction", ID: actionID, From: string(from), To: string(domain.AgentActionFailed),
		}
	}
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return fmt.Errorf("store/postgres agent_actions.MarkFailed: action=%s: %w", actionID, err)
	}

	const update = `
		UPDATE agent_actions
		   SET status = 'FAILED', error = $3, completed_at = $4
		 WHERE id = $1 AND status = $2`
	affected, err := affectedRows(ctx, r.conn, "agent_actions.MarkFailed", update,
		actionID, string(from), errPayload, now)
	if err != nil {
		return err
	}
	switch {
	case affected == 0:
		return fmt.Errorf("store/postgres agent_actions.MarkFailed: action=%s %s -> FAILED: %w",
			actionID, from, domain.ErrStaleClaim)
	case affected > 1:
		return errUnexpectedRows("agent_actions.MarkFailed", actionID, affected)
	}
	return nil
}

// MarkTimedOut conditionally fails an Action that is still READY, RUNNING or
// WAITING_CALLBACK. Only the Agent timeout transaction may use it: the expired deadline
// ends the current Action in whichever non-terminal state it sits, including the READY one
// no executor ever claimed, which no other failure path is allowed to touch. An Action
// that already succeeded or failed affects nothing and reports false.
func (r *agentActionRepository) MarkTimedOut(ctx context.Context, actionID string, now time.Time, execErr domain.ExecutionError) (bool, error) {
	errPayload, err := marshalExecutionError(&execErr)
	if err != nil {
		return false, fmt.Errorf("store/postgres agent_actions.MarkTimedOut: action=%s: %w", actionID, err)
	}

	const update = `
		UPDATE agent_actions
		   SET status = 'FAILED', error = $2, completed_at = $3
		 WHERE id = $1 AND status IN ('READY', 'RUNNING', 'WAITING_CALLBACK')`
	affected, err := affectedRows(ctx, r.conn, "agent_actions.MarkTimedOut", update, actionID, errPayload, now)
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("agent_actions.MarkTimedOut", actionID, affected)
	}
	return affected == 1, nil
}

func (r *agentActionRepository) queryOne(ctx context.Context, op, query string, arg any) (domain.AgentAction, error) {
	rows, err := r.conn.Query(ctx, query, arg)
	if err != nil {
		return domain.AgentAction{}, mapError(op, err, arg)
	}
	actions, err := scanAgentActions(rows)
	if err != nil {
		return domain.AgentAction{}, mapError(op, err, arg)
	}
	if len(actions) == 0 {
		return domain.AgentAction{}, mapError(op, pgx.ErrNoRows, arg)
	}
	return actions[0], nil
}

func scanAgentActions(rows pgx.Rows) ([]domain.AgentAction, error) {
	defer rows.Close()

	out := []domain.AgentAction{}
	for rows.Next() {
		var (
			action     domain.AgentAction
			actionType string
			status     string
			errPayload []byte
		)
		if err := rows.Scan(
			&action.ID, &action.TurnID, &action.DecisionID, &actionType, &status,
			&action.CreatedAt, &action.StartedAt, &action.WaitingAt, &action.CompletedAt, &errPayload,
		); err != nil {
			return nil, err
		}

		action.Type = domain.AgentActionType(actionType)
		action.Status = domain.AgentActionStatus(status)

		var err error
		if action.Error, err = unmarshalExecutionError(errPayload); err != nil {
			return nil, fmt.Errorf("action=%s: %w", action.ID, err)
		}
		out = append(out, action)
	}
	return out, rows.Err()
}

var _ store.AgentActionRepository = (*agentActionRepository)(nil)
