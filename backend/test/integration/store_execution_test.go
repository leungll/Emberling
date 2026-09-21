//go:build integration

// Package integration: this file covers the store additions the service layer needs for
// completion writes, retry bookkeeping, Attempt dispatch/result writes and Reconciler
// scanning (docs/06-execution-model.md §1.3-§1.5, §2; docs/09-testing-and-acceptance.md
// §3.1-§3.3).
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// RunRepository: output write-once, terminal completed_at, latest Run lookup
// ---------------------------------------------------------------------------

func TestRuns_SetOutput_SecondWriteReturnsConflict(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	first := json.RawMessage(`{"image":"first.png"}`)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
		if err != nil {
			return err
		}
		return tx.Runs().SetOutput(ctx, lock, first)
	}); err != nil {
		t.Fatalf("first SetOutput: %v", err)
	}

	second := json.RawMessage(`{"image":"second.png"}`)
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
		if err != nil {
			return err
		}
		return tx.Runs().SetOutput(ctx, lock, second)
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second SetOutput: want domain.ErrConflict, got %v", err)
	}

	// PostgreSQL re-serialises JSONB (e.g. inserting a space after ':'), so compare
	// decoded values rather than raw bytes.
	run := getRun(ctx, t, uow, f.runID)
	var gotOutput, wantOutput map[string]any
	if err := json.Unmarshal(run.Output, &gotOutput); err != nil {
		t.Fatalf("unmarshal stored output: %v", err)
	}
	if err := json.Unmarshal(first, &wantOutput); err != nil {
		t.Fatalf("unmarshal expected output: %v", err)
	}
	if gotOutput["image"] != wantOutput["image"] {
		t.Fatalf("run.output after rejected second write: want %v, got %v", wantOutput, gotOutput)
	}
}

func TestRuns_UpdateAggregate_TerminalSetsCompletedAtOnce(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	firstCompletion := fixtureTime.Add(time.Minute)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
		if err != nil {
			return err
		}
		return tx.Runs().UpdateAggregate(ctx, lock, domain.RunCompleted, firstCompletion)
	}); err != nil {
		t.Fatalf("first UpdateAggregate: %v", err)
	}

	run := getRun(ctx, t, uow, f.runID)
	if run.CompletedAt == nil || !run.CompletedAt.Equal(firstCompletion) {
		t.Fatalf("completed_at after first terminal write: want %s, got %v", firstCompletion, run.CompletedAt)
	}

	// A second terminal write (e.g. a duplicated advancement path) must not move
	// completed_at, even though status and last_seq may still be rewritten.
	secondCompletion := fixtureTime.Add(time.Hour)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
		if err != nil {
			return err
		}
		return tx.Runs().UpdateAggregate(ctx, lock, domain.RunCompleted, secondCompletion)
	}); err != nil {
		t.Fatalf("second UpdateAggregate: %v", err)
	}

	run = getRun(ctx, t, uow, f.runID)
	if run.CompletedAt == nil || !run.CompletedAt.Equal(firstCompletion) {
		t.Fatalf("completed_at after second terminal write: want unchanged %s, got %v", firstCompletion, run.CompletedAt)
	}
}

// TestRuns_UpdateAggregate_NonTerminalAfterTerminal_Rejected proves the WHERE guard
// added to UpdateAggregate: once a Run has reached a terminal status, a later call
// writing a non-terminal status back (a duplicated or stale advancement path racing
// behind the terminal write) must be rejected rather than resurrecting the Run.
func TestRuns_UpdateAggregate_NonTerminalAfterTerminal_Rejected(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	completedAt := fixtureTime.Add(time.Minute)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
		if err != nil {
			return err
		}
		return tx.Runs().UpdateAggregate(ctx, lock, domain.RunCompleted, completedAt)
	}); err != nil {
		t.Fatalf("terminal UpdateAggregate: %v", err)
	}

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
		if err != nil {
			return err
		}
		return tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, fixtureTime.Add(time.Hour))
	})
	var invalid *domain.InvalidStateTransitionError
	if !errors.As(err, &invalid) {
		t.Fatalf("non-terminal UpdateAggregate after terminal: want *domain.InvalidStateTransitionError, got %v", err)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("non-terminal UpdateAggregate after terminal: want error satisfying ErrConflict, got %v", err)
	}

	run := getRun(ctx, t, uow, f.runID)
	if run.Status != domain.RunCompleted {
		t.Fatalf("status after rejected non-terminal write: want unchanged COMPLETED, got %s", run.Status)
	}
	if run.CompletedAt == nil || !run.CompletedAt.Equal(completedAt) {
		t.Fatalf("completed_at after rejected non-terminal write: want unchanged %s, got %v", completedAt, run.CompletedAt)
	}
}

