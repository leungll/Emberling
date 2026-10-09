//go:build integration

// Fact requirement tests: a Tool that declares required facts is checked against the
// Run's committed facts in its Action's claim transaction, under the Run aggregate lock,
// after the allowlist and Input Schema checks and before any Tool Attempt exists. An
// unmet requirement fails the Action exactly like an invalid Decision -- PRECONDITION_UNMET,
// termination INVALID_ACTION, no Attempt and no external dispatch -- while a satisfied one
// lets the claim proceed unchanged. They cover what a pure matcher test cannot: the
// candidates come from PostgreSQL newest first, the rejection commits atomically with its
// Events, a crash after the claim commit never re-enters the check, and a producer that
// left the Registry does not invalidate the facts it already committed.
//
// They share the agentHarness of agent_loop_test.go and the fact fixtures of
// agent_action_facts_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
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
	requirementVideoTool = "fact_video"
	// requirementOtherPhotoID is a second photo of the set. Its value must never reach an
	// error or an Event, because it is an argument value rather than the subject.
	requirementOtherPhotoID = "photo-zeta"
	// requirementOtherAssetID is an asset generated from that photo, about which no fact
	// exists.
	requirementOtherAssetID = "gen-asset-0042"
	// requirementMaxTurns leaves room for every scripted Tool round of these tests.
	requirementMaxTurns = 6
)

// requirementProvider is a Mock Provider whose task dispatches are counted as they
// arrive, so a test can prove that a rejected claim never reached the external system.
type requirementProvider struct {
	url        string
	dispatches atomic.Int32
}

func newRequirementProvider(t *testing.T) *requirementProvider {
	t.Helper()
	dispatcher := mockprovider.NewDispatcher(nil)
	handler := mockprovider.NewServer(dispatcher)
	p := &requirementProvider{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tasks" {
			p.dispatches.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		server.Close()
		_ = dispatcher.Shutdown(context.Background())
	})
	p.url = server.URL
	return p
}

// requirementVideo is the protected asynchronous Tool's Executor. It dispatches a real
// task to the Mock Provider through the remote_lookup Executor, with a "lost" scenario so
// no callback ever arrives, and runs during, when set, before the dispatch.
type requirementVideo struct {
	delegate *remotelookup.Executor
	during   func(ctx context.Context, action registry.ToolAction)
	calls    atomic.Int32
}

func newRequirementVideo(p *requirementProvider) *requirementVideo {
	return &requirementVideo{delegate: remotelookup.New(p.url, &http.Client{Timeout: 5 * time.Second})}
}

func (v *requirementVideo) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	v.calls.Add(1)
	if v.during != nil {
		v.during(ctx, action)
	}
	action.Arguments = json.RawMessage(`{"key":"mock:lost"}`)
	return v.delegate.Execute(ctx, action)
}

func (v *requirementVideo) OnCallback(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
	return v.delegate.OnCallback(ctx, state, payload)
}

