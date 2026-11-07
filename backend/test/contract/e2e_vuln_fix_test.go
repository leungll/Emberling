//go:build integration

// Vulnerability-fix stories over the public API: an Agent edits a bound workspace through
// the workspace Tools and verifies its patch through the sandbox runner's asynchronous
// verification. The runner runs in-process with its test controls and the deterministic
// mock verification backend, so a story can hold the verification callback at the runner's
// barrier and compare the Runtime's facts with what the runner recorded.
package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
	"github.com/leungll/Emberling/backend/internal/tools/workspace"
)

// vulnRepositoryID and vulnBaseCommit bind the fixture Definition to the generated example
// repository; the commit is the reproducible baseline the generator always produces.
const (
	vulnRepositoryID = "vuln-demo"
	vulnBaseCommit   = "2706ca6fb4ddbe969e9acfa5464a347ff90c7774"
	vulnAdvisory     = "GO-2025-3553"
)

// vulnFixture is the sandbox runner and the example repository one story runs against.
// Both outlive an Emberling restart.
type vulnFixture struct {
	callbacks *callbackTransport
	runner    *deliveryRunner
	repoPath  string
	assetRoot string
}

func newVulnFixture(t *testing.T) *vulnFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required to generate the example repository: %v", err)
	}
	f := &vulnFixture{
		callbacks: newCallbackTransport(),
		repoPath:  filepath.Join(t.TempDir(), vulnRepositoryID),
		assetRoot: t.TempDir(),
	}
	f.callbacks.release()
	t.Cleanup(func() { close(f.callbacks.done) })

	generate := exec.Command("sh", "../../internal/fixture/vulnrepo/generate.sh", f.repoPath)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate example repository: %v\n%s", err, out)
	}

	record, err := mockcontrol.OpenRecord(filepath.Join(t.TempDir(), "runner.jsonl"))
	if err != nil {
		t.Fatalf("open sandbox runner record: %v", err)
	}
	store, _, err := sandboxrunner.OpenRedeliveryStore(filepath.Join(t.TempDir(), "redelivery.jsonl"))
	if err != nil {
		t.Fatalf("open sandbox runner redelivery store: %v", err)
	}
	server := sandboxrunner.NewServer(sandboxrunner.NewMockBackend(),
		sandboxrunner.WithHTTPClient(&http.Client{Transport: f.callbacks, Timeout: e2eHTTPTimeout}),
		sandboxrunner.WithTestControls(record, store),
		sandboxrunner.WithVerification(sandboxrunner.NewMockVerificationBackend()))
	f.runner = &deliveryRunner{server: server, http: httptest.NewServer(server), record: record, store: store}
	t.Cleanup(func() { f.runner.stop(t) })
	return f
}

// startBackend wires one Emberling Backend against this fixture: the workspace Tools over
// the example repository and verify_patch against the runner, writing its log Artifacts to
// the fixture's Asset volume. pool == nil provisions a fresh database; a previous Backend's
// pool restarts over the same facts and the same volume.
func (f *vulnFixture) startBackend(t *testing.T, opts testEnvOptions) *testEnv {
	t.Helper()
	artifacts := asset.NewArtifactStore(f.assetRoot, defaultTestArtifactMaxBytes)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	opts.AssetStorageRoot = f.assetRoot
	opts.WorkspaceRepositories = []string{vulnRepositoryID}
	opts.ExtraToolRegistrations = append(append(
		workspace.Registrations(workspace.NewLocalGit(map[string]string{vulnRepositoryID: f.repoPath})),
		verifypatch.Registration(f.runner.http.URL, nil, artifacts),
		changerequest.Registration(artifacts),
	), opts.ExtraToolRegistrations...)
	env := newTestEnvWithOptions(t, opts)
	f.callbacks.setTarget(env.server.URL)
	return env
}

// saveVulnDefinition saves the vulnerability-fix fixture with its Agent bound to script.
func saveVulnDefinition(t *testing.T, env *testEnv, script string) (string, int) {
	t.Helper()
	return saveVulnDefinitionWith(t, env, script, nil)
}