func TestRuns_LatestByWorkflow_ReturnsNewestOrNil(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	// No Run has ever been created for this Workflow yet.
	saveDefinition(ctx, t, uow, newDefinition("wf_latest", 1, "Latest"))

	var none *domain.Run
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		none, err = tx.Runs().LatestByWorkflow(ctx, "wf_latest")
		return err
	}); err != nil {
		t.Fatalf("LatestByWorkflow with no Runs: %v", err)
	}
	if none != nil {
		t.Fatalf("LatestByWorkflow with no Runs: want nil, got %+v", none)
	}

	older := fixtureTime
	newer := fixtureTime.Add(time.Hour)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.Runs().Create(ctx, domain.Run{
			ID: "run_latest_older", WorkflowID: "wf_latest", DefinitionVersion: 1,
			Status: domain.RunCompleted, Input: json.RawMessage(`{}`),
			StartedAt: older, UpdatedAt: older,
		}); err != nil {
			return err
		}
		return tx.Runs().Create(ctx, domain.Run{
			ID: "run_latest_newer", WorkflowID: "wf_latest", DefinitionVersion: 1,
			Status: domain.RunRunning, Input: json.RawMessage(`{}`),
			StartedAt: newer, UpdatedAt: newer,
		})
	}); err != nil {
		t.Fatalf("seed runs: %v", err)
	}

	var latest *domain.Run
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		latest, err = tx.Runs().LatestByWorkflow(ctx, "wf_latest")
		return err
	}); err != nil {
		t.Fatalf("LatestByWorkflow: %v", err)
	}
	if latest == nil || latest.ID != "run_latest_newer" {
		t.Fatalf("LatestByWorkflow: want run_latest_newer, got %+v", latest)
	}
}

// ---------------------------------------------------------------------------
// NodeRunRepository: retry claim, retry scheduling, terminal writes
// ---------------------------------------------------------------------------

func TestNodeRuns_ClaimRetry_ConcurrentClaims_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	now := time.Unix(1_800_000_000, 0).UTC()
	elapsed := now.Add(-time.Minute)

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Transition(ctx, f.nodeRunID, domain.NodeRunReady, domain.NodeRunRunning, fixtureTime)
	}); err != nil {
		t.Fatalf("move to RUNNING: %v", err)
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().ScheduleRetry(ctx, f.nodeRunID, elapsed, fixtureTime)
	}); err != nil {
		t.Fatalf("ScheduleRetry: %v", err)
	}

	const claimants = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(claimants)

	results := make([]bool, claimants)
	failures := make([]error, claimants)

	for i := range claimants {
		go func() {
			defer done.Done()
			start.Wait()
			failures[i] = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
				if _, err := tx.Runs().LockForUpdate(ctx, f.runID); err != nil {
					return err
				}
				won, err := tx.NodeRuns().ClaimRetry(ctx, f.nodeRunID, now)
				if err != nil {
					return err
				}
				results[i] = won
				return nil
			})
		}()
	}

	start.Done()
	done.Wait()

	winners := 0
	for i := range claimants {
		if failures[i] != nil {
			t.Fatalf("claimant %d: unexpected error %v", i, failures[i])
		}
		if results[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("ClaimRetry winners: want exactly 1, got %d", winners)
	}

	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.NextAttemptAt != nil {
		t.Fatalf("next_attempt_at after claim: want cleared, got %v", nr.NextAttemptAt)
	}
	if nr.Status != domain.NodeRunRunning {
		t.Fatalf("NodeRun status after retry claim: want RUNNING, got %s", nr.Status)
	}
}

