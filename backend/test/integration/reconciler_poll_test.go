//go:build integration

// Reconciler proofs for Provider polling against real PostgreSQL. The Reconciler only
// discovers DISPATCHED Attempts whose persisted poll time is due and hands each to the
// Provider poll use case; the claim, the Provider query and the resume all happen inside
// that use case. These scenarios cover recovery after a restart that lost every
// in-process timer, the rescheduling of a non-final answer, the race with a callback, and
// an Attempt no poll can route to.
package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
)

func pollReconciler(h *asyncHarness, executor reconciler.Advancer) *reconciler.Reconciler {
	return reconciler.New(reconciler.Config{UoW: h.uow, Executor: executor, Clock: h.clock, BatchLimit: 100})
}

func attemptStatusesOf(ctx context.Context, t *testing.T, h *asyncHarness, nodeRunID string) []domain.NodeAttemptStatus {
	t.Helper()
	rows, err := h.pool.Query(ctx, `SELECT status FROM node_attempts WHERE node_run_id = $1 ORDER BY attempt_no`, nodeRunID)
	if err != nil {
		t.Fatalf("list attempts of %s: %v", nodeRunID, err)
	}
	defer rows.Close()
	var out []domain.NodeAttemptStatus
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan attempt status: %v", err)
		}
		out = append(out, domain.NodeAttemptStatus(status))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list attempts of %s: %v", nodeRunID, err)
	}
	return out
}

// TestReconciler_RestartAfterDispatch_PollCompletesAttempt dispatches a pollable Node
// whose Provider never delivers a callback, then restarts: a fresh service, Executor and
// Reconciler over the same database, with no in-process work carried over. Once the
// persisted poll time is due, the Reconciler's scan alone polls the Provider, the
// SUCCEEDED answer completes the Attempt through the resume path, and later passes drive
// the downstream Node until the Run is COMPLETED.
func TestReconciler_RestartAfterDispatch_PollCompletesAttempt(t *testing.T) {
	h, _ := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-reconciler-poll-restart")

	restartedProvider := newPollScriptExecutor(&asyncFakeExecutor{}, pollReturning(pollSucceeded("polled after restart")))
	restarted := h.pollService(h.uow, pollingRegistration(restartedProvider, pollUnknownSide), service.PollHooks{})
	rec := pollReconciler(h, restarted)

	early, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once before the poll is due: %v", err)
	}
	if early.DuePollsFound != 0 || restartedProvider.pollCalls() != 0 {
		t.Fatalf("pass before the poll is due: want nothing found and no Provider query, got %+v and %d queries", early, restartedProvider.pollCalls())
	}

	h.clock.Advance(pollAttemptInterval)
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report.Errors: want none, got %v", report.Errors)
	}
	if report.DuePollsFound != 1 || report.PollsClaimed != 1 || report.PollsResumed != 1 {
		t.Fatalf("report: want one due poll claimed and resumed, got %+v", report)
	}
	if restartedProvider.pollCalls() != 1 {
		t.Fatalf("Provider polls: want 1, got %d", restartedProvider.pollCalls())
	}

	for i := 0; i < 10 && getRun(h.ctx, t, h.uow, run.ID).Status == domain.RunRunning; i++ {
		drain, err := rec.RunOnce(h.ctx)
		if err != nil {
			t.Fatalf("drain reconciler run once: %v", err)
		}
		if len(drain.Errors) != 0 || drain.DuePollsFound != 0 {
			t.Fatalf("drain pass: want no errors and no further due poll, got %+v", drain)
		}
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("Run after the polled completion: want COMPLETED, got %s", got)
	}
	if got := attemptStatusesOf(h.ctx, t, h, dispatch.nodeRunID); len(got) != 1 || got[0] != domain.NodeAttemptSucceeded {
		t.Fatalf("Attempts of the polled NodeRun: want exactly one SUCCEEDED, got %v", got)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	completed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCompleted)
	if len(completed) != 1 || decodeCompletionSource(t, completed[0]) != string(domain.CompletionProviderPoll) {
		t.Fatalf("NODE_COMPLETED of the polled NodeRun: want one with PROVIDER_POLL, got %v", execEventTypes(events))
	}
	if got := asyncEventsOfType(events, domain.EventNodeCallbackReceived); len(got) != 0 {
		t.Fatalf("NODE_CALLBACK_RECEIVED: want none for a polled completion, got %v", execEventTypes(events))
	}
	if restartedProvider.pollCalls() != 1 {
		t.Fatalf("Provider polls after the Run completed: want still 1, got %d", restartedProvider.pollCalls())
	}
}

