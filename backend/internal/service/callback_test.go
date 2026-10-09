package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ---------------------------------------------------------------------------
// In-memory store fake
//
// Unlike execFakeTx, this fake implements the whole execution surface the async
// dispatch/resume use cases touch, including CallbackBindings and PendingCallbacks, and
// it models transaction rollback: WithinTx mutates a private copy of the state and
// publishes it only when the closure returns nil. That is what makes "a duplicate
// callback appends no Event and consumes no seq" assertable here at all. It is never a
// substitute for the PostgreSQL proofs in test/integration: conditional-claim races,
// lock ordering and constraint behaviour are proven there.
// ---------------------------------------------------------------------------

var errCbNotImplemented = errors.New("callback_test: fake store method not implemented")

type cbState struct {
	runs     map[string]domain.Run
	nodeRuns map[string]domain.NodeRun
	attempts map[string]domain.NodeAttempt
	bindings map[string]domain.CallbackBinding
	pending  map[string]domain.PendingCallback
	events   []domain.Event
	lastSeq  int64
}

func newCbState() *cbState {
	return &cbState{
		runs:     map[string]domain.Run{},
		nodeRuns: map[string]domain.NodeRun{},
		attempts: map[string]domain.NodeAttempt{},
		bindings: map[string]domain.CallbackBinding{},
		pending:  map[string]domain.PendingCallback{},
	}
}

func (s *cbState) clone() *cbState {
	out := newCbState()
	for k, v := range s.runs {
		out.runs[k] = v
	}
	for k, v := range s.nodeRuns {
		out.nodeRuns[k] = v
	}
	for k, v := range s.attempts {
		out.attempts[k] = v
	}
	for k, v := range s.bindings {
		out.bindings[k] = v
	}
	for k, v := range s.pending {
		out.pending[k] = v
	}
	out.events = append(out.events, s.events...)
	out.lastSeq = s.lastSeq
	return out
}

type cbStore struct {
	state *cbState
	def   domain.Definition

	// failAppend, when set, makes the next matching Event append fail, so a test can
	// prove the whole resume transaction rolls back.
	failAppend func(domain.Event) error
}

func (s *cbStore) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	working := s.state.clone()
	tx := &cbTx{store: s, state: working}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	s.state = working
	return nil
}

func (s *cbStore) WithinReadTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return fn(ctx, &cbTx{store: s, state: s.state.clone()})
}

type cbTx struct {
	store *cbStore
	state *cbState
}

func (t *cbTx) Definitions() store.DefinitionRepository           { return &cbDefinitionRepo{tx: t} }
func (t *cbTx) Runs() store.RunRepository                         { return &cbRunRepo{tx: t} }
func (t *cbTx) NodeRuns() store.NodeRunRepository                 { return &cbNodeRunRepo{tx: t} }
func (t *cbTx) NodeAttempts() store.NodeAttemptRepository         { return &cbNodeAttemptRepo{tx: t} }
func (t *cbTx) CallbackBindings() store.CallbackBindingRepository { return &cbBindingRepo{tx: t} }
func (t *cbTx) PendingCallbacks() store.PendingCallbackRepository { return &cbPendingRepo{tx: t} }

// Assets is unused here: callback handling never reads or writes Asset Metadata.
func (t *cbTx) Assets() store.AssetRepository { return nil }
func (t *cbTx) Events() store.EventRepository { return &cbEventRepo{tx: t} }

// The callback use case never reads or writes Agent facts, so these accessors stay nil;
// the Agent repositories are proved against a real database in test/integration.
func (t *cbTx) AgentRuns() store.AgentRunRepository           { return nil }
func (t *cbTx) AgentTurns() store.AgentTurnRepository         { return nil }
func (t *cbTx) AgentDecisions() store.AgentDecisionRepository { return nil }
func (t *cbTx) AgentActions() store.AgentActionRepository     { return nil }
func (t *cbTx) ToolAttempts() store.ToolAttemptRepository     { return nil }

func (t *cbTx) AgentContextVersions() store.AgentContextVersionRepository { return nil }
func (t *cbTx) AgentStateVersions() store.AgentStateVersionRepository     { return nil }
func (t *cbTx) ExecutionFacts() store.ExecutionFactRepository             { return nil }

type cbDefinitionRepo struct{ tx *cbTx }

func (r *cbDefinitionRepo) Save(context.Context, domain.Definition) error { return errCbNotImplemented }
func (r *cbDefinitionRepo) GetVersion(_ context.Context, workflowID string, version int) (domain.Definition, error) {
	def := r.tx.store.def
	if def.WorkflowID != workflowID || def.Version != version {
		return domain.Definition{}, domain.ErrNotFound
	}
	return def, nil
}
func (r *cbDefinitionRepo) ListVersions(context.Context, string) ([]domain.Definition, error) {
	return nil, errCbNotImplemented
}
func (r *cbDefinitionRepo) GetWorkflow(context.Context, string) (domain.Workflow, error) {
	return domain.Workflow{}, errCbNotImplemented
}
func (r *cbDefinitionRepo) ListWorkflows(context.Context) ([]store.WorkflowSummary, error) {
	return nil, errCbNotImplemented
}

type cbRunRepo struct{ tx *cbTx }

