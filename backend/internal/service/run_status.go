package service

import (
	"context"
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
// Writing a Run status directly with UpdateAggregate is reserved for a status that cannot
// end a Run: the lock's own status, for the seq watermark alone, or RUNNING.
func writeRunStatusLocked(ctx context.Context, tx store.Tx, lock *store.RunLock, status domain.RunStatus, now time.Time) error {
	if status.IsTerminal() {
		if err := tx.ReleaseOccupations().ScheduleQueriesOfRun(ctx, lock.RunID(), now); err != nil {
			return err
		}
	}
	return tx.Runs().UpdateAggregate(ctx, lock, status, now)
}