// saveVulnDefinitionWith saves the fixture with its Agent bound to script, after adjust,
// when given, changed the Agent node's config: a story that needs more Tools or an
// approval timeout edits the fixture's config instead of carrying a second fixture file.
func saveVulnDefinitionWith(t *testing.T, env *testEnv, script string, adjust func(config map[string]any)) (string, int) {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/definitions/vuln_fix.json")
	if err != nil {
		t.Fatalf("read vuln_fix.json: %v", err)
	}
	var fx documentProcessingFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal vuln_fix.json: %v", err)
	}
	for i, node := range fx.Nodes {
		if node.ID != "node_agent" {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(node.Config, &config); err != nil {
			t.Fatalf("decode node_agent config: %v", err)
		}
		config["modelConfig"] = map[string]any{"script": script}
		if adjust != nil {
			adjust(config)
		}
		if fx.Nodes[i].Config, err = json.Marshal(config); err != nil {
			t.Fatalf("encode node_agent config: %v", err)
		}
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (vulnerability fix) status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	return workflowID, int(version)
}

// createVulnRun saves the fixture bound to script and starts a Run over the repair task,
// a JSON object the Task Input publishes unchanged.
func createVulnRun(t *testing.T, env *testEnv, script string) string {
	t.Helper()
	workflowID, version := saveVulnDefinition(t, env, script)
	task := map[string]any{"advisory": vulnAdvisory, "goal": "PREPARE_ONLY", "service": "token-service"}
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": map[string]any{"task": task},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (vulnerability fix) status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	return runID
}

// vulnTrace is the slice of the Agent Trace the stories assert on: the delivery slice plus
// the workspace with its patch records and the completion verdict.
type vulnTrace struct {
	deliveryTrace
	// AgentRun shadows the delivery slice's view to add the completion verdict.
	AgentRun struct {
		Termination *string         `json:"termination"`
		Completion  *vulnCompletion `json:"completion"`
	} `json:"agentRun"`
	ChangeRequests struct {
		Items []struct {
			ID                 string `json:"id"`
			PatchDigest        string `json:"patchDigest"`
			VerificationFactID string `json:"verificationFactId"`
		} `json:"items"`
	} `json:"changeRequests"`
	Workspace *struct {
		RepositoryID string `json:"repositoryId"`
		BaseCommit   string `json:"baseCommit"`
		Patches      []struct {
			Seq           int      `json:"seq"`
			PatchDigest   string   `json:"patchDigest"`
			TouchedPaths  []string `json:"touchedPaths"`
			ToolAttemptID string   `json:"toolAttemptId"`
		} `json:"patches"`
	} `json:"workspace"`
}

func loadVulnTrace(t *testing.T, env *testEnv, runID, agentNodeRunID string) vulnTrace {
	t.Helper()
	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, body)
	}
	return decodeBody[vulnTrace](t, body)
}

// latestPatchDigest is the digest of the workspace's most recent patch record.
func (tr vulnTrace) latestPatchDigest(t *testing.T) string {
	t.Helper()
	if tr.Workspace == nil || len(tr.Workspace.Patches) == 0 {
		t.Fatalf("trace workspace = %+v, want patch records", tr.Workspace)
	}
	return tr.Workspace.Patches[len(tr.Workspace.Patches)-1].PatchDigest
}

// vulnCompletion is the completion verdict as the Trace projects it.
type vulnCompletion struct {
	Outcome string `json:"outcome"`
	Checks  []struct {
		Name   string   `json:"name"`
		Passed bool     `json:"passed"`
		Refs   []string `json:"refs"`
	} `json:"checks"`
	Missing []struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	} `json:"missing"`
	EvaluatedAt string `json:"evaluatedAt"`
}

func (c vulnCompletion) missingCodes() []string {
	codes := make([]string, len(c.Missing))
	for i, item := range c.Missing {
		codes[i] = item.Code
	}
	return codes
}

func (c vulnCompletion) check(name string) (passed bool, refs []string, found bool) {
	for _, check := range c.Checks {
		if check.Name == name {
			return check.Passed, check.Refs, true
		}
	}
	return false, nil, false
}

