//go:build integration

// Multi-model Agent tests: several real decision models registered behind one
// OpenAI-compatible endpoint, each under its own stable Model ID. They prove that every
// Agent node uses the Model ID frozen into its own Agent Run, verbatim, and that a Model
// ID removed from configuration fails the next Turn explicitly instead of being replaced.
// The Provider is an httptest stand-in; no live credential is involved.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

const multiModelAPIKey = "sk-multi-model-test"

// upstreamDecision is one scripted answer in the strict Decision form the Adapter asks
// for: arguments is a JSON-encoded string, output is the plain answer text of a text-form
// Agent, and either is null when it does not apply.
type upstreamDecision struct {
	Kind       string  `json:"kind"`
	Tool       *string `json:"tool"`
	Arguments  *string `json:"arguments"`
	Output     *string `json:"output"`
	StatePatch *string `json:"statePatch"`
}

func upstreamToolCall(tool, arguments string) upstreamDecision {
	return upstreamDecision{Kind: "TOOL_CALL", Tool: &tool, Arguments: &arguments}
}

func upstreamFinal(output string) upstreamDecision {
	return upstreamDecision{Kind: "FINAL", Output: &output}
}

// multiModelUpstream is a Chat Completions stand-in shared by every registered model. It
// answers per upstream model name from a script, one entry per call, and records the
// model name of every request in arrival order.
type multiModelUpstream struct {
	t       *testing.T
	mu      sync.Mutex
	scripts map[string][]upstreamDecision
	seen    []string
	server  *httptest.Server
}

func newMultiModelUpstream(t *testing.T, scripts map[string][]upstreamDecision) *multiModelUpstream {
	t.Helper()
	u := &multiModelUpstream{t: t, scripts: scripts}
	u.server = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.server.Close)
	return u
}

