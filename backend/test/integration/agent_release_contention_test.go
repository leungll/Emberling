//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
)

// secondTask is another Backend of the same deployment, with its own repository checkout
// and model script, over the same database, clock and production as f.
func (f *releaseFixture) secondTask(t *testing.T, hooks service.ApprovalHooks) *releaseFixture {
	t.Helper()
	return newReleaseFixture(t, releaseFixtureOptions{
		bounds: f.bounds, production: f.production, pool: f.h.pool, clock: f.h.clock, approvalHooks: hooks,
	})
}

// assertReturnedBeforeAttempt checks a release call handed back to the Agent with code
// before any Attempt: the Action FAILED with the code, no Attempt, no occupation of its
// own, a REJECTED tool message, and the Agent not paused.
func (f *releaseFixture) assertReturnedBeforeAttempt(t *testing.T, action domain.AgentAction, turnNo int, code domain.AgentReturnCode) map[string]any {
	t.Helper()
	got := f.action(t, action.ID)
	if got.Status != domain.AgentActionFailed || got.Error == nil || got.Error.Code != string(code) {
		t.Fatalf("action = %s %+v, want FAILED %s", got.Status, got.Error, code)
	}
	if attempts := f.attempts(t, action.ID); len(attempts) != 0 {
		t.Errorf("attempts of the returned call = %+v, want none", attempts)
	}
	if occupations := f.occupations(t, action.ID); len(occupations) != 0 {
		t.Errorf("occupations of the returned call = %+v, want none", occupations)
	}
	message := f.toolMessage(t, turnNo, action.ID)
	if message["status"] != "REJECTED" || message["code"] != string(code) {
		t.Errorf("tool message = %+v, want REJECTED %s", message, code)
	}
	if agentRun := f.agentRun(t); agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after the return", agentRun.PauseReason)
	}
	return message
}

func (f *releaseFixture) hasApproval(t *testing.T, actionID string) bool {
	t.Helper()
	found := true
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Approvals().GetByActionID(ctx, actionID)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("approval of action %s: %v", actionID, err)
	}
	return found
}

// holdTarget approves f's release while production never completes it, so f's Action
// holds the target for as long as the test needs.
func (f *releaseFixture) holdTarget(t *testing.T, action domain.AgentAction) {
	t.Helper()
	f.production.server.PauseCompletion()
	f.approve(t, action)
	if occupation, found := f.activeOccupation(t); !found || occupation.ActionID != action.ID {
		t.Fatalf("active occupation = %+v (%v), want the holding action's", occupation, found)
	}
}

// Contract case: two tasks approve a release of the same target at the same time. The
// database decides: one occupation is ACTIVE, the other approval is APPROVED but its Action
// is handed back as TARGET_OCCUPIED.
func TestRelease_ConcurrentApprovalsOfOneTarget_OneOccupiesOtherReturned(t *testing.T) {
	var barrier sync.WaitGroup
	barrier.Add(2)
	hooks := service.ApprovalHooks{BeforeDecisionLock: func(string) {
		barrier.Done()
		barrier.Wait()
	}}
	a := newReleaseFixture(t, releaseFixtureOptions{approvalHooks: hooks})
	b := a.secondTask(t, hooks)
	actionA := a.startToRelease(t, "wf_release_race_a")
	actionB := b.startToRelease(t, "wf_release_race_b")

	errs := make(chan error, 2)
	decide := func(f *releaseFixture, actionID string) {
		approval := approvalOfAction(f.h.ctx, t, f.h.uow, actionID)
		_, err := f.h.svc.DecideApproval(f.h.ctx, service.DecideApproval{
			ApprovalID: approval.ID, Decision: domain.ApprovalApproved, DecidedBy: "release-operator",
		})
		errs <- err
	}
	go decide(a, actionA.ID)
	go decide(b, actionB.ID)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent approval: %v", err)
		}
	}
	for _, f := range []*releaseFixture{a, b} {
		if err := f.h.drainTurns(f.h.ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
	}

	occupation, found := a.activeOccupation(t)
	if !found {
		t.Fatal("no occupation is ACTIVE after two approvals")
	}
	winner, loser, loserAction := a, b, actionB
	if occupation.ActionID == actionB.ID {
		winner, loser, loserAction = b, a, actionA
	} else if occupation.ActionID != actionA.ID {
		t.Fatalf("the ACTIVE occupation belongs to %s, want one of the two actions", occupation.ActionID)
	}
	if got := winner.action(t, occupation.ActionID); got.Status != domain.AgentActionWaitingCallback {
		t.Errorf("winning action = %s, want WAITING_CALLBACK", got.Status)
	}
	loser.assertReturnedBeforeAttempt(t, loserAction, releaseTurn, domain.ReturnTargetOccupied)
	if approval := approvalOfAction(loser.h.ctx, t, loser.h.uow, loserAction.ID); approval.Status != domain.ApprovalApproved {
		t.Errorf("losing approval = %s, want APPROVED", approval.Status)
	}
	loser.finish(t)

	winner.poll(t, winner.reconciler())
	winner.assertDeployedOnce(t, winner.action(t, occupation.ActionID), occupation.OperationKey)
	winner.finish(t)
}

