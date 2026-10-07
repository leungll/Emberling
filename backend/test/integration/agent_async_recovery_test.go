//go:build integration

// Recovery tests for asynchronous Agent Tool Attempts. A Tool Action that is
// WAITING_CALLBACK holds no in-process state worth keeping: the DISPATCHED Tool Attempt,
// its Callback Binding, the Agent deadline and any stored early callback are all
// committed rows, so a fresh ExecutionService and Reconciler over the same database must
// be able to finish, time out or replay it without re-executing the Action (the
// persisted-work recovery rule). They share the agentHarness of agent_loop_test.go and
// the scripted async Tool of agent_async_tool_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
)

// agentAsyncRestart is a process restart while an async Tool Action waits: a second
// ExecutionService over the crashed one's database and clock, with its own `remote_lookup`
// Executor, its own Mock Model Provider scripted to answer FINAL, and no queue, notifier or
// memory carried across. The returned counter records its model calls.
func agentAsyncRestart(t *testing.T, crashed *agentHarness, tool *agentAsyncTool) (*agentHarness, func() int32) {
	t.Helper()
	registration := remotelookup.Registration("http://mock-provider.invalid", nil)
	registration.Executor = tool
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool:  crashed.pool,
		Clock: crashed.clock,
		Tools: []registry.ToolRegistration{registration},
	})
	calls := agentScriptFinal(restarted, mockmodel.Scenario{})
	return restarted, func() int32 { return calls.Load() }
}

// agentRunOnceUntil runs reconciliation passes until the Run reaches want, bounded so a
// Reconciler that stops making progress fails instead of spinning.
func agentRunOnceUntil(t *testing.T, h *agentHarness, runID string, want domain.RunStatus) {
	t.Helper()
	rec := agentReconciler(h)
	for pass := 0; pass < 8; pass++ {
		if agentRunRow(h.ctx, t, h.uow, runID).Status == want {
			return
		}
		agentRunOnce(h, rec)
	}
	if got := agentRunRow(h.ctx, t, h.uow, runID).Status; got != want {
		t.Fatalf("run status after bounded reconciliation = %s, want %s", got, want)
	}
}

func agentPendingRow(t *testing.T, h *agentHarness, externalTaskID string) (domain.PendingCallback, bool) {
	t.Helper()
	var row domain.PendingCallback
	found := true
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = tx.PendingCallbacks().GetByExternalTaskID(ctx, externalTaskID)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("read pending callback %s: %v", externalTaskID, err)
	}
	return row, found
}

// agentAsyncForeignPending drives a Run until its Tool Action is WAITING_CALLBACK with an
// unconsumed Pending Callback row for its external task whose credential hash is not the
// Attempt's. The row is stored from inside the Tool call, before the Binding commits,
// which is exactly when an early delivery is stored; the post-commit replay then refuses
// it.
func agentAsyncForeignPending(t *testing.T, workflowID string, tool *agentAsyncTool) (*agentHarness, domain.Run, service.AdvanceOutcome) {
	t.Helper()
	tool.result = agentAsyncDispatched(agentAsyncExternalTaskID)
	var h *agentHarness
	var seedErr error
	tool.during = func(ctx context.Context, _ registry.ToolAction) {
		now := h.clock.Now()
		seedErr = h.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			_, err := tx.PendingCallbacks().Record(ctx, domain.PendingCallback{
				ExternalTaskID:    agentAsyncExternalTaskID,
				Payload:           json.RawMessage(agentCallbackSucceeded),
				PayloadHash:       agentSHA256Hex(agentCallbackSucceeded),
				CallbackTokenHash: agentSHA256Hex("a-credential-minted-for-another-attempt"),
				ReceivedAt:        now,
				ExpiresAt:         now.Add(time.Hour),
			})
			return err
		})
	}
	h, run, outcome := newAgentAsyncHarness(t, workflowID, tool)
	h.execute(outcome)
	if seedErr != nil {
		t.Fatalf("seed foreign pending callback: %v", seedErr)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
	}
	if row, found := agentPendingRow(t, h, agentAsyncExternalTaskID); !found || row.ConsumedAt != nil {
		t.Fatalf("precondition: pending row found=%v consumedAt=%v, want stored and unconsumed", found, row.ConsumedAt)
	}
	return h, run, outcome
}

