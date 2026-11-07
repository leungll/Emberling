//go:build integration

// Reconciler scans and terminal Runs. A batch-limited scan stays useful only if a row that
// can never advance cannot occupy the batch. A Run that ended can leave READY or waiting
// siblings behind, so two kinds of scan exist: those whose use case does nothing for a
// terminal Run must not list its rows, or a backlog of them hides new work forever; and
// those whose use case does real work for a terminal Run, ending an Attempt, freeing a
// Provider slot, closing a wait or asking production about a release, must keep listing
// them. Both halves are proven here against PostgreSQL.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// terminalScanBatch is the batch limit these tests scan with, and terminalScanBacklog the
// number of dead rows seeded ahead of the work that must stay visible: more than one full
// batch, so a scan that does not skip them can never reach the row behind them.
const (
	terminalScanBatch   = 3
	terminalScanBacklog = terminalScanBatch + 2
)

// addScanRun commits one more Run of the seeded Workflow with the given status. It returns
// a fixture naming that Run.
func addScanRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, f fixture, runID string, status domain.RunStatus) fixture {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Runs().Create(ctx, domain.Run{
			ID: runID, WorkflowID: f.workflowID, DefinitionVersion: 1, Status: status,
			Input: json.RawMessage(`{}`), StartedAt: fixtureTime, UpdatedAt: fixtureTime,
		})
	}); err != nil {
		t.Fatalf("add run %s: %v", runID, err)
	}
	return fixture{workflowID: f.workflowID, runID: runID}
}

// seedScanNodeRun commits one READY NodeRun, or a RUNNING one with an elapsed retry backoff
// when retryAt is set, that became ready at the given offset from the fixture time.
func seedScanNodeRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, suffix string, offset time.Duration, retryAt *time.Time) string {
	t.Helper()
	nr := newNodeRun("nr_"+suffix, runID, "node_"+suffix)
	nr.ReadyAt = fixtureTime.Add(offset)
	if retryAt != nil {
		nr.Status = domain.NodeRunRunning
		nr.NextAttemptAt = retryAt
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Create(ctx, nr)
	}); err != nil {
		t.Fatalf("seed node run %s: %v", nr.ID, err)
	}
	return nr.ID
}

// seedScanTurn commits a RUNNING Agent NodeRun with one Agent Run and its READY Turn. The
// Agent deadline is the given time.
func seedScanTurn(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, suffix string, deadline time.Time) (agentRunID, turnID string) {
	t.Helper()
	agentRunID, turnID = "ar_"+suffix, "turn_"+suffix
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		nr := newNodeRun("nr_"+suffix, runID, "node_"+suffix)
		nr.Status = domain.NodeRunRunning
		if err := tx.NodeRuns().Create(ctx, nr); err != nil {
			return err
		}
		agentRun := newAgentRun(agentRunID, nr.ID)
		agentRun.Deadline = deadline
		if err := tx.AgentRuns().Create(ctx, agentRun); err != nil {
			return err
		}
		return tx.AgentTurns().Create(ctx, newAgentTurn(turnID, agentRunID, 1))
	}); err != nil {
		t.Fatalf("seed turn %s: %v", suffix, err)
	}
	return agentRunID, turnID
}

// seedScanAction continues seedScanTurn to a COMPLETED Turn with its Decision and READY
// Action.
func seedScanAction(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, suffix string, deadline time.Time) (agentRunID, actionID string) {
	t.Helper()
	agentRunID, turnID := seedScanTurn(ctx, t, uow, runID, suffix, deadline)
	actionID = "action_" + suffix
	decisionID := "decision_" + suffix
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.AgentTurns().ClaimReady(ctx, turnID, fixtureTime); err != nil {
			return err
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, turnID, fixtureTime, json.RawMessage(`{"kind":"TOOL_CALL"}`), nil, nil); err != nil {
			return err
		}
		if err := tx.AgentDecisions().Create(ctx, newAgentDecision(decisionID, turnID)); err != nil {
			return err
		}
		return tx.AgentActions().Create(ctx, newAgentAction(actionID, turnID, decisionID))
	}); err != nil {
		t.Fatalf("seed action %s: %v", suffix, err)
	}
	return agentRunID, actionID
}

