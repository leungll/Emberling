//go:build integration

// Image model selection tests: an Agent node selects the image model its image-model
// Tools run with. The selection is frozen into the Agent Run by the initialisation
// transaction, handed verbatim to every such Tool call, and re-resolved against the
// current Model Registry both at initialisation and at the claim of each call. A frozen
// image Model ID that no longer resolves fails explicitly -- the NodeRun at
// initialisation, the Action before any Tool Attempt at the claim -- and is never
// replaced by another registered model.
//
// They share the agentHarness of agent_loop_test.go and the generation harness of
// agent_generation_limit_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// capabilityModel registers one Model ID with the given capabilities. Registering the
// same ID with different capabilities in a restarted harness reproduces a configuration
// change across a restart. It provides no Decision; these tests never ask it for one.
type capabilityModel struct {
	id           string
	capabilities []string
}

func imageModel(id string) capabilityModel {
	return capabilityModel{id: id, capabilities: []string{domain.ModelCapabilityReferenceImageGeneration}}
}

func (m capabilityModel) Models(context.Context) ([]registry.ModelRegistration, error) {
	return []registry.ModelRegistration{{ModelMetadata: domain.ModelMetadata{
		ID:           m.id,
		DisplayName:  m.id,
		Capabilities: m.capabilities,
		ConfigSchema: json.RawMessage(`{"type":"object"}`),
	}}}, nil
}

func (capabilityModel) Generate(context.Context, registry.ModelRequest) (registry.ModelResponse, error) {
	return registry.ModelResponse{}, errors.New("capabilityModel: no Decision is expected from this model")
}

// withAgentConfig merges overrides into the named Agent node's config.
func withAgentConfig(t *testing.T, def domain.Definition, nodeID string, overrides map[string]any) domain.Definition {
	t.Helper()
	for i, node := range def.Nodes {
		if node.ID != nodeID {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(node.Config, &config); err != nil {
			t.Fatalf("decode config of %s: %v", nodeID, err)
		}
		for k, v := range overrides {
			config[k] = v
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			t.Fatalf("encode config of %s: %v", nodeID, err)
		}
		def.Nodes[i].Config = encoded
		return def
	}
	t.Fatalf("definition has no node %s", nodeID)
	return def
}

// imageAgentDefinition is the fixture graph whose Agent may call generate_image with the
// given image model.
func imageAgentDefinition(t *testing.T, workflowID, imageModelID string) domain.Definition {
	t.Helper()
	return withAgentConfig(t, agentLoopDefinition(workflowID), "node_agent", map[string]any{
		"allowedTools": []string{generateimage.ToolName},
		"imageModelId": imageModelID,
	})
}

// scriptOneGenerationPerAgent makes the first Turn of every Agent Run call generate_image
// and every later Turn finish: a first Turn is the only one whose Context holds just the
// Agent's input message.
func scriptOneGenerationPerAgent(h *agentHarness) {
	h.provider.Script = func(request registry.ModelRequest) *mockmodel.Scenario {
		if len(request.Messages) == 1 {
			call := generationCall("photo-1")
			call.Kind = mockmodel.ScenarioToolCall
			return &call
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
	}
}

// imageModelCalls records the image Model ID every generate_image call received, per
// Agent Run.
type imageModelCalls struct {
	mu    sync.Mutex
	calls map[string][]string
}

func (c *imageModelCalls) record(_ context.Context, action registry.ToolAction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string][]string{}
	}
	c.calls[action.AgentRunID] = append(c.calls[action.AgentRunID], action.ImageModelID)
}

func (c *imageModelCalls) of(agentRunID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls[agentRunID]...)
}

// assertImageModelRejectedAtClaim proves the generate_image Action failed in its claim
// transaction as a Tool failure with MODEL_NOT_REGISTERED: no Tool Attempt, no
// AGENT_ACTION_STARTED, no Executor call and no Provider request.
func assertImageModelRejectedAtClaim(t *testing.T, h *agentHarness, p *generationProvider, executor *generationExecutor, runID string, outcome service.AdvanceOutcome) {
	t.Helper()
	failed := assertAgentActionFailed(t, h, runID, outcome, domain.TerminationToolError, domain.FailureSyncExecution)
	if failed.Error == nil || failed.Error.Code != "MODEL_NOT_REGISTERED" {
		t.Errorf("action error = %+v, want MODEL_NOT_REGISTERED", failed.Error)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, failed.ID); len(attempts) != 0 {
		t.Errorf("tool attempts = %+v, want none: the image model is resolved before any Attempt exists", attempts)
	}
	if got := executor.calls.Load(); got != 0 {
		t.Errorf("generate_image executor calls = %d, want 0: no other image model is substituted", got)
	}
	if got := p.generations.Load(); got != 0 {
		t.Errorf("provider generation requests = %d, want 0", got)
	}
	events := listEvents(h.ctx, t, h.uow, runID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentActionStarted) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_STARTED", types)
	}
	failedEvent := agentEventPayload(t, events, domain.EventAgentActionFailed)
	if errObj, ok := failedEvent["error"].(map[string]any); !ok || errObj["code"] != "MODEL_NOT_REGISTERED" {
		t.Errorf("AGENT_ACTION_FAILED error = %v, want code MODEL_NOT_REGISTERED", failedEvent["error"])
	}
}