// TestReconciler_DuePollRunning_NoEventAndRescheduled proves a RUNNING answer found by the
// Reconciler writes no Event and no business state, consumes one poll and pushes the next
// poll time one interval past the claim, so a pass before that time claims nothing and
// does not query the Provider.
func TestReconciler_DuePollRunning_NoEventAndRescheduled(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-reconciler-poll-running")
	poller.pollFn = pollReturning(registry.PollResult{Status: registry.PollRunning})
	rec := pollReconciler(h, h.svc)

	h.clock.Advance(pollAttemptInterval)
	before := h.pollFacts(dispatch)
	claimedAt := h.clock.Now()

	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report.Errors: want none, got %v", report.Errors)
	}
	if report.DuePollsFound != 1 || report.PollsClaimed != 1 || report.PollsResumed != 0 {
		t.Fatalf("report: want one due poll claimed and not resumed, got %+v", report)
	}
	if after := h.pollFacts(dispatch); after != before {
		t.Fatalf("facts after a RUNNING poll: want unchanged %+v, got %+v", before, after)
	}
	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	wantNext := claimedAt.Add(pollAttemptInterval)
	if attempt.PollCount != 1 || !equalTimePtr(attempt.NextPollAt, &wantNext) {
		t.Fatalf("poll schedule after a RUNNING poll: want poll_count 1 and next_poll_at %v, got %d/%v", wantNext, attempt.PollCount, attempt.NextPollAt)
	}

	h.clock.Advance(pollAttemptInterval / 2)
	second, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("second reconciler run once: %v", err)
	}
	if second.DuePollsFound != 0 || second.PollsClaimed != 0 || len(second.Errors) != 0 {
		t.Fatalf("pass before the next poll time: want nothing found or claimed, got %+v", second)
	}
	if poller.pollCalls() != 1 {
		t.Fatalf("Provider polls: want only the first, got %d", poller.pollCalls())
	}
	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).PollCount; got != 1 {
		t.Fatalf("poll_count after a pass before the next poll time: want 1, got %d", got)
	}
}