// seedScanConfirmDeadline commits a release Action that waits on production, with a STARTED
// Attempt whose confirm deadline is the given time and the paused Agent a release leaves.
// The pause is the reason such a row has no other way out on a terminal Run: an Agent
// deadline does not run while the Agent is paused.
func seedScanConfirmDeadline(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, suffix string, agentDeadline, confirm time.Time) string {
	t.Helper()
	agentRunID, actionID := seedScanAction(ctx, t, uow, runID, suffix, agentDeadline)
	attemptID := "tool_attempt_" + suffix
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.AgentActions().ClaimReady(ctx, actionID, fixtureTime); err != nil {
			return err
		}
		if err := tx.ToolAttempts().Create(ctx, newReleaseAttempt(attemptID, actionID, 1, confirm)); err != nil {
			return err
		}
		if err := tx.AgentActions().MarkWaiting(ctx, actionID, fixtureTime); err != nil {
			return err
		}
		return tx.AgentRuns().Pause(ctx, agentRunID, domain.PauseRelease, fixtureTime)
	}); err != nil {
		t.Fatalf("seed confirm deadline %s: %v", suffix, err)
	}
	return attemptID
}

func scanNodeRunIDs(ctx context.Context, t *testing.T, uow store.UnitOfWork, now time.Time, limit int) []string {
	t.Helper()
	var ids []string
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		rows, err := tx.NodeRuns().ListReadyOrRetryable(ctx, now, limit)
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return err
	}); err != nil {
		t.Fatalf("ListReadyOrRetryable: %v", err)
	}
	return ids
}

func scanTurnIDs(ctx context.Context, t *testing.T, uow store.UnitOfWork, limit int) []string {
	t.Helper()
	var ids []string
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		rows, err := tx.AgentTurns().ListReady(ctx, limit)
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return err
	}); err != nil {
		t.Fatalf("AgentTurns.ListReady: %v", err)
	}
	return ids
}

func scanActionIDs(ctx context.Context, t *testing.T, uow store.UnitOfWork, limit int) []string {
	t.Helper()
	var ids []string
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		rows, err := tx.AgentActions().ListReady(ctx, limit)
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return err
	}); err != nil {
		t.Fatalf("AgentActions.ListReady: %v", err)
	}
	return ids
}

func scanOrphanIDs(ctx context.Context, t *testing.T, uow store.UnitOfWork, runnerID string, now time.Time, limit int) []string {
	t.Helper()
	var ids []string
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		rows, err := tx.ToolAttempts().ListOrphanStarted(ctx, runnerID, now, limit)
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return err
	}); err != nil {
		t.Fatalf("ListOrphanStarted: %v", err)
	}
	return ids
}

