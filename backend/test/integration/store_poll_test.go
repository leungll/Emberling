//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/migrations"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// Provider polling scheduling facts (next_poll_at / poll_count). A poll claim is a
// conditional update on the Attempt row and writes no Event; persisted due polls are
// rediscovered from PostgreSQL rather than from an in-process timer.
// ---------------------------------------------------------------------------

const pollInterval = 5 * time.Second

func TestNodeAttempts_MarkDispatched_WithFirstPollAt_WritesNextPollAt(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "polled", &first)

	attempt := getAttempt(ctx, t, uow, attemptID)
	if attempt.Status != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status: want DISPATCHED, got %s", attempt.Status)
	}
	if attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(first) {
		t.Fatalf("next_poll_at: want %v, got %v", first, attempt.NextPollAt)
	}
	if attempt.PollCount != 0 {
		t.Fatalf("poll_count: want 0, got %d", attempt.PollCount)
	}
}

func TestNodeAttempts_MarkDispatched_WithoutPollPolicy_LeavesNextPollAtNull(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "unpolled", nil)

	attempt := getAttempt(ctx, t, uow, attemptID)
	if attempt.Status != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status: want DISPATCHED, got %s", attempt.Status)
	}
	if attempt.NextPollAt != nil {
		t.Fatalf("next_poll_at: want NULL, got %v", attempt.NextPollAt)
	}
	if attempt.PollCount != 0 {
		t.Fatalf("poll_count: want 0, got %d", attempt.PollCount)
	}
}

func TestNodeAttempts_ClaimPoll_NotYetDue_ClaimsNothing(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "early", &first)

	if claimed := claimPoll(ctx, t, uow, attemptID, first.Add(-time.Millisecond), 3); claimed {
		t.Fatal("ClaimPoll before next_poll_at: want not claimed, got claimed")
	}
	assertPollFacts(ctx, t, uow, attemptID, &first, 0)
}

func TestNodeAttempts_ClaimPoll_AttemptNotDispatched_ClaimsNothing(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "completed", &first)

	// A callback completed the Attempt; its next_poll_at is still set, so only the
	// status condition stops a late poll claim.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().MarkSucceeded(ctx, attemptID, domain.NodeAttemptDispatched,
			fixtureTime.Add(time.Second), json.RawMessage(`{"imageUrl":"https://example.test/a.png"}`))
	}); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}

	if claimed := claimPoll(ctx, t, uow, attemptID, first.Add(time.Minute), 3); claimed {
		t.Fatal("ClaimPoll on SUCCEEDED Attempt: want not claimed, got claimed")
	}
	assertPollFacts(ctx, t, uow, attemptID, &first, 0)
}

