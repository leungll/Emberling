//go:build integration

// Package integration: this file covers the Agent execution facts against a real
// PostgreSQL —
// the frozen Agent Run configuration, the identity constraints that stop a second
// Agent Run, Turn, Action or Tool Attempt from existing, the conditional claims that
// pick one winner between immediate advancement and the Reconciler, and the Agent Run
// recovery pointers.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// Agent Run: frozen configuration and one Agent Run per Agent NodeRun
// ---------------------------------------------------------------------------

func TestAgentRunStore_InsertAndGet_RoundTripsFrozenConfig(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	want := newAgentRun("ar_1", f.nodeRunID)
	createAgentRun(ctx, t, uow, want)

	var byID, byNodeRun domain.AgentRun
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if byID, err = tx.AgentRuns().Get(ctx, want.ID); err != nil {
			return err
		}
		byNodeRun, err = tx.AgentRuns().GetByNodeRunID(ctx, f.nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("read back Agent Run: %v", err)
	}

	if byNodeRun.ID != want.ID {
		t.Fatalf("GetByNodeRunID(%s).ID = %s, want %s", f.nodeRunID, byNodeRun.ID, want.ID)
	}
	if byID.Instructions != want.Instructions || byID.ModelID != want.ModelID {
		t.Fatalf("frozen instructions/model: got %q/%q, want %q/%q",
			byID.Instructions, byID.ModelID, want.Instructions, want.ModelID)
	}
	if len(byID.AllowedTools) != 1 || byID.AllowedTools[0] != "lookup" {
		t.Fatalf("frozen Tool allowlist: got %v, want [lookup]", byID.AllowedTools)
	}
	// PostgreSQL re-serialises JSONB, so the frozen Schemas are compared decoded.
	assertSameJSON(t, "modelConfig", want.ModelConfig, byID.ModelConfig)
	assertSameJSON(t, "contextSchema", want.ContextSchema, byID.ContextSchema)
	assertSameJSON(t, "stateSchema", want.StateSchema, byID.StateSchema)
	assertSameJSON(t, "outputSchema", want.OutputSchema, byID.OutputSchema)
	if byID.MaxTurns != want.MaxTurns || byID.CurrentTurnNo != 1 ||
		byID.CurrentContextVersion != 0 || byID.CurrentStateVersion != 0 {
		t.Fatalf("bounds and pointers: got maxTurns=%d turn=%d ctx=%d state=%d",
			byID.MaxTurns, byID.CurrentTurnNo, byID.CurrentContextVersion, byID.CurrentStateVersion)
	}
	if !byID.StartedAt.Equal(want.StartedAt) || !byID.Deadline.Equal(want.Deadline) {
		t.Fatalf("started_at/deadline: got %s/%s, want %s/%s",
			byID.StartedAt, byID.Deadline, want.StartedAt, want.Deadline)
	}
	if byID.TerminatedAt != nil || byID.Termination != nil || byID.Error != nil {
		t.Fatalf("a running Agent Run must carry no termination, got %v/%v/%v",
			byID.TerminatedAt, byID.Termination, byID.Error)
	}
}

// One Agent NodeRun has at most one Agent Run. The guarantee is a UNIQUE constraint, not
// a process-local check, so a duplicated initialisation transaction loses in the
// database.
func TestAgentRunStore_SecondRunForSameNodeRun_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentRuns().Create(ctx, newAgentRun("ar_2", f.nodeRunID))
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Agent Run for one Agent NodeRun: want domain.ErrConflict, got %v", err)
	}
}

// AdvancePointers is how a Tool result transaction moves the recovery position (after a
// process restart, the Runtime resumes the Agent Loop only from the current pointers and
// persisted work items). A caller working from a stale pointer must not overwrite the
// committed one.
func TestAgentRunStore_UpdatePointers_ConditionalOnCurrentVersion(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))

	from := store.AgentRunPointers{CurrentTurnNo: 1, CurrentContextVersion: 0, CurrentStateVersion: 0}
	to := store.AgentRunPointers{CurrentTurnNo: 2, CurrentContextVersion: 1, CurrentStateVersion: 1}

	advanced := advancePointers(ctx, t, uow, "ar_1", from, to)
	if !advanced {
		t.Fatal("first AdvancePointers: want true (one row affected), got false")
	}

	// A second advancement path still holding the pre-commit pointers affects zero rows.
	stale := advancePointers(ctx, t, uow, "ar_1", from,
		store.AgentRunPointers{CurrentTurnNo: 2, CurrentContextVersion: 9, CurrentStateVersion: 9})
	if stale {
		t.Fatal("stale AdvancePointers: want false (zero rows affected), got true")
	}

	run := getAgentRun(ctx, t, uow, "ar_1")
	if run.CurrentTurnNo != 2 || run.CurrentContextVersion != 1 || run.CurrentStateVersion != 1 {
		t.Fatalf("pointers after the stale update: got turn=%d ctx=%d state=%d, want 2/1/1",
			run.CurrentTurnNo, run.CurrentContextVersion, run.CurrentStateVersion)
	}
}

