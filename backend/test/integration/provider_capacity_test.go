//go:build integration

// Provider capacity integration tests. A slot is a persisted fact taken inside the claim
// transaction and freed inside the terminal transaction, and the rules proven here are
// transactional or races: the held count never exceeds the limit at any commit point, a
// crash leaves occupancy exactly as committed, two Runs serialize on the Provider row
// lock, and a refusal writes its Event once. None of this can be shown with a fake
// repository, so everything runs against PostgreSQL.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

const (
	capTaskType       = "capacity_task"
	capSyncType       = "capacity_sync"
	capProviderID     = "capacity-provider-v1"
	capFreeProviderID = "capacity-free-provider-v1"
	capModelID        = "capacity-model"
	capFreeModelID    = "capacity-free-model"
	capSigningSecret  = "capacity-callback-signing-secret"
	capBaseURL        = "https://emberling.test"
)

// ---------------------------------------------------------------------------
// Deterministic nodes
// ---------------------------------------------------------------------------

// capDispatch is what one accepted dispatch leaves the test holding: the plaintext
// callback credential, which exists nowhere else, and the external task id.
type capDispatch struct {
	token          string
	externalTaskID string
}

// capTaskExecutor dispatches to an imaginary Provider that never answers on its own. The
// Provider of a task is the one the configured model belongs to, exactly as the Runtime
// resolved it for admission.
type capTaskExecutor struct {
	mu          sync.Mutex
	dispatches  map[string]capDispatch // by NodeRun id, latest Attempt
	dispatchErr error
}

func (e *capTaskExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (e *capTaskExecutor) setDispatchErr(err error) {
	e.mu.Lock()
	e.dispatchErr = err
	e.mu.Unlock()
}
func (e *capTaskExecutor) dispatchOf(nodeRunID string) (capDispatch, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.dispatches[nodeRunID]
	return d, ok
}
func (e *capTaskExecutor) Execute(_ context.Context, in registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dispatchErr != nil {
		return registry.NodeResult{}, e.dispatchErr
	}
	if in.Callback == nil || in.Callback.Token == "" {
		return registry.NodeResult{}, errors.New("capacity_task: no callback credential was provided")
	}
	providerID := capFreeProviderID
	if modelID, _ := config["modelId"].(string); modelID == capModelID {
		providerID = capProviderID
	}
	taskID := fmt.Sprintf("task-%s-%d", in.NodeRunID, in.AttemptNo)
	if e.dispatches == nil {
		e.dispatches = make(map[string]capDispatch)
	}
	e.dispatches[in.NodeRunID] = capDispatch{token: in.Callback.Token, externalTaskID: taskID}
	return registry.NodeResult{
		Kind:         registry.NodeResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: providerID, ExternalTaskID: taskID},
	}, nil
}

func (e *capTaskExecutor) OnCallback(_ context.Context, _ registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error) {
	var body struct {
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.NodeOutput{}, fmt.Errorf("capacity_task: undecodable callback payload: %w", err)
	}
	if body.Status == "FAILED" {
		return registry.NodeOutput{}, &registry.ProviderFailure{
			Err: domain.ExecutionError{Code: "PROVIDER_TASK_FAILED", Message: "provider reported the task failed"},
		}
	}
	return registry.NodeOutput{Ports: map[string]json.RawMessage{
		"text": json.RawMessage(strconv.Quote(body.Text)),
	}}, nil
}

// capSyncExecutor joins up to two texts, or fails when its config says so. It stands in
// for every synchronous node a capacity scenario needs: a gate, a join, a failure.
type capSyncExecutor struct{}

func (capSyncExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (capSyncExecutor) Execute(_ context.Context, in registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	if fail, _ := config["fail"].(bool); fail {
		return registry.NodeResult{}, errors.New("capacity_sync: configured to fail")
	}
	a, _ := in.Port("a")
	b, _ := in.Port("b")
	return registry.CompletedResult(map[string]json.RawMessage{
		"text": json.RawMessage(strconv.Quote(string(a) + "+" + string(b))),
	}), nil
}

func capTaskRegistration(executor *capTaskExecutor) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          capTaskType,
			DisplayName:   "Capacity Task",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionAsync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText}},
			ConfigSchema: json.RawMessage(`{"type":"object","properties":{"modelId":{"type":"string"}},
				"required":["modelId"],"additionalProperties":false}`),
			SideEffect:        domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
			DispatchModelPath: "modelId",
		},
		Binding: registry.ExecutorBinding{Executor: executor},
	}
}

func capSyncRegistration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          capSyncType,
			DisplayName:   "Capacity Sync",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs: []domain.PortMetadata{
				{Name: "a", DataType: domain.PortTypeText, Required: true},
				{Name: "b", DataType: domain.PortTypeText, Required: false},
			},
			Outputs:      []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText}},
			ConfigSchema: json.RawMessage(`{"type":"object","properties":{"fail":{"type":"boolean"}},"additionalProperties":false}`),
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: capSyncExecutor{}},
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// capQueue records every Run the service offered to the work queue after a commit, so a
// test can assert which Run a released slot was handed to.
type capQueue struct {
	mu       sync.Mutex
	advances []string
}

