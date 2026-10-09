//go:build integration

// Provider Poll results entering the single idempotent resume use case. A poll result is
// already normalized, so it skips the Executor's OnCallback step and nothing else: the
// Callback Binding routes it, the conditional update from DISPATCHED elects the single
// winner, and the state change and its Events commit in one transaction. These guarantees
// are transactional or races, so they run against real PostgreSQL on the async harness of
// service_async_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

var pollUnknownSide = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}

func pollSucceeded(text string) registry.PollResult {
	return registry.PollResult{
		Status: registry.PollSucceeded,
		Output: &registry.NodeOutput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`"` + text + `"`)}},
	}
}

func pollFailed() registry.PollResult {
	return registry.PollResult{
		Status: registry.PollFailed,
		Error:  &domain.ExecutionError{Code: "PROVIDER_TASK_FAILED", Message: "provider reported the task failed"},
	}
}

func (h *asyncHarness) resumePolled(dispatch asyncDispatch, result registry.PollResult) (service.ResumeOutcome, error) {
	return h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: dispatch.externalTaskID,
		Polled:         &service.PolledResult{AttemptID: dispatch.attemptID, Result: result},
	})
}

// pollWaitingFacts is everything a poll that must change nothing has to leave as it was.
type pollWaitingFacts struct {
	attemptStatus domain.NodeAttemptStatus
	nodeRunStatus domain.NodeRunStatus
	runStatus     domain.RunStatus
	lastSeq       int64
	events        int
}

func (h *asyncHarness) pollFacts(dispatch asyncDispatch) pollWaitingFacts {
	h.t.Helper()
	run := getRun(h.ctx, h.t, h.uow, dispatch.run.ID)
	return pollWaitingFacts{
		attemptStatus: getAttempt(h.ctx, h.t, h.uow, dispatch.attemptID).Status,
		nodeRunStatus: getNodeRun(h.ctx, h.t, h.uow, dispatch.nodeRunID).Status,
		runStatus:     run.Status,
		lastSeq:       run.LastSeq,
		events:        len(listEvents(h.ctx, h.t, h.uow, dispatch.run.ID)),
	}
}