// Termination is written once: a Tool failure, a timeout and a Final completion can all
// reach the same Agent Run, and only one of them may record why it stopped.
func TestAgentRunStore_Terminate_SecondTermination_ReportsNoClaim(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))

	execErr := domain.ExecutionError{Code: "TOOL_ERROR", Message: "lookup failed"}
	first := terminateAgentRun(ctx, t, uow, "ar_1", domain.TerminationToolError, &execErr)
	if !first {
		t.Fatal("first Terminate: want true, got false")
	}

	second := terminateAgentRun(ctx, t, uow, "ar_1", domain.TerminationTimeout, nil)
	if second {
		t.Fatal("second Terminate: want false (already terminated), got true")
	}

	run := getAgentRun(ctx, t, uow, "ar_1")
	if run.Termination == nil || *run.Termination != domain.TerminationToolError {
		t.Fatalf("termination after the losing timeout: got %v, want TOOL_ERROR", run.Termination)
	}
	if run.Termination.IsSuccess() {
		t.Fatal("TOOL_ERROR.IsSuccess() = true, want false")
	}
	if run.TerminatedAt == nil {
		t.Fatal("terminated_at: want set by the winning Terminate, got nil")
	}
	if run.Error == nil || run.Error.Code != execErr.Code {
		t.Fatalf("error after termination: got %v, want %v", run.Error, execErr)
	}
}

// TestStoreAgentRuns_ListExpired_ReturnsOnlyUnterminatedPastDeadline covers the Agent
// deadline row of the Reconciler scan table: the timeout use case may only be
// handed Agent Runs whose deadline has passed and that nothing has terminated yet. A
// terminated Agent Run is a committed outcome, and an Agent Run still inside its deadline
// is live work; listing either one would let recovery terminate a Run it has no authority
// over.
func TestStoreAgentRuns_ListExpired_ReturnsOnlyUnterminatedPastDeadline(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.NodeRuns().Create(ctx, newNodeRun("nr_expired_2", f.runID, "node_input_2")); err != nil {
			return err
		}
		return tx.NodeRuns().Create(ctx, newNodeRun("nr_expired_3", f.runID, "node_input_3"))
	}); err != nil {
		t.Fatalf("seed extra node runs: %v", err)
	}

	expired := newAgentRun("ar_expired", f.nodeRunID)
	expired.Deadline = fixtureTime.Add(time.Minute)

	terminated := newAgentRun("ar_expired_terminated", "nr_expired_2")
	terminated.Deadline = fixtureTime.Add(time.Minute)

	live := newAgentRun("ar_live", "nr_expired_3")
	live.Deadline = fixtureTime.Add(time.Hour)

	for _, run := range []domain.AgentRun{expired, terminated, live} {
		createAgentRun(ctx, t, uow, run)
	}
	if won := terminateAgentRun(ctx, t, uow, terminated.ID, domain.TerminationToolError, nil); !won {
		t.Fatal("terminate the already-finished Agent Run: want true")
	}

	now := fixtureTime.Add(30 * time.Minute)
	got := listExpiredAgentRuns(ctx, t, uow, now, 10)
	if len(got) != 1 || got[0].ID != expired.ID {
		ids := make([]string, len(got))
		for i, run := range got {
			ids[i] = run.ID
		}
		t.Fatalf("ListExpired = %v, want exactly [%s]: a terminated Agent Run is a committed outcome and a live one is not due", ids, expired.ID)
	}

	// The scan is bounded like every other Reconciler scan.
	if bounded := listExpiredAgentRuns(ctx, t, uow, now, 1); len(bounded) != 1 {
		t.Fatalf("ListExpired with limit 1 returned %d rows, want 1", len(bounded))
	}
	if before := listExpiredAgentRuns(ctx, t, uow, expired.Deadline.Add(-time.Second), 10); len(before) != 0 {
		t.Fatalf("ListExpired one second before the deadline = %d rows, want 0", len(before))
	}
	if atDeadline := listExpiredAgentRuns(ctx, t, uow, expired.Deadline, 10); len(atDeadline) != 1 {
		t.Fatalf("ListExpired exactly at the deadline = %d rows, want 1 (deadline <= now)", len(atDeadline))
	}
}

func listExpiredAgentRuns(ctx context.Context, t *testing.T, uow store.UnitOfWork, before time.Time, limit int) []domain.AgentRun {
	t.Helper()
	var runs []domain.AgentRun
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		runs, err = tx.AgentRuns().ListExpired(ctx, before, limit)
		return err
	}); err != nil {
		t.Fatalf("list expired Agent Runs: %v", err)
	}
	return runs
}

// ---------------------------------------------------------------------------
// Turn: conditional READY -> RUNNING claim, one Turn per turn_no
// ---------------------------------------------------------------------------

