//go:build integration

// Agent Tool acceptance cases that the synchronous Tool path already covers, proven again
// on the asynchronous callback path. A callback resume enters the same completion and
// failure use cases as a synchronous Tool result, so each test pins that the shared
// transaction keeps its guarantee when it is reached from a callback: State patch
// atomicity, rollback of a failure transaction, and no recovery of an Agent whose Tool
// has already failed. They share the agentHarness of agent_loop_test.go and the scripted
// async Tool of agent_async_tool_test.go.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
)

// agentAsyncPatchedDispatch drives a fresh Run whose Agent may call only `remote_lookup`
// until its Action is WAITING_CALLBACK. The first Turn's TOOL_CALL Decision carries
// statePatch and the Agent's frozen State Schema is stateSchema (none when empty), so the
// patch is decided only when a callback resumes the Action.
func agentAsyncPatchedDispatch(t *testing.T, workflowID, stateSchema, statePatch string, tool *agentAsyncTool) (*agentHarness, domain.Run, service.AdvanceOutcome, string) {
	t.Helper()
	tool.result = agentAsyncDispatched(agentAsyncExternalTaskID)
	registration := remotelookup.Registration("http://mock-provider.invalid", nil)
	registration.Executor = tool
	// The Agent deadline bounds the real Tool call's context, so the fake clock starts at
	// wall-clock time for the reason newAgentAsyncHarnessMaxTurns gives.
	h := newAgentHarness(t, agentHarnessOptions{
		Tools: []registry.ToolRegistration{registration},
		Clock: newExecClock(time.Now().UTC().Truncate(time.Millisecond)),
	})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName:      remotelookup.ToolName,
		ToolArguments: json.RawMessage(agentToolArguments),
		StatePatch:    json.RawMessage(statePatch),
	})
	def := agentFinalDefinition(workflowID, "", stateSchema)
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, `["`+remotelookup.ToolName+`"]`, 1))
		}
	}
	def = h.saveDefinition(def)
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
	}
	return h, run, outcome, tool.lastToken(t)
}

// TestAgentAsyncToolCallback_StatePatch_CommitsStateVersionWithResult covers a real State
// change on the callback path. A TOOL_CALL's patch is atomic with the Tool result, so
// while the Action waits there is no State Version; the resume transaction then commits
// the new State Version, the moved pointer, the Action result and AGENT_STATE_UPDATED
// together, and the Event records the previous and new State Versions and the Context
// Version committed with them.
func TestAgentAsyncToolCallback_StatePatch_CommitsStateVersionWithResult(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentAsyncPatchedDispatch(t, "wf-agent-cb-state-patch", "", `{"seen":["k1"]}`, tool)
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Fatalf("a State Version exists while the Action waits: the patch must wait for the Tool result")
	}
	if got := agentPointers(agentRun); got != [3]int{1, 0, 0} {
		t.Fatalf("pointers while waiting = %v, want [1 0 0]", got)
	}
	eventsBefore := len(listEvents(h.ctx, t, h.uow, run.ID))

	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", got, err)
	}

	agentRun, _ = agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if got := agentPointers(agentRun); got != [3]int{2, 1, 1} {
		t.Errorf("pointers (turn, context, state) = %v, want [2 1 1]", got)
	}
	stateV1 := agentStateVersion(h.ctx, t, h.uow, agentRun.ID, 1)
	assertSameJSON(t, "state version 1", json.RawMessage(`{"seen":["k1"]}`), stateV1.Value)
	if stateV1.SourceTurnID == nil || *stateV1.SourceTurnID != outcome.AgentTurnID {
		t.Errorf("state version 1 sourceTurnId = %v, want %s", stateV1.SourceTurnID, outcome.AgentTurnID)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", action.Status)
	}
	assertSameJSON(t, "tool attempt result", json.RawMessage(agentCallbackResult), agentOnlyToolAttempt(t, h, action.ID).Result)

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	tail := execEventTypes(events[eventsBefore:])
	want := []domain.EventType{domain.EventAgentActionCompleted, domain.EventAgentStateUpdated, domain.EventAgentTurnReady, domain.EventRunResumed}
	if len(tail) != len(want) {
		t.Fatalf("resume events = %v, want %v", tail, want)
	}
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("resume events = %v, want %v", tail, want)
		}
	}
	updated := agentLastEventPayload(t, events, domain.EventAgentStateUpdated)
	if updated["turnId"] != outcome.AgentTurnID || updated["previousStateVersion"] != float64(0) ||
		updated["stateVersion"] != float64(1) || updated["contextVersion"] != float64(1) {
		t.Errorf("AGENT_STATE_UPDATED = %v, want turn %s, State 0 -> 1, Context Version 1", updated, outcome.AgentTurnID)
	}
	assertAgentTokenAbsent(t, h, run.ID, token)
}