func (r *cbRunRepo) Create(_ context.Context, run domain.Run) error {
	r.tx.state.runs[run.ID] = run
	return nil
}
func (r *cbRunRepo) Get(_ context.Context, runID string) (domain.Run, error) {
	run, ok := r.tx.state.runs[runID]
	if !ok {
		return domain.Run{}, domain.ErrNotFound
	}
	return run, nil
}
func (r *cbRunRepo) LockForUpdate(_ context.Context, runID string) (*store.RunLock, error) {
	run, ok := r.tx.state.runs[runID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return store.NewRunLock(runID, run.Status, r.tx.state.lastSeq), nil
}
func (r *cbRunRepo) UpdateAggregate(_ context.Context, lock *store.RunLock, status domain.RunStatus, now time.Time) error {
	run := r.tx.state.runs[lock.RunID()]
	run.Status = status
	run.LastSeq = lock.LastSeq()
	run.UpdatedAt = now
	r.tx.state.runs[lock.RunID()] = run
	r.tx.state.lastSeq = lock.LastSeq()
	return nil
}
func (r *cbRunRepo) SetOutput(_ context.Context, lock *store.RunLock, output json.RawMessage) error {
	run := r.tx.state.runs[lock.RunID()]
	if len(run.Output) > 0 {
		return domain.ErrConflict
	}
	run.Output = output
	r.tx.state.runs[lock.RunID()] = run
	return nil
}
func (r *cbRunRepo) SetError(_ context.Context, lock *store.RunLock, execErr domain.ExecutionError) error {
	run := r.tx.state.runs[lock.RunID()]
	e := execErr
	run.Error = &e
	r.tx.state.runs[lock.RunID()] = run
	return nil
}
func (r *cbRunRepo) LatestByWorkflow(context.Context, string) (*domain.Run, error) {
	return nil, errCbNotImplemented
}

type cbNodeRunRepo struct{ tx *cbTx }

func (r *cbNodeRunRepo) Create(_ context.Context, nr domain.NodeRun) error {
	r.tx.state.nodeRuns[nr.ID] = nr
	return nil
}
func (r *cbNodeRunRepo) Get(_ context.Context, nodeRunID string) (domain.NodeRun, error) {
	nr, ok := r.tx.state.nodeRuns[nodeRunID]
	if !ok {
		return domain.NodeRun{}, domain.ErrNotFound
	}
	return nr, nil
}
func (r *cbNodeRunRepo) ListByRun(_ context.Context, runID string) ([]domain.NodeRun, error) {
	var out []domain.NodeRun
	for _, nr := range r.tx.state.nodeRuns {
		if nr.RunID == runID {
			out = append(out, nr)
		}
	}
	return out, nil
}
func (r *cbNodeRunRepo) ClaimReady(_ context.Context, nodeRunID string, now time.Time) (bool, error) {
	nr := r.tx.state.nodeRuns[nodeRunID]
	if nr.Status != domain.NodeRunReady {
		return false, nil
	}
	nr.Status = domain.NodeRunRunning
	nr.StartedAt = &now
	r.tx.state.nodeRuns[nodeRunID] = nr
	return true, nil
}
func (r *cbNodeRunRepo) Transition(_ context.Context, nodeRunID string, from, to domain.NodeRunStatus, now time.Time) error {
	if !from.CanTransitionTo(to) {
		return &domain.InvalidStateTransitionError{Entity: "NodeRun", ID: nodeRunID, From: string(from), To: string(to)}
	}
	nr := r.tx.state.nodeRuns[nodeRunID]
	if nr.Status != from {
		return domain.ErrStaleClaim
	}
	nr.Status = to
	nr.UpdatedAt = now
	r.tx.state.nodeRuns[nodeRunID] = nr
	return nil
}
func (r *cbNodeRunRepo) ListReadyOrRetryable(context.Context, time.Time, int) ([]domain.NodeRun, error) {
	return nil, errCbNotImplemented
}
func (r *cbNodeRunRepo) ClaimRetry(context.Context, string, time.Time) (bool, error) {
	return false, errCbNotImplemented
}
func (r *cbNodeRunRepo) ScheduleRetry(_ context.Context, nodeRunID string, nextAttemptAt, now time.Time) error {
	nr := r.tx.state.nodeRuns[nodeRunID]
	if nr.Status != domain.NodeRunRunning {
		return domain.ErrStaleClaim
	}
	at := nextAttemptAt
	nr.NextAttemptAt = &at
	nr.UpdatedAt = now
	r.tx.state.nodeRuns[nodeRunID] = nr
	return nil
}
func (r *cbNodeRunRepo) IncrementAttemptCount(_ context.Context, nodeRunID string) (int, error) {
	nr := r.tx.state.nodeRuns[nodeRunID]
	nr.AttemptCount++
	r.tx.state.nodeRuns[nodeRunID] = nr
	return nr.AttemptCount, nil
}
func (r *cbNodeRunRepo) SetInput(_ context.Context, nodeRunID string, input json.RawMessage) error {
	nr := r.tx.state.nodeRuns[nodeRunID]
	nr.Input = input
	r.tx.state.nodeRuns[nodeRunID] = nr
	return nil
}
func (r *cbNodeRunRepo) MarkSucceeded(_ context.Context, nodeRunID string, from domain.NodeRunStatus, now time.Time, outcome store.NodeRunOutcome) error {
	if !from.CanTransitionTo(domain.NodeRunSucceeded) {
		return &domain.InvalidStateTransitionError{Entity: "NodeRun", ID: nodeRunID, From: string(from), To: string(domain.NodeRunSucceeded)}
	}
	nr := r.tx.state.nodeRuns[nodeRunID]
	if nr.Status != from {
		return domain.ErrStaleClaim
	}
	nr.Status = domain.NodeRunSucceeded
	nr.Output = outcome.Output
	nr.TokenUsage = outcome.TokenUsage
	nr.LatencyMs = outcome.LatencyMs
	nr.CompletedAt = &now
	nr.UpdatedAt = now
	r.tx.state.nodeRuns[nodeRunID] = nr
	return nil
}
func (r *cbNodeRunRepo) MarkFailed(_ context.Context, nodeRunID string, from domain.NodeRunStatus, now time.Time, execErr domain.ExecutionError, usage *domain.TokenUsage) error {
	if !from.CanTransitionTo(domain.NodeRunFailed) {
		return &domain.InvalidStateTransitionError{Entity: "NodeRun", ID: nodeRunID, From: string(from), To: string(domain.NodeRunFailed)}
	}
	nr := r.tx.state.nodeRuns[nodeRunID]
	if nr.Status != from {
		return domain.ErrStaleClaim
	}
	e := execErr
	nr.Status = domain.NodeRunFailed
	nr.Error = &e
	nr.TokenUsage = usage
	nr.CompletedAt = &now
	nr.UpdatedAt = now
	r.tx.state.nodeRuns[nodeRunID] = nr
	return nil
}
func (r *cbNodeRunRepo) MarkWaiting(_ context.Context, nodeRunID string, now time.Time) error {
	nr := r.tx.state.nodeRuns[nodeRunID]
	if nr.Status != domain.NodeRunRunning {
		return domain.ErrStaleClaim
	}
	nr.Status = domain.NodeRunWaitingCallback
	nr.WaitingAt = &now
	nr.UpdatedAt = now
	r.tx.state.nodeRuns[nodeRunID] = nr
	return nil
}

type cbNodeAttemptRepo struct{ tx *cbTx }

func (r *cbNodeAttemptRepo) Create(_ context.Context, attempt domain.NodeAttempt) error {
	r.tx.state.attempts[attempt.ID] = attempt
	return nil
}
func (r *cbNodeAttemptRepo) Get(_ context.Context, attemptID string) (domain.NodeAttempt, error) {
	attempt, ok := r.tx.state.attempts[attemptID]
	if !ok {
		return domain.NodeAttempt{}, domain.ErrNotFound
	}
	return attempt, nil
}
func (r *cbNodeAttemptRepo) ListByNodeRun(context.Context, string) ([]domain.NodeAttempt, error) {
	return nil, errCbNotImplemented
}
func (r *cbNodeAttemptRepo) Transition(context.Context, string, domain.NodeAttemptStatus, domain.NodeAttemptStatus, time.Time) error {
	return errCbNotImplemented
}
func (r *cbNodeAttemptRepo) MarkDispatched(_ context.Context, attemptID string, now time.Time, firstPollAt *time.Time) error {
	attempt := r.tx.state.attempts[attemptID]
	if attempt.Status != domain.NodeAttemptStarted {
		return domain.ErrStaleClaim
	}
	attempt.Status = domain.NodeAttemptDispatched
	attempt.DispatchedAt = &now
	attempt.NextPollAt = firstPollAt
	r.tx.state.attempts[attemptID] = attempt
	return nil
}
func (r *cbNodeAttemptRepo) MarkSucceeded(_ context.Context, attemptID string, from domain.NodeAttemptStatus, now time.Time, result json.RawMessage) error {
	if !from.CanTransitionTo(domain.NodeAttemptSucceeded) {
		return &domain.InvalidStateTransitionError{Entity: "NodeAttempt", ID: attemptID, From: string(from), To: string(domain.NodeAttemptSucceeded)}
	}
	attempt := r.tx.state.attempts[attemptID]
	if attempt.Status != from {
		return domain.ErrStaleClaim
	}
	attempt.Status = domain.NodeAttemptSucceeded
	attempt.Result = result
	attempt.CompletedAt = &now
	r.tx.state.attempts[attemptID] = attempt
	return nil
}
func (r *cbNodeAttemptRepo) MarkFailed(_ context.Context, attemptID string, from domain.NodeAttemptStatus, now time.Time, execErr domain.ExecutionError) error {
	if !from.CanTransitionTo(domain.NodeAttemptFailed) {
		return &domain.InvalidStateTransitionError{Entity: "NodeAttempt", ID: attemptID, From: string(from), To: string(domain.NodeAttemptFailed)}
	}
	attempt := r.tx.state.attempts[attemptID]
	if attempt.Status != from {
		return domain.ErrStaleClaim
	}
	e := execErr
	attempt.Status = domain.NodeAttemptFailed
	attempt.Error = &e
	attempt.CompletedAt = &now
	r.tx.state.attempts[attemptID] = attempt
	return nil
}
func (r *cbNodeAttemptRepo) ListExpired(context.Context, time.Time, int) ([]domain.NodeAttempt, error) {
	return nil, errCbNotImplemented
}
func (r *cbNodeAttemptRepo) Latest(context.Context, string) (*domain.NodeAttempt, error) {
	return nil, errCbNotImplemented
}
func (r *cbNodeAttemptRepo) ClaimPoll(context.Context, string, time.Time, time.Duration, int) (bool, error) {
	return false, errCbNotImplemented
}
func (r *cbNodeAttemptRepo) ClearPoll(context.Context, string) (bool, error) {
	return false, errCbNotImplemented
}
func (r *cbNodeAttemptRepo) ListDuePolls(context.Context, time.Time, int) ([]store.DuePoll, error) {
	return nil, errCbNotImplemented
}

type cbBindingRepo struct{ tx *cbTx }

func (r *cbBindingRepo) Create(_ context.Context, binding domain.CallbackBinding) error {
	if _, exists := r.tx.state.bindings[binding.ExternalTaskID]; exists {
		return domain.ErrConflict
	}
	r.tx.state.bindings[binding.ExternalTaskID] = binding
	return nil
}
func (r *cbBindingRepo) GetByExternalTaskID(_ context.Context, externalTaskID string) (domain.CallbackBinding, error) {
	binding, ok := r.tx.state.bindings[externalTaskID]
	if !ok {
		return domain.CallbackBinding{}, domain.ErrNotFound
	}
	return binding, nil
}
func (r *cbBindingRepo) ListByTargets(context.Context, domain.CallbackTargetType, []string) ([]domain.CallbackBinding, error) {
	return nil, errCbNotImplemented
}

type cbPendingRepo struct{ tx *cbTx }

func (r *cbPendingRepo) Record(_ context.Context, pending domain.PendingCallback) (bool, error) {
	existing, ok := r.tx.state.pending[pending.ExternalTaskID]
	if ok {
		existing.DuplicateCount++
		existing.ReceivedAt = pending.ReceivedAt
		r.tx.state.pending[pending.ExternalTaskID] = existing
		return true, nil
	}
	r.tx.state.pending[pending.ExternalTaskID] = pending
	return false, nil
}
func (r *cbPendingRepo) GetByExternalTaskID(_ context.Context, externalTaskID string) (domain.PendingCallback, error) {
	pending, ok := r.tx.state.pending[externalTaskID]
	if !ok {
		return domain.PendingCallback{}, domain.ErrNotFound
	}
	return pending, nil
}
func (r *cbPendingRepo) ConsumeOnce(_ context.Context, externalTaskID string, now time.Time) (domain.PendingCallback, bool, error) {
	pending, ok := r.tx.state.pending[externalTaskID]
	if !ok || pending.ConsumedAt != nil || !now.Before(pending.ExpiresAt) {
		return domain.PendingCallback{}, false, nil
	}
	at := now
	pending.ConsumedAt = &at
	r.tx.state.pending[externalTaskID] = pending
	return pending, true, nil
}
func (r *cbPendingRepo) ListConsumableForWaiting(context.Context, time.Time, int) ([]store.PendingForWaiting, error) {
	return nil, errCbNotImplemented
}
func (r *cbPendingRepo) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, errCbNotImplemented
}

