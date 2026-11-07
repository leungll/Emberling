//go:build integration

// The maximum wait of a dispatched Node Attempt. A task the Provider accepted may never
// answer: its callback can be lost, its polls can run out or only ever come back unknown.
// Dispatch therefore writes a bound onto an Attempt with no deadline of its own, and the
// ordinary timeout scan ends the Attempt there, frees its slot and resends nothing. The
// bound is a fact on the Attempt, written once; every guarantee here is a transaction, a
// race or a restart, so all of it runs against PostgreSQL.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/migrations"
	"github.com/leungll/Emberling/backend/test/testdb"
)

const (
	maxWaitKeyedType     = "max_wait_keyed"
	maxWaitUnknownType   = "max_wait_unknown"
	maxWaitPolledType    = "max_wait_polled"
	maxWaitForgottenType = "max_wait_forgotten"

	maxWaitPollInterval = time.Second
	maxWaitMaxPolls     = 3

	maxWaitTimeoutCode = "TIMEOUT"
)

// maxWaitPolledExecutor queries the Mock Provider through the real Adapter. A `lost` task
// stays RUNNING there for good. With forgetTask set the query names a task the Provider
// does not know, which is what a Provider that restarted and lost the task answers.
type maxWaitPolledExecutor struct {
	orphanProviderExecutor
	forgetTask bool
	lastAnswer atomic.Value
}

func (e *maxWaitPolledExecutor) Poll(ctx context.Context, state registry.NodeAsyncState) (registry.PollResult, error) {
	id := state.ExternalTask.ExternalTaskID
	if e.forgetTask {
		id += "-forgotten"
	}
	status, _, err := e.adapter.Poll(ctx, id)
	if err != nil {
		return registry.PollResult{}, err
	}
	e.lastAnswer.Store(status)
	return registry.PollResult{Status: status}, nil
}

// maxWaitRegistration is a Provider task governed by the slot limit, dispatched through the
// Mock Provider so what reached the Provider is read from its own record.
func maxWaitRegistration(nodeType string, side domain.SideEffectPolicy, executor registry.NodeExecutor, poll *domain.PollPolicy) registry.NodeRegistration {
	reg := orphanProviderRegistration(nodeType, side, executor)
	reg.Metadata.ConfigSchema = json.RawMessage(`{"type":"object","properties":{"modelId":{"type":"string"}},
		"required":["modelId"],"additionalProperties":false}`)
	reg.Metadata.DispatchModelPath = "modelId"
	reg.Metadata.Poll = poll
	return reg
}

type maxWaitHarness struct {
	*orphanHarness
	running   *maxWaitPolledExecutor
	forgotten *maxWaitPolledExecutor
}

func newMaxWaitHarness(t *testing.T) *maxWaitHarness {
	t.Helper()
	h := &maxWaitHarness{orphanHarness: newOrphanHarness(t, 1)}
	base := orphanProviderExecutor{adapter: h.adapter}
	h.running = &maxWaitPolledExecutor{orphanProviderExecutor: base}
	h.forgotten = &maxWaitPolledExecutor{orphanProviderExecutor: base, forgetTask: true}
	keyed := domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}
	unknown := domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}
	poll := &domain.PollPolicy{IntervalMs: maxWaitPollInterval.Milliseconds(), MaxPolls: maxWaitMaxPolls}
	h.extraNodes = append(h.extraNodes,
		maxWaitRegistration(maxWaitKeyedType, keyed, &orphanProviderExecutor{adapter: h.adapter}, nil),
		maxWaitRegistration(maxWaitUnknownType, unknown, &orphanProviderExecutor{adapter: h.adapter}, nil),
		maxWaitRegistration(maxWaitPolledType, keyed, h.running, poll),
		maxWaitRegistration(maxWaitForgottenType, keyed, h.forgotten, poll),
	)
	h.svc = h.newService()
	return h
}

// waitingTask creates a Run of nodeType and drives it until the Provider holds its task
// and the Attempt waits for an answer that will not come. The policy allows retries, so
// that nothing but the timeout rule stands between these Attempts and a resend.
func (h *maxWaitHarness) waitingTask(nodeType string) (domain.Run, domain.NodeRun, domain.NodeAttempt) {
	h.t.Helper()
	run := h.createRun(h.chain(nodeType, fmt.Sprintf(`{"modelId":%q}`, capModelID), &domain.ExecutionPolicy{MaxAttempts: 3}))
	h.drive(run.ID)
	nr := h.nodeRun(run.ID, "task")
	if nr.Status != domain.NodeRunWaitingCallback {
		h.t.Fatalf("task = %s, want WAITING_CALLBACK", nr.Status)
	}
	h.assertHeld(nr)
	if n := len(h.providerRecord()); n != 1 {
		h.t.Fatalf("provider record = %d, want the one dispatch", n)
	}
	return run, nr, h.attempts(nr.ID)[0]
}

