//go:build integration

package contract

import (
	"net/http"
	"testing"
	"time"
)

// TestAPI_CreateDefinition_ValidDefinition_Returns201WithFrozenRunInputSchema covers
// POST /definitions (docs/08-interface-spec.md §3.1): a compilable graph is persisted as
// version 1 and the response carries the frozen runInputSchema the Compiler produced.
func TestAPI_CreateDefinition_ValidDefinition_Returns201WithFrozenRunInputSchema(t *testing.T) {
	env := newTestEnv(t)
	fx := loadDocumentProcessingFixture(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	def := decodeBody[map[string]any](t, body)
	if def["workflowId"] == "" || def["workflowId"] == nil {
		t.Errorf("POST /api/definitions response workflowId is empty: %s", body)
	}
	if v, _ := def["version"].(float64); v != 1 {
		t.Errorf("POST /api/definitions response version = %v, want 1", def["version"])
	}
	if _, ok := def["runInputSchema"]; !ok {
		t.Errorf("POST /api/definitions response missing runInputSchema key: %s", body)
	}
}

// TestAPI_CreateDefinition_CycleGraph_Returns422WithStructuredErrors covers the
// DAG_HAS_CYCLE branch of writeError's compile-failure mapping: docs/08-interface-spec.md
// §6 groups an invalid graph shape under HTTP 422, and the response body must carry the
// same structured, bounded ValidationError list POST /definitions/validate would.
func TestAPI_CreateDefinition_CycleGraph_Returns422WithStructuredErrors(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", cyclicDefinitionRequest())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /api/definitions (cycle) status = %d, want %d, body=%s", resp.StatusCode, http.StatusUnprocessableEntity, body)
	}
	env2 := decodeBody[map[string]any](t, body)
	errBody, ok := env2["error"].(map[string]any)
	if !ok {
		t.Fatalf("POST /api/definitions (cycle) response is not an error envelope: %s", body)
	}
	if code, _ := errBody["code"].(string); code != "DAG_HAS_CYCLE" {
		t.Errorf("POST /api/definitions (cycle) error.code = %q, want %q", code, "DAG_HAS_CYCLE")
	}
	details, ok := errBody["details"].(map[string]any)
	if !ok {
		t.Fatalf("POST /api/definitions (cycle) error.details missing: %s", body)
	}
	errs, ok := details["errors"].([]any)
	if !ok || len(errs) == 0 {
		t.Fatalf("POST /api/definitions (cycle) error.details.errors missing or empty: %s", body)
	}
	first, ok := errs[0].(map[string]any)
	if !ok {
		t.Fatalf("POST /api/definitions (cycle) error.details.errors[0] is not an object: %s", body)
	}
	if _, ok := first["path"]; !ok {
		t.Errorf("POST /api/definitions (cycle) error.details.errors[0] missing required \"path\" key (validationErrorDTO normalization): %s", body)
	}
}

// TestAPI_ValidateDefinition_InvalidGraph_Returns200WithValidFalse covers the
// docs/08-interface-spec.md §3.1 design decision that /definitions/validate always
// answers 200 with a discriminated result, never the error envelope: an invalid
// Definition is a successful validation outcome, not a failed request.
func TestAPI_ValidateDefinition_InvalidGraph_Returns200WithValidFalse(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions/validate", cyclicDefinitionRequest())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/definitions/validate (cycle) status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	result := decodeBody[map[string]any](t, body)
	if valid, _ := result["valid"].(bool); valid {
		t.Errorf("POST /api/definitions/validate (cycle) valid = true, want false")
	}
	if _, ok := result["runInputSchema"]; ok {
		t.Errorf("POST /api/definitions/validate (cycle) response carries runInputSchema alongside valid=false: %s", body)
	}
	errs, ok := result["errors"].([]any)
	if !ok || len(errs) == 0 {
		t.Fatalf("POST /api/definitions/validate (cycle) errors missing or empty: %s", body)
	}
}

