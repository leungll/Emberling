package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ---------------------------------------------------------------------------
// Shared test doubles (service package unit tests only; PostgreSQL-backed
// behaviour lives in test/integration/definitions_service_test.go).
// ---------------------------------------------------------------------------

// defsFakeCatalog is a minimal runtime.NodeCatalog: just enough registered Node Types to
// compile the cycle fixture used below. It intentionally does not cover every MVP Node
// Type; runtime's own compiler tests already own that breadth.
type defsFakeCatalog struct {
	metadata map[string]domain.NodeMetadata
}

func newDefsFakeCatalog() *defsFakeCatalog {
	permissive := json.RawMessage(`{"type":"object"}`)
	return &defsFakeCatalog{metadata: map[string]domain.NodeMetadata{
		runtime.NodeTypePromptTemplate: {
			Type:          runtime.NodeTypePromptTemplate,
			DisplayName:   "Prompt Template",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  permissive,
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
	}}
}

func (c *defsFakeCatalog) NodeMetadata(nodeType string) (domain.NodeMetadata, bool) {
	m, ok := c.metadata[nodeType]
	return m, ok
}

func (c *defsFakeCatalog) ValidateSemantics(context.Context, string, map[string]any) error {
	return nil
}

// defsCycleDefinition builds a two-node cycle: both nodes are registered, so Compile reaches
// the Graph stage and fails with DAG_HAS_CYCLE rather than an earlier-stage error.
func defsCycleDefinition() domain.Definition {
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

// defsFakeIDs is a deterministic domain.IDGenerator: sequential per prefix, so a test can
// predict the minted Workflow id.
type defsFakeIDs struct{ counts map[string]int }

func newDefsFakeIDs() *defsFakeIDs { return &defsFakeIDs{counts: map[string]int{}} }

func (f *defsFakeIDs) NewID(prefix string) string {
	f.counts[prefix]++
	return fmt.Sprintf("%s_%d", prefix, f.counts[prefix])
}

var errDefsFakeNotImplemented = errors.New("service test fake: method not implemented")

// defsFakeUoW runs fn directly against one fixed defsFakeTx: no real transactional isolation,
// only enough to exercise service-layer orchestration against fakes.
type defsFakeUoW struct{ tx *defsFakeTx }

func (u *defsFakeUoW) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return fn(ctx, u.tx)
}

// WithinReadTx has no real transactional isolation to offer here (see the type comment);
// it runs fn against the same fixed defsFakeTx as WithinTx, which is enough to exercise
// service-layer orchestration. The read-only, snapshot-consistency guarantee itself is
// proved against real PostgreSQL by store_snapshot_test.go.
func (u *defsFakeUoW) WithinReadTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return fn(ctx, u.tx)
}

type defsFakeTx struct {
	defs     *defsFakeDefinitionRepo
	runs     *defsFakeRunRepo
	nodeRuns *defsFakeNodeRunRepo
	attempts *defsFakeNodeAttemptRepo
	events   *defsFakeEventRepo
	bindings *defsFakeCallbackBindingRepo
}

func newDefsFakeTx() *defsFakeTx {
	return &defsFakeTx{
		defs:     newDefsFakeDefinitionRepo(),
		runs:     newDefsFakeRunRepo(),
		nodeRuns: newDefsFakeNodeRunRepo(),
		attempts: newDefsFakeNodeAttemptRepo(),
		events:   newDefsFakeEventRepo(),
		bindings: newDefsFakeCallbackBindingRepo(),
	}
}

func (t *defsFakeTx) Definitions() store.DefinitionRepository   { return t.defs }
func (t *defsFakeTx) Runs() store.RunRepository                 { return t.runs }
func (t *defsFakeTx) NodeRuns() store.NodeRunRepository         { return t.nodeRuns }
func (t *defsFakeTx) NodeAttempts() store.NodeAttemptRepository { return t.attempts }
func (t *defsFakeTx) Events() store.EventRepository             { return t.events }

