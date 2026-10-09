//go:build integration

// Execution fact tests: a Tool that declares the fact its successful result establishes
// gets that fact written in the very transaction that commits the Tool result, the
// Action's completion and AGENT_ACTION_COMPLETED, under the Run aggregate lock. They cover
// what a mock repository cannot prove: the fact rolls back with its Event, a second fact
// for the same Attempt and type aborts the round instead of being ignored, a missing basis
// fact commits the shared Action failure in place of the round, and the asynchronous
// callback resume writes facts through the same result path as the synchronous call.
//
// They share the agentHarness of agent_loop_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const (
	factGenerateTool = "fact_generate"
	factReviewTool   = "fact_review"

	factTypeGenerated = "image_generated"
	factTypeReviewed  = "asset_reviewed"

	// factSignedURL stands for a signed source URL an argument may carry. It must never
	// reach an Event, an ExecutionError or a fact row.
	factSignedURL = "https://assets.example.invalid/photo-a.jpg?X-Signature=fact-secret-signature"

	factPhotoID      = "photo-a"
	factGeneratedID  = "gen-photo-a"
	factDigest       = "sha256:0123abcd"
	factPolicy       = "policy-v1"
	factGenerateArgs = `{"photoAssetId":"` + factPhotoID + `","sourceUrl":"` + factSignedURL + `"}`
	factReviewArgs   = `{"assetRef":"` + factGeneratedID + `","sourceUrl":"` + factSignedURL + `"}`
)

// factGenerateRegistration is a synchronous generation Tool whose result establishes an
// image_generated fact about the generated asset, bound to the source photo (an argument)
// and the applied settings digest (a result value). The digest is optional in the output
// schema, so a result without it is schema-valid but does not honour the declaration.
func factGenerateRegistration(executor registry.ToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:        factGenerateTool,
			Description: "Generate one example image from a photo",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"photoAssetId": {"type": "string", "minLength": 1},
					"sourceUrl": {"type": "string"}
				},
				"required": ["photoAssetId"],
				"additionalProperties": false
			}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"assetId": {"type": "string"},
					"settingsDigest": {"type": "string"}
				},
				"required": ["assetId"],
				"additionalProperties": false
			}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       factTypeGenerated,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/assetId"},
				BindArguments: map[string]domain.FactPointer{
					"photoAssetId":   {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
					"settingsDigest": {Source: domain.FactPointerResult, Pointer: "/settingsDigest"},
				},
			},
		},
		Executor: executor,
	}
}

// factReviewRegistration is a synchronous review Tool whose asset_reviewed fact takes the
// newest image_generated fact about the same asset as its basis. It deliberately declares
// no requirement, so the claim lets a review of an ungenerated asset through and the
// result transaction meets the missing basis.
func factReviewRegistration(executor registry.ToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:        factReviewTool,
			Description: "Review one generated asset",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"assetRef": {"type": "string", "minLength": 1},
					"sourceUrl": {"type": "string"}
				},
				"required": ["assetRef"],
				"additionalProperties": false
			}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"assetRef": {"type": "string"},
					"approved": {"type": "boolean"},
					"policyVersion": {"type": "string"}
				},
				"required": ["assetRef", "approved", "policyVersion"],
				"additionalProperties": false
			}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       factTypeReviewed,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/assetRef"},
				BindArguments: map[string]domain.FactPointer{
					"policyVersion": {Source: domain.FactPointerResult, Pointer: "/policyVersion"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/approved"},
				BasisFactType:  factTypeGenerated,
			},
		},
		Executor: executor,
	}
}

// factScriptedTool returns one fixed result and runs during, when set, inside the call.
type factScriptedTool struct {
	output json.RawMessage
	during func(ctx context.Context, action registry.ToolAction)
	calls  atomic.Int32
}

func (f *factScriptedTool) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	f.calls.Add(1)
	if f.during != nil {
		f.during(ctx, action)
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: f.output}}, nil
}

func factGenerateOutput() json.RawMessage {
	return json.RawMessage(`{"assetId":"` + factGeneratedID + `","settingsDigest":"` + factDigest + `"}`)
}

func factReviewOutput(approved bool) json.RawMessage {
	verdict := "false"
	if approved {
		verdict = "true"
	}
	return json.RawMessage(`{"assetRef":"` + factGeneratedID + `","approved":` + verdict + `,"policyVersion":"` + factPolicy + `"}`)
}

