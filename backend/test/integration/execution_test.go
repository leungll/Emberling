//go:build integration

// Execution-layer integration tests. These exist because ExecutionService's guarantees
// are transactional, lock-based or recovery guarantees a mock repository cannot prove
// (CLAUDE.md testing standard): atomic rollback on Event insert failure, exactly-one
// winner under concurrent claims, and rediscovery of committed-but-unexecuted work after
// a simulated crash. Every helper here is prefixed `exec` per the orchestrator's naming
// directive for this track, to avoid colliding with the sibling `defs`-prefixed test
// helpers in package service; that directive named internal/service specifically, but the
// prefix is kept here too for consistency across this track's whole test surface.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/prompttemplate"
	"github.com/leungll/Emberling/backend/internal/nodes/textgeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/internal/work"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// execClock is a mutable, mutex-guarded fake domain.Clock. Every test drives time by
// calling Advance explicitly; nothing here ever sleeps or reads the wall clock
// (CLAUDE.md testing standard: "inject clocks... where outcomes depend on time").
type execClock struct {
	mu  sync.Mutex
	now time.Time
}

func newExecClock(start time.Time) *execClock {
	return &execClock{now: start}
}

// execDeadlineClockStart seeds an execClock that a harness feeds into
// service.Deps.Clock, for the harnesses whose ExecutionService derives a real
// context.WithDeadline from Clock.Now() + a policy timeout (see
// internal/service/execution.go, agent_action.go, agent_turn.go) to bound an
// actual Model/Tool/Provider call. context.WithDeadline is always measured
// against real time, so a clock seeded from the fixed fixtureTime constant is
// a time bomb: once real "now" passes fixtureTime, every deadline derived
// from it is born already expired and the real call fails before it starts.
// Starting from wall-clock time instead keeps derived deadlines genuinely in
// the future no matter which day the suite runs. Harnesses that only persist
// or assert fixed fixtureTime values (no real deadline is computed from
// them) keep using fixtureTime directly, per CLAUDE.md's testing standard to
// inject clocks rather than depend on when a test happens to run.
func execDeadlineClockStart() time.Time {
	return time.Now().UTC().Truncate(time.Second)
}

func (c *execClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *execClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// execForcedIDs wraps the real production UUID generator so exactly one future Event ID
// allocation can be forced to a caller-chosen value. It exists only to manufacture a
// deterministic Postgres primary-key collision for
// TestExecution_EventInsertFailure_RollsBackNodeRunTransition -- kept minimal (a single
// forced-value slot, real generator otherwise) since store.UnitOfWork is expected to gain
// a second method at merge time and this harness intentionally stays a thin wrapper
// around the real UoW/ID generator rather than a parallel fake implementation.
type execForcedIDs struct {
	real domain.IDGenerator

	mu            sync.Mutex
	forcedEventID string
}

func newExecForcedIDs() *execForcedIDs {
	return &execForcedIDs{real: store.NewUUIDGenerator()}
}

// ForceNextEventID makes the next NewID(domain.IDPrefixEvent) call return id instead of a
// fresh UUID. It is one-shot: the override is cleared as soon as it is consumed.
func (g *execForcedIDs) ForceNextEventID(id string) {
	g.mu.Lock()
	g.forcedEventID = id
	g.mu.Unlock()
}

func (g *execForcedIDs) NewID(prefix string) string {
	if prefix == domain.IDPrefixEvent {
		g.mu.Lock()
		if g.forcedEventID != "" {
			id := g.forcedEventID
			g.forcedEventID = ""
			g.mu.Unlock()
			return id
		}
		g.mu.Unlock()
	}
	return g.real.NewID(prefix)
}

// execHarness wires one real PostgreSQL-backed ExecutionService, using the actual
// production Node Registry (the four M1 built-in node types), the deterministic Mock
// Model Provider, and an injected fake clock. Every test gets its own database
// (testdb.Open) and its own harness, so tests never observe each other's rows.
type execHarness struct {
	t   *testing.T
	ctx context.Context

	uow      store.UnitOfWork
	nodes    *registry.NodeRegistry
	models   *registry.ModelRegistry
	compiler *runtime.Compiler
	clock    *execClock
	ids      *execForcedIDs
	provider *mockmodel.Provider
	svc      *service.ExecutionService
}

func newExecHarness(t *testing.T) *execHarness {
	t.Helper()
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	nodeRegistry := registry.NewNodeRegistry()
	modelRegistry := registry.NewModelRegistry()
	provider := mockmodel.NewProvider()
	if err := modelRegistry.Register(ctx, provider); err != nil {
		t.Fatalf("register mock model provider: %v", err)
	}

	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(),
		prompttemplate.Registration(),
		textgeneration.Registration(modelRegistry),
		textoutput.Registration(),
	} {
		if err := nodeRegistry.Register(reg); err != nil {
			t.Fatalf("register node type: %v", err)
		}
	}

	clock := newExecClock(execDeadlineClockStart())
	compiler := runtime.NewCompiler(nodeRegistry, clock)
	ids := newExecForcedIDs()

	svc := service.NewExecutionService(service.Deps{
		UoW:      uow,
		Nodes:    nodeRegistry,
		Models:   modelRegistry,
		Compiler: compiler,
		Clock:    clock,
		IDs:      ids,
	})

	return &execHarness{
		t: t, ctx: ctx,
		uow: uow, nodes: nodeRegistry, models: modelRegistry, compiler: compiler,
		clock: clock, ids: ids, provider: provider, svc: svc,
	}
}

