//go:build integration

// Asynchronous Agent Tool acceptance at the HTTP level (09 §1 DoD row 8: an Agent waiting
// on an asynchronous Tool survives a restart and commits the Tool result, Context, State
// and Events exactly once). One Run of the agent_lookup fixture, with remote_lookup as its
// only Tool, is driven through the real work Pool and the in-process Mock Provider:
// Definition -> Run -> Agent Turn -> TOOL_CALL -> Provider /v1/tasks -> WAITING_CALLBACK
// -> Backend restart -> Provider callback -> resume -> FINAL -> Run COMPLETED.
//
// Every fact is read through the public surface (Snapshot, Node Detail, Agent Trace, the
// Event list and an SSE stream). The wait on WAITING_CALLBACK is a barrier, not a race:
// the Provider's callback stays gated in callbackTransport until the test releases it, so
// the resume cannot overtake the observations made while the Action waits.
package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
)

// TestE2E_AgentAsyncTool_RestartWhileWaitingCallback_CallbackResumesOriginalActionOnce
// covers 06 §1.7 and 06 §4 ("Agent 异步 Tool 等待时重启: callback 恢复原 Tool Attempt")
// end to end. While the Action waits, the Snapshot shows the Run PAUSED, the Agent Trace
// shows the DISPATCHED Attempt with dispatchedAt and a token-free Binding, and SSE has
// delivered AGENT_ACTION_WAITING and RUN_PAUSED from one transaction. After a restart the
// Provider's callback resumes that very Attempt on the second Backend, which calls the
// model exactly once more and completes the Run. The full SSE replay shows every Event
// once, in contiguous seq order, and no response anywhere carries the callback token or
// its hash (08 §3.4, CLAUDE.md "Persistence and transactions").
func TestE2E_AgentAsyncTool_RestartWhileWaitingCallback_CallbackResumesOriginalActionOnce(t *testing.T) {
	fx := newProviderFixture(t)
	first := fx.startBackend(t, nil)
	runID := createAgentAsyncRun(t, first)

	// --- barrier: the dispatch transaction committed; the callback is still gated.
	waiting := first.waitForNodeRunStatus(t, runID, agentFixtureNodeID, "WAITING_CALLBACK", agentE2EWait)
	agentNodeRunID, _ := waiting["id"].(string)
	if waiting["waitingAt"] == nil {
		t.Errorf("waiting Agent NodeRun has no waitingAt: %v", waiting)
	}
	if status := first.runStatus(t, runID); status != "PAUSED" {
		t.Fatalf("Run status while the Tool waits = %q, want PAUSED (derived from the waiting NodeRun)", status)
	}

	dispatches := fx.tasks.dispatches()
	if len(dispatches) != 1 {
		t.Fatalf("provider received %d dispatches before the restart, want 1", len(dispatches))
	}
	var sent struct {
		CallbackToken string `json:"callbackToken"`
	}
	if err := json.Unmarshal(dispatches[0], &sent); err != nil || sent.CallbackToken == "" {
		t.Fatalf("dispatch carried no callback token: %v", err)
	}
	tokenSum := sha256.Sum256([]byte(sent.CallbackToken))
	forbidden := []string{sent.CallbackToken, hex.EncodeToString(tokenSum[:]), "callbackTokenHash", "callbackToken"}
	assertNoTokenMaterial := func(label string, body []byte) {
		t.Helper()
		for _, f := range forbidden {
			if strings.Contains(string(body), f) {
				t.Errorf("%s leaks %q", label, f)
			}
		}
	}

	// A MANAGED_AGENT NodeRun's Node Detail carries no Node Attempts: the Tool Attempt is
	// an Agent fact and is read from the Agent Trace instead.
	detail, detailBody := first.nodeRunDetail(t, runID, agentNodeRunID)
	if attempts := attemptsOf(t, detail); len(attempts) != 0 {
		t.Errorf("Agent Node Detail attempts = %v, want none", attempts)
	}
	assertNoTokenMaterial("Node Detail while waiting", detailBody)

	status, traceBody := getAgentTrace(t, first, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace while waiting: status = %d, body=%s", status, traceBody)
	}
	waitingTrace := decodeAgentAsyncTrace(t, traceBody)
	if len(waitingTrace.Turns) != 1 || waitingTrace.Turns[0].Action == nil || len(waitingTrace.Turns[0].ToolAttempts) != 1 {
		t.Fatalf("agent trace while waiting = %s, want one Turn with one Action and one Tool Attempt", traceBody)
	}
	if got := waitingTrace.Turns[0].Action.Status; got != "WAITING_CALLBACK" {
		t.Errorf("action status while waiting = %q, want WAITING_CALLBACK", got)
	}
	waitingAttempt := waitingTrace.Turns[0].ToolAttempts[0]
	if waitingAttempt.Status != "DISPATCHED" || waitingAttempt.StartedAt == nil || waitingAttempt.DispatchedAt == nil || waitingAttempt.CompletedAt != nil {
		t.Errorf("tool attempt while waiting = %+v, want DISPATCHED with startedAt and dispatchedAt and no completedAt", waitingAttempt)
	}
	binding := waitingAttempt.CallbackBinding
	if binding == nil || binding.ID == "" || binding.ProviderID != remotelookup.ProviderID || binding.ExternalTaskID == "" {
		t.Fatalf("callback binding while waiting = %+v, want id, provider %q and an external task id", binding, remotelookup.ProviderID)
	}
	assertNoTokenMaterial("Agent Trace while waiting", traceBody)

	// SSE shows the wait live: the dispatch transaction's AGENT_ACTION_WAITING and the
	// Run's RUNNING -> PAUSED transition commit together, so they arrive on adjacent seqs.
	live := first.streamUntil(t, runID, "RUN_PAUSED", agentE2EWait)
	if n := len(live); n < 2 || live[n-2].typ != "AGENT_ACTION_WAITING" || live[n-2].seq+1 != live[n-1].seq {
		t.Fatalf("live SSE before the restart = %v, want AGENT_ACTION_WAITING immediately followed by RUN_PAUSED", streamedTypes(live))
	}
	if id, _ := live[len(live)-2].payload["callbackBindingId"].(string); id != binding.ID {
		t.Errorf("AGENT_ACTION_WAITING.callbackBindingId = %q, want the Trace's binding %q", id, binding.ID)
	}

	// --- restart: same database, new everything else. The second Backend's model answers
	// FINAL, and only it may be asked for turn 2.
	first.stop()
	second := fx.startBackend(t, first.pool)
	secondCalls := scriptFinalOnly(second, agentAsyncFinalOutput)

	fx.callbacks.release()
	delivery := fx.waitDelivery(t, agentE2EWait)
	if delivery.StatusCode != http.StatusOK {
		t.Fatalf("callback after the restart: status = %d, want %d, body=%s", delivery.StatusCode, http.StatusOK, delivery.Body)
	}
	if outcome := decodeBody[callbackResponseDTO](t, delivery.Body); !outcome.Accepted || outcome.Pending || outcome.Duplicate {
		t.Errorf("callback outcome after the restart = %+v, want accepted", outcome)
	}
	if delivery.ExternalTaskID != binding.ExternalTaskID {
		t.Errorf("callback named externalTaskId %q, want the bound %q", delivery.ExternalTaskID, binding.ExternalTaskID)
	}
	assertNoTokenMaterial("callback response", delivery.Body)

	snapshot := second.waitForTerminal(t, runID, agentE2EWait)
	run, _ := snapshot["run"].(map[string]any)
	if got, _ := run["status"].(string); got != "COMPLETED" {
		t.Fatalf("Run status after the callback = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	if got := secondCalls.Load(); got != 1 {
		t.Errorf("model calls in the second process = %d, want exactly 1 (turn 2 only)", got)
	}
	if got := len(fx.tasks.dispatches()); got != 1 {
		t.Errorf("provider dispatches = %d, want 1: the waiting Action is never re-executed", got)
	}

	status, traceBody = getAgentTrace(t, second, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace after completion: status = %d, body=%s", status, traceBody)
	}
	done := decodeAgentAsyncTrace(t, traceBody)
	if len(done.Turns) != 2 || len(done.Turns[0].ToolAttempts) != 1 {
		t.Fatalf("agent trace after completion = %s, want two Turns and one Tool Attempt", traceBody)
	}
	resumed := done.Turns[0].ToolAttempts[0]
	if resumed.ID != waitingAttempt.ID || resumed.Status != "SUCCEEDED" || resumed.CompletedAt == nil {
		t.Errorf("resumed tool attempt = %+v, want the original %s SUCCEEDED with completedAt", resumed, waitingAttempt.ID)
	}
	if resumed.CallbackBinding == nil || resumed.CallbackBinding.ID != binding.ID {
		t.Errorf("resumed tool attempt binding = %+v, want the original %+v", resumed.CallbackBinding, binding)
	}
	if done.Turns[0].Action == nil || done.Turns[0].Action.Status != "SUCCEEDED" {
		t.Errorf("tool action after the callback = %+v, want SUCCEEDED", done.Turns[0].Action)
	}
	assertNoTokenMaterial("Agent Trace after completion", traceBody)
	_, detailBody = second.nodeRunDetail(t, runID, agentNodeRunID)
	assertNoTokenMaterial("Node Detail after completion", detailBody)
	snapshotBody, _ := json.Marshal(snapshot)
	assertNoTokenMaterial("Snapshot after completion", snapshotBody)
	listed := second.listEvents(t, runID)
	listedBody, _ := json.Marshal(listed)
	assertNoTokenMaterial("Event list", listedBody)
	assertContiguousSeq(t, listed)

	// The authoritative history, replayed from seq 0 on the second Backend, spans both
	// processes. The resume transaction writes AGENT_ACTION_COMPLETED (CALLBACK), the next
	// Turn's AGENT_TURN_READY and RUN_RESUMED together; no NODE_CALLBACK_RECEIVED is
	// written for a Tool target (05 §2.2).
	events := second.replayEvents(t, runID, agentE2EWait)
	assertAscendingSeq(t, events)
	for _, ev := range events {
		raw, _ := json.Marshal(ev.payload)
		assertNoTokenMaterial("SSE "+ev.typ+" payload", raw)
	}
	want := []string{
		"NODE_STARTED", "AGENT_STARTED",
		"AGENT_TURN_READY", "AGENT_TURN_STARTED", "AGENT_DECISION_COMMITTED",
		"AGENT_ACTION_STARTED", "AGENT_ACTION_WAITING", "RUN_PAUSED",
		"AGENT_ACTION_COMPLETED", "AGENT_TURN_READY", "RUN_RESUMED",
		"AGENT_TURN_STARTED", "AGENT_DECISION_COMMITTED",
		"AGENT_ACTION_STARTED", "AGENT_ACTION_COMPLETED",
		"AGENT_COMPLETED", "NODE_COMPLETED", "RUN_COMPLETED",
	}
	if got := agentEventSequence(events, agentNodeRunID, want); !sameStrings(got, want) {
		t.Fatalf("Agent Events over SSE =\n  %v\nwant\n  %v\n(full stream: %v)", got, want, streamedTypes(events))
	}
	assertAdjacent(t, events, "AGENT_ACTION_WAITING", "RUN_PAUSED")
	assertAdjacent(t, events, "RUN_PAUSED", "AGENT_ACTION_COMPLETED")
	assertAdjacent(t, events, "AGENT_ACTION_COMPLETED", "AGENT_TURN_READY", "RUN_RESUMED")
	if got := len(streamedOfType(events, "NODE_CALLBACK_RECEIVED")); got != 0 {
		t.Errorf("NODE_CALLBACK_RECEIVED events = %d, want 0 for a Tool callback", got)
	}
	for _, typ := range []string{"AGENT_ACTION_WAITING", "RUN_PAUSED", "RUN_RESUMED", "AGENT_COMPLETED"} {
		if got := len(streamedOfType(events, typ)); got != 1 {
			t.Errorf("%s events = %d, want exactly 1", typ, got)
		}
	}
	callbackCompletions := 0
	for _, ev := range streamedOfType(events, "AGENT_ACTION_COMPLETED") {
		if source, _ := ev.payload["completionSource"].(string); source == "CALLBACK" {
			callbackCompletions++
			if id, _ := ev.payload["toolAttemptId"].(string); id != waitingAttempt.ID {
				t.Errorf("CALLBACK completion toolAttemptId = %q, want %q", id, waitingAttempt.ID)
			}
		}
	}
	if callbackCompletions != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", callbackCompletions)
	}
}

