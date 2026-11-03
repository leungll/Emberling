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

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
	"github.com/leungll/Emberling/backend/internal/registry"
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
// the workspace with its patch records.
type vulnTrace struct {
	deliveryTrace
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

// vulnReport is the FINAL document the Agent hands to the result Node.
type vulnReport struct {
	Status       string `json:"status"`
	Advisory     string `json:"advisory"`
	PatchDigest  string `json:"patchDigest"`
	ResultDigest string `json:"resultDigest"`
	Passed       bool   `json:"passed"`
}

func vulnReportOf(t *testing.T, snapshot map[string]any) vulnReport {
	t.Helper()
	text, _ := nodeRunOutputOf(t, snapshot, "node_result")["text"].(string)
	var report vulnReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatalf("repair report %q is not JSON: %v", text, err)
	}
	return report
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
// the second verification passes. The Run completes with a report naming the latest
// patch, the passing patch_verified fact is about exactly that digest, and the runner
// verified twice.
func TestE2E_VulnFix_Verify_FailsOnceThenPassesWithFactBoundToLatestPatch(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := createVulnRun(t, env, "vuln-fix-main-verify")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	trace := loadVulnTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))

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

	report := vulnReportOf(t, snapshot)
	if report.Status != "PATCH_VERIFIED" || report.Advisory != vulnAdvisory || report.PatchDigest != latest || !report.Passed {
		t.Errorf("repair report = %+v, want PATCH_VERIFIED for %s on patch %s", report, vulnAdvisory, latest)
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
	if n := len(eventsOfType(events, "AGENT_DECISION_COMMITTED")); n != 9 {
		t.Errorf("AGENT_DECISION_COMMITTED events = %d, want 9: eight scripted Turns and the inserted adaptation", n)
	}
	if n := agentAsyncCallbackCompletions(events); n != 2 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 2", n)
	}
}

// TestE2E_VulnFix_ScopeDenied_ReturnedBeforeAnyAttempt plays a write outside the allowed
// directories: the Action fails with SCOPE_DENIED before any Attempt exists, nothing
// reaches the runner, the next model request carries the refusal as a tool message, and
// the Agent completes with the write inside the allowed directory.
func TestE2E_VulnFix_ScopeDenied_ReturnedBeforeAnyAttempt(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := createVulnRun(t, env, "vuln-fix-scope-denied")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	trace := loadVulnTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if len(trace.Turns) != 3 {
		t.Fatalf("turns = %d, want 3: the refused write, the allowed write and the FINAL", len(trace.Turns))
	}
	refused := trace.Turns[0]
	if refused.Action == nil || refused.Action.Status != "FAILED" || refused.Action.Error == nil ||
		refused.Action.Error.Code != "SCOPE_DENIED" || refused.Action.FailureSource == nil || *refused.Action.FailureSource != "AUTHORIZATION" {
		t.Fatalf("refused Action = %+v, want FAILED with SCOPE_DENIED from AUTHORIZATION", refused.Action)
	}
	if len(refused.ToolAttempts) != 0 {
		t.Errorf("refused Turn Attempts = %+v, want none: the refusal came before any Attempt", refused.ToolAttempts)
	}
	if trace.Workspace == nil || len(trace.Workspace.Patches) != 1 || trace.Workspace.Patches[0].TouchedPaths[0] != "go.mod" {
		t.Errorf("workspace patches = %+v, want exactly the allowed go.mod write", trace.Workspace)
	}
	if report := vulnReportOf(t, snapshot); report.Status != "SCOPE_RESPECTED" || report.PatchDigest != trace.latestPatchDigest(t) {
		t.Errorf("repair report = %+v, want SCOPE_RESPECTED naming the allowed write's patch", report)
	}

	requests := env.provider.Requests()
	if len(requests) != 3 {
		t.Fatalf("model requests = %d, want 3", len(requests))
	}
	messages := toolMessagesOf(t, requests[1])
	if len(messages) != 1 || messages[0]["status"] != "REJECTED" || messages[0]["code"] != "SCOPE_DENIED" {
		t.Errorf("second request tool messages = %v, want the SCOPE_DENIED refusal", messages)
	}
	if lines := serviceRecord(t, f.runner.http.URL); len(lines) != 0 {
		t.Errorf("runner record = %v, want empty: a refused write makes no external call", lines)
	}
	events := env.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := len(eventsOfType(events, "AGENT_ACTION_FAILED")); n != 1 {
		t.Errorf("AGENT_ACTION_FAILED events = %d, want 1", n)
	}
}

// TestE2E_VulnFix_StaleEvidence_OldDigestRejectedAndRunnerAskedTwice plays a verification
// naming a digest the workspace has moved past: it is refused with STALE_PATCH before any
// Attempt, the Agent verifies the latest digest, and the runner saw exactly two
// verifications.
func TestE2E_VulnFix_StaleEvidence_OldDigestRejectedAndRunnerAskedTwice(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := createVulnRun(t, env, "vuln-fix-stale-evidence")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	trace := loadVulnTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
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
	if report := vulnReportOf(t, snapshot); report.PatchDigest != latest || !report.Passed {
		t.Errorf("repair report = %+v, want the latest patch %s verified", report, latest)
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
// and the story completes as it does without the restart.
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
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	trace := loadVulnTrace(t, second, runID, agentNodeRunID)
	verifications := trace.callsOf(verifypatch.ToolName)
	if len(verifications) != 2 || verifications[0].attempt.ID != heldAttemptID || verifications[0].attempt.Status != "SUCCEEDED" {
		t.Fatalf("verify_patch Attempts after the restart = %+v, want the held Attempt %s SUCCEEDED and one more", verifications, heldAttemptID)
	}
	if report := vulnReportOf(t, snapshot); report.PatchDigest != trace.latestPatchDigest(t) || !report.Passed {
		t.Errorf("repair report = %+v, want the latest patch verified", report)
	}

	// The second Backend asked the model only for the Turns that follow the held
	// verification; the first request it made already carried the six results.
	requests := second.provider.Requests()
	if len(requests) != 3 {
		t.Fatalf("model calls after the restart = %d, want 3: the adaptation, the second verification and the FINAL", len(requests))
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