// Immediate advancement and the Reconciler race for the same READY Turn; only the
// transaction whose UPDATE affects one row may call the model (an immediate advancement or
// Reconciler pass whose UPDATE affects zero rows must stop and must not call the model
// again).
func TestAgentTurnStore_ClaimReady_TwoClaimersOneWins(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_1", "ar_1", 1))

	const claimants = 2
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(claimants)

	results := make([]bool, claimants)
	failures := make([]error, claimants)
	claimedAt := fixtureTime.Add(time.Minute)

	for i := range claimants {
		go func() {
			defer done.Done()
			// Barrier: both claimants are parked here until the test releases them, so
			// the race is deterministic without sleeping.
			start.Wait()

			failures[i] = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
				if _, err := tx.Runs().LockForUpdate(ctx, f.runID); err != nil {
					return err
				}
				won, err := tx.AgentTurns().ClaimReady(ctx, "turn_1", claimedAt)
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

	turn := getAgentTurn(ctx, t, uow, "turn_1")
	if turn.Status != domain.AgentTurnRunning {
		t.Fatalf("Turn status after the claim: want RUNNING, got %s", turn.Status)
	}
	if turn.StartedAt == nil || !turn.StartedAt.Equal(claimedAt) {
		t.Fatalf("Turn started_at after the claim: want %s, got %v", claimedAt, turn.StartedAt)
	}

	// A claimed Turn is no longer recoverable work for the Reconciler.
	if ready := listReadyTurns(ctx, t, uow); len(ready) != 0 {
		t.Fatalf("ListReady after the claim: want 0 Turns, got %d", len(ready))
	}
}

func TestAgentTurnStore_DuplicateTurnNo_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_1", "ar_1", 1))

	// UNIQUE (agent_run_id, turn_no) is what stops immediate advancement, a callback and
	// the Reconciler from producing a duplicate round.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentTurns().Create(ctx, newAgentTurn("turn_1_dup", "ar_1", 1))
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate turn_no: want domain.ErrConflict, got %v", err)
	}

	ready := listReadyTurns(ctx, t, uow)
	if len(ready) != 1 || ready[0].ID != "turn_1" {
		t.Fatalf("ListReady after the rejected duplicate: want [turn_1], got %+v", ready)
	}
}

// ListByAgentRunID is the read the Agent Trace projection expands one Agent Run's rounds
// from, expanded in persisted order. It must return the Turns of
// that Agent Run only, ordered by turn_no, whatever order the rows were inserted in --
// the Reconciler and immediate advancement create rounds from different processes, so
// insertion order is not the Agent Loop's order.
func TestAgentTurnStore_ListByAgentRunID_OrdersByTurnNoAndScopesToOneAgentRun(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	// A second Agent NodeRun in the same Run, so "scoped to one Agent Run" is proved
	// against a database that really holds another Agent Run's Turns.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Create(ctx, newNodeRun("nr_agent_2", f.runID, "node_agent_2"))
	}); err != nil {
		t.Fatalf("seed a second Agent NodeRun: %v", err)
	}
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))
	createAgentRun(ctx, t, uow, newAgentRun("ar_2", "nr_agent_2"))

	// Inserted out of turn_no order on purpose.
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_3", "ar_1", 3))
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_1", "ar_1", 1))
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_2", "ar_1", 2))
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_other", "ar_2", 1))

	turns := listTurnsOfAgentRun(ctx, t, uow, "ar_1")
	if len(turns) != 3 {
		t.Fatalf("ListByAgentRunID(ar_1) returned %d Turns, want 3: %+v", len(turns), turns)
	}
	for i, want := range []struct {
		id     string
		turnNo int
	}{{"turn_1", 1}, {"turn_2", 2}, {"turn_3", 3}} {
		if turns[i].ID != want.id || turns[i].TurnNo != want.turnNo {
			t.Errorf("ListByAgentRunID(ar_1)[%d] = %s (turn_no %d), want %s (turn_no %d)",
				i, turns[i].ID, turns[i].TurnNo, want.id, want.turnNo)
		}
		if turns[i].AgentRunID != "ar_1" {
			t.Errorf("ListByAgentRunID(ar_1)[%d].AgentRunID = %s, want ar_1: another Agent Run's round leaked into the projection",
				i, turns[i].AgentRunID)
		}
	}

	// A committed Turn round-trips through the list read, not only through Get: the
	// projection reads status and timestamps from these rows.
	if turns[0].Status != domain.AgentTurnReady {
		t.Errorf("ListByAgentRunID(ar_1)[0].Status = %s, want READY", turns[0].Status)
	}

	// An Agent Run whose first Turn has not been created yet (or that has none at all) is
	// an empty list, not an error: the Trace projection shows what was persisted.
	if got := listTurnsOfAgentRun(ctx, t, uow, "ar_missing"); len(got) != 0 {
		t.Errorf("ListByAgentRunID(unknown agent run) = %+v, want no Turns", got)
	}
}

func listTurnsOfAgentRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string) []domain.AgentTurn {
	t.Helper()
	var turns []domain.AgentTurn
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		turns, err = tx.AgentTurns().ListByAgentRunID(ctx, agentRunID)
		return err
	}); err != nil {
		t.Fatalf("list Turns of Agent Run %s: %v", agentRunID, err)
	}
	return turns
}

