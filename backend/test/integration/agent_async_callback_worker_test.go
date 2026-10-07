//go:build integration

// Asynchronous Agent Tool resume tests for what happens after the resume commits and for
// the callback-vs-timeout race. The resume only commits facts: the next READY Turn is
// handed to the bounded in-process work queue, and no model or Tool call ever runs on the
// callback request. A refused enqueue leaves the READY Turn for the Reconciler. They
// share the harnesses of agent_async_tool_test.go and agent_async_callback_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
	"github.com/leungll/Emberling/backend/internal/work"
)

// agentPoolWait bounds how long a test waits for a work.Pool goroutine to finish the Agent
// Loop; it is a failure deadline, never a way to order events.
const agentPoolWait = 10 * time.Second

// agentStartPool starts one work.Pool worker draining queue into h's ExecutionService, the
// way cmd/emberling wires it. Every AfterExecute is signalled on the returned channel so a
// test can wait on the worker path instead of sleeping. The Pool is stopped with the test.
func agentStartPool(t *testing.T, h *agentHarness, queue *work.Queue) <-chan string {
	t.Helper()
	executed := make(chan string, 64)
	pool := work.NewPool(queue, h.svc, 1, work.Hooks{
		AfterExecute: func(runID string) {
			select {
			case executed <- runID:
			default:
			}
		},
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	t.Cleanup(func() {
		cancel()
		pool.Stop()
	})
	return executed
}

// agentAwaitRunStatus re-reads the Run after every Pool item until it reaches want.
func agentAwaitRunStatus(t *testing.T, h *agentHarness, runID string, want domain.RunStatus, executed <-chan string) {
	t.Helper()
	deadline := time.After(agentPoolWait)
	for {
		if got := agentRunRow(h.ctx, t, h.uow, runID).Status; got == want {
			return
		}
		select {
		case <-executed:
		case <-deadline:
			t.Fatalf("run status = %s after %s of Pool work, want %s", agentRunRow(h.ctx, t, h.uow, runID).Status, agentPoolWait, want)
		}
	}
}

// agentRefusingEnqueuer is a WorkEnqueuer whose bounded queue is always full: every offer
// is refused, which the caller must tolerate without rolling anything back.
type agentRefusingEnqueuer struct {
	mu      sync.Mutex
	refused []string
}

func (*agentRefusingEnqueuer) EnqueueAdvance(string) bool { return false }

func (e *agentRefusingEnqueuer) EnqueueAgentTurn(_, turnID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refused = append(e.refused, turnID)
	return false
}

func (e *agentRefusingEnqueuer) refusedTurns() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.refused...)
}

// TestAgentAsyncToolCallback_HandlerReturnsAtResumeCommit_WorkerRunsNextTurn covers
// post-commit handoff: HandleCallback returns as soon as the resume transaction commits,
// with the next Turn still READY -- the callback request makes no model call. The Turn
// reaches the work queue as an AGENT_TURN item, and a work.Pool goroutine, not the
// handler, finishes the Agent Loop and the Run.
func TestAgentAsyncToolCallback_HandlerReturnsAtResumeCommit_WorkerRunsNextTurn(t *testing.T) {
	queue := work.NewQueue(16)
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsyncWith(t, "wf-agent-cb-worker", tool, agentHarnessOptions{Queue: queue})

	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("handle callback = %+v, %v, want accepted", got, err)
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	turns := listReadyTurns(h.ctx, t, h.uow)
	if len(turns) != 1 || turns[0].AgentRunID != agentRun.ID || turns[0].TurnNo != 2 {
		t.Fatalf("ready turns when the callback returned = %+v, want turn 2 still READY: the handler must not run it", turns)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID).Status; got != domain.RunRunning {
		t.Fatalf("run status when the callback returned = %s, want RUNNING", got)
	}

	executed := agentStartPool(t, h, queue)
	agentAwaitRunStatus(t, h, run.ID, domain.RunCompleted, executed)

	if turn := getAgentTurn(h.ctx, t, h.uow, turns[0].ID); turn.Status != domain.AgentTurnCompleted {
		t.Errorf("turn 2 status = %s, want COMPLETED by the worker", turn.Status)
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunSucceeded {
		t.Errorf("agent node run status = %s, want SUCCEEDED", nr.Status)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}
}

// TestAgentAsyncToolCallback_RequestCancelledAtResumeCommit_WorkerStillCompletesLoop covers
// the Provider disconnecting mid-request: the callback's request context is cancelled the
// moment the resume transaction commits and again after HandleCallback returns. The next
// Turn runs under the Pool's own context, so the Agent Loop and the Run still complete.
func TestAgentAsyncToolCallback_RequestCancelledAtResumeCommit_WorkerStillCompletesLoop(t *testing.T) {
	queue := work.NewQueue(16)
	stop := &agentStopNotifier{}
	tool := &agentAsyncTool{}
	h, run, _, token := agentDispatchedAsyncWith(t, "wf-agent-cb-cancelled", tool, agentHarnessOptions{Queue: queue, Notifier: stop})
	executed := agentStartPool(t, h, queue)

	requestCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop.cancel = cancel
	tool.onCallback = func(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
		stop.arm()
		return remotelookup.New("http://mock-provider.invalid", nil).OnCallback(ctx, state, payload)
	}

	got, err := agentDeliver(requestCtx, h, token, agentCallbackSucceeded)
	cancel()
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("handle callback = %+v, %v, want accepted", got, err)
	}

	agentAwaitRunStatus(t, h, run.ID, domain.RunCompleted, executed)
	if turns := listReadyTurns(h.ctx, t, h.uow); len(turns) != 0 {
		t.Errorf("ready turns after completion = %+v, want none", turns)
	}
	assertContiguousSeq(t, listEvents(h.ctx, t, h.uow, run.ID))
}

