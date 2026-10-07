//go:build integration

// Asynchronous Agent Tool resume tests. A callback for a TOOL_ATTEMPT Binding enters the
// same idempotent resume use case as a Node Attempt's: routing facts are read without a
// lock, OnCallback runs outside every lock, and one transaction under the Run aggregate
// lock resolves the DISPATCHED Tool Attempt and the WAITING_CALLBACK Action and continues
// the Agent Loop exactly as the synchronous path does. They share the agentHarness of
// agent_loop_test.go and the scripted async Tool of agent_async_tool_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
)

const (
	agentCallbackSucceeded = `{"status":"SUCCEEDED","key":"k1","record":"ember record"}`
	agentCallbackResult    = `{"key":"k1","record":"ember record"}`
	agentCallbackFailed    = `{"status":"FAILED","error":{"code":"REMOTE_LOOKUP_FAILED","message":"provider could not find k1"}}`
)

// agentDispatchedAsync drives a fresh Run until its Agent Action is WAITING_CALLBACK on
// agentAsyncExternalTaskID and returns the plaintext callback token the Tool was handed.
func agentDispatchedAsync(t *testing.T, workflowID string, tool *agentAsyncTool, notifier service.EventNotifier) (*agentHarness, domain.Run, service.AdvanceOutcome, string) {
	t.Helper()
	return agentDispatchedAsyncWith(t, workflowID, tool, agentHarnessOptions{Notifier: notifier})
}

// agentDispatchedAsyncWith is agentDispatchedAsync with the notifier and WorkEnqueuer
// taken from opts.
func agentDispatchedAsyncWith(t *testing.T, workflowID string, tool *agentAsyncTool, opts agentHarnessOptions) (*agentHarness, domain.Run, service.AdvanceOutcome, string) {
	t.Helper()
	if tool.result.Kind == "" {
		tool.result = agentAsyncDispatched(agentAsyncExternalTaskID)
	}
	h, run, outcome := newAgentAsyncHarnessWithOptions(t, workflowID, tool, opts)
	h.execute(outcome)
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
	}
	return h, run, outcome, tool.lastToken(t)
}

func agentDeliver(h *agentHarness, ctx context.Context, token, payload string) (service.CallbackOutcome, error) {
	return h.svc.HandleCallback(ctx, service.HandleCallback{
		Token:          token,
		ExternalTaskID: agentAsyncExternalTaskID,
		Payload:        json.RawMessage(payload),
	})
}

// agentWaitingSnapshot is everything a rejected or duplicate delivery must leave exactly
// as it found it.
type agentWaitingSnapshot struct {
	actionStatus  domain.AgentActionStatus
	attemptStatus domain.ToolAttemptStatus
	nodeRunStatus domain.NodeRunStatus
	runStatus     domain.RunStatus
	lastSeq       int64
	events        int
	bindings      int
}

func agentSnapshot(t *testing.T, h *agentHarness, run domain.Run, outcome service.AdvanceOutcome) agentWaitingSnapshot {
	t.Helper()
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	persisted := agentRunRow(h.ctx, t, h.uow, run.ID)
	events := listEvents(h.ctx, t, h.uow, run.ID)
	return agentWaitingSnapshot{
		actionStatus:  action.Status,
		attemptStatus: attempt.Status,
		nodeRunStatus: agentNodeRun(h.ctx, t, h.uow, run.ID).Status,
		runStatus:     persisted.Status,
		lastSeq:       persisted.LastSeq,
		events:        len(events),
		bindings:      len(agentToolBindings(t, h, attempt.ID)),
	}
}

// agentCallbackCompletions counts the AGENT_ACTION_COMPLETED Events a callback wrote.
func agentCallbackCompletions(t *testing.T, events []domain.Event) int {
	t.Helper()
	n := 0
	for _, ev := range events {
		if ev.Type != domain.EventAgentActionCompleted {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", ev.Type, err)
		}
		if payload["completionSource"] == string(domain.CompletionCallback) {
			n++
		}
	}
	return n
}

