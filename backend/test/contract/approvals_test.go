//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// testOperatorName is the deciding identity the harness configures for the approval routes.
const testOperatorName = "test-operator"

// testOperatorToken is the operator credential these tests configure. It is long enough
// for the deployment rule and distinctive enough that a leak into a log line or a response
// body is unambiguous.
const testOperatorToken = "operator-token-contract-test-5f1c7b2e9a4d3c6f8b1e"

// approvalWait bounds how long a Run driven through the real work Pool may take to reach
// the state a test waits for. It is a safety net against a hung binary, not a
// synchronization device.
const approvalWait = 20 * time.Second

// taskInputNodeType is a test Input Node that publishes the object its config holds on its
// text port, so the Agent node receives an input a goalPointer can resolve in.
const taskInputNodeType = "test_task_input"

type taskInputExecutor struct{}

func (taskInputExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (taskInputExecutor) Execute(_ context.Context, _ registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	raw, err := json.Marshal(config["task"])
	if err != nil {
		return registry.NodeResult{}, err
	}
	return registry.CompletedResult(map[string]json.RawMessage{"text": raw}), nil
}

func taskInputRegistration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          taskInputNodeType,
			DisplayName:   "Test Task Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{"task":{"type":"object"}},"required":["task"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: taskInputExecutor{}},
	}
}

// productionToolName is the synchronous test Tool whose declared risk needs a person's
// approval under FIX_AND_DEPLOY.
const productionToolName = "test_production_tool"

// productionToolTarget is the argument the scripted call carries; the approval detail must
// show it and nothing else may leak it.
const productionToolTarget = "orders-api"

type countingToolExecutor struct{ calls atomic.Int32 }

func (e *countingToolExecutor) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	e.calls.Add(1)
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: json.RawMessage(`{"ok":true}`)}}, nil
}

func productionToolRegistration(executor registry.ToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          productionToolName,
			Description:   "A test tool that changes a production system",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","minLength":1}},"required":["target"],"additionalProperties":false}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionSync,
			Authorization: &domain.ToolAuthorization{Risk: domain.ToolRiskProduction},
		},
		Executor: executor,
	}
}

// approvalEnv is one Backend with the approval routes served, the production Tool and the
// task input node registered, and its log output captured.
type approvalEnv struct {
	*testEnv
	executor *countingToolExecutor
	logs     *syncBuffer
}