func scanConfirmDeadlineIDs(ctx context.Context, t *testing.T, uow store.UnitOfWork, now time.Time, limit int) []string {
	t.Helper()
	var ids []string
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		rows, err := tx.ToolAttempts().ListDueConfirmDeadlines(ctx, now, limit)
		for _, row := range rows {
			ids = append(ids, row.AttemptID)
		}
		return err
	}); err != nil {
		t.Fatalf("ListDueConfirmDeadlines: %v", err)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Scans whose use case does nothing for a terminal Run
// ---------------------------------------------------------------------------

func TestNodeRuns_ListReadyOrRetryable_TerminalBacklogBeyondBatch_FindsActiveWork(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	failed := addScanRun(ctx, t, uow, f, "run_failed", domain.RunFailed)
	completed := addScanRun(ctx, t, uow, f, "run_completed", domain.RunCompleted)
	paused := addScanRun(ctx, t, uow, f, "run_paused", domain.RunPaused)
	now := fixtureTime.Add(time.Hour)
	elapsed := now.Add(-time.Minute)

	// The dead rows are the oldest in the table, in both branches of the scan and in both
	// terminal statuses, so without a filter they fill every batch.
	for i := range terminalScanBacklog {
		seedScanNodeRun(ctx, t, uow, failed.runID, fmt.Sprintf("failed_ready_%d", i), -time.Hour, nil)
		seedScanNodeRun(ctx, t, uow, failed.runID, fmt.Sprintf("failed_retry_%d", i), -time.Hour, &elapsed)
		seedScanNodeRun(ctx, t, uow, completed.runID, fmt.Sprintf("completed_ready_%d", i), -time.Hour, nil)
	}
	liveReady := seedScanNodeRun(ctx, t, uow, f.runID, "live_ready", time.Minute, nil)
	liveRetry := seedScanNodeRun(ctx, t, uow, f.runID, "live_retry", 2*time.Minute, &elapsed)
	pausedReady := seedScanNodeRun(ctx, t, uow, paused.runID, "paused_ready", 3*time.Minute, nil)

	got := scanNodeRunIDs(ctx, t, uow, now, terminalScanBatch+1)
	want := []string{f.nodeRunID, liveReady, liveRetry, pausedReady}
	if !slices.Equal(got, want) {
		t.Fatalf("ListReadyOrRetryable behind a terminal backlog = %v, want %v: only work that can still advance, oldest first", got, want)
	}
}

func TestAgentTurns_ListReady_TerminalBacklogBeyondBatch_FindsActiveWork(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	failed := addScanRun(ctx, t, uow, f, "run_failed", domain.RunFailed)
	paused := addScanRun(ctx, t, uow, f, "run_paused", domain.RunPaused)
	deadline := fixtureTime.Add(time.Hour)

	for i := range terminalScanBacklog {
		seedScanTurn(ctx, t, uow, failed.runID, fmt.Sprintf("a_dead_%d", i), deadline)
	}
	_, live := seedScanTurn(ctx, t, uow, f.runID, "z_live", deadline)
	_, waiting := seedScanTurn(ctx, t, uow, paused.runID, "z_paused", deadline)

	got := scanTurnIDs(ctx, t, uow, terminalScanBatch)
	if want := []string{live, waiting}; !slices.Equal(got, want) {
		t.Fatalf("AgentTurns.ListReady behind a terminal backlog = %v, want %v", got, want)
	}
}

func TestAgentActions_ListReady_TerminalBacklogBeyondBatch_FindsActiveWork(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	failed := addScanRun(ctx, t, uow, f, "run_failed", domain.RunFailed)
	deadline := fixtureTime.Add(time.Hour)

	for i := range terminalScanBacklog {
		seedScanAction(ctx, t, uow, failed.runID, fmt.Sprintf("a_dead_%d", i), deadline)
	}
	_, live := seedScanAction(ctx, t, uow, f.runID, "z_live", deadline)

	got := scanActionIDs(ctx, t, uow, terminalScanBatch)
	if want := []string{live}; !slices.Equal(got, want) {
		t.Fatalf("AgentActions.ListReady behind a terminal backlog = %v, want %v", got, want)
	}
}

func TestToolAttempts_ListOrphanStarted_TerminalBacklogBeyondBatch_FindsActiveWork(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	failed := addScanRun(ctx, t, uow, f, "run_failed", domain.RunFailed)
	now := fixtureTime.Add(time.Hour)
	previous := "runner-previous"

	// The dead Agents are paused: nothing else ever ends such a row, because a paused
	// Agent's deadline does not run.
	for i := range terminalScanBacklog {
		seedOrphanScanRow(ctx, t, uow, failed, orphanScanRow{suffix: fmt.Sprintf("a_dead_%d", i), runnerID: &previous, paused: true}, now)
	}
	live := seedOrphanScanRow(ctx, t, uow, f, orphanScanRow{suffix: "z_live", runnerID: &previous}, now)
	livePaused := seedOrphanScanRow(ctx, t, uow, f, orphanScanRow{suffix: "z_paused", runnerID: &previous, paused: true}, now)

	got := scanOrphanIDs(ctx, t, uow, "runner-current", now, terminalScanBatch)
	if want := []string{live, livePaused}; !slices.Equal(got, want) {
		t.Fatalf("ListOrphanStarted behind a terminal backlog = %v, want %v", got, want)
	}
}

func TestToolAttempts_ListDueConfirmDeadlines_TerminalBacklogBeyondBatch_FindsActiveWork(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	failed := addScanRun(ctx, t, uow, f, "run_failed", domain.RunFailed)
	now := fixtureTime.Add(time.Hour)
	agentDeadline := now.Add(time.Hour)

	for i := range terminalScanBacklog {
		seedScanConfirmDeadline(ctx, t, uow, failed.runID, fmt.Sprintf("dead_%d", i), agentDeadline, now.Add(-time.Hour))
	}
	live := seedScanConfirmDeadline(ctx, t, uow, f.runID, "live", agentDeadline, now.Add(-time.Minute))

	got := scanConfirmDeadlineIDs(ctx, t, uow, now, terminalScanBatch)
	if want := []string{live}; !slices.Equal(got, want) {
		t.Fatalf("ListDueConfirmDeadlines behind a terminal backlog = %v, want %v", got, want)
	}
}

// reconcilerSpy is an Advancer that records what one reconciliation pass handed over and
// does nothing else, so the assertion is about discovery alone. A use case it does not
// override would panic on the nil embedded interface: every row seeded for the pass must
// reach one of the recorded entries.
type reconcilerSpy struct {
	reconciler.Advancer
	calls []string
}

func (s *reconcilerSpy) Advance(_ context.Context, runID string) (service.AdvanceOutcome, error) {
	s.calls = append(s.calls, "advance "+runID)
	return service.AdvanceOutcome{}, nil
}

func (s *reconcilerSpy) AdvanceAgentTurn(_ context.Context, turnID string, _ domain.ClaimSource) error {
	s.calls = append(s.calls, "turn "+turnID)
	return nil
}

func (s *reconcilerSpy) ExecuteAgentAction(_ context.Context, actionID string, _ domain.ClaimSource) error {
	s.calls = append(s.calls, "action "+actionID)
	return nil
}

func (s *reconcilerSpy) HandleOrphanToolAttempt(_ context.Context, attemptID string) error {
	s.calls = append(s.calls, "orphan "+attemptID)
	return nil
}

func (s *reconcilerSpy) OpenReleaseHumanWait(_ context.Context, attemptID string) error {
	s.calls = append(s.calls, "confirm "+attemptID)
	return nil
}

// TestReconciler_TerminalBacklogFillsEveryBatch_HandsOverOnlyActiveWork is the whole gap in
// one pass: with more dead rows than the batch limit in every affected scan, one
// reconciliation still reaches the one live item of each scan and nothing else.
func TestReconciler_TerminalBacklogFillsEveryBatch_HandsOverOnlyActiveWork(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	failed := addScanRun(ctx, t, uow, f, "run_failed", domain.RunFailed)
	live := addScanRun(ctx, t, uow, f, "run_live", domain.RunRunning)
	clock := newExecClock(fixtureTime.Add(time.Hour))
	now := clock.Now()
	future := now.Add(time.Hour)
	previous := "runner-previous"

	for i := range terminalScanBacklog {
		seedScanNodeRun(ctx, t, uow, failed.runID, fmt.Sprintf("a_dead_%d", i), -time.Hour, nil)
		seedScanTurn(ctx, t, uow, failed.runID, fmt.Sprintf("a_turn_%d", i), future)
		seedScanAction(ctx, t, uow, failed.runID, fmt.Sprintf("a_action_%d", i), future)
		seedOrphanScanRow(ctx, t, uow, failed, orphanScanRow{suffix: fmt.Sprintf("a_orphan_%d", i), runnerID: &previous, paused: true}, now)
		seedScanConfirmDeadline(ctx, t, uow, failed.runID, fmt.Sprintf("a_confirm_%d", i), future, now.Add(-time.Hour))
	}
	seedScanNodeRun(ctx, t, uow, live.runID, "z_live", time.Minute, nil)
	_, turn := seedScanTurn(ctx, t, uow, live.runID, "z_turn", future)
	_, action := seedScanAction(ctx, t, uow, live.runID, "z_action", future)
	orphan := seedOrphanScanRow(ctx, t, uow, live, orphanScanRow{suffix: "z_orphan", runnerID: &previous}, now)
	confirm := seedScanConfirmDeadline(ctx, t, uow, live.runID, "z_confirm", future, now.Add(-time.Minute))

	// The seeded fixture Run's own READY NodeRun is not part of this story.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
		if err != nil {
			return err
		}
		return tx.Runs().UpdateAggregate(ctx, lock, domain.RunFailed, now)
	}); err != nil {
		t.Fatalf("end the fixture run: %v", err)
	}

	spy := &reconcilerSpy{}
	rec := reconciler.New(reconciler.Config{
		UoW: uow, Executor: spy, Clock: clock, BatchLimit: terminalScanBatch, RunnerID: "runner-current",
	})
	report, err := rec.RunOnce(ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report errors: %v", report.Errors)
	}

	want := []string{
		"advance " + live.runID,
		"orphan " + orphan,
		"confirm " + confirm,
		"turn " + turn,
		"action " + action,
	}
	if !slices.Equal(spy.calls, want) {
		t.Fatalf("handed over %v, want exactly %v", spy.calls, want)
	}
}