// Assets is unused here: the Definition use cases never read or write Asset Metadata.
func (t *defsFakeTx) Assets() store.AssetRepository { return nil }

// CallbackBindings backs QueryService.NodeRunDetail's projection (internal/service/query.go);
// these fakes never populate a binding, so ListByTargets always answers empty, matching a
// NodeRun whose Attempts never dispatched. Callback dispatch itself -- Create alongside the
// DISPATCHED Attempt and WAITING_CALLBACK NodeRun in one transaction -- is proved against a
// real database in test/integration, not against this fake.
func (t *defsFakeTx) CallbackBindings() store.CallbackBindingRepository { return t.bindings }
func (t *defsFakeTx) PendingCallbacks() store.PendingCallbackRepository { return nil }

// Definition use cases never touch Agent facts; those repositories are exercised against a
// real database in test/integration.
func (t *defsFakeTx) AgentRuns() store.AgentRunRepository           { return nil }
func (t *defsFakeTx) AgentTurns() store.AgentTurnRepository         { return nil }
func (t *defsFakeTx) AgentDecisions() store.AgentDecisionRepository { return nil }
func (t *defsFakeTx) AgentActions() store.AgentActionRepository     { return nil }
func (t *defsFakeTx) ToolAttempts() store.ToolAttemptRepository     { return nil }

func (t *defsFakeTx) AgentContextVersions() store.AgentContextVersionRepository { return nil }
func (t *defsFakeTx) AgentStateVersions() store.AgentStateVersionRepository     { return nil }

// defsFakeCallbackBindingRepo is a minimal store.CallbackBindingRepository double: only
// ListByTargets is exercised (by QueryService.NodeRunDetail), always against an empty set,
// so it need not track any actual bindings.
type defsFakeCallbackBindingRepo struct{}

func newDefsFakeCallbackBindingRepo() *defsFakeCallbackBindingRepo {
	return &defsFakeCallbackBindingRepo{}
}

func (r *defsFakeCallbackBindingRepo) Create(context.Context, domain.CallbackBinding) error {
	return errDefsFakeNotImplemented
}

func (r *defsFakeCallbackBindingRepo) GetByExternalTaskID(context.Context, string) (domain.CallbackBinding, error) {
	return domain.CallbackBinding{}, errDefsFakeNotImplemented
}

func (r *defsFakeCallbackBindingRepo) ListByTargets(context.Context, domain.CallbackTargetType, []string) ([]domain.CallbackBinding, error) {
	return nil, nil
}

// defsFakeDefinitionRepo mirrors just enough of store/postgres's version-conflict semantics
// (new Workflow must start at 1, existing Workflow accepts only latest+1) to test
// DefinitionService without a database; store/postgres's own locking behaviour is proved
// by the integration tests.
type defsFakeDefinitionRepo struct {
	workflows map[string]domain.Workflow
	versions  map[string]map[int]domain.Definition
	saveCalls int
}

func newDefsFakeDefinitionRepo() *defsFakeDefinitionRepo {
	return &defsFakeDefinitionRepo{
		workflows: map[string]domain.Workflow{},
		versions:  map[string]map[int]domain.Definition{},
	}
}

func (r *defsFakeDefinitionRepo) Save(ctx context.Context, def domain.Definition) error {
	r.saveCalls++
	wf, ok := r.workflows[def.WorkflowID]
	if !ok {
		if def.Version != 1 {
			return domain.ErrVersionConflict
		}
		wf = domain.Workflow{WorkflowID: def.WorkflowID, Name: def.Name, LatestVersion: 1, CreatedAt: def.CreatedAt, UpdatedAt: def.CreatedAt}
	} else {
		if def.Version != wf.LatestVersion+1 {
			return domain.ErrVersionConflict
		}
		wf.LatestVersion = def.Version
		wf.Name = def.Name
		wf.UpdatedAt = def.CreatedAt
	}
	r.workflows[def.WorkflowID] = wf
	if r.versions[def.WorkflowID] == nil {
		r.versions[def.WorkflowID] = map[int]domain.Definition{}
	}
	r.versions[def.WorkflowID][def.Version] = def
	return nil
}