func (q *capQueue) EnqueueAdvance(runID string) bool {
	q.mu.Lock()
	q.advances = append(q.advances, runID)
	q.mu.Unlock()
	return true
}

func (q *capQueue) EnqueueAgentTurn(string, string) bool { return true }

func (q *capQueue) advanced() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.advances...)
}

type capHarness struct {
	t   *testing.T
	ctx context.Context

	pool  *pgxpool.Pool
	uow   store.UnitOfWork
	clock *execClock
	ids   *execForcedIDs
	exec  *capTaskExecutor
	queue *capQueue
	hooks service.CapacityHooks
	limit map[string]int
	svc   *service.ExecutionService
}

func newCapHarness(t *testing.T, limit int) *capHarness {
	t.Helper()
	pool := testdb.Open(t)
	h := &capHarness{
		t: t, ctx: context.Background(), pool: pool, uow: postgres.NewUnitOfWork(pool),
		clock: newExecClock(execDeadlineClockStart()),
		ids:   newExecForcedIDs(),
		exec:  &capTaskExecutor{},
		queue: &capQueue{},
		limit: map[string]int{capProviderID: limit},
	}
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ProviderSlots().SeedLocks(ctx, []string{capProviderID})
	}); err != nil {
		t.Fatalf("seed provider slot locks: %v", err)
	}
	h.svc = h.newService()
	return h
}

func (h *capHarness) registries() (*registry.NodeRegistry, *registry.ModelRegistry) {
	h.t.Helper()
	nodes := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(), textoutput.Registration(), capTaskRegistration(h.exec), capSyncRegistration(),
	} {
		if err := nodes.Register(reg); err != nil {
			h.t.Fatalf("register node type: %v", err)
		}
	}
	models := registry.NewModelRegistry()
	for providerID, modelID := range map[string]string{capProviderID: capModelID, capFreeProviderID: capFreeModelID} {
		provider := mocktask.NewTaskModels(providerID, []mocktask.TaskModel{{ID: modelID, DisplayName: modelID}})
		if err := models.Register(h.ctx, provider); err != nil {
			h.t.Fatalf("register models of %s: %v", providerID, err)
		}
	}
	return nodes, models
}

// newService builds an ExecutionService over this harness's database. Calling it again
// models a process restart: registries, plan cache and queue are fresh, and PostgreSQL is
// the only carried-over state.
func (h *capHarness) newService() *service.ExecutionService {
	h.t.Helper()
	nodes, models := h.registries()
	return service.NewExecutionService(service.Deps{
		UoW:      h.uow,
		Nodes:    nodes,
		Models:   models,
		Compiler: runtime.NewCompiler(nodes, h.clock),
		Clock:    h.clock,
		IDs:      h.ids,
		Queue:    h.queue,
		Callback: service.CallbackConfig{
			BaseURL: capBaseURL, SigningSecret: []byte(capSigningSecret), PendingTTL: time.Hour,
		},
		ProviderCapacity: h.limit,
		CapacityHooks:    h.hooks,
	})
}

func (h *capHarness) save(def domain.Definition) domain.Definition {
	h.t.Helper()
	nodes, _ := h.registries()
	plan, err := runtime.NewCompiler(nodes, h.clock).Compile(h.ctx, def)
	if err != nil {
		h.t.Fatalf("compile %s: %v", def.WorkflowID, err)
	}
	def.RunInputSchema = plan.RunInputSchema
	def.Validation = plan.Validation
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	}); err != nil {
		h.t.Fatalf("save %s: %v", def.WorkflowID, err)
	}
	return def
}