// TestReconciler_FailedRunLeavesReadySibling_NoLongerRediscovered reaches the dead row the
// way production does: one branch fails the Run while a sibling stays READY and queued.
// The sibling is left alone and the scan stops listing it.
func TestReconciler_FailedRunLeavesReadySibling_NoLongerRediscovered(t *testing.T) {
	h := newCapHarness(t, 1)
	_, held := h.holdSlot(h.linearDefinition(capModelID, nil))

	failing := h.createRun(h.fanOutDefinition(true))
	h.drive(failing.ID)
	if outcome := h.callback(h.nodeRun(failing.ID, "b_wait").ID, `{"status":"SUCCEEDED","text":"b"}`); !outcome.Accepted {
		t.Fatalf("callback for b_wait = %+v, want accepted", outcome)
	}
	h.drive(failing.ID)
	if got := h.run(failing.ID).Status; got != domain.RunFailed {
		t.Fatalf("run with the failing branch = %s, want FAILED", got)
	}
	sibling := h.nodeRun(failing.ID, "z_task")
	if sibling.Status != domain.NodeRunReady {
		t.Fatalf("z_task of the failed Run = %s, want left READY", sibling.Status)
	}

	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock})
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.ReadyOrRetryableFound != 0 || report.RunsAdvanced != 0 || len(report.Errors) != 0 {
		t.Fatalf("report = %+v, want nothing to advance: the slot holder %s waits and the sibling's Run ended", report, held.ID)
	}
	if got := h.nodeRun(failing.ID, "z_task"); got.Status != domain.NodeRunReady {
		t.Fatalf("z_task after the scan = %s, want untouched: no historic row is rewritten", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Scans whose use case does real work for a terminal Run
// ---------------------------------------------------------------------------

// TestReconciler_FailedRunSiblingPastDeadline_TimeoutEndsAttemptAndFreesSlot is why the
// node timeout scan must not skip terminal Runs: the Attempt of a sibling that kept waiting
// when the Run failed is what holds a Provider slot, and only its timeout frees it. The row
// is processed once and leaves the set, so it cannot pin the scan.
func TestReconciler_FailedRunSiblingPastDeadline_TimeoutEndsAttemptAndFreesSlot(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.fanOutDefinition(true)
	def.Version = 2
	for i := range def.Nodes {
		if def.Nodes[i].ID == "z_task" {
			def.Nodes[i].ExecutionPolicy = &domain.ExecutionPolicy{MaxAttempts: 1, Backoff: domain.BackoffFixed, TimeoutMs: 60_000}
		}
	}
	run := h.createRun(h.save(def))
	h.drive(run.ID)
	holder := h.nodeRun(run.ID, "z_task")
	if holder.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("z_task = %s, want WAITING_CALLBACK holding the slot", holder.Status)
	}
	h.assertHeld(holder)
	if outcome := h.callback(h.nodeRun(run.ID, "b_wait").ID, `{"status":"SUCCEEDED","text":"b"}`); !outcome.Accepted {
		t.Fatalf("callback for b_wait = %+v, want accepted", outcome)
	}
	h.drive(run.ID)
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED", got)
	}
	h.assertHeld(holder)

	h.clock.Advance(24 * time.Hour)
	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock})
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.ExpiredAttemptsFound != 1 || report.AttemptsTimedOut != 1 || len(report.Errors) != 0 {
		t.Fatalf("report = %+v, want the sibling's Attempt found and timed out", report)
	}
	h.assertNotHeld(holder)
	if attempts := h.attempts(holder.ID); len(attempts) != 1 || attempts[0].Status != domain.NodeAttemptFailed {
		t.Fatalf("attempts = %+v, want the one Attempt FAILED", attempts)
	}
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run after the sibling's timeout = %s, want FAILED", got)
	}

	again, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("second reconciler pass: %v", err)
	}
	if again.ExpiredAttemptsFound != 0 {
		t.Fatalf("second pass found %d expired attempts, want the handled row gone", again.ExpiredAttemptsFound)
	}
}