func (r *defsFakeDefinitionRepo) GetVersion(ctx context.Context, workflowID string, version int) (domain.Definition, error) {
	def, ok := r.versions[workflowID][version]
	if !ok {
		return domain.Definition{}, domain.ErrNotFound
	}
	return def, nil
}

func (r *defsFakeDefinitionRepo) ListVersions(ctx context.Context, workflowID string) ([]domain.Definition, error) {
	wf, ok := r.workflows[workflowID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	out := make([]domain.Definition, 0, wf.LatestVersion)
	for v := 1; v <= wf.LatestVersion; v++ {
		if def, ok := r.versions[workflowID][v]; ok {
			out = append(out, def)
		}
	}
	return out, nil
}

func (r *defsFakeDefinitionRepo) GetWorkflow(ctx context.Context, workflowID string) (domain.Workflow, error) {
	wf, ok := r.workflows[workflowID]
	if !ok {
		return domain.Workflow{}, domain.ErrNotFound
	}
	return wf, nil
}

func (r *defsFakeDefinitionRepo) ListWorkflows(ctx context.Context) ([]store.WorkflowSummary, error) {
	out := make([]store.WorkflowSummary, 0, len(r.workflows))
	for _, wf := range r.workflows {
		// Mirror the real store's join: the latest version's Description, looked up the
		// same way store/postgres/definitions.go's ListWorkflows joins workflow_definitions.
		out = append(out, store.WorkflowSummary{Workflow: wf, Description: r.versions[wf.WorkflowID][wf.LatestVersion].Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Workflow.WorkflowID < out[j].Workflow.WorkflowID })
	return out, nil
}

var _ store.DefinitionRepository = (*defsFakeDefinitionRepo)(nil)

// defsFakeRunRepo implements only Get and LatestByWorkflow with real behaviour; every other
// method is unused by the unit tests in this package and panics loudly if that changes.
type defsFakeRunRepo struct {
	byID       map[string]domain.Run
	byWorkflow map[string]*domain.Run
}

func newDefsFakeRunRepo() *defsFakeRunRepo {
	return &defsFakeRunRepo{byID: map[string]domain.Run{}, byWorkflow: map[string]*domain.Run{}}
}

func (r *defsFakeRunRepo) Create(context.Context, domain.Run) error { return errDefsFakeNotImplemented }

func (r *defsFakeRunRepo) Get(ctx context.Context, runID string) (domain.Run, error) {
	run, ok := r.byID[runID]
	if !ok {
		return domain.Run{}, domain.ErrNotFound
	}
	return run, nil
}

func (r *defsFakeRunRepo) LockForUpdate(context.Context, string) (*store.RunLock, error) {
	return nil, errDefsFakeNotImplemented
}
func (r *defsFakeRunRepo) UpdateAggregate(context.Context, *store.RunLock, domain.RunStatus, time.Time) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeRunRepo) SetOutput(context.Context, *store.RunLock, json.RawMessage) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeRunRepo) SetError(context.Context, *store.RunLock, domain.ExecutionError) error {
	return errDefsFakeNotImplemented
}

func (r *defsFakeRunRepo) LatestByWorkflow(ctx context.Context, workflowID string) (*domain.Run, error) {
	return r.byWorkflow[workflowID], nil
}

var _ store.RunRepository = (*defsFakeRunRepo)(nil)

// defsFakeNodeRunRepo implements only Get; every other method panics via errDefsFakeNotImplemented
// if a test path reaches it unexpectedly.
type defsFakeNodeRunRepo struct{ byID map[string]domain.NodeRun }

func newDefsFakeNodeRunRepo() *defsFakeNodeRunRepo {
	return &defsFakeNodeRunRepo{byID: map[string]domain.NodeRun{}}
}

func (r *defsFakeNodeRunRepo) Create(context.Context, domain.NodeRun) error {
	return errDefsFakeNotImplemented
}

