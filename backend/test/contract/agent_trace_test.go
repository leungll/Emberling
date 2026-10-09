//go:build integration

// Agent Trace query contract:
// GET /runs/{runId}/nodes/{nodeRunId}/agent projects the persisted Agent Run, its Turns,
// each Turn's committed Decision and Action, and every Tool Attempt, in persisted order.
//
// Everything here is asserted through the HTTP response only: the projection is read-only,
// so what it must NOT carry (model request/response bodies, Tool input/result, Context and
// State contents, Decision arguments, storage keys, callback token hashes, Provider
// credentials) is as much part of the contract as what it must carry.
package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// agentTraceWait bounds how long an Agent Run driven through the real work Pool may take
// to reach its terminal status. It is a safety net against a hung binary, never a
// synchronization device.
const agentTraceWait = 20 * time.Second

// agentTraceToolKey is the lookup Tool argument the scripted TOOL_CALL Decision carries.
// The Tool's own result ("record for <key>") is one of the strings the secrecy test
// proves never reaches the Trace response.
const agentTraceToolKey = "ember-trace"

// agentTraceFinalOutput is the scripted FINAL output. It is the Agent NodeRun's output and
// therefore the downstream text_output's, but it is still a model payload and must not
// appear in the Agent Trace projection.
const agentTraceFinalOutput = "the traced final answer"

// scriptToolCallThenFinal makes the fixture model decide one TOOL_CALL on its first Turn
// and FINAL on every later one, so a Run drives exactly two Turns.
func scriptToolCallThenFinal(env *testEnv, key, finalOutput string) *atomic.Int32 {
	var calls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if n := calls.Add(1); n == 1 {
			toolName := lookup.ToolName
			return &mockmodel.Scenario{
				Kind:          mockmodel.ScenarioToolCall,
				ToolName:      toolName,
				ToolArguments: json.RawMessage(`{"key":"` + key + `"}`),
			}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: finalOutput}
	}
	return &calls
}

// saveAgentFixture saves test/fixtures/definitions/agent_lookup.json through the public
// API and returns its workflow identity.
func saveAgentFixture(t *testing.T, env *testEnv) (workflowID string, version int) {
	t.Helper()
	fx := loadAgentLookupFixture(t)
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (agent_lookup) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ = created["workflowId"].(string)
	v, _ := created["version"].(float64)
	if workflowID == "" {
		t.Fatalf("POST /api/definitions (agent_lookup) response missing workflowId: %s", body)
	}
	return workflowID, int(v)
}

// createAgentRun starts a Run of the agent fixture and returns its id.
func createAgentRun(t *testing.T, env *testEnv, workflowID string, version int, question string) string {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"question": question},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (agent_lookup) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs (agent_lookup) response missing id: %s", body)
	}
	return runID
}

// nodeRunIDOf returns the id of the NodeRun of nodeID in a Run Snapshot.
func nodeRunIDOf(t *testing.T, snapshot map[string]any, nodeID string) string {
	t.Helper()
	nodeRuns, _ := snapshot["nodeRuns"].([]any)
	for _, nr := range nodeRuns {
		m, _ := nr.(map[string]any)
		if id, _ := m["nodeId"].(string); id == nodeID {
			nodeRunID, _ := m["id"].(string)
			return nodeRunID
		}
	}
	t.Fatalf("snapshot has no nodeRun for node %q: %v", nodeID, snapshot["nodeRuns"])
	return ""
}

// getAgentTrace issues the Agent Trace query and returns the status code and the raw body,
// so a caller can assert on the decoded projection and on the literal bytes alike.
func getAgentTrace(t *testing.T, env *testEnv, runID, nodeRunID string) (int, []byte) {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/nodes/"+nodeRunID+"/agent", nil)
	return resp.StatusCode, body
}