// saveDefinition compiles def through the harness's own Compiler and merges the frozen
// RunInputSchema/Validation back onto def before persisting, mirroring what a real
// "validate then save" flow would freeze. runtime.CompiledDefinition.Definition is the
// caller's def unchanged; RunInputSchema and Validation are separate fields Compile
// returns alongside it, so a caller that wants a realistic, as-if-saved Definition must
// copy them back on manually -- Compile itself never mutates def.
func (h *execHarness) saveDefinition(def domain.Definition) domain.Definition {
	h.t.Helper()
	plan, err := h.compiler.Compile(h.ctx, def)
	if err != nil {
		h.t.Fatalf("compile definition %s v%d: %v", def.WorkflowID, def.Version, err)
	}
	def.RunInputSchema = plan.RunInputSchema
	def.Validation = plan.Validation

	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	}); err != nil {
		h.t.Fatalf("save definition %s v%d: %v", def.WorkflowID, def.Version, err)
	}
	return def
}

func (h *execHarness) createRun(workflowID string, version int, input string) domain.Run {
	h.t.Helper()
	run, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: workflowID, DefinitionVersion: version, Input: json.RawMessage(input),
	})
	if err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	return run
}

func (h *execHarness) advance(runID string) service.AdvanceOutcome {
	h.t.Helper()
	outcome, err := h.svc.Advance(h.ctx, runID)
	if err != nil {
		h.t.Fatalf("advance %s: %v", runID, err)
	}
	return outcome
}

func (h *execHarness) execute(outcome service.AdvanceOutcome) {
	h.t.Helper()
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		h.t.Fatalf("execute node_run=%s attempt=%s: %v", outcome.NodeRunID, outcome.AttemptID, err)
	}
}

// drain repeatedly Advances and Executes runID until Advance makes no further claim,
// exactly like internal/work.Pool.processItem and reconciler.Reconciler.drive. The
// iteration bound only guards against a test bug turning into an infinite loop; no
// document_processing scenario in this file needs more than a handful of claims.
func (h *execHarness) drain(runID string) {
	h.t.Helper()
	const maxIterations = 50
	for i := 0; i < maxIterations; i++ {
		outcome, err := h.svc.Advance(h.ctx, runID)
		if err != nil {
			h.t.Fatalf("drain: advance %s: %v", runID, err)
		}
		if !outcome.Claimed {
			return
		}
		if err := h.svc.Execute(h.ctx, outcome); err != nil {
			h.t.Fatalf("drain: execute node_run=%s: %v", outcome.NodeRunID, err)
		}
	}
	h.t.Fatalf("drain: exceeded %d iterations without Advance reporting unclaimed", maxIterations)
}

// ---------------------------------------------------------------------------
// Fixture Definitions
// ---------------------------------------------------------------------------

// execDocDefinition is the shared "document processing" scenario used across this file:
// text_input --text--> prompt_template --text--> text_generation --prompt/text-->
// text_output. It matches the four M1 built-in node types end to end, with
// text_generation the only EXTERNAL-side-effect node (07 §1.2) and text_output the
// Definition's single Output Node.
func execDocDefinition(workflowID string) domain.Definition {
	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Document Processing",
		Description: "text_input -> prompt_template -> text_generation -> text_output",
		Nodes: []domain.Node{
			{ID: "input", Type: "text_input", Name: "Brief",
				Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			{ID: "prompt", Type: "prompt_template", Name: "Prompt",
				Config: json.RawMessage(`{"template":"Write about {{text}}"}`)},
			{ID: "generate", Type: "text_generation", Name: "Generate",
				Config: json.RawMessage(`{"modelId":"text-model-v1"}`)},
			{ID: "output", Type: "text_output", Name: "Output",
				Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "input", SourceHandle: "text", Target: "prompt", TargetHandle: "text"},
			{ID: "e2", Source: "prompt", SourceHandle: "text", Target: "generate", TargetHandle: "prompt"},
			{ID: "e3", Source: "generate", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: fixtureTime,
	}
}

// execWithPolicy returns a copy of def with policy attached to the named node, leaving
// def itself untouched. Several tests need an explicit ExecutionPolicy on exactly one
// node: runtime.DecideRetry refuses to retry a node with no configured
// ExecutionPolicy (MaxAttempts defaults to 0, so AttemptNo >= MaxAttempts immediately),
// so a retry- or timeout-permission test must set one explicitly to isolate the
// SideEffect/Uncertain decision it actually means to exercise, rather than trivial
// attempt-exhaustion.
func execWithPolicy(def domain.Definition, nodeID string, policy domain.ExecutionPolicy) domain.Definition {
	nodes := make([]domain.Node, len(def.Nodes))
	copy(nodes, def.Nodes)
	for i := range nodes {
		if nodes[i].ID == nodeID {
			p := policy
			nodes[i].ExecutionPolicy = &p
		}
	}
	def.Nodes = nodes
	return def
}

// execAppendFixedEvent commits one Event with a caller-chosen ID directly through the
// Store, bypassing ExecutionService entirely. It exists only to pre-occupy an Event ID
// (the `events` table primary key) for the rollback test below.
func execAppendFixedEvent(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, eventID string, now time.Time) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, runID)
		if err != nil {
			return err
		}
		if _, err := tx.Events().Append(ctx, lock, domain.Event{
			ID: eventID, RunID: runID, Type: domain.EventNodeReady,
			Timestamp: now, Payload: json.RawMessage(`{}`),
		}); err != nil {
			return err
		}
		return tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now)
	}); err != nil {
		t.Fatalf("append fixed event %s: %v", eventID, err)
	}
}