// TestAgentRun_TwoNodesDifferentImageModels_EachCallUsesFrozenID drives one Run through
// two Agent nodes that select different image models. Each Agent Run freezes its own
// node's selection, and every generate_image call of that Agent Run receives exactly that
// frozen ID.
func TestAgentRun_TwoNodesDifferentImageModels_EachCallUsesFrozenID(t *testing.T) {
	const otherImageID = "image-other-1.0"
	p := newGenerationProvider(t)
	h, executor := newGenerationHarnessWith(t, p, agentHarnessOptions{
		ModelProviders: []registry.ModelProvider{imageModel(otherImageID)},
	})
	calls := &imageModelCalls{}
	executor.during = calls.record
	scriptOneGenerationPerAgent(h)

	def := twoAgentDefinition(t, "wf-agent-two-image-models", mockmodel.ModelID, mockmodel.ModelID)
	def = withAgentConfig(t, def, "node_agent", map[string]any{
		"allowedTools": []string{generateimage.ToolName}, "imageModelId": mockmodel.ImageModelID,
	})
	def = withAgentConfig(t, def, "node_agent_2", map[string]any{
		"allowedTools": []string{generateimage.ToolName}, "imageModelId": otherImageID,
	})
	def = h.saveDefinition(def)
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	agentDrive(h, run.ID)

	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunCompleted {
		t.Fatalf("run status = %s, want COMPLETED", persisted.Status)
	}
	var nodeRuns []domain.NodeRun
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		nodeRuns, err = tx.NodeRuns().ListByRun(ctx, run.ID)
		return err
	}); err != nil {
		t.Fatalf("list node runs: %v", err)
	}
	for _, tc := range []struct{ nodeID, imageModelID string }{
		{"node_agent", mockmodel.ImageModelID},
		{"node_agent_2", otherImageID},
	} {
		nodeRun := execNodeRunByNodeID(t, nodeRuns, tc.nodeID)
		agentRun, found := agentRunOfNodeRun(h.ctx, t, h.uow, nodeRun.ID)
		if !found {
			t.Fatalf("no agent run for %s", tc.nodeID)
		}
		if got := frozenImageModelOf(agentRun); got != tc.imageModelID {
			t.Errorf("%s frozen image model = %q, want %q", tc.nodeID, got, tc.imageModelID)
		}
		if got, want := strings.Join(calls.of(agentRun.ID), ","), tc.imageModelID; got != want {
			t.Errorf("%s generate_image calls received image models %q, want %q", tc.nodeID, got, want)
		}
	}
	if got := executor.calls.Load(); got != 2 {
		t.Errorf("generate_image executor calls = %d, want 2", got)
	}
}

// TestAgentAction_ImageModelRemovedAfterRunCreated_FailsBeforeAttempt covers an image
// model removed from configuration while an Agent Run that froze it is in flight. The
// generate_image Decision commits under the original Backend; the restarted Backend no
// longer registers that image model, so the claim of the committed Action fails as a Tool
// failure with MODEL_NOT_REGISTERED before any Tool Attempt exists. Nothing reaches the
// Provider and the still-registered mock image model is not used in its place.
func TestAgentAction_ImageModelRemovedAfterRunCreated_FailsBeforeAttempt(t *testing.T) {
	const removedID = "image-removed-1.0"
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	first, _ := newGenerationHarnessWith(t, p, agentHarnessOptions{
		Notifier:       stop,
		ModelProviders: []registry.ModelProvider{imageModel(removedID)},
	})
	scriptOneGenerationPerAgent(first)
	def := first.saveDefinition(imageAgentDefinition(t, "wf-agent-image-model-removed", removedID))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentStopAfterDecision(first, stop, run.ID)

	agentRun, _ := agentRunOfNodeRun(first.ctx, t, first.uow, outcome.NodeRunID)
	if got := frozenImageModelOf(agentRun); got != removedID {
		t.Fatalf("frozen image model before the restart = %q, want %q", got, removedID)
	}

	restarted, executor := newGenerationHarnessWith(t, p, agentHarnessOptions{Pool: first.pool, Clock: first.clock})
	if err := restarted.svc.ExecuteAgentAction(restarted.ctx, action.ID, domain.ClaimReconciler); err != nil {
		t.Fatalf("execute agent action against the registry without %s: %v", removedID, err)
	}

	assertImageModelRejectedAtClaim(t, restarted, p, executor, run.ID, outcome)
	if len(restarted.provider.Requests()) != 0 {
		t.Errorf("model requests after the restart = %d, want 0: the committed Decision is not regenerated", len(restarted.provider.Requests()))
	}
	after, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if got := frozenImageModelOf(after); got != removedID {
		t.Errorf("frozen image model after the failure = %q, want %q unchanged", got, removedID)
	}
	if after.Error == nil || after.Error.Code != "MODEL_NOT_REGISTERED" || !strings.Contains(after.Error.Message, removedID) {
		t.Errorf("agent run error = %+v, want MODEL_NOT_REGISTERED naming %q", after.Error, removedID)
	}
}

