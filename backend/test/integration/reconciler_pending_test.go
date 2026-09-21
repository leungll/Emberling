//go:build integration

// Reconciler proofs for the async-Node recovery surfaces owned by this track:
// 06 §2.1 ("Reconciler 必须能够扫描过期的 STARTED 或 DISPATCHED Attempt"), the pending
// callback consumption scan (06 §2.1/§3, design decision C3), and Pending Callback
// retention (05 §1.7). These reuse asyncHarness from service_async_test.go rather than
// duplicating its definition, executor and dispatch helpers (CLAUDE.md "locate the
// existing implementation ... before introducing a new abstraction").
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/store"
)

// TestReconciler_DispatchedAttemptPastDeadline_TimesOutAndLateCallbackIsDuplicate proves
// the Reconciler drives a DISPATCHED Attempt past its deadline through the same
// TimeoutAttempt use case TestTimeoutAttempt_DispatchedAttemptPastDeadline_
// FailsAttemptAndNodeRun exercises directly, then proves a callback arriving after that
// commit is a duplicate that writes nothing (06 §2.3, 09 §3.2 "timeout 与 callback ...
// 竞争同一完成权，只有一方提交").
func TestReconciler_DispatchedAttemptPastDeadline_TimesOutAndLateCallbackIsDuplicate(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-reconciler-timeout")

	h.clock.Advance(2 * time.Minute)
	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100})
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.ExpiredAttemptsFound != 1 || report.AttemptsTimedOut != 1 {
		t.Fatalf("report: want 1 expired DISPATCHED attempt timed out, got %+v", report)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report: want no errors, got %v", report.Errors)
	}

	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptFailed {
		t.Fatalf("Attempt status after reconciler timeout: want FAILED, got %s", attempt.Status)
	}
	if attempt.Error == nil || attempt.Error.Code != "TIMEOUT" {
		t.Fatalf("Attempt error after reconciler timeout: want code TIMEOUT, got %+v", attempt.Error)
	}
	if got := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after reconciler timeout: want FAILED, got %s", got)
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Fatalf("Run status after reconciler timeout: want FAILED, got %s", got)
	}

	eventsAfterTimeout := listEvents(h.ctx, t, h.uow, run.ID)
	if got := len(asyncNodeEventsOfType(eventsAfterTimeout, dispatch.nodeRunID, domain.EventNodeFailed)); got != 1 {
		t.Fatalf("NODE_FAILED events after reconciler timeout: want 1, got %d (%v)", got, execEventTypes(eventsAfterTimeout))
	}
	if got := len(asyncEventsOfType(eventsAfterTimeout, domain.EventRunFailed)); got != 1 {
		t.Fatalf("RUN_FAILED events after reconciler timeout: want 1, got %d (%v)", got, execEventTypes(eventsAfterTimeout))
	}

	// A callback that arrives after the Reconciler already won the completion right must
	// be accepted (its token and Binding are still valid) but change nothing: the Attempt
	// is no longer DISPATCHED, so ResumeNode reports Duplicate and no Event seq is
	// consumed.
	late, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"too late"}`)
	if err != nil {
		t.Fatalf("late callback: %v", err)
	}
	if !late.Accepted || !late.Duplicate {
		t.Fatalf("late callback outcome: want Accepted and Duplicate, got %+v", late)
	}
	eventsAfterLateCallback := listEvents(h.ctx, t, h.uow, run.ID)
	if len(eventsAfterLateCallback) != len(eventsAfterTimeout) {
		t.Fatalf("events after a late duplicate callback: want unchanged at %d, got %d (%v)",
			len(eventsAfterTimeout), len(eventsAfterLateCallback), execEventTypes(eventsAfterLateCallback))
	}
}

// commitDispatchWithoutConsumingEarlyCallback commits exactly the state
// dispatchNode's own transaction commits -- the Attempt DISPATCHED, the NodeRun
// WAITING_CALLBACK, and the Callback Binding -- through the same repositories
// dispatchNode uses, but without the in-process consumeEarlyCallback step dispatchNode
// runs immediately afterward (execution.go's dispatchNode calls it synchronously right
// after this transaction commits). It stands in for "the process crashed between that
// commit and the post-commit check": the only other way a Pending Callback recorded
// before this commit could remain unconsumed is a real, unrepeatable timing race between
// two goroutines, which is not something a deterministic test can force without an
// injected hook (none exists here; CLAUDE.md forbids inventing one to solve a test
// convenience). This mirrors the store_callbacks_test.go seedAttempt/createBinding fixture
// pattern already used to seed a store_test's PendingCallbacks/CallbackBindings and
// NodeAttempts state directly, applied to a real asyncHarness Attempt (with its real,
// already-issued callback token) instead of a synthetic fixture row, so the Pending
// Callback recorded against that same token remains a legitimate delivery -- it is not a
// forged or mismatched credential, only one whose consumption never ran.
func commitDispatchWithoutConsumingEarlyCallback(h *asyncHarness, attemptID, nodeRunID, externalTaskID string) {
	h.t.Helper()
	now := h.clock.Now()
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.NodeAttempts().MarkDispatched(ctx, attemptID, now); err != nil {
			return err
		}
		if err := tx.NodeRuns().MarkWaiting(ctx, nodeRunID, now); err != nil {
			return err
		}
		return tx.CallbackBindings().Create(ctx, domain.CallbackBinding{
			ID:             "cb_" + externalTaskID,
			ProviderID:     asyncProviderID,
			ExternalTaskID: externalTaskID,
			TargetType:     domain.CallbackTargetNodeAttempt,
			TargetID:       attemptID,
			CreatedAt:      now,
		})
	}); err != nil {
		h.t.Fatalf("commit dispatch without consuming early callback: %v", err)
	}
}

// TestReconciler_EarlyCallbackRecordedBeforeBinding_ConsumedOnceAfterRestart proves the
// pending callback consumption scan (design decision C3): a Pending Callback recorded
// before its Binding existed, whose in-process post-commit consumption never ran, is
// rediscovered and consumed by a brand-new Reconciler + service instance built over the
// same database (09 §3.2 "WAITING_CALLBACK 期间重启"), exactly once.
func TestReconciler_EarlyCallbackRecordedBeforeBinding_ConsumedOnceAfterRestart(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run := h.createRun()
	const externalTaskID = "provider-task-early-reconciler"
	h.exec.setExternalTaskID(externalTaskID)
	outcome := h.claimAsyncNode(run)
	token := outcome.Input.Callback.Token

	// The Provider's callback arrives before the dispatch transaction commits.
	early, err := h.handleCallback(token, externalTaskID, `{"text":"resumed-by-reconciler"}`)
	if err != nil {
		t.Fatalf("early callback: %v", err)
	}
	if !early.Pending {
		t.Fatalf("early callback outcome: want Pending, got %+v", early)
	}

	// The dispatch itself commits (Attempt DISPATCHED, NodeRun WAITING_CALLBACK, Binding
	// created) but the post-commit consumption that would normally follow never runs.
	commitDispatchWithoutConsumingEarlyCallback(h, outcome.AttemptID, outcome.NodeRunID, externalTaskID)

	if got := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("NodeRun status before reconciliation: want WAITING_CALLBACK, got %s", got)
	}
	if pending := getPending(h.ctx, t, h.uow, externalTaskID); pending.ConsumedAt != nil {
		t.Fatalf("pending consumedAt before reconciliation: want nil, got %v", pending.ConsumedAt)
	}

	// Simulated restart: a brand-new ExecutionService (fresh registry, fresh plan cache,
	// fresh Executor instance) driven by a brand-new Reconciler, both built over the same
	// database (invariant #1).
	restarted := h.newService(domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}, &asyncFakeExecutor{externalTaskID: externalTaskID})
	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: restarted, Clock: h.clock, BatchLimit: 100})

	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.ConsumablePendingFound != 1 {
		t.Fatalf("report.ConsumablePendingFound: want 1, got %d (%+v)", report.ConsumablePendingFound, report)
	}
	if report.PendingCallbacksResumed != 1 {
		t.Fatalf("report.PendingCallbacksResumed: want 1, got %d (%+v)", report.PendingCallbacksResumed, report)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report.Errors: want none, got %v", report.Errors)
	}

	if got := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun status after reconciler consumed the early callback: want SUCCEEDED, got %s", got)
	}
	pending := getPending(h.ctx, t, h.uow, externalTaskID)
	if pending.ConsumedAt == nil {
		t.Fatal("pending consumedAt after reconciliation: want a timestamp, got nil")
	}
	eventsAfterFirstRun := listEvents(h.ctx, t, h.uow, run.ID)
	if got := len(asyncNodeEventsOfType(eventsAfterFirstRun, outcome.NodeRunID, domain.EventNodeCompleted)); got != 1 {
		t.Fatalf("NODE_COMPLETED events after reconciler resume: want 1, got %d (%v)", got, execEventTypes(eventsAfterFirstRun))
	}

	// The definition still has a downstream text_output Node the reconciled async Node
	// feeds; RunOnce's own ReadyOrRetryable scan runs before its pending-callback scan
	// (reconciler.go RunOnce), so that Node was not yet READY when this pass looked for
	// ready work. Subsequent passes drive it to completion exactly like a work.Pool would,
	// with no further pending callback left to find, until the Run is COMPLETED.
	for i := 0; i < 10 && getRun(h.ctx, t, h.uow, run.ID).Status == domain.RunRunning; i++ {
		drain, err := rec.RunOnce(h.ctx)
		if err != nil {
			t.Fatalf("drain reconciler run once: %v", err)
		}
		if drain.ConsumablePendingFound != 0 || drain.PendingCallbacksResumed != 0 {
			t.Fatalf("drain RunOnce report: want nothing left to consume, got %+v", drain)
		}
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("Run status after draining downstream of the reconciled Node: want COMPLETED, got %s", got)
	}
	eventsAfterDrain := listEvents(h.ctx, t, h.uow, run.ID)
	maxSeqAfterDrain := maxEventSeq(eventsAfterDrain)

	// Once fully drained, a further pass is a true no-op: the pending callback stays
	// consumed once (invariant #5's idempotency), and there is no more ready or expired
	// work for anything else in this Run.
	final, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("final reconciler run once: %v", err)
	}
	if final.ConsumablePendingFound != 0 || final.PendingCallbacksResumed != 0 || final.RunsAdvanced != 0 || final.AttemptsTimedOut != 0 {
		t.Fatalf("final RunOnce report: want nothing left to do, got %+v", final)
	}
	eventsAfterFinal := listEvents(h.ctx, t, h.uow, run.ID)
	if len(eventsAfterFinal) != len(eventsAfterDrain) {
		t.Fatalf("events after a final, no-op RunOnce: want unchanged at %d, got %d (%v)",
			len(eventsAfterDrain), len(eventsAfterFinal), execEventTypes(eventsAfterFinal))
	}
	if got := maxEventSeq(eventsAfterFinal); got != maxSeqAfterDrain {
		t.Fatalf("max event seq after a final, no-op RunOnce: want unchanged at %d, got %d", maxSeqAfterDrain, got)
	}
}

// TestReconciler_ExpiredPendingCallback_DeletedNotConsumed proves the retention scan (05
// §1.7): an early callback whose dispatch never arrived to bind it is never eligible for
// resume (06 §4 "无法匹配的 Pending Callback ... 只进入运维审计并按保留策略清理"), and once
// its own TTL has passed the Reconciler deletes the row outright rather than resuming
// anything with it.
func TestReconciler_ExpiredPendingCallback_DeletedNotConsumed(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	const externalTaskID = "provider-task-orphaned-early-callback"

	now := h.clock.Now()
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.PendingCallbacks().Record(ctx, domain.PendingCallback{
			ExternalTaskID:    externalTaskID,
			Payload:           json.RawMessage(`{"text":"orphan"}`),
			PayloadHash:       "sha256:orphan",
			CallbackTokenHash: "sha256:orphan-token",
			ReceivedAt:        now,
			ExpiresAt:         now.Add(time.Minute),
		})
		return err
	}); err != nil {
		t.Fatalf("record orphan pending callback: %v", err)
	}

	if pending := getPending(h.ctx, t, h.uow, externalTaskID); pending.ExternalTaskID != externalTaskID {
		t.Fatalf("pending callback before expiry: want it stored, got %+v", pending)
	}

	h.clock.Advance(2 * time.Minute)
	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100})
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.ConsumablePendingFound != 0 || report.PendingCallbacksResumed != 0 {
		t.Fatalf("report: an unbound pending callback must never be resumed, got %+v", report)
	}
	if report.ExpiredPendingDeleted < 1 {
		t.Fatalf("report.ExpiredPendingDeleted: want at least 1, got %d", report.ExpiredPendingDeleted)
	}

	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.PendingCallbacks().GetByExternalTaskID(ctx, externalTaskID)
		if err == nil {
			t.Fatal("pending callback after retention: want it deleted, got a row")
		}
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("pending callback lookup after retention: want ErrNotFound, got %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("check pending callback deleted: %v", err)
	}
}

// maxEventSeq returns the highest Seq among events, or 0 for an empty slice.
func maxEventSeq(events []domain.Event) int64 {
	var max int64
	for _, ev := range events {
		if ev.Seq > max {
			max = ev.Seq
		}
	}
	return max
}