// TestAgentAsyncToolRecovery_LostEarlyCallbackReplay_ReconcilerConsumesOnceAfterRestart
// covers the persisted-work recovery rule for a Tool target: the Provider's early
// callback was stored as Pending before the Binding committed, and the process died
// between that commit and the in-process replay. After a restart the Reconciler must
// rediscover the row through its Binding to the DISPATCHED Tool Attempt, replay it
// through ResumeNode exactly once, stamp consumed_at, and let the Agent Loop finish --
// without calling the Tool again and without a second CALLBACK completion.
func TestAgentAsyncToolRecovery_LostEarlyCallbackReplay_ReconcilerConsumesOnceAfterRestart(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &agentStopNotifier{cancel: cancel}

	var h *agentHarness
	var early service.CallbackOutcome
	var earlyErr error
	tool.during = func(ctx context.Context, action registry.ToolAction) {
		early, earlyErr = h.svc.HandleCallback(ctx, service.HandleCallback{
			Token:          action.Callback.Token,
			ExternalTaskID: agentAsyncExternalTaskID,
			Payload:        json.RawMessage(agentCallbackSucceeded),
		})
		// The next commit is the dispatch transaction; the process "dies" right after it,
		// before the post-commit replay of this stored delivery can read it.
		stop.arm()
	}
	h, run, outcome := newAgentAsyncHarnessWithNotifier(t, "wf-agent-recover-lost-replay", tool, stop)

	if err := h.svc.Execute(ctx, outcome); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("execute agent action: %v", err)
	}
	if earlyErr != nil || !early.Pending {
		t.Fatalf("early callback = %+v, %v, want stored as pending", early, earlyErr)
	}
	token := tool.lastToken(t)
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK (the replay was lost)", action.Status)
	}
	if status := asyncPendingStatus(h.ctx, t, h.uow, agentAsyncExternalTaskID); status != "stored" {
		t.Fatalf("precondition: pending row = %s, want stored and unconsumed", status)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)

	restartedTool := &agentAsyncTool{}
	restarted, modelCalls := agentAsyncRestart(t, h, restartedTool)
	rec := agentReconciler(restarted)
	report := agentRunOnce(restarted, rec)
	if report.ConsumablePendingFound != 1 || report.PendingCallbacksResumed != 1 {
		t.Fatalf("reconciler report = %+v, want the stored Tool callback found and resumed once", report)
	}

	pending, found := agentPendingRow(t, restarted, agentAsyncExternalTaskID)
	if !found || pending.ConsumedAt == nil {
		t.Fatalf("pending row found=%v consumedAt=%v, want kept and stamped consumed", found, pending.ConsumedAt)
	}
	action, _ = agentActionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED from the replayed callback", action.Status)
	}
	attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID)
	if len(attempts) != 1 || attempts[0].ID != attempt.ID || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Fatalf("tool attempts = %+v, want the original Attempt %s SUCCEEDED and no other", attempts, attempt.ID)
	}
	assertSameJSON(t, "tool attempt result", json.RawMessage(agentCallbackResult), attempts[0].Result)
	if restartedTool.count() != 0 || restartedTool.callbackCount() != 1 {
		t.Errorf("restarted tool Execute=%d OnCallback=%d, want 0 and 1", restartedTool.count(), restartedTool.callbackCount())
	}
	if tool.count() != 1 {
		t.Errorf("crashed tool Execute = %d, want 1", tool.count())
	}

	// A second pass finds nothing left to consume: the row is already stamped.
	if again := agentRunOnce(restarted, rec); again.ConsumablePendingFound != 0 || again.PendingCallbacksResumed != 0 {
		t.Errorf("second pass report = %+v, want no consumable pending callback", again)
	}
	agentRunOnceUntil(t, restarted, run.ID, domain.RunCompleted)
	if got := modelCalls(); got != 1 {
		t.Errorf("model calls after the restart = %d, want 1 (turn 2 only)", got)
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}
	types := execEventTypes(events)
	for _, want := range []domain.EventType{domain.EventAgentActionWaiting, domain.EventRunResumed, domain.EventAgentCompleted} {
		if agentCountEventType(types, want) != 1 {
			t.Errorf("events = %v, want exactly one %s", types, want)
		}
	}
	assertAgentTokenAbsent(t, restarted, run.ID, token)
}

