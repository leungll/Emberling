//go:build integration

package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/service"
)

// startToVoidedAnswer is a release production never received whose human wait is open,
// with production back and the void grace passed: the next poll of the Attempt asks
// production to void the operation and learns VOIDED. hooks is installed with armed false,
// so the polls this setup itself drives pass through untouched.
func startToVoidedAnswer(t *testing.T, workflowID string, hooks func(*releaseFixture, *atomic.Bool, *string) service.PollHooks) (*releaseFixture, domain.AgentAction, domain.HumanWait, string) {
	t.Helper()
	var (
		armed  atomic.Bool
		target string
		f      *releaseFixture
	)
	holder := &releaseFixture{}
	f = newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds, pollHooks: hooks(holder, &armed, &target)})
	action := f.startToRelease(t, workflowID)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	f.approve(t, action)
	f.assertOutcomeUnknown(t, action)
	wait := f.openHumanWait(t, action)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})
	f.h.clock.Advance(time.Duration(unknownOutcomeBounds.voidGraceMs) * time.Millisecond)
	target = f.attempts(t, action.ID)[0].ID
	*holder = *f
	armed.Store(true)
	return f, action, wait, target
}

// Contract case: a person's stop and production's VOIDED answer contend for the Run lock,
// and the stop gets it first. The task ends as HUMAN_STOPPED with the target held; the
// voided conclusion then finds the Attempt closed and writes nothing, and the post-task
// reconciliation, which may void because production had lost the operation, frees the
// target as VOIDED without touching the ended Run.
func TestHumanStop_StopAndVoidedAnswerContend_StopFirst_TargetFreedOnlyByReconciliation(t *testing.T) {
	handoff := newGateHandoff()
	stopErrs := make(chan error, 1)
	var waitID string
	f, action, wait, target := startToVoidedAnswer(t, "wf_stop_void_race_stop_first", func(f *releaseFixture, armed *atomic.Bool, target *string) service.PollHooks {
		return service.PollHooks{AfterClaim: func(ctx context.Context, attemptID string) error {
			if !armed.Load() || attemptID != *target {
				return nil
			}
			handoff.take(ctx, f.h.pool, f.run.ID, func(gate *runLockGate) error {
				go func() {
					_, err := f.h.svc.StopHumanWait(ctx, waitID, stopOperator)
					stopErrs <- err
				}()
				return gate.waitForWaiters(ctx, 1)
			})
			return nil
		}}
	})
	waitID = wait.ID

	pollErrs := make(chan error, 1)
	go func() {
		_, err := f.h.svc.PollToolAttempt(f.h.ctx, service.PollToolAttempt{AttemptID: target, ActionID: action.ID})
		pollErrs <- err
	}()
	gate := handoff.await(t)
	gate.awaitWaiters(t, 2)
	gate.open(t)
	collect(t, "stop", stopErrs)
	collect(t, "poll", pollErrs)

	closedBy := stopOperator
	f.assertEndedReleaseUnknown(t, action, domain.HumanWaitStopped, &closedBy)
	key := domain.ReleaseOperationKey(action.ID, 1)
	if status := f.production.operationStatus(t, key); status != "VOIDED" {
		t.Fatalf("production operation = %q, want VOIDED: the answer did exist", status)
	}
	if voided := f.eventsOf(t, domain.EventReleaseVoided, action.ID); len(voided) != 0 {
		t.Errorf("RELEASE_VOIDED events = %+v, want none: the stop ended the release first", voided)
	}
	events := len(f.events(t))

	f.postReconcile(t)
	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonVoided || occupation.DeployedUncheckedVersion != nil {
		t.Errorf("occupation after the reconciliation = %+v, want RELEASED VOIDED with no live version", occupation)
	}
	if got := len(f.events(t)); got != events {
		t.Errorf("events after the reconciliation = %d, want %d: the ended Run gets nothing", got, events)
	}
	if n := f.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests production received = %d, want only the unanswered one", n)
	}
}

