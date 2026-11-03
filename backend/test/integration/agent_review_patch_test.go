//go:build integration

// The review_patch Tool end to end against PostgreSQL, driven by a scripted review model:
// saving a Definition rejects a missing or non-independent review model; a stale digest is
// handed back as STALE_PATCH before any Attempt exists and before the model is asked; an
// opinion the Tool cannot read and a failing Provider both fail the Attempt without a fact;
// a blocking and a clean opinion record a patch_reviewed fact whose verdict says whether no
// blocking problem was found, bound to the opinion Artifact and never carrying its text;
// and a change request that cites a review attaches it, while a citation of another
// patch's review is refused in the claim and a tampered record in the result transaction.
//
// They share the changeRequestFixture of agent_change_request_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/reviewpatch"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
	"github.com/leungll/Emberling/backend/internal/tools/workspace"
)

const (
	reviewTextModelID = "text-reviewer-1.0"
	reviewDoubleTool  = "fake_review_change_request"

	reviewBlockingOpinion = `{"blocking":true,"findings":[{"severity":"blocking","path":"internal/auth/token.go","message":"The caller still type-asserts a ValidationError."}],"summary":"The upgrade leaves one caller unadapted."}`
	reviewCleanOpinion    = `{"blocking":false,"findings":[],"summary":"The patch is complete and follows the project rules."}`
)

// scriptedReviewModel is a text model with structured decision output whose answers a test
// scripts. It serves as its own Model Resolver for the review Tool, and records every request
// so a test can prove the model was, or was not, asked.
type scriptedReviewModel struct {
	mu       sync.Mutex
	respond  func(request registry.ModelRequest) (registry.ModelResponse, error)
	requests []registry.ModelRequest
}

func newScriptedReviewModel(output string) *scriptedReviewModel {
	m := &scriptedReviewModel{}
	m.answer(output)
	return m
}

func (m *scriptedReviewModel) answer(output string) {
	m.respond = func(registry.ModelRequest) (registry.ModelResponse, error) {
		return registry.ModelResponse{
			Decision: registry.ModelDecision{Kind: registry.DecisionFinal, Output: json.RawMessage(output)},
			Cost:     &domain.Cost{Amount: "0.001", Currency: domain.CurrencyUSD, Source: "upstream"},
		}, nil
	}
}

func (m *scriptedReviewModel) fail(err error) {
	m.respond = func(registry.ModelRequest) (registry.ModelResponse, error) { return registry.ModelResponse{}, err }
}

func (m *scriptedReviewModel) registration() registry.ModelRegistration {
	return registry.ModelRegistration{ModelMetadata: domain.ModelMetadata{
		ID: reviewTextModelID, DisplayName: reviewTextModelID,
		Capabilities: []string{domain.ModelCapabilityTextGeneration, domain.ModelCapabilityStructuredDecision},
		ConfigSchema: json.RawMessage(`{"type":"object"}`),
	}}
}

func (m *scriptedReviewModel) Models(context.Context) ([]registry.ModelRegistration, error) {
	return []registry.ModelRegistration{m.registration()}, nil
}

func (m *scriptedReviewModel) Generate(_ context.Context, request registry.ModelRequest) (registry.ModelResponse, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	respond := m.respond
	m.mu.Unlock()
	return respond(request)
}

func (m *scriptedReviewModel) Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool) {
	if modelID != reviewTextModelID {
		return registry.ModelRegistration{}, nil, false
	}
	return m.registration(), m, true
}

