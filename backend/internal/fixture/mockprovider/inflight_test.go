package mockprovider

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// inFlightState is GET /control/inflight as a test decodes it.
type inFlightState struct {
	InFlight  int  `json:"inFlight"`
	Peak      int  `json:"peak"`
	Limit     int  `json:"limit"`
	OverLimit bool `json:"overLimit"`
}

func (p *controlledProvider) inFlight(t *testing.T) inFlightState {
	t.Helper()
	resp, err := http.Get(p.srv.URL + "/control/inflight")
	if err != nil {
		t.Fatalf("GET /control/inflight error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /control/inflight status = %d, want 200", resp.StatusCode)
	}
	var out inFlightState
	decodeBody(t, resp, &out)
	return out
}

// newHeldCallbackProvider returns a controlled provider whose delayed callbacks all wait on
// the returned release channel, so accepted tasks stay in flight until the test lets one
// finish.
func newHeldCallbackProvider(t *testing.T, opts ...Option) (*controlledProvider, chan time.Time) {
	t.Helper()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"), opts...)
	release := make(chan time.Time)
	p.dispatcher.after = func(time.Duration) <-chan time.Time { return release }
	return p, release
}

// acceptDelayed posts n tasks whose callbacks wait on the injected timer.
func acceptDelayed(t *testing.T, p *controlledProvider, callbackURL string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		resp := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{
			ExternalTaskID: id,
			CallbackURL:    callbackURL,
			CallbackToken:  "tok-" + id,
			DelayMs:        delaySpec{Mode: delayAfter, Duration: time.Second},
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST %s StatusCode = %d, want %d", id, resp.StatusCode, http.StatusAccepted)
		}
	}
}

func TestMockProvider_InFlightPeak_TracksAcceptedUnfinishedTasks(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, release := newHeldCallbackProvider(t)

	acceptDelayed(t, p, receiver.server.URL, "task-a", "task-b", "task-c")
	if got := p.inFlight(t); got.InFlight != 3 || got.Peak != 3 {
		t.Fatalf("in flight = %+v, want 3 in flight with peak 3", got)
	}

	release <- time.Time{}
	waitForCalls(t, p.notify, 1)
	if got := p.inFlight(t); got.InFlight != 2 || got.Peak != 3 {
		t.Fatalf("in flight after one finished = %+v, want 2 in flight with peak still 3", got)
	}

	acceptDelayed(t, p, receiver.server.URL, "task-d")
	if got := p.inFlight(t); got.InFlight != 3 || got.Peak != 3 {
		t.Fatalf("in flight after a new task = %+v, want 3 in flight with peak 3", got)
	}

	// A replayed idempotency key creates no task, so it never counts as in flight.
	first := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "task-keyed", CallbackURL: receiver.server.URL, CallbackToken: "tok-keyed", IdempotencyKey: "key-1", DelayMs: delaySpec{Mode: delayLost}})
	first.Body.Close()
	replay := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "task-keyed-2", CallbackURL: receiver.server.URL, CallbackToken: "tok-keyed", IdempotencyKey: "key-1", DelayMs: delaySpec{Mode: delayLost}})
	replay.Body.Close()
	if got := p.inFlight(t); got.InFlight != 4 || got.Peak != 4 {
		t.Fatalf("in flight after a replay = %+v, want 4 in flight with peak 4", got)
	}
}

func TestMockProvider_InFlightPeak_DuplicateDelivery_FinishesTaskOnce(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, _ := newHeldCallbackProvider(t)

	acceptDelayed(t, p, receiver.server.URL, "task-held")
	resp := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "task-dup", CallbackURL: receiver.server.URL, CallbackToken: "tok-dup", DelayMs: delaySpec{Mode: delayDuplicate}})
	resp.Body.Close()
	waitForCalls(t, p.notify, 2)

	if got := p.inFlight(t); got.InFlight != 1 || got.Peak != 2 {
		t.Fatalf("in flight = %+v, want 1 in flight with peak 2: a second delivery finishes nothing", got)
	}
}