func (u *multiModelUpstream) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || r.Header.Get("Authorization") != "Bearer "+multiModelAPIKey {
		http.Error(w, `{"error":{"type":"invalid_request"}}`, http.StatusBadRequest)
		return
	}
	u.mu.Lock()
	u.seen = append(u.seen, body.Model)
	script := u.scripts[body.Model]
	var next *upstreamDecision
	if len(script) > 0 {
		next = &script[0]
		u.scripts[body.Model] = script[1:]
	}
	u.mu.Unlock()
	if next == nil {
		http.Error(w, `{"error":{"type":"unscripted_model"}}`, http.StatusNotFound)
		return
	}
	decision, _ := json.Marshal(next)
	content, _ := json.Marshal(string(decision))
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"chatcmpl-multi","choices":[{"message":{"content":`+string(content)+`},"finish_reason":"stop"}]}`)
}

func (u *multiModelUpstream) requests() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

// recordingModel wraps one registered Provider and records the Model ID of every request
// the Runtime sends it.
type recordingModel struct {
	registry.ModelProvider
	mu  sync.Mutex
	ids []string
}

func (r *recordingModel) Generate(ctx context.Context, request registry.ModelRequest) (registry.ModelResponse, error) {
	r.mu.Lock()
	r.ids = append(r.ids, request.ModelID)
	r.mu.Unlock()
	return r.ModelProvider.Generate(ctx, request)
}

func (r *recordingModel) modelIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// newUpstreamModel builds one OpenAI-compatible Provider for id, sending upstreamName to
// the shared stand-in.
func newUpstreamModel(t *testing.T, upstream *multiModelUpstream, id, upstreamName string) *recordingModel {
	t.Helper()
	provider, err := openaimodel.New(openaimodel.Config{
		ModelID:        id,
		DisplayName:    id,
		BaseURL:        upstream.server.URL,
		APIKey:         multiModelAPIKey,
		Model:          upstreamName,
		HTTPClient:     upstream.server.Client(),
		DecisionSchema: registry.DecisionSchema(),
	})
	if err != nil {
		t.Fatalf("openaimodel.New(%s): %v", id, err)
	}
	return &recordingModel{ModelProvider: provider}
}

// withAgentModel replaces the Model ID of the named Agent node's config.
func withAgentModel(t *testing.T, def domain.Definition, nodeID, modelID string) domain.Definition {
	t.Helper()
	for i, node := range def.Nodes {
		if node.ID != nodeID {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(node.Config, &config); err != nil {
			t.Fatalf("decode config of %s: %v", nodeID, err)
		}
		config["modelId"] = modelID
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

// twoAgentDefinition chains two Agent nodes: text_input -> node_agent -> node_agent_2 ->
// text_output, each with its own Model ID.
func twoAgentDefinition(t *testing.T, workflowID, firstModel, secondModel string) domain.Definition {
	t.Helper()
	def := withAgentModel(t, agentLoopDefinition(workflowID), "node_agent", firstModel)
	second := def.Nodes[1]
	second.ID = "node_agent_2"
	second.Name = "Second Agent"
	def = withAgentModel(t, domain.Definition{
		WorkflowID: def.WorkflowID, Version: def.Version, Name: def.Name, Description: def.Description,
		Nodes:     []domain.Node{def.Nodes[0], def.Nodes[1], second, def.Nodes[2]},
		CreatedAt: def.CreatedAt,
	}, "node_agent_2", secondModel)
	def.Edges = []domain.Edge{
		{ID: "e1", Source: "node_input", SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
		{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_agent_2", TargetHandle: "input"},
		{ID: "e3", Source: "node_agent_2", SourceHandle: "text", Target: "node_output", TargetHandle: "text"},
	}
	return def
}

func agentTurnsOf(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string) []domain.AgentTurn {
	t.Helper()
	var turns []domain.AgentTurn
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		turns, err = tx.AgentTurns().ListByAgentRunID(ctx, agentRunID)
		return err
	}); err != nil {
		t.Fatalf("list turns of agent run %s: %v", agentRunID, err)
	}
	return turns
}

func turnRequestModelID(t *testing.T, turn domain.AgentTurn) string {
	t.Helper()
	var request struct {
		ModelID string `json:"modelId"`
	}
	if err := json.Unmarshal(turn.Request, &request); err != nil {
		t.Fatalf("decode request of turn %s: %v", turn.ID, err)
	}
	return request.ModelID
}

// TestAgentRun_TwoNodesDifferentModels_EachTurnUsesFrozenModelID drives one Run through
// two Agent nodes that select different models of the same Adapter. Every Turn of each
// Agent Run must carry that node's frozen Model ID, reach only the Provider registered
// under it, and send only that model's upstream name; the mock model is never used.
func TestAgentRun_TwoNodesDifferentModels_EachTurnUsesFrozenModelID(t *testing.T) {
	const (
		alphaID, alphaUpstream = "alpha-1.0", "vendor/alpha-upstream"
		betaID, betaUpstream   = "beta-2.0", "vendor/beta-upstream"
	)
	upstream := newMultiModelUpstream(t, map[string][]upstreamDecision{
		alphaUpstream: {upstreamToolCall(lookup.ToolName, `{"key":"ada"}`), upstreamFinal("alpha answer")},
		betaUpstream:  {upstreamFinal("beta answer")},
	})
	alpha := newUpstreamModel(t, upstream, alphaID, alphaUpstream)
	beta := newUpstreamModel(t, upstream, betaID, betaUpstream)
	h := newAgentHarness(t, agentHarnessOptions{ModelProviders: []registry.ModelProvider{alpha, beta}})

	def := h.saveDefinition(twoAgentDefinition(t, "wf-agent-two-models", alphaID, betaID))
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
	for _, tc := range []struct {
		nodeID, modelID string
		turns           int
	}{
		{"node_agent", alphaID, 2},
		{"node_agent_2", betaID, 1},
	} {
		nodeRun := execNodeRunByNodeID(t, nodeRuns, tc.nodeID)
		agentRun, found := agentRunOfNodeRun(h.ctx, t, h.uow, nodeRun.ID)
		if !found {
			t.Fatalf("no agent run for %s", tc.nodeID)
		}
		if agentRun.ModelID != tc.modelID {
			t.Errorf("%s frozen ModelID = %q, want %q", tc.nodeID, agentRun.ModelID, tc.modelID)
		}
		turns := agentTurnsOf(h.ctx, t, h.uow, agentRun.ID)
		if len(turns) != tc.turns {
			t.Errorf("%s turns = %d, want %d", tc.nodeID, len(turns), tc.turns)
		}
		for _, turn := range turns {
			if got := turnRequestModelID(t, turn); got != tc.modelID {
				t.Errorf("%s turn %d request modelId = %q, want the frozen %q", tc.nodeID, turn.TurnNo, got, tc.modelID)
			}
		}
	}

	if got, want := strings.Join(alpha.modelIDs(), ","), alphaID+","+alphaID; got != want {
		t.Errorf("requests reaching the %s Provider carried Model IDs %q, want %q", alphaID, got, want)
	}
	if got, want := strings.Join(beta.modelIDs(), ","), betaID; got != want {
		t.Errorf("requests reaching the %s Provider carried Model IDs %q, want %q", betaID, got, want)
	}
	if got, want := strings.Join(upstream.requests(), ","), strings.Join([]string{alphaUpstream, alphaUpstream, betaUpstream}, ","); got != want {
		t.Errorf("upstream model names in arrival order = %q, want %q", got, want)
	}
	if got := len(h.provider.Requests()); got != 0 {
		t.Errorf("mock model requests = %d, want 0: no Turn may fall back to another model", got)
	}
}

// TestAgentTurn_ModelRemovedAfterRunCreated_FailsModelErrorRetainsTrace covers a Model ID
// removed from configuration while an Agent Run that froze it is in flight. Turn 1 runs
// under the original Backend; the process dies after Turn 2 commits READY; the restarted
// Backend registers every model except that one. Its next Turn fails as MODEL_ERROR
// without any model call -- neither the removed model's upstream nor another registered
// model is asked -- while Turn 1's Decision, Action and Events remain intact.
func TestAgentTurn_ModelRemovedAfterRunCreated_FailsModelErrorRetainsTrace(t *testing.T) {
	const (
		removedID, removedUpstream = "removed-1.0", "vendor/removed-upstream"
		keptID, keptUpstream       = "kept-1.0", "vendor/kept-upstream"
	)
	upstream := newMultiModelUpstream(t, map[string][]upstreamDecision{
		removedUpstream: {upstreamToolCall(lookup.ToolName, `{"key":"ada"}`), upstreamFinal("must never be produced")},
		keptUpstream:    {upstreamFinal("must never be produced")},
	})
	first := newAgentHarness(t, agentHarnessOptions{ModelProviders: []registry.ModelProvider{
		newUpstreamModel(t, upstream, removedID, removedUpstream),
		newUpstreamModel(t, upstream, keptID, keptUpstream),
	}})
	def := first.saveDefinition(withAgentModel(t, agentLoopDefinition("wf-agent-model-removed"), "node_agent", removedID))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := first.claimAgentNode(run.ID)

	// Turn 1 decides a lookup, the Tool round completes and Turn 2 commits READY. The
	// offer of Turn 2 to the in-process queue is then lost with the process: only the
	// persisted READY Turn survives.
	if err := first.svc.Execute(first.ctx, outcome); err != nil {
		t.Fatalf("execute turn 1 under the original registry: %v", err)
	}
	first.dropPendingTurns()
	ready := listReadyTurns(first.ctx, t, first.uow)
	if len(ready) != 1 || ready[0].TurnNo != 2 {
		t.Fatalf("ready turns before the restart = %+v, want exactly Turn 2", ready)
	}
	nextTurnID := ready[0].ID
	turnOneDecision, found := agentDecisionOfTurn(first.ctx, t, first.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("turn 1 committed no decision")
	}
	eventsBefore := listEvents(first.ctx, t, first.uow, run.ID)

	kept := newUpstreamModel(t, upstream, keptID, keptUpstream)
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool: first.pool, Clock: first.clock, ModelProviders: []registry.ModelProvider{kept},
	})
	if err := restarted.svc.AdvanceAgentTurn(restarted.ctx, nextTurnID, domain.ClaimReconciler); err != nil {
		t.Fatalf("advance turn 2 against the registry without %s: %v", removedID, err)
	}

	if got := strings.Join(upstream.requests(), ","); got != removedUpstream {
		t.Errorf("upstream requests = %q, want only Turn 1's %q", got, removedUpstream)
	}
	if got := len(kept.modelIDs()); got != 0 {
		t.Errorf("requests to the still-registered %s = %d, want 0: a removed model is never replaced", keptID, got)
	}
	if got := len(restarted.provider.Requests()); got != 0 {
		t.Errorf("mock model requests = %d, want 0: a removed model is never replaced", got)
	}

	turn := getAgentTurn(restarted.ctx, t, restarted.uow, nextTurnID)
	if turn.Status != domain.AgentTurnFailed {
		t.Errorf("turn 2 status = %s, want FAILED", turn.Status)
	}
	if turn.Error == nil || turn.Error.Code != "MODEL_ERROR" || !strings.Contains(turn.Error.Message, removedID) {
		t.Errorf("turn 2 error = %+v, want MODEL_ERROR naming %q", turn.Error, removedID)
	}
	if _, found := agentDecisionOfTurn(restarted.ctx, t, restarted.uow, nextTurnID); found {
		t.Errorf("a decision was committed for the failed turn 2")
	}

	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationModelError {
		t.Errorf("agent run termination = %v, want MODEL_ERROR", agentRun.Termination)
	}
	if agentRun.ModelID != removedID {
		t.Errorf("agent run ModelID = %q, want the frozen %q unchanged", agentRun.ModelID, removedID)
	}
	if nodeRun := agentNodeRun(restarted.ctx, t, restarted.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if persisted := agentRunRow(restarted.ctx, t, restarted.uow, run.ID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	assertNoDownstreamNodeRun(restarted.ctx, t, restarted.uow, run.ID)

	// Trace retention: Turn 1 and everything it committed survive unchanged.
	turnOne := getAgentTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if turnOne.Status != domain.AgentTurnCompleted || turnRequestModelID(t, turnOne) != removedID {
		t.Errorf("turn 1 = status %s modelId %q, want COMPLETED with %q", turnOne.Status, turnRequestModelID(t, turnOne), removedID)
	}
	decision, found := agentDecisionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if !found || decision.ID != turnOneDecision.ID || decision.Kind != domain.DecisionToolCall {
		t.Errorf("turn 1 decision after the restart = %+v (found %v), want the original TOOL_CALL %s", decision, found, turnOneDecision.ID)
	}
	if action, found := agentActionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID); !found || action.Status != domain.AgentActionSucceeded {
		t.Errorf("turn 1 action = %+v (found %v), want SUCCEEDED", action, found)
	}
	eventsAfter := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	if len(eventsAfter) <= len(eventsBefore) {
		t.Fatalf("events after the failure = %d, want more than the %d before it", len(eventsAfter), len(eventsBefore))
	}
	for i, before := range eventsBefore {
		if after := eventsAfter[i]; after.Seq != before.Seq || after.Type != before.Type {
			t.Errorf("event %d changed across the restart: had seq %d %s, now seq %d %s", i, before.Seq, before.Type, after.Seq, after.Type)
		}
	}
	types := agentEventTypesFor(eventsAfter, outcome.NodeRunID)
	if len(types) < 3 ||
		types[len(types)-3] != domain.EventAgentTurnStarted ||
		types[len(types)-2] != domain.EventAgentFailed ||
		types[len(types)-1] != domain.EventNodeFailed {
		t.Errorf("agent node run events = %v, want them to end with AGENT_TURN_STARTED, AGENT_FAILED, NODE_FAILED", types)
	}
}
