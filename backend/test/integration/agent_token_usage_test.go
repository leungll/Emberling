//go:build integration

// Agent NodeRun Token usage tests: the Agent NodeRun carries the sum of its Agent Turns'
// reported usage, written in the same transaction that terminates the Agent, on success
// and on failure alike. The expected sum is recomputed from the persisted Turn rows, so
// the tests prove the committed NodeRun fact agrees with the committed Turn facts.
//
// They share the agentHarness and the fixture Definitions of agent_loop_test.go and
// agent_final_test.go.
package integration

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// agentScriptToolCallThen answers the first Turn with a lookup Tool call and every later
// Turn with the given scenario.
func agentScriptToolCallThen(h *agentHarness, later mockmodel.Scenario) *atomic.Int32 {
	var generated atomic.Int32
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if generated.Add(1) > 1 {
			next := later
			return &next
		}
		return &mockmodel.Scenario{
			Kind: mockmodel.ScenarioToolCall, ToolName: lookup.ToolName,
			ToolArguments: json.RawMessage(agentToolArguments),
		}
	}
	return &generated
}

// persistedTurnUsageSum reads every Turn of the Agent Run and sums the usage each one
// reported, returning the number of Turns that reported any.
func persistedTurnUsageSum(t *testing.T, h *agentHarness, agentRunID string) (domain.TokenUsage, int, int) {
	t.Helper()
	turns := listTurnsOfAgentRun(h.ctx, t, h.uow, agentRunID)
	var sum domain.TokenUsage
	reported := 0
	for _, turn := range turns {
		if turn.TokenUsage == nil {
			continue
		}
		reported++
		sum.InputTokens += turn.TokenUsage.InputTokens
		sum.OutputTokens += turn.TokenUsage.OutputTokens
		sum.TotalTokens += turn.TokenUsage.TotalTokens
	}
	return sum, reported, len(turns)
}

func assertNodeRunUsage(t *testing.T, nodeRun domain.NodeRun, want domain.TokenUsage) {
	t.Helper()
	if nodeRun.TokenUsage == nil {
		t.Fatalf("agent node run tokenUsage = nil, want the Turn sum %+v", want)
	}
	if *nodeRun.TokenUsage != want {
		t.Errorf("agent node run tokenUsage = %+v, want the Turn sum %+v", *nodeRun.TokenUsage, want)
	}
	if want.TotalTokens == 0 {
		t.Errorf("the Turn sum is zero: the fixture model must report non-zero usage for this test to prove a sum")
	}
}

// TestAgentTokenUsage_TwoTurnsSucceed_NodeRunCarriesTurnSum covers the success
// termination: a Tool-call Turn followed by a FINAL Turn, each reporting usage, leaves the
// SUCCEEDED Agent NodeRun with their field-by-field sum.
func TestAgentTokenUsage_TwoTurnsSucceed_NodeRunCarriesTurnSum(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	generated := agentScriptToolCallThen(h, mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "answer: k1"})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-usage-success"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	if got := generated.Load(); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	want, reported, total := persistedTurnUsageSum(t, h, agentRun.ID)
	if total != 2 || reported != 2 {
		t.Fatalf("turns = %d with %d reporting usage, want 2 and 2", total, reported)
	}
	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run status = %s, want SUCCEEDED", nodeRun.Status)
	}
	assertNodeRunUsage(t, nodeRun, want)

	payload := agentLastEventPayload(t, listEvents(h.ctx, t, h.uow, run.ID), domain.EventNodeCompleted)
	if _, ok := payload["tokenUsage"]; !ok {
		t.Errorf("NODE_COMPLETED payload = %v, want the committed tokenUsage summary", payload)
	}
}

// TestAgentTokenUsage_FinalFailsOutputSchema_FailedNodeRunCarriesTurnSum covers a failing
// termination after two reporting Turns: the second Turn's FINAL violates the frozen
// Output Schema, the Agent terminates INVALID_ACTION, and the FAILED Agent NodeRun still
// carries the usage both Turns consumed.
func TestAgentTokenUsage_FinalFailsOutputSchema_FailedNodeRunCarriesTurnSum(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	agentScriptToolCallThen(h, mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "x"})
	def := h.saveDefinition(agentFinalDefinition("wf-agent-usage-invalid-final",
		`{"type":"string","enum":["","approved"]}`, ""))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Fatalf("agent run termination = %v, want INVALID_ACTION", agentRun.Termination)
	}
	want, reported, total := persistedTurnUsageSum(t, h, agentRun.ID)
	if total != 2 || reported != 2 {
		t.Fatalf("turns = %d with %d reporting usage, want 2 and 2", total, reported)
	}
	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Fatalf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	assertNodeRunUsage(t, nodeRun, want)
}

// TestAgentTokenUsage_SecondModelCallFails_NodeRunCarriesFirstTurnUsage covers the
// MODEL_ERROR termination: the failed model call reports nothing, so the FAILED Agent
// NodeRun carries exactly the usage of the Turn that did complete.
func TestAgentTokenUsage_SecondModelCallFails_NodeRunCarriesFirstTurnUsage(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	agentScriptToolCallThen(h, mockmodel.Scenario{Kind: mockmodel.ScenarioFail})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-usage-model-error"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationModelError {
		t.Fatalf("agent run termination = %v, want MODEL_ERROR", agentRun.Termination)
	}
	want, reported, total := persistedTurnUsageSum(t, h, agentRun.ID)
	if total != 2 || reported != 1 {
		t.Fatalf("turns = %d with %d reporting usage, want 2 and 1", total, reported)
	}
	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Fatalf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	assertNodeRunUsage(t, nodeRun, want)
}

// TestAgentTokenUsage_NoTurnReports_NodeRunUsageNull covers the absence rule: when no Turn
// reported usage the Agent NodeRun stores null, never a fabricated zero.
func TestAgentTokenUsage_NoTurnReports_NodeRunUsageNull(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	agentScriptFinal(h, mockmodel.Scenario{Output: "answer", OmitTokenUsage: true})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-usage-unreported"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run status = %s, want SUCCEEDED", nodeRun.Status)
	}
	if nodeRun.TokenUsage != nil {
		t.Errorf("agent node run tokenUsage = %+v, want nil when no Turn reported usage", *nodeRun.TokenUsage)
	}
}
