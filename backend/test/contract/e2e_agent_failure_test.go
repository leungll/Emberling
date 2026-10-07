//go:build integration

// Agent failure blocks downstream: when the Agent NodeRun fails, the Run ends FAILED and
// the node downstream of the Agent never comes into existence - no NodeRun row, no
// NODE_READY, nothing dispatched. Both Agent failure shapes are covered: the Model call
// itself erroring (MODEL_ERROR, no Decision) and a committed Decision whose synchronous
// Tool fails (TOOL_ERROR).
//
// Everything is observed through the public HTTP surface: the terminal Snapshot and an SSE
// replay from seq 0. The MODEL_ERROR case uses only the Run's input ("mock:fail" directive)
// so it is reachable exactly as Studio would drive it; the TOOL_ERROR case scripts the
// Mock Model Provider to call the lookup Tool with its one deterministically failing key.
package contract

import (
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

const agentFixtureDownstreamNodeID = "node_output"

func TestE2E_AgentFailure_BlocksDownstream_RunFailedWithoutDownstreamNodeRun(t *testing.T) {
	t.Run("model error: AGENT_FAILED then NODE_FAILED then RUN_FAILED, nothing downstream", func(t *testing.T) {
		env := newTestEnv(t)
		workflowID, version := saveAgentFixture(t, env)
		runID := createAgentRun(t, env, workflowID, version, "mock:fail")

		assertAgentFailureBlocksDownstream(t, env, runID, []string{
			"NODE_STARTED", "AGENT_STARTED",
			"AGENT_TURN_READY", "AGENT_TURN_STARTED",
			"AGENT_FAILED", "NODE_FAILED", "RUN_FAILED",
		})
	})

	t.Run("tool error: AGENT_ACTION_FAILED then AGENT_FAILED then NODE_FAILED then RUN_FAILED, nothing downstream", func(t *testing.T) {
		env := newTestEnv(t)
		env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
			return &mockmodel.Scenario{
				Kind:          mockmodel.ScenarioToolCall,
				ToolName:      lookup.ToolName,
				ToolArguments: json.RawMessage(`{"key":"` + lookup.MissingKey + `"}`),
			}
		}
		workflowID, version := saveAgentFixture(t, env)
		runID := createAgentRun(t, env, workflowID, version, "what is missing?")

		assertAgentFailureBlocksDownstream(t, env, runID, []string{
			"NODE_STARTED", "AGENT_STARTED",
			"AGENT_TURN_READY", "AGENT_TURN_STARTED", "AGENT_DECISION_COMMITTED",
			"AGENT_ACTION_STARTED", "AGENT_ACTION_FAILED",
			"AGENT_FAILED", "NODE_FAILED", "RUN_FAILED",
		})
	})
}

// assertAgentFailureBlocksDownstream replays the Run's Events and reads its terminal
// Snapshot, then asserts the failure ordering wantAgentSequence for the Agent NodeRun,
// that RUN_FAILED is the Run's last Event, and that the downstream node left no trace at
// all: no NODE_READY after the Agent failed, exactly the two upstream NODE_READY Events
// overall, no Event attributed to any NodeRun other than the two upstream ones, and no
// NodeRun for it in the Snapshot.
func assertAgentFailureBlocksDownstream(t *testing.T, env *testEnv, runID string, wantAgentSequence []string) {
	t.Helper()

	events := env.replayEvents(t, runID, agentE2EWait)
	assertAscendingSeq(t, events)
	if last := events[len(events)-1]; last.typ != "RUN_FAILED" {
		t.Fatalf("last replayed Event = %s, want RUN_FAILED: %v", last.typ, streamedTypes(events))
	}

	snapshot := env.waitForTerminal(t, runID, agentE2EWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "FAILED" {
		t.Fatalf("run status = %q, want FAILED", status)
	}

	agentNodeRunID := nodeRunIDOf(t, snapshot, agentFixtureNodeID)
	if got := agentEventSequence(events, agentNodeRunID, wantAgentSequence); !sameStrings(got, wantAgentSequence) {
		t.Fatalf("agent failure Event sequence = %v, want %v (full replay: %v)", got, wantAgentSequence, streamedTypes(events))
	}

	// The downstream node must not exist in any persisted form.
	nodeRuns, _ := snapshot["nodeRuns"].([]any)
	upstream := map[string]bool{}
	for _, raw := range nodeRuns {
		nodeRun, _ := raw.(map[string]any)
		nodeID, _ := nodeRun["nodeId"].(string)
		if nodeID == agentFixtureDownstreamNodeID {
			t.Fatalf("snapshot has a NodeRun for the downstream node %q after the Agent failed: %v", nodeID, nodeRun)
		}
		id, _ := nodeRun["id"].(string)
		upstream[id] = true
	}
	if len(nodeRuns) != 2 {
		t.Fatalf("snapshot has %d NodeRuns, want 2 (input and agent only): %v", len(nodeRuns), snapshot["nodeRuns"])
	}

	agentFailedAt := -1
	readyCount := 0
	for i, ev := range events {
		if ev.typ == "AGENT_FAILED" && ev.nodeRunID == agentNodeRunID {
			agentFailedAt = i
		}
		if ev.typ == "NODE_READY" {
			readyCount++
			if agentFailedAt >= 0 {
				t.Fatalf("NODE_READY at seq %d was emitted after AGENT_FAILED (seq %d): downstream work was scheduled despite the Agent failure: %v", ev.seq, events[agentFailedAt].seq, streamedTypes(events))
			}
		}
		if ev.nodeRunID != "" && !upstream[ev.nodeRunID] {
			t.Fatalf("Event %s at seq %d is attributed to NodeRun %s, which is neither the input nor the agent NodeRun", ev.typ, ev.seq, ev.nodeRunID)
		}
	}
	if agentFailedAt < 0 {
		t.Fatalf("no AGENT_FAILED Event for the agent NodeRun %s: %v", agentNodeRunID, streamedTypes(events))
	}
	if readyCount != 2 {
		t.Fatalf("NODE_READY count = %d, want 2 (input and agent; none for the downstream node): %v", readyCount, streamedTypes(events))
	}
}