// TestAgentAsyncToolCallback_QueueRefusesTurn_ReconcilerAdvancesReadyTurn covers the
// persisted-work recovery rule: a full queue refuses the AGENT_TURN item, the resume
// stays committed, and the READY Turn is rediscovered by the Reconciler's ListReady scan,
// which drives the Agent Loop through the same use case to completion.
func TestAgentAsyncToolCallback_QueueRefusesTurn_ReconcilerAdvancesReadyTurn(t *testing.T) {
	enqueuer := &agentRefusingEnqueuer{}
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsyncWith(t, "wf-agent-cb-refused", tool, agentHarnessOptions{Queue: enqueuer})

	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("handle callback = %+v, %v, want accepted despite the refused enqueue", got, err)
	}

	turns := listReadyTurns(h.ctx, t, h.uow)
	if len(turns) != 1 || turns[0].TurnNo != 2 {
		t.Fatalf("ready turns = %+v, want turn 2 left READY for the Reconciler", turns)
	}
	if refused := enqueuer.refusedTurns(); len(refused) != 1 || refused[0] != turns[0].ID {
		t.Errorf("refused AGENT_TURN items = %v, want exactly turn %s", refused, turns[0].ID)
	}
	if action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID); action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want the committed SUCCEEDED to stand", action.Status)
	}

	rec := agentReconciler(h)
	for pass := 0; pass < 4 && agentRunRow(h.ctx, t, h.uow, run.ID).Status != domain.RunCompleted; pass++ {
		agentRunOnce(h, rec)
	}
	if status := agentRunRow(h.ctx, t, h.uow, run.ID).Status; status != domain.RunCompleted {
		t.Fatalf("run status after reconciliation = %s, want COMPLETED", status)
	}
	if turn := getAgentTurn(h.ctx, t, h.uow, turns[0].ID); turn.Status != domain.AgentTurnCompleted {
		t.Errorf("turn 2 status = %s, want COMPLETED by the Reconciler", turn.Status)
	}
	assertContiguousSeq(t, listEvents(h.ctx, t, h.uow, run.ID))
}

// TestAgentAsyncToolCallback_TimeoutCommitsDuringOnCallback_CallbackSuperseded covers the
// race between an async Tool callback and the timeout, and the single-winner
// conditional-update rule. The resume's routing read has seen the Attempt DISPATCHED;
// inside OnCallback -- outside every lock -- the Agent timeout commits. The resume
// transaction's conditional update from DISPATCHED then affects no row, rolls back as
// superseded and reports a Duplicate: nothing is written, no seq is consumed, and the
// Agent ends as TIMEOUT. Both payload branches are covered.
func TestAgentAsyncToolCallback_TimeoutCommitsDuringOnCallback_CallbackSuperseded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		workflowID string
		payload    string
	}{
		{name: "success payload", workflowID: "wf-agent-cb-race-success", payload: agentCallbackSucceeded},
		{name: "provider failure payload", workflowID: "wf-agent-cb-race-failure", payload: agentCallbackFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &agentAsyncTool{}
			h, run, outcome, token := agentDispatchedAsync(t, tc.workflowID, tool, nil)

			var afterTimeout agentWaitingSnapshot
			var timeoutErr error
			tool.onCallback = func(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
				// Barrier: Phase 1 already read DISPATCHED. The timeout commits here, before
				// the resume transaction takes the Run lock.
				h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
				timeoutErr = h.svc.TimeoutAgentRun(ctx, outcome.AgentRunID)
				afterTimeout = agentSnapshot(t, h, run, outcome)
				return remotelookup.New("http://mock-provider.invalid", nil).OnCallback(ctx, state, payload)
			}

			got, err := agentDeliver(h.ctx, h, token, tc.payload)
			if timeoutErr != nil {
				t.Fatalf("timeout agent run inside OnCallback: %v", timeoutErr)
			}
			if err != nil {
				t.Fatalf("handle callback: %v", err)
			}
			if !got.Accepted || !got.Duplicate {
				t.Errorf("callback outcome = %+v, want an accepted duplicate that lost to the timeout", got)
			}
			if afterTimeout.attemptStatus != domain.ToolAttemptFailed || afterTimeout.actionStatus != domain.AgentActionFailed ||
				afterTimeout.runStatus != domain.RunFailed {
				t.Fatalf("state after the timeout = %+v, want the Attempt, Action and Run FAILED", afterTimeout)
			}
			if after := agentSnapshot(t, h, run, outcome); after != afterTimeout {
				t.Fatalf("state after the superseded callback = %+v, want unchanged %+v", after, afterTimeout)
			}

			agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
			if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
				t.Errorf("agent termination = %v, want TIMEOUT", agentRun.Termination)
			}
			action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
			if attempt := agentOnlyToolAttempt(t, h, action.ID); attempt.Error == nil || attempt.Error.Code != "TIMEOUT" || len(attempt.Result) != 0 {
				t.Errorf("tool attempt error = %+v result %s, want the TIMEOUT outcome to stand", attempt.Error, attempt.Result)
			}
			events := listEvents(h.ctx, t, h.uow, run.ID)
			assertContiguousSeq(t, events)
			if n := agentCallbackCompletions(t, events); n != 0 {
				t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 0", n)
			}
			failed := agentLastEventPayload(t, events, domain.EventAgentActionFailed)
			if failed["failureSource"] != string(domain.FailureTimeout) {
				t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want TIMEOUT", failed["failureSource"])
			}
			if agentCountEventType(execEventTypes(events), domain.EventAgentActionFailed) != 1 {
				t.Errorf("events = %v, want exactly one AGENT_ACTION_FAILED", execEventTypes(events))
			}
		})
	}
}