func TestNodeRuns_ClaimRetry_BeforeNextAttemptAt_NotClaimed(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	now := time.Unix(1_800_000_000, 0).UTC()
	future := now.Add(time.Hour)

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Transition(ctx, f.nodeRunID, domain.NodeRunReady, domain.NodeRunRunning, fixtureTime)
	}); err != nil {
		t.Fatalf("move to RUNNING: %v", err)
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().ScheduleRetry(ctx, f.nodeRunID, future, fixtureTime)
	}); err != nil {
		t.Fatalf("ScheduleRetry: %v", err)
	}

	var won bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		won, err = tx.NodeRuns().ClaimRetry(ctx, f.nodeRunID, now)
		return err
	}); err != nil {
		t.Fatalf("ClaimRetry: %v", err)
	}
	if won {
		t.Fatal("ClaimRetry before next_attempt_at: want false, got true")
	}

	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.NextAttemptAt == nil || !nr.NextAttemptAt.Equal(future) {
		t.Fatalf("next_attempt_at after rejected claim: want unchanged %s, got %v", future, nr.NextAttemptAt)
	}
}

func TestNodeRuns_ScheduleRetry_FromNonRunning_ReturnsStaleClaim(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	next := fixtureTime.Add(time.Minute)

	// f.nodeRunID is still READY: ScheduleRetry requires RUNNING.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().ScheduleRetry(ctx, f.nodeRunID, next, fixtureTime)
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("ScheduleRetry from READY: want domain.ErrStaleClaim, got %v", err)
	}

	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.NextAttemptAt != nil {
		t.Fatalf("next_attempt_at after rejected schedule: want nil, got %v", nr.NextAttemptAt)
	}
}

func TestNodeRuns_MarkSucceeded_WrongFrom_ReturnsStaleClaimAndLeavesRowUntouched(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	// The row is actually READY; claim as if it were RUNNING (a legal edge for the state
	// machine, but stale for this particular row).
	outcome := store.NodeRunOutcome{Output: json.RawMessage(`{"ok":true}`)}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().MarkSucceeded(ctx, f.nodeRunID, domain.NodeRunRunning, fixtureTime.Add(time.Minute), outcome)
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("MarkSucceeded from wrong actual state: want domain.ErrStaleClaim, got %v", err)
	}

	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.Status != domain.NodeRunReady {
		t.Fatalf("NodeRun status after stale MarkSucceeded: want unchanged READY, got %s", nr.Status)
	}
	if len(nr.Output) != 0 {
		t.Fatalf("NodeRun output after stale MarkSucceeded: want untouched, got %s", nr.Output)
	}
}

func TestNodeRuns_MarkFailed_WritesErrorAndCompletedAt(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Transition(ctx, f.nodeRunID, domain.NodeRunReady, domain.NodeRunRunning, fixtureTime)
	}); err != nil {
		t.Fatalf("move to RUNNING: %v", err)
	}

	completedAt := fixtureTime.Add(time.Minute)
	execErr := domain.ExecutionError{Code: "PROVIDER_ERROR", Message: "model call failed"}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().MarkFailed(ctx, f.nodeRunID, domain.NodeRunRunning, completedAt, execErr)
	}); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.Status != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after MarkFailed: want FAILED, got %s", nr.Status)
	}
	if nr.Error == nil || nr.Error.Code != execErr.Code || nr.Error.Message != execErr.Message {
		t.Fatalf("NodeRun error after MarkFailed: want %+v, got %v", execErr, nr.Error)
	}
	if nr.CompletedAt == nil || !nr.CompletedAt.Equal(completedAt) {
		t.Fatalf("NodeRun completed_at after MarkFailed: want %s, got %v", completedAt, nr.CompletedAt)
	}
}

func TestNodeRuns_IncrementAttemptCount_ReturnsNewCount(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	var first, second int
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		first, err = tx.NodeRuns().IncrementAttemptCount(ctx, f.nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("first IncrementAttemptCount: %v", err)
	}
	if first != 1 {
		t.Fatalf("first IncrementAttemptCount: want 1, got %d", first)
	}

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		second, err = tx.NodeRuns().IncrementAttemptCount(ctx, f.nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("second IncrementAttemptCount: %v", err)
	}
	if second != 2 {
		t.Fatalf("second IncrementAttemptCount: want 2, got %d", second)
	}

	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.AttemptCount != 2 {
		t.Fatalf("stored attempt_count: want 2, got %d", nr.AttemptCount)
	}
}

