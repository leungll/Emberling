//go:build integration

// Package integration verifies the PostgreSQL Store against a real database. Every test
// here exists because a transaction, lock, constraint or recovery guarantee cannot be
// demonstrated by a mock repository.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// Unit of Work atomicity (invariant #4; 09 §3.1 "状态更新后、Event COMMIT 前失败")
// ---------------------------------------------------------------------------

func TestUnitOfWork_EventInsertFails_RollsBackState(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	// Occupy seq 1 in its own committed transaction so that the next append inside the
	// state-changing transaction is guaranteed to violate UNIQUE (run_id, seq).
	appendEvent(ctx, t, uow, f.runID, domain.EventRunCreated)

	// A stale lock still believes last_seq is 0, so its next allocation collides.
	staleLock := store.NewRunLock(f.runID, domain.RunRunning, 0)

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.Runs().LockForUpdate(ctx, f.runID); err != nil {
			return err
		}
		if err := tx.NodeRuns().Transition(ctx, f.nodeRunID,
			domain.NodeRunReady, domain.NodeRunRunning, time.Now().UTC()); err != nil {
			return err
		}
		_, err := tx.Events().Append(ctx, staleLock, domain.Event{
			ID:      "evt_duplicate_seq",
			RunID:   f.runID,
			Type:    domain.EventNodeStarted,
			Payload: json.RawMessage(`{}`),
		})
		return err
	})
	if err == nil {
		t.Fatal("WithinTx: want duplicate seq error, got nil")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("WithinTx error: want wrapped domain.ErrConflict, got %v", err)
	}

	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.Status != domain.NodeRunReady {
		t.Fatalf("NodeRun status after rollback: want READY, got %s", nr.Status)
	}

	events := listEvents(ctx, t, uow, f.runID)
	if len(events) != 1 {
		t.Fatalf("events after rollback: want 1 committed event, got %d", len(events))
	}
}

// ---------------------------------------------------------------------------
// Conditional claim (invariants #5 and #7; 09 §3.1 "READY → RUNNING 发生并发抢占")
// ---------------------------------------------------------------------------

func TestNodeRuns_ConcurrentClaimReady_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

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
			// Barrier: every goroutine is parked here until the test releases them, so
			// the race is deterministic without sleeping.
			start.Wait()

			failures[i] = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
				if _, err := tx.Runs().LockForUpdate(ctx, f.runID); err != nil {
					return err
				}
				won, err := tx.NodeRuns().ClaimReady(ctx, f.nodeRunID, time.Now().UTC())
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
		t.Fatalf("ClaimReady winners: want exactly 1, got %d", winners)
	}

	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.Status != domain.NodeRunRunning {
		t.Fatalf("NodeRun status after claim: want RUNNING, got %s", nr.Status)
	}
	if nr.StartedAt == nil {
		t.Fatal("NodeRun started_at after claim: want set, got nil")
	}
}

// ---------------------------------------------------------------------------
// Run aggregate lock and seq allocation (invariants #1 and #4)
// ---------------------------------------------------------------------------