// exhaustPolls spends the poll budget, leaving a dispatched Attempt nothing to query with.
func (h *maxWaitHarness) exhaustPolls(rec *reconciler.Reconciler, attempt domain.NodeAttempt) {
	h.t.Helper()
	for i := 0; i < maxWaitMaxPolls; i++ {
		h.clock.Advance(maxWaitPollInterval)
		h.pass(rec)
	}
	got := h.attempt(attempt.ID)
	if got.Status != domain.NodeAttemptDispatched || got.PollCount != maxWaitMaxPolls || got.NextPollAt != nil {
		h.t.Fatalf("attempt = %s polls=%d next=%v, want DISPATCHED with its polls spent", got.Status, got.PollCount, got.NextPollAt)
	}
	if attempt.DeadlineAt == nil || got.DeadlineAt == nil || !got.DeadlineAt.Equal(*attempt.DeadlineAt) {
		h.t.Fatalf("deadline after polls = %v, want it unmoved at %v", got.DeadlineAt, attempt.DeadlineAt)
	}
}

// assertEndsAtTheBound proves the Attempt waits up to its deadline, ends exactly there
// through the timeout, frees its slot, and that the Provider heard nothing more.
func (h *maxWaitHarness) assertEndsAtTheBound(rec *reconciler.Reconciler, run domain.Run, nr domain.NodeRun, attempt domain.NodeAttempt) {
	h.t.Helper()
	if attempt.DeadlineAt == nil {
		h.t.Fatal("dispatched Attempt has no deadline, so nothing bounds its wait")
	}
	h.clock.Advance(attempt.DeadlineAt.Sub(h.clock.Now()) - time.Second)
	h.pass(rec)
	if got := h.nodeRun(run.ID, "task").Status; got != domain.NodeRunWaitingCallback {
		h.t.Fatalf("task just before the bound = %s, want WAITING_CALLBACK", got)
	}
	h.assertHeld(nr)

	h.clock.Advance(time.Second)
	report := h.pass(rec)
	if report.AttemptsTimedOut != 1 {
		h.t.Fatalf("report = %+v, want the timeout scan to end the Attempt", report)
	}
	h.assertFailed(attempt.ID, maxWaitTimeoutCode, true)
	if got := h.nodeRun(run.ID, "task").Status; got != domain.NodeRunFailed {
		h.t.Fatalf("task at the bound = %s, want FAILED", got)
	}
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		h.t.Fatalf("run at the bound = %s, want FAILED", got)
	}
	h.assertNotHeld(nr)

	h.clock.Advance(orphanLongAfter)
	h.pass(rec)
	if n := len(h.attempts(nr.ID)); n != 1 {
		h.t.Fatalf("attempts = %d, want no new Attempt after an uncertain wait", n)
	}
	if lines := h.providerRecord(); len(lines) != 1 {
		h.t.Fatalf("provider record = %+v, want the original dispatch alone", lines)
	}
}

// ---------------------------------------------------------------------------
// Writing the bound
// ---------------------------------------------------------------------------

func TestNodeMaxWait_BoundWrittenWithTheDispatch(t *testing.T) {
	h := newOrphanHarness(t, 1)
	_, outcome := h.claim(h.linearDefinition(capModelID, nil), capTaskType)
	if got := h.attempt(outcome.AttemptID); got.DeadlineAt != nil {
		t.Fatalf("STARTED deadline = %v, want none: the orphan path owns this state", got.DeadlineAt)
	}

	h.clock.Advance(time.Minute)
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := h.attempt(outcome.AttemptID)
	if got.Status != domain.NodeAttemptDispatched || got.DispatchedAt == nil || got.DeadlineAt == nil {
		t.Fatalf("attempt = %s dispatched=%v deadline=%v, want DISPATCHED with a deadline", got.Status, got.DispatchedAt, got.DeadlineAt)
	}
	if want := got.DispatchedAt.Add(orphanMaxWait); !got.DeadlineAt.Equal(want) {
		t.Fatalf("deadline = %v, want dispatched_at plus the maximum wait, %v", got.DeadlineAt, want)
	}
}

