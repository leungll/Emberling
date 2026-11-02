package mockprovider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// payloadUsage decodes the usage member of a task result payload; ok is false when the
// payload has none.
func payloadUsage(t *testing.T, payload json.RawMessage) (usage struct {
	Cost json.Number `json:"cost"`
}, ok bool) {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil {
		t.Fatalf("payload %s is not a JSON object: %v", payload, err)
	}
	raw, ok := members["usage"]
	if !ok {
		return usage, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&usage); err != nil {
		t.Fatalf("usage %s is not an object: %v", raw, err)
	}
	return usage, true
}

func TestMockProvider_TaskResult_CarriesFixedCost(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	// The caller's explicit payload is how the image task Adapter reports its image; the
	// cost is added beside it without touching the members the caller sent.
	resp := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-cost",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-cost",
		Payload:        json.RawMessage(`{"status":"SUCCEEDED","image":{"source":"EXTERNAL"}}`),
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	waitForCalls(t, p.notify, 1)

	calls := receiver.Calls()
	if len(calls) != 1 {
		t.Fatalf("callbacks = %d, want 1", len(calls))
	}
	usage, ok := payloadUsage(t, calls[0].Body.Payload)
	if !ok || usage.Cost.String() != fixedTaskCost {
		t.Fatalf("callback usage = %+v (present %v), want cost %s", usage, ok, fixedTaskCost)
	}
	var callback struct {
		Status string          `json:"status"`
		Image  json.RawMessage `json:"image"`
	}
	if err := json.Unmarshal(calls[0].Body.Payload, &callback); err != nil || callback.Status != taskStatusSucceeded || string(callback.Image) != `{"source":"EXTERNAL"}` {
		t.Fatalf("callback payload = %s, want the caller's members kept", calls[0].Body.Payload)
	}

	status, body := getTaskStatus(t, p.srv.URL, "task-cost")
	if status != http.StatusOK {
		t.Fatalf("GET task-cost status = %d, want 200", status)
	}
	polled := decodeTaskStatus(t, body)
	usage, ok = payloadUsage(t, polled.Payload)
	if !ok || usage.Cost.String() != fixedTaskCost {
		t.Fatalf("polled usage = %+v (present %v), want cost %s", usage, ok, fixedTaskCost)
	}
}

func TestMockProvider_TaskResult_DefaultSuccessPayload_CarriesFixedCost(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	resp := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "task-default", CallbackURL: receiver.server.URL, CallbackToken: "tok-default"})
	resp.Body.Close()
	waitForCalls(t, p.notify, 1)

	usage, ok := payloadUsage(t, receiver.Calls()[0].Body.Payload)
	if !ok || usage.Cost.String() != fixedTaskCost {
		t.Fatalf("callback usage = %+v (present %v), want cost %s", usage, ok, fixedTaskCost)
	}
}

func TestMockProvider_TaskResult_FailedOutcome_CarriesNoCost(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	resp := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "task-failed", CallbackURL: receiver.server.URL, CallbackToken: "tok-failed", Outcome: outcomeFailed})
	resp.Body.Close()
	waitForCalls(t, p.notify, 1)

	if _, ok := payloadUsage(t, receiver.Calls()[0].Body.Payload); ok {
		t.Fatalf("failed callback payload = %s, want no usage: a failed task reports no cost", receiver.Calls()[0].Body.Payload)
	}
}

func TestMockProvider_TaskResult_CallerSuppliedUsage_IsKept(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	resp := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{
		ExternalTaskID: "task-own-usage",
		CallbackURL:    receiver.server.URL,
		CallbackToken:  "tok-own-usage",
		Payload:        json.RawMessage(`{"status":"SUCCEEDED","usage":{}}`),
	})
	resp.Body.Close()
	waitForCalls(t, p.notify, 1)

	if got := string(receiver.Calls()[0].Body.Payload); got != `{"status":"SUCCEEDED","usage":{}}` {
		t.Fatalf("callback payload = %s, want the caller's own usage forwarded verbatim", got)
	}
}

func TestMockProvider_TaskResult_WithoutTestControls_CarriesNoCost(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 1)
	dispatcher.notify = notify
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "task-plain", CallbackURL: receiver.server.URL, CallbackToken: "tok-plain"})
	resp.Body.Close()
	waitForCalls(t, notify, 1)

	if got := string(receiver.Calls()[0].Body.Payload); got != string(succeededPayload) {
		t.Fatalf("callback payload = %s, want %s unchanged without test controls", got, succeededPayload)
	}
}
