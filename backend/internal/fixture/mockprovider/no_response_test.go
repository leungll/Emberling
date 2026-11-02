package mockprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// postTaskExpectingNoResponse sends one task request and requires that the Provider closed
// the connection without writing any response.
func postTaskExpectingNoResponse(t *testing.T, baseURL string, req taskRequest) {
	t.Helper()
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	resp, err := http.Post(baseURL+"/v1/tasks", "application/json", bytes.NewReader(encoded))
	if err == nil {
		resp.Body.Close()
		t.Fatalf("POST /v1/tasks answered %d, want the connection closed without a response", resp.StatusCode)
	}
}

// newNoResponseProvider returns a controlled provider whose delayed callbacks wait on the
// returned release channel, so a test decides when the configured delay has elapsed.
func newNoResponseProvider(t *testing.T, delay time.Duration) (*controlledProvider, chan time.Time, chan time.Duration) {
	t.Helper()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"), WithNoResponseCallbackDelay(delay))
	release := make(chan time.Time)
	requested := make(chan time.Duration, 4)
	p.dispatcher.after = func(d time.Duration) <-chan time.Time {
		requested <- d
		return release
	}
	return p, release, requested
}

func TestMockProvider_NoResponse_ClosesConnectionWithoutWritingResponse(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, _, _ := newNoResponseProvider(t, time.Second)

	postTaskExpectingNoResponse(t, p.srv.URL, taskRequest{
		ExternalTaskID: "task-dropped",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-dropped",
		DelayMs:        delaySpec{Mode: delayNoResponse},
	})

	events := eventsFor(p.recordLines(t, ""), "task-dropped")
	if !contains(events, recordArrived) || !contains(events, recordResponseDropped) {
		t.Fatalf("record events = %v, want arrived and %s", events, recordResponseDropped)
	}
	if contains(events, recordResponded) {
		t.Fatalf("record events = %v, want no responded line for a dropped response", events)
	}
}

func TestMockProvider_NoResponse_CreatesTaskAndClaimsIdempotencyKey(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, _, _ := newNoResponseProvider(t, time.Second)

	req := taskRequest{
		ExternalTaskID: "task-original",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-original",
		IdempotencyKey: "key-lost-response",
		DelayMs:        delaySpec{Mode: delayNoResponse},
	}
	postTaskExpectingNoResponse(t, p.srv.URL, req)

	status, body := getTaskStatus(t, p.srv.URL, "task-original")
	if status != http.StatusOK || decodeTaskStatus(t, body).Status != taskStatusRunning {
		t.Fatalf("GET task-original = %d %s, want 200 RUNNING: the task exists although no response was written", status, body)
	}

	// The retry carries the same key and a different proposed id: the Provider answers with
	// the original task, exactly as a real Provider replays a claimed key.
	retry := req
	retry.ExternalTaskID = "task-retry"
	retry.CallbackToken = "tok-retry"
	resp := postJSON(t, p.srv.URL+"/v1/tasks", retry)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry StatusCode = %d, want %d for a replayed idempotency key", resp.StatusCode, http.StatusOK)
	}
	var replayed taskResponse
	decodeBody(t, resp, &replayed)
	if replayed.ExternalTaskID != "task-original" {
		t.Fatalf("retry ExternalTaskID = %q, want task-original", replayed.ExternalTaskID)
	}
}

func TestMockProvider_NoResponse_DeliversCallbackAfterConfiguredDelay(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, release, requested := newNoResponseProvider(t, 1500*time.Millisecond)

	postTaskExpectingNoResponse(t, p.srv.URL, taskRequest{
		ExternalTaskID: "task-late",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-late",
		DelayMs:        delaySpec{Mode: delayNoResponse},
	})

	select {
	case got := <-requested:
		if got != 1500*time.Millisecond {
			t.Fatalf("callback delay = %v, want the configured 1.5s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no callback was scheduled after the dropped response")
	}
	if calls := receiver.Calls(); len(calls) != 0 {
		t.Fatalf("callbacks before the delay elapsed = %d, want 0", len(calls))
	}

	release <- time.Time{}
	waitForCalls(t, p.notify, 1)
	calls := receiver.Calls()
	if len(calls) != 1 || calls[0].Body.ExternalTaskID != "task-late" || calls[0].Token != "tok-late" {
		t.Fatalf("callbacks = %+v, want exactly one for task-late with its own token", calls)
	}
}

func TestMockProvider_NoResponse_WithoutTestControls_IsRejected(t *testing.T) {
	dispatcher := NewDispatcher(nil)
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-uncontrolled",
		CallbackURL:    "http://callback.invalid/cb",
		CallbackToken:  "tok-uncontrolled",
		DelayMs:        delaySpec{Mode: delayNoResponse},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d: noResponse exists only with test controls", resp.StatusCode, http.StatusBadRequest)
	}
	if status, _ := getTaskStatus(t, srv.URL, "task-uncontrolled"); status != http.StatusNotFound {
		t.Fatalf("GET task-uncontrolled = %d, want 404: a rejected request creates no task", status)
	}
	if err := dispatcher.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestMockProvider_NoResponse_RecordNeverContainsCallbackToken(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p, release, _ := newNoResponseProvider(t, time.Second)

	const token = "tok-must-not-leak-noresponse"
	postTaskExpectingNoResponse(t, p.srv.URL, taskRequest{
		ExternalTaskID: "task-secret",
		CallbackURL:    receiver.server.URL + "/cb?sig=secret-query",
		CallbackToken:  token,
		IdempotencyKey: "key-secret",
		DelayMs:        delaySpec{Mode: delayNoResponse},
	})
	release <- time.Time{}
	waitForCalls(t, p.notify, 1)

	raw, err := os.ReadFile(p.recordPath)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	for _, secret := range []string{token, "secret-query"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("record contains %q: %s", secret, raw)
		}
	}
}