func TestRunLock_ConcurrentAppend_SeqStrictlyIncreasingAndUnique(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	aLocked := make(chan struct{})
	bParked := make(chan struct{})
	aDone := make(chan error, 1)
	bDone := make(chan error, 1)

	appendPair := func(tx store.Tx, first, second domain.EventType) error {
		lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
		if err != nil {
			return err
		}
		for _, typ := range []domain.EventType{first, second} {
			if _, err := tx.Events().Append(ctx, lock, newEvent(f.runID, typ)); err != nil {
				return err
			}
		}
		return tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, time.Now().UTC())
	}

	go func() {
		aDone <- uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			lock, err := tx.Runs().LockForUpdate(ctx, f.runID)
			if err != nil {
				return err
			}
			close(aLocked)
			// Hold the row lock until B is observably parked on it.
			<-bParked
			for _, typ := range []domain.EventType{domain.EventRunCreated, domain.EventNodeReady} {
				if _, err := tx.Events().Append(ctx, lock, newEvent(f.runID, typ)); err != nil {
					return err
				}
			}
			return tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, time.Now().UTC())
		})
	}()

	<-aLocked

	go func() {
		bDone <- uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			return appendPair(tx, domain.EventNodeStarted, domain.EventNodeCompleted)
		})
	}()

	// Barrier on observable database state rather than on elapsed time: B must actually
	// be waiting for A's row lock. If LockForUpdate stopped taking the lock, B would
	// never block and this would fail instead of passing by timing luck.
	if err := waitUntilBlockedOnLock(ctx, pool); err != nil {
		close(bParked)
		<-aDone
		<-bDone
		t.Fatalf("transaction B never parked on the Run aggregate lock: %v", err)
	}
	close(bParked)

	if err := <-aDone; err != nil {
		t.Fatalf("transaction A: %v", err)
	}
	if err := <-bDone; err != nil {
		t.Fatalf("transaction B: %v", err)
	}

	events := listEvents(ctx, t, uow, f.runID)
	if len(events) != 4 {
		t.Fatalf("committed events: want 4, got %d", len(events))
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d seq: want %d, got %d", i, i+1, ev.Seq)
		}
	}

	run := getRun(ctx, t, uow, f.runID)
	if run.LastSeq != 4 {
		t.Fatalf("runs.last_seq: want 4, got %d", run.LastSeq)
	}
}

func TestEvents_AppendWithoutLock_NotPossible(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	// Compile-level guarantee: EventRepository.Append takes a *store.RunLock, and
	// store.RunLock has only unexported fields. There is no way to append an Event
	// without a value produced by RunRepository.LockForUpdate, so "append without a
	// lock" cannot be expressed. The runtime checks below cover the two degenerate
	// values a caller could still construct.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Events().Append(ctx, nil, newEvent(f.runID, domain.EventRunCreated))
		return err
	})
	if err == nil {
		t.Fatal("Append with nil lock: want error, got nil")
	}

	err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Events().Append(ctx, &store.RunLock{}, newEvent(f.runID, domain.EventRunCreated))
		return err
	})
	if err == nil {
		t.Fatal("Append with zero-value lock: want error, got nil")
	}

	if got := listEvents(ctx, t, uow, f.runID); len(got) != 0 {
		t.Fatalf("events after unlocked appends: want 0, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Immutable Definition (invariant #8; 09 §3.4)
// ---------------------------------------------------------------------------

func TestDefinitions_SaveSameVersionTwice_ReturnsVersionConflict(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	def := newDefinition("wf_conflict", 1, "First")
	saveDefinition(ctx, t, uow, def)

	second := newDefinition("wf_conflict", 1, "Second")
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, second)
	})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("second Save of version 1: want domain.ErrVersionConflict, got %v", err)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Save of version 1: want error matching domain.ErrConflict, got %v", err)
	}
}

func TestDefinitions_StoredVersion_IsImmutable(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	original := newDefinition("wf_immutable", 1, "Original")
	saveDefinition(ctx, t, uow, original)

	// store.DefinitionRepository exposes no update method, so the only way to attempt a
	// rewrite is another Save of the same version. It must be rejected and must leave
	// the stored content byte-identical.
	rewrite := newDefinition("wf_immutable", 1, "Rewritten")
	rewrite.Nodes = []domain.Node{{ID: "node_injected", Type: "text_input", Config: json.RawMessage(`{}`)}}
	_ = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, rewrite)
	})

	var stored domain.Definition
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		stored, err = tx.Definitions().GetVersion(ctx, "wf_immutable", 1)
		return err
	}); err != nil {
		t.Fatalf("GetVersion: %v", err)
	}

	if stored.Name != original.Name {
		t.Fatalf("stored name: want %q, got %q", original.Name, stored.Name)
	}
	if len(stored.Nodes) != len(original.Nodes) {
		t.Fatalf("stored nodes: want %d, got %d", len(original.Nodes), len(stored.Nodes))
	}
	if len(stored.Nodes) > 0 && stored.Nodes[0].ID != original.Nodes[0].ID {
		t.Fatalf("stored node id: want %q, got %q", original.Nodes[0].ID, stored.Nodes[0].ID)
	}
}