func capWorkflowID(prefix string) string {
	return "wf_cap_" + prefix + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func capTaskNode(id, modelID string) domain.Node {
	return domain.Node{ID: id, Type: capTaskType, Name: id, Config: json.RawMessage(`{"modelId":"` + modelID + `"}`)}
}

// linearDefinition is text_input -> capacity_task(modelID) -> text_output.
func (h *capHarness) linearDefinition(modelID string, policy *domain.ExecutionPolicy) domain.Definition {
	task := capTaskNode("task", modelID)
	task.ExecutionPolicy = policy
	return h.save(domain.Definition{
		WorkflowID: capWorkflowID("linear"), Version: 1, Name: "Capacity linear", Description: "one governed task",
		Nodes: []domain.Node{
			{ID: "a_input", Type: "text_input", Name: "Brief", Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			task,
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "a_input", SourceHandle: "text", Target: "task", TargetHandle: "text"},
			{ID: "e2", Source: "task", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: h.clock.Now(),
	})
}

// fanOutDefinition has two governed branches that join before the output. Node ids are
// chosen so the compiled order is a_input, b_wait, c_late, z_task, join, output: z_task
// becomes READY right after the input, while c_late only becomes READY once b_wait (an
// asynchronous task of an unlimited Provider) completes, yet the scheduler prefers c_late
// over z_task whenever both are READY. With lateFails, c_late is a synchronous node that
// fails instead of a governed task.
func (h *capHarness) fanOutDefinition(lateFails bool) domain.Definition {
	late := capTaskNode("c_late", capModelID)
	if lateFails {
		late = domain.Node{ID: "c_late", Type: capSyncType, Name: "c_late", Config: json.RawMessage(`{"fail":true}`)}
	}
	lateIn := "text"
	if lateFails {
		lateIn = "a"
	}
	return h.save(domain.Definition{
		WorkflowID: capWorkflowID("fanout"), Version: 1, Name: "Capacity fan-out", Description: "two governed branches",
		Nodes: []domain.Node{
			{ID: "a_input", Type: "text_input", Name: "Brief", Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			capTaskNode("b_wait", capFreeModelID),
			late,
			capTaskNode("z_task", capModelID),
			{ID: "join", Type: capSyncType, Name: "join", Config: json.RawMessage(`{}`)},
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "a_input", SourceHandle: "text", Target: "b_wait", TargetHandle: "text"},
			{ID: "e2", Source: "a_input", SourceHandle: "text", Target: "z_task", TargetHandle: "text"},
			{ID: "e3", Source: "b_wait", SourceHandle: "text", Target: "c_late", TargetHandle: lateIn},
			{ID: "e4", Source: "c_late", SourceHandle: "text", Target: "join", TargetHandle: "a"},
			{ID: "e5", Source: "z_task", SourceHandle: "text", Target: "join", TargetHandle: "b"},
			{ID: "e6", Source: "join", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: h.clock.Now(),
	})
}

func (h *capHarness) createRun(def domain.Definition) domain.Run {
	h.t.Helper()
	run, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: def.WorkflowID, DefinitionVersion: def.Version, Input: json.RawMessage(`{"brief":"ember"}`),
	})
	if err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	return run
}

// step is one Advance followed by Execute when something was claimed, the same pair the
// worker pool and the Reconciler perform. It reports whether a claim happened.
func (h *capHarness) step(runID string) (service.AdvanceOutcome, bool) {
	h.t.Helper()
	outcome, err := h.svc.Advance(h.ctx, runID)
	if err != nil {
		h.t.Fatalf("advance %s: %v", runID, err)
	}
	if !outcome.Claimed {
		return outcome, false
	}
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		h.t.Fatalf("execute %s: %v", outcome.NodeType, err)
	}
	return outcome, true
}

// drive steps a Run until Advance claims nothing, which is how the pool drains a Run.
func (h *capHarness) drive(runID string) {
	h.t.Helper()
	for i := 0; i < 20; i++ {
		if _, claimed := h.step(runID); !claimed {
			return
		}
	}
	h.t.Fatalf("run %s did not drain in 20 steps", runID)
}

func (h *capHarness) nodeRun(runID, nodeID string) domain.NodeRun {
	h.t.Helper()
	return execNodeRunByNodeID(h.t, listNodeRuns(h.ctx, h.t, h.uow, runID), nodeID)
}

func (h *capHarness) run(runID string) domain.Run {
	h.t.Helper()
	var run domain.Run
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		run, err = tx.Runs().Get(ctx, runID)
		return err
	}); err != nil {
		h.t.Fatalf("get run %s: %v", runID, err)
	}
	return run
}

func (h *capHarness) events(runID string) []domain.Event {
	h.t.Helper()
	return listEvents(h.ctx, h.t, h.uow, runID)
}

func (h *capHarness) waitingEvents(runID, nodeRunID string) []domain.Event {
	h.t.Helper()
	return asyncNodeEventsOfType(h.events(runID), nodeRunID, domain.EventNodeWaitingCapacity)
}