func newApprovalEnv(t *testing.T, token string) *approvalEnv {
	t.Helper()
	executor := &countingToolExecutor{}
	logs := &syncBuffer{}
	env := newTestEnvWithOptions(t, testEnvOptions{
		OperatorToken:          token,
		ExtraNodeRegistrations: []registry.NodeRegistration{taskInputRegistration()},
		ExtraToolRegistrations: []registry.ToolRegistration{productionToolRegistration(executor)},
		Logger:                 slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return &approvalEnv{testEnv: env, executor: executor, logs: logs}
}

// doOperator is doJSON with the operator credential header; token == "" sends no header.
func (e *approvalEnv) doOperator(t *testing.T, token, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var reqBody *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = strings.NewReader(string(raw))
	} else {
		reqBody = strings.NewReader("")
	}
	req, err := http.NewRequest(method, e.server.URL+path, reqBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Emberling-Operator-Token", token)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		out.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	return resp, []byte(out.String())
}

// startApprovalRun saves a task_input -> agent -> text_output Definition whose Agent froze
// goal, scripts the model to call the production Tool once and then answer, starts a Run
// and returns its id once the Agent node waits for the person.
func (e *approvalEnv) startApprovalRun(t *testing.T, goal domain.AgentGoal) (runID, agentNodeRunID string) {
	t.Helper()
	var calls atomic.Int32
	e.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if calls.Add(1) == 1 {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: productionToolName,
				ToolArguments: json.RawMessage(`{"target":"` + productionToolTarget + `"}`)}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "the release is done"}
	}
	resp, body := e.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": "Approval Graph", "description": "task_input -> agent -> text_output",
		"nodes": []map[string]any{
			{"id": "node_input", "type": taskInputNodeType, "name": "Task", "position": map[string]any{"x": 0, "y": 0},
				"config": map[string]any{"task": map[string]any{"goal": string(goal)}}},
			{"id": "node_agent", "type": "agent", "name": "Release Agent", "position": map[string]any{"x": 200, "y": 0},
				"config": baseAgentConfig(map[string]any{
					"allowedTools": []string{productionToolName}, "goalPointer": "/goal", "approvalTimeoutMs": 3600000,
				})},
			{"id": "node_output", "type": "text_output", "name": "Answer", "position": map[string]any{"x": 400, "y": 0}, "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "edge_input_agent", "source": "node_input", "sourceHandle": "text", "target": "node_agent", "targetHandle": "input"},
			{"id": "edge_agent_output", "source": "node_agent", "sourceHandle": "text", "target": "node_output", "targetHandle": "text"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)

	resp, body = e.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": int(version), "input": map[string]any{},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ = decodeBody[map[string]any](t, body)["id"].(string)
	nodeRun := e.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", approvalWait)
	agentNodeRunID, _ = nodeRun["id"].(string)
	return runID, agentNodeRunID
}

// pendingApproval lists the PENDING approvals and returns the one of runID.
func (e *approvalEnv) pendingApproval(t *testing.T, runID string) map[string]any {
	t.Helper()
	resp, body := e.doOperator(t, testOperatorToken, http.MethodGet, "/api/approvals?status=PENDING", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/approvals status = %d, body=%s", resp.StatusCode, body)
	}
	items, _ := decodeBody[map[string]any](t, body)["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["runId"] == runID {
			return item
		}
	}
	t.Fatalf("no PENDING approval for run %s in %s", runID, body)
	return nil
}

func agentActionOf(t *testing.T, trace map[string]any, turnNo float64) (map[string]any, map[string]any) {
	t.Helper()
	turns, _ := trace["turns"].([]any)
	for _, raw := range turns {
		turn, _ := raw.(map[string]any)
		if turn["turnNo"] == turnNo {
			action, _ := turn["action"].(map[string]any)
			return turn, action
		}
	}
	t.Fatalf("trace has no turn %v: %v", turnNo, trace)
	return nil, nil
}

func eventTypeCounts(events []map[string]any) map[string]int {
	out := map[string]int{}
	for _, typ := range eventTypes(events) {
		out[typ]++
	}
	return out
}

// TestApprovals_ApproveThroughAPI_ExecutesToolAndProjectsApproval is the positive control
// of the approval contract: a production Tool call waits for a person, the approval list
// and the Agent Trace show the wait, the decision through the API runs the Tool once and
// the Run completes with the approval and the pause visible in the projections.
func TestApprovals_ApproveThroughAPI_ExecutesToolAndProjectsApproval(t *testing.T) {
	env := newApprovalEnv(t, testOperatorToken)
	runID, agentNodeRunID := env.startApprovalRun(t, domain.AgentGoalFixAndDeploy)

	approval := env.pendingApproval(t, runID)
	if approval["toolName"] != productionToolName || approval["status"] != "PENDING" || approval["deadline"] == nil {
		t.Errorf("pending approval = %v, want the production tool PENDING with a deadline", approval)
	}
	if args, _ := approval["arguments"].(map[string]any); args["target"] != productionToolTarget {
		t.Errorf("approval arguments = %v, want the committed call arguments", approval["arguments"])
	}
	approvalID, _ := approval["id"].(string)

	resp, body := env.doOperator(t, testOperatorToken, http.MethodGet, "/api/approvals/"+approvalID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/approvals/%s status = %d, body=%s", approvalID, resp.StatusCode, body)
	}
	detail := decodeBody[map[string]any](t, body)
	if detail["runId"] != runID || detail["nodeRunId"] != agentNodeRunID || detail["workflowId"] == nil || detail["agentRunId"] == nil {
		t.Errorf("approval detail = %v, want the Run, Agent Run, node run and workflow identities", detail)
	}

	status, traceBody := getAgentTrace(t, env.testEnv, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	trace := decodeBody[map[string]any](t, traceBody)
	agentRun, _ := trace["agentRun"].(map[string]any)
	if agentRun["pausedAt"] == nil || agentRun["pauseReason"] != "APPROVAL" || agentRun["goal"] != "FIX_AND_DEPLOY" {
		t.Errorf("agentRun = pausedAt %v pauseReason %v goal %v, want paused for APPROVAL under FIX_AND_DEPLOY", agentRun["pausedAt"], agentRun["pauseReason"], agentRun["goal"])
	}
	_, action := agentActionOf(t, trace, 1)
	if action["status"] != "WAITING_APPROVAL" || action["authorization"] != "ASK" {
		t.Errorf("action = status %v authorization %v, want WAITING_APPROVAL with ASK", action["status"], action["authorization"])
	}
	if traced, _ := action["approval"].(map[string]any); traced["id"] != approvalID || traced["status"] != "PENDING" {
		t.Errorf("action.approval = %v, want the PENDING approval %s", action["approval"], approvalID)
	}
	if run, _ := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil); run.StatusCode != http.StatusOK {
		t.Fatalf("GET run: %d", run.StatusCode)
	}

	resp, body = env.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "APPROVE"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST decision status = %d, body=%s", resp.StatusCode, body)
	}
	decided := decodeBody[map[string]any](t, body)
	decidedApproval, _ := decided["approval"].(map[string]any)
	if decided["repeated"] != false || decidedApproval["status"] != "APPROVED" || decidedApproval["decidedBy"] != testOperatorName {
		t.Errorf("decision response = %v, want a fresh APPROVED by %s", decided, testOperatorName)
	}

	snapshot := env.waitForTerminal(t, runID, approvalWait)
	run, _ := snapshot["run"].(map[string]any)
	if run["status"] != "COMPLETED" {
		t.Fatalf("run status = %v, want COMPLETED; snapshot=%v", run["status"], snapshot)
	}
	if env.executor.calls.Load() != 1 {
		t.Errorf("production tool calls = %d, want 1", env.executor.calls.Load())
	}

	// The same decision again is answered from the committed approval.
	resp, body = env.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "APPROVE"})
	if resp.StatusCode != http.StatusOK || decodeBody[map[string]any](t, body)["repeated"] != true {
		t.Errorf("repeated APPROVE = %d %s, want 200 repeated", resp.StatusCode, body)
	}

	status, traceBody = getAgentTrace(t, env.testEnv, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	trace = decodeBody[map[string]any](t, traceBody)
	agentRun, _ = trace["agentRun"].(map[string]any)
	if agentRun["pausedAt"] != nil || agentRun["pauseReason"] != nil {
		t.Errorf("agentRun after approval = pausedAt %v pauseReason %v, want the clock running", agentRun["pausedAt"], agentRun["pauseReason"])
	}
	_, action = agentActionOf(t, trace, 1)
	traced, _ := action["approval"].(map[string]any)
	if action["status"] != "SUCCEEDED" || traced["status"] != "APPROVED" || traced["decidedBy"] != testOperatorName || traced["decidedAt"] == nil {
		t.Errorf("action after approval = %v approval %v, want SUCCEEDED with APPROVED by %s", action["status"], traced, testOperatorName)
	}
	if list, _ := env.doOperator(t, testOperatorToken, http.MethodGet, "/api/approvals?status=APPROVED", nil); list.StatusCode != http.StatusOK {
		t.Errorf("GET /api/approvals?status=APPROVED status = %d", list.StatusCode)
	}

	types := eventTypeCounts(env.listEvents(t, runID))
	for _, want := range []string{"AGENT_ACTION_WAITING_APPROVAL", "AGENT_PAUSED", "APPROVAL_DECIDED", "AGENT_RESUMED", "RUN_PAUSED", "RUN_RESUMED"} {
		if types[want] != 1 {
			t.Errorf("event %s count = %d, want 1 (all: %v)", want, types[want], types)
		}
	}
	// The FINAL Action of Turn 2 starts too; the approved Tool call is the only start that
	// names a Tool Attempt.
	toolStarts := 0
	for _, event := range eventsOfType(env.listEvents(t, runID), "AGENT_ACTION_STARTED") {
		if payload, _ := event["payload"].(map[string]any); payload["toolAttemptId"] != nil {
			toolStarts++
		}
	}
	if toolStarts != 1 {
		t.Errorf("AGENT_ACTION_STARTED with a tool attempt = %d, want exactly one for the approved call", toolStarts)
	}
}