func decodeCompletionSource(t *testing.T, ev domain.Event) string {
	t.Helper()
	var payload struct {
		CompletionSource string `json:"completionSource"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("decode %s payload: %v", ev.Type, err)
	}
	return payload.CompletionSource
}

// TestResumeNode_PollSucceeded_CompletesAttemptWithProviderPollSource proves a successful
// poll result completes the DISPATCHED Attempt through the shared resume transaction: the
// Attempt and NodeRun succeed, NODE_COMPLETED records PROVIDER_POLL and takes the next
// seq directly (no NODE_CALLBACK_RECEIVED is written), and the Run advances exactly as it
// does after a callback.
func TestResumeNode_PollSucceeded_CompletesAttemptWithProviderPollSource(t *testing.T) {
	h := newAsyncHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-succeeded")
	beforeSeq := getRun(h.ctx, t, h.uow, run.ID).LastSeq

	outcome, err := h.resumePolled(dispatch, pollSucceeded("polled"))
	if err != nil {
		t.Fatalf("resume from poll: %v", err)
	}
	if outcome.Duplicate || outcome.Failed {
		t.Fatalf("poll resume outcome: want a fresh completion, got %+v", outcome)
	}
	if outcome.RunID != run.ID || outcome.NodeRunID != dispatch.nodeRunID || outcome.AttemptID != dispatch.attemptID {
		t.Fatalf("poll resume routed to %+v, want run %s node run %s attempt %s", outcome, run.ID, dispatch.nodeRunID, dispatch.attemptID)
	}
	if got := h.exec.callbacks; got != 0 {
		t.Fatalf("OnCallback calls for a normalized poll result: want 0, got %d", got)
	}

	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).Status; got != domain.NodeAttemptSucceeded {
		t.Fatalf("Attempt status after a successful poll: want SUCCEEDED, got %s", got)
	}
	if got := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun status after a successful poll: want SUCCEEDED, got %s", got)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	if got := asyncEventsOfType(events, domain.EventNodeCallbackReceived); len(got) != 0 {
		t.Fatalf("NODE_CALLBACK_RECEIVED after a poll completion: want none, got %v", execEventTypes(events))
	}
	completed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCompleted)
	if len(completed) != 1 {
		t.Fatalf("NODE_COMPLETED for the async Node: want 1, got %v", execEventTypes(events))
	}
	if completed[0].Seq != beforeSeq+1 {
		t.Fatalf("NODE_COMPLETED seq: want %d (one step after the waiting state), got %d", beforeSeq+1, completed[0].Seq)
	}
	if got := decodeCompletionSource(t, completed[0]); got != string(domain.CompletionProviderPoll) {
		t.Fatalf("NODE_COMPLETED completionSource: want PROVIDER_POLL, got %q", got)
	}
	if len(asyncEventsOfType(events, domain.EventRunResumed)) != 1 {
		t.Fatalf("RUN_RESUMED after the poll completion: want 1, got %v", execEventTypes(events))
	}
	if got := asyncNodeRun(h.ctx, t, h.uow, run.ID, "output").Status; got != domain.NodeRunReady {
		t.Fatalf("downstream Output NodeRun after the poll completion: want READY, got %s", got)
	}

	for i := 0; i < 10; i++ {
		next, err := h.svc.Advance(h.ctx, run.ID)
		if err != nil {
			t.Fatalf("advance after poll completion: %v", err)
		}
		if !next.Claimed {
			break
		}
		if err := h.svc.Execute(h.ctx, next); err != nil {
			t.Fatalf("execute after poll completion: %v", err)
		}
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("Run status after the poll completion and drain: want COMPLETED, got %s", got)
	}
}

// TestResumeNode_PollFailed_FailsAttemptWithProviderPollSource proves a Provider failure
// reported by poll takes the same decision as one reported by callback: the waiting
// Attempt and NodeRun fail terminally, with no re-dispatch even though the Node is
// EXTERNAL+KEYED with attempts to spare, and the failure source is PROVIDER_POLL.
func TestResumeNode_PollFailed_FailsAttemptWithProviderPollSource(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-failed")

	outcome, err := h.resumePolled(dispatch, pollFailed())
	if err != nil {
		t.Fatalf("resume from failed poll: %v", err)
	}
	if outcome.Duplicate || !outcome.Failed || outcome.FailureSource != domain.FailureProviderPoll {
		t.Fatalf("failed poll outcome: want Failed with source PROVIDER_POLL, got %+v", outcome)
	}

	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptFailed {
		t.Fatalf("Attempt status after a failed poll: want FAILED, got %s", attempt.Status)
	}
	if attempt.Error == nil || attempt.Error.Code != "PROVIDER_TASK_FAILED" {
		t.Fatalf("Attempt error after a failed poll: want PROVIDER_TASK_FAILED, got %+v", attempt.Error)
	}
	nodeRun := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID)
	if nodeRun.Status != domain.NodeRunFailed || nodeRun.NextAttemptAt != nil {
		t.Fatalf("NodeRun after a failed poll: want FAILED with no retry scheduled, got %s / %v", nodeRun.Status, nodeRun.NextAttemptAt)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if len(asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeRetrying)) != 0 {
		t.Fatalf("NODE_RETRYING after a failed poll: want none, got %v", execEventTypes(events))
	}
	if len(asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeFailed)) != 1 {
		t.Fatalf("NODE_FAILED after a failed poll: want 1, got %v", execEventTypes(events))
	}
	if len(asyncEventsOfType(events, domain.EventNodeCallbackReceived)) != 0 {
		t.Fatalf("NODE_CALLBACK_RECEIVED after a failed poll: want none, got %v", execEventTypes(events))
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Fatalf("Run status after its only live NodeRun failed: want FAILED, got %s", got)
	}

	h.clock.Advance(time.Minute)
	next, err := h.svc.Advance(h.ctx, run.ID)
	if err != nil {
		t.Fatalf("advance after a failed poll: %v", err)
	}
	if next.Claimed || h.exec.dispatchCount() != 1 {
		t.Fatalf("after a failed poll: want no new claim and one external task, got claimed=%v dispatches=%d", next.Claimed, h.exec.dispatchCount())
	}
}

// assertPollChangesNothing resumes with a non-final poll result and proves it wrote
// nothing, then that the Attempt is still completable by its callback.
func assertPollChangesNothing(t *testing.T, status registry.PollStatus) {
	t.Helper()
	h := newAsyncHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-"+string(status))
	before := h.pollFacts(dispatch)

	outcome, err := h.resumePolled(dispatch, registry.PollResult{Status: status})
	if err != nil {
		t.Fatalf("resume with poll status %s: want no error, got %v", status, err)
	}
	if outcome.Duplicate || outcome.Failed {
		t.Fatalf("poll status %s outcome: want neither duplicate nor failed, got %+v", status, outcome)
	}
	if after := h.pollFacts(dispatch); after != before {
		t.Fatalf("facts after poll status %s: want unchanged %+v, got %+v", status, before, after)
	}
	if before.attemptStatus != domain.NodeAttemptDispatched || before.nodeRunStatus != domain.NodeRunWaitingCallback {
		t.Fatalf("precondition: want DISPATCHED / WAITING_CALLBACK, got %+v", before)
	}

	final, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"generated"}`)
	if err != nil || !final.Accepted || final.Duplicate {
		t.Fatalf("callback after poll status %s: want accepted, got %+v / %v", status, final, err)
	}
}