func TestAgentTurnStore_MarkCompleted_NotRunning_StaleClaim(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_1", "ar_1", 1))

	// The model result transaction may only complete the Turn it actually claimed.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentTurns().MarkCompleted(ctx, "turn_1", fixtureTime,
			json.RawMessage(`{"kind":"FINAL"}`), &domain.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5})
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("MarkCompleted on a READY Turn: want domain.ErrStaleClaim, got %v", err)
	}

	claimTurn(ctx, t, uow, f.runID, "turn_1")
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentTurns().MarkCompleted(ctx, "turn_1", fixtureTime,
			json.RawMessage(`{"kind":"FINAL"}`), &domain.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5})
	}); err != nil {
		t.Fatalf("MarkCompleted on the claimed Turn: %v", err)
	}

	turn := getAgentTurn(ctx, t, uow, "turn_1")
	if turn.Status != domain.AgentTurnCompleted {
		t.Fatalf("Turn status: want COMPLETED, got %s", turn.Status)
	}
	if turn.TokenUsage == nil || turn.TokenUsage.TotalTokens != 5 {
		t.Fatalf("Turn token usage: want 5 total, got %v", turn.TokenUsage)
	}
	if turn.CompletedAt == nil {
		t.Fatal("Turn completed_at: want set, got nil")
	}
	assertSameJSON(t, "response", json.RawMessage(`{"kind":"FINAL"}`), turn.Response)
}

// ---------------------------------------------------------------------------
// Decision and Action: one Decision and one Action per Turn
// ---------------------------------------------------------------------------

func TestAgentActionStore_ClaimReady_ZeroRowsWhenNotReady(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")

	first := claimAction(ctx, t, uow, f.runID, "action_1")
	if !first {
		t.Fatal("first ClaimReady: want true (one row affected), got false")
	}

	// The Action is RUNNING now: the Reconciler's claim must affect zero rows rather
	// than dispatch the Tool a second time.
	second := claimAction(ctx, t, uow, f.runID, "action_1")
	if second {
		t.Fatal("second ClaimReady on a RUNNING Action: want false, got true")
	}

	action := getAgentAction(ctx, t, uow, "action_1")
	if action.Status != domain.AgentActionRunning {
		t.Fatalf("Action status: want RUNNING, got %s", action.Status)
	}
	if action.StartedAt == nil {
		t.Fatal("Action started_at: want stamped by the winning claim, got nil")
	}
	if ready := listReadyActions(ctx, t, uow); len(ready) != 0 {
		t.Fatalf("ListReady after the claim: want 0 Actions, got %d", len(ready))
	}
}

func TestAgentActionStore_SecondActionForSameTurn_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")

	// Each Turn has at most one Decision and one Action, so a second
	// advancement path cannot create a competing work item for the same Turn.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().Create(ctx, newAgentAction("action_2", "turn_1", "decision_1"))
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Action for one Turn: want domain.ErrConflict, got %v", err)
	}

	var byTurn domain.AgentAction
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		byTurn, err = tx.AgentActions().GetByTurnID(ctx, "turn_1")
		return err
	}); err != nil {
		t.Fatalf("GetByTurnID: %v", err)
	}
	if byTurn.ID != "action_1" || byTurn.DecisionID != "decision_1" {
		t.Fatalf("GetByTurnID(turn_1) = %s/%s, want action_1/decision_1", byTurn.ID, byTurn.DecisionID)
	}
}

func TestAgentActionStore_MarkSucceeded_NotRunning_StaleClaim(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")

	// A completion path that never held the Action's execution right must lose.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkSucceeded(ctx, "action_1", domain.AgentActionRunning, fixtureTime)
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("MarkSucceeded on a READY Action: want domain.ErrStaleClaim, got %v", err)
	}

	if !claimAction(ctx, t, uow, f.runID, "action_1") {
		t.Fatal("ClaimReady: want true, got false")
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkSucceeded(ctx, "action_1", domain.AgentActionRunning, fixtureTime)
	}); err != nil {
		t.Fatalf("MarkSucceeded on the claimed Action: %v", err)
	}

	action := getAgentAction(ctx, t, uow, "action_1")
	if action.Status != domain.AgentActionSucceeded || action.CompletedAt == nil {
		t.Fatalf("Action after completion: status=%s completed_at=%v", action.Status, action.CompletedAt)
	}
}

// ---------------------------------------------------------------------------
// Tool Attempt: one Attempt per attempt_no, result written once
// ---------------------------------------------------------------------------

func TestToolAttemptStore_DuplicateAttemptNo_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")
	createToolAttempt(ctx, t, uow, newToolAttempt("tool_attempt_1", "action_1", 1))

	// UNIQUE (action_id, attempt_no): a duplicated dispatch transaction cannot record a
	// second call under the same Attempt number.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().Create(ctx, newToolAttempt("tool_attempt_dup", "action_1", 1))
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate attempt_no: want domain.ErrConflict, got %v", err)
	}

	var attempts []domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempts, err = tx.ToolAttempts().ListByActionID(ctx, "action_1")
		return err
	}); err != nil {
		t.Fatalf("ListByActionID: %v", err)
	}
	if len(attempts) != 1 || attempts[0].ID != "tool_attempt_1" {
		t.Fatalf("Tool Attempts of action_1: want [tool_attempt_1], got %+v", attempts)
	}
	if attempts[0].CallbackTokenHash != nil {
		t.Fatal("a sync Tool Attempt must carry no callback token hash")
	}
}