// heldNodeRuns lists the NodeRun ids holding a slot of the governed Provider.
func (h *capHarness) heldNodeRuns() []string {
	h.t.Helper()
	rows, err := h.pool.Query(h.ctx, `SELECT node_run_id FROM provider_slots WHERE provider_id = $1 ORDER BY node_run_id`, capProviderID)
	if err != nil {
		h.t.Fatalf("list provider slots: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			h.t.Fatalf("scan provider slot: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func (h *capHarness) heldCount() int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM provider_slots WHERE provider_id = $1`, capProviderID).Scan(&n); err != nil {
		h.t.Fatalf("count provider slots: %v", err)
	}
	return n
}

// callback delivers the Provider's answer for the NodeRun's latest dispatch.
func (h *capHarness) callback(nodeRunID, payload string) service.CallbackOutcome {
	h.t.Helper()
	d, ok := h.exec.dispatchOf(nodeRunID)
	if !ok {
		h.t.Fatalf("node run %s was never dispatched", nodeRunID)
	}
	outcome, err := h.svc.HandleCallback(h.ctx, service.HandleCallback{
		Token: d.token, ExternalTaskID: d.externalTaskID, Payload: json.RawMessage(payload),
	})
	if err != nil {
		h.t.Fatalf("callback for %s: %v", nodeRunID, err)
	}
	return outcome
}

// holdSlot creates a linear Run and drives it until its governed task is dispatched and
// waiting, holding one slot. It returns the Run and the waiting NodeRun.
func (h *capHarness) holdSlot(def domain.Definition) (domain.Run, domain.NodeRun) {
	h.t.Helper()
	run := h.createRun(def)
	h.drive(run.ID)
	nr := h.nodeRun(run.ID, "task")
	if nr.Status != domain.NodeRunWaitingCallback {
		h.t.Fatalf("task of %s = %s, want WAITING_CALLBACK holding a slot", run.ID, nr.Status)
	}
	return run, nr
}

// queueRun creates a linear Run and drives it until its governed task is refused and
// queued. It advances the clock first so every queued NodeRun has its own ready_at.
func (h *capHarness) queueRun(def domain.Definition) (domain.Run, domain.NodeRun) {
	h.t.Helper()
	h.clock.Advance(time.Second)
	run := h.createRun(def)
	h.drive(run.ID)
	nr := h.nodeRun(run.ID, "task")
	if nr.Status != domain.NodeRunReady || nr.CapacityDeferredAt == nil {
		h.t.Fatalf("task of %s = %s deferred=%v, want READY and queued", run.ID, nr.Status, nr.CapacityDeferredAt)
	}
	return run, nr
}

func (h *capHarness) assertQueued(nr domain.NodeRun, wantPosition int) {
	h.t.Helper()
	got := h.nodeRun(nr.RunID, nr.NodeID)
	if got.Status != domain.NodeRunReady || got.CapacityDeferredAt == nil || got.CapacityProviderID == nil || *got.CapacityProviderID != capProviderID {
		h.t.Fatalf("node run %s = %s deferred=%v provider=%v, want READY queued for %s", nr.ID, got.Status, got.CapacityDeferredAt, got.CapacityProviderID, capProviderID)
	}
	waiting := h.waitingEvents(nr.RunID, nr.ID)
	if len(waiting) != 1 {
		h.t.Fatalf("node run %s has %d NODE_WAITING_CAPACITY events, want exactly 1", nr.ID, len(waiting))
	}
	var payload struct {
		ProviderID string `json:"providerId"`
		Limit      int    `json:"limit"`
		Position   int    `json:"position"`
	}
	if err := json.Unmarshal(waiting[0].Payload, &payload); err != nil {
		h.t.Fatalf("decode NODE_WAITING_CAPACITY payload: %v", err)
	}
	if payload.ProviderID != capProviderID || payload.Limit != h.limit[capProviderID] || payload.Position != wantPosition {
		h.t.Fatalf("NODE_WAITING_CAPACITY payload = %+v, want provider %s limit %d position %d", payload, capProviderID, h.limit[capProviderID], wantPosition)
	}
}

func (h *capHarness) assertHeld(nr domain.NodeRun) {
	h.t.Helper()
	for _, id := range h.heldNodeRuns() {
		if id == nr.ID {
			return
		}
	}
	h.t.Fatalf("node run %s holds no slot; held: %v", nr.ID, h.heldNodeRuns())
}

func (h *capHarness) assertNotHeld(nr domain.NodeRun) {
	h.t.Helper()
	for _, id := range h.heldNodeRuns() {
		if id == nr.ID {
			h.t.Fatalf("node run %s still holds a slot", nr.ID)
		}
	}
}

func (h *capHarness) attempts(nodeRunID string) []domain.NodeAttempt {
	h.t.Helper()
	var attempts []domain.NodeAttempt
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempts, err = tx.NodeAttempts().ListByNodeRun(ctx, nodeRunID)
		return err
	}); err != nil {
		h.t.Fatalf("list attempts of %s: %v", nodeRunID, err)
	}
	return attempts
}

// lastHandoff is the Run the most recent post-commit enqueue offered to the queue.
func (h *capHarness) lastHandoff() string {
	h.t.Helper()
	advanced := h.queue.advanced()
	if len(advanced) == 0 {
		h.t.Fatal("nothing was enqueued")
	}
	return advanced[len(advanced)-1]
}

// ---------------------------------------------------------------------------
// Admission
// ---------------------------------------------------------------------------

func TestProviderCapacity_UngovernedProvider_SkipsAdmission(t *testing.T) {
	h := newCapHarness(t, 1)
	// The governed Provider is full, so any admission attempt would be refused.
	h.holdSlot(h.linearDefinition(capModelID, nil))

	free := h.createRun(h.linearDefinition(capFreeModelID, nil))
	h.drive(free.ID)
	task := h.nodeRun(free.ID, "task")
	if task.Status != domain.NodeRunWaitingCallback || task.CapacityDeferredAt != nil {
		t.Fatalf("unlimited provider task = %s deferred=%v, want dispatched and never queued", task.Status, task.CapacityDeferredAt)
	}
	if got := h.heldNodeRuns(); len(got) != 1 {
		t.Fatalf("held = %v, want only the governed task's row", got)
	}
	// Synchronous nodes of the same Runs were admitted without any capacity fact.
	for _, run := range []domain.Run{free} {
		for _, nr := range listNodeRuns(h.ctx, t, h.uow, run.ID) {
			if nr.NodeID != "task" && (nr.CapacityDeferredAt != nil || nr.CapacityProviderID != nil) {
				t.Fatalf("synchronous node %s carries capacity facts %v/%v", nr.NodeID, nr.CapacityDeferredAt, nr.CapacityProviderID)
			}
		}
	}
	if n := len(asyncEventsOfType(h.events(free.ID), domain.EventNodeWaitingCapacity)); n != 0 {
		t.Fatalf("unlimited Run has %d NODE_WAITING_CAPACITY events, want none", n)
	}
}

func TestProviderCapacity_DeferredCandidate_AdvanceReturnsUnclaimed(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, nil)
	h.holdSlot(def)

	run := h.createRun(def)
	// The input node is claimed and executed; the governed task is refused.
	if _, claimed := h.step(run.ID); !claimed {
		t.Fatal("first step claimed nothing, want the input node")
	}
	// CreateRun and the input node's completion enqueued this Run already; the refusal
	// must not enqueue it again, or the pool would spin on a Run that cannot progress.
	enqueuedBefore := len(h.queue.advanced())
	outcome, err := h.svc.Advance(h.ctx, run.ID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if outcome.Claimed {
		t.Fatalf("Advance claimed %s while the Provider is full, want Claimed=false", outcome.NodeType)
	}
	h.assertQueued(h.nodeRun(run.ID, "task"), 1)
	if got := len(h.queue.advanced()); got != enqueuedBefore {
		t.Fatalf("a refusal enqueued its own Run %s; the pool's drain loop must end instead", run.ID)
	}
}

func TestProviderCapacity_RefusedTwice_SingleWaitingCapacityEvent(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, nil)
	h.holdSlot(def)
	run, task := h.queueRun(def)
	seqAfterFirst := h.run(run.ID).LastSeq
	eventsAfterFirst := len(h.events(run.ID))

	h.clock.Advance(time.Second)
	for i := 0; i < 3; i++ {
		outcome, err := h.svc.Advance(h.ctx, run.ID)
		if err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
		if outcome.Claimed {
			t.Fatalf("advance %d claimed %s, want a repeated refusal", i, outcome.NodeType)
		}
	}
	h.assertQueued(task, 1)
	if got := h.run(run.ID).LastSeq; got != seqAfterFirst {
		t.Fatalf("last_seq = %d after repeated refusals, want unchanged %d", got, seqAfterFirst)
	}
	if got := len(h.events(run.ID)); got != eventsAfterFirst {
		t.Fatalf("events = %d after repeated refusals, want unchanged %d", got, eventsAfterFirst)
	}
	if got := h.nodeRun(run.ID, "task").CapacityDeferredAt; !got.Equal(*task.CapacityDeferredAt) {
		t.Fatalf("capacity_deferred_at moved to %v, want the first refusal time %v", got, task.CapacityDeferredAt)
	}
}

func TestProviderCapacity_WaitingEventAppendFails_DeferralRolledBack(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, nil)
	holder, _ := h.holdSlot(def)

	run := h.createRun(def)
	if _, claimed := h.step(run.ID); !claimed {
		t.Fatal("first step claimed nothing, want the input node")
	}
	// The next Event id collides with one that already exists, so the
	// NODE_WAITING_CAPACITY insert fails inside the refusal transaction.
	h.ids.ForceNextEventID(h.events(holder.ID)[0].ID)
	if _, err := h.svc.Advance(h.ctx, run.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("advance with a colliding event id: error = %v, want ErrConflict", err)
	}
	task := h.nodeRun(run.ID, "task")
	if task.CapacityDeferredAt != nil || task.CapacityProviderID != nil {
		t.Fatalf("deferral %v/%v survived the rolled-back Event, want both nil", task.CapacityDeferredAt, task.CapacityProviderID)
	}
	if n := len(h.waitingEvents(run.ID, task.ID)); n != 0 {
		t.Fatalf("%d NODE_WAITING_CAPACITY events after rollback, want none", n)
	}
	// With a fresh Event id the same refusal commits normally.
	if outcome, err := h.svc.Advance(h.ctx, run.ID); err != nil || outcome.Claimed {
		t.Fatalf("advance after rollback = (%v, %v), want an unclaimed refusal", outcome.Claimed, err)
	}
	h.assertQueued(task, 1)
}

func TestProviderCapacity_TwoRunsRaceForLastSlot_ExactlyOneAdmitted(t *testing.T) {
	h := newCapHarness(t, 1)
	entered := make(chan string, 1)
	release := make(chan struct{})
	var holds int32
	h.hooks = service.CapacityHooks{AfterProviderLock: func(nodeRunID string) {
		// Only the first admission is held; the second one reaches the hook only after
		// the first committed and released the Provider row lock.
		if atomic.AddInt32(&holds, 1) == 1 {
			entered <- nodeRunID
			<-release
		}
	}}
	h.svc = h.newService()
	def := h.linearDefinition(capModelID, nil)

	runs := []domain.Run{h.createRun(def), h.createRun(def)}
	for _, run := range runs {
		if _, claimed := h.step(run.ID); !claimed {
			t.Fatal("input node was not claimed")
		}
	}

	results := make(chan service.AdvanceOutcome, 2)
	errs := make(chan error, 2)
	advance := func(runID string) {
		outcome, err := h.svc.Advance(h.ctx, runID)
		errs <- err
		results <- outcome
	}
	go advance(runs[0].ID)
	first := <-entered
	go advance(runs[1].ID)
	if err := waitUntilBlockedOnLock(h.ctx, h.pool); err != nil {
		t.Fatalf("second admission never waited on the Provider lock: %v", err)
	}
	close(release)

	var admitted int
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("advance: %v", err)
		}
		if (<-results).Claimed {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("%d of 2 racing Runs were admitted, want exactly 1", admitted)
	}
	held := h.heldNodeRuns()
	if len(held) != 1 || held[0] != first {
		t.Fatalf("held = %v, want only the first lock holder %s", held, first)
	}
	loser := h.nodeRun(runs[1].ID, "task")
	h.assertQueued(loser, 1)
}

func TestProviderCapacity_ConcurrentRunsBeyondLimit_HeldNeverExceedsLimit(t *testing.T) {
	const limit, runs = 2, 5
	h := newCapHarness(t, limit)
	def := h.linearDefinition(capModelID, nil)
	ids := make([]string, runs)
	for i := range ids {
		ids[i] = h.createRun(def).ID
	}

	var maxHeld int32
	var wg sync.WaitGroup
	errs := make(chan error, runs*20)
	for _, runID := range ids {
		wg.Add(1)
		go func(runID string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				outcome, err := h.svc.Advance(h.ctx, runID)
				if err != nil {
					errs <- err
					return
				}
				// Every commit point of admission is observed from outside the
				// transaction: the count must never exceed the limit.
				var held int32
				if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM provider_slots WHERE provider_id = $1`, capProviderID).Scan(&held); err != nil {
					errs <- err
					return
				}
				for {
					seen := atomic.LoadInt32(&maxHeld)
					if held <= seen || atomic.CompareAndSwapInt32(&maxHeld, seen, held) {
						break
					}
				}
				if !outcome.Claimed {
					return
				}
				if err := h.svc.Execute(h.ctx, outcome); err != nil {
					errs <- err
					return
				}
			}
			errs <- fmt.Errorf("run %s did not drain", runID)
		}(runID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("driving runs: %v", err)
	}
	if got := atomic.LoadInt32(&maxHeld); got > limit {
		t.Fatalf("held slots reached %d, want at most the limit %d", got, limit)
	}
	var waiting, queuedEvents int
	for _, runID := range ids {
		task := h.nodeRun(runID, "task")
		switch task.Status {
		case domain.NodeRunWaitingCallback:
			waiting++
			if task.CapacityDeferredAt != nil {
				t.Fatalf("task of %s was admitted yet marked queued", runID)
			}
		case domain.NodeRunReady:
			if task.CapacityDeferredAt == nil {
				t.Fatalf("task of %s is READY but was never queued", runID)
			}
			queuedEvents += len(h.waitingEvents(runID, task.ID))
		default:
			t.Fatalf("task of %s = %s, want WAITING_CALLBACK or READY", runID, task.Status)
		}
	}
	if waiting != limit || h.heldCount() != limit {
		t.Fatalf("waiting = %d held = %d, want %d of each", waiting, h.heldCount(), limit)
	}
	if queuedEvents != runs-limit {
		t.Fatalf("NODE_WAITING_CAPACITY events = %d, want one per queued Run (%d)", queuedEvents, runs-limit)
	}
}

