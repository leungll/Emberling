//go:build integration

package contract

import (
	"bytes"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
)

// runListItemKeys is the complete key set of one GET /api/runs item; nullable keys are
// always present.
var runListItemKeys = []string{
	"completedAt", "cost", "createdAt", "definitionVersion", "error", "failedStep", "id", "latestVersion",
	"startedAt", "status", "waitingSteps", "workflowId", "workflowName",
}

func (e *testEnv) listRuns(t *testing.T, query url.Values) map[string]any {
	t.Helper()
	resp, body := e.doJSON(t, http.MethodGet, "/api/runs?"+query.Encode(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs?%s status = %d, body=%s", query.Encode(), resp.StatusCode, body)
	}
	return decodeBody[map[string]any](t, body)
}

func listItemIDs(t *testing.T, page map[string]any) []string {
	t.Helper()
	items, ok := page["items"].([]any)
	if !ok {
		t.Fatalf("items is not an array: %v", page["items"])
	}
	ids := make([]string, len(items))
	for i, raw := range items {
		item, _ := raw.(map[string]any)
		ids[i], _ = item["id"].(string)
	}
	return ids
}

func (e *testEnv) createCompletedFixtureRun(t *testing.T, workflowID string, version int) string {
	t.Helper()
	resp, body := e.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": map[string]any{"document": "hello"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	e.waitForTerminal(t, runID, e2eWait)
	return runID
}

// TestAPI_ListRuns_FailedAndCompletedRuns_ItemShapeCountsAndLastRun runs one Workflow to
// FAILED (the image dispatch fails at the transport) and another to COMPLETED, then checks
// the Run list item shape, the server-side counts, that a failed item carries only the
// error code and its failed step, and the same two fields on the Definitions list lastRun.
func TestAPI_ListRuns_FailedAndCompletedRuns_ItemShapeCountsAndLastRun(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{
		MockTaskBaseURL: "http://mockprovider:9101",
		TaskClient:      &http.Client{Transport: redactingTaskTransport{}},
	})

	failedRunID := env.createAIGCRun(t, "a small ember creature")
	failedSnap := env.waitForTerminal(t, failedRunID, e2eWait)
	failedRun, _ := failedSnap["run"].(map[string]any)
	if status, _ := failedRun["status"].(string); status != "FAILED" {
		t.Fatalf("AIGC Run status = %q, want FAILED", status)
	}
	runError, _ := failedRun["error"].(map[string]any)
	wantCode, _ := runError["code"].(string)
	wantMessage, _ := runError["message"].(string)
	if wantCode == "" || wantMessage == "" {
		t.Fatalf("failed Run error = %v, want a code and a message", runError)
	}

	workflowID, version := createFixtureDefinition(t, env)
	completedRunID := env.createCompletedFixtureRun(t, workflowID, version)

	resp, body := env.doJSON(t, http.MethodGet, "/api/runs", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	if bytes.Contains(body, []byte(wantMessage)) || bytes.Contains(body, []byte(`"input"`)) || bytes.Contains(body, []byte(`"output"`)) {
		t.Errorf("Run list carries an error message, input or output: %s", body)
	}
	page := decodeBody[map[string]any](t, body)
	for _, key := range []string{"nextCursor", "prevCursor"} {
		if v, ok := page[key]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want present and null on a single page", key, v, ok)
		}
	}
	counts, _ := page["counts"].(map[string]any)
	wantCounts := map[string]any{"all": 2.0, "running": 0.0, "paused": 0.0, "completed": 1.0, "failed": 1.0}
	for k, v := range wantCounts {
		if counts[k] != v {
			t.Errorf("counts.%s = %v, want %v (counts %v)", k, counts[k], v, counts)
		}
	}
	if ids := listItemIDs(t, page); !slices.Equal(ids, []string{completedRunID, failedRunID}) {
		t.Fatalf("items = %v, want newest first [%s %s]", ids, completedRunID, failedRunID)
	}

	items, _ := page["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if keys := sortedKeys(item); !slices.Equal(keys, runListItemKeys) {
			t.Errorf("item %v keys = %v, want %v", item["id"], keys, runListItemKeys)
		}
		if item["createdAt"] != item["startedAt"] {
			t.Errorf("item %v createdAt %v != startedAt %v", item["id"], item["createdAt"], item["startedAt"])
		}
		if steps, ok := item["waitingSteps"].([]any); !ok || len(steps) != 0 {
			t.Errorf("item %v waitingSteps = %v, want an empty array", item["id"], item["waitingSteps"])
		}
		if item["latestVersion"] != 1.0 || item["definitionVersion"] != 1.0 {
			t.Errorf("item %v versions = %v of %v, want 1 of 1", item["id"], item["definitionVersion"], item["latestVersion"])
		}
		if name, _ := item["workflowName"].(string); name == "" {
			t.Errorf("item %v workflowName is empty", item["id"])
		}
	}

	completed, _ := items[0].(map[string]any)
	if completed["error"] != nil || completed["failedStep"] != nil || completed["completedAt"] == nil {
		t.Errorf("completed item = %v, want null error and failedStep and a completedAt", completed)
	}
	failed, _ := items[1].(map[string]any)
	errObj, _ := failed["error"].(map[string]any)
	if len(errObj) != 1 || errObj["code"] != wantCode {
		t.Errorf("failed item error = %v, want exactly {code: %s}", failed["error"], wantCode)
	}
	step, _ := failed["failedStep"].(map[string]any)
	if keys := sortedKeys(step); !slices.Equal(keys, []string{"nodeId", "nodeName"}) || step["nodeId"] != "image" {
		t.Errorf("failed item failedStep = %v, want {nodeId: image, nodeName}", failed["failedStep"])
	}

	// The Definitions list's lastRun carries the same two fields.
	_, defsBody := env.doJSON(t, http.MethodGet, "/api/definitions", nil)
	if bytes.Contains(defsBody, []byte(wantMessage)) {
		t.Errorf("Definitions list carries the Run error message: %s", defsBody)
	}
	defs := decodeBody[map[string]any](t, defsBody)
	defItems, _ := defs["items"].([]any)
	seen := 0
	for _, raw := range defItems {
		def, _ := raw.(map[string]any)
		lastRun, _ := def["lastRun"].(map[string]any)
		if keys := sortedKeys(lastRun); !slices.Equal(keys, []string{"createdAt", "error", "failedStep", "id", "status"}) {
			t.Errorf("lastRun keys = %v", keys)
		}
		switch lastRun["id"] {
		case failedRunID:
			seen++
			e, _ := lastRun["error"].(map[string]any)
			s, _ := lastRun["failedStep"].(map[string]any)
			if len(e) != 1 || e["code"] != wantCode || s["nodeId"] != "image" {
				t.Errorf("failed lastRun = %v, want error {code: %s} and failedStep image", lastRun, wantCode)
			}
		case completedRunID:
			seen++
			if lastRun["error"] != nil || lastRun["failedStep"] != nil {
				t.Errorf("completed lastRun = %v, want null error and failedStep", lastRun)
			}
		}
	}
	if seen != 2 {
		t.Errorf("Definitions list lastRun matched %d of the 2 Runs: %s", seen, defsBody)
	}
}