type cbEventRepo struct{ tx *cbTx }

func (r *cbEventRepo) Append(_ context.Context, lock *store.RunLock, ev domain.Event) (domain.Event, error) {
	if fail := r.tx.store.failAppend; fail != nil {
		if err := fail(ev); err != nil {
			return domain.Event{}, err
		}
	}
	ev.Seq = lock.NextSeq()
	r.tx.state.events = append(r.tx.state.events, ev)
	return ev, nil
}
func (r *cbEventRepo) ListAfter(context.Context, string, int64, int) ([]domain.Event, error) {
	return nil, errCbNotImplemented
}

// ---------------------------------------------------------------------------
// Fakes: clock, ids, async executor
// ---------------------------------------------------------------------------

type cbClock struct{ now time.Time }

func (c *cbClock) Now() time.Time { return c.now }

type cbIDs struct{ n int }

func (g *cbIDs) NewID(prefix string) string {
	g.n++
	return prefix + "_" + strconv.Itoa(g.n)
}

// cbAsyncExecutor is a deterministic AsyncNodeExecutor: Execute dispatches (or reports a
// caller-chosen error/result) and OnCallback interprets the payload with the injected
// function. cmd/mockprovider is an HTTP fixture for the end-to-end track and is
// deliberately not used here.
type cbAsyncExecutor struct {
	externalTaskID string
	providerID     string
	execErr        error
	onCallback     func(state registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error)

	lastInput registry.NodeInput
	lastState registry.NodeAsyncState
}