// ---------------------------------------------------------------------------
// Identity constraints (05 §1.4 and §1.5)
// ---------------------------------------------------------------------------

func TestNodeRuns_DuplicateNodeIDInRun_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	duplicate := newNodeRun("nr_duplicate", f.runID, "node_input")
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Create(ctx, duplicate)
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate (run_id, node_id): want domain.ErrConflict, got %v", err)
	}

	nodeRuns := listNodeRuns(ctx, t, uow, f.runID)
	if len(nodeRuns) != 1 {
		t.Fatalf("NodeRuns in run: want 1, got %d", len(nodeRuns))
	}
}

func TestNodeAttempts_DuplicateAttemptNo_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	first := newNodeAttempt("attempt_1", f.nodeRunID, 1)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().Create(ctx, first)
	}); err != nil {
		t.Fatalf("create attempt 1: %v", err)
	}

	duplicate := newNodeAttempt("attempt_2", f.nodeRunID, 1)
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().Create(ctx, duplicate)
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate (node_run_id, attempt_no): want domain.ErrConflict, got %v", err)
	}

	var attempts []domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempts, err = tx.NodeAttempts().ListByNodeRun(ctx, f.nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("ListByNodeRun: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts preserved: want 1, got %d", len(attempts))
	}
}

// ---------------------------------------------------------------------------
// Migrations and MVP status sets (09 §3.9; 05 §4.2)
// ---------------------------------------------------------------------------

func TestMigrations_ApplyOnFreshDatabase_Succeeds(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OpenUnmigrated(t)

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate on fresh database: %v", err)
	}

	for _, table := range []string{
		"workflows", "workflow_definitions", "assets", "execution_artifacts",
		"runs", "node_runs", "node_attempts", "events",
		"callback_bindings", "pending_callbacks",
		"agent_runs", "agent_turns", "agent_decisions", "agent_actions",
		"tool_attempts", "agent_context_versions", "agent_state_versions",
	} {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                 WHERE table_schema = 'public' AND table_name = $1)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s: want created by migration, got missing", table)
		}
	}
}