func vulnNodeRunOf(t *testing.T, snapshot map[string]any, nodeID string) map[string]any {
	t.Helper()
	nodeRuns, _ := snapshot["nodeRuns"].([]any)
	for _, nr := range nodeRuns {
		m, _ := nr.(map[string]any)
		if id, _ := m["nodeId"].(string); id == nodeID {
			return m
		}
	}
	t.Fatalf("snapshot has no nodeRun for node %q: %v", nodeID, snapshot["nodeRuns"])
	return nil
}

// vulnResult is the Agent NodeRun's published result as the result node received it: the
// completion verdict with the Agent's own FINAL report inside.
type vulnResult struct {
	Outcome  string `json:"outcome"`
	Missing  []any  `json:"missing"`
	Checks   []any  `json:"checks"`
	Evidence []any  `json:"evidence"`
	// AgentReport is the Agent's FINAL answer as published: the fixture Agent answers in
	// text form, so it is a JSON string holding the report document.
	AgentReport json.RawMessage `json:"agentReport"`
}

type vulnAgentReport struct {
	Status      string `json:"status"`
	Advisory    string `json:"advisory"`
	PatchDigest string `json:"patchDigest"`
	Passed      bool   `json:"passed"`
}

func vulnResultOf(t *testing.T, snapshot map[string]any) (vulnResult, vulnAgentReport) {
	t.Helper()
	text, _ := nodeRunOutputOf(t, snapshot, "node_result")["text"].(string)
	var result vulnResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("repair result %q is not JSON: %v", text, err)
	}
	var reportText string
	if err := json.Unmarshal(result.AgentReport, &reportText); err != nil {
		t.Fatalf("repair result agentReport %s is not the Agent's text answer: %v", result.AgentReport, err)
	}
	var report vulnAgentReport
	if err := json.Unmarshal([]byte(reportText), &report); err != nil {
		t.Fatalf("agent report %q is not JSON: %v", reportText, err)
	}
	return result, report
}

// assertVulnReadyForApproval proves a PREPARE_ONLY story is complete: the Run COMPLETED,
// the Agent Run ended with FINAL_RESPONSE, the verdict is READY_FOR_APPROVAL with every
// check passed and nothing missing, exactly one change request names the latest patch and
// the result node received the verdict with the Agent's report, which names that patch too.
func assertVulnReadyForApproval(t *testing.T, snapshot map[string]any, trace vulnTrace) vulnCompletion {
	t.Helper()
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	if agentNode := vulnNodeRunOf(t, snapshot, "node_agent"); agentNode["status"] != "SUCCEEDED" {
		t.Fatalf("node_agent = %v, want SUCCEEDED", agentNode)
	}
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != "FINAL_RESPONSE" {
		t.Errorf("agentRun.termination = %v, want FINAL_RESPONSE", trace.AgentRun.Termination)
	}
	if trace.AgentRun.Completion == nil {
		t.Fatalf("agent trace carries no completion verdict")
	}
	completion := *trace.AgentRun.Completion
	if completion.Outcome != "READY_FOR_APPROVAL" || len(completion.Missing) != 0 || completion.EvaluatedAt == "" {
		t.Fatalf("completion = %+v, want READY_FOR_APPROVAL with nothing missing", completion)
	}
	for _, check := range completion.Checks {
		if !check.Passed {
			t.Errorf("completion check %s failed in a READY_FOR_APPROVAL verdict: %+v", check.Name, check)
		}
	}
	latest := trace.latestPatchDigest(t)
	if len(trace.ChangeRequests.Items) != 1 || trace.ChangeRequests.Items[0].PatchDigest != latest {
		t.Errorf("change requests = %+v, want exactly one about the latest patch %s", trace.ChangeRequests.Items, latest)
	}
	if passed, refs, found := completion.check("change_request_binds_latest_patch"); !found || !passed || len(refs) == 0 || refs[0] != trace.ChangeRequests.Items[0].ID {
		t.Errorf("completion check change_request_binds_latest_patch = passed %v refs %v found %v, want it passed citing the change request", passed, refs, found)
	}
	result, report := vulnResultOf(t, snapshot)
	if result.Outcome != "READY_FOR_APPROVAL" || len(result.Missing) != 0 || len(result.Checks) != len(completion.Checks) || len(result.Evidence) == 0 {
		t.Errorf("repair result = %+v, want the READY_FOR_APPROVAL verdict with its checks and evidence", result)
	}
	if report.Advisory != vulnAdvisory || report.PatchDigest != latest || !report.Passed {
		t.Errorf("repair result agentReport = %+v, want the Agent's report of the verified latest patch %s", report, latest)
	}
	return completion
}