// TestAgentAsyncToolCallback_StatePatchFailsSchema_FailsInvalidActionKeepsResult covers a
// State patch that fails to apply or fails the State Schema on the callback path: the
// Tool really succeeded, so its Attempt keeps the callback's result, but the Action fails
// as INVALID_ACTION with failureSource CALLBACK and no Context or State Version is
// committed.
func TestAgentAsyncToolCallback_StatePatchFailsSchema_FailsInvalidActionKeepsResult(t *testing.T) {
	const stateSchema = `{"type":"object","properties":{"count":{"type":"number"}},"additionalProperties":false}`
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentAsyncPatchedDispatch(t, "wf-agent-cb-state-patch-schema", stateSchema, `{"count":"not-a-number"}`, tool)

	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", got, err)
	}

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationInvalidAction, domain.FailureCallback)
	if action.Error == nil || action.Error.Code != "INVALID_ACTION" {
		t.Errorf("action error = %+v, want INVALID_ACTION", action.Error)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempt status = %s, want SUCCEEDED: the Tool is not what failed", attempt.Status)
	}
	assertSameJSON(t, "tool attempt result", json.RawMessage(agentCallbackResult), attempt.Result)
	types := execEventTypes(listEvents(h.ctx, t, h.uow, run.ID))
	if agentCountEventType(types, domain.EventAgentStateUpdated) != 0 {
		t.Errorf("events = %v, want no AGENT_STATE_UPDATED", types)
	}
	assertAgentTokenAbsent(t, h, run.ID, token)
}

// TestAgentAsyncToolCallback_FailureTxEventInsertFails_RollsBackTermination covers a Tool
// failure transaction that fails before COMMIT on the callback path: when the shared Tool
// failure transaction reached from a Provider-reported failure cannot insert its first
// Event, no termination, no Context or State Version and no next Turn survive, the Action
// is still WAITING_CALLBACK, and the Provider's re-delivery then fails it exactly once.
func TestAgentAsyncToolCallback_FailureTxEventInsertFails_RollsBackTermination(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-failure-rollback", tool, nil)
	const collidingEventID = "ev_agent_async_failure_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())
	before := agentSnapshot(t, h, run, outcome)

	armed := true
	tool.onCallback = func(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
		// Armed outside the lock, so the collision hits the failure transaction's first Event.
		if armed {
			armed = false
			h.ids.ForceNextEventID(collidingEventID)
		}
		return remotelookup.New("http://mock-provider.invalid", nil).OnCallback(ctx, state, payload)
	}

	if _, err := agentDeliver(h.ctx, h, token, agentCallbackFailed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("handle callback = %v, want the conflict from the duplicated Event ID", err)
	}

	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after rollback = %+v, want unchanged %+v", after, before)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.CompletedAt != nil || action.Error != nil {
		t.Errorf("rolled-back action recorded completedAt=%v error=%v", action.CompletedAt, action.Error)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.CompletedAt != nil || attempt.Error != nil {
		t.Errorf("rolled-back tool attempt recorded completedAt=%v error=%v", attempt.CompletedAt, attempt.Error)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil || agentRun.TerminatedAt != nil || agentRun.Error != nil {
		t.Errorf("agent run terminated as %v after a rolled-back failure transaction", agentRun.Termination)
	}
	if got := agentPointers(agentRun); got != [3]int{1, 0, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 0 0]", got)
	}
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) || agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) ||
		agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("rolled-back failure left a Context Version, State Version or next Turn behind")
	}

	got, err := agentDeliver(h.ctx, h, token, agentCallbackFailed)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("re-delivery = %+v, %v, want accepted", got, err)
	}
	assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationToolError, domain.FailureCallback)
	assertContiguousSeq(t, listEvents(h.ctx, t, h.uow, run.ID))
}

