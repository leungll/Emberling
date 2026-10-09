package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// execFakePlan builds a distinguishable *runtime.CompiledDefinition so a test can tell
// which plan a cache slot currently holds without comparing pointer identity directly.
func execFakePlan(marker string) *runtime.CompiledDefinition {
	return &runtime.CompiledDefinition{
		Definition:     domain.Definition{WorkflowID: marker},
		OutputNodeID:   marker,
		RunInputSchema: json.RawMessage(`{}`),
	}
}

func TestPlanCache_GetMiss_ReturnsFalse(t *testing.T) {
	c := newPlanCache(4)
	if _, ok := c.get("wf_missing", 1); ok {
		t.Fatal("get on empty cache: want ok=false, got true")
	}
}

func TestPlanCache_PutThenGet_ReturnsSamePlan(t *testing.T) {
	c := newPlanCache(4)
	plan := execFakePlan("wf_a")
	c.put("wf_a", 1, plan)

	got, ok := c.get("wf_a", 1)
	if !ok {
		t.Fatal("get after put: want ok=true, got false")
	}
	if got != plan {
		t.Fatalf("get after put: want the exact plan pointer back, got a different value")
	}

	// A different version of the same Workflow is a distinct cache slot: CreateRun and
	// Advance both key by (workflowID, version) because a Run stays bound to one
	// immutable Definition version for its whole lifetime.
	if _, ok := c.get("wf_a", 2); ok {
		t.Fatal("get with mismatched version: want ok=false, got true")
	}
}

func TestPlanCache_PutSameKeyTwice_OverwritesWithoutGrowingOrder(t *testing.T) {
	c := newPlanCache(1)
	first := execFakePlan("wf_a")
	second := execFakePlan("wf_a_v2")

	c.put("wf_a", 1, first)
	c.put("wf_a", 1, second)

	got, ok := c.get("wf_a", 1)
	if !ok || got != second {
		t.Fatalf("get after overwrite: want the second plan, got ok=%v plan=%v", ok, got)
	}
	if len(c.order) != 1 {
		t.Fatalf("order length after overwriting the only key: want 1, got %d", len(c.order))
	}
}

// TestPlanCache_AtCapacity_EvictsOldestFIFO proves the documented eviction policy: FIFO,
// not LRU. plan_cache.go's own doc comment explains why -- a miss just recompiles, so
// correctness never depends on which entry is evicted.
func TestPlanCache_AtCapacity_EvictsOldestFIFO(t *testing.T) {
	c := newPlanCache(2)
	c.put("wf_1", 1, execFakePlan("wf_1"))
	c.put("wf_2", 1, execFakePlan("wf_2"))

	// Touching wf_1 again would make it "recently used" under an LRU policy; FIFO must
	// ignore that and still evict wf_1 first purely by insertion order.
	if _, ok := c.get("wf_1", 1); !ok {
		t.Fatal("get wf_1 before eviction: want ok=true, got false")
	}

	c.put("wf_3", 1, execFakePlan("wf_3"))

	if _, ok := c.get("wf_1", 1); ok {
		t.Fatal("get wf_1 after third put at capacity 2: want evicted (ok=false), got still present")
	}
	if _, ok := c.get("wf_2", 1); !ok {
		t.Fatal("get wf_2 after third put: want still present, got evicted")
	}
	if _, ok := c.get("wf_3", 1); !ok {
		t.Fatal("get wf_3 after put: want present, got missing")
	}
}

func TestPlanCache_NonPositiveCapacity_FallsBackToDefault(t *testing.T) {
	c := newPlanCache(0)
	if c.capacity != planCacheCapacity {
		t.Fatalf("capacity with non-positive constructor arg: want default %d, got %d", planCacheCapacity, c.capacity)
	}
}