// assertVulnIncomplete proves the goal-based completion check ended a PREPARE_ONLY story
// that declared completion without a change request: the Agent Run ended with
// FINAL_RESPONSE (the model answered), the verdict is INCOMPLETE naming the change request
// among wantMissing, the Agent NodeRun FAILED with TASK_INCOMPLETE, the Run FAILED and the
// result node never ran.
func assertVulnIncomplete(t *testing.T, snapshot map[string]any, trace vulnTrace, wantMissing ...string) vulnCompletion {
	t.Helper()
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED from the completion check; snapshot=%v", got, snapshot)
	}
	agentNode := vulnNodeRunOf(t, snapshot, "node_agent")
	nodeError, _ := agentNode["error"].(map[string]any)
	if agentNode["status"] != "FAILED" || nodeError == nil || nodeError["code"] != "TASK_INCOMPLETE" {
		t.Fatalf("node_agent = status %v error %v, want FAILED with TASK_INCOMPLETE", agentNode["status"], agentNode["error"])
	}
	nodeRuns, _ := snapshot["nodeRuns"].([]any)
	for _, nr := range nodeRuns {
		m, _ := nr.(map[string]any)
		if id, _ := m["nodeId"].(string); id == "node_result" {
			t.Errorf("node_result = %v, want no NodeRun after the Agent NodeRun failed", m)
		}
	}
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != "FINAL_RESPONSE" {
		t.Errorf("agentRun.termination = %v, want FINAL_RESPONSE: the model did answer", trace.AgentRun.Termination)
	}
	if trace.AgentRun.Completion == nil {
		t.Fatalf("agent trace carries no completion verdict")
	}
	completion := *trace.AgentRun.Completion
	if completion.Outcome != "INCOMPLETE" || completion.EvaluatedAt == "" {
		t.Errorf("completion = %+v, want INCOMPLETE with an evaluation time", completion)
	}
	codes := completion.missingCodes()
	for _, want := range append([]string{"CHANGE_REQUEST_MISSING"}, wantMissing...) {
		found := false
		for _, code := range codes {
			found = found || code == want
		}
		if !found {
			t.Errorf("completion.missing = %v, want %s named", codes, want)
		}
	}
	for _, item := range completion.Missing {
		if item.Description == "" {
			t.Errorf("missing item %s carries no description", item.Code)
		}
	}
	message, _ := nodeError["message"].(string)
	if !strings.Contains(message, "CHANGE_REQUEST_MISSING") {
		t.Errorf("node_agent error message %q does not name the missing change request", message)
	}
	return completion
}

// verifiedFacts returns the patch_verified facts in Trace order.
func verifiedFacts(tr vulnTrace) []struct {
	Subject string
	Verdict *bool
	Attempt string
} {
	var out []struct {
		Subject string
		Verdict *bool
		Attempt string
	}
	for _, fact := range tr.Facts.Items {
		if fact.FactType == verifypatch.FactType {
			out = append(out, struct {
				Subject string
				Verdict *bool
				Attempt string
			}{fact.Subject, fact.Verdict, fact.ToolAttemptID})
		}
	}
	return out
}

// toolMessagesOf returns the decoded tool messages of one model request, in order.
func toolMessagesOf(t *testing.T, request registry.ModelRequest) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		var content map[string]any
		if err := json.Unmarshal(message.Content, &content); err != nil {
			t.Fatalf("tool message %s is not a JSON object: %v", message.Content, err)
		}
		out = append(out, content)
	}
	return out
}