func TestMigrations_ApplyTwice_Idempotent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OpenUnmigrated(t)

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate: want no-op, got %v", err)
	}

	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM goose_db_version WHERE version_id > 0`).Scan(&applied); err != nil {
		t.Fatalf("read goose version table: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied migrations after two runs: want 1, got %d", applied)
	}
}

func TestRunStatusCheckConstraint_RejectsPhase2Value(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	// Run CANCELLED, NodeRun SKIPPED and NodeRun CANCELLED are Phase 2. MVP must not
	// reserve them, so the database has to reject them outright.
	_, err := pool.Exec(ctx, `UPDATE runs SET status = 'CANCELLED' WHERE id = $1`, f.runID)
	if err == nil {
		t.Fatal("UPDATE runs SET status = 'CANCELLED': want CHECK violation, got success")
	}

	for _, phase2 := range []string{"SKIPPED", "CANCELLED"} {
		_, err := pool.Exec(ctx, `UPDATE node_runs SET status = $1 WHERE id = $2`, phase2, f.nodeRunID)
		if err == nil {
			t.Fatalf("UPDATE node_runs SET status = %q: want CHECK violation, got success", phase2)
		}
	}
}

// ---------------------------------------------------------------------------
// Reconciler rediscovery (invariant #7; 09 §3.9 "启动扫描发现 READY ... 工作")
// ---------------------------------------------------------------------------

func TestNodeRuns_ListReadyOrRetryable_FindsPersistedWorkAfterRestart(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	now := time.Unix(1_800_000_000, 0).UTC()

	// A RUNNING NodeRun whose retry backoff already elapsed is recoverable work too.
	retryable := newNodeRun("nr_retrying", f.runID, "node_image")
	retryable.Status = domain.NodeRunRunning
	elapsed := now.Add(-time.Minute)
	retryable.NextAttemptAt = &elapsed

	// A NodeRun still inside its backoff window must not be returned yet.
	backoff := newNodeRun("nr_backing_off", f.runID, "node_caption")
	backoff.Status = domain.NodeRunRunning
	future := now.Add(time.Hour)
	backoff.NextAttemptAt = &future

	// A waiting NodeRun is recovered through its Callback Binding, not this scan.
	waiting := newNodeRun("nr_waiting", f.runID, "node_video")
	waiting.Status = domain.NodeRunWaitingCallback

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for _, nr := range []domain.NodeRun{retryable, backoff, waiting} {
			if err := tx.NodeRuns().Create(ctx, nr); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed node runs: %v", err)
	}

	var found []domain.NodeRun
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		found, err = tx.NodeRuns().ListReadyOrRetryable(ctx, now, 50)
		return err
	}); err != nil {
		t.Fatalf("ListReadyOrRetryable: %v", err)
	}

	got := map[string]bool{}
	for _, nr := range found {
		got[nr.ID] = true
	}
	if !got[f.nodeRunID] {
		t.Fatalf("READY NodeRun %s: want rediscovered, got missing", f.nodeRunID)
	}
	if !got["nr_retrying"] {
		t.Fatal("NodeRun with elapsed backoff: want rediscovered, got missing")
	}
	if got["nr_backing_off"] {
		t.Fatal("NodeRun still in backoff: want skipped, got returned")
	}
	if got["nr_waiting"] {
		t.Fatal("WAITING_CALLBACK NodeRun: want skipped, got returned")
	}
}

func TestNodeRuns_TransitionFromWrongState_ReturnsStaleClaim(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	// A late callback for a NodeRun that is still READY must not advance anything.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Transition(ctx, f.nodeRunID,
			domain.NodeRunWaitingCallback, domain.NodeRunSucceeded, time.Now().UTC())
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("Transition from unmatched state: want domain.ErrStaleClaim, got %v", err)
	}

	if nr := getNodeRun(ctx, t, uow, f.nodeRunID); nr.Status != domain.NodeRunReady {
		t.Fatalf("NodeRun status: want unchanged READY, got %s", nr.Status)
	}
}

func TestNodeRuns_TransitionAcrossIllegalEdge_ReturnsInvalidStateTransition(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Transition(ctx, f.nodeRunID,
			domain.NodeRunReady, domain.NodeRunSucceeded, time.Now().UTC())
	})
	var invalid *domain.InvalidStateTransitionError
	if !errors.As(err, &invalid) {
		t.Fatalf("READY -> SUCCEEDED: want *domain.InvalidStateTransitionError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// waitUntilBlockedOnLock returns once at least one backend in this test's database is
// waiting for a lock held by another backend. It polls a database fact and bounds the
// wait; it never sleeps for a guessed duration to let a race "probably" happen.
func waitUntilBlockedOnLock(ctx context.Context, pool *pgxpool.Pool) error {
	const query = `
		SELECT count(*) FROM pg_stat_activity
		 WHERE datname = current_database()
		   AND cardinality(pg_blocking_pids(pid)) > 0`

	deadline := time.Now().Add(blockedWaitTimeout)
	for {
		var blocked int
		if err := pool.QueryRow(ctx, query).Scan(&blocked); err != nil {
			return fmt.Errorf("query blocked backends: %w", err)
		}
		if blocked > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no backend waited for a row lock within %s", blockedWaitTimeout)
		}
		// Poll interval only. The barrier is the observed lock wait, never elapsed time:
		// the loop exits as soon as the condition holds and fails if it never does.
		time.Sleep(blockedPollInterval)
	}
}

const (
	blockedWaitTimeout  = 15 * time.Second
	blockedPollInterval = 2 * time.Millisecond
)

type fixture struct {
	workflowID string
	runID      string
	nodeRunID  string
}

var fixtureTime = time.Unix(1_790_000_000, 0).UTC()

// seedRun commits one Workflow version, one Run and one READY NodeRun.
func seedRun(ctx context.Context, t *testing.T, uow store.UnitOfWork) fixture {
	t.Helper()

	f := fixture{workflowID: "wf_seed", runID: "run_seed", nodeRunID: "nr_seed"}

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.Definitions().Save(ctx, newDefinition(f.workflowID, 1, "Seed")); err != nil {
			return err
		}
		if err := tx.Runs().Create(ctx, domain.Run{
			ID:                f.runID,
			WorkflowID:        f.workflowID,
			DefinitionVersion: 1,
			Status:            domain.RunRunning,
			Input:             json.RawMessage(`{"brief":"a small ember creature"}`),
			LastSeq:           0,
			StartedAt:         fixtureTime,
			UpdatedAt:         fixtureTime,
		}); err != nil {
			return err
		}
		return tx.NodeRuns().Create(ctx, newNodeRun(f.nodeRunID, f.runID, "node_input"))
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return f
}

func newDefinition(workflowID string, version int, name string) domain.Definition {
	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     version,
		Name:        name,
		Description: "integration fixture",
		Nodes: []domain.Node{{
			ID:       "node_input",
			Type:     "text_input",
			Name:     "Brief",
			Position: domain.Position{X: 0, Y: 0},
			Config:   json.RawMessage(`{"inputKey":"brief","required":true}`),
		}},
		Edges:          []domain.Edge{},
		RunInputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{},"required":[]}`),
		Validation: domain.Validation{
			Status:           domain.ValidationValid,
			ValidatorVersion: "mvp-v1",
			ValidatedAt:      fixtureTime,
		},
		CreatedAt: fixtureTime,
	}
}

