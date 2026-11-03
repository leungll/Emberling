//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
)

// TestRelease_Approved_DeploysAndConfirmsOnce is the normal release: the approval starts
// one Attempt with one operation key and occupies the target, the dispatch is accepted,
// the poll learns DEPLOYED, the target is checked and the release_confirmed fact, the
// released occupation, the SUCCEEDED Action and the Agent's next Turn commit together.
func TestRelease_Approved_DeploysAndConfirmsOnce(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{})
	action := f.startToRelease(t, "wf_release_ok")
	before := f.agentRun(t)
	// The person takes a minute: the approval wait is not execution time.
	f.h.clock.Advance(time.Minute)

	f.approve(t, action)

	attempts := f.attempts(t, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("attempts after approval = %d, want 1", len(attempts))
	}
	attempt := attempts[0]
	wantKey := domain.ReleaseOperationKey(action.ID, 1)
	if operationKey(attempt) != wantKey {
		t.Errorf("operation key = %q, want %q", operationKey(attempt), wantKey)
	}
	if attempt.Status != domain.ToolAttemptDispatched {
		t.Errorf("attempt = %s, want DISPATCHED after the accepted dispatch", attempt.Status)
	}
	if attempt.ConfirmDeadline == nil {
		t.Error("attempt has no confirm deadline")
	} else if want := attempt.StartedAt.Add(time.Duration(f.bounds.confirmTimeoutMs) * time.Millisecond); !attempt.ConfirmDeadline.Equal(want) {
		t.Errorf("confirm deadline = %s, want %s", attempt.ConfirmDeadline, want)
	}
	if attempt.CallbackTokenHash != nil {
		t.Error("a release attempt must not carry a callback credential")
	}
	bindings := f.bindings(t, attempt.ID)
	if len(bindings) != 1 || bindings[0].ProviderID != domain.ReleaseProviderID || bindings[0].ExternalTaskID != wantKey {
		t.Fatalf("bindings = %+v, want one release binding keyed by the operation key", bindings)
	}
	if got := f.action(t, action.ID); got.Status != domain.AgentActionWaitingCallback {
		t.Errorf("action after dispatch = %s, want WAITING_CALLBACK", got.Status)
	}
	occupation, found := f.activeOccupation(t)
	if !found || occupation.ActionID != action.ID || occupation.OperationKey != wantKey {
		t.Fatalf("active occupation = %+v (%v), want the action's occupation keyed %s", occupation, found, wantKey)
	}
	agentRun := f.agentRun(t)
	if agentRun.PausedAt == nil || agentRun.PauseReason == nil || *agentRun.PauseReason != domain.PauseRelease {
		t.Errorf("agent run during the release = paused %v reason %v, want RELEASE", agentRun.PausedAt, agentRun.PauseReason)
	}
	if !agentRun.Deadline.After(before.Deadline) {
		t.Errorf("deadline %s did not move past %s after the approval wait", agentRun.Deadline, before.Deadline)
	}
	resumed := agentPayloadsFor(t, f.events(t), domain.EventAgentResumed, "agentRunId", agentRun.ID)
	if len(resumed) != 1 || resumed[0]["reason"] != string(domain.PauseApproval) || resumed[0]["pausedMs"] != float64(time.Minute.Milliseconds()) {
		t.Errorf("AGENT_RESUMED = %+v, want the approval minute credited back once", resumed)
	}
	paused := agentPayloadsFor(t, f.events(t), domain.EventAgentPaused, "agentRunId", agentRun.ID)
	if len(paused) != 2 || paused[0]["reason"] != string(domain.PauseApproval) || paused[1]["reason"] != string(domain.PauseRelease) {
		t.Errorf("AGENT_PAUSED = %+v, want APPROVAL then RELEASE", paused)
	}
	started := f.eventsOf(t, domain.EventAgentActionStarted, action.ID)
	if len(started) != 1 || started[0]["operationKey"] != wantKey || started[0]["occupationId"] != occupation.ID {
		t.Errorf("AGENT_ACTION_STARTED = %+v, want operationKey and occupationId", started)
	}
	if waiting := f.eventsOf(t, domain.EventAgentActionWaiting, action.ID); len(waiting) != 1 {
		t.Errorf("AGENT_ACTION_WAITING events = %d, want 1", len(waiting))
	}

	rec := f.reconciler()
	f.poll(t, rec)

	attempt = f.attempts(t, action.ID)[0]
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Fatalf("attempt after the poll = %s %v, want SUCCEEDED", attempt.Status, attempt.Error)
	}
	if got := f.action(t, action.ID); got.Status != domain.AgentActionSucceeded {
		t.Errorf("action = %s, want SUCCEEDED", got.Status)
	}
	if _, found := f.activeOccupation(t); found {
		t.Error("the target is still occupied after the confirmed release")
	}
	occupations := f.occupations(t, action.ID)
	if len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased || occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonDeployed {
		t.Errorf("occupations = %+v, want one RELEASED DEPLOYED", occupations)
	}
	facts := f.releaseFacts(t)
	if len(facts) != 1 || facts[0].Verdict == nil || !*facts[0].Verdict || facts[0].SubjectRef != baselineDigest() {
		t.Fatalf("release facts = %+v, want one healthy fact about %s", facts, baselineDigest())
	}
	message := f.toolMessage(t, releaseTurn, action.ID)
	if message["operationKey"] != wantKey || message["healthy"] != true || message["patchDigest"] != baselineDigest() {
		t.Errorf("tool message = %+v, want the deployed operation", message)
	}
	target, ok := f.production.target(t)
	if !ok || target["patchDigest"] != baselineDigest() {
		t.Errorf("production target = %+v (%v), want the released patch", target, ok)
	}
	agentRun = f.agentRun(t)
	if agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after the confirmed release", agentRun.PauseReason)
	}
	if n := f.production.counts.count("POST", "/v2/deployments"); n != 1 {
		t.Errorf("release requests = %d, want exactly one", n)
	}
	f.finish(t)
}

// TestRelease_FactSchema_RecordsPatchDigestAndTarget guards the fact the release writes:
// the fact type of the deploy_change Tool, with the released patch as its subject.
func TestRelease_FactSchema_RecordsPatchDigestAndTarget(t *testing.T) {
	if deployrelease.FactType != "release_confirmed" {
		t.Fatalf("fact type = %q, want release_confirmed", deployrelease.FactType)
	}
}
