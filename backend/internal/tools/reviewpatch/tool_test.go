package reviewpatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testPatch       = "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -5 +5 @@\n-require github.com/golang-jwt/jwt v3.2.2+incompatible\n+require github.com/golang-jwt/jwt/v5 v5.3.1\n"
	testBaseCommit  = "2706ca6fb4ddbe969e9acfa5464a347ff90c7774"
	testReviewModel = "text-reviewer.1"
	testRules       = "# Project rules\n\nUse the latest mirrored major version.\n"
	testAdvisory    = "# GO-2025-3553\n\nFixed in v5.2.2.\n"
)

// memoryArtifacts is an in-memory ArtifactTextStore that keeps every text it was handed.
type memoryArtifacts struct {
	texts  []string
	failed error
}

func (m *memoryArtifacts) WriteText(_ context.Context, r io.Reader) (domain.ArtifactRef, error) {
	if m.failed != nil {
		return domain.ArtifactRef{}, m.failed
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return domain.ArtifactRef{}, err
	}
	m.texts = append(m.texts, string(content))
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	id, _ := domain.ArtifactIDForSHA256(digest)
	return domain.ArtifactRef{ArtifactID: id, MediaType: domain.ArtifactMediaTypeTextPlain, SizeBytes: int64(len(content)), SHA256: digest}, nil
}

// fakeWorkspace answers every read with the fixture's rules file and advisory and records
// the names asked for.
type fakeWorkspace struct {
	names  []string
	failed error
}

func (w *fakeWorkspace) ReadFiles(_ context.Context, _ *registry.WorkspaceView, names []string) ([]ContextFile, error) {
	w.names = names
	if w.failed != nil {
		return nil, w.failed
	}
	return []ContextFile{{Path: "RULES.md", Content: testRules}, {Path: "advisories/GO-2025-3553.md", Content: testAdvisory, Truncated: true}}, nil
}

// fakeReviewModel answers every Generate with answer or err and records the requests.
type fakeReviewModel struct {
	capabilities []string
	answer       registry.ModelDecision
	cost         *domain.Cost
	err          error
	requests     []registry.ModelRequest
}

func (m *fakeReviewModel) Models(context.Context) ([]registry.ModelRegistration, error) {
	return nil, nil
}

func (m *fakeReviewModel) Generate(_ context.Context, request registry.ModelRequest) (registry.ModelResponse, error) {
	m.requests = append(m.requests, request)
	if m.err != nil {
		return registry.ModelResponse{}, m.err
	}
	return registry.ModelResponse{Decision: m.answer, Cost: m.cost}, nil
}

func (m *fakeReviewModel) Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool) {
	if modelID != testReviewModel {
		return registry.ModelRegistration{}, nil, false
	}
	return registry.ModelRegistration{ModelMetadata: domain.ModelMetadata{ID: modelID, Capabilities: m.capabilities}}, m, true
}

func textReviewer(output string) *fakeReviewModel {
	return &fakeReviewModel{
		capabilities: []string{domain.ModelCapabilityStructuredDecision},
		answer:       registry.ModelDecision{Kind: registry.DecisionFinal, Output: json.RawMessage(output)},
	}
}

func workspace() *registry.WorkspaceView {
	return &registry.WorkspaceView{
		RepositoryID: "vuln-demo", BaseCommit: testBaseCommit, Patch: testPatch,
		PatchDigest: domain.WorkspacePatchDigest(testPatch), PatchSeq: 2, AllowedPaths: []string{"go.mod", "internal/auth"},
	}
}

func action(patchDigest, reviewModelID string) registry.ToolAction {
	return registry.ToolAction{
		RunID: "run_1", AgentRunID: "ar_1", TurnID: "turn_3", ActionID: "act_3", ToolName: ToolName, AttemptNo: 1,
		Arguments: json.RawMessage(`{"patchDigest":"` + patchDigest + `"}`), Workspace: workspace(), ReviewModelID: reviewModelID,
	}
}

type fixture struct {
	model     *fakeReviewModel
	artifacts *memoryArtifacts
	workspace *fakeWorkspace
	executor  *Executor
}

func newFixture(model *fakeReviewModel) *fixture {
	f := &fixture{model: model, artifacts: &memoryArtifacts{}, workspace: &fakeWorkspace{}}
	f.executor = New(Ports{Models: model, Artifacts: f.artifacts, Workspace: f.workspace})
	return f
}

