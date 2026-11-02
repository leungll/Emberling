//go:build integration

package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// postRunWithKey POSTs /api/runs carrying the given Idempotency-Key header values; no
// values sends no header.
func postRunWithKey(t *testing.T, env *testEnv, body map[string]any, keys ...string) (*http.Response, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/runs", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, key := range keys {
		req.Header.Add("Idempotency-Key", key)
	}
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/runs: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, respBody
}

func idemRunBody(workflowID string, version int, document string) map[string]any {
	return map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"document": document},
	}
}

// runCountOfWorkflow reads the Run list total for one Workflow.
func runCountOfWorkflow(t *testing.T, env *testEnv, workflowID string) int {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodGet, "/api/runs?workflowId="+workflowID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs: status %d body %s", resp.StatusCode, body)
	}
	page := decodeBody[map[string]any](t, body)
	items, _ := page["items"].([]any)
	return len(items)
}

func TestAPI_CreateRun_SameKeySameContent_Returns201Then200ReplayedWithOriginalRun(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	resp, body := postRunWithKey(t, env, idemRunBody(workflowID, version, "Emberling is durable."), "order-42")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first keyed POST status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	if got := resp.Header.Get("Idempotent-Replayed"); got != "" {
		t.Errorf("first keyed POST Idempotent-Replayed = %q, want absent", got)
	}
	created := decodeBody[map[string]any](t, body)
	runID, _ := created["id"].(string)

	env.waitForTerminal(t, runID, 5*time.Second)

	resp, body = postRunWithKey(t, env, idemRunBody(workflowID, version, "Emberling is durable."), "order-42")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replayed POST status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	if got := resp.Header.Get("Idempotent-Replayed"); got != "true" {
		t.Errorf("replayed POST Idempotent-Replayed = %q, want %q", got, "true")
	}
	replayed := decodeBody[map[string]any](t, body)
	if id, _ := replayed["id"].(string); id != runID {
		t.Errorf("replayed POST id = %q, want the original %q", id, runID)
	}
	// The replay reports the Run's current state, not the state at creation.
	if status, _ := replayed["status"].(string); status != "COMPLETED" {
		t.Errorf("replayed POST status = %q, want the current %q", status, "COMPLETED")
	}
	for _, field := range []string{"workflowId", "definitionVersion", "lastSeq"} {
		if _, ok := replayed[field]; !ok {
			t.Errorf("replayed POST body missing %q: %s", field, body)
		}
	}
	if n := runCountOfWorkflow(t, env, workflowID); n != 1 {
		t.Errorf("runs of workflow after replay = %d, want 1", n)
	}
}

func TestAPI_CreateRun_SameKeyDifferentContent_Returns409MismatchAndNoNewRun(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	resp, body := postRunWithKey(t, env, idemRunBody(workflowID, version, "first"), "order-43")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first keyed POST status = %d, body=%s", resp.StatusCode, body)
	}

	resp, body = postRunWithKey(t, env, idemRunBody(workflowID, version, "second"), "order-43")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("mismatched POST status = %d, want %d, body=%s", resp.StatusCode, http.StatusConflict, body)
	}
	if code := errorCode(t, body); code != "IDEMPOTENCY_KEY_MISMATCH" {
		t.Errorf("mismatched POST error.code = %q, want %q", code, "IDEMPOTENCY_KEY_MISMATCH")
	}
	if strings.Contains(string(body), "order-43") {
		t.Errorf("mismatched POST error echoes the key: %s", body)
	}
	if n := runCountOfWorkflow(t, env, workflowID); n != 1 {
		t.Errorf("runs of workflow after mismatch = %d, want 1", n)
	}
}

func TestAPI_CreateRun_NoKey_Returns201ForEverySubmission(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		resp, body := postRunWithKey(t, env, idemRunBody(workflowID, version, "same content"))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("unkeyed POST %d status = %d, want %d, body=%s", i, resp.StatusCode, http.StatusCreated, body)
		}
		if got := resp.Header.Get("Idempotent-Replayed"); got != "" {
			t.Errorf("unkeyed POST %d Idempotent-Replayed = %q, want absent", i, got)
		}
		created := decodeBody[map[string]any](t, body)
		id, _ := created["id"].(string)
		ids[id] = true
	}
	if len(ids) != 2 {
		t.Errorf("unkeyed submissions: want two distinct Runs, got %v", ids)
	}
}

func TestAPI_CreateRun_InvalidKey_Returns400AndNoRun(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	for name, keys := range map[string][]string{
		"empty":         {""},
		"too long":      {strings.Repeat("k", 129)},
		"non-ASCII":     {"commande-é"},
		"tab character": {"key\tvalue"},
		"two values":    {"key-a", "key-b"},
	} {
		resp, body := postRunWithKey(t, env, idemRunBody(workflowID, version, "content"), keys...)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d, body=%s", name, resp.StatusCode, http.StatusBadRequest, body)
			continue
		}
		if code := errorCode(t, body); code != "VALIDATION_FAILED" {
			t.Errorf("%s: error.code = %q, want %q", name, code, "VALIDATION_FAILED")
		}
	}

	resp, body := postRunWithKey(t, env, idemRunBody(workflowID, version, "content"), strings.Repeat("k", 128))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("128-character key: status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	if n := runCountOfWorkflow(t, env, workflowID); n != 1 {
		t.Errorf("runs of workflow = %d, want only the one valid-key submission", n)
	}
}
