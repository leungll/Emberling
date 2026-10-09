//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/prompttemplate"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

const factProbeNodeType = "fact_probe"

// factProbeExecutor passes its text through and records the facts the service attached
// to its input, so a test can see exactly what a fact-declaring node received.
type factProbeExecutor struct {
	mu       sync.Mutex
	received []map[string]registry.FactSet
}

func (*factProbeExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (e *factProbeExecutor) Execute(_ context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	e.mu.Lock()
	e.received = append(e.received, input.Facts)
	e.mu.Unlock()
	text, _ := input.Port("text")
	return registry.CompletedResult(map[string]json.RawMessage{"text": text}), nil
}

func (e *factProbeExecutor) calls() []map[string]registry.FactSet {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]registry.FactSet(nil), e.received...)
}

func factProbeDefinition(workflowID string) domain.Definition {
	return domain.Definition{
		WorkflowID: workflowID,
		Version:    1,
		Name:       "Fact Probe",
		Nodes: []domain.Node{
			{ID: "input", Type: "text_input", Name: "Brief",
				Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			{ID: "probe", Type: factProbeNodeType, Name: "Probe",
				Config: json.RawMessage(`{"template":"{{text}}"}`)},
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "input", SourceHandle: "text", Target: "probe", TargetHandle: "text"},
			{ID: "e2", Source: "probe", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: fixtureTime,
	}
}

// seedFactsOnRun records Tool Attempts on the Run's input NodeRun and commits one fact per
// entry, so the facts are this Run's own committed facts before any node of it runs.
func seedFactsOnRun(t *testing.T, h *execHarness, runID, agentRunID string, facts []domain.ExecutionFact) {
	t.Helper()
	var nodeRuns []domain.NodeRun
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		nodeRuns, err = tx.NodeRuns().ListByRun(ctx, runID)
		return err
	}); err != nil {
		t.Fatalf("list NodeRuns of %s: %v", runID, err)
	}
	tools := make([]string, len(facts))
	for i := range tools {
		tools[i] = "generate_image"
	}
	seedToolAttemptsOn(h.ctx, t, h.uow, execNodeRunByNodeID(t, nodeRuns, "input").ID, agentRunID, tools)
	for i, fact := range facts {
		fact.RunID = runID
		fact.AgentRunID = agentRunID
		fact.ToolAttemptID = seedID("tool_attempt", agentRunID, i+1)
		insertExecutionFact(h.ctx, t, h.uow, fact)
	}
}

// A node declaring fact inputs receives, in its input, exactly its own Run's committed
// facts of the declared types, oldest first; facts of an undeclared type or of another
// Run are never attached, and a declared type without facts arrives as an empty set.
func TestNodeInputFacts_DeclaringNode_ReceivesOnlyItsRunsDeclaredFacts(t *testing.T) {
	h := newExecHarness(t)
	probe := &factProbeExecutor{}
	reg := prompttemplate.Registration()
	reg.Metadata.Type = factProbeNodeType
	reg.Metadata.DisplayName = "Fact Probe"
	reg.Metadata.FactInputs = []string{"image_generated", "asset_reviewed"}
	reg.Binding = registry.ExecutorBinding{Executor: probe}
	if err := h.nodes.Register(reg); err != nil {
		t.Fatalf("register %s: %v", factProbeNodeType, err)
	}
	def := h.saveDefinition(factProbeDefinition("wf_fact_probe"))

	run := h.createRun(def.WorkflowID, def.Version, `{"brief":"three photos"}`)
	other := h.createRun(def.WorkflowID, def.Version, `{"brief":"another set"}`)

	at := func(fact domain.ExecutionFact, minutes int) domain.ExecutionFact {
		fact.CreatedAt = fixtureTime.Add(time.Duration(minutes) * time.Minute)
		return fact
	}
	seedFactsOnRun(t, h, run.ID, "ar_probe", []domain.ExecutionFact{
		at(newExecutionFact("fact_gen_late", "", "", "", "image_generated", "asset_b"), 3),
		at(newExecutionFact("fact_gen_early", "", "", "", "image_generated", "asset_a"), 1),
		at(newExecutionFact("fact_video", "", "", "", "video_generated", "asset_v"), 2),
	})
	seedFactsOnRun(t, h, other.ID, "ar_probe_other", []domain.ExecutionFact{
		at(newExecutionFact("fact_gen_other_run", "", "", "", "image_generated", "asset_x"), 0),
		at(newExecutionFact("fact_review_other_run", "", "", "", "asset_reviewed", "asset_x"), 0),
	})

	h.drain(run.ID)

	calls := probe.calls()
	if len(calls) != 1 {
		t.Fatalf("probe executions: want 1, got %d", len(calls))
	}
	got := calls[0]
	if len(got) != 2 {
		t.Fatalf("attached fact types: want exactly the two declared types, got %v", factSetKeys(got))
	}
	generated, ok := got["image_generated"]
	if !ok || generated.Truncated {
		t.Fatalf("image_generated: want a complete set, got %+v (present=%v)", generated, ok)
	}
	if ids := factIDs(generated.Facts); len(ids) != 2 || ids[0] != "fact_gen_early" || ids[1] != "fact_gen_late" {
		t.Fatalf("image_generated: want this Run's [fact_gen_early fact_gen_late], got %v", ids)
	}
	for _, fact := range generated.Facts {
		if fact.RunID != run.ID {
			t.Fatalf("image_generated: fact %s belongs to Run %s, want %s", fact.ID, fact.RunID, run.ID)
		}
	}
	reviewed, ok := got["asset_reviewed"]
	if !ok || reviewed.Truncated || len(reviewed.Facts) != 0 {
		t.Fatalf("asset_reviewed: want an empty set for this Run, got %+v (present=%v)", reviewed, ok)
	}
}

func factSetKeys(sets map[string]registry.FactSet) []string {
	keys := make([]string, 0, len(sets))
	for key := range sets {
		keys = append(keys, key)
	}
	return keys
}
