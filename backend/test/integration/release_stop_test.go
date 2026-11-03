//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
)

// stopOperator is the authenticated identity the stop tests record as the closing person.
const stopOperator = "release-operator"

// unknownOutcomeBounds open the human wait five seconds after an unanswered release and
// let the post-task reconciliation void the operation twenty seconds after production
// stopped knowing it; the human wait itself stays open for half an hour.
var unknownOutcomeBounds = releaseBounds{confirmTimeoutMs: 5_000, voidGraceMs: 20_000, maxReleaseRetries: 1, humanWaitTimeoutMs: 1_800_000}

// stopWait records a person's stop of the release's human wait and fails the test unless
// it took effect.
func (f *releaseFixture) stopWait(t *testing.T, wait domain.HumanWait) service.StopHumanWaitOutcome {
	t.Helper()
	outcome, err := f.h.svc.StopHumanWait(f.h.ctx, wait.ID, stopOperator)
	if err != nil || outcome.Repeated {
		t.Fatalf("stop human wait = %+v, %v, want a stop that took effect", outcome, err)
	}
	return outcome
}

// occupationOf reads the Action's single occupation.
func (f *releaseFixture) occupationOf(t *testing.T, actionID string) domain.ReleaseOccupation {
	t.Helper()
	occupations := f.occupations(t, actionID)
	if len(occupations) != 1 {
		t.Fatalf("occupations of action %s = %+v, want one", actionID, occupations)
	}
	return occupations[0]
}

