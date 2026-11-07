package reviewartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testRunID       = "run_1"
	testReviewModel = "text-review"
	testRequirement = "a 45 second clip on why the method needs a large sample"

	transcriptID = "art_transcript"
	timelineID   = "art_timeline"

	// fiveLines is a transcript whose fourth sentence is cut short by the fifth: S4 alone
	// says the method does not work, S5 restores the condition.
	fiveLines = "S1\t0.000\t2.500\tHello and welcome to the show.\n" +
		"S2\t2.500\t6.000\tToday we talk about sampling.\n" +
		"S3\t6.000\t9.000\tMany teams get this wrong.\n" +
		"S4\t9.000\t12.000\tThis approach does not work\n" +
		"S5\t12.000\t15.000\tunless the sample is large enough.\n"
)

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// timelineFor is the canonical timeline the media service produces for the given
// sentence ranges over the fiveLines transcript: sorted keys, no whitespace.
func timelineFor(transcriptSha string, ranges ...[2]string) string {
	segments := make([]string, 0, len(ranges))
	for _, r := range ranges {
		segments = append(segments, `{"endMs":12000,"from":"`+r[0]+`","startMs":9000,"text":"selected","to":"`+r[1]+`"}`)
	}
	return `{"durationMs":3000,"ruleVersion":"v1","segments":[` + strings.Join(segments, ",") + `],"sourceId":"src_1","sourceSha256":"` +
		strings.Repeat("1", 64) + `","targetDurationMs":45000,"transcriptSha256":"` + transcriptSha + `"}`
}

// fakeReader serves text artifacts keyed by Run, the way the Run-scoped artifact read
// does: an artifact another Run produced is not found.
type fakeReader struct {
	texts map[string]string // runID + "/" + artifactID -> content
	media map[string]string // artifactID -> media type override
	fail  map[string]error  // artifactID -> error the open fails with
}

