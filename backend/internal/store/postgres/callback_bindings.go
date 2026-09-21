package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type callbackBindingRepository struct {
	conn pgx.Tx
}

const callbackBindingColumns = `id, provider_id, external_task_id, target_type, target_id, created_at`

// Create inserts a binding. UNIQUE (external_task_id) is the database-level guarantee
// that one external identity can only ever route to one Attempt, so a duplicate dispatch
// is rejected here rather than silently re-pointing a live route.
func (r *callbackBindingRepository) Create(ctx context.Context, binding domain.CallbackBinding) error {
	const insert = `
		INSERT INTO callback_bindings (` + callbackBindingColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6)`

	if _, err := r.conn.Exec(ctx, insert,
		binding.ID, binding.ProviderID, binding.ExternalTaskID,
		string(binding.TargetType), binding.TargetID, binding.CreatedAt,
	); err != nil {
		return mapError("callback_bindings.Create", err, binding.ID, binding.ExternalTaskID)
	}
	return nil
}

// GetByExternalTaskID resolves the callback target. domain.ErrNotFound here means the
// dispatch transaction has not committed yet (or never will): the caller records a
// Pending Callback instead of inferring a target from ID shape or in-process state.
func (r *callbackBindingRepository) GetByExternalTaskID(ctx context.Context, externalTaskID string) (domain.CallbackBinding, error) {
	const query = `SELECT ` + callbackBindingColumns + `
		  FROM callback_bindings WHERE external_task_id = $1`

	rows, err := r.conn.Query(ctx, query, externalTaskID)
	if err != nil {
		return domain.CallbackBinding{}, mapError("callback_bindings.GetByExternalTaskID", err, externalTaskID)
	}
	bindings, err := scanCallbackBindings(rows)
	if err != nil {
		return domain.CallbackBinding{}, mapError("callback_bindings.GetByExternalTaskID", err, externalTaskID)
	}
	if len(bindings) == 0 {
		return domain.CallbackBinding{}, mapError("callback_bindings.GetByExternalTaskID", pgx.ErrNoRows, externalTaskID)
	}
	return bindings[0], nil
}

// ListByTargets returns the bindings of a bounded set of Attempts using the
// (target_type, target_id) index. The caller supplies the ids, so the read is bounded by
// the Attempts it already holds.
func (r *callbackBindingRepository) ListByTargets(ctx context.Context, targetType domain.CallbackTargetType, targetIDs []string) ([]domain.CallbackBinding, error) {
	if len(targetIDs) == 0 {
		return []domain.CallbackBinding{}, nil
	}

	const query = `SELECT ` + callbackBindingColumns + `
		  FROM callback_bindings
		 WHERE target_type = $1 AND target_id = ANY($2)
		 ORDER BY created_at ASC, id ASC`

	rows, err := r.conn.Query(ctx, query, string(targetType), targetIDs)
	if err != nil {
		return nil, mapError("callback_bindings.ListByTargets", err, targetType)
	}
	bindings, err := scanCallbackBindings(rows)
	if err != nil {
		return nil, mapError("callback_bindings.ListByTargets", err, targetType)
	}
	return bindings, nil
}

func scanCallbackBindings(rows pgx.Rows) ([]domain.CallbackBinding, error) {
	defer rows.Close()

	out := []domain.CallbackBinding{}
	for rows.Next() {
		var (
			binding    domain.CallbackBinding
			targetType string
		)
		if err := rows.Scan(
			&binding.ID, &binding.ProviderID, &binding.ExternalTaskID,
			&targetType, &binding.TargetID, &binding.CreatedAt,
		); err != nil {
			return nil, err
		}
		binding.TargetType = domain.CallbackTargetType(targetType)
		out = append(out, binding)
	}
	return out, rows.Err()
}

var _ store.CallbackBindingRepository = (*callbackBindingRepository)(nil)