// TestTimeoutAgentRun_RunAlreadyFailed_EndsAgentAndClearsItsReadyTurn is why the Agent
// deadline scan must not skip terminal Runs while the READY Turn scan may: on a failed Run
// the Turn's own use case does nothing, and the Agent timeout is the transaction that ends
// the Agent Run and the Turn with it. The Agent Run leaves the expiry set once handled.
func TestTimeoutAgentRun_RunAlreadyFailed_EndsAgentAndClearsItsReadyTurn(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	f := seedRun(h.ctx, t, h.uow)
	failed := addScanRun(h.ctx, t, h.uow, f, "run_failed", domain.RunFailed)
	deadline := h.clock.Now().Add(time.Minute)
	agentRunID, turnID := seedScanTurn(h.ctx, t, h.uow, failed.runID, "dead", deadline)

	h.clock.Advance(2 * time.Minute)
	if got := listExpiredAgentRuns(h.ctx, t, h.uow, h.clock.Now(), 10); len(got) != 1 || got[0].ID != agentRunID {
		t.Fatalf("Agent ListExpired = %+v, want the Agent Run of the failed Run", got)
	}
	if err := h.svc.TimeoutAgentRun(h.ctx, agentRunID); err != nil {
		t.Fatalf("TimeoutAgentRun: %v", err)
	}

	agentRun := getAgentRun(h.ctx, t, h.uow, agentRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("termination = %v, want TIMEOUT", agentRun.Termination)
	}
	if got := getAgentTurn(h.ctx, t, h.uow, turnID).Status; got != domain.AgentTurnFailed {
		t.Fatalf("turn = %s, want FAILED", got)
	}
	if got := listExpiredAgentRuns(h.ctx, t, h.uow, h.clock.Now(), 10); len(got) != 0 {
		t.Fatalf("Agent ListExpired after the timeout = %+v, want the handled row gone", got)
	}
	if got := getRun(h.ctx, t, h.uow, failed.runID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED", got)
	}
}

