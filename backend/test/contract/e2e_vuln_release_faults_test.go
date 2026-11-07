//go:build integration

package contract

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
)

// approveFirst waits for the Run's release request and approves it, returning the release
// Action's id.
func approveFirst(t *testing.T, s *releaseStory, runID string) string {
	t.Helper()
	pending := waitForPendingApproval(t, s.env, runID, e2eWait)
	approvalID, _ := pending["id"].(string)
	actionID, _ := pending["actionId"].(string)
	if status, body := s.decide(t, approvalID, "APPROVE", ""); status != http.StatusOK {
		t.Fatalf("approve = %d %v", status, body)
	}
	return actionID
}

// assertReturnedRelease proves a release ended definitely without a deployment and was
// handed back to the Agent: the release Action FAILED with code from PROVIDER_REFUSAL, the
// occupation was released for reason, the Agent answered FINAL right after the refusal
// instead of failing on a Tool error, and the FIX_AND_DEPLOY task, never deployed, is
// INCOMPLETE rather than of unknown release outcome.
func assertReturnedRelease(t *testing.T, s *releaseStory, runID, actionID, code, reason string) releaseStoryTurn {
	t.Helper()
	snapshot := s.env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED: the task was not deployed\n%s", got, runStateDump(t, s.env, runID))
	}
	agentNode := vulnNodeRunOf(t, snapshot, "node_agent")
	if nodeError, _ := agentNode["error"].(map[string]any); nodeError["code"] != "TASK_INCOMPLETE" {
		t.Errorf("node_agent error = %v, want TASK_INCOMPLETE", agentNode["error"])
	}
	trace, story := s.traces(t, runID)
	if strOf(trace.AgentRun.Termination) != "FINAL_RESPONSE" || trace.AgentRun.Completion == nil || trace.AgentRun.Completion.Outcome != "INCOMPLETE" {
		t.Errorf("agent run = termination %v completion %+v, want FINAL_RESPONSE judged INCOMPLETE, never RELEASE_UNKNOWN or a Tool error",
			trace.AgentRun.Termination, trace.AgentRun.Completion)
	}
	releases := story.releases()
	if len(releases) != 1 || releases[0].Action.ID != actionID {
		t.Fatalf("release turns = %+v, want the one request %s", releases, actionID)
	}
	release := releases[0]
	if release.Action.Status != "FAILED" || release.Action.Error == nil || release.Action.Error.Code != code ||
		strOf(release.Action.FailureSource) != "PROVIDER_REFUSAL" {
		t.Errorf("release action = %+v, want FAILED %s from PROVIDER_REFUSAL", release.Action, code)
	}
	if occ := release.Action.Release.Occupation; occ == nil || occ.Status != "RELEASED" || strOf(occ.ReleaseReason) != reason {
		t.Errorf("occupation = %+v, want RELEASED as %s", occ, reason)
	}
	if last := story.Turns[len(story.Turns)-1]; last.Decision == nil || last.Decision.Kind != "FINAL" || last.TurnNo != release.TurnNo+1 {
		t.Errorf("last Turn = %+v, want the FINAL right after the returned release", last)
	}
	if n := deployedUnderAction(serviceRecord(t, s.productionURL), actionID); n != 0 {
		t.Errorf("production deployed the action %d times, want never", n)
	}
	return release
}

// TestE2E_VulnRelease_CrashAfterSendDeployed_QueriedAndConfirmedNotResent is the
// release-crash-deployed variant: production accepts the release but its answer is lost,
// and the Backend stops before it learns the outcome. The restarted Backend queries the
// operation instead of sending it again, learns DEPLOYED once production finishes, and
// confirms the target under the one operation key.
func TestE2E_VulnRelease_CrashAfterSendDeployed_QueriedAndConfirmedNotResent(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"})
	s.production.SetReleaseFaults(mockproduction.ReleaseFaults{DropResponse: true})
	s.production.PauseCompletion()
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	actionID := approveFirst(t, s, runID)
	key := "op_" + actionID + "_1"
	waitRecordLine(t, s.productionURL, "responseDropped", "release", func(line map[string]any) bool { return line["operationId"] == key })
	decisionsBefore := len(eventsOfType(s.env.listEvents(t, runID), "AGENT_DECISION_COMMITTED"))

	// --- restart: production accepted the release, the Backend never learned it.
	s.restart(t)
	s.production.SetReleaseFaults(mockproduction.ReleaseFaults{})
	s.production.ResumeCompletion()
	s.drive(t, 3*time.Second, runID)
	assertDeployedOnce(t, s, runID, actionID)

	events := s.env.listEvents(t, runID)
	if unknown := eventPayloads(events, "RELEASE_OUTCOME_UNKNOWN"); len(unknown) != 1 || unknown[0]["operationKey"] != key {
		t.Errorf("RELEASE_OUTCOME_UNKNOWN = %v, want one for %s", unknown, key)
	}
	if n := len(eventsOfType(events, "RELEASE_VOIDED")); n != 0 {
		t.Errorf("RELEASE_VOIDED events = %d, want none: the operation was deployed", n)
	}
	lines := serviceRecord(t, s.productionURL)
	if productionCount(lines, "responseDropped", key) != 1 {
		t.Errorf("production dropped %d answers for %s, want 1", productionCount(lines, "responseDropped", key), key)
	}
	if decisions := len(eventsOfType(events, "AGENT_DECISION_COMMITTED")); decisionsBefore != 10 || decisions != 11 {
		t.Errorf("Decisions = %d before the restart and %d at the end, want 10 and 11: no Decision asked again", decisionsBefore, decisions)
	}
}

