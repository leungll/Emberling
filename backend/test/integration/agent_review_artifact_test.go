//go:build integration

// review_artifact end to end against PostgreSQL, driven by a scripted review model: a
// real Agent Run keeps a transcript and a timeline as text artifacts of its own Run, then
// reviews the timeline; the committed result records a content_reviewed fact about the
// timeline digest, bound to the policy version, with the verdict the model gave, and the
// Attempt carries the review model's charge. A review that does not pass is a successful
// call whose issues reach the Agent and whose fact says false; a timeline cut from
// another transcript fails the Attempt without a fact. The transcript text reaches the
// review model's request and nothing else.
//
// They share the agentHarness of agent_loop_test.go, the fact fixtures of
// agent_action_facts_test.go and the scripted review model of agent_review_patch_test.go.
package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/reviewartifact"
)

const (
	// keepTextTool is a synchronous test Tool that keeps the given text as a text artifact
	// of the Run, standing in for the transcript node and the timeline check, so the
	// review reads artifacts a committed result of the same Run declared.
	keepTextTool = "keep_text"

	reviewTranscript = "S1\t0.000\t2.500\tHello and welcome to the show.\n" +
		"S2\t2.500\t6.000\tToday we talk about sampling.\n" +
		"S3\t6.000\t9.000\tThis approach does not work\n" +
		"S4\t9.000\t12.000\tunless the sample is large enough.\n" +
		"S5\t12.000\t15.000\tThank you for watching.\n"
	reviewRequirement = "a 30 second clip on why the sample must be large"

	reviewPassedOpinion = `{"passed":true,"issues":[]}`
	reviewFailedOpinion = `{"passed":false,"issues":[{"type":"MEANING_DISTORTED","sentences":["S3"],"reason":"S3 alone drops the condition S4 adds."}]}`
)

// reviewTimeline is the canonical timeline over the given transcript digest selecting S3
// alone.
func reviewTimeline(transcriptSha string) string {
	return `{"durationMs":3000,"ruleVersion":"v1","segments":[{"endMs":9000,"from":"S3","startMs":6000,"text":"This approach does not work","to":"S3"}],` +
		`"sourceId":"src_review","sourceSha256":"` + strings.Repeat("1", 64) + `","targetDurationMs":30000,"transcriptSha256":"` + transcriptSha + `"}`
}

// keepTextExecutor writes the text it is given to the artifact store and declares it.
type keepTextExecutor struct {
	artifacts *asset.ArtifactStore
}

func (e keepTextExecutor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(action.Arguments, &args); err != nil {
		return registry.ToolExecutionResult{}, err
	}
	ref, err := e.artifacts.WriteText(ctx, strings.NewReader(args.Text))
	if err != nil {
		return registry.ToolExecutionResult{}, err
	}
	output, _ := json.Marshal(map[string]string{"artifactId": ref.ArtifactID, "sha256": ref.SHA256})
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output, Artifacts: []domain.ArtifactRef{ref}}}, nil
}

func keepTextRegistration(artifacts *asset.ArtifactStore) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          keepTextTool,
			Description:   "Keep text as an artifact of this run",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"artifactId":{"type":"string"},"sha256":{"type":"string"}},"required":["artifactId","sha256"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
		},
		Executor: keepTextExecutor{artifacts: artifacts},
	}
}

// reviewArtifactFixture is one Backend with keep_text and review_artifact registered, the
// review reading through the real Run-scoped artifact read and reviewing with the
// scripted model the Definition selects as reviewModelId.
type reviewArtifactFixture struct {
	h         *agentHarness
	model     *scriptedReviewModel
	artifacts *asset.ArtifactStore
	run       domain.Run
	outcome   service.AdvanceOutcome
}

func newReviewArtifactFixture(t *testing.T, model *scriptedReviewModel) *reviewArtifactFixture {
	t.Helper()
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	materials := &lateMaterials{}
	h := newAgentHarness(t, agentHarnessOptions{
		Tools: []registry.ToolRegistration{
			keepTextRegistration(artifacts),
			reviewartifact.Registration(reviewartifact.Ports{Models: model, Materials: materials}),
		},
		ModelProviders: []registry.ModelProvider{model},
	})
	materials.svc = service.NewArtifactService(service.Deps{UoW: h.uow, ArtifactContent: artifacts})
	return &reviewArtifactFixture{h: h, model: model, artifacts: artifacts}
}