// TestAgentAsyncToolRecovery_PendingCredentialMismatch_ReconcilerLeavesActionWaiting
// covers the Tool-target half of the Pending credential check: a stored early callback
// whose credential hash is not the bound Tool Attempt's is found by the Reconciler but
// refused inside the resume transaction, which rolls back -- the row stays unconsumed,
// the Action stays WAITING_CALLBACK and no Event is written. The Provider's own
// authenticated callback still resumes the Attempt afterwards.
func TestAgentAsyncToolRecovery_PendingCredentialMismatch_ReconcilerLeavesActionWaiting(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome := agentAsyncForeignPending(t, "wf-agent-recover-mismatch", tool)
	token := tool.lastToken(t)
	before := agentSnapshot(t, h, run, outcome)

	report := agentRunOnce(h, agentReconciler(h))
	if report.ConsumablePendingFound != 1 || report.PendingCallbacksResumed != 0 {
		t.Fatalf("reconciler report = %+v, want the row found but not resumed", report)
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after the refused replay = %+v, want unchanged %+v", after, before)
	}
	if row, _ := agentPendingRow(t, h, agentAsyncExternalTaskID); row.ConsumedAt != nil {
		t.Fatalf("pending consumedAt = %v, want nil: the refusing transaction rolled back", row.ConsumedAt)
	}

	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate || got.Pending {
		t.Fatalf("authenticated callback = %+v, %v, want accepted", got, err)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED from the authenticated callback", action.Status)
	}
	// The Attempt is resolved, so the foreign row no longer routes anywhere.
	if again := agentRunOnce(h, agentReconciler(h)); again.ConsumablePendingFound != 0 {
		t.Errorf("reconciler report after the resume = %+v, want no consumable pending callback", again)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}
}

// TestAgentAsyncToolRecovery_PendingRetention_DeletedOnlyAfterExpiry covers Pending
// Callback retention for Tool targets: the Reconciler's TTL sweep keeps a Tool-target
// Pending row -- consumed or not -- while it is inside its TTL, and removes it once
// expires_at has passed.
func TestAgentAsyncToolRecovery_PendingRetention_DeletedOnlyAfterExpiry(t *testing.T) {
	sweepAround := func(t *testing.T, h *agentHarness) {
		t.Helper()
		rec := agentReconciler(h)
		row, found := agentPendingRow(t, h, agentAsyncExternalTaskID)
		if !found {
			t.Fatalf("precondition: no pending row")
		}

		h.clock.Advance(row.ExpiresAt.Sub(h.clock.Now()) - time.Millisecond)
		if report := agentRunOnce(h, rec); report.ExpiredPendingDeleted != 0 {
			t.Errorf("sweep one millisecond before expiry deleted %d rows, want 0", report.ExpiredPendingDeleted)
		}
		if kept, found := agentPendingRow(t, h, agentAsyncExternalTaskID); !found || !kept.ExpiresAt.Equal(row.ExpiresAt) {
			t.Fatalf("pending row before expiry found=%v, want kept unchanged", found)
		}

		h.clock.Advance(time.Millisecond)
		report := agentRunOnce(h, rec)
		if report.ConsumablePendingFound != 0 || report.ExpiredPendingDeleted != 1 {
			t.Errorf("sweep at expiry report = %+v, want the expired row deleted, never consumed", report)
		}
		if _, found := agentPendingRow(t, h, agentAsyncExternalTaskID); found {
			t.Errorf("pending row still present after its expiry")
		}
	}

	t.Run("consumed", func(t *testing.T) {
		tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
		var h *agentHarness
		tool.during = func(ctx context.Context, action registry.ToolAction) {
			if _, err := h.svc.HandleCallback(ctx, service.HandleCallback{
				Token:          action.Callback.Token,
				ExternalTaskID: agentAsyncExternalTaskID,
				Payload:        json.RawMessage(agentCallbackSucceeded),
			}); err != nil {
				t.Errorf("early callback: %v", err)
			}
		}
		var outcome service.AdvanceOutcome
		h, _, outcome = newAgentAsyncHarness(t, "wf-agent-retention-consumed", tool)
		h.execute(outcome)
		if status := asyncPendingStatus(h.ctx, t, h.uow, agentAsyncExternalTaskID); status != "consumed" {
			t.Fatalf("precondition: pending row = %s, want consumed by the post-dispatch replay", status)
		}
		sweepAround(t, h)
	})

	t.Run("unconsumed", func(t *testing.T) {
		h, _, _ := agentAsyncForeignPending(t, "wf-agent-retention-unconsumed", &agentAsyncTool{})
		sweepAround(t, h)
	})
}

