//go:build integration

package contract

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

// The stop variants of the vulnerability-fix demo and the Agent that declares completion
// too early, each played over the public API the way the demo script plays it.

// pass advances the story clock by step and runs one reconciler pass that must report no
// error, the work a running process's Reconciler ticker would do.
func (s *releaseStory) pass(t *testing.T, step time.Duration) reconciler.Report {
	t.Helper()
	s.clock.advance(step)
	report, err := s.rec.RunOnce(context.Background())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("reconciler pass: err=%v errors=%v", err, report.Errors)
	}
	return report
}

// openHumanWaitOf returns the OPEN human wait of runID as the operator list shows it, or
// nil.
func (s *releaseStory) openHumanWaitOf(t *testing.T, runID string) map[string]any {
	t.Helper()
	resp, raw := s.operator.doOperator(t, testOperatorToken, http.MethodGet, "/api/human-waits", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/human-waits status = %d, body=%s", resp.StatusCode, raw)
	}
	items, _ := decodeBody[map[string]any](t, raw)["items"].([]any)
	for _, item := range items {
		if wait, _ := item.(map[string]any); wait["runId"] == runID {
			return wait
		}
	}
	return nil
}

// stopAfterConfirmDeadline drives reconciler passes until the confirm deadline handed the
// release of runID to a person, checks why the list says it was handed over, stops the
// wait through the operator API and returns the ended Run's snapshot. The task ends
// FAILED as RELEASE_UNKNOWN, ended by HUMAN_STOPPED.
func (s *releaseStory) stopAfterConfirmDeadline(t *testing.T, runID, actionID, wantReason string) map[string]any {
	t.Helper()
	var wait map[string]any
	for pass := 1; wait == nil; pass++ {
		if pass > 20 {
			t.Fatalf("no human wait opened after 20 reconciler passes\n%s", runStateDump(t, s.env, runID))
		}
		s.pass(t, 3*time.Second)
		wait = s.openHumanWaitOf(t, runID)
	}
	if wait["actionId"] != actionID || wait["operationKey"] != "op_"+actionID+"_1" || wait["targetKey"] != "token-service/staging" ||
		wait["reason"] != wantReason {
		t.Errorf("open human wait = %v, want the release of %s on token-service/staging handed over as %s", wait, actionID, wantReason)
	}
	waitID, _ := wait["id"].(string)
	resp, raw := s.operator.doOperator(t, testOperatorToken, http.MethodPost, "/api/human-waits/"+waitID+"/stop", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST stop status = %d, body=%s", resp.StatusCode, raw)
	}
	snapshot := s.env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status after the stop = %q, want FAILED", got)
	}
	if nodeError, _ := vulnNodeRunOf(t, snapshot, "node_agent")["error"].(map[string]any); nodeError["code"] != "RELEASE_UNKNOWN" {
		t.Errorf("node_agent error = %v, want RELEASE_UNKNOWN", nodeError)
	}
	trace, _ := s.traces(t, runID)
	if strOf(trace.AgentRun.Termination) != "HUMAN_STOPPED" || trace.AgentRun.Completion == nil || trace.AgentRun.Completion.Outcome != "RELEASE_UNKNOWN" {
		t.Errorf("agent run = termination %v completion %+v, want HUMAN_STOPPED judged RELEASE_UNKNOWN", trace.AgentRun.Termination, trace.AgentRun.Completion)
	}
	view := s.releaseViewOf(t, runID, actionID)
	if occ := view.Occupation; occ == nil || occ.Status != "ACTIVE" || occ.NextQueryAt == nil {
		t.Errorf("occupation after the stop = %+v, want ACTIVE with a post-task query schedule", occ)
	}
	if hw := view.HumanWait; hw == nil || hw.Status != "STOPPED" || strOf(hw.ClosedBy) != testOperatorName || strOf(hw.Outcome) != "RELEASE_UNKNOWN" {
		t.Errorf("human wait after the stop = %+v, want STOPPED by %s as RELEASE_UNKNOWN", hw, testOperatorName)
	}
	return snapshot
}

// releaseViewOf reads the release view of actionID from runID's Agent Trace.
func (s *releaseStory) releaseViewOf(t *testing.T, runID, actionID string) humanStopRelease {
	t.Helper()
	agentNodeRun := nodeRunIDOf(t, s.env.runSnapshot(t, runID), "node_agent")
	status, raw := getAgentTrace(t, s.env, runID, agentNodeRun)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, raw)
	}
	for _, turn := range decodeBody[humanWaitTrace](t, raw).Turns {
		if turn.Action != nil && turn.Action.ID == actionID && turn.Action.Release != nil {
			return *turn.Action.Release
		}
	}
	t.Fatalf("the trace of %s has no release view for action %s", runID, actionID)
	return humanStopRelease{}
}