// TestAPI_ValidateDefinition_ValidGraph_Returns200WithRunInputSchema is the positive
// control: a compilable graph answers 200 with valid=true and the runInputSchema, and no
// Definition is ever persisted by this endpoint (nothing here asserts persistence
// directly since the endpoint has no side effect to observe, but GET /definitions is
// exercised by a separate test with an actually-created Definition).
func TestAPI_ValidateDefinition_ValidGraph_Returns200WithRunInputSchema(t *testing.T) {
	env := newTestEnv(t)
	fx := loadDocumentProcessingFixture(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions/validate", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/definitions/validate status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	result := decodeBody[map[string]any](t, body)
	if valid, _ := result["valid"].(bool); !valid {
		t.Fatalf("POST /api/definitions/validate valid = false, want true, body=%s", body)
	}
	if _, ok := result["runInputSchema"]; !ok {
		t.Errorf("POST /api/definitions/validate valid=true response missing runInputSchema: %s", body)
	}
}

// TestAPI_GetDefinition_UnknownWorkflow_Returns404DefinitionNotFound covers the
// notFoundCode plumbing through writeError for GET /definitions/{workflowId}.
func TestAPI_GetDefinition_UnknownWorkflow_Returns404DefinitionNotFound(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.doJSON(t, http.MethodGet, "/api/definitions/wf_does_not_exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/definitions/{unknown} status = %d, want %d, body=%s", resp.StatusCode, http.StatusNotFound, body)
	}
	if code := errorCode(t, body); code != "DEFINITION_NOT_FOUND" {
		t.Errorf("GET /api/definitions/{unknown} error.code = %q, want %q", code, "DEFINITION_NOT_FOUND")
	}
}

// TestAPI_SaveDefinition_StaleBaseVersion_Returns409VersionConflict covers PUT
// /definitions/{workflowId} racing a stale baseVersion against the domain.ErrVersionConflict
// branch of writeError (docs/09-testing-and-acceptance.md §3.4).
func TestAPI_SaveDefinition_StaleBaseVersion_Returns409VersionConflict(t *testing.T) {
	env := newTestEnv(t)
	fx := loadDocumentProcessingFixture(t)

	_, createBody := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	created := decodeBody[map[string]any](t, createBody)
	workflowID, _ := created["workflowId"].(string)
	if workflowID == "" {
		t.Fatalf("create response missing workflowId: %s", createBody)
	}

	resp, body := env.doJSON(t, http.MethodPut, "/api/definitions/"+workflowID, map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
		"baseVersion": 999,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT /api/definitions/{id} with stale baseVersion status = %d, want %d, body=%s", resp.StatusCode, http.StatusConflict, body)
	}
	if code := errorCode(t, body); code != "VERSION_CONFLICT" {
		t.Errorf("PUT /api/definitions/{id} with stale baseVersion error.code = %q, want %q", code, "VERSION_CONFLICT")
	}
}

// TestAPI_ListDefinitions_IncludesDescriptionAndNullLastRun covers definitionListItemDTO's
// full shape (docs/08-interface-spec.md §3.1): description is carried through, lastRun is
// JSON null (not an omitted key) for a Definition that has never run and populated for one
// that has, and the list is ordered by updatedAt descending (the more recently
// touched/created Definition first).
func TestAPI_ListDefinitions_IncludesDescriptionAndNullLastRun(t *testing.T) {
	env := newTestEnv(t)
	fx := loadDocumentProcessingFixture(t)

	// Workflow A: created first, never run.
	respA, bodyA := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": "Workflow A", "description": "First workflow, never run", "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if respA.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (A) status = %d, want %d, body=%s", respA.StatusCode, http.StatusCreated, bodyA)
	}
	createdA := decodeBody[map[string]any](t, bodyA)
	workflowIDA, _ := createdA["workflowId"].(string)

	// Workflow B: created second (so its updatedAt is later than A's), and run to
	// completion so its lastRun is populated.
	respB, bodyB := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": "Workflow B", "description": "Second workflow, has a run", "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if respB.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (B) status = %d, want %d, body=%s", respB.StatusCode, http.StatusCreated, bodyB)
	}
	createdB := decodeBody[map[string]any](t, bodyB)
	workflowIDB, _ := createdB["workflowId"].(string)
	versionB, _ := createdB["version"].(float64)

	runResp, runBody := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowIDB, "definitionVersion": int(versionB),
		"input": map[string]any{"document": sseTestDocument},
	})
	if runResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (B) status = %d, want %d, body=%s", runResp.StatusCode, http.StatusCreated, runBody)
	}
	runCreated := decodeBody[map[string]any](t, runBody)
	runIDB, _ := runCreated["id"].(string)
	env.waitForTerminal(t, runIDB, 5*time.Second)

	listResp, listBody := env.doJSON(t, http.MethodGet, "/api/definitions", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/definitions status = %d, want %d, body=%s", listResp.StatusCode, http.StatusOK, listBody)
	}
	list := decodeBody[map[string]any](t, listBody)
	items, ok := list["items"].([]any)
	if !ok || len(items) < 2 {
		t.Fatalf("GET /api/definitions items missing or fewer than 2: %s", listBody)
	}

	byWorkflowID := map[string]map[string]any{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		id, _ := m["workflowId"].(string)
		byWorkflowID[id] = m
	}
	itemA, ok := byWorkflowID[workflowIDA]
	if !ok {
		t.Fatalf("GET /api/definitions response missing workflow A (%s): %s", workflowIDA, listBody)
	}
	itemB, ok := byWorkflowID[workflowIDB]
	if !ok {
		t.Fatalf("GET /api/definitions response missing workflow B (%s): %s", workflowIDB, listBody)
	}

	if desc, _ := itemA["description"].(string); desc != "First workflow, never run" {
		t.Errorf("workflow A description = %q, want %q", desc, "First workflow, never run")
	}
	if desc, _ := itemB["description"].(string); desc != "Second workflow, has a run" {
		t.Errorf("workflow B description = %q, want %q", desc, "Second workflow, has a run")
	}
	if lv, _ := itemA["latestVersion"].(float64); int(lv) != 1 {
		t.Errorf("workflow A latestVersion = %v, want 1", itemA["latestVersion"])
	}

	lastRunRaw, hasKey := itemA["lastRun"]
	if !hasKey {
		t.Errorf("workflow A (never run) response omits the \"lastRun\" key entirely, want it present and null: %s", listBody)
	} else if lastRunRaw != nil {
		t.Errorf("workflow A (never run) lastRun = %v, want null", lastRunRaw)
	}

	lastRunB, ok := itemB["lastRun"].(map[string]any)
	if !ok {
		t.Fatalf("workflow B (has a run) lastRun is not an object: %v", itemB["lastRun"])
	}
	if id, _ := lastRunB["id"].(string); id != runIDB {
		t.Errorf("workflow B lastRun.id = %q, want %q", id, runIDB)
	}
	if status, _ := lastRunB["status"].(string); status != "COMPLETED" {
		t.Errorf("workflow B lastRun.status = %q, want %q", status, "COMPLETED")
	}
	if _, ok := lastRunB["createdAt"]; !ok {
		t.Errorf("workflow B lastRun missing createdAt: %v", lastRunB)
	}

	// Ordering: B was created (and run) after A, so B's updatedAt must be >= A's, and B
	// must sort before A in the descending-by-updatedAt list.
	indexOf := func(workflowID string) int {
		for i, it := range items {
			m, _ := it.(map[string]any)
			if id, _ := m["workflowId"].(string); id == workflowID {
				return i
			}
		}
		return -1
	}
	if indexOf(workflowIDB) >= indexOf(workflowIDA) {
		t.Errorf("GET /api/definitions order: workflow B (index %d) is not before workflow A (index %d); want updatedAt descending", indexOf(workflowIDB), indexOf(workflowIDA))
	}
}