// TestProviderCapacity_FanOutLaterPickInSameRun_UsesRunQueueKey: a Run that already has
// one NodeRun queued competes with the key of that earlier arrival. Run F queues z_task
// first, Run G queues after it, then F's c_late becomes READY and is preferred by the
// scheduler. Had c_late competed with its own key it would sit behind G (position 2);
// with the Run's key it sits at the head (position 1).
func TestProviderCapacity_FanOutLaterPickInSameRun_UsesRunQueueKey(t *testing.T) {
	h := newCapHarness(t, 1)
	h.holdSlot(h.linearDefinition(capModelID, nil))

	fanOut := h.createRun(h.fanOutDefinition(false))
	h.drive(fanOut.ID)
	if nr := h.nodeRun(fanOut.ID, "b_wait"); nr.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("b_wait = %s, want WAITING_CALLBACK on the unlimited provider", nr.Status)
	}
	h.assertQueued(h.nodeRun(fanOut.ID, "z_task"), 1)
	_, later := h.queueRun(h.linearDefinition(capModelID, nil))
	h.assertQueued(later, 2)

	h.clock.Advance(time.Second)
	bWait := h.nodeRun(fanOut.ID, "b_wait")
	if outcome := h.callback(bWait.ID, `{"status":"SUCCEEDED","text":"b"}`); !outcome.Accepted {
		t.Fatalf("callback for b_wait = %+v, want accepted", outcome)
	}
	h.drive(fanOut.ID)
	cLate := h.nodeRun(fanOut.ID, "c_late")
	h.assertQueued(cLate, 1)
	if zTask := h.nodeRun(fanOut.ID, "z_task"); zTask.Status != domain.NodeRunReady || zTask.CapacityDeferredAt == nil {
		t.Fatalf("z_task = %s deferred=%v, want still queued", zTask.Status, zTask.CapacityDeferredAt)
	}
	if h.heldCount() != 1 {
		t.Fatalf("held = %d, want the single slot of the holder", h.heldCount())
	}
}

