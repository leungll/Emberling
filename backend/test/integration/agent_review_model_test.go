//go:build integration

// Review model selection tests: an Agent node may select the vision model its review Tools
// run with. The selection is frozen into the Agent Run by the initialisation transaction,
// handed verbatim to every Tool that declares UsesReviewModel, and re-resolved against the
// current Model Registry at initialisation and at each claim. A selection that does not
// resolve, or no longer declares vision input, fails explicitly and never falls back to
// the mock review policy, which applies only when no review model was selected.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
)

const (
	reviewProbeTool = "review_probe"
	reviewModelID   = "vision-review-1.0"
)

func visionModel(id string) capabilityModel {
	return capabilityModel{id: id, capabilities: []string{domain.ModelCapabilityVisionInput}}
}

// reviewProbe is a Tool that declares UsesReviewModel and records the review Model ID
// each call receives, so these tests observe the claim's resolution and nothing else.
type reviewProbe struct {
	mu    sync.Mutex
	calls atomic.Int64
	seen  []string
}

func (p *reviewProbe) Execute(_ context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.seen = append(p.seen, action.ReviewModelID)
	p.mu.Unlock()
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: json.RawMessage(`{}`)}}, nil
}

func (p *reviewProbe) received() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func (p *reviewProbe) registration() registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:            reviewProbeTool,
			Description:     "Record the review model a call receives",
			InputSchema:     json.RawMessage(`{"type":"object"}`),
			OutputSchema:    json.RawMessage(`{"type":"object"}`),
			SideEffect:      domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind:   domain.ToolExecutionSync,
			Requires:        []domain.FactRequirement{},
			UsesReviewModel: true,
		},
		Executor: p,
	}
}

// newReviewHarness wires an Agent harness with the review probe Tool and extra models.
func newReviewHarness(t *testing.T, probe *reviewProbe, opts agentHarnessOptions, models ...registry.ModelProvider) *agentHarness {
	t.Helper()
	opts.Tools = append(opts.Tools, probe.registration())
	opts.ModelProviders = append(opts.ModelProviders, models...)
	h := newAgentHarness(t, opts)
	h.provider.Script = func(request registry.ModelRequest) *mockmodel.Scenario {
		if len(request.Messages) == 1 {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: reviewProbeTool, ToolArguments: json.RawMessage(`{}`)}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
	}
	return h
}

func reviewAgentDefinition(t *testing.T, workflowID string, overrides map[string]any) domain.Definition {
	t.Helper()
	config := map[string]any{"allowedTools": []string{reviewProbeTool}}
	for k, v := range overrides {
		config[k] = v
	}
	return withAgentConfig(t, agentLoopDefinition(workflowID), "node_agent", config)
}

func frozenReviewModelOf(agentRun domain.AgentRun) string {
	if agentRun.ReviewModelID == nil {
		return ""
	}
	return *agentRun.ReviewModelID
}

// TestAgentRun_ReviewModelSelected_FrozenAndPassedToReviewTool covers the happy path: the
// Agent Run freezes the selected review model and the review Tool call receives it.
func TestAgentRun_ReviewModelSelected_FrozenAndPassedToReviewTool(t *testing.T) {
	probe := &reviewProbe{}
	h := newReviewHarness(t, probe, agentHarnessOptions{}, visionModel(reviewModelID))
	def := h.saveDefinition(reviewAgentDefinition(t, "wf-agent-review-model", map[string]any{"reviewModelId": reviewModelID}))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	agentDrive(h, run.ID)

	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunCompleted {
		t.Fatalf("run status = %s, want COMPLETED", persisted.Status)
	}
	agentRun, found := agentRunOfNodeRun(h.ctx, t, h.uow, agentNodeRun(h.ctx, t, h.uow, run.ID).ID)
	if !found {
		t.Fatal("no agent run")
	}
	if got := frozenReviewModelOf(agentRun); got != reviewModelID {
		t.Errorf("frozen review model = %q, want %q", got, reviewModelID)
	}
	if got := strings.Join(probe.received(), ","); got != reviewModelID {
		t.Errorf("review tool received %q, want %q", got, reviewModelID)
	}
}

// TestAgentRun_NoReviewModel_ToolReceivesNone covers the mock policy path: an Agent node
// that selects no review model freezes none, and its review Tool receives no model ID.
func TestAgentRun_NoReviewModel_ToolReceivesNone(t *testing.T) {
	probe := &reviewProbe{}
	h := newReviewHarness(t, probe, agentHarnessOptions{})
	def := h.saveDefinition(reviewAgentDefinition(t, "wf-agent-no-review-model", nil))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	agentDrive(h, run.ID)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, agentNodeRun(h.ctx, t, h.uow, run.ID).ID)
	if agentRun.ReviewModelID != nil {
		t.Errorf("frozen review model = %q, want none", *agentRun.ReviewModelID)
	}
	if got := probe.received(); len(got) != 1 || got[0] != "" {
		t.Errorf("review tool received %q, want one call with no review model", got)
	}
}

