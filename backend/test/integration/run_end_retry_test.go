//go:build integration

// A NodeRun waiting for a scheduled retry when a sibling ends its Run. Nothing claims work
// of an ended Run, so that retry will never start: without an end of its own the NodeRun
// would stay RUNNING, keep its Provider slot, and come back from the recovery scan on every
// pass. The Run's end settles it in the same transaction. An Attempt that is actually in
// flight is left to the path that ends it.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

const runEndRetryFailure = "UPSTREAM_BUSY"

// retryScheduled leaves z_task RUNNING with a retry scheduled and no live Attempt, holding
// its slot, while b_wait waits on a Provider that is not limited.
func (h *orphanHarness) retryScheduled() (domain.Run, domain.NodeRun, domain.NodeRun, domain.NodeAttempt) {
	h.t.Helper()
	run, outcome := h.claim(h.siblings(&domain.ExecutionPolicy{MaxAttempts: 3}), capTaskType)
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		h.t.Fatalf("dispatch b_wait: %v", err)
	}
	outcome = h.claimIn(run.ID, capTaskType)
	if err := h.svc.FailNode(h.ctx, service.FailNode{
		AttemptID: outcome.AttemptID,
		Error:     domain.ExecutionError{Code: runEndRetryFailure, Message: "the Provider refused the task"},
		Source:    domain.FailureSyncExecution,
	}); err != nil {
		h.t.Fatalf("fail z_task: %v", err)
	}
	task := h.assertRetryPending(run.ID)
	return run, h.nodeRun(run.ID, "b_wait"), task, h.attempt(outcome.AttemptID)
}

func (h *orphanHarness) assertRetryPending(runID string) domain.NodeRun {
	h.t.Helper()
	task := h.nodeRun(runID, "z_task")
	if task.Status != domain.NodeRunRunning || task.NextAttemptAt == nil {
		h.t.Fatalf("z_task = %s next=%v, want RUNNING with a scheduled retry", task.Status, task.NextAttemptAt)
	}
	h.assertHeld(task)
	return task
}

func (h *orphanHarness) failSibling(waiting domain.NodeRun) {
	h.t.Helper()
	h.callback(waiting.ID, `{"status":"FAILED"}`)
}

// recoverable reads what the recovery scan would return for any point in time.
func (h *orphanHarness) recoverable() []domain.NodeRun {
	h.t.Helper()
	var rows []domain.NodeRun
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = tx.NodeRuns().ListReadyOrRetryable(ctx, h.clock.Now().Add(365*24*time.Hour), 100)
		return err
	}); err != nil {
		h.t.Fatalf("list recoverable node runs: %v", err)
	}
	return rows
}

func (h *orphanHarness) assertNotRecoverable(nr domain.NodeRun) {
	h.t.Helper()
	for _, row := range h.recoverable() {
		if row.ID == nr.ID {
			h.t.Fatalf("node run %s is still recovery work: status=%s next=%v", nr.ID, row.Status, row.NextAttemptAt)
		}
	}
}

func (h *orphanHarness) assertRetrySettled(run domain.Run, task domain.NodeRun, attempt domain.NodeAttempt) {
	h.t.Helper()
	got := h.nodeRun(run.ID, "z_task")
	if got.Status != domain.NodeRunFailed || got.NextAttemptAt != nil || got.CompletedAt == nil {
		h.t.Fatalf("z_task = %s next=%v completed=%v, want FAILED with no retry scheduled", got.Status, got.NextAttemptAt, got.CompletedAt)
	}
	if got.Error == nil || attempt.Error == nil || got.Error.Code != attempt.Error.Code {
		h.t.Fatalf("z_task error = %+v, want the failure of its last Attempt, %+v", got.Error, attempt.Error)
	}
	h.assertNotHeld(task)
	h.assertNotRecoverable(task)
	if n := len(h.attempts(task.ID)); n != 1 {
		h.t.Fatalf("attempts = %d, want no new Attempt", n)
	}

	events := h.events(run.ID)
	failed := asyncNodeEventsOfType(events, task.ID, domain.EventNodeFailed)
	if len(failed) != 1 {
		h.t.Fatalf("z_task NODE_FAILED events = %d, want 1", len(failed))
	}
	var payload struct {
		AttemptNo int                   `json:"attemptNo"`
		Error     domain.ExecutionError `json:"error"`
	}
	if err := json.Unmarshal(failed[0].Payload, &payload); err != nil {
		h.t.Fatalf("decode NODE_FAILED: %v", err)
	}
	if payload.AttemptNo != attempt.AttemptNo || payload.Error.Code != attempt.Error.Code {
		h.t.Fatalf("NODE_FAILED = %+v, want Attempt %d and its failure", payload, attempt.AttemptNo)
	}
	ended := asyncEventsOfType(events, domain.EventRunFailed)
	if len(ended) != 1 {
		h.t.Fatalf("RUN_FAILED events = %d, want 1", len(ended))
	}
	if failed[0].Seq <= ended[0].Seq {
		h.t.Fatalf("NODE_FAILED seq %d, want after RUN_FAILED seq %d", failed[0].Seq, ended[0].Seq)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			h.t.Fatalf("event seq %d follows %d, want a gapless order", events[i].Seq, events[i-1].Seq)
		}
	}
}