// TestProviderCapacity_DeferredInFailedRun_DoesNotBlockQueue: a queued NodeRun whose Run
// already failed stays READY in the database yet counts for nobody.
func TestProviderCapacity_DeferredInFailedRun_DoesNotBlockQueue(t *testing.T) {
	h := newCapHarness(t, 1)
	h.holdSlot(h.linearDefinition(capModelID, nil))

	failing := h.createRun(h.fanOutDefinition(true))
	h.drive(failing.ID)
	zTask := h.nodeRun(failing.ID, "z_task")
	h.assertQueued(zTask, 1)
	bWait := h.nodeRun(failing.ID, "b_wait")
	if outcome := h.callback(bWait.ID, `{"status":"SUCCEEDED","text":"b"}`); !outcome.Accepted {
		t.Fatalf("callback for b_wait = %+v, want accepted", outcome)
	}
	h.drive(failing.ID)
	if got := h.run(failing.ID).Status; got != domain.RunFailed {
		t.Fatalf("run with the failing branch = %s, want FAILED", got)
	}
	if got := h.nodeRun(failing.ID, "z_task"); got.Status != domain.NodeRunReady || got.CapacityDeferredAt == nil {
		t.Fatalf("z_task of the failed Run = %s deferred=%v, want left READY and queued", got.Status, got.CapacityDeferredAt)
	}

	_, task := h.queueRun(h.linearDefinition(capModelID, nil))
	h.assertQueued(task, 1)
}