// assertEndedReleaseUnknown checks what ending a human wait without an answer leaves: the
// wait closed with status and closedBy, the Action FAILED RELEASE_UNKNOWN, its Attempt
// FAILED with an uncertain outcome, the Agent Run ended HUMAN_STOPPED and no longer paused,
// the NodeRun and the Run FAILED, the occupation still ACTIVE with a query due, and no
// release conclusion of any kind.
func (f *releaseFixture) assertEndedReleaseUnknown(t *testing.T, action domain.AgentAction, status domain.HumanWaitStatus, closedBy *string) {
	t.Helper()
	wait, found := f.humanWait(t, action.ID)
	if !found || wait.Status != status || wait.ClosedAt == nil {
		t.Errorf("human wait = %+v (%v), want %s", wait, found, status)
	}
	switch {
	case closedBy == nil && wait.ClosedBy != nil:
		t.Errorf("human wait closed by %q, want nobody", *wait.ClosedBy)
	case closedBy != nil && (wait.ClosedBy == nil || *wait.ClosedBy != *closedBy):
		t.Errorf("human wait closed by %v, want %q", wait.ClosedBy, *closedBy)
	}
	got := f.action(t, action.ID)
	if got.Status != domain.AgentActionFailed || got.Error == nil || got.Error.Code != "RELEASE_UNKNOWN" {
		t.Errorf("action = %s %+v, want FAILED RELEASE_UNKNOWN", got.Status, got.Error)
	}
	attempts := f.attempts(t, action.ID)
	last := attempts[len(attempts)-1]
	if last.Status != domain.ToolAttemptFailed || last.Uncertain == nil || !*last.Uncertain {
		t.Errorf("release attempt = %s uncertain %v, want FAILED with an uncertain outcome", last.Status, last.Uncertain)
	}
	agentRun := f.agentRun(t)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationHumanStopped {
		t.Errorf("agent termination = %s, want HUMAN_STOPPED", agentTermination(agentRun))
	}
	if agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after it ended", agentRun.PauseReason)
	}
	if nodeRun := f.nodeRun(t); nodeRun.Status != domain.NodeRunFailed || nodeRun.Error == nil || nodeRun.Error.Code != "RELEASE_UNKNOWN" {
		t.Errorf("agent node run = %s %+v, want FAILED RELEASE_UNKNOWN", nodeRun.Status, nodeRun.Error)
	}
	if run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID); run.Status != domain.RunFailed || run.Error == nil || run.Error.Code != runtime.CodeReleaseUnknown {
		t.Errorf("run = %s %+v, want FAILED %s", run.Status, run.Error, runtime.CodeReleaseUnknown)
	}
	completion := completionOf(t, f.h, agentRun.ID)
	if completion == nil || completion.Outcome != domain.CompletionReleaseUnknown {
		t.Fatalf("completion verdict = %+v, want RELEASE_UNKNOWN", completion)
	}
	if wait.ClosedAt != nil && !completion.EvaluatedAt.Equal(*wait.ClosedAt) {
		t.Errorf("verdict evaluated at %s, wait closed at %s: want the instant of the one ending transaction", completion.EvaluatedAt, *wait.ClosedAt)
	}
	// The ending transaction appends the Action failure, the Agent failure and the verdict
	// as consecutive Events under one Run lock, all stamped with that transaction's instant.
	events := f.events(t)
	var evaluated []domain.Event
	for _, event := range events {
		if event.Type == domain.EventAgentCompletionEvaluated {
			evaluated = append(evaluated, event)
		}
	}
	if len(evaluated) != 1 {
		t.Fatalf("AGENT_COMPLETION_EVALUATED events = %d, want exactly one", len(evaluated))
	}
	payload := agentLastEventPayload(t, events, domain.EventAgentCompletionEvaluated)
	if payload["outcome"] != string(domain.CompletionReleaseUnknown) || payload["agentRunId"] != agentRun.ID {
		t.Errorf("AGENT_COMPLETION_EVALUATED payload = %v, want the RELEASE_UNKNOWN verdict of %s", payload, agentRun.ID)
	}
	bySeq := make(map[int64]domain.Event, len(events))
	for _, event := range events {
		bySeq[event.Seq] = event
	}
	verdict := evaluated[0]
	agentFailed, actionFailed := bySeq[verdict.Seq-1], bySeq[verdict.Seq-2]
	if agentFailed.Type != domain.EventAgentFailed || actionFailed.Type != domain.EventAgentActionFailed ||
		!agentFailed.Timestamp.Equal(verdict.Timestamp) || !actionFailed.Timestamp.Equal(verdict.Timestamp) {
		t.Errorf("events before the verdict = %s@%s, %s@%s; want AGENT_ACTION_FAILED then AGENT_FAILED at %s",
			actionFailed.Type, actionFailed.Timestamp, agentFailed.Type, agentFailed.Timestamp, verdict.Timestamp)
	}
	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationActive || occupation.NextQueryAt == nil {
		t.Errorf("occupation = %s next query %v, want ACTIVE with a query scheduled", occupation.Status, occupation.NextQueryAt)
	}
	if facts := f.releaseFacts(t); len(facts) != 0 {
		t.Errorf("release facts = %+v, want none: nothing was confirmed", facts)
	}
	if completed := f.eventsOf(t, domain.EventAgentActionCompleted, action.ID); len(completed) != 0 {
		t.Errorf("AGENT_ACTION_COMPLETED = %d, want none", len(completed))
	}
	failed := f.eventsOf(t, domain.EventAgentActionFailed, action.ID)
	if len(failed) != 1 || failed[0]["failureSource"] != string(domain.FailureHuman) || failed[0]["humanWaitId"] != wait.ID {
		t.Errorf("AGENT_ACTION_FAILED = %+v, want one HUMAN failure naming the wait", failed)
	}
}

// postReconcile advances the clock past the poll interval and runs one reconciliation
// pass, which fails the test on any error the pass collected.
func (f *releaseFixture) postReconcile(t *testing.T) reconciler.Report {
	t.Helper()
	f.h.clock.Advance(releasePollStep)
	report := agentRunOnce(f.h, f.reconciler())
	if len(report.Errors) != 0 {
		t.Fatalf("reconciliation pass errors = %v", report.Errors)
	}
	return report
}

// startToHumanWait approves a release production keeps DEPLOYING and lets the confirm
// deadline hand it to a person.
func (f *releaseFixture) startToHumanWait(t *testing.T, workflowID string) (domain.AgentAction, domain.HumanWait) {
	t.Helper()
	action := f.startToRelease(t, workflowID)
	f.production.server.PauseCompletion()
	f.approve(t, action)
	return action, f.openHumanWait(t, action)
}

