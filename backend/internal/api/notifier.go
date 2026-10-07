package api

import "sync"

// Hub wakes SSE cursor loops after Events commit for a Run. It implements
// service.EventNotifier. PostgreSQL Event rows remain the sole authority for replay: a
// missed, coalesced, or entirely absent wake only costs one extra poll tick, never a lost
// or duplicated Event (CLAUDE.md: "In-process notification only wakes a cursor query").
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[*subscriber]struct{}
}

// subscriber is one open SSE connection's wake signal. wake is buffered by exactly one
// slot: a burst of commits while the subscriber is busy re-querying coalesces into a
// single pending wake instead of blocking the committing transaction's caller.
type subscriber struct {
	wake chan struct{}
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[*subscriber]struct{})}
}

// Subscribe registers a new listener for runID. The caller must invoke the returned
// unsubscribe func exactly once (typically deferred) when the connection ends, so a
// disconnected client's subscriber is dropped rather than accumulating forever.
func (h *Hub) Subscribe(runID string) (*subscriber, func()) { //nolint:revive // unexported-return: only this package reads a subscriber's wake channel
	sub := &subscriber{wake: make(chan struct{}, 1)}

	h.mu.Lock()
	set, ok := h.subs[runID]
	if !ok {
		set = make(map[*subscriber]struct{})
		h.subs[runID] = set
	}
	set[sub] = struct{}{}
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set, ok := h.subs[runID]; ok {
			delete(set, sub)
			if len(set) == 0 {
				delete(h.subs, runID)
			}
		}
	}
	return sub, unsubscribe
}

// EventsCommitted implements service.EventNotifier. It never blocks the caller (the
// committing transaction's post-COMMIT step): every send is non-blocking, and a
// subscriber that has not drained its previous wake simply keeps the one pending signal
// it already has instead of receiving a second one.
func (h *Hub) EventsCommitted(runID string, _ int64) {
	h.mu.Lock()
	set := h.subs[runID]
	subs := make([]*subscriber, 0, len(set))
	for s := range set {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	for _, s := range subs {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}
