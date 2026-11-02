package taskgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Callback delivery bounds: the first retry waits initialBackoff, each later one twice as
// long up to maxBackoff, and one delivery request may take at most deliveryTimeout.
const (
	initialBackoff  = time.Second
	maxBackoff      = 30 * time.Second
	deliveryTimeout = 10 * time.Second
)

// callbackHeader carries the one-time callback token.
const callbackHeader = "X-Emberling-Callback-Token"

// delivery is one callback to deliver. token lives only here, and the delivery is dropped
// once it ends.
type delivery struct {
	externalTaskID string
	url            string
	token          string
	payload        json.RawMessage
	deadline       time.Time
}

// Dispatcher owns every goroutine that delivers a callback. It runs against its own
// context, never an inbound request's, and stops on Shutdown.
//
// A delivery ends on the first 2xx, or on a 4xx, which means Emberling answered and
// refused it (an invalid token is 401). A transport failure or 5xx is retried with
// exponential backoff while the next attempt would still start before the task deadline:
// a callback after the deadline is of no use to the receiver, whose Attempt has ended.
type Dispatcher struct {
	client *http.Client
	logger *slog.Logger
	// after and now are injected so a test drives backoff without sleeping.
	after func(time.Duration) <-chan time.Time
	now   func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	// stop closes when Shutdown starts. A delivery waiting out a backoff ends at once; a
	// request already on the wire is allowed to finish.
	stop chan struct{}

	mu      sync.Mutex
	wg      sync.WaitGroup
	stopped bool
}

func newDispatcher(client *http.Client, logger *slog.Logger, after func(time.Duration) <-chan time.Time, now func() time.Time) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{client: client, logger: logger, after: after, now: now, ctx: ctx, cancel: cancel, stop: make(chan struct{})}
}

// deliver starts one delivery in a goroutine this Dispatcher owns. After Shutdown has
// started it is a no-op.
func (d *Dispatcher) deliver(task delivery) {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.wg.Add(1)
	d.mu.Unlock()

	go func() {
		defer d.wg.Done()
		d.run(task)
	}()
}

func (d *Dispatcher) run(task delivery) {
	backoff := initialBackoff
	for attempt := 1; ; attempt++ {
		status, err := d.send(task)
		switch {
		case err == nil && status < http.StatusInternalServerError:
			d.logger.Info("taskgateway: callback delivered",
				slog.String("external_task_id", task.externalTaskID), slog.Int("http_status", status), slog.Int("attempt", attempt))
			return
		case err != nil:
			d.logger.Warn("taskgateway: callback delivery failed",
				slog.String("external_task_id", task.externalTaskID), slog.Int("attempt", attempt))
		default:
			d.logger.Warn("taskgateway: callback delivery failed",
				slog.String("external_task_id", task.externalTaskID), slog.Int("http_status", status), slog.Int("attempt", attempt))
		}
		if !d.now().Add(backoff).Before(task.deadline) {
			d.logger.Warn("taskgateway: callback delivery abandoned at the task deadline",
				slog.String("external_task_id", task.externalTaskID), slog.Int("attempts", attempt))
			return
		}
		select {
		case <-d.after(backoff):
		case <-d.stop:
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// send performs one callback POST and returns the receiver's status. The error is never
// logged: a transport error's text names the callback URL.
func (d *Dispatcher) send(task delivery) (int, error) {
	body, err := json.Marshal(callbackBody{ExternalTaskID: task.externalTaskID, Payload: task.payload})
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, deliveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, task.url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(callbackHeader, task.token)
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// Shutdown stops new deliveries and waits for those in progress, until ctx ends; then it
// cancels whatever is still waiting so no goroutine is left behind.
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	if !d.stopped {
		d.stopped = true
		close(d.stop)
	}
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
		<-done
		return ctx.Err()
	}
}