func (e *cbAsyncExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (e *cbAsyncExecutor) Execute(_ context.Context, in registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	e.lastInput = in
	if e.execErr != nil {
		return registry.NodeResult{}, e.execErr
	}
	return registry.NodeResult{
		Kind:         registry.NodeResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: e.providerID, ExternalTaskID: e.externalTaskID},
	}, nil
}

func (e *cbAsyncExecutor) OnCallback(_ context.Context, state registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error) {
	e.lastState = state
	if e.onCallback != nil {
		return e.onCallback(state, payload)
	}
	return registry.NodeOutput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`"done"`)}}, nil
}

const cbAsyncNodeType = "async_dispatch"

func cbAsyncRegistration(executor registry.NodeExecutor, side domain.SideEffectPolicy) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          cbAsyncNodeType,
			DisplayName:   "Async Dispatch",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionAsync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText}},
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			SideEffect:    side,
		},
		Binding: registry.ExecutorBinding{Executor: executor},
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

const (
	cbRunID          = "run_async"
	cbInputNodeRunID = "nr_input"
	cbTaskNodeRunID  = "nr_task"
	cbAttemptID      = "attempt_task_1"
	cbExternalTaskID = "provider_task_1"
	cbBindingID      = "binding_1"
	cbProviderID     = "mock-async-provider-v1"
)

type cbHarness struct {
	t     *testing.T
	store *cbStore
	svc   *ExecutionService
	exec  *cbAsyncExecutor
	clock *cbClock
	def   domain.Definition
	nodes *registry.NodeRegistry
}

