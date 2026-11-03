package changerequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testPatch      = "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -5 +5 @@\n-require github.com/golang-jwt/jwt v3.2.2+incompatible\n+require github.com/golang-jwt/jwt/v5 v5.3.1\n"
	testBaseCommit = "2706ca6fb4ddbe969e9acfa5464a347ff90c7774"
	testFactID     = "fact_verify_1"
)

// memoryArtifacts is an in-memory ArtifactTextStore that keeps every text it was handed.
type memoryArtifacts struct {
	mu     sync.Mutex
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
	m.mu.Lock()
	m.texts = append(m.texts, string(content))
	m.mu.Unlock()
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	id, _ := domain.ArtifactIDForSHA256(digest)
	return domain.ArtifactRef{ArtifactID: id, MediaType: domain.ArtifactMediaTypeTextPlain, SizeBytes: int64(len(content)), SHA256: digest}, nil
}

func workspace() *registry.WorkspaceView {
	return &registry.WorkspaceView{
		RepositoryID: "vuln-demo", BaseCommit: testBaseCommit, Patch: testPatch,
		PatchDigest: domain.WorkspacePatchDigest(testPatch), PatchSeq: 2,
		AllowedPaths: []string{"go.mod", "internal/auth"},
		DeployTarget: &domain.WorkspaceDeployTarget{Service: "token-service", Environment: "staging"},
	}
}

func passingFact(subject string) domain.ExecutionFact {
	verdict := true
	return domain.ExecutionFact{
		ID: testFactID, RunID: "run_1", AgentRunID: "ar_1", ToolAttemptID: "tool_attempt_verify", FactType: RequiredFactType,
		SubjectRef: subject, Binding: json.RawMessage(`{"resultDigest":"sha256:result","baseCommit":"` + testBaseCommit + `"}`), Verdict: &verdict,
	}
}

func argumentsFor(patchDigest string, basis []map[string]any) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{
		"patchDigest":   patchDigest,
		"summary":       "Upgrade golang-jwt to v5 to fix GO-2025-3553.",
		"impact":        "Token parsing errors change type; callers adapted.",
		"rollbackPlan":  "Redeploy the previous version.",
		"decisionBasis": basis,
	})
	return encoded
}

func defaultBasis() []map[string]any {
	return []map[string]any{
		{"statement": "The advisory names v5.2.2 as the fixed version.", "turnNo": 1, "manifestRef": "tool_attempt_read_advisory"},
		{"statement": "The project rule requires the latest mirrored major version.", "turnNo": 2, "manifestRef": "RULES.md"},
	}
}

func action(args json.RawMessage, facts ...domain.ExecutionFact) registry.ToolAction {
	return registry.ToolAction{
		RunID: "run_1", AgentRunID: "ar_1", TurnID: "turn_3", ActionID: "act_3", ToolName: ToolName, AttemptNo: 1,
		Arguments: args, Workspace: workspace(), RequiredFacts: facts,
	}
}

func TestRegistration_MetadataIsValidAndDeclaresTheContract(t *testing.T) {
	reg := Registration(&memoryArtifacts{})
	if err := reg.Metadata.Validate(); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	m := reg.Metadata
	if m.ExecutionKind != domain.ToolExecutionSync || m.SideEffect.Kind != domain.SideEffectNone || m.SideEffect.Idempotency != domain.IdempotencySafe {
		t.Errorf("execution %s side effect %+v, want SYNC with NONE + SAFE", m.ExecutionKind, m.SideEffect)
	}
	if !m.UsesWorkspace || m.WorkspacePatchDigestPath != "/patchDigest" || !m.ReturnUnmetRequirement {
		t.Errorf("metadata = %+v, want a workspace Tool naming its digest at /patchDigest that returns an unmet requirement", m)
	}
	if !m.DeclaresRecord(domain.ToolRecordChangeRequest) || m.DeclaresRecord(domain.ToolRecordWorkspacePatch) {
		t.Errorf("records = %v, want exactly CHANGE_REQUEST", m.Records)
	}
	if len(m.Requires) != 1 || m.Requires[0].FactType != RequiredFactType || m.Requires[0].SubjectArgument != "/patchDigest" ||
		m.Requires[0].RequireVerdict == nil || !*m.Requires[0].RequireVerdict {
		t.Errorf("requires = %+v, want one passing %s fact about /patchDigest", m.Requires, RequiredFactType)
	}
	if m.Produces != nil || m.Authorization != nil {
		t.Errorf("metadata produces %v authorization %v, want neither", m.Produces, m.Authorization)
	}
}