func assertAgentTokenAbsent(t *testing.T, h *agentHarness, runID, token string) {
	t.Helper()
	hash := agentSHA256Hex(token)
	for _, ev := range listEvents(h.ctx, t, h.uow, runID) {
		if strings.Contains(string(ev.Payload), token) || strings.Contains(string(ev.Payload), hash) {
			t.Errorf("event %s payload carries the callback token or its hash", ev.Type)
		}
	}
}

// TestAgentAsyncToolCallback_Success_ResumesLoopWithNextReadyTurn covers the successful
// resume: a valid callback resolves the DISPATCHED Tool Attempt and the WAITING_CALLBACK
// Action, appends the Context, creates the next READY Turn, returns the Agent NodeRun to
// RUNNING and the Run from PAUSED to RUNNING, all in one transaction, with
// completionSource CALLBACK and no NODE_CALLBACK_RECEIVED. The resume ends at that commit
// and this harness has no work queue, so the next Turn is observed as committed READY
// work.
func TestAgentAsyncToolCallback_Success_ResumesLoopWithNextReadyTurn(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-success", tool, nil)
	eventsBefore := len(listEvents(h.ctx, t, h.uow, run.ID))

	got, err := agentDeliver(h, h.ctx, token, agentCallbackSucceeded)
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if !got.Accepted || got.Pending || got.Duplicate || got.RunID != run.ID || got.NodeRunID != outcome.NodeRunID {
		t.Fatalf("callback outcome = %+v, want accepted for run %s node run %s", got, run.ID, outcome.NodeRunID)
	}

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded || action.CompletedAt == nil || action.WaitingAt == nil {
		t.Errorf("action status = %s completedAt = %v waitingAt = %v, want SUCCEEDED with both", action.Status, action.CompletedAt, action.WaitingAt)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptSucceeded || attempt.CompletedAt == nil || attempt.DispatchedAt == nil {
		t.Errorf("tool attempt status = %s completedAt = %v, want SUCCEEDED", attempt.Status, attempt.CompletedAt)
	}
	assertSameJSON(t, "tool attempt result", json.RawMessage(agentCallbackResult), attempt.Result)
	if bindings := agentToolBindings(t, h, attempt.ID); len(bindings) != 1 {
		t.Errorf("bindings = %d, want the Binding kept for audit", len(bindings))
	}

	if tool.callbackCount() != 1 {
		t.Fatalf("OnCallback calls = %d, want 1", tool.callbackCount())
	}
	state := tool.callbacks[0]
	if state.ActionID != action.ID || state.AgentRunID != outcome.AgentRunID || state.ToolName != remotelookup.ToolName ||
		state.AttemptNo != 1 || state.ExternalTask.ExternalTaskID != agentAsyncExternalTaskID || state.ExternalTask.ProviderID != remotelookup.ProviderID {
		t.Errorf("OnCallback state = %+v, want the committed Action's async state", state)
	}

	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nr.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunRunning {
		t.Errorf("run status = %s, want RUNNING", persisted.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s after a successful callback", *agentRun.Termination)
	}
	if got := agentPointers(agentRun); got != [3]int{2, 1, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [2 1 0]", got)
	}
	messages := agentContextMessages(t, agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 1).Messages)
	if len(messages) != 3 {
		t.Fatalf("context version 1 holds %d messages, want 3", len(messages))
	}
	if last := messages[2]; last.Role != runtime.AgentRoleTool || last.ToolActionID == nil || *last.ToolActionID != action.ID {
		t.Errorf("last context message = %+v, want the tool result linked to action %s", last, action.ID)
	}
	assertSameJSON(t, "tool context message", json.RawMessage(agentCallbackResult), messages[2].Content)

	turns := listReadyTurns(h.ctx, t, h.uow)
	if len(turns) != 1 || turns[0].AgentRunID != agentRun.ID || turns[0].TurnNo != 2 {
		t.Fatalf("ready turns = %+v, want exactly turn 2 of agent run %s", turns, agentRun.ID)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	tail := execEventTypes(events[eventsBefore:])
	want := []domain.EventType{domain.EventAgentActionCompleted, domain.EventAgentTurnReady, domain.EventRunResumed}
	if len(tail) != len(want) {
		t.Fatalf("resume events = %v, want %v", tail, want)
	}
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("resume events = %v, want %v", tail, want)
		}
	}
	if agentCountEventType(execEventTypes(events), domain.EventNodeCallbackReceived) != 0 {
		t.Errorf("an async Tool callback wrote NODE_CALLBACK_RECEIVED")
	}
	completed := agentLastEventPayload(t, events, domain.EventAgentActionCompleted)
	if completed["completionSource"] != string(domain.CompletionCallback) || completed["toolAttemptId"] != attempt.ID || completed["actionId"] != action.ID {
		t.Errorf("AGENT_ACTION_COMPLETED = %v, want completionSource CALLBACK for %s", completed, attempt.ID)
	}
	if _, hasResult := completed["result"]; hasResult {
		t.Errorf("AGENT_ACTION_COMPLETED carries the Tool result; Event payloads stay bounded")
	}
	resumed := agentLastEventPayload(t, events, domain.EventRunResumed)
	if resumed["from"] != string(domain.RunPaused) || resumed["to"] != string(domain.RunRunning) {
		t.Errorf("RUN_RESUMED = %v, want PAUSED -> RUNNING", resumed)
	}
	assertAgentTokenAbsent(t, h, run.ID, token)
}