func cbDefinition(workflowID string) domain.Definition {
	return domain.Definition{
		WorkflowID: workflowID,
		Version:    1,
		Name:       "Async dispatch",
		Nodes: []domain.Node{
			{ID: "input", Type: "text_input", Config: json.RawMessage(`{"inputKey":"brief","required":false}`)},
			{ID: "task", Type: cbAsyncNodeType, Config: json.RawMessage(`{}`)},
			{ID: "output", Type: "text_output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "input", SourceHandle: "text", Target: "task", TargetHandle: "text"},
			{ID: "e2", Source: "task", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
	}
}

func newCbHarness(t *testing.T, side domain.SideEffectPolicy) *cbHarness {
	t.Helper()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	clock := &cbClock{now: now}
	exec := &cbAsyncExecutor{externalTaskID: cbExternalTaskID, providerID: cbProviderID}

	nodes := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(),
		textoutput.Registration(),
		cbAsyncRegistration(exec, side),
	} {
		if err := nodes.Register(reg); err != nil {
			t.Fatalf("register node type: %v", err)
		}
	}

	def := cbDefinition("wf_async")
	st := &cbStore{state: newCbState(), def: def}

	svc := NewExecutionService(Deps{
		UoW:      st,
		Nodes:    nodes,
		Compiler: runtime.NewCompiler(nodes, clock),
		Clock:    clock,
		IDs:      &cbIDs{},
		Callback: CallbackConfig{
			BaseURL:       "https://emberling.test",
			SigningSecret: []byte("unit-test-signing-secret"),
			PendingTTL:    time.Hour,
		},
	})

	return &cbHarness{t: t, store: st, svc: svc, exec: exec, clock: clock, def: def, nodes: nodes}
}

// seedWaiting commits the state a committed dispatch leaves behind: the upstream input
// NodeRun SUCCEEDED, the async NodeRun WAITING_CALLBACK, its Attempt DISPATCHED with the
// stored token hash, the Callback Binding, and the Run PAUSED.
func (h *cbHarness) seedWaiting(tokenHash string) {
	h.t.Helper()
	now := h.clock.Now()
	st := h.store.state
	st.runs[cbRunID] = domain.Run{
		ID: cbRunID, WorkflowID: h.def.WorkflowID, DefinitionVersion: h.def.Version,
		Status: domain.RunPaused, Input: json.RawMessage(`{"brief":"ember"}`), StartedAt: now,
	}
	st.nodeRuns[cbInputNodeRunID] = domain.NodeRun{
		ID: cbInputNodeRunID, RunID: cbRunID, NodeID: "input", NodeType: "text_input",
		Status: domain.NodeRunSucceeded, Output: json.RawMessage(`{"text":"ember"}`), ReadyAt: now,
	}
	started := now
	st.nodeRuns[cbTaskNodeRunID] = domain.NodeRun{
		ID: cbTaskNodeRunID, RunID: cbRunID, NodeID: "task", NodeType: cbAsyncNodeType,
		Status: domain.NodeRunWaitingCallback, Input: json.RawMessage(`{"text":"ember"}`),
		AttemptCount: 1, ReadyAt: now, StartedAt: &started, WaitingAt: &started,
	}
	dispatched := now
	var hash *string
	if tokenHash != "" {
		hash = &tokenHash
	}
	st.attempts[cbAttemptID] = domain.NodeAttempt{
		ID: cbAttemptID, NodeRunID: cbTaskNodeRunID, AttemptNo: 1, Status: domain.NodeAttemptDispatched,
		Input: json.RawMessage(`{"text":"ember"}`), CallbackTokenHash: hash,
		StartedAt: now, DispatchedAt: &dispatched,
	}
	st.bindings[cbExternalTaskID] = domain.CallbackBinding{
		ID: cbBindingID, ProviderID: cbProviderID, ExternalTaskID: cbExternalTaskID,
		TargetType: domain.CallbackTargetNodeAttempt, TargetID: cbAttemptID, CreatedAt: now,
	}
	st.lastSeq = 7
}

func (h *cbHarness) eventTypes() []domain.EventType {
	out := make([]domain.EventType, 0, len(h.store.state.events))
	for _, ev := range h.store.state.events {
		out = append(out, ev.Type)
	}
	return out
}

func (h *cbHarness) eventOfType(typ domain.EventType) (domain.Event, bool) {
	for _, ev := range h.store.state.events {
		if ev.Type == typ {
			return ev, true
		}
	}
	return domain.Event{}, false
}

// ---------------------------------------------------------------------------
// Resume
// ---------------------------------------------------------------------------

// TestNodeResume_ProviderPollCompletion_OmitsNodeCallbackReceivedAndSetsProviderPollSource
// proves that only a callback that wins the completion right writes
// NODE_CALLBACK_RECEIVED, and a Provider Poll completion records completionSource
// PROVIDER_POLL on the completion Event instead. The poll result is already normalized,
// so the Executor's OnCallback is never asked to interpret it.
func TestNodeResume_ProviderPollCompletion_OmitsNodeCallbackReceivedAndSetsProviderPollSource(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h.seedWaiting("")
	h.exec.onCallback = func(registry.NodeAsyncState, []byte) (registry.NodeOutput, error) {
		t.Error("OnCallback was called for a normalized poll result")
		return registry.NodeOutput{}, errors.New("unexpected OnCallback")
	}

	outcome, err := h.svc.ResumeNode(context.Background(), ResumeNode{
		ExternalTaskID: cbExternalTaskID,
		Polled: &PolledResult{
			AttemptID: cbAttemptID,
			Result: registry.PollResult{
				Status: registry.PollSucceeded,
				Output: &registry.NodeOutput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`"generated"`)}},
			},
		},
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if outcome.Duplicate || outcome.Failed {
		t.Fatalf("resume outcome: want a fresh success, got %+v", outcome)
	}

	if _, found := h.eventOfType(domain.EventNodeCallbackReceived); found {
		t.Fatalf("NODE_CALLBACK_RECEIVED after a PROVIDER_POLL completion: want absent, got present (events: %v)", h.eventTypes())
	}
	completed, found := h.eventOfType(domain.EventNodeCompleted)
	if !found {
		t.Fatalf("NODE_COMPLETED after resume: want present, got events %v", h.eventTypes())
	}
	var payload nodeCompletedPayload
	if err := json.Unmarshal(completed.Payload, &payload); err != nil {
		t.Fatalf("decode NODE_COMPLETED payload: %v", err)
	}
	if payload.CompletionSource != domain.CompletionProviderPoll {
		t.Fatalf("NODE_COMPLETED completionSource: want %s, got %s", domain.CompletionProviderPoll, payload.CompletionSource)
	}
	if got := h.store.state.nodeRuns[cbTaskNodeRunID].Status; got != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun status after poll completion: want SUCCEEDED, got %s", got)
	}
	if got := h.store.state.attempts[cbAttemptID].Status; got != domain.NodeAttemptSucceeded {
		t.Fatalf("Attempt status after poll completion: want SUCCEEDED, got %s", got)
	}
}