func artifactIDOf(t *testing.T, text string) string {
	t.Helper()
	id, err := domain.ArtifactIDForSHA256(sha256Hex([]byte(text)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// start keeps the transcript and the timeline, reviews the timeline against the
// transcript, and lets the model finish; the Run runs to its end.
func (f *reviewArtifactFixture) start(t *testing.T, workflowID, transcript, timeline string) {
	t.Helper()
	calls := []mockmodel.Scenario{
		wsCall(keepTextTool, map[string]any{"text": transcript}),
		wsCall(keepTextTool, map[string]any{"text": timeline}),
		wsCall(reviewartifact.ToolName, map[string]any{
			"timelineArtifactId": artifactIDOf(t, timeline), "transcriptArtifactId": artifactIDOf(t, transcript), "requirement": reviewRequirement,
		}),
	}
	factScriptToolCalls(f.h, calls...)
	def := f.h.saveDefinition(withAgentConfig(t, factDefinition(workflowID, keepTextTool, reviewartifact.ToolName), "node_agent",
		map[string]any{"reviewModelId": reviewTextModelID}))
	f.run = f.h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	f.outcome = f.h.claimAgentNode(f.run.ID)
	f.h.execute(f.outcome)
	// Drive the nodes after the Agent to the Run's terminal status.
	for i := 0; i < 10; i++ {
		outcome := f.h.advance(f.run.ID)
		if !outcome.Claimed {
			break
		}
		f.h.execute(outcome)
	}
}

// assertReviewRecorded proves the review of Turn 3 committed: the Attempt SUCCEEDED with
// the review model's charge; exactly one content_reviewed fact, about the timeline digest,
// bound to the policy version alone, with the given verdict; the tool message carries the
// verdict and the issues; and the transcript text reached no Event, fact or Attempt.
func (f *reviewArtifactFixture) assertReviewRecorded(t *testing.T, wantPassed bool, timeline string) domain.ExecutionFact {
	t.Helper()
	agentRunID := factAgentRunID(t, f.h, f.outcome)
	action := wsActionOfTurn(t, f.h, agentRunID, 3)
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if action.Status != domain.AgentActionSucceeded || attempt.Status != domain.ToolAttemptSucceeded {
		t.Fatalf("action %s attempt %s, want both SUCCEEDED", action.Status, attempt.Status)
	}
	if attempt.Cost == nil || attempt.Cost.Source != reviewTextModelID || attempt.Cost.Amount != "0.001" {
		t.Errorf("attempt cost = %+v, want the charge attributed to the review model", attempt.Cost)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	var fact domain.ExecutionFact
	reviews := 0
	for _, candidate := range facts {
		if candidate.FactType == reviewartifact.FactType {
			fact, reviews = candidate, reviews+1
		}
	}
	if reviews != 1 {
		t.Fatalf("facts = %+v, want exactly one %s", facts, reviewartifact.FactType)
	}
	if fact.SubjectRef != sha256Hex([]byte(timeline)) || fact.ToolAttemptID != attempt.ID || fact.Verdict == nil || *fact.Verdict != wantPassed || fact.BasisFactID != nil {
		t.Errorf("fact = %+v, want it about the timeline digest from attempt %s with verdict %t and no basis", fact, attempt.ID, wantPassed)
	}
	var binding map[string]string
	if err := json.Unmarshal(fact.Binding, &binding); err != nil {
		t.Fatalf("decode binding %s: %v", fact.Binding, err)
	}
	if len(binding) != 1 || binding["policyVersion"] != reviewartifact.PolicyVersion {
		t.Errorf("binding = %v, want exactly the policy version %s", binding, reviewartifact.PolicyVersion)
	}
	message := wsToolMessage(t, f.h, agentRunID, 3, action.ID)
	if message["passed"] != wantPassed || message["policyVersion"] != reviewartifact.PolicyVersion || message["timelineSha256"] != fact.SubjectRef {
		t.Errorf("tool message = %v, want the verdict, the policy version and the timeline digest", message)
	}
	if _, ok := message["issues"].([]any); !ok {
		t.Errorf("tool message = %v, want an issues list", message)
	}
	for _, event := range listEvents(f.h.ctx, t, f.h.uow, f.run.ID) {
		if strings.Contains(string(event.Payload), "does not work") || strings.Contains(string(event.Payload), "Hello and welcome") {
			t.Errorf("event %s %s carries transcript text", event.ID, event.Type)
		}
	}
	if strings.Contains(string(attempt.Result), "Hello and welcome") || strings.Contains(string(fact.Binding), "does not work") {
		t.Errorf("attempt result %s or fact binding %s carries transcript text", attempt.Result, fact.Binding)
	}
	if got := getRun(f.h.ctx, t, f.h.uow, f.run.ID).Status; got != domain.RunCompleted {
		t.Errorf("run status = %s, want COMPLETED", got)
	}
	return fact
}

// A passing review: the fact's verdict is true and the review model read the selected
// sentence with its context.
func TestReviewArtifact_ModelPasses_FactVerdictTrueWithCostOnAttempt(t *testing.T) {
	f := newReviewArtifactFixture(t, newScriptedReviewModel(reviewPassedOpinion))
	timeline := reviewTimeline(sha256Hex([]byte(reviewTranscript)))
	f.start(t, "wf-review-artifact-pass", reviewTranscript, timeline)
	f.assertReviewRecorded(t, true, timeline)
	if f.model.calls() != 1 {
		t.Fatalf("review model asked %d times, want 1", f.model.calls())
	}
	text := f.model.requests[0].Messages[0].Parts[0].Text
	for _, want := range []string{"Requirement: " + reviewRequirement, "> S3\tThis approach does not work", "  S4\tunless the sample is large enough.", "  S1\tHello and welcome to the show."} {
		if !strings.Contains(text, want) {
			t.Errorf("review request lacks %q:\n%s", want, text)
		}
	}
}

// A review that does not pass: the call succeeds, the fact's verdict is false and the
// Agent reads the issue in the tool message.
func TestReviewArtifact_ModelFails_SuccessfulCallWithFalseVerdict(t *testing.T) {
	f := newReviewArtifactFixture(t, newScriptedReviewModel(reviewFailedOpinion))
	timeline := reviewTimeline(sha256Hex([]byte(reviewTranscript)))
	f.start(t, "wf-review-artifact-fail", reviewTranscript, timeline)
	f.assertReviewRecorded(t, false, timeline)
	agentRunID := factAgentRunID(t, f.h, f.outcome)
	message := wsToolMessage(t, f.h, agentRunID, 3, wsActionOfTurn(t, f.h, agentRunID, 3).ID)
	issues, _ := message["issues"].([]any)
	if len(issues) != 1 {
		t.Fatalf("tool message issues = %v, want the one issue", message["issues"])
	}
	issue, _ := issues[0].(map[string]any)
	if issue["type"] != reviewartifact.IssueMeaningDistorted || issue["reason"] != "S3 alone drops the condition S4 adds." {
		t.Errorf("issue = %v, want the distortion of S3 with its reason", issue)
	}
}

// The timeline names another transcript's digest: the Attempt fails with the mismatch
// code before the model is asked, no fact is written, and the error carries no material.
func TestReviewArtifact_TranscriptDigestMismatch_AttemptFailsWithoutFactOrModelCall(t *testing.T) {
	f := newReviewArtifactFixture(t, newScriptedReviewModel(reviewPassedOpinion))
	f.start(t, "wf-review-artifact-mismatch", reviewTranscript, reviewTimeline(strings.Repeat("a", 64)))
	agentRunID := factAgentRunID(t, f.h, f.outcome)
	action := wsActionOfTurn(t, f.h, agentRunID, 3)
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if action.Status != domain.AgentActionFailed || attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "TOOL_ERROR" {
		t.Fatalf("action %s attempt %+v, want both FAILED with TOOL_ERROR", action.Status, attempt)
	}
	if !strings.Contains(attempt.Error.Message, reviewartifact.FailureTranscriptMismatch) {
		t.Errorf("attempt error %q should name %s", attempt.Error.Message, reviewartifact.FailureTranscriptMismatch)
	}
	for _, forbidden := range []string{"does not work", "Hello and welcome", "segments"} {
		if strings.Contains(attempt.Error.Message, forbidden) {
			t.Errorf("attempt error %q carries material", attempt.Error.Message)
		}
	}
	if f.model.calls() != 0 {
		t.Errorf("review model asked %d times, want 0", f.model.calls())
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
}