// TestAgentAsyncToolCallback_ResumeTxEventInsertFails_RollsBackEverything covers the
// state-and-Event atomicity rule: when the resume transaction cannot insert its first
// Event, the Tool Attempt, the Action, the Context, the next Turn and the Run status all
// stay as the dispatch committed them, and a re-delivery of the same callback then
// succeeds.
func TestAgentAsyncToolCallback_ResumeTxEventInsertFails_RollsBackEverything(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-rollback", tool, nil)
	const collidingEventID = "ev_agent_async_callback_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())
	before := agentSnapshot(t, h, run, outcome)

	armed := true
	tool.onCallback = func(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
		// Armed outside the lock, so the collision hits the resume transaction's first Event.
		if armed {
			armed = false
			h.ids.ForceNextEventID(collidingEventID)
		}
		return remotelookup.New("http://mock-provider.invalid", nil).OnCallback(ctx, state, payload)
	}

	if _, err := agentDeliver(h, h.ctx, token, agentCallbackSucceeded); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("handle callback = %v, want the conflict from the duplicated Event ID", err)
	}

	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after rollback = %+v, want unchanged %+v", after, before)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.CompletedAt != nil {
		t.Errorf("rolled-back action records completed_at")
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if len(attempt.Result) != 0 || attempt.CompletedAt != nil {
		t.Errorf("rolled-back tool attempt kept result %s completedAt %v", attempt.Result, attempt.CompletedAt)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) || agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("rolled-back resume left a Context Version or a next Turn behind")
	}

	// The waiting state is intact, so the Provider's re-delivery resumes normally.
	got, err := agentDeliver(h, h.ctx, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("re-delivery = %+v, %v, want accepted", got, err)
	}
	if a := getAgentAction(h.ctx, t, h.uow, action.ID); a.Status != domain.AgentActionSucceeded {
		t.Errorf("action status after re-delivery = %s, want SUCCEEDED", a.Status)
	}
	assertContiguousSeq(t, listEvents(h.ctx, t, h.uow, run.ID))
}