// TestAgentAsyncToolRecovery_RestartWhileWaiting_NoReexecutionThenCallbackCompletes
// covers a restart while an async Agent Tool waits: the callback resumes the original
// Tool Attempt. A restarted Reconciler pass over a WAITING_CALLBACK Tool Action finds no
// work: it neither re-executes the Action nor creates an Attempt nor calls the model. The
// Provider's callback, carrying the token the crashed process minted, then resumes that
// very Attempt and Action on the restarted process, and reconciliation finishes the loop.
func TestAgentAsyncToolRecovery_RestartWhileWaiting_NoReexecutionThenCallbackCompletes(t *testing.T) {
	tool := &agentAsyncTool{}
	crashed, run, outcome, token := agentDispatchedAsync(t, "wf-agent-recover-restart", tool, nil)
	action, _ := agentActionOfTurn(crashed.ctx, t, crashed.uow, outcome.AgentTurnID)
	attempt := agentOnlyToolAttempt(t, crashed, action.ID)
	before := agentSnapshot(t, crashed, run, outcome)

	restartedTool := &agentAsyncTool{}
	restarted, modelCalls := agentAsyncRestart(t, crashed, restartedTool)
	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ReadyAgentActionsFound != 0 || report.ReadyAgentTurnsFound != 0 || report.ExpiredAgentRunsFound != 0 || report.ConsumablePendingFound != 0 {
		t.Fatalf("reconciler report = %+v, want no Agent work while the Action waits", report)
	}
	if after := agentSnapshot(t, restarted, run, outcome); after != before {
		t.Fatalf("state after the restarted pass = %+v, want unchanged %+v", after, before)
	}
	if restartedTool.count() != 0 || modelCalls() != 0 {
		t.Fatalf("restarted pass: tool calls = %d model calls = %d, want 0 and 0", restartedTool.count(), modelCalls())
	}
	if attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID); len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want the original one only", len(attempts))
	}

	got, err := agentDeliver(restarted.ctx, restarted, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate || got.Pending || got.RunID != run.ID {
		t.Fatalf("callback after the restart = %+v, %v, want accepted for run %s", got, err, run.ID)
	}
	if restartedTool.callbackCount() != 1 {
		t.Fatalf("restarted OnCallback calls = %d, want 1", restartedTool.callbackCount())
	}
	if state := restartedTool.callbacks[0]; state.ActionID != action.ID || state.AttemptNo != 1 {
		t.Errorf("OnCallback state = %+v, want the original Action %s Attempt 1", state, action.ID)
	}
	attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID)
	if len(attempts) != 1 || attempts[0].ID != attempt.ID || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Fatalf("tool attempts = %+v, want the original Attempt %s SUCCEEDED", attempts, attempt.ID)
	}

	agentRunOnceUntil(t, restarted, run.ID, domain.RunCompleted)
	if got := modelCalls(); got != 1 {
		t.Errorf("model calls after the restart = %d, want 1 (turn 2 only)", got)
	}
	if restartedTool.count() != 0 || tool.count() != 1 {
		t.Errorf("tool Execute calls crashed=%d restarted=%d, want 1 and 0", tool.count(), restartedTool.count())
	}
	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}
	if agentCountEventType(execEventTypes(events), domain.EventAgentActionStarted) != 2 {
		t.Errorf("events = %v, want two AGENT_ACTION_STARTED (the Tool Action and turn 2's FINAL), no re-execution", execEventTypes(events))
	}
	assertAgentTokenAbsent(t, restarted, run.ID, token)
}