func (m *scriptedReviewModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// repoContextReader adapts the workspace package's reader to the review Tool's port, as the
// process entry point does.
type repoContextReader struct {
	reader *workspace.ContextReader
}

func (r repoContextReader) ReadFiles(ctx context.Context, view *registry.WorkspaceView, names []string) ([]reviewpatch.ContextFile, error) {
	files, err := r.reader.ReadFiles(ctx, view, names)
	if err != nil {
		return nil, err
	}
	out := make([]reviewpatch.ContextFile, len(files))
	for i, file := range files {
		out[i] = reviewpatch.ContextFile{Path: file.Path, Content: file.Content, Truncated: file.Truncated}
	}
	return out, nil
}

// reviewFixture is a changeRequestFixture whose Backend also registers review_patch against
// the scripted review model, and whose Definitions select that model as reviewModelId.
type reviewFixture struct {
	*changeRequestFixture
	model *scriptedReviewModel
}

func newReviewFixture(t *testing.T, model *scriptedReviewModel, extra ...registry.ToolRegistration) *reviewFixture {
	t.Helper()
	stub := newVerifyRunnerStub()
	runner := httptest.NewServer(stub)
	t.Cleanup(runner.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	repo := newReviewRepo(t)
	tools := []registry.ToolRegistration{
		verifypatch.Registration(runner.URL, runner.Client(), artifacts),
		changerequest.Registration(artifacts),
		reviewpatch.Registration(reviewPorts(model, artifacts, repo)),
	}
	h := newWorkspaceHarness(t, workspaceHarnessOptions{
		repo: repo, extraTools: append(tools, extra...), modelProviders: []registry.ModelProvider{model},
	})
	return &reviewFixture{changeRequestFixture: &changeRequestFixture{h: h, repo: repo, stub: stub, artifacts: artifacts}, model: model}
}

const (
	reviewRulesFile    = "RULES.md"
	reviewAdvisoryFile = "advisories/fixture-advisory.md"
	reviewRulesText    = "# Project rules\n\nAdapt every caller of a changed type.\n"
	reviewAdvisoryText = "# Advisory\n\nUpgrade the token library to the fixed version.\n"
)

// newReviewRepo is the workspace fixture repository with the rules file and an advisory the
// review Tool reads beside the patch, committed as the base commit.
func newReviewRepo(t *testing.T) *workspaceRepo {
	t.Helper()
	repo := newWorkspaceRepo(t)
	for rel, content := range map[string]string{reviewRulesFile: reviewRulesText, reviewAdvisoryFile: reviewAdvisoryText} {
		full := filepath.Join(repo.path, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wsGit(t, repo.path, "add", "-A")
	wsGit(t, repo.path, "commit", "-q", "-m", "review context")
	repo.commit = wsGit(t, repo.path, "rev-parse", "HEAD")
	return repo
}

func reviewPorts(model *scriptedReviewModel, artifacts *asset.ArtifactStore, repo *workspaceRepo) reviewpatch.Ports {
	repos := workspace.NewLocalGit(map[string]string{wsRepositoryID: repo.path})
	return reviewpatch.Ports{Models: model, Artifacts: artifacts, Workspace: repoContextReader{reader: workspace.NewContextReader(repos)}}
}

// reviewDefinition is the fixture Definition allowing the workspace, verification, change
// request and review Tools plus extraTools, with reviewModelId set to the scripted model.
func (f *reviewFixture) reviewDefinition(t *testing.T, workflowID string, extraTools []string) domain.Definition {
	t.Helper()
	tools := append(append([]string{}, wsAllTools...), verifypatch.ToolName, changerequest.ToolName, reviewpatch.ToolName)
	tools = append(tools, extraTools...)
	return withAgentConfig(t, workspaceDefinition(workflowID, f.repo.commit, true, tools...), "node_agent",
		map[string]any{"reviewModelId": reviewTextModelID})
}

// start mirrors changeRequestFixture.start with the review Tool allowed and the review model
// selected.
func (f *reviewFixture) start(t *testing.T, workflowID string, extraTools []string, calls ...crCall) {
	t.Helper()
	saved := f.h.saveDefinition(f.reviewDefinition(t, workflowID, extraTools))
	f.run = f.h.createRun(saved.WorkflowID, saved.Version, `{"question":"`+agentQuestion+`"}`)
	f.outcome = f.h.claimAgentNode(f.run.ID)
	var turn int
	var mu sync.Mutex
	f.h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		mu.Lock()
		turn++
		n := turn
		mu.Unlock()
		if n > len(calls) {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
		call := calls[n-1]()
		call.Kind = mockmodel.ScenarioToolCall
		return &call
	}
	f.h.execute(f.outcome)
}

func reviewCall(patchDigest string) crCall {
	return fixed(wsCall(reviewpatch.ToolName, map[string]any{"patchDigest": patchDigest}))
}

// reviewFactOf returns the one patch_reviewed fact of the fixture's Agent Run.
func (f *reviewFixture) reviewFactOf(t *testing.T) domain.ExecutionFact {
	t.Helper()
	for _, fact := range listFactsByAgentRun(f.h.ctx, t, f.h.uow, f.agentRunID(t)) {
		if fact.FactType == reviewpatch.FactType {
			return fact
		}
	}
	t.Fatal("no patch_reviewed fact recorded")
	return domain.ExecutionFact{}
}

// attemptArtifactLinks counts the Artifacts linked to attemptID.
func (f *reviewFixture) attemptArtifactLinks(t *testing.T, attemptID string) int {
	t.Helper()
	var linked int
	if err := f.h.pool.QueryRow(f.h.ctx, `SELECT count(*) FROM tool_attempt_artifacts WHERE tool_attempt_id = $1`, attemptID).Scan(&linked); err != nil {
		t.Fatalf("count attempt artifact links: %v", err)
	}
	return linked
}

// definitionService runs the Definition save path over the harness's own registrations, the
// way the API's save handler does, so a configuration defect is reported as a validation
// failure (an HTTP 400) rather than at Run creation.
func (f *reviewFixture) definitionService() *service.DefinitionService {
	return service.NewDefinitionService(service.Deps{
		UoW: f.h.uow, Nodes: f.h.nodes, Models: f.h.models, Tools: f.h.tools, Compiler: f.h.compiler, Clock: f.h.clock, IDs: f.h.ids,
	})
}

// assertSaveRejectedAtReviewModel saves def through the Definition service and proves the
// save failed validation with SEMANTIC_VALIDATION_FAILED at the node's reviewModelId whose
// message mentions wantMention.
func (f *reviewFixture) assertSaveRejectedAtReviewModel(t *testing.T, def domain.Definition, wantMention string) {
	t.Helper()
	_, err := f.definitionService().Create(f.h.ctx, service.CreateDefinition{Name: def.Name, Nodes: def.Nodes, Edges: def.Edges})
	var compileErr *service.CompileFailedError
	if !errors.As(err, &compileErr) {
		t.Fatalf("Create error = %v, want a compile failure", err)
	}
	for _, item := range compileErr.Result.Errors {
		if item.Path == "nodes[node_agent].config.reviewModelId" && item.Code == runtime.CodeSemanticValidationFailed && strings.Contains(item.Message, wantMention) {
			return
		}
	}
	t.Fatalf("validation errors = %+v, want SEMANTIC_VALIDATION_FAILED at reviewModelId mentioning %q", compileErr.Result.Errors, wantMention)
}

// ---------------------------------------------------------------------------
// Definition validation
// ---------------------------------------------------------------------------

// The allowlist holds review_patch but the node selects no review model: the save is a
// semantic validation failure, because the review must come from a model and never from
// a mock policy.
func TestDefinitionSave_ReviewPatchWithoutReviewModel_RejectedAsSemanticValidation(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(reviewCleanOpinion))
	def := workspaceDefinition("wf-review-no-model", f.repo.commit, true, append(append([]string{}, wsAllTools...), reviewpatch.ToolName)...)
	f.assertSaveRejectedAtReviewModel(t, def, reviewpatch.ToolName)
	if f.model.calls() != 0 {
		t.Errorf("review model asked %d times, want 0", f.model.calls())
	}
}

// The review model is the decision model itself: an independent review needs another
// model, so the save fails the same way.
func TestDefinitionSave_ReviewModelSameAsDecisionModel_RejectedAsSemanticValidation(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(reviewCleanOpinion))
	def := withAgentConfig(t, workspaceDefinition("wf-review-same-model", f.repo.commit, true, append(append([]string{}, wsAllTools...), reviewpatch.ToolName)...),
		"node_agent", map[string]any{"reviewModelId": mockmodel.ModelID})
	f.assertSaveRejectedAtReviewModel(t, def, "modelId")
}

// Run creation applies the same rule, so a Definition saved by an older process cannot
// start a Run with a non-independent review.
func TestCreateRun_ReviewModelSameAsDecisionModel_RejectedWithoutReviewCall(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(reviewCleanOpinion))
	def := f.h.saveDefinition(withAgentConfig(t, workspaceDefinition("wf-review-same-model-run", f.repo.commit, true, append(append([]string{}, wsAllTools...), reviewpatch.ToolName)...),
		"node_agent", map[string]any{"reviewModelId": mockmodel.ModelID}))
	_, err := f.h.svc.CreateRun(f.h.ctx, service.CreateRun{WorkflowID: def.WorkflowID, DefinitionVersion: def.Version, Input: json.RawMessage(`{"question":"` + agentQuestion + `"}`)})
	var resolution *service.RegistryResolutionError
	if !errors.As(err, &resolution) {
		t.Fatalf("CreateRun error = %v, want a registry resolution error", err)
	}
	found := false
	for _, item := range resolution.Underlying.Errors {
		if item.Path == "nodes[node_agent].config.reviewModelId" && item.Code == runtime.CodeSemanticValidationFailed {
			found = true
		}
	}
	if !found {
		t.Errorf("validation errors = %+v, want SEMANTIC_VALIDATION_FAILED at reviewModelId", resolution.Underlying.Errors)
	}
}

// ---------------------------------------------------------------------------
// Refusal at claim
// ---------------------------------------------------------------------------

// A write moves the workspace to patch 1; a review naming the baseline digest is STALE_PATCH:
// no Attempt, no model call, no fact, no Artifact.
func TestReviewPatch_StalePatchDigest_ReturnedWithoutAttemptOrModelCall(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(reviewCleanOpinion))
	f.start(t, "wf-review-stale", nil, fixed(wsWrite(wsTokenPath, wsNewToken)), reviewCall(baselineDigest()))

	message := f.assertReturned(t, 2, domain.ReturnStalePatch)
	latest := wsLatestPatch(t, f.h, f.agentRunID(t))
	if latest == nil || latest.Seq != 1 {
		t.Fatalf("latest patch = %+v, want the write's patch 1", latest)
	}
	if text, _ := message["message"].(string); !strings.Contains(text, baselineDigest()) || !strings.Contains(text, latest.PatchDigest) {
		t.Errorf("tool message %q should name the stale digest and the current one", text)
	}
	if f.model.calls() != 0 {
		t.Errorf("review model asked %d times, want 0", f.model.calls())
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, f.agentRunID(t)); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
}

