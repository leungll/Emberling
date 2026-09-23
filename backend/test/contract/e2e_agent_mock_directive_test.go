//go:build integration

// This file is the M5 slice 5.3b acceptance: the deterministic Mock Model Provider must be
// able to emit an Agent TOOL_CALL Decision with non-empty arguments when driven only
// through cmd/emberling's public HTTP surface (a Run's input), not through the in-process
// mockmodel.Provider.Script hook every other Agent-async contract test in this package
// uses. That hook is a Go closure held by the test binary; Studio and Playwright can never
// reach it, so the "Agent with asynchronous Tool" scenario (docs/09-testing-and-acceptance.md
// §1 DoD row 8) was unreachable from Studio before this directive extension existed.
//
// remote_lookup (internal/tools/remotelookup) requires a non-empty "key" argument
// (InputSchema minLength 1), so a TOOL_CALL with empty arguments fails
// runtime.ValidateToolCall as INVALID_ACTION before the Tool is ever dispatched. Before
// the mockmodel "mock:tool-call:<name>:<json-arguments>" extension, the "mock:tool-call:"
// directive text could only ever produce empty arguments, so this scenario could never
// reach WAITING_CALLBACK over HTTP.
package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
)

// createAgentAsyncRunFromDirective saves the Agent fixture with remote_lookup as its only
// allowed Tool, same as createAgentAsyncRun, but starts the Run with question set to a
// literal "mock:" directive instead of scripting env.provider.Script. This is the one path
// that proves the scenario is reachable without the in-process Script hook: env.provider
// is never touched here.
func createAgentAsyncRunFromDirective(t *testing.T, env *testEnv, directive string) string {
	t.Helper()
	fx := loadAgentLookupFixture(t)
	for i, node := range fx.Nodes {
		if node.ID == "node_agent" {
			fx.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, `["`+remotelookup.ToolName+`"]`, 1))
		}
	}
	resp, raw := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, raw)
	}
	created := decodeBody[map[string]any](t, raw)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	return createAgentRun(t, env, workflowID, int(version), directive)
}

// TestE2E_AgentAsyncTool_MockDirectiveWithArguments_DispatchesAndCompletesOverHTTP drives
// Definition -> Run -> Agent Turn 1 -> TOOL_CALL remote_lookup{key} -> Provider /v1/tasks ->
// WAITING_CALLBACK -> Provider callback -> Turn 2 -> FINAL -> Run COMPLETED using only the
// public HTTP surface: no test-only Script override selects the Decision. The Run's own
// input carries the "mock:tool-call:remote_lookup:<json>" directive that
// internal/adapters/mockmodel.Provider now parses into a non-empty argument set.
func TestE2E_AgentAsyncTool_MockDirectiveWithArguments_DispatchesAndCompletesOverHTTP(t *testing.T) {
	fx := newProviderFixture(t)
	env := fx.startBackend(t, nil)

	directive := `mock:tool-call:remote_lookup:{"key":"directive-k1"}`
	runID := createAgentAsyncRunFromDirective(t, env, directive)

	waiting := env.waitForNodeRunStatus(t, runID, agentFixtureNodeID, "WAITING_CALLBACK", agentE2EWait)
	agentNodeRunID, _ := waiting["id"].(string)

	dispatches := fx.tasks.dispatches()
	if len(dispatches) != 1 {
		t.Fatalf("provider received %d dispatches, want 1", len(dispatches))
	}
	var sent struct {
		Payload struct {
			Key string `json:"key"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(dispatches[0], &sent); err != nil {
		t.Fatalf("decode dispatch payload: %v; body=%s", err, dispatches[0])
	}
	if sent.Payload.Key != "directive-k1" {
		t.Fatalf("dispatched payload.key = %q, want %q (the directive's own \"key\" argument, "+
			"not an empty/derived one)", sent.Payload.Key, "directive-k1")
	}

	fx.callbacks.release()
	delivery := fx.waitDelivery(t, agentE2EWait)
	if delivery.StatusCode != http.StatusOK {
		t.Fatalf("callback delivery status = %d, want %d, body=%s", delivery.StatusCode, http.StatusOK, delivery.Body)
	}

	snapshot := env.waitForTerminal(t, runID, agentE2EWait)
	run, _ := snapshot["run"].(map[string]any)
	if got, _ := run["status"].(string); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}

	status, traceBody := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace: status = %d, body=%s", status, traceBody)
	}
	trace := decodeAgentAsyncTrace(t, traceBody)
	if len(trace.Turns) != 2 {
		t.Fatalf("agent trace turns = %d, want 2 (TOOL_CALL then FINAL); body=%s", len(trace.Turns), traceBody)
	}
	if len(trace.Turns[0].ToolAttempts) != 1 || trace.Turns[0].ToolAttempts[0].Status != "SUCCEEDED" {
		t.Fatalf("turn 1 tool attempts = %+v, want exactly one SUCCEEDED attempt", trace.Turns[0].ToolAttempts)
	}
}
