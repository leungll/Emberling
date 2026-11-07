//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// mergeNodeType is a test node joining two text inputs into one text output. It gives the
// Agent and a sibling branch one Output Node to reach, so a Definition can hold a branch
// whose failure ends the Run while the Agent waits.
const mergeNodeType = "test_merge"

type mergeExecutor struct{}

func (mergeExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (mergeExecutor) Execute(_ context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	a, _ := input.Port("a")
	b, _ := input.Port("b")
	merged, err := json.Marshal(string(a) + "\n" + string(b))
	if err != nil {
		return registry.NodeResult{}, err
	}
	return registry.CompletedResult(map[string]json.RawMessage{"text": merged}), nil
}

func mergeNodeRegistration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          mergeNodeType,
			DisplayName:   "Test Merge",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs: []domain.PortMetadata{
				{Name: "a", DataType: domain.PortTypeText, Required: true},
				{Name: "b", DataType: domain.PortTypeText, Required: true},
			},
			Outputs:      []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: mergeExecutor{}},
	}
}

// startApprovalRunWithFailingSibling is startApprovalRun with a second branch beside the
// Agent: the task input also feeds the test async node, whose callback the test delivers
// itself, and both branches join in the merge node before the Output. Failing the sibling
// ends the Run while the Agent still waits for the person.
func (e *approvalEnv) startApprovalRunWithFailingSibling(t *testing.T) (runID string) {
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
		"name": "Approval Graph With Sibling", "description": "task_input -> agent and task_input -> async, joined before text_output",
		"nodes": []map[string]any{
			{"id": "node_input", "type": taskInputNodeType, "name": "Task", "position": map[string]any{"x": 0, "y": 0},
				"config": map[string]any{"task": map[string]any{"goal": string(domain.AgentGoalFixAndDeploy)}}},
			{"id": "node_agent", "type": "agent", "name": "Release Agent", "position": map[string]any{"x": 200, "y": 0},
				"config": baseAgentConfig(map[string]any{
					"allowedTools": []string{productionToolName}, "goalPointer": "/goal", "approvalTimeoutMs": 3600000,
				})},
			{"id": "node_async", "type": testAsyncNodeType, "name": "Sibling", "position": map[string]any{"x": 200, "y": 200}, "config": map[string]any{}},
			{"id": "node_merge", "type": mergeNodeType, "name": "Merge", "position": map[string]any{"x": 400, "y": 100}, "config": map[string]any{}},
			{"id": "node_output", "type": "text_output", "name": "Answer", "position": map[string]any{"x": 600, "y": 100}, "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "edge_input_agent", "source": "node_input", "sourceHandle": "text", "target": "node_agent", "targetHandle": "input"},
			{"id": "edge_input_async", "source": "node_input", "sourceHandle": "text", "target": "node_async", "targetHandle": "in"},
			{"id": "edge_agent_merge", "source": "node_agent", "sourceHandle": "text", "target": "node_merge", "targetHandle": "a"},
			{"id": "edge_async_merge", "source": "node_async", "sourceHandle": "out", "target": "node_merge", "targetHandle": "b"},
			{"id": "edge_merge_output", "source": "node_merge", "sourceHandle": "text", "target": "node_output", "targetHandle": "text"},
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
	e.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", approvalWait)
	e.waitForNodeRunStatus(t, runID, "node_async", "WAITING_CALLBACK", approvalWait)
	return runID
}

// TestApprovals_RunEndedBeforeDecision_409NotActionable proves the HTTP mapping of a
// decision on an approval whose Run has already ended: 409 with APPROVAL_NOT_ACTIONABLE,
// distinct from the conflict of an approval decided differently, the approval still PENDING
// afterwards and the Tool never called.
func TestApprovals_RunEndedBeforeDecision_409NotActionable(t *testing.T) {
	env := newApprovalEnv(t, testOperatorToken, mergeNodeRegistration())
	runID := env.startApprovalRunWithFailingSibling(t)
	approval := env.pendingApproval(t, runID)
	approvalID, _ := approval["id"].(string)

	dispatch := env.dispatcher.last()
	if dispatch.Token == "" {
		t.Fatal("the sibling async node dispatched no task")
	}
	resp, body := env.doCallback(t, dispatch.Token, dispatch.ExternalTaskID, map[string]any{
		"status": "FAILED", "error": map[string]any{"code": "PROVIDER_TASK_FAILED", "message": "the sibling task failed"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sibling FAILED callback status = %d, body=%s", resp.StatusCode, body)
	}
	snapshot := env.waitForTerminal(t, runID, approvalWait)
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("run status = %q, want FAILED after the sibling failed; snapshot=%v", got, snapshot)
	}

	resp, body = env.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "APPROVE"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST decision after the run ended status = %d, want 409; body=%s", resp.StatusCode, body)
	}
	envelope, _ := decodeBody[map[string]any](t, body)["error"].(map[string]any)
	if envelope["code"] != "APPROVAL_NOT_ACTIONABLE" {
		t.Errorf("error code = %v, want APPROVAL_NOT_ACTIONABLE; body=%s", envelope["code"], body)
	}

	resp, body = env.doOperator(t, testOperatorToken, http.MethodGet, "/api/approvals/"+approvalID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/approvals/%s status = %d, body=%s", approvalID, resp.StatusCode, body)
	}
	detail := decodeBody[map[string]any](t, body)
	if detail["status"] != "PENDING" || detail["decidedBy"] != nil {
		t.Errorf("approval after the refused decision = status %v decidedBy %v, want still PENDING and undecided", detail["status"], detail["decidedBy"])
	}
	if _, present := detail["changeRequest"]; !present {
		t.Errorf("approval detail lacks the changeRequest member; body=%s", body)
	}
	if detail["changeRequest"] != nil || detail["patch"] != nil {
		t.Errorf("approval detail = changeRequest %v patch %v, want both null for a call that names no change request", detail["changeRequest"], detail["patch"])
	}
	if env.executor.calls.Load() != 0 {
		t.Errorf("production tool calls = %d, want 0", env.executor.calls.Load())
	}
}