func (f *fixture) review(t *testing.T) (result, registry.ToolResult) {
	t.Helper()
	got, err := f.executor.Execute(context.Background(), action(workspace().PatchDigest, testReviewModel))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultCompleted || got.Result == nil {
		t.Fatalf("result = %+v, want a completed result", got)
	}
	var out result
	if err := json.Unmarshal(got.Result.Output, &out); err != nil {
		t.Fatalf("decode output %s: %v", got.Result.Output, err)
	}
	return out, *got.Result
}

const blockingOpinion = `{"blocking":true,"findings":[{"severity":"blocking","path":"internal/auth/token.go","message":"ParseWithClaims still expects a ValidationError, which v5 no longer returns."},{"severity":"minor","path":"go.mod","message":"The old require line can go."}],"summary":"The upgrade is incomplete: one caller is not adapted."}`

const cleanOpinion = `{"blocking":false,"findings":[{"severity":"minor","path":"","message":"Consider a changelog entry."}],"summary":"The patch upgrades the dependency and adapts every caller."}`

func TestRegistration_IndependentReviewProducingVerdictFact_RegistersCleanly(t *testing.T) {
	reg := Registration(Ports{})
	if err := reg.Metadata.Validate(); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	m := reg.Metadata
	if m.ExecutionKind != domain.ToolExecutionSync || m.SideEffect.Kind != domain.SideEffectNone || m.SideEffect.Idempotency != domain.IdempotencySafe {
		t.Errorf("execution %s side effect %+v, want SYNC with NONE + SAFE", m.ExecutionKind, m.SideEffect)
	}
	if !m.UsesWorkspace || m.WorkspacePatchDigestPath != "/patchDigest" || m.WorkspaceWritePath != "" || len(m.Records) != 0 {
		t.Errorf("metadata = %+v, want a read-only workspace Tool naming its digest at /patchDigest", m)
	}
	if !m.UsesReviewModel || !m.RequiresReviewModel || m.ReviewModelCapability != domain.ModelCapabilityStructuredDecision {
		t.Errorf("metadata = %+v, want a required independent review model declaring %s", m, domain.ModelCapabilityStructuredDecision)
	}
	produces := m.Produces
	if produces == nil || produces.FactType != FactType || produces.VerdictPointer == nil || produces.VerdictPointer.Pointer != "/passed" ||
		produces.SubjectPointer.Pointer != "/patchDigest" || produces.BindArguments["opinionArtifactId"].Pointer != "/opinionArtifactId" ||
		produces.BindArguments["reviewModelId"].Pointer != "/reviewModelId" || produces.BasisFactType != "" {
		t.Errorf("produces = %+v, want %s about the patch digest bound to the model and the opinion artifact", produces, FactType)
	}
	if len(m.Requires) != 0 || m.Authorization != nil || m.CountsTowardGenerationLimit {
		t.Errorf("metadata requires %v authorization %v counted %t, want an ungated, uncounted LOW-risk review", m.Requires, m.Authorization, m.CountsTowardGenerationLimit)
	}
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestExecute_BlockingOpinion_FailsVerdictAndListsBlockingFindings(t *testing.T) {
	f := newFixture(textReviewer(blockingOpinion))
	f.model.cost = &domain.Cost{Amount: "0.0042", Currency: domain.CurrencyUSD, Source: "upstream"}
	out, result := f.review(t)

	ws := workspace()
	if out.PatchDigest != ws.PatchDigest || out.ReviewModelID != testReviewModel || out.Passed || !out.Blocking || out.FindingCount != 2 {
		t.Errorf("output = %+v, want a blocking review of the current patch with two findings", out)
	}
	if len(out.BlockingFindings) != 1 || out.BlockingFindings[0].Path != "internal/auth/token.go" || out.BlockingFindings[0].Severity != severityBlocking {
		t.Errorf("blocking findings = %+v, want the one blocking finding", out.BlockingFindings)
	}
	if out.Summary != "The upgrade is incomplete: one caller is not adapted." {
		t.Errorf("summary = %q", out.Summary)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].ArtifactID != out.OpinionArtifactID || result.Artifacts[0].MediaType != domain.ArtifactMediaTypeTextPlain {
		t.Fatalf("artifacts = %+v, want the one opinion artifact the output names", result.Artifacts)
	}
	if len(f.artifacts.texts) != 1 || f.artifacts.texts[0] != blockingOpinion {
		t.Errorf("stored opinion = %q, want the model's answer verbatim", f.artifacts.texts)
	}
	if result.Cost == nil || result.Cost.Amount != "0.0042" || result.Cost.Source != testReviewModel {
		t.Errorf("cost = %+v, want the reported charge attributed to the review model", result.Cost)
	}
	if result.WorkspacePatch != nil || result.ChangeRequest != nil {
		t.Errorf("result carries records %+v %+v, want none", result.WorkspacePatch, result.ChangeRequest)
	}
}

func TestExecute_CleanOpinion_PassesWithEmptyBlockingList(t *testing.T) {
	f := newFixture(textReviewer(cleanOpinion))
	out, result := f.review(t)
	if !out.Passed || out.Blocking || out.FindingCount != 1 || len(out.BlockingFindings) != 0 || out.BlockingFindings == nil {
		t.Errorf("output = %+v, want a passing review with an empty blocking list", out)
	}
	if result.Cost != nil {
		t.Errorf("cost = %+v, want none when the model reported none", result.Cost)
	}
	if !strings.Contains(string(result.Output), `"blockingFindings":[]`) {
		t.Errorf("output %s should encode the empty blocking list as a list", result.Output)
	}
}

func TestExecute_ModelRequest_CarriesRulesAdvisoriesAndPatchAsText(t *testing.T) {
	f := newFixture(textReviewer(cleanOpinion))
	f.review(t)
	if strings.Join(f.workspace.names, ",") != strings.Join(ContextNames, ",") {
		t.Errorf("read %v, want %v", f.workspace.names, ContextNames)
	}
	if len(f.model.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(f.model.requests))
	}
	request := f.model.requests[0]
	if request.ModelID != testReviewModel || request.Instructions != reviewInstructions || string(request.FinalOutputSchema) != opinionSchema || len(request.DecisionSchema) == 0 {
		t.Errorf("request = %+v, want the review model, the fixed instructions and the opinion schema", request)
	}
	if len(request.Messages) != 1 || len(request.Messages[0].Parts) != 1 || request.Messages[0].Parts[0].Type != registry.ContentPartText {
		t.Fatalf("messages = %+v, want one user message with one text part", request.Messages)
	}
	text := request.Messages[0].Parts[0].Text
	for _, want := range []string{testBaseCommit, workspace().PatchDigest, "Touched paths: go.mod", "Repository file RULES.md ---", testRules,
		"Repository file advisories/GO-2025-3553.md (truncated) ---", testAdvisory, "--- Patch (unified diff", testPatch} {
		if !strings.Contains(text, want) {
			t.Errorf("user message lacks %q:\n%s", want, text)
		}
	}
}