// TestCreateRun_ReviewModelUnusable_RejectedWithoutMockFallback covers a reviewModelId
// that is not registered, or registered without vision input: creating a Run fails
// validation instead of running the review Tools on the mock policy.
func TestCreateRun_ReviewModelUnusable_RejectedWithoutMockFallback(t *testing.T) {
	for name, tc := range map[string]struct {
		modelID  string
		wantCode string
	}{
		"unregistered":       {modelID: "vision-missing-1.0", wantCode: runtime.CodeModelNotFound},
		"lacks vision input": {modelID: mockmodel.ModelID, wantCode: runtime.CodeSemanticValidationFailed},
	} {
		t.Run(name, func(t *testing.T) {
			probe := &reviewProbe{}
			h := newReviewHarness(t, probe, agentHarnessOptions{})
			def := h.saveDefinition(reviewAgentDefinition(t, "wf-agent-review-unusable", map[string]any{"reviewModelId": tc.modelID}))
			_, err := h.svc.CreateRun(h.ctx, service.CreateRun{
				WorkflowID: def.WorkflowID, DefinitionVersion: def.Version, Input: json.RawMessage(`{"question":"` + agentQuestion + `"}`),
			})
			var resolution *service.RegistryResolutionError
			if !errors.As(err, &resolution) {
				t.Fatalf("CreateRun error = %v, want a registry resolution error", err)
			}
			found := false
			for _, item := range resolution.Underlying.Errors {
				if item.Path == "nodes[node_agent].config.reviewModelId" && item.Code == tc.wantCode {
					found = true
				}
			}
			if !found {
				t.Errorf("validation errors = %+v, want %s at reviewModelId", resolution.Underlying.Errors, tc.wantCode)
			}
			if got := probe.calls.Load(); got != 0 {
				t.Errorf("review tool calls = %d, want 0", got)
			}
		})
	}
}

// TestAgentInit_ReviewModelNotRegistered_FailsNodeRun covers a review model removed from
// configuration after a Run was created but before its Agent NodeRun started: the
// initialisation transaction fails the NodeRun and creates no Agent Run.
func TestAgentInit_ReviewModelNotRegistered_FailsNodeRun(t *testing.T) {
	const removedID = "vision-removed-1.0"
	probe := &reviewProbe{}
	first := newReviewHarness(t, probe, agentHarnessOptions{}, visionModel(removedID))
	def := first.saveDefinition(reviewAgentDefinition(t, "wf-agent-init-review-model", map[string]any{"reviewModelId": removedID}))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	first.execute(first.advance(run.ID))

	restarted := newReviewHarness(t, probe, agentHarnessOptions{Pool: first.pool, Clock: first.clock})
	assertAgentInitFailed(t, restarted, run.ID, removedID)
}

// TestAgentInit_ReviewModelLostVisionInput_FailsNodeRun covers a review model whose
// registration no longer declares vision input after a restart.
func TestAgentInit_ReviewModelLostVisionInput_FailsNodeRun(t *testing.T) {
	probe := &reviewProbe{}
	first := newReviewHarness(t, probe, agentHarnessOptions{}, visionModel(reviewModelID))
	def := first.saveDefinition(reviewAgentDefinition(t, "wf-agent-init-review-capability", map[string]any{"reviewModelId": reviewModelID}))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	first.execute(first.advance(run.ID))

	restarted := newReviewHarness(t, probe, agentHarnessOptions{Pool: first.pool, Clock: first.clock}, capabilityModel{
		id: reviewModelID, capabilities: []string{domain.ModelCapabilityStructuredDecision},
	})
	assertAgentInitFailed(t, restarted, run.ID, reviewModelID)
}

// TestAgentAction_ReviewModelRemovedAfterRunCreated_FailsBeforeAttempt covers a review
// model removed while an Agent Run that froze it is in flight. The claim of the committed
// review Action fails as a Tool failure with MODEL_NOT_REGISTERED before any Tool Attempt
// exists, and the Tool is never called on the mock policy instead.
func TestAgentAction_ReviewModelRemovedAfterRunCreated_FailsBeforeAttempt(t *testing.T) {
	const removedID = "vision-removed-1.0"
	probe := &reviewProbe{}
	stop := &agentStopNotifier{}
	first := newReviewHarness(t, probe, agentHarnessOptions{Notifier: stop}, visionModel(removedID))
	def := first.saveDefinition(reviewAgentDefinition(t, "wf-agent-review-model-removed", map[string]any{"reviewModelId": removedID}))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentStopAfterDecision(first, stop, run.ID)

	restarted := newReviewHarness(t, probe, agentHarnessOptions{Pool: first.pool, Clock: first.clock})
	if err := restarted.svc.ExecuteAgentAction(restarted.ctx, action.ID, domain.ClaimReconciler); err != nil {
		t.Fatalf("execute agent action without %s: %v", removedID, err)
	}

	failed := assertAgentActionFailed(t, restarted, run.ID, outcome, domain.TerminationToolError, domain.FailureSyncExecution)
	if failed.Error == nil || failed.Error.Code != "MODEL_NOT_REGISTERED" || !strings.Contains(failed.Error.Message, removedID) {
		t.Errorf("action error = %+v, want MODEL_NOT_REGISTERED naming %q", failed.Error, removedID)
	}
	if attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, failed.ID); len(attempts) != 0 {
		t.Errorf("tool attempts = %+v, want none", attempts)
	}
	if got := probe.calls.Load(); got != 0 {
		t.Errorf("review tool calls = %d, want 0: the mock policy is never substituted", got)
	}
	after, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if got := frozenReviewModelOf(after); got != removedID {
		t.Errorf("frozen review model after the failure = %q, want %q unchanged", got, removedID)
	}
}
