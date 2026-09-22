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

type pendingCallbackRepository struct {
	conn pgx.Tx
}

const pendingCallbackColumns = `external_task_id, payload, payload_hash, callback_token_hash,
	received_at, expires_at, consumed_at, duplicate_count`

// Record persists a first arrival or registers a duplicate delivery. The ON CONFLICT
// clause deliberately touches only duplicate_count and received_at: a Provider that
// re-delivers with a different body must not be able to replace the first payload that
// was already authenticated and may already have resumed an Execution (05 §1.7).
//
// duplicate_count is inserted as 0 and only ever incremented here, so a returned value
// above zero is exactly "this row already existed".
func (r *pendingCallbackRepository) Record(ctx context.Context, pending domain.PendingCallback) (bool, error) {
	const upsert = `
		INSERT INTO pending_callbacks (external_task_id, payload, payload_hash,
		                               callback_token_hash, received_at, expires_at, duplicate_count)
		VALUES ($1, $2, $3, $4, $5, $6, 0)
		ON CONFLICT (external_task_id) DO UPDATE
		   SET duplicate_count = pending_callbacks.duplicate_count + 1,
		       received_at     = EXCLUDED.received_at
		RETURNING duplicate_count`

	var duplicateCount int
	if err := r.conn.QueryRow(ctx, upsert,
		pending.ExternalTaskID, []byte(pending.Payload), pending.PayloadHash,
		pending.CallbackTokenHash, pending.ReceivedAt, pending.ExpiresAt,
	).Scan(&duplicateCount); err != nil {
		return false, mapError("pending_callbacks.Record", err, pending.ExternalTaskID)
	}
	return duplicateCount > 0, nil
}

func (r *pendingCallbackRepository) GetByExternalTaskID(ctx context.Context, externalTaskID string) (domain.PendingCallback, error) {
	const query = `SELECT ` + pendingCallbackColumns + `
		  FROM pending_callbacks WHERE external_task_id = $1`

	rows, err := r.conn.Query(ctx, query, externalTaskID)
	if err != nil {
		return domain.PendingCallback{}, mapError("pending_callbacks.GetByExternalTaskID", err, externalTaskID)
	}
	pendings, err := scanPendingCallbacks(rows)
	if err != nil {
		return domain.PendingCallback{}, mapError("pending_callbacks.GetByExternalTaskID", err, externalTaskID)
	}
	if len(pendings) == 0 {
		return domain.PendingCallback{}, mapError("pending_callbacks.GetByExternalTaskID", pgx.ErrNoRows, externalTaskID)
	}
	return pendings[0], nil
}

// ConsumeOnce conditionally claims the record. The predicate is the whole decision: the
// callback handler and the check that runs after WAITING_CALLBACK commits race here, and
// only the UPDATE that affects one row may enter resume. An expired record loses too, so
// a result past its retention window can never advance an Execution.
func (r *pendingCallbackRepository) ConsumeOnce(ctx context.Context, externalTaskID string, now time.Time) (domain.PendingCallback, bool, error) {
	const consume = `
		UPDATE pending_callbacks
		   SET consumed_at = $2
		 WHERE external_task_id = $1 AND consumed_at IS NULL AND expires_at > $2
		RETURNING ` + pendingCallbackColumns

	rows, err := r.conn.Query(ctx, consume, externalTaskID, now)
	if err != nil {
		return domain.PendingCallback{}, false, mapError("pending_callbacks.ConsumeOnce", err, externalTaskID)
	}
	pendings, err := scanPendingCallbacks(rows)
	if err != nil {
		return domain.PendingCallback{}, false, mapError("pending_callbacks.ConsumeOnce", err, externalTaskID)
	}
	switch {
	case len(pendings) == 0:
		return domain.PendingCallback{}, false, nil
	case len(pendings) > 1:
		return domain.PendingCallback{}, false,
			errUnexpectedRows("pending_callbacks.ConsumeOnce", externalTaskID, int64(len(pendings)))
	}
	return pendings[0], true, nil
}