// TestAgentAsyncToolCallback_ProviderFailure_FailsThroughSharedToolFailure covers a
// Provider-reported failure: it enters the shared Tool failure use case with
// failureSource CALLBACK -- the Tool Attempt and Action fail, the Agent Run terminates as
// TOOL_ERROR, and the Agent NodeRun and the Run fail.
func TestAgentAsyncToolCallback_ProviderFailure_FailsThroughSharedToolFailure(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-provider-failure", tool, nil)
	eventsBefore := len(listEvents(h.ctx, t, h.uow, run.ID))

	got, err := agentDeliver(h, h.ctx, token, agentCallbackFailed)
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if !got.Accepted || got.Duplicate {
		t.Fatalf("callback outcome = %+v, want accepted", got)
	}

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationToolError, domain.FailureCallback)
	if action.Error == nil || action.Error.Code != "REMOTE_LOOKUP_FAILED" {
		t.Errorf("action error = %+v, want the Provider's code", action.Error)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "REMOTE_LOOKUP_FAILED" || len(attempt.Result) != 0 {
		t.Errorf("tool attempt = status %s error %+v result %s, want FAILED with the Provider's code", attempt.Status, attempt.Error, attempt.Result)
	}
	if bindings := agentToolBindings(t, h, attempt.ID); len(bindings) != 1 {
		t.Errorf("bindings = %d, want the Binding kept", len(bindings))
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	tail := execEventTypes(events[eventsBefore:])
	for _, want := range []domain.EventType{domain.EventAgentActionFailed, domain.EventAgentFailed, domain.EventNodeFailed, domain.EventRunFailed} {
		if agentCountEventType(tail, want) != 1 {
			t.Errorf("failure events = %v, want exactly one %s", tail, want)
		}
	}
	if agentCountEventType(tail, domain.EventNodeCallbackReceived) != 0 {
		t.Errorf("failure events = %v, want no NODE_CALLBACK_RECEIVED", tail)
	}
	failed := agentLastEventPayload(t, events, domain.EventAgentActionFailed)
	if failed["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_FAILED toolAttemptId = %v, want %s", failed["toolAttemptId"], attempt.ID)
	}
	assertAgentTokenAbsent(t, h, run.ID, token)
}

// TestAgentAsyncToolCallback_ResultViolatesOutputSchema_FailsWithCallbackSource covers
// the Tool OutputSchema check on the callback path: an interpreted result that the
// registered schema rejects fails the Action exactly as a synchronous invalid result
// does, with failureSource CALLBACK.
func TestAgentAsyncToolCallback_ResultViolatesOutputSchema_FailsWithCallbackSource(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-invalid-result", tool, nil)
	tool.onCallback = func(context.Context, registry.ToolAsyncState, []byte) (registry.ToolResult, error) {
		return registry.ToolResult{Output: json.RawMessage(`{"key":"k1"}`)}, nil
	}

	if _, err := agentDeliver(h, h.ctx, token, agentCallbackSucceeded); err != nil {
		t.Fatalf("handle callback: %v", err)
	}

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationToolError, domain.FailureCallback)
	if action.Error == nil || action.Error.Code != "TOOL_RESULT_INVALID" {
		t.Errorf("action error = %+v, want TOOL_RESULT_INVALID", action.Error)
	}
	if attempt := agentOnlyToolAttempt(t, h, action.ID); attempt.Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempt status = %s, want FAILED", attempt.Status)
	}
}

// TestAgentAsyncToolCallback_DuplicateDelivery_WritesNothingConsumesNoSeq covers
// duplicate delivery: a re-delivery of the same callback, or a different body for the
// same external task, after the Attempt was resolved is an idempotent duplicate -- no
// state change, no Event, no seq, and OnCallback is not called again.
func TestAgentAsyncToolCallback_DuplicateDelivery_WritesNothingConsumesNoSeq(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-duplicate", tool, nil)

	first, err := agentDeliver(h, h.ctx, token, agentCallbackSucceeded)
	if err != nil || !first.Accepted || first.Duplicate {
		t.Fatalf("first delivery = %+v, %v, want accepted", first, err)
	}
	before := agentSnapshot(t, h, run, outcome)
	callbacksBefore := tool.callbackCount()

	for _, payload := range []string{agentCallbackSucceeded, agentCallbackFailed} {
		got, err := agentDeliver(h, h.ctx, token, payload)
		if err != nil {
			t.Fatalf("duplicate delivery: %v", err)
		}
		if !got.Accepted || !got.Duplicate {
			t.Errorf("duplicate delivery outcome = %+v, want accepted duplicate", got)
		}
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after duplicates = %+v, want unchanged %+v", after, before)
	}
	if tool.callbackCount() != callbacksBefore {
		t.Errorf("OnCallback calls grew from %d to %d on duplicates", callbacksBefore, tool.callbackCount())
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}
}