// agentAsyncTraceView is the slice of the Agent Trace this file asserts on.
type agentAsyncTraceView struct {
	Turns []struct {
		TurnNo int `json:"turnNo"`
		Action *struct {
			Status string `json:"status"`
		} `json:"action"`
		ToolAttempts []struct {
			ID              string  `json:"id"`
			Status          string  `json:"status"`
			StartedAt       *string `json:"startedAt"`
			DispatchedAt    *string `json:"dispatchedAt"`
			CompletedAt     *string `json:"completedAt"`
			CallbackBinding *struct {
				ID             string `json:"id"`
				ProviderID     string `json:"providerId"`
				ExternalTaskID string `json:"externalTaskId"`
			} `json:"callbackBinding"`
		} `json:"toolAttempts"`
	} `json:"turns"`
}

func decodeAgentAsyncTrace(t *testing.T, body []byte) agentAsyncTraceView {
	t.Helper()
	var trace agentAsyncTraceView
	if err := json.Unmarshal(body, &trace); err != nil {
		t.Fatalf("decode agent trace: %v body=%s", err, body)
	}
	return trace
}

// streamUntil opens a live SSE stream from seq 0 and reads frames until one of type stop
// arrives, returning every frame read. The wait ends on that frame, not on the clock;
// timeout only guards against a hung Backend.
func (e *testEnv) streamUntil(t *testing.T, runID, stop string, timeout time.Duration) []streamedEvent {
	t.Helper()
	resp, reader := e.openSSE(t, runID, 0, "")
	defer resp.Body.Close()

	var out []streamedEvent
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("SSE for run %s did not deliver %s within %s; got %v", runID, stop, timeout, streamedTypes(out))
		}
		frame := waitForNextSSEFrame(t, reader, remaining, "waiting for "+stop+" on run "+runID)
		ev := streamedEvent{seq: frame.id, typ: frame.event}
		var data struct {
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal([]byte(frame.data), &data); err != nil {
			t.Fatalf("decode SSE frame seq %d: %v; data=%s", frame.id, err, frame.data)
		}
		ev.payload = data.Payload
		out = append(out, ev)
		if frame.event == stop {
			return out
		}
	}
}

// assertAdjacent fails unless the first Event of types[0] is immediately followed, on
// consecutive seqs, by one Event of each later type in order -- which is how a single
// transaction's Events appear in the Run's seq order.
func assertAdjacent(t *testing.T, events []streamedEvent, types ...string) {
	t.Helper()
	for i, ev := range events {
		if ev.typ != types[0] {
			continue
		}
		for j := 1; j < len(types); j++ {
			if i+j >= len(events) || events[i+j].typ != types[j] || events[i+j].seq != ev.seq+int64(j) {
				t.Errorf("Events after %s at seq %d are not %v on consecutive seqs: %v", ev.typ, ev.seq, types, streamedTypes(events))
				return
			}
		}
		return
	}
	t.Errorf("no %s Event in the stream: %v", types[0], streamedTypes(events))
}
