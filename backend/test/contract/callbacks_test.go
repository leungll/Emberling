//go:build integration

package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// callbackTokenHeaderForTest mirrors internal/api's unexported callbackTokenHeader
// constant. This test package cannot import that identifier -- it is unexported in a
// different package -- and duplicating the literal here, rather than adding an exported
// alias production code would otherwise never need, keeps the production surface
// unchanged.
const callbackTokenHeaderForTest = "X-Emberling-Callback-Token"

// ---- shared helpers for this file ----

// createAsyncEchoDefinition POSTs asyncEchoDefinitionRequest and returns its workflowId
// and version, for tests that need a real, compiled, frozen Definition around
// testAsyncNodeType to create a Run against.
func createAsyncEchoDefinition(t *testing.T, env *testEnv) (workflowID string, version int) {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", asyncEchoDefinitionRequest())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create async echo definition: status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ = created["workflowId"].(string)
	v, _ := created["version"].(float64)
	return workflowID, int(v)
}

// createAsyncEchoRun creates a Definition and a Run against it, returning the Run id.
func (e *testEnv) createAsyncEchoRun(t *testing.T, input map[string]any) string {
	t.Helper()
	workflowID, version := createAsyncEchoDefinition(t, e)
	resp, body := e.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": input,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs response missing id: %s", body)
	}
	return runID
}

// waitForNodeRunStatus polls GET /api/runs/{runId} until a NodeRun with the given nodeId
// reports status, returning that NodeRun's own JSON object. Ordinary HTTP-client polling
// of a real async server, same reasoning as waitForTerminal: the work Pool advances the
// Run off the request goroutine, entirely through PostgreSQL-committed facts, so there is
// no injectable barrier for "the dispatch transaction committed" from outside the process.
func (e *testEnv) waitForNodeRunStatus(t *testing.T, runID, nodeID, status string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, raw := e.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/runs/%s: status %d body %s", runID, resp.StatusCode, raw)
		}
		snap := decodeBody[map[string]any](t, raw)
		nodeRuns, _ := snap["nodeRuns"].([]any)
		for _, nr := range nodeRuns {
			m, _ := nr.(map[string]any)
			if id, _ := m["nodeId"].(string); id == nodeID {
				if s, _ := m["status"].(string); s == status {
					return m
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %q in run %s did not reach status %q within %s", nodeID, runID, status, timeout)
	return nil
}

// pendingCallbackExists reports whether a Pending Callback row is persisted for
// externalTaskID, read directly through the same store.UnitOfWork the wired services use
// -- no HTTP response exposes this fact, so an assertion that a rejected delivery
// "persisted nothing" needs this direct read.
func (e *testEnv) pendingCallbackExists(t *testing.T, externalTaskID string) bool {
	t.Helper()
	var found bool
	err := e.uow.WithinReadTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		_, err := tx.PendingCallbacks().GetByExternalTaskID(ctx, externalTaskID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		t.Fatalf("query pending callback %s: %v", externalTaskID, err)
	}
	return found
}

// listEvents returns every committed Event for runID via the plain JSON branch of
// GET /api/runs/{runId}/events (the default Accept header doJSON sends is not
// text/event-stream, so this always hits listEventsJSON, never the SSE stream).
func (e *testEnv) listEvents(t *testing.T, runID string) []map[string]any {
	t.Helper()
	resp, body := e.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/events", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s/events status = %d, body=%s", runID, resp.StatusCode, body)
	}
	env := decodeBody[map[string]any](t, body)
	items, _ := env["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		out = append(out, m)
	}
	return out
}

// doCallback POSTs {externalTaskId, payload} to /api/callbacks with token as the
// credential header. token == "" sends the request with no credential header at all
// (matching a client that never set one, the "missing" case the callback contract
// distinguishes from an empty one only in that neither carries a usable credential).
func (e *testEnv) doCallback(t *testing.T, token, externalTaskID string, payload map[string]any) (*http.Response, []byte) {
	t.Helper()
	reqBody := map[string]any{"externalTaskId": externalTaskID, "payload": payload}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal callback body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/api/callbacks", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("build callback request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(callbackTokenHeaderForTest, token)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/callbacks: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read callback response body: %v", err)
	}
	return resp, body
}