func TestMockProvider_InFlightPeak_OverLimit_MarksRecord(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, _ := newHeldCallbackProvider(t, WithInFlightLimit(2))

	acceptDelayed(t, p, receiver.server.URL, "task-1", "task-2", "task-3")

	got := p.inFlight(t)
	if got.InFlight != 3 || got.Peak != 3 || got.Limit != 2 || !got.OverLimit {
		t.Fatalf("in flight = %+v, want 3 in flight, peak 3, limit 2, overLimit", got)
	}

	marked := map[string]bool{}
	counts := map[string]int{}
	for _, line := range p.recordLines(t, "") {
		if line.Event != recordResponded || line.Status != http.StatusAccepted {
			continue
		}
		marked[line.ExternalTaskID] = line.OverLimit
		counts[line.ExternalTaskID] = line.InFlight
	}
	for id, want := range map[string]bool{"task-1": false, "task-2": false, "task-3": true} {
		if marked[id] != want {
			t.Errorf("record overLimit for %s = %v, want %v", id, marked[id], want)
		}
	}
	for i, id := range []string{"task-1", "task-2", "task-3"} {
		if counts[id] != i+1 {
			t.Errorf("record inFlight for %s = %d, want %d", id, counts[id], i+1)
		}
	}
}

func TestMockProvider_InFlightPeak_WithinLimit_NothingMarked(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, _ := newHeldCallbackProvider(t, WithInFlightLimit(5))

	ids := make([]string, 5)
	for i := range ids {
		ids[i] = "task-" + strconv.Itoa(i)
	}
	acceptDelayed(t, p, receiver.server.URL, ids...)

	if got := p.inFlight(t); got.Peak != 5 || got.OverLimit {
		t.Fatalf("in flight = %+v, want peak 5 and not over the limit of 5", got)
	}
	for _, line := range p.recordLines(t, "") {
		if line.OverLimit {
			t.Fatalf("record line %+v is marked over limit, want none at the limit itself", line)
		}
	}
}

func TestMockProvider_InFlightPeak_NoResponseTask_CountsAndMarksDroppedLine(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, _ := newHeldCallbackProvider(t, WithInFlightLimit(1), WithNoResponseCallbackDelay(time.Second))

	acceptDelayed(t, p, receiver.server.URL, "task-first")
	postTaskExpectingNoResponse(t, p.srv.URL, taskRequest{ExternalTaskID: "task-dropped", CallbackURL: receiver.server.URL, CallbackToken: "tok-dropped", DelayMs: delaySpec{Mode: delayNoResponse}})

	if got := p.inFlight(t); got.InFlight != 2 || !got.OverLimit {
		t.Fatalf("in flight = %+v, want 2 in flight over the limit of 1", got)
	}
	for _, line := range p.recordLines(t, "") {
		if line.Event == recordResponseDropped && (line.InFlight != 2 || !line.OverLimit) {
			t.Fatalf("dropped line = %+v, want inFlight 2 marked over limit", line)
		}
	}
}

func TestMockProvider_InFlightPeak_WithoutTestControls_RouteIsAbsent(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/control/inflight")
	if err != nil {
		t.Fatalf("GET /control/inflight error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /control/inflight status = %d, want 404 without test controls", resp.StatusCode)
	}
}

func TestTaskStatuses_EvictedUnfinishedTask_LeavesInFlight(t *testing.T) {
	statuses := newTaskStatuses()
	for i := 0; i < maxTaskStatuses+1; i++ {
		statuses.accept("task-"+strconv.Itoa(i), succeededPayload, false)
	}
	if current, peak := statuses.inFlightCounts(); current != maxTaskStatuses || peak != maxTaskStatuses {
		t.Fatalf("inFlightCounts() = %d, %d; want %d, %d: an evicted task is no longer counted", current, peak, maxTaskStatuses, maxTaskStatuses)
	}
}
