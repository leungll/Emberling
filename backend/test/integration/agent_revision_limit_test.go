//go:build integration

// Revision limits against PostgreSQL and the in-process media task service: once an Agent
// Run has recorded more failed facts of a bounded type than its frozen maxFailedFacts
// allows, the next call of a Tool bounded by that type is handed back to the Agent as
// REVISION_LIMIT_REACHED in its claim transaction, before any Attempt exists and without
// the service receiving anything; failures equal to the limit still allow the call; the
// technical and the semantic failures are counted per fact type; and a type the node does
// not bound never refuses.
//
// They share the mediaFixture of agent_media_tools_test.go and the test task-input node of
// agent_approval_reconcile_test.go.
package integration

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

const (
	// clipReviewTool is a test-only synchronous Tool that establishes a content_reviewed
	// fact about a timeline digest, standing in for the real review Tool: the completion
	// check and the revision limit decide by the registered fact type, never by the name.
	clipReviewTool  = "review_content_test"
	clipPolicy      = "policy-test-v1"
	clipReportSha   = mediaClipSha
	clipOtherSha    = "4444444444444444444444444444444444444444444444444444444444444444"
	clipMaxTurns    = 12
	clipTimeoutMs   = 120000
	clipTaskInputID = "node_input"
)

// clipReviewRegistration declares the test review Tool. Each call records
// content_reviewed about the timeline digest it was asked to review, with the next
// scripted verdict, and counts toward both media revision limits like the real Tool.
func clipReviewRegistration(verdicts *clipReviewVerdicts) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:        clipReviewTool,
			Description: "Review the content of a timeline, named by its digest",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"timelineSha256": {"type": "string", "pattern": "^[0-9a-f]{64}$"}},
				"required": ["timelineSha256"],
				"additionalProperties": false
			}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"passed": {"type": "boolean"}, "policyVersion": {"type": "string"}},
				"required": ["passed", "policyVersion"],
				"additionalProperties": false
			}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       domain.FactTypeContentReviewed,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerArguments, Pointer: "/timelineSha256"},
				BindArguments: map[string]domain.FactPointer{
					"policyVersion": {Source: domain.FactPointerResult, Pointer: "/policyVersion"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"},
			},
			Requires:           []domain.FactRequirement{},
			Records:            []domain.ToolRecordKind{},
			CountedFailedFacts: []string{domain.FactTypeMediaChecked, domain.FactTypeContentReviewed},
		},
		Executor: verdicts,
	}
}

// clipReviewVerdicts answers each review call with the next scripted verdict and records
// how many calls reached the executor.
type clipReviewVerdicts struct {
	verdicts []bool
	calls    atomic.Int32
}

func (v *clipReviewVerdicts) Execute(_ context.Context, _ registry.ToolAction) (registry.ToolExecutionResult, error) {
	n := int(v.calls.Add(1))
	passed := n <= len(v.verdicts) && v.verdicts[n-1]
	output, _ := json.Marshal(map[string]any{"passed": passed, "policyVersion": clipPolicy})
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output}}, nil
}

func contentReviewCall(timelineSha string) mockmodel.Scenario {
	return wsCall(clipReviewTool, map[string]any{"timelineSha256": timelineSha})
}

// clipFinal is the FINAL output of a clip delivery Agent: outcome DELIVERED with the clip
// digest, or CANNOT_SATISFY with the reason.
func clipFinal(outcome string, clipSha, cannotSatisfyReason *string) mockmodel.Scenario {
	report := map[string]any{"outcome": outcome, "clipSha256": nil, "selectionReason": "the second sentence states the claim", "cannotSatisfyReason": nil}
	if clipSha != nil {
		report["clipSha256"] = *clipSha
	}
	if cannotSatisfyReason != nil {
		report["cannotSatisfyReason"] = *cannotSatisfyReason
	}
	encoded, _ := json.Marshal(report)
	return mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, FinalOutput: encoded}
}

func deliveredFinal(clipSha string) mockmodel.Scenario { return clipFinal("DELIVERED", &clipSha, nil) }

func cannotSatisfyFinal(reason *string) mockmodel.Scenario {
	return clipFinal("CANNOT_SATISFY", nil, reason)
}