// doCallbackRaw POSTs an arbitrary raw body (not necessarily valid JSON), for the
// malformed-body and oversized-body tests.
func (e *testEnv) doCallbackRaw(t *testing.T, token string, rawBody []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/api/callbacks", bytes.NewReader(rawBody))
	if err != nil {
		t.Fatalf("build callback request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(callbackTokenHeaderForTest, token)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/callbacks: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read callback response body: %v", err)
	}
	return resp, body
}

// callbackResponseDTO decodes the {accepted, pending, duplicate} confirmation body.
type callbackResponseDTO struct {
	Accepted  bool `json:"accepted"`
	Pending   bool `json:"pending"`
	Duplicate bool `json:"duplicate"`
}

// syncBuffer is a concurrency-safe io.Writer, for a slog.Logger a test installs across a
// server whose handlers and work.Pool workers may log from multiple goroutines at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// -----------------------------------------------------------------------------------
// 1. Missing credential
// -----------------------------------------------------------------------------------

// TestAPI_Callback_MissingToken_Returns401AndPersistsNothing covers the credential-first
// contract: a callback with no X-Emberling-Callback-Token header at all is
// rejected before anything is read from the database, let alone written to it.
func TestAPI_Callback_MissingToken_Returns401AndPersistsNothing(t *testing.T) {
	env := newTestEnv(t)

	const externalTaskID = "no-token-task"
	resp, body := env.doCallback(t, "", externalTaskID, map[string]any{"status": "SUCCEEDED", "output": "x"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /api/callbacks (missing token) status = %d, want %d, body=%s", resp.StatusCode, http.StatusUnauthorized, body)
	}
	if code := errorCode(t, body); code != "INVALID_CALLBACK_CREDENTIAL" {
		t.Errorf("POST /api/callbacks (missing token) error.code = %q, want %q", code, "INVALID_CALLBACK_CREDENTIAL")
	}
	if env.pendingCallbackExists(t, externalTaskID) {
		t.Errorf("POST /api/callbacks (missing token) persisted a Pending Callback row for %q, want none", externalTaskID)
	}
}

// -----------------------------------------------------------------------------------
// 2. Tampered credential
// -----------------------------------------------------------------------------------

// TestAPI_Callback_TamperedToken_Returns401AndPersistsNothing covers a non-empty but
// invalid credential: verifyCallbackToken rejects it by signature alone, without ever
// reading the Callback Binding or writing a Pending Callback row. This
// uses a genuine dispatched Attempt's own token, tampered by one character, so the test
// proves tampering specifically -- not just "any garbage string" -- is rejected.
func TestAPI_Callback_TamperedToken_Returns401AndPersistsNothing(t *testing.T) {
	env := newTestEnv(t)
	runID := env.createAsyncEchoRun(t, map[string]any{"prompt": "hello"})
	env.waitForNodeRunStatus(t, runID, "async", "WAITING_CALLBACK", 5*time.Second)

	dispatch := env.dispatcher.last()
	if dispatch.Token == "" || dispatch.ExternalTaskID == "" {
		t.Fatalf("no dispatch recorded for run %s", runID)
	}
	tampered := tamperToken(dispatch.Token)

	resp, body := env.doCallback(t, tampered, dispatch.ExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "hi"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /api/callbacks (tampered token) status = %d, want %d, body=%s", resp.StatusCode, http.StatusUnauthorized, body)
	}
	if code := errorCode(t, body); code != "INVALID_CALLBACK_CREDENTIAL" {
		t.Errorf("POST /api/callbacks (tampered token) error.code = %q, want %q", code, "INVALID_CALLBACK_CREDENTIAL")
	}

	// The Attempt is already bound (WAITING_CALLBACK was reached), so "persists nothing"
	// here means no *new* Pending Callback row -- there is no pending row for a bound
	// external task id in the first place, since HandleCallback only ever stores one for
	// an *unbound* delivery.
	if env.pendingCallbackExists(t, dispatch.ExternalTaskID) {
		t.Errorf("POST /api/callbacks (tampered token) persisted a Pending Callback row for %q, want none", dispatch.ExternalTaskID)
	}

	// The NodeRun must still be exactly where it was: a rejected credential must not
	// resume it.
	nr := env.waitForNodeRunStatus(t, runID, "async", "WAITING_CALLBACK", 1*time.Second)
	if nr == nil {
		t.Fatalf("NodeRun for node \"async\" no longer WAITING_CALLBACK after a rejected callback")
	}
}

// tamperToken flips one character of the token's signature segment, which
// verifyCallbackToken checks in constant time before decoding anything else -- so this is
// guaranteed to fail signature verification, not accidentally still parse as some other
// valid attempt id.
func tamperToken(token string) string {
	if token == "" {
		return "x"
	}
	runes := []rune(token)
	last := runes[len(runes)-1]
	if last == 'A' {
		runes[len(runes)-1] = 'B'
	} else {
		runes[len(runes)-1] = 'A'
	}
	return string(runes)
}

// -----------------------------------------------------------------------------------
// 3. Malformed body
// -----------------------------------------------------------------------------------

// TestAPI_Callback_MalformedBody_Returns400InvalidCallbackPayload covers three ways a
// credentialed request can still fail to decode into {externalTaskId, payload}:
// invalid JSON, a missing externalTaskId, and a missing payload. None of these ever reach
// service.HandleCallback, so a valid-looking but otherwise unused token is enough.
func TestAPI_Callback_MalformedBody_Returns400InvalidCallbackPayload(t *testing.T) {
	env := newTestEnv(t)
	const token = "irrelevant-nonempty-token"

	t.Run("not JSON", func(t *testing.T) {
		resp, body := env.doCallbackRaw(t, token, []byte("this is not json"))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
		}
		if code := errorCode(t, body); code != "INVALID_CALLBACK_PAYLOAD" {
			t.Errorf("error.code = %q, want %q", code, "INVALID_CALLBACK_PAYLOAD")
		}
	})

	t.Run("missing externalTaskId", func(t *testing.T) {
		resp, body := env.doCallback(t, token, "", map[string]any{"status": "SUCCEEDED", "output": "x"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
		}
		if code := errorCode(t, body); code != "INVALID_CALLBACK_PAYLOAD" {
			t.Errorf("error.code = %q, want %q", code, "INVALID_CALLBACK_PAYLOAD")
		}
	})

	t.Run("missing payload", func(t *testing.T) {
		resp, body := env.doCallbackRaw(t, token, []byte(`{"externalTaskId":"some-task"}`))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
		}
		if code := errorCode(t, body); code != "INVALID_CALLBACK_PAYLOAD" {
			t.Errorf("error.code = %q, want %q", code, "INVALID_CALLBACK_PAYLOAD")
		}
	})
}