func TestToolAttemptStore_MarkSucceeded_SecondWrite_StaleClaim(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")
	createToolAttempt(ctx, t, uow, newToolAttempt("tool_attempt_1", "action_1", 1))

	result := json.RawMessage(`{"answer":"ember"}`)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkSucceeded(ctx, "tool_attempt_1", domain.ToolAttemptStarted, fixtureTime, result)
	}); err != nil {
		t.Fatalf("first MarkSucceeded: %v", err)
	}

	// A duplicated Tool result transaction must not overwrite the committed result.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkFailed(ctx, "tool_attempt_1", domain.ToolAttemptStarted, fixtureTime,
			domain.ExecutionError{Code: "TOOL_ERROR", Message: "late failure"})
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("MarkFailed after success: want domain.ErrStaleClaim, got %v", err)
	}

	var attempt domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempt, err = tx.ToolAttempts().Get(ctx, "tool_attempt_1")
		return err
	}); err != nil {
		t.Fatalf("Get Tool Attempt: %v", err)
	}
	if attempt.Status != domain.ToolAttemptSucceeded || attempt.Error != nil {
		t.Fatalf("Tool Attempt after the losing failure: status=%s error=%v", attempt.Status, attempt.Error)
	}
	assertSameJSON(t, "tool result", result, attempt.Result)
}

func TestToolAttemptStore_CallbackResolvesDispatchedAttempt_LateWriteStale(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")
	createToolAttempt(ctx, t, uow, newToolAttempt("tool_attempt_1", "action_1", 1))
	createToolAttempt(ctx, t, uow, newToolAttempt("tool_attempt_2", "action_1", 2))

	// A callback path expecting DISPATCHED loses against an Attempt that is still
	// STARTED: its dispatch has not committed, so there is nothing to resume yet.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkSucceeded(ctx, "tool_attempt_1", domain.ToolAttemptDispatched, fixtureTime,
			json.RawMessage(`{"answer":"early"}`))
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("MarkSucceeded(from DISPATCHED) on a STARTED Attempt: want domain.ErrStaleClaim, got %v", err)
	}

	for _, id := range []string{"tool_attempt_1", "tool_attempt_2"} {
		if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			return tx.ToolAttempts().MarkDispatched(ctx, id, fixtureTime)
		}); err != nil {
			t.Fatalf("MarkDispatched %s: %v", id, err)
		}
	}

	result := json.RawMessage(`{"answer":"ember"}`)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkSucceeded(ctx, "tool_attempt_1", domain.ToolAttemptDispatched, fixtureTime, result)
	}); err != nil {
		t.Fatalf("MarkSucceeded(from DISPATCHED): %v", err)
	}
	callbackFailure := domain.ExecutionError{Code: "REMOTE_LOOKUP_FAILED", Message: "provider failed"}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkFailed(ctx, "tool_attempt_2", domain.ToolAttemptDispatched, fixtureTime, callbackFailure)
	}); err != nil {
		t.Fatalf("MarkFailed(from DISPATCHED): %v", err)
	}

	// A duplicated or late callback finds the row no longer DISPATCHED and loses.
	err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkFailed(ctx, "tool_attempt_1", domain.ToolAttemptDispatched, fixtureTime,
			domain.ExecutionError{Code: "TOOL_ERROR", Message: "late failure"})
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("late MarkFailed after callback success: want domain.ErrStaleClaim, got %v", err)
	}
	err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkSucceeded(ctx, "tool_attempt_2", domain.ToolAttemptDispatched, fixtureTime, result)
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("late MarkSucceeded after callback failure: want domain.ErrStaleClaim, got %v", err)
	}

	// A from-status that cannot reach the target is a programming error, not a lost race.
	var invalid *domain.InvalidStateTransitionError
	err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkSucceeded(ctx, "tool_attempt_1", domain.ToolAttemptSucceeded, fixtureTime, result)
	})
	if !errors.As(err, &invalid) {
		t.Fatalf("MarkSucceeded(from SUCCEEDED): want InvalidStateTransitionError, got %v", err)
	}

	var first, second domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if first, err = tx.ToolAttempts().Get(ctx, "tool_attempt_1"); err != nil {
			return err
		}
		second, err = tx.ToolAttempts().Get(ctx, "tool_attempt_2")
		return err
	}); err != nil {
		t.Fatalf("Get Tool Attempts: %v", err)
	}
	if first.Status != domain.ToolAttemptSucceeded || first.Error != nil || first.CompletedAt == nil {
		t.Fatalf("tool_attempt_1: status=%s error=%v completed_at=%v", first.Status, first.Error, first.CompletedAt)
	}
	assertSameJSON(t, "callback result", result, first.Result)
	if second.Status != domain.ToolAttemptFailed || second.Error == nil ||
		second.Error.Code != callbackFailure.Code || second.Result != nil {
		t.Fatalf("tool_attempt_2: status=%s error=%v result=%s", second.Status, second.Error, second.Result)
	}
}