// -----------------------------------------------------------------------------------
// TestAdvance_SuccessfulClaim_NotifiesEventsCommitted
//
// A minimal fake store, scoped to exactly what one successful Advance claim touches:
// Definitions().GetVersion, Runs().Get/LockForUpdate/UpdateAggregate, NodeRuns().
// ListByRun/ClaimReady/IncrementAttemptCount/SetInput, NodeAttempts().Create and
// Events().Append. Every other store method is unreachable by this scenario (a single
// READY NodeRun with no upstream edges) and returns errExecFakeNotImplemented so an
// accidental new call site fails loudly instead of silently returning a zero value.
// -----------------------------------------------------------------------------------

var errExecFakeNotImplemented = errors.New("execution_test: fake store method not implemented")

// execFixedClock is a non-mutable fake domain.Clock: this scenario has no need to move
// time, unlike the integration suite's mutable execClock.
type execFixedClock struct{ now time.Time }

func (c execFixedClock) Now() time.Time { return c.now }

// execFakeIDs returns a fixed, distinguishable ID per prefix; uniqueness across calls is
// not needed since this scenario creates at most one row of each kind.
type execFakeIDs struct{}

func (execFakeIDs) NewID(prefix string) string { return prefix + "_fixed" }

// execFakeNotifier records every EventsCommitted call so the test can assert both the
// call count and the committed lastSeq without a channel or a sleep.
type execFakeNotifier struct {
	calls []struct {
		runID   string
		lastSeq int64
	}
}

func (n *execFakeNotifier) EventsCommitted(runID string, lastSeq int64) {
	n.calls = append(n.calls, struct {
		runID   string
		lastSeq int64
	}{runID, lastSeq})
}

type execFakeUoW struct{ tx *execFakeTx }

func (u *execFakeUoW) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return fn(ctx, u.tx)
}

// WithinReadTx is never exercised by Advance; it delegates to the same fake Tx purely so
// execFakeUoW satisfies store.UnitOfWork.
func (u *execFakeUoW) WithinReadTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return fn(ctx, u.tx)
}

type execFakeTx struct {
	defs     *execFakeDefinitionRepo
	runs     *execFakeRunRepo
	nodeRuns *execFakeNodeRunRepo
	attempts *execFakeNodeAttemptRepo
	events   *execFakeEventRepo
}

func (t *execFakeTx) Definitions() store.DefinitionRepository   { return t.defs }
func (t *execFakeTx) Runs() store.RunRepository                 { return t.runs }
func (t *execFakeTx) NodeRuns() store.NodeRunRepository         { return t.nodeRuns }
func (t *execFakeTx) NodeAttempts() store.NodeAttemptRepository { return t.attempts }
func (t *execFakeTx) Events() store.EventRepository             { return t.events }

// Assets is unused here: advancing an execution never reads or writes Asset Metadata.
func (t *execFakeTx) Assets() store.AssetRepository { return nil }

// Callback persistence is not exercised by these fakes: an async dispatch is proved
// against a real database in test/integration, where the binding, the DISPATCHED Attempt
// and WAITING_CALLBACK must commit together.
func (t *execFakeTx) CallbackBindings() store.CallbackBindingRepository { return nil }
func (t *execFakeTx) PendingCallbacks() store.PendingCallbackRepository { return nil }

// Agent facts are likewise out of scope for these Node-execution fakes; their persistence
// is proved against a real database in test/integration.
func (t *execFakeTx) AgentRuns() store.AgentRunRepository           { return nil }
func (t *execFakeTx) AgentTurns() store.AgentTurnRepository         { return nil }
func (t *execFakeTx) AgentDecisions() store.AgentDecisionRepository { return nil }
func (t *execFakeTx) AgentActions() store.AgentActionRepository     { return nil }
func (t *execFakeTx) ToolAttempts() store.ToolAttemptRepository     { return nil }

func (t *execFakeTx) AgentContextVersions() store.AgentContextVersionRepository { return nil }
func (t *execFakeTx) AgentStateVersions() store.AgentStateVersionRepository     { return nil }
func (t *execFakeTx) ExecutionFacts() store.ExecutionFactRepository             { return nil }