// TestNodeResume_InvalidPayload_LeavesNodeRunWaiting proves that a payload that fails
// to parse cannot change the waiting state. A plain OnCallback error is not a Provider failure, so nothing is written
// and the caller gets a typed error it can still answer 200 to.
func TestNodeResume_InvalidPayload_LeavesNodeRunWaiting(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h.seedWaiting("")
	h.exec.onCallback = func(registry.NodeAsyncState, []byte) (registry.NodeOutput, error) {
		return registry.NodeOutput{}, errors.New("payload is missing the result field")
	}

	_, err := h.svc.ResumeNode(context.Background(), ResumeNode{
		ExternalTaskID: cbExternalTaskID,
		Payload:        json.RawMessage(`{"unexpected":true}`),
		Source:         domain.CompletionCallback,
	})
	var rejected *CallbackPayloadRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("resume with an uninterpretable payload: want *CallbackPayloadRejectedError, got %v", err)
	}

	if got := h.store.state.nodeRuns[cbTaskNodeRunID].Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("NodeRun status after a rejected payload: want WAITING_CALLBACK, got %s", got)
	}
	if got := h.store.state.attempts[cbAttemptID].Status; got != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status after a rejected payload: want DISPATCHED, got %s", got)
	}
	if len(h.store.state.events) != 0 {
		t.Fatalf("events after a rejected payload: want none, got %v", h.eventTypes())
	}
	if h.store.state.lastSeq != 7 {
		t.Fatalf("Run lastSeq after a rejected payload: want the pre-callback watermark 7, got %d", h.store.state.lastSeq)
	}
}

// TestNodeResume_ProviderFailureCallback_FailsAttemptWithCallbackSource proves the other
// half of callback resume: a ProviderFailure is a definite failure of the external task, so the
// DISPATCHED Attempt and the WAITING_CALLBACK NodeRun both fail, with failureSource
// CALLBACK rather than SYNC_EXECUTION or TIMEOUT.
func TestNodeResume_ProviderFailureCallback_FailsAttemptWithCallbackSource(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h.seedWaiting("")
	h.exec.onCallback = func(registry.NodeAsyncState, []byte) (registry.NodeOutput, error) {
		return registry.NodeOutput{}, &registry.ProviderFailure{
			Err: domain.ExecutionError{Code: "PROVIDER_TASK_FAILED", Message: "render failed"},
		}
	}

	outcome, err := h.svc.ResumeNode(context.Background(), ResumeNode{
		ExternalTaskID: cbExternalTaskID,
		Payload:        json.RawMessage(`{"status":"FAILED"}`),
		Source:         domain.CompletionCallback,
	})
	if err != nil {
		t.Fatalf("resume with a provider failure: %v", err)
	}
	if !outcome.Failed {
		t.Fatalf("resume outcome: want Failed=true, got %+v", outcome)
	}
	if outcome.FailureSource != domain.FailureCallback {
		t.Fatalf("resume failure source: want %s, got %s", domain.FailureCallback, outcome.FailureSource)
	}
	if got := h.store.state.attempts[cbAttemptID].Status; got != domain.NodeAttemptFailed {
		t.Fatalf("Attempt status after a provider failure callback: want FAILED, got %s", got)
	}
	if got := h.store.state.nodeRuns[cbTaskNodeRunID].Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after a provider failure callback: want FAILED, got %s", got)
	}
	if got := h.store.state.attempts[cbAttemptID].Error; got == nil || got.Code != "PROVIDER_TASK_FAILED" {
		t.Fatalf("Attempt error after a provider failure callback: want code PROVIDER_TASK_FAILED, got %+v", got)
	}
	if _, found := h.eventOfType(domain.EventNodeFailed); !found {
		t.Fatalf("NODE_FAILED after a provider failure callback: want present, got events %v", h.eventTypes())
	}
}

// TestNodeResume_PendingCallbackWithForeignTokenHash_RollsBackAndConsumesNothing proves
// that an unmatched Pending Callback has no right to advance Execution and that the
// callback token is Attempt-scoped: a stored early callback whose
// credential hash belongs to a different Attempt than the one dispatched must not
// complete this Attempt merely because both happen to share an external task id.
// ConsumeOnce still conditionally claims the row inside the transaction, but the whole
// transaction rolls back once the credential is found not to match, so the row is left
// exactly as it was for its real owner or for expiry to clean up.
func TestNodeResume_PendingCallbackWithForeignTokenHash_RollsBackAndConsumesNothing(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h.seedWaiting(hashCallbackToken("token-issued-to-this-attempt"))

	payload := json.RawMessage(`{"text":"generated"}`)
	h.store.state.pending[cbExternalTaskID] = domain.PendingCallback{
		ExternalTaskID:    cbExternalTaskID,
		Payload:           payload,
		PayloadHash:       payloadHash(payload),
		CallbackTokenHash: hashCallbackToken("token-issued-to-a-different-attempt"),
		ReceivedAt:        h.clock.Now(),
		ExpiresAt:         h.clock.Now().Add(time.Hour),
	}

	_, err := h.svc.ResumeNode(context.Background(), ResumeNode{
		ExternalTaskID: cbExternalTaskID,
		Payload:        payload,
		Source:         domain.CompletionCallback,
		ConsumePending: true,
		PayloadHash:    payloadHash(payload),
	})
	var mismatch *PendingCallbackCredentialMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("resume with a foreign pending-callback credential: want *PendingCallbackCredentialMismatchError, got %v", err)
	}
	if mismatch.AttemptID != cbAttemptID || mismatch.ExternalTaskID != cbExternalTaskID {
		t.Fatalf("mismatch error fields: want attempt %s / external task %s, got %+v", cbAttemptID, cbExternalTaskID, mismatch)
	}
	if strings.Contains(mismatch.Error(), hashCallbackToken("token-issued-to-this-attempt")) ||
		strings.Contains(mismatch.Error(), hashCallbackToken("token-issued-to-a-different-attempt")) {
		t.Fatalf("mismatch error message must not contain a token hash, got %q", mismatch.Error())
	}

	if got := h.store.state.nodeRuns[cbTaskNodeRunID].Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("NodeRun status after a foreign-credential pending callback: want WAITING_CALLBACK, got %s", got)
	}
	if got := h.store.state.attempts[cbAttemptID].Status; got != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status after a foreign-credential pending callback: want DISPATCHED, got %s", got)
	}
	if consumedAt := h.store.state.pending[cbExternalTaskID].ConsumedAt; consumedAt != nil {
		t.Fatalf("pending callback consumedAt after a rolled-back mismatch: want nil, got %v", consumedAt)
	}
	if len(h.store.state.events) != 0 {
		t.Fatalf("events after a rolled-back mismatch: want none, got %v", h.eventTypes())
	}
	if h.store.state.lastSeq != 7 {
		t.Fatalf("Run lastSeq after a rolled-back mismatch: want the pre-callback watermark 7, got %d", h.store.state.lastSeq)
	}
}

