package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// factsFakeTx serves only the execution fact reads; any other repository use panics on
// the nil embedded interface, proving the assembly touches nothing else.
type factsFakeTx struct {
	store.Tx
	facts *factsFakeRepo
}

func (t factsFakeTx) ExecutionFacts() store.ExecutionFactRepository { return t.facts }

type factsFakeRepo struct {
	store.ExecutionFactRepository
	byType map[string][]domain.ExecutionFact
	calls  []string
}

func (r *factsFakeRepo) ListByRunAndType(_ context.Context, runID, factType string, limit int) ([]domain.ExecutionFact, error) {
	r.calls = append(r.calls, fmt.Sprintf("%s/%s/%d", runID, factType, limit))
	rows := r.byType[factType]
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func factsTestFacts(factType string, n int) []domain.ExecutionFact {
	out := make([]domain.ExecutionFact, n)
	for i := range out {
		out[i] = domain.ExecutionFact{
			ID: fmt.Sprintf("fact_%s_%03d", factType, i), RunID: "run_1", FactType: factType,
			SubjectRef: fmt.Sprintf("asset_%d", i), Binding: json.RawMessage(`{}`),
			CreatedAt: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
		}
	}
	return out
}

func factsTestService(t *testing.T, factInputs []string) *ExecutionService {
	t.Helper()
	nodes := registry.NewNodeRegistry()
	reg := textinput.Registration()
	if err := nodes.Register(reg); err != nil {
		t.Fatalf("register text_input: %v", err)
	}
	probe := textinput.Registration()
	probe.Metadata.Type = "fact_probe"
	probe.Metadata.FactInputs = factInputs
	if err := nodes.Register(probe); err != nil {
		t.Fatalf("register fact_probe: %v", err)
	}
	return &ExecutionService{deps: Deps{Nodes: nodes}}
}

func TestNodeInputFacts_NodeWithoutFactInputs_ReadsNothing(t *testing.T) {
	svc := factsTestService(t, []string{"image_generated"})
	repo := &factsFakeRepo{}

	for _, nodeType := range []string{"text_input", "not_registered"} {
		facts, err := svc.nodeInputFacts(context.Background(), factsFakeTx{facts: repo}, "run_1", nodeType)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", nodeType, err)
		}
		if facts != nil {
			t.Fatalf("%s: want no facts, got %+v", nodeType, facts)
		}
	}
	if len(repo.calls) != 0 {
		t.Fatalf("a node without FactInputs must not read facts, got reads %v", repo.calls)
	}
}

func TestNodeInputFacts_DeclaredTypes_EachReadBoundedForThisRun(t *testing.T) {
	svc := factsTestService(t, []string{"image_generated", "asset_reviewed"})
	repo := &factsFakeRepo{byType: map[string][]domain.ExecutionFact{
		"image_generated": factsTestFacts("image_generated", 2),
		"other_fact":      factsTestFacts("other_fact", 1),
	}}

	facts, err := svc.nodeInputFacts(context.Background(), factsFakeTx{facts: repo}, "run_1", "fact_probe")
	if err != nil {
		t.Fatalf("nodeInputFacts: %v", err)
	}
	wantCalls := []string{
		fmt.Sprintf("run_1/image_generated/%d", maxNodeInputFactsPerType+1),
		fmt.Sprintf("run_1/asset_reviewed/%d", maxNodeInputFactsPerType+1),
	}
	if fmt.Sprint(repo.calls) != fmt.Sprint(wantCalls) {
		t.Fatalf("reads: got %v, want %v", repo.calls, wantCalls)
	}
	if len(facts) != 2 {
		t.Fatalf("want exactly the two declared types, got %+v", facts)
	}
	if got := facts["image_generated"]; len(got.Facts) != 2 || got.Truncated {
		t.Fatalf("image_generated: got %+v, want two complete facts", got)
	}
	reviewed, ok := facts["asset_reviewed"]
	if !ok || reviewed.Facts == nil || len(reviewed.Facts) != 0 || reviewed.Truncated {
		t.Fatalf("asset_reviewed: a declared type without facts must be present and empty, got %+v (present=%v)", reviewed, ok)
	}
}

func TestBoundedFactSet_MoreRowsThanLimit_KeepsOldestAndMarksTruncated(t *testing.T) {
	rows := factsTestFacts("image_generated", 4)

	full := boundedFactSet(rows[:3], 3)
	if len(full.Facts) != 3 || full.Truncated {
		t.Fatalf("exactly the limit: got %d facts truncated=%v, want 3 complete", len(full.Facts), full.Truncated)
	}

	over := boundedFactSet(rows, 3)
	if !over.Truncated || len(over.Facts) != 3 || over.Facts[0].ID != rows[0].ID || over.Facts[2].ID != rows[2].ID {
		t.Fatalf("over the limit: got %+v, want the oldest three and truncated", over)
	}
}