// TestResumeNode_PollRunning_NoStateChangeNoEvent proves a RUNNING poll result writes
// nothing and returns without error.
func TestResumeNode_PollRunning_NoStateChangeNoEvent(t *testing.T) {
	assertPollChangesNothing(t, registry.PollRunning)
}

// TestResumeNode_PollUnknown_NoStateChangeNoEvent proves a poll result the Provider
// could not answer -- normalized as UNKNOWN -- and a status this service does not
// recognise both write nothing and return without error.
func TestResumeNode_PollUnknown_NoStateChangeNoEvent(t *testing.T) {
	for _, status := range []registry.PollStatus{"UNKNOWN", "PROVIDER_SPECIFIC"} {
		t.Run(string(status), func(t *testing.T) {
			assertPollChangesNothing(t, status)
		})
	}
}

// pollBarrierUoW holds every state-changing transaction at a barrier until the test
// releases it, so a poll result and a callback that both already read the Attempt as
// DISPATCHED reach the conditional update together. Read-only routing transactions pass
// straight through.
type pollBarrierUoW struct {
	store.UnitOfWork
	arrived chan struct{}
	release chan struct{}
}

func (u *pollBarrierUoW) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	u.arrived <- struct{}{}
	<-u.release
	return u.UnitOfWork.WithinTx(ctx, fn)
}

// serviceOver builds an ExecutionService over the harness database through uow, with the
// harness's registry, clock, IDs and Executor.
func (h *asyncHarness) serviceOver(uow store.UnitOfWork, side domain.SideEffectPolicy) *service.ExecutionService {
	h.t.Helper()
	nodes := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(),
		textoutput.Registration(),
		asyncRegistration(h.exec, side),
	} {
		if err := nodes.Register(reg); err != nil {
			h.t.Fatalf("register node type: %v", err)
		}
	}
	return service.NewExecutionService(service.Deps{
		UoW:      uow,
		Nodes:    nodes,
		Compiler: runtime.NewCompiler(nodes, h.clock),
		Clock:    h.clock,
		IDs:      h.ids,
		Callback: service.CallbackConfig{
			BaseURL:       asyncBaseURL,
			SigningSecret: []byte(asyncSigningSecret),
			PendingTTL:    time.Hour,
		},
	})
}

