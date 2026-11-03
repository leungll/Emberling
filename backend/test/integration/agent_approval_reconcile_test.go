//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// The approval expiry scan, end to end: a Run whose Agent waits for a person is driven to
// its wait through the real Agent loop, the injected clock passes the approval deadline,
// and one Reconciler pass settles the approval and returns the Action to the Agent.

const (
	approvalTaskInputType  = "test_task_input"
	approvalProductionTool = "test_production_tool"
	approvalWaitMs         = 3_600_000
)

type approvalTaskInputExecutor struct{}

func (approvalTaskInputExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (approvalTaskInputExecutor) Execute(_ context.Context, _ registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	raw, err := json.Marshal(config["task"])
	if err != nil {
		return registry.NodeResult{}, err
	}
	return registry.CompletedResult(map[string]json.RawMessage{"text": raw}), nil
}

// approvalTaskInputRegistration publishes its configured task object, so the Agent's goal
// pointer has an object to read; the built-in text input publishes a string.
func approvalTaskInputRegistration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          approvalTaskInputType,
			DisplayName:   "Test Task Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{"task":{"type":"object"}},"required":["task"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: approvalTaskInputExecutor{}},
	}
}

type approvalCountingTool struct{ calls int32 }

func (e *approvalCountingTool) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	e.calls++
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: json.RawMessage(`{"ok":true}`)}}, nil
}

func approvalProductionRegistration(executor registry.ToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          approvalProductionTool,
			Description:   "A test tool that changes a production system",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionSync,
			Authorization: &domain.ToolAuthorization{Risk: domain.ToolRiskProduction},
		},
		Executor: executor,
	}
}

func approvalDefinition(workflowID string) domain.Definition {
	config := `{
		"instructions": "` + agentInstructions + `",
		"modelId": "` + mockmodel.ModelID + `",
		"allowedTools": ["` + approvalProductionTool + `"],
		"maxTurns": 4,
		"timeoutMs": 600000,
		"goalPointer": "/goal",
		"approvalTimeoutMs": ` + strconv.Itoa(approvalWaitMs) + `
	}`
	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Approval Expiry",
		Description: "task input -> agent -> text_output",
		Nodes: []domain.Node{
			{ID: "node_input", Type: approvalTaskInputType, Name: "Task",
				Config: json.RawMessage(`{"task":{"goal":"FIX_AND_DEPLOY","question":"` + agentQuestion + `"}}`)},
			{ID: "node_agent", Type: "agent", Name: "Agent", Config: json.RawMessage(config)},
			{ID: "node_output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_input", SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
			{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_output", TargetHandle: "text"},
		},
	}
}

func approvalOfAction(ctx context.Context, t *testing.T, uow store.UnitOfWork, actionID string) domain.Approval {
	t.Helper()
	var approval domain.Approval
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		approval, err = tx.Approvals().GetByActionID(ctx, actionID)
		return err
	}); err != nil {
		t.Fatalf("approval of action %s: %v", actionID, err)
	}
	return approval
}

func TestReconciler_ExpiredApprovalScan_SettlesApprovalAndReturnsAction(t *testing.T) {
	tool := &approvalCountingTool{}
	h := newAgentHarness(t, agentHarnessOptions{
		Nodes: []registry.NodeRegistration{approvalTaskInputRegistration()},
		Tools: []registry.ToolRegistration{approvalProductionRegistration(tool)},
	})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{ToolName: approvalProductionTool, ToolArguments: json.RawMessage(agentToolArguments)})
	def := h.saveDefinition(approvalDefinition("wf_approval_expiry"))
	run := h.createRun(def.WorkflowID, def.Version, `{}`)
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)

	action, ok := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !ok || action.Status != domain.AgentActionWaitingApproval {
		t.Fatalf("precondition: action = %+v, want WAITING_APPROVAL", action)
	}
	approval := approvalOfAction(h.ctx, t, h.uow, action.ID)
	if approval.Status != domain.ApprovalPending {
		t.Fatalf("precondition: approval = %s, want PENDING", approval.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.PausedAt == nil {
		t.Fatalf("precondition: the Agent clock is not paused")
	}
	originalDeadline := agentRun.Deadline

	rec := agentReconciler(h)
	// Before the approval deadline the scan finds nothing: the paused Agent is neither an
	// expired approval nor an expired Agent Run.
	early := agentRunOnce(h, rec)
	if early.ExpiredApprovalsFound != 0 || early.ApprovalsExpired != 0 {
		t.Fatalf("report before the deadline = %+v, want no expired approval", early)
	}

	paused := time.Duration(approvalWaitMs)*time.Millisecond + time.Second
	h.clock.Advance(paused)
	report := agentRunOnce(h, rec)
	if report.ExpiredApprovalsFound != 1 || report.ApprovalsExpired != 1 {
		t.Fatalf("report = %+v, want one expired approval found and settled", report)
	}

	settled := approvalOfAction(h.ctx, t, h.uow, action.ID)
	if settled.Status != domain.ApprovalExpired || settled.DecidedBy != nil {
		t.Errorf("approval = %+v, want EXPIRED by nobody", settled)
	}
	returned, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if returned.Status != domain.AgentActionFailed || returned.Error == nil || returned.Error.Code != string(domain.ReturnApprovalExpired) {
		t.Errorf("action = %s %+v, want FAILED with APPROVAL_EXPIRED returned to the Agent", returned.Status, returned.Error)
	}
	resumed, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if resumed.PausedAt != nil || !resumed.Deadline.Equal(originalDeadline.Add(paused)) {
		t.Errorf("agent run = paused %v deadline %v, want running with the deadline moved by %s", resumed.PausedAt, resumed.Deadline, paused)
	}
	if tool.calls != 0 {
		t.Errorf("the production tool ran %d times without approval", tool.calls)
	}
	// A second pass finds nothing: the settled approval left the scan.
	if again := agentRunOnce(h, rec); again.ExpiredApprovalsFound != 0 {
		t.Errorf("second report = %+v, want no expired approval", again)
	}

	// The next Turn the expiry created runs the scripted FINAL answer, and the Run ends.
	if err := h.drainTurns(h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}
	agentDrive(h, run.ID)
	final, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if final.Termination == nil || *final.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %v, want FINAL_RESPONSE after the refusal", final.Termination)
	}
}