// TestApprovals_RejectThenApprove_409 proves a decision on an approval already settled
// differently is refused as a conflict, and that a rejection returns the call to the Agent.
func TestApprovals_RejectThenApprove_409(t *testing.T) {
	env := newApprovalEnv(t, testOperatorToken)
	runID, agentNodeRunID := env.startApprovalRun(t, domain.AgentGoalFixAndDeploy)
	approvalID, _ := env.pendingApproval(t, runID)["id"].(string)

	resp, body := env.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "REJECT"})
	if resp.StatusCode != http.StatusBadRequest || errorCode(t, body) != "VALIDATION_FAILED" {
		t.Errorf("REJECT without reason = %d %s, want 400 VALIDATION_FAILED", resp.StatusCode, body)
	}
	resp, body = env.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "MAYBE"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown decision = %d %s, want 400", resp.StatusCode, body)
	}

	resp, body = env.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "REJECT", "reason": "not in this change window"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("REJECT status = %d, body=%s", resp.StatusCode, body)
	}
	resp, body = env.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "APPROVE"})
	if resp.StatusCode != http.StatusConflict || errorCode(t, body) != "APPROVAL_CONFLICT" {
		t.Errorf("APPROVE after REJECT = %d %s, want 409 APPROVAL_CONFLICT", resp.StatusCode, body)
	}
	if env.executor.calls.Load() != 0 {
		t.Errorf("production tool calls = %d, want 0 after a rejection", env.executor.calls.Load())
	}

	snapshot := env.waitForTerminal(t, runID, approvalWait)
	run, _ := snapshot["run"].(map[string]any)
	if run["status"] != "COMPLETED" {
		t.Fatalf("run status = %v, want COMPLETED after the Agent answered the rejection; snapshot=%v", run["status"], snapshot)
	}
	status, traceBody := getAgentTrace(t, env.testEnv, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d", status)
	}
	_, action := agentActionOf(t, decodeBody[map[string]any](t, traceBody), 1)
	traced, _ := action["approval"].(map[string]any)
	if action["status"] != "FAILED" || traced["status"] != "REJECTED" || traced["reason"] != "not in this change window" {
		t.Errorf("rejected action = %v approval %v, want FAILED with the REJECTED approval and its reason", action["status"], traced)
	}
	if resp, _ := env.doOperator(t, testOperatorToken, http.MethodGet, "/api/approvals/missing-approval", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown approval status = %d, want 404", resp.StatusCode)
	}
}