func TestAgentActionStore_WaitingActionResolvesOnce_LateWriteStale(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")
	// A second decided Turn of the same Agent Run gives a second Action to fail.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.AgentTurns().Create(ctx, newAgentTurn("turn_2", "ar_1", 2)); err != nil {
			return err
		}
		if _, err := tx.AgentTurns().ClaimReady(ctx, "turn_2", fixtureTime); err != nil {
			return err
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, "turn_2", fixtureTime,
			json.RawMessage(`{"kind":"TOOL_CALL"}`), nil); err != nil {
			return err
		}
		if err := tx.AgentDecisions().Create(ctx, newAgentDecision("decision_2", "turn_2")); err != nil {
			return err
		}
		return tx.AgentActions().Create(ctx, newAgentAction("action_2", "turn_2", "decision_2"))
	}); err != nil {
		t.Fatalf("seed second decided Turn: %v", err)
	}

	for _, id := range []string{"action_1", "action_2"} {
		if !claimAction(ctx, t, uow, f.runID, id) {
			t.Fatalf("ClaimReady %s: want true, got false", id)
		}
	}

	// A RUNNING Action has not started waiting; a callback path expecting
	// WAITING_CALLBACK must lose rather than complete a synchronous call's Action.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkSucceeded(ctx, "action_1", domain.AgentActionWaitingCallback, fixtureTime)
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("MarkSucceeded(from WAITING_CALLBACK) on a RUNNING Action: want domain.ErrStaleClaim, got %v", err)
	}

	for _, id := range []string{"action_1", "action_2"} {
		if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			return tx.AgentActions().MarkWaiting(ctx, id, fixtureTime)
		}); err != nil {
			t.Fatalf("MarkWaiting %s: %v", id, err)
		}
	}

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkSucceeded(ctx, "action_1", domain.AgentActionWaitingCallback, fixtureTime)
	}); err != nil {
		t.Fatalf("MarkSucceeded(from WAITING_CALLBACK): %v", err)
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkFailed(ctx, "action_2", domain.AgentActionWaitingCallback, fixtureTime,
			domain.ExecutionError{Code: "REMOTE_LOOKUP_FAILED", Message: "provider failed"})
	}); err != nil {
		t.Fatalf("MarkFailed(from WAITING_CALLBACK): %v", err)
	}

	// A late callback finds the Action already resolved and loses.
	err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkFailed(ctx, "action_1", domain.AgentActionWaitingCallback, fixtureTime,
			domain.ExecutionError{Code: "TOOL_ERROR", Message: "late failure"})
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("late MarkFailed after success: want domain.ErrStaleClaim, got %v", err)
	}

	var invalid *domain.InvalidStateTransitionError
	err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkSucceeded(ctx, "action_2", domain.AgentActionReady, fixtureTime)
	})
	if !errors.As(err, &invalid) {
		t.Fatalf("MarkSucceeded(from READY): want InvalidStateTransitionError, got %v", err)
	}

	if a := getAgentAction(ctx, t, uow, "action_1"); a.Status != domain.AgentActionSucceeded || a.Error != nil {
		t.Fatalf("action_1: status=%s error=%v", a.Status, a.Error)
	}
	if a := getAgentAction(ctx, t, uow, "action_2"); a.Status != domain.AgentActionFailed || a.Error == nil {
		t.Fatalf("action_2: status=%s error=%v", a.Status, a.Error)
	}
}

// TestAgentActionStore_MarkFailedFromReady_RejectedAsTimeoutOnly covers that failing an
// Action that no executor ever claimed is the Agent timeout's transition alone
// (MarkTimedOut). MarkFailed refuses READY as its expected state before any SQL runs, and
// the READY row is left untouched.
func TestAgentActionStore_MarkFailedFromReady_RejectedAsTimeoutOnly(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")

	var invalid *domain.InvalidStateTransitionError
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkFailed(ctx, "action_1", domain.AgentActionReady, fixtureTime,
			domain.ExecutionError{Code: "TOOL_ERROR", Message: "not a timeout"})
	})
	if !errors.As(err, &invalid) {
		t.Fatalf("MarkFailed(from READY): want InvalidStateTransitionError, got %v", err)
	}
	if a := getAgentAction(ctx, t, uow, "action_1"); a.Status != domain.AgentActionReady || a.Error != nil || a.CompletedAt != nil {
		t.Fatalf("action_1 after a refused MarkFailed: status=%s error=%v completedAt=%v, want untouched READY", a.Status, a.Error, a.CompletedAt)
	}
}

// ---------------------------------------------------------------------------
// Context and State versions: immutable chains keyed by (agent_run_id, version)
// ---------------------------------------------------------------------------

func TestAgentContextVersionStore_DuplicateVersion_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))

	messages := json.RawMessage(`[{"role":"user","content":"summarise the brief"}]`)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentContextVersions().Create(ctx, domain.AgentContextVersion{
			ID: "ctxv_0", AgentRunID: "ar_1", Version: 0, Messages: messages, CreatedAt: fixtureTime,
		})
	}); err != nil {
		t.Fatalf("create Context Version 0: %v", err)
	}

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentContextVersions().Create(ctx, domain.AgentContextVersion{
			ID: "ctxv_0_dup", AgentRunID: "ar_1", Version: 0,
			Messages: json.RawMessage(`[]`), CreatedAt: fixtureTime,
		})
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate Context Version: want domain.ErrConflict, got %v", err)
	}

	var stored domain.AgentContextVersion
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		stored, err = tx.AgentContextVersions().GetByRunAndVersion(ctx, "ar_1", 0)
		return err
	}); err != nil {
		t.Fatalf("GetByRunAndVersion: %v", err)
	}
	if stored.ID != "ctxv_0" || stored.SourceTurnID != nil {
		t.Fatalf("Context Version 0: got id=%s sourceTurn=%v, want ctxv_0 with no source Turn",
			stored.ID, stored.SourceTurnID)
	}
	assertSameJSON(t, "context messages", messages, stored.Messages)
}