// factScriptToolCalls makes the model decide the given TOOL_CALLs on consecutive Turns
// and answer FINAL afterwards.
func factScriptToolCalls(h *agentHarness, calls ...mockmodel.Scenario) {
	var turn atomic.Int32
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		n := int(turn.Add(1))
		if n > len(calls) {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
		call := calls[n-1]
		call.Kind = mockmodel.ScenarioToolCall
		return &call
	}
}

// factDefinition is the fixture graph with the Agent allowed exactly the given Tools.
func factDefinition(workflowID string, tools ...string) domain.Definition {
	def := agentLoopDefinition(workflowID)
	encoded, _ := json.Marshal(tools)
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, string(encoded), 1))
		}
	}
	return def
}

// newFactHarness registers both fact-producing Tools around the given executors, saves a
// Definition whose Agent may call both, scripts the TOOL_CALLs and claims the Agent
// NodeRun.
func newFactHarness(t *testing.T, workflowID string, generate, review registry.ToolExecutor, calls ...mockmodel.Scenario) (*agentHarness, domain.Run, service.AdvanceOutcome) {
	t.Helper()
	h := newAgentHarness(t, agentHarnessOptions{
		Tools: []registry.ToolRegistration{factGenerateRegistration(generate), factReviewRegistration(review)},
	})
	factScriptToolCalls(h, calls...)
	def := h.saveDefinition(factDefinition(workflowID, factGenerateTool, factReviewTool))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	return h, run, h.claimAgentNode(run.ID)
}

func factGenerateCall() mockmodel.Scenario {
	return mockmodel.Scenario{ToolName: factGenerateTool, ToolArguments: json.RawMessage(factGenerateArgs)}
}

func factReviewCall() mockmodel.Scenario {
	return mockmodel.Scenario{ToolName: factReviewTool, ToolArguments: json.RawMessage(factReviewArgs)}
}

func factAgentRunID(t *testing.T, h *agentHarness, outcome service.AdvanceOutcome) string {
	t.Helper()
	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("no agent run for node run %s", outcome.NodeRunID)
	}
	return agentRun.ID
}

// assertNoSignedURL proves the signed URL from the arguments reached no Event payload, no
// fact row and no Action error.
func assertNoSignedURL(t *testing.T, h *agentHarness, runID, agentRunID string) {
	t.Helper()
	for _, event := range listEvents(h.ctx, t, h.uow, runID) {
		if strings.Contains(string(event.Payload), "X-Signature") || strings.Contains(string(event.Payload), factSignedURL) {
			t.Errorf("event %s %s carries the signed argument URL: %s", event.ID, event.Type, event.Payload)
		}
	}
	for _, fact := range listFactsByAgentRun(h.ctx, t, h.uow, agentRunID) {
		encoded, _ := json.Marshal(fact)
		if strings.Contains(string(encoded), "X-Signature") {
			t.Errorf("fact %s carries the signed argument URL: %s", fact.ID, encoded)
		}
	}
}

// ---------------------------------------------------------------------------
// Synchronous success
// ---------------------------------------------------------------------------

// TestAgentActionFacts_SyncToolSuccess_WritesDeclaredFactWithRound covers the plain
// production: a successful synchronous call of a Tool that declares a fact commits that
// fact with the subject from the result, the bindings from the arguments and the result,
// no verdict and no basis, attributed to the Attempt that produced it, in the same round
// that completes the Action.
func TestAgentActionFacts_SyncToolSuccess_WritesDeclaredFactWithRound(t *testing.T) {
	generate := &factScriptedTool{output: factGenerateOutput()}
	h, run, outcome := newFactHarness(t, "wf-fact-sync", generate, &factScriptedTool{}, factGenerateCall())
	h.execute(outcome)

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Fatalf("action status = %s, want SUCCEEDED", action.Status)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	agentRunID := factAgentRunID(t, h, outcome)

	facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want exactly one", facts)
	}
	fact := facts[0]
	if fact.RunID != run.ID || fact.ToolAttemptID != attempt.ID || fact.FactType != factTypeGenerated || fact.SubjectRef != factGeneratedID {
		t.Errorf("fact = %+v, want %s about %s from attempt %s", fact, factTypeGenerated, factGeneratedID, attempt.ID)
	}
	if !strings.HasPrefix(fact.ID, domain.IDPrefixExecutionFact) {
		t.Errorf("fact id = %q, want the %q prefix", fact.ID, domain.IDPrefixExecutionFact)
	}
	assertSameJSON(t, "fact binding", json.RawMessage(`{"photoAssetId":"`+factPhotoID+`","settingsDigest":"`+factDigest+`"}`), fact.Binding)
	if fact.Verdict != nil || fact.BasisFactID != nil {
		t.Errorf("generation fact verdict=%v basis=%v, want both nil", fact.Verdict, fact.BasisFactID)
	}
	if attempt.CompletedAt == nil || !fact.CreatedAt.Equal(*attempt.CompletedAt) {
		t.Errorf("fact created_at = %s, want the result transaction's time %v", fact.CreatedAt, attempt.CompletedAt)
	}
	assertNoSignedURL(t, h, run.ID, agentRunID)
}