func TestNodeMaxWait_DispatchRollsBack_NoBoundAndTheOrphanPathStillApplies(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.linearDefinition(capModelID, nil), capTaskType)

	const collidingID = "evt_node_max_wait_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingID, h.clock.Now())
	h.ids.ForceNextEventID(collidingID)
	if err := h.svc.Execute(h.ctx, outcome); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("execute with a forced Event ID collision: err = %v, want domain.ErrConflict", err)
	}

	got := h.attempt(outcome.AttemptID)
	if got.Status != domain.NodeAttemptStarted || got.DeadlineAt != nil || got.DispatchedAt != nil {
		t.Fatalf("attempt after rollback = %s deadline=%v, want STARTED with no deadline", got.Status, got.DeadlineAt)
	}
	h.pass(h.restart())
	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
}

func TestNodeMaxWait_AttemptWithItsOwnDeadline_KeepsIt(t *testing.T) {
	h := newOrphanHarness(t, 1)
	timeout := 60 * time.Second
	policy := &domain.ExecutionPolicy{TimeoutMs: timeout.Milliseconds()}
	_, outcome := h.claim(h.linearDefinition(capModelID, policy), capTaskType)
	claimed := h.attempt(outcome.AttemptID)
	if claimed.DeadlineAt == nil {
		t.Fatal("claimed Attempt has no deadline, want the configured timeout")
	}

	h.clock.Advance(time.Second)
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := h.attempt(outcome.AttemptID)
	if got.Status != domain.NodeAttemptDispatched || !got.DeadlineAt.Equal(*claimed.DeadlineAt) {
		t.Fatalf("attempt = %s deadline=%v, want DISPATCHED keeping %v", got.Status, got.DeadlineAt, claimed.DeadlineAt)
	}
}

func TestNodeMaxWait_ZeroValueUsesTheDefault(t *testing.T) {
	h := newCapHarness(t, 1)
	_, waiting := h.holdSlot(h.linearDefinition(capModelID, nil))

	got := h.attempts(waiting.ID)[0]
	if got.DeadlineAt == nil || !got.DeadlineAt.Equal(got.DispatchedAt.Add(24*time.Hour)) {
		t.Fatalf("deadline = %v, want 24 hours after dispatch at %v", got.DeadlineAt, got.DispatchedAt)
	}
}

// The bound is what the dispatch wrote, not what the process now configures.
func TestNodeMaxWait_RestartWithAnotherValue_LeavesTheDeadline(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting := h.holdSlot(h.linearDefinition(capModelID, nil))
	before := h.attempts(waiting.ID)[0]
	if before.DeadlineAt == nil {
		t.Fatal("dispatched Attempt has no deadline")
	}

	h.maxWait = time.Minute
	rec := h.restart()
	h.clock.Advance(time.Hour)
	h.pass(rec)

	got := h.attempt(before.ID)
	if got.Status != domain.NodeAttemptDispatched || !got.DeadlineAt.Equal(*before.DeadlineAt) {
		t.Fatalf("attempt = %s deadline=%v, want DISPATCHED keeping %v", got.Status, got.DeadlineAt, before.DeadlineAt)
	}
	if got := h.nodeRun(run.ID, "task").Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("task = %s, want WAITING_CALLBACK", got)
	}
}

// ---------------------------------------------------------------------------
// Waits that never get an answer
// ---------------------------------------------------------------------------

func TestNodeMaxWait_DispatchedThenCrashed_EndsAtTheBoundWithoutResending(t *testing.T) {
	for _, nodeType := range []string{maxWaitKeyedType, maxWaitUnknownType} {
		t.Run(nodeType, func(t *testing.T) {
			h := newMaxWaitHarness(t)
			run, nr, attempt := h.waitingTask(nodeType)
			h.assertEndsAtTheBound(h.restart(), run, nr, attempt)
		})
	}
}

func TestNodeMaxWait_PollsExhausted_EndsAtTheBoundWithoutResending(t *testing.T) {
	h := newMaxWaitHarness(t)
	run, nr, attempt := h.waitingTask(maxWaitPolledType)
	rec := h.restart()

	h.exhaustPolls(rec, attempt)
	if got := h.running.lastAnswer.Load(); got != registry.PollRunning {
		t.Fatalf("provider answered %v, want RUNNING", got)
	}

	h.assertEndsAtTheBound(rec, run, nr, attempt)
}