// ---------------------------------------------------------------------------
// Release
// ---------------------------------------------------------------------------

func TestProviderCapacity_ReleaseOnSuccess_EarliestDeferredRunAdmitted(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, nil)
	holder, held := h.holdSlot(def)
	first, firstTask := h.queueRun(def)
	second, secondTask := h.queueRun(def)
	h.assertQueued(secondTask, 2)

	if outcome := h.callback(held.ID, `{"status":"SUCCEEDED","text":"done"}`); !outcome.Accepted {
		t.Fatalf("callback = %+v, want accepted", outcome)
	}
	h.assertNotHeld(held)
	if h.heldCount() != 0 {
		t.Fatalf("held = %d right after the release, want 0", h.heldCount())
	}
	if got := h.lastHandoff(); got != first.ID {
		t.Fatalf("release handed the slot to %s, want the earliest queued Run %s", got, first.ID)
	}
	h.drive(holder.ID)
	if got := h.run(holder.ID).Status; got != domain.RunCompleted {
		t.Fatalf("holder run = %s, want COMPLETED", got)
	}

	// The later Run reaches the free slot first yet is refused: an earlier queued Run
	// of another live Run is still waiting.
	if outcome, err := h.svc.Advance(h.ctx, second.ID); err != nil || outcome.Claimed {
		t.Fatalf("advance of the later Run = (%v, %v), want refused while an earlier Run waits", outcome.Claimed, err)
	}
	h.assertQueued(secondTask, 2)
	if h.heldCount() != 0 {
		t.Fatalf("held = %d after the refused overtake, want 0", h.heldCount())
	}

	h.drive(first.ID)
	admitted := h.nodeRun(first.ID, "task")
	if admitted.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("earliest queued task = %s, want dispatched", admitted.Status)
	}
	h.assertHeld(admitted)
	if admitted.CapacityDeferredAt == nil || !admitted.CapacityDeferredAt.Equal(*firstTask.CapacityDeferredAt) {
		t.Fatalf("admission erased the queued-since fact %v, want it kept as %v", admitted.CapacityDeferredAt, firstTask.CapacityDeferredAt)
	}
	if n := len(h.waitingEvents(first.ID, admitted.ID)); n != 1 {
		t.Fatalf("admitted task has %d NODE_WAITING_CAPACITY events, want the single first refusal", n)
	}
	h.drive(second.ID)
	h.assertQueued(secondTask, 2)
}

func TestProviderCapacity_NewlyReadyNodeRun_CannotOvertakeEarlierDeferred(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, nil)
	_, held := h.holdSlot(def)
	queued, queuedTask := h.queueRun(def)
	h.callback(held.ID, `{"status":"SUCCEEDED","text":"done"}`)
	if h.heldCount() != 0 {
		t.Fatalf("held = %d after the release, want 0", h.heldCount())
	}

	// A Run created after the release finds a free slot and is still refused, behind
	// the Run that queued while the slot was taken.
	h.clock.Advance(time.Second)
	late := h.createRun(def)
	h.drive(late.ID)
	h.assertQueued(h.nodeRun(late.ID, "task"), 2)
	if h.heldCount() != 0 {
		t.Fatalf("held = %d after the newcomer was refused, want 0", h.heldCount())
	}

	h.drive(queued.ID)
	h.assertHeld(h.nodeRun(queued.ID, "task"))
	h.drive(late.ID)
	h.assertQueued(h.nodeRun(late.ID, "task"), 2)
	_ = queuedTask
}