// TestAgentAsyncToolRecovery_AfterCallbackFailure_RestartRecoversNothing covers
// recovering an Agent NodeRun after its Tool already failed, on the callback path: once a
// Provider-reported failure committed, a restarted process's Reconciler and a late
// success callback apply no state patch, create no next Turn, never call the model again
// and leave the committed Decision as it was. A failed Agent is terminal, not recoverable
// work.
func TestAgentAsyncToolRecovery_AfterCallbackFailure_RestartRecoversNothing(t *testing.T) {
	tool := &agentAsyncTool{}
	crashed, run, outcome, token := agentAsyncPatchedDispatch(t, "wf-agent-recover-after-failure", "", `{"seen":["k1"]}`, tool)
	decision, ok := agentDecisionOfTurn(crashed.ctx, t, crashed.uow, outcome.AgentTurnID)
	if !ok {
		t.Fatalf("no Decision committed for turn %s", outcome.AgentTurnID)
	}
	if got, err := agentDeliver(crashed.ctx, crashed, token, agentCallbackFailed); err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("failure callback = %+v, %v, want accepted", got, err)
	}
	assertAgentActionFailed(t, crashed, run.ID, outcome, domain.TerminationToolError, domain.FailureCallback)
	before := agentSnapshot(t, crashed, run, outcome)

	restartedTool := &agentAsyncTool{}
	restarted, modelCalls := agentAsyncRestart(t, crashed, restartedTool)
	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ReadyAgentActionsFound != 0 || report.ReadyAgentTurnsFound != 0 || report.ExpiredAgentRunsFound != 0 || report.ConsumablePendingFound != 0 {
		t.Errorf("reconciler report = %+v, want no Agent work after the Tool failed", report)
	}
	late, err := agentDeliver(restarted.ctx, restarted, token, agentCallbackSucceeded)
	if err != nil || !late.Accepted || !late.Duplicate {
		t.Errorf("late success callback = %+v, %v, want an accepted duplicate", late, err)
	}

	if after := agentSnapshot(t, restarted, run, outcome); after != before {
		t.Fatalf("state after restart, reconciliation and late callback = %+v, want unchanged %+v", after, before)
	}
	if modelCalls() != 0 || restartedTool.count() != 0 || restartedTool.callbackCount() != 0 {
		t.Errorf("restarted process: model calls = %d tool calls = %d OnCallback calls = %d, want none",
			modelCalls(), restartedTool.count(), restartedTool.callbackCount())
	}
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentHasStateVersion(restarted.ctx, t, restarted.uow, agentRun.ID, 1) || agentTurnNoExists(restarted.ctx, t, restarted.uow, agentRun.ID, 2) {
		t.Errorf("a State Version or next Turn appeared after the Tool failed")
	}
	after, ok := agentDecisionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if !ok || after.ID != decision.ID || !bytes.Equal(after.StatePatch, decision.StatePatch) || after.Kind != decision.Kind {
		t.Errorf("Decision after recovery = %+v, want the original %+v", after, decision)
	}
	assertAgentTokenAbsent(t, restarted, run.ID, token)
}