// clipDefinition is the clip delivery graph: the test task input publishes {goal, question}
// with the DELIVER_CLIP goal, the Agent reads its goal at /goal, may call the given Tools,
// freezes the given revision limits and answers with the fixed clip delivery report; its
// answer feeds a text output.
func clipDefinition(workflowID string, maxTurns int, limits map[string]int, tools ...string) domain.Definition {
	config := map[string]any{
		"instructions": agentInstructions,
		"modelId":      mockmodel.ModelID,
		"allowedTools": tools,
		"maxTurns":     maxTurns,
		"timeoutMs":    clipTimeoutMs,
		"goalPointer":  "/goal",
		"outputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"outcome":             map[string]any{"type": "string", "enum": []string{"DELIVERED", "CANNOT_SATISFY"}},
				"clipSha256":          map[string]any{"type": []string{"string", "null"}},
				"selectionReason":     map[string]any{"type": "string"},
				"cannotSatisfyReason": map[string]any{"type": []string{"string", "null"}},
			},
			"required":             []string{"outcome", "clipSha256", "selectionReason", "cannotSatisfyReason"},
			"additionalProperties": false,
		},
	}
	if limits != nil {
		config["maxFailedFacts"] = limits
	}
	encoded, _ := json.Marshal(config)
	task, _ := json.Marshal(map[string]any{"task": map[string]any{"goal": domain.AgentGoalDeliverClip, "question": agentQuestion}})
	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Clip delivery",
		Description: "task input -> agent -> text_output",
		Nodes: []domain.Node{
			{ID: clipTaskInputID, Type: approvalTaskInputType, Name: "Task", Config: task},
			{ID: "node_agent", Type: "agent", Name: "Agent", Config: encoded},
			{ID: "node_output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: clipTaskInputID, SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
			{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_output", TargetHandle: "text"},
		},
	}
}

// clipFixture is a mediaFixture with the test task-input node and the test review Tool
// registered, for Agents frozen to the DELIVER_CLIP goal.
type clipFixture struct {
	*mediaFixture
	reviews *clipReviewVerdicts
}

func newClipFixture(t *testing.T, reviewVerdicts []bool, opts agentHarnessOptions) *clipFixture {
	t.Helper()
	reviews := &clipReviewVerdicts{verdicts: reviewVerdicts}
	opts.Nodes = append(opts.Nodes, approvalTaskInputRegistration())
	opts.Tools = append(opts.Tools, clipReviewRegistration(reviews))
	return &clipFixture{mediaFixture: newMediaFixture(t, opts), reviews: reviews}
}

// clipScript makes the model decide the scripted scenarios on consecutive Turns: TOOL_CALLs
// first and the FINAL report last. A Turn past the script fails the test, which keeps a
// refusal from silently being answered with another FINAL.
func clipScript(h *agentHarness, scenarios ...mockmodel.Scenario) {
	var turn atomic.Int32
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		n := int(turn.Add(1))
		if n > len(scenarios) {
			h.t.Errorf("the model was asked for turn %d, past the %d scripted scenarios", n, len(scenarios))
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
		scenario := scenarios[n-1]
		if scenario.Kind == "" {
			scenario.Kind = mockmodel.ScenarioToolCall
		}
		return &scenario
	}
}

// start saves the clip Definition, creates the Run, claims the Agent NodeRun and executes
// it up to the first call that waits on the service or the loop's end.
func (f *clipFixture) start(t *testing.T, workflowID string, maxTurns int, limits map[string]int, tools []string, scenarios ...mockmodel.Scenario) (domain.Run, service.AdvanceOutcome) {
	t.Helper()
	clipScript(f.h, scenarios...)
	def := f.h.saveDefinition(clipDefinition(workflowID, maxTurns, limits, tools...))
	run := f.h.createRun(def.WorkflowID, def.Version, `{}`)
	outcome := f.h.claimAgentNode(run.ID)
	f.h.execute(outcome)
	return run, outcome
}

var clipTools = []string{externaltool.RunChecksToolName, externaltool.RenderTimelineToolName}

var clipReviewedTools = []string{externaltool.RunChecksToolName, externaltool.RenderTimelineToolName, clipReviewTool}

