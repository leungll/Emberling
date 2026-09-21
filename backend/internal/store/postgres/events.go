package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// errNoRunLock reports an attempt to allocate Event seq or write the Run aggregate
// without holding the Run aggregate lock.
var errNoRunLock = errors.New("run aggregate lock not held")

type eventRepository struct {
	conn pgx.Tx
}

const eventColumns = `id, run_id, node_run_id, type, seq, occurred_at, payload`

// Append allocates seq from the held Run aggregate lock and inserts the Event. Because
// seq comes only from a lock read inside this transaction, and UNIQUE (run_id, seq)
// backs it in the database, concurrent advancement of one Run cannot fork the timeline.
//
// The caller is responsible for writing the watermark back with
// RunRepository.UpdateAggregate before the transaction commits.
func (r *eventRepository) Append(ctx context.Context, lock *store.RunLock, ev domain.Event) (domain.Event, error) {
	if !lock.Held() {
		return domain.Event{}, fmt.Errorf("store/postgres events.Append: run=%s: %w", ev.RunID, errNoRunLock)
	}
	if lock.RunID() != ev.RunID {
		return domain.Event{}, fmt.Errorf("store/postgres events.Append: lock holds run=%s, event belongs to run=%s",
			lock.RunID(), ev.RunID)
	}
	if !ev.Type.IsValid() {
		return domain.Event{}, fmt.Errorf("store/postgres events.Append: run=%s: unknown event type %q", ev.RunID, ev.Type)
	}

	ev.Seq = lock.NextSeq()
	payload := ev.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	const insert = `
		INSERT INTO events (` + eventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	if _, err := r.conn.Exec(ctx, insert,
		ev.ID, ev.RunID, ev.NodeRunID, string(ev.Type), ev.Seq, ev.Timestamp, payload,
	); err != nil {
		return domain.Event{}, mapError("events.Append", err, ev.RunID, ev.Seq)
	}
	return ev, nil
}

// ListAfter replays committed Events with seq greater than afterSeq. It is the only
// authority for SSE: an in-process notification merely wakes this query.
func (r *eventRepository) ListAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres events.ListAfter: run=%s: limit must be positive, got %d", runID, limit)
	}

	const query = `SELECT ` + eventColumns + `
		  FROM events
		 WHERE run_id = $1 AND seq > $2
		 ORDER BY seq ASC
		 LIMIT $3`

	rows, err := r.conn.Query(ctx, query, runID, afterSeq, limit)
	if err != nil {
		return nil, mapError("events.ListAfter", err, runID, afterSeq)
	}
	defer rows.Close()

	out := []domain.Event{}
	for rows.Next() {
		var (
			ev  domain.Event
			typ string
		)
		if err := rows.Scan(&ev.ID, &ev.RunID, &ev.NodeRunID, &typ, &ev.Seq, &ev.Timestamp, &ev.Payload); err != nil {
			return nil, mapError("events.ListAfter scan", err, runID)
		}
		ev.Type = domain.EventType(typ)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("events.ListAfter", err, runID)
	}
	return out, nil
}

var _ store.EventRepository = (*eventRepository)(nil)