func execNodeRunByNodeID(t *testing.T, nodeRuns []domain.NodeRun, nodeID string) domain.NodeRun {
	t.Helper()
	for _, nr := range nodeRuns {
		if nr.NodeID == nodeID {
			return nr
		}
	}
	t.Fatalf("node run for node id %q not found among %d node runs", nodeID, len(nodeRuns))
	return domain.NodeRun{}
}

func execEventTypes(events []domain.Event) []domain.EventType {
	out := make([]domain.EventType, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. Happy path: full document_processing run, ordered events, Run.output
// ---------------------------------------------------------------------------

func TestExecution_DocumentProcessing_CompletesWithOutputAndOrderedEvents(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_doc_processing"))
	run := h.createRun("wf_doc_processing", 1, `{"brief":"a small ember creature"}`)

	h.drain(run.ID)

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunCompleted {
		t.Fatalf("run status: want COMPLETED, got %s (error=%v)", got.Status, got.Error)
	}

	var output map[string]json.RawMessage
	if err := json.Unmarshal(got.Output, &output); err != nil {
		t.Fatalf("decode run output: %v", err)
	}
	var text string
	if err := json.Unmarshal(output["text"], &text); err != nil {
		t.Fatalf("decode output text port: %v", err)
	}
	const want = "echo: Write about a small ember creature"
	if text != want {
		t.Fatalf("run output text: want %q, got %q", want, text)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	wantTypes := []domain.EventType{
		domain.EventRunCreated,
		domain.EventNodeReady,                              // input
		domain.EventNodeStarted, domain.EventNodeCompleted, // input
		domain.EventNodeReady,                              // prompt
		domain.EventNodeStarted, domain.EventNodeCompleted, // prompt
		domain.EventNodeReady,                              // generate
		domain.EventNodeStarted, domain.EventNodeCompleted, // generate
		domain.EventNodeReady,                              // output
		domain.EventNodeStarted, domain.EventNodeCompleted, // output
		domain.EventRunCompleted,
	}
	gotTypes := execEventTypes(events)
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("event count: want %d %v, got %d %v", len(wantTypes), wantTypes, len(gotTypes), gotTypes)
	}
	for i := range wantTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Fatalf("event[%d]: want %s, got %s (full sequence: %v)", i, wantTypes[i], gotTypes[i], gotTypes)
		}
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Fatalf("event seq not strictly increasing by 1 at index %d: %d -> %d", i, events[i-1].Seq, events[i].Seq)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Crash after CreateRun's own commit, before any Advance call at all: the "input"
//    NodeRun is still plain READY (no Attempt was ever started), so the Reconciler must
//    rediscover it through the plain READY branch of ListReadyOrRetryable, not through a
//    timed-out Attempt (docs/09 §3.1 row 1: "任一 READY 工作提交后、即时推进前崩溃 ->
//    Reconciler 重新发现同一 NodeRun").
// ---------------------------------------------------------------------------

func TestExecution_ProcessCrashAfterCommitBeforeExecute_ReconcilerRediscoversReadyWork(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_crash_before_advance"))

	// CreateRun's transaction commits the Run and its initial READY "input" NodeRun. The
	// process is modelled as crashing right here: this test never calls Advance or
	// Execute itself. h.svc's Queue defaults to service.NoopEnqueuer (Deps.withDefaults),
	// so nothing in-process is watching this Run either -- the Reconciler's own
	// ListReadyOrRetryable scan is the only thing that can find it.
	run := h.createRun("wf_crash_before_advance", 1, `{"brief":"a small ember creature"}`)

	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100})

	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.ReadyOrRetryableFound < 1 {
		t.Fatalf("report: want at least 1 ready/retryable node run discovered through the plain READY branch, got %+v", report)
	}
	if report.RunsAdvanced != 4 {
		t.Fatalf("report: want 4 Advance claims (input, prompt, generate, output) driven to completion in one pass, got %+v", report)
	}

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunCompleted {
		t.Fatalf("run status after reconciler drive: want COMPLETED, got %s (error=%v)", got.Status, got.Error)
	}

	var output map[string]json.RawMessage
	if err := json.Unmarshal(got.Output, &output); err != nil {
		t.Fatalf("decode run output: %v", err)
	}
	var text string
	if err := json.Unmarshal(output["text"], &text); err != nil {
		t.Fatalf("decode output text port: %v", err)
	}
	const want = "echo: Write about a small ember creature"
	if text != want {
		t.Fatalf("run output text: want %q, got %q", want, text)
	}
}

// ---------------------------------------------------------------------------
// 2b. Crash after Advance's own claim commits, before Execute: the STARTED Attempt is not
//     yet visible to ListReadyOrRetryable (it is RUNNING, not READY, and its backoff has
//     not elapsed), so the Reconciler can only rediscover it once its deadline expires via
//     ListExpired -- distinct from TestExecution_ProcessCrashAfterCommitBeforeExecute_
//     ReconcilerRediscoversReadyWork below, which crashes before any Advance call at all
//     and is rediscovered through the plain READY branch of ListReadyOrRetryable instead.
// ---------------------------------------------------------------------------