// Contract case: the target is occupied when the Agent asks to release. The call is handed
// back as TARGET_OCCUPIED in the claim transaction and no approval is requested.
func TestRelease_TargetOccupiedAtAsk_ReturnedWithoutApproval(t *testing.T) {
	a := newReleaseFixture(t, releaseFixtureOptions{})
	a.holdTarget(t, a.startToRelease(t, "wf_release_held_a"))

	b := a.secondTask(t, service.ApprovalHooks{})
	b.start(t, "wf_release_ask_occupied", verifyBaselineCall(), func() mockmodel.Scenario {
		return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), b.instructionsBasis(t)))
	}, b.releaseCall(t))
	b.deliverVerification(t, true)

	action := b.actionOfTurn(t, releaseTurn)
	message := b.assertReturnedBeforeAttempt(t, action, releaseTurn, domain.ReturnTargetOccupied)
	if b.hasApproval(t, action.ID) {
		t.Error("an approval was requested for a release of an occupied target")
	}
	if text, _ := message["message"].(string); !strings.Contains(text, releaseTargetKey) {
		t.Errorf("tool message %q should name the occupied target", text)
	}
	if waiting := b.eventsOf(t, domain.EventAgentActionWaitingApproval, action.ID); len(waiting) != 0 {
		t.Errorf("AGENT_ACTION_WAITING_APPROVAL = %+v, want none", waiting)
	}
	b.finish(t)
}

// Contract case: the target was free when the Agent asked and is occupied by the time a
// person approves. The approval is APPROVED and the Action is handed back as
// TARGET_OCCUPIED without an Attempt.
func TestRelease_TargetOccupiedDuringApprovalWait_ReturnedAfterApproval(t *testing.T) {
	b := newReleaseFixture(t, releaseFixtureOptions{})
	actionB := b.startToRelease(t, "wf_release_wait_b")

	a := b.secondTask(t, service.ApprovalHooks{})
	a.holdTarget(t, a.startToRelease(t, "wf_release_wait_a"))

	b.approve(t, actionB)
	b.assertReturnedBeforeAttempt(t, actionB, releaseTurn, domain.ReturnTargetOccupied)
	if approval := approvalOfAction(b.h.ctx, t, b.h.uow, actionB.ID); approval.Status != domain.ApprovalApproved {
		t.Errorf("approval = %s, want APPROVED", approval.Status)
	}
	if occupation, found := a.activeOccupation(t); !found || occupation.ActionID == actionB.ID {
		t.Errorf("active occupation = %+v (%v), want the holder's untouched", occupation, found)
	}
	b.finish(t)
}

