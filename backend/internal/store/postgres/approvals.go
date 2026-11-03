package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type approvalRepository struct {
	conn pgx.Tx
}

const approvalColumns = `id, action_id, run_id, agent_run_id, status, requested_at, deadline,
	decided_at, decided_by, reason`

// Create inserts the PENDING approval in the claim transaction that suspended its Action.
// UNIQUE (action_id) is what makes a second request for the same Action lose.
func (r *approvalRepository) Create(ctx context.Context, approval domain.Approval) error {
	if !approval.Status.IsValid() {
		return fmt.Errorf("store/postgres approvals.Create: approval=%s: unknown status %q", approval.ID, approval.Status)
	}
	const insert = `
		INSERT INTO approvals (` + approvalColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	if _, err := r.conn.Exec(ctx, insert,
		approval.ID, approval.ActionID, approval.RunID, approval.AgentRunID, string(approval.Status),
		approval.RequestedAt, approval.Deadline, approval.DecidedAt, approval.DecidedBy, approval.Reason,
	); err != nil {
		return mapError("approvals.Create", err, approval.ID, approval.ActionID)
	}
	return nil
}

func (r *approvalRepository) Get(ctx context.Context, approvalID string) (domain.Approval, error) {
	const query = `SELECT ` + approvalColumns + ` FROM approvals WHERE id = $1`
	return r.queryOne(ctx, "approvals.Get", query, approvalID)
}

func (r *approvalRepository) GetByActionID(ctx context.Context, actionID string) (domain.Approval, error) {
	const query = `SELECT ` + approvalColumns + ` FROM approvals WHERE action_id = $1`
	return r.queryOne(ctx, "approvals.GetByActionID", query, actionID)
}

// ListByStatus returns approvals in one status, oldest request first.
func (r *approvalRepository) ListByStatus(ctx context.Context, status domain.ApprovalStatus, limit int) ([]domain.Approval, error) {
	if !status.IsValid() {
		return nil, fmt.Errorf("store/postgres approvals.ListByStatus: unknown status %q", status)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres approvals.ListByStatus: limit must be positive, got %d", limit)
	}
	const query = `SELECT ` + approvalColumns + `
		  FROM approvals
		 WHERE status = $1
		 ORDER BY requested_at ASC, id ASC
		 LIMIT $2`
	rows, err := r.conn.Query(ctx, query, string(status), limit)
	if err != nil {
		return nil, mapError("approvals.ListByStatus", err)
	}
	approvals, err := scanApprovals(rows)
	if err != nil {
		return nil, mapError("approvals.ListByStatus", err)
	}
	return approvals, nil
}

// ListByAgentRun returns the approvals one Agent Run requested, oldest request first.
func (r *approvalRepository) ListByAgentRun(ctx context.Context, agentRunID string, limit int) ([]domain.Approval, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres approvals.ListByAgentRun: limit must be positive, got %d", limit)
	}
	const query = `SELECT ` + approvalColumns + `
		  FROM approvals
		 WHERE agent_run_id = $1
		 ORDER BY requested_at ASC, id ASC
		 LIMIT $2`
	rows, err := r.conn.Query(ctx, query, agentRunID, limit)
	if err != nil {
		return nil, mapError("approvals.ListByAgentRun", err, agentRunID)
	}
	approvals, err := scanApprovals(rows)
	if err != nil {
		return nil, mapError("approvals.ListByAgentRun", err, agentRunID)
	}
	return approvals, nil
}

// ListExpiredPending discovers the expiry work through the partial pending-deadline index.
// It claims nothing; the expiry use case settles each row by its own conditional update.
func (r *approvalRepository) ListExpiredPending(ctx context.Context, now time.Time, limit int) ([]domain.Approval, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres approvals.ListExpiredPending: limit must be positive, got %d", limit)
	}
	const query = `SELECT ` + approvalColumns + `
		  FROM approvals
		 WHERE status = 'PENDING' AND deadline <= $1
		 ORDER BY deadline ASC, id ASC
		 LIMIT $2`
	rows, err := r.conn.Query(ctx, query, now, limit)
	if err != nil {
		return nil, mapError("approvals.ListExpiredPending", err)
	}
	approvals, err := scanApprovals(rows)
	if err != nil {
		return nil, mapError("approvals.ListExpiredPending", err)
	}
	return approvals, nil
}

// Decide settles a PENDING approval once. `status = 'PENDING'` is the single arbiter
// between an approval, a rejection and the expiry racing for the same row: the first to
// commit wins, every later one affects no row and writes nothing.
func (r *approvalRepository) Decide(ctx context.Context, approvalID string, status domain.ApprovalStatus, now time.Time, decidedBy, reason *string) (bool, error) {
	if !status.IsValid() || !status.IsDecided() {
		return false, fmt.Errorf("store/postgres approvals.Decide: approval=%s: %q is not a terminal approval status", approvalID, status)
	}
	const update = `
		UPDATE approvals
		   SET status = $2, decided_at = $3, decided_by = $4, reason = $5
		 WHERE id = $1 AND status = 'PENDING'`
	affected, err := affectedRows(ctx, r.conn, "approvals.Decide", update, approvalID, string(status), now, nullableString(decidedBy), nullableString(reason))
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("approvals.Decide", approvalID, affected)
	}
	return affected == 1, nil
}

func (r *approvalRepository) queryOne(ctx context.Context, op, query string, arg any) (domain.Approval, error) {
	rows, err := r.conn.Query(ctx, query, arg)
	if err != nil {
		return domain.Approval{}, mapError(op, err, arg)
	}
	approvals, err := scanApprovals(rows)
	if err != nil {
		return domain.Approval{}, mapError(op, err, arg)
	}
	if len(approvals) == 0 {
		return domain.Approval{}, mapError(op, pgx.ErrNoRows, arg)
	}
	return approvals[0], nil
}

func scanApprovals(rows pgx.Rows) ([]domain.Approval, error) {
	defer rows.Close()

	out := []domain.Approval{}
	for rows.Next() {
		var (
			approval domain.Approval
			status   string
		)
		if err := rows.Scan(
			&approval.ID, &approval.ActionID, &approval.RunID, &approval.AgentRunID, &status,
			&approval.RequestedAt, &approval.Deadline, &approval.DecidedAt, &approval.DecidedBy, &approval.Reason,
		); err != nil {
			return nil, err
		}
		approval.Status = domain.ApprovalStatus(status)
		out = append(out, approval)
	}
	return out, rows.Err()
}

var _ store.ApprovalRepository = (*approvalRepository)(nil)