// ---------------------------------------------------------------------------
// NodeAttemptRepository: expiry scan, terminal writes, latest lookup
// ---------------------------------------------------------------------------

func TestNodeAttempts_ListExpired_ReturnsOnlyPastDeadlineNonTerminal(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	now := time.Unix(1_800_000_000, 0).UTC()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	expiredStarted := newNodeAttempt("attempt_expired_started", f.nodeRunID, 1)
	expiredStarted.DeadlineAt = &past

	notYetDue := newNodeAttempt("attempt_not_due", f.nodeRunID, 2)
	notYetDue.DeadlineAt = &future

	expiredButSucceeded := newNodeAttempt("attempt_expired_terminal", f.nodeRunID, 3)
	expiredButSucceeded.DeadlineAt = &past
	expiredButSucceeded.Status = domain.NodeAttemptSucceeded
	expiredButSucceeded.Result = json.RawMessage(`{"done":true}`)

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for _, a := range []domain.NodeAttempt{expiredStarted, notYetDue, expiredButSucceeded} {
			if err := tx.NodeAttempts().Create(ctx, a); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}

	var found []domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		found, err = tx.NodeAttempts().ListExpired(ctx, now, 50)
		return err
	}); err != nil {
		t.Fatalf("ListExpired: %v", err)
	}

	got := map[string]bool{}
	for _, a := range found {
		got[a.ID] = true
	}
	if !got["attempt_expired_started"] {
		t.Fatal("expired STARTED Attempt: want rediscovered, got missing")
	}
	if got["attempt_not_due"] {
		t.Fatal("Attempt before its deadline: want skipped, got returned")
	}
	if got["attempt_expired_terminal"] {
		t.Fatal("already SUCCEEDED Attempt past its deadline: want skipped, got returned")
	}
}

func TestNodeAttempts_MarkSucceeded_FromFailed_ReturnsInvalidStateTransition(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	attempt := newNodeAttempt("attempt_failed_first", f.nodeRunID, 1)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().Create(ctx, attempt)
	}); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().MarkFailed(ctx, attempt.ID, domain.NodeAttemptStarted, fixtureTime.Add(time.Minute),
			domain.ExecutionError{Code: "TIMEOUT", Message: "deadline exceeded"})
	}); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().MarkSucceeded(ctx, attempt.ID, domain.NodeAttemptFailed, fixtureTime.Add(2*time.Minute),
			json.RawMessage(`{"late":true}`))
	})
	var invalid *domain.InvalidStateTransitionError
	if !errors.As(err, &invalid) {
		t.Fatalf("MarkSucceeded from FAILED: want *domain.InvalidStateTransitionError, got %v", err)
	}
}

func TestNodeAttempts_Latest_ReturnsHighestAttemptNo(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	var none *domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		none, err = tx.NodeAttempts().Latest(ctx, f.nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("Latest with no Attempts: %v", err)
	}
	if none != nil {
		t.Fatalf("Latest with no Attempts: want nil, got %+v", none)
	}

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for i := 1; i <= 3; i++ {
			if err := tx.NodeAttempts().Create(ctx, newNodeAttempt(fmt.Sprintf("attempt_latest_%d", i), f.nodeRunID, i)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}

	var latest *domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		latest, err = tx.NodeAttempts().Latest(ctx, f.nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest == nil || latest.AttemptNo != 3 {
		t.Fatalf("Latest attempt_no: want 3, got %+v", latest)
	}
}

// ---------------------------------------------------------------------------
// DefinitionRepository: sequential version enforcement, Workflow listing
// ---------------------------------------------------------------------------

func TestDefinitions_SaveNonSequentialVersion_ReturnsVersionConflict(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	saveDefinition(ctx, t, uow, newDefinition("wf_sequential", 1, "First"))

	// Jumping straight to version 3 skips the required version 2.
	skip := newDefinition("wf_sequential", 3, "Skip ahead")
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, skip)
	})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("Save version 3 after latest 1: want domain.ErrVersionConflict, got %v", err)
	}

	wf, err := getWorkflow(ctx, t, uow, "wf_sequential")
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if wf.LatestVersion != 1 {
		t.Fatalf("latest_version after rejected skip: want unchanged 1, got %d", wf.LatestVersion)
	}

	// A brand new Workflow must start at version 1; version 2 for a Workflow that has
	// never been saved is equally out of sequence.
	err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, newDefinition("wf_never_saved", 2, "Second first"))
	})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("Save version 2 for new workflow: want domain.ErrVersionConflict, got %v", err)
	}

	// The correct next version, 2, must still succeed.
	saveDefinition(ctx, t, uow, newDefinition("wf_sequential", 2, "Second"))
	wf, err = getWorkflow(ctx, t, uow, "wf_sequential")
	if err != nil {
		t.Fatalf("GetWorkflow after correct version: %v", err)
	}
	if wf.LatestVersion != 2 {
		t.Fatalf("latest_version after version 2: want 2, got %d", wf.LatestVersion)
	}
}

