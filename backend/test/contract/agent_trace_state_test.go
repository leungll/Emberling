//go:build integration

// Agent Trace latest State contract:
// agentRun.latestState is the Agent Run's current State Version projected as
// {version, value}. The value is the saved JSON when its canonical encoding fits the Trace
// field bound; above the bound it is null and withheld as TOO_LARGE, never truncated.
package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// latestStateBody decodes agentRun.latestState with raw members, so a null value and a
// missing one are told apart.
type latestStateBody struct {
	Version  *int            `json:"version"`
	Value    json.RawMessage `json:"value"`
	Withheld *string         `json:"withheld"`
}

// scriptPatchingToolCallThenFinal makes the fixture model decide one TOOL_CALL carrying
// patch on its first Turn and FINAL on every later one.
func scriptPatchingToolCallThenFinal(env *testEnv, patch json.RawMessage) {
	var calls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if calls.Add(1) == 1 {
			return &mockmodel.Scenario{
				Kind:          mockmodel.ScenarioToolCall,
				ToolName:      lookup.ToolName,
				ToolArguments: json.RawMessage(`{"key":"` + agentTraceToolKey + `"}`),
				StatePatch:    patch,
			}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: agentTraceFinalOutput}
	}
}

// latestStateOf runs the agent fixture to completion and returns the decoded
// agentRun.latestState and currentStateVersion of its Agent NodeRun's Trace.
func latestStateOf(t *testing.T, env *testEnv) (latestStateBody, int, []byte) {
	t.Helper()
	workflowID, version := saveAgentFixture(t, env)
	runID := createAgentRun(t, env, workflowID, version, "what is the plan?")
	snapshot := env.waitForTerminal(t, runID, agentTraceWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("agent Run terminal status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}
	status, body := getAgentTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	var trace struct {
		AgentRun struct {
			CurrentStateVersion int              `json:"currentStateVersion"`
			LatestState         *latestStateBody `json:"latestState"`
		} `json:"agentRun"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		t.Fatalf("decode agent trace: %v; body=%s", err, body)
	}
	if trace.AgentRun.LatestState == nil {
		t.Fatalf("agentRun.latestState missing or null: %s", body)
	}
	return *trace.AgentRun.LatestState, trace.AgentRun.CurrentStateVersion, body
}

// TestAgentTrace_LatestState_NoPatchIsVersionZeroEmptyObject: an Agent Run whose
// Decisions never patched State still points at State Version 0, which is persisted as
// the empty object, and the Trace shows exactly that.
func TestAgentTrace_LatestState_NoPatchIsVersionZeroEmptyObject(t *testing.T) {
	env := newTestEnv(t)
	_ = scriptToolCallThenFinal(env, agentTraceToolKey, agentTraceFinalOutput)

	state, current, body := latestStateOf(t, env)
	if current != 0 {
		t.Errorf("currentStateVersion = %d, want 0: %s", current, body)
	}
	if state.Version == nil || *state.Version != 0 {
		t.Errorf("latestState.version = %v, want 0: %s", state.Version, body)
	}
	if string(state.Value) != `{}` {
		t.Errorf("latestState.value = %s, want {}: %s", state.Value, body)
	}
	if state.Withheld != nil {
		t.Errorf("latestState.withheld = %q, want absent: %s", *state.Withheld, body)
	}
}

// TestAgentTrace_LatestState_AfterPatchShowsNewestVersion: a TOOL_CALL Decision carrying a
// state patch commits State Version 1 with the Action's result, and the Trace returns that
// version's saved value, not the patch.
func TestAgentTrace_LatestState_AfterPatchShowsNewestVersion(t *testing.T) {
	env := newTestEnv(t)
	scriptPatchingToolCallThenFinal(env, json.RawMessage(`{"plan":["look it up","answer"],"step":1}`))

	state, current, body := latestStateOf(t, env)
	if current != 1 {
		t.Errorf("currentStateVersion = %d, want 1: %s", current, body)
	}
	if state.Version == nil || *state.Version != 1 {
		t.Errorf("latestState.version = %v, want 1: %s", state.Version, body)
	}
	var value map[string]any
	if err := json.Unmarshal(state.Value, &value); err != nil {
		t.Fatalf("latestState.value = %s, want a JSON object: %v", state.Value, err)
	}
	plan, _ := value["plan"].([]any)
	if step, _ := value["step"].(float64); len(plan) != 2 || step != 1 {
		t.Errorf("latestState.value = %s, want the patched plan and step", state.Value)
	}
	if state.Withheld != nil {
		t.Errorf("latestState.withheld = %q, want absent: %s", *state.Withheld, body)
	}
}

// TestAgentTrace_LatestState_OversizedIsWithheldNotTruncated: under a field bound smaller
// than the saved State, the Trace returns version with a null value and TOO_LARGE, and no
// part of the value, so Studio never shows a cut JSON document as the Agent's notes.
func TestAgentTrace_LatestState_OversizedIsWithheldNotTruncated(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{TraceMaxFieldBytes: 16})
	scriptPatchingToolCallThenFinal(env, json.RawMessage(`{"plan":"a plan well beyond sixteen bytes of notes"}`))

	state, current, body := latestStateOf(t, env)
	if current != 1 {
		t.Errorf("currentStateVersion = %d, want 1: %s", current, body)
	}
	if state.Version == nil || *state.Version != 1 {
		t.Errorf("latestState.version = %v, want 1: %s", state.Version, body)
	}
	if string(state.Value) != "null" {
		t.Errorf("latestState.value = %s, want null: %s", state.Value, body)
	}
	if state.Withheld == nil || *state.Withheld != "TOO_LARGE" {
		t.Errorf("latestState.withheld = %v, want TOO_LARGE: %s", state.Withheld, body)
	}
	if strings.Contains(string(body), "a plan well beyond") {
		t.Errorf("agent trace carries part of the withheld State value: %s", body)
	}
}