// runCompletedAgentLoop drives one Run of the agent fixture through a TOOL_CALL round and
// a FINAL round and returns the Run id and its terminal Snapshot.
func runCompletedAgentLoop(t *testing.T, env *testEnv) (string, map[string]any) {
	t.Helper()
	_ = scriptToolCallThenFinal(env, agentTraceToolKey, agentTraceFinalOutput)
	workflowID, version := saveAgentFixture(t, env)
	runID := createAgentRun(t, env, workflowID, version, "what is the answer?")
	snapshot := env.waitForTerminal(t, runID, agentTraceWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("agent Run terminal status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}
	return runID, snapshot
}

// TestAgentTrace_CompletedTwoRoundLoop_ProjectsTurnsInOrder is the positive control: after
// a TOOL_CALL round and a FINAL round, the projection carries both Turns in turnNo order
// with their committed Decision, Action and Tool Attempts, and the Agent Run's
// FINAL_RESPONSE termination.
func TestAgentTrace_CompletedTwoRoundLoop_ProjectsTurnsInOrder(t *testing.T) {
	env := newTestEnv(t)
	runID, snapshot := runCompletedAgentLoop(t, env)
	agentNodeRunID := nodeRunIDOf(t, snapshot, "node_agent")

	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	trace := decodeBody[map[string]any](t, body)

	agentRun, _ := trace["agentRun"].(map[string]any)
	if agentRun == nil {
		t.Fatalf("agent trace has no agentRun: %s", body)
	}
	if got, _ := agentRun["nodeRunId"].(string); got != agentNodeRunID {
		t.Errorf("agentRun.nodeRunId = %q, want %q", got, agentNodeRunID)
	}
	if got, _ := agentRun["termination"].(string); got != "FINAL_RESPONSE" {
		t.Errorf("agentRun.termination = %v, want FINAL_RESPONSE", agentRun["termination"])
	}
	if got, _ := agentRun["currentTurnNo"].(float64); got != 2 {
		t.Errorf("agentRun.currentTurnNo = %v, want 2", agentRun["currentTurnNo"])
	}
	if agentRun["terminatedAt"] == nil {
		t.Errorf("agentRun.terminatedAt = null on a terminated Agent Run: %s", body)
	}
	if agentRun["error"] != nil {
		t.Errorf("agentRun.error = %v, want null on a FINAL_RESPONSE termination", agentRun["error"])
	}
	for _, key := range []string{"id", "nodeRunId", "termination", "currentTurnNo",
		"currentContextVersion", "currentStateVersion", "deadline", "terminatedAt", "error"} {
		if _, present := agentRun[key]; !present {
			t.Errorf("agentRun is missing the required key %q: %s", key, body)
		}
	}

	turns, _ := trace["turns"].([]any)
	if len(turns) != 2 {
		t.Fatalf("agent trace turns = %d, want 2: %s", len(turns), body)
	}

	first, _ := turns[0].(map[string]any)
	second, _ := turns[1].(map[string]any)
	if no, _ := first["turnNo"].(float64); no != 1 {
		t.Errorf("turns[0].turnNo = %v, want 1 (persisted order)", first["turnNo"])
	}
	if no, _ := second["turnNo"].(float64); no != 2 {
		t.Errorf("turns[1].turnNo = %v, want 2 (persisted order)", second["turnNo"])
	}
	for i, turn := range []map[string]any{first, second} {
		if got, _ := turn["status"].(string); got != "COMPLETED" {
			t.Errorf("turns[%d].status = %v, want COMPLETED", i, turn["status"])
		}
		if turn["startedAt"] == nil || turn["completedAt"] == nil {
			t.Errorf("turns[%d] timestamps = started %v completed %v, want both set", i, turn["startedAt"], turn["completedAt"])
		}
		if turn["error"] != nil {
			t.Errorf("turns[%d].error = %v, want null", i, turn["error"])
		}
	}

	firstDecision, _ := first["decision"].(map[string]any)
	if firstDecision == nil {
		t.Fatalf("turns[0].decision = nil, want the committed TOOL_CALL: %s", body)
	}
	if kind, _ := firstDecision["kind"].(string); kind != "TOOL_CALL" {
		t.Errorf("turns[0].decision.kind = %v, want TOOL_CALL", firstDecision["kind"])
	}
	if tool, _ := firstDecision["tool"].(string); tool != lookup.ToolName {
		t.Errorf("turns[0].decision.tool = %v, want %q", firstDecision["tool"], lookup.ToolName)
	}
	if patch, _ := firstDecision["hasStatePatch"].(bool); patch {
		t.Errorf("turns[0].decision.hasStatePatch = true, want false: the scripted Decision carries no patch")
	}

	secondDecision, _ := second["decision"].(map[string]any)
	if secondDecision == nil {
		t.Fatalf("turns[1].decision = nil, want the committed FINAL: %s", body)
	}
	if kind, _ := secondDecision["kind"].(string); kind != "FINAL" {
		t.Errorf("turns[1].decision.kind = %v, want FINAL", secondDecision["kind"])
	}
	if secondDecision["tool"] != nil {
		t.Errorf("turns[1].decision.tool = %v, want null on a FINAL Decision", secondDecision["tool"])
	}

	firstAction, _ := first["action"].(map[string]any)
	secondAction, _ := second["action"].(map[string]any)
	if firstAction == nil || secondAction == nil {
		t.Fatalf("both Turns must project their committed Action: %s", body)
	}
	if typ, _ := firstAction["type"].(string); typ != "TOOL_CALL" {
		t.Errorf("turns[0].action.type = %v, want TOOL_CALL", firstAction["type"])
	}
	if typ, _ := secondAction["type"].(string); typ != "FINAL" {
		t.Errorf("turns[1].action.type = %v, want FINAL", secondAction["type"])
	}
	for i, action := range []map[string]any{firstAction, secondAction} {
		if got, _ := action["status"].(string); got != "SUCCEEDED" {
			t.Errorf("turns[%d].action.status = %v, want SUCCEEDED", i, action["status"])
		}
		for _, key := range []string{"id", "type", "status", "startedAt", "completedAt", "error"} {
			if _, present := action[key]; !present {
				t.Errorf("turns[%d].action is missing the required key %q: %s", i, key, body)
			}
		}
	}

	firstAttempts, _ := first["toolAttempts"].([]any)
	if len(firstAttempts) != 1 {
		t.Fatalf("turns[0].toolAttempts = %d, want exactly 1: %s", len(firstAttempts), body)
	}
	attempt, _ := firstAttempts[0].(map[string]any)
	if name, _ := attempt["toolName"].(string); name != lookup.ToolName {
		t.Errorf("turns[0].toolAttempts[0].toolName = %v, want %q", attempt["toolName"], lookup.ToolName)
	}
	if no, _ := attempt["attemptNo"].(float64); no != 1 {
		t.Errorf("turns[0].toolAttempts[0].attemptNo = %v, want 1", attempt["attemptNo"])
	}
	if got, _ := attempt["status"].(string); got != "SUCCEEDED" {
		t.Errorf("turns[0].toolAttempts[0].status = %v, want SUCCEEDED", attempt["status"])
	}
	if value, present := attempt["callbackBinding"]; !present || value != nil {
		t.Errorf("turns[0].toolAttempts[0].callbackBinding = %v (present=%v), want the key present and null for a synchronous Tool",
			value, present)
	}
	if attempt["dispatchedAt"] != nil {
		t.Errorf("turns[0].toolAttempts[0].dispatchedAt = %v, want null for a synchronous Tool", attempt["dispatchedAt"])
	}
	for _, key := range []string{"id", "toolName", "attemptNo", "status", "callbackBinding",
		"startedAt", "dispatchedAt", "completedAt", "error"} {
		if _, present := attempt[key]; !present {
			t.Errorf("turns[0].toolAttempts[0] is missing the required key %q: %s", key, body)
		}
	}

	secondAttempts, _ := second["toolAttempts"].([]any)
	if len(secondAttempts) != 0 {
		t.Errorf("turns[1].toolAttempts = %d, want 0: a FINAL Action calls no Tool", len(secondAttempts))
	}
}

// TestAgentTrace_NodeRunNotAgent_404 covers a NodeRun that exists in the Run but is not a
// MANAGED_AGENT NodeRun: it has no Agent Run, so there is no Agent Trace resource at that
// path and the query answers 404 NODE_RUN_NOT_FOUND rather than an empty projection.
func TestAgentTrace_NodeRunNotAgent_404(t *testing.T) {
	env := newTestEnv(t)
	runID, snapshot := runCompletedAgentLoop(t, env)
	outputNodeRunID := nodeRunIDOf(t, snapshot, "node_output")

	status, body := getAgentTrace(t, env, runID, outputNodeRunID)
	if status != http.StatusNotFound {
		t.Fatalf("GET agent trace of a non-Agent NodeRun status = %d, want %d, body=%s", status, http.StatusNotFound, body)
	}
	if code := errorCode(t, body); code != "NODE_RUN_NOT_FOUND" {
		t.Errorf("GET agent trace of a non-Agent NodeRun error.code = %q, want NODE_RUN_NOT_FOUND", code)
	}
}

// TestAgentTrace_NodeRunOfOtherRun_404 proves a caller can never read another Run's Agent
// facts by reusing a NodeRun id: the NodeRun exists, but not in the Run named in the path.
func TestAgentTrace_NodeRunOfOtherRun_404(t *testing.T) {
	env := newTestEnv(t)
	firstRunID, firstSnapshot := runCompletedAgentLoop(t, env)
	agentNodeRunID := nodeRunIDOf(t, firstSnapshot, "node_agent")

	// A second Run of the same Definition, so the path names a real Run and a real
	// NodeRun that simply do not belong together.
	resp, body := env.doJSON(t, http.MethodGet, "/api/runs/"+firstRunID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s status = %d, body=%s", firstRunID, resp.StatusCode, body)
	}
	snapshot := decodeBody[map[string]any](t, body)
	run, _ := snapshot["run"].(map[string]any)
	workflowID, _ := run["workflowId"].(string)
	version, _ := run["definitionVersion"].(float64)

	secondRunID := createAgentRun(t, env, workflowID, int(version), "a second question")
	env.waitForTerminal(t, secondRunID, agentTraceWait)

	status, traceBody := getAgentTrace(t, env, secondRunID, agentNodeRunID)
	if status != http.StatusNotFound {
		t.Fatalf("GET agent trace of another Run's NodeRun status = %d, want %d, body=%s", status, http.StatusNotFound, traceBody)
	}
	if code := errorCode(t, traceBody); code != "NODE_RUN_NOT_FOUND" {
		t.Errorf("GET agent trace of another Run's NodeRun error.code = %q, want NODE_RUN_NOT_FOUND", code)
	}
}

// TestAgentTrace_ResponseCarriesNoSecretsOrPayloads is the negative half of the contract
// (Trace projection and operational redaction rules): the projection never carries model
// request/response bodies, Tool input or result, Context or State contents, Decision
// arguments, a storage key, a callback token hash or a Provider credential.
func TestAgentTrace_ResponseCarriesNoSecretsOrPayloads(t *testing.T) {
	env := newTestEnv(t)
	runID, snapshot := runCompletedAgentLoop(t, env)
	agentNodeRunID := nodeRunIDOf(t, snapshot, "node_agent")

	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	raw := string(body)

	// Exact payload strings the Run really produced: the Tool's deterministic result, the
	// Tool argument it was derived from, and the model's FINAL output text.
	for _, forbidden := range []string{
		"record for " + agentTraceToolKey,
		agentTraceToolKey,
		agentTraceFinalOutput,
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("agent trace response contains the payload %q: %s", forbidden, raw)
		}
	}

	// Field names whose presence would mean a payload or a secret was projected at all.
	// "statePatch" is case-sensitive on purpose: the allowed boolean is `hasStatePatch`.
	for _, forbidden := range []string{"messages", "arguments", "statePatch", "request", "response",
		"instructions", "input", "result", "output"} {
		if strings.Contains(raw, `"`+forbidden+`"`) {
			t.Errorf("agent trace response carries the field %q: %s", forbidden, raw)
		}
	}
	for _, forbidden := range []string{"storage_key", "storagekey", "token", "authorization", "secret", "credential"} {
		if strings.Contains(strings.ToLower(withoutTokenUsageFieldNames(raw)), forbidden) {
			t.Errorf("agent trace response contains %q: %s", forbidden, raw)
		}
	}
}

// TestNodeRunDetail_AgentNodeRun_AttemptsEmpty pins the Node Detail rule that a
// MANAGED_AGENT NodeRun creates no Node Attempt, so the existing Node Detail query
// returns an empty attempts array and the Agent internals are reached through /agent
// instead.
func TestNodeRunDetail_AgentNodeRun_AttemptsEmpty(t *testing.T) {
	env := newTestEnv(t)
	runID, snapshot := runCompletedAgentLoop(t, env)
	agentNodeRunID := nodeRunIDOf(t, snapshot, "node_agent")

	resp, body := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/nodes/"+agentNodeRunID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET node detail of the Agent NodeRun status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	detail := decodeBody[map[string]any](t, body)
	attempts, present := detail["attempts"]
	if !present {
		t.Fatalf("node detail of the Agent NodeRun is missing attempts: %s", body)
	}
	list, ok := attempts.([]any)
	if !ok {
		t.Fatalf("node detail attempts = %v, want an array: %s", attempts, body)
	}
	if len(list) != 0 {
		t.Errorf("node detail attempts for a MANAGED_AGENT NodeRun = %d, want 0: %s", len(list), body)
	}
	nodeRun, _ := detail["nodeRun"].(map[string]any)
	if got, _ := nodeRun["nodeType"].(string); got != "agent" {
		t.Errorf("node detail nodeRun.nodeType = %v, want agent", nodeRun["nodeType"])
	}
}