// TestAPI_ListRuns_CursorPaging_WalksBothWaysAndBindsFilters pages three Runs one at a
// time forward and back, and refuses a cursor once a filter changes.
func TestAPI_ListRuns_CursorPaging_WalksBothWaysAndBindsFilters(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)
	var created []string
	for i := 0; i < 3; i++ {
		created = append(created, env.createCompletedFixtureRun(t, workflowID, version))
	}
	slices.Reverse(created)

	q := url.Values{"workflowId": {workflowID}, "limit": {"1"}}
	var (
		walked []string
		cursor string
		last   map[string]any
	)
	for i := 0; i < 3; i++ {
		page := env.listRuns(t, withCursor(q, cursor))
		walked = append(walked, listItemIDs(t, page)...)
		if (page["prevCursor"] == nil) != (i == 0) {
			t.Errorf("page %d prevCursor = %v", i, page["prevCursor"])
		}
		last = page
		if i < 2 {
			cursor, _ = page["nextCursor"].(string)
			if cursor == "" {
				t.Fatalf("page %d nextCursor missing", i)
			}
		}
	}
	if last["nextCursor"] != nil {
		t.Errorf("last page nextCursor = %v, want null", last["nextCursor"])
	}
	if !slices.Equal(walked, created) {
		t.Fatalf("forward walk = %v, want %v", walked, created)
	}

	prev, _ := last["prevCursor"].(string)
	back := env.listRuns(t, withCursor(q, prev))
	if ids := listItemIDs(t, back); !slices.Equal(ids, created[1:2]) {
		t.Errorf("prevCursor page = %v, want %v", ids, created[1:2])
	}

	// The same cursor under a different status filter is refused.
	changed := withCursor(q, prev)
	changed.Set("status", "FAILED")
	resp, body := env.doJSON(t, http.MethodGet, "/api/runs?"+changed.Encode(), nil)
	if resp.StatusCode != http.StatusBadRequest || errorCode(t, body) != "VALIDATION_FAILED" {
		t.Errorf("cursor under changed filter: status %d body %s, want 400 VALIDATION_FAILED", resp.StatusCode, body)
	}

	// The status filter narrows items but not counts; the maximum page size is accepted.
	filtered := env.listRuns(t, url.Values{"workflowId": {workflowID}, "status": {"FAILED"}, "limit": {"100"}})
	if ids := listItemIDs(t, filtered); len(ids) != 0 {
		t.Errorf("status=FAILED items = %v, want none", ids)
	}
	if counts, _ := filtered["counts"].(map[string]any); counts["all"] != 3.0 || counts["completed"] != 3.0 {
		t.Errorf("status=FAILED counts = %v, want all and completed 3", counts)
	}

	// A short code finds the Run whose ID it starts.
	code := created[0][len("run_") : len("run_")+8]
	byCode := env.listRuns(t, url.Values{"code": {code}})
	if ids := listItemIDs(t, byCode); !slices.Contains(ids, created[0]) {
		t.Errorf("code=%s items = %v, want to include %s", code, ids, created[0])
	}
	// The full Run ID resolves through the same parameter to exactly that Run.
	byFullID := env.listRuns(t, url.Values{"code": {created[0]}})
	if ids := listItemIDs(t, byFullID); !slices.Equal(ids, []string{created[0]}) {
		t.Errorf("code=%s items = %v, want only that Run", created[0], ids)
	}
}

