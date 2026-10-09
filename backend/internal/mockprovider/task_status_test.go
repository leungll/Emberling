package mockprovider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// getTaskStatus queries GET /v1/tasks/{id} and returns the status code and raw body.
func getTaskStatus(t *testing.T, baseURL, externalTaskID string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(baseURL + "/v1/tasks/" + externalTaskID)
	if err != nil {
		t.Fatalf("GET /v1/tasks/%s error = %v", externalTaskID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET /v1/tasks/%s body error = %v", externalTaskID, err)
	}
	return resp.StatusCode, body
}

func decodeTaskStatus(t *testing.T, body []byte) taskStatusResponse {
	t.Helper()
	var out taskStatusResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("task status body %s is not JSON: %v", body, err)
	}
	return out
}

// newTimerControlledServer returns a Mock Provider whose delayed callbacks wait on the
// returned release channel, plus the delivery barrier for its Dispatcher.
func newTimerControlledServer(t *testing.T) (*httptest.Server, chan time.Time, chan callbackOutcome) {
	t.Helper()
	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 4)
	dispatcher.notify = notify
	release := make(chan time.Time)
	dispatcher.after = func(time.Duration) <-chan time.Time { return release }
	srv := httptest.NewServer(NewServer(dispatcher))
	t.Cleanup(srv.Close)
	return srv, release, notify
}

func TestMockProvider_TaskStatus_BeforeScheduledOutcome_ReportsRunning(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	srv, release, notify := newTimerControlledServer(t)

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-running",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-running",
		DelayMs:        delaySpec{Mode: delayAfter, Duration: time.Minute},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /v1/tasks status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	status, body := getTaskStatus(t, srv.URL, "task-running")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	got := decodeTaskStatus(t, body)
	if got.ExternalTaskID != "task-running" || got.Status != taskStatusRunning {
		t.Fatalf("task status = %+v, want externalTaskId task-running and status RUNNING", got)
	}
	if got.Payload != nil {
		t.Fatalf("payload = %s, want none while RUNNING", got.Payload)
	}

	// Let the scheduled callback go so the Dispatcher goroutine can finish.
	release <- time.Now()
	waitForCalls(t, notify, 1)
}

func TestMockProvider_TaskStatus_AfterOutcome_ReportsSucceededWithPayload(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	srv, release, notify := newTimerControlledServer(t)

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-done",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-done",
		DelayMs:        delaySpec{Mode: delayAfter, Duration: time.Minute},
		Payload:        json.RawMessage(`{"status":"SUCCEEDED","uri":"https://mock.invalid/v1/images/a.png"}`),
	})
	resp.Body.Close()

	release <- time.Now()
	waitForCalls(t, notify, 1)

	status, body := getTaskStatus(t, srv.URL, "task-done")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	got := decodeTaskStatus(t, body)
	if got.Status != taskStatusSucceeded {
		t.Fatalf("status = %q, want %q", got.Status, taskStatusSucceeded)
	}
	if want := `{"status":"SUCCEEDED","uri":"https://mock.invalid/v1/images/a.png"}`; string(got.Payload) != want {
		t.Fatalf("payload = %s, want the callback payload %s", got.Payload, want)
	}
	if calls := receiver.Calls(); len(calls) != 1 {
		t.Fatalf("callbacks delivered = %d, want 1: a status query must not deliver another", len(calls))
	}
}

func TestMockProvider_TaskStatus_FailedOutcome_ReportsFailed(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	srv, _, notify := newTimerControlledServer(t)

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-failed",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-failed",
		Outcome:        outcomeFailed,
	})
	resp.Body.Close()
	waitForCalls(t, notify, 1)

	status, body := getTaskStatus(t, srv.URL, "task-failed")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	got := decodeTaskStatus(t, body)
	if got.Status != taskStatusFailed {
		t.Fatalf("status = %q, want %q", got.Status, taskStatusFailed)
	}
	if string(got.Payload) != string(failedPayload) {
		t.Fatalf("payload = %s, want %s", got.Payload, failedPayload)
	}
}

func TestMockProvider_TaskStatus_LostCallback_StaysRunning(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	srv, _, _ := newTimerControlledServer(t)

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-lost",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-lost",
		DelayMs:        delaySpec{Mode: delayLost},
	})
	resp.Body.Close()

	status, body := getTaskStatus(t, srv.URL, "task-lost")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	if got := decodeTaskStatus(t, body); got.Status != taskStatusRunning {
		t.Fatalf("status = %q, want %q for a task whose callback is never scheduled", got.Status, taskStatusRunning)
	}
}