// TestAgentAsyncToolRecovery_RestartPastDeadline_ReconcilerTimesOutAgent covers the Agent
// deadline after a restart: the Agent deadline is a committed fact, so a restarted
// Reconciler that finds it passed ends the Agent as TIMEOUT through TimeoutAgentRun,
// closing the DISPATCHED Attempt and the WAITING_CALLBACK Action; the late callback that
// follows changes nothing.
func TestAgentAsyncToolRecovery_RestartPastDeadline_ReconcilerTimesOutAgent(t *testing.T) {
	tool := &agentAsyncTool{}
	crashed, run, outcome, token := agentDispatchedAsync(t, "wf-agent-recover-deadline", tool, nil)

	restartedTool := &agentAsyncTool{}
	restarted, modelCalls := agentAsyncRestart(t, crashed, restartedTool)
	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ExpiredAgentRunsFound != 1 || report.AgentRunsTimedOut != 1 {
		t.Fatalf("reconciler report = %+v, want one expired Agent Run timed out", report)
	}

	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent termination = %v, want TIMEOUT", agentRun.Termination)
	}
	action, _ := agentActionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionFailed || action.CompletedAt == nil {
		t.Errorf("action status = %s completedAt = %v, want FAILED and closed", action.Status, action.CompletedAt)
	}
	attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed || attempts[0].Error == nil || attempts[0].Error.Code != "TIMEOUT" {
		t.Fatalf("tool attempts = %+v, want the original Attempt FAILED with TIMEOUT", attempts)
	}
	if nr := agentNodeRun(restarted.ctx, t, restarted.uow, run.ID); nr.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nr.Status)
	}
	if persisted := agentRunRow(restarted.ctx, t, restarted.uow, run.ID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	if restartedTool.count() != 0 || restartedTool.callbackCount() != 0 || modelCalls() != 0 {
		t.Errorf("restarted tool Execute=%d OnCallback=%d model=%d, want all 0", restartedTool.count(), restartedTool.callbackCount(), modelCalls())
	}

	after := agentSnapshot(t, restarted, run, outcome)
	got, err := agentDeliver(restarted.ctx, restarted, token, agentCallbackSucceeded)
	if err != nil || !got.Duplicate {
		t.Fatalf("late callback = %+v, %v, want a duplicate", got, err)
	}
	if late := agentSnapshot(t, restarted, run, outcome); late != after {
		t.Errorf("state after the late callback = %+v, want unchanged %+v", late, after)
	}
	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 0 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 0", n)
	}
}

// TestAgentAsyncToolRecovery_ReconcilerTimeoutDuringOnCallback_CallbackSuperseded covers
// the race between an async Tool callback and the timeout, with the Reconciler as the
// timeout's caller. The barrier is OnCallback: the callback's unlocked routing read has
// already seen DISPATCHED, then a full Reconciler pass past the deadline commits the
// TIMEOUT before the resume transaction takes the Run lock. The conditional updates make
// the timeout the single winner: the callback is answered as a duplicate and writes
// nothing.
func TestAgentAsyncToolRecovery_ReconcilerTimeoutDuringOnCallback_CallbackSuperseded(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-recover-race-timeout-first", tool, nil)
	rec := agentReconciler(h)

	var report struct{ found, timedOut int }
	var afterTimeout agentWaitingSnapshot
	tool.onCallback = func(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
		h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
		r := agentRunOnce(h, rec)
		report.found, report.timedOut = r.ExpiredAgentRunsFound, r.AgentRunsTimedOut
		afterTimeout = agentSnapshot(t, h, run, outcome)
		return remotelookup.New("http://mock-provider.invalid", nil).OnCallback(ctx, state, payload)
	}

	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || !got.Duplicate {
		t.Fatalf("callback = %+v, %v, want an accepted duplicate that lost to the timeout", got, err)
	}
	if report.found != 1 || report.timedOut != 1 {
		t.Fatalf("reconciler pass inside OnCallback found=%d timedOut=%d, want 1 and 1", report.found, report.timedOut)
	}
	if afterTimeout.attemptStatus != domain.ToolAttemptFailed || afterTimeout.actionStatus != domain.AgentActionFailed || afterTimeout.runStatus != domain.RunFailed {
		t.Fatalf("state after the timeout = %+v, want the Attempt, Action and Run FAILED", afterTimeout)
	}
	if after := agentSnapshot(t, h, run, outcome); after != afterTimeout {
		t.Fatalf("state after the superseded callback = %+v, want unchanged %+v", after, afterTimeout)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent termination = %v, want TIMEOUT", agentRun.Termination)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 0 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 0", n)
	}
	if agentCountEventType(execEventTypes(events), domain.EventAgentActionFailed) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_ACTION_FAILED", execEventTypes(events))
	}
}

