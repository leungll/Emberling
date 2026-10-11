package mockcontrol

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
)

// ErrStopping reports that a held request was rejected because the fixture is shutting
// down.
var ErrStopping = errors.New("mockcontrol: fixture is shutting down")

// ErrCallerGone reports that the caller disconnected while its request was held.
var ErrCallerGone = errors.New("mockcontrol: caller disconnected while held")

// Request describes one incoming request to Admit.
type Request struct {
	// Kind is the fixture's request kind; a pause may name it to hold only that kind.
	Kind string
	// Detail is the fixture's own fields on the arrived line, encoded after kind.
	Detail any
	// Ref identifies the request on its held entry and on the released, rejected and
	// abandoned lines, encoded after kind. nil leaves only the kind.
	Ref any
}

// HeldRequest is one request the barrier is currently holding.
type HeldRequest struct {
	Kind string
	// Ref is the Request's Ref, encoded between kind and arrivalSeq.
	Ref        any
	ArrivalSeq int64
}

// MarshalJSON encodes kind, the Ref's fields, then arrivalSeq, in that order.
func (h HeldRequest) MarshalJSON() ([]byte, error) {
	head, err := json.Marshal(struct {
		Kind string `json:"kind"`
	}{h.Kind})
	if err != nil {
		return nil, err
	}
	return joinObjects(head, h.Ref, struct {
		ArrivalSeq int64 `json:"arrivalSeq"`
	}{h.ArrivalSeq})
}

// Barrier holds matching requests between pause and release. Pause arms a fresh gate;
// Release closes it, letting every request waiting on that gate proceed at once, and
// disarms. Pausing again after a release arms a new gate. Stop rejects every held request
// and makes later pauses no-ops, so shutdown never waits on a request nobody will release.
type Barrier struct {
	// pausable lists the kinds a pause may name, in the order the barrier state reports
	// them.
	pausable []string

	mu      sync.Mutex
	paused  bool
	kinds   map[string]bool // empty means every kind
	gate    chan struct{}
	stopped chan struct{}
	isStop  bool
	nextID  int64
	held    map[int64]HeldRequest
}

// NewBarrier returns an unpaused barrier whose pause may name only the pausable kinds.
// A request of any other kind is held only by a pause of every kind.
func NewBarrier(pausable ...string) *Barrier {
	return &Barrier{
		pausable: slices.Clone(pausable),
		stopped:  make(chan struct{}),
		held:     make(map[int64]HeldRequest),
	}
}

// pause arms the barrier for kinds (every kind when empty). Pausing an already paused
// barrier keeps its gate and the requests already held, and only replaces the kind filter.
func (b *Barrier) pause(kinds []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.isStop {
		return
	}
	filter := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		filter[kind] = true
	}
	b.kinds = filter
	if !b.paused {
		b.paused = true
		b.gate = make(chan struct{})
	}
}

// release lets every held request proceed and disarms the barrier. It returns how many
// requests it released; releasing an unpaused barrier is a no-op that returns 0.
func (b *Barrier) release() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.paused {
		return 0
	}
	released := len(b.held)
	close(b.gate)
	b.paused = false
	b.gate = nil
	b.held = make(map[int64]HeldRequest)
	return released
}

// Stop rejects every held request and disables the barrier for good.
func (b *Barrier) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.isStop {
		return
	}
	b.isStop = true
	close(b.stopped)
	b.paused = false
	b.gate = nil
	b.held = make(map[int64]HeldRequest)
}

// enter registers req as held when the barrier is paused for its kind. It returns the gate
// to wait on and a ticket for leave, or a nil gate when the request must not be held.
func (b *Barrier) enter(req HeldRequest) (<-chan struct{}, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.paused || (len(b.kinds) > 0 && !b.kinds[req.Kind]) {
		return nil, 0
	}
	b.nextID++
	b.held[b.nextID] = req
	return b.gate, b.nextID
}

// leave removes a request that stops waiting for a reason other than release.
func (b *Barrier) leave(ticket int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.held, ticket)
}

// state returns whether the barrier is paused and which requests it is holding, ordered by
// arrival.
func (b *Barrier) state() (bool, []string, []HeldRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	held := make([]HeldRequest, 0, len(b.held))
	for _, req := range b.held {
		held = append(held, req)
	}
	slices.SortFunc(held, func(a, b HeldRequest) int { return cmp.Compare(a.ArrivalSeq, b.ArrivalSeq) })
	kinds := make([]string, 0, len(b.kinds))
	for _, kind := range b.pausable {
		if b.kinds[kind] {
			kinds = append(kinds, kind)
		}
	}
	return b.paused, kinds, held
}

// Admit records the arrival of req in record and, while the barrier is paused for its
// kind, holds it until release. It returns nil when the request may proceed, the record's
// write error when the arrival could not be recorded, ErrStopping when Stop rejected it
// and ErrCallerGone when ctx ended while it was held. onHeld, when non-nil, runs once
// right after the request is recorded and registered as held; it is a test hook.
func (b *Barrier) Admit(ctx context.Context, record *Record, req Request, onHeld func()) error {
	seq, err := record.Append(Line{Event: EventArrived, Kind: req.Kind, Detail: req.Detail})
	if err != nil {
		return err
	}
	gate, ticket := b.enter(HeldRequest{Kind: req.Kind, Ref: req.Ref, ArrivalSeq: seq})
	if gate == nil {
		return nil
	}
	if onHeld != nil {
		onHeld()
	}

	outcome := Line{Kind: req.Kind, Detail: req.Ref}
	select {
	case <-gate:
		outcome.Event = EventReleased
		_, _ = record.Append(outcome)
		return nil
	case <-b.stopped:
		outcome.Event = EventRejected
		_, _ = record.Append(outcome)
		return ErrStopping
	case <-ctx.Done():
		b.leave(ticket)
		outcome.Event = EventAbandoned
		_, _ = record.Append(outcome)
		return ErrCallerGone
	}
}