// requirementVideoRegistration is an asynchronous video Tool with an uncertain external
// side effect. It requires a generation fact about the asset it is asked to animate,
// bound to the same source photo, and the newest review of that asset to have passed.
func requirementVideoRegistration(executor registry.ToolExecutor) registry.ToolRegistration {
	approved := true
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:        requirementVideoTool,
			Description: "Generate one video from a reviewed asset",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"assetRef": {"type": "string", "minLength": 1},
					"photoAssetId": {"type": "string", "minLength": 1},
					"sourceUrl": {"type": "string"}
				},
				"required": ["assetRef", "photoAssetId"],
				"additionalProperties": false
			}`),
			OutputSchema:  remotelookup.Registration("http://mock-provider.invalid", nil).Metadata.OutputSchema,
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionAsync,
			Requires: []domain.FactRequirement{
				{FactType: factTypeGenerated, SubjectArgument: "/assetRef", MatchBindings: []string{"photoAssetId"}},
				{FactType: factTypeReviewed, SubjectArgument: "/assetRef", RequireVerdict: &approved},
			},
		},
		Executor: executor,
	}
}

// factSequenceTool returns its outputs in order, repeating the last one, and runs during,
// when set, inside every call.
type factSequenceTool struct {
	outputs []json.RawMessage
	during  func(ctx context.Context, action registry.ToolAction)
	calls   atomic.Int32
}

func (f *factSequenceTool) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	n := int(f.calls.Add(1))
	if f.during != nil {
		f.during(ctx, action)
	}
	output := f.outputs[min(n, len(f.outputs))-1]
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output}}, nil
}

// requirementSetup selects the Tools one Backend image registers. A restart is a second
// setup over the first one's pool and clock.
type requirementSetup struct {
	generate     registry.ToolExecutor
	review       registry.ToolExecutor
	video        *requirementVideo
	skipGenerate bool
	pool         *pgxpool.Pool
	clock        *execClock
}

func (s requirementSetup) harness(t *testing.T) *agentHarness {
	t.Helper()
	var tools []registry.ToolRegistration
	if !s.skipGenerate {
		tools = append(tools, factGenerateRegistration(s.generate))
	}
	tools = append(tools, factReviewRegistration(s.review), requirementVideoRegistration(s.video))
	return newAgentHarness(t, agentHarnessOptions{Tools: tools, Pool: s.pool, Clock: s.clock})
}

// requirementDefinition is the fixture graph whose Agent may call the generation, review
// and video Tools, with enough rounds for every script below.
func requirementDefinition(workflowID string) domain.Definition {
	def := agentLoopDefinitionMaxTurns(workflowID, requirementMaxTurns)
	encoded, _ := json.Marshal([]string{factGenerateTool, factReviewTool, requirementVideoTool})
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, string(encoded), 1))
		}
	}
	return def
}

// startRequirementRun scripts the TOOL_CALLs, saves the Definition, creates the Run and
// claims its Agent NodeRun.
func startRequirementRun(h *agentHarness, workflowID string, calls ...mockmodel.Scenario) (domain.Run, service.AdvanceOutcome) {
	h.t.Helper()
	factScriptToolCalls(h, calls...)
	def := h.saveDefinition(requirementDefinition(workflowID))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	return run, h.claimAgentNode(run.ID)
}

// requirementVideoCall asks for a video of assetRef generated from photoID. The signed
// source URL rides along as an argument that must never surface anywhere.
func requirementVideoCall(assetRef, photoID string) mockmodel.Scenario {
	return mockmodel.Scenario{ToolName: requirementVideoTool, ToolArguments: json.RawMessage(
		`{"assetRef":"` + assetRef + `","photoAssetId":"` + photoID + `","sourceUrl":"` + factSignedURL + `"}`)}
}

// requirementActionOfTurn returns the Action of the Agent Run's Turn turnNo.
func requirementActionOfTurn(t *testing.T, h *agentHarness, outcome service.AdvanceOutcome, turnNo int) domain.AgentAction {
	t.Helper()
	turn := agentTurnByNo(h.ctx, t, h.uow, factAgentRunID(t, h, outcome), turnNo)
	action, found := agentActionOfTurn(h.ctx, t, h.uow, turn.ID)
	if !found {
		t.Fatalf("no action exists for turn %d", turnNo)
	}
	return action
}

// assertRequirementRejected proves the video Action of Turn turnNo was rejected in its
// claim transaction: FAILED with PRECONDITION_UNMET and exactly wantDetails, no Tool
// Attempt and no AGENT_ACTION_STARTED for it, the Agent terminated as INVALID_ACTION with
// the usual failure Events, the NodeRun and Run FAILED, no next Turn, and nothing
// dispatched. No error or Event carries the signed URL or any value in forbidden.
func assertRequirementRejected(t *testing.T, h *agentHarness, p *requirementProvider, video *requirementVideo,
	runID string, outcome service.AdvanceOutcome, turnNo int, wantDetails map[string]string, forbidden ...string,
) domain.AgentAction {
	t.Helper()
	action := requirementActionOfTurn(t, h, outcome, turnNo)
	if action.Status != domain.AgentActionFailed || action.CompletedAt == nil {
		t.Errorf("video action = status %s completed_at %v, want FAILED with completed_at", action.Status, action.CompletedAt)
	}
	if action.Error == nil || action.Error.Code != runtime.CodePreconditionUnmet {
		t.Fatalf("video action error = %+v, want %s", action.Error, runtime.CodePreconditionUnmet)
	}
	var details map[string]string
	if err := json.Unmarshal(action.Error.Details, &details); err != nil {
		t.Fatalf("decode error details %s: %v", action.Error.Details, err)
	}
	if len(details) != len(wantDetails) {
		t.Errorf("error details = %v, want exactly %v", details, wantDetails)
	}
	for key, want := range wantDetails {
		if details[key] != want {
			t.Errorf("error details[%s] = %q, want %q (details %v)", key, details[key], want, details)
		}
	}
	for _, value := range append([]string{"X-Signature", factSignedURL}, forbidden...) {
		if strings.Contains(action.Error.Message, value) || strings.Contains(string(action.Error.Details), value) {
			t.Errorf("action error carries the argument value %q: %+v", value, action.Error)
		}
	}

	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the rejected action = %+v, want none", attempts)
	}
	if got := video.calls.Load(); got != 0 {
		t.Errorf("video executor calls = %d, want 0", got)
	}
	if got := p.dispatches.Load(); got != 0 {
		t.Errorf("mock provider task dispatches = %d, want 0", got)
	}

	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Errorf("agent run termination = %s, want INVALID_ACTION", agentTermination(agentRun))
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, turnNo+1) {
		t.Errorf("a Turn %d exists after the rejected Action", turnNo+1)
	}
	if got := agentNodeRun(h.ctx, t, h.uow, runID).Status; got != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", got)
	}
	if got := agentRunRow(h.ctx, t, h.uow, runID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}

	events := listEvents(h.ctx, t, h.uow, runID)
	if started := agentPayloadsFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID); len(started) != 0 {
		t.Errorf("AGENT_ACTION_STARTED for the rejected action = %v, want none", started)
	}
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureSyncExecution) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want SYNC_EXECUTION", failed["failureSource"])
	}
	if errorObject, _ := failed["error"].(map[string]any); errorObject["code"] != runtime.CodePreconditionUnmet {
		t.Errorf("AGENT_ACTION_FAILED error = %v, want code %s", failed["error"], runtime.CodePreconditionUnmet)
	}
	if agentFailed := agentLastEventPayload(t, events, domain.EventAgentFailed); agentFailed["termination"] != string(domain.TerminationInvalidAction) {
		t.Errorf("AGENT_FAILED termination = %v, want INVALID_ACTION", agentFailed["termination"])
	}
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentFailed) != 1 || agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_FAILED and one NODE_FAILED", types)
	}
	assertNoSignedURL(t, h, runID, agentRun.ID)
	for _, event := range events {
		for _, value := range forbidden {
			if strings.Contains(string(event.Payload), value) {
				t.Errorf("event %s %s carries the argument value %q: %s", event.ID, event.Type, value, event.Payload)
			}
		}
	}
	return action
}

// ---------------------------------------------------------------------------
// Rejection at claim
// ---------------------------------------------------------------------------

// TestAgentActionRequirements_NoFact_RejectsAtClaimWithoutDispatch covers a protected Tool
// requested before anything established the facts it needs: the first requirement finds
// no fact about the asset, the Action fails with PRECONDITION_UNMET and reason NO_FACT in
// its claim transaction, the Agent terminates as INVALID_ACTION, no Tool Attempt exists,
// and the Mock Provider never sees a task.
func TestAgentActionRequirements_NoFact_RejectsAtClaimWithoutDispatch(t *testing.T) {
	p := newRequirementProvider(t)
	video := newRequirementVideo(p)
	h := requirementSetup{generate: &factScriptedTool{}, review: &factScriptedTool{}, video: video}.harness(t)
	run, outcome := startRequirementRun(h, "wf-requirement-no-fact", requirementVideoCall(factGeneratedID, factPhotoID))
	h.execute(outcome)

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationInvalidAction, domain.FailureSyncExecution)
	if action.Error == nil || action.Error.Code != runtime.CodePreconditionUnmet {
		t.Fatalf("action error = %+v, want %s", action.Error, runtime.CodePreconditionUnmet)
	}
	assertRequirementRejected(t, h, p, video, run.ID, outcome, 1, map[string]string{
		"factType": factTypeGenerated, "subject": factGeneratedID, "reason": string(runtime.RequirementNoFact),
	})
}

// TestAgentActionRequirements_OtherPhotosFacts_RejectsMismatch covers facts that exist
// for a different photo. Asking for a video of photo A's generated asset while naming
// photo B breaks the photoAssetId binding of the generation fact; asking for an asset
// generated from photo B, of which there is none, finds no fact about that subject. Photo
// A's passing review satisfies neither.
func TestAgentActionRequirements_OtherPhotosFacts_RejectsMismatch(t *testing.T) {
	cases := []struct {
		name        string
		call        mockmodel.Scenario
		wantDetails map[string]string
		forbidden   []string
	}{
		{
			name: "binding",
			call: requirementVideoCall(factGeneratedID, requirementOtherPhotoID),
			wantDetails: map[string]string{
				"factType": factTypeGenerated, "subject": factGeneratedID,
				"reason": string(runtime.RequirementBindingMismatch), "binding": "photoAssetId",
			},
			forbidden: []string{requirementOtherPhotoID},
		},
		{
			name: "subject",
			call: requirementVideoCall(requirementOtherAssetID, requirementOtherPhotoID),
			wantDetails: map[string]string{
				"factType": factTypeGenerated, "subject": requirementOtherAssetID, "reason": string(runtime.RequirementNoFact),
			},
			forbidden: []string{requirementOtherPhotoID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newRequirementProvider(t)
			video := newRequirementVideo(p)
			review := &factScriptedTool{output: factReviewOutput(true)}
			h := requirementSetup{generate: &factScriptedTool{output: factGenerateOutput()}, review: review, video: video}.harness(t)
			run, outcome := startRequirementRun(h, "wf-requirement-other-photo-"+tc.name,
				factGenerateCall(), factReviewCall(), tc.call)
			h.execute(outcome)

			if review.calls.Load() != 1 {
				t.Fatalf("review calls = %d, want 1: photo A's review must have passed", review.calls.Load())
			}
			assertRequirementRejected(t, h, p, video, run.ID, outcome, 3, tc.wantDetails, tc.forbidden...)
		})
	}
}

// TestAgentActionRequirements_FailedReview_RejectsVerdictMismatch covers a review that
// did not pass: the generation requirement holds, the review requirement finds the
// review fact about the asset but its verdict is false, so the claim rejects the video
// with VERDICT_MISMATCH.
func TestAgentActionRequirements_FailedReview_RejectsVerdictMismatch(t *testing.T) {
	p := newRequirementProvider(t)
	video := newRequirementVideo(p)
	h := requirementSetup{
		generate: &factScriptedTool{output: factGenerateOutput()},
		review:   &factScriptedTool{output: factReviewOutput(false)},
		video:    video,
	}.harness(t)
	run, outcome := startRequirementRun(h, "wf-requirement-verdict",
		factGenerateCall(), factReviewCall(), requirementVideoCall(factGeneratedID, factPhotoID))
	h.execute(outcome)

	assertRequirementRejected(t, h, p, video, run.ID, outcome, 3, map[string]string{
		"factType": factTypeReviewed, "subject": factGeneratedID, "reason": string(runtime.RequirementVerdictMismatch),
	})
}

// TestAgentActionRequirements_NewerFailedReview_OverridesOlderPass covers the
// newest-fact rule against PostgreSQL ordering: a passing review followed by a failing
// one about the same asset leaves the failing one in force, so the video is rejected even
// though an approving fact still exists. The clock moves on inside each review so the
// two facts are ordered by time rather than by identifier.
func TestAgentActionRequirements_NewerFailedReview_OverridesOlderPass(t *testing.T) {
	p := newRequirementProvider(t)
	video := newRequirementVideo(p)
	review := &factSequenceTool{outputs: []json.RawMessage{factReviewOutput(true), factReviewOutput(false)}}
	h := requirementSetup{generate: &factScriptedTool{output: factGenerateOutput()}, review: review, video: video}.harness(t)
	review.during = func(context.Context, registry.ToolAction) { h.clock.Advance(time.Second) }
	run, outcome := startRequirementRun(h, "wf-requirement-newer-review",
		factGenerateCall(), factReviewCall(), factReviewCall(), requirementVideoCall(factGeneratedID, factPhotoID))
	h.execute(outcome)

	var verdicts []bool
	for _, fact := range listFactsByAgentRun(h.ctx, t, h.uow, factAgentRunID(t, h, outcome)) {
		if fact.FactType == factTypeReviewed && fact.Verdict != nil {
			verdicts = append(verdicts, *fact.Verdict)
		}
	}
	if len(verdicts) != 2 {
		t.Fatalf("review verdicts = %v, want an approving and a rejecting one", verdicts)
	}
	assertRequirementRejected(t, h, p, video, run.ID, outcome, 4, map[string]string{
		"factType": factTypeReviewed, "subject": factGeneratedID, "reason": string(runtime.RequirementVerdictMismatch),
	})
}

// ---------------------------------------------------------------------------
// Satisfied requirements
// ---------------------------------------------------------------------------

// TestAgentActionRequirements_FactsHold_ClaimsAndDispatchesOnce covers the happy path:
// with the generation fact bound to the same photo and a passing review in force, the
// claim proceeds exactly as for an unprotected Tool -- one Attempt, AGENT_ACTION_STARTED,
// one dispatch to the Mock Provider -- and the Action waits for its callback.
func TestAgentActionRequirements_FactsHold_ClaimsAndDispatchesOnce(t *testing.T) {
	p := newRequirementProvider(t)
	video := newRequirementVideo(p)
	h := requirementSetup{
		generate: &factScriptedTool{output: factGenerateOutput()},
		review:   &factScriptedTool{output: factReviewOutput(true)},
		video:    video,
	}.harness(t)
	run, outcome := startRequirementRun(h, "wf-requirement-happy",
		factGenerateCall(), factReviewCall(), requirementVideoCall(factGeneratedID, factPhotoID))
	h.execute(outcome)

	action := requirementActionOfTurn(t, h, outcome, 3)
	if action.Status != domain.AgentActionWaitingCallback || action.Error != nil {
		t.Fatalf("video action = status %s error %+v, want WAITING_CALLBACK without error", action.Status, action.Error)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptDispatched {
		t.Errorf("video attempt status = %s, want DISPATCHED", attempt.Status)
	}
	if got := video.calls.Load(); got != 1 {
		t.Errorf("video executor calls = %d, want 1", got)
	}
	if got := p.dispatches.Load(); got != 1 {
		t.Errorf("mock provider task dispatches = %d, want exactly 1", got)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	agentOnlyPayloadFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID)
	if failed := agentPayloadsFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID); len(failed) != 0 {
		t.Errorf("AGENT_ACTION_FAILED for the video action = %v, want none", failed)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID).Status; got != domain.RunPaused {
		t.Errorf("run status = %s, want PAUSED while the video task is outstanding", got)
	}
	assertNoSignedURL(t, h, run.ID, factAgentRunID(t, h, outcome))
}

// ---------------------------------------------------------------------------
// Recovery and registry drift
// ---------------------------------------------------------------------------

// TestAgentActionRequirements_CrashAfterClaim_RecoveryNeverReevaluates covers a crash
// after the claim committed the RUNNING Action and its STARTED Attempt but before the
// dispatch. A newer failing review is then committed, so any second evaluation of the
// requirements would reject. Recovery goes through the single existing path: a
// reconciler pass and a direct re-entry leave the claimed Action alone, nothing is
// dispatched, no Attempt is added, and the Agent deadline finally closes the Action as
// TIMEOUT -- never as a late PRECONDITION_UNMET rejection.
func TestAgentActionRequirements_CrashAfterClaim_RecoveryNeverReevaluates(t *testing.T) {
	p := newRequirementProvider(t)
	crashedVideo := newRequirementVideo(p)
	crashedVideo.during = func(context.Context, registry.ToolAction) {
		panic("simulated crash after the video claim committed, before the dispatch")
	}
	generate := &factScriptedTool{output: factGenerateOutput()}
	review := &factScriptedTool{output: factReviewOutput(true)}
	crashed := requirementSetup{generate: generate, review: review, video: crashedVideo}.harness(t)
	run, outcome := startRequirementRun(crashed, "wf-requirement-crash",
		factGenerateCall(), factReviewCall(), requirementVideoCall(factGeneratedID, factPhotoID))
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		crashed.execute(outcome)
	}()

	action := requirementActionOfTurn(t, crashed, outcome, 3)
	attempts := agentToolAttempts(crashed.ctx, t, crashed.uow, action.ID)
	if action.Status != domain.AgentActionRunning || len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Fatalf("after the crash: action %s with attempts %+v, want RUNNING with one STARTED attempt", action.Status, attempts)
	}
	attempt := attempts[0]

	// A newer failing review of the same asset, attributed to the generation Attempt
	// (which holds no review fact yet). Any re-evaluation would now reject the video.
	agentRunID := factAgentRunID(t, crashed, outcome)
	generationAttempt := agentOnlyToolAttempt(t, crashed, requirementActionOfTurn(t, crashed, outcome, 1).ID)
	crashed.clock.Advance(time.Second)
	rejected := false
	newer := newExecutionFact("fact_newer_failed_review", run.ID, agentRunID, generationAttempt.ID, factTypeReviewed, factGeneratedID)
	newer.Binding = json.RawMessage(`{"policyVersion":"` + factPolicy + `"}`)
	newer.Verdict = &rejected
	newer.CreatedAt = crashed.clock.Now()
	insertExecutionFact(crashed.ctx, t, crashed.uow, newer)

	video := newRequirementVideo(p)
	restarted := requirementSetup{
		generate: generate, review: review, video: video, pool: crashed.pool, clock: crashed.clock,
	}.harness(t)
	rec := agentReconciler(restarted)
	agentRunOnce(restarted, rec)
	if err := restarted.svc.ExecuteAgentAction(restarted.ctx, action.ID, domain.ClaimReconciler); err != nil {
		t.Fatalf("re-enter the claimed action: %v", err)
	}
	if got := getAgentAction(restarted.ctx, t, restarted.uow, action.ID); got.Status != domain.AgentActionRunning || got.Error != nil {
		t.Fatalf("action before the deadline = status %s error %+v, want still RUNNING without error", got.Status, got.Error)
	}

	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	agentRunOnce(restarted, rec)

	closed := getAgentAction(restarted.ctx, t, restarted.uow, action.ID)
	if closed.Status != domain.AgentActionFailed || closed.Error == nil || closed.Error.Code == runtime.CodePreconditionUnmet {
		t.Errorf("action after the deadline = status %s error %+v, want FAILED by the timeout, not a requirement", closed.Status, closed.Error)
	}
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	final := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID)
	if len(final) != 1 || final[0].ID != attempt.ID || final[0].Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempts = %+v, want only %s, closed FAILED", final, attempt.ID)
	}
	if got := video.calls.Load(); got != 0 {
		t.Errorf("video executor calls after restart = %d, want 0", got)
	}
	if got := p.dispatches.Load(); got != 0 {
		t.Errorf("mock provider task dispatches = %d, want 0", got)
	}
	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	agentOnlyPayloadFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID)
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureTimeout) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want TIMEOUT", failed["failureSource"])
	}
}

// TestAgentActionRequirements_ProducerUnregistered_ConsumerUsesPersistedFacts covers
// registry drift around facts. The facts a producer committed stay authoritative after
// that producer leaves the Registry: a restarted Backend without the generation Tool
// still claims and dispatches the video once against them. Requesting the removed
// producer itself is unchanged registry drift: TOOL_NOT_REGISTERED with termination
// TOOL_ERROR.
func TestAgentActionRequirements_ProducerUnregistered_ConsumerUsesPersistedFacts(t *testing.T) {
	t.Run("consumer", func(t *testing.T) {
		p := newRequirementProvider(t)
		stop := &agentStopNotifier{}
		review := &factScriptedTool{output: factReviewOutput(true)}
		first := newAgentHarness(t, agentHarnessOptions{Notifier: stop, Tools: []registry.ToolRegistration{
			factGenerateRegistration(&factScriptedTool{output: factGenerateOutput()}),
			factReviewRegistration(review),
			requirementVideoRegistration(newRequirementVideo(p)),
		}})
		run, outcome := startRequirementRun(first, "wf-requirement-producer-drift",
			factGenerateCall(), factReviewCall(), requirementVideoCall(factGeneratedID, factPhotoID))

		// A model call needs every allowlisted Tool to resolve, so the video Decision must
		// be committed while the producer is still registered. The chain is stopped at
		// that Decision commit, leaving the video Action READY.
		stopCtx, cancel := context.WithCancel(first.ctx)
		defer cancel()
		stop.cancel = cancel
		var modelCalls atomic.Int32
		first.provider.BeforeReturn = func(context.Context) {
			if modelCalls.Add(1) == 3 {
				stop.arm()
			}
		}
		if err := first.svc.Execute(stopCtx, outcome); err != nil {
			t.Fatalf("execute the generation round: %v", err)
		}
		if err := first.drainTurns(stopCtx); !errors.Is(err, context.Canceled) {
			t.Fatalf("drain turns = %v, want context.Canceled at the video Decision commit", err)
		}
		action := requirementActionOfTurn(t, first, outcome, 3)
		if action.Status != domain.AgentActionReady || review.calls.Load() != 1 {
			t.Fatalf("before the restart: video action %s, review calls %d, want READY after one review", action.Status, review.calls.Load())
		}

		video := newRequirementVideo(p)
		restarted := requirementSetup{
			review: review, video: video, skipGenerate: true, pool: first.pool, clock: first.clock,
		}.harness(t)
		if err := restarted.svc.ExecuteAgentAction(restarted.ctx, action.ID, domain.ClaimReconciler); err != nil {
			t.Fatalf("execute the video action on the drifted registry: %v", err)
		}

		action = getAgentAction(restarted.ctx, t, restarted.uow, action.ID)
		if action.Status != domain.AgentActionWaitingCallback || action.Error != nil {
			t.Fatalf("video action = status %s error %+v, want WAITING_CALLBACK", action.Status, action.Error)
		}
		if attempt := agentOnlyToolAttempt(t, restarted, action.ID); attempt.ToolName != requirementVideoTool || attempt.Status != domain.ToolAttemptDispatched {
			t.Errorf("video attempt = %s %s, want %s DISPATCHED", attempt.ToolName, attempt.Status, requirementVideoTool)
		}
		if got := p.dispatches.Load(); got != 1 {
			t.Errorf("mock provider task dispatches = %d, want exactly 1", got)
		}
		if got := agentRunRow(restarted.ctx, t, restarted.uow, run.ID).Status; got != domain.RunPaused {
			t.Errorf("run status = %s, want PAUSED while the video task is outstanding", got)
		}
	})

	t.Run("producer", func(t *testing.T) {
		stop := &agentStopNotifier{}
		p := newRequirementProvider(t)
		first := newAgentHarness(t, agentHarnessOptions{Notifier: stop, Tools: []registry.ToolRegistration{
			factGenerateRegistration(&factScriptedTool{output: factGenerateOutput()}),
			factReviewRegistration(&factScriptedTool{}),
			requirementVideoRegistration(newRequirementVideo(p)),
		}})
		factScriptToolCalls(first, factGenerateCall())
		def := first.saveDefinition(requirementDefinition("wf-requirement-producer-removed"))
		run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
		outcome, action := agentStopAfterDecision(first, stop, run.ID)

		restarted := requirementSetup{
			review: &factScriptedTool{}, video: newRequirementVideo(p), skipGenerate: true, pool: first.pool, clock: first.clock,
		}.harness(t)
		if err := restarted.svc.ExecuteAgentAction(restarted.ctx, action.ID, domain.ClaimReconciler); err != nil {
			t.Fatalf("execute the generation action on the drifted registry: %v", err)
		}
		failed := assertAgentActionFailed(t, restarted, run.ID, outcome, domain.TerminationToolError, domain.FailureSyncExecution)
		if failed.Error == nil || failed.Error.Code != "TOOL_NOT_REGISTERED" {
			t.Errorf("action error = %+v, want TOOL_NOT_REGISTERED", failed.Error)
		}
		if attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID); len(attempts) != 0 {
			t.Errorf("tool attempts = %+v, want none", attempts)
		}
	})
}