func (r *defsFakeNodeRunRepo) Get(ctx context.Context, nodeRunID string) (domain.NodeRun, error) {
	nr, ok := r.byID[nodeRunID]
	if !ok {
		return domain.NodeRun{}, domain.ErrNotFound
	}
	return nr, nil
}

func (r *defsFakeNodeRunRepo) ListByRun(context.Context, string) ([]domain.NodeRun, error) {
	return nil, errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) ClaimReady(context.Context, string, time.Time) (bool, error) {
	return false, errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) Transition(context.Context, string, domain.NodeRunStatus, domain.NodeRunStatus, time.Time) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) ListReadyOrRetryable(context.Context, time.Time, int) ([]domain.NodeRun, error) {
	return nil, errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) ClaimRetry(context.Context, string, time.Time) (bool, error) {
	return false, errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) ScheduleRetry(context.Context, string, time.Time, time.Time) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) IncrementAttemptCount(context.Context, string) (int, error) {
	return 0, errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) SetInput(context.Context, string, json.RawMessage) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) MarkSucceeded(context.Context, string, domain.NodeRunStatus, time.Time, store.NodeRunOutcome) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) MarkFailed(context.Context, string, domain.NodeRunStatus, time.Time, domain.ExecutionError) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeRunRepo) MarkWaiting(context.Context, string, time.Time) error {
	return errDefsFakeNotImplemented
}

var _ store.NodeRunRepository = (*defsFakeNodeRunRepo)(nil)

// defsFakeNodeAttemptRepo implements only ListByNodeRun with real behaviour.
type defsFakeNodeAttemptRepo struct {
	byNodeRun map[string][]domain.NodeAttempt
}

func newDefsFakeNodeAttemptRepo() *defsFakeNodeAttemptRepo {
	return &defsFakeNodeAttemptRepo{byNodeRun: map[string][]domain.NodeAttempt{}}
}

func (r *defsFakeNodeAttemptRepo) Create(context.Context, domain.NodeAttempt) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) Get(context.Context, string) (domain.NodeAttempt, error) {
	return domain.NodeAttempt{}, errDefsFakeNotImplemented
}

func (r *defsFakeNodeAttemptRepo) ListByNodeRun(ctx context.Context, nodeRunID string) ([]domain.NodeAttempt, error) {
	return r.byNodeRun[nodeRunID], nil
}

func (r *defsFakeNodeAttemptRepo) Transition(context.Context, string, domain.NodeAttemptStatus, domain.NodeAttemptStatus, time.Time) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) MarkDispatched(context.Context, string, time.Time, *time.Time) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) ClaimPoll(context.Context, string, time.Time, time.Duration, int) (bool, error) {
	return false, errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) ClearPoll(context.Context, string) (bool, error) {
	return false, errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) ListDuePolls(context.Context, time.Time, int) ([]store.DuePoll, error) {
	return nil, errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) MarkSucceeded(context.Context, string, domain.NodeAttemptStatus, time.Time, json.RawMessage) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) MarkFailed(context.Context, string, domain.NodeAttemptStatus, time.Time, domain.ExecutionError) error {
	return errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) ListExpired(context.Context, time.Time, int) ([]domain.NodeAttempt, error) {
	return nil, errDefsFakeNotImplemented
}
func (r *defsFakeNodeAttemptRepo) Latest(context.Context, string) (*domain.NodeAttempt, error) {
	return nil, errDefsFakeNotImplemented
}

var _ store.NodeAttemptRepository = (*defsFakeNodeAttemptRepo)(nil)

// defsFakeEventRepo implements only ListAfter with real behaviour, capturing the arguments it
// was called with so a test can assert on them (e.g. limit clamping).
type defsFakeEventRepo struct {
	events       []domain.Event
	lastAfterSeq int64
	lastLimit    int
}

func newDefsFakeEventRepo() *defsFakeEventRepo { return &defsFakeEventRepo{} }

