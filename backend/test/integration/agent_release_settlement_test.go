//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

func (f *releaseFixture) nodeRun(t *testing.T) domain.NodeRun {
	t.Helper()
	var nodeRun domain.NodeRun
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		nodeRun, err = tx.NodeRuns().Get(ctx, f.outcome.NodeRunID)
		return err
	}); err != nil {
		t.Fatalf("get agent node run: %v", err)
	}
	return nodeRun
}

// openHumanWait drives a release whose production answer is still outstanding past its
// confirm deadline, so the reconciler hands the Action to a person.
func (f *releaseFixture) openHumanWait(t *testing.T, action domain.AgentAction) domain.HumanWait {
	t.Helper()
	f.h.clock.Advance(time.Duration(f.bounds.confirmTimeoutMs) * time.Millisecond)
	report := agentRunOnce(f.h, f.reconciler())
	if report.HumanWaitsOpened != 1 {
		t.Fatalf("confirm deadline pass opened %d human waits, want 1", report.HumanWaitsOpened)
	}
	if got := f.action(t, action.ID); got.Status != domain.AgentActionWaitingHuman {
		t.Fatalf("action = %s, want WAITING_HUMAN", got.Status)
	}
	wait, found := f.humanWait(t, action.ID)
	if !found || wait.Status != domain.HumanWaitOpen {
		t.Fatalf("human wait = %+v (%v), want OPEN", wait, found)
	}
	return wait
}

// Once a person watches the release the Runtime starts no production operation of its own:
// a confirmed void with a re-release still allowed frees the target as NOT_DEPLOYED, closes
// the wait RESOLVED and returns RELEASE_NOT_DEPLOYED to the Agent, telling it the release
// was voided after the confirm deadline and must be asked for again.
func TestRelease_VoidedWhileWaitingHuman_RetryLeft_ReturnedNotDeployedWithoutRelease(t *testing.T) {
	bounds := releaseBounds{confirmTimeoutMs: 5_000, voidGraceMs: 20_000, maxReleaseRetries: 1, humanWaitTimeoutMs: 1_800_000}
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: bounds})
	action := f.startToRelease(t, "wf_release_void_waiting_human")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	f.approve(t, action)
	f.assertOutcomeUnknown(t, action)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})

	f.openHumanWait(t, action)
	f.h.clock.Advance(time.Duration(bounds.voidGraceMs) * time.Millisecond)
	f.poll(t, f.reconciler())

	attempts := f.attempts(t, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed || attempts[0].Error == nil || attempts[0].Error.Code != domain.ReleaseCodeVoided {
		t.Fatalf("attempts = %+v, want the one FAILED VOIDED and no re-release", attempts)
	}
	if n := f.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests = %d, want no new operation once a person watches the release", n)
	}
	got := f.action(t, action.ID)
	if got.Status != domain.AgentActionFailed || got.Error == nil || got.Error.Code != string(domain.ReturnReleaseNotDeployed) {
		t.Errorf("action = %s %+v, want FAILED RELEASE_NOT_DEPLOYED", got.Status, got.Error)
	}
	wait, _ := f.humanWait(t, action.ID)
	if wait.Status != domain.HumanWaitResolved || wait.Outcome == nil || *wait.Outcome != string(domain.ReleaseReasonNotDeployed) {
		t.Errorf("human wait = %+v, want RESOLVED with the NOT_DEPLOYED outcome", wait)
	}
	occupations := f.occupations(t, action.ID)
	if len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased || occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonNotDeployed {
		t.Errorf("occupations = %+v, want one RELEASED NOT_DEPLOYED", occupations)
	}
	message := f.toolMessage(t, releaseTurn, action.ID)
	text, _ := message["message"].(string)
	if message["code"] != string(domain.ReturnReleaseNotDeployed) || !strings.Contains(text, "after its confirm deadline") ||
		!strings.Contains(text, "Ask for the release again") || strings.Contains(text, "no re-release is left") {
		t.Errorf("tool message = %+v, want RELEASE_NOT_DEPLOYED saying it was voided after the confirm deadline and must be asked for again", message)
	}
	if agentRun := f.agentRun(t); agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after the return", agentRun.PauseReason)
	}
	f.finish(t)
}

