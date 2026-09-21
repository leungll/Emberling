//go:build integration

package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// createFixtureDefinition POSTs the document_processing fixture and returns its
// workflowId and version, for tests that need a real, compiled, frozen Definition to run
// against.
func createFixtureDefinition(t *testing.T, env *testEnv) (workflowID string, version int) {
	t.Helper()
	fx := loadDocumentProcessingFixture(t)
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create fixture definition: status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ = created["workflowId"].(string)
	v, _ := created["version"].(float64)
	return workflowID, int(v)
}

// TestAPI_CreateRun_UnknownWorkflow_Returns404DefinitionNotFound covers POST /runs
// against a workflowId/version pair with no persisted Definition: writeError maps
// domain.ErrNotFound here to DEFINITION_NOT_FOUND (the notFoundCode createRun passes),
// not RUN_NOT_FOUND, since nothing about a Run is in question yet.
func TestAPI_CreateRun_UnknownWorkflow_Returns404DefinitionNotFound(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": "wf_does_not_exist", "definitionVersion": 1, "input": map[string]any{},
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /api/runs (unknown workflow) status = %d, want %d, body=%s", resp.StatusCode, http.StatusNotFound, body)
	}
	if code := errorCode(t, body); code != "DEFINITION_NOT_FOUND" {
		t.Errorf("POST /api/runs (unknown workflow) error.code = %q, want %q", code, "DEFINITION_NOT_FOUND")
	}
}

// TestAPI_CreateRun_InvalidInput_Returns400WithSchemaPaths covers CreateRun's
// RunInputInvalidError branch. Task brief note: the brief's own literal test name implies
// HTTP 422, but service.RunInputInvalidError's doc comment pins this to
// runtime.CodeValidationFailed's family, which docs/08-interface-spec.md §6 maps to 400
// ("请求内容无效"), not 422 (an invalid graph *shape*). This test asserts the code's own
// documented behavior (400), not the brief's literal number; see the final report's
// "undocumented decisions" section.
func TestAPI_CreateRun_InvalidInput_Returns400WithSchemaPaths(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	// document_processing.json's text_input node requires an inputKey "document"
	// (required: true); omitting it must fail runtime.ValidateRunInput against the
	// frozen runInputSchema.
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": map[string]any{},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/runs (missing required input) status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
	}
	env2 := decodeBody[map[string]any](t, body)
	errBody, ok := env2["error"].(map[string]any)
	if !ok {
		t.Fatalf("POST /api/runs (missing required input) response is not an error envelope: %s", body)
	}
	details, ok := errBody["details"].(map[string]any)
	if !ok {
		t.Fatalf("POST /api/runs (missing required input) error.details missing: %s", body)
	}
	errs, ok := details["errors"].([]any)
	if !ok || len(errs) == 0 {
		t.Fatalf("POST /api/runs (missing required input) error.details.errors missing or empty: %s", body)
	}
	first, _ := errs[0].(map[string]any)
	if path, _ := first["path"].(string); path == "" {
		t.Errorf("POST /api/runs (missing required input) error.details.errors[0].path is empty, want a JSON-pointer-style path: %s", body)
	}
}

