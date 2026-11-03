package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type humanWaitRepository struct {
	conn pgx.Tx
}

const humanWaitColumns = `id, action_id, run_id, status, opened_at, deadline, closed_at, closed_by, outcome`

// Create inserts the OPEN wait in the transaction that moved its Action to WAITING_HUMAN.
// UNIQUE (action_id) makes a second wait for the same Action lose in the database.
func (r *humanWaitRepository) Create(ctx context.Context, wait domain.HumanWait) error {
	if wait.Status != domain.HumanWaitOpen {
		return fmt.Errorf("store/postgres human_waits.Create: human_wait=%s: status must be OPEN, got %q", wait.ID, wait.Status)
	}
	const insert = `
		INSERT INTO human_waits (` + humanWaitColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	if _, err := r.conn.Exec(ctx, insert,
		wait.ID, wait.ActionID, wait.RunID, string(wait.Status), wait.OpenedAt, wait.Deadline,
		wait.ClosedAt, wait.ClosedBy, wait.Outcome,
	); err != nil {
		return mapError("human_waits.Create", err, wait.ID, wait.ActionID)
	}
	return nil
}

func (r *humanWaitRepository) Get(ctx context.Context, id string) (domain.HumanWait, error) {
	const query = `SELECT ` + humanWaitColumns + ` FROM human_waits WHERE id = $1`
	return r.queryOne(ctx, "human_waits.Get", query, id)
}

func (r *humanWaitRepository) GetByActionID(ctx context.Context, actionID string) (domain.HumanWait, error) {
	const query = `SELECT ` + humanWaitColumns + ` FROM human_waits WHERE action_id = $1`
	return r.queryOne(ctx, "human_waits.GetByActionID", query, actionID)
}

// Close conditionally settles an OPEN wait with a terminal status. `status = 'OPEN'`
// decides the single winner between a production answer, a human stop and the wait's own
// expiry; a false result means another closing committed and nothing was written.
func (r *humanWaitRepository) Close(ctx context.Context, id string, status domain.HumanWaitStatus, now time.Time, closedBy, outcome *string) (bool, error) {
	if !status.IsValid() || status == domain.HumanWaitOpen {
		return false, fmt.Errorf("store/postgres human_waits.Close: human_wait=%s: %q is not a terminal human wait status", id, status)
	}
	const update = `
		UPDATE human_waits
		   SET status = $2, closed_at = $3, closed_by = $4, outcome = $5
		 WHERE id = $1 AND status = 'OPEN'`
	affected, err := affectedRows(ctx, r.conn, "human_waits.Close", update, id, string(status), now, nullableString(closedBy), nullableString(outcome))
	if err != nil {
		return false, err
	}
	if affected > 1 {
		return false, errUnexpectedRows("human_waits.Close", id, affected)
	}
	return affected == 1, nil
}

func (r *humanWaitRepository) queryOne(ctx context.Context, op, query string, arg any) (domain.HumanWait, error) {
	rows, err := r.conn.Query(ctx, query, arg)
	if err != nil {
		return domain.HumanWait{}, mapError(op, err, arg)
	}
	waits, err := scanHumanWaits(rows)
	if err != nil {
		return domain.HumanWait{}, mapError(op, err, arg)
	}
	if len(waits) == 0 {
		return domain.HumanWait{}, mapError(op, pgx.ErrNoRows, arg)
	}
	return waits[0], nil
}

func scanHumanWaits(rows pgx.Rows) ([]domain.HumanWait, error) {
	defer rows.Close()

	out := []domain.HumanWait{}
	for rows.Next() {
		var (
			wait   domain.HumanWait
			status string
		)
		if err := rows.Scan(
			&wait.ID, &wait.ActionID, &wait.RunID, &status, &wait.OpenedAt, &wait.Deadline,
			&wait.ClosedAt, &wait.ClosedBy, &wait.Outcome,
		); err != nil {
			return nil, err
		}
		wait.Status = domain.HumanWaitStatus(status)
		out = append(out, wait)
	}
	return out, rows.Err()
}

var _ store.HumanWaitRepository = (*humanWaitRepository)(nil)