func TestExecute_RendersMarkdownArtifactAndChangeRequestRecord(t *testing.T) {
	store := &memoryArtifacts{}
	ws := workspace()
	got, err := New(store).Execute(context.Background(), action(argumentsFor(ws.PatchDigest, defaultBasis()), passingFact(ws.PatchDigest)))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultCompleted || got.Result == nil || got.Result.ChangeRequest == nil || len(got.Result.Artifacts) != 1 {
		t.Fatalf("result = %+v, want a completed result with one artifact and a change request", got)
	}
	if len(store.texts) != 1 {
		t.Fatalf("artifacts written = %d, want 1", len(store.texts))
	}
	content := store.texts[0]
	sum := sha256.Sum256([]byte(content))
	wantDigest := hex.EncodeToString(sum[:])

	cr := got.Result.ChangeRequest
	ref := got.Result.Artifacts[0]
	if cr.PatchDigest != ws.PatchDigest || cr.VerificationFactID != testFactID || cr.ContentDigest != wantDigest ||
		cr.ContentArtifact != ref || ref.SHA256 != wantDigest || ref.MediaType != domain.ArtifactMediaTypeTextPlain {
		t.Errorf("change request = %+v with artifact %+v, want the patch digest, the fact id and the content digest of the Markdown", cr, ref)
	}
	basis, err := domain.DecodeChangeRequestBasis(cr.DecisionBasis)
	if err != nil || len(basis) != 2 || basis[0].TurnNo != 1 || basis[1].ManifestRef != "RULES.md" {
		t.Errorf("decision basis = %s (%v), want the two cited items", cr.DecisionBasis, err)
	}

	var output map[string]any
	if err := json.Unmarshal(got.Result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["patchDigest"] != ws.PatchDigest || output["verificationFactId"] != testFactID || output["contentArtifactId"] != ref.ArtifactID ||
		output["contentDigest"] != wantDigest || output["basisCount"] != float64(2) {
		t.Errorf("output = %v, want the digest, fact, artifact id, content digest and basis count", output)
	}
	if _, present := output["changeRequestId"]; present {
		t.Errorf("output names a changeRequestId; the Runtime allocates it")
	}

	for _, want := range []string{
		"# Change request", "## Summary", "Upgrade golang-jwt to v5", "## Impact", "## Rollback plan",
		"`" + ws.PatchDigest + "`", "`" + testBaseCommit + "`", "`go.mod`", "token-service (staging)",
		"## Verification", "`" + testFactID + "`", "resultDigest: `sha256:result`", "Verdict: `passed`",
		"## Decision basis", "1. The advisory names v5.2.2 as the fixed version. (turn 1, manifest item `tool_attempt_read_advisory`)",
		"2. The project rule", "(turn 2, manifest item `RULES.md`)",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("markdown lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "+require github.com/golang-jwt/jwt/v5") {
		t.Errorf("markdown copies the patch text; the patch is named by digest only")
	}
}

func TestExecute_SameInputTwice_SameContentDigest(t *testing.T) {
	ws := workspace()
	run := func() *registry.ChangeRequest {
		got, err := New(&memoryArtifacts{}).Execute(context.Background(), action(argumentsFor(ws.PatchDigest, defaultBasis()), passingFact(ws.PatchDigest)))
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		return got.Result.ChangeRequest
	}
	first, second := run(), run()
	if first.ContentDigest != second.ContentDigest || first.ContentArtifact != second.ContentArtifact {
		t.Errorf("digests %s / %s, want the same content digest for the same request", first.ContentDigest, second.ContentDigest)
	}
}

func TestExecute_StaleDigest_FailsWithoutWriting(t *testing.T) {
	store := &memoryArtifacts{}
	stale := domain.WorkspacePatchDigest("older")
	_, err := New(store).Execute(context.Background(), action(argumentsFor(stale, defaultBasis()), passingFact(stale)))
	if err == nil || !strings.Contains(err.Error(), "current patch") {
		t.Fatalf("err = %v, want the stale digest refused", err)
	}
	if len(store.texts) != 0 {
		t.Errorf("an artifact was written for a refused call")
	}
}

func TestExecute_NoPassingVerificationFact_Fails(t *testing.T) {
	ws := workspace()
	failing := passingFact(ws.PatchDigest)
	verdict := false
	failing.Verdict = &verdict
	for name, facts := range map[string][]domain.ExecutionFact{
		"none":            nil,
		"false verdict":   {failing},
		"other subject":   {passingFact(domain.WorkspacePatchDigest("other"))},
		"other fact type": {func() domain.ExecutionFact { f := passingFact(ws.PatchDigest); f.FactType = "patch_reviewed"; return f }()},
	} {
		store := &memoryArtifacts{}
		_, err := New(store).Execute(context.Background(), action(argumentsFor(ws.PatchDigest, defaultBasis()), facts...))
		if err == nil || !strings.Contains(err.Error(), RequiredFactType) {
			t.Errorf("%s: err = %v, want the missing verification named", name, err)
		}
		if len(store.texts) != 0 {
			t.Errorf("%s: an artifact was written", name)
		}
	}
}

func TestExecute_InvalidDecisionBasis_Fails(t *testing.T) {
	ws := workspace()
	for name, basis := range map[string][]map[string]any{
		"empty":        {},
		"no statement": {{"statement": "", "turnNo": 1, "manifestRef": "x"}},
		"turn zero":    {{"statement": "s", "turnNo": 0, "manifestRef": "x"}},
		"no ref":       {{"statement": "s", "turnNo": 1, "manifestRef": ""}},
	} {
		_, err := New(&memoryArtifacts{}).Execute(context.Background(), action(argumentsFor(ws.PatchDigest, basis), passingFact(ws.PatchDigest)))
		if err == nil || !strings.Contains(err.Error(), "decision basis") {
			t.Errorf("%s: err = %v, want the decision basis refused", name, err)
		}
	}
}

func TestExecute_StoreFailure_DoesNotQuoteContent(t *testing.T) {
	ws := workspace()
	store := &memoryArtifacts{failed: errors.New("disk full")}
	_, err := New(store).Execute(context.Background(), action(argumentsFor(ws.PatchDigest, defaultBasis()), passingFact(ws.PatchDigest)))
	if err == nil || !strings.Contains(err.Error(), "disk full") || strings.Contains(err.Error(), "Upgrade golang-jwt") {
		t.Fatalf("err = %v, want the store failure without the content", err)
	}
}

func TestExecute_NoWorkspace_Fails(t *testing.T) {
	a := action(argumentsFor(workspace().PatchDigest, defaultBasis()))
	a.Workspace = nil
	if _, err := New(&memoryArtifacts{}).Execute(context.Background(), a); err == nil {
		t.Fatal("want an error for a call without a workspace view")
	}
}

const testReviewFactID = "fact_review_1"

func reviewFact(subject string, passed bool) domain.ExecutionFact {
	return domain.ExecutionFact{
		ID: testReviewFactID, RunID: "run_1", AgentRunID: "ar_1", ToolAttemptID: "tool_attempt_review", FactType: ReviewFactType,
		SubjectRef: subject, Binding: json.RawMessage(`{"reviewModelId":"text-reviewer.1","opinionArtifactId":"art_opinion"}`), Verdict: &passed,
	}
}

func argumentsWithReview(patchDigest string, unresolved []string) json.RawMessage {
	var args map[string]any
	_ = json.Unmarshal(argumentsFor(patchDigest, defaultBasis()), &args)
	args["reviewFactId"] = testReviewFactID
	if unresolved != nil {
		args["unresolvedFindings"] = unresolved
	}
	encoded, _ := json.Marshal(args)
	return encoded
}

func TestExecute_CitedReview_RendersReviewSectionAndNamesTheFact(t *testing.T) {
	store := &memoryArtifacts{}
	ws := workspace()
	unresolved := []string{"The old require line is still present; it is removed in a follow-up.", "A changelog entry is missing."}
	got, err := New(store).Execute(context.Background(), action(argumentsWithReview(ws.PatchDigest, unresolved), passingFact(ws.PatchDigest), reviewFact(ws.PatchDigest, false)))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Result.ChangeRequest.ReviewFactID != testReviewFactID {
		t.Errorf("record review fact = %q, want %s", got.Result.ChangeRequest.ReviewFactID, testReviewFactID)
	}
	var output map[string]any
	_ = json.Unmarshal(got.Result.Output, &output)
	if output["reviewFactId"] != testReviewFactID {
		t.Errorf("output = %v, want it to name the review fact", output)
	}
	content := store.texts[0]
	for _, want := range []string{
		"## Review", "Fact: `" + testReviewFactID + "`", "Fact type: `" + ReviewFactType + "`", "Blocking findings: `yes`",
		"opinionArtifactId: `art_opinion`", "reviewModelId: `text-reviewer.1`", "Unresolved findings:", "- " + unresolved[0], "- " + unresolved[1],
	} {
		if !strings.Contains(content, want) {
			t.Errorf("markdown lacks %q:\n%s", want, content)
		}
	}
	if strings.Index(content, "## Review") > strings.Index(content, "## Decision basis") {
		t.Errorf("the review section should precede the decision basis:\n%s", content)
	}
}

func TestExecute_CleanReviewWithoutUnresolvedFindings_RendersNone(t *testing.T) {
	store := &memoryArtifacts{}
	ws := workspace()
	if _, err := New(store).Execute(context.Background(), action(argumentsWithReview(ws.PatchDigest, nil), passingFact(ws.PatchDigest), reviewFact(ws.PatchDigest, true))); err != nil {
		t.Fatalf("execute: %v", err)
	}
	content := store.texts[0]
	for _, want := range []string{"Blocking findings: `none`", "Unresolved findings:\n\n- none\n"} {
		if !strings.Contains(content, want) {
			t.Errorf("markdown lacks %q:\n%s", want, content)
		}
	}
}

func TestExecute_NoReviewCited_RendersUnattachedReviewAndOmitsTheField(t *testing.T) {
	store := &memoryArtifacts{}
	ws := workspace()
	got, err := New(store).Execute(context.Background(), action(argumentsFor(ws.PatchDigest, defaultBasis()), passingFact(ws.PatchDigest)))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Result.ChangeRequest.ReviewFactID != "" || strings.Contains(string(got.Result.Output), "reviewFactId") {
		t.Errorf("result = %+v / %s, want no review fact", got.Result.ChangeRequest, got.Result.Output)
	}
	if !strings.Contains(store.texts[0], "## Review\n\nNo independent review is attached") {
		t.Errorf("markdown lacks the unattached review note:\n%s", store.texts[0])
	}
}

func TestExecute_CitedReviewNotCarried_Refused(t *testing.T) {
	ws := workspace()
	cases := map[string][]domain.ExecutionFact{
		"no review fact":          {passingFact(ws.PatchDigest)},
		"review of another patch": {passingFact(ws.PatchDigest), reviewFact(strings.Repeat("0", 64), true)},
		"review under another id": {passingFact(ws.PatchDigest), func() domain.ExecutionFact { f := reviewFact(ws.PatchDigest, true); f.ID = "fact_other"; return f }()},
	}
	for name, facts := range cases {
		t.Run(name, func(t *testing.T) {
			store := &memoryArtifacts{}
			_, err := New(store).Execute(context.Background(), action(argumentsWithReview(ws.PatchDigest, nil), facts...))
			if err == nil || !strings.Contains(err.Error(), ReviewFactType) {
				t.Fatalf("err = %v, want the missing review refused", err)
			}
			if len(store.texts) != 0 {
				t.Errorf("artifacts written = %d, want none", len(store.texts))
			}
		})
	}
}

func TestExecute_UnresolvedFindingsOutOfBounds_Refused(t *testing.T) {
	ws := workspace()
	tooMany := make([]string, MaxUnresolvedFindings+1)
	for i := range tooMany {
		tooMany[i] = "finding"
	}
	for name, unresolved := range map[string][]string{
		"too many": tooMany,
		"too long": {strings.Repeat("x", MaxUnresolvedFindingRunes+1)},
		"blank":    {"   "},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(&memoryArtifacts{}).Execute(context.Background(), action(argumentsWithReview(ws.PatchDigest, unresolved), passingFact(ws.PatchDigest), reviewFact(ws.PatchDigest, true)))
			if err == nil || !strings.Contains(err.Error(), "unresolved finding") {
				t.Fatalf("err = %v, want the findings refused", err)
			}
		})
	}
}