func TestExecution_ProcessCrashAfterClaimBeforeExecute_ReconcilerTimesOutAndRetries(t *testing.T) {
	h := newExecHarness(t)
	def := execWithPolicy(execDocDefinition("wf_crash_recovery"), "input",
		domain.ExecutionPolicy{TimeoutMs: 5000, MaxAttempts: 2, Backoff: domain.BackoffFixed})
	h.saveDefinition(def)
	run := h.createRun("wf_crash_recovery", 1, `{"brief":"a small ember creature"}`)

	// Advance claims the "input" NodeRun and commits its STARTED Attempt. The process
	// is modelled as crashing right here: Execute is deliberately never called.
	outcome := h.advance(run.ID)
	if !outcome.Claimed || outcome.NodeType != "text_input" {
		t.Fatalf("advance: want a claim on text_input, got %+v", outcome)
	}

	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100})

	// First pass: nothing is READY (the only NodeRun is RUNNING with no elapsed
	// backoff), but the STARTED Attempt's deadline has now passed, so it is expired.
	// TimeoutAttempt fails it (uncertain=false: text_input has no external side
	// effect) and, since MaxAttempts=2 permits a second attempt, reschedules it rather
	// than failing the Run outright.
	h.clock.Advance(6 * time.Second)
	report1, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once (1): %v", err)
	}
	if report1.ExpiredAttemptsFound != 1 || report1.AttemptsTimedOut != 1 {
		t.Fatalf("report1: want 1 expired attempt timed out, got %+v", report1)
	}
	if report1.RunsAdvanced != 0 {
		t.Fatalf("report1: want no Advance claim yet (retry not due), got RunsAdvanced=%d", report1.RunsAdvanced)
	}

	nr := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if nr.Status != domain.NodeRunRunning || nr.NextAttemptAt == nil {
		t.Fatalf("node run after timeout: want RUNNING with a scheduled retry, got status=%s nextAttemptAt=%v", nr.Status, nr.NextAttemptAt)
	}

	// Second pass, once the rescheduled backoff has elapsed: the Reconciler alone
	// (nothing in this test ever calls Advance/Execute directly again) rediscovers and
	// drives the retried "input" NodeRun and every node after it through to
	// completion, entirely from persisted state.
	h.clock.Advance(2 * time.Second)
	report2, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once (2): %v", err)
	}
	if report2.RunsAdvanced != 4 {
		t.Fatalf("report2: want 4 Advance claims (retried input, prompt, generate, output), got %+v", report2)
	}

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunCompleted {
		t.Fatalf("run status after reconciler drive: want COMPLETED, got %s (error=%v)", got.Status, got.Error)
	}
}

// ---------------------------------------------------------------------------
// 3. A late completion of an Attempt that a timeout already resolved is ignored.
// ---------------------------------------------------------------------------

func TestExecution_StaleAttemptCompletion_IsIgnored(t *testing.T) {
	h := newExecHarness(t)
	def := execWithPolicy(execDocDefinition("wf_stale_completion"), "input",
		domain.ExecutionPolicy{TimeoutMs: 5000, MaxAttempts: 1, Backoff: domain.BackoffFixed})
	h.saveDefinition(def)
	run := h.createRun("wf_stale_completion", 1, `{"brief":"a small ember creature"}`)

	outcome := h.advance(run.ID)
	if !outcome.Claimed {
		t.Fatal("advance: want a claim, got none")
	}

	h.clock.Advance(6 * time.Second)
	if err := h.svc.TimeoutAttempt(h.ctx, outcome.AttemptID); err != nil {
		t.Fatalf("timeout attempt: %v", err)
	}

	failedRun := getRun(h.ctx, t, h.uow, run.ID)
	if failedRun.Status != domain.RunFailed {
		t.Fatalf("run status after timeout (MaxAttempts=1, no retry): want FAILED, got %s", failedRun.Status)
	}
	beforeEvents := listEvents(h.ctx, t, h.uow, run.ID)

	// A late completion of the same, now-stale Attempt: e.g. a synchronous Execute call
	// that was still in flight when the Reconciler's timeout won the race.
	err := h.svc.CompleteNode(h.ctx, service.CompleteNode{
		AttemptID: outcome.AttemptID,
		Output:    registry.NodeOutput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`"too late"`)}},
	})
	if err != nil {
		t.Fatalf("late CompleteNode on a stale attempt: want nil (silently ignored), got %v", err)
	}

	afterRun := getRun(h.ctx, t, h.uow, run.ID)
	if afterRun.Status != domain.RunFailed {
		t.Fatalf("run status after stale completion: want still FAILED, got %s", afterRun.Status)
	}
	afterNR := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if afterNR.Status != domain.NodeRunFailed {
		t.Fatalf("node run status after stale completion: want still FAILED, got %s", afterNR.Status)
	}
	afterEvents := listEvents(h.ctx, t, h.uow, run.ID)
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("event count after stale completion: want unchanged at %d, got %d", len(beforeEvents), len(afterEvents))
	}
}

// ---------------------------------------------------------------------------
// 4. A duplicate CompleteNode call for an already-SUCCEEDED Attempt is a no-op.
// ---------------------------------------------------------------------------

func TestExecution_DuplicateComplete_SecondCallNoOp(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_duplicate_complete"))
	run := h.createRun("wf_duplicate_complete", 1, `{"brief":"a small ember creature"}`)

	outcome := h.advance(run.ID)
	if outcome.NodeType != "text_input" {
		t.Fatalf("advance: want text_input claimed first, got %s", outcome.NodeType)
	}

	complete := service.CompleteNode{
		AttemptID: outcome.AttemptID,
		Output:    registry.NodeOutput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`"a small ember creature"`)}},
	}
	if err := h.svc.CompleteNode(h.ctx, complete); err != nil {
		t.Fatalf("first CompleteNode: %v", err)
	}

	beforeEvents := listEvents(h.ctx, t, h.uow, run.ID)
	beforeNodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)

	// Duplicate delivery of the same result for the same Attempt (e.g. a retried
	// callback, or a race between two delivery paths).
	if err := h.svc.CompleteNode(h.ctx, complete); err != nil {
		t.Fatalf("duplicate CompleteNode: want nil (silently ignored), got %v", err)
	}

	afterEvents := listEvents(h.ctx, t, h.uow, run.ID)
	afterNodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("event count after duplicate complete: want unchanged at %d, got %d", len(beforeEvents), len(afterEvents))
	}
	if len(afterNodeRuns) != len(beforeNodeRuns) {
		t.Fatalf("node run count after duplicate complete: want unchanged at %d (no second 'prompt' NodeRun created), got %d", len(beforeNodeRuns), len(afterNodeRuns))
	}
}