// TestAPI_CreateRun_ValidInput_RunsToCompletion_SnapshotAndNodeRunDetailReflectResult
// covers the full pipeline end-to-end through the real production Node Types and the
// Mock Model Provider: POST /runs starts the Run, the work Pool advances it entirely off
// the request goroutine, and GET /runs/{id} eventually reports COMPLETED with the
// Text Output node's result as Run.output. It also covers GET /runs/{id}/nodes/{nodeRunId}
// (NodeRunDetail), folded into this test rather than a fourteenth test to stay within the
// track's 13-test budget: both endpoints read the same completed Run, so splitting them
// would only duplicate the run-to-completion setup.
func TestAPI_CreateRun_ValidInput_RunsToCompletion_SnapshotAndNodeRunDetailReflectResult(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"document": "Emberling is a durable agent runtime."},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs response missing id: %s", body)
	}
	if status, _ := created["status"].(string); status != "RUNNING" {
		t.Errorf("POST /api/runs response status = %q, want %q", status, "RUNNING")
	}

	snap := env.waitForTerminal(t, runID, 5*time.Second)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run reached a terminal status = %q, want %q; snapshot=%v", status, "COMPLETED", snap)
	}
	if _, ok := run["output"]; !ok {
		t.Errorf("completed Run snapshot missing output: %v", run)
	}

	nodeRuns, _ := snap["nodeRuns"].([]any)
	if len(nodeRuns) == 0 {
		t.Fatalf("completed Run snapshot has no nodeRuns: %v", snap)
	}
	var outputNodeRunID string
	for _, nr := range nodeRuns {
		m, _ := nr.(map[string]any)
		if nodeID, _ := m["nodeId"].(string); nodeID == "node_output" {
			outputNodeRunID, _ = m["id"].(string)
		}
	}
	if outputNodeRunID == "" {
		t.Fatalf("completed Run snapshot has no nodeRun for node_output: %v", nodeRuns)
	}

	detailResp, detailBody := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/nodes/"+outputNodeRunID, nil)
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/{id}/nodes/{nodeRunId} status = %d, want %d, body=%s", detailResp.StatusCode, http.StatusOK, detailBody)
	}
	detail := decodeBody[map[string]any](t, detailBody)
	attempts, _ := detail["attempts"].([]any)
	if len(attempts) == 0 {
		t.Fatalf("NodeRunDetail for node_output has no attempts: %s", detailBody)
	}
	firstAttempt, _ := attempts[0].(map[string]any)
	if _, ok := firstAttempt["callbackBinding"]; !ok {
		t.Errorf("NodeAttempt missing required callbackBinding key (Studio requires it non-optional, always null in M1): %s", detailBody)
	}
}

// TestAPI_GetRunEvents_JSON_ReturnsOrderedEventsAfterSeq covers GET /runs/{id}/events
// with the default (non-SSE) Accept header: a JSON page of the Run's Event history,
// filtered by afterSeq and returned in ascending seq order.
func TestAPI_GetRunEvents_JSON_ReturnsOrderedEventsAfterSeq(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	_, createBody := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"document": "Emberling is a durable agent runtime."},
	})
	created := decodeBody[map[string]any](t, createBody)
	runID, _ := created["id"].(string)
	env.waitForTerminal(t, runID, 5*time.Second)

	resp, body := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/events", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/{id}/events status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	list := decodeBody[map[string]any](t, body)
	items, ok := list["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("GET /api/runs/{id}/events items missing or empty: %s", body)
	}
	var lastSeq float64 = -1
	for i, it := range items {
		m, _ := it.(map[string]any)
		seq, _ := m["seq"].(float64)
		if seq <= lastSeq {
			t.Fatalf("event[%d].seq = %v, want strictly greater than previous %v (ascending order)", i, seq, lastSeq)
		}
		lastSeq = seq
	}

	// afterSeq must exclude everything up to and including the given cursor.
	resp2, body2 := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/events?afterSeq=1", nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/{id}/events?afterSeq=1 status = %d, want %d, body=%s", resp2.StatusCode, http.StatusOK, body2)
	}
	list2 := decodeBody[map[string]any](t, body2)
	items2, _ := list2["items"].([]any)
	for _, it := range items2 {
		m, _ := it.(map[string]any)
		seq, _ := m["seq"].(float64)
		if seq <= 1 {
			t.Errorf("GET /api/runs/{id}/events?afterSeq=1 returned seq=%v, want > 1", seq)
		}
	}
}

// TestAPI_GetRunEvents_SSE_StreamsEventsAndClosesOnTerminal covers the
// text/event-stream branch of GET /runs/{id}/events: it must deliver every Event as an
// SSE frame and close the connection once the Run reaches a terminal status and the
// cursor has caught up, rather than hanging forever (docs/08-interface-spec.md §5).
func TestAPI_GetRunEvents_SSE_StreamsEventsAndClosesOnTerminal(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	_, createBody := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"document": "Emberling is a durable agent runtime."},
	})
	created := decodeBody[map[string]any](t, createBody)
	runID, _ := created["id"].(string)

	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/api/runs/"+runID+"/events", nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/runs/{id}/events (SSE): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/{id}/events (SSE) status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("GET /api/runs/{id}/events (SSE) Content-Type = %q, want text/event-stream", ct)
	}

	// The server closes the connection once the Run is terminal and the cursor has
	// caught up (stream.go's own termination rule), so reading to EOF is itself the
	// assertion that the stream ends rather than hanging: a bounded client Timeout above
	// turns a regression (the stream never closing) into a normal test failure instead of
	// hanging the whole suite.
	buf := make([]byte, 4096)
	var raw []byte
	for {
		n, readErr := resp.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if readErr != nil {
			break
		}
	}
	if len(raw) == 0 {
		t.Fatal("GET /api/runs/{id}/events (SSE) stream closed with zero bytes read")
	}
	frameCount := strings.Count(string(raw), "event: ")
	if frameCount == 0 {
		t.Fatalf("GET /api/runs/{id}/events (SSE) stream contained no SSE frames: %q", raw)
	}
	if !strings.Contains(string(raw), "id: ") {
		t.Errorf("GET /api/runs/{id}/events (SSE) frames missing \"id: \" (Last-Event-ID replay id): %q", raw)
	}
}