// TestResumeNode_PollRacesCallback_ExactlyOneWinner holds a poll result and a callback
// at a barrier after both have routed to the DISPATCHED Attempt and before either
// transaction starts, then releases them together. The conditional update, not the
// arrival order, elects the single winner: exactly one SUCCEEDED, one NODE_COMPLETED
// carrying the winner's source, and the loser reports a duplicate.
func TestResumeNode_PollRacesCallback_ExactlyOneWinner(t *testing.T) {
	h := newAsyncHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-race")
	beforeSeq := getRun(h.ctx, t, h.uow, run.ID).LastSeq

	barrier := &pollBarrierUoW{UnitOfWork: h.uow, arrived: make(chan struct{}), release: make(chan struct{})}
	racing := h.serviceOver(barrier, pollUnknownSide)

	var wg sync.WaitGroup
	var pollOutcome service.ResumeOutcome
	var callbackOutcome service.CallbackOutcome
	var pollErr, callbackErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		pollOutcome, pollErr = racing.ResumeNode(h.ctx, service.ResumeNode{
			ExternalTaskID: dispatch.externalTaskID,
			Polled:         &service.PolledResult{AttemptID: dispatch.attemptID, Result: pollSucceeded("polled")},
		})
	}()
	go func() {
		defer wg.Done()
		callbackOutcome, callbackErr = racing.HandleCallback(h.ctx, service.HandleCallback{
			Token: dispatch.token, ExternalTaskID: dispatch.externalTaskID, Payload: json.RawMessage(`{"text":"called back"}`),
		})
	}()
	<-barrier.arrived
	<-barrier.arrived
	close(barrier.release)
	wg.Wait()

	if pollErr != nil || callbackErr != nil {
		t.Fatalf("racing deliveries: poll err %v, callback err %v", pollErr, callbackErr)
	}
	if pollOutcome.Duplicate == callbackOutcome.Duplicate {
		t.Fatalf("racing deliveries: want exactly one duplicate, got poll %+v and callback %+v", pollOutcome, callbackOutcome)
	}

	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).Status; got != domain.NodeAttemptSucceeded {
		t.Fatalf("Attempt after the race: want SUCCEEDED, got %s", got)
	}
	if got := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun after the race: want SUCCEEDED, got %s", got)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	completed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCompleted)
	received := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCallbackReceived)
	if len(completed) != 1 {
		t.Fatalf("NODE_COMPLETED after the race: want exactly 1, got %v", execEventTypes(events))
	}
	source := decodeCompletionSource(t, completed[0])
	if !pollOutcome.Duplicate {
		if source != string(domain.CompletionProviderPoll) || len(received) != 0 {
			t.Fatalf("poll won: want PROVIDER_POLL and no NODE_CALLBACK_RECEIVED, got %q and %v", source, execEventTypes(events))
		}
		if completed[0].Seq != beforeSeq+1 {
			t.Fatalf("poll won: NODE_COMPLETED seq want %d, got %d", beforeSeq+1, completed[0].Seq)
		}
	} else {
		if source != string(domain.CompletionCallback) || len(received) != 1 {
			t.Fatalf("callback won: want CALLBACK and one NODE_CALLBACK_RECEIVED, got %q and %v", source, execEventTypes(events))
		}
	}
}