func withCursor(q url.Values, cursor string) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = slices.Clone(v)
	}
	if cursor != "" {
		out.Set("cursor", cursor)
	}
	return out
}

// TestAPI_ListRuns_UnknownWorkflow_ReturnsEmptyPageWithZeroCounts: a Workflow that does
// not exist is an empty result, not a 404.
func TestAPI_ListRuns_UnknownWorkflow_ReturnsEmptyPageWithZeroCounts(t *testing.T) {
	env := newTestEnv(t)
	page := env.listRuns(t, url.Values{"workflowId": {"wf_does_not_exist"}})
	if items, ok := page["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want an empty array", page["items"])
	}
	counts, _ := page["counts"].(map[string]any)
	for _, k := range []string{"all", "running", "paused", "completed", "failed"} {
		if counts[k] != 0.0 {
			t.Errorf("counts.%s = %v, want 0", k, counts[k])
		}
	}
}

// TestAPI_ListRuns_InvalidQuery_Returns400ValidationFailed covers every rejected query
// parameter value.
func TestAPI_ListRuns_InvalidQuery_Returns400ValidationFailed(t *testing.T) {
	env := newTestEnv(t)
	for name, q := range map[string]url.Values{
		"unknown status":         {"status": {"DONE"}},
		"limit zero":             {"limit": {"0"}},
		"limit above maximum":    {"limit": {"101"}},
		"limit not an integer":   {"limit": {"ten"}},
		"startedAfter not 3339":  {"startedAfter": {"2026-08-03"}},
		"code too short":         {"code": {"abc123"}},
		"code with punctuation":  {"code": {"abcd-12345"}},
		"cursor not base64":      {"cursor": {"!!!"}},
		"cursor forged contents": {"cursor": {"eyJ2IjoxLCJkIjoib2xkZXIiLCJ0IjoxLCJpZCI6InJ1bl94IiwiZiI6IngifQ"}},
	} {
		resp, body := env.doJSON(t, http.MethodGet, "/api/runs?"+q.Encode(), nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body=%s", name, resp.StatusCode, body)
			continue
		}
		if code := errorCode(t, body); code != "VALIDATION_FAILED" {
			t.Errorf("%s: error.code = %q, want VALIDATION_FAILED", name, code)
		}
	}
}