// ---------------------------------------------------------------------------
// Failures of the review itself
// ---------------------------------------------------------------------------

// assertReviewFailed proves the review of Turn turnNo ended as a Tool error: the Attempt
// FAILED with TOOL_ERROR, no fact, no Artifact linked to the Attempt, the Agent Run
// terminated with TOOL_ERROR, and the recorded error names the review model but never the
// patch, the repository text or the model's answer.
func (f *reviewFixture) assertReviewFailed(t *testing.T, turnNo int, wantMention string) {
	t.Helper()
	agentRunID := f.agentRunID(t)
	action := wsActionOfTurn(t, f.h, agentRunID, turnNo)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "TOOL_ERROR" {
		t.Fatalf("action = status %s error %+v, want FAILED with TOOL_ERROR", action.Status, action.Error)
	}
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "TOOL_ERROR" || len(attempt.Result) != 0 {
		t.Fatalf("attempt = %+v, want FAILED with TOOL_ERROR and no result", attempt)
	}
	for _, text := range []string{attempt.Error.Message, action.Error.Message} {
		if !strings.Contains(text, reviewTextModelID) || !strings.Contains(text, wantMention) {
			t.Errorf("error %q should name the review model and mention %q", text, wantMention)
		}
		for _, forbidden := range []string{wsNewToken, "+require", "Project rules", "ValidationError", "looks fine"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("error %q carries %q, which must stay out of Trace", text, forbidden)
			}
		}
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
	if linked := f.attemptArtifactLinks(t, attempt.ID); linked != 0 {
		t.Errorf("artifacts linked to the failed attempt = %d, want none", linked)
	}
	events := listEvents(f.h.ctx, t, f.h.uow, f.run.ID)
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureSyncExecution) || failed["returnedToAgent"] == true {
		t.Errorf("AGENT_ACTION_FAILED = %v, want the SYNC_EXECUTION source without a return to the Agent", failed)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationToolError {
		t.Errorf("agent termination = %s, want TOOL_ERROR", agentTermination(agentRun))
	}
	if got := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
}