// TestResumeNode_PollStaleAttempt_NoEffect proves a poll result that arrives after the
// timeout already failed the Attempt is a duplicate: no state change, no Event, no seq.
func TestResumeNode_PollStaleAttempt_NoEffect(t *testing.T) {
	h := newAsyncHarness(t, pollUnknownSide, domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-stale")

	h.clock.Advance(2 * time.Minute)
	if err := h.svc.TimeoutAttempt(h.ctx, dispatch.attemptID); err != nil {
		t.Fatalf("timeout attempt: %v", err)
	}
	before := h.pollFacts(dispatch)
	if before.attemptStatus != domain.NodeAttemptFailed {
		t.Fatalf("precondition: Attempt after timeout want FAILED, got %s", before.attemptStatus)
	}

	for _, result := range []registry.PollResult{pollSucceeded("too late"), pollFailed()} {
		outcome, err := h.resumePolled(dispatch, result)
		if err != nil {
			t.Fatalf("stale poll %s: %v", result.Status, err)
		}
		if !outcome.Duplicate || outcome.Failed {
			t.Fatalf("stale poll %s outcome: want Duplicate only, got %+v", result.Status, outcome)
		}
		if after := h.pollFacts(dispatch); after != before {
			t.Fatalf("facts after stale poll %s: want unchanged %+v, got %+v", result.Status, before, after)
		}
	}
}

// TestResumeNode_PollBindingTargetsOtherAttempt_Rejected proves the Callback Binding is a
// poll result's only route: a result taken for a superseded Attempt, presented under the
// external task id the current Attempt is bound to, is refused and writes nothing.
func TestResumeNode_PollBindingTargetsOtherAttempt_Rejected(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	run := h.createRun()

	first := h.claimAsyncNode(run)
	h.exec.setDispatchErr(errors.New("provider connection reset while accepting the task"))
	if err := h.svc.Execute(h.ctx, first); err != nil {
		t.Fatalf("execute the failing dispatch: %v", err)
	}
	h.exec.setDispatchErr(nil)
	h.clock.Advance(time.Minute)
	second := h.dispatchAsyncNode(run, "provider-task-poll-current")
	before := h.pollFacts(second)

	for _, result := range []registry.PollResult{pollSucceeded("misrouted"), pollFailed(), {Status: registry.PollRunning}} {
		_, err := h.svc.ResumeNode(h.ctx, service.ResumeNode{
			ExternalTaskID: second.externalTaskID,
			Polled:         &service.PolledResult{AttemptID: first.AttemptID, Result: result},
		})
		if !errors.Is(err, service.ErrPollBindingMismatch) {
			t.Fatalf("poll %s for another attempt: want ErrPollBindingMismatch, got %v", result.Status, err)
		}
		if after := h.pollFacts(second); after != before {
			t.Fatalf("facts after a misrouted poll %s: want unchanged %+v, got %+v", result.Status, before, after)
		}
	}
	if got := getAttempt(h.ctx, t, h.uow, first.AttemptID).Status; got != domain.NodeAttemptFailed {
		t.Fatalf("superseded Attempt: want FAILED, got %s", got)
	}
}

// TestResumeNode_PollDoesNotTouchPendingCallbacks proves a poll result neither consumes a
// stored early callback for its external task nor stores one when no Binding exists.
func TestResumeNode_PollDoesNotTouchPendingCallbacks(t *testing.T) {
	h := newAsyncHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-pending")

	now := h.clock.Now()
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.PendingCallbacks().Record(ctx, domain.PendingCallback{
			ExternalTaskID:    dispatch.externalTaskID,
			Payload:           json.RawMessage(`{"text":"early"}`),
			PayloadHash:       asyncSHA256(`{"text":"early"}`),
			CallbackTokenHash: asyncSHA256(dispatch.token),
			ReceivedAt:        now,
			ExpiresAt:         now.Add(time.Hour),
		})
		return err
	}); err != nil {
		t.Fatalf("store pending callback: %v", err)
	}

	outcome, err := h.resumePolled(dispatch, pollSucceeded("polled"))
	if err != nil || outcome.Duplicate {
		t.Fatalf("poll completion: want a fresh completion, got %+v / %v", outcome, err)
	}
	if got := asyncPendingStatus(h.ctx, t, h.uow, dispatch.externalTaskID); got != "stored" {
		t.Fatalf("pending callback after a poll completion: want still stored and unconsumed, got %s", got)
	}

	unbound := "provider-task-poll-unbound"
	_, err = h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: unbound,
		Polled:         &service.PolledResult{AttemptID: dispatch.attemptID, Result: pollSucceeded("nowhere")},
	})
	if !errors.Is(err, service.ErrNoCallbackBinding) {
		t.Fatalf("poll for an unbound external task: want ErrNoCallbackBinding, got %v", err)
	}
	if got := asyncPendingStatus(h.ctx, t, h.uow, unbound); got != "absent" {
		t.Fatalf("pending callback for an unbound poll: want none, got %s", got)
	}
}

// TestResumeNode_PollForToolAttempt_NotSupported proves a poll result for an external
// task bound to an Agent Tool Attempt is refused explicitly: asynchronous Tools resume by
// callback only, and the refusal writes nothing and leaves the callback path intact.
func TestResumeNode_PollForToolAttempt_NotSupported(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, token := agentDispatchedAsync(t, "wf-agent-poll-unsupported", tool, nil)
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	before := agentSnapshot(t, h, run, outcome)
	callbacksBefore := tool.callbackCount()

	_, err := h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: agentAsyncExternalTaskID,
		Polled: &service.PolledResult{
			AttemptID: attempt.ID,
			Result:    registry.PollResult{Status: registry.PollSucceeded, ToolResult: &registry.ToolResult{Output: json.RawMessage(agentCallbackResult)}},
		},
	})
	if !errors.Is(err, service.ErrToolPollNotSupported) {
		t.Fatalf("poll for a tool attempt: want ErrToolPollNotSupported, got %v", err)
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Fatalf("state after a refused tool poll: want unchanged %+v, got %+v", before, after)
	}
	if tool.callbackCount() != callbacksBefore {
		t.Fatalf("OnCallback calls grew from %d to %d on a refused tool poll", callbacksBefore, tool.callbackCount())
	}

	delivered, err := agentDeliver(h.ctx, h, token, agentCallbackSucceeded)
	if err != nil || !delivered.Accepted || delivered.Duplicate {
		t.Fatalf("callback after a refused tool poll: want accepted, got %+v / %v", delivered, err)
	}
}
