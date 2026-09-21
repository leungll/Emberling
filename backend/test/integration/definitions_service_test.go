//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// These tests cover the transactional half of DefinitionService/QueryService that a fake
// store cannot demonstrate: the version-conflict row lock in
// store/postgres/definitions.go, and the Run/NodeRuns/LastSeq mutual consistency of
// QueryService.Snapshot's single transaction. Everything else (compile-result shape,
// not-found propagation, limit clamping) is already covered by unit tests in
// internal/service against fakes.

// ---------------------------------------------------------------------------
// Fixtures and helpers specific to the Definition/Query service tests. Fixtures shared
// with the Store tests (seedRun, appendEvent, newDefinition, fixtureTime, listEvents,
// ...) live in store_test.go and are reused as-is.
// ---------------------------------------------------------------------------

// defServiceCatalog is a minimal runtime.NodeCatalog covering exactly the four Node
// Types used by test/fixtures/definitions/document_processing.json. runtime's own
// fakeCatalog (internal/runtime/catalog_test.go) is unexported and unusable from here, so
// DefinitionService's integration tests need their own.
type defServiceCatalog struct {
	metadata map[string]domain.NodeMetadata
}

func newDefServiceCatalog() *defServiceCatalog {
	permissive := json.RawMessage(`{"type":"object"}`)
	textPort := func(name string, required bool) domain.PortMetadata {
		return domain.PortMetadata{Name: name, DataType: domain.PortTypeText, Required: required}
	}
	noSideEffect := domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}

	entries := []domain.NodeMetadata{
		{
			Type:          runtime.NodeTypeTextInput,
			DisplayName:   "Text Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{textPort("text", true)},
			ConfigSchema:  permissive,
			SideEffect:    noSideEffect,
		},
		{
			Type:          runtime.NodeTypePromptTemplate,
			DisplayName:   "Prompt Template",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{textPort("text", true)},
			Outputs:       []domain.PortMetadata{textPort("text", true)},
			ConfigSchema:  permissive,
			SideEffect:    noSideEffect,
		},
		{
			Type:          runtime.NodeTypeTextGeneration,
			DisplayName:   "Text Generation",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{textPort("prompt", true)},
			Outputs:       []domain.PortMetadata{textPort("text", true)},
			ConfigSchema:  permissive,
			SideEffect:    noSideEffect,
		},
		{
			Type:          runtime.NodeTypeTextOutput,
			DisplayName:   "Text Output",
			Category:      domain.NodeCategoryOutput,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{textPort("text", true)},
			ConfigSchema:  permissive,
			SideEffect:    noSideEffect,
		},
	}

	byType := make(map[string]domain.NodeMetadata, len(entries))
	for _, m := range entries {
		byType[m.Type] = m
	}
	return &defServiceCatalog{metadata: byType}
}

func (c *defServiceCatalog) NodeMetadata(nodeType string) (domain.NodeMetadata, bool) {
	m, ok := c.metadata[nodeType]
	return m, ok
}

func (c *defServiceCatalog) ValidateSemantics(context.Context, string, map[string]any) error {
	return nil
}

// fixedClock is a deterministic domain.Clock (CLAUDE.md testing standard: "Inject clocks
// and ID generators where outcomes depend on time or identity").
type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

// sequentialIDs is a deterministic domain.IDGenerator.
type sequentialIDs struct {
	mu sync.Mutex
	n  int
}

func (g *sequentialIDs) NewID(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("%s_defsvc_%d", prefix, g.n)
}

// newDefinitionServiceDeps builds the service.Deps DefinitionService/QueryService tests
// in this file share: a real Postgres-backed UnitOfWork, the fixture-matching catalog
// above wired into a real runtime.Compiler, and deterministic Clock/IDs.
func newDefinitionServiceDeps(uow store.UnitOfWork) (service.Deps, *defServiceCatalog) {
	catalog := newDefServiceCatalog()
	clock := fixedClock{at: fixtureTime}
	return service.Deps{
		UoW:      uow,
		Compiler: runtime.NewCompiler(catalog, clock),
		Clock:    clock,
		IDs:      &sequentialIDs{},
	}, catalog
}