// TestE2E_VulnFix_Verify_FailsOnceThenPassesWithFactBoundToLatestPatch plays the main
// story: the Agent upgrades go.mod and the import path, the verification of that
// unadapted upgrade fails as its build really does, the Agent adapts the calling code and
// the second verification passes, the change request names that patch and the task is
// READY_FOR_APPROVAL. The passing patch_verified fact is about exactly the latest digest
// and the runner verified twice.
func TestE2E_VulnFix_Verify_FailsOnceThenPassesWithFactBoundToLatestPatch(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := createVulnRun(t, env, "vuln-fix-main-verify")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	trace := loadVulnTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	completion := assertVulnReadyForApproval(t, snapshot, trace)

	verifications := trace.callsOf(verifypatch.ToolName)
	if len(verifications) != 2 {
		t.Fatalf("verify_patch Attempts = %+v, want exactly 2", verifications)
	}
	for i, call := range verifications {
		if call.attempt.Status != "SUCCEEDED" || call.actionStatus != "SUCCEEDED" || call.attempt.CallbackBinding == nil ||
			call.attempt.CallbackBinding.ProviderID != verifypatch.ProviderID {
			t.Errorf("verify_patch call %d = %+v, want a SUCCEEDED Attempt bound to the sandbox runner", i, call)
		}
	}

	latest := trace.latestPatchDigest(t)
	if trace.Workspace == nil || len(trace.Workspace.Patches) != 3 || trace.Workspace.Patches[1].PatchDigest == latest {
		t.Fatalf("workspace patches = %+v, want three patch records: go.mod, the unadapted import, the adaptation", trace.Workspace)
	}
	facts := verifiedFacts(trace)
	if len(facts) != 2 || facts[0].Verdict == nil || *facts[0].Verdict || facts[1].Verdict == nil || !*facts[1].Verdict {
		t.Fatalf("patch_verified facts = %+v, want one failed then one passed", facts)
	}
	if facts[0].Subject != trace.Workspace.Patches[1].PatchDigest || facts[1].Subject != latest ||
		facts[1].Attempt != verifications[1].attempt.ID {
		t.Errorf("fact subjects = %+v, want the unadapted upgrade then the latest patch %s from the second verification", facts, latest)
	}

	if passed, refs, found := completion.check("latest_patch_verified"); !found || !passed || len(refs) != 1 || refs[0] != trace.ChangeRequests.Items[0].VerificationFactID {
		t.Errorf("completion check latest_patch_verified = passed %v refs %v found %v, want it passed citing the passing fact the change request binds", passed, refs, found)
	}

	// The second Turn reads the advisory the task names; the Turn after it carries that
	// read in its Context Manifest under the real path, so the Agent read the advisory
	// rather than a literal placeholder path.
	advisoryRead := false
	for i, turn := range trace.Turns {
		for _, item := range turn.Manifest {
			if strings.Contains(item.Ref, "{{") {
				t.Errorf("turns[%d] manifest item %+v names an unresolved placeholder", i, item)
			}
			if i == 2 && item.Kind == "WORKSPACE_FILE" && item.Ref == "advisories/"+vulnAdvisory+".md" {
				advisoryRead = true
			}
		}
	}
	if !advisoryRead {
		t.Errorf("turns[2] manifest = %+v, want a WORKSPACE_FILE item for advisories/%s.md", trace.Turns[2].Manifest, vulnAdvisory)
	}

	lines := serviceRecord(t, f.runner.http.URL)
	if arrived := recordLines(lines, "arrived", "verification"); len(arrived) != 2 {
		t.Errorf("runner verification arrivals = %v, want 2", arrived)
	}
	if n := len(recordLines(lines, "arrived", "test")); n != 0 {
		t.Errorf("runner test arrivals = %d, want none: the story only verifies", n)
	}
	events := env.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := len(eventsOfType(events, "AGENT_DECISION_COMMITTED")); n != 10 {
		t.Errorf("AGENT_DECISION_COMMITTED events = %d, want 10: nine scripted Turns and the inserted adaptation", n)
	}
	if n := agentAsyncCallbackCompletions(events); n != 2 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 2", n)
	}
}

