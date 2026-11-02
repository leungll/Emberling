package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/store"
)

// providerSlotRepository persists which NodeRun holds a slot of which asynchronous
// Provider. The lock order is fixed by the callers: the Run aggregate lock first, then
// LockProvider; releasing a slot takes no Provider lock at all, because a delete can only
// lower the count another admission reads.
type providerSlotRepository struct {
	conn pgx.Tx
}

// SeedLocks inserts the Provider rows admission later locks. Repeated startups and
// concurrent processes converge on the same rows: an existing row is left alone.
func (r *providerSlotRepository) SeedLocks(ctx context.Context, providerIDs []string) error {
	if len(providerIDs) == 0 {
		return nil
	}
	const insert = `
		INSERT INTO provider_slot_locks (provider_id)
		SELECT unnest($1::text[])
		ON CONFLICT (provider_id) DO NOTHING`
	if _, err := r.conn.Exec(ctx, insert, providerIDs); err != nil {
		return mapError("provider_slots.SeedLocks", err)
	}
	return nil
}

// LockProvider serializes every admission of one Provider on its row for the rest of the
// transaction. A missing row is an error: nothing could serialize on it, so admitting
// without it could hand out one slot too many.
func (r *providerSlotRepository) LockProvider(ctx context.Context, providerID string) error {
	const lock = `SELECT provider_id FROM provider_slot_locks WHERE provider_id = $1 FOR UPDATE`
	var locked string
	if err := r.conn.QueryRow(ctx, lock, providerID).Scan(&locked); err != nil {
		return mapError("provider_slots.LockProvider", err, providerID)
	}
	return nil
}

// CountHeld counts the slots of providerID currently held. Issued as its own statement
// after LockProvider returned, it reads a fresh snapshot under READ COMMITTED and so sees
// every admission the previous lock holders committed.
func (r *providerSlotRepository) CountHeld(ctx context.Context, providerID string) (int, error) {
	const query = `SELECT count(*) FROM provider_slots WHERE provider_id = $1`
	var held int
	if err := r.conn.QueryRow(ctx, query, providerID).Scan(&held); err != nil {
		return 0, mapError("provider_slots.CountHeld", err, providerID)
	}
	return held, nil
}

// Acquire records that nodeRunID now holds a slot of providerID. The primary key refuses
// a second row for the same NodeRun; that is returned as an error rather than ignored,
// because a NodeRun claimed from READY cannot already hold a slot.
func (r *providerSlotRepository) Acquire(ctx context.Context, nodeRunID, providerID string, now time.Time) error {
	const insert = `
		INSERT INTO provider_slots (node_run_id, provider_id, acquired_at)
		VALUES ($1, $2, $3)`
	if _, err := r.conn.Exec(ctx, insert, nodeRunID, providerID, now); err != nil {
		return mapError("provider_slots.Acquire", err, nodeRunID, providerID)
	}
	return nil
}

// Release deletes the occupancy row of nodeRunID. Zero affected rows means the NodeRun
// held no slot, or released it already; both are ordinary outcomes.
func (r *providerSlotRepository) Release(ctx context.Context, nodeRunID string) (string, bool, error) {
	const remove = `DELETE FROM provider_slots WHERE node_run_id = $1 RETURNING provider_id`
	var providerID string
	err := r.conn.QueryRow(ctx, remove, nodeRunID).Scan(&providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, mapError("provider_slots.Release", err, nodeRunID)
	}
	return providerID, true, nil
}

// deferredReadyPredicate selects the NodeRuns that count as queued for a Provider: READY,
// already refused once, and owned by a Run that is still RUNNING or PAUSED. A Run that
// already failed never schedules its remaining NodeRuns again, so a queued NodeRun it
// left behind must not hold the Provider's queue.
const deferredReadyPredicate = `
	nr.status = 'READY'
	AND nr.capacity_deferred_at IS NOT NULL
	AND nr.capacity_provider_id = $1
	AND EXISTS (SELECT 1 FROM runs r WHERE r.id = nr.run_id AND r.status IN ('RUNNING', 'PAUSED'))`

// CountEarlierDeferred is both the first-come check and the queue position: one query
// serves both so that what is shown is exactly what was decided.
func (r *providerSlotRepository) CountEarlierDeferred(ctx context.Context, providerID, excludeRunID string, key store.QueueKey) (int, error) {
	const query = `
		SELECT count(*) FROM node_runs nr
		 WHERE ` + deferredReadyPredicate + `
		   AND nr.run_id <> $2
		   AND (nr.ready_at, nr.id) < ($3, $4)`
	var earlier int
	if err := r.conn.QueryRow(ctx, query, providerID, excludeRunID, key.ReadyAt, key.NodeRunID).Scan(&earlier); err != nil {
		return 0, mapError("provider_slots.CountEarlierDeferred", err, providerID, excludeRunID)
	}
	return earlier, nil
}

// NextDeferredRun selects the Run to wake after a slot of providerID was released: the
// one owning the earliest queued NodeRun in queue order.
func (r *providerSlotRepository) NextDeferredRun(ctx context.Context, providerID string) (string, bool, error) {
	const query = `
		SELECT nr.run_id FROM node_runs nr
		 WHERE ` + deferredReadyPredicate + `
		 ORDER BY nr.ready_at ASC, nr.id ASC
		 LIMIT 1`
	var runID string
	err := r.conn.QueryRow(ctx, query, providerID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, mapError("provider_slots.NextDeferredRun", err, providerID)
	}
	return runID, true, nil
}

var _ store.ProviderSlotRepository = (*providerSlotRepository)(nil)
