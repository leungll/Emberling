package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// handleEvents serves GET /runs/{runId}/events, content-negotiated by Accept
// (docs/08-interface-spec.md §5): text/event-stream opens an SSE connection, anything
// else (the default) returns one page of JSON history.
func (d Deps) handleEvents(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")

	afterSeq, ok := parseAfterSeq(w, d.Logger, r)
	if !ok {
		return
	}

	if wantsSSE(r) {
		d.streamEvents(w, r, runID, afterSeq)
		return
	}
	d.listEventsJSON(w, r, runID, afterSeq)
}

func wantsSSE(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

// parseAfterSeq resolves the replay cursor. Last-Event-ID (a reconnect's own last
// delivered seq) takes priority over afterSeq, matching docs/08-interface-spec.md §5
// exactly ("`Last-Event-ID` 优先, 避免浏览器重连时退回初次连接的旧 afterSeq"); with neither
// present the cursor is 0.
func parseAfterSeq(w http.ResponseWriter, logger *slog.Logger, r *http.Request) (int64, bool) {
	if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		v, err := strconv.ParseInt(lastEventID, 10, 64)
		if err != nil {
			writeError(w, logger, &badRequestError{message: "Last-Event-ID must be an integer"}, "")
			return 0, false
		}
		return v, true
	}
	q := r.URL.Query().Get("afterSeq")
	if q == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		writeError(w, logger, &badRequestError{message: "afterSeq must be an integer"}, "")
		return 0, false
	}
	return v, true
}

// parseLimit reads an optional limit query parameter. An absent or invalid value is 0,
// which service.QueryService.Events already treats as "use its own default"; this
// handler does not duplicate that clamping.
func parseLimit(r *http.Request) int {
	q := r.URL.Query().Get("limit")
	if q == "" {
		return 0
	}
	v, err := strconv.Atoi(q)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// listEventsJSON serves the Accept: application/json branch of GET /runs/{runId}/events.
func (d Deps) listEventsJSON(w http.ResponseWriter, r *http.Request, runID string, afterSeq int64) {
	events, err := d.Query.Events(r.Context(), runID, afterSeq, parseLimit(r))
	if err != nil {
		writeError(w, d.Logger, err, codeRunNotFound)
		return
	}
	if events == nil {
		events = []domain.Event{}
	}
	writeJSON(w, http.StatusOK, listEnvelope[domain.Event]{Items: events})
}

// streamEvents serves the SSE branch. It never treats a Notifier wakeup as the Event
// itself (CLAUDE.md: "In-process notification only wakes a cursor query"): every loop
// iteration re-reads PostgreSQL through QueryService, and a missed or coalesced wakeup
// only costs one extra tick of PollInterval, never a lost or duplicated Event.
func (d Deps) streamEvents(w http.ResponseWriter, r *http.Request, runID string, cursor int64) {
	// Existence check before any header is written: once streaming starts, the status
	// code can no longer change, so a missing Run must 404 here, not mid-stream.
	if _, err := d.Query.Snapshot(r.Context(), runID); err != nil {
		writeError(w, d.Logger, err, codeRunNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, d.Logger, fmt.Errorf("api: streaming unsupported by this response writer"), "")
		return
	}

	sub, unsubscribe := d.Notifier.Subscribe(runID)
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(d.PollInterval)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		events, err := d.Query.Events(ctx, runID, cursor, 0)
		if err != nil {
			d.Logger.Error("api: sse query failed", "run", runID, "error", err)
			return
		}
		for _, ev := range events {
			if err := writeSSEEvent(w, ev); err != nil {
				return
			}
			cursor = ev.Seq
		}
		if len(events) > 0 {
			flusher.Flush()
		}

		if len(events) == 0 {
			// Nothing new: check whether the Run has already reached a terminal status
			// as of a fresh read and this cursor has caught up to it, in which case no
			// future Event can ever arrive and the connection can close.
			snap, err := d.Query.Snapshot(ctx, runID)
			if err != nil {
				d.Logger.Error("api: sse snapshot failed", "run", runID, "error", err)
				return
			}
			if snap.Run.Status.IsTerminal() && cursor >= snap.LastSeq {
				return
			}

			// The Run is still active and nothing new committed this iteration (whether
			// triggered by a poll tick or a coalesced/false-positive notifier wake): emit
			// an SSE comment frame so a proxy or load balancer sitting between the client
			// and this connection sees bytes at least once per PollInterval, instead of
			// treating a quiet-but-healthy connection as dead.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}

		// docs/08 §5 handoff invariant: there is no one-shot "replay then live" switch.
		// The subscriber registered above the loop, so an Event committed between this
		// iteration's (empty) query and the wait below leaves a pending wake token in
		// sub.wake (buffer of one) and the next iteration re-queries from the same cursor;
		// the poll tick is the bounded fallback. The hook only lets a test hold the loop
		// in exactly that window to prove it.
		if d.StreamHooks.BeforeWait != nil {
			d.StreamHooks.BeforeWait(ctx, runID, cursor)
		}

		select {
		case <-ctx.Done():
			return
		case <-sub.wake:
		case <-ticker.C:
		}
	}
}

// StreamHooks are optional observation points inside streamEvents. Production wiring
// leaves every field nil (a no-op); contract tests inject a barrier to pause the cursor
// loop at a specific point without sleeping.
type StreamHooks struct {
	// BeforeWait runs once per loop iteration after the cursor query (and any frames it
	// produced) and immediately before the loop blocks on the notifier wake or poll tick.
	// cursor is the seq the next query will start after.
	BeforeWait func(ctx context.Context, runID string, cursor int64)
}

// writeSSEEvent writes one Event as a single SSE frame. The frame's data is the same
// domain.Event JSON encoding a JSON history page returns, so a client parses both
// identically.
func writeSSEEvent(w http.ResponseWriter, ev domain.Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, payload)
	return err
}