// documentProcessingFixture is the subset of document_processing.json DefinitionService's
// Create/Save inputs need. The fixture's own "workflowId" field is ignored: Create mints
// its own Workflow id, and Save's WorkflowID is supplied by the test per-case.
type documentProcessingFixture struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Nodes       []domain.Node `json:"nodes"`
	Edges       []domain.Edge `json:"edges"`
}

func loadDocumentProcessingFixture(t *testing.T) documentProcessingFixture {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/definitions/document_processing.json")
	if err != nil {
		t.Fatalf("read document_processing.json: %v", err)
	}
	var fx documentProcessingFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal document_processing.json: %v", err)
	}
	return fx
}

// defServiceCycleDefinition builds a two-node cycle: both Node Types are registered in
// defServiceCatalog, so Compile reaches the Graph stage and fails with DAG_HAS_CYCLE
// rather than an earlier-stage error. Mirrors internal/service/definitions_test.go's
// defsCycleDefinition, kept separate because that one lives in a different package.
func defServiceCycleDefinition() domain.Definition {
	return domain.Definition{
		Nodes: []domain.Node{
			{ID: "a", Type: runtime.NodeTypePromptTemplate, Config: json.RawMessage(`{}`)},
			{ID: "b", Type: runtime.NodeTypePromptTemplate, Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "a", SourceHandle: "text", Target: "b", TargetHandle: "text"},
			{ID: "e2", Source: "b", SourceHandle: "text", Target: "a", TargetHandle: "text"},
		},
	}
}

// assertJSONEqual compares two JSON values by decoded content rather than raw bytes.
// PostgreSQL's jsonb column re-serialises object key order on read back (verified
// directly: storing {"type":...,"additionalProperties":...,"properties":...,"required":...}
// and reading it back with psql reorders the top-level keys to
// (type, required, properties, additionalProperties) -- ascending by (key length, then
// lexicographic), not insertion order). store_execution_test.go's SetOutput test notes the
// same fact for a different column. A value that survives that round trip is exactly what
// "stored" must mean here.
func assertJSONEqual(t *testing.T, label string, got, want json.RawMessage) {
	t.Helper()
	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("%s: unmarshal got: %v (%s)", label, err, got)
	}
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("%s: unmarshal want: %v (%s)", label, err, want)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Fatalf("%s mismatch:\n got:  %s\n want: %s", label, got, want)
	}
}

// ---------------------------------------------------------------------------
// DefinitionService.Create (docs/08-interface-spec.md §3.1; §1.2 frozen RunInputSchema)
// ---------------------------------------------------------------------------

