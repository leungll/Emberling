//go:build integration

package contract

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

// assertDeployedOnce proves a FIX_AND_DEPLOY story ended released: the Run COMPLETED, the
// verdict is DEPLOYED with nothing missing, the release Action SUCCEEDED under its first
// operation key with the occupation released as DEPLOYED, and production deployed that one
// operation once, with the latest patch.
func assertDeployedOnce(t *testing.T, s *releaseStory, runID, actionID string) {
	t.Helper()
	snapshot := s.env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED\n%s", got, runStateDump(t, s.env, runID))
	}
	trace, story := s.traces(t, runID)
	latest := trace.latestPatchDigest(t)
	if c := trace.AgentRun.Completion; c == nil || c.Outcome != "DEPLOYED" || len(c.Missing) != 0 {
		t.Errorf("completion = %+v, want DEPLOYED with nothing missing", trace.AgentRun.Completion)
	}
	key := "op_" + actionID + "_1"
	var found bool
	for _, turn := range story.releases() {
		if turn.Action.ID != actionID {
			continue
		}
		found = true
		release := turn.Action.Release
		if turn.Action.Status != "SUCCEEDED" || len(turn.ToolAttempts) != 1 || turn.ToolAttempts[0].Status != "SUCCEEDED" ||
			release == nil || len(release.Operations) != 1 || release.Operations[0].OperationKey != key || release.Operations[0].Outcome != "DEPLOYED" ||
			release.Occupation == nil || release.Occupation.Status != "RELEASED" || strOf(release.Occupation.ReleaseReason) != "DEPLOYED" {
			t.Errorf("release action %s = %+v attempts %+v, want SUCCEEDED once under %s with the occupation RELEASED as DEPLOYED", actionID, turn.Action, turn.ToolAttempts, key)
		}
	}
	if !found {
		t.Fatalf("the trace has no release Turn for action %s", actionID)
	}
	lines := serviceRecord(t, s.productionURL)
	if productionCount(lines, "arrived", key) != 1 || productionCount(lines, "deployed", key) != 1 || deployedUnderAction(lines, actionID) != 1 {
		t.Errorf("production saw %s arrive %d times and deploy %d times (%d under the action), want once each",
			key, productionCount(lines, "arrived", key), productionCount(lines, "deployed", key), deployedUnderAction(lines, actionID))
	}
	for _, line := range recordLines(lines, "deployed", "release") {
		if line["operationId"] == key && line["patchDigest"] != latest {
			t.Errorf("production deployed %s with patch %v, want the latest %s", key, line["patchDigest"], latest)
		}
	}
}