// TestAgentActionFacts_ReviewWithBasis_LinksGenerationFact covers the provenance chain: a
// review whose declaration names image_generated as its basis records the generation
// fact about the same asset as basis_fact_id, together with its verdict.
func TestAgentActionFacts_ReviewWithBasis_LinksGenerationFact(t *testing.T) {
	generate := &factScriptedTool{output: factGenerateOutput()}
	review := &factScriptedTool{output: factReviewOutput(true)}
	h, run, outcome := newFactHarness(t, "wf-fact-basis", generate, review, factGenerateCall(), factReviewCall())
	h.execute(outcome)

	if review.calls.Load() != 1 {
		t.Fatalf("review calls = %d, want 1", review.calls.Load())
	}
	agentRunID := factAgentRunID(t, h, outcome)
	facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID)
	if len(facts) != 2 {
		t.Fatalf("facts = %+v, want the generation and the review fact", facts)
	}
	// The harness clock is fixed, so both facts share created_at; select them by type.
	byType := map[string]domain.ExecutionFact{}
	for _, fact := range facts {
		byType[fact.FactType] = fact
	}
	generated, okGenerated := byType[factTypeGenerated]
	reviewed, okReviewed := byType[factTypeReviewed]
	if !okGenerated || !okReviewed {
		t.Fatalf("facts = %+v, want one %s and one %s", facts, factTypeGenerated, factTypeReviewed)
	}
	if reviewed.SubjectRef != factGeneratedID {
		t.Errorf("review subject = %s, want %s", reviewed.SubjectRef, factGeneratedID)
	}
	if reviewed.BasisFactID == nil || *reviewed.BasisFactID != generated.ID {
		t.Errorf("review basis = %v, want the generation fact %s", reviewed.BasisFactID, generated.ID)
	}
	if reviewed.Verdict == nil || !*reviewed.Verdict {
		t.Errorf("review verdict = %v, want true", reviewed.Verdict)
	}
	assertSameJSON(t, "review binding", json.RawMessage(`{"policyVersion":"`+factPolicy+`"}`), reviewed.Binding)
	if reviewed.ToolAttemptID == generated.ToolAttemptID {
		t.Errorf("both facts name attempt %s; each comes from its own Attempt", reviewed.ToolAttemptID)
	}
	assertNoSignedURL(t, h, run.ID, agentRunID)
}

// ---------------------------------------------------------------------------
// Rollback
// ---------------------------------------------------------------------------

// TestAgentActionFacts_EventInsertFailsAfterFactInsert_RollsBackFact covers the atomicity
// of a fact with its Event: the fact is inserted before AGENT_ACTION_COMPLETED, and when
// that Event cannot be inserted the whole result transaction rolls back -- no fact, the
// Action still RUNNING and the Attempt still STARTED without a result, exactly as the
// result transaction's rollback leaves any other round.
func TestAgentActionFacts_EventInsertFailsAfterFactInsert_RollsBackFact(t *testing.T) {
	generate := &factScriptedTool{output: factGenerateOutput()}
	h, run, outcome := newFactHarness(t, "wf-fact-event-rollback", generate, &factScriptedTool{}, factGenerateCall())

	const collidingEventID = "ev_fact_result_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())
	// Armed during the Tool call, so the collision hits the result transaction's first
	// Event, which follows the fact insert.
	generate.during = func(context.Context, registry.ToolAction) {
		h.ids.ForceNextEventID(collidingEventID)
	}

	if err := h.svc.Execute(h.ctx, outcome); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("execute = %v, want the conflict from the duplicated Event ID", err)
	}

	if facts := listFactsByAgentRun(h.ctx, t, h.uow, factAgentRunID(t, h, outcome)); len(facts) != 0 {
		t.Errorf("facts after rollback = %+v, want none", facts)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionRunning || action.CompletedAt != nil {
		t.Errorf("action = status %s completed_at %v, want RUNNING without completed_at", action.Status, action.CompletedAt)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted || len(attempts[0].Result) != 0 {
		t.Fatalf("tool attempts = %+v, want exactly one still STARTED without a result", attempts)
	}
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentActionCompleted) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_COMPLETED", types)
	}
}