func (f fakeReader) OpenText(_ context.Context, runID, artifactID string) (domain.Artifact, io.ReadCloser, error) {
	if err, ok := f.fail[artifactID]; ok {
		return domain.Artifact{}, nil, err
	}
	content, ok := f.texts[runID+"/"+artifactID]
	if !ok {
		return domain.Artifact{}, nil, fmt.Errorf("artifact %s: %w", artifactID, domain.ErrNotFound)
	}
	mediaType := domain.ArtifactMediaTypeTextPlain
	if override, ok := f.media[artifactID]; ok {
		mediaType = override
	}
	return domain.Artifact{ArtifactID: artifactID, MediaType: mediaType, SizeBytes: int64(len(content)), SHA256: digestOf(content)},
		io.NopCloser(strings.NewReader(content)), nil
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

const (
	passedOpinion = `{"passed":true,"issues":[]}`
	failedOpinion = `{"passed":false,"issues":[{"type":"MEANING_DISTORTED","sentences":["S4"],"reason":"S4 alone drops the condition S5 adds."},` +
		`{"type":"REQUIREMENT_NOT_MET","sentences":["S4"],"reason":"The clip never says why a large sample matters."}]}`
)

type fixture struct {
	model    *fakeReviewModel
	reader   *fakeReader
	executor *Executor
}

// newFixture serves the fiveLines transcript and a timeline over the given ranges to
// Run run_1, and reviews with model.
func newFixture(model *fakeReviewModel, ranges ...[2]string) *fixture {
	if len(ranges) == 0 {
		ranges = [][2]string{{"S4", "S4"}}
	}
	f := &fixture{model: model, reader: &fakeReader{texts: map[string]string{
		testRunID + "/" + transcriptID: fiveLines,
		testRunID + "/" + timelineID:   timelineFor(digestOf(fiveLines), ranges...),
	}}}
	f.executor = New(Ports{Models: model, Materials: f.reader})
	return f
}

func (f *fixture) timeline() string {
	return f.reader.texts[testRunID+"/"+timelineID]
}

func action(timelineArtifactID, transcriptArtifactID, reviewModelID string) registry.ToolAction {
	arguments, _ := json.Marshal(map[string]string{
		"timelineArtifactId": timelineArtifactID, "transcriptArtifactId": transcriptArtifactID, "requirement": testRequirement,
	})
	return registry.ToolAction{
		RunID: testRunID, AgentRunID: "ar_1", TurnID: "turn_3", ActionID: "act_3", ToolName: ToolName, AttemptNo: 1,
		Arguments: arguments, ReviewModelID: reviewModelID,
	}
}

func (f *fixture) execute(t *testing.T) (registry.ToolExecutionResult, error) {
	t.Helper()
	return f.executor.Execute(context.Background(), action(timelineID, transcriptID, testReviewModel))
}

// review runs one call, checks the result against the registered OutputSchema and
// decodes it.
func (f *fixture) review(t *testing.T) (result, registry.ToolResult) {
	t.Helper()
	got, err := f.execute(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultCompleted || got.Result == nil {
		t.Fatalf("result = %+v, want a completed result", got)
	}
	schema, err := registry.CompileSchema(Registration(Ports{}).Metadata.OutputSchema)
	if err != nil {
		t.Fatalf("compile output schema: %v", err)
	}
	var value any
	if err := json.Unmarshal(got.Result.Output, &value); err != nil {
		t.Fatalf("decode output %s: %v", got.Result.Output, err)
	}
	if err := registry.ValidateValue(schema, value); err != nil {
		t.Fatalf("result %s does not satisfy the OutputSchema: %v", got.Result.Output, err)
	}
	var out result
	if err := json.Unmarshal(got.Result.Output, &out); err != nil {
		t.Fatalf("decode output %s: %v", got.Result.Output, err)
	}
	return out, *got.Result
}

// userText is the one text part of the one user message the model was asked with.
func (f *fixture) userText(t *testing.T) string {
	t.Helper()
	if len(f.model.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(f.model.requests))
	}
	request := f.model.requests[0]
	if len(request.Messages) != 1 || len(request.Messages[0].Parts) != 1 || request.Messages[0].Parts[0].Type != registry.ContentPartText {
		t.Fatalf("messages = %+v, want one user message with one text part", request.Messages)
	}
	return request.Messages[0].Parts[0].Text
}

func TestRegistration_ReviewProducingContentReviewedFact_RegistersCleanly(t *testing.T) {
	reg := Registration(Ports{})
	if err := reg.Metadata.Validate(); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	m := reg.Metadata
	if m.Name != ToolName || m.ExecutionKind != domain.ToolExecutionSync || m.SideEffect.Kind != domain.SideEffectNone || m.SideEffect.Idempotency != domain.IdempotencySafe {
		t.Errorf("metadata = %+v, want SYNC %s with NONE + SAFE", m, ToolName)
	}
	if !m.UsesReviewModel || !m.RequiresReviewModel || m.ReviewModelCapability != domain.ModelCapabilityStructuredDecision {
		t.Errorf("metadata = %+v, want a required independent review model declaring %s", m, domain.ModelCapabilityStructuredDecision)
	}
	produces := m.Produces
	if produces == nil || produces.FactType != FactType || produces.VerdictPointer == nil || *produces.VerdictPointer != (domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"}) ||
		produces.SubjectPointer != (domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/timelineSha256"}) ||
		len(produces.BindArguments) != 1 || produces.BindArguments["policyVersion"] != (domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/policyVersion"}) ||
		produces.BasisFactType != "" {
		t.Errorf("produces = %+v, want %s about the timeline digest bound to the policy version with the verdict at /passed", produces, FactType)
	}
	if len(m.CountedFailedFacts) != 1 || m.CountedFailedFacts[0] != FactType {
		t.Errorf("countedFailedFacts = %v, want [%s]", m.CountedFailedFacts, FactType)
	}
	if len(m.Requires) != 0 || m.Authorization != nil || m.CountsTowardGenerationLimit || m.UsesWorkspace || m.MaterialPointer != "" || len(m.Records) != 0 {
		t.Errorf("metadata = %+v, want an ungated, uncounted review without workspace, records or material declaration", m)
	}
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.ValidateFactDeclarations([]domain.ToolMetadata{m}, nil); err != nil {
		t.Fatalf("fact declarations: %v", err)
	}
}

func TestExecute_ModelPasses_ResultPassesWithEmptyIssuesAndCost(t *testing.T) {
	f := newFixture(textReviewer(passedOpinion))
	f.model.cost = &domain.Cost{Amount: "0.0042", Currency: domain.CurrencyUSD, Source: "upstream"}
	out, result := f.review(t)
	if !out.Passed || out.PolicyVersion != PolicyVersion || out.TimelineSha256 != digestOf(f.timeline()) || len(out.Issues) != 0 {
		t.Errorf("output = %+v, want a passing review of the timeline digest under %s", out, PolicyVersion)
	}
	if !strings.Contains(string(result.Output), `"issues":[]`) {
		t.Errorf("output %s should encode the empty issue list as a list", result.Output)
	}
	if result.Cost == nil || result.Cost.Amount != "0.0042" || result.Cost.Currency != domain.CurrencyUSD || result.Cost.Source != testReviewModel {
		t.Errorf("cost = %+v, want the reported charge attributed to the review model", result.Cost)
	}
	if len(result.Artifacts) != 0 || result.WorkspacePatch != nil || result.ChangeRequest != nil {
		t.Errorf("result declares artifacts %v or records, want none", result.Artifacts)
	}
}

// A review that does not pass is a successful call: the verdict is false and the issues
// come back to the Agent.
func TestExecute_ModelFails_SuccessfulResultWithIssues(t *testing.T) {
	f := newFixture(textReviewer(failedOpinion))
	out, result := f.review(t)
	if out.Passed || len(out.Issues) != 2 {
		t.Fatalf("output = %+v, want a failed review with two issues", out)
	}
	if out.Issues[0].Type != IssueMeaningDistorted || len(out.Issues[0].Sentences) != 1 || out.Issues[0].Sentences[0] != "S4" || out.Issues[0].Reason != "S4 alone drops the condition S5 adds." {
		t.Errorf("first issue = %+v, want the distortion of S4", out.Issues[0])
	}
	if out.Issues[1].Type != IssueRequirementNotMet {
		t.Errorf("second issue = %+v, want the unmet requirement", out.Issues[1])
	}
	if result.Cost != nil {
		t.Errorf("cost = %+v, want none when the model reported none", result.Cost)
	}
}

func TestExecute_ModelRequest_CarriesRequirementPolicyAndSelectionWithContext(t *testing.T) {
	f := newFixture(textReviewer(passedOpinion), [2]string{"S3", "S4"})
	f.review(t)
	request := f.model.requests[0]
	if request.ModelID != testReviewModel || request.Instructions != reviewInstructions || string(request.FinalOutputSchema) != opinionSchema || len(request.DecisionSchema) == 0 {
		t.Errorf("request = %+v, want the review model, the fixed instructions and the opinion schema", request)
	}
	text := f.userText(t)
	for _, want := range []string{
		"Policy: " + PolicyVersion,
		"Requirement: " + testRequirement,
		"Segment 1: S3 to S4",
		"  S1\tHello and welcome to the show.",
		"  S2\tToday we talk about sampling.",
		"> S3\tMany teams get this wrong.",
		"> S4\tThis approach does not work",
		"  S5\tunless the sample is large enough.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("user message lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "0.000") || strings.Contains(text, "9.000") {
		t.Errorf("user message carries sentence times, which the review does not need:\n%s", text)
	}
}

// Two sentences of context on each side, clamped at the transcript's edges; a selection
// at the start has no sentence before it, one at the end none after it.
func TestExcerpt_ContextWindowClampedAtTranscriptEdges(t *testing.T) {
	sentences := []sentence{{ID: "S1", Text: "a"}, {ID: "S2", Text: "b"}, {ID: "S3", Text: "c"}, {ID: "S4", Text: "d"}, {ID: "S5", Text: "e"}}
	cases := map[string]struct {
		ranges   [][2]string
		wantIDs  []string
		selected []bool
	}{
		"start":      {[][2]string{{"S1", "S2"}}, []string{"S1", "S2", "S3", "S4"}, []bool{true, true, false, false}},
		"end":        {[][2]string{{"S4", "S5"}}, []string{"S2", "S3", "S4", "S5"}, []bool{false, false, true, true}},
		"middle":     {[][2]string{{"S3", "S3"}}, []string{"S1", "S2", "S3", "S4", "S5"}, []bool{false, false, true, false, false}},
		"whole":      {[][2]string{{"S1", "S5"}}, []string{"S1", "S2", "S3", "S4", "S5"}, []bool{true, true, true, true, true}},
		"overlap":    {[][2]string{{"S1", "S1"}, {"S5", "S5"}}, []string{"S1", "S2", "S3", "S4", "S5"}, []bool{true, false, false, false, true}},
		"two apart":  {[][2]string{{"S1", "S1"}, {"S4", "S4"}}, []string{"S1", "S2", "S3", "S4", "S5"}, []bool{true, false, false, true, false}},
		"same twice": {[][2]string{{"S2", "S2"}, {"S2", "S2"}}, []string{"S1", "S2", "S3", "S4"}, []bool{false, true, false, false}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			segments := make([]timelineSegment, 0, len(tc.ranges))
			for _, r := range tc.ranges {
				segments = append(segments, timelineSegment{From: r[0], To: r[1]})
			}
			lines, err := excerpt(sentences, segments)
			if err != nil {
				t.Fatalf("excerpt: %v", err)
			}
			if len(lines) != len(tc.wantIDs) {
				t.Fatalf("excerpt = %+v, want ids %v", lines, tc.wantIDs)
			}
			for i, line := range lines {
				if line.ID != tc.wantIDs[i] || line.Selected != tc.selected[i] {
					t.Errorf("line %d = %+v, want %s selected=%t", i, line, tc.wantIDs[i], tc.selected[i])
				}
			}
		})
	}
}

// A gap between two excerpt windows is marked, so the model does not read two distant
// sentences as adjacent.
func TestExecute_SeparateSegments_GapMarkedBetweenWindows(t *testing.T) {
	ten := ""
	for i := 1; i <= 10; i++ {
		ten += fmt.Sprintf("S%d\t%d.000\t%d.000\tsentence %d.\n", i, i, i+1, i)
	}
	f := newFixture(textReviewer(passedOpinion))
	f.reader.texts[testRunID+"/"+transcriptID] = ten
	f.reader.texts[testRunID+"/"+timelineID] = timelineFor(digestOf(ten), [2]string{"S1", "S1"}, [2]string{"S10", "S10"})
	f.review(t)
	text := f.userText(t)
	if !strings.Contains(text, "> S1\tsentence 1.\n  S2\tsentence 2.\n  S3\tsentence 3.\n  [...]\n  S8\tsentence 8.\n  S9\tsentence 9.\n> S10\tsentence 10.\n") {
		t.Errorf("user message does not mark the gap between the windows:\n%s", text)
	}
	if strings.Contains(text, "sentence 5.") {
		t.Errorf("user message carries a sentence outside every window:\n%s", text)
	}
}

func TestExecute_IssuesBoundedToThreeWithTrimmedFields(t *testing.T) {
	issues := make([]string, 0, maxIssues+2)
	for i := 0; i < maxIssues+2; i++ {
		issues = append(issues, fmt.Sprintf(`{"type":"REQUIREMENT_NOT_MET","sentences":[" S%d ","%s"],"reason":" %s "}`, i+1, strings.Repeat("s", maxSentenceIDRunes+5), strings.Repeat("r", maxReasonRunes+5)))
	}
	f := newFixture(textReviewer(`{"passed":false,"issues":[` + strings.Join(issues, ",") + `]}`))
	out, _ := f.review(t)
	if out.Passed || len(out.Issues) != maxIssues {
		t.Fatalf("output = %+v, want a failed review with %d issues", out, maxIssues)
	}
	first := out.Issues[0]
	if first.Sentences[0] != "S1" || len([]rune(first.Sentences[1])) != maxSentenceIDRunes || len([]rune(first.Reason)) != maxReasonRunes {
		t.Errorf("first issue = %+v, want trimmed sentence ids and reason within the bounds", first)
	}
}

func TestExecute_UnusableOpinion_FailsWithoutQuotingIt(t *testing.T) {
	for name, decision := range map[string]registry.ModelDecision{
		"tool call":     {Kind: registry.DecisionToolCall},
		"not JSON":      {Kind: registry.DecisionFinal, Output: json.RawMessage(`the clip reads fine`)},
		"wrong shape":   {Kind: registry.DecisionFinal, Output: json.RawMessage(`{"passed":"yes","issues":[]}`)},
		"bad type":      {Kind: registry.DecisionFinal, Output: json.RawMessage(`{"passed":false,"issues":[{"type":"nitpick","sentences":["S4"],"reason":"r"}]}`)},
		"no issues key": {Kind: registry.DecisionFinal, Output: json.RawMessage(`{"passed":true}`)},
		"oversized":     {Kind: registry.DecisionFinal, Output: json.RawMessage(`{"passed":true,"issues":[],"x":"` + strings.Repeat("x", maxOpinionBytes) + `"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			model := textReviewer(passedOpinion)
			model.answer = decision
			f := newFixture(model)
			_, err := f.execute(t)
			if err == nil {
				t.Fatal("execute succeeded, want the unusable opinion refused")
			}
			if !strings.Contains(err.Error(), testReviewModel) || !strings.Contains(err.Error(), FailureOpinionInvalid) {
				t.Errorf("err = %v, want it naming the review model and %s", err, FailureOpinionInvalid)
			}
			if strings.Contains(err.Error(), "reads fine") || strings.Contains(err.Error(), "nitpick") {
				t.Errorf("error %q quotes the model's answer", err)
			}
		})
	}
}

// The timeline names a transcript digest that is not the transcript artifact's content:
// the two do not belong together, so no review is asked.
func TestExecute_TranscriptDigestMismatch_FailsBeforeAskingTheModel(t *testing.T) {
	f := newFixture(textReviewer(passedOpinion))
	f.reader.texts[testRunID+"/"+timelineID] = timelineFor(strings.Repeat("a", 64), [2]string{"S4", "S4"})
	_, err := f.execute(t)
	if err == nil || !strings.Contains(err.Error(), FailureTranscriptMismatch) {
		t.Fatalf("err = %v, want %s", err, FailureTranscriptMismatch)
	}
	if len(f.model.requests) != 0 {
		t.Errorf("model asked %d times, want 0", len(f.model.requests))
	}
}

// An artifact that does not exist, belongs to another Run or is not text is a Tool
// failure naming the artifact id: the ids come from committed results the Agent already
// holds, and a successful result must carry the timeline digest the fact is about.
func TestExecute_ArtifactNotReadable_FailsWithStableCode(t *testing.T) {
	cases := map[string]func(f *fixture, a *registry.ToolAction){
		"timeline missing": func(_ *fixture, a *registry.ToolAction) {
			a.Arguments = action("art_other", transcriptID, testReviewModel).Arguments
		},
		"transcript missing": func(_ *fixture, a *registry.ToolAction) {
			a.Arguments = action(timelineID, "art_other", testReviewModel).Arguments
		},
		"other run": func(_ *fixture, a *registry.ToolAction) { a.RunID = "run_2" },
		"timeline not text": func(f *fixture, _ *registry.ToolAction) {
			f.reader.media = map[string]string{timelineID: "image/png"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(textReviewer(passedOpinion))
			a := action(timelineID, transcriptID, testReviewModel)
			mutate(f, &a)
			_, err := f.executor.Execute(context.Background(), a)
			if err == nil || !strings.Contains(err.Error(), FailureArtifactNotFound) {
				t.Fatalf("err = %v, want %s", err, FailureArtifactNotFound)
			}
			if strings.Contains(err.Error(), "welcome") || strings.Contains(err.Error(), "segments") {
				t.Errorf("error %q carries material", err)
			}
			if len(f.model.requests) != 0 {
				t.Errorf("model asked %d times, want 0", len(f.model.requests))
			}
		})
	}
}

func TestExecute_RefusesBeforeAskingTheModel(t *testing.T) {
	cases := map[string]struct {
		mutate func(*fixture, *registry.ToolAction)
		want   string
	}{
		"no review model":      {func(_ *fixture, a *registry.ToolAction) { a.ReviewModelID = "" }, "mock policy"},
		"unknown review model": {func(_ *fixture, a *registry.ToolAction) { a.ReviewModelID = "other.1" }, "not registered"},
		"model lacks structured output": {func(f *fixture, _ *registry.ToolAction) {
			f.model.capabilities = []string{domain.ModelCapabilityVisionInput}
		}, domain.ModelCapabilityStructuredDecision},
		"empty arguments": {func(_ *fixture, a *registry.ToolAction) { a.Arguments = json.RawMessage(`{}`) }, "requirement"},
		"timeline not JSON": {func(f *fixture, _ *registry.ToolAction) {
			f.reader.texts[testRunID+"/"+timelineID] = "not a timeline"
		}, FailureTimelineInvalid},
		"timeline without segments": {func(f *fixture, _ *registry.ToolAction) {
			f.reader.texts[testRunID+"/"+timelineID] = `{"segments":[],"transcriptSha256":"` + digestOf(fiveLines) + `"}`
		}, FailureTimelineInvalid},
		"timeline names unknown sentence": {func(f *fixture, _ *registry.ToolAction) {
			f.reader.texts[testRunID+"/"+timelineID] = timelineFor(digestOf(fiveLines), [2]string{"S4", "S9"})
		}, FailureTimelineInvalid},
		"timeline range reversed": {func(f *fixture, _ *registry.ToolAction) {
			f.reader.texts[testRunID+"/"+timelineID] = timelineFor(digestOf(fiveLines), [2]string{"S4", "S2"})
		}, FailureTimelineInvalid},
		"timeline over the bound": {func(f *fixture, _ *registry.ToolAction) {
			f.reader.texts[testRunID+"/"+timelineID] = `{"segments":[],"pad":"` + strings.Repeat("x", maxTimelineBytes) + `"}`
		}, FailureInputTooLarge},
		"transcript malformed line": {func(f *fixture, _ *registry.ToolAction) {
			malformed := "S1\t0.000\t2.500\tHello.\nno tabs here\n"
			f.reader.texts[testRunID+"/"+transcriptID] = malformed
			f.reader.texts[testRunID+"/"+timelineID] = timelineFor(digestOf(malformed), [2]string{"S1", "S1"})
		}, FailureTranscriptInvalid},
		"transcript over the bound": {func(f *fixture, _ *registry.ToolAction) {
			huge := "S1\t0.000\t2.500\t" + strings.Repeat("x", maxTranscriptBytes) + "\n"
			f.reader.texts[testRunID+"/"+transcriptID] = huge
			f.reader.texts[testRunID+"/"+timelineID] = timelineFor(digestOf(huge), [2]string{"S1", "S1"})
		}, FailureInputTooLarge},
		"transcript read fails": {func(f *fixture, _ *registry.ToolAction) {
			f.reader.fail = map[string]error{transcriptID: errors.New("disk gone")}
		}, "disk gone"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(textReviewer(passedOpinion))
			a := action(timelineID, transcriptID, testReviewModel)
			tc.mutate(f, &a)
			_, err := f.executor.Execute(context.Background(), a)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal mentioning %q", err, tc.want)
			}
			for _, material := range []string{"welcome", "not a timeline", "no tabs here", "does not work"} {
				if strings.Contains(err.Error(), material) {
					t.Errorf("error %q carries material", err)
				}
			}
			if len(f.model.requests) != 0 {
				t.Errorf("model asked %d times, want 0", len(f.model.requests))
			}
		})
	}
}

func TestExecute_ProviderFailure_ReturnsErrorNamingTheModel(t *testing.T) {
	model := textReviewer(passedOpinion)
	model.err = errors.New("upstream 503")
	f := newFixture(model)
	_, err := f.execute(t)
	if err == nil || !strings.Contains(err.Error(), testReviewModel) || !strings.Contains(err.Error(), "upstream 503") {
		t.Fatalf("err = %v, want the provider failure naming the review model", err)
	}
}

func TestExecute_UnconfiguredPorts_FailsBeforeAnything(t *testing.T) {
	_, err := New(Ports{}).Execute(context.Background(), action(timelineID, transcriptID, testReviewModel))
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v, want the configuration refusal", err)
	}
}