// Contract case: a person's stop and production's DEPLOYED answer contend for the Run lock,
// and the stop gets it first. The task ends as HUMAN_STOPPED with the release unknown and
// the target occupied; the poll's conclusion then finds the Attempt closed and writes
// nothing, and the answer reaches the occupation only through the post-task
// reconciliation, which frees it without touching the ended Run.
func TestHumanStop_StopAndAnswerContend_StopFirst_TaskStoppedAnswerOnlyReconciles(t *testing.T) {
	handoff := newGateHandoff()
	stopErrs := make(chan error, 1)
	var (
		f      *releaseFixture
		waitID string
		target string
	)
	hooks := service.PollHooks{AfterClaim: func(ctx context.Context, attemptID string) error {
		if attemptID != target {
			return nil
		}
		// The stop queues on the Run lock first; the poll goes on to its conclusion only
		// once the stop waits there.
		handoff.take(ctx, f.h.pool, f.run.ID, func(gate *runLockGate) error {
			go func() {
				_, err := f.h.svc.StopHumanWait(ctx, waitID, stopOperator)
				stopErrs <- err
			}()
			return gate.waitForWaiters(ctx, 1)
		})
		return nil
	}}
	f = newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds, pollHooks: hooks})
	action, wait := f.startToHumanWait(t, "wf_stop_race_stop_first")
	waitID, target = wait.ID, f.attempts(t, action.ID)[0].ID
	f.production.server.ResumeCompletion()

	pollErrs := make(chan error, 1)
	f.h.clock.Advance(releasePollStep)
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
	if status := f.production.operationStatus(t, domain.ReleaseOperationKey(action.ID, 1)); status != "DEPLOYED" {
		t.Fatalf("production operation = %q, want DEPLOYED: the answer did exist", status)
	}
	events := len(f.events(t))

	f.postReconcile(t)
	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonDeployed {
		t.Errorf("occupation after the reconciliation = %+v, want RELEASED DEPLOYED", occupation)
	}
	if got := len(f.events(t)); got != events {
		t.Errorf("events after the reconciliation = %d, want %d: the ended Run gets nothing", got, events)
	}
	if agentRun := f.agentRun(t); agentRun.Termination == nil || *agentRun.Termination != domain.TerminationHumanStopped {
		t.Errorf("agent termination after the reconciliation = %s, want HUMAN_STOPPED unchanged", agentTermination(agentRun))
	}
}

// Contract case: production's DEPLOYED answer and a person's stop contend for the Run lock,
// and the answer gets it first. The release concludes and resolves the wait; the stop is
// refused as resolved and writes nothing.
func TestHumanStop_StopAndAnswerContend_AnswerFirst_ReleaseConcludesStopRefused(t *testing.T) {
	handoff := newGateHandoff()
	var (
		f      *releaseFixture
		target string
	)
	hooks := service.PollHooks{AfterClaim: func(ctx context.Context, attemptID string) error {
		if attemptID == target {
			handoff.take(ctx, f.h.pool, f.run.ID, nil)
		}
		return nil
	}}
	f = newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds, pollHooks: hooks})
	action, wait := f.startToHumanWait(t, "wf_stop_race_answer_first")
	target = f.attempts(t, action.ID)[0].ID
	f.production.server.ResumeCompletion()

	pollErrs, stopErrs := make(chan error, 1), make(chan error, 1)
	f.h.clock.Advance(releasePollStep)
	go func() {
		_, err := f.h.svc.PollToolAttempt(f.h.ctx, service.PollToolAttempt{AttemptID: target, ActionID: action.ID})
		pollErrs <- err
	}()
	gate := handoff.await(t)
	// The gate was taken after the poll claim, so the first waiter is the conclusion.
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
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}

	f.assertDeployedOnce(t, action, domain.ReleaseOperationKey(action.ID, 1))
	got, _ := f.humanWait(t, action.ID)
	if got.Status != domain.HumanWaitResolved || got.ClosedBy != nil {
		t.Errorf("human wait = %+v, want RESOLVED by production, closed by nobody", got)
	}
	f.finish(t)
}