// TestE2E_VulnRelease_RestartDuringApproval_StillWaitingClockStoppedNoReask is the
// restart-during-approval variant: the Backend stops while the release request waits for a
// person and another starts over the same facts. The approval is still PENDING, the Agent's
// clock is still stopped at the same instant with the same deadline, no Decision is asked
// again, and the approval given to the new process releases the original request once, the
// clock resuming with exactly the execution time that remained when it stopped.
func TestE2E_VulnRelease_RestartDuringApproval_StillWaitingClockStoppedNoReask(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"})
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	pending := waitForPendingApproval(t, s.env, runID, e2eWait)
	approvalID, _ := pending["id"].(string)
	actionID, _ := pending["actionId"].(string)

	_, before := s.traces(t, runID)
	if before.AgentRun.PausedAt == nil || strOf(before.AgentRun.PauseReason) != "APPROVAL" {
		t.Fatalf("agent run before the restart = %+v, want its clock paused for APPROVAL", before.AgentRun)
	}
	decisionsBefore := len(eventsOfType(s.env.listEvents(t, runID), "AGENT_DECISION_COMMITTED"))
	if decisionsBefore != 10 || len(s.env.provider.Requests()) != 10 {
		t.Fatalf("Decisions before the restart = %d (model calls %d), want 10: the repair, the change request and the release request",
			decisionsBefore, len(s.env.provider.Requests()))
	}

	// --- restart while the request waits for a person.
	s.restart(t)

	if got := s.approval(t, approvalID); got["status"] != "PENDING" || got["actionId"] != actionID {
		t.Fatalf("approval after the restart = %v, want the same request still PENDING", got)
	}
	_, after := s.traces(t, runID)
	if after.AgentRun.PausedAt == nil || !after.AgentRun.PausedAt.Equal(*before.AgentRun.PausedAt) ||
		!after.AgentRun.Deadline.Equal(before.AgentRun.Deadline) || strOf(after.AgentRun.PauseReason) != "APPROVAL" {
		t.Errorf("agent run after the restart = %+v, want it still paused at %v for APPROVAL with deadline %v",
			after.AgentRun, before.AgentRun.PausedAt, before.AgentRun.Deadline)
	}
	if releases := after.releases(); len(releases) != 1 || releases[0].Action.Status != "WAITING_APPROVAL" || len(releases[0].ToolAttempts) != 0 {
		t.Errorf("release turns after the restart = %+v, want the one request WAITING_APPROVAL with no Attempt", releases)
	}
	if n := len(s.env.provider.Requests()); n != 0 {
		t.Errorf("the restarted Backend asked the model %d times while the request waited, want none", n)
	}
	if n := len(eventsOfType(s.env.listEvents(t, runID), "AGENT_DECISION_COMMITTED")); n != decisionsBefore {
		t.Errorf("Decisions after the restart = %d, want %d", n, decisionsBefore)
	}

	if status, body := s.decide(t, approvalID, "APPROVE", ""); status != http.StatusOK {
		t.Fatalf("approve after the restart = %d %v", status, body)
	}
	s.drive(t, 3*time.Second, runID)
	assertDeployedOnce(t, s, runID, actionID)

	events := s.env.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := len(eventsOfType(events, "AGENT_DECISION_COMMITTED")); n != 11 || len(s.env.provider.Requests()) != 1 {
		t.Errorf("Decisions = %d with %d model calls after the restart, want 11 and only the FINAL asked anew", n, len(s.env.provider.Requests()))
	}
	remaining := before.AgentRun.Deadline.Sub(*before.AgentRun.PausedAt).Milliseconds()
	var resumed bool
	for _, payload := range eventPayloads(events, "AGENT_RESUMED") {
		if payload["reason"] != "APPROVAL" {
			continue
		}
		resumed = true
		if got, _ := payload["remainingMs"].(float64); int64(got) != remaining {
			t.Errorf("AGENT_RESUMED after the approval = %v, want remainingMs %d: the wait and the restart consumed no Agent time", payload, remaining)
		}
	}
	if !resumed {
		t.Errorf("events lack the AGENT_RESUMED of the approval: %v", eventTypeCounts(events))
	}
}

// TestE2E_VulnRelease_DuplicateApproval_TakesEffectOnce is the duplicate-approval variant:
// the same approval is submitted twice at once and once more afterwards. One submission
// decides it, the others are answered as repeated without a write, and production receives
// the release once.
func TestE2E_VulnRelease_DuplicateApproval_TakesEffectOnce(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"})
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	pending := waitForPendingApproval(t, s.env, runID, e2eWait)
	approvalID, _ := pending["id"].(string)
	actionID, _ := pending["actionId"].(string)

	// Both submissions leave together once the start barrier opens.
	start := make(chan struct{})
	var wg sync.WaitGroup
	type answer struct {
		status   int
		repeated any
	}
	answers := make([]answer, 2)
	for i := range answers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, body := s.decide(t, approvalID, "APPROVE", "")
			answers[i] = answer{status, body["repeated"]}
		}()
	}
	close(start)
	wg.Wait()
	repeated := 0
	for _, a := range answers {
		if a.status != http.StatusOK {
			t.Fatalf("concurrent approvals = %+v, want both answered 200", answers)
		}
		if a.repeated == true {
			repeated++
		}
	}
	if repeated != 1 {
		t.Fatalf("concurrent approvals = %+v, want exactly one decided and one repeated", answers)
	}

	s.drive(t, 3*time.Second, runID)
	assertDeployedOnce(t, s, runID, actionID)
	eventsAtEnd := s.env.listEvents(t, runID)
	if n := len(eventsOfType(eventsAtEnd, "APPROVAL_DECIDED")); n != 1 {
		t.Errorf("APPROVAL_DECIDED events = %d, want 1", n)
	}
	if status, body := s.decide(t, approvalID, "APPROVE", ""); status != http.StatusOK || body["repeated"] != true {
		t.Errorf("a later approval = %d %v, want 200 repeated", status, body)
	}
	if n := len(s.env.listEvents(t, runID)); n != len(eventsAtEnd) {
		t.Errorf("the later approval wrote %d events, want none", n-len(eventsAtEnd))
	}
	if n := deployedUnderAction(serviceRecord(t, s.productionURL), actionID); n != 1 {
		t.Errorf("production deployed the action %d times, want once", n)
	}
}

