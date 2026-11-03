//go:build integration

package contract

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// humanStopRelease is the release view of one Action as the human stop story reads it.
type humanStopRelease struct {
	Operations []struct {
		OperationKey string `json:"operationKey"`
		Outcome      string `json:"outcome"`
	} `json:"operations"`
	Occupation *struct {
		Status                   string  `json:"status"`
		ReleaseReason            *string `json:"releaseReason"`
		NextQueryAt              *string `json:"nextQueryAt"`
		DeployedUncheckedVersion *string `json:"deployedUncheckedVersion"`
	} `json:"occupation"`
	HumanWait *struct {
		Status   string  `json:"status"`
		ClosedBy *string `json:"closedBy"`
		Outcome  *string `json:"outcome"`
	} `json:"humanWait"`
}

// humanWaitTrace is the slice of the Agent Trace the human stop story asserts on.
type humanWaitTrace struct {
	Turns []struct {
		Action *struct {
			ID      string            `json:"id"`
			Release *humanStopRelease `json:"release"`
		} `json:"action"`
	} `json:"turns"`
}

// humanStopStory is one Backend serving the release story against a mock production
// service whose completion the test holds, with its log output captured.
type humanStopStory struct {
	env        *testEnv
	operator   *approvalEnv
	production *mockproduction.Server
	clock      *takeoverClock
	logs       *syncBuffer
	rec        *reconciler.Reconciler
	runID      string
	actionID   string
}

// startHumanStopStory drives the scripted release story until the approved release is
// dispatched and production holds it in DEPLOYING.
func startHumanStopStory(t *testing.T) *humanStopStory {
	t.Helper()
	f := newVulnFixture(t)
	artifacts := asset.NewArtifactStore(f.assetRoot, defaultTestArtifactMaxBytes)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	record, err := mockproduction.OpenRecord(filepath.Join(t.TempDir(), "production.jsonl"))
	if err != nil {
		t.Fatalf("open mock production record: %v", err)
	}
	production := mockproduction.NewServer(mockproduction.WithTestControls(record), mockproduction.WithCompletionDelay(0))
	production.PauseCompletion()
	productionHTTP := httptest.NewServer(production)
	t.Cleanup(func() {
		production.StopHolding()
		productionHTTP.Close()
		_ = record.Close()
	})
	clock := &takeoverClock{}
	logs := &syncBuffer{}
	env := f.startBackend(t, testEnvOptions{
		Clock:         clock,
		OperatorToken: testOperatorToken,
		Logger:        slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ExtraToolRegistrations: []registry.ToolRegistration{
			changerequest.Registration(artifacts), deployrelease.Registration(productionHTTP.URL, nil),
		},
	})
	workflowID, version := saveVulnDefinitionWith(t, env, "vuln-fix-release", func(config map[string]any) {
		config["allowedTools"] = []string{"read_file", "list_files", "write_file", verifypatch.ToolName, changerequest.ToolName, deployrelease.ToolName}
		workspace, _ := config["workspace"].(map[string]any)
		workspace["deployTarget"] = map[string]any{"service": "token-service", "environment": "staging"}
		config["approvalTimeoutMs"] = 3600000
		config["releaseConfirmTimeoutMs"] = 10000
		config["voidGraceMs"] = 600000
		config["maxReleaseRetries"] = 1
		config["humanWaitTimeoutMs"] = 600000
	})
	task := map[string]any{"advisory": vulnAdvisory, "goal": "FIX_AND_DEPLOY", "service": "token-service"}
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": map[string]any{"task": task},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	approval := waitForPendingApproval(t, env, runID, e2eWait)
	approvalID, _ := approval["id"].(string)
	actionID, _ := approval["actionId"].(string)
	operator := &approvalEnv{testEnv: env}
	resp, body = operator.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "APPROVE"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST decision status = %d, body=%s", resp.StatusCode, body)
	}
	env.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", e2eWait)
	return &humanStopStory{
		env: env, operator: operator, production: production, clock: clock, logs: logs,
		rec:   reconciler.New(reconciler.Config{UoW: env.uow, Executor: env.execution, Clock: clock, BatchLimit: 100}),
		runID: runID, actionID: actionID,
	}
}

// pass advances the clock by step and runs one reconciler pass that must report no error.
func (s *humanStopStory) pass(t *testing.T, step time.Duration) reconciler.Report {
	t.Helper()
	s.clock.advance(step)
	report, err := s.rec.RunOnce(context.Background())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("reconciler pass: err=%v errors=%v", err, report.Errors)
	}
	return report
}