// Contract case: the named change request no longer describes the workspace when the Agent
// asks: a write moved the workspace past it. The call is STALE_PATCH before any approval.
func TestRelease_ChangeRequestStaleAtAsk_ReturnedStalePatch(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{})
	f.start(t, "wf_release_stale_ask", verifyBaselineCall(), func() mockmodel.Scenario {
		return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), f.instructionsBasis(t)))
	}, fixed(wsWrite(wsTokenPath, wsNewToken)), f.releaseCall(t))
	f.deliverVerification(t, true)

	action := f.actionOfTurn(t, releaseTurn+1)
	message := f.assertReturnedBeforeAttempt(t, action, releaseTurn+1, domain.ReturnStalePatch)
	if f.hasApproval(t, action.ID) {
		t.Error("an approval was requested for a stale change request")
	}
	latest := wsLatestPatch(t, f.h, f.agentRunID(t))
	if text, _ := message["message"].(string); latest == nil || !strings.Contains(text, latest.PatchDigest[:24]) {
		t.Errorf("tool message %q should name the current patch digest", text)
	}
	f.finish(t)
}

// Contract case: the workspace moved past the change request while the approval waited.
// The approval transaction re-checks and hands the call back as STALE_PATCH: no Attempt,
// no occupation, approval APPROVED.
func TestRelease_ChangeRequestStaleAtApproval_ReturnedStalePatch(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{})
	action := f.startToRelease(t, "wf_release_stale_approve")
	requests := changeRequestsOf(t, f.h, f.agentRunID(t))
	// The workspace moves on while the person decides: a later patch supersedes the one the
	// change request describes.
	if err := f.h.uow.WithinTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.WorkspacePatches().Append(ctx, domain.WorkspacePatch{
			ID: f.h.ids.NewID(domain.IDPrefixWorkspacePatch), AgentRunID: f.agentRunID(t), Seq: 1,
			Patch: wsPatchFor(wsTokenPath, wsNewToken), PatchDigest: "sha256:" + strings.Repeat("ab", 32),
			TouchedPaths: []string{wsTokenPath}, ToolAttemptID: requests[0].ToolAttemptID, CreatedAt: f.h.clock.Now(),
		})
	}); err != nil {
		t.Fatalf("append the later patch: %v", err)
	}

	f.approve(t, action)
	f.assertReturnedBeforeAttempt(t, action, releaseTurn, domain.ReturnStalePatch)
	if approval := approvalOfAction(f.h.ctx, t, f.h.uow, action.ID); approval.Status != domain.ApprovalApproved {
		t.Errorf("approval = %s, want APPROVED", approval.Status)
	}
	if _, found := f.activeOccupation(t); found {
		t.Error("a stale release occupied the target")
	}
	f.finish(t)
}

// Contract case: production refuses the release with a definite answer. The Attempt FAILED
// with a known outcome, the Action is handed back as RELEASE_REFUSED (not a Tool error),
// and the occupation is released as REFUSED.
func TestRelease_ProductionRefuses_ReturnedReleaseRefusedAndTargetFreed(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{})
	action := f.startToRelease(t, "wf_release_refused")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Reject: true, RejectCode: "CHANGE_FREEZE"})

	f.approve(t, action)

	attempts := f.attempts(t, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed || attempts[0].Uncertain == nil || *attempts[0].Uncertain {
		t.Fatalf("attempts = %+v, want one FAILED with a definite outcome", attempts)
	}
	if attempts[0].Error == nil || attempts[0].Error.Code != "CHANGE_FREEZE" {
		t.Errorf("attempt error = %+v, want production's code", attempts[0].Error)
	}
	got := f.action(t, action.ID)
	if got.Status != domain.AgentActionFailed || got.Error == nil || got.Error.Code != string(domain.ReturnReleaseRefused) {
		t.Errorf("action = %s %+v, want FAILED RELEASE_REFUSED", got.Status, got.Error)
	}
	message := f.toolMessage(t, releaseTurn, action.ID)
	if message["code"] != string(domain.ReturnReleaseRefused) || message["status"] != "REJECTED" {
		t.Errorf("tool message = %+v, want REJECTED RELEASE_REFUSED", message)
	}
	if text, _ := message["message"].(string); !strings.Contains(text, "CHANGE_FREEZE") {
		t.Errorf("tool message %q should carry production's refusal code", text)
	}
	occupations := f.occupations(t, action.ID)
	if len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased || occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonRefused {
		t.Errorf("occupations = %+v, want one RELEASED REFUSED", occupations)
	}
	failed := agentOnlyPayloadFor(t, f.events(t), domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["returnedToAgent"] != true || failed["failureSource"] != string(domain.FailureProviderRefusal) {
		t.Errorf("AGENT_ACTION_FAILED = %+v, want returnedToAgent with the PROVIDER_REFUSAL source", failed)
	}
	if agentRun := f.agentRun(t); agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after the refusal", agentRun.PauseReason)
	}
	if facts := f.releaseFacts(t); len(facts) != 0 {
		t.Errorf("release facts = %+v, want none", facts)
	}
	f.finish(t)
}