// TestReconcilerScans_TerminalRunRowsWithRealWork_StayListed pins the scans a blanket
// "exclude terminal Runs" would silently break. Each row belongs to a FAILED Run, and each
// use case behind the scan writes something for it. The use cases themselves are proven by
// the tests above and by the approval, human wait and post-task reconciliation tests.
func TestReconcilerScans_TerminalRunRowsWithRealWork_StayListed(t *testing.T) {
	now := fixtureTime.Add(2 * time.Hour)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	type scan struct {
		name string
		// seed commits a row of the given Run and returns its identifier.
		seed func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string
		list func(ctx context.Context, tx store.Tx) ([]string, error)
	}
	scans := []scan{
		{
			name: "node attempts past their deadline",
			seed: func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string {
				t.Helper()
				nodeRunID, attemptID := "nr_expired", "attempt_expired"
				if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
					if err := tx.NodeRuns().Create(ctx, newNodeRun(nodeRunID, run.runID, "node_expired")); err != nil {
						return err
					}
					attempt := newNodeAttempt(attemptID, nodeRunID, 1)
					attempt.DeadlineAt = &past
					return tx.NodeAttempts().Create(ctx, attempt)
				}); err != nil {
					t.Fatalf("seed expired attempt: %v", err)
				}
				return attemptID
			},
			list: func(ctx context.Context, tx store.Tx) ([]string, error) {
				rows, err := tx.NodeAttempts().ListExpired(ctx, now, 10)
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
		{
			name: "node attempts with a due poll",
			seed: func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string {
				t.Helper()
				_, attemptID := seedAttempt(ctx, t, uow, run, "poll", domain.NodeRunWaitingCallback, false)
				if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
					return tx.NodeAttempts().MarkDispatched(ctx, attemptID, fixtureTime, &past)
				}); err != nil {
					t.Fatalf("dispatch attempt: %v", err)
				}
				return attemptID
			},
			list: func(ctx context.Context, tx store.Tx) ([]string, error) {
				rows, err := tx.NodeAttempts().ListDuePolls(ctx, now, 10)
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.AttemptID)
				}
				return ids, err
			},
		},
		{
			name: "tool attempts with a due poll",
			seed: func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string {
				t.Helper()
				_, actionID := seedScanAction(ctx, t, uow, run.runID, "poll", future)
				attempt := newToolAttempt("tool_attempt_poll", actionID, 1)
				dispatchToolAttemptAt(ctx, t, uow, attempt, &past)
				return attempt.ID
			},
			list: func(ctx context.Context, tx store.Tx) ([]string, error) {
				rows, err := tx.ToolAttempts().ListDuePolls(ctx, now, 10)
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.AttemptID)
				}
				return ids, err
			},
		},
		{
			name: "stored early callbacks of a waiting attempt",
			seed: func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string {
				t.Helper()
				_, attemptID := seedAttempt(ctx, t, uow, run, "pending", domain.NodeRunWaitingCallback, true)
				createBinding(ctx, t, uow, newBinding("binding_pending", "task_pending", domain.CallbackTargetNodeAttempt, attemptID))
				if _, err := recordPending(ctx, uow, newPending("task_pending", `{"ok":true}`, "sha256:p", "sha256:t")); err != nil {
					t.Fatalf("record pending: %v", err)
				}
				return "task_pending"
			},
			list: func(ctx context.Context, tx store.Tx) ([]string, error) {
				rows, err := tx.PendingCallbacks().ListConsumableForWaiting(ctx, now, 10)
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.Pending.ExternalTaskID)
				}
				return ids, err
			},
		},
		{
			name: "agent runs past their deadline",
			seed: func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string {
				t.Helper()
				agentRunID, _ := seedScanTurn(ctx, t, uow, run.runID, "expired", past)
				return agentRunID
			},
			list: func(ctx context.Context, tx store.Tx) ([]string, error) {
				rows, err := tx.AgentRuns().ListExpired(ctx, now, 10)
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
		{
			name: "pending approvals past their deadline",
			seed: func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string {
				t.Helper()
				agentRunID, actionID := seedScanAction(ctx, t, uow, run.runID, "approval", future)
				approval := domain.Approval{
					ID: "approval_expired", ActionID: actionID, RunID: run.runID, AgentRunID: agentRunID,
					Status: domain.ApprovalPending, RequestedAt: fixtureTime, Deadline: past,
				}
				if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
					return tx.Approvals().Create(ctx, approval)
				}); err != nil {
					t.Fatalf("create approval: %v", err)
				}
				return approval.ID
			},
			list: func(ctx context.Context, tx store.Tx) ([]string, error) {
				rows, err := tx.Approvals().ListExpiredPending(ctx, now, 10)
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
		{
			name: "open human waits past their deadline",
			seed: func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string {
				t.Helper()
				_, actionID := seedScanAction(ctx, t, uow, run.runID, "wait", future)
				wait := newHumanWait("hwait_expired", run.runID, actionID)
				wait.Deadline = past
				if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
					return tx.HumanWaits().Create(ctx, wait)
				}); err != nil {
					t.Fatalf("create human wait: %v", err)
				}
				return wait.ID
			},
			list: func(ctx context.Context, tx store.Tx) ([]string, error) {
				rows, err := tx.HumanWaits().ListExpiredOpen(ctx, now, 10)
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
		{
			name: "active occupations with a due post-task query",
			seed: func(ctx context.Context, t *testing.T, uow store.UnitOfWork, run fixture) string {
				t.Helper()
				_, actionID := seedScanAction(ctx, t, uow, run.runID, "occupation", future)
				occupation := newReleaseOccupation("occupation_due", run.runID, actionID, "op_due")
				acquireOccupation(ctx, t, uow, occupation)
				if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
					return tx.ReleaseOccupations().ScheduleQuery(ctx, occupation.ID, past)
				}); err != nil {
					t.Fatalf("schedule query: %v", err)
				}
				return occupation.ID
			},
			list: func(ctx context.Context, tx store.Tx) ([]string, error) {
				rows, err := tx.ReleaseOccupations().ListDueQueries(ctx, now, 10)
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
	}

	for _, sc := range scans {
		t.Run(sc.name, func(t *testing.T) {
			ctx := context.Background()
			uow := postgres.NewUnitOfWork(testdb.Open(t))
			f := seedRun(ctx, t, uow)
			failed := addScanRun(ctx, t, uow, f, "run_failed", domain.RunFailed)
			id := sc.seed(ctx, t, uow, failed)

			var got []string
			if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
				var err error
				got, err = sc.list(ctx, tx)
				return err
			}); err != nil {
				t.Fatalf("list: %v", err)
			}
			if !slices.Equal(got, []string{id}) {
				t.Fatalf("scan of a FAILED Run = %v, want [%s]: its use case still has work to do", got, id)
			}
		})
	}
}