type execFakeDefinitionRepo struct{ def domain.Definition }

func (r *execFakeDefinitionRepo) Save(context.Context, domain.Definition) error {
	return errExecFakeNotImplemented
}
func (r *execFakeDefinitionRepo) GetVersion(_ context.Context, workflowID string, version int) (domain.Definition, error) {
	if workflowID != r.def.WorkflowID || version != r.def.Version {
		return domain.Definition{}, domain.ErrNotFound
	}
	return r.def, nil
}
func (r *execFakeDefinitionRepo) ListVersions(context.Context, string) ([]domain.Definition, error) {
	return nil, errExecFakeNotImplemented
}
func (r *execFakeDefinitionRepo) GetWorkflow(context.Context, string) (domain.Workflow, error) {
	return domain.Workflow{}, errExecFakeNotImplemented
}
func (r *execFakeDefinitionRepo) ListWorkflows(context.Context) ([]store.WorkflowSummary, error) {
	return nil, errExecFakeNotImplemented
}

type execFakeRunRepo struct {
	run         domain.Run
	lockLastSeq int64
}

func (r *execFakeRunRepo) Create(context.Context, domain.Run) error { return errExecFakeNotImplemented }
func (r *execFakeRunRepo) Get(_ context.Context, runID string) (domain.Run, error) {
	if runID != r.run.ID {
		return domain.Run{}, domain.ErrNotFound
	}
	return r.run, nil
}
func (r *execFakeRunRepo) LockForUpdate(_ context.Context, runID string) (*store.RunLock, error) {
	if runID != r.run.ID {
		return nil, domain.ErrNotFound
	}
	return store.NewRunLock(runID, r.run.Status, r.lockLastSeq), nil
}
func (r *execFakeRunRepo) UpdateAggregate(context.Context, *store.RunLock, domain.RunStatus, time.Time) error {
	return nil
}
func (r *execFakeRunRepo) SetOutput(context.Context, *store.RunLock, json.RawMessage) error {
	return errExecFakeNotImplemented
}
func (r *execFakeRunRepo) SetError(context.Context, *store.RunLock, domain.ExecutionError) error {
	return errExecFakeNotImplemented
}
func (r *execFakeRunRepo) LatestByWorkflow(context.Context, string) (*domain.Run, error) {
	return nil, errExecFakeNotImplemented
}

// execFakeNodeRunRepo serves nr as the one NodeRun a claim can act on; others are listed
// alongside it for the Advance scan but are never claimed or written.
type execFakeNodeRunRepo struct {
	nr     domain.NodeRun
	others []domain.NodeRun
}

func (r *execFakeNodeRunRepo) Create(context.Context, domain.NodeRun) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeRunRepo) Get(context.Context, string) (domain.NodeRun, error) {
	return domain.NodeRun{}, errExecFakeNotImplemented
}
func (r *execFakeNodeRunRepo) ListByRun(_ context.Context, runID string) ([]domain.NodeRun, error) {
	if runID != r.nr.RunID {
		return nil, nil
	}
	return append(append([]domain.NodeRun{}, r.others...), r.nr), nil
}
func (r *execFakeNodeRunRepo) ClaimReady(_ context.Context, nodeRunID string, _ time.Time) (bool, error) {
	return nodeRunID == r.nr.ID, nil
}
func (r *execFakeNodeRunRepo) Transition(context.Context, string, domain.NodeRunStatus, domain.NodeRunStatus, time.Time) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeRunRepo) ListReadyOrRetryable(context.Context, time.Time, int) ([]domain.NodeRun, error) {
	return nil, errExecFakeNotImplemented
}
func (r *execFakeNodeRunRepo) ClaimRetry(context.Context, string, time.Time) (bool, error) {
	return false, errExecFakeNotImplemented
}
func (r *execFakeNodeRunRepo) ScheduleRetry(context.Context, string, time.Time, time.Time) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeRunRepo) IncrementAttemptCount(_ context.Context, nodeRunID string) (int, error) {
	if nodeRunID != r.nr.ID {
		return 0, errExecFakeNotImplemented
	}
	return 1, nil
}
func (r *execFakeNodeRunRepo) SetInput(_ context.Context, nodeRunID string, _ json.RawMessage) error {
	if nodeRunID != r.nr.ID {
		return errExecFakeNotImplemented
	}
	return nil
}
func (r *execFakeNodeRunRepo) MarkSucceeded(context.Context, string, domain.NodeRunStatus, time.Time, store.NodeRunOutcome) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeRunRepo) MarkFailed(context.Context, string, domain.NodeRunStatus, time.Time, domain.ExecutionError) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeRunRepo) MarkWaiting(context.Context, string, time.Time) error {
	return errExecFakeNotImplemented
}