func TestMockProvider_TaskStatus_UnknownTask_Returns404(t *testing.T) {
	srv, _, _ := newTimerControlledServer(t)

	status, body := getTaskStatus(t, srv.URL, "task-never-accepted")
	if status != http.StatusNotFound {
		t.Fatalf("GET status = %d, want %d, body=%s", status, http.StatusNotFound, body)
	}
	var out errorResponse
	if err := json.Unmarshal(body, &out); err != nil || out.Error != "unknown externalTaskId" {
		t.Fatalf("body = %s, want error %q", body, "unknown externalTaskId")
	}
}

func TestMockProvider_TaskStatus_Body_NeverContainsCallbackToken(t *testing.T) {
	const token = "secret-callback-token-value"
	receiver := newCallbackReceiver()
	defer receiver.Close()
	srv, release, notify := newTimerControlledServer(t)

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-token",
		CallbackURL:    receiver.server.URL + "/callbacks?sig=query-secret",
		CallbackToken:  token,
		DelayMs:        delaySpec{Mode: delayAfter, Duration: time.Minute},
	})
	resp.Body.Close()

	_, running := getTaskStatus(t, srv.URL, "task-token")
	release <- time.Now()
	waitForCalls(t, notify, 1)
	_, terminal := getTaskStatus(t, srv.URL, "task-token")

	for _, body := range [][]byte{running, terminal} {
		for _, secret := range []string{token, "query-secret", "callbackUrl", "callbackToken"} {
			if strings.Contains(string(body), secret) {
				t.Fatalf("task status body %s contains %q", body, secret)
			}
		}
	}
}

func TestMockProvider_TaskStatus_WithoutTestControls_IsServed(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	srv, _, notify := newTimerControlledServer(t)

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-no-controls",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok",
	})
	resp.Body.Close()
	waitForCalls(t, notify, 1)

	status, body := getTaskStatus(t, srv.URL, "task-no-controls")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d without test controls, body=%s", status, http.StatusOK, body)
	}
	if got := decodeTaskStatus(t, body); got.Status != taskStatusSucceeded {
		t.Fatalf("status = %q, want %q", got.Status, taskStatusSucceeded)
	}
}

func TestMockProvider_TaskStatus_WithTestControls_WritesPollRecordLine(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.ndjson"))

	resp := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-polled",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-polled",
	})
	resp.Body.Close()
	waitForCalls(t, p.notify, 1)

	if status, body := getTaskStatus(t, p.srv.URL, "task-polled"); status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	if status, _ := getTaskStatus(t, p.srv.URL, "task-missing"); status != http.StatusNotFound {
		t.Fatalf("GET unknown status = %d, want %d", status, http.StatusNotFound)
	}

	var polls []recordEntry
	for _, entry := range p.recordLines(t, "") {
		if entry.Kind == kindPoll {
			polls = append(polls, entry)
		}
	}
	if len(polls) != 2 {
		t.Fatalf("poll record lines = %+v, want 2", polls)
	}
	if got := polls[0]; got.Event != recordPolled || got.ExternalTaskID != "task-polled" || got.Status != http.StatusOK || got.TaskStatus != taskStatusSucceeded {
		t.Fatalf("first poll line = %+v, want polled task-polled status 200 taskStatus SUCCEEDED", got)
	}
	if got := polls[1]; got.Event != recordPolled || got.ExternalTaskID != "task-missing" || got.Status != http.StatusNotFound || got.TaskStatus != "" {
		t.Fatalf("second poll line = %+v, want polled task-missing status 404 and no taskStatus", got)
	}
}

func TestTaskStatuses_Full_EvictsOldestTask(t *testing.T) {
	statuses := newTaskStatuses()
	for i := 0; i <= maxTaskStatuses; i++ {
		statuses.accept("task-"+strconv.Itoa(i), succeededPayload, false)
	}
	if _, ok := statuses.lookup("task-0"); ok {
		t.Fatal("lookup(task-0) found, want the oldest task evicted once the map is full")
	}
	if _, ok := statuses.lookup("task-" + strconv.Itoa(maxTaskStatuses)); !ok {
		t.Fatal("lookup(newest) not found, want it kept")
	}
	if len(statuses.tasks) != maxTaskStatuses {
		t.Fatalf("len(tasks) = %d, want %d", len(statuses.tasks), maxTaskStatuses)
	}
}