// TestE2E_VulnRelease_CrashAfterSendVoided_VoidedThenReleasedUnderNextKey is the
// release-crash-voided variant: the Backend stops while production holds the release
// request, so the request is abandoned before production accepts it. The restarted Backend
// queries the unknown operation, voids it once the grace has passed and, without a new
// approval, releases the same approved request again under the next operation key, which
// deploys once; the first key is never deployed.
func TestE2E_VulnRelease_CrashAfterSendVoided_VoidedThenReleasedUnderNextKey(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release", graceMs: 10000})
	control(t, s.productionURL, "pause", `{"kinds":["release"]}`)
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	actionID := approveFirst(t, s, runID)
	first, second := "op_"+actionID+"_1", "op_"+actionID+"_2"
	waitBarrierHeld(t, s.productionURL, "release")

	// --- restart while production holds the request: its caller is gone, so it is abandoned.
	s.restart(t)
	waitRecordLine(t, s.productionURL, "abandoned", "release", func(line map[string]any) bool { return line["operationId"] == first })
	control(t, s.productionURL, "release", "")
	s.drive(t, 3*time.Second, runID)

	snapshot := s.env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED\n%s", got, runStateDump(t, s.env, runID))
	}
	trace, story := s.traces(t, runID)
	if c := trace.AgentRun.Completion; c == nil || c.Outcome != "DEPLOYED" {
		t.Errorf("completion = %+v, want DEPLOYED", c)
	}
	release := story.releases()[0]
	attempts := release.ToolAttempts
	if len(attempts) != 2 || attempts[0].Status != "FAILED" || attempts[0].Error == nil || attempts[0].Error.Code != "VOIDED" ||
		attempts[0].CallbackBinding == nil || attempts[0].CallbackBinding.ExternalTaskID != first ||
		attempts[1].Status != "SUCCEEDED" || attempts[1].CallbackBinding == nil || attempts[1].CallbackBinding.ExternalTaskID != second {
		t.Errorf("release attempts = %+v, want %s FAILED VOIDED then %s SUCCEEDED", attempts, first, second)
	}
	if occ := release.Action.Release.Occupation; release.Action.Status != "SUCCEEDED" || occ == nil || occ.OperationKey != second ||
		occ.Status != "RELEASED" || strOf(occ.ReleaseReason) != "DEPLOYED" {
		t.Errorf("release action %s occupation %+v, want SUCCEEDED and RELEASED as DEPLOYED under %s", release.Action.Status, occ, second)
	}
	if approvals := s.approvalsOfRun(t, runID); len(approvals) != 1 {
		t.Errorf("approvals = %v, want the one: a re-release needs no new approval", approvals)
	}
	events := s.env.listEvents(t, runID)
	if n := len(eventsOfType(events, "RELEASE_OUTCOME_UNKNOWN")); n != 1 {
		t.Errorf("RELEASE_OUTCOME_UNKNOWN events = %d, want 1", n)
	}
	if voided := eventPayloads(events, "RELEASE_VOIDED"); len(voided) != 1 || voided[0]["operationKey"] != first || voided[0]["nextOperationKey"] != second {
		t.Errorf("RELEASE_VOIDED = %v, want one voiding %s and naming %s", voided, first, second)
	}
	lines := serviceRecord(t, s.productionURL)
	if productionCount(lines, "arrived", first) != 1 || productionCount(lines, "abandoned", first) != 1 ||
		productionCount(lines, "voided", first) != 1 || productionCount(lines, "deployed", first) != 0 {
		t.Errorf("production lines of %s = arrived %d abandoned %d voided %d deployed %d, want 1 1 1 0", first,
			productionCount(lines, "arrived", first), productionCount(lines, "abandoned", first),
			productionCount(lines, "voided", first), productionCount(lines, "deployed", first))
	}
	if productionCount(lines, "arrived", second) != 1 || productionCount(lines, "deployed", second) != 1 {
		t.Errorf("production lines of %s = arrived %d deployed %d, want once each", second,
			productionCount(lines, "arrived", second), productionCount(lines, "deployed", second))
	}
	if n := len(eventsOfType(events, "AGENT_DECISION_COMMITTED")); n != 11 {
		t.Errorf("Decisions = %d, want 11: no Decision asked again", n)
	}
}