// The review model answers with prose instead of the structured opinion: the Tool result is
// unusable, the Attempt fails and no fact or Artifact is written.
func TestReviewPatch_UnparsableOpinion_AttemptFailsWithoutFact(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(`the patch looks fine to me`))
	f.start(t, "wf-review-unparsable", nil, reviewCall(baselineDigest()))
	f.assertReviewFailed(t, 1, "opinion")
	if f.model.calls() != 1 {
		t.Errorf("review model asked %d times, want 1", f.model.calls())
	}
}

// The review model's Provider fails: the synchronous Tool rule applies, the Attempt fails as
// a Tool error, the Agent Run ends with TOOL_ERROR and nothing is recorded.
func TestReviewPatch_ReviewModelProviderFails_ToolErrorWithoutFact(t *testing.T) {
	model := newScriptedReviewModel(reviewCleanOpinion)
	model.fail(errors.New("upstream returned 503"))
	f := newReviewFixture(t, model)
	f.start(t, "wf-review-provider-fails", nil, reviewCall(baselineDigest()))
	f.assertReviewFailed(t, 1, "upstream returned 503")
}

// ---------------------------------------------------------------------------
// Recorded reviews
// ---------------------------------------------------------------------------

// assertReviewRecorded proves the review of Turn turnNo committed: the Attempt SUCCEEDED, a
// patch_reviewed fact about the baseline digest with the given verdict, bound to the review
// model and an opinion Artifact whose stored text is the model's opinion and which the fact
// never copies; the tool message carries the summary and the blocking list; and the Trace
// projection lists the fact.
func (f *reviewFixture) assertReviewRecorded(t *testing.T, turnNo int, wantPassed bool, opinion string) domain.ExecutionFact {
	t.Helper()
	agentRunID := f.agentRunID(t)
	action := wsActionOfTurn(t, f.h, agentRunID, turnNo)
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if action.Status != domain.AgentActionSucceeded || attempt.Status != domain.ToolAttemptSucceeded {
		t.Fatalf("action %s attempt %s, want both SUCCEEDED", action.Status, attempt.Status)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	var fact domain.ExecutionFact
	reviews := 0
	for _, candidate := range facts {
		if candidate.FactType == reviewpatch.FactType {
			fact, reviews = candidate, reviews+1
		}
	}
	if reviews != 1 {
		t.Fatalf("facts = %+v, want exactly one %s", facts, reviewpatch.FactType)
	}
	if fact.SubjectRef != baselineDigest() || fact.ToolAttemptID != attempt.ID || fact.Verdict == nil || *fact.Verdict != wantPassed || fact.BasisFactID != nil {
		t.Errorf("fact = %+v, want it about the baseline digest from attempt %s with verdict %t and no basis", fact, attempt.ID, wantPassed)
	}
	var binding map[string]string
	if err := json.Unmarshal(fact.Binding, &binding); err != nil {
		t.Fatalf("decode binding %s: %v", fact.Binding, err)
	}
	if binding["reviewModelId"] != reviewTextModelID || binding["opinionArtifactId"] == "" || len(binding) != 2 {
		t.Errorf("binding = %v, want exactly the review model and the opinion artifact reference", binding)
	}
	if strings.Contains(string(fact.Binding), "findings") || strings.Contains(string(fact.Binding), "summary") {
		t.Errorf("binding %s copies the opinion; the fact must only reference it", fact.Binding)
	}
	record, content := f.artifactContent(t, binding["opinionArtifactId"])
	if content != opinion || record.Artifact.MediaType != domain.ArtifactMediaTypeTextPlain {
		t.Errorf("opinion artifact = %+v with content %q, want the model's opinion as text", record.Artifact, content)
	}
	if linked := f.attemptArtifactLinks(t, attempt.ID); linked != 1 {
		t.Errorf("artifacts linked to the attempt = %d, want the one opinion", linked)
	}
	if attempt.Cost == nil || attempt.Cost.Source != reviewTextModelID || attempt.Cost.Amount != "0.001" {
		t.Errorf("attempt cost = %+v, want the charge attributed to the review model", attempt.Cost)
	}

	message := wsToolMessage(t, f.h, agentRunID, turnNo, action.ID)
	if message["passed"] != wantPassed || message["blocking"] != !wantPassed || message["opinionArtifactId"] != binding["opinionArtifactId"] || message["reviewModelId"] != reviewTextModelID {
		t.Errorf("tool message = %v, want the verdict, the model and the opinion reference", message)
	}
	if _, ok := message["summary"].(string); !ok {
		t.Errorf("tool message = %v, want a summary", message)
	}
	if _, ok := message["blockingFindings"].([]any); !ok {
		t.Errorf("tool message = %v, want a blocking findings list", message)
	}

	query := service.NewQueryService(service.Deps{UoW: f.h.uow})
	trace, err := query.AgentTrace(f.h.ctx, f.run.ID, f.outcome.NodeRunID)
	if err != nil {
		t.Fatalf("agent trace: %v", err)
	}
	listed := false
	for _, item := range trace.Facts.Facts {
		if item.ID == fact.ID && item.FactType == reviewpatch.FactType {
			listed = true
		}
	}
	if !listed {
		t.Errorf("trace facts = %+v, want the review fact listed", trace.Facts)
	}
	return fact
}

// A blocking opinion: the fact's verdict is false, the opinion is kept as a text Artifact and
// the Agent reads the blocking finding; the loop continues to the Agent's next Turn.
func TestReviewPatch_BlockingOpinion_FactVerdictFalseWithOpinionArtifact(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(reviewBlockingOpinion))
	f.start(t, "wf-review-blocking", nil, reviewCall(baselineDigest()))

	f.assertReviewRecorded(t, 1, false, reviewBlockingOpinion)
	message := wsToolMessage(t, f.h, f.agentRunID(t), 1, wsActionOfTurn(t, f.h, f.agentRunID(t), 1).ID)
	findings, _ := message["blockingFindings"].([]any)
	if len(findings) != 1 || message["findingCount"] != float64(1) {
		t.Errorf("tool message = %v, want the one blocking finding", message)
	}
	if f.model.calls() != 1 {
		t.Errorf("review model asked %d times, want 1", f.model.calls())
	}
	request := f.model.requests[0]
	if request.ModelID != reviewTextModelID || len(request.Messages) != 1 || len(request.Messages[0].Parts) != 1 {
		t.Fatalf("review request = %+v, want one text message to the review model", request)
	}
	text := request.Messages[0].Parts[0].Text
	for _, want := range []string{"Repository file " + reviewRulesFile, "Repository file " + reviewAdvisoryFile, reviewRulesText, reviewAdvisoryText, f.repo.commit, baselineDigest()} {
		if !strings.Contains(text, want) {
			t.Errorf("review request lacks %q", want)
		}
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %s, want FINAL_RESPONSE after the Agent read the review", agentTermination(agentRun))
	}
}