// TestAgentAsyncToolRecovery_CallbackCommitsBeforeReconcilerTimeout_ReconcilerWritesNothing
// is the reverse order of the same race: the deadline has passed, but the callback's
// resume transaction commits first and ends the Agent as TIMEOUT through its own deadline
// check, keeping the Tool result. The Reconciler pass that follows finds no unterminated
// expired Agent Run and writes nothing. This test orders the two sequentially; the
// interleaving in which the timeout caller already listed the Agent Run as expired (a
// stale list) and only calls TimeoutAgentRun after the callback committed is covered by
// TestAgentAsyncToolCallback_CallbackAfterDeadlineBeforeTimeout_EndsAsTimeout, whose late
// TimeoutAgentRun must find nothing to fail. Together they cover the race between an
// async Tool callback and the timeout in the callback-first direction.
func TestAgentAsyncToolRecovery_CallbackCommitsBeforeReconcilerTimeout_ReconcilerWritesNothing(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-recover-race-callback-first", tool, nil)

	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("callback after the deadline = %+v, %v, want accepted", got, err)
	}
	afterCallback := agentSnapshot(t, h, run, outcome)
	if afterCallback.attemptStatus != domain.ToolAttemptSucceeded || afterCallback.actionStatus != domain.AgentActionSucceeded {
		t.Fatalf("state after the callback = %+v, want the Attempt and Action SUCCEEDED", afterCallback)
	}

	report := agentRunOnce(h, agentReconciler(h))
	if report.ExpiredAgentRunsFound != 0 || report.AgentRunsTimedOut != 0 {
		t.Errorf("reconciler report = %+v, want no expired Agent Run left", report)
	}
	if after := agentSnapshot(t, h, run, outcome); after != afterCallback {
		t.Fatalf("state after the reconciler pass = %+v, want unchanged %+v", after, afterCallback)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent termination = %v, want TIMEOUT", agentRun.Termination)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}
	if agentCountEventType(execEventTypes(events), domain.EventAgentActionFailed) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_FAILED", execEventTypes(events))
	}
}

// TestAgentAsyncToolRecovery_CallbackAtMaxTurns_EndsAsMaxTurns covers the round bound at
// resume time: when the callback resolves the Tool Action of the Agent's last allowed
// Turn, the resume transaction keeps the Tool result and ends the Agent as MAX_TURNS
// instead of creating a Turn nobody may run.
func TestAgentAsyncToolRecovery_CallbackAtMaxTurns_EndsAsMaxTurns(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	h, run, outcome := newAgentAsyncHarnessMaxTurns(t, "wf-agent-recover-max-turns", tool, agentHarnessOptions{}, 1)
	h.execute(outcome)
	token := tool.lastToken(t)

	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", got, err)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", action.Status)
	}
	if attempt := agentOnlyToolAttempt(t, h, action.ID); attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationMaxTurns {
		t.Errorf("agent termination = %v, want MAX_TURNS", agentRun.Termination)
	}
	if turns := listReadyTurns(h.ctx, t, h.uow); len(turns) != 0 {
		t.Errorf("ready turns = %d, want 0", len(turns))
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nr.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentTurnReady) != 1 {
		t.Errorf("events = %v, want no second AGENT_TURN_READY", types)
	}
	if agentCountEventType(types, domain.EventAgentFailed) != 1 || agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("events = %v, want one AGENT_FAILED and one NODE_FAILED", types)
	}
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}
}
