//go:build integration

package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/service"
)

// recoveryBounds make the void grace short enough to drive and keep every other wait away.
var recoveryBounds = releaseBounds{confirmTimeoutMs: 3_600_000, voidGraceMs: 10_000, maxReleaseRetries: 1, humanWaitTimeoutMs: 3_600_000}

const (
	deploymentsPath = "/v2/deployments"
	voidSuffix      = "/void"
)

// deployBody is the request the release Tool sends, for a test that plays the request
// production received from a process that died before learning the answer.
func deployBody(operationKey, baseCommit string) string {
	return fmt.Sprintf(`{"operationId":%q,"target":{"service":%q,"environment":%q},"baseCommit":%q,"patchDigest":%q}`,
		operationKey, releaseService, releaseEnvironment, baseCommit, baselineDigest())
}

// crashBeforeDispatch approves the release and loses the process before any worker made
// the call: the approval transaction committed the STARTED Attempt, the occupation and the
// release Binding, and nothing else happened.
func (f *releaseFixture) crashBeforeDispatch(t *testing.T, action domain.AgentAction) domain.ToolAttempt {
	t.Helper()
	f.decide(t, action)
	f.dropQueuedActions()
	attempts := f.attempts(t, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Fatalf("attempts after the approval = %+v, want one STARTED", attempts)
	}
	if _, found := f.activeOccupation(t); !found {
		t.Fatal("the approval did not occupy the target")
	}
	return attempts[0]
}

// assertOutcomeUnknown checks that the Attempt entered the query state once: DISPATCHED
// with outcome_unknown_since, a due poll, one RELEASE_OUTCOME_UNKNOWN Event and the Action
// waiting on production.
func (f *releaseFixture) assertOutcomeUnknown(t *testing.T, action domain.AgentAction) domain.ToolAttempt {
	t.Helper()
	attempt := f.attempts(t, action.ID)[0]
	if attempt.Status != domain.ToolAttemptDispatched || attempt.OutcomeUnknownSince == nil || attempt.NextPollAt == nil {
		t.Fatalf("attempt = %s unknownSince %v nextPoll %v, want DISPATCHED with both set", attempt.Status, attempt.OutcomeUnknownSince, attempt.NextPollAt)
	}
	if unknown := f.eventsOf(t, domain.EventReleaseOutcomeUnknown, action.ID); len(unknown) != 1 || unknown[0]["operationKey"] != operationKey(attempt) {
		t.Errorf("RELEASE_OUTCOME_UNKNOWN events = %+v, want exactly one naming %s", unknown, operationKey(attempt))
	}
	if got := f.action(t, action.ID); got.Status != domain.AgentActionWaitingCallback {
		t.Errorf("action = %s, want WAITING_CALLBACK", got.Status)
	}
	return attempt
}

// assertVoidedAndReleasedAgain checks the void's outcome when a retry is left: the first
// Attempt FAILED with the VOIDED code and a definite outcome, a second Attempt under the
// next operation key, the occupation moved to that key and RELEASE_VOIDED naming it.
func (f *releaseFixture) assertVoidedAndReleasedAgain(t *testing.T, action domain.AgentAction) domain.ToolAttempt {
	t.Helper()
	attempts := f.attempts(t, action.ID)
	if len(attempts) != 2 {
		t.Fatalf("attempts after the void = %d, want 2", len(attempts))
	}
	first, second := attempts[0], attempts[1]
	if first.Status != domain.ToolAttemptFailed || first.Error == nil || first.Error.Code != domain.ReleaseCodeVoided || first.Uncertain == nil || *first.Uncertain {
		t.Errorf("voided attempt = %s %+v uncertain %v, want FAILED VOIDED with a definite outcome", first.Status, first.Error, first.Uncertain)
	}
	wantKey := domain.ReleaseOperationKey(action.ID, 2)
	if operationKey(second) != wantKey || second.Status != domain.ToolAttemptDispatched || second.OutcomeUnknownSince != nil {
		t.Errorf("re-release attempt = %s key %q unknownSince %v, want DISPATCHED %s with a known outcome", second.Status, operationKey(second), second.OutcomeUnknownSince, wantKey)
	}
	occupation, found := f.activeOccupation(t)
	if !found || occupation.ActionID != action.ID || occupation.OperationKey != wantKey {
		t.Errorf("active occupation = %+v (%v), want the action's, keyed %s", occupation, found, wantKey)
	}
	voided := f.eventsOf(t, domain.EventReleaseVoided, action.ID)
	if len(voided) != 1 || voided[0]["nextOperationKey"] != wantKey || voided[0]["operationKey"] != operationKey(first) {
		t.Errorf("RELEASE_VOIDED = %+v, want one naming %s and the next key %s", voided, operationKey(first), wantKey)
	}
	return second
}