func TestNodeMaxWait_ProviderForgotTheTask_EndsAtTheBoundWithoutResending(t *testing.T) {
	h := newMaxWaitHarness(t)
	run, nr, attempt := h.waitingTask(maxWaitForgottenType)
	rec := h.restart()

	h.exhaustPolls(rec, attempt)
	if got := h.forgotten.lastAnswer.Load(); got != registry.PollStatusUnknown {
		t.Fatalf("provider answer = %v, want UNKNOWN", got)
	}

	h.assertEndsAtTheBound(rec, run, nr, attempt)
}

func TestNodeMaxWait_FreedSlotAdmitsTheQueuedRunWithoutOverAdmission(t *testing.T) {
	h := newOrphanHarness(t, 1)
	def := h.linearDefinition(capModelID, nil)
	_, waiting := h.holdSlot(def)
	queued, queuedTask := h.queueRun(def)
	h.assertQueued(queuedTask, 1)

	rec := h.restart()
	h.clock.Advance(orphanMaxWait)
	h.pass(rec)
	h.pass(rec)

	h.assertNotHeld(waiting)
	admitted := h.nodeRun(queued.ID, "task")
	if admitted.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("queued task = %s, want admitted and dispatched", admitted.Status)
	}
	h.assertHeld(admitted)
	if n := h.heldCount(); n != 1 {
		t.Fatalf("held slots = %d, want the limit of 1", n)
	}
}

// ---------------------------------------------------------------------------
// Callback against the bound
// ---------------------------------------------------------------------------

func TestNodeMaxWait_CallbackBeforeTheBound_Wins(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting := h.holdSlot(h.linearDefinition(capModelID, nil))
	rec := h.restart()

	h.clock.Advance(orphanMaxWait - time.Second)
	h.callback(waiting.ID, `{"status":"SUCCEEDED","text":"just in time"}`)
	h.clock.Advance(orphanLongAfter)
	report := h.pass(rec)
	h.pass(rec)

	if report.AttemptsTimedOut != 0 {
		t.Fatalf("report = %+v, want no timeout of a resolved Attempt", report)
	}
	if got := h.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run = %s, want COMPLETED", got)
	}
	h.assertNotHeld(waiting)
}

// The scan is what ends the Attempt, not the clock: until it commits, the Attempt is still
// waiting and a callback has the same right to it as ever.
func TestNodeMaxWait_CallbackAfterTheBoundBeforeTheScan_Wins(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting := h.holdSlot(h.linearDefinition(capModelID, nil))
	rec := h.restart()

	h.clock.Advance(orphanMaxWait + time.Second)
	h.callback(waiting.ID, `{"status":"SUCCEEDED","text":"late but first"}`)
	report := h.pass(rec)
	h.pass(rec)

	if report.AttemptsTimedOut != 0 {
		t.Fatalf("report = %+v, want no timeout", report)
	}
	if got := h.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run = %s, want COMPLETED", got)
	}
}

func TestNodeMaxWait_CallbackAfterTheTimeout_ChangesNothing(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting := h.holdSlot(h.linearDefinition(capModelID, nil))
	attemptID := h.attempts(waiting.ID)[0].ID
	rec := h.restart()

	h.clock.Advance(orphanMaxWait)
	h.pass(rec)
	h.assertFailed(attemptID, maxWaitTimeoutCode, true)
	eventsBefore := len(h.events(run.ID))

	d, ok := h.exec.dispatchOf(waiting.ID)
	if !ok {
		t.Fatal("task was never dispatched")
	}
	if _, err := h.svc.HandleCallback(h.ctx, service.HandleCallback{
		Token: d.token, ExternalTaskID: d.externalTaskID, Payload: json.RawMessage(`{"status":"SUCCEEDED","text":"too late"}`),
	}); err != nil {
		t.Fatalf("late callback: %v", err)
	}

	h.assertFailed(attemptID, maxWaitTimeoutCode, true)
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED", got)
	}
	if got := len(h.events(run.ID)); got != eventsBefore {
		t.Fatalf("events = %d, want still %d", got, eventsBefore)
	}
	h.assertNotHeld(waiting)
}