func TestDefinitionService_Create_ValidDefinition_StoresFrozenRunInputSchema(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	deps, catalog := newDefinitionServiceDeps(uow)
	svc := service.NewDefinitionService(deps)

	fx := loadDocumentProcessingFixture(t)
	created, err := svc.Create(ctx, service.CreateDefinition{
		Name:        fx.Name,
		Description: fx.Description,
		Nodes:       fx.Nodes,
		Edges:       fx.Edges,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.RunInputSchema) == 0 {
		t.Fatal("Create: RunInputSchema is empty")
	}

	// Compile the identical Nodes/Edges independently (same catalog and clock) to get the
	// schema Create should have frozen verbatim.
	compiled, err := runtime.NewCompiler(catalog, deps.Clock).Compile(ctx, domain.Definition{
		Nodes: fx.Nodes,
		Edges: fx.Edges,
	})
	if err != nil {
		t.Fatalf("independent Compile: %v", err)
	}

	// Strict byte-for-byte: the value Create returns in memory is the exact slice the
	// Compiler produced, never re-encoded by the service layer.
	if !bytes.Equal(created.RunInputSchema, compiled.RunInputSchema) {
		t.Fatalf("Create() RunInputSchema not byte-identical to the Compiler's own output:\n got:  %s\n want: %s",
			created.RunInputSchema, compiled.RunInputSchema)
	}

	// The store round trip (through the workflow_definitions jsonb column) must still be
	// semantically identical; compared by decoded value per assertJSONEqual's doc comment.
	stored, err := svc.GetVersion(ctx, created.WorkflowID, created.Version)
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	assertJSONEqual(t, "stored vs compiled RunInputSchema", stored.RunInputSchema, compiled.RunInputSchema)
}

// ---------------------------------------------------------------------------
// DefinitionService.Save version conflicts (docs/09-testing-and-acceptance.md §3.4)
// ---------------------------------------------------------------------------

func TestDefinitionService_Save_StaleBaseVersion_ReturnsVersionConflict(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	deps, _ := newDefinitionServiceDeps(uow)
	svc := service.NewDefinitionService(deps)

	fx := loadDocumentProcessingFixture(t)
	created, err := svc.Create(ctx, service.CreateDefinition{
		Name: fx.Name, Description: fx.Description, Nodes: fx.Nodes, Edges: fx.Edges,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.Save(ctx, service.SaveDefinition{
		WorkflowID:  created.WorkflowID,
		BaseVersion: created.Version + 1, // one past the real latest version (1)
		Name:        fx.Name,
		Description: fx.Description,
		Nodes:       fx.Nodes,
		Edges:       fx.Edges,
	})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("Save() with stale BaseVersion error = %v, want errors.Is(err, domain.ErrVersionConflict)", err)
	}

	versions, err := svc.ListVersions(ctx, created.WorkflowID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions after rejected Save: want 1 (nothing persisted), got %d", len(versions))
	}
}

func TestDefinitionService_Save_ConcurrentSameBaseVersion_OnlyOneWins(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	deps, _ := newDefinitionServiceDeps(uow)
	svc := service.NewDefinitionService(deps)

	fx := loadDocumentProcessingFixture(t)
	created, err := svc.Create(ctx, service.CreateDefinition{
		Name: fx.Name, Description: fx.Description, Nodes: fx.Nodes, Edges: fx.Edges,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Two racers save the same BaseVersion. DefinitionService.Save's own pre-check
	// (GetWorkflow without a lock) is not the arbiter here -- both racers can observe
	// BaseVersion==latest before either commits. store/postgres/definitions.go's Save
	// takes the workflows row FOR UPDATE, so exactly one of the two nested Save calls
	// actually serializes second and observes the row already advanced to version 2.
	const racers = 2
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(racers)
	errs := make([]error, racers)

	for i := range racers {
		go func(i int) {
			defer done.Done()
			start.Wait() // barrier: released simultaneously below, no sleeps
			_, err := svc.Save(ctx, service.SaveDefinition{
				WorkflowID:  created.WorkflowID,
				BaseVersion: created.Version,
				Name:        fmt.Sprintf("%s (racer %d)", fx.Name, i),
				Description: fx.Description,
				Nodes:       fx.Nodes,
				Edges:       fx.Edges,
			})
			errs[i] = err
		}(i)
	}
	start.Done()
	done.Wait()

	successes, conflicts := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("racer %d: unexpected error: %v", i, err)
		}
	}
	if successes != 1 {
		t.Errorf("successful Save() calls = %d, want exactly 1", successes)
	}
	if conflicts != racers-1 {
		t.Errorf("Save() calls returning domain.ErrVersionConflict = %d, want exactly %d", conflicts, racers-1)
	}

	versions, err := svc.ListVersions(ctx, created.WorkflowID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions after the race: want exactly 2 (v1 plus the single winner), got %d", len(versions))
	}
}

// TestDefinitionService_Save_CompileFailure_DoesNotCreateVersion covers
// docs/09-testing-and-acceptance.md §3.4: "Save 校验失败 | 不创建 Definition version，不改变最新版本".
// A cyclic graph reaches the Graph compile stage (both Node Types are registered) and
// fails with DAG_HAS_CYCLE, so this exercises the same rollback path a real authoring
// mistake would.
func TestDefinitionService_Save_CompileFailure_DoesNotCreateVersion(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	deps, _ := newDefinitionServiceDeps(uow)
	svc := service.NewDefinitionService(deps)

	fx := loadDocumentProcessingFixture(t)
	created, err := svc.Create(ctx, service.CreateDefinition{
		Name: fx.Name, Description: fx.Description, Nodes: fx.Nodes, Edges: fx.Edges,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cyclic := defServiceCycleDefinition()
	_, err = svc.Save(ctx, service.SaveDefinition{
		WorkflowID:  created.WorkflowID,
		BaseVersion: created.Version,
		Name:        fx.Name,
		Description: fx.Description,
		Nodes:       cyclic.Nodes,
		Edges:       cyclic.Edges,
	})
	var compileErr *service.CompileFailedError
	if !errors.As(err, &compileErr) {
		t.Fatalf("Save() with a cyclic definition error = %v, want errors.As(err, *service.CompileFailedError)", err)
	}

	versions, err := svc.ListVersions(ctx, created.WorkflowID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions after a rejected compile-failing Save: want 1 (nothing persisted), got %d", len(versions))
	}

	got, err := svc.GetVersion(ctx, created.WorkflowID, created.Version)
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if got.Name != created.Name || got.Description != created.Description {
		t.Fatalf("GetVersion after rejected Save: want the original v1 (Name=%q Description=%q), got Name=%q Description=%q",
			created.Name, created.Description, got.Name, got.Description)
	}
}

// ---------------------------------------------------------------------------
// DefinitionService.List (docs/08-interface-spec.md §3.1: name, description,
// latestVersion, updatedAt, lastRun)
// ---------------------------------------------------------------------------

func TestDefinitionService_List_ReturnsLatestVersionDescription(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	deps, _ := newDefinitionServiceDeps(uow)
	svc := service.NewDefinitionService(deps)

	fx := loadDocumentProcessingFixture(t)
	created, err := svc.Create(ctx, service.CreateDefinition{
		Name: fx.Name, Description: "a", Nodes: fx.Nodes, Edges: fx.Edges,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Save(ctx, service.SaveDefinition{
		WorkflowID:  created.WorkflowID,
		BaseVersion: created.Version,
		Name:        fx.Name,
		Description: "b",
		Nodes:       fx.Nodes,
		Edges:       fx.Edges,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	items, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var item *service.DefinitionListItem
	for i := range items {
		if items[i].Workflow.WorkflowID == created.WorkflowID {
			item = &items[i]
			break
		}
	}
	if item == nil {
		t.Fatalf("List: want workflow=%s present, got missing", created.WorkflowID)
	}
	if item.Description != "b" {
		t.Fatalf("List() Description = %q, want %q (the latest, v2, version's description, not v1's %q)",
			item.Description, "b", "a")
	}
	if item.Workflow.LatestVersion != 2 {
		t.Fatalf("List() LatestVersion = %d, want 2", item.Workflow.LatestVersion)
	}
}

// ---------------------------------------------------------------------------
// QueryService.Snapshot (Snapshot-to-SSE handoff contract; docs/08-interface-spec.md §5)
// ---------------------------------------------------------------------------

func TestQueryService_Snapshot_LastSeqMatchesLatestEvent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	appendEvent(ctx, t, uow, f.runID, domain.EventRunCreated)
	appendEvent(ctx, t, uow, f.runID, domain.EventNodeReady)
	appendEvent(ctx, t, uow, f.runID, domain.EventNodeStarted)

	events := listEvents(ctx, t, uow, f.runID)
	if len(events) != 3 {
		t.Fatalf("seeded events: want 3, got %d", len(events))
	}
	wantLastSeq := events[len(events)-1].Seq

	svc := service.NewQueryService(service.Deps{UoW: uow})
	snapshot, err := svc.Snapshot(ctx, f.runID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.LastSeq != wantLastSeq {
		t.Errorf("Snapshot LastSeq = %d, want %d (the latest committed Event's seq)", snapshot.LastSeq, wantLastSeq)
	}
	if snapshot.Run.LastSeq != wantLastSeq {
		t.Errorf("Snapshot Run.LastSeq = %d, want %d", snapshot.Run.LastSeq, wantLastSeq)
	}
	if snapshot.Run.ID != f.runID {
		t.Errorf("Snapshot Run.ID = %q, want %q", snapshot.Run.ID, f.runID)
	}
	if len(snapshot.NodeRuns) != 1 || snapshot.NodeRuns[0].ID != f.nodeRunID {
		t.Errorf("Snapshot NodeRuns = %+v, want exactly [%s]", snapshot.NodeRuns, f.nodeRunID)
	}
}