// TestAgentAction_ImageToolWithoutFrozenImageModel_FailsBeforeAttempt covers an Agent Run
// whose allowlist holds generate_image but which froze no image model, as an Agent Run
// created before image models were frozen does. The claim fails explicitly instead of
// guessing a model.
func TestAgentAction_ImageToolWithoutFrozenImageModel_FailsBeforeAttempt(t *testing.T) {
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	h, executor := newGenerationHarnessWith(t, p, agentHarnessOptions{Notifier: stop})
	scriptOneGenerationPerAgent(h)
	def := h.saveDefinition(imageAgentDefinition(t, "wf-agent-image-model-unfrozen", mockmodel.ImageModelID))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentStopAfterDecision(h, stop, run.ID)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if _, err := h.pool.Exec(h.ctx, `UPDATE agent_runs SET image_model_id = NULL WHERE id = $1`, agentRun.ID); err != nil {
		t.Fatalf("clear the frozen image model: %v", err)
	}

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimReconciler); err != nil {
		t.Fatalf("execute agent action: %v", err)
	}
	assertImageModelRejectedAtClaim(t, h, p, executor, run.ID, outcome)
}

// TestAgentInit_ImageModelNotRegistered_FailsNodeRun covers an image model removed from
// configuration after a Run was created but before its Agent NodeRun started. The
// initialisation transaction fails the NodeRun with MODEL_NOT_REGISTERED and creates no
// Agent Run.
func TestAgentInit_ImageModelNotRegistered_FailsNodeRun(t *testing.T) {
	const removedID = "image-removed-1.0"
	p := newGenerationProvider(t)
	first, _ := newGenerationHarnessWith(t, p, agentHarnessOptions{
		ModelProviders: []registry.ModelProvider{imageModel(removedID)},
	})
	def := first.saveDefinition(imageAgentDefinition(t, "wf-agent-init-image-model", removedID))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	first.execute(first.advance(run.ID))

	restarted, _ := newGenerationHarnessWith(t, p, agentHarnessOptions{Pool: first.pool, Clock: first.clock})
	assertAgentInitFailed(t, restarted, run.ID, removedID)
}

// TestAgentInit_ModelLacksStructuredDecision_FailsNodeRun covers a decision Model ID whose
// registration no longer declares structured_decision after a restart: the Agent cannot
// be driven by it, so the NodeRun fails at initialisation instead of asking it for a
// Decision.
func TestAgentInit_ModelLacksStructuredDecision_FailsNodeRun(t *testing.T) {
	const modelID = "decider-1.0"
	first := newAgentHarness(t, agentHarnessOptions{ModelProviders: []registry.ModelProvider{capabilityModel{
		id: modelID, capabilities: []string{domain.ModelCapabilityTextGeneration, domain.ModelCapabilityStructuredDecision},
	}}})
	def := first.saveDefinition(withAgentConfig(t, agentLoopDefinition("wf-agent-init-decision-capability"), "node_agent",
		map[string]any{"modelId": modelID}))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	first.execute(first.advance(run.ID))

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: first.pool, Clock: first.clock, ModelProviders: []registry.ModelProvider{
		capabilityModel{id: modelID, capabilities: []string{domain.ModelCapabilityReferenceImageGeneration}},
	}})
	assertAgentInitFailed(t, restarted, run.ID, modelID)
}

// assertAgentInitFailed claims the Agent NodeRun and proves the initialisation
// transaction failed it with MODEL_NOT_REGISTERED naming modelID, creating no Agent Run.
func assertAgentInitFailed(t *testing.T, h *agentHarness, runID, modelID string) {
	t.Helper()
	if outcome := h.advance(runID); outcome.Claimed {
		t.Errorf("advance reported a usable claim for an agent whose model cannot be resolved: %+v", outcome)
	}
	nodeRun := agentNodeRun(h.ctx, t, h.uow, runID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if nodeRun.Error == nil || nodeRun.Error.Code != "MODEL_NOT_REGISTERED" || !strings.Contains(nodeRun.Error.Message, modelID) {
		t.Errorf("agent node run error = %+v, want MODEL_NOT_REGISTERED naming %q", nodeRun.Error, modelID)
	}
	if _, found := agentRunOfNodeRun(h.ctx, t, h.uow, nodeRun.ID); found {
		t.Errorf("an agent run was created although its model cannot be resolved")
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, runID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, runID), nodeRun.ID)
	if agentCountEventType(types, domain.EventAgentStarted) != 0 || agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("agent node run events = %v, want one NODE_FAILED and no AGENT_STARTED", types)
	}
}

func frozenImageModelOf(agentRun domain.AgentRun) string {
	if agentRun.ImageModelID == nil {
		return ""
	}
	return *agentRun.ImageModelID
}