func TestRunEnd_RetryNotDueYet_SettledWithTheRun(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting, task, attempt := h.retryScheduled()

	h.failSibling(waiting)

	if got := h.run(run.ID); got.Status != domain.RunFailed || got.Error == nil || got.Error.Code == runEndRetryFailure {
		t.Fatalf("run = %s error=%+v, want FAILED by the sibling's failure", got.Status, got.Error)
	}
	h.assertRetrySettled(run, task, attempt)
}

func TestRunEnd_RetryAlreadyDue_SettledAndNeverClaimed(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting, task, attempt := h.retryScheduled()
	rec := h.restart()

	h.clock.Advance(orphanLongAfter)
	h.failSibling(waiting)
	ended := h.run(run.ID)
	report := h.pass(rec)
	h.clock.Advance(orphanLongAfter)
	h.pass(rec)

	if report.ReadyOrRetryableFound != 0 {
		t.Fatalf("report = %+v, want nothing of the ended Run to recover", report)
	}
	h.assertRetrySettled(run, task, attempt)
	if got := h.run(run.ID); got.Status != domain.RunFailed || got.Error.Code != ended.Error.Code || !got.CompletedAt.Equal(*ended.CompletedAt) {
		t.Fatalf("run = %+v, want status, error and completed_at as the failure set them", got)
	}
}

func TestRunEnd_OrphanRetryScheduledThenSiblingEndsRun_SlotFreed(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.siblings(&domain.ExecutionPolicy{MaxAttempts: 3}), capTaskType)
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		t.Fatalf("dispatch b_wait: %v", err)
	}
	outcome = h.claimIn(run.ID, capTaskType)
	rec := h.restart()

	h.pass(rec)
	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
	task := h.assertRetryPending(run.ID)

	h.failSibling(h.nodeRun(run.ID, "b_wait"))

	h.assertRetrySettled(run, task, h.attempt(outcome.AttemptID))
}