// Contract case: after a stop the target stays occupied, so another task's release call of
// the same target is handed back as TARGET_OCCUPIED before any approval.
func TestHumanStop_AfterStop_NewTaskReleaseReturnedTargetOccupied(t *testing.T) {
	a := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	actionA, wait := a.startToHumanWait(t, "wf_stop_held_a")
	a.stopWait(t, wait)

	b := a.secondTask(t, service.ApprovalHooks{})
	b.start(t, "wf_stop_held_b", verifyBaselineCall(), func() mockmodel.Scenario {
		return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), b.instructionsBasis(t)))
	}, b.releaseCall(t))
	b.deliverVerification(t, true)

	action := b.actionOfTurn(t, releaseTurn)
	b.assertReturnedBeforeAttempt(t, action, releaseTurn, domain.ReturnTargetOccupied)
	if b.hasApproval(t, action.ID) {
		t.Error("an approval was requested for a release of a target a stopped task still occupies")
	}
	if occupation, found := a.activeOccupation(t); !found || occupation.ActionID != actionA.ID {
		t.Errorf("active occupation = %+v (%v), want the stopped task's", occupation, found)
	}
	b.finish(t)
}

// Contract case: production is unreachable for the whole confirm deadline; the person stops
// waiting and the target stays occupied. Once production is back and the void grace has
// passed, the post-task reconciliation asks production to void the operation it never
// received, which frees the target as VOIDED, and a new task releases to the same target.
func TestPostReconcile_ProductionRecoversAndVoids_TargetFreedNewTaskReleases(t *testing.T) {
	a := newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds})
	actionA := a.startToRelease(t, "wf_post_void_a")
	a.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	a.approve(t, actionA)
	a.assertOutcomeUnknown(t, actionA)
	wait := a.openHumanWait(t, actionA)
	a.stopWait(t, wait)
	closedBy := stopOperator
	a.assertEndedReleaseUnknown(t, actionA, domain.HumanWaitStopped, &closedBy)

	// Still unreachable: the reconciliation gets no answer and keeps the target.
	if report := a.postReconcile(t); report.OccupationQueriesClaimed != 1 || report.OccupationsReleased != 0 {
		t.Fatalf("reconciliation while production is down = claimed %d released %d, want 1/0", report.OccupationQueriesClaimed, report.OccupationsReleased)
	}
	if occupation := a.occupationOf(t, actionA.ID); occupation.Status != domain.ReleaseOccupationActive || occupation.QueryCount != 1 {
		t.Fatalf("occupation while production is down = %s after %d queries, want ACTIVE after one", occupation.Status, occupation.QueryCount)
	}

	a.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})
	a.h.clock.Advance(time.Duration(unknownOutcomeBounds.voidGraceMs) * time.Millisecond)
	a.postReconcile(t)
	occupation := a.occupationOf(t, actionA.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonVoided || occupation.DeployedUncheckedVersion != nil {
		t.Fatalf("occupation after production voided = %+v, want RELEASED VOIDED with no live version", occupation)
	}
	key := domain.ReleaseOperationKey(actionA.ID, 1)
	if status := a.production.operationStatus(t, key); status != "VOIDED" {
		t.Errorf("production operation %s = %q, want VOIDED", key, status)
	}
	if n := a.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests production received = %d, want only the unanswered one: nothing is released again", n)
	}

	b := a.secondTask(t, service.ApprovalHooks{})
	actionB := b.startToRelease(t, "wf_post_void_b")
	b.approve(t, actionB)
	b.poll(t, b.reconciler())
	b.assertDeployedOnce(t, actionB, domain.ReleaseOperationKey(actionB.ID, 1))
	b.finish(t)
}