// -----------------------------------------------------------------------------------
// 4. Oversized body
// -----------------------------------------------------------------------------------

// TestAPI_Callback_BodyOverLimit_Returns413 covers the http.MaxBytesReader bound
// (config.PendingCallback.MaxPayloadBytes in production); this test wires a small limit
// through testEnvOptions.CallbackMaxPayloadBytes so the oversized body itself stays tiny.
func TestAPI_Callback_BodyOverLimit_Returns413(t *testing.T) {
	const limit = 64
	env := newTestEnvWithOptions(t, testEnvOptions{CallbackMaxPayloadBytes: limit})

	oversized := bytes.Repeat([]byte("a"), limit*4)
	rawBody, err := json.Marshal(map[string]any{
		"externalTaskId": "oversized-task",
		"payload":        map[string]any{"status": "SUCCEEDED", "output": string(oversized)},
	})
	if err != nil {
		t.Fatalf("marshal oversized body: %v", err)
	}
	if len(rawBody) <= limit {
		t.Fatalf("test body (%d bytes) is not actually larger than the limit (%d bytes)", len(rawBody), limit)
	}

	resp, body := env.doCallbackRaw(t, "irrelevant-nonempty-token", rawBody)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body=%s", resp.StatusCode, http.StatusRequestEntityTooLarge, body)
	}
	if code := errorCode(t, body); code != "PAYLOAD_TOO_LARGE" {
		t.Errorf("error.code = %q, want %q", code, "PAYLOAD_TOO_LARGE")
	}
}

