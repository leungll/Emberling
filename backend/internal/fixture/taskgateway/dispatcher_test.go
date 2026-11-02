package taskgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

// recordingAfter fires at once and records every backoff it was asked for.
type recordingAfter struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (a *recordingAfter) after(d time.Duration) <-chan time.Time {
	a.mu.Lock()
	a.waits = append(a.waits, d)
	a.mu.Unlock()
	ready := make(chan time.Time, 1)
	ready <- testNow
	return ready
}

func (a *recordingAfter) recorded() []time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]time.Duration(nil), a.waits...)
}

func newTestDispatcher(after func(time.Duration) <-chan time.Time) *Dispatcher {
	return newDispatcher(&http.Client{Timeout: testWait}, slog.New(slog.DiscardHandler), after, func() time.Time { return testNow })
}

func shutdown(t *testing.T, d *Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func testDelivery(url string, deadline time.Time) delivery {
	return delivery{
		externalTaskID: "gw_0000000000000001",
		url:            url,
		token:          testToken,
		payload:        json.RawMessage(`{"status":"SUCCEEDED"}`),
		deadline:       deadline,
	}
}

func TestDispatcher_FirstDeliveryFails_RetriesUntilAccepted(t *testing.T) {
	receiver := newCallbackReceiver(t, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusOK)
	after := &recordingAfter{}
	d := newTestDispatcher(after.after)

	d.deliver(testDelivery(receiver.server.URL, testNow.Add(time.Hour)))
	first, second, third := receiver.next(t), receiver.next(t), receiver.next(t)
	shutdown(t, d)

	if receiver.count() != 3 {
		t.Fatalf("deliveries = %d, want 3: two failures, then the accepted one", receiver.count())
	}
	if !bytes.Equal(first.body, third.body) || !bytes.Equal(second.body, third.body) || third.token != testToken {
		t.Fatalf("retried deliveries differ: %s / %s / %s", first.body, second.body, third.body)
	}
	if got := after.recorded(); len(got) != 2 || got[0] != time.Second || got[1] != 2*time.Second {
		t.Fatalf("backoffs = %v, want [1s 2s]", got)
	}
}

func TestDispatcher_Rejected4xx_StopsRetrying(t *testing.T) {
	receiver := newCallbackReceiver(t, http.StatusUnauthorized)
	after := &recordingAfter{}
	d := newTestDispatcher(after.after)

	d.deliver(testDelivery(receiver.server.URL, testNow.Add(time.Hour)))
	receiver.next(t)
	shutdown(t, d)

	if receiver.count() != 1 || len(after.recorded()) != 0 {
		t.Fatalf("deliveries = %d, backoffs = %v; want one delivery and no retry after a 4xx", receiver.count(), after.recorded())
	}
}

func TestDispatcher_DeadlineReached_StopsRetrying(t *testing.T) {
	receiver := newCallbackReceiver(t, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	after := &recordingAfter{}
	d := newTestDispatcher(after.after)

	// The deadline leaves room for the 1s backoff but not for the 2s one.
	d.deliver(testDelivery(receiver.server.URL, testNow.Add(1500*time.Millisecond)))
	receiver.next(t)
	receiver.next(t)
	shutdown(t, d)

	if receiver.count() != 2 {
		t.Fatalf("deliveries = %d, want 2: the retry that fits the deadline and no more", receiver.count())
	}
	if got := after.recorded(); len(got) != 1 || got[0] != time.Second {
		t.Fatalf("backoffs = %v, want [1s]", got)
	}
}

func TestDispatcher_BackoffDoublesUpToCap(t *testing.T) {
	statuses := make([]int, 8)
	for i := range statuses {
		statuses[i] = http.StatusServiceUnavailable
	}
	receiver := newCallbackReceiver(t, statuses...)
	after := &recordingAfter{}
	d := newTestDispatcher(after.after)

	d.deliver(testDelivery(receiver.server.URL, testNow.Add(time.Hour)))
	for range 9 {
		receiver.next(t)
	}
	shutdown(t, d)

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	got := after.recorded()
	if len(got) != len(want) {
		t.Fatalf("backoffs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoffs = %v, want %v", got, want)
		}
	}
}

func TestDispatcher_ShutdownDuringBackoff_EndsDelivery(t *testing.T) {
	receiver := newCallbackReceiver(t, http.StatusServiceUnavailable)
	waiting := make(chan struct{})
	never := func(time.Duration) <-chan time.Time {
		close(waiting)
		return make(chan time.Time)
	}
	d := newTestDispatcher(never)

	d.deliver(testDelivery(receiver.server.URL, testNow.Add(time.Hour)))
	receiver.next(t)
	<-waiting
	shutdown(t, d)
	if receiver.count() != 1 {
		t.Fatalf("deliveries = %d, want 1", receiver.count())
	}
}