// ListConsumableForWaiting joins a still-claimable record to the route its binding now
// resolves to. The join conditions are the ones that make consumption legitimate, one
// branch per Binding target type:
//
//   - NODE_ATTEMPT: the Node Attempt is still DISPATCHED and its NodeRun still
//     WAITING_CALLBACK.
//   - TOOL_ATTEMPT: the Tool Attempt is still DISPATCHED, its Agent Action still
//     WAITING_CALLBACK, and the Agent NodeRun that owns the Action still WAITING_CALLBACK.
//     NodeRunID is that Agent NodeRun and AttemptID the Tool Attempt.
//
// Both branches return the same row shape, so the Reconciler replays either through the
// one ResumeNode use case, which routes on the Binding's target type (06 §2.1, invariant
// #5). A record without a binding stays behind for audit only; it never advances an
// Execution (06 §4).
//
// Ordering by received_at keeps the oldest rediscovered callback first; external_task_id
// breaks ties so a bounded batch is stable across scans. The single outer LIMIT bounds the
// batch across both target types.
func (r *pendingCallbackRepository) ListConsumableForWaiting(ctx context.Context, now time.Time, limit int) ([]store.PendingForWaiting, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres pending_callbacks.ListConsumableForWaiting: limit must be positive, got %d", limit)
	}

	const query = `
		SELECT c.external_task_id, c.payload, c.payload_hash, c.callback_token_hash,
		       c.received_at, c.expires_at, c.consumed_at, c.duplicate_count,
		       c.binding_id, c.provider_id, c.binding_external_task_id, c.target_type,
		       c.target_id, c.binding_created_at, c.run_id, c.node_run_id, c.attempt_id
		  FROM (
		        SELECT p.external_task_id, p.payload, p.payload_hash, p.callback_token_hash,
		               p.received_at, p.expires_at, p.consumed_at, p.duplicate_count,
		               b.id AS binding_id, b.provider_id,
		               b.external_task_id AS binding_external_task_id, b.target_type,
		               b.target_id, b.created_at AS binding_created_at,
		               nr.run_id, nr.id AS node_run_id, a.id AS attempt_id
		          FROM pending_callbacks p
		          JOIN callback_bindings b ON b.external_task_id = p.external_task_id
		          JOIN node_attempts a ON a.id = b.target_id
		          JOIN node_runs nr ON nr.id = a.node_run_id
		         WHERE p.consumed_at IS NULL
		           AND p.expires_at > $1
		           AND b.target_type = 'NODE_ATTEMPT'
		           AND a.status = 'DISPATCHED'
		           AND nr.status = 'WAITING_CALLBACK'
		        UNION ALL
		        SELECT p.external_task_id, p.payload, p.payload_hash, p.callback_token_hash,
		               p.received_at, p.expires_at, p.consumed_at, p.duplicate_count,
		               b.id, b.provider_id, b.external_task_id, b.target_type,
		               b.target_id, b.created_at,
		               nr.run_id, nr.id, ta.id
		          FROM pending_callbacks p
		          JOIN callback_bindings b ON b.external_task_id = p.external_task_id
		          JOIN tool_attempts ta ON ta.id = b.target_id
		          JOIN agent_actions aa ON aa.id = ta.action_id
		          JOIN agent_turns t ON t.id = aa.turn_id
		          JOIN agent_runs ar ON ar.id = t.agent_run_id
		          JOIN node_runs nr ON nr.id = ar.node_run_id
		         WHERE p.consumed_at IS NULL
		           AND p.expires_at > $1
		           AND b.target_type = 'TOOL_ATTEMPT'
		           AND ta.status = 'DISPATCHED'
		           AND aa.status = 'WAITING_CALLBACK'
		           AND nr.status = 'WAITING_CALLBACK'
		       ) c
		 ORDER BY c.received_at ASC, c.external_task_id ASC
		 LIMIT $2`

	rows, err := r.conn.Query(ctx, query, now, limit)
	if err != nil {
		return nil, mapError("pending_callbacks.ListConsumableForWaiting", err)
	}
	defer rows.Close()

	out := []store.PendingForWaiting{}
	for rows.Next() {
		var (
			row        store.PendingForWaiting
			payload    []byte
			targetType string
		)
		if err := rows.Scan(
			&row.Pending.ExternalTaskID, &payload, &row.Pending.PayloadHash,
			&row.Pending.CallbackTokenHash, &row.Pending.ReceivedAt, &row.Pending.ExpiresAt,
			&row.Pending.ConsumedAt, &row.Pending.DuplicateCount,
			&row.Binding.ID, &row.Binding.ProviderID, &row.Binding.ExternalTaskID,
			&targetType, &row.Binding.TargetID, &row.Binding.CreatedAt,
			&row.RunID, &row.NodeRunID, &row.AttemptID,
		); err != nil {
			return nil, mapError("pending_callbacks.ListConsumableForWaiting", err)
		}
		row.Pending.Payload = json.RawMessage(payload)
		row.Binding.TargetType = domain.CallbackTargetType(targetType)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("pending_callbacks.ListConsumableForWaiting", err)
	}
	return out, nil
}

// DeleteExpired removes records past their expiry, consumed or not: a consumed record has
// already served its purpose, and an unmatched one has lost the right to advance anything
// (05 §1.7). The inner SELECT bounds the batch so retention never locks the whole table.
func (r *pendingCallbackRepository) DeleteExpired(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("store/postgres pending_callbacks.DeleteExpired: limit must be positive, got %d", limit)
	}

	const del = `
		DELETE FROM pending_callbacks
		 WHERE external_task_id IN (
		       SELECT external_task_id
		         FROM pending_callbacks
		        WHERE expires_at <= $1
		        ORDER BY expires_at ASC
		        LIMIT $2)`

	return affectedRows(ctx, r.conn, "pending_callbacks.DeleteExpired", del, now, limit)
}

func scanPendingCallbacks(rows pgx.Rows) ([]domain.PendingCallback, error) {
	defer rows.Close()

	out := []domain.PendingCallback{}
	for rows.Next() {
		var (
			pending domain.PendingCallback
			payload []byte
		)
		if err := rows.Scan(
			&pending.ExternalTaskID, &payload, &pending.PayloadHash, &pending.CallbackTokenHash,
			&pending.ReceivedAt, &pending.ExpiresAt, &pending.ConsumedAt, &pending.DuplicateCount,
		); err != nil {
			return nil, err
		}
		pending.Payload = json.RawMessage(payload)
		out = append(out, pending)
	}
	return out, rows.Err()
}

var _ store.PendingCallbackRepository = (*pendingCallbackRepository)(nil)
