//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

// releaseTrace is the slice of the Agent Trace the release story asserts on: every Action's
// release view with its Attempts, and the fact ledger.
type releaseTrace struct {
	Turns []struct {
		Action *struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Release *struct {
				Operations []struct {
					OperationKey string `json:"operationKey"`
					Outcome      string `json:"outcome"`
					Status       string `json:"status"`
				} `json:"operations"`
				Occupation *struct {
					Status        string  `json:"status"`
					ReleaseReason *string `json:"releaseReason"`
					OperationKey  string  `json:"operationKey"`
					TargetKey     string  `json:"targetKey"`
				} `json:"occupation"`
				HumanWait *struct {
					Status string `json:"status"`
				} `json:"humanWait"`
			} `json:"release"`
		} `json:"action"`
		ToolAttempts []deliveryToolAttempt `json:"toolAttempts"`
	} `json:"turns"`
}

// releaseReport is the FINAL document of the release story.
type releaseReport struct {
	Status          string `json:"status"`
	Advisory        string `json:"advisory"`
	PatchDigest     string `json:"patchDigest"`
	ChangeRequestID string `json:"changeRequestId"`
	OperationKey    string `json:"operationKey"`
	Version         string `json:"version"`
	Healthy         bool   `json:"healthy"`
}

// TestE2E_VulnFix_Release_ApprovedDeploysOnceAndConfirmsTarget plays the demo's release
// story through the public API with the scripted trajectory it uses: the Agent repairs and
// verifies the patch, describes it in a change request citing the files it relied on, and
// asks to release it; the operator approves; the approval occupies the frozen target and
// starts one operation under its key with a release Binding and no callback credential;
// the mock production service deploys it once, the poll learns DEPLOYED, the target is
// checked, and the occupation, the healthy release_confirmed fact and the Agent's report
// all name that one operation.
func TestE2E_VulnFix_Release_ApprovedDeploysOnceAndConfirmsTarget(t *testing.T) {
	f := newVulnFixture(t)
	artifacts := asset.NewArtifactStore(f.assetRoot, defaultTestArtifactMaxBytes)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	record, err := mockproduction.OpenRecord(filepath.Join(t.TempDir(), "production.jsonl"))
	if err != nil {
		t.Fatalf("open mock production record: %v", err)
	}
	// A zero completion delay makes the mock deploy an accepted release inside the request
	// that accepts it, so the first poll already learns DEPLOYED without a real-time wait.
	production := mockproduction.NewServer(mockproduction.WithTestControls(record), mockproduction.WithCompletionDelay(0))
	productionHTTP := httptest.NewServer(production)
	t.Cleanup(func() {
		production.StopHolding()
		productionHTTP.Close()
		_ = record.Close()
	})
	clock := &takeoverClock{}
	env := f.startBackend(t, testEnvOptions{
		Clock:         clock,
		OperatorToken: testOperatorToken,
		ExtraToolRegistrations: []registry.ToolRegistration{
			changerequest.Registration(artifacts), deployrelease.Registration(productionHTTP.URL, nil),
		},
	})

	workflowID, version := saveVulnDefinitionWith(t, env, "vuln-fix-release", func(config map[string]any) {
		config["allowedTools"] = []string{"read_file", "list_files", "write_file", verifypatch.ToolName, changerequest.ToolName, deployrelease.ToolName}
		workspace, _ := config["workspace"].(map[string]any)
		workspace["deployTarget"] = map[string]any{"service": "token-service", "environment": "staging"}
		config["approvalTimeoutMs"] = 3600000
		config["releaseConfirmTimeoutMs"] = 600000
		config["voidGraceMs"] = 600000
		config["maxReleaseRetries"] = 1
		config["humanWaitTimeoutMs"] = 600000
	})
	task := map[string]any{"advisory": vulnAdvisory, "goal": "FIX_AND_DEPLOY", "service": "token-service"}
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": map[string]any{"task": task},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (release story) status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)

	approval := waitForPendingApproval(t, env, runID, e2eWait)
	if approval["toolName"] != deployrelease.ToolName {
		t.Fatalf("pending approval = %v, want one for %s", approval, deployrelease.ToolName)
	}
	approvalID, _ := approval["id"].(string)
	actionID, _ := approval["actionId"].(string)
	operator := &approvalEnv{testEnv: env}
	resp, body = operator.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "APPROVE"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST decision status = %d, body=%s", resp.StatusCode, body)
	}

	// The harness runs no Reconciler ticker: once the approval has dispatched the release
	// and the Agent waits on its answer, drive the poll the way the restarted process
	// would, one pass at a time past the Tool's poll interval, until the Run ends.
	env.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", e2eWait)
	rec := reconciler.New(reconciler.Config{UoW: env.uow, Executor: env.execution, Clock: clock, BatchLimit: 100})
	for pass := 1; !env.allTerminal(t, []string{runID}); pass++ {
		if pass > 50 {
			t.Fatalf("run did not end after %d reconciler passes\n%s", pass-1, runStateDump(t, env, runID))
		}
		clock.advance(3 * time.Second)
		if report, err := rec.RunOnce(context.Background()); err != nil || len(report.Errors) != 0 {
			t.Fatalf("reconciler pass %d: err=%v errors=%v", pass, err, report.Errors)
		}
		time.Sleep(20 * time.Millisecond)
	}
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	agentNodeRun := nodeRunIDOf(t, snapshot, "node_agent")
	trace := loadVulnTrace(t, env, runID, agentNodeRun)
	latest := trace.latestPatchDigest(t)
	status, raw := getAgentTrace(t, env, runID, agentNodeRun)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, raw)
	}
	release := decodeBody[releaseTrace](t, raw)

	wantKey := "op_" + actionID + "_1"
	releases := trace.callsOf(deployrelease.ToolName)
	if len(releases) != 1 || releases[0].actionID != actionID || releases[0].attempt.Status != "SUCCEEDED" || releases[0].actionStatus != "SUCCEEDED" {
		t.Fatalf("deploy_change calls = %+v, want one SUCCEEDED Attempt of action %s", releases, actionID)
	}
	if binding := releases[0].attempt.CallbackBinding; binding == nil || binding.ProviderID != deployrelease.ProviderID || binding.ExternalTaskID != wantKey {
		t.Errorf("release binding = %+v, want provider %s keyed %s", binding, deployrelease.ProviderID, wantKey)
	}
	var seen bool
	for _, turn := range release.Turns {
		if turn.Action == nil || turn.Action.ID != actionID {
			if turn.Action != nil && turn.Action.Release != nil {
				t.Errorf("action %s carries a release view %+v, want none outside the release call", turn.Action.ID, turn.Action.Release)
			}
			continue
		}
		seen = true
		view := turn.Action.Release
		if view == nil {
			t.Fatalf("the release action has no release view")
		}
		if len(view.Operations) != 1 || view.Operations[0].OperationKey != wantKey || view.Operations[0].Outcome != "DEPLOYED" {
			t.Errorf("operations = %+v, want one DEPLOYED %s", view.Operations, wantKey)
		}
		if view.Occupation == nil || view.Occupation.Status != "RELEASED" || view.Occupation.ReleaseReason == nil || *view.Occupation.ReleaseReason != "DEPLOYED" ||
			view.Occupation.OperationKey != wantKey || view.Occupation.TargetKey != "token-service/staging" {
			t.Errorf("occupation = %+v, want RELEASED DEPLOYED under %s on token-service/staging", view.Occupation, wantKey)
		}
		if view.HumanWait != nil {
			t.Errorf("human wait = %+v, want none", view.HumanWait)
		}
	}
	if !seen {
		t.Fatalf("the trace has no turn for action %s", actionID)
	}

	var confirmed []struct {
		Subject string
		Verdict *bool
	}
	for _, fact := range trace.Facts.Items {
		if fact.FactType == deployrelease.FactType {
			confirmed = append(confirmed, struct {
				Subject string
				Verdict *bool
			}{fact.Subject, fact.Verdict})
		}
	}
	if len(confirmed) != 1 || confirmed[0].Subject != latest || confirmed[0].Verdict == nil || !*confirmed[0].Verdict {
		t.Errorf("release_confirmed facts = %+v, want one healthy fact about %s", confirmed, latest)
	}

	text, _ := nodeRunOutputOf(t, snapshot, "node_result")["text"].(string)
	var report releaseReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatalf("release report %q is not JSON: %v", text, err)
	}
	if report.Status != "RELEASED" || report.Advisory != vulnAdvisory || report.PatchDigest != latest || report.OperationKey != wantKey || !report.Healthy || report.Version == "" || report.ChangeRequestID == "" {
		t.Errorf("release report = %+v, want RELEASED for %s on patch %s under %s", report, vulnAdvisory, latest, wantKey)
	}

	lines := serviceRecord(t, productionHTTP.URL)
	deployed := recordLines(lines, "deployed", "release")
	if len(deployed) != 1 || deployed[0]["operationId"] != wantKey || deployed[0]["patchDigest"] != latest {
		t.Errorf("production deployed lines = %v, want exactly one for %s with patch %s", deployed, wantKey, latest)
	}
	if arrived := recordLines(lines, "arrived", "release"); len(arrived) != 1 {
		t.Errorf("production release arrivals = %v, want exactly one", arrived)
	}
	events := env.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := len(eventsOfType(events, "AGENT_DECISION_COMMITTED")); n != 11 {
		t.Errorf("AGENT_DECISION_COMMITTED events = %d, want 11: ten scripted Turns and the inserted adaptation", n)
	}
	for _, typ := range []string{"RELEASE_OUTCOME_UNKNOWN", "RELEASE_VOIDED", "AGENT_ACTION_WAITING_HUMAN"} {
		if n := len(eventsOfType(events, typ)); n != 0 {
			t.Errorf("%s events = %d, want none in the plain release", typ, n)
		}
	}
	for _, event := range eventsOfType(events, "AGENT_ACTION_STARTED") {
		payload, _ := event["payload"].(map[string]any)
		if payload["actionId"] == actionID && (payload["operationKey"] != wantKey || payload["occupationId"] == nil) {
			t.Errorf("AGENT_ACTION_STARTED of the release = %v, want operationKey %s and an occupationId", payload, wantKey)
		}
	}
}