// assertDeployedOnce checks the conclusion: the Action SUCCEEDED, its occupation RELEASED
// as DEPLOYED, one healthy fact, and production's target running the operation key.
func (f *releaseFixture) assertDeployedOnce(t *testing.T, action domain.AgentAction, wantKey string) {
	t.Helper()
	if got := f.action(t, action.ID); got.Status != domain.AgentActionSucceeded {
		t.Errorf("action = %s %+v, want SUCCEEDED", got.Status, got.Error)
	}
	if _, found := f.activeOccupation(t); found {
		t.Error("the target is still occupied after the conclusion")
	}
	occupations := f.occupations(t, action.ID)
	if len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased || occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonDeployed {
		t.Errorf("occupations = %+v, want one RELEASED DEPLOYED", occupations)
	}
	if facts := f.releaseFacts(t); len(facts) != 1 || facts[0].Verdict == nil || !*facts[0].Verdict {
		t.Errorf("release facts = %+v, want one healthy fact", facts)
	}
	target, ok := f.production.target(t)
	if !ok || target["lastOperationId"] != wantKey {
		t.Errorf("production target = %+v (%v), want lastOperationId %s", target, ok, wantKey)
	}
}

// Contract case: the process dies after the approval transaction and before the dispatch;
// the restarted process finds the STARTED Attempt of a runner that is gone and enters the
// query state instead of sending the request again. Production never saw the operation, so
// after the void grace the Runtime voids it and releases again under the next key.
func TestRelease_CrashBeforeDispatch_OrphanQueriesThenVoidsAndReleasesAgain(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_crash_void")
	f.crashBeforeDispatch(t, action)

	r := f.restart(t, service.PollHooks{})
	rec := r.reconciler()
	report := agentRunOnce(r.h, rec)
	if report.OrphanToolAttemptsFound != 1 || report.OrphanToolAttemptsHandled != 1 {
		t.Fatalf("orphan report = found %d handled %d, want 1/1", report.OrphanToolAttemptsFound, report.OrphanToolAttemptsHandled)
	}
	r.assertOutcomeUnknown(t, action)
	if n := r.production.counts.count("POST", deploymentsPath); n != 0 {
		t.Fatalf("the orphan handling sent %d release request(s), want none", n)
	}

	// Inside the grace a 404 is only an unknown outcome: no resend and no void yet.
	r.poll(t, rec)
	if n := r.production.counts.count("POST", deploymentsPath+"/"+domain.ReleaseOperationKey(action.ID, 1)+voidSuffix); n != 0 {
		t.Fatalf("void requests inside the grace = %d, want none", n)
	}
	if attempts := r.attempts(t, action.ID); len(attempts) != 1 {
		t.Fatalf("attempts inside the grace = %d, want 1", len(attempts))
	}

	r.h.clock.Advance(time.Duration(recoveryBounds.voidGraceMs) * time.Millisecond)
	r.poll(t, rec)
	second := r.assertVoidedAndReleasedAgain(t, action)
	if n := r.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests after the re-release = %d, want exactly the second operation's", n)
	}

	r.poll(t, rec)
	r.assertDeployedOnce(t, action, operationKey(second))
	r.finish(t)
}

// Contract case: the process dies after the approval transaction, but its request did reach
// production. The restarted process queries the operation, learns DEPLOYED and concludes
// without a second request.
func TestRelease_CrashBeforeDispatch_RequestReachedProduction_ConcludesWithoutResend(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_crash_deployed")
	attempt := f.crashBeforeDispatch(t, action)
	if status := f.production.post(t, deploymentsPath, deployBody(operationKey(attempt), f.repo.commit)); status != 202 {
		t.Fatalf("the played request answered %d, want 202", status)
	}

	r := f.restart(t, service.PollHooks{})
	rec := r.reconciler()
	// The orphan handling makes the poll due at once, so the same pass queries production.
	report := agentRunOnce(r.h, rec)
	if report.OrphanToolAttemptsHandled != 1 {
		t.Fatalf("orphan report = %+v, want one handled", report)
	}
	if unknown := r.eventsOf(t, domain.EventReleaseOutcomeUnknown, action.ID); len(unknown) != 1 {
		t.Errorf("RELEASE_OUTCOME_UNKNOWN events = %+v, want exactly one", unknown)
	}
	if err := r.h.drainTurns(r.h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}
	if attempts := r.attempts(t, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Fatalf("attempts = %+v, want the one Attempt SUCCEEDED", attempts)
	}
	r.assertDeployedOnce(t, action, operationKey(attempt))
	if n := r.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests = %d, want only the one that reached production", n)
	}
	r.finish(t)
}