// TestE2E_VulnRelease_RejectResubmit_NewPatchNewChangeRequestNewApproval is the
// reject-resubmit variant: the person rejects the first release request with a reason; the
// Agent receives the reason, changes the code, verifies the new patch, records a second
// change request about it and asks again; the second request needs its own approval, which
// releases it. The rejected approval stays bound to the first Action and never reaches the
// release that ran.
func TestE2E_VulnRelease_RejectResubmit_NewPatchNewChangeRequestNewApproval(t *testing.T) {
	const reason = "Require an expiry on every token through the v5 validator before this goes to staging."
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-rejected-resubmit", maxTurns: 16})
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	first := waitForPendingApproval(t, s.env, runID, e2eWait)
	firstID, _ := first["id"].(string)
	firstAction, _ := first["actionId"].(string)
	if status, body := s.decide(t, firstID, "REJECT", reason); status != http.StatusOK {
		t.Fatalf("reject = %d %v", status, body)
	}
	second := waitForPendingApproval(t, s.env, runID, e2eWait)
	secondID, _ := second["id"].(string)
	secondAction, _ := second["actionId"].(string)
	if secondID == firstID || secondAction == firstAction {
		t.Fatalf("second approval %s of action %s, want a new approval of a new action", secondID, secondAction)
	}
	if status, body := s.decide(t, secondID, "APPROVE", ""); status != http.StatusOK {
		t.Fatalf("approve the resubmission = %d %v", status, body)
	}
	s.drive(t, 3*time.Second, runID)
	assertDeployedOnce(t, s, runID, secondAction)

	trace, story := s.traces(t, runID)
	latest := trace.latestPatchDigest(t)
	releases := story.releases()
	if len(releases) != 2 {
		t.Fatalf("release turns = %+v, want two requests", releases)
	}
	rejected, approved := releases[0], releases[1]
	if rejected.Action.ID != firstAction || rejected.Action.Status != "FAILED" || rejected.Action.Error == nil ||
		rejected.Action.Error.Code != "APPROVAL_REJECTED" || strOf(rejected.Action.FailureSource) != "HUMAN" || len(rejected.ToolAttempts) != 0 ||
		rejected.Action.Approval == nil || rejected.Action.Approval.ID != firstID || rejected.Action.Approval.Status != "REJECTED" ||
		strOf(rejected.Action.Approval.Reason) != reason {
		t.Errorf("first release request = %+v attempts %+v, want FAILED APPROVAL_REJECTED from HUMAN, no Attempt, bound to the REJECTED approval %s",
			rejected.Action, rejected.ToolAttempts, firstID)
	}
	if approved.Action.Approval == nil || approved.Action.Approval.ID != secondID || approved.Action.Approval.Status != "APPROVED" {
		t.Errorf("second release request approval = %+v, want the APPROVED approval %s", approved.Action.Approval, secondID)
	}
	binding := approved.ToolAttempts[0].CallbackBinding
	if binding == nil || binding.ExternalTaskID != "op_"+secondAction+"_1" ||
		strings.Contains(binding.ExternalTaskID, firstID) || strings.Contains(binding.ExternalTaskID, firstAction) {
		t.Errorf("release binding = %+v, want op_%s_1, naming neither the rejected approval nor its action", binding, secondAction)
	}

	requests := trace.ChangeRequests.Items
	if len(requests) != 2 || requests[0].PatchDigest == requests[1].PatchDigest || requests[1].PatchDigest != latest {
		t.Errorf("change requests = %+v, want two about different patches, the second about the latest %s", requests, latest)
	}
	if verifications := trace.callsOf(verifypatch.ToolName); len(verifications) != 3 {
		t.Errorf("verify_patch calls = %d, want 3: the failed upgrade, the adaptation and the resubmitted patch", len(verifications))
	}
	var reasonSeen bool
	for _, request := range s.env.provider.Requests() {
		for _, message := range toolMessagesOf(t, request) {
			if message["code"] == "APPROVAL_REJECTED" && strings.Contains(message["message"].(string), reason) {
				reasonSeen = true
			}
		}
	}
	if !reasonSeen {
		t.Errorf("no model request carried the rejection with its reason")
	}
	snapshot := s.env.runSnapshot(t, runID)
	if _, report := agentReport(t, snapshot); report["status"] != "RELEASED" || report["changeRequestId"] != requests[1].ID ||
		report["rejectedChangeRequestId"] != requests[0].ID || report["patchDigest"] != latest {
		t.Errorf("agent report = %v, want RELEASED for %s after %s was rejected", report, requests[1].ID, requests[0].ID)
	}
	if approvals := s.approvalsOfRun(t, runID); len(approvals) != 2 {
		t.Errorf("approvals of the run = %v, want two", approvals)
	}
}