// -----------------------------------------------------------------------------------
// 5. Delivery before the Callback Binding commits
// -----------------------------------------------------------------------------------

// TestAPI_Callback_BeforeBinding_Returns202Pending covers the Pending Callback path: a
// delivery that names an external task id with no Callback Binding yet is stored,
// answered 202 with pending=true, and -- once the dispatch transaction commits --
// consumed automatically without any further client action (consumeEarlyCallback:
// a persisted fact is recoverable work, the in-process resume is only a
// latency optimization).
//
// The barrier stalls fakeAsyncDispatcher.dispatch, which production code calls from
// inside Execute -- after the STARTED Attempt (and its callback token) already committed,
// before the DISPATCHED Attempt / WAITING_CALLBACK NodeRun / Callback Binding commit. That
// window is exactly "the credential is valid but nothing routes it yet".
func TestAPI_Callback_BeforeBinding_Returns202Pending(t *testing.T) {
	env := newTestEnv(t)
	barrier := newBarrierAtCall(1)
	env.dispatcher.BeforeReturn = barrier.beforeReturn

	runID := env.createAsyncEchoRun(t, map[string]any{"prompt": "hello"})
	barrier.waitEntered(t, 5*time.Second)

	dispatch := env.dispatcher.last()
	if dispatch.Token == "" || dispatch.ExternalTaskID == "" {
		t.Fatalf("dispatch barrier entered but recorded no dispatch")
	}

	resp, body := env.doCallback(t, dispatch.Token, dispatch.ExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "hi"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/callbacks (before binding) status = %d, want %d, body=%s", resp.StatusCode, http.StatusAccepted, body)
	}
	outcome := decodeBody[callbackResponseDTO](t, body)
	if !outcome.Accepted || !outcome.Pending || outcome.Duplicate {
		t.Errorf("POST /api/callbacks (before binding) outcome = %+v, want {Accepted:true Pending:true Duplicate:false}", outcome)
	}
	if !env.pendingCallbackExists(t, dispatch.ExternalTaskID) {
		t.Errorf("POST /api/callbacks (before binding) did not persist a Pending Callback row for %q", dispatch.ExternalTaskID)
	}

	barrier.Release()

	snap := env.waitForTerminal(t, runID, 5*time.Second)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("run reached terminal status = %q, want COMPLETED (early callback should have resumed it); snapshot=%v", status, snap)
	}
}

// -----------------------------------------------------------------------------------
// 6. First delivery after the binding exists
// -----------------------------------------------------------------------------------

// TestAPI_Callback_FirstDelivery_Returns200AndRunProceeds covers the ordinary path: once
// the Callback Binding has committed (NodeRun WAITING_CALLBACK), a matching delivery
// enters ResumeNode directly, answers 200 (not 202 -- nothing was pending), and the Run
// proceeds to COMPLETED with the Provider's reported output on the Output Node.
func TestAPI_Callback_FirstDelivery_Returns200AndRunProceeds(t *testing.T) {
	env := newTestEnv(t)
	runID := env.createAsyncEchoRun(t, map[string]any{"prompt": "hello"})
	env.waitForNodeRunStatus(t, runID, "async", "WAITING_CALLBACK", 5*time.Second)

	dispatch := env.dispatcher.last()
	if dispatch.Token == "" || dispatch.ExternalTaskID == "" {
		t.Fatalf("no dispatch recorded for run %s", runID)
	}

	resp, body := env.doCallback(t, dispatch.Token, dispatch.ExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "hello back"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/callbacks (first delivery) status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	outcome := decodeBody[callbackResponseDTO](t, body)
	if !outcome.Accepted || outcome.Pending || outcome.Duplicate {
		t.Errorf("POST /api/callbacks (first delivery) outcome = %+v, want {Accepted:true Pending:false Duplicate:false}", outcome)
	}

	snap := env.waitForTerminal(t, runID, 5*time.Second)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("run reached terminal status = %q, want COMPLETED; snapshot=%v", status, snap)
	}
	output, _ := run["output"].(map[string]any)
	if text, _ := output["text"].(string); text != "hello back" {
		t.Errorf("completed run output = %v, want {\"text\":\"hello back\"}", output)
	}
}