// Contract case: production accepted and completed the deployment but the response was
// lost. The dispatch enters the query state and the poll learns DEPLOYED.
func TestRelease_ResponseDropped_Deployed_ConcludesFromQuery(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_drop_deployed")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{DropResponse: true})

	f.approve(t, action)
	attempt := f.assertOutcomeUnknown(t, action)
	if n := f.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Fatalf("release requests = %d, want 1", n)
	}

	rec := f.reconciler()
	f.poll(t, rec)
	f.assertDeployedOnce(t, action, operationKey(attempt))
	if n := f.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests after the conclusion = %d, want no resend", n)
	}
	f.finish(t)
}

// Contract case: the process dies after its request reached production and before the
// answer came back: production accepted and completed the deployment, the response was
// dropped. The restarted process holds only the committed query state, polls, learns
// DEPLOYED and concludes without a second request.
func TestRelease_ResponseDroppedThenRestart_Deployed_ConcludesWithoutResend(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_drop_restart_deployed")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{DropResponse: true})
	f.approve(t, action)
	attempt := f.assertOutcomeUnknown(t, action)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})

	r := f.restart(t, service.PollHooks{})
	r.poll(t, r.reconciler())
	r.assertDeployedOnce(t, action, operationKey(attempt))
	if n := r.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests = %d, want only the one whose answer was dropped", n)
	}
	if attempts := r.attempts(t, action.ID); len(attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(attempts))
	}
	r.finish(t)
}

// Contract case: the process dies after production accepted the operation and before the
// answer came back, and the operation has not started yet. The restarted process sees it
// in flight inside the void grace, voids it past the grace, production confirms VOIDED and
// cancels it, and the re-release under the next key deploys; the first operation never
// deploys.
func TestRelease_ResponseDroppedThenRestart_AcceptedNotStarted_VoidedAndReleasedAgain(t *testing.T) {
	production := newProductionFixture(t, mockproduction.WithCompletionDelay(time.Hour))
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds, production: production})
	action := f.startToRelease(t, "wf_release_drop_restart_voided")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{DropResponse: true})
	f.approve(t, action)
	first := f.assertOutcomeUnknown(t, action)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})
	if status := f.production.operationStatus(t, operationKey(first)); status != "ACCEPTED" {
		t.Fatalf("first operation = %q, want ACCEPTED and not started", status)
	}

	r := f.restart(t, service.PollHooks{})
	rec := r.reconciler()
	r.poll(t, rec)
	if attempts := r.attempts(t, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptDispatched || attempts[0].VoidRequestedAt != nil {
		t.Fatalf("attempts inside the grace = %+v, want the one DISPATCHED and not voided", attempts)
	}

	// The re-release completes in the request that accepts it.
	r.production.server.SetCompletionDelay(0)
	r.h.clock.Advance(time.Duration(recoveryBounds.voidGraceMs) * time.Millisecond)
	r.poll(t, rec)
	second := r.assertVoidedAndReleasedAgain(t, action)
	if status := r.production.operationStatus(t, operationKey(first)); status != "VOIDED" {
		t.Errorf("first operation = %q, want VOIDED", status)
	}

	r.poll(t, rec)
	r.assertDeployedOnce(t, action, operationKey(second))
	if status := r.production.operationStatus(t, operationKey(first)); status != "VOIDED" {
		t.Errorf("first operation after the re-release = %q, want still VOIDED", status)
	}
	if n := r.production.counts.count("POST", deploymentsPath); n != 2 {
		t.Errorf("release requests = %d, want the dropped one and the re-release", n)
	}
	r.finish(t)
}

// Contract case: the dispatch got no usable answer (production was unreachable) and the
// operation never reached it. After the grace the Runtime voids the operation, production
// confirms VOIDED with a tombstone, and the re-release deploys. (An operation production
// accepted is one it completes; a void is confirmed only for one it never started.)
func TestRelease_DispatchUnanswered_Voided_ReleasesAgain(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_unanswered_voided")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})

	f.approve(t, action)
	f.assertOutcomeUnknown(t, action)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})

	rec := f.reconciler()
	f.poll(t, rec)
	if attempts := f.attempts(t, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptDispatched {
		t.Fatalf("attempts inside the grace = %+v, want the one DISPATCHED", attempts)
	}

	f.h.clock.Advance(time.Duration(recoveryBounds.voidGraceMs) * time.Millisecond)
	f.poll(t, rec)
	second := f.assertVoidedAndReleasedAgain(t, action)

	f.poll(t, rec)
	f.assertDeployedOnce(t, action, operationKey(second))
	f.finish(t)
}