// TestE2E_VulnFix_ScopeDenied_ReturnedBeforeAnyAttempt plays a write outside the allowed
// directories: the Action fails with SCOPE_DENIED before any Attempt exists, nothing
// reaches the runner before it, the next model request carries the refusal as a tool
// message, and the Agent completes the task inside the allowed directories: it writes
// go.mod and the adapted code, verifies, records the change request and is
// READY_FOR_APPROVAL.
func TestE2E_VulnFix_ScopeDenied_ReturnedBeforeAnyAttempt(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := createVulnRun(t, env, "vuln-fix-scope-denied")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	trace := loadVulnTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	assertVulnReadyForApproval(t, snapshot, trace)
	if len(trace.Turns) != 8 {
		t.Fatalf("turns = %d, want 8: the refused write, the allowed write, two reads, the adaptation, the verification, the change request and the FINAL", len(trace.Turns))
	}
	refused := trace.Turns[0]
	if refused.Action == nil || refused.Action.Status != "FAILED" || refused.Action.Error == nil ||
		refused.Action.Error.Code != "SCOPE_DENIED" || refused.Action.FailureSource == nil || *refused.Action.FailureSource != "AUTHORIZATION" {
		t.Fatalf("refused Action = %+v, want FAILED with SCOPE_DENIED from AUTHORIZATION", refused.Action)
	}
	if len(refused.ToolAttempts) != 0 {
		t.Errorf("refused Turn Attempts = %+v, want none: the refusal came before any Attempt", refused.ToolAttempts)
	}
	if trace.Workspace == nil || len(trace.Workspace.Patches) != 2 || trace.Workspace.Patches[0].TouchedPaths[0] != "go.mod" {
		t.Errorf("workspace patches = %+v, want the allowed go.mod write and the adaptation, nothing under tests/", trace.Workspace)
	}
	for _, patch := range trace.Workspace.Patches {
		for _, path := range patch.TouchedPaths {
			if path != "go.mod" && !strings.HasPrefix(path, "internal/auth/") {
				t.Errorf("patch %d touched %s, outside the allowed directories", patch.Seq, path)
			}
		}
	}

	requests := env.provider.Requests()
	if len(requests) != 8 {
		t.Fatalf("model requests = %d, want 8", len(requests))
	}
	messages := toolMessagesOf(t, requests[1])
	if len(messages) != 1 || messages[0]["status"] != "REJECTED" || messages[0]["code"] != "SCOPE_DENIED" {
		t.Errorf("second request tool messages = %v, want the SCOPE_DENIED refusal", messages)
	}
	// The refusal cost no external call: the runner saw only the one verification of the
	// patch inside the allowed directories, made after it.
	lines := serviceRecord(t, f.runner.http.URL)
	if arrived := recordLines(lines, "arrived", "verification"); len(arrived) != 1 || arrived[0]["patchDigest"] != trace.latestPatchDigest(t) {
		t.Errorf("runner verification arrivals = %v, want exactly the latest patch", arrived)
	}
	events := env.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := len(eventsOfType(events, "AGENT_ACTION_FAILED")); n != 1 {
		t.Errorf("AGENT_ACTION_FAILED events = %d, want 1", n)
	}
}