type execFakeNodeAttemptRepo struct{}

func (r *execFakeNodeAttemptRepo) Create(context.Context, domain.NodeAttempt) error { return nil }
func (r *execFakeNodeAttemptRepo) Get(context.Context, string) (domain.NodeAttempt, error) {
	return domain.NodeAttempt{}, errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) ListByNodeRun(context.Context, string) ([]domain.NodeAttempt, error) {
	return nil, errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) Transition(context.Context, string, domain.NodeAttemptStatus, domain.NodeAttemptStatus, time.Time) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) MarkDispatched(context.Context, string, time.Time, *time.Time) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) ClaimPoll(context.Context, string, time.Time, time.Duration, int) (bool, error) {
	return false, errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) ClearPoll(context.Context, string) (bool, error) {
	return false, errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) ListDuePolls(context.Context, time.Time, int) ([]store.DuePoll, error) {
	return nil, errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) MarkSucceeded(context.Context, string, domain.NodeAttemptStatus, time.Time, json.RawMessage) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) MarkFailed(context.Context, string, domain.NodeAttemptStatus, time.Time, domain.ExecutionError) error {
	return errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) ListExpired(context.Context, time.Time, int) ([]domain.NodeAttempt, error) {
	return nil, errExecFakeNotImplemented
}
func (r *execFakeNodeAttemptRepo) Latest(context.Context, string) (*domain.NodeAttempt, error) {
	return nil, errExecFakeNotImplemented
}

// execFakeEventRepo.Append allocates seq from the lock exactly like a real
// EventRepository implementation must (store.go: "seq must never be allocated without
// the aggregate lock"), so lock.LastSeq() after Append reflects the real watermark
// Advance is expected to hand to Notifier.EventsCommitted.
type execFakeEventRepo struct{}

func (r *execFakeEventRepo) Append(_ context.Context, lock *store.RunLock, ev domain.Event) (domain.Event, error) {
	ev.Seq = lock.NextSeq()
	return ev, nil
}
func (r *execFakeEventRepo) ListAfter(context.Context, string, int64, int) ([]domain.Event, error) {
	return nil, errExecFakeNotImplemented
}

