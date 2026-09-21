package mockprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// callbackTask is one scheduled callback delivery.
type callbackTask struct {
	ExternalTaskID string
	CallbackURL    string
	CallbackToken  string
	Payload        json.RawMessage
}

// callbackOutcome reports one completed delivery attempt to whatever test hook is
// listening; production code never reads it.
type callbackOutcome struct {
	Task callbackTask
	Err  error
}

// Dispatcher owns every goroutine that delivers a scheduled callback. It is never tied to
// an inbound HTTP request's context: a request handler returns as soon as a task is
// scheduled, and a request's context is cancelled the moment that handler returns, which
// would otherwise silently kill any callback delayed past that point. Dispatcher instead
// runs against its own context, cancelled explicitly by Shutdown.
type Dispatcher struct {
	client *http.Client
	// after abstracts time.After so tests can inject a controllable timer instead of a
	// real one and avoid sleep-based races.
	after func(time.Duration) <-chan time.Time
	// notify, if non-nil, receives one callbackOutcome per completed delivery attempt.
	// It is a test-only barrier hook; Schedule sends best-effort and never blocks on it.
	notify chan callbackOutcome

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	wg      sync.WaitGroup
	stopped bool
}

// NewDispatcher returns a Dispatcher that delivers callbacks with client (a nil client
// defaults to http.DefaultClient's zero-value equivalent with a bounded timeout).
func NewDispatcher(client *http.Client) *Dispatcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		client: client,
		after:  func(d time.Duration) <-chan time.Time { return time.After(d) },
		ctx:    ctx,
		cancel: cancel,
	}
}

// Schedule delivers task once after delay, in a goroutine this Dispatcher owns and waits
// for during Shutdown. Called after Shutdown has started, it is a no-op: a shutting-down
// process must not accept new background work it cannot finish.
func (d *Dispatcher) Schedule(task callbackTask, delay time.Duration) {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.wg.Add(1)
	d.mu.Unlock()

	go func() {
		defer d.wg.Done()
		if delay > 0 {
			select {
			case <-d.after(delay):
			case <-d.ctx.Done():
				return
			}
		}
		err := d.send(d.ctx, task)
		if d.notify != nil {
			select {
			case d.notify <- callbackOutcome{Task: task, Err: err}:
			case <-d.ctx.Done():
			}
		}
	}()
}

// SendNow delivers task synchronously on the caller's goroutine, honoring ctx (normally
// the inbound request's context) rather than the Dispatcher's own lifetime. It backs the
// delayBeforeResponse scenario, where the callback must complete before /v1/tasks answers.
func (d *Dispatcher) SendNow(ctx context.Context, task callbackTask) error {
	return d.send(ctx, task)
}

// send performs one callback POST. It never retries: a real external Provider gets exactly
// as many delivery attempts as this call graph gives it, matching the "no autonomous
// retry" rule for anything simulating an Adapter's external boundary.
func (d *Dispatcher) send(ctx context.Context, task callbackTask) error {
	payload := task.Payload
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(callbackBody{ExternalTaskID: task.ExternalTaskID, Payload: payload})
	if err != nil {
		return fmt.Errorf("mockprovider: encode callback body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, task.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mockprovider: build callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The callback token is forwarded exactly as received and never logged (10 §2/§4):
	// this package is a Provider stand-in, not a holder of Emberling credentials.
	req.Header.Set("X-Emberling-Callback-Token", task.CallbackToken)

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("mockprovider: deliver callback: %w", err)
	}
	// The receiver's status is deliberately not inspected: a rejection such as 08 §4's 401
	// for a mismatched token is a completed delivery attempt with a defined answer, not a
	// transport failure, and this Provider stand-in never retries on its own either way.
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Shutdown stops Schedule from accepting new work and waits for every in-flight delivery
// goroutine to finish, or for ctx to expire first. In-flight deliveries keep the
// Dispatcher's own context alive so they can complete normally within ctx's deadline;
// Shutdown only cancels that context once ctx itself expires, to unblock whatever is still
// waiting rather than leak it.
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	d.stopped = true
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		d.cancel()
		return nil
	case <-ctx.Done():
		d.cancel()
		return ctx.Err()
	}
}