// Contract case: after the stop production reports the operation DEPLOYED. The post-task
// reconciliation frees the target as DEPLOYED and records the version production runs as
// live but never health-checked; the ended Run gets no Event and no state change.
func TestPostReconcile_DeployedAfterStop_ReleasedWithUncheckedVersionNoRunEvents(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_post_deployed")
	f.stopWait(t, wait)
	events := len(f.events(t))
	run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID)

	f.production.server.ResumeCompletion()
	f.postReconcile(t)

	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonDeployed {
		t.Fatalf("occupation = %+v, want RELEASED DEPLOYED", occupation)
	}
	target, ok := f.production.target(t)
	if !ok {
		t.Fatal("production reports no deployment on the target")
	}
	if occupation.DeployedUncheckedVersion == nil || *occupation.DeployedUncheckedVersion != target["version"] {
		t.Errorf("unchecked version = %v, want the live version %v", occupation.DeployedUncheckedVersion, target["version"])
	}
	if occupation.NextQueryAt != nil {
		t.Errorf("released occupation still schedules a query at %s", occupation.NextQueryAt)
	}
	if got := len(f.events(t)); got != events {
		t.Errorf("events = %d, want %d: the ended Run gets nothing", got, events)
	}
	if after := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID); after.Status != run.Status || after.LastSeq != run.LastSeq {
		t.Errorf("run after the reconciliation = %s seq %d, want %s seq %d unchanged", after.Status, after.LastSeq, run.Status, run.LastSeq)
	}
	if facts := f.releaseFacts(t); len(facts) != 0 {
		t.Errorf("release facts = %+v, want none: the task never checked the deployment", facts)
	}
}

// Contract case: the process restarts after the stop. The new process has nothing in
// memory; the occupation's persisted schedule alone lets its Reconciler find the work and
// free the target once production answers.
func TestPostReconcile_RestartAfterStop_ContinuesFromPersistedSchedule(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_post_restart")
	f.stopWait(t, wait)

	restarted := f.restart(t, service.PollHooks{})
	f.production.server.ResumeCompletion()
	restarted.postReconcile(t)

	occupation := restarted.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonDeployed || occupation.DeployedUncheckedVersion == nil {
		t.Errorf("occupation after the restart = %+v, want RELEASED DEPLOYED with the live version", occupation)
	}
}

// Contract case: two Reconcilers scan the same due occupation at once. Both queue on the
// Run lock behind a barrier; the conditional claim of the schedule lets exactly one of them
// ask production.
func TestPostReconcile_TwoScannersSameOccupation_OnlyOneQueriesProduction(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_post_two_scanners")
	f.stopWait(t, wait)
	occupation := f.occupationOf(t, action.ID)
	path := deploymentsPath + "/" + occupation.OperationKey
	before := f.production.counts.count("GET", path)
	f.h.clock.Advance(releasePollStep)

	gate, err := lockRunRow(f.h.ctx, f.h.pool, f.run.ID)
	if err != nil {
		t.Fatalf("take the run lock gate: %v", err)
	}
	t.Cleanup(func() { gate.close(context.Background()) })
	outcomes := make(chan service.ReleaseReconcileOutcome, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			outcome, err := f.h.svc.ReconcileReleaseOccupation(f.h.ctx, occupation.ID)
			outcomes <- outcome
			errs <- err
		}()
	}
	gate.awaitWaiters(t, 2)
	gate.open(t)
	claimed := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Errorf("reconcile: %v", err)
		}
		if (<-outcomes).Claimed {
			claimed++
		}
	}
	if claimed != 1 {
		t.Errorf("claimed queries = %d, want exactly 1", claimed)
	}
	if n := f.production.counts.count("GET", path) - before; n != 1 {
		t.Errorf("production queries of %s = %d, want exactly 1", occupation.OperationKey, n)
	}
	if after := f.occupationOf(t, action.ID); after.QueryCount != 1 {
		t.Errorf("query count = %d, want 1", after.QueryCount)
	}
}

// Contract case: nobody stops the wait and its own deadline passes. The Reconciler ends it
// exactly as a stop would, as EXPIRED and closed by nobody.
func TestHumanWaitExpiry_DeadlinePassed_EndsAsStopWithoutOperator(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_wait_expiry")

	f.h.clock.Advance(time.Duration(humanWaitBounds.humanWaitTimeoutMs) * time.Millisecond)
	report := agentRunOnce(f.h, f.reconciler())
	if report.ExpiredHumanWaitsFound != 1 || report.HumanWaitsExpired != 1 {
		t.Fatalf("expiry report = found %d expired %d, want 1/1", report.ExpiredHumanWaitsFound, report.HumanWaitsExpired)
	}
	f.assertEndedReleaseUnknown(t, action, domain.HumanWaitExpired, nil)

	// The person arrives too late: the wait already ended.
	_, err := f.h.svc.StopHumanWait(f.h.ctx, wait.ID, stopOperator)
	var conflict *service.HumanWaitConflictError
	if !errors.As(err, &conflict) || conflict.Resolved() || conflict.Status != domain.HumanWaitExpired {
		t.Errorf("late stop = %v, want a conflict saying the wait expired", err)
	}
}

