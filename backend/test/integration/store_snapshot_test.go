//go:build integration

// Package integration: this file covers store.UnitOfWork.WithinReadTx, the read-only
// REPEATABLE READ transaction the Snapshot-to-SSE handoff contract needs
// (docs/08-interface-spec.md §5: "lastSeq 与 Snapshot 在同一个一致性读取中取得"). A plain
// READ COMMITTED transaction cannot give that guarantee: two separate statements
// (Runs().Get, then NodeRuns().ListByRun) can each see a different, more recent snapshot
// if another transaction commits in between.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

func TestUnitOfWork_WithinReadTx_ConcurrentCommitBetweenReads_NotVisible(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)

	var nodeRunsAfterConcurrentCommit []domain.NodeRun

	err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		// Read the Run first, same as QueryService.Snapshot: a stray duplicate Event is
		// tolerated by SSE dedup, but a gap (NodeRuns read from a snapshot older than the
		// Run's LastSeq) is not, so Run must never be the one read from the newer half of
		// two straddled snapshots.
		if _, err := tx.Runs().Get(ctx, f.runID); err != nil {
			return err
		}

		// A fully separate, real, committing transaction advances the same Run while this
		// read transaction is still open. If WithinReadTx only gives READ COMMITTED, the
		// next statement below will observe this commit; REPEATABLE READ must not.
		if err := uow.WithinTx(ctx, func(ctx context.Context, wtx store.Tx) error {
			lock, err := wtx.Runs().LockForUpdate(ctx, f.runID)
			if err != nil {
				return err
			}
			if err := wtx.NodeRuns().Transition(ctx, f.nodeRunID, domain.NodeRunReady, domain.NodeRunRunning, time.Now().UTC()); err != nil {
				return err
			}
			if _, err := wtx.Events().Append(ctx, lock, newEvent(f.runID, domain.EventNodeStarted)); err != nil {
				return err
			}
			return wtx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, time.Now().UTC())
		}); err != nil {
			return err
		}

		var err error
		nodeRunsAfterConcurrentCommit, err = tx.NodeRuns().ListByRun(ctx, f.runID)
		return err
	})
	if err != nil {
		t.Fatalf("WithinReadTx: %v", err)
	}

	if len(nodeRunsAfterConcurrentCommit) != 1 || nodeRunsAfterConcurrentCommit[0].Status != domain.NodeRunReady {
		t.Fatalf("NodeRuns read inside WithinReadTx after a concurrent commit: want still READY (the read transaction's own snapshot, taken before the concurrent commit), got %+v",
			nodeRunsAfterConcurrentCommit)
	}

	// A write attempted inside a read-only transaction must fail outright: PostgreSQL
	// rejects any write statement there (SQLSTATE 25006). This runs as its own
	// WithinReadTx call rather than inside the block above, because PostgreSQL aborts the
	// rest of a transaction after any failed statement — reusing the same transaction
	// would make the consistency check above depend on statement ordering instead of
	// testing it directly.
	writeErr := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Transition(ctx, f.nodeRunID, domain.NodeRunReady, domain.NodeRunRunning, time.Now().UTC())
	})
	if writeErr == nil {
		t.Fatal("write inside WithinReadTx: want error, got nil")
	}

	// The concurrent transaction's commit is real: a fresh transaction must see it.
	nr := getNodeRun(ctx, t, uow, f.nodeRunID)
	if nr.Status != domain.NodeRunRunning {
		t.Fatalf("NodeRun status after the concurrent commit, read fresh: want RUNNING, got %s", nr.Status)
	}
}