func TestAgentStateVersionStore_DuplicateVersion_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))

	// State Version 0 is fixed to the empty object.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentStateVersions().Create(ctx, domain.AgentStateVersion{
			ID: "statev_0", AgentRunID: "ar_1", Version: 0,
			Value: json.RawMessage(`{}`), CreatedAt: fixtureTime,
		})
	}); err != nil {
		t.Fatalf("create State Version 0: %v", err)
	}

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentStateVersions().Create(ctx, domain.AgentStateVersion{
			ID: "statev_0_dup", AgentRunID: "ar_1", Version: 0,
			Value: json.RawMessage(`{"seen":1}`), CreatedAt: fixtureTime,
		})
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate State Version: want domain.ErrConflict, got %v", err)
	}

	var stored domain.AgentStateVersion
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		stored, err = tx.AgentStateVersions().GetByRunAndVersion(ctx, "ar_1", 0)
		return err
	}); err != nil {
		t.Fatalf("GetByRunAndVersion: %v", err)
	}
	if stored.ID != "statev_0" {
		t.Fatalf("State Version 0: got id=%s, want statev_0", stored.ID)
	}
	assertSameJSON(t, "state value", json.RawMessage(`{}`), stored.Value)
}

// ---------------------------------------------------------------------------
// Fixtures and helpers
// ---------------------------------------------------------------------------

func newAgentRun(id, nodeRunID string) domain.AgentRun {
	return domain.AgentRun{
		ID:                    id,
		NodeRunID:             nodeRunID,
		Instructions:          "Use the lookup Tool before answering.",
		ModelID:               "mock/deterministic-1",
		ModelConfig:           json.RawMessage(`{"temperature":0}`),
		AllowedTools:          []string{"lookup"},
		ContextSchema:         json.RawMessage(`{"type":"array"}`),
		StateSchema:           json.RawMessage(`{"type":"object"}`),
		OutputSchema:          json.RawMessage(`{"type":"string"}`),
		MaxTurns:              4,
		CurrentTurnNo:         1,
		CurrentContextVersion: 0,
		CurrentStateVersion:   0,
		StartedAt:             fixtureTime,
		Deadline:              fixtureTime.Add(10 * time.Minute),
	}
}

func newAgentTurn(id, agentRunID string, turnNo int) domain.AgentTurn {
	return domain.AgentTurn{
		ID:         id,
		AgentRunID: agentRunID,
		TurnNo:     turnNo,
		Status:     domain.AgentTurnReady,
		Request:    json.RawMessage(`{"messages":[{"role":"user","content":"summarise the brief"}]}`),
	}
}

func newAgentDecision(id, turnID string) domain.AgentDecision {
	toolName := "lookup"
	return domain.AgentDecision{
		ID:              id,
		TurnID:          turnID,
		Kind:            domain.DecisionToolCall,
		ToolName:        &toolName,
		Arguments:       json.RawMessage(`{"q":"ember"}`),
		ResponseSummary: json.RawMessage(`{"finishReason":"tool_call"}`),
		CreatedAt:       fixtureTime,
	}
}

func newAgentAction(id, turnID, decisionID string) domain.AgentAction {
	return domain.AgentAction{
		ID:         id,
		TurnID:     turnID,
		DecisionID: decisionID,
		Type:       domain.AgentActionToolCall,
		Status:     domain.AgentActionReady,
		CreatedAt:  fixtureTime,
	}
}

func newToolAttempt(id, actionID string, attemptNo int) domain.ToolAttempt {
	return domain.ToolAttempt{
		ID:        id,
		ActionID:  actionID,
		AttemptNo: attemptNo,
		ToolName:  "lookup",
		Status:    domain.ToolAttemptStarted,
		Input:     json.RawMessage(`{"q":"ember"}`),
		StartedAt: fixtureTime,
	}
}

// seedDecidedTurn commits the state a Tool or Final Action starts from: an Agent Run, a
// COMPLETED Turn, its immutable Decision and the single READY Action.
func seedDecidedTurn(ctx context.Context, t *testing.T, uow store.UnitOfWork, f fixture, agentRunID, turnID, decisionID, actionID string) {
	t.Helper()

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.AgentRuns().Create(ctx, newAgentRun(agentRunID, f.nodeRunID)); err != nil {
			return err
		}
		if err := tx.AgentTurns().Create(ctx, newAgentTurn(turnID, agentRunID, 1)); err != nil {
			return err
		}
		if _, err := tx.AgentTurns().ClaimReady(ctx, turnID, fixtureTime); err != nil {
			return err
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, turnID, fixtureTime,
			json.RawMessage(`{"kind":"TOOL_CALL"}`), nil); err != nil {
			return err
		}
		if err := tx.AgentDecisions().Create(ctx, newAgentDecision(decisionID, turnID)); err != nil {
			return err
		}
		return tx.AgentActions().Create(ctx, newAgentAction(actionID, turnID, decisionID))
	}); err != nil {
		t.Fatalf("seed decided Turn: %v", err)
	}
}

func createAgentRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, run domain.AgentRun) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentRuns().Create(ctx, run)
	}); err != nil {
		t.Fatalf("create Agent Run %s: %v", run.ID, err)
	}
}

func createAgentTurn(ctx context.Context, t *testing.T, uow store.UnitOfWork, turn domain.AgentTurn) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentTurns().Create(ctx, turn)
	}); err != nil {
		t.Fatalf("create Agent Turn %s: %v", turn.ID, err)
	}
}

func createToolAttempt(ctx context.Context, t *testing.T, uow store.UnitOfWork, attempt domain.ToolAttempt) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().Create(ctx, attempt)
	}); err != nil {
		t.Fatalf("create Tool Attempt %s: %v", attempt.ID, err)
	}
}

func getAgentRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string) domain.AgentRun {
	t.Helper()
	var run domain.AgentRun
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		run, err = tx.AgentRuns().Get(ctx, agentRunID)
		return err
	}); err != nil {
		t.Fatalf("get Agent Run %s: %v", agentRunID, err)
	}
	return run
}

func getAgentTurn(ctx context.Context, t *testing.T, uow store.UnitOfWork, turnID string) domain.AgentTurn {
	t.Helper()
	var turn domain.AgentTurn
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		turn, err = tx.AgentTurns().Get(ctx, turnID)
		return err
	}); err != nil {
		t.Fatalf("get Agent Turn %s: %v", turnID, err)
	}
	return turn
}

func getAgentAction(ctx context.Context, t *testing.T, uow store.UnitOfWork, actionID string) domain.AgentAction {
	t.Helper()
	var action domain.AgentAction
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		action, err = tx.AgentActions().Get(ctx, actionID)
		return err
	}); err != nil {
		t.Fatalf("get Agent Action %s: %v", actionID, err)
	}
	return action
}

func claimTurn(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, turnID string) bool {
	t.Helper()
	var won bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.Runs().LockForUpdate(ctx, runID); err != nil {
			return err
		}
		var err error
		won, err = tx.AgentTurns().ClaimReady(ctx, turnID, fixtureTime)
		return err
	}); err != nil {
		t.Fatalf("claim Turn %s: %v", turnID, err)
	}
	return won
}

func claimAction(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, actionID string) bool {
	t.Helper()
	var won bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.Runs().LockForUpdate(ctx, runID); err != nil {
			return err
		}
		var err error
		won, err = tx.AgentActions().ClaimReady(ctx, actionID, fixtureTime)
		return err
	}); err != nil {
		t.Fatalf("claim Action %s: %v", actionID, err)
	}
	return won
}

func advancePointers(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, from, to store.AgentRunPointers) bool {
	t.Helper()
	var advanced bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		advanced, err = tx.AgentRuns().AdvancePointers(ctx, agentRunID, from, to)
		return err
	}); err != nil {
		t.Fatalf("advance pointers of %s: %v", agentRunID, err)
	}
	return advanced
}

func terminateAgentRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, termination domain.AgentTermination, execErr *domain.ExecutionError) bool {
	t.Helper()
	var terminated bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		terminated, err = tx.AgentRuns().Terminate(ctx, agentRunID, termination, fixtureTime, execErr)
		return err
	}); err != nil {
		t.Fatalf("terminate Agent Run %s: %v", agentRunID, err)
	}
	return terminated
}

func listReadyTurns(ctx context.Context, t *testing.T, uow store.UnitOfWork) []domain.AgentTurn {
	t.Helper()
	var turns []domain.AgentTurn
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		turns, err = tx.AgentTurns().ListReady(ctx, 10)
		return err
	}); err != nil {
		t.Fatalf("list READY Turns: %v", err)
	}
	return turns
}

func listReadyActions(ctx context.Context, t *testing.T, uow store.UnitOfWork) []domain.AgentAction {
	t.Helper()
	var actions []domain.AgentAction
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		actions, err = tx.AgentActions().ListReady(ctx, 10)
		return err
	}); err != nil {
		t.Fatalf("list READY Actions: %v", err)
	}
	return actions
}

// assertSameJSON compares two JSON documents by value: PostgreSQL re-serialises JSONB, so
// raw bytes differ even when the stored logical value is identical.
func assertSameJSON(t *testing.T, label string, want, got json.RawMessage) {
	t.Helper()
	var wantValue, gotValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("unmarshal expected %s: %v", label, err)
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal stored %s: %v", label, err)
	}
	wantEncoded, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatalf("re-encode expected %s: %v", label, err)
	}
	gotEncoded, err := json.Marshal(gotValue)
	if err != nil {
		t.Fatalf("re-encode stored %s: %v", label, err)
	}
	if string(wantEncoded) != string(gotEncoded) {
		t.Fatalf("%s: want %s, got %s", label, wantEncoded, gotEncoded)
	}
}