// ---------------------------------------------------------------------------
// 5. Concurrent Advance on the same Run: exactly one caller ever claims the single
//    execution slot. Real PostgreSQL locking is the only thing that can prove this.
// ---------------------------------------------------------------------------

func TestExecution_ConcurrentAdvance_SameRun_OnlyOneRunningNodeRun(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_concurrent_advance"))
	run := h.createRun("wf_concurrent_advance", 1, `{"brief":"a small ember creature"}`)

	const claimants = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(claimants)

	outcomes := make([]service.AdvanceOutcome, claimants)
	errs := make([]error, claimants)

	for i := 0; i < claimants; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait()
			outcomes[i], errs[i] = h.svc.Advance(h.ctx, run.ID)
		}(i)
	}
	start.Done()
	done.Wait()

	claimed := 0
	for i := 0; i < claimants; i++ {
		if errs[i] != nil {
			t.Fatalf("advance[%d]: unexpected error: %v", i, errs[i])
		}
		if outcomes[i].Claimed {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent Advance claimants: want exactly 1 winner, got %d", claimed)
	}

	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	inputNR := execNodeRunByNodeID(t, nodeRuns, "input")
	if inputNR.Status != domain.NodeRunRunning {
		t.Fatalf("input node run status: want RUNNING, got %s", inputNR.Status)
	}
	if inputNR.AttemptCount != 1 {
		t.Fatalf("input node run attempt count: want exactly 1 (one winner started exactly one Attempt), got %d", inputNR.AttemptCount)
	}
}

// ---------------------------------------------------------------------------
// 6. Direct TimeoutAttempt: EXTERNAL+UNKNOWN uncertainty forbids retry outright, no
//    matter how much attempt budget remains.
// ---------------------------------------------------------------------------