// TestReleaseOccupations_ListDueQueries_LiveRunStaysWithThePollPath shows the scan is the
// mirror image of the recovery scans: only an ended Run's occupation is post-task work,
// however many of them there are.
func TestReleaseOccupations_ListDueQueries_LiveRunStaysWithThePollPath(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	failed := addScanRun(ctx, t, uow, f, "run_failed", domain.RunFailed)
	now := fixtureTime.Add(time.Hour)
	future := now.Add(time.Hour)

	var want []string
	for i, run := range []fixture{f, failed} {
		_, actionID := seedScanAction(ctx, t, uow, run.runID, fmt.Sprintf("occupation_%d", i), future)
		occupation := newReleaseOccupation(fmt.Sprintf("occupation_%d", i), run.runID, actionID, fmt.Sprintf("op_%d", i))
		occupation.TargetKey = fmt.Sprintf("service-%d/production", i)
		acquireOccupation(ctx, t, uow, occupation)
		if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			return tx.ReleaseOccupations().ScheduleQuery(ctx, occupation.ID, fixtureTime)
		}); err != nil {
			t.Fatalf("schedule query: %v", err)
		}
		if run.runID == failed.runID {
			want = append(want, occupation.ID)
		}
	}

	var got []string
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		rows, err := tx.ReleaseOccupations().ListDueQueries(ctx, now, 10)
		for _, row := range rows {
			got = append(got, row.ID)
		}
		return err
	}); err != nil {
		t.Fatalf("ListDueQueries: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ListDueQueries = %v, want %v", got, want)
	}
}
