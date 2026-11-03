//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// runLockGate holds the Run row lock in a transaction of the test's own. Service
// transactions that take the Run aggregate lock queue behind it, and PostgreSQL grants a
// row lock to its waiters in the order they arrived, so committing the gate lets the queued
// transactions proceed one at a time in a known order.
type runLockGate struct {
	pool *pgxpool.Pool
	conn *pgxpool.Conn
	done bool
}

// lockRunRow opens the gate's transaction on a connection of its own and takes the Run row
// lock in it.
func lockRunRow(ctx context.Context, pool *pgxpool.Pool, runID string) (*runLockGate, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	gate := &runLockGate{pool: pool, conn: conn}
	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		conn.Release()
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, runID); err != nil {
		gate.close(ctx)
		return nil, err
	}
	return gate, nil
}

// close rolls the gate's transaction back unless it was committed and returns its
// connection to the pool.
func (g *runLockGate) close(ctx context.Context) {
	if g.done {
		return
	}
	g.done = true
	_, _ = g.conn.Exec(ctx, `ROLLBACK`)
	g.conn.Release()
}

// waitForWaiters blocks until n backends of this database wait for a lock, which is the
// barrier that proves each queued transaction reached the Run lock. The wait is bounded.
func (g *runLockGate) waitForWaiters(ctx context.Context, n int) error {
	const query = `
		SELECT count(DISTINCT l.pid)
		  FROM pg_locks l
		  JOIN pg_stat_activity a ON a.pid = l.pid
		 WHERE NOT l.granted AND a.datname = current_database()`
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := g.pool.QueryRow(ctx, query).Scan(&waiting); err != nil {
			return fmt.Errorf("count lock waiters: %w", err)
		}
		if waiting >= n {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lock waiters = %d after 10s, want %d", waiting, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// awaitWaiters is waitForWaiters on the test goroutine.
func (g *runLockGate) awaitWaiters(t *testing.T, n int) {
	t.Helper()
	if err := g.waitForWaiters(context.Background(), n); err != nil {
		t.Fatal(err)
	}
}

func (g *runLockGate) open(t *testing.T) {
	t.Helper()
	if _, err := g.conn.Exec(context.Background(), `COMMIT`); err != nil {
		t.Fatalf("release the run lock gate: %v", err)
	}
	g.done = true
	g.conn.Release()
}

// gateHandoff passes the gate a poll hook takes back to the test goroutine.
type gateHandoff struct {
	ready chan struct{}
	gate  *runLockGate
	err   error
}

func newGateHandoff() *gateHandoff { return &gateHandoff{ready: make(chan struct{})} }

// take runs on the poll's goroutine: it locks the Run row and, when given, does more while
// holding it, then hands the gate over. It runs for the first call only.
func (h *gateHandoff) take(ctx context.Context, pool *pgxpool.Pool, runID string, thenWhileHeld func(*runLockGate) error) {
	select {
	case <-h.ready:
		return
	default:
	}
	defer close(h.ready)
	if h.gate, h.err = lockRunRow(ctx, pool, runID); h.err != nil {
		return
	}
	if thenWhileHeld != nil {
		h.err = thenWhileHeld(h.gate)
	}
}

// await blocks until the hook handed the gate over and registers its cleanup.
func (h *gateHandoff) await(t *testing.T) *runLockGate {
	t.Helper()
	select {
	case <-h.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the poll never reached its hook")
	}
	if h.gate != nil {
		gate := h.gate
		t.Cleanup(func() { gate.close(context.Background()) })
	}
	if h.err != nil {
		t.Fatalf("take the run lock gate: %v", h.err)
	}
	return h.gate
}

// raceSetup is a release approved and dispatched, deployed by production, with both its
// poll and its confirm deadline due: the conclusion of the poll and the human wait of the
// deadline are about to contend for the Run lock.
type raceSetup struct {
	f         *releaseFixture
	action    domain.AgentAction
	attemptID string
}

func newRaceSetup(t *testing.T, workflowID string, hooks func(*raceSetup) service.PollHooks) *raceSetup {
	t.Helper()
	setup := &raceSetup{}
	setup.f = newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds, pollHooks: hooks(setup)})
	setup.action = setup.f.startToRelease(t, workflowID)
	setup.f.approve(t, setup.action)
	setup.attemptID = setup.f.attempts(t, setup.action.ID)[0].ID
	setup.f.h.clock.Advance(time.Duration(humanWaitBounds.confirmTimeoutMs)*time.Millisecond + releasePollStep)
	return setup
}

func (s *raceSetup) poll(ctx context.Context, errs chan<- error) {
	_, err := s.f.h.svc.PollToolAttempt(ctx, service.PollToolAttempt{AttemptID: s.attemptID, ActionID: s.action.ID})
	errs <- err
}

