//go:build integration

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

// releaseToolName is a synchronous test Tool of PRODUCTION risk whose only argument names
// a change request, the shape the release Tool takes: a call to it under FIX_AND_DEPLOY
// waits for a person, and the approval detail shows the named change request and its patch.
const releaseToolName = "test_release_tool"

// vulnBasis is the decision basis the scripted Agent cites: the rules file read in Turn 1
// enters Turn 2's Context Manifest as a workspace file, go.mod read in Turn 3 enters Turn
// 4's.
var vulnBasis = []map[string]any{
	{"statement": "RULES.md restricts the fix to go.mod and internal/auth.", "turnNo": 2, "manifestRef": "RULES.md"},
	{"statement": "go.mod pins the vulnerable release of the dependency.", "turnNo": 4, "manifestRef": "go.mod"},
}

const (
	crTestSummary  = "Upgrade github.com/golang-jwt/jwt to the fixed release and adapt the token verifier."
	crTestImpact   = "The token service verifies tokens with the patched library; the public API does not change."
	crTestRollback = "Redeploy the previous image; tokens issued meanwhile stay verifiable."
)

func releaseToolRegistration(executor *countingToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          releaseToolName,
			Description:   "A test tool that releases a change request to production",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"changeRequestId":{"type":"string","minLength":1}},"required":["changeRequestId"],"additionalProperties":false}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionSync,
			Authorization: &domain.ToolAuthorization{Risk: domain.ToolRiskProduction},
		},
		Executor: executor,
	}
}

// approvalDetailBody is the approval detail as this test reads it.
type approvalDetailBody struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
	RunID      string          `json:"runId"`
	AgentRunID string          `json:"agentRunId"`

	ChangeRequest *struct {
		ID                 string `json:"id"`
		PatchDigest        string `json:"patchDigest"`
		VerificationFactID string `json:"verificationFactId"`
		ContentArtifactID  string `json:"contentArtifactId"`
		ContentDigest      string `json:"contentDigest"`
		ToolAttemptID      string `json:"toolAttemptId"`
		Content            string `json:"content"`
		DecisionBasis      []struct {
			Statement   string `json:"statement"`
			TurnNo      int    `json:"turnNo"`
			TurnID      string `json:"turnId"`
			ManifestRef string `json:"manifestRef"`
			Item        *struct {
				Kind    string `json:"kind"`
				Label   string `json:"label"`
				Ref     string `json:"ref"`
				Version string `json:"version"`
			} `json:"item"`
		} `json:"decisionBasis"`
	} `json:"changeRequest"`
	Patch *struct {
		Seq           int      `json:"seq"`
		PatchDigest   string   `json:"patchDigest"`
		TouchedPaths  []string `json:"touchedPaths"`
		ToolAttemptID string   `json:"toolAttemptId"`
		Patch         string   `json:"patch"`
	} `json:"patch"`
}

// changeRequestScript continues the fixture's verification trajectory once the latest
// verification passed: it records the change request about the verified patch, asks the
// release Tool to ship it and then answers FINAL. Until the verification passes the
// scripted trajectory decides, so the Agent edits and verifies exactly as in the main story.
func changeRequestScript() func(registry.ModelRequest) *mockmodel.Scenario {
	return func(request registry.ModelRequest) *mockmodel.Scenario {
		var verified, changeRequestID string
		released := false
		for _, message := range request.Messages {
			if message.Role != "tool" || message.ToolName == nil {
				continue
			}
			var content map[string]any
			if json.Unmarshal(message.Content, &content) != nil {
				continue
			}
			switch *message.ToolName {
			case verifypatch.ToolName:
				if passed, _ := content["passed"].(bool); passed {
					verified, _ = content["patchDigest"].(string)
				}
			case changerequest.ToolName:
				changeRequestID, _ = content["changeRequestId"].(string)
			case releaseToolName:
				released = true
			}
		}
		switch {
		case verified == "":
			return nil
		case changeRequestID == "":
			arguments, _ := json.Marshal(map[string]any{
				"patchDigest": verified, "summary": crTestSummary, "impact": crTestImpact, "rollbackPlan": crTestRollback,
				"decisionBasis": vulnBasis,
			})
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: changerequest.ToolName, ToolArguments: arguments}
		case !released:
			arguments, _ := json.Marshal(map[string]any{"changeRequestId": changeRequestID})
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: releaseToolName, ToolArguments: arguments}
		default:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: `{"status":"RELEASED","advisory":"` + vulnAdvisory + `","patchDigest":"` + verified + `","resultDigest":"","passed":true}`}
		}
	}
}