// A clean opinion: the fact's verdict is true and the empty blocking list reaches the Agent.
func TestReviewPatch_CleanOpinion_FactVerdictTrueWithOpinionArtifact(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(reviewCleanOpinion))
	f.start(t, "wf-review-clean", nil, reviewCall(baselineDigest()))

	f.assertReviewRecorded(t, 1, true, reviewCleanOpinion)
	message := wsToolMessage(t, f.h, f.agentRunID(t), 1, wsActionOfTurn(t, f.h, f.agentRunID(t), 1).ID)
	findings, _ := message["blockingFindings"].([]any)
	if len(findings) != 0 || message["findingCount"] != float64(0) {
		t.Errorf("tool message = %v, want no blocking finding", message)
	}
}

// ---------------------------------------------------------------------------
// Change requests that attach a review
// ---------------------------------------------------------------------------

// changeRequestCiting scripts a create_change_request call whose patch digest and cited
// review fact are resolved when the model is asked, so they can name records earlier
// rounds committed.
func (f *reviewFixture) changeRequestCiting(t *testing.T, patchDigest, factID func() string, basisTurn int, unresolved ...string) crCall {
	t.Helper()
	return func() mockmodel.Scenario {
		args := changeRequestArguments(patchDigest(), f.instructionsBasis(t, basisTurn))
		args["reviewFactId"] = factID()
		if len(unresolved) > 0 {
			args["unresolvedFindings"] = unresolved
		}
		return wsCall(changerequest.ToolName, args)
	}
}