// TestApprovals_MissingOrWrongToken_401WritesNothing proves every approval route answers
// 401 without a matching operator credential and that a refused decision changes nothing.
func TestApprovals_MissingOrWrongToken_401WritesNothing(t *testing.T) {
	env := newApprovalEnv(t, testOperatorToken)
	runID, _ := env.startApprovalRun(t, domain.AgentGoalFixAndDeploy)
	approvalID, _ := env.pendingApproval(t, runID)["id"].(string)
	eventsBefore := len(env.listEvents(t, runID))

	for _, tc := range []struct {
		name, token, method, path string
		body                      any
	}{
		{"list without token", "", http.MethodGet, "/api/approvals", nil},
		{"list with wrong token", "wrong-" + testOperatorToken, http.MethodGet, "/api/approvals", nil},
		{"detail without token", "", http.MethodGet, "/api/approvals/" + approvalID, nil},
		{"decision without token", "", http.MethodPost, "/api/approvals/" + approvalID + "/decision", map[string]any{"decision": "APPROVE"}},
		{"decision with wrong token", "nearly-" + testOperatorToken, http.MethodPost, "/api/approvals/" + approvalID + "/decision", map[string]any{"decision": "APPROVE"}},
	} {
		resp, body := env.doOperator(t, tc.token, tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusUnauthorized || errorCode(t, body) != "INVALID_OPERATOR_CREDENTIAL" {
			t.Errorf("%s = %d %s, want 401 INVALID_OPERATOR_CREDENTIAL", tc.name, resp.StatusCode, body)
		}
	}

	if approval := env.pendingApproval(t, runID); approval["status"] != "PENDING" || approval["decidedBy"] != nil {
		t.Errorf("approval after refused decisions = %v, want still PENDING", approval)
	}
	if n := len(env.listEvents(t, runID)); n != eventsBefore {
		t.Errorf("refused decisions wrote %d events", n-eventsBefore)
	}
	if env.executor.calls.Load() != 0 {
		t.Errorf("production tool calls = %d, want 0", env.executor.calls.Load())
	}
}

// TestApprovals_TokenNotConfigured_RoutesAbsent proves a Backend without the operator
// credential does not serve the approval routes at all.
func TestApprovals_TokenNotConfigured_RoutesAbsent(t *testing.T) {
	env := newApprovalEnv(t, "")
	for _, path := range []string{"/api/approvals", "/api/approvals/any", "/api/approvals/any/decision"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/decision") {
			method = http.MethodPost
		}
		resp, body := env.doOperator(t, "", method, path, map[string]any{"decision": "APPROVE"})
		if resp.StatusCode != http.StatusNotFound || errorCode(t, body) != "NOT_FOUND" {
			t.Errorf("%s %s = %d %s, want 404 NOT_FOUND", method, path, resp.StatusCode, body)
		}
	}
}