func TestProviderCapacity_RetryOfSameNodeRun_KeepsSingleSlotRow(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, &domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed, TimeoutMs: 60_000})
	h.exec.setDispatchErr(errors.New("provider unreachable"))

	run := h.createRun(def)
	h.drive(run.ID)
	task := h.nodeRun(run.ID, "task")
	if task.Status != domain.NodeRunRunning || task.NextAttemptAt == nil {
		t.Fatalf("task after a failed dispatch = %s next=%v, want RUNNING with a scheduled retry", task.Status, task.NextAttemptAt)
	}
	// The slot stays with the NodeRun across the retry wait: a second Run is refused.
	h.assertHeld(task)
	_, other := h.queueRun(def)
	h.assertQueued(other, 1)

	h.exec.setDispatchErr(nil)
	h.clock.Advance(time.Minute)
	h.drive(run.ID)
	task = h.nodeRun(run.ID, "task")
	if task.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("task after the retry = %s, want WAITING_CALLBACK", task.Status)
	}
	if n := len(h.attempts(task.ID)); n != 2 {
		t.Fatalf("attempts = %d, want the failed first and the dispatched second", n)
	}
	if got := h.heldNodeRuns(); len(got) != 1 || got[0] != task.ID {
		t.Fatalf("held = %v, want exactly one row for the retried NodeRun", got)
	}
	if task.CapacityDeferredAt != nil {
		t.Fatalf("retried task was marked queued at %v, want never queued: a retry bypasses admission", task.CapacityDeferredAt)
	}
}

func TestProviderCapacity_TerminalFailure_ReleasesSlot(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, &domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed, TimeoutMs: 60_000})
	holder, held := h.holdSlot(def)
	queued, _ := h.queueRun(def)

	// A Provider-reported failure of a waiting task is terminal even though attempts
	// remain: the slot is freed in that same transaction.
	if outcome := h.callback(held.ID, `{"status":"FAILED"}`); !outcome.Accepted {
		t.Fatalf("failure callback = %+v, want accepted", outcome)
	}
	failed := h.nodeRun(holder.ID, "task")
	if failed.Status != domain.NodeRunFailed {
		t.Fatalf("task after the provider failure = %s, want FAILED", failed.Status)
	}
	h.assertNotHeld(failed)
	if got := h.run(holder.ID).Status; got != domain.RunFailed {
		t.Fatalf("holder run = %s, want FAILED", got)
	}
	if got := h.lastHandoff(); got != queued.ID {
		t.Fatalf("release handed the slot to %s, want %s", got, queued.ID)
	}
	h.drive(queued.ID)
	h.assertHeld(h.nodeRun(queued.ID, "task"))
}

func TestProviderCapacity_UncertainTimeoutNotRetryable_ReleasesSlotAndAttemptUncertain(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, &domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed, TimeoutMs: 1_000})
	holder, held := h.holdSlot(def)
	queued, _ := h.queueRun(def)
	attempts := h.attempts(held.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.NodeAttemptDispatched {
		t.Fatalf("attempts = %+v, want one DISPATCHED attempt", attempts)
	}

	h.clock.Advance(2 * time.Second)
	if err := h.svc.TimeoutAttempt(h.ctx, attempts[0].ID); err != nil {
		t.Fatalf("timeout: %v", err)
	}
	attempt := h.attempts(held.ID)[0]
	if attempt.Status != domain.NodeAttemptFailed || attempt.Uncertain == nil || !*attempt.Uncertain {
		t.Fatalf("timed-out attempt = %s uncertain=%v, want FAILED and uncertain", attempt.Status, attempt.Uncertain)
	}
	task := h.nodeRun(holder.ID, "task")
	if task.Status != domain.NodeRunFailed {
		t.Fatalf("task after the wait timeout = %s, want FAILED: a dispatched task is never re-dispatched", task.Status)
	}
	h.assertNotHeld(task)
	if n := len(h.attempts(held.ID)); n != 1 {
		t.Fatalf("attempts = %d after an uncertain timeout, want no retry", n)
	}
	if got := h.lastHandoff(); got != queued.ID {
		t.Fatalf("release handed the slot to %s, want %s", got, queued.ID)
	}
	h.drive(queued.ID)
	h.assertHeld(h.nodeRun(queued.ID, "task"))
}

// ---------------------------------------------------------------------------
// Recovery
// ---------------------------------------------------------------------------

func TestProviderCapacity_CrashWithAllSlotsHeld_RowsUnchangedAfterRestart(t *testing.T) {
	h := newCapHarness(t, 1)
	def := h.linearDefinition(capModelID, nil)
	_, held := h.holdSlot(def)
	queued, queuedTask := h.queueRun(def)
	heldBefore := h.heldNodeRuns()

	// The process dies with the slot held and one Run queued. The next process has no
	// queue items and no memory of either; the Reconciler scans persisted READY work.
	h.svc = h.newService()
	h.queue = &capQueue{}
	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100})
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}
	if report.ReadyOrRetryableFound == 0 {
		t.Fatal("reconciler rediscovered no READY work, want the queued NodeRun")
	}
	if got := h.heldNodeRuns(); len(got) != len(heldBefore) || got[0] != heldBefore[0] {
		t.Fatalf("held after restart = %v, want unchanged %v", got, heldBefore)
	}
	h.assertQueued(queuedTask, 1)

	// Only the slot's release lets the queued Run in, and the Reconciler alone (no
	// in-process handoff survived the restart) rediscovers it.
	h.callback(held.ID, `{"status":"SUCCEEDED","text":"done"}`)
	if _, err := rec.RunOnce(h.ctx); err != nil {
		t.Fatalf("reconcile after the release: %v", err)
	}
	admitted := h.nodeRun(queued.ID, "task")
	if admitted.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("queued task after the release and a reconcile pass = %s, want dispatched", admitted.Status)
	}
	h.assertHeld(admitted)
}