// TestE2E_VulnRelease_VoidExhausted_TargetFreedNotDeployedReturnedToAgent is the
// void-exhausted variant: production holds every release request until the caller gives
// up, so each operation's outcome is unknown; the first is voided and released again under
// the next key, the second is voided too, and with no re-release left the target is freed
// and the Agent is told RELEASE_NOT_DEPLOYED. The task ends INCOMPLETE, never as an
// unknown release outcome.
func TestE2E_VulnRelease_VoidExhausted_TargetFreedNotDeployedReturnedToAgent(t *testing.T) {
	// The short client timeout lets each held request be given up quickly; production's
	// barrier then records it abandoned and accepts nothing under its key.
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release", graceMs: 10000, client: &http.Client{Timeout: 300 * time.Millisecond}})
	control(t, s.productionURL, "pause", `{"kinds":["release"]}`)
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	actionID := approveFirst(t, s, runID)
	first, second := "op_"+actionID+"_1", "op_"+actionID+"_2"
	s.drive(t, 3*time.Second, runID)

	release := assertReturnedRelease(t, s, runID, actionID, "RELEASE_NOT_DEPLOYED", "NOT_DEPLOYED")
	attempts := release.ToolAttempts
	if len(attempts) != 2 || attempts[0].Status != "FAILED" || attempts[1].Status != "FAILED" ||
		attempts[0].Error == nil || attempts[0].Error.Code != "VOIDED" || attempts[1].Error == nil || attempts[1].Error.Code != "VOIDED" {
		t.Errorf("release attempts = %+v, want both FAILED VOIDED", attempts)
	}
	if ops := release.Action.Release.Operations; len(ops) != 2 || ops[0].OperationKey != first || ops[1].OperationKey != second ||
		ops[0].Outcome != "VOIDED" || ops[1].Outcome != "VOIDED" {
		t.Errorf("operations = %+v, want %s and %s both VOIDED", ops, first, second)
	}
	events := s.env.listEvents(t, runID)
	voided := eventPayloads(events, "RELEASE_VOIDED")
	if len(voided) != 2 || voided[0]["operationKey"] != first || voided[0]["nextOperationKey"] != second ||
		voided[1]["operationKey"] != second || voided[1]["nextOperationKey"] != nil {
		t.Errorf("RELEASE_VOIDED = %v, want %s voided and retried as %s, then %s voided with no retry left", voided, first, second, second)
	}
	lines := serviceRecord(t, s.productionURL)
	for _, key := range []string{first, second} {
		if productionCount(lines, "arrived", key) != 1 || productionCount(lines, "abandoned", key) != 1 ||
			productionCount(lines, "voided", key) != 1 || productionCount(lines, "deployed", key) != 0 {
			t.Errorf("production lines of %s = arrived %d abandoned %d voided %d deployed %d, want 1 1 1 0", key,
				productionCount(lines, "arrived", key), productionCount(lines, "abandoned", key),
				productionCount(lines, "voided", key), productionCount(lines, "deployed", key))
		}
	}
	var told bool
	for _, request := range s.env.provider.Requests() {
		for _, message := range toolMessagesOf(t, request) {
			told = told || message["code"] == "RELEASE_NOT_DEPLOYED"
		}
	}
	if !told {
		t.Errorf("no model request carried RELEASE_NOT_DEPLOYED")
	}
}