// awaitOccupationReleased drives reconciler passes until post-task reconciliation freed a
// target.
func (s *releaseStory) awaitOccupationReleased(t *testing.T) {
	t.Helper()
	for pass := 1; ; pass++ {
		if pass > 30 {
			t.Fatalf("no occupation released after %d reconciler passes", pass-1)
		}
		if report := s.pass(t, 10*time.Second); report.OccupationsReleased > 0 {
			return
		}
	}
}

// TestE2E_VulnRelease_UnreachableStop_TargetHeldUntilVoidedThenNewTaskReleases is the
// release-unreachable-stop variant: production answers nothing but 503 from before the
// approved release is sent, so the outcome stays unknown until the confirm deadline hands
// it to a person, who stops waiting. The task ends as RELEASE_UNKNOWN with the target
// still occupied, and a new task's release request to that target is refused as
// TARGET_OCCUPIED before any approval. Once production is back, post-task reconciliation
// voids the never-received operation and frees the target as VOIDED without touching the
// ended Run, and a further task releases to the same target once.
func TestE2E_VulnRelease_UnreachableStop_TargetHeldUntilVoidedThenNewTaskReleases(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release", confirmMs: 10000, graceMs: 10000})
	s.production.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	stopped := s.createRun(t, "FIX_AND_DEPLOY")
	actionID := approveFirst(t, s, stopped)
	key := "op_" + actionID + "_1"
	s.stopAfterConfirmDeadline(t, stopped, actionID, "OUTCOME_UNKNOWN")
	eventsAtEnd := len(s.env.listEvents(t, stopped))
	if unknown := eventPayloads(s.env.listEvents(t, stopped), "RELEASE_OUTCOME_UNKNOWN"); len(unknown) != 1 || unknown[0]["operationKey"] != key {
		t.Errorf("RELEASE_OUTCOME_UNKNOWN = %v, want one for %s", unknown, key)
	}

	// While production stays unreachable, the target stays held: a new task is refused.
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
	if c := trace.AgentRun.Completion; c == nil || c.Outcome != "INCOMPLETE" {
		t.Errorf("contender completion = %+v, want INCOMPLETE", c)
	}
	if approvals := s.approvalsOfRun(t, contender); len(approvals) != 0 {
		t.Errorf("approvals of the contender = %v, want none", approvals)
	}
	if report := s.pass(t, 10*time.Second); report.OccupationsReleased != 0 {
		t.Fatalf("a post-task query freed the target while production was unreachable: %+v", report)
	}
	if occ := s.releaseViewOf(t, stopped, actionID).Occupation; occ == nil || occ.Status != "ACTIVE" {
		t.Fatalf("occupation while production is unreachable = %+v, want ACTIVE", occ)
	}

	// Production is back: the operation it never received is voided and the target freed.
	s.production.SetReleaseFaults(mockproduction.ReleaseFaults{})
	s.awaitOccupationReleased(t)
	view := s.releaseViewOf(t, stopped, actionID)
	if occ := view.Occupation; occ == nil || occ.Status != "RELEASED" || strOf(occ.ReleaseReason) != "VOIDED" || occ.NextQueryAt != nil ||
		occ.DeployedUncheckedVersion != nil {
		t.Errorf("occupation after production came back = %+v, want RELEASED as VOIDED with no schedule and no unchecked version", occ)
	}
	if n := len(s.env.listEvents(t, stopped)); n != eventsAtEnd {
		t.Errorf("post-task reconciliation wrote %d events on the ended Run, want none", n-eventsAtEnd)
	}
	if got := runStatusOf(s.env.runSnapshot(t, stopped)); got != "FAILED" {
		t.Errorf("stopped Run status after the void = %q, want FAILED unchanged", got)
	}
	lines := serviceRecord(t, s.productionURL)
	if productionCount(lines, "arrived", key) != 0 || productionCount(lines, "voided", key) != 1 || productionCount(lines, "deployed", key) != 0 {
		t.Errorf("production lines of %s = arrived %d voided %d deployed %d, want 0 1 0: it never received the operation",
			key, productionCount(lines, "arrived", key), productionCount(lines, "voided", key), productionCount(lines, "deployed", key))
	}

	// The freed target accepts a new task's release.
	successor := s.createRun(t, "FIX_AND_DEPLOY")
	successorAction := approveFirst(t, s, successor)
	s.drive(t, 3*time.Second, successor)
	assertDeployedOnce(t, s, successor, successorAction)
}

