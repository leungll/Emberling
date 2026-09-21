package api

import "testing"

// TestHub_EventsCommitted_WakesSubscriberOfSameRun proves the basic wake path: Subscribe
// then EventsCommitted must leave exactly one pending signal on the subscriber's wake
// channel. EventsCommitted's own doc comment guarantees the send happens synchronously
// before it returns, so this needs no wait at all (CLAUDE.md testing standard: no sleeps
// to manufacture a result that is already deterministic).
func TestHub_EventsCommitted_WakesSubscriberOfSameRun(t *testing.T) {
	h := NewHub()
	sub, unsubscribe := h.Subscribe("run_1")
	defer unsubscribe()

	h.EventsCommitted("run_1", 1)

	select {
	case <-sub.wake:
	default:
		t.Fatal("EventsCommitted did not wake the subscriber of the same runID")
	}
}

// TestHub_EventsCommitted_DoesNotWakeSubscriberOfDifferentRun proves per-Run isolation:
// a subscriber registered for one runID must never be woken by another Run's commit.
func TestHub_EventsCommitted_DoesNotWakeSubscriberOfDifferentRun(t *testing.T) {
	h := NewHub()
	sub, unsubscribe := h.Subscribe("run_1")
	defer unsubscribe()

	h.EventsCommitted("run_2", 1)

	select {
	case <-sub.wake:
		t.Fatal("EventsCommitted(\"run_2\", ...) woke a subscriber registered for \"run_1\"")
	default:
	}
}

// TestHub_EventsCommitted_CoalescesBurstsIntoOnePendingWake proves the buffered-1
// coalescing behavior notifier.go documents: a subscriber that has not drained its
// previous wake must keep exactly one pending signal after any number of additional
// commits, never block the committing goroutine, and never queue a second signal.
func TestHub_EventsCommitted_CoalescesBurstsIntoOnePendingWake(t *testing.T) {
	h := NewHub()
	sub, unsubscribe := h.Subscribe("run_1")
	defer unsubscribe()

	const commits = 5
	for i := 0; i < commits; i++ {
		h.EventsCommitted("run_1", int64(i)) // must never block regardless of queue depth
	}

	select {
	case <-sub.wake:
	default:
		t.Fatal("expected exactly one pending wake after a burst of commits, got none")
	}
	select {
	case <-sub.wake:
		t.Fatal("expected exactly one pending wake after a burst of commits, got a second one")
	default:
	}
}

// TestHub_Unsubscribe_StopsFurtherWakesAndRemovesEmptyRunEntry proves the documented
// cleanup contract: after unsubscribe, the subscriber must never be woken again, and a
// Run with no remaining subscribers must not linger in the Hub's map (a disconnected
// client's subscriber must not accumulate forever, per Subscribe's own doc comment).
func TestHub_Unsubscribe_StopsFurtherWakesAndRemovesEmptyRunEntry(t *testing.T) {
	h := NewHub()
	sub, unsubscribe := h.Subscribe("run_1")
	unsubscribe()

	h.EventsCommitted("run_1", 1)

	select {
	case <-sub.wake:
		t.Fatal("EventsCommitted woke a subscriber after it had unsubscribed")
	default:
	}

	h.mu.Lock()
	_, stillPresent := h.subs["run_1"]
	h.mu.Unlock()
	if stillPresent {
		t.Fatal("Hub retained an entry for a runID with no remaining subscribers")
	}
}

// TestHub_EventsCommitted_WakesEveryConcurrentSubscriberOfTheRun proves that more than
// one open SSE connection for the same Run (e.g. two browser tabs) are woken
// independently: unsubscribing one must not affect the other.
func TestHub_EventsCommitted_WakesEveryConcurrentSubscriberOfTheRun(t *testing.T) {
	h := NewHub()
	subA, unsubA := h.Subscribe("run_1")
	defer unsubA()
	subB, unsubB := h.Subscribe("run_1")
	defer unsubB()

	h.EventsCommitted("run_1", 1)

	for name, s := range map[string]*subscriber{"A": subA, "B": subB} {
		select {
		case <-s.wake:
		default:
			t.Fatalf("subscriber %s was not woken", name)
		}
	}
}