func TestExecution_ExternalUnknownTimeout_NoRetryRunFailed(t *testing.T) {
	h := newExecHarness(t)
	def := execWithPolicy(execDocDefinition("wf_external_timeout"), "generate",
		domain.ExecutionPolicy{TimeoutMs: 5000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	h.saveDefinition(def)
	run := h.createRun("wf_external_timeout", 1, `{"brief":"a small ember creature"}`)

	h.execute(h.advance(run.ID)) // input
	h.execute(h.advance(run.ID)) // prompt

	outcome := h.advance(run.ID) // generate: claimed, deliberately never executed
	if !outcome.Claimed || outcome.NodeType != "text_generation" {
		t.Fatalf("advance: want a claim on text_generation, got %+v", outcome)
	}

	h.clock.Advance(6 * time.Second)
	if err := h.svc.TimeoutAttempt(h.ctx, outcome.AttemptID); err != nil {
		t.Fatalf("timeout attempt: %v", err)
	}

	nr := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if nr.Status != domain.NodeRunFailed {
		t.Fatalf("generate node run status: want FAILED (uncertain external result forbids retry despite 2 remaining attempts), got %s", nr.Status)
	}
	if nr.NextAttemptAt != nil {
		t.Fatalf("generate node run: want no retry scheduled, got NextAttemptAt=%v", nr.NextAttemptAt)
	}
	if nr.Error == nil || nr.Error.Code != "TIMEOUT" {
		t.Fatalf("generate node run error: want code TIMEOUT, got %+v", nr.Error)
	}

	run2 := getRun(h.ctx, t, h.uow, run.ID)
	if run2.Status != domain.RunFailed {
		t.Fatalf("run status: want FAILED, got %s", run2.Status)
	}
}

// ---------------------------------------------------------------------------
// 7. Retry permitted: a definite (non-uncertain) failure of an EXTERNAL node may retry,
//    and the retried Attempt succeeds.
// ---------------------------------------------------------------------------

func TestExecution_RetryPermitted_TransientFailureSucceedsOnSecondAttempt(t *testing.T) {
	h := newExecHarness(t)
	def := execWithPolicy(execDocDefinition("wf_retry_permitted"), "generate",
		domain.ExecutionPolicy{TimeoutMs: 5000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	h.saveDefinition(def)
	run := h.createRun("wf_retry_permitted", 1, `{"brief":"a small ember creature"}`)

	var calls int32
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if atomic.AddInt32(&calls, 1) == 1 {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFail}
		}
		return nil // fall through to the default FINAL scenario
	}

	// Drains input and prompt, then generate's first Attempt fails a definite
	// (non-timeout) error: isUncertainFailure only classifies a context deadline as
	// uncertain, so Uncertain=false here and DecideRetry permits a retry.
	h.drain(run.ID)

	nr := getNodeRun(h.ctx, t, h.uow, execNodeRunByNodeID(t, listNodeRuns(h.ctx, t, h.uow, run.ID), "generate").ID)
	if nr.Status != domain.NodeRunRunning || nr.NextAttemptAt == nil {
		t.Fatalf("generate node run after first failure: want RUNNING with a scheduled retry, got status=%s nextAttemptAt=%v", nr.Status, nr.NextAttemptAt)
	}
	if nr.AttemptCount != 1 {
		t.Fatalf("generate node run attempt count after first failure: want 1, got %d", nr.AttemptCount)
	}

	h.clock.Advance(2 * time.Second)
	h.drain(run.ID)

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunCompleted {
		t.Fatalf("run status after retry succeeds: want COMPLETED, got %s (error=%v)", got.Status, got.Error)
	}

	finalNR := getNodeRun(h.ctx, t, h.uow, nr.ID)
	if finalNR.AttemptCount != 2 {
		t.Fatalf("generate node run final attempt count: want 2 (old Attempt preserved, retry created a new one), got %d", finalNR.AttemptCount)
	}

	events := execEventTypes(listEvents(h.ctx, t, h.uow, run.ID))
	sawRetrying := false
	for _, typ := range events {
		if typ == domain.EventNodeRetrying {
			sawRetrying = true
		}
	}
	if !sawRetrying {
		t.Fatalf("event log: want a NODE_RETRYING event, got %v", events)
	}
}

// ---------------------------------------------------------------------------
// 8. An Event insert failure rolls back the whole Advance transaction, including the
//    NodeRun claim and Attempt creation made earlier in the same transaction.
// ---------------------------------------------------------------------------

func TestExecution_EventInsertFailure_RollsBackNodeRunTransition(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_event_rollback"))
	run := h.createRun("wf_event_rollback", 1, `{"brief":"a small ember creature"}`)

	beforeEvents := listEvents(h.ctx, t, h.uow, run.ID)
	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	inputNR := execNodeRunByNodeID(t, nodeRuns, "input")

	// Occupy an Event ID (the `events` table's primary key) in its own committed
	// transaction, then force ExecutionService's very next Event ID allocation to
	// collide with it. Advance's startAttempt appends exactly one Event
	// (NODE_STARTED) after already claiming the NodeRun and creating the Attempt in
	// the same transaction, so this Event insert is the one that fails.
	const collidingID = "evt_forced_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingID, h.clock.Now())
	h.ids.ForceNextEventID(collidingID)

	_, err := h.svc.Advance(h.ctx, run.ID)
	if err == nil {
		t.Fatal("advance with a forced Event ID collision: want an error, got nil")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("advance error: want wrapped domain.ErrConflict, got %v", err)
	}

	afterNR := getNodeRun(h.ctx, t, h.uow, inputNR.ID)
	if afterNR.Status != domain.NodeRunReady {
		t.Fatalf("input node run status after rollback: want still READY, got %s", afterNR.Status)
	}
	if afterNR.AttemptCount != 0 {
		t.Fatalf("input node run attempt count after rollback: want 0, got %d", afterNR.AttemptCount)
	}

	afterEvents := listEvents(h.ctx, t, h.uow, run.ID)
	// beforeEvents already includes the manually-appended collidingID event; the
	// rolled-back transaction must add nothing on top of it.
	if len(afterEvents) != len(beforeEvents)+1 {
		t.Fatalf("event count after rollback: want %d (before + only the manually appended collision event), got %d", len(beforeEvents)+1, len(afterEvents))
	}
	for _, ev := range afterEvents {
		if ev.Type == domain.EventNodeStarted {
			t.Fatalf("event log after rollback: want no NODE_STARTED event, found one: %+v", ev)
		}
	}
}

// ---------------------------------------------------------------------------
// 9. A Run stays bound to the Definition version it started with; a newer version of
//    the same Workflow never affects it.
// ---------------------------------------------------------------------------

func TestExecution_RunBoundToDefinitionVersion_NewVersionDoesNotAffectRun(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_version_bound"))
	run := h.createRun("wf_version_bound", 1, `{"brief":"a small ember creature"}`)

	v2 := execDocDefinition("wf_version_bound")
	v2.Version = 2
	for i := range v2.Nodes {
		if v2.Nodes[i].ID == "prompt" {
			v2.Nodes[i].Config = json.RawMessage(`{"template":"Rewrite for clarity: {{text}}"}`)
		}
	}
	h.saveDefinition(v2)

	h.drain(run.ID)

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunCompleted {
		t.Fatalf("run status: want COMPLETED, got %s (error=%v)", got.Status, got.Error)
	}
	if got.DefinitionVersion != 1 {
		t.Fatalf("run definition version: want still 1, got %d", got.DefinitionVersion)
	}

	var output map[string]json.RawMessage
	if err := json.Unmarshal(got.Output, &output); err != nil {
		t.Fatalf("decode run output: %v", err)
	}
	var text string
	if err := json.Unmarshal(output["text"], &text); err != nil {
		t.Fatalf("decode output text port: %v", err)
	}
	// v1's prompt template, not v2's: proves the Run compiled and executed against the
	// frozen version it was created with, never the newer one saved afterward.
	const want = "echo: Write about a small ember creature"
	if text != want {
		t.Fatalf("run output text: want v1's template result %q, got %q", want, text)
	}
}

// ---------------------------------------------------------------------------
// 10. CreateRun with input that fails the frozen runInputSchema writes nothing.
// ---------------------------------------------------------------------------

func TestExecution_CreateRun_InvalidInput_RejectedNoRowsWritten(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_invalid_input"))

	_, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: "wf_invalid_input", DefinitionVersion: 1, Input: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("create run with input missing the required 'brief' key: want an error, got nil")
	}
	var invalidErr *service.RunInputInvalidError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("create run error: want *service.RunInputInvalidError, got %T: %v", err, err)
	}

	var latest *domain.Run
	if txErr := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		latest, err = tx.Runs().LatestByWorkflow(ctx, "wf_invalid_input")
		return err
	}); txErr != nil {
		t.Fatalf("query latest run: %v", txErr)
	}
	if latest != nil {
		t.Fatalf("latest run after rejected CreateRun: want none, got %+v", latest)
	}
}