// openWait lists the OPEN human waits through the API and returns the one of the story's
// Run, or nil.
func (s *humanStopStory) openWait(t *testing.T) map[string]any {
	t.Helper()
	resp, body := s.operator.doOperator(t, testOperatorToken, http.MethodGet, "/api/human-waits", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/human-waits status = %d, body=%s", resp.StatusCode, body)
	}
	items, _ := decodeBody[map[string]any](t, body)["items"].([]any)
	for _, raw := range items {
		if item, _ := raw.(map[string]any); item["runId"] == s.runID {
			return item
		}
	}
	return nil
}

// releaseView reads the release view of the story's release Action from the Agent Trace,
// with the raw Trace body.
func (s *humanStopStory) releaseView(t *testing.T) ([]byte, humanStopRelease) {
	t.Helper()
	agentNodeRun := nodeRunIDOf(t, s.env.runSnapshot(t, s.runID), "node_agent")
	status, raw := getAgentTrace(t, s.env, s.runID, agentNodeRun)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, raw)
	}
	for _, turn := range decodeBody[humanWaitTrace](t, raw).Turns {
		if turn.Action != nil && turn.Action.ID == s.actionID && turn.Action.Release != nil {
			return raw, *turn.Action.Release
		}
	}
	t.Fatalf("the trace has no release view for action %s: %s", s.actionID, raw)
	return nil, humanStopRelease{}
}