// -----------------------------------------------------------------------------------
// 7. Duplicate delivery
// -----------------------------------------------------------------------------------

// TestAPI_Callback_Duplicate_Returns200DuplicateWithoutNewEvents covers re-delivery of an
// already-resumed callback: ResumeNode finds the Attempt no longer DISPATCHED
// and writes nothing, so the second delivery answers 200 with duplicate=true and commits
// no additional Event -- the Event list is byte-for-byte identical before and after.
func TestAPI_Callback_Duplicate_Returns200DuplicateWithoutNewEvents(t *testing.T) {
	env := newTestEnv(t)
	runID := env.createAsyncEchoRun(t, map[string]any{"prompt": "hello"})
	env.waitForNodeRunStatus(t, runID, "async", "WAITING_CALLBACK", 5*time.Second)
	dispatch := env.dispatcher.last()

	firstResp, firstBody := env.doCallback(t, dispatch.Token, dispatch.ExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "hello back"})
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("first delivery status = %d, want %d, body=%s", firstResp.StatusCode, http.StatusOK, firstBody)
	}
	env.waitForTerminal(t, runID, 5*time.Second)

	eventsBefore := env.listEvents(t, runID)

	resp, body := env.doCallback(t, dispatch.Token, dispatch.ExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "hello back"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/callbacks (duplicate) status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	outcome := decodeBody[callbackResponseDTO](t, body)
	if !outcome.Accepted || outcome.Pending || !outcome.Duplicate {
		t.Errorf("POST /api/callbacks (duplicate) outcome = %+v, want {Accepted:true Pending:false Duplicate:true}", outcome)
	}

	eventsAfter := env.listEvents(t, runID)
	if len(eventsAfter) != len(eventsBefore) {
		t.Errorf("duplicate delivery changed the Event count: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
	lastSeq := func(events []map[string]any) any {
		if len(events) == 0 {
			return nil
		}
		return events[len(events)-1]["seq"]
	}
	if lastSeq(eventsBefore) != lastSeq(eventsAfter) {
		t.Errorf("duplicate delivery advanced lastSeq: before=%v after=%v", lastSeq(eventsBefore), lastSeq(eventsAfter))
	}
}

// -----------------------------------------------------------------------------------
// 8. NodeRunDetail projects the Callback Binding without its token/hash
// -----------------------------------------------------------------------------------

// TestAPI_NodeDetail_DispatchedAttempt_ProjectsCallbackBindingWithoutTokenHash covers the
// Callback Binding projection: GET /runs/{runId}/nodes/{nodeRunId} on a dispatched
// Attempt now projects a real callbackBinding object {id, providerId, externalTaskId,
// createdAt} instead of a pinned null, and that projection never carries the token or its
// hash (those four fields are the only ones a Callback Binding may expose).
func TestAPI_NodeDetail_DispatchedAttempt_ProjectsCallbackBindingWithoutTokenHash(t *testing.T) {
	env := newTestEnv(t)
	runID := env.createAsyncEchoRun(t, map[string]any{"prompt": "hello"})
	nr := env.waitForNodeRunStatus(t, runID, "async", "WAITING_CALLBACK", 5*time.Second)
	nodeRunID, _ := nr["id"].(string)
	if nodeRunID == "" {
		t.Fatalf("WAITING_CALLBACK NodeRun has no id: %v", nr)
	}
	dispatch := env.dispatcher.last()

	resp, body := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/nodes/"+nodeRunID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET NodeRunDetail status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}

	// The plaintext token itself (env.dispatcher recorded it at dispatch time, never
	// through any API response) must never appear in this projection.
	if dispatch.Token != "" && strings.Contains(string(body), dispatch.Token) {
		t.Fatalf("NodeRunDetail response contains the plaintext callback token: %s", body)
	}

	detail := decodeBody[map[string]any](t, body)
	attempts, _ := detail["attempts"].([]any)
	if len(attempts) == 0 {
		t.Fatalf("NodeRunDetail has no attempts: %s", body)
	}
	last, _ := attempts[len(attempts)-1].(map[string]any)
	binding, ok := last["callbackBinding"].(map[string]any)
	if !ok {
		t.Fatalf("attempts[-1].callbackBinding = %v (%T), want a {id, providerId, externalTaskId, createdAt} object", last["callbackBinding"], last["callbackBinding"])
	}
	// The allowed-fields check below is the authoritative guard against a stray
	// tokenHash (or any other) field ever being added to this projection.
	if id, _ := binding["id"].(string); id == "" {
		t.Errorf("callbackBinding.id is empty: %v", binding)
	}
	if providerID, _ := binding["providerId"].(string); providerID != "test-async-provider" {
		t.Errorf("callbackBinding.providerId = %q, want %q", providerID, "test-async-provider")
	}
	if externalTaskID, _ := binding["externalTaskId"].(string); externalTaskID != dispatch.ExternalTaskID {
		t.Errorf("callbackBinding.externalTaskId = %q, want %q", externalTaskID, dispatch.ExternalTaskID)
	}
	if createdAt, _ := binding["createdAt"].(string); createdAt == "" {
		t.Errorf("callbackBinding.createdAt is empty: %v", binding)
	}
	allowed := map[string]bool{"id": true, "providerId": true, "externalTaskId": true, "createdAt": true}
	for key := range binding {
		if !allowed[key] {
			t.Errorf("callbackBinding has unexpected field %q, want only {id, providerId, externalTaskId, createdAt}", key)
		}
	}
}

// -----------------------------------------------------------------------------------
// 9. Token never appears in a response or a log
// -----------------------------------------------------------------------------------

// TestAPI_Callback_ResponseAndLogs_NeverContainToken drives every callback path this file
// covers -- missing credential, tampered credential, pending, first delivery, duplicate --
// against one testEnv with a captured logger, and asserts the genuine plaintext token this
// Run's own dispatch minted appears in none of the response bodies and nowhere in the
// captured log output (CLAUDE.md: "Keep Provider credentials, authorization headers,
// callback tokens, signing secrets... out of business fields, Events, Trace, logs, and
// errors").
func TestAPI_Callback_ResponseAndLogs_NeverContainToken(t *testing.T) {
	logBuf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	env := newTestEnvWithOptions(t, testEnvOptions{Logger: logger})

	runID := env.createAsyncEchoRun(t, map[string]any{"prompt": "hello"})
	env.waitForNodeRunStatus(t, runID, "async", "WAITING_CALLBACK", 5*time.Second)
	dispatch := env.dispatcher.last()
	if dispatch.Token == "" {
		t.Fatalf("no dispatch recorded for run %s", runID)
	}
	token := dispatch.Token

	var responses [][]byte

	_, body := env.doCallback(t, "", "missing-token-task", map[string]any{"status": "SUCCEEDED", "output": "x"})
	responses = append(responses, body)

	_, body = env.doCallback(t, tamperToken(token), dispatch.ExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "x"})
	responses = append(responses, body)

	_, body = env.doCallback(t, token, dispatch.ExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "hello back"})
	responses = append(responses, body)
	env.waitForTerminal(t, runID, 5*time.Second)

	_, body = env.doCallback(t, token, dispatch.ExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "hello back"})
	responses = append(responses, body)

	for i, resp := range responses {
		if strings.Contains(string(resp), token) {
			t.Errorf("response #%d contains the plaintext callback token: %s", i, resp)
		}
	}
	if logged := logBuf.String(); strings.Contains(logged, token) {
		t.Errorf("captured log output contains the plaintext callback token:\n%s", logged)
	}
}