// Contract case: production deploys but the target is unhealthy. The Action succeeds, the
// fact's verdict is false with the reason, and the occupation is released as DEPLOYED.
func TestRelease_TargetUnhealthy_FactVerdictFalseAndTargetFreed(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{})
	action := f.startToRelease(t, "wf_release_unhealthy")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unhealthy: true})

	f.approve(t, action)
	f.poll(t, f.reconciler())

	if got := f.action(t, action.ID); got.Status != domain.AgentActionSucceeded {
		t.Errorf("action = %s %+v, want SUCCEEDED", got.Status, got.Error)
	}
	facts := f.releaseFacts(t)
	if len(facts) != 1 || facts[0].Verdict == nil || *facts[0].Verdict {
		t.Fatalf("release facts = %+v, want one with a false verdict", facts)
	}
	message := f.toolMessage(t, releaseTurn, action.ID)
	if message["healthy"] != false || message["reason"] == nil || message["reason"] == "" {
		t.Errorf("tool message = %+v, want healthy false with a reason", message)
	}
	occupations := f.occupations(t, action.ID)
	if len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased || occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonDeployed {
		t.Errorf("occupations = %+v, want one RELEASED DEPLOYED", occupations)
	}
	f.finish(t)
}

// humanWaitBounds make the confirm deadline short enough to pass between two polls.
var humanWaitBounds = releaseBounds{confirmTimeoutMs: 5_000, voidGraceMs: 3_600_000, maxReleaseRetries: 1, humanWaitTimeoutMs: 1_800_000}