// TestE2E_HumanStop_StopThroughAPI_TaskEndsAndLateDeployFreesTargetUnchecked plays the
// operator stop through the public API: production never confirms the approved release,
// the confirm deadline hands the release to a person, the human wait list shows the target,
// the operation and the reason, refused stops change nothing, the operator stops the wait,
// the task ends as HUMAN_STOPPED while the target stays occupied with a query schedule, a
// repeated stop is answered as repeated, and production's late DEPLOYED frees the target
// with the version it runs recorded as unchecked, without touching the ended Run.
func TestE2E_HumanStop_StopThroughAPI_TaskEndsAndLateDeployFreesTargetUnchecked(t *testing.T) {
	s := startHumanStopStory(t)

	var wait map[string]any
	for pass := 1; wait == nil; pass++ {
		if pass > 20 {
			t.Fatalf("no human wait opened after %d reconciler passes\n%s", pass-1, runStateDump(t, s.env, s.runID))
		}
		s.pass(t, 3*time.Second)
		wait = s.openWait(t)
	}
	waitID, _ := wait["id"].(string)
	if wait["actionId"] != s.actionID || wait["targetKey"] != "token-service/staging" || wait["operationKey"] != "op_"+s.actionID+"_1" ||
		wait["reason"] != "NOT_CONFIRMED" || wait["toolName"] != deployrelease.ToolName || wait["confirmDeadline"] == nil || wait["deadline"] == nil {
		t.Errorf("open human wait = %v, want the held release of %s on token-service/staging, NOT_CONFIRMED", wait, s.actionID)
	}

	eventsBefore := len(s.env.listEvents(t, s.runID))
	for _, tc := range []struct{ name, token, method, path string }{
		{"list without token", "", http.MethodGet, "/api/human-waits"},
		{"list with wrong token", "wrong-" + testOperatorToken, http.MethodGet, "/api/human-waits"},
		{"stop without token", "", http.MethodPost, "/api/human-waits/" + waitID + "/stop"},
		{"stop with wrong token", "nearly-" + testOperatorToken, http.MethodPost, "/api/human-waits/" + waitID + "/stop"},
	} {
		resp, body := s.operator.doOperator(t, tc.token, tc.method, tc.path, nil)
		if resp.StatusCode != http.StatusUnauthorized || errorCode(t, body) != "INVALID_OPERATOR_CREDENTIAL" {
			t.Errorf("%s = %d %s, want 401 INVALID_OPERATOR_CREDENTIAL", tc.name, resp.StatusCode, body)
		}
		if strings.Contains(string(body), testOperatorToken) {
			t.Errorf("%s repeats the operator credential: %s", tc.name, body)
		}
	}
	if n := len(s.env.listEvents(t, s.runID)); n != eventsBefore {
		t.Errorf("refused stops wrote %d events", n-eventsBefore)
	}
	if s.openWait(t) == nil {
		t.Fatalf("refused stops closed the human wait")
	}
	if resp, body := s.operator.doOperator(t, testOperatorToken, http.MethodPost, "/api/human-waits/missing-wait/stop", nil); resp.StatusCode != http.StatusNotFound || errorCode(t, body) != "HUMAN_WAIT_NOT_FOUND" {
		t.Errorf("stop of a missing wait = %d %s, want 404 HUMAN_WAIT_NOT_FOUND", resp.StatusCode, body)
	}

	resp, body := s.operator.doOperator(t, testOperatorToken, http.MethodPost, "/api/human-waits/"+waitID+"/stop", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST stop status = %d, body=%s", resp.StatusCode, body)
	}
	stopped := decodeBody[map[string]any](t, body)
	if hw, _ := stopped["humanWait"].(map[string]any); hw["status"] != "STOPPED" || hw["closedBy"] != testOperatorName || stopped["repeated"] != false {
		t.Errorf("stop response = %v, want STOPPED by %s, not repeated", stopped, testOperatorName)
	}
	snapshot := s.env.waitForTerminal(t, s.runID, e2eWait)
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status after stop = %q, want FAILED", got)
	}
	events := s.env.listEvents(t, s.runID)
	assertContiguousSeq(t, events)
	var terminated bool
	for _, event := range eventsOfType(events, "AGENT_FAILED") {
		payload, _ := event["payload"].(map[string]any)
		execErr, _ := payload["error"].(map[string]any)
		if payload["termination"] == "HUMAN_STOPPED" && execErr["code"] == "RELEASE_UNKNOWN" {
			terminated = true
		}
	}
	if !terminated {
		t.Errorf("events lack an AGENT_FAILED as HUMAN_STOPPED with RELEASE_UNKNOWN: %v", eventTypeCounts(events))
	}
	_, view := s.releaseView(t)
	if occ := view.Occupation; occ == nil || occ.Status != "ACTIVE" || occ.NextQueryAt == nil {
		t.Errorf("occupation after stop = %+v, want ACTIVE with a query schedule", occ)
	}
	if hw := view.HumanWait; hw == nil || hw.Status != "STOPPED" || hw.ClosedBy == nil || *hw.ClosedBy != testOperatorName ||
		hw.Outcome == nil || *hw.Outcome != "RELEASE_UNKNOWN" {
		t.Errorf("human wait after stop = %+v, want STOPPED by %s with RELEASE_UNKNOWN", hw, testOperatorName)
	}

	resp, body = s.operator.doOperator(t, testOperatorToken, http.MethodPost, "/api/human-waits/"+waitID+"/stop", nil)
	if resp.StatusCode != http.StatusOK || decodeBody[map[string]any](t, body)["repeated"] != true {
		t.Errorf("repeated stop = %d %s, want 200 repeated", resp.StatusCode, body)
	}
	if s.openWait(t) != nil {
		t.Errorf("the stopped wait is still listed as OPEN")
	}
	eventsAtEnd := len(s.env.listEvents(t, s.runID))

	s.production.ResumeCompletion()
	for pass := 1; ; pass++ {
		if pass > 20 {
			t.Fatalf("occupation not released after %d reconciler passes", pass-1)
		}
		if report := s.pass(t, 10*time.Second); report.OccupationsReleased > 0 {
			break
		}
	}
	traceBody, view := s.releaseView(t)
	if occ := view.Occupation; occ == nil || occ.Status != "RELEASED" || occ.ReleaseReason == nil || *occ.ReleaseReason != "DEPLOYED" ||
		occ.NextQueryAt != nil || occ.DeployedUncheckedVersion == nil || *occ.DeployedUncheckedVersion == "" {
		t.Errorf("occupation after the late deploy = %+v, want RELEASED DEPLOYED with an unchecked version and no schedule", occ)
	}
	if n := len(s.env.listEvents(t, s.runID)); n != eventsAtEnd {
		t.Errorf("post-task reconciliation wrote %d Run events, want none", n-eventsAtEnd)
	}
	if got := runStatusOf(s.env.runSnapshot(t, s.runID)); got != "FAILED" {
		t.Errorf("Run status after the late deploy = %q, want FAILED unchanged", got)
	}

	if strings.Contains(string(traceBody), testOperatorToken) {
		t.Errorf("the trace repeats the operator credential")
	}
	if logs := s.logs.String(); strings.Contains(logs, testOperatorToken) {
		t.Errorf("log output contains the operator credential")
	}
}

// TestHumanWaits_TokenNotConfigured_RoutesAbsent proves a Backend without the operator
// credential does not serve the human wait routes at all.
func TestHumanWaits_TokenNotConfigured_RoutesAbsent(t *testing.T) {
	env := newApprovalEnv(t, "")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/human-waits"},
		{http.MethodPost, "/api/human-waits/any/stop"},
	} {
		resp, body := env.doOperator(t, "", tc.method, tc.path, nil)
		if resp.StatusCode != http.StatusNotFound || errorCode(t, body) != "NOT_FOUND" {
			t.Errorf("%s %s = %d %s, want 404 NOT_FOUND", tc.method, tc.path, resp.StatusCode, body)
		}
	}
}