// The baseline is verified and reviewed; the change request cites the review and lists an
// unresolved finding. The record commits, its content carries the review section and the
// request names the fact.
func TestChangeRequest_CitedReview_AttachedToTheRecordedRequest(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(reviewBlockingOpinion))
	f.start(t, "wf-cr-review", nil,
		verifyBaselineCall(),
		reviewCall(baselineDigest()),
		f.changeRequestCiting(t, baselineDigest, func() string { return f.reviewFactOf(t).ID }, 1, "The caller type assertion is adapted in a follow-up."),
	)
	f.deliverVerification(t, true)

	request, attempt := f.recordedRequest(t, 3)
	review := f.reviewFactOf(t)
	_, content := f.artifactContent(t, request.ContentArtifactID)
	for _, want := range []string{"## Review", "Fact: `" + review.ID + "`", "Blocking findings: `yes`", "- The caller type assertion is adapted in a follow-up."} {
		if !strings.Contains(content, want) {
			t.Errorf("content lacks %q:\n%s", want, content)
		}
	}
	var output map[string]any
	_ = json.Unmarshal(attempt.Result, &output)
	if output["reviewFactId"] != review.ID {
		t.Errorf("attempt result = %s, want it to name the review fact %s", attempt.Result, review.ID)
	}
	if message := wsToolMessage(t, f.h, f.agentRunID(t), 3, wsActionOfTurn(t, f.h, f.agentRunID(t), 3).ID); message["reviewFactId"] != review.ID {
		t.Errorf("tool message = %v, want it to name the review fact", message)
	}
}