// TestAPI_CreateRun_PinsRequestedDefinitionVersion covers CLAUDE.md's "A Run remains
// bound to one immutable Definition version": creating v2 of a Definition with a
// materially different node_prompt template must not affect a Run explicitly pinned to
// v1 via POST /runs's definitionVersion field - neither its reported version nor the
// template actually applied while executing it.
func TestAPI_CreateRun_PinsRequestedDefinitionVersion(t *testing.T) {
	env := newTestEnv(t)
	fx := loadDocumentProcessingFixture(t)

	createResp, createBody := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (v1) status = %d, want %d, body=%s", createResp.StatusCode, http.StatusCreated, createBody)
	}
	created := decodeBody[map[string]any](t, createBody)
	workflowID, _ := created["workflowId"].(string)
	v1, _ := created["version"].(float64)
	if workflowID == "" || int(v1) != 1 {
		t.Fatalf("POST /api/definitions (v1) response = %s, want workflowId set and version=1", createBody)
	}

	// v2: identical graph shape, but node_prompt's template is changed to something
	// trivially distinguishable from v1's own template text in the final output.
	v2Nodes := make([]domain.Node, len(fx.Nodes))
	copy(v2Nodes, fx.Nodes)
	for i, n := range v2Nodes {
		if n.ID == "node_prompt" {
			n.Config = json.RawMessage(`{"template": "REVISED_V2_TEMPLATE {{text}}"}`)
			v2Nodes[i] = n
		}
	}
	saveResp, saveBody := env.doJSON(t, http.MethodPut, "/api/definitions/"+workflowID, map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": v2Nodes, "edges": fx.Edges,
		"baseVersion": int(v1),
	})
	if saveResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/definitions/%s (v2) status = %d, want %d, body=%s", workflowID, saveResp.StatusCode, http.StatusOK, saveBody)
	}
	saved := decodeBody[map[string]any](t, saveBody)
	if v2, _ := saved["version"].(float64); int(v2) != 2 {
		t.Fatalf("PUT /api/definitions/%s (v2) response version = %v, want 2", workflowID, saved["version"])
	}

	// POST /runs pinned to definitionVersion: 1 must run against v1's template, never v2's,
	// even though v2 is now the Definition's latest saved version.
	runResp, runBody := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": 1,
		"input": map[string]any{"document": sseTestDocument},
	})
	if runResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (pinned v1) status = %d, want %d, body=%s", runResp.StatusCode, http.StatusCreated, runBody)
	}
	runCreated := decodeBody[map[string]any](t, runBody)
	runID, _ := runCreated["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs (pinned v1) response missing id: %s", runBody)
	}

	snap := env.waitForTerminal(t, runID, 5*time.Second)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("pinned Run reached status = %q, want %q: %v", status, "COMPLETED", run)
	}
	if dv, _ := run["definitionVersion"].(float64); int(dv) != 1 {
		t.Errorf("pinned Run.definitionVersion = %v, want 1", run["definitionVersion"])
	}

	outputRaw, err := json.Marshal(run["output"])
	if err != nil {
		t.Fatalf("marshal Run.output for inspection: %v", err)
	}
	output := string(outputRaw)
	if strings.Contains(output, "REVISED_V2_TEMPLATE") {
		t.Errorf("Run pinned to definitionVersion=1 produced output containing v2's template marker: %s", output)
	}
	if !strings.Contains(output, "Summarise the following document") {
		t.Errorf("Run pinned to definitionVersion=1 output does not reflect v1's own template text: %s", output)
	}
}