// assertRevisionRefused proves the Action of Turn turnNo was handed back as
// REVISION_LIMIT_REACHED in its claim transaction: FAILED with the code, no Attempt, no
// AGENT_ACTION_STARTED, AGENT_ACTION_FAILED with returnedToAgent and the AUTHORIZATION
// source, a REJECTED tool message naming the fact type and the limit, and a new Turn.
func assertRevisionRefused(t *testing.T, h *agentHarness, run domain.Run, agentRunID string, turnNo int, factType string, limit int) {
	t.Helper()
	action := wsActionOfTurn(t, h, agentRunID, turnNo)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != string(domain.ReturnRevisionLimitReached) {
		t.Fatalf("action of turn %d = status %s error %+v, want FAILED with REVISION_LIMIT_REACHED", turnNo, action.Status, action.Error)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the refused call = %+v, want none", attempts)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if started := agentPayloadsFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID); len(started) != 0 {
		t.Errorf("AGENT_ACTION_STARTED for the refused call = %v, want none", started)
	}
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["returnedToAgent"] != true || failed["failureSource"] != string(domain.FailureAuthorization) {
		t.Errorf("AGENT_ACTION_FAILED = %v, want returnedToAgent with the AUTHORIZATION source", failed)
	}
	message := wsToolMessage(t, h, agentRunID, turnNo, action.ID)
	if message["status"] != "REJECTED" || message["code"] != string(domain.ReturnRevisionLimitReached) {
		t.Errorf("tool message = %v, want REJECTED REVISION_LIMIT_REACHED", message)
	}
	text, _ := message["message"].(string)
	if !strings.Contains(text, `"`+factType+`"`) || !strings.Contains(text, "limit of "+strconv.Itoa(limit)) {
		t.Errorf("tool message %q should name the fact type %s and the limit %d", text, factType, limit)
	}
	if !agentTurnNoExists(h.ctx, t, h.uow, agentRunID, turnNo+1) {
		t.Errorf("want a Turn to follow the refusal of turn %d", turnNo)
	}
}

// Failures equal to the limit still allow the call: with one revision allowed, the check
// after one failed check reaches the service, and the Agent Run froze the limit it was
// configured with.
func TestRevisionLimit_FailuresEqualLimit_CallAllowed(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	first, second := canonicalTimelineFor("S12", "S13"), canonicalTimelineFor("S12", "S14")
	_, outcome := f.start(t, "wf-revision-at-limit", clipMaxTurns, map[string]int{domain.FactTypeMediaChecked: 1}, clipTools,
		timelineCheckCall("S12", "S13"), timelineCheckCall("S12", "S14"), cannotSatisfyFinal(strPtr("no sentence states the claim within the target length")))
	agentRunID := factAgentRunID(t, f.h, outcome)

	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if len(agentRun.MaxFailedFacts) != 1 || agentRun.MaxFailedFacts[domain.FactTypeMediaChecked] != 1 {
		t.Fatalf("frozen revision limits = %v, want media_checked 1", agentRun.MaxFailedFacts)
	}
	_, firstTask := f.waitingAction(t, agentRunID, 1)
	f.deliver(t, firstTask, timelineCheckedPayload(first, false))

	_, secondTask := f.waitingAction(t, agentRunID, 2)
	if task := f.service.task(t, secondTask); task.taskType != externaltool.TaskTypeCheckTimeline {
		t.Fatalf("second check dispatched %s, want CHECK_TIMELINE", task.taskType)
	}
	f.deliver(t, secondTask, timelineCheckedPayload(second, true))
	if submits, _ := f.service.counts(); submits != 2 {
		t.Errorf("the media service saw %d submits, want both checks", submits)
	}
}