// Contract case: production's VOIDED answer and a person's stop contend for the Run lock,
// and the answer gets it first. The voided release frees the target as NOT_DEPLOYED and
// resolves the wait; the stop is refused as resolved and writes nothing.
func TestHumanStop_StopAndVoidedAnswerContend_AnswerFirst_ReleaseNotDeployedStopRefused(t *testing.T) {
	handoff := newGateHandoff()
	f, action, wait, target := startToVoidedAnswer(t, "wf_stop_void_race_answer_first", func(f *releaseFixture, armed *atomic.Bool, target *string) service.PollHooks {
		return service.PollHooks{AfterClaim: func(ctx context.Context, attemptID string) error {
			if armed.Load() && attemptID == *target {
				handoff.take(ctx, f.h.pool, f.run.ID, nil)
			}
			return nil
		}}
	})

	pollErrs, stopErrs := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := f.h.svc.PollToolAttempt(f.h.ctx, service.PollToolAttempt{AttemptID: target, ActionID: action.ID})
		pollErrs <- err
	}()
	gate := handoff.await(t)
	// The gate was taken after the poll claim, so the first waiter is the voided conclusion.
	gate.awaitWaiters(t, 1)
	go func() {
		_, err := f.h.svc.StopHumanWait(f.h.ctx, wait.ID, stopOperator)
		stopErrs <- err
	}()
	gate.awaitWaiters(t, 2)
	gate.open(t)
	collect(t, "poll", pollErrs)
	stopErr := <-stopErrs
	var conflict *service.HumanWaitConflictError
	if !errors.As(stopErr, &conflict) || !conflict.Resolved() {
		t.Fatalf("stop = %v, want a conflict saying production resolved the wait", stopErr)
	}

	got, _ := f.humanWait(t, action.ID)
	if got.Status != domain.HumanWaitResolved || got.ClosedBy != nil {
		t.Errorf("human wait = %+v, want RESOLVED by production, closed by nobody", got)
	}
	if occupations := f.occupations(t, action.ID); len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased ||
		occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonNotDeployed {
		t.Errorf("occupations = %+v, want one RELEASED NOT_DEPLOYED", occupations)
	}
	if failed := f.eventsOf(t, domain.EventAgentActionFailed, action.ID); len(failed) != 1 || failed[0]["failureSource"] == string(domain.FailureHuman) {
		t.Errorf("AGENT_ACTION_FAILED = %+v, want one that is not a human stop", failed)
	}
	if agentRun := f.agentRun(t); agentRun.Termination != nil && *agentRun.Termination == domain.TerminationHumanStopped {
		t.Errorf("agent run terminated as HUMAN_STOPPED by a refused stop")
	}
}

// Contract case: the wait's deadline and a person's stop contend for the Run lock, and the
// expiry gets it first. The release ends exactly once, as EXPIRED with nobody closing it,
// and the stop that queued behind it is refused as already ended without writing anything.
func TestHumanWaitExpiry_ExpiryAndStopContend_ExpiryFirst_EndsOnceStopRefused(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_expiry_stop_race")
	f.h.clock.Advance(time.Duration(humanWaitBounds.humanWaitTimeoutMs) * time.Millisecond)

	gate, err := lockRunRow(f.h.ctx, f.h.pool, f.run.ID)
	if err != nil {
		t.Fatalf("take the run lock gate: %v", err)
	}
	t.Cleanup(func() { gate.close(context.Background()) })
	expireErrs, stopErrs := make(chan error, 1), make(chan error, 1)
	go func() { expireErrs <- f.h.svc.ExpireHumanWait(f.h.ctx, wait.ID) }()
	gate.awaitWaiters(t, 1)
	go func() {
		_, err := f.h.svc.StopHumanWait(f.h.ctx, wait.ID, stopOperator)
		stopErrs <- err
	}()
	gate.awaitWaiters(t, 2)
	gate.open(t)
	collect(t, "expire", expireErrs)
	stopErr := <-stopErrs
	var conflict *service.HumanWaitConflictError
	if !errors.As(stopErr, &conflict) || conflict.Resolved() || conflict.RunEnded || conflict.Status != domain.HumanWaitExpired {
		t.Fatalf("stop = %v (%+v), want a conflict saying the wait already EXPIRED", stopErr, conflict)
	}

	f.assertEndedReleaseUnknown(t, action, domain.HumanWaitExpired, nil)
	if failed := f.eventsOf(t, domain.EventAgentActionFailed, action.ID); len(failed) != 1 {
		t.Errorf("AGENT_ACTION_FAILED = %d, want exactly one ending", len(failed))
	}
}