func newNodeRun(id, runID, nodeID string) domain.NodeRun {
	return domain.NodeRun{
		ID:        id,
		RunID:     runID,
		NodeID:    nodeID,
		NodeType:  "text_input",
		Status:    domain.NodeRunReady,
		Input:     json.RawMessage(`{}`),
		ReadyAt:   fixtureTime,
		UpdatedAt: fixtureTime,
	}
}

func newNodeAttempt(id, nodeRunID string, attemptNo int) domain.NodeAttempt {
	return domain.NodeAttempt{
		ID:        id,
		NodeRunID: nodeRunID,
		AttemptNo: attemptNo,
		Status:    domain.NodeAttemptStarted,
		Input:     json.RawMessage(`{}`),
		StartedAt: fixtureTime,
	}
}

var eventCounter int

func newEvent(runID string, typ domain.EventType) domain.Event {
	eventCounter++
	return domain.Event{
		ID:        fmt.Sprintf("evt_%d", eventCounter),
		RunID:     runID,
		Type:      typ,
		Timestamp: fixtureTime,
		Payload:   json.RawMessage(`{}`),
	}
}

func saveDefinition(ctx context.Context, t *testing.T, uow store.UnitOfWork, def domain.Definition) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	}); err != nil {
		t.Fatalf("save definition %s v%d: %v", def.WorkflowID, def.Version, err)
	}
}

func appendEvent(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID string, typ domain.EventType) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, runID)
		if err != nil {
			return err
		}
		if _, err := tx.Events().Append(ctx, lock, newEvent(runID, typ)); err != nil {
			return err
		}
		return tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), time.Now().UTC())
	}); err != nil {
		t.Fatalf("append event %s: %v", typ, err)
	}
}

func getRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID string) domain.Run {
	t.Helper()
	var run domain.Run
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		run, err = tx.Runs().Get(ctx, runID)
		return err
	}); err != nil {
		t.Fatalf("get run %s: %v", runID, err)
	}
	return run
}

func getNodeRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, nodeRunID string) domain.NodeRun {
	t.Helper()
	var nr domain.NodeRun
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		nr, err = tx.NodeRuns().Get(ctx, nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("get node run %s: %v", nodeRunID, err)
	}
	return nr
}

func listNodeRuns(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID string) []domain.NodeRun {
	t.Helper()
	var out []domain.NodeRun
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.NodeRuns().ListByRun(ctx, runID)
		return err
	}); err != nil {
		t.Fatalf("list node runs of %s: %v", runID, err)
	}
	return out
}

func listEvents(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID string) []domain.Event {
	t.Helper()
	var out []domain.Event
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.Events().ListAfter(ctx, runID, 0, 100)
		return err
	}); err != nil {
		t.Fatalf("list events of %s: %v", runID, err)
	}
	return out
}
