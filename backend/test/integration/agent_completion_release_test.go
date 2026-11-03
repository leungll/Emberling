//go:build integration

package integration

import (
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

// releasedTaskTurn is the Turn of the release call in the stories below: the write, the
// verification of the written patch and its change request come first.
const releasedTaskTurn = 4

// startPatchedRelease drives a FIX_AND_DEPLOY task through real material up to the release
// call waiting on a person: the Agent writes a patch inside the allowed paths, verifies
// exactly that patch, describes it in a change request and asks to release it. It returns
// the WAITING_APPROVAL release Action.
func (f *releaseFixture) startPatchedRelease(t *testing.T, workflowID string) domain.AgentAction {
	t.Helper()
	return f.startPatchedReleaseWith(t, workflowID, f.releaseCall(t))
}

// startPatchedReleaseWith is startPatchedRelease with the release Decision the given call
// scripts.
func (f *releaseFixture) startPatchedReleaseWith(t *testing.T, workflowID string, release crCall) domain.AgentAction {
	t.Helper()
	f.start(t, workflowID,
		fixed(wsWrite(wsTokenPath, wsNewToken)),
		func() mockmodel.Scenario {
			return wsCall(verifypatch.ToolName, map[string]any{"patchDigest": wsLatestPatch(t, f.h, f.agentRunID(t)).PatchDigest})
		},
		func() mockmodel.Scenario {
			// Turn 3 is the first request carrying the verification's result, so its Manifest
			// holds the TOOL_RESULT item the basis cites.
			verifyAttempt := agentOnlyToolAttempt(t, f.h, wsActionOfTurn(t, f.h, f.agentRunID(t), 2).ID)
			return wsCall(changerequest.ToolName, changeRequestArguments(wsLatestPatch(t, f.h, f.agentRunID(t)).PatchDigest,
				basisItem("The verification of the patch passed every check.", 3, verifyAttempt.ID)))
		},
		release,
	)
	latest := wsLatestPatch(t, f.h, f.agentRunID(t))
	if latest == nil {
		t.Fatalf("no workspace patch was recorded before the verification")
	}
	token, _ := f.stub.dispatched(t)
	delivered, err := f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{
		Token: token, ExternalTaskID: f.stub.externalTaskID(),
		Payload: verifySucceededPayload(f.repo.commit, latest.PatchDigest, "sha256:verified", true),
	})
	if err != nil || !delivered.Accepted || delivered.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns after the verification: %v", err)
	}
	action := f.actionOfTurn(t, releasedTaskTurn)
	if action.Status != domain.AgentActionWaitingApproval {
		t.Fatalf("release action = %s %+v, want WAITING_APPROVAL", action.Status, action.Error)
	}
	return action
}

// TestAgentCompletion_ReleasedToHealthyTarget_Deployed drives the completion check with the
// fact the real release records: the approved deploy_change call is deployed by the mock
// production service, the poll learns DEPLOYED and the healthy release_confirmed fact about
// the latest patch commits. The Agent's FINAL is then judged DEPLOYED: every check passes,
// the release check cites that fact, the Agent NodeRun SUCCEEDED and published the verdict,
// and the Run COMPLETED.
func TestAgentCompletion_ReleasedToHealthyTarget_Deployed(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{})
	action := f.startPatchedRelease(t, "wf_completion_deployed")

	approved := f.approve(t, action)
	f.poll(t, f.reconciler())

	agentRunID := f.agentRunID(t)
	latest := wsLatestPatch(t, f.h, agentRunID)
	facts := f.releaseFacts(t)
	if len(facts) != 1 || facts[0].SubjectRef != latest.PatchDigest || facts[0].Verdict == nil || !*facts[0].Verdict {
		t.Fatalf("release facts = %+v, want one healthy release_confirmed fact about the latest patch %s", facts, latest.PatchDigest)
	}

	completion := f.finish(t)
	if completion.Outcome != domain.CompletionDeployed || completion.Goal != domain.AgentGoalFixAndDeploy || len(completion.Missing) != 0 {
		t.Fatalf("verdict = %+v, want DEPLOYED for FIX_AND_DEPLOY with nothing missing", completion)
	}
	byName := map[string]domain.CompletionCheck{}
	for _, check := range completion.Checks {
		byName[check.Name] = check
		if !check.Passed {
			t.Errorf("check %+v failed in a DEPLOYED verdict", check)
		}
	}
	for _, name := range []string{runtime.CheckWorkspacePatch, runtime.CheckPatchVerified, runtime.CheckChangeRequest,
		runtime.CheckApproval, runtime.CheckReleaseConfirmed, runtime.CheckReleaseVersionBound, runtime.CheckReleaseHealthCheck} {
		if _, ok := byName[name]; !ok {
			t.Errorf("verdict has no %s check: %+v", name, completion.Checks)
		}
	}
	if refs := byName[runtime.CheckReleaseConfirmed].Refs; len(refs) != 1 || refs[0] != facts[0].ID {
		t.Errorf("release check refs = %v, want the release_confirmed fact %s", refs, facts[0].ID)
	}
	if refs := byName[runtime.CheckApproval].Refs; len(refs) != 1 || refs[0] != approved.Approval.ID {
		t.Errorf("approval check refs = %v, want the release's approval", refs)
	}

	nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, f.run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run = %s %+v, want SUCCEEDED", nodeRun.Status, nodeRun.Error)
	}
	if output := decodeCompletionNodeOutput(t, nodeRun); output.Outcome != domain.CompletionDeployed || len(output.Evidence) == 0 {
		t.Errorf("node output = %+v, want the DEPLOYED verdict with its evidence", output)
	}
	payload := agentLastEventPayload(t, f.events(t), domain.EventAgentCompletionEvaluated)
	if payload["outcome"] != string(domain.CompletionDeployed) || payload["agentRunId"] != agentRunID {
		t.Errorf("AGENT_COMPLETION_EVALUATED payload = %v, want the DEPLOYED verdict of %s", payload, agentRunID)
	}
}

// TestAgentCompletion_ReleasedToUnhealthyTarget_DeployedCheckFailed is the same release with
// the target unhealthy afterwards: the release_confirmed fact about the latest patch has a
// false verdict, so the task is DEPLOYED_CHECK_FAILED, a successful Agent step that names
// the failed health check, and nothing else is missing.
func TestAgentCompletion_ReleasedToUnhealthyTarget_DeployedCheckFailed(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{})
	action := f.startPatchedRelease(t, "wf_completion_deployed_unhealthy")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unhealthy: true})

	f.approve(t, action)
	f.poll(t, f.reconciler())

	completion := f.finish(t)
	if completion.Outcome != domain.CompletionDeployedCheckFailed || len(completion.Missing) != 0 {
		t.Fatalf("verdict = %+v, want DEPLOYED_CHECK_FAILED with nothing listed as missing", completion)
	}
	for _, check := range completion.Checks {
		if check.Passed == (check.Name == runtime.CheckReleaseHealthCheck) {
			t.Errorf("check %+v, want only the health check failed", check)
		}
	}
	if nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, f.run.ID); nodeRun.Status != domain.NodeRunSucceeded {
		t.Errorf("agent node run = %s %+v, want SUCCEEDED", nodeRun.Status, nodeRun.Error)
	}
}