// waitForPendingApproval polls the approval list until runID has a PENDING approval.
func waitForPendingApproval(t *testing.T, env *testEnv, runID string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, body := doOperatorRequest(t, env, testOperatorToken, http.MethodGet, "/api/approvals?status=PENDING")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/approvals status = %d, body=%s", resp.StatusCode, body)
		}
		items, _ := decodeBody[map[string]any](t, body)["items"].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["runId"] == runID {
				return item
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, snapshot := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
	t.Fatalf("run %s reached no PENDING approval within %s; snapshot=%s", runID, timeout, snapshot)
	return nil
}

// doOperatorRequest sends a bodiless request with the operator credential header; token ==
// "" sends no header.
func doOperatorRequest(t *testing.T, env *testEnv, token, method, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, env.server.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("X-Emberling-Operator-Token", token)
	}
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		out.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	return resp, []byte(out.String())
}

// TestApprovals_Detail_ShowsChangeRequestAndPatchOfTheNamedRequest plays the release
// story up to the person: the Agent repairs and verifies the patch, records a change
// request about it and asks the release Tool to ship it. The approval detail then carries
// the change request the call names, with its Markdown, its decision basis resolved to the
// Manifest items it cites, and the full text of the workspace patch the request is about;
// the list keeps its shape; without the operator credential nothing is served. Approving
// runs the release Tool once with the same change request id.
func TestApprovals_Detail_ShowsChangeRequestAndPatchOfTheNamedRequest(t *testing.T) {
	f := newVulnFixture(t)
	executor := &countingToolExecutor{}
	env := f.startBackend(t, testEnvOptions{
		OperatorToken:          testOperatorToken,
		ExtraToolRegistrations: []registry.ToolRegistration{releaseToolRegistration(executor)},
	})
	env.provider.Script = changeRequestScript()

	workflowID, version := saveVulnDefinitionWith(t, env, "vuln-fix-main-verify", func(config map[string]any) {
		config["allowedTools"] = []string{"read_file", "list_files", "write_file", verifypatch.ToolName, changerequest.ToolName, releaseToolName}
		config["maxTurns"] = 16
		config["approvalTimeoutMs"] = 3600000
	})
	task := map[string]any{"advisory": vulnAdvisory, "goal": "FIX_AND_DEPLOY", "service": "token-service"}
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": map[string]any{"task": task},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)

	pending := waitForPendingApproval(t, env, runID, e2eWait)
	approvalID, _ := pending["id"].(string)
	if pending["toolName"] != releaseToolName {
		t.Fatalf("pending approval = %v, want a %s call", pending, releaseToolName)
	}
	if _, listed := pending["changeRequest"]; listed {
		t.Errorf("the approval list item carries changeRequest; the list keeps the approval view shape")
	}

	for _, token := range []string{"", "not-the-operator-token-0123456789abcdefghijklmn"} {
		resp, body := doOperatorRequest(t, env, token, http.MethodGet, "/api/approvals/"+approvalID)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET detail with credential %q status = %d, want 401; body=%s", token, resp.StatusCode, body)
		}
		if strings.Contains(string(body), "Change request") || strings.Contains(string(body), "diff --git") {
			t.Errorf("an unauthenticated detail read served content: %s", body)
		}
	}

	resp, body = doOperatorRequest(t, env, testOperatorToken, http.MethodGet, "/api/approvals/"+approvalID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/approvals/%s status = %d, body=%s", approvalID, resp.StatusCode, body)
	}
	detail := decodeBody[approvalDetailBody](t, body)
	var arguments struct {
		ChangeRequestID string `json:"changeRequestId"`
	}
	if err := json.Unmarshal(detail.Arguments, &arguments); err != nil || arguments.ChangeRequestID == "" {
		t.Fatalf("approval arguments = %s, want a changeRequestId", detail.Arguments)
	}
	if detail.ChangeRequest == nil {
		t.Fatalf("approval detail carries no changeRequest: %s", body)
	}
	cr := detail.ChangeRequest
	if cr.ID != arguments.ChangeRequestID {
		t.Errorf("changeRequest.id = %s, want the id the call names, %s", cr.ID, arguments.ChangeRequestID)
	}
	if !strings.HasPrefix(cr.Content, "# Change request\n") || !strings.Contains(cr.Content, cr.PatchDigest) ||
		!strings.Contains(cr.Content, crTestSummary) || !strings.Contains(cr.Content, cr.VerificationFactID) {
		t.Errorf("changeRequest.content does not read as the rendered request about %s:\n%s", cr.PatchDigest, cr.Content)
	}
	sum := sha256.Sum256([]byte(cr.Content))
	if hex.EncodeToString(sum[:]) != cr.ContentDigest {
		t.Errorf("content digest = %s, want the SHA-256 of the served content %x", cr.ContentDigest, sum)
	}
	if len(cr.DecisionBasis) != len(vulnBasis) {
		t.Fatalf("decisionBasis = %+v, want %d items", cr.DecisionBasis, len(vulnBasis))
	}
	for i, item := range cr.DecisionBasis {
		want := vulnBasis[i]
		if item.Statement != want["statement"] || item.TurnNo != want["turnNo"] || item.ManifestRef != want["manifestRef"] || item.TurnID == "" {
			t.Errorf("decisionBasis[%d] = %+v, want %v with the Turn's id", i, item, want)
		}
		if item.Item == nil || item.Item.Kind != "WORKSPACE_FILE" || item.Item.Ref != want["manifestRef"] || item.Item.Label != want["manifestRef"] {
			t.Errorf("decisionBasis[%d].item = %+v, want the WORKSPACE_FILE Manifest item %s", i, item.Item, want["manifestRef"])
		}
	}

	if detail.Patch == nil {
		t.Fatalf("approval detail carries no patch: %s", body)
	}
	if detail.Patch.PatchDigest != cr.PatchDigest || domain.WorkspacePatchDigest(detail.Patch.Patch) != detail.Patch.PatchDigest {
		t.Errorf("patch digest = %s over text of digest %s, want the change request's patch %s",
			detail.Patch.PatchDigest, domain.WorkspacePatchDigest(detail.Patch.Patch), cr.PatchDigest)
	}
	if !strings.Contains(detail.Patch.Patch, "go.mod") || !strings.Contains(detail.Patch.Patch, "internal/auth/token.go") {
		t.Errorf("patch text does not read as the cumulative diff over go.mod and internal/auth:\n%s", detail.Patch.Patch)
	}
	agentNodeRunID, _ := env.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", e2eWait)["id"].(string)
	trace := loadVulnTrace(t, env, runID, agentNodeRunID)
	latest := trace.Workspace.Patches[len(trace.Workspace.Patches)-1]
	if detail.Patch.Seq != latest.Seq || detail.Patch.ToolAttemptID != latest.ToolAttemptID || len(detail.Patch.TouchedPaths) != len(latest.TouchedPaths) {
		t.Errorf("patch = seq %d attempt %s paths %v, want the workspace's latest record seq %d attempt %s paths %v",
			detail.Patch.Seq, detail.Patch.ToolAttemptID, detail.Patch.TouchedPaths, latest.Seq, latest.ToolAttemptID, latest.TouchedPaths)
	}

	resp, body = env.doJSON(t, http.MethodPost, "/api/approvals/"+approvalID+"/decision", map[string]any{"decision": "APPROVE"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST decision without credential status = %d, want 401; body=%s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/api/approvals/"+approvalID+"/decision", strings.NewReader(`{"decision":"APPROVE"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emberling-Operator-Token", testOperatorToken)
	decided, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST decision: %v", err)
	}
	_ = decided.Body.Close()
	if decided.StatusCode != http.StatusOK {
		t.Fatalf("POST decision status = %d, want 200", decided.StatusCode)
	}
	final := env.waitForTerminal(t, runID, e2eWait)
	if executor.calls.Load() != 1 {
		t.Errorf("release tool calls = %d, want exactly 1 after the approval", executor.calls.Load())
	}
	// The test release Tool records no release confirmation, so the FIX_AND_DEPLOY task
	// is approved but never confirmed deployed: the completion check ends it INCOMPLETE
	// naming the confirmation, and the Run fails rather than reporting a release nobody
	// confirmed.
	if got := runStatusOf(final); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED from the completion check; snapshot=%v", got, final)
	}
	trace = loadVulnTrace(t, env, runID, agentNodeRunID)
	if trace.AgentRun.Completion == nil || trace.AgentRun.Completion.Outcome != "INCOMPLETE" {
		t.Fatalf("completion = %+v, want INCOMPLETE", trace.AgentRun.Completion)
	}
	if passed, refs, found := trace.AgentRun.Completion.check("release_approved"); !found || !passed || len(refs) != 1 || refs[0] != approvalID {
		t.Errorf("completion check release_approved = passed %v refs %v found %v, want it passed citing approval %s", passed, refs, found, approvalID)
	}
	codes := trace.AgentRun.Completion.missingCodes()
	if len(codes) != 1 || codes[0] != "RELEASE_CONFIRMATION_MISSING" {
		t.Errorf("completion.missing = %v, want exactly RELEASE_CONFIRMATION_MISSING", codes)
	}
}