// A repeated stop of the same wait is answered from the committed stop and writes nothing.
func TestHumanStop_RepeatedStop_RepeatedWithoutWrites(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	_, wait := f.startToHumanWait(t, "wf_stop_repeated")
	f.stopWait(t, wait)
	events := len(f.events(t))

	outcome, err := f.h.svc.StopHumanWait(f.h.ctx, wait.ID, stopOperator)
	if err != nil || !outcome.Repeated || outcome.Wait.Status != domain.HumanWaitStopped {
		t.Fatalf("repeated stop = %+v, %v, want the committed STOPPED wait as repeated", outcome, err)
	}
	if got := len(f.events(t)); got != events {
		t.Errorf("events after the repeated stop = %d, want %d", got, events)
	}
}

// endedRunOccupationViolations is the invariant report's check that every ACTIVE
// occupation of an ended Run carries a post-task query schedule, as the same predicate.
func endedRunOccupationViolations(t *testing.T, f *releaseFixture) int {
	t.Helper()
	var n int
	if err := f.h.pool.QueryRow(f.h.ctx, `
		SELECT count(*)
		  FROM release_occupations o
		  JOIN runs r ON r.id = o.run_id
		 WHERE o.status = 'ACTIVE'
		   AND r.status IN ('COMPLETED', 'FAILED')
		   AND o.next_query_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("invariant query: %v", err)
	}
	return n
}

// Invariant: a Run that ended while its release outcome is unknown leaves its ACTIVE
// occupation with a post-task query schedule; the check does report an occupation that
// lacks one.
func TestReleaseInvariant_EndedRunActiveOccupation_HasQuerySchedule(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_invariant_ended_occupation")
	f.stopWait(t, wait)
	if n := endedRunOccupationViolations(t, f); n != 0 {
		t.Fatalf("ended-run occupations without a query schedule = %d, want 0", n)
	}

	// The check is not vacuous: an occupation stripped of its schedule is reported.
	occupation := f.occupationOf(t, action.ID)
	if _, err := f.h.pool.Exec(f.h.ctx, `UPDATE release_occupations SET next_query_at = NULL WHERE id = $1`, occupation.ID); err != nil {
		t.Fatalf("strip the schedule: %v", err)
	}
	if n := endedRunOccupationViolations(t, f); n != 1 {
		t.Errorf("ended-run occupations without a query schedule = %d, want the stripped one", n)
	}
}

// The store's claim of a post-task query is conditional on the schedule being due: a claim
// at an instant before next_query_at, or of an occupation already released, writes nothing.
func TestReleaseOccupationStore_ClaimQuery_OnlyDueActiveOccupationClaimed(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_store_claim_query")
	f.stopWait(t, wait)
	occupation := f.occupationOf(t, action.ID)
	now := *occupation.NextQueryAt

	claim := func(at time.Time) bool {
		var claimed bool
		if err := f.h.uow.WithinTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			claimed, err = tx.ReleaseOccupations().ClaimQuery(ctx, occupation.ID, at, at.Add(time.Minute))
			return err
		}); err != nil {
			t.Fatalf("claim query: %v", err)
		}
		return claimed
	}
	if claim(now.Add(-time.Second)) {
		t.Error("a query was claimed before it was due")
	}
	if !claim(now) {
		t.Fatal("the due query was not claimed")
	}
	if claim(now) {
		t.Error("the same due time was claimed twice")
	}
	if err := f.h.uow.WithinTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		released, err := tx.ReleaseOccupations().ReleaseAfterRun(ctx, occupation.ID, domain.ReleaseReasonVoided, now, nil)
		if err == nil && !released {
			err = errors.New("the ACTIVE occupation was not released")
		}
		return err
	}); err != nil {
		t.Fatalf("release after run: %v", err)
	}
	if claim(now.Add(time.Hour)) {
		t.Error("a released occupation's query was claimed")
	}
}
