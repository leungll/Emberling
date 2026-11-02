//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// Provider slot store tests. The occupancy row and the deferral columns are persisted
// facts admission and release depend on, so their conditional behaviour (one row per
// NodeRun, a release that finds nothing, a lock row that must exist, seeding that
// converges) is proven against PostgreSQL, not a fake.

const slotsProviderID = "slots-provider-v1"

type slotsHarness struct {
	ctx  context.Context
	t    *testing.T
	pool *pgxpool.Pool
	uow  store.UnitOfWork
	f    fixture
}

func newSlotsHarness(t *testing.T) *slotsHarness {
	t.Helper()
	pool := testdb.Open(t)
	h := &slotsHarness{ctx: context.Background(), t: t, pool: pool, uow: postgres.NewUnitOfWork(pool)}
	h.f = seedRun(h.ctx, t, h.uow)
	h.seedLocks(slotsProviderID)
	return h
}

func (h *slotsHarness) seedLocks(ids ...string) {
	h.t.Helper()
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ProviderSlots().SeedLocks(ctx, ids)
	}); err != nil {
		h.t.Fatalf("seed locks: %v", err)
	}
}

func (h *slotsHarness) within(fn func(ctx context.Context, tx store.Tx) error) error {
	h.t.Helper()
	return h.uow.WithinTx(h.ctx, fn)
}

// addRun commits one more Run of the seeded Workflow with the given status and one READY
// NodeRun whose ready_at is the fixture time plus offset.
func (h *slotsHarness) addRun(runID, nodeRunID string, status domain.RunStatus, offset time.Duration) {
	h.t.Helper()
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		if err := tx.Runs().Create(ctx, domain.Run{
			ID: runID, WorkflowID: h.f.workflowID, DefinitionVersion: 1, Status: status,
			Input: json.RawMessage(`{}`), StartedAt: fixtureTime, UpdatedAt: fixtureTime,
		}); err != nil {
			return err
		}
		nr := newNodeRun(nodeRunID, runID, "node_input")
		nr.ReadyAt = fixtureTime.Add(offset)
		return tx.NodeRuns().Create(ctx, nr)
	}); err != nil {
		h.t.Fatalf("add run %s: %v", runID, err)
	}
}

func (h *slotsHarness) defer_(nodeRunID string, at time.Time) bool {
	h.t.Helper()
	var first bool
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		var err error
		first, err = tx.NodeRuns().MarkCapacityDeferred(ctx, nodeRunID, slotsProviderID, at)
		return err
	}); err != nil {
		h.t.Fatalf("mark deferred %s: %v", nodeRunID, err)
	}
	return first
}