// TestAdvance_SuccessfulClaim_NotifiesEventsCommitted proves Advance's post-commit
// obligation: a successful claim must wake an SSE cursor for the Run it just advanced,
// exactly like CreateRun/CompleteNode/FailNode already do through postCommit. Without
// this call, NODE_STARTED could sit uncoalesced until some later Event fires the
// Notifier, so a connected SSE client would not learn a NodeRun started dispatching until
// it also completed.
func TestAdvance_SuccessfulClaim_NotifiesEventsCommitted(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const runID = "run_advance_notify"
	const nodeRunID = "nr_advance_notify"

	// A minimal two-node graph, not a single node: runtime's GRAPH compile stage requires
	// exactly one Output Node (text_output) and rejects a required input port with no
	// incoming edge, so text_input alone would fail to compile.
	def := domain.Definition{
		WorkflowID: "wf_advance_notify",
		Version:    1,
		Nodes: []domain.Node{
			{ID: "n1", Type: "text_input", Config: json.RawMessage(`{"inputKey":"brief","required":false}`)},
			{ID: "n2", Type: "text_output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "n1", SourceHandle: "text", Target: "n2", TargetHandle: "text"},
		},
	}

	tx := &execFakeTx{
		defs: &execFakeDefinitionRepo{def: def},
		runs: &execFakeRunRepo{
			run:         domain.Run{ID: runID, WorkflowID: def.WorkflowID, DefinitionVersion: def.Version, Status: domain.RunRunning, Input: json.RawMessage(`{}`)},
			lockLastSeq: 3,
		},
		nodeRuns: &execFakeNodeRunRepo{nr: domain.NodeRun{ID: nodeRunID, RunID: runID, NodeID: "n1", NodeType: "text_input", Status: domain.NodeRunReady, ReadyAt: now}},
		attempts: &execFakeNodeAttemptRepo{},
		events:   &execFakeEventRepo{},
	}

	nodes := registry.NewNodeRegistry()
	if err := nodes.Register(textinput.Registration()); err != nil {
		t.Fatalf("register text_input: %v", err)
	}
	if err := nodes.Register(textoutput.Registration()); err != nil {
		t.Fatalf("register text_output: %v", err)
	}
	notifier := &execFakeNotifier{}

	svc := NewExecutionService(Deps{
		UoW:      &execFakeUoW{tx: tx},
		Nodes:    nodes,
		Compiler: runtime.NewCompiler(nodes, execFixedClock{now: now}),
		Clock:    execFixedClock{now: now},
		IDs:      execFakeIDs{},
		Notifier: notifier,
	})

	outcome, err := svc.Advance(context.Background(), runID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !outcome.Claimed {
		t.Fatalf("advance: want a claim, got none")
	}

	if len(notifier.calls) != 1 {
		t.Fatalf("EventsCommitted calls after a successful Advance claim: want 1, got %d (%+v)", len(notifier.calls), notifier.calls)
	}
	// The fake lock starts at lastSeq=3; startAttempt appends exactly one Event
	// (NODE_STARTED), so the committed watermark Advance must report is 4.
	if got := notifier.calls[0]; got.runID != runID || got.lastSeq != 4 {
		t.Fatalf("EventsCommitted call: want (runID=%q, lastSeq=4), got (runID=%q, lastSeq=%d)", runID, got.runID, got.lastSeq)
	}
}

// TestAdvance_SettledOrWaitingNodeRun_OffersNoClaim pins how the Advance scan treats
// the NodeRun statuses that are neither READY nor RUNNING: a WAITING_CALLBACK, SUCCEEDED
// or FAILED NodeRun is never claimed, and a SUCCEEDED upstream does not stop its READY
// downstream from being claimed.
func TestAdvance_SettledOrWaitingNodeRun_OffersNoClaim(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const runID = "run_advance_scan"
	def := domain.Definition{
		WorkflowID: "wf_advance_scan",
		Version:    1,
		Nodes: []domain.Node{
			{ID: "n1", Type: "text_input", Config: json.RawMessage(`{"inputKey":"brief","required":false}`)},
			{ID: "n2", Type: "text_output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "n1", SourceHandle: "text", Target: "n2", TargetHandle: "text"},
		},
	}
	nodes := registry.NewNodeRegistry()
	if err := nodes.Register(textinput.Registration()); err != nil {
		t.Fatalf("register text_input: %v", err)
	}
	if err := nodes.Register(textoutput.Registration()); err != nil {
		t.Fatalf("register text_output: %v", err)
	}
	advance := func(t *testing.T, nodeRuns *execFakeNodeRunRepo) AdvanceOutcome {
		t.Helper()
		tx := &execFakeTx{
			defs: &execFakeDefinitionRepo{def: def},
			runs: &execFakeRunRepo{
				run: domain.Run{ID: runID, WorkflowID: def.WorkflowID, DefinitionVersion: def.Version, Status: domain.RunRunning, Input: json.RawMessage(`{}`)},
			},
			nodeRuns: nodeRuns,
			attempts: &execFakeNodeAttemptRepo{},
			events:   &execFakeEventRepo{},
		}
		svc := NewExecutionService(Deps{
			UoW:      &execFakeUoW{tx: tx},
			Nodes:    nodes,
			Compiler: runtime.NewCompiler(nodes, execFixedClock{now: now}),
			Clock:    execFixedClock{now: now},
			IDs:      execFakeIDs{},
		})
		outcome, err := svc.Advance(context.Background(), runID)
		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		return outcome
	}

	for _, status := range []domain.NodeRunStatus{domain.NodeRunWaitingCallback, domain.NodeRunSucceeded, domain.NodeRunFailed} {
		t.Run(string(status), func(t *testing.T) {
			// The fake would let a claim on this NodeRun win, so only the scan keeps it unclaimed.
			outcome := advance(t, &execFakeNodeRunRepo{
				nr: domain.NodeRun{ID: "nr_n1", RunID: runID, NodeID: "n1", NodeType: "text_input", Status: status, ReadyAt: now},
			})
			if outcome.Claimed {
				t.Fatalf("advance over a lone %s NodeRun: want no claim, got %+v", status, outcome)
			}
		})
	}

	t.Run("SUCCEEDED upstream with READY downstream", func(t *testing.T) {
		outcome := advance(t, &execFakeNodeRunRepo{
			nr: domain.NodeRun{ID: "nr_n2", RunID: runID, NodeID: "n2", NodeType: "text_output", Status: domain.NodeRunReady, ReadyAt: now},
			others: []domain.NodeRun{{
				ID: "nr_n1", RunID: runID, NodeID: "n1", NodeType: "text_input", Status: domain.NodeRunSucceeded,
				ReadyAt: now, Output: json.RawMessage(`{"text":"hello"}`),
			}},
		})
		if !outcome.Claimed || outcome.NodeRunID != "nr_n2" {
			t.Fatalf("advance: want the READY downstream nr_n2 claimed, got %+v", outcome)
		}
	})
}

// setTaskPolicy gives the async task node an ExecutionPolicy that permits a second
// Attempt, so a test can observe whether the retry decision allowed one.
func (h *cbHarness) setTaskPolicy(policy domain.ExecutionPolicy) {
	h.t.Helper()
	for i := range h.store.def.Nodes {
		if h.store.def.Nodes[i].ID == "task" {
			p := policy
			h.store.def.Nodes[i].ExecutionPolicy = &p
			return
		}
	}
	h.t.Fatal("setTaskPolicy: definition has no task node")
}

// TestExecute_KeyedExternalNode_PassesNodeRunIDAsIdempotencyKey covers the EXTERNAL+KEYED
// row of the SideEffectPolicy table: the key is the NodeRun id (stable across the
// NodeRun's Attempts), and it is what allows runtime.DecideRetry to retry a failed
// external call at all.
func TestExecute_KeyedExternalNode_PassesNodeRunIDAsIdempotencyKey(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed})
	h.setTaskPolicy(domain.ExecutionPolicy{MaxAttempts: 2, Backoff: domain.BackoffFixed})
	h.seedReadyTask()
	h.exec.execErr = errors.New("provider refused the dispatch")

	outcome, err := h.svc.Advance(context.Background(), cbRunID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got := outcome.Input.IdempotencyKey; got != cbTaskNodeRunID {
		t.Fatalf("NodeInput.IdempotencyKey for an EXTERNAL+KEYED node: want the NodeRun id %q, got %q", cbTaskNodeRunID, got)
	}
	if err := h.svc.Execute(context.Background(), outcome); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := h.exec.lastInput.IdempotencyKey; got != cbTaskNodeRunID {
		t.Fatalf("IdempotencyKey seen by the Executor: want %q, got %q", cbTaskNodeRunID, got)
	}
	if _, found := h.eventOfType(domain.EventNodeRetrying); !found {
		t.Fatalf("NODE_RETRYING after a keyed external failure: want present, got events %v", h.eventTypes())
	}
	if _, found := h.eventOfType(domain.EventNodeFailed); found {
		t.Fatalf("NODE_FAILED after a keyed external failure with attempts left: want absent, got events %v", h.eventTypes())
	}
	if got := h.store.state.nodeRuns[cbTaskNodeRunID].NextAttemptAt; got == nil {
		t.Fatal("nextAttemptAt after a retryable keyed external failure: want a scheduled retry, got nil")
	}
}

// TestExecute_UnknownIdempotencyExternalNode_PassesNoIdempotencyKey is the counterpart:
// a Provider whose deduplication semantics are unknown gets no key invented for it.
func TestExecute_UnknownIdempotencyExternalNode_PassesNoIdempotencyKey(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h.seedReadyTask()

	outcome, err := h.svc.Advance(context.Background(), cbRunID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got := outcome.Input.IdempotencyKey; got != "" {
		t.Fatalf("NodeInput.IdempotencyKey for an EXTERNAL+UNKNOWN node: want empty, got %q", got)
	}
}

// execClassifiedError is an Adapter-style error that reports whether the external call's
// result is unknown, the same contract the dispatch Adapter exposes.
type execClassifiedError struct{ uncertain bool }

func (e *execClassifiedError) Error() string   { return "dispatch failed" }
func (e *execClassifiedError) Uncertain() bool { return e.uncertain }

// TestExecute_DispatchErrorReportingUncertain_FailsNodeAsUncertain proves Emberling reads
// the Adapter's own classification: an uncertain dispatch of an EXTERNAL+UNKNOWN node must
// not be retried (CLAUDE.md: "do not retry an uncertain external side effect").
func TestExecute_DispatchErrorReportingUncertain_FailsNodeAsUncertain(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h.setTaskPolicy(domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed})
	h.seedReadyTask()
	h.exec.execErr = &execClassifiedError{uncertain: true}

	outcome, err := h.svc.Advance(context.Background(), cbRunID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := h.svc.Execute(context.Background(), outcome); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if _, found := h.eventOfType(domain.EventNodeRetrying); found {
		t.Fatalf("NODE_RETRYING after an uncertain EXTERNAL+UNKNOWN dispatch: want absent, got events %v", h.eventTypes())
	}
	if _, found := h.eventOfType(domain.EventNodeFailed); !found {
		t.Fatalf("NODE_FAILED after an uncertain EXTERNAL+UNKNOWN dispatch: want present, got events %v", h.eventTypes())
	}
	if got := h.store.state.nodeRuns[cbTaskNodeRunID].Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after an uncertain dispatch failure: want FAILED, got %s", got)
	}
}

// TestExecute_DispatchErrorReportingDefinite_FailsNodeAsDefinite is the definite
// counterpart: the Provider certainly did not accept the task, so the same node may retry.
func TestExecute_DispatchErrorReportingDefinite_FailsNodeAsDefinite(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h.setTaskPolicy(domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed})
	h.seedReadyTask()
	h.exec.execErr = &execClassifiedError{uncertain: false}

	outcome, err := h.svc.Advance(context.Background(), cbRunID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := h.svc.Execute(context.Background(), outcome); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if _, found := h.eventOfType(domain.EventNodeRetrying); !found {
		t.Fatalf("NODE_RETRYING after a definite dispatch failure with attempts left: want present, got events %v", h.eventTypes())
	}
	if got := h.store.state.nodeRuns[cbTaskNodeRunID].Status; got != domain.NodeRunRunning {
		t.Fatalf("NodeRun status after a definite, retryable dispatch failure: want RUNNING, got %s", got)
	}
}