// The baseline is reviewed, then a write moves the workspace to patch 1, which is verified.
// A change request for patch 1 citing the baseline's review cites a review of another
// patch: the claim refuses it as a defective Decision, before any Attempt exists.
func TestChangeRequest_ReviewFactOfAnotherPatch_RejectedAtClaim(t *testing.T) {
	f := newReviewFixture(t, newScriptedReviewModel(reviewCleanOpinion))
	patch1Digest := func() string { return wsLatestPatch(t, f.h, f.agentRunID(t)).PatchDigest }
	f.start(t, "wf-cr-review-other-patch", nil,
		reviewCall(baselineDigest()),
		fixed(wsWrite(wsTokenPath, wsNewToken)),
		func() mockmodel.Scenario {
			return wsCall(verifypatch.ToolName, map[string]any{"patchDigest": patch1Digest()})
		},
		f.changeRequestCiting(t, patch1Digest, func() string { return f.reviewFactOf(t).ID }, 1),
	)
	token, _ := f.stub.dispatched(t)
	delivered, err := f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{
		Token: token, ExternalTaskID: verifyExternalTaskID,
		Payload: verifySucceededPayload(f.repo.commit, patch1Digest(), "sha256:verified", true),
	})
	if err != nil || !delivered.Accepted {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns after the verification: %v", err)
	}

	f.assertBasisRejected(t, 4, "another patch")
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, f.agentRunID(t)); len(facts) != 2 {
		t.Errorf("facts = %d, want the baseline's review and patch 1's verification only", len(facts))
	}
}