func (h *slotsHarness) countLockRows() int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM provider_slot_locks`).Scan(&n); err != nil {
		h.t.Fatalf("count lock rows: %v", err)
	}
	return n
}

func TestProviderSlots_SeedLocks_RepeatedCall_Idempotent(t *testing.T) {
	h := newSlotsHarness(t)
	h.seedLocks(slotsProviderID, "slots-provider-v2")
	h.seedLocks("slots-provider-v2", slotsProviderID)
	if got := h.countLockRows(); got != 2 {
		t.Fatalf("lock rows = %d, want 2 after repeated seeding", got)
	}
}

func TestProviderSlots_LockProvider_MissingRowErrors(t *testing.T) {
	h := newSlotsHarness(t)
	err := h.within(func(ctx context.Context, tx store.Tx) error {
		return tx.ProviderSlots().LockProvider(ctx, "never-seeded-provider")
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LockProvider(unseeded) error = %v, want ErrNotFound", err)
	}
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		return tx.ProviderSlots().LockProvider(ctx, slotsProviderID)
	}); err != nil {
		t.Fatalf("LockProvider(seeded) error = %v, want nil", err)
	}
}

func TestProviderSlots_Acquire_DuplicateNodeRunRejected(t *testing.T) {
	h := newSlotsHarness(t)
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		return tx.ProviderSlots().Acquire(ctx, h.f.nodeRunID, slotsProviderID, fixtureTime)
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	err := h.within(func(ctx context.Context, tx store.Tx) error {
		return tx.ProviderSlots().Acquire(ctx, h.f.nodeRunID, slotsProviderID, fixtureTime.Add(time.Second))
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Acquire error = %v, want ErrConflict", err)
	}
	var held int
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		var err error
		held, err = tx.ProviderSlots().CountHeld(ctx, slotsProviderID)
		return err
	}); err != nil {
		t.Fatalf("CountHeld: %v", err)
	}
	if held != 1 {
		t.Fatalf("held = %d, want exactly the first row", held)
	}
}

func TestProviderSlots_Release_SecondCallAffectsZeroRows(t *testing.T) {
	h := newSlotsHarness(t)
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		return tx.ProviderSlots().Acquire(ctx, h.f.nodeRunID, slotsProviderID, fixtureTime)
	}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release := func() (string, bool) {
		var id string
		var released bool
		if err := h.within(func(ctx context.Context, tx store.Tx) error {
			var err error
			id, released, err = tx.ProviderSlots().Release(ctx, h.f.nodeRunID)
			return err
		}); err != nil {
			t.Fatalf("Release: %v", err)
		}
		return id, released
	}
	if id, released := release(); !released || id != slotsProviderID {
		t.Fatalf("first Release = (%q, %v), want (%q, true)", id, released, slotsProviderID)
	}
	if id, released := release(); released || id != "" {
		t.Fatalf("second Release = (%q, %v), want (\"\", false) with no error", id, released)
	}
}

func TestProviderSlots_MarkCapacityDeferred_OnlyFirstRefusalWrites(t *testing.T) {
	h := newSlotsHarness(t)
	first := fixtureTime.Add(time.Minute)
	if !h.defer_(h.f.nodeRunID, first) {
		t.Fatal("first MarkCapacityDeferred = false, want true")
	}
	if h.defer_(h.f.nodeRunID, first.Add(time.Minute)) {
		t.Fatal("second MarkCapacityDeferred = true, want false")
	}
	nr := getNodeRun(h.ctx, t, h.uow, h.f.nodeRunID)
	if nr.CapacityDeferredAt == nil || !nr.CapacityDeferredAt.Equal(first) {
		t.Fatalf("CapacityDeferredAt = %v, want the first refusal time %v", nr.CapacityDeferredAt, first)
	}
	if nr.CapacityProviderID == nil || *nr.CapacityProviderID != slotsProviderID {
		t.Fatalf("CapacityProviderID = %v, want %q", nr.CapacityProviderID, slotsProviderID)
	}
	if nr.Status != domain.NodeRunReady {
		t.Fatalf("status = %s, want READY: a refusal changes no NodeRun status", nr.Status)
	}
}

func TestProviderSlots_MarkCapacityDeferred_NonReadyNodeRun_WritesNothing(t *testing.T) {
	h := newSlotsHarness(t)
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		won, err := tx.NodeRuns().ClaimReady(ctx, h.f.nodeRunID, fixtureTime)
		if err != nil || !won {
			return errors.New("claim ready did not win")
		}
		return nil
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if h.defer_(h.f.nodeRunID, fixtureTime.Add(time.Minute)) {
		t.Fatal("MarkCapacityDeferred on a RUNNING NodeRun = true, want false")
	}
	if nr := getNodeRun(h.ctx, t, h.uow, h.f.nodeRunID); nr.CapacityDeferredAt != nil {
		t.Fatalf("CapacityDeferredAt = %v, want nil", nr.CapacityDeferredAt)
	}
}

func TestProviderSlots_DeferralColumns_RejectHalfWrittenRow(t *testing.T) {
	h := newSlotsHarness(t)
	_, err := h.pool.Exec(h.ctx, `UPDATE node_runs SET capacity_deferred_at = $2 WHERE id = $1`, h.f.nodeRunID, fixtureTime)
	if err == nil {
		t.Fatal("a deferral time without its provider id was accepted, want the CHECK to refuse it")
	}
}

func TestProviderSlots_QueueQueries_FollowReadyOrderAndSkipTerminalRuns(t *testing.T) {
	h := newSlotsHarness(t)
	// Queue order is (ready_at, id). run_a's NodeRun is the earliest and lives in a Run
	// that already failed, so it must count nowhere; run_b and run_c are live.
	h.addRun("run_a", "nr_a", domain.RunFailed, -2*time.Minute)
	h.addRun("run_b", "nr_b", domain.RunRunning, -time.Minute)
	h.addRun("run_c", "nr_c", domain.RunPaused, 0)
	h.addRun("run_d", "nr_d", domain.RunRunning, time.Minute)
	for _, id := range []string{"nr_a", "nr_b", "nr_c"} {
		h.defer_(id, fixtureTime)
	}
	// nr_d is READY but was never refused: it is not in the queue.

	var earlierForSeed, earlierForC int
	var nextRun string
	var found bool
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		var err error
		// The seeded NodeRun shares ready_at with nr_c; its id "nr_seed" sorts after "nr_c",
		// so nr_b and nr_c are both earlier.
		earlierForSeed, err = tx.ProviderSlots().CountEarlierDeferred(ctx, slotsProviderID, h.f.runID, store.QueueKey{ReadyAt: fixtureTime, NodeRunID: h.f.nodeRunID})
		if err != nil {
			return err
		}
		// For run_c itself only nr_b is earlier: its own Run is excluded, nr_a's Run is terminal.
		earlierForC, err = tx.ProviderSlots().CountEarlierDeferred(ctx, slotsProviderID, "run_c", store.QueueKey{ReadyAt: fixtureTime, NodeRunID: "nr_c"})
		if err != nil {
			return err
		}
		nextRun, found, err = tx.ProviderSlots().NextDeferredRun(ctx, slotsProviderID)
		return err
	}); err != nil {
		t.Fatalf("queue queries: %v", err)
	}
	if earlierForSeed != 2 {
		t.Fatalf("CountEarlierDeferred(seed) = %d, want 2 (nr_b, nr_c; nr_a's Run is FAILED, nr_d never queued)", earlierForSeed)
	}
	if earlierForC != 1 {
		t.Fatalf("CountEarlierDeferred(run_c) = %d, want 1 (nr_b only)", earlierForC)
	}
	if !found || nextRun != "run_b" {
		t.Fatalf("NextDeferredRun = (%q, %v), want run_b: the earliest queued NodeRun in a live Run", nextRun, found)
	}

	// Another Provider has nothing queued.
	if err := h.within(func(ctx context.Context, tx store.Tx) error {
		_, found, err := tx.ProviderSlots().NextDeferredRun(ctx, "slots-provider-other")
		if err != nil {
			return err
		}
		if found {
			return errors.New("found a queued NodeRun for a Provider nothing queued on")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