// TestAgentActionFacts_DuplicateFactForAttempt_RollsBackAndSurfacesError covers the
// single-write rule of facts: when the Attempt already recorded a fact of the declared
// type, the insert violates the uniqueness of (Tool Attempt, fact type), the error
// surfaces and the round rolls back rather than being silently skipped.
func TestAgentActionFacts_DuplicateFactForAttempt_RollsBackAndSurfacesError(t *testing.T) {
	generate := &factScriptedTool{output: factGenerateOutput()}
	h, run, outcome := newFactHarness(t, "wf-fact-duplicate", generate, &factScriptedTool{}, factGenerateCall())
	agentRunID := factAgentRunID(t, h, outcome)

	// The claim transaction has committed the Attempt by the time the Tool runs, so the
	// conflicting fact can reference it.
	generate.during = func(ctx context.Context, action registry.ToolAction) {
		attempts := agentToolAttempts(ctx, t, h.uow, action.ActionID)
		if len(attempts) != 1 {
			t.Errorf("tool attempts during the call = %d, want 1", len(attempts))
			return
		}
		existing := newExecutionFact("fact_preexisting", run.ID, agentRunID, attempts[0].ID, factTypeGenerated, "earlier-subject")
		existing.CreatedAt = h.clock.Now()
		insertExecutionFact(ctx, t, h.uow, existing)
	}

	err := h.svc.Execute(h.ctx, outcome)
	if !errors.Is(err, domain.ErrExecutionFactDuplicate) {
		t.Fatalf("execute = %v, want the duplicate fact error", err)
	}

	facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID)
	if len(facts) != 1 || facts[0].ID != "fact_preexisting" {
		t.Errorf("facts = %+v, want only the pre-existing one", facts)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionRunning {
		t.Errorf("action status = %s, want RUNNING: the round rolled back", action.Status)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Errorf("tool attempts = %+v, want one still STARTED", attempts)
	}
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentActionCompleted) != 0 || agentCountEventType(types, domain.EventAgentActionFailed) != 0 {
		t.Errorf("events = %v, want neither completion nor failure of the Action", types)
	}
}

// ---------------------------------------------------------------------------
// Declaration violations
// ---------------------------------------------------------------------------

// TestAgentActionFacts_BasisMissing_FailsActionInvalidActionKeepsResult covers a review
// whose basis fact does not exist: the Provider did complete the call, so the Attempt is
// SUCCEEDED with its result; the Action fails with FACT_BASIS_MISSING through the shared
// Action failure with failureSource SYNC_EXECUTION, the Agent terminates as
// INVALID_ACTION, no fact (not even the review's own) is written, and no Event or error
// carries an argument value.
func TestAgentActionFacts_BasisMissing_FailsActionInvalidActionKeepsResult(t *testing.T) {
	review := &factScriptedTool{output: factReviewOutput(true)}
	h, run, outcome := newFactHarness(t, "wf-fact-basis-missing", &factScriptedTool{}, review, factReviewCall())
	h.execute(outcome)

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationInvalidAction, domain.FailureSyncExecution)
	if action.Error == nil || action.Error.Code != runtime.CodeFactBasisMissing {
		t.Fatalf("action error = %+v, want %s", action.Error, runtime.CodeFactBasisMissing)
	}
	var details map[string]string
	if err := json.Unmarshal(action.Error.Details, &details); err != nil {
		t.Fatalf("decode error details %s: %v", action.Error.Details, err)
	}
	if details["factType"] != factTypeReviewed || details["basisFactType"] != factTypeGenerated || details["subject"] != factGeneratedID {
		t.Errorf("error details = %v, want the review fact type, its basis type and the subject", details)
	}
	if strings.Contains(action.Error.Message, "X-Signature") || strings.Contains(string(action.Error.Details), "X-Signature") {
		t.Errorf("action error carries the signed argument URL: %+v", action.Error)
	}

	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempt status = %s, want SUCCEEDED: the Provider completed the call", attempt.Status)
	}
	assertSameJSON(t, "retained tool result", factReviewOutput(true), attempt.Result)

	agentRunID := factAgentRunID(t, h, outcome)
	if facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
	failed := agentLastEventPayload(t, listEvents(h.ctx, t, h.uow, run.ID), domain.EventAgentActionFailed)
	if failed["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_FAILED toolAttemptId = %v, want %s", failed["toolAttemptId"], attempt.ID)
	}
	assertNoSignedURL(t, h, run.ID, agentRunID)
}