// TestE2E_VulnFix_StaleEvidence_OldDigestRejectedAndRunnerAskedTwice plays a verification
// naming a digest the workspace has moved past: it is refused with STALE_PATCH before any
// Attempt, the Agent verifies the latest digest, the runner saw exactly two verifications
// and the change request names the latest patch.
func TestE2E_VulnFix_StaleEvidence_OldDigestRejectedAndRunnerAskedTwice(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := createVulnRun(t, env, "vuln-fix-stale-evidence")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	trace := loadVulnTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	completion := assertVulnReadyForApproval(t, snapshot, trace)
	if trace.Workspace == nil || len(trace.Workspace.Patches) != 2 {
		t.Fatalf("workspace patches = %+v, want two", trace.Workspace)
	}
	first, latest := trace.Workspace.Patches[0].PatchDigest, trace.latestPatchDigest(t)

	var stale []int
	for _, turn := range trace.Turns {
		if turn.Action != nil && turn.Action.Status == "FAILED" {
			if turn.Action.Error == nil || turn.Action.Error.Code != "STALE_PATCH" || len(turn.ToolAttempts) != 0 {
				t.Fatalf("failed Action %+v attempts %+v, want STALE_PATCH with no Attempt", turn.Action, turn.ToolAttempts)
			}
			stale = append(stale, turn.TurnNo)
		}
	}
	if len(stale) != 1 {
		t.Fatalf("STALE_PATCH refusals in turns %v, want exactly one", stale)
	}
	verifications := trace.callsOf(verifypatch.ToolName)
	if len(verifications) != 2 || verifications[0].attempt.Status != "SUCCEEDED" || verifications[1].attempt.Status != "SUCCEEDED" {
		t.Fatalf("verify_patch Attempts = %+v, want two SUCCEEDED", verifications)
	}
	facts := verifiedFacts(trace)
	if len(facts) != 2 || facts[0].Subject != first || facts[1].Subject != latest || facts[1].Verdict == nil || !*facts[1].Verdict {
		t.Errorf("patch_verified facts = %+v, want %s then a passing %s", facts, first, latest)
	}
	if passed, _, found := completion.check("latest_patch_verified"); !found || !passed {
		t.Errorf("completion check latest_patch_verified = passed %v found %v, want it passed for %s", passed, found, latest)
	}

	lines := serviceRecord(t, f.runner.http.URL)
	arrived := recordLines(lines, "arrived", "verification")
	if len(arrived) != 2 || arrived[0]["patchDigest"] != first || arrived[1]["patchDigest"] != latest {
		t.Errorf("runner verification arrivals = %v, want exactly %s then %s", arrived, first, latest)
	}
}

// TestE2E_VulnFix_RestartDuringVerify_NoModelReaskNoRerun holds the runner's verification
// callback while the Agent waits for it, stops the Backend and starts another over the same
// database and Asset volume. The released callback resumes the original Attempt: the model
// is not asked again for any committed Decision, the runner runs no verification twice,
// and the story ends exactly as it does without the restart.
func TestE2E_VulnFix_RestartDuringVerify_NoModelReaskNoRerun(t *testing.T) {
	f := newVulnFixture(t)
	control(t, f.runner.http.URL, "pause", `{"kinds":["callback"]}`)
	first := f.startBackend(t, testEnvOptions{})

	runID := createVulnRun(t, first, "vuln-fix-main-verify")
	waiting := first.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", e2eWait)
	agentNodeRunID, _ := waiting["id"].(string)
	waitBarrierHeld(t, f.runner.http.URL, "callback")

	before := loadVulnTrace(t, first, runID, agentNodeRunID)
	held := before.callsOf(verifypatch.ToolName)
	if len(held) != 1 || held[0].attempt.Status != "DISPATCHED" || held[0].actionStatus != "WAITING_CALLBACK" || held[0].attempt.CallbackBinding == nil {
		t.Fatalf("verify_patch call while waiting = %+v, want one bound DISPATCHED Attempt and its Action WAITING_CALLBACK", held)
	}
	heldAttemptID := held[0].attempt.ID
	verificationID := held[0].attempt.CallbackBinding.ExternalTaskID
	modelCallsBefore := len(first.provider.Requests())
	if modelCallsBefore != 6 {
		t.Fatalf("model calls before the restart = %d, want 6: three reads, two writes and the verification", modelCallsBefore)
	}
	arrivalsBefore := len(recordLines(serviceRecord(t, f.runner.http.URL), "arrived", "verification"))

	// --- restart: same database and volume, new Backend; the runner's callback is still held.
	pool := first.pool
	first.stop()
	second := f.startBackend(t, testEnvOptions{Pool: pool})
	control(t, f.runner.http.URL, "release", "")

	snapshot := second.waitForTerminal(t, runID, e2eWait)
	trace := loadVulnTrace(t, second, runID, agentNodeRunID)
	completion := assertVulnReadyForApproval(t, snapshot, trace)
	verifications := trace.callsOf(verifypatch.ToolName)
	if len(verifications) != 2 || verifications[0].attempt.ID != heldAttemptID || verifications[0].attempt.Status != "SUCCEEDED" {
		t.Fatalf("verify_patch Attempts after the restart = %+v, want the held Attempt %s SUCCEEDED and one more", verifications, heldAttemptID)
	}
	if passed, _, found := completion.check("latest_patch_verified"); !found || !passed {
		t.Errorf("completion check latest_patch_verified = passed %v found %v, want it passed for %s", passed, found, trace.latestPatchDigest(t))
	}

	// The second Backend asked the model only for the Turns that follow the held
	// verification; the first request it made already carried the six results.
	requests := second.provider.Requests()
	if len(requests) != 4 {
		t.Fatalf("model calls after the restart = %d, want 4: the adaptation, the second verification, the change request and the FINAL", len(requests))
	}
	if messages := toolMessagesOf(t, requests[0]); len(messages) != 6 || messages[5]["passed"] != false {
		t.Errorf("first request after the restart carries %v, want the six results ending in the failed verification", messages)
	}
	events := second.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := len(eventsOfType(events, "AGENT_DECISION_COMMITTED")); n != modelCallsBefore+len(requests) {
		t.Errorf("AGENT_DECISION_COMMITTED events = %d, want %d: one per model call across both Backends", n, modelCallsBefore+len(requests))
	}

	lines := serviceRecord(t, f.runner.http.URL)
	var heldStarted int
	for _, line := range recordLines(lines, "started", "verification") {
		if line["verificationId"] == verificationID {
			heldStarted++
		}
	}
	if heldStarted != 1 {
		t.Errorf("runner started the held verification %d times, want once", heldStarted)
	}
	if arrived := recordLines(lines, "arrived", "verification"); len(arrived) != arrivalsBefore+1 || arrivalsBefore != 1 {
		t.Errorf("runner verification arrivals = %d after %d, want exactly one more for the second verification", len(arrived), arrivalsBefore)
	}
	if n := agentAsyncCallbackCompletions(events); n != 2 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 2", n)
	}
}