// Contract case: an unknown operation inside the void grace is neither resent nor voided;
// the Attempt stays in the query state and the target stays occupied.
func TestRelease_UnknownOperationInsideGrace_NoResendNoVoid(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_unknown_grace")
	attempt := f.crashBeforeDispatch(t, action)

	r := f.restart(t, service.PollHooks{})
	rec := r.reconciler()
	agentRunOnce(r.h, rec)
	for i := 0; i < 2; i++ {
		r.poll(t, rec)
	}

	after := r.attempts(t, action.ID)
	if len(after) != 1 || after[0].Status != domain.ToolAttemptDispatched || after[0].VoidRequestedAt != nil {
		t.Fatalf("attempts = %+v, want the one still DISPATCHED without a void request", after)
	}
	if n := r.production.counts.count("POST", deploymentsPath); n != 0 {
		t.Errorf("release requests = %d, want none", n)
	}
	if n := r.production.counts.count("POST", deploymentsPath+"/"+operationKey(attempt)+voidSuffix); n != 0 {
		t.Errorf("void requests = %d, want none inside the grace", n)
	}
	if occupation, found := r.activeOccupation(t); !found || occupation.ActionID != action.ID {
		t.Errorf("active occupation = %+v (%v), want the action still holding the target", occupation, found)
	}
	if agentRun := r.agentRun(t); agentRun.PausedAt == nil || agentRun.PauseReason == nil || *agentRun.PauseReason != domain.PauseRelease {
		t.Errorf("agent run = paused %v reason %v, want still paused RELEASE", agentRun.PausedAt, agentRun.PauseReason)
	}
}

// Contract case: after the void, the old operation's request arrives late at production and
// is refused as VOIDED; the new operation deploys and the target names it.
func TestRelease_LateRequestAfterVoid_RefusedWhileNewOperationDeploys(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_late_request")
	first := f.crashBeforeDispatch(t, action)

	r := f.restart(t, service.PollHooks{})
	rec := r.reconciler()
	agentRunOnce(r.h, rec)
	r.h.clock.Advance(time.Duration(recoveryBounds.voidGraceMs) * time.Millisecond)
	r.poll(t, rec)
	second := r.assertVoidedAndReleasedAgain(t, action)

	if status := r.production.post(t, deploymentsPath, deployBody(operationKey(first), r.repo.commit)); status != 409 {
		t.Errorf("the late request answered %d, want 409", status)
	}

	r.poll(t, rec)
	r.assertDeployedOnce(t, action, operationKey(second))
	r.finish(t)
}

// Contract case: with no re-release left, a confirmed void returns RELEASE_NOT_DEPLOYED to
// the Agent and frees the target as NOT_DEPLOYED; the Agent continues.
func TestRelease_VoidedWithNoRetryLeft_ReturnsNotDeployedAndReleasesTarget(t *testing.T) {
	bounds := recoveryBounds
	bounds.maxReleaseRetries = 0
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: bounds})
	action := f.startToRelease(t, "wf_release_exhausted")
	f.crashBeforeDispatch(t, action)

	r := f.restart(t, service.PollHooks{})
	rec := r.reconciler()
	agentRunOnce(r.h, rec)
	r.h.clock.Advance(time.Duration(recoveryBounds.voidGraceMs) * time.Millisecond)
	r.poll(t, rec)

	attempts := r.attempts(t, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed || attempts[0].Error == nil || attempts[0].Error.Code != domain.ReleaseCodeVoided {
		t.Fatalf("attempts = %+v, want the one FAILED VOIDED and no re-release", attempts)
	}
	got := r.action(t, action.ID)
	if got.Status != domain.AgentActionFailed || got.Error == nil || got.Error.Code != string(domain.ReturnReleaseNotDeployed) {
		t.Errorf("action = %s %+v, want FAILED RELEASE_NOT_DEPLOYED", got.Status, got.Error)
	}
	if voided := r.eventsOf(t, domain.EventReleaseVoided, action.ID); len(voided) != 1 || voided[0]["nextOperationKey"] != nil {
		t.Errorf("RELEASE_VOIDED = %+v, want one without a next operation key", voided)
	}
	occupations := r.occupations(t, action.ID)
	if len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased || occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonNotDeployed {
		t.Errorf("occupations = %+v, want one RELEASED NOT_DEPLOYED", occupations)
	}
	if _, found := r.activeOccupation(t); found {
		t.Error("the target is still occupied after the exhausted release")
	}
	if message := r.toolMessage(t, releaseTurn, action.ID); message["code"] != string(domain.ReturnReleaseNotDeployed) {
		t.Errorf("tool message = %+v, want RELEASE_NOT_DEPLOYED", message)
	}
	if facts := r.releaseFacts(t); len(facts) != 0 {
		t.Errorf("release facts = %+v, want none", facts)
	}
	if _, ok := r.production.target(t); ok {
		t.Error("production reports a deployment that never happened")
	}
	r.finish(t)
}