// TestAgentActionFacts_ResultBreaksDeclaration_FailsWithExtractionCode covers a
// schema-valid result that lacks a value its Tool's production declaration binds: the
// Tool broke its own declaration, so the Action fails with FACT_EXTRACTION_FAILED and the
// Agent terminates as INVALID_ACTION, while the Attempt keeps its successful result.
func TestAgentActionFacts_ResultBreaksDeclaration_FailsWithExtractionCode(t *testing.T) {
	generate := &factScriptedTool{output: json.RawMessage(`{"assetId":"` + factGeneratedID + `"}`)}
	h, run, outcome := newFactHarness(t, "wf-fact-extraction", generate, &factScriptedTool{}, factGenerateCall())
	h.execute(outcome)

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationInvalidAction, domain.FailureSyncExecution)
	if action.Error == nil || action.Error.Code != runtime.CodeFactExtractionFailed {
		t.Fatalf("action error = %+v, want %s", action.Error, runtime.CodeFactExtractionFailed)
	}
	if attempt := agentOnlyToolAttempt(t, h, action.ID); attempt.Status != domain.ToolAttemptSucceeded || len(attempt.Result) == 0 {
		t.Errorf("tool attempt = status %s result %s, want SUCCEEDED with its result", attempt.Status, attempt.Result)
	}
	agentRunID := factAgentRunID(t, h, outcome)
	if facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
	assertNoSignedURL(t, h, run.ID, agentRunID)
}

// ---------------------------------------------------------------------------
// Asynchronous resume
// ---------------------------------------------------------------------------

// TestAgentActionFacts_CallbackCompletesAsyncTool_WritesFactInResumeTx covers the single
// result path: an asynchronous Tool that declares a fact gets it written by the callback
// resume transaction, attributed to the DISPATCHED Attempt the callback resolved, with the
// Action completed by CALLBACK in the same commit.
func TestAgentActionFacts_CallbackCompletesAsyncTool_WritesFactInResumeTx(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	registration := remotelookup.Registration("http://mock-provider.invalid", nil)
	registration.Executor = tool
	registration.Metadata.Produces = &domain.FactProduction{
		FactType:       "record_fetched",
		SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/key"},
		BindArguments: map[string]domain.FactPointer{
			"record": {Source: domain.FactPointerResult, Pointer: "/record"},
		},
	}
	h := newAgentHarness(t, agentHarnessOptions{
		Tools: []registry.ToolRegistration{registration},
		Clock: newExecClock(time.Now().UTC().Truncate(time.Millisecond)),
	})
	factScriptToolCalls(h, mockmodel.Scenario{ToolName: remotelookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments)})
	def := h.saveDefinition(factDefinition("wf-fact-callback", remotelookup.ToolName))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
	}
	agentRunID := factAgentRunID(t, h, outcome)
	if facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID); len(facts) != 0 {
		t.Fatalf("facts before the callback = %+v, want none: dispatch establishes no fact", facts)
	}

	got, err := agentDeliver(h.ctx, h, tool.lastToken(t), agentCallbackSucceeded)
	if err != nil || !got.Accepted || got.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", got, err)
	}

	action = getAgentAction(h.ctx, t, h.uow, action.ID)
	if action.Status != domain.AgentActionSucceeded {
		t.Fatalf("action status = %s, want SUCCEEDED", action.Status)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want exactly one", facts)
	}
	fact := facts[0]
	if fact.ToolAttemptID != attempt.ID || fact.FactType != "record_fetched" || fact.SubjectRef != "k1" {
		t.Errorf("fact = %+v, want record_fetched about k1 from attempt %s", fact, attempt.ID)
	}
	assertSameJSON(t, "fact binding", json.RawMessage(`{"record":"ember record"}`), fact.Binding)
	if attempt.CompletedAt == nil || !fact.CreatedAt.Equal(*attempt.CompletedAt) {
		t.Errorf("fact created_at = %s, want the resume transaction's time %v", fact.CreatedAt, attempt.CompletedAt)
	}
	completed := agentLastEventPayload(t, listEvents(h.ctx, t, h.uow, run.ID), domain.EventAgentActionCompleted)
	if completed["completionSource"] != string(domain.CompletionCallback) {
		t.Errorf("AGENT_ACTION_COMPLETED completionSource = %v, want CALLBACK", completed["completionSource"])
	}
}