// TestAgentAsyncToolCallback_LateAfterAgentTimeout_RejectedWithoutMutation covers the
// timeout side of the single-winner conditional-update rule: once the Agent timeout
// transaction failed the waiting Attempt, a late callback writes nothing, consumes no seq
// and leaves the Binding.
func TestAgentAsyncToolCallback_LateAfterAgentTimeout_RejectedWithoutMutation(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-late", tool, nil)

	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	if err := h.svc.TimeoutAgentRun(h.ctx, outcome.AgentRunID); err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}
	before := agentSnapshot(t, h, run, outcome)
	if before.attemptStatus != domain.ToolAttemptFailed || before.runStatus != domain.RunFailed {
		t.Fatalf("precondition: attempt %s run %s, want FAILED after the timeout", before.attemptStatus, before.runStatus)
	}

	got, err := agentDeliver(h, h.ctx, token, agentCallbackSucceeded)
	if err != nil {
		t.Fatalf("late callback: %v", err)
	}
	if !got.Accepted || !got.Duplicate {
		t.Errorf("late callback outcome = %+v, want accepted duplicate", got)
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after late callback = %+v, want unchanged %+v", after, before)
	}
	if before.bindings != 1 {
		t.Errorf("bindings = %d, want the Binding kept", before.bindings)
	}
	if tool.callbackCount() != 0 {
		t.Errorf("OnCallback was called for an Attempt the timeout already resolved")
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if attempt := agentOnlyToolAttempt(t, h, action.ID); attempt.Error == nil || attempt.Error.Code != "TIMEOUT" || len(attempt.Result) != 0 {
		t.Errorf("tool attempt error = %+v result %s, want the TIMEOUT outcome to stand", attempt.Error, attempt.Result)
	}
}

// TestAgentAsyncToolCallback_WrongOrExpiredToken_RejectedWithoutMutation covers the
// callback-token check: a forged token, a valid token of a different Tool Attempt, and an
// expired token are all refused with the credential error before anything is persisted.
func TestAgentAsyncToolCallback_WrongOrExpiredToken_RejectedWithoutMutation(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-token", tool, nil)

	// A second Run's Tool Attempt gets its own, genuinely signed token.
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: remotelookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	tool.result = agentAsyncDispatched("provider_task_agent_async_other")
	other := h.createRun(run.WorkflowID, run.DefinitionVersion, `{"question":"`+agentQuestion+`"}`)
	h.execute(h.claimAgentNode(other.ID))
	otherToken := tool.lastToken(t)
	if otherToken == token {
		t.Fatal("precondition: the second Tool Attempt reused the first token")
	}

	before := agentSnapshot(t, h, run, outcome)
	forged := token[:len(token)-2] + "xx"
	for name, candidate := range map[string]string{"forged": forged, "other attempt": otherToken, "empty": ""} {
		if _, err := agentDeliver(h, h.ctx, candidate, agentCallbackSucceeded); !errors.Is(err, service.ErrInvalidCallbackCredential) {
			t.Errorf("%s token: err = %v, want ErrInvalidCallbackCredential", name, err)
		}
	}

	// The credential outlives the Agent deadline by the Pending TTL and no longer.
	h.clock.Advance(agentTimeoutMs*time.Millisecond + 2*time.Hour)
	if _, err := agentDeliver(h, h.ctx, token, agentCallbackSucceeded); !errors.Is(err, service.ErrInvalidCallbackCredential) {
		t.Errorf("expired token: err = %v, want ErrInvalidCallbackCredential", err)
	}

	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after refused callbacks = %+v, want unchanged %+v", after, before)
	}
	if tool.callbackCount() != 0 {
		t.Errorf("OnCallback was called for a refused credential")
	}
	if status := asyncPendingStatus(h.ctx, t, h.uow, agentAsyncExternalTaskID); status != "absent" {
		t.Errorf("pending callback row = %s, want absent: a refused sender persists nothing", status)
	}
}

