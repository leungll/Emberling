//go:build integration

// Agent Trace contract for a synchronous Tool call whose process stopped: once a restarted
// Backend hands the orphaned STARTED Attempt back to the Agent, the Trace shows the
// Action failed with failureSource RUNNER_LOST, the Attempt failed RESULT_UNCERTAIN, and
// the Agent's next Turn and Decision after it; the Events record AGENT_ACTION_FAILED and
// then AGENT_TURN_READY for that next Turn.
package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// orphanTraceToolName is a synchronous Tool with an uncertain external side effect,
// registered only by this test.
const orphanTraceToolName = "test_uncertain_call"

// orphanTraceInFlightWait bounds how long the test waits for the Tool call to start; it is
// a safety net, never a synchronization device.
const orphanTraceInFlightWait = 10 * time.Second

// heldUncertainCall holds its call until the calling process stops.
type heldUncertainCall struct {
	inFlight chan struct{}
}

func (h heldUncertainCall) Execute(ctx context.Context, _ registry.ToolAction) (registry.ToolExecutionResult, error) {
	close(h.inFlight)
	<-ctx.Done()
	return registry.ToolExecutionResult{}, ctx.Err()
}

func orphanTraceTool(executor registry.ToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          orphanTraceToolName,
			Description:   "Call an external system whose result is uncertain",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionSync,
		},
		Executor: executor,
	}
}

type orphanTraceResponse struct {
	AgentRun struct {
		Termination *string `json:"termination"`
	} `json:"agentRun"`
	Turns []struct {
		TurnNo   int `json:"turnNo"`
		Decision *struct {
			Kind string  `json:"kind"`
			Tool *string `json:"tool"`
		} `json:"decision"`
		Action *struct {
			Status        string                 `json:"status"`
			Error         *domain.ExecutionError `json:"error"`
			FailureSource *string                `json:"failureSource"`
		} `json:"action"`
		ToolAttempts []struct {
			ID     string                 `json:"id"`
			Status string                 `json:"status"`
			Error  *domain.ExecutionError `json:"error"`
		} `json:"toolAttempts"`
	} `json:"turns"`
}

func TestAgentTrace_OrphanUncertain_ShowsResultUncertainAndNextDecision(t *testing.T) {
	held := heldUncertainCall{inFlight: make(chan struct{})}
	crashed := newTestEnvWithOptions(t, testEnvOptions{
		RunnerID:               "runner-before-restart",
		ExtraToolRegistrations: []registry.ToolRegistration{orphanTraceTool(held)},
	})
	crashed.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{
			Kind: mockmodel.ScenarioToolCall, ToolName: orphanTraceToolName, ToolArguments: json.RawMessage(`{"key":"orphan"}`),
		}
	}
	req := agentGraphRequest(baseAgentConfig(map[string]any{"allowedTools": []string{orphanTraceToolName}}))
	resp, body := crashed.doJSON(t, http.MethodPost, "/api/definitions", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	runID := createAgentRun(t, crashed, workflowID, int(version), "call it once")

	select {
	case <-held.inFlight:
	case <-time.After(orphanTraceInFlightWait):
		t.Fatal("the Tool call never started")
	}
	// The process stops while the call is in flight: nothing commits its outcome.
	crashed.stop()

	var attemptID string
	if err := crashed.pool.QueryRow(context.Background(),
		`SELECT id FROM tool_attempts WHERE tool_name = $1 AND status = 'STARTED'`, orphanTraceToolName).Scan(&attemptID); err != nil {
		t.Fatalf("read the STARTED attempt: %v", err)
	}

	restarted := newTestEnvWithOptions(t, testEnvOptions{
		Pool:                   crashed.pool,
		RunnerID:               "runner-after-restart",
		ExtraToolRegistrations: []registry.ToolRegistration{orphanTraceTool(heldUncertainCall{inFlight: make(chan struct{})})},
	})
	restarted.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "answered after the uncertain call"}
	}
	if err := restarted.execution.HandleOrphanToolAttempt(context.Background(), attemptID); err != nil {
		t.Fatalf("handle orphan tool attempt: %v", err)
	}
	snapshot := restarted.waitForTerminal(t, runID, agentTraceWait)
	if run, _ := snapshot["run"].(map[string]any); run["status"] != "COMPLETED" {
		t.Fatalf("run = %v, want COMPLETED after the Agent's next Decision", run)
	}

	status, traceBody := getAgentTrace(t, restarted, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	trace := decodeBody[orphanTraceResponse](t, traceBody)
	if len(trace.Turns) != 2 {
		t.Fatalf("trace turns = %d, want the orphaned round and the next one: %s", len(trace.Turns), traceBody)
	}
	first, next := trace.Turns[0], trace.Turns[1]
	if first.Action == nil || first.Action.Status != "FAILED" || first.Action.FailureSource == nil ||
		*first.Action.FailureSource != string(domain.FailureRunnerLost) || first.Action.Error == nil || first.Action.Error.Code != "RESULT_UNCERTAIN" {
		t.Errorf("Turn 1 action = %+v, want FAILED with RUNNER_LOST and RESULT_UNCERTAIN", first.Action)
	}
	if len(first.ToolAttempts) != 1 || first.ToolAttempts[0].ID != attemptID || first.ToolAttempts[0].Status != "FAILED" ||
		first.ToolAttempts[0].Error == nil || first.ToolAttempts[0].Error.Code != first.Action.Error.Code {
		t.Errorf("Turn 1 tool attempts = %+v, want the one FAILED Attempt with the Action's code", first.ToolAttempts)
	}
	if next.TurnNo != 2 || next.Decision == nil || next.Decision.Kind != string(domain.DecisionFinal) {
		t.Errorf("Turn 2 = %+v, want the Agent's next Decision", next)
	}
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != string(domain.TerminationFinalResponse) {
		t.Errorf("agent run termination = %v, want FINAL_RESPONSE", trace.AgentRun.Termination)
	}

	// The Events tell the same story in order: the failed Action, then the next Turn, then
	// its Decision.
	var order []domain.EventType
	if err := restarted.uow.WithinTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		events, err := tx.Events().ListAfter(ctx, runID, 0, 1000)
		if err != nil {
			return err
		}
		for _, ev := range events {
			switch ev.Type {
			case domain.EventAgentActionFailed:
				var payload struct {
					FailureSource string                `json:"failureSource"`
					Error         domain.ExecutionError `json:"error"`
				}
				if err := json.Unmarshal(ev.Payload, &payload); err != nil {
					return err
				}
				if payload.FailureSource != string(domain.FailureRunnerLost) || payload.Error.Code != "RESULT_UNCERTAIN" {
					t.Errorf("AGENT_ACTION_FAILED payload = %s, want RUNNER_LOST and RESULT_UNCERTAIN", ev.Payload)
				}
				order = append(order, ev.Type)
			case domain.EventAgentTurnReady, domain.EventAgentDecisionCommitted:
				order = append(order, ev.Type)
			default:
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read events: %v", err)
	}
	want := []domain.EventType{
		domain.EventAgentTurnReady, domain.EventAgentDecisionCommitted,
		domain.EventAgentActionFailed, domain.EventAgentTurnReady, domain.EventAgentDecisionCommitted,
	}
	if len(order) != len(want) {
		t.Fatalf("agent events = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("agent events = %v, want %v", order, want)
		}
	}
}
