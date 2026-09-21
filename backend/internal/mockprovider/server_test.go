package mockprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("http.Post(%s) error = %v", url, err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode response body error = %v", err)
	}
}

func TestServer_HandleHealthz_ReturnsOK(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServer_HandleGenerate_DefaultScenarioReturnsFinalEcho(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/text/generate", generateRequest{Prompt: "hello"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body generateResponse
	decodeBody(t, resp, &body)
	if body.Decision.Kind != "FINAL" {
		t.Fatalf("Decision.Kind = %q, want %q", body.Decision.Kind, "FINAL")
	}
	if body.Decision.Output != "echo: hello" {
		t.Fatalf("Decision.Output = %q, want %q", body.Decision.Output, "echo: hello")
	}
}

func TestServer_HandleGenerate_ToolCallScenarioReturnsToolName(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/text/generate", generateRequest{Prompt: "hi", Scenario: "tool-call", ToolName: "lookup"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body generateResponse
	decodeBody(t, resp, &body)
	if body.Decision.Kind != "TOOL_CALL" || body.Decision.ToolName != "lookup" {
		t.Fatalf("Decision = %+v, want TOOL_CALL/lookup", body.Decision)
	}
}

func TestServer_HandleGenerate_FailScenarioReturnsBadGateway(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/text/generate", generateRequest{Prompt: "hi", Scenario: "fail"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
}

func TestServer_HandleGenerate_UnknownScenarioReturnsBadRequest(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/text/generate", generateRequest{Prompt: "hi", Scenario: "nonsense"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestServer_HandleGenerate_MalformedBodyReturnsBadRequest(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/text/generate", "application/json", bytes.NewReader([]byte("{not json")))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestServer_HandleTasks_MissingCallbackURLReturnsBadRequest(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackToken: "tok"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestServer_HandleTasks_MissingCallbackTokenReturnsBadRequest(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: "http://example.invalid"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestServer_HandleTasks_GeneratesExternalTaskIDWhenAbsent(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 1)
	dispatcher.notify = notify
	server := NewServer(dispatcher)
	srv := httptest.NewServer(server)
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-a"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	var body taskResponse
	decodeBody(t, resp, &body)
	if body.ExternalTaskID == "" {
		t.Fatal("ExternalTaskID = \"\", want a generated id")
	}

	waitForCalls(t, notify, 1)
	calls := receiver.Calls()
	if len(calls) != 1 || calls[0].Body.ExternalTaskID != body.ExternalTaskID {
		t.Fatalf("Calls() = %+v, want one delivery for %q", calls, body.ExternalTaskID)
	}
}

func TestServer_HandleTasks_HonorsCallerSuppliedExternalTaskID(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 1)
	dispatcher.notify = notify
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "caller-chosen", CallbackURL: receiver.server.URL, CallbackToken: "tok-b"})
	var body taskResponse
	decodeBody(t, resp, &body)
	if body.ExternalTaskID != "caller-chosen" {
		t.Fatalf("ExternalTaskID = %q, want %q", body.ExternalTaskID, "caller-chosen")
	}
	waitForCalls(t, notify, 1)
}

func TestServer_HandleTasks_DelayMsZeroDeliversImmediately(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 1)
	dispatcher.notify = notify
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-c", DelayMs: delaySpec{Mode: delayImmediate}})
	waitForCalls(t, notify, 1)
	if len(receiver.Calls()) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1", len(receiver.Calls()))
	}
}

func TestServer_HandleTasks_DelayMsLostNeverDelivers(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-d", DelayMs: delaySpec{Mode: delayLost}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d even for a lost callback", resp.StatusCode, http.StatusAccepted)
	}

	// Shutdown drains any Dispatcher goroutine deterministically; since delayMs="lost"
	// never schedules one, this only proves nothing was scheduled, not that a scheduled
	// send happened to lose a race.
	if err := dispatcher.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}
	if len(receiver.Calls()) != 0 {
		t.Fatalf("len(Calls()) = %d, want 0 for delayMs=\"lost\"", len(receiver.Calls()))
	}
}

func TestServer_HandleTasks_DelayMsDuplicateDeliversTwice(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 2)
	dispatcher.notify = notify
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-e", DelayMs: delaySpec{Mode: delayDuplicate}})
	waitForCalls(t, notify, 2)
	if len(receiver.Calls()) != 2 {
		t.Fatalf("len(Calls()) = %d, want 2 for delayMs=\"duplicate\"", len(receiver.Calls()))
	}
}