func TestExecute_UnusableOpinion_FailsWithoutQuotingIt(t *testing.T) {
	for name, decision := range map[string]registry.ModelDecision{
		"tool call":       {Kind: registry.DecisionToolCall},
		"not JSON":        {Kind: registry.DecisionFinal, Output: json.RawMessage(`the patch looks fine`)},
		"wrong shape":     {Kind: registry.DecisionFinal, Output: json.RawMessage(`{"blocking":"no","findings":[],"summary":"ok"}`)},
		"bad severity":    {Kind: registry.DecisionFinal, Output: json.RawMessage(`{"blocking":false,"findings":[{"severity":"nit","path":"","message":"m"}],"summary":"ok"}`)},
		"missing summary": {Kind: registry.DecisionFinal, Output: json.RawMessage(`{"blocking":false,"findings":[]}`)},
		"oversized":       {Kind: registry.DecisionFinal, Output: json.RawMessage(`{"blocking":false,"findings":[],"summary":"` + strings.Repeat("x", maxOpinionBytes) + `"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			model := textReviewer(cleanOpinion)
			model.answer = decision
			f := newFixture(model)
			_, err := f.executor.Execute(context.Background(), action(workspace().PatchDigest, testReviewModel))
			if err == nil {
				t.Fatal("execute succeeded, want the unusable opinion refused")
			}
			if strings.Contains(err.Error(), "patch looks fine") || strings.Contains(err.Error(), "nit") {
				t.Errorf("error %q quotes the model's answer", err)
			}
			if len(f.artifacts.texts) != 0 {
				t.Errorf("artifacts written = %d, want none for an unusable opinion", len(f.artifacts.texts))
			}
		})
	}
}

func TestExecute_ProviderFailure_ReturnsErrorWithoutArtifact(t *testing.T) {
	model := textReviewer(cleanOpinion)
	model.err = errors.New("upstream 503")
	f := newFixture(model)
	_, err := f.executor.Execute(context.Background(), action(workspace().PatchDigest, testReviewModel))
	if err == nil || !strings.Contains(err.Error(), testReviewModel) || !strings.Contains(err.Error(), "upstream 503") {
		t.Fatalf("err = %v, want the provider failure naming the review model", err)
	}
	if len(f.artifacts.texts) != 0 {
		t.Errorf("artifacts written = %d, want none", len(f.artifacts.texts))
	}
}

func TestExecute_RefusesBeforeAskingTheModel(t *testing.T) {
	ws := workspace()
	cases := map[string]struct {
		mutate func(*fixture, *registry.ToolAction)
		want   string
	}{
		"stale digest": {func(_ *fixture, a *registry.ToolAction) {
			a.Arguments = json.RawMessage(`{"patchDigest":"` + strings.Repeat("0", 64) + `"}`)
		}, "current patch"},
		"no workspace view":    {func(_ *fixture, a *registry.ToolAction) { a.Workspace = nil }, "workspace view"},
		"no review model":      {func(_ *fixture, a *registry.ToolAction) { a.ReviewModelID = "" }, "mock policy"},
		"unknown review model": {func(_ *fixture, a *registry.ToolAction) { a.ReviewModelID = "other.1" }, "not registered"},
		"model lacks structured output": {func(f *fixture, _ *registry.ToolAction) {
			f.model.capabilities = []string{domain.ModelCapabilityVisionInput}
		}, domain.ModelCapabilityStructuredDecision},
		"repository read fails": {func(f *fixture, _ *registry.ToolAction) {
			f.workspace.failed = errors.New("export failed")
		}, "repository context"},
		"patch over the bound": {func(_ *fixture, a *registry.ToolAction) {
			view := *a.Workspace
			view.Patch = strings.Repeat("+", maxPatchBytes+1)
			view.PatchDigest = domain.WorkspacePatchDigest(view.Patch)
			a.Workspace = &view
			a.Arguments = json.RawMessage(`{"patchDigest":"` + view.PatchDigest + `"}`)
		}, "review bound"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(textReviewer(cleanOpinion))
			a := action(ws.PatchDigest, testReviewModel)
			tc.mutate(f, &a)
			_, err := f.executor.Execute(context.Background(), a)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal mentioning %q", err, tc.want)
			}
			if len(f.model.requests) != 0 {
				t.Errorf("model asked %d times, want 0", len(f.model.requests))
			}
			if len(f.artifacts.texts) != 0 {
				t.Errorf("artifacts written = %d, want none", len(f.artifacts.texts))
			}
		})
	}
}

func TestExecute_UnconfiguredPorts_FailsBeforeAnything(t *testing.T) {
	_, err := New(Ports{}).Execute(context.Background(), action(workspace().PatchDigest, testReviewModel))
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v, want the configuration refusal", err)
	}
}

func TestExecute_ArtifactStoreFailure_FailsWithoutResult(t *testing.T) {
	f := newFixture(textReviewer(cleanOpinion))
	f.artifacts.failed = errors.New("disk full")
	_, err := f.executor.Execute(context.Background(), action(workspace().PatchDigest, testReviewModel))
	if err == nil || !strings.Contains(err.Error(), "disk full") || strings.Contains(err.Error(), "adapts every caller") {
		t.Fatalf("err = %v, want the store failure without the opinion text", err)
	}
}

func TestBlockingFindings_BoundsCountAndLength(t *testing.T) {
	var findings []finding
	for i := 0; i < maxBlockingFindings+3; i++ {
		findings = append(findings, finding{Severity: " blocking ", Path: strings.Repeat("p", maxFindingPathRunes+5), Message: strings.Repeat("m", maxFindingMessageRunes+5) + "\xff"})
	}
	findings = append(findings, finding{Severity: "major", Path: "x", Message: "kept out of the list"})
	out := blockingFindings(findings)
	if len(out) != maxBlockingFindings {
		t.Fatalf("len = %d, want %d", len(out), maxBlockingFindings)
	}
	if len([]rune(out[0].Path)) != maxFindingPathRunes || len([]rune(out[0].Message)) != maxFindingMessageRunes || out[0].Severity != severityBlocking {
		t.Errorf("finding = %+v, want bounded path and message with the blocking severity", out[0])
	}
}