// TestE2E_VulnRelease_ProductionRefuses_TargetFreedReasonReturnedNoToolError is the
// release-refused variant: production refuses the approved release definitely; the target
// is freed, the refusal code reaches the Agent, which reports instead of failing on a
// Tool error.
func TestE2E_VulnRelease_ProductionRefuses_TargetFreedReasonReturnedNoToolError(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"})
	s.production.SetReleaseFaults(mockproduction.ReleaseFaults{Reject: true, RejectCode: "OUTSIDE_RELEASE_WINDOW"})
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	actionID := approveFirst(t, s, runID)
	s.drive(t, 3*time.Second, runID)

	release := assertReturnedRelease(t, s, runID, actionID, "RELEASE_REFUSED", "REFUSED")
	if len(release.ToolAttempts) != 1 || release.ToolAttempts[0].Status != "FAILED" || release.ToolAttempts[0].Error == nil ||
		release.ToolAttempts[0].Error.Code != "OUTSIDE_RELEASE_WINDOW" {
		t.Errorf("release attempts = %+v, want one FAILED with production's code", release.ToolAttempts)
	}
	var told bool
	for _, request := range s.env.provider.Requests() {
		for _, message := range toolMessagesOf(t, request) {
			text, _ := message["message"].(string)
			told = told || (message["code"] == "RELEASE_REFUSED" && strings.Contains(text, "OUTSIDE_RELEASE_WINDOW"))
		}
	}
	if !told {
		t.Errorf("no model request carried RELEASE_REFUSED with production's code")
	}
	lines := serviceRecord(t, s.productionURL)
	key := "op_" + actionID + "_1"
	if productionCount(lines, "arrived", key) != 1 {
		t.Errorf("production saw %s arrive %d times, want once", key, productionCount(lines, "arrived", key))
	}
}

// TestE2E_VulnRelease_UnhealthyAfterDeploy_ReportedAsCheckFailed is the unhealthy variant:
// the release deploys but the target's health check fails afterwards. The target is freed
// as DEPLOYED, the release_confirmed fact records the failed check, the Agent reports it
// as it is and the task is judged DEPLOYED_CHECK_FAILED.
func TestE2E_VulnRelease_UnhealthyAfterDeploy_ReportedAsCheckFailed(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"})
	s.production.SetReleaseFaults(mockproduction.ReleaseFaults{Unhealthy: true})
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	actionID := approveFirst(t, s, runID)
	s.drive(t, 3*time.Second, runID)

	snapshot := s.env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED: a failed health check is reported, not a failure\n%s", got, runStateDump(t, s.env, runID))
	}
	trace, story := s.traces(t, runID)
	latest := trace.latestPatchDigest(t)
	if c := trace.AgentRun.Completion; c == nil || c.Outcome != "DEPLOYED_CHECK_FAILED" {
		t.Errorf("completion = %+v, want DEPLOYED_CHECK_FAILED", c)
	}
	release := story.releases()[0]
	if occ := release.Action.Release.Occupation; release.Action.Status != "SUCCEEDED" || occ == nil || occ.Status != "RELEASED" || strOf(occ.ReleaseReason) != "DEPLOYED" {
		t.Errorf("release action %s occupation %+v, want SUCCEEDED with the target RELEASED as DEPLOYED", release.Action.Status, occ)
	}
	var confirmed int
	for _, fact := range trace.Facts.Items {
		if fact.FactType != deployrelease.FactType {
			continue
		}
		confirmed++
		if fact.Subject != latest || fact.Verdict == nil || *fact.Verdict {
			t.Errorf("release_confirmed fact = %+v, want an unhealthy verdict about %s", fact, latest)
		}
	}
	if confirmed != 1 {
		t.Errorf("release_confirmed facts = %d, want 1", confirmed)
	}
	if outcome, report := agentReport(t, snapshot); outcome != "DEPLOYED_CHECK_FAILED" || report["status"] != "DEPLOYED_CHECK_FAILED" ||
		report["healthy"] != false || !strings.Contains(report["nextStep"].(string), "rollback plan") {
		t.Errorf("result %s report %v, want DEPLOYED_CHECK_FAILED, unhealthy, leaving the rollback to a person", outcome, report)
	}
	lines := serviceRecord(t, s.productionURL)
	if deployedUnderAction(lines, actionID) != 1 {
		t.Errorf("production deployed the action %d times, want once", deployedUnderAction(lines, actionID))
	}
	var unhealthy bool
	for _, line := range recordLines(lines, "health", "release") {
		unhealthy = unhealthy || line["healthy"] == false
	}
	if !unhealthy {
		t.Errorf("production recorded no unhealthy target")
	}
}
