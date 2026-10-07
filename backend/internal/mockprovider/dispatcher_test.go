package mockprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// receivedCallback is one call the test's fake callback endpoint recorded, together with
// the status that endpoint answered it with.
type receivedCallback struct {
	Token  string
	Body   callbackBody
	Status int
}

// callbackReceiver is an httptest server that stands in for Emberling's own
// POST /api/callbacks endpoint, recording every delivery under a mutex so
// concurrent Dispatcher goroutines can be asserted against safely.
type callbackReceiver struct {
	server *httptest.Server

	mu sync.Mutex
	// expectToken, when non-empty, makes the receiver enforce the callback token rule: any
	// other token is answered 401 and its payload is not recorded.
	expectToken string
	logs        []receivedCallback
}

func newCallbackReceiver() *callbackReceiver {
	c := &callbackReceiver{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body callbackBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		token := r.Header.Get("X-Emberling-Callback-Token")

		c.mu.Lock()
		expect := c.expectToken
		if expect != "" && token != expect {
			// An invalid token must not have its payload saved. Only the token and
			// task id are recorded, so a test can still prove the delivery happened.
			c.logs = append(c.logs, receivedCallback{
				Token:  token,
				Body:   callbackBody{ExternalTaskID: body.ExternalTaskID},
				Status: http.StatusUnauthorized,
			})
			c.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c.logs = append(c.logs, receivedCallback{Token: token, Body: body, Status: http.StatusOK})
		c.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted":true,"pending":false,"duplicate":false}`))
	}))
	return c
}

// RejectTokensOtherThan makes this receiver answer 401 for every callback whose
// X-Emberling-Callback-Token differs from token, as Emberling's own handler does.
func (c *callbackReceiver) RejectTokensOtherThan(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expectToken = token
}

func (c *callbackReceiver) Close() { c.server.Close() }

func (c *callbackReceiver) Calls() []receivedCallback {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]receivedCallback, len(c.logs))
	copy(out, c.logs)
	return out
}

// waitForCalls blocks on notify until n outcomes have been observed or t fails via timeout.
// It is the barrier this suite uses instead of a sleep.
func waitForCalls(t *testing.T, notify chan callbackOutcome, n int) []callbackOutcome {
	t.Helper()
	outcomes := make([]callbackOutcome, 0, n)
	for i := 0; i < n; i++ {
		select {
		case outcome := <-notify:
			outcomes = append(outcomes, outcome)
		case <-time.After(2 * time.Second):
			t.Fatalf("waitForCalls: timed out waiting for outcome %d/%d", i+1, n)
		}
	}
	return outcomes
}

func TestDispatcher_Schedule_ZeroDelayDeliversExactlyOnce(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	d := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 4)
	d.notify = notify

	task := callbackTask{ExternalTaskID: "task_1", CallbackURL: receiver.server.URL, CallbackToken: "tok-1", Payload: json.RawMessage(`{"ok":true}`)}
	d.Schedule(task, 0)

	outcomes := waitForCalls(t, notify, 1)
	if outcomes[0].Err != nil {
		t.Fatalf("outcome.Err = %v, want nil", outcomes[0].Err)
	}

	calls := receiver.Calls()
	if len(calls) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1", len(calls))
	}
	if calls[0].Token != "tok-1" {
		t.Fatalf("Calls()[0].Token = %q, want %q", calls[0].Token, "tok-1")
	}
	if calls[0].Body.ExternalTaskID != "task_1" {
		t.Fatalf("Calls()[0].Body.ExternalTaskID = %q, want %q", calls[0].Body.ExternalTaskID, "task_1")
	}
}

func TestDispatcher_Schedule_DelayWaitsForInjectedTimerBeforeDelivering(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	d := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 4)
	d.notify = notify

	// Replace the real timer with a barrier this test controls explicitly: the goroutine
	// blocks until the test sends on release, so no sleep or wall-clock delay is needed to
	// prove the callback waits for the timer.
	release := make(chan time.Time)
	d.after = func(time.Duration) <-chan time.Time { return release }

	task := callbackTask{ExternalTaskID: "task_2", CallbackURL: receiver.server.URL, CallbackToken: "tok-2"}
	d.Schedule(task, 5*time.Second)

	select {
	case <-notify:
		t.Fatal("callback delivered before the injected timer fired")
	case <-time.After(50 * time.Millisecond):
		// Expected: still waiting on the timer.
	}

	release <- time.Now()
	waitForCalls(t, notify, 1)

	if len(receiver.Calls()) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1 once the timer fires", len(receiver.Calls()))
	}
}

func TestDispatcher_SendNow_DeliversSynchronouslyOnCallerGoroutine(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	d := NewDispatcher(nil)
	task := callbackTask{ExternalTaskID: "task_3", CallbackURL: receiver.server.URL, CallbackToken: "tok-3"}
	if err := d.SendNow(context.Background(), task); err != nil {
		t.Fatalf("SendNow() error = %v, want nil", err)
	}
	if len(receiver.Calls()) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1 immediately after SendNow returns", len(receiver.Calls()))
	}
}

func TestDispatcher_Send_UnreachableCallbackURLReturnsError(t *testing.T) {
	d := NewDispatcher(nil)
	task := callbackTask{ExternalTaskID: "task_4", CallbackURL: "http://127.0.0.1:0", CallbackToken: "tok-4"}
	if err := d.SendNow(context.Background(), task); err == nil {
		t.Fatal("SendNow() error = nil, want error for an unreachable callback URL")
	}
}

func TestDispatcher_Shutdown_WaitsForInFlightDeliveryThenReturns(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	d := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 1)
	d.notify = notify

	release := make(chan time.Time)
	d.after = func(time.Duration) <-chan time.Time { return release }

	task := callbackTask{ExternalTaskID: "task_5", CallbackURL: receiver.server.URL, CallbackToken: "tok-5"}
	d.Schedule(task, time.Second)

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- d.Shutdown(context.Background())
	}()

	// Shutdown must not return while the goroutine still waits on the timer.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown() returned before the in-flight delivery's timer fired")
	case <-time.After(50 * time.Millisecond):
	}

	release <- time.Now()
	waitForCalls(t, notify, 1)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown() did not return after the in-flight delivery finished")
	}
}

func TestDispatcher_Shutdown_ContextDeadlineExceededReturnsErrorWithoutHanging(t *testing.T) {
	d := NewDispatcher(nil)
	release := make(chan time.Time) // never fires
	d.after = func(time.Duration) <-chan time.Time { return release }
	d.Schedule(callbackTask{ExternalTaskID: "task_6", CallbackURL: "http://example.invalid", CallbackToken: "tok-6"}, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := d.Shutdown(ctx); err == nil {
		t.Fatal("Shutdown() error = nil, want deadline-exceeded error when a goroutine never finishes")
	}
}

func TestDispatcher_Schedule_AfterShutdownIsANoOp(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	d := NewDispatcher(nil)
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}

	notify := make(chan callbackOutcome, 1)
	d.notify = notify
	d.Schedule(callbackTask{ExternalTaskID: "task_7", CallbackURL: receiver.server.URL, CallbackToken: "tok-7"}, 0)

	select {
	case <-notify:
		t.Fatal("Schedule() ran after Shutdown; a shut-down Dispatcher must not accept new work")
	case <-time.After(100 * time.Millisecond):
	}
	if len(receiver.Calls()) != 0 {
		t.Fatalf("len(Calls()) = %d, want 0: Schedule after Shutdown must not deliver", len(receiver.Calls()))
	}
}