func TestRunEnd_WriteFails_LeavesSlotAndNodeRunForTheNextEnd(t *testing.T) {
	const collidingID = "evt_run_end_retry_collision"

	check := func(t *testing.T, h *orphanHarness, run domain.Run, waiting, task domain.NodeRun, attempt domain.NodeAttempt, eventsBefore int) {
		t.Helper()
		h.assertRetryPending(run.ID)
		if got := h.run(run.ID).Status; got != domain.RunRunning {
			t.Fatalf("run after rollback = %s, want RUNNING", got)
		}
		if got := h.nodeRun(run.ID, "b_wait").Status; got != domain.NodeRunWaitingCallback {
			t.Fatalf("b_wait after rollback = %s, want WAITING_CALLBACK", got)
		}
		if got := len(h.events(run.ID)); got != eventsBefore {
			t.Fatalf("events after rollback = %d, want still %d", got, eventsBefore)
		}
		h.failSibling(waiting)
		h.assertRetrySettled(run, task, attempt)
	}
	callback := func(h *orphanHarness, waiting domain.NodeRun) error {
		d, ok := h.exec.dispatchOf(waiting.ID)
		if !ok {
			h.t.Fatal("b_wait was never dispatched")
		}
		_, err := h.svc.HandleCallback(h.ctx, service.HandleCallback{
			Token: d.token, ExternalTaskID: d.externalTaskID, Payload: json.RawMessage(`{"status":"FAILED"}`),
		})
		return err
	}

	t.Run("the settled NodeRun's Event", func(t *testing.T) {
		h := newOrphanHarness(t, 1)
		run, waiting, task, attempt := h.retryScheduled()
		execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingID, h.clock.Now())
		eventsBefore := len(h.events(run.ID))

		// The failing sibling writes NODE_FAILED and RUN_FAILED first, so the collision
		// lands on the Event of the NodeRun the Run's end settles.
		h.ids.ForceEventIDAfter(2, collidingID)
		if err := callback(h, waiting); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("callback with a forced Event ID collision: err = %v, want domain.ErrConflict", err)
		}
		check(t, h, run, waiting, task, attempt, eventsBefore)
	})

	t.Run("the Run's own end", func(t *testing.T) {
		h := newOrphanHarness(t, 1)
		run, waiting, task, attempt := h.retryScheduled()
		eventsBefore := len(h.events(run.ID))
		if _, err := h.pool.Exec(h.ctx, `
			CREATE FUNCTION refuse_run_end() RETURNS trigger LANGUAGE plpgsql AS
			$$ BEGIN RAISE EXCEPTION 'run end refused'; END $$;
			CREATE TRIGGER refuse_run_end BEFORE UPDATE ON runs
			   FOR EACH ROW WHEN (NEW.status = 'FAILED') EXECUTE FUNCTION refuse_run_end()`); err != nil {
			t.Fatalf("install the failure: %v", err)
		}
		t.Cleanup(func() {
			_, _ = h.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS refuse_run_end ON runs; DROP FUNCTION IF EXISTS refuse_run_end()`)
		})

		if err := callback(h, waiting); err == nil {
			t.Fatal("callback with the Run's end refused: want an error")
		}
		if _, err := h.pool.Exec(h.ctx, `DROP TRIGGER refuse_run_end ON runs; DROP FUNCTION refuse_run_end()`); err != nil {
			t.Fatalf("remove the failure: %v", err)
		}
		check(t, h, run, waiting, task, attempt, eventsBefore)
	})
}

func TestRunEnd_FreedSlotAdmitsOneQueuedRunWithoutOverAdmission(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting, task, _ := h.retryScheduled()
	def := h.linearDefinition(capModelID, nil)
	first, firstTask := h.queueRun(def)
	second, secondTask := h.queueRun(def)
	h.assertQueued(firstTask, 1)
	h.assertQueued(secondTask, 2)
	rec := h.restart()

	h.failSibling(waiting)
	h.assertNotHeld(task)
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED", got)
	}
	h.pass(rec)
	h.pass(rec)

	admitted := h.nodeRun(first.ID, "task")
	if admitted.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("first queued task = %s, want admitted", admitted.Status)
	}
	h.assertHeld(admitted)
	if got := h.nodeRun(second.ID, "task").Status; got != domain.NodeRunReady {
		t.Fatalf("second queued task = %s, want still READY", got)
	}
	if n := h.heldCount(); n != 1 {
		t.Fatalf("held slots = %d, want the limit of 1", n)
	}
}

// A retry that started before the Run ended has a live Attempt, and that Attempt's own
// completion, timeout or orphan path ends it, exactly as for a first Attempt.
func TestRunEnd_RetryAlreadyStarted_LeftToItsAttempt(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting, task, _ := h.retryScheduled()
	h.clock.Advance(orphanLongAfter)
	outcome, err := h.svc.Advance(h.ctx, run.ID)
	if err != nil || !outcome.Claimed || outcome.NodeRunID != task.ID {
		t.Fatalf("advance = %+v err=%v, want the retry of z_task claimed", outcome, err)
	}

	h.failSibling(waiting)

	got := h.nodeRun(run.ID, "z_task")
	if got.Status != domain.NodeRunRunning {
		t.Fatalf("z_task = %s, want RUNNING under its live Attempt", got.Status)
	}
	h.assertHeld(task)
	if live := h.attempt(outcome.AttemptID); live.Status != domain.NodeAttemptStarted {
		t.Fatalf("live Attempt = %s, want STARTED", live.Status)
	}
	if n := len(asyncNodeEventsOfType(h.events(run.ID), task.ID, domain.EventNodeFailed)); n != 0 {
		t.Fatalf("z_task NODE_FAILED events = %d, want none yet", n)
	}

	if err := h.svc.FailNode(h.ctx, service.FailNode{
		AttemptID: outcome.AttemptID,
		Error:     domain.ExecutionError{Code: runEndRetryFailure, Message: "the Provider refused the task"},
		Source:    domain.FailureSyncExecution,
	}); err != nil {
		t.Fatalf("fail the live Attempt: %v", err)
	}
	if got := h.nodeRun(run.ID, "z_task").Status; got != domain.NodeRunFailed {
		t.Fatalf("z_task = %s, want FAILED", got)
	}
	h.assertNotHeld(task)
}

func TestRunEnd_RacingTheRetryClaim_OneWinner(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting, task, attempt := h.retryScheduled()
	d, ok := h.exec.dispatchOf(waiting.ID)
	if !ok {
		t.Fatal("b_wait was never dispatched")
	}
	h.clock.Advance(orphanLongAfter)

	release := h.holdRunLock(run.ID)
	claimed := make(chan service.AdvanceOutcome, 1)
	errs := make(chan error, 2)
	go func() {
		outcome, err := h.svc.Advance(h.ctx, run.ID)
		claimed <- outcome
		errs <- err
	}()
	go func() {
		_, err := h.svc.HandleCallback(h.ctx, service.HandleCallback{
			Token: d.token, ExternalTaskID: d.externalTaskID, Payload: json.RawMessage(`{"status":"FAILED"}`),
		})
		errs <- err
	}()
	h.waitForBlocked(2)
	release()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	outcome := <-claimed

	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED whichever racer went first", got)
	}
	got := h.nodeRun(run.ID, "z_task")
	if outcome.Claimed {
		if got.Status != domain.NodeRunRunning || got.NextAttemptAt != nil {
			t.Fatalf("z_task after a won claim = %s next=%v, want RUNNING with no retry scheduled", got.Status, got.NextAttemptAt)
		}
		if n := len(h.attempts(task.ID)); n != 2 {
			t.Fatalf("attempts after a won claim = %d, want 2", n)
		}
		h.assertHeld(task)
		return
	}
	h.assertRetrySettled(run, task, attempt)
}

// A row left behind by a Run that ended before the Run's end settled pending retries. Its
// retry never starts and no later end of that Run comes, so Advance, which the recovery
// scan runs for it, settles it under the Run lock as the Run's end would have: with its
// Event, its slot freed, and the Event order persisted.
func TestAdvance_EndedRunWithAPendingRetry_OnePassSettlesIt(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, _, task, attempt := h.retryScheduled()
	rec := h.restart()
	completedAt := h.clock.Now()
	if _, err := h.pool.Exec(h.ctx, `UPDATE runs SET status = 'FAILED', completed_at = $2 WHERE id = $1`, run.ID, completedAt); err != nil {
		t.Fatalf("end the Run without settling: %v", err)
	}

	h.clock.Advance(orphanLongAfter)
	h.pass(rec)

	got := h.nodeRun(run.ID, "z_task")
	if got.Status != domain.NodeRunFailed || got.NextAttemptAt != nil || got.Error == nil || got.Error.Code != attempt.Error.Code {
		t.Fatalf("z_task = %s next=%v error=%+v, want FAILED with its last Attempt's failure", got.Status, got.NextAttemptAt, got.Error)
	}
	h.assertNotHeld(task)
	h.assertNotRecoverable(task)
	if n := len(h.attempts(task.ID)); n != 1 {
		t.Fatalf("attempts = %d, want no new Attempt", n)
	}
	events := h.events(run.ID)
	if n := len(asyncNodeEventsOfType(events, task.ID, domain.EventNodeFailed)); n != 1 {
		t.Fatalf("z_task NODE_FAILED events = %d, want 1", n)
	}
	ended := h.run(run.ID)
	if ended.Status != domain.RunFailed || !ended.CompletedAt.Equal(completedAt) {
		t.Fatalf("run = %s completed %v, want FAILED and completed at %v", ended.Status, ended.CompletedAt, completedAt)
	}
	if last := events[len(events)-1].Seq; ended.LastSeq != last {
		t.Fatalf("run last_seq = %d, want the newest Event's %d", ended.LastSeq, last)
	}
}

// Advance on an ended Run with nothing to settle writes nothing, so a queue item left over
// for that Run moves no fact and no timestamp.
func TestAdvance_EndedRunWithNothingToSettle_WritesNothing(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting, _, _ := h.retryScheduled()
	h.failSibling(waiting)
	before := h.run(run.ID)
	seen := len(h.events(run.ID))

	h.clock.Advance(orphanLongAfter)
	outcome, err := h.svc.Advance(h.ctx, run.ID)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}

	if outcome.Claimed {
		t.Fatalf("outcome = %+v, want nothing claimed", outcome)
	}
	if got := h.run(run.ID); !got.UpdatedAt.Equal(before.UpdatedAt) || got.LastSeq != before.LastSeq {
		t.Fatalf("run updated %v seq %d, want the %v and %d it ended with", got.UpdatedAt, got.LastSeq, before.UpdatedAt, before.LastSeq)
	}
	if n := len(h.events(run.ID)); n != seen {
		t.Fatalf("events = %d, want %d", n, seen)
	}
}