// TestAgentAsyncToolCallback_CallbackAfterDeadlineBeforeTimeout_EndsAsTimeout covers the
// reverse order of the same race: the deadline has passed but the timeout has not run
// yet, so the callback wins the conditional updates. Its result transaction keeps the
// Tool result and ends the Agent as TIMEOUT through its own deadline check, creating no
// next Turn; the later timeout then finds nothing to fail and writes nothing.
func TestAgentAsyncToolCallback_CallbackAfterDeadlineBeforeTimeout_EndsAsTimeout(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-race-reverse", tool, nil)

	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	got, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("callback after the deadline = %+v, %v, want accepted", got, err)
	}

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED: the Tool result arrived", action.Status)
	}
	if attempt := agentOnlyToolAttempt(t, h, action.ID); attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent termination = %v, want TIMEOUT from the result transaction's deadline check", agentRun.Termination)
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("a next Turn was created after the deadline")
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nr.Status)
	}
	if status := agentRunRow(h.ctx, t, h.uow, run.ID).Status; status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", status)
	}

	before := agentSnapshot(t, h, run, outcome)
	if err := h.svc.TimeoutAgentRun(h.ctx, outcome.AgentRunID); err != nil {
		t.Fatalf("timeout agent run after the callback: %v", err)
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after the late timeout = %+v, want unchanged %+v", after, before)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}
}

// TestAgentAsyncToolCallback_ToolNoLongerRegistered_RegistryResolutionError covers
// registry drift on resume (CLAUDE.md "Extensions and external calls"): a Backend whose
// Tool Registry no longer has the Attempt's ASYNC Tool refuses the callback with the same
// RegistryResolutionError the async Node resume uses, never substitutes another Tool and
// changes nothing, so the Action keeps waiting.
func TestAgentAsyncToolCallback_ToolNoLongerRegistered_RegistryResolutionError(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-cb-drift", tool, nil)
	before := agentSnapshot(t, h, run, outcome)

	// A restarted Backend over the same facts, without `remote_lookup` registered.
	restarted := newAgentHarness(t, agentHarnessOptions{Pool: h.pool, Clock: h.clock})
	_, err := restarted.svc.HandleCallback(restarted.ctx, service.HandleCallback{
		Token:          token,
		ExternalTaskID: agentAsyncExternalTaskID,
		Payload:        json.RawMessage(agentCallbackSucceeded),
	})
	var resolution *service.RegistryResolutionError
	if !errors.As(err, &resolution) {
		t.Fatalf("handle callback = %v, want RegistryResolutionError", err)
	}
	if resolution.WorkflowID != run.WorkflowID || resolution.Version != run.DefinitionVersion {
		t.Errorf("resolution error = %+v, want the Run's workflow %s v%d", resolution, run.WorkflowID, run.DefinitionVersion)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), agentSHA256Hex(token)) {
		t.Errorf("resolution error carries the callback token or its hash")
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after the refused callback = %+v, want unchanged %+v", after, before)
	}
	if tool.callbackCount() != 0 {
		t.Errorf("OnCallback was called although the Tool is no longer registered")
	}
}