func TestDefinitions_ListWorkflows_ReflectsLatestVersion(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	saveDefinition(ctx, t, uow, newDefinition("wf_list_a", 1, "A v1"))
	saveDefinition(ctx, t, uow, newDefinition("wf_list_b", 1, "B v1"))
	saveDefinition(ctx, t, uow, newDefinition("wf_list_b", 2, "B v2"))

	var workflows []store.WorkflowSummary
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		workflows, err = tx.Definitions().ListWorkflows(ctx)
		return err
	}); err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}

	byID := map[string]domain.Workflow{}
	for _, wf := range workflows {
		byID[wf.Workflow.WorkflowID] = wf.Workflow
	}
	a, ok := byID["wf_list_a"]
	if !ok {
		t.Fatal("ListWorkflows: want wf_list_a present, got missing")
	}
	if a.LatestVersion != 1 {
		t.Fatalf("wf_list_a latest_version: want 1, got %d", a.LatestVersion)
	}
	b, ok := byID["wf_list_b"]
	if !ok {
		t.Fatal("ListWorkflows: want wf_list_b present, got missing")
	}
	if b.LatestVersion != 2 {
		t.Fatalf("wf_list_b latest_version: want 2, got %d", b.LatestVersion)
	}
}

// TestDefinitions_ConcurrentCreateSameNewWorkflow_ExactlyOneWinsOtherVersionConflict
// covers the brand-new-workflow race: since no workflows row exists yet, SELECT ...
// FOR UPDATE locks nothing for either racer, so both reach the workflows INSERT and
// race on its workflow_id PRIMARY KEY. Exactly one must win; the other must observe
// domain.ErrVersionConflict, not the generic domain.ErrConflict a plain unique
// violation would otherwise map to.
func TestDefinitions_ConcurrentCreateSameNewWorkflow_ExactlyOneWinsOtherVersionConflict(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	const racers = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(racers)

	errs := make([]error, racers)

	for i := range racers {
		go func() {
			defer done.Done()
			start.Wait()
			errs[i] = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
				return tx.Definitions().Save(ctx, newDefinition("wf_concurrent_new", 1, "Concurrent new"))
			})
		}()
	}

	start.Done()
	done.Wait()

	wins := 0
	for i := range racers {
		switch {
		case errs[i] == nil:
			wins++
		case errors.Is(errs[i], domain.ErrVersionConflict):
			// expected loser outcome
		default:
			t.Fatalf("racer %d: want nil or domain.ErrVersionConflict, got %v", i, errs[i])
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent create of same new workflow: want exactly 1 winner, got %d", wins)
	}

	wf, err := getWorkflow(ctx, t, uow, "wf_concurrent_new")
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if wf.LatestVersion != 1 {
		t.Fatalf("latest_version after concurrent create: want 1, got %d", wf.LatestVersion)
	}
}

func getWorkflow(ctx context.Context, t *testing.T, uow store.UnitOfWork, workflowID string) (domain.Workflow, error) {
	t.Helper()
	var wf domain.Workflow
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		wf, err = tx.Definitions().GetWorkflow(ctx, workflowID)
		return err
	})
	return wf, err
}
