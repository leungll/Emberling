//go:build integration

package contract

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/api"
	"github.com/leungll/Emberling/backend/internal/service"
)

// TestAPI_Stream_EventCommittedBetweenQueryAndWait_DeliveredOnceInOrder covers the
// query-to-wait switch window: the stream's cursor query returns nothing, and before the
// loop blocks waiting for a notification or poll tick, a new Event commits through the
// normal service path. There is no one-shot "replay then live" switch, so that Event must
// still arrive - exactly once, at the next contiguous seq - and a later reconnect with
// Last-Event-ID must not replay it.
//
// Every remaining Event of the Run (through RUN_COMPLETED) commits while the loop is held,
// and PollInterval is 10 minutes, so nothing later can paper over a lost in-window wake:
// if the notification that fired while the loop was held did not survive into the wait,
// no frame ever arrives and the test fails within its 5s bound instead of being rescued
// by a later commit or the poll fallback.
func TestAPI_Stream_EventCommittedBetweenQueryAndWait_DeliveredOnceInOrder(t *testing.T) {
	model := newBarrierAtCall(1)
	wait := newBarrierAtCall(1)
	var heldCursor atomic.Int64
	var committed *commitObserver
	env := newTestEnvWithOptions(t, testEnvOptions{
		PollInterval: 10 * time.Minute,
		WrapServiceNotifier: func(hub *api.Hub) service.EventNotifier {
			committed = newCommitObserver(hub)
			return committed
		},
		StreamHooks: api.StreamHooks{
			BeforeWait: func(ctx context.Context, _ string, cursor int64) {
				heldCursor.Store(cursor)
				wait.beforeReturn(ctx)
			},
		},
	})
	env.provider.BeforeReturn = model.beforeReturn

	runID := createAndBarrierRun(t, env, model)
	committed.Watch(runID)
	preSeq := snapshotLastSeq(t, env, runID)

	// Open the stream already caught up to preSeq. Its first query is empty (execution
	// is stalled at the model call), and the loop then stalls at the wait barrier: held
	// after the query, before it blocks on the notifier wake / poll tick.
	resp, reader := env.openSSE(t, runID, preSeq, "")
	defer resp.Body.Close()
	wait.waitEntered(t, 5*time.Second)
	if got := heldCursor.Load(); got != preSeq {
		t.Fatalf("stream held before its wait at cursor=%d, want the opening afterSeq=%d (the query it just ran must have been empty)", got, preSeq)
	}

	// While the loop is held, let execution run to completion: every remaining Event
	// commits through the service path, and the notifier fires only after each COMMIT.
	model.Release()
	committed.WaitCommitted(t, 5*time.Second)
	env.waitForTerminal(t, runID, 5*time.Second)
	finalSeq := snapshotLastSeq(t, env, runID)

	wait.Release()

	first := waitForNextSSEFrame(t, reader, 5*time.Second,
		"an Event committed between the cursor query and the wait was lost (PollInterval=10m, so only the pending wake could deliver it)")
	if first.id != preSeq+1 {
		t.Fatalf("first frame after the held window has seq=%d, want %d (next contiguous seq after the opening cursor)", first.id, preSeq+1)
	}

	seqs := []int64{first.id}
	terminal := first.event == "RUN_COMPLETED" || first.event == "RUN_FAILED"
	for !terminal {
		f := waitForNextSSEFrame(t, reader, 5*time.Second, "stream stalled before the terminal Event")
		seqs = append(seqs, f.id)
		terminal = f.event == "RUN_COMPLETED" || f.event == "RUN_FAILED"
	}
	for i, s := range seqs {
		if want := preSeq + 1 + int64(i); s != want {
			t.Fatalf("stream delivered seqs=%v with a gap/duplicate after the held window: seqs[%d] = %d, want %d", seqs, i, s, want)
		}
	}
	if seqs[len(seqs)-1] != finalSeq {
		t.Fatalf("stream's last seq=%d, want the terminal Snapshot's lastSeq=%d", seqs[len(seqs)-1], finalSeq)
	}

	// Reconnect claiming to have read exactly the Event that committed inside the window:
	// it must not be replayed (a stream that replayed it would deliver it as its very
	// first frame), and everything after it arrives once, in order, through the terminal
	// Event. The frames are read one by one rather than until EOF: with PollInterval=10m
	// the stream only re-checks for closure on its next wake or tick, and the closure
	// timing is not what this test is about.
	inWindow := preSeq + 1
	reResp, reReader := env.openSSE(t, runID, 0, strconv.FormatInt(inWindow, 10))
	defer reResp.Body.Close()
	want := seqs[1:]
	for i, wantSeq := range want {
		f := waitForNextSSEFrame(t, reReader, 5*time.Second, "reconnect with Last-Event-ID stalled before the terminal Event")
		if f.id <= inWindow {
			t.Fatalf("reconnect with Last-Event-ID=%d replayed seq=%d at frame [%d]", inWindow, f.id, i)
		}
		if f.id != wantSeq {
			t.Fatalf("reconnect with Last-Event-ID=%d delivered seq=%d at frame [%d], want %d (of %v)", inWindow, f.id, i, wantSeq, want)
		}
	}
}

// commitObserver wraps the *api.Hub as service.Deps.Notifier (forwarding every call, so
// SSE wakeups stay real) and additionally closes committed the first time the watched
// Run's Events are reported committed. A test uses it to know that a post-COMMIT
// notification has genuinely fired while a stream is held, instead of sleeping and
// assuming it has.
type commitObserver struct {
	hub       *api.Hub
	watchID   atomic.Value // string
	committed chan struct{}
	once      sync.Once
}

func newCommitObserver(hub *api.Hub) *commitObserver {
	return &commitObserver{hub: hub, committed: make(chan struct{})}
}

// Watch starts observing runID; EventsCommitted calls for other Runs are only forwarded.
func (o *commitObserver) Watch(runID string) {
	o.watchID.Store(runID)
}

func (o *commitObserver) EventsCommitted(runID string, lastSeq int64) {
	o.hub.EventsCommitted(runID, lastSeq)
	if id, ok := o.watchID.Load().(string); ok && id == runID {
		o.once.Do(func() { close(o.committed) })
	}
}

// WaitCommitted blocks until the watched Run has had Events committed and notified, or
// fails the test after timeout.
func (o *commitObserver) WaitCommitted(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-o.committed:
	case <-time.After(timeout):
		t.Fatalf("no EventsCommitted notification for the watched Run within %s", timeout)
	}
}
