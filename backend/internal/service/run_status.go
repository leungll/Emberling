package service

import (
	"context"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// writeRunStatusLocked persists a Run status derived from NodeRun state, together with the
// seq watermark, under the Run aggregate lock the caller holds. It is the one path every
// derived status takes, so it is where a Run ends, whichever use case ended it.
//
// A Run that ends may still hold a release target: a parallel branch failed while the Agent
// waited on production, or a person stopped waiting. No live path settles that release any
// more, so a terminal status hands it over here, in the same transaction: every ACTIVE
// occupation of the Run gets its first post-task query, due now, and the Reconciler finds it
// from the database after any crash. Run status stays a pure aggregation; this only records
// what the end of the Run leaves to do.
//
// A retry that has not started ends here too, see settlePendingRetries.
//
// Writing a Run status directly with UpdateAggregate is reserved for a status that cannot
// end a Run: the lock's own status, for the seq watermark alone, or RUNNING.
func (s *ExecutionService) writeRunStatusLocked(ctx context.Context, tx store.Tx, lock *store.RunLock, status domain.RunStatus, now time.Time) error {
	if status.IsTerminal() {
		if err := tx.ReleaseOccupations().ScheduleQueriesOfRun(ctx, lock.RunID(), now); err != nil {
			return err
		}
		if err := s.settlePendingRetries(ctx, tx, lock, now); err != nil {
			return err
		}
	}
	return tx.Runs().UpdateAggregate(ctx, lock, status, now)
}

// settlePendingRetries ends every NodeRun of an ending Run that is waiting for a scheduled
// retry. Nothing claims work of an ended Run, so that retry would never start: the NodeRun
// would stay RUNNING, keep its Provider slot and return from the recovery scan on every pass.
//
// Such a NodeRun has no live Attempt, and its outcome is the failure of its last Attempt,
// which the retry was going to replace. It therefore fails with that error, and frees its
// slot, in the transaction that ends the Run. FAILED is the only ending the state machine
// offers a RUNNING NodeRun that did not succeed. NODE_FAILED lands after RUN_*, as it does for
// any sibling that fails on an ended Run. Run status, error and completed_at stay as the
// ending set them, because the caller writes them from its own aggregation.
//
// A NodeRun whose retry already started has no scheduled retry and is not touched: its live
// Attempt's completion, timeout or orphan path ends it. The Run aggregate lock the caller
// holds serializes this with the retry claim, so each NodeRun gets exactly one of the two.
//
// The freed slot is not offered to the in-process queue: the queued NodeRun is persisted
// READY work, so the Reconciler admits it. Only latency depends on that offer.
func (s *ExecutionService) settlePendingRetries(ctx context.Context, tx store.Tx, lock *store.RunLock, now time.Time) error {
	nodeRuns, err := tx.NodeRuns().ListByRun(ctx, lock.RunID())
	if err != nil {
		return err
	}
	for _, nr := range nodeRuns {
		if nr.Status != domain.NodeRunRunning || nr.NextAttemptAt == nil {
			continue
		}
		attempts, err := tx.NodeAttempts().ListByNodeRun(ctx, nr.ID)
		if err != nil {
			return err
		}
		if len(attempts) == 0 {
			return fmt.Errorf("execution: node run %s has a scheduled retry and no Attempt", nr.ID)
		}
		last := attempts[len(attempts)-1]
		if last.Status != domain.NodeAttemptFailed || last.Error == nil {
			return fmt.Errorf("execution: node run %s has a scheduled retry but attempt %s is %s", nr.ID, last.ID, last.Status)
		}
		if err := tx.NodeRuns().MarkFailed(ctx, nr.ID, domain.NodeRunRunning, now, *last.Error, nil); err != nil {
			return err
		}
		if _, err := releaseSlot(ctx, tx, nr.ID); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, lock, lock.RunID(), &nr.ID, domain.EventNodeFailed, now, nodeFailedPayload{
			AttemptNo: last.AttemptNo, Error: *last.Error,
		}); err != nil {
			return err
		}
	}
	return nil
}