func TestNodeMaxWait_TimeoutRacingCallback_OneWinner(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting := h.holdSlot(h.linearDefinition(capModelID, nil))
	attemptID := h.attempts(waiting.ID)[0].ID
	d, ok := h.exec.dispatchOf(waiting.ID)
	if !ok {
		t.Fatal("task was never dispatched")
	}
	rec := h.restart()
	h.clock.Advance(orphanMaxWait)

	release := h.holdRunLock(run.ID)
	errs := make(chan error, 2)
	go func() { errs <- h.svc.TimeoutAttempt(h.ctx, attemptID) }()
	go func() {
		_, err := h.svc.HandleCallback(h.ctx, service.HandleCallback{
			Token: d.token, ExternalTaskID: d.externalTaskID, Payload: json.RawMessage(`{"status":"SUCCEEDED","text":"raced"}`),
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
	h.pass(rec)

	nr := h.nodeRun(run.ID, "task")
	attempt := h.attempt(attemptID)
	switch attempt.Status {
	case domain.NodeAttemptSucceeded:
		if nr.Status != domain.NodeRunSucceeded {
			t.Fatalf("attempt SUCCEEDED but node run = %s", nr.Status)
		}
	case domain.NodeAttemptFailed:
		h.assertFailed(attemptID, maxWaitTimeoutCode, true)
		if nr.Status != domain.NodeRunFailed {
			t.Fatalf("attempt FAILED but node run = %s", nr.Status)
		}
	default:
		t.Fatalf("attempt = %s, want a single terminal outcome", attempt.Status)
	}
	h.assertNotHeld(nr)
	if n := h.eventCount(run.ID, domain.EventNodeFailed) + len(asyncNodeEventsOfType(h.events(run.ID), nr.ID, domain.EventNodeCompleted)); n != 1 {
		t.Fatalf("terminal node events = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Rows from before the bound
// ---------------------------------------------------------------------------

func TestMigrations_NodeAttemptRunner_BoundsWaitingAttemptsWithNoDeadline(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OpenUnmigrated(t)

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	if err := goose.UpToContext(ctx, db, ".", 20); err != nil {
		t.Fatalf("apply the schema before the bound: %v", err)
	}

	uow := postgres.NewUnitOfWork(pool)
	f := seedLegacyRun(ctx, t, pool, uow)
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_attempts (id, node_run_id, attempt_no, status, input, started_at, deadline_at, dispatched_at, completed_at)
		VALUES ('a_waiting', $1, 1, 'DISPATCHED', '{}', $2, NULL, $2,   NULL),
		       ('a_bounded', $1, 2, 'DISPATCHED', '{}', $2, $2,   $2,   NULL),
		       ('a_stuck',   $1, 3, 'STARTED',    '{}', $2, NULL, NULL, NULL),
		       ('a_done',    $1, 4, 'SUCCEEDED',  '{}', $2, NULL, NULL, $2)`, f.nodeRunID, fixtureTime); err != nil {
		t.Fatalf("insert attempts: %v", err)
	}

	var before, after time.Time
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&before); err != nil {
		t.Fatalf("read the clock: %v", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&after); err != nil {
		t.Fatalf("read the clock: %v", err)
	}

	deadlineOf := func(id string) *time.Time {
		t.Helper()
		var deadline *time.Time
		if err := pool.QueryRow(ctx, `SELECT deadline_at FROM node_attempts WHERE id = $1`, id).Scan(&deadline); err != nil {
			t.Fatalf("read deadline of %s: %v", id, err)
		}
		return deadline
	}
	waiting := deadlineOf("a_waiting")
	if waiting == nil || waiting.Before(before.Add(24*time.Hour)) || waiting.After(after.Add(24*time.Hour)) {
		t.Fatalf("waiting deadline = %v, want 24 hours after the migration, between %v and %v", waiting, before, after)
	}
	if got := deadlineOf("a_bounded"); got == nil || !got.Equal(fixtureTime) {
		t.Fatalf("bounded deadline = %v, want its own %v", got, fixtureTime)
	}
	if got := deadlineOf("a_stuck"); got != nil {
		t.Fatalf("STARTED deadline = %v, want none: the orphan path owns this state", got)
	}
	if got := deadlineOf("a_done"); got != nil {
		t.Fatalf("finished Attempt deadline = %v, want none", got)
	}

	var expired, orphans []domain.NodeAttempt
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if expired, err = tx.NodeAttempts().ListExpired(ctx, waiting.Add(time.Second), 10); err != nil {
			return err
		}
		orphans, err = tx.NodeAttempts().ListOrphanStarted(ctx, orphanRunnerAfter, 10)
		return err
	}); err != nil {
		t.Fatalf("list recovery work: %v", err)
	}
	ids := make(map[string]bool, len(expired))
	for _, attempt := range expired {
		ids[attempt.ID] = true
	}
	if !ids["a_waiting"] {
		t.Fatalf("expired rows = %d, want the backfilled row reachable by the timeout scan", len(expired))
	}
	if len(orphans) != 1 || orphans[0].ID != "a_stuck" {
		t.Fatalf("orphans = %d, want the STARTED row alone, still found by the orphan scan", len(orphans))
	}
}