// TestAgentAsyncToolRecovery_DispatchResponseLost_FailsAtDeadlineNeverRedispatched covers
// a Provider that accepted the task while the dispatch response was lost, and a Provider
// that accepted the request while no external_task_id was saved locally, for an async
// Agent Tool: the Provider accepted the task and even delivered its callback, but the
// process died before the dispatch transaction committed the Binding. The Attempt is then
// a STARTED Attempt with no Binding, an uncertain external side effect. A restarted
// Reconciler must neither dispatch again nor consume the stored Pending Callback, which
// has no Binding to route through; the Agent fails at its deadline with failureSource
// TIMEOUT and is never reported as recovered.
func TestAgentAsyncToolRecovery_DispatchResponseLost_FailsAtDeadlineNeverRedispatched(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	crashed, run, outcome := newAgentAsyncHarness(t, "wf-agent-recover-lost-dispatch", tool)
	tool.during = func(ctx context.Context, action registry.ToolAction) {
		// The Provider accepted the task and called back before Execute returned; the
		// callback has no Binding yet, so it can only be stored as Pending.
		got, err := agentDeliver(ctx, crashed, action.Callback.Token, agentCallbackSucceeded)
		if err != nil || !got.Pending {
			t.Errorf("early callback = %+v, %v, want stored as Pending", got, err)
		}
		panic("simulated crash after the Provider accepted, before the dispatch transaction")
	}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		_ = crashed.svc.Execute(crashed.ctx, outcome)
	}()

	action, _ := agentActionOfTurn(crashed.ctx, t, crashed.uow, outcome.AgentTurnID)
	attempt := agentOnlyToolAttempt(t, crashed, action.ID)
	if action.Status != domain.AgentActionRunning || attempt.Status != domain.ToolAttemptStarted || attempt.DispatchedAt != nil {
		t.Fatalf("after the crash: action %s attempt %s dispatchedAt %v, want RUNNING, STARTED, none", action.Status, attempt.Status, attempt.DispatchedAt)
	}
	if bindings := agentToolBindings(t, crashed, attempt.ID); len(bindings) != 0 {
		t.Fatalf("bindings after the crash = %d, want 0: the dispatch transaction never committed", len(bindings))
	}

	restartedTool := &agentAsyncTool{}
	restarted, modelCalls := agentAsyncRestart(t, crashed, restartedTool)
	rec := agentReconciler(restarted)
	report := agentRunOnce(restarted, rec)
	if report.ReadyAgentActionsFound != 0 || report.ConsumablePendingFound != 0 || report.ExpiredAgentRunsFound != 0 {
		t.Fatalf("reconciler report before the deadline = %+v, want no Agent work and no consumable Pending", report)
	}
	if pending, found := agentPendingRow(t, restarted, agentAsyncExternalTaskID); !found || pending.ConsumedAt != nil {
		t.Fatalf("pending callback = %+v found=%v, want kept unconsumed without a Binding", pending, found)
	}
	if a := getAgentAction(restarted.ctx, t, restarted.uow, action.ID); a.Status != domain.AgentActionRunning {
		t.Fatalf("action status before the deadline = %s, want RUNNING", a.Status)
	}

	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	report = agentRunOnce(restarted, rec)
	if report.ExpiredAgentRunsFound != 1 || report.AgentRunsTimedOut != 1 || report.PendingCallbacksResumed != 0 {
		t.Fatalf("reconciler report past the deadline = %+v, want the Agent timed out and no Pending resumed", report)
	}

	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("agent run termination = %v, want TIMEOUT", agentTermination(agentRun))
	}
	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureTimeout) || failed["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_FAILED = %v, want failureSource TIMEOUT for the STARTED Attempt %s", failed, attempt.ID)
	}
	if attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempts = %+v, want the one Attempt FAILED and no second dispatch", attempts)
	}
	if tool.count() != 1 || restartedTool.count() != 0 || restartedTool.callbackCount() != 0 || modelCalls() != 0 {
		t.Errorf("calls: crashed Execute = %d, restarted Execute = %d, OnCallback = %d, model = %d; want 1, 0, 0, 0",
			tool.count(), restartedTool.count(), restartedTool.callbackCount(), modelCalls())
	}
	if agentCountEventType(execEventTypes(events), domain.EventAgentActionWaiting) != 0 || agentCallbackCompletions(t, events) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_WAITING and no callback completion", execEventTypes(events))
	}
	if got := agentRunRow(restarted.ctx, t, restarted.uow, run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
	assertContiguousSeq(t, events)
}