func TestNodeAttempts_ClaimPoll_PollCountAtMax_ClaimsNothing(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "exhausted", &first)

	// The registered bound was lowered after earlier polls: the row still has a
	// scheduled poll, but poll_count already equals the current maximum.
	if _, err := pool.Exec(ctx, `UPDATE node_attempts SET poll_count = 2 WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("set poll_count: %v", err)
	}

	if claimed := claimPoll(ctx, t, uow, attemptID, first.Add(time.Minute), 2); claimed {
		t.Fatal("ClaimPoll at poll bound: want not claimed, got claimed")
	}
	assertPollFacts(ctx, t, uow, attemptID, &first, 2)
}

func TestNodeAttempts_ClaimPoll_Due_IncrementsCountAndAdvancesNextPollAt(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "due", &first)

	now := first.Add(250 * time.Millisecond)
	if claimed := claimPoll(ctx, t, uow, attemptID, now, 3); !claimed {
		t.Fatal("ClaimPoll when due: want claimed, got not claimed")
	}
	next := now.Add(pollInterval)
	assertPollFacts(ctx, t, uow, attemptID, &next, 1)

	// The claim is a scheduling fact only: the Attempt stays DISPATCHED.
	if attempt := getAttempt(ctx, t, uow, attemptID); attempt.Status != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status after claim: want DISPATCHED, got %s", attempt.Status)
	}
	// The same instant can no longer claim: the next poll is scheduled in the future.
	if claimed := claimPoll(ctx, t, uow, attemptID, now, 3); claimed {
		t.Fatal("second ClaimPoll at the same instant: want not claimed, got claimed")
	}
}

func TestNodeAttempts_ClaimPoll_ClaimReachesMax_ClearsNextPollAt(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "last", &first)

	const maxPolls = 2
	now := first
	if claimed := claimPoll(ctx, t, uow, attemptID, now, maxPolls); !claimed {
		t.Fatal("first ClaimPoll: want claimed, got not claimed")
	}
	next := now.Add(pollInterval)
	assertPollFacts(ctx, t, uow, attemptID, &next, 1)

	if claimed := claimPoll(ctx, t, uow, attemptID, next, maxPolls); !claimed {
		t.Fatal("ClaimPoll reaching the bound: want claimed, got not claimed")
	}
	// No further poll is scheduled; callback or deadline decides the Attempt.
	assertPollFacts(ctx, t, uow, attemptID, nil, maxPolls)

	if claimed := claimPoll(ctx, t, uow, attemptID, next.Add(time.Hour), maxPolls); claimed {
		t.Fatal("ClaimPoll after the bound: want not claimed, got claimed")
	}
}

func TestNodeAttempts_ClaimPoll_ConcurrentClaimers_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "raced", &first)

	now := first.Add(time.Second)
	aClaimed := make(chan struct{})
	bParked := make(chan struct{})
	type outcome struct {
		claimed bool
		err     error
	}
	aDone := make(chan outcome, 1)
	bDone := make(chan outcome, 1)

	// A claims and holds its uncommitted row lock until B is observably waiting on it.
	go func() {
		var o outcome
		o.err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			o.claimed, err = tx.NodeAttempts().ClaimPoll(ctx, attemptID, now, pollInterval, 3)
			if err != nil {
				return err
			}
			close(aClaimed)
			<-bParked
			return nil
		})
		aDone <- o
	}()

	<-aClaimed

	// B is the competing claimer (the Reconciler and the in-process scheduler both
	// reach the same due poll). It must re-check the WHERE clause against A's commit.
	go func() {
		var o outcome
		o.err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			o.claimed, err = tx.NodeAttempts().ClaimPoll(ctx, attemptID, now, pollInterval, 3)
			return err
		})
		bDone <- o
	}()

	// Barrier on observable database state rather than elapsed time: B must actually be
	// waiting for A's row lock before A commits.
	if err := waitUntilBlockedOnLock(ctx, pool); err != nil {
		close(bParked)
		t.Fatalf("barrier: %v", err)
	}
	close(bParked)

	a, b := <-aDone, <-bDone
	if a.err != nil || b.err != nil {
		t.Fatalf("claim errors: A=%v B=%v", a.err, b.err)
	}
	if !a.claimed || b.claimed {
		t.Fatalf("claim winners: want only A, got A=%v B=%v", a.claimed, b.claimed)
	}
	next := now.Add(pollInterval)
	assertPollFacts(ctx, t, uow, attemptID, &next, 1)
}

func TestNodeAttempts_ListDuePolls_ReturnsOnlyDueDispatchedInOrderWithinLimit(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	now := fixtureTime.Add(time.Minute)

	oldest := now.Add(-30 * time.Second)
	middle := now.Add(-20 * time.Second)
	newest := now
	future := now.Add(time.Second)

	// Seeded out of due order so ordering comes from next_poll_at, not insertion.
	nrMiddle, attMiddle := seedPollAttempt(ctx, t, uow, f, "middle", &middle)
	nrOldest, attOldest := seedPollAttempt(ctx, t, uow, f, "oldest", &oldest)
	_, attNewest := seedPollAttempt(ctx, t, uow, f, "newest", &newest)
	seedPollAttempt(ctx, t, uow, f, "future", &future)
	seedPollAttempt(ctx, t, uow, f, "nopoll", nil)
	_, attDone := seedPollAttempt(ctx, t, uow, f, "done", &oldest)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().MarkFailed(ctx, attDone, domain.NodeAttemptDispatched, now,
			domain.ExecutionError{Code: "TIMEOUT", Message: "attempt deadline exceeded"})
	}); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	all := listDuePolls(ctx, t, uow, now, 10)
	wantIDs := []string{attOldest, attMiddle, attNewest}
	if len(all) != len(wantIDs) {
		t.Fatalf("ListDuePolls: want %d rows, got %d: %+v", len(wantIDs), len(all), all)
	}
	for i, id := range wantIDs {
		if all[i].AttemptID != id {
			t.Fatalf("ListDuePolls[%d]: want %s, got %s (all %+v)", i, id, all[i].AttemptID, all)
		}
		if all[i].RunID != f.runID {
			t.Fatalf("ListDuePolls[%d].RunID: want %s, got %s", i, f.runID, all[i].RunID)
		}
		if all[i].PollCount != 0 {
			t.Fatalf("ListDuePolls[%d].PollCount: want 0, got %d", i, all[i].PollCount)
		}
	}
	if all[0].NodeRunID != nrOldest || all[1].NodeRunID != nrMiddle {
		t.Fatalf("ListDuePolls node runs: want %s, %s, got %s, %s",
			nrOldest, nrMiddle, all[0].NodeRunID, all[1].NodeRunID)
	}

	bounded := listDuePolls(ctx, t, uow, now, 2)
	if len(bounded) != 2 || bounded[0].AttemptID != attOldest || bounded[1].AttemptID != attMiddle {
		t.Fatalf("ListDuePolls limit 2: want [%s %s], got %+v", attOldest, attMiddle, bounded)
	}
}

func TestNodeAttempts_ClearPoll_Dispatched_SetsNextPollAtNull(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "drifted", &first)

	if cleared := clearPoll(ctx, t, uow, attemptID); !cleared {
		t.Fatal("ClearPoll on DISPATCHED: want cleared, got not cleared")
	}
	assertPollFacts(ctx, t, uow, attemptID, nil, 0)
	if attempt := getAttempt(ctx, t, uow, attemptID); attempt.Status != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status after ClearPoll: want DISPATCHED, got %s", attempt.Status)
	}
	if due := listDuePolls(ctx, t, uow, first.Add(time.Hour), 10); len(due) != 0 {
		t.Fatalf("ListDuePolls after ClearPoll: want none, got %+v", due)
	}
}

func TestNodeAttempts_ClearPoll_Succeeded_ChangesNothing(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	first := fixtureTime.Add(pollInterval)
	_, attemptID := seedPollAttempt(ctx, t, uow, f, "finished", &first)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().MarkSucceeded(ctx, attemptID, domain.NodeAttemptDispatched,
			fixtureTime.Add(time.Second), json.RawMessage(`{"imageUrl":"https://example.test/a.png"}`))
	}); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}

	if cleared := clearPoll(ctx, t, uow, attemptID); cleared {
		t.Fatal("ClearPoll on SUCCEEDED: want not cleared, got cleared")
	}
	assertPollFacts(ctx, t, uow, attemptID, &first, 0)
}

func TestMigrations_NodeAttemptPolling_AppliesOnTopOfInitialSchema(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OpenUnmigrated(t)

	// Apply only the initial schema, write an Attempt dispatched before polling
	// existed, then apply the remaining migrations through the production entry point.
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	if err := goose.UpToContext(ctx, db, ".", 1); err != nil {
		t.Fatalf("apply initial schema: %v", err)
	}

	uow := postgres.NewUnitOfWork(pool)
	f := seedRun(ctx, t, uow)
	// Raw SQL: the Store already maps the new columns, which do not exist yet here.
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_attempts (id, node_run_id, attempt_no, status, input, started_at, dispatched_at)
		VALUES ('attempt_legacy', $1, 1, 'DISPATCHED', '{}', $2, $2)`, f.nodeRunID, fixtureTime); err != nil {
		t.Fatalf("insert legacy attempt: %v", err)
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate on top of initial schema: %v", err)
	}

	// An Attempt dispatched before the migration has no scheduled poll.
	assertPollFacts(ctx, t, uow, "attempt_legacy", nil, 0)

	var indexExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'node_attempts' AND indexname = 'node_attempts_due_poll_idx')`,
	).Scan(&indexExists); err != nil {
		t.Fatalf("check due-poll index: %v", err)
	}
	if !indexExists {
		t.Fatal("node_attempts_due_poll_idx: want created by migration, got missing")
	}

	if _, err := pool.Exec(ctx, `UPDATE node_attempts SET poll_count = -1 WHERE id = 'attempt_legacy'`); err == nil {
		t.Fatal("negative poll_count: want CHECK violation, got accepted")
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// seedPollAttempt commits a WAITING_CALLBACK NodeRun with its first Attempt, then
// dispatches that Attempt with firstPollAt as its first scheduled poll.
func seedPollAttempt(
	ctx context.Context,
	t *testing.T,
	uow store.UnitOfWork,
	f fixture,
	suffix string,
	firstPollAt *time.Time,
) (nodeRunID, attemptID string) {
	t.Helper()
	nodeRunID, attemptID = seedAttempt(ctx, t, uow, f, suffix, domain.NodeRunWaitingCallback, false)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().MarkDispatched(ctx, attemptID, fixtureTime, firstPollAt)
	}); err != nil {
		t.Fatalf("dispatch attempt %s: %v", suffix, err)
	}
	return nodeRunID, attemptID
}

func claimPoll(ctx context.Context, t *testing.T, uow store.UnitOfWork, attemptID string, now time.Time, maxPolls int) bool {
	t.Helper()
	var claimed bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		claimed, err = tx.NodeAttempts().ClaimPoll(ctx, attemptID, now, pollInterval, maxPolls)
		return err
	}); err != nil {
		t.Fatalf("ClaimPoll %s: %v", attemptID, err)
	}
	return claimed
}

func clearPoll(ctx context.Context, t *testing.T, uow store.UnitOfWork, attemptID string) bool {
	t.Helper()
	var cleared bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		cleared, err = tx.NodeAttempts().ClearPoll(ctx, attemptID)
		return err
	}); err != nil {
		t.Fatalf("ClearPoll %s: %v", attemptID, err)
	}
	return cleared
}

func listDuePolls(ctx context.Context, t *testing.T, uow store.UnitOfWork, now time.Time, limit int) []store.DuePoll {
	t.Helper()
	var due []store.DuePoll
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		due, err = tx.NodeAttempts().ListDuePolls(ctx, now, limit)
		return err
	}); err != nil {
		t.Fatalf("ListDuePolls: %v", err)
	}
	return due
}

func assertPollFacts(ctx context.Context, t *testing.T, uow store.UnitOfWork, attemptID string, wantNext *time.Time, wantCount int) {
	t.Helper()
	attempt := getAttempt(ctx, t, uow, attemptID)
	switch {
	case wantNext == nil && attempt.NextPollAt != nil:
		t.Fatalf("%s next_poll_at: want NULL, got %v", attemptID, attempt.NextPollAt)
	case wantNext != nil && (attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(*wantNext)):
		t.Fatalf("%s next_poll_at: want %v, got %v", attemptID, *wantNext, attempt.NextPollAt)
	}
	if attempt.PollCount != wantCount {
		t.Fatalf("%s poll_count: want %d, got %d", attemptID, wantCount, attempt.PollCount)
	}
}