// TestE2E_VulnRelease_StopThenDeployed_TargetFreedWithUncheckedVersion is the
// stop-then-deployed variant: production accepts the approved release but does not finish
// it by the confirm deadline, the person stops waiting and the task ends as
// RELEASE_UNKNOWN. Production then finishes the deployment; post-task reconciliation frees
// the target as DEPLOYED and records the version it runs as live but never checked, the
// ended Run gains no Event and no release_confirmed fact, and production deployed the
// operation once.
func TestE2E_VulnRelease_StopThenDeployed_TargetFreedWithUncheckedVersion(t *testing.T) {
	s := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release", confirmMs: 10000})
	s.production.PauseCompletion()
	runID := s.createRun(t, "FIX_AND_DEPLOY")
	actionID := approveFirst(t, s, runID)
	key := "op_" + actionID + "_1"
	waitRecordLine(t, s.productionURL, "arrived", "release", func(line map[string]any) bool { return line["operationId"] == key })
	s.stopAfterConfirmDeadline(t, runID, actionID, "NOT_CONFIRMED")
	eventsAtEnd := len(s.env.listEvents(t, runID))

	s.production.ResumeCompletion()
	s.awaitOccupationReleased(t)
	lines := serviceRecord(t, s.productionURL)
	deployed := recordLines(lines, "deployed", "release")
	if len(deployed) != 1 || deployed[0]["operationId"] != key {
		t.Fatalf("production deployed lines = %v, want exactly %s", deployed, key)
	}
	view := s.releaseViewOf(t, runID, actionID)
	if occ := view.Occupation; occ == nil || occ.Status != "RELEASED" || strOf(occ.ReleaseReason) != "DEPLOYED" || occ.NextQueryAt != nil ||
		occ.DeployedUncheckedVersion == nil || *occ.DeployedUncheckedVersion != deployed[0]["version"] {
		t.Errorf("occupation after the late deploy = %+v, want RELEASED as DEPLOYED with the unchecked version %v", occ, deployed[0]["version"])
	}
	if n := len(s.env.listEvents(t, runID)); n != eventsAtEnd {
		t.Errorf("post-task reconciliation wrote %d events on the ended Run, want none", n-eventsAtEnd)
	}
	trace, _ := s.traces(t, runID)
	for _, fact := range trace.Facts.Items {
		if fact.FactType == deployrelease.FactType {
			t.Errorf("release_confirmed fact %+v on the stopped Run, want none: the late deployment was never checked by the task", fact)
		}
	}
	if c := trace.AgentRun.Completion; c == nil || c.Outcome != "RELEASE_UNKNOWN" {
		t.Errorf("completion after the late deploy = %+v, want RELEASE_UNKNOWN unchanged", c)
	}
	if productionCount(lines, "arrived", key) != 1 || deployedUnderAction(lines, actionID) != 1 {
		t.Errorf("production saw %s arrive %d times and deployed the action %d times, want once each",
			key, productionCount(lines, "arrived", key), deployedUnderAction(lines, actionID))
	}
}

// TestE2E_VulnFix_IncompleteFinal_MissingFactsListed is the incomplete-final variant: the
// Agent adapts the code after the failed verification of the unadapted upgrade and declares
// completion at once. The completion check finds no verification of the latest patch and no
// change request, so the task is INCOMPLETE and names exactly those two facts; the runner
// verified only the earlier patch.
func TestE2E_VulnFix_IncompleteFinal_MissingFactsListed(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := createVulnRun(t, env, "vuln-fix-incomplete-final")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	trace := loadVulnTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	completion := assertVulnIncomplete(t, snapshot, trace, "PATCH_VERIFICATION_MISSING")
	if codes := completion.missingCodes(); !slices.Equal(codes, []string{"PATCH_VERIFICATION_MISSING", "CHANGE_REQUEST_MISSING"}) {
		t.Errorf("completion.missing = %v, want exactly the latest patch's verification and the change request", codes)
	}
	for name, want := range map[string]bool{
		"agent_declared_completion":         true,
		"workspace_patch_recorded":          true,
		"latest_patch_verified":             false,
		"change_request_binds_latest_patch": false,
	} {
		if passed, _, found := completion.check(name); !found || passed != want {
			t.Errorf("completion check %s = passed %v found %v, want passed %v", name, passed, found, want)
		}
	}

	latest := trace.latestPatchDigest(t)
	facts := verifiedFacts(trace)
	if len(facts) != 1 || facts[0].Subject == latest {
		t.Errorf("patch_verified facts = %+v, want one about an earlier patch than the latest %s", facts, latest)
	}
	if len(trace.ChangeRequests.Items) != 0 {
		t.Errorf("change requests = %+v, want none", trace.ChangeRequests.Items)
	}
	if verifications := trace.callsOf(verifypatch.ToolName); len(verifications) != 1 {
		t.Errorf("verify_patch Attempts = %d, want 1", len(verifications))
	}
	arrived := recordLines(serviceRecord(t, f.runner.http.URL), "arrived", "verification")
	if len(arrived) != 1 || arrived[0]["patchDigest"] == latest {
		t.Errorf("runner verification arrivals = %v, want one, of an earlier patch than the latest", arrived)
	}
	if n := len(eventsOfType(env.listEvents(t, runID), "AGENT_DECISION_COMMITTED")); n != 8 {
		t.Errorf("AGENT_DECISION_COMMITTED events = %d, want 8: seven Tool calls and the FINAL", n)
	}
}
