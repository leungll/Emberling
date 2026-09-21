package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func newDefsTestQueryService(tx *defsFakeTx) *QueryService {
	return NewQueryService(Deps{UoW: &defsFakeUoW{tx: tx}})
}

func TestQueryService_Events_LimitClampedToMaximum(t *testing.T) {
	tx := newDefsFakeTx()
	tx.runs.byID["run_1"] = domain.Run{ID: "run_1"}
	svc := newDefsTestQueryService(tx)

	if _, err := svc.Events(context.Background(), "run_1", 0, 5000); err != nil {
		t.Fatalf("Events() unexpected error: %v", err)
	}
	if tx.events.lastLimit != maxEventsLimit {
		t.Errorf("ListAfter limit = %d, want clamped to %d", tx.events.lastLimit, maxEventsLimit)
	}

	if _, err := svc.Events(context.Background(), "run_1", 0, 0); err != nil {
		t.Fatalf("Events() unexpected error: %v", err)
	}
	if tx.events.lastLimit != defaultEventsLimit {
		t.Errorf("ListAfter limit = %d, want default %d when requested limit <= 0", tx.events.lastLimit, defaultEventsLimit)
	}
}

func TestQueryService_Events_UnknownRun_ReturnsNotFound(t *testing.T) {
	tx := newDefsFakeTx()
	svc := newDefsTestQueryService(tx)

	_, err := svc.Events(context.Background(), "does_not_exist", 0, 10)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Events() error = %v, want errors.Is(err, domain.ErrNotFound)", err)
	}
}

func TestQueryService_NodeRunDetail_ForeignRun_ReturnsNotFound(t *testing.T) {
	tx := newDefsFakeTx()
	tx.nodeRuns.byID["nr_1"] = domain.NodeRun{ID: "nr_1", RunID: "run_other"}
	svc := newDefsTestQueryService(tx)

	_, err := svc.NodeRunDetail(context.Background(), "run_mine", "nr_1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("NodeRunDetail() error = %v, want errors.Is(err, domain.ErrNotFound) for a NodeRun belonging to a different Run", err)
	}
}

func TestQueryService_NodeRunDetail_OwnedRun_ReturnsAttempts(t *testing.T) {
	tx := newDefsFakeTx()
	tx.nodeRuns.byID["nr_1"] = domain.NodeRun{ID: "nr_1", RunID: "run_mine"}
	tx.attempts.byNodeRun["nr_1"] = []domain.NodeAttempt{{ID: "attempt_1", NodeRunID: "nr_1", AttemptNo: 1}}
	svc := newDefsTestQueryService(tx)

	detail, err := svc.NodeRunDetail(context.Background(), "run_mine", "nr_1")
	if err != nil {
		t.Fatalf("NodeRunDetail() unexpected error: %v", err)
	}
	if detail.NodeRun.ID != "nr_1" {
		t.Errorf("NodeRun.ID = %q, want nr_1", detail.NodeRun.ID)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].ID != "attempt_1" {
		t.Errorf("Attempts = %+v, want exactly attempt_1", detail.Attempts)
	}
}

// defsFakeNodeExecutor is a minimal registry.NodeExecutor for CatalogService's registration
// fixtures. It is never invoked: CatalogService only reads Metadata.
type defsFakeNodeExecutor struct{}

func (defsFakeNodeExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }
func (defsFakeNodeExecutor) Execute(context.Context, registry.NodeInput, map[string]any) (registry.NodeResult, error) {
	return registry.NodeResult{}, errDefsFakeNotImplemented
}

func TestCatalogService_NodeTypes_ReturnsRegisteredMetadataInStableOrder(t *testing.T) {
	nodes := registry.NewNodeRegistry()
	permissive := json.RawMessage(`{"type":"object"}`)
	// Registered out of alphabetical order on purpose, so the test would fail if
	// ListMetadata (and therefore CatalogService.NodeTypes) did not sort.
	typesInRegistrationOrder := []string{"text_output", "image_generation", "agent_review"}
	for _, typ := range typesInRegistrationOrder {
		err := nodes.Register(registry.NodeRegistration{
			Metadata: domain.NodeMetadata{
				Type:          typ,
				DisplayName:   typ,
				Category:      domain.NodeCategoryOutput,
				ExecutionKind: domain.NodeExecutionSync,
				ConfigSchema:  permissive,
				SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			},
			Binding: registry.ExecutorBinding{Executor: defsFakeNodeExecutor{}},
		})
		if err != nil {
			t.Fatalf("Register(%s): %v", typ, err)
		}
	}

	svc := NewCatalogService(Deps{Nodes: nodes, Models: registry.NewModelRegistry(), Tools: registry.NewToolRegistry()})

	got := svc.NodeTypes()
	if len(got) != len(typesInRegistrationOrder) {
		t.Fatalf("NodeTypes() = %d entries, want %d", len(got), len(typesInRegistrationOrder))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Type > got[i].Type {
			t.Fatalf("NodeTypes() not sorted by Type: %q before %q", got[i-1].Type, got[i].Type)
		}
	}
	// Calling twice must reproduce the exact same order (determinism, not accidental
	// map-iteration luck).
	again := svc.NodeTypes()
	for i := range got {
		if got[i].Type != again[i].Type {
			t.Fatalf("NodeTypes() order changed between calls: %q vs %q at index %d", got[i].Type, again[i].Type, i)
		}
	}
}