// TestApprovals_Token_NeverInLogsErrorsOrResponses drives every approval route, the
// refused ones included, and proves the credential appears in no log line, no error body
// and no success body, and that the Trace carries no credential either.
func TestApprovals_Token_NeverInLogsErrorsOrResponses(t *testing.T) {
	env := newApprovalEnv(t, testOperatorToken)
	runID, agentNodeRunID := env.startApprovalRun(t, domain.AgentGoalFixAndDeploy)
	approvalID, _ := env.pendingApproval(t, runID)["id"].(string)

	var bodies []string
	collect := func(token, method, path string, body any) {
		_, raw := env.doOperator(t, token, method, path, body)
		bodies = append(bodies, string(raw))
	}
	collect("", http.MethodGet, "/api/approvals", nil)
	collect("wrong-"+testOperatorToken, http.MethodGet, "/api/approvals/"+approvalID, nil)
	collect(testOperatorToken, http.MethodGet, "/api/approvals", nil)
	collect(testOperatorToken, http.MethodGet, "/api/approvals/"+approvalID, nil)
	collect(testOperatorToken, http.MethodGet, "/api/approvals/missing-approval", nil)
	collect(testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "NONSENSE"})
	collect(testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "APPROVE"})
	env.waitForTerminal(t, runID, approvalWait)
	_, traceBody := getAgentTrace(t, env.testEnv, runID, agentNodeRunID)
	bodies = append(bodies, string(traceBody))
	_, eventsBody := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/events", nil)
	bodies = append(bodies, string(eventsBody))

	for i, body := range bodies {
		if strings.Contains(body, testOperatorToken) {
			t.Errorf("response %d repeats the operator credential: %s", i, body)
		}
	}
	if logs := env.logs.String(); strings.Contains(logs, testOperatorToken) {
		t.Errorf("log output contains the operator credential:\n%s", logs)
	}
}