// Contract case: the confirm deadline passes without a definite answer.
func TestRelease_ConfirmDeadlinePassed_OpensHumanWaitThenDeployedResolvesIt(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action := f.startToRelease(t, "wf_release_confirm_deadline")
	f.production.server.PauseCompletion()
	f.approve(t, action)

	rec := f.reconciler()
	f.poll(t, rec)
	if got := f.action(t, action.ID); got.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("action inside the confirm deadline = %s, want WAITING_CALLBACK", got.Status)
	}

	f.h.clock.Advance(releasePollStep)
	report := agentRunOnce(f.h, rec)
	if report.DueConfirmDeadlinesFound != 1 || report.HumanWaitsOpened != 1 {
		t.Fatalf("confirm deadline report = found %d opened %d, want 1/1", report.DueConfirmDeadlinesFound, report.HumanWaitsOpened)
	}
	if got := f.action(t, action.ID); got.Status != domain.AgentActionWaitingHuman {
		t.Fatalf("action = %s, want WAITING_HUMAN", got.Status)
	}
	wait, found := f.humanWait(t, action.ID)
	if !found || wait.Status != domain.HumanWaitOpen {
		t.Fatalf("human wait = %+v (%v), want OPEN", wait, found)
	}
	if want := f.h.clock.Now().Add(time.Duration(humanWaitBounds.humanWaitTimeoutMs) * time.Millisecond); !wait.Deadline.Equal(want) {
		t.Errorf("human wait deadline = %s, want %s", wait.Deadline, want)
	}
	if agentRun := f.agentRun(t); agentRun.PauseReason == nil || *agentRun.PauseReason != domain.PauseHuman {
		t.Errorf("agent pause reason = %v, want HUMAN", agentRun.PauseReason)
	}
	if waiting := f.eventsOf(t, domain.EventAgentActionWaitingHuman, action.ID); len(waiting) != 1 || waiting[0]["humanWaitId"] != wait.ID {
		t.Errorf("AGENT_ACTION_WAITING_HUMAN = %+v, want one naming the wait", waiting)
	}
	if attempts := f.attempts(t, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptDispatched {
		t.Errorf("attempts = %+v, want the one still DISPATCHED and polled", attempts)
	}

	// A second pass opens nothing more: the deadline was handled once.
	if report := agentRunOnce(f.h, rec); report.HumanWaitsOpened != 0 {
		t.Errorf("second pass opened %d human waits, want none", report.HumanWaitsOpened)
	}

	f.production.server.ResumeCompletion()
	f.poll(t, rec)
	f.assertDeployedOnce(t, action, domain.ReleaseOperationKey(action.ID, 1))
	wait, _ = f.humanWait(t, action.ID)
	if wait.Status != domain.HumanWaitResolved || wait.Outcome == nil || *wait.Outcome != string(domain.ReleaseReasonDeployed) {
		t.Errorf("human wait after the conclusion = %+v, want RESOLVED with the DEPLOYED outcome", wait)
	}
	if agentRun := f.agentRun(t); agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after the resolved wait", agentRun.PauseReason)
	}
	f.finish(t)
}

// Contract case: production's DEPLOYED answer and the confirm deadline race. The deadline
// path runs after the poll has claimed the Attempt and before its answer commits; the
// conditional updates let the wait open once and the conclusion close it once, with one
// SUCCEEDED Action and one released occupation.
func TestRelease_PollAnswerRacesConfirmDeadline_OneConclusion(t *testing.T) {
	var (
		svc        *service.ExecutionService
		releaseID  string
		clock      *execClock
		hookRuns   int
		hookErrors []error
	)
	hooks := service.PollHooks{AfterClaim: func(ctx context.Context, attemptID string) error {
		if attemptID != releaseID {
			return nil
		}
		hookRuns++
		// The confirm deadline passes while the poll is on its way to production.
		clock.Advance(time.Duration(humanWaitBounds.confirmTimeoutMs) * time.Millisecond)
		if err := svc.OpenReleaseHumanWait(ctx, attemptID); err != nil {
			hookErrors = append(hookErrors, err)
		}
		return nil
	}}
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds, pollHooks: hooks})
	svc, clock = f.h.svc, f.h.clock
	action := f.startToRelease(t, "wf_release_poll_race")
	f.approve(t, action)
	releaseID = f.attempts(t, action.ID)[0].ID

	f.poll(t, f.reconciler())

	if hookRuns != 1 || len(hookErrors) != 0 {
		t.Fatalf("hook ran %d time(s) with errors %v, want once without error", hookRuns, hookErrors)
	}
	f.assertDeployedOnce(t, action, domain.ReleaseOperationKey(action.ID, 1))
	wait, found := f.humanWait(t, action.ID)
	if !found || wait.Status != domain.HumanWaitResolved {
		t.Errorf("human wait = %+v (%v), want one RESOLVED", wait, found)
	}
	events := f.events(t)
	if waiting := agentPayloadsFor(t, events, domain.EventAgentActionWaitingHuman, "actionId", action.ID); len(waiting) != 1 {
		t.Errorf("AGENT_ACTION_WAITING_HUMAN = %d, want 1", len(waiting))
	}
	if completed := agentPayloadsFor(t, events, domain.EventAgentActionCompleted, "actionId", action.ID); len(completed) != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED = %d, want 1", len(completed))
	}
	f.finish(t)
}