// Contract case: a void request production never answered is sent again by the next scan
// without counting as a re-release; the retry count is unchanged when the void finally
// lands.
func TestRelease_LostVoidRequest_ResentWithoutCountingAsRetry(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_lost_void")
	first := f.crashBeforeDispatch(t, action)

	r := f.restart(t, service.PollHooks{})
	rec := r.reconciler()
	agentRunOnce(r.h, rec)
	r.h.clock.Advance(time.Duration(recoveryBounds.voidGraceMs) * time.Millisecond)
	r.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	voidPath := deploymentsPath + "/" + operationKey(first) + voidSuffix

	r.poll(t, rec)
	attempt := r.attempts(t, action.ID)[0]
	if attempt.VoidRequestedAt == nil || attempt.Status != domain.ToolAttemptDispatched {
		t.Fatalf("attempt after the lost void = %s voidRequested %v, want DISPATCHED with the void recorded", attempt.Status, attempt.VoidRequestedAt)
	}
	firstVoidAt := *attempt.VoidRequestedAt
	if n := r.production.counts.count("POST", voidPath); n != 1 {
		t.Fatalf("void requests = %d, want 1", n)
	}

	r.poll(t, rec)
	if n := r.production.counts.count("POST", voidPath); n != 2 {
		t.Errorf("void requests after the next scan = %d, want the lost one sent again", n)
	}
	attempt = r.attempts(t, action.ID)[0]
	if attempt.VoidRequestedAt == nil || !attempt.VoidRequestedAt.Equal(firstVoidAt) {
		t.Errorf("void_requested_at = %v, want the first request's time %s kept", attempt.VoidRequestedAt, firstVoidAt)
	}
	if attempts := r.attempts(t, action.ID); len(attempts) != 1 {
		t.Fatalf("attempts after lost voids = %d, want still 1", len(attempts))
	}

	r.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})
	r.poll(t, rec)
	second := r.assertVoidedAndReleasedAgain(t, action)
	r.poll(t, rec)
	r.assertDeployedOnce(t, action, operationKey(second))
	r.finish(t)
}

// Contract case: an Agent paused for the release is not timed out when its original
// deadline passes, and its orphan Attempt is still found and handled.
func TestRelease_PausedAgentPastDeadline_OrphanStillFoundWithoutTimeout(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds})
	action := f.startToRelease(t, "wf_release_past_deadline")
	f.crashBeforeDispatch(t, action)
	before := f.agentRun(t)
	// Past the Agent's own deadline, still inside the release's confirm deadline.
	f.h.clock.Advance(before.Deadline.Sub(f.h.clock.Now()) + time.Minute)

	r := f.restart(t, service.PollHooks{})
	report := agentRunOnce(r.h, r.reconciler())
	if report.ExpiredAgentRunsFound != 0 || report.AgentRunsTimedOut != 0 {
		t.Errorf("expired agent runs = found %d timed out %d, want none for a paused Agent", report.ExpiredAgentRunsFound, report.AgentRunsTimedOut)
	}
	if report.OrphanToolAttemptsFound != 1 || report.OrphanToolAttemptsHandled != 1 {
		t.Errorf("orphan report = found %d handled %d, want 1/1", report.OrphanToolAttemptsFound, report.OrphanToolAttemptsHandled)
	}
	r.assertOutcomeUnknown(t, action)
	agentRun := r.agentRun(t)
	if agentRun.Termination != nil || agentRun.PausedAt == nil {
		t.Errorf("agent run = termination %s paused %v, want still paused and not terminated", agentTermination(agentRun), agentRun.PausedAt)
	}
	if failed := agentPayloadsFor(t, r.events(t), domain.EventAgentFailed, "agentRunId", agentRun.ID); len(failed) != 0 {
		t.Errorf("AGENT_FAILED = %+v, want no timeout of the paused Agent", failed)
	}
}