// ---------------------------------------------------------------------------
// 11. CreateRun against a frozen version whose Node Type the current Registry can no
//     longer resolve is rejected, not silently substituted.
// ---------------------------------------------------------------------------

func TestExecution_CreateRun_UnknownNodeTypeInFrozenVersion_Rejected(t *testing.T) {
	h := newExecHarness(t)

	// Saved directly through the Store, bypassing the Compiler entirely: this
	// simulates a version that was valid when saved (e.g. against a Registry that has
	// since dropped this Node Type), which the frozen Definition itself can still
	// represent even though nothing compiles it anymore.
	def := domain.Definition{
		WorkflowID:  "wf_unknown_node_type",
		Version:     1,
		Name:        "Stale Registration",
		Description: "references a node type the current registry cannot resolve",
		Nodes: []domain.Node{
			{ID: "n1", Type: "does_not_exist", Name: "Bad Node", Config: json.RawMessage(`{}`)},
		},
		Edges:          []domain.Edge{},
		RunInputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{},"required":[]}`),
		Validation: domain.Validation{
			Status: domain.ValidationValid, ValidatorVersion: "mvp-v1", ValidatedAt: fixtureTime,
		},
		CreatedAt: fixtureTime,
	}
	saveDefinition(h.ctx, t, h.uow, def)

	_, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: "wf_unknown_node_type", DefinitionVersion: 1, Input: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("create run against an unresolvable frozen version: want an error, got nil")
	}
	var regErr *service.RegistryResolutionError
	if !errors.As(err, &regErr) {
		t.Fatalf("create run error: want *service.RegistryResolutionError, got %T: %v", err, err)
	}

	var latest *domain.Run
	if txErr := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		latest, err = tx.Runs().LatestByWorkflow(ctx, "wf_unknown_node_type")
		return err
	}); txErr != nil {
		t.Fatalf("query latest run: %v", txErr)
	}
	if latest != nil {
		t.Fatalf("latest run after rejected CreateRun: want none, got %+v", latest)
	}
}

// ---------------------------------------------------------------------------
// 12. reconciler.Reconciler.RunOnce times out an expired Attempt through its own
//     ListExpired scan and, for an EXTERNAL+UNKNOWN node, fails the Run without
//     scheduling a retry.
// ---------------------------------------------------------------------------

func TestReconciler_RunOnce_TimesOutExpiredAttempts(t *testing.T) {
	h := newExecHarness(t)
	def := execWithPolicy(execDocDefinition("wf_reconciler_timeout"), "generate",
		domain.ExecutionPolicy{TimeoutMs: 5000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	h.saveDefinition(def)
	run := h.createRun("wf_reconciler_timeout", 1, `{"brief":"a small ember creature"}`)

	h.execute(h.advance(run.ID)) // input
	h.execute(h.advance(run.ID)) // prompt

	outcome := h.advance(run.ID) // generate: claimed, deliberately never executed
	if !outcome.Claimed || outcome.NodeType != "text_generation" {
		t.Fatalf("advance: want a claim on text_generation, got %+v", outcome)
	}

	h.clock.Advance(6 * time.Second)
	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100})
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.ExpiredAttemptsFound != 1 || report.AttemptsTimedOut != 1 {
		t.Fatalf("report: want 1 expired attempt timed out, got %+v", report)
	}
	if report.RunsAdvanced != 0 {
		t.Fatalf("report: want no further Advance claim (uncertain external result forbids retry), got RunsAdvanced=%d", report.RunsAdvanced)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report: want no errors, got %v", report.Errors)
	}

	nr := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if nr.Status != domain.NodeRunFailed {
		t.Fatalf("generate node run status: want FAILED, got %s", nr.Status)
	}

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunFailed {
		t.Fatalf("run status: want FAILED, got %s", got.Status)
	}

	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	for _, nr := range nodeRuns {
		if nr.NodeID == "output" {
			t.Fatalf("node runs after reconciler timeout: want no 'output' node run created (generate never succeeded), found %+v", nr)
		}
	}
}

// ---------------------------------------------------------------------------
// 13. A full in-process work queue refuses the post-COMMIT enqueue without rolling back
//     or blocking the committing transaction, and the Reconciler eventually advances the
//     work the queue dropped (docs/09 §3.5: "in-process work queue 已满 -> 提交事务不回
//     滚、不阻塞持锁事务；Reconciler 最终推进遗漏的工作").
// ---------------------------------------------------------------------------

// execRecordingEnqueuer wraps a real *work.Queue so a test can observe which
// EnqueueAdvance calls the bounded queue refused. It delegates every decision to the
// production queue; it never changes the outcome.
type execRecordingEnqueuer struct {
	inner *work.Queue

	mu       sync.Mutex
	accepted []string
	refused  []string
}

func (e *execRecordingEnqueuer) EnqueueAdvance(runID string) bool {
	ok := e.inner.EnqueueAdvance(runID)
	e.mu.Lock()
	if ok {
		e.accepted = append(e.accepted, runID)
	} else {
		e.refused = append(e.refused, runID)
	}
	e.mu.Unlock()
	return ok
}

func (e *execRecordingEnqueuer) snapshot() (accepted, refused []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.accepted...), append([]string(nil), e.refused...)
}

func TestExecution_WorkQueueFull_CommitSucceedsAndReconcilerAdvances(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_queue_full"))

	// A real bounded work.Queue at capacity 1, pre-filled and with no Pool draining it:
	// every EnqueueAdvance this service makes hits a full queue and is refused. The
	// refusal is a synchronous return value, so nothing here waits on time.
	queue := work.NewQueue(1)
	if !queue.EnqueueAdvance("run_preoccupying_slot") {
		t.Fatal("pre-fill enqueue on an empty capacity-1 queue: want accepted")
	}
	enq := &execRecordingEnqueuer{inner: queue}

	svc := service.NewExecutionService(service.Deps{
		UoW: h.uow, Nodes: h.nodes, Models: h.models, Compiler: h.compiler,
		Clock: h.clock, IDs: h.ids, Queue: enq,
	})

	run, err := svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: "wf_queue_full", DefinitionVersion: 1,
		Input: json.RawMessage(`{"brief":"a small ember creature"}`),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Drive exactly one node: Execute's CompleteNode transaction commits the SUCCEEDED
	// "input" NodeRun, the downstream READY "prompt" NodeRun and its NODE_READY Event,
	// and only then offers the Run to the (full) queue. If the refused enqueue rolled
	// anything back, the READY fact below would be missing; if it blocked the
	// transaction, Execute would never return and the test could not proceed.
	outcome, err := svc.Advance(h.ctx, run.ID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !outcome.Claimed || outcome.NodeType != "text_input" {
		t.Fatalf("advance: want a claim on text_input, got %+v", outcome)
	}
	if err := svc.Execute(h.ctx, outcome); err != nil {
		t.Fatalf("execute input node: %v", err)
	}

	accepted, refused := enq.snapshot()
	if len(accepted) != 0 {
		t.Fatalf("full queue accepted %v, want none: the capacity-1 queue was pre-filled and never drained", accepted)
	}
	refusedThisRun := 0
	for _, id := range refused {
		if id == run.ID {
			refusedThisRun++
		}
	}
	if refusedThisRun < 1 {
		t.Fatalf("want at least one refused enqueue for run %s (the queue-full case under test), got refusals %v", run.ID, refused)
	}

	// The committed facts survived the refused enqueue: downstream "prompt" is READY
	// and its NODE_READY Event is persisted, with the Run still RUNNING.
	promptNR := execNodeRunByNodeID(t, listNodeRuns(h.ctx, t, h.uow, run.ID), "prompt")
	if promptNR.Status != domain.NodeRunReady {
		t.Fatalf("prompt node run status after refused enqueue: want READY (commit not rolled back), got %s", promptNR.Status)
	}
	committedTypes := execEventTypes(listEvents(h.ctx, t, h.uow, run.ID))
	if last := committedTypes[len(committedTypes)-1]; last != domain.EventNodeReady {
		t.Fatalf("last committed event after refused enqueue: want NODE_READY for prompt, got %s (full sequence: %v)", last, committedTypes)
	}
	midRun := getRun(h.ctx, t, h.uow, run.ID)
	if midRun.Status != domain.RunRunning {
		t.Fatalf("run status after refused enqueue: want RUNNING, got %s (error=%v)", midRun.Status, midRun.Error)
	}

	// The Reconciler alone rediscovers the persisted READY "prompt" NodeRun the queue
	// dropped, and drives prompt, generate and output to completion. Its Executor is
	// the same svc, so every completion along the way keeps hitting the full queue --
	// and keeps being recovered by this same scan.
	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: svc, Clock: h.clock, BatchLimit: 100})
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if report.ReadyOrRetryableFound < 1 {
		t.Fatalf("report: want the dropped READY prompt node run rediscovered, got %+v", report)
	}
	if report.RunsAdvanced != 3 {
		t.Fatalf("report: want 3 Advance claims (prompt, generate, output), got %+v", report)
	}

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunCompleted {
		t.Fatalf("run status after reconciler drive: want COMPLETED, got %s (error=%v)", got.Status, got.Error)
	}

	// No duplicate Events: the log is exactly the single happy-path sequence with a
	// strictly increasing seq, despite every post-COMMIT enqueue having been refused.
	events := listEvents(h.ctx, t, h.uow, run.ID)
	wantTypes := []domain.EventType{
		domain.EventRunCreated,
		domain.EventNodeReady,                              // input
		domain.EventNodeStarted, domain.EventNodeCompleted, // input
		domain.EventNodeReady,                              // prompt
		domain.EventNodeStarted, domain.EventNodeCompleted, // prompt
		domain.EventNodeReady,                              // generate
		domain.EventNodeStarted, domain.EventNodeCompleted, // generate
		domain.EventNodeReady,                              // output
		domain.EventNodeStarted, domain.EventNodeCompleted, // output
		domain.EventRunCompleted,
	}
	gotTypes := execEventTypes(events)
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("event count: want %d %v, got %d %v", len(wantTypes), wantTypes, len(gotTypes), gotTypes)
	}
	for i := range wantTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Fatalf("event[%d]: want %s, got %s (full sequence: %v)", i, wantTypes[i], gotTypes[i], gotTypes)
		}
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Fatalf("event seq not strictly increasing by 1 at index %d: %d -> %d", i, events[i-1].Seq, events[i].Seq)
		}
	}

	// No duplicate Attempts: each of the four NodeRuns ran exactly once.
	finalNodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	if len(finalNodeRuns) != 4 {
		t.Fatalf("node run count: want 4, got %d", len(finalNodeRuns))
	}
	for _, nr := range finalNodeRuns {
		if nr.AttemptCount != 1 {
			t.Fatalf("node %s attempt count: want exactly 1 (no duplicate Attempt), got %d", nr.NodeID, nr.AttemptCount)
		}
	}
}