// TestApprovals_PrepareOnlyGoal_DeniesProductionToolWithoutApproval proves the goal the
// Agent Run froze decides before any person is asked: under PREPARE_ONLY the production
// call is returned to the Agent as GOAL_DENIED, no approval exists and the Tool never runs.
func TestApprovals_PrepareOnlyGoal_DeniesProductionToolWithoutApproval(t *testing.T) {
	env := newApprovalEnv(t, testOperatorToken)
	var calls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if calls.Add(1) == 1 {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: productionToolName,
				ToolArguments: json.RawMessage(`{"target":"` + productionToolTarget + `"}`)}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "prepared only"}
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": "Prepare Only Graph", "description": "task_input -> agent -> text_output",
		"nodes": []map[string]any{
			{"id": "node_input", "type": taskInputNodeType, "name": "Task", "position": map[string]any{"x": 0, "y": 0},
				"config": map[string]any{"task": map[string]any{"goal": "PREPARE_ONLY"}}},
			{"id": "node_agent", "type": "agent", "name": "Release Agent", "position": map[string]any{"x": 200, "y": 0},
				"config": baseAgentConfig(map[string]any{
					"allowedTools": []string{productionToolName}, "goalPointer": "/goal", "approvalTimeoutMs": 3600000,
				})},
			{"id": "node_output", "type": "text_output", "name": "Answer", "position": map[string]any{"x": 400, "y": 0}, "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "edge_input_agent", "source": "node_input", "sourceHandle": "text", "target": "node_agent", "targetHandle": "input"},
			{"id": "edge_agent_output", "source": "node_agent", "sourceHandle": "text", "target": "node_output", "targetHandle": "text"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	resp, body = env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": created["workflowId"], "definitionVersion": created["version"], "input": map[string]any{},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	snapshot := env.waitForTerminal(t, runID, approvalWait)
	run, _ := snapshot["run"].(map[string]any)
	if run["status"] != "COMPLETED" {
		t.Fatalf("run status = %v, want COMPLETED; snapshot=%v", run["status"], snapshot)
	}
	if env.executor.calls.Load() != 0 {
		t.Errorf("production tool calls = %d, want 0 under PREPARE_ONLY", env.executor.calls.Load())
	}
	_, traceBody := getAgentTrace(t, env.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	trace := decodeBody[map[string]any](t, traceBody)
	_, action := agentActionOf(t, trace, 1)
	actionErr, _ := action["error"].(map[string]any)
	if action["status"] != "FAILED" || action["authorization"] != "DENY" || action["authorizationReason"] == nil || actionErr["code"] != "GOAL_DENIED" || action["approval"] != nil {
		t.Errorf("denied action = %v, want FAILED with DENY, a reason, GOAL_DENIED and no approval", action)
	}
	if resp, body := env.doOperator(t, testOperatorToken, http.MethodGet, "/api/approvals", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/approvals = %d %s", resp.StatusCode, body)
	} else if items, _ := decodeBody[map[string]any](t, body)["items"].([]any); len(items) != 0 {
		t.Errorf("pending approvals = %v, want none for a denied call", items)
	}
}

// TestDefinitions_Save_ProductionToolWithoutGoalOrApprovalTimeout_400 proves a Definition
// that allows a production-risk Tool must say where the goal comes from and how long a
// person may take, since the Runtime substitutes neither.
func TestDefinitions_Save_ProductionToolWithoutGoalOrApprovalTimeout_400(t *testing.T) {
	env := newApprovalEnv(t, "")
	for name, cfg := range map[string]map[string]any{
		"no goal pointer":     {"allowedTools": []string{productionToolName}, "approvalTimeoutMs": 3600000},
		"no approval timeout": {"allowedTools": []string{productionToolName}, "goalPointer": "/goal"},
		"neither":             {"allowedTools": []string{productionToolName}},
	} {
		resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", agentGraphRequest(baseAgentConfig(cfg)))
		if resp.StatusCode != http.StatusBadRequest || errorCode(t, body) != "SEMANTIC_VALIDATION_FAILED" {
			t.Errorf("%s: status = %d %s, want 400 SEMANTIC_VALIDATION_FAILED", name, resp.StatusCode, body)
		}
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", agentGraphRequest(baseAgentConfig(map[string]any{
		"allowedTools": []string{productionToolName}, "goalPointer": "/goal", "approvalTimeoutMs": 3600000,
	})))
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("fully configured production tool: status = %d %s, want 201", resp.StatusCode, body)
	}
}