// TestAPI_ListRuns_DefinitionVersion_FiltersAndPagesWithinVersion saves a second version
// of a Workflow, runs both versions, and checks that definitionVersion narrows the items
// and the counts, that a cursor issued under the version pages within it and is refused
// once the version changes, and that the parameter is rejected without workflowId or with
// a value that is not a positive integer.
func TestAPI_ListRuns_DefinitionVersion_FiltersAndPagesWithinVersion(t *testing.T) {
	env := newTestEnv(t)
	workflowID, v1 := createFixtureDefinition(t, env)
	fx := loadDocumentProcessingFixture(t)
	resp, body := env.doJSON(t, http.MethodPut, "/api/definitions/"+workflowID, map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges, "baseVersion": v1,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/definitions/%s status = %d, body=%s", workflowID, resp.StatusCode, body)
	}
	v2f, _ := decodeBody[map[string]any](t, body)["version"].(float64)
	v2 := int(v2f)

	// Versions alternate, so a version page always skips Runs of the other version.
	var v1Runs, v2Runs []string
	for i := 0; i < 3; i++ {
		v1Runs = append(v1Runs, env.createCompletedFixtureRun(t, workflowID, v1))
		v2Runs = append(v2Runs, env.createCompletedFixtureRun(t, workflowID, v2))
	}
	slices.Reverse(v1Runs)
	slices.Reverse(v2Runs)

	q := url.Values{"workflowId": {workflowID}, "definitionVersion": {strconv.Itoa(v2)}, "limit": {"2"}}
	first := env.listRuns(t, q)
	if ids := listItemIDs(t, first); !slices.Equal(ids, v2Runs[:2]) {
		t.Fatalf("version %d first page = %v, want %v", v2, ids, v2Runs[:2])
	}
	if counts, _ := first["counts"].(map[string]any); counts["all"] != 3.0 || counts["completed"] != 3.0 {
		t.Errorf("version %d counts = %v, want all and completed 3", v2, counts)
	}
	items, _ := first["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["definitionVersion"] != float64(v2) || item["latestVersion"] != float64(v2) {
			t.Errorf("item %v versions = %v of %v, want %d of %d", item["id"], item["definitionVersion"], item["latestVersion"], v2, v2)
		}
	}
	next, _ := first["nextCursor"].(string)
	if next == "" {
		t.Fatal("first page nextCursor missing")
	}
	second := env.listRuns(t, withCursor(q, next))
	if ids := listItemIDs(t, second); !slices.Equal(ids, v2Runs[2:]) {
		t.Errorf("version %d second page = %v, want %v", v2, ids, v2Runs[2:])
	}
	if second["nextCursor"] != nil || second["prevCursor"] == nil {
		t.Errorf("second page cursors = next %v prev %v, want null next and a prev", second["nextCursor"], second["prevCursor"])
	}
	prev, _ := second["prevCursor"].(string)
	if ids := listItemIDs(t, env.listRuns(t, withCursor(q, prev))); !slices.Equal(ids, v2Runs[:2]) {
		t.Errorf("version %d page back = %v, want %v", v2, ids, v2Runs[:2])
	}

	all := env.listRuns(t, url.Values{"workflowId": {workflowID}, "definitionVersion": {strconv.Itoa(v1)}})
	if ids := listItemIDs(t, all); !slices.Equal(ids, v1Runs) {
		t.Errorf("version %d items = %v, want %v", v1, ids, v1Runs)
	}

	for name, changed := range map[string]url.Values{
		"cursor under another version":        {"workflowId": {workflowID}, "definitionVersion": {strconv.Itoa(v1)}, "limit": {"2"}, "cursor": {next}},
		"cursor without the version":          {"workflowId": {workflowID}, "limit": {"2"}, "cursor": {next}},
		"version without workflow":            {"definitionVersion": {strconv.Itoa(v2)}},
		"version zero":                        {"workflowId": {workflowID}, "definitionVersion": {"0"}},
		"version negative":                    {"workflowId": {workflowID}, "definitionVersion": {"-1"}},
		"version not an integer":              {"workflowId": {workflowID}, "definitionVersion": {"two"}},
		"version with status but no workflow": {"definitionVersion": {"1"}, "status": {"FAILED"}},
	} {
		resp, body := env.doJSON(t, http.MethodGet, "/api/runs?"+changed.Encode(), nil)
		if resp.StatusCode != http.StatusBadRequest || errorCode(t, body) != "VALIDATION_FAILED" {
			t.Errorf("%s: status %d body %s, want 400 VALIDATION_FAILED", name, resp.StatusCode, body)
		}
	}

	// A version the Workflow never had is an empty page, not an error.
	empty := env.listRuns(t, url.Values{"workflowId": {workflowID}, "definitionVersion": {"99"}})
	if ids := listItemIDs(t, empty); len(ids) != 0 {
		t.Errorf("unknown version items = %v, want none", ids)
	}
	if counts, _ := empty["counts"].(map[string]any); counts["all"] != 0.0 {
		t.Errorf("unknown version counts = %v, want all 0", counts)
	}
}