// Failures over the limit refuse the next bounded call before any Attempt: with one
// revision allowed and two failed checks recorded, the third check is handed back as
// REVISION_LIMIT_REACHED and the service receives nothing for it.
func TestRevisionLimit_FailuresOverLimit_RefusedWithoutAnyCall(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	first, second := canonicalTimelineFor("S12", "S13"), canonicalTimelineFor("S12", "S14")
	run, outcome := f.start(t, "wf-revision-over-limit", clipMaxTurns, map[string]int{domain.FactTypeMediaChecked: 1}, clipTools,
		timelineCheckCall("S12", "S13"), timelineCheckCall("S12", "S14"), timelineCheckCall("S12", "S15"),
		cannotSatisfyFinal(strPtr("the checks kept failing")))
	agentRunID := factAgentRunID(t, f.h, outcome)

	_, firstTask := f.waitingAction(t, agentRunID, 1)
	f.deliver(t, firstTask, timelineCheckedPayload(first, false))
	_, secondTask := f.waitingAction(t, agentRunID, 2)
	f.deliver(t, secondTask, timelineCheckedPayload(second, false))

	assertRevisionRefused(t, f.h, run, agentRunID, 3, domain.FactTypeMediaChecked, 1)
	if submits, gets := f.service.counts(); submits != 2 || gets != 0 {
		t.Errorf("the media service saw %d submits and %d status queries, want the two checks only", submits, gets)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	if len(factsOfType(facts, domain.FactTypeMediaChecked)) != 2 {
		t.Errorf("media_checked facts = %+v, want the two failed checks and nothing from the refused call", facts)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %s, want FINAL_RESPONSE: the Agent answered after the refusal", agentTermination(agentRun))
	}
}

// Semantic failures do not consume the technical count: each fact type is counted against
// its own limit, so after one failed check and one failed review (two failures, one limit
// each) a check is still allowed. Once the semantic failures pass their limit, every Tool
// bounded by that type is refused, the check included: the task is over, not just the
// review.
func TestRevisionLimit_SemanticAndTechnicalFailures_CountedPerFactType(t *testing.T) {
	f := newClipFixture(t, []bool{false, false, true}, agentHarnessOptions{})
	first, second := canonicalTimelineFor("S12", "S13"), canonicalTimelineFor("S12", "S14")
	firstSha, secondSha := timelineDigest(first), timelineDigest(second)
	limits := map[string]int{domain.FactTypeMediaChecked: 1, domain.FactTypeContentReviewed: 1}
	run, outcome := f.start(t, "wf-revision-per-type", clipMaxTurns, limits, clipReviewedTools,
		timelineCheckCall("S12", "S13"), contentReviewCall(firstSha),
		timelineCheckCall("S12", "S14"), contentReviewCall(secondSha),
		contentReviewCall(secondSha), timelineCheckCall("S12", "S15"),
		cannotSatisfyFinal(strPtr("the review kept failing")))
	agentRunID := factAgentRunID(t, f.h, outcome)

	_, firstTask := f.waitingAction(t, agentRunID, 1)
	f.deliver(t, firstTask, timelineCheckedPayload(first, false))
	// Turn 2 reviews synchronously (fails) and Turn 3's check is allowed: one failed
	// check equals the technical limit, and the failed review is counted elsewhere.
	_, thirdTask := f.waitingAction(t, agentRunID, 3)
	f.deliver(t, thirdTask, timelineCheckedPayload(second, true))
	// Turn 4 reviews again (allowed: one failed review equals the semantic limit) and
	// fails; Turn 5's review and Turn 6's check are both over the semantic limit and
	// refused, while the technical failures stayed at one.
	assertRevisionRefused(t, f.h, run, agentRunID, 5, domain.FactTypeContentReviewed, 1)
	assertRevisionRefused(t, f.h, run, agentRunID, 6, domain.FactTypeContentReviewed, 1)

	if calls := f.reviews.calls.Load(); calls != 2 {
		t.Errorf("review executor calls = %d, want two: the refused review never ran", calls)
	}
	if submits, _ := f.service.counts(); submits != 2 {
		t.Errorf("the media service saw %d submits, want the two allowed checks", submits)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	if checks, reviews := factsOfType(facts, domain.FactTypeMediaChecked), factsOfType(facts, domain.FactTypeContentReviewed); len(checks) != 2 || len(reviews) != 2 {
		t.Errorf("facts = %d checks and %d reviews, want 2 and 2", len(checks), len(reviews))
	}
}

// A fact type the node does not bound never refuses: with only the technical limit
// configured, review failures accumulate without any refusal.
func TestRevisionLimit_UnboundedFactType_NeverRefuses(t *testing.T) {
	f := newClipFixture(t, []bool{false, false, false}, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	timelineSha := timelineDigest(timeline)
	_, outcome := f.start(t, "wf-revision-unbounded", clipMaxTurns, map[string]int{domain.FactTypeMediaChecked: 0}, clipReviewedTools,
		timelineCheckCall("S12", "S13"), contentReviewCall(timelineSha), contentReviewCall(timelineSha), contentReviewCall(timelineSha),
		cannotSatisfyFinal(strPtr("the review never passed")))
	agentRunID := factAgentRunID(t, f.h, outcome)

	_, firstTask := f.waitingAction(t, agentRunID, 1)
	f.deliver(t, firstTask, timelineCheckedPayload(timeline, true))

	if calls := f.reviews.calls.Load(); calls != 3 {
		t.Errorf("review executor calls = %d, want all three", calls)
	}
	for turnNo := 2; turnNo <= 4; turnNo++ {
		if action := wsActionOfTurn(t, f.h, agentRunID, turnNo); action.Status != domain.AgentActionSucceeded {
			t.Errorf("review action of turn %d = %s, want SUCCEEDED", turnNo, action.Status)
		}
	}
}

func strPtr(s string) *string { return &s }