// verifiedWithoutChangeRequestScript follows the main story's scripted Decisions until a
// verification passes, then answers FINAL at once: an Agent that declares the repair done
// without recording the change request its goal requires.
func verifiedWithoutChangeRequestScript() func(registry.ModelRequest) *mockmodel.Scenario {
	return func(request registry.ModelRequest) *mockmodel.Scenario {
		for _, message := range request.Messages {
			if message.Role != "tool" || message.ToolName == nil || *message.ToolName != verifypatch.ToolName {
				continue
			}
			var content map[string]any
			if json.Unmarshal(message.Content, &content) != nil {
				continue
			}
			if passed, _ := content["passed"].(bool); passed {
				digest, _ := content["patchDigest"].(string)
				return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: `{"status":"PATCH_VERIFIED","advisory":"` + vulnAdvisory + `","patchDigest":"` + digest + `","resultDigest":"","passed":true}`}
			}
		}
		return nil
	}
}

// TestE2E_VulnFix_FinalWithoutChangeRequest_TaskIncomplete is the failure path of the
// completion check over the public API: the Agent verified the latest patch but answered
// FINAL without a change request, so the task is INCOMPLETE, the Agent NodeRun FAILED with
// TASK_INCOMPLETE and the Run FAILED instead of reporting a repair nobody can approve.
func TestE2E_VulnFix_FinalWithoutChangeRequest_TaskIncomplete(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})
	env.provider.Script = verifiedWithoutChangeRequestScript()

	runID := createVulnRun(t, env, "vuln-fix-main-verify")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	trace := loadVulnTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	completion := assertVulnIncomplete(t, snapshot, trace)
	if len(trace.ChangeRequests.Items) != 0 {
		t.Errorf("change requests = %+v, want none", trace.ChangeRequests.Items)
	}
	if passed, refs, found := completion.check("latest_patch_verified"); !found || !passed || len(refs) != 1 {
		t.Errorf("completion check latest_patch_verified = passed %v refs %v found %v, want it passed: the verification itself was fine", passed, refs, found)
	}
	if passed, _, found := completion.check("change_request_binds_latest_patch"); !found || passed {
		t.Errorf("completion check change_request_binds_latest_patch = passed %v found %v, want it failed", passed, found)
	}
	if codes := completion.missingCodes(); len(codes) != 1 || codes[0] != "CHANGE_REQUEST_MISSING" {
		t.Errorf("completion.missing = %v, want exactly CHANGE_REQUEST_MISSING", codes)
	}
}