func TestServer_HandleTasks_DelayMsBeforeResponseDeliversBeforeResponseReturns(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-f", DelayMs: delaySpec{Mode: delayBeforeResponse}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	// No barrier or sleep needed here: SendNow is synchronous, so by the time the HTTP
	// client has the response, the callback POST has already completed.
	if len(receiver.Calls()) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1 delivered before /v1/tasks' response returned", len(receiver.Calls()))
	}
}

func TestServer_HandleTasks_DelayMsNumericWaitsForInjectedTimer(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 1)
	dispatcher.notify = notify
	release := make(chan time.Time)
	dispatcher.after = func(time.Duration) <-chan time.Time { return release }
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-g", DelayMs: delaySpec{Mode: delayAfter, Duration: time.Minute}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if len(receiver.Calls()) != 0 {
		t.Fatal("callback delivered before the injected timer fired")
	}
	release <- time.Now()
	waitForCalls(t, notify, 1)
	if len(receiver.Calls()) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1 once the timer fires", len(receiver.Calls()))
	}
}

func TestServer_HandleTasks_MalformedDelayMsReturnsBadRequest(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/tasks", "application/json", bytes.NewReader([]byte(
		`{"callbackUrl":"http://example.invalid","callbackToken":"tok","delayMs":"not-a-mode"}`,
	)))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestServer_Tasks_OutcomeFailed_CallbackPayloadReportsFailure(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 1)
	dispatcher.notify = notify
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		CallbackURL:   receiver.server.URL,
		CallbackToken: "tok-failed",
		Outcome:       outcomeFailed,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	waitForCalls(t, notify, 1)
	calls := receiver.Calls()
	if len(calls) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1", len(calls))
	}
	var payload struct {
		Status string `json:"status"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(calls[0].Body.Payload, &payload); err != nil {
		t.Fatalf("decode callback payload error = %v", err)
	}
	if payload.Status != "FAILED" {
		t.Fatalf("payload.status = %q, want %q", payload.Status, "FAILED")
	}
	if payload.Error.Code != "PROVIDER_TASK_FAILED" {
		t.Fatalf("payload.error.code = %q, want %q", payload.Error.Code, "PROVIDER_TASK_FAILED")
	}
	if payload.Error.Message == "" {
		t.Fatal("payload.error.message = \"\", want a non-empty description")
	}
	// A Provider-reported failure is carried in the callback body, not by refusing
	// delivery: the callback is still one well-formed, correctly authenticated POST.
	if calls[0].Token != "tok-failed" {
		t.Fatalf("Calls()[0].Token = %q, want the unmodified callback token", calls[0].Token)
	}
}

func TestServer_Tasks_WrongTokenScenario_DeliversMismatchedTokenAndDoesNotRetry(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	receiver.RejectTokensOtherThan("tok-real")

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 2)
	dispatcher.notify = notify
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{
		CallbackURL:   receiver.server.URL,
		CallbackToken: "tok-real",
		Outcome:       outcomeWrongToken,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	outcomes := waitForCalls(t, notify, 1)
	// 08 §4's 401 is this scenario's expected result, not a transport failure: the
	// Dispatcher reports one completed attempt and must not treat it as retryable.
	if outcomes[0].Err != nil {
		t.Fatalf("outcome.Err = %v, want nil: a rejected callback is still one completed delivery", outcomes[0].Err)
	}

	// Shutdown drains every Dispatcher goroutine, so any retry would have to surface here.
	if err := dispatcher.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}
	calls := receiver.Calls()
	if len(calls) != 1 {
		t.Fatalf("len(Calls()) = %d, want exactly 1: a 401 must not be retried", len(calls))
	}
	if calls[0].Token == "tok-real" {
		t.Fatal("Calls()[0].Token = the real token, want a deliberately mismatched token")
	}
	if calls[0].Token == "" {
		t.Fatal("Calls()[0].Token = \"\", want a mismatched but present token")
	}
	if calls[0].Status != http.StatusUnauthorized {
		t.Fatalf("receiver responded %d, want %d for a mismatched token", calls[0].Status, http.StatusUnauthorized)
	}
}

func TestServer_Tasks_SameIdempotencyKey_ReturnsSameTaskAndSendsOneCallback(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 2)
	dispatcher.notify = notify
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	req := taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-idem", IdempotencyKey: "key-1"}

	first := postJSON(t, srv.URL+"/v1/tasks", req)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first StatusCode = %d, want %d", first.StatusCode, http.StatusAccepted)
	}
	var firstBody taskResponse
	decodeBody(t, first, &firstBody)

	waitForCalls(t, notify, 1)

	second := postJSON(t, srv.URL+"/v1/tasks", req)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second StatusCode = %d, want %d for a replayed idempotency key", second.StatusCode, http.StatusOK)
	}
	var secondBody taskResponse
	decodeBody(t, second, &secondBody)
	if secondBody.ExternalTaskID != firstBody.ExternalTaskID {
		t.Fatalf("second ExternalTaskID = %q, want the first task's %q", secondBody.ExternalTaskID, firstBody.ExternalTaskID)
	}

	// Shutdown drains every scheduled goroutine, so a second callback would appear here.
	if err := dispatcher.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}
	if len(receiver.Calls()) != 1 {
		t.Fatalf("len(Calls()) = %d, want 1: a replayed idempotency key must not schedule another callback", len(receiver.Calls()))
	}
}

func TestServer_Tasks_DifferentIdempotencyKeys_CreateDistinctTasks(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 2)
	dispatcher.notify = notify
	srv := httptest.NewServer(NewServer(dispatcher))
	defer srv.Close()

	first := postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-idem-a", IdempotencyKey: "key-a"})
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first StatusCode = %d, want %d", first.StatusCode, http.StatusAccepted)
	}
	var firstBody taskResponse
	decodeBody(t, first, &firstBody)

	second := postJSON(t, srv.URL+"/v1/tasks", taskRequest{CallbackURL: receiver.server.URL, CallbackToken: "tok-idem-b", IdempotencyKey: "key-b"})
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("second StatusCode = %d, want %d for a distinct idempotency key", second.StatusCode, http.StatusAccepted)
	}
	var secondBody taskResponse
	decodeBody(t, second, &secondBody)
	if secondBody.ExternalTaskID == firstBody.ExternalTaskID {
		t.Fatalf("both keys produced ExternalTaskID %q, want distinct tasks", firstBody.ExternalTaskID)
	}

	waitForCalls(t, notify, 2)
	if len(receiver.Calls()) != 2 {
		t.Fatalf("len(Calls()) = %d, want 2: distinct keys each schedule their own callback", len(receiver.Calls()))
	}
}

// TestServer_ServeImage_ReturnsTheSameDeterministicPNG covers the route an asynchronous
// task callback's EXTERNAL image reference points at: it must serve readable,
// credential-free image bytes, so the reference a caller publishes is not a dangling URL.
func TestServer_ServeImage_ReturnsTheSameDeterministicPNG(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	get := func(path string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("http.Get(%s) error = %v", path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s body: %v", path, err)
		}
		return resp, body
	}

	resp, first := get("/v1/images/55afbada5b95ee98.png")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want %q", got, "image/png")
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "max-age=") || !strings.Contains(got, "public") {
		t.Errorf("Cache-Control = %q, want a public, cacheable directive", got)
	}
	if !bytes.HasPrefix(first, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("body does not start with the PNG signature: %x", first)
	}

	// The same name and a different name both serve the identical fixed image: the route
	// stores nothing, so it is stable across requests and across server instances.
	if _, again := get("/v1/images/55afbada5b95ee98.png"); !bytes.Equal(first, again) {
		t.Error("two requests for the same image returned different bytes")
	}
	if _, other := get("/v1/images/0011223344556677.png"); !bytes.Equal(first, other) {
		t.Error("a different image name returned different bytes, want one fixed deterministic image")
	}
}

func TestServer_ServeImage_RejectsAnythingButAPNGName(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	defer srv.Close()

	for name, path := range map[string]string{
		"other extension": "/v1/images/abc.jpg",
		"no extension":    "/v1/images/abc",
		"empty name":      "/v1/images/.png",
		"nested path":     "/v1/images/a/b.png",
		"collection":      "/v1/images",
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("http.Get(%s) error = %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want %d", path, resp.StatusCode, http.StatusNotFound)
			}
		})
	}
}