// A DEPLOYED answer whose round cannot be committed, here because the release Decision's
// state patch violates the State Schema, still settles the release: the occupation is
// released as DEPLOYED, the open human wait closes and the Agent's clock resumes, while the
// Action fails as INVALID_ACTION and the Agent Run ends as before.
func TestRelease_DeployedWithInvalidStatePatch_TargetFreedWaitClosedClockResumed(t *testing.T) {
	const stateSchema = `{"type":"object","properties":{"count":{"type":"number"}},"additionalProperties":false}`
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds, stateSchema: stateSchema})
	action := f.startToReleaseWith(t, "wf_release_deployed_bad_patch", f.releaseCallWithPatch(t, `{"count":"not-a-number"}`))
	f.production.server.PauseCompletion()
	f.approve(t, action)
	f.openHumanWait(t, action)

	f.production.server.ResumeCompletion()
	f.poll(t, f.reconciler())

	got := f.action(t, action.ID)
	if got.Status != domain.AgentActionFailed || got.Error == nil || got.Error.Code != "INVALID_ACTION" {
		t.Errorf("action = %s %+v, want FAILED INVALID_ACTION", got.Status, got.Error)
	}
	if attempts := f.attempts(t, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Errorf("attempts = %+v, want the one SUCCEEDED: production did deploy", attempts)
	}
	occupations := f.occupations(t, action.ID)
	if len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased || occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonDeployed {
		t.Errorf("occupations = %+v, want one RELEASED DEPLOYED", occupations)
	}
	if _, found := f.activeOccupation(t); found {
		t.Error("the target is still occupied after the deployed release")
	}
	wait, _ := f.humanWait(t, action.ID)
	if wait.Status != domain.HumanWaitResolved || wait.Outcome == nil || *wait.Outcome != string(domain.ReleaseReasonDeployed) {
		t.Errorf("human wait = %+v, want RESOLVED with the DEPLOYED outcome", wait)
	}
	agentRun := f.agentRun(t)
	if agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after the deployed release", agentRun.PauseReason)
	}
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Errorf("agent termination = %s, want INVALID_ACTION", agentTermination(agentRun))
	}
	if facts := f.releaseFacts(t); len(facts) != 0 {
		t.Errorf("release facts = %+v, want none from a round that failed", facts)
	}
	if run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID); run.Status != domain.RunFailed {
		t.Errorf("run = %s, want FAILED", run.Status)
	}
}

// A confirm deadline that passes before the dispatch of an approved release settled finds
// the Agent NodeRun still RUNNING: the human wait moves it to WAITING_CALLBACK so the Run
// shows PAUSED, and the settlement that follows keeps the Action with the person and
// appends no AGENT_ACTION_WAITING.
func TestRelease_ConfirmDeadlineBeforeDispatchSettled_NodeRunWaitsAndNoActionWaitingEvent(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action := f.startToRelease(t, "wf_release_deadline_before_settle")
	attempt := f.crashBeforeDispatch(t, action)
	if run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID); run.Status != domain.RunRunning {
		t.Fatalf("run before the deadline = %s, want RUNNING", run.Status)
	}

	f.h.clock.Advance(time.Duration(humanWaitBounds.confirmTimeoutMs) * time.Millisecond)
	if err := f.h.svc.OpenReleaseHumanWait(f.h.ctx, attempt.ID); err != nil {
		t.Fatalf("open human wait: %v", err)
	}
	if got := f.action(t, action.ID); got.Status != domain.AgentActionWaitingHuman {
		t.Fatalf("action = %s, want WAITING_HUMAN", got.Status)
	}
	if nodeRun := f.nodeRun(t); nodeRun.Status != domain.NodeRunWaitingCallback {
		t.Errorf("agent node run = %s, want WAITING_CALLBACK", nodeRun.Status)
	}
	if run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID); run.Status != domain.RunPaused {
		t.Errorf("run = %s, want PAUSED", run.Status)
	}

	// The dead process's request did reach production before it died.
	if status := f.production.post(t, deploymentsPath, deployBody(operationKey(attempt), f.repo.commit)); status != 202 {
		t.Fatalf("the played request answered %d, want 202", status)
	}
	r := f.restart(t, service.PollHooks{})
	rec := r.reconciler()
	// The orphan handling settles the dispatch first; the poll it makes due concludes the
	// release in the same pass.
	if report := agentRunOnce(r.h, rec); report.OrphanToolAttemptsHandled != 1 {
		t.Fatalf("orphan report = %+v, want one handled", report)
	}
	if waiting := r.eventsOf(t, domain.EventAgentActionWaiting, action.ID); len(waiting) != 0 {
		t.Errorf("AGENT_ACTION_WAITING = %+v, want none for an Action a person already watches", waiting)
	}
	if unknown := r.eventsOf(t, domain.EventReleaseOutcomeUnknown, action.ID); len(unknown) != 1 {
		t.Errorf("RELEASE_OUTCOME_UNKNOWN = %+v, want exactly one", unknown)
	}
	if err := r.h.drainTurns(r.h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}
	r.assertDeployedOnce(t, action, operationKey(attempt))
	if wait, _ := r.humanWait(t, action.ID); wait.Status != domain.HumanWaitResolved {
		t.Errorf("human wait = %+v, want RESOLVED", wait)
	}
	r.finish(t)
}