// tamperedReviewChangeRequest wraps the real executor and swaps the review fact the record
// names for the verification fact, which exists in the Run but is no review: the result
// transaction's own check must refuse the record.
type tamperedReviewChangeRequest struct {
	inner registry.ToolExecutor
}

func (d *tamperedReviewChangeRequest) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	got, err := d.inner.Execute(ctx, action)
	if err != nil {
		return got, err
	}
	got.Result.ChangeRequest.ReviewFactID = got.Result.ChangeRequest.VerificationFactID
	return got, nil
}

func TestChangeRequest_ReviewFactTampered_ResultTransactionRejects(t *testing.T) {
	model := newScriptedReviewModel(reviewCleanOpinion)
	stub := newVerifyRunnerStub()
	runner := httptest.NewServer(stub)
	t.Cleanup(runner.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	double := changerequest.Registration(artifacts)
	double.Metadata.Name = reviewDoubleTool
	double.Executor = &tamperedReviewChangeRequest{inner: changerequest.New(artifacts)}
	repo := newReviewRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, modelProviders: []registry.ModelProvider{model}, extraTools: []registry.ToolRegistration{
		verifypatch.Registration(runner.URL, runner.Client(), artifacts), changerequest.Registration(artifacts),
		reviewpatch.Registration(reviewPorts(model, artifacts, repo)), double,
	}})
	f := &reviewFixture{changeRequestFixture: &changeRequestFixture{h: h, repo: repo, stub: stub, artifacts: artifacts}, model: model}
	f.start(t, "wf-cr-review-tampered", []string{reviewDoubleTool},
		verifyBaselineCall(),
		reviewCall(baselineDigest()),
		func() mockmodel.Scenario {
			args := changeRequestArguments(baselineDigest(), f.instructionsBasis(t, 1))
			args["reviewFactId"] = f.reviewFactOf(t).ID
			return wsCall(reviewDoubleTool, args)
		},
	)
	f.deliverVerification(t, true)
	agentRunID := f.agentRunID(t)

	action := wsActionOfTurn(t, f.h, agentRunID, 3)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "CHANGE_REQUEST_INVALID" {
		t.Fatalf("action = status %s error %+v, want FAILED with CHANGE_REQUEST_INVALID", action.Status, action.Error)
	}
	if !strings.Contains(action.Error.Message, "review") {
		t.Errorf("action error = %q, want it to mention the review", action.Error.Message)
	}
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "CHANGE_REQUEST_INVALID" || len(attempt.Result) != 0 {
		t.Errorf("attempt = %+v, want FAILED with CHANGE_REQUEST_INVALID and no result", attempt)
	}
	if requests := changeRequestsOf(t, f.h, agentRunID); len(requests) != 0 {
		t.Errorf("change requests = %+v, want none", requests)
	}
	var count int
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		requests, err := tx.ChangeRequests().ListByAgentRun(ctx, agentRunID, 10)
		count = len(requests)
		return err
	}); err != nil || count != 0 {
		t.Errorf("change requests after rollback = %d (%v), want none", count, err)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationToolError {
		t.Errorf("agent termination = %s, want TOOL_ERROR", agentTermination(agentRun))
	}
}