// TestE2E_VulnRelease_PrepareOnly_ReleaseRefusedBeforeApprovalTaskReady is the prepare-only
// variant: a task that only prepares the change allows the release Tool, the Agent asks to
// release anyway, the request is refused as GOAL_DENIED before any approval or Attempt, and
// the task is judged READY_FOR_APPROVAL on its verified patch and change request.
func TestE2E_VulnRelease_PrepareOnly_ReleaseRefusedBeforeApprovalTaskReady(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"})
	runID := s.createRun(t, "PREPARE_ONLY")
	snapshot := s.env.waitForTerminal(t, runID, e2eWait)
	trace, story := s.traces(t, runID)
	assertVulnReadyForApproval(t, snapshot, trace)

	releases := story.releases()
	if len(releases) != 1 || releases[0].Action.Status != "FAILED" || releases[0].Action.Error == nil ||
		releases[0].Action.Error.Code != "GOAL_DENIED" || strOf(releases[0].Action.FailureSource) != "AUTHORIZATION" ||
		len(releases[0].ToolAttempts) != 0 || releases[0].Action.Approval != nil {
		t.Errorf("release turns = %+v, want one refused as GOAL_DENIED from AUTHORIZATION, without approval or Attempt", releases)
	}
	if approvals := s.approvalsOfRun(t, runID); len(approvals) != 0 {
		t.Errorf("approvals of the run = %v, want none", approvals)
	}
	if lines := recordLines(serviceRecord(t, s.productionURL), "arrived", "release"); len(lines) != 0 {
		t.Errorf("production release arrivals = %v, want none", lines)
	}
	if _, report := agentReport(t, snapshot); report["status"] != "PREPARED_ONLY" || report["code"] != "GOAL_DENIED" {
		t.Errorf("agent report = %v, want PREPARED_ONLY naming GOAL_DENIED", report)
	}
}

// TestE2E_VulnRelease_TargetOccupied_SecondTaskRefusedBeforeApproval is the target-occupied
// variant: a first task's approved release holds the deploy target while production still
// deploys it; a second task asks to release to the same target and is refused as
// TARGET_OCCUPIED before any approval, and the first task's release completes once.
func TestE2E_VulnRelease_TargetOccupied_SecondTaskRefusedBeforeApproval(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"})
	s.production.PauseCompletion()
	holder := s.createRun(t, "FIX_AND_DEPLOY")
	pending := waitForPendingApproval(t, s.env, holder, e2eWait)
	approvalID, _ := pending["id"].(string)
	holderAction, _ := pending["actionId"].(string)
	if status, body := s.decide(t, approvalID, "APPROVE", ""); status != http.StatusOK {
		t.Fatalf("approve the holder = %d %v", status, body)
	}
	waitRecordLine(t, s.productionURL, "arrived", "release", nil)

	contender := s.createRun(t, "FIX_AND_DEPLOY")
	snapshot := s.env.waitForTerminal(t, contender, e2eWait)
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("contender Run status = %q, want FAILED: its task was not deployed", got)
	}
	trace, story := s.traces(t, contender)
	releases := story.releases()
	if len(releases) != 1 || releases[0].Action.Status != "FAILED" || releases[0].Action.Error == nil ||
		releases[0].Action.Error.Code != "TARGET_OCCUPIED" || strOf(releases[0].Action.FailureSource) != "AUTHORIZATION" ||
		len(releases[0].ToolAttempts) != 0 || releases[0].Action.Approval != nil {
		t.Errorf("contender release turns = %+v, want one refused as TARGET_OCCUPIED from AUTHORIZATION, without approval or Attempt", releases)
	}
	if last := story.Turns[len(story.Turns)-1]; last.Decision == nil || last.Decision.Kind != "FINAL" || last.TurnNo != releases[0].TurnNo+1 {
		t.Errorf("contender's last Turn = %+v, want the FINAL right after the refusal", last)
	}
	if strOf(trace.AgentRun.Termination) != "FINAL_RESPONSE" || trace.AgentRun.Completion == nil || trace.AgentRun.Completion.Outcome != "INCOMPLETE" {
		t.Errorf("contender agent run = termination %v completion %+v, want FINAL_RESPONSE judged INCOMPLETE", trace.AgentRun.Termination, trace.AgentRun.Completion)
	}
	if approvals := s.approvalsOfRun(t, contender); len(approvals) != 0 {
		t.Errorf("approvals of the contender = %v, want none", approvals)
	}

	s.production.ResumeCompletion()
	s.drive(t, 3*time.Second, holder)
	assertDeployedOnce(t, s, holder, holderAction)
	if n := len(recordLines(serviceRecord(t, s.productionURL), "arrived", "release")); n != 1 {
		t.Errorf("production release arrivals = %d, want only the holder's", n)
	}
}