// TestAPI_CreateDefinition_AIGCGraph_WithMediaOutput_Validates proves the last missing
// piece of the AIGC media scenario (docs/01-scenarios.md, docs/02-scope.md §3.2): a
// Definition ending in the built-in media_output Node Type passes the same server-side
// validation chain (ConfigSchema, ValidateSemantics, DAG/connectivity/unique-Output-sink)
// every other Node Type goes through. Without media_output registered, POST /definitions
// would reject "output" with UNKNOWN_NODE_TYPE and the graph could never reach a unique,
// reachable Output Node sink.
func TestAPI_CreateDefinition_AIGCGraph_WithMediaOutput_Validates(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", aigcMediaGraphRequest())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (AIGC graph with media_output) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	def := decodeBody[map[string]any](t, body)
	if def["workflowId"] == "" || def["workflowId"] == nil {
		t.Errorf("POST /api/definitions (AIGC graph with media_output) response workflowId is empty: %s", body)
	}
	if _, ok := def["runInputSchema"]; !ok {
		t.Errorf("POST /api/definitions (AIGC graph with media_output) response missing runInputSchema key: %s", body)
	}

	// The registered Node Metadata Catalog (docs/08-interface-spec.md §3.1 GET
	// /node-types) must list media_output under the Output category, the same category
	// text_output uses, so Studio's palette (which owns no node catalog of its own) can
	// render it.
	catalogResp, catalogBody := env.doJSON(t, http.MethodGet, "/api/node-types", nil)
	if catalogResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/node-types status = %d, want %d, body=%s", catalogResp.StatusCode, http.StatusOK, catalogBody)
	}
	catalog := decodeBody[map[string]any](t, catalogBody)
	items, ok := catalog["items"].([]any)
	if !ok {
		t.Fatalf("GET /api/node-types response missing items: %s", catalogBody)
	}
	var mediaOutputMeta map[string]any
	for _, it := range items {
		m, _ := it.(map[string]any)
		if typ, _ := m["type"].(string); typ == "media_output" {
			mediaOutputMeta = m
			break
		}
	}
	if mediaOutputMeta == nil {
		t.Fatalf("GET /api/node-types response does not list media_output: %s", catalogBody)
	}
	if category, _ := mediaOutputMeta["category"].(string); category != "Output" {
		t.Errorf("GET /api/node-types media_output category = %q, want %q", category, "Output")
	}
}