// ---------------------------------------------------------------------------
// Callback token
// ---------------------------------------------------------------------------

func TestCallbackToken_TamperedSignatureOrExpired_IsRejectedWithoutPersisting(t *testing.T) {
	secret := []byte("unit-test-signing-secret")
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	token, err := issueCallbackToken(secret, cbAttemptID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	claims, err := verifyCallbackToken(secret, token, now)
	if err != nil {
		t.Fatalf("verify a fresh token: %v", err)
	}
	if claims.AttemptID != cbAttemptID {
		t.Fatalf("verified token attempt id: want %s, got %s", cbAttemptID, claims.AttemptID)
	}

	tampered := token[:len(token)-2] + flipSuffix(token)
	if _, err := verifyCallbackToken(secret, tampered, now); !errors.Is(err, ErrInvalidCallbackCredential) {
		t.Fatalf("verify a tampered token: want ErrInvalidCallbackCredential, got %v", err)
	}
	if _, err := verifyCallbackToken([]byte("another-secret"), token, now); !errors.Is(err, ErrInvalidCallbackCredential) {
		t.Fatalf("verify with the wrong signing secret: want ErrInvalidCallbackCredential, got %v", err)
	}
	if _, err := verifyCallbackToken(secret, token, now.Add(2*time.Hour)); !errors.Is(err, ErrInvalidCallbackCredential) {
		t.Fatalf("verify an expired token: want ErrInvalidCallbackCredential, got %v", err)
	}

	// An Attempt with a deadline gets a grace window past it (deadline + PendingTTL), so a
	// callback that arrives while the timeout transaction is still racing is verifiable and
	// competes through the conditional update instead of being refused at the door. Once the
	// window closes the credential is dead.
	deadline := now.Add(time.Minute)
	graced, err := issueCallbackToken(secret, cbAttemptID, deadline.Add(time.Hour))
	if err != nil {
		t.Fatalf("issue token with a grace window: %v", err)
	}
	if _, err := verifyCallbackToken(secret, graced, deadline.Add(time.Second)); err != nil {
		t.Fatalf("verify a token just past its Attempt deadline but inside the grace window: %v", err)
	}
	if _, err := verifyCallbackToken(secret, graced, deadline.Add(2*time.Hour)); !errors.Is(err, ErrInvalidCallbackCredential) {
		t.Fatalf("verify a token past its grace window: want ErrInvalidCallbackCredential, got %v", err)
	}

	// An Attempt with no deadline has no expiry to derive one from, and inventing one would
	// silently kill a long-running Provider task. The Attempt's own DISPATCHED status is the
	// lifetime: the Binding target check and the from-DISPATCHED conditional update reject
	// anything late.
	endless, err := issueCallbackToken(secret, cbAttemptID, time.Time{})
	if err != nil {
		t.Fatalf("issue a token for an Attempt with no deadline: %v", err)
	}
	endlessClaims, err := verifyCallbackToken(secret, endless, now.AddDate(1, 0, 0))
	if err != nil {
		t.Fatalf("verify a deadline-less token a year later: want it accepted, got %v", err)
	}
	if !endlessClaims.ExpiresAt.IsZero() {
		t.Fatalf("deadline-less token expiry: want the zero time, got %s", endlessClaims.ExpiresAt)
	}

	// An unverifiable credential must never reach the database: HandleCallback rejects
	// before it records a Pending Callback.
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	if _, err := h.svc.HandleCallback(context.Background(), HandleCallback{
		Token: tampered, ExternalTaskID: cbExternalTaskID, Payload: json.RawMessage(`{"text":"x"}`),
	}); !errors.Is(err, ErrInvalidCallbackCredential) {
		t.Fatalf("HandleCallback with a tampered token: want ErrInvalidCallbackCredential, got %v", err)
	}
	if len(h.store.state.pending) != 0 {
		t.Fatalf("pending callbacks after a rejected credential: want none, got %d", len(h.store.state.pending))
	}
	if len(h.store.state.events) != 0 {
		t.Fatalf("events after a rejected credential: want none, got %v", h.eventTypes())
	}
}

// flipSuffix returns a two-character suffix that differs from the token's own last two
// characters, so the tampered token stays the same length and base64url alphabet.
func flipSuffix(token string) string {
	if strings.HasSuffix(token, "AA") {
		return "BB"
	}
	return "AA"
}

// TestCallbackToken_PlaintextNeverAppearsInEventsOrErrors covers the CLAUDE.md
// persistence rule ("keep... callback tokens... out of business fields, Events, Trace,
// logs, and errors") on both async paths: the committed dispatch and the failure that
// follows an unroutable dispatch. Only the SHA-256 hash may be persisted.
func TestCallbackToken_PlaintextNeverAppearsInEventsOrErrors(t *testing.T) {
	h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h.seedReadyTask()

	outcome, err := h.svc.Advance(context.Background(), cbRunID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !outcome.Claimed {
		t.Fatal("advance: want the async NodeRun claimed, got no claim")
	}
	if outcome.Input.Callback == nil || outcome.Input.Callback.Token == "" {
		t.Fatalf("claimed async Attempt: want a callback credential in NodeInput, got %+v", outcome.Input.Callback)
	}
	token := outcome.Input.Callback.Token
	if got, want := outcome.Input.Callback.URL, "https://emberling.test/api/callbacks"; got != want {
		t.Fatalf("callback URL: want %q, got %q", want, got)
	}
	stored := h.store.state.attempts[outcome.AttemptID].CallbackTokenHash
	if stored == nil || *stored != hashCallbackToken(token) {
		t.Fatalf("persisted callback_token_hash: want sha256(token), got %v", stored)
	}
	if stored != nil && *stored == token {
		t.Fatal("persisted callback_token_hash: the plaintext token itself was stored")
	}

	if err := h.svc.Execute(context.Background(), outcome); err != nil {
		t.Fatalf("execute async dispatch: %v", err)
	}
	for _, ev := range h.store.state.events {
		if strings.Contains(string(ev.Payload), token) {
			t.Fatalf("event %s payload contains the plaintext callback token", ev.Type)
		}
	}

	// Second half: a dispatch that cannot be routed fails the Attempt; neither the error
	// message nor the persisted failure summary may carry the credential.
	h2 := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
	h2.seedReadyTask()
	h2.exec.execErr = fmt.Errorf("provider rejected dispatch")
	outcome2, err := h2.svc.Advance(context.Background(), cbRunID)
	if err != nil {
		t.Fatalf("advance (failure path): %v", err)
	}
	token2 := outcome2.Input.Callback.Token
	if err := h2.svc.Execute(context.Background(), outcome2); err != nil {
		if strings.Contains(err.Error(), token2) {
			t.Fatal("Execute error message contains the plaintext callback token")
		}
		t.Fatalf("execute failing dispatch: %v", err)
	}
	for _, ev := range h2.store.state.events {
		if strings.Contains(string(ev.Payload), token2) {
			t.Fatalf("event %s payload contains the plaintext callback token", ev.Type)
		}
	}
	attemptErr := h2.store.state.attempts[outcome2.AttemptID].Error
	if attemptErr != nil && strings.Contains(attemptErr.Message, token2) {
		t.Fatal("persisted Attempt error contains the plaintext callback token")
	}
}

// seedReadyTask commits an upstream-satisfied, still READY async NodeRun, so Advance can
// claim it and startAttempt has to issue the callback credential.
func (h *cbHarness) seedReadyTask() {
	h.t.Helper()
	now := h.clock.Now()
	st := h.store.state
	st.runs[cbRunID] = domain.Run{
		ID: cbRunID, WorkflowID: h.def.WorkflowID, DefinitionVersion: h.def.Version,
		Status: domain.RunRunning, Input: json.RawMessage(`{"brief":"ember"}`), StartedAt: now,
	}
	st.nodeRuns[cbInputNodeRunID] = domain.NodeRun{
		ID: cbInputNodeRunID, RunID: cbRunID, NodeID: "input", NodeType: "text_input",
		Status: domain.NodeRunSucceeded, Output: json.RawMessage(`{"text":"ember"}`), ReadyAt: now,
	}
	st.nodeRuns[cbTaskNodeRunID] = domain.NodeRun{
		ID: cbTaskNodeRunID, RunID: cbRunID, NodeID: "task", NodeType: cbAsyncNodeType,
		Status: domain.NodeRunReady, Input: json.RawMessage(`{}`), ReadyAt: now,
	}
	st.lastSeq = 3
}

// TestResumeNode_MixedCallbackAndPollShape_RejectedBeforeAnyRead pins the request shape of
// the single resume use case: a poll result never carries a callback body, never replays a
// Pending Callback, and PROVIDER_POLL is never handed to OnCallback as a raw body. Each
// mixed shape is refused before any routing read, so nothing is written.
func TestResumeNode_MixedCallbackAndPollShape_RejectedBeforeAnyRead(t *testing.T) {
	polled := &PolledResult{AttemptID: cbAttemptID, Result: registry.PollResult{Status: registry.PollRunning}}
	cases := map[string]ResumeNode{
		"poll source without a poll result": {ExternalTaskID: cbExternalTaskID, Payload: json.RawMessage(`{"text":"x"}`), Source: domain.CompletionProviderPoll},
		"poll result with callback source":  {ExternalTaskID: cbExternalTaskID, Source: domain.CompletionCallback, Polled: polled},
		"poll result with a callback body":  {ExternalTaskID: cbExternalTaskID, Payload: json.RawMessage(`{"text":"x"}`), Polled: polled},
		"poll result replaying a pending":   {ExternalTaskID: cbExternalTaskID, ConsumePending: true, Polled: polled},
		"poll result with a payload hash":   {ExternalTaskID: cbExternalTaskID, PayloadHash: "h", Polled: polled},
		"poll result naming no attempt":     {ExternalTaskID: cbExternalTaskID, Polled: &PolledResult{Result: registry.PollResult{Status: registry.PollRunning}}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			h := newCbHarness(t, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown})
			h.seedWaiting("")
			before := len(h.store.state.events)
			if _, err := h.svc.ResumeNode(context.Background(), req); !errors.Is(err, ErrInvalidResumeRequest) {
				t.Fatalf("ResumeNode(%s): want ErrInvalidResumeRequest, got %v", name, err)
			}
			if got := len(h.store.state.events); got != before {
				t.Fatalf("events after a refused request: want %d, got %d", before, got)
			}
			if got := h.store.state.attempts[cbAttemptID].Status; got != domain.NodeAttemptDispatched {
				t.Fatalf("Attempt after a refused request: want DISPATCHED, got %s", got)
			}
		})
	}
}