// Contract case: an OPEN wait whose Run already ended -- the state a Run left behind before
// it handed the query schedule to the occupation -- is closed by its deadline alone: the
// wait becomes EXPIRED with nobody closing it, the occupation gets its post-task query
// schedule in the same transaction, and the ended Run gets no Event. A stop of such a wait
// is refused as belonging to an ended Run, and the wait list no longer shows it.
func TestHumanWaitExpiry_RunAlreadyEnded_ClosesWaitSchedulesQueryNoEvent(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_expiry_run_ended")
	f.stopWait(t, wait)
	// Put the committed facts back to an OPEN wait of the ended Run whose occupation has no
	// schedule.
	if _, err := f.h.pool.Exec(f.h.ctx, `UPDATE human_waits SET status = 'OPEN', closed_at = NULL, closed_by = NULL, outcome = NULL WHERE id = $1`, wait.ID); err != nil {
		t.Fatalf("reopen the wait: %v", err)
	}
	occupation := f.occupationOf(t, action.ID)
	if _, err := f.h.pool.Exec(f.h.ctx, `UPDATE release_occupations SET next_query_at = NULL WHERE id = $1`, occupation.ID); err != nil {
		t.Fatalf("strip the schedule: %v", err)
	}
	events := len(f.events(t))

	_, stopErr := f.h.svc.StopHumanWait(f.h.ctx, wait.ID, stopOperator)
	var conflict *service.HumanWaitConflictError
	if !errors.As(stopErr, &conflict) || !conflict.RunEnded || conflict.Resolved() {
		t.Fatalf("stop of a wait of an ended run = %v, want a conflict saying the run ended", stopErr)
	}
	views, err := service.NewQueryService(service.Deps{UoW: f.h.uow}).ListOpenHumanWaits(f.h.ctx)
	if err != nil {
		t.Fatalf("list open human waits: %v", err)
	}
	for _, view := range views {
		if view.Wait.ID == wait.ID {
			t.Errorf("the wait list shows a wait of an ended run: %+v", view)
		}
	}

	f.h.clock.Advance(time.Duration(humanWaitBounds.humanWaitTimeoutMs) * time.Millisecond)
	if err := f.h.svc.ExpireHumanWait(f.h.ctx, wait.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	got, _ := f.humanWait(t, action.ID)
	if got.Status != domain.HumanWaitExpired || got.ClosedBy != nil || got.ClosedAt == nil {
		t.Errorf("human wait = %+v, want EXPIRED, closed by nobody", got)
	}
	occupation = f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationActive || occupation.NextQueryAt == nil || !occupation.NextQueryAt.Equal(*got.ClosedAt) {
		t.Errorf("occupation = %s next query %v, want ACTIVE with a query due when the wait closed (%v)", occupation.Status, occupation.NextQueryAt, got.ClosedAt)
	}
	if n := len(f.events(t)); n != events {
		t.Errorf("events = %d after the expiry, want %d: the ended run gets nothing", n, events)
	}
	if n := endedRunOccupationViolations(t, f); n != 0 {
		t.Errorf("ended-run occupations without a query schedule = %d, want 0", n)
	}
}

// Contract case: the process died after the approval committed the STARTED Attempt and
// before the call went out, and the confirm deadline handed the release to a person, who
// stops it. The Attempt fails from STARTED as interrupted with an uncertain outcome and no
// unknown-since time -- nobody asked production yet -- and the target stays held for the
// post-task reconciliation; production received nothing.
func TestHumanStop_AttemptStillStarted_InterruptedUncertainTargetHeld(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds})
	action := f.startToRelease(t, "wf_stop_started_attempt")
	started := f.crashBeforeDispatch(t, action)
	wait := f.openHumanWait(t, action)
	if attempt := f.attempts(t, action.ID)[0]; attempt.Status != domain.ToolAttemptStarted {
		t.Fatalf("attempt when the wait opened = %s, want still STARTED", attempt.Status)
	}
	f.stopWait(t, wait)

	closedBy := stopOperator
	f.assertEndedReleaseUnknown(t, action, domain.HumanWaitStopped, &closedBy)
	attempt := f.attempts(t, action.ID)[0]
	if attempt.ID != started.ID || attempt.DispatchedAt != nil || attempt.OutcomeUnknownSince != nil || attempt.CompletedAt == nil ||
		attempt.Error == nil || attempt.Error.Code != "RELEASE_UNKNOWN" {
		t.Errorf("attempt = %+v, want the STARTED one failed as RELEASE_UNKNOWN, never dispatched, unknown-since unset", attempt)
	}
	if n := f.production.counts.count("POST", deploymentsPath); n != 0 {
		t.Errorf("release requests production received = %d, want none", n)
	}
}