// TestReconciler_DuePollRacesCallback_ExactlyOneWinner lets the Reconciler claim a due
// poll whose Provider answers SUCCEEDED, then holds that poll's resume transaction and a
// callback's state-changing transaction at one barrier and releases them together. The
// conditional update from DISPATCHED elects the single winner: one NODE_COMPLETED, and
// the loser is a duplicate that the Reconciler does not report as an error.
func TestReconciler_DuePollRacesCallback_ExactlyOneWinner(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-reconciler-poll-race")
	h.clock.Advance(pollAttemptInterval)

	arrived := make(chan struct{})
	release := make(chan struct{})
	pollUoW := &pollArmedBarrierUoW{UnitOfWork: h.uow, arrived: arrived, release: release}
	poller.pollFn = pollReturning(pollSucceeded("polled"))
	pollSvc := h.pollService(pollUoW, pollingRegistration(poller, pollUnknownSide), service.PollHooks{
		AfterClaim: func(context.Context, string) error {
			pollUoW.armed.Store(true)
			return nil
		},
	})
	rec := pollReconciler(h, pollSvc)
	callbackSvc := h.serviceOver(&pollBarrierUoW{UnitOfWork: h.uow, arrived: arrived, release: release}, pollUnknownSide)

	var wg sync.WaitGroup
	var report reconciler.Report
	var callbackOutcome service.CallbackOutcome
	var recErr, callbackErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		report, recErr = rec.RunOnce(h.ctx)
	}()
	go func() {
		defer wg.Done()
		callbackOutcome, callbackErr = callbackSvc.HandleCallback(h.ctx, service.HandleCallback{
			Token: dispatch.token, ExternalTaskID: dispatch.externalTaskID, Payload: []byte(`{"text":"called back"}`),
		})
	}()
	<-arrived
	<-arrived
	close(release)
	wg.Wait()

	if recErr != nil || callbackErr != nil {
		t.Fatalf("racing deliveries: reconciler err %v, callback err %v", recErr, callbackErr)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report.Errors: want none (a lost race is not an error), got %v", report.Errors)
	}
	pollWon := report.PollsResumed == 1
	if report.DuePollsFound != 1 || report.PollsResumed+report.PollsDuplicate != 1 || pollWon == !callbackOutcome.Duplicate {
		t.Fatalf("racing deliveries: want exactly one winner, got report %+v and callback %+v", report, callbackOutcome)
	}

	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptSucceeded || attempt.PollCount != 1 {
		t.Fatalf("Attempt after the race: want SUCCEEDED with one claimed poll, got %s/%d", attempt.Status, attempt.PollCount)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	completed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCompleted)
	if len(completed) != 1 {
		t.Fatalf("NODE_COMPLETED after the race: want exactly 1, got %v", execEventTypes(events))
	}
	received := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCallbackReceived)
	source := decodeCompletionSource(t, completed[0])
	if pollWon && (source != string(domain.CompletionProviderPoll) || len(received) != 0) {
		t.Fatalf("poll won: want PROVIDER_POLL and no NODE_CALLBACK_RECEIVED, got %q and %v", source, execEventTypes(events))
	}
	if !pollWon && (source != string(domain.CompletionCallback) || len(received) != 1) {
		t.Fatalf("callback won: want CALLBACK and one NODE_CALLBACK_RECEIVED, got %q and %v", source, execEventTypes(events))
	}
}

// TestReconciler_BindingMismatch_ClearsPollAndStopsRelisting repoints the current
// Attempt's Callback Binding at the superseded Attempt of a keyed retry. The Reconciler
// hands the due poll to the use case, which refuses it with the mismatch error, queries
// nothing and clears the poll schedule; the next pass no longer discovers the Attempt, so
// the refusal is reported once rather than on every scan and the deadline decides.
func TestReconciler_BindingMismatch_ClearsPollAndStopsRelisting(t *testing.T) {
	h, poller := newPollingHarness(t,
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
	second := h.dispatchAsyncNode(run, "provider-task-reconciler-poll-misbound")

	if _, err := h.pool.Exec(h.ctx,
		`UPDATE callback_bindings SET target_id = $1 WHERE external_task_id = $2`,
		first.AttemptID, second.externalTaskID); err != nil {
		t.Fatalf("repoint callback binding: %v", err)
	}
	h.clock.Advance(pollAttemptInterval)
	before := h.pollFacts(second)
	rec := pollReconciler(h, h.svc)

	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.DuePollsFound != 1 || report.PollsClaimed != 0 {
		t.Fatalf("report: want one due poll found and none claimed, got %+v", report)
	}
	if len(report.Errors) != 1 || !errors.Is(report.Errors[0], service.ErrPollBindingMismatch) {
		t.Fatalf("report.Errors: want only the binding mismatch, got %v", report.Errors)
	}
	attempt := getAttempt(h.ctx, t, h.uow, second.attemptID)
	if attempt.Status != domain.NodeAttemptDispatched || attempt.PollCount != 0 || attempt.NextPollAt != nil {
		t.Fatalf("Attempt after a refused poll: want DISPATCHED, poll_count 0, next_poll_at NULL, got %s/%d/%v",
			attempt.Status, attempt.PollCount, attempt.NextPollAt)
	}
	if after := h.pollFacts(second); after != before {
		t.Fatalf("facts after a refused poll: want unchanged %+v, got %+v", before, after)
	}

	h.clock.Advance(pollAttemptInterval)
	again, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("second reconciler run once: %v", err)
	}
	if again.DuePollsFound != 0 || len(again.Errors) != 0 {
		t.Fatalf("pass after the schedule was cleared: want nothing found and no error, got %+v", again)
	}
	if poller.pollCalls() != 0 {
		t.Fatalf("Provider polls for a refused poll: want 0, got %d", poller.pollCalls())
	}
}