// TestAgentAsyncToolCallback_EarlyPendingCallback_ConsumedOnceAfterDispatch covers early
// callback storage for a Tool Attempt: a callback that arrives before the dispatch
// transaction commits is stored as a Pending Callback, and once the Binding commits the
// dispatcher replays it through the same resume use case, consuming the row in the
// transaction that acts on it. A later live re-delivery is a duplicate.
func TestAgentAsyncToolCallback_EarlyPendingCallback_ConsumedOnceAfterDispatch(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	var h *agentHarness
	var early service.CallbackOutcome
	var earlyErr error
	tool.during = func(ctx context.Context, action registry.ToolAction) {
		early, earlyErr = h.svc.HandleCallback(ctx, service.HandleCallback{
			Token:          action.Callback.Token,
			ExternalTaskID: agentAsyncExternalTaskID,
			Payload:        json.RawMessage(agentCallbackSucceeded),
		})
	}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-cb-early", tool)

	h.execute(outcome)

	if earlyErr != nil || !early.Pending || early.Accepted {
		t.Fatalf("early callback = %+v, %v, want stored as pending", early, earlyErr)
	}
	if status := asyncPendingStatus(h.ctx, t, h.uow, agentAsyncExternalTaskID); status != "consumed" {
		t.Fatalf("pending callback row = %s, want consumed", status)
	}
	var pending domain.PendingCallback
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		pending, err = tx.PendingCallbacks().GetByExternalTaskID(ctx, agentAsyncExternalTaskID)
		return err
	}); err != nil {
		t.Fatalf("read pending callback: %v", err)
	}
	if pending.ConsumedAt == nil {
		t.Fatalf("pending callback consumed_at is not stamped")
	}

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED from the replayed callback", action.Status)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	assertSameJSON(t, "tool attempt result", json.RawMessage(agentCallbackResult), attempt.Result)
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("the replayed callback did not continue the Agent Loop to turn 2")
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	types := execEventTypes(events)
	for _, want := range []domain.EventType{domain.EventAgentActionWaiting, domain.EventRunPaused, domain.EventRunResumed} {
		if agentCountEventType(types, want) != 1 {
			t.Errorf("events = %v, want exactly one %s", types, want)
		}
	}
	// Only the Tool Action's completion carries CALLBACK; any later FINAL completion
	// written by whoever advances Turn 2 carries its own source.
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}

	// The Provider re-delivers live: the Attempt is resolved, so nothing more is written.
	seqBefore := agentRunRow(h.ctx, t, h.uow, run.ID).LastSeq
	got, err := agentDeliver(h, h.ctx, tool.lastToken(t), agentCallbackSucceeded)
	if err != nil || !got.Duplicate {
		t.Fatalf("live re-delivery = %+v, %v, want duplicate", got, err)
	}
	if seqAfter := agentRunRow(h.ctx, t, h.uow, run.ID).LastSeq; seqAfter != seqBefore {
		t.Errorf("last seq moved from %d to %d on a duplicate", seqBefore, seqAfter)
	}
	if tool.callbackCount() != 1 {
		t.Errorf("OnCallback calls = %d, want 1", tool.callbackCount())
	}
}

// TestAgentAsyncToolCallback_UninterpretablePayload_LeavesActionWaiting covers an
// uninterpretable callback: a payload the registered Executor cannot interpret is not
// evidence that the external task failed. Nothing is persisted, the Action keeps waiting,
// and a later valid callback still resumes it.
func TestAgentAsyncToolCallback_UninterpretablePayload_LeavesActionWaiting(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-rejected", tool, nil)
	before := agentSnapshot(t, h, run, outcome)

	_, err := agentDeliver(h, h.ctx, token, `{"status":"SOMETHING_ELSE"}`)
	var rejected *service.CallbackPayloadRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("handle callback = %v, want CallbackPayloadRejectedError", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("rejection error carries the callback token")
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after a rejected payload = %+v, want unchanged %+v", after, before)
	}

	got, err := agentDeliver(h, h.ctx, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("valid delivery after a rejected one = %+v, %v, want accepted", got, err)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", action.Status)
	}
}