func (r *defsFakeEventRepo) Append(context.Context, *store.RunLock, domain.Event) (domain.Event, error) {
	return domain.Event{}, errDefsFakeNotImplemented
}

func (r *defsFakeEventRepo) ListAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]domain.Event, error) {
	r.lastAfterSeq = afterSeq
	r.lastLimit = limit
	return r.events, nil
}

var _ store.EventRepository = (*defsFakeEventRepo)(nil)

func newDefsTestDefinitionService(t *testing.T) (*DefinitionService, *defsFakeTx, *defsFakeIDs) {
	t.Helper()
	tx := newDefsFakeTx()
	ids := newDefsFakeIDs()
	deps := Deps{
		UoW:      &defsFakeUoW{tx: tx},
		Compiler: runtime.NewCompiler(newDefsFakeCatalog(), defsFixedClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}),
		Clock:    defsFixedClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		IDs:      ids,
	}
	return NewDefinitionService(deps), tx, ids
}

// defsFixedClock is a deterministic domain.Clock for tests.
type defsFixedClock struct{ at time.Time }

func (c defsFixedClock) Now() time.Time { return c.at }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDefinitionService_Validate_CycleDefinition_ReturnsInvalidWithoutPersisting(t *testing.T) {
	svc, tx, _ := newDefsTestDefinitionService(t)
	def := defsCycleDefinition()

	result, err := svc.Validate(context.Background(), def.Nodes, def.Edges)
	if err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
	if result.Valid {
		t.Fatalf("Validate().Valid = true, want false for a cyclic Definition")
	}
	found := false
	for _, e := range result.Errors {
		if e.Code == runtime.CodeDAGHasCycle {
			found = true
		}
	}
	if !found {
		t.Errorf("Errors = %+v, want to contain %s", result.Errors, runtime.CodeDAGHasCycle)
	}
	if tx.defs.saveCalls != 0 {
		t.Errorf("Definitions().Save called %d times, want 0: Validate must never persist", tx.defs.saveCalls)
	}
}

func TestDefinitionService_Create_CompileFailure_PersistsNothing(t *testing.T) {
	svc, tx, _ := newDefsTestDefinitionService(t)
	def := defsCycleDefinition()

	_, err := svc.Create(context.Background(), CreateDefinition{Name: "Cyclic", Nodes: def.Nodes, Edges: def.Edges})
	if err == nil {
		t.Fatal("Create() expected error, got nil")
	}
	var compileErr *CompileFailedError
	if !errors.As(err, &compileErr) {
		t.Fatalf("Create() error = %v (%T), want *CompileFailedError", err, err)
	}
	if compileErr.Result.Valid {
		t.Errorf("CompileFailedError.Result.Valid = true, want false")
	}
	if len(compileErr.Result.Errors) == 0 {
		t.Errorf("CompileFailedError.Result.Errors is empty, want at least one ValidationError")
	}
	if tx.defs.saveCalls != 0 {
		t.Errorf("Definitions().Save called %d times, want 0: a compile failure must persist nothing", tx.defs.saveCalls)
	}
	if len(tx.defs.workflows) != 0 {
		t.Errorf("workflows = %v, want none created", tx.defs.workflows)
	}
}

func TestDefinitionService_List_WorkflowNeverRun_HasNilLastRun(t *testing.T) {
	svc, tx, _ := newDefsTestDefinitionService(t)
	tx.defs.workflows["wf_1"] = domain.Workflow{WorkflowID: "wf_1", Name: "Never Run", LatestVersion: 1}
	// tx.runs.byWorkflow intentionally has no entry for wf_1: LatestByWorkflow must
	// report nil, not an error, when the Workflow has never run.

	items, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("List() = %d items, want 1", len(items))
	}
	if items[0].Workflow.WorkflowID != "wf_1" {
		t.Errorf("Workflow.WorkflowID = %q, want wf_1", items[0].Workflow.WorkflowID)
	}
	if items[0].LastRun != nil {
		t.Errorf("LastRun = %+v, want nil for a Workflow that has never run", items[0].LastRun)
	}
}