func (s *raceSetup) openHumanWait(ctx context.Context, errs chan<- error) {
	errs <- s.f.h.svc.OpenReleaseHumanWait(ctx, s.attemptID)
}

func collect(t *testing.T, name string, errs <-chan error) {
	t.Helper()
	if err := <-errs; err != nil {
		t.Errorf("%s: %v", name, err)
	}
}

// Contract case: production's DEPLOYED answer and the confirm deadline contend for the Run
// lock, and the conclusion gets it first. The release concludes; the deadline transaction
// then finds the Action already SUCCEEDED and opens no human wait.
func TestRelease_ConclusionAndConfirmDeadlineContend_ConclusionFirst_NoHumanWait(t *testing.T) {
	handoff := newGateHandoff()
	setup := newRaceSetup(t, "wf_release_race_conclusion_first", func(s *raceSetup) service.PollHooks {
		return service.PollHooks{AfterClaim: func(ctx context.Context, attemptID string) error {
			if attemptID == s.attemptID {
				handoff.take(ctx, s.f.h.pool, s.f.run.ID, nil)
			}
			return nil
		}}
	})
	f, action := setup.f, setup.action

	pollErrs, waitErrs := make(chan error, 1), make(chan error, 1)
	go setup.poll(f.h.ctx, pollErrs)
	gate := handoff.await(t)
	// The gate was taken after the poll claim, so the first waiter is the conclusion.
	gate.awaitWaiters(t, 1)
	go setup.openHumanWait(f.h.ctx, waitErrs)
	gate.awaitWaiters(t, 2)
	gate.open(t)
	collect(t, "poll", pollErrs)
	collect(t, "open human wait", waitErrs)
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}

	f.assertDeployedOnce(t, action, domain.ReleaseOperationKey(action.ID, 1))
	if wait, found := f.humanWait(t, action.ID); found {
		t.Errorf("human wait = %+v, want none: the release concluded first", wait)
	}
	if waiting := f.eventsOf(t, domain.EventAgentActionWaitingHuman, action.ID); len(waiting) != 0 {
		t.Errorf("AGENT_ACTION_WAITING_HUMAN = %d, want none", len(waiting))
	}
	if completed := f.eventsOf(t, domain.EventAgentActionCompleted, action.ID); len(completed) != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED = %d, want 1", len(completed))
	}
	f.finish(t)
}

// Contract case: production's DEPLOYED answer and the confirm deadline contend for the Run
// lock, and the deadline gets it first. The human wait opens; the conclusion then settles
// the release from WAITING_HUMAN and closes the wait RESOLVED. The release concludes once.
func TestRelease_ConclusionAndConfirmDeadlineContend_DeadlineFirst_WaitOpenedThenResolved(t *testing.T) {
	handoff := newGateHandoff()
	waitErrs := make(chan error, 1)
	setup := newRaceSetup(t, "wf_release_race_deadline_first", func(s *raceSetup) service.PollHooks {
		return service.PollHooks{AfterClaim: func(ctx context.Context, attemptID string) error {
			if attemptID != s.attemptID {
				return nil
			}
			// The deadline transaction queues first; the poll goes on to its conclusion
			// only once that one waits on the lock.
			handoff.take(ctx, s.f.h.pool, s.f.run.ID, func(gate *runLockGate) error {
				go s.openHumanWait(ctx, waitErrs)
				return gate.waitForWaiters(ctx, 1)
			})
			return nil
		}}
	})
	f, action := setup.f, setup.action

	pollErrs := make(chan error, 1)
	go setup.poll(f.h.ctx, pollErrs)
	gate := handoff.await(t)
	gate.awaitWaiters(t, 2)
	gate.open(t)
	collect(t, "open human wait", waitErrs)
	collect(t, "poll", pollErrs)
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}

	f.assertDeployedOnce(t, action, domain.ReleaseOperationKey(action.ID, 1))
	wait, found := f.humanWait(t, action.ID)
	if !found || wait.Status != domain.HumanWaitResolved || wait.Outcome == nil || *wait.Outcome != string(domain.ReleaseReasonDeployed) {
		t.Errorf("human wait = %+v (%v), want one RESOLVED with the DEPLOYED outcome", wait, found)
	}
	if waiting := f.eventsOf(t, domain.EventAgentActionWaitingHuman, action.ID); len(waiting) != 1 {
		t.Errorf("AGENT_ACTION_WAITING_HUMAN = %d, want 1", len(waiting))
	}
	if completed := f.eventsOf(t, domain.EventAgentActionCompleted, action.ID); len(completed) != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED = %d, want 1", len(completed))
	}
	if agentRun := f.agentRun(t); agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after the conclusion", agentRun.PauseReason)
	}
	f.finish(t)
}
