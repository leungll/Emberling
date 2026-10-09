//go:build integration

// Async Node suspend/resume integration tests. Every guarantee proven here is
// transactional or a race: the dispatch transaction commits the Callback Binding and
// WAITING_CALLBACK together or not at all, and callback, Provider Poll and timeout all
// compete for one completion right decided by a conditional UPDATE. A mock repository
// cannot prove either (CLAUDE.md testing standard), so these run against real PostgreSQL.
//
// Helpers are prefixed `async` to stay clear of the `exec`/`store` helpers that the
// sibling files in this package already own. The Node used here is a deterministic
// in-package fake: cmd/mockprovider is the HTTP fixture for the end-to-end track, and
// depending on it would make these transaction proofs depend on a network server.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

const (
	asyncNodeType      = "async_dispatch"
	asyncProviderID    = "mock-async-provider-v1"
	asyncSigningSecret = "integration-callback-signing-secret"
	asyncBaseURL       = "https://emberling.test"
)

// ---------------------------------------------------------------------------
// Deterministic async Node
// ---------------------------------------------------------------------------

// asyncFakeExecutor dispatches to an imaginary Provider and interprets its callbacks. It
// is safe for concurrent use because the timeout-vs-callback test drives two goroutines
// through the same service instance.
type asyncFakeExecutor struct {
	mu sync.Mutex

	externalTaskID string
	dispatchErr    error
	omitTaskID     bool
	dispatched     []string
	keys           []string
	callbacks      int
}

func (e *asyncFakeExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (e *asyncFakeExecutor) setExternalTaskID(id string) {
	e.mu.Lock()
	e.externalTaskID = id
	e.mu.Unlock()
}

func (e *asyncFakeExecutor) setDispatchErr(err error) {
	e.mu.Lock()
	e.dispatchErr = err
	e.mu.Unlock()
}

// dispatchCount reports how many external tasks this Provider was actually asked to
// create, which is how "no re-dispatch" is observed from the Provider's side.
func (e *asyncFakeExecutor) dispatchCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.dispatched)
}

// idempotencyKeys reports, in order, the NodeInput.IdempotencyKey of every dispatch call
// the Provider received. The keyed-retry test asserts byte-for-byte reuse, so the
// observation point is the Provider boundary itself, not the AdvanceOutcome.
func (e *asyncFakeExecutor) idempotencyKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.keys...)
}

func (e *asyncFakeExecutor) Execute(_ context.Context, in registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.keys = append(e.keys, in.IdempotencyKey)
	if e.dispatchErr != nil {
		return registry.NodeResult{}, e.dispatchErr
	}
	if in.Callback == nil || in.Callback.Token == "" {
		return registry.NodeResult{}, fmt.Errorf("async_dispatch: no callback credential was provided")
	}
	if e.omitTaskID {
		return registry.NodeResult{Kind: registry.NodeResultDispatched}, nil
	}
	e.dispatched = append(e.dispatched, e.externalTaskID)
	return registry.NodeResult{
		Kind:         registry.NodeResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: asyncProviderID, ExternalTaskID: e.externalTaskID},
	}, nil
}

// OnCallback interprets the Provider body: {"text": ...} completes the task, "FAILED"
// reports a Provider failure, and anything else is an uninterpretable payload.
func (e *asyncFakeExecutor) OnCallback(_ context.Context, _ registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error) {
	e.mu.Lock()
	e.callbacks++
	e.mu.Unlock()

	var body struct {
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.NodeOutput{}, fmt.Errorf("async_dispatch: undecodable callback payload: %w", err)
	}
	switch {
	case body.Status == "FAILED":
		return registry.NodeOutput{}, &registry.ProviderFailure{
			Err: domain.ExecutionError{Code: "PROVIDER_TASK_FAILED", Message: "provider reported the task failed"},
		}
	case body.Text != "":
		return registry.NodeOutput{Ports: map[string]json.RawMessage{
			"text": json.RawMessage(strconv.Quote(body.Text)),
		}}, nil
	default:
		return registry.NodeOutput{}, errors.New("async_dispatch: payload carries neither a status nor a text result")
	}
}

func asyncRegistration(executor registry.NodeExecutor, side domain.SideEffectPolicy) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          asyncNodeType,
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

type asyncHarness struct {
	t   *testing.T
	ctx context.Context

	pool  *pgxpool.Pool
	uow   store.UnitOfWork
	clock *execClock
	ids   *execForcedIDs
	exec  *asyncFakeExecutor
	svc   *service.ExecutionService
	def   domain.Definition
}

func newAsyncHarness(t *testing.T, side domain.SideEffectPolicy, policy domain.ExecutionPolicy) *asyncHarness {
	t.Helper()
	pool := testdb.Open(t)
	h := &asyncHarness{t: t, ctx: context.Background(), pool: pool, uow: postgres.NewUnitOfWork(pool)}
	h.clock = newExecClock(fixtureTime)
	h.ids = newExecForcedIDs()
	h.exec = &asyncFakeExecutor{externalTaskID: "provider-task-1"}
	h.svc = h.newService(side, h.exec)
	h.def = h.saveAsyncDefinition(side, policy)
	return h
}

// newService builds an ExecutionService over this harness's database. Calling it a second
// time models a process restart: registry, compiled-plan cache and Executor instance are
// all fresh, and PostgreSQL is the only carried-over state.
func (h *asyncHarness) newService(side domain.SideEffectPolicy, executor *asyncFakeExecutor) *service.ExecutionService {
	h.t.Helper()
	nodes := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(),
		textoutput.Registration(),
		asyncRegistration(executor, side),
	} {
		if err := nodes.Register(reg); err != nil {
			h.t.Fatalf("register node type: %v", err)
		}
	}
	return service.NewExecutionService(service.Deps{
		UoW:      h.uow,
		Nodes:    nodes,
		Compiler: runtime.NewCompiler(nodes, h.clock),
		Clock:    h.clock,
		IDs:      h.ids,
		Callback: service.CallbackConfig{
			BaseURL:       asyncBaseURL,
			SigningSecret: []byte(asyncSigningSecret),
			PendingTTL:    time.Hour,
		},
	})
}

func (h *asyncHarness) saveAsyncDefinition(side domain.SideEffectPolicy, policy domain.ExecutionPolicy) domain.Definition {
	h.t.Helper()
	task := domain.Node{ID: "task", Type: asyncNodeType, Name: "Dispatch", Config: json.RawMessage(`{}`)}
	if policy.MaxAttempts > 0 || policy.TimeoutMs > 0 {
		p := policy
		task.ExecutionPolicy = &p
	}
	def := domain.Definition{
		WorkflowID:  "wf_async_" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Version:     1,
		Name:        "Async dispatch",
		Description: "text_input -> async_dispatch -> text_output",
		Nodes: []domain.Node{
			{ID: "input", Type: "text_input", Name: "Brief", Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			task,
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "input", SourceHandle: "text", Target: "task", TargetHandle: "text"},
			{ID: "e2", Source: "task", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: fixtureTime,
	}

	nodes := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(),
		textoutput.Registration(),
		asyncRegistration(h.exec, side),
	} {
		if err := nodes.Register(reg); err != nil {
			h.t.Fatalf("register node type: %v", err)
		}
	}
	compiler := runtime.NewCompiler(nodes, h.clock)
	plan, err := compiler.Compile(h.ctx, def)
	if err != nil {
		h.t.Fatalf("compile async definition: %v", err)
	}
	def.RunInputSchema = plan.RunInputSchema
	def.Validation = plan.Validation

	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	}); err != nil {
		h.t.Fatalf("save async definition: %v", err)
	}
	return def
}

// asyncDispatch is what a committed dispatch leaves the test holding: the plaintext
// credential (which exists nowhere else) and the external task id it was bound to.
type asyncDispatch struct {
	run            domain.Run
	nodeRunID      string
	attemptID      string
	token          string
	externalTaskID string
}

func (h *asyncHarness) createRun() domain.Run {
	h.t.Helper()
	run, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: h.def.WorkflowID, DefinitionVersion: h.def.Version,
		Input: json.RawMessage(`{"brief":"ember"}`),
	})
	if err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	return run
}

// claimAsyncNode advances the Run until the async Node's Attempt is claimed, executing
// every upstream Node on the way. The returned outcome still holds the callback token: the
// Provider has not been called yet.
func (h *asyncHarness) claimAsyncNode(run domain.Run) service.AdvanceOutcome {
	h.t.Helper()
	for i := 0; i < 10; i++ {
		outcome, err := h.svc.Advance(h.ctx, run.ID)
		if err != nil {
			h.t.Fatalf("advance: %v", err)
		}
		if !outcome.Claimed {
			h.t.Fatalf("advance: no claim before reaching the async node")
		}
		if outcome.NodeType == asyncNodeType {
			if outcome.Input.Callback == nil || outcome.Input.Callback.Token == "" {
				h.t.Fatal("claimed async Attempt carries no callback credential")
			}
			return outcome
		}
		if err := h.svc.Execute(h.ctx, outcome); err != nil {
			h.t.Fatalf("execute %s: %v", outcome.NodeType, err)
		}
	}
	h.t.Fatal("advance: async node was never claimed")
	return service.AdvanceOutcome{}
}

// dispatchAsyncNode runs the whole three-phase dispatch and returns the committed facts.
func (h *asyncHarness) dispatchAsyncNode(run domain.Run, externalTaskID string) asyncDispatch {
	h.t.Helper()
	h.exec.setExternalTaskID(externalTaskID)
	outcome := h.claimAsyncNode(run)
	token := outcome.Input.Callback.Token
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		h.t.Fatalf("execute async dispatch: %v", err)
	}
	return asyncDispatch{
		run: run, nodeRunID: outcome.NodeRunID, attemptID: outcome.AttemptID,
		token: token, externalTaskID: externalTaskID,
	}
}

func (h *asyncHarness) handleCallback(token, externalTaskID, payload string) (service.CallbackOutcome, error) {
	return h.svc.HandleCallback(h.ctx, service.HandleCallback{
		Token: token, ExternalTaskID: externalTaskID, Payload: json.RawMessage(payload),
	})
}

func asyncSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// asyncNodeEventsOfType keeps an assertion on the async Node's own Events: a Run also
// contains the upstream and downstream Nodes' NODE_COMPLETED rows.
func asyncNodeEventsOfType(events []domain.Event, nodeRunID string, typ domain.EventType) []domain.Event {
	var out []domain.Event
	for _, ev := range events {
		if ev.Type == typ && ev.NodeRunID != nil && *ev.NodeRunID == nodeRunID {
			out = append(out, ev)
		}
	}
	return out
}

// asyncPendingStatus reports whether a Pending Callback row exists for an external task id
// and whether it was consumed, without failing the test when it is legitimately absent.
func asyncPendingStatus(ctx context.Context, t *testing.T, uow store.UnitOfWork, externalTaskID string) string {
	t.Helper()
	status := "absent"
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		row, err := tx.PendingCallbacks().GetByExternalTaskID(ctx, externalTaskID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		status = "stored"
		if row.ConsumedAt != nil {
			status = "consumed"
		}
		return nil
	}); err != nil {
		t.Fatalf("read pending callback %s: %v", externalTaskID, err)
	}
	return status
}

func asyncEventsOfType(events []domain.Event, typ domain.EventType) []domain.Event {
	var out []domain.Event
	for _, ev := range events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func asyncNodeRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, nodeID string) domain.NodeRun {
	t.Helper()
	return execNodeRunByNodeID(t, listNodeRuns(ctx, t, uow, runID), nodeID)
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// TestNodeDispatch_ProviderAcceptedTask_CommitsBindingAndWaitingCallbackTogether proves
// the second phase of async dispatch is atomic in both directions: on success the
// Binding, the WAITING_CALLBACK NodeRun, the DISPATCHED Attempt and NODE_DISPATCHED all
// commit together; when the Event insert fails, none of them survive.
func TestNodeDispatch_ProviderAcceptedTask_CommitsBindingAndWaitingCallbackTogether(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-dispatch")

	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status after dispatch: want DISPATCHED, got %s", attempt.Status)
	}
	if attempt.DispatchedAt == nil {
		t.Fatal("Attempt dispatchedAt after dispatch: want a timestamp, got nil")
	}
	if attempt.CallbackTokenHash == nil || *attempt.CallbackTokenHash != asyncSHA256(dispatch.token) {
		t.Fatalf("persisted callback_token_hash: want sha256 of the issued token, got %v", attempt.CallbackTokenHash)
	}

	nodeRun := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID)
	if nodeRun.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("NodeRun status after dispatch: want WAITING_CALLBACK, got %s", nodeRun.Status)
	}
	binding := getBinding(h.ctx, t, h.uow, dispatch.externalTaskID)
	if binding.TargetType != domain.CallbackTargetNodeAttempt || binding.TargetID != dispatch.attemptID {
		t.Fatalf("Callback Binding target: want NODE_ATTEMPT %s, got %s %s", dispatch.attemptID, binding.TargetType, binding.TargetID)
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunPaused {
		t.Fatalf("Run status while its only live NodeRun waits: want PAUSED, got %s", got)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	dispatched := asyncEventsOfType(events, domain.EventNodeDispatched)
	if len(dispatched) != 1 {
		t.Fatalf("NODE_DISPATCHED events: want 1, got %d (%v)", len(dispatched), execEventTypes(events))
	}
	var payload map[string]any
	if err := json.Unmarshal(dispatched[0].Payload, &payload); err != nil {
		t.Fatalf("decode NODE_DISPATCHED payload: %v", err)
	}
	if payload["callbackBindingId"] != binding.ID {
		t.Fatalf("NODE_DISPATCHED callbackBindingId: want %q, got %v", binding.ID, payload["callbackBindingId"])
	}
	if _, present := payload["attemptNo"]; !present {
		t.Fatalf("NODE_DISPATCHED payload: want an attemptNo, got %v", payload)
	}
	// providerId/externalTaskId are projected from the Callback Binding and
	// never copied into an Event.
	for _, forbidden := range []string{"providerId", "externalTaskId", "token", "callbackToken"} {
		if _, present := payload[forbidden]; present {
			t.Fatalf("NODE_DISPATCHED payload must not carry %q, got %v", forbidden, payload)
		}
	}
	if len(asyncEventsOfType(events, domain.EventRunPaused)) != 1 {
		t.Fatalf("RUN_PAUSED events after dispatch: want 1, got %v", execEventTypes(events))
	}

	// Same dispatch, but the NODE_DISPATCHED insert fails: nothing may survive.
	h2 := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run2 := h2.createRun()
	h2.exec.setExternalTaskID("provider-task-rollback")
	outcome := h2.claimAsyncNode(run2)
	collidingID := "evt_dispatch_collision"
	execAppendFixedEvent(h2.ctx, t, h2.uow, run2.ID, collidingID, h2.clock.Now())
	h2.ids.ForceNextEventID(collidingID)

	if err := h2.svc.Execute(h2.ctx, outcome); err == nil {
		t.Fatal("execute with a colliding NODE_DISPATCHED event id: want an error, got nil")
	}
	if got := getNodeRun(h2.ctx, t, h2.uow, outcome.NodeRunID).Status; got != domain.NodeRunRunning {
		t.Fatalf("NodeRun status after the dispatch transaction rolled back: want RUNNING, got %s", got)
	}
	if got := getAttempt(h2.ctx, t, h2.uow, outcome.AttemptID).Status; got != domain.NodeAttemptStarted {
		t.Fatalf("Attempt status after the dispatch transaction rolled back: want STARTED, got %s", got)
	}
	err := h2.uow.WithinTx(h2.ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.CallbackBindings().GetByExternalTaskID(ctx, "provider-task-rollback")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Callback Binding after the dispatch transaction rolled back: want ErrNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Resume
// ---------------------------------------------------------------------------

// TestNodeResume_CallbackWins_WritesNodeCallbackReceivedInSameTransaction proves the
// resume transaction of async dispatch: the winning callback writes
// NODE_CALLBACK_RECEIVED and NODE_COMPLETED under one lock, with consecutive seq values,
// and resumes the Run.
func TestNodeResume_CallbackWins_WritesNodeCallbackReceivedInSameTransaction(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-win")

	outcome, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"generated"}`)
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if !outcome.Accepted || outcome.Duplicate {
		t.Fatalf("callback outcome: want accepted and not duplicate, got %+v", outcome)
	}

	if got := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun status after the winning callback: want SUCCEEDED, got %s", got)
	}
	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).Status; got != domain.NodeAttemptSucceeded {
		t.Fatalf("Attempt status after the winning callback: want SUCCEEDED, got %s", got)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	received := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCallbackReceived)
	completed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCompleted)
	if len(received) != 1 || len(completed) != 1 {
		t.Fatalf("callback events: want exactly one NODE_CALLBACK_RECEIVED and one NODE_COMPLETED, got %v", execEventTypes(events))
	}
	if completed[0].Seq != received[0].Seq+1 {
		t.Fatalf("NODE_CALLBACK_RECEIVED seq %d and NODE_COMPLETED seq %d: want consecutive seq from one transaction", received[0].Seq, completed[0].Seq)
	}
	var receivedPayload map[string]any
	if err := json.Unmarshal(received[0].Payload, &receivedPayload); err != nil {
		t.Fatalf("decode NODE_CALLBACK_RECEIVED payload: %v", err)
	}
	if got := receivedPayload["payloadHash"]; got != asyncSHA256(`{"text":"generated"}`) {
		t.Fatalf("NODE_CALLBACK_RECEIVED payloadHash: want sha256 of the body, got %v", got)
	}
	for _, forbidden := range []string{"payload", "token", "callbackTokenHash", "externalTaskId"} {
		if _, present := receivedPayload[forbidden]; present {
			t.Fatalf("NODE_CALLBACK_RECEIVED payload must not carry %q, got %v", forbidden, receivedPayload)
		}
	}
	var completedPayload struct {
		CompletionSource string `json:"completionSource"`
	}
	if err := json.Unmarshal(completed[0].Payload, &completedPayload); err != nil {
		t.Fatalf("decode NODE_COMPLETED payload: %v", err)
	}
	if completedPayload.CompletionSource != string(domain.CompletionCallback) {
		t.Fatalf("NODE_COMPLETED completionSource: want CALLBACK, got %q", completedPayload.CompletionSource)
	}
	if len(asyncEventsOfType(events, domain.EventRunResumed)) != 1 {
		t.Fatalf("RUN_RESUMED events after the callback: want 1, got %v", execEventTypes(events))
	}

	// The Run carries on normally: the downstream Output Node became READY in the same
	// transaction and can now be executed.
	if got := asyncNodeRun(h.ctx, t, h.uow, run.ID, "output").Status; got != domain.NodeRunReady {
		t.Fatalf("downstream Output NodeRun after the callback: want READY, got %s", got)
	}
}

// TestNodeResume_DuplicateCallback_DoesNotAdvanceTwice covers duplicate callbacks:
// the second delivery of the same result changes nothing and is reported as a duplicate.
func TestNodeResume_DuplicateCallback_DoesNotAdvanceTwice(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-duplicate")

	if _, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"generated"}`); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	firstEvents := listEvents(h.ctx, t, h.uow, run.ID)
	firstSeq := getRun(h.ctx, t, h.uow, run.ID).LastSeq

	second, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"generated"}`)
	if err != nil {
		t.Fatalf("second callback: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("second delivery of the same callback: want Duplicate=true, got %+v", second)
	}

	secondEvents := listEvents(h.ctx, t, h.uow, run.ID)
	if len(secondEvents) != len(firstEvents) {
		t.Fatalf("events after a duplicate callback: want %d unchanged, got %d (%v)", len(firstEvents), len(secondEvents), execEventTypes(secondEvents))
	}
	if len(asyncNodeEventsOfType(secondEvents, dispatch.nodeRunID, domain.EventNodeCompleted)) != 1 {
		t.Fatalf("NODE_COMPLETED events after a duplicate callback: want exactly 1, got %v", execEventTypes(secondEvents))
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).LastSeq; got != firstSeq {
		t.Fatalf("Run lastSeq after a duplicate callback: want %d unchanged, got %d", firstSeq, got)
	}
}

// TestNodeResume_StaleCallback_DoesNotConsumeEventSeq covers that an invalid or late
// callback does not consume an Event seq: a delivery for an Attempt that already failed
// must leave the Event log and the seq watermark untouched.
func TestNodeResume_StaleCallback_DoesNotConsumeEventSeq(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-stale")

	h.clock.Advance(2 * time.Minute)
	if err := h.svc.TimeoutAttempt(h.ctx, dispatch.attemptID); err != nil {
		t.Fatalf("timeout attempt: %v", err)
	}
	beforeEvents := listEvents(h.ctx, t, h.uow, run.ID)
	beforeSeq := getRun(h.ctx, t, h.uow, run.ID).LastSeq

	// The token expired with the Attempt deadline, so the delivery enters through the
	// shared resume use case the Provider Poll and the Reconciler also use.
	outcome, err := h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: dispatch.externalTaskID,
		Payload:        json.RawMessage(`{"text":"too late"}`),
		Source:         domain.CompletionCallback,
	})
	if err != nil {
		t.Fatalf("stale resume: %v", err)
	}
	if !outcome.Duplicate {
		t.Fatalf("stale callback outcome: want Duplicate=true, got %+v", outcome)
	}

	afterEvents := listEvents(h.ctx, t, h.uow, run.ID)
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("events after a stale callback: want %d unchanged, got %d (%v)", len(beforeEvents), len(afterEvents), execEventTypes(afterEvents))
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).LastSeq; got != beforeSeq {
		t.Fatalf("Run lastSeq after a stale callback: want %d unchanged, got %d", beforeSeq, got)
	}
	if got := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after the timeout won: want FAILED, got %s", got)
	}
}

// TestNodeResume_OldAttemptCallback_DoesNotOverwriteNewerAttempt covers a callback for an
// old Attempt: a credential issued to a superseded Attempt must not complete the NodeRun
// that a newer Attempt is now waiting on.
//
// The newer Attempt is produced the only way MVP allows one: the first Attempt failed
// definitely while still STARTED -- the Provider call came back with an error, so no
// Callback Binding was ever committed -- and an EXTERNAL+KEYED Node carries a Provider
// idempotency key, so that failure is retryable. An Attempt that already reached
// WAITING_CALLBACK never produces a second one.
func TestNodeResume_OldAttemptCallback_DoesNotOverwriteNewerAttempt(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	run := h.createRun()

	h.exec.setExternalTaskID("provider-task-old")
	first := h.claimAsyncNode(run)
	firstToken := first.Input.Callback.Token
	h.exec.setDispatchErr(errors.New("provider connection reset while accepting the task"))
	if err := h.svc.Execute(h.ctx, first); err != nil {
		t.Fatalf("execute the failing dispatch: %v", err)
	}
	nodeRun := getNodeRun(h.ctx, t, h.uow, first.NodeRunID)
	if nodeRun.Status != domain.NodeRunRunning || nodeRun.NextAttemptAt == nil {
		t.Fatalf("NodeRun after a definite dispatch failure with attempts left: want RUNNING with a nextAttemptAt, got %s / %v", nodeRun.Status, nodeRun.NextAttemptAt)
	}

	h.exec.setDispatchErr(nil)
	h.clock.Advance(time.Minute)
	second := h.dispatchAsyncNode(run, "provider-task-new")
	if second.attemptID == first.AttemptID {
		t.Fatal("retry reused the original Attempt id; a retry must create a new Attempt")
	}
	if second.token == firstToken {
		t.Fatal("retry reused the original callback credential; each Attempt gets its own")
	}

	beforeEvents := listEvents(h.ctx, t, h.uow, run.ID)

	// The old Attempt's credential presented against the live Attempt's Binding: the token
	// is Attempt-scoped, so it is refused before anything is read or written.
	if _, err := h.handleCallback(firstToken, second.externalTaskID, `{"text":"stolen by the old attempt"}`); !errors.Is(err, service.ErrInvalidCallbackCredential) {
		t.Fatalf("old credential against the newer Attempt's binding: want ErrInvalidCallbackCredential, got %v", err)
	}
	if got := asyncPendingStatus(h.ctx, t, h.uow, second.externalTaskID); got != "absent" {
		t.Fatalf("pending callback after a refused credential: want none, got %s", got)
	}

	// The old Attempt's own external task -- the Provider may still have accepted it before
	// the connection broke -- has no Binding at all. It is stored for its owner, which no
	// longer exists, and must not touch the Attempt that is waiting now.
	outcome, err := h.handleCallback(firstToken, "provider-task-old", `{"text":"result of the abandoned attempt"}`)
	if err != nil {
		t.Fatalf("callback for the abandoned attempt: %v", err)
	}
	if !outcome.Pending || outcome.Accepted {
		t.Fatalf("callback for an Attempt that never committed a Binding: want Pending, got %+v", outcome)
	}

	if got := getAttempt(h.ctx, t, h.uow, first.AttemptID).Status; got != domain.NodeAttemptFailed {
		t.Fatalf("superseded Attempt status: want FAILED, got %s", got)
	}
	if got := getAttempt(h.ctx, t, h.uow, second.attemptID).Status; got != domain.NodeAttemptDispatched {
		t.Fatalf("current Attempt status after the old callbacks: want DISPATCHED, got %s", got)
	}
	if got := getNodeRun(h.ctx, t, h.uow, second.nodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("NodeRun status after the old callbacks: want WAITING_CALLBACK, got %s", got)
	}
	afterEvents := listEvents(h.ctx, t, h.uow, run.ID)
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("events after the old-Attempt callbacks: want %d unchanged, got %d (%v)", len(beforeEvents), len(afterEvents), execEventTypes(afterEvents))
	}

	// The live Attempt's own credential still completes it exactly once.
	final, err := h.handleCallback(second.token, second.externalTaskID, `{"text":"real result"}`)
	if err != nil {
		t.Fatalf("callback for the live attempt: %v", err)
	}
	if !final.Accepted || final.Duplicate {
		t.Fatalf("callback for the live Attempt: want accepted, got %+v", final)
	}
	if got := getNodeRun(h.ctx, t, h.uow, second.nodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun status after the live Attempt's callback: want SUCCEEDED, got %s", got)
	}
}

// TestNodeResume_ProviderPollCompletion_OmitsCallbackEventInPostgres is the PostgreSQL
// counterpart of the unit-level PROVIDER_POLL test: the same resume use case, entered with
// a normalized poll result instead of a callback body, must not write
// NODE_CALLBACK_RECEIVED.
func TestNodeResume_ProviderPollCompletion_OmitsCallbackEventInPostgres(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll")

	outcome, err := h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: dispatch.externalTaskID,
		Polled: &service.PolledResult{
			AttemptID: dispatch.attemptID,
			Result: registry.PollResult{
				Status: registry.PollSucceeded,
				Output: &registry.NodeOutput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`"polled"`)}},
			},
		},
	})
	if err != nil {
		t.Fatalf("resume from poll: %v", err)
	}
	if outcome.Duplicate {
		t.Fatalf("poll resume outcome: want a fresh completion, got %+v", outcome)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	if got := asyncEventsOfType(events, domain.EventNodeCallbackReceived); len(got) != 0 {
		t.Fatalf("NODE_CALLBACK_RECEIVED after a PROVIDER_POLL completion: want none, got %v", execEventTypes(events))
	}
	completed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCompleted)
	if len(completed) != 1 {
		t.Fatalf("NODE_COMPLETED events for the async Node: want 1, got %v", execEventTypes(events))
	}
	var payload struct {
		CompletionSource string `json:"completionSource"`
	}
	if err := json.Unmarshal(completed[0].Payload, &payload); err != nil {
		t.Fatalf("decode NODE_COMPLETED payload: %v", err)
	}
	if payload.CompletionSource != string(domain.CompletionProviderPoll) {
		t.Fatalf("NODE_COMPLETED completionSource: want PROVIDER_POLL, got %q", payload.CompletionSource)
	}
}

// ---------------------------------------------------------------------------
// Callback intake
// ---------------------------------------------------------------------------

// TestCallbackIntake_BindingNotYetCommitted_StoresPendingCallback covers the early
// callback of async dispatch: the Provider answered before the dispatch transaction
// committed, so the delivery is stored instead of being lost or acted on.
func TestCallbackIntake_BindingNotYetCommitted_StoresPendingCallback(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run := h.createRun()
	h.exec.setExternalTaskID("provider-task-early")
	outcome := h.claimAsyncNode(run)
	token := outcome.Input.Callback.Token

	body := `{"text":"early"}`
	first, err := h.handleCallback(token, "provider-task-early", body)
	if err != nil {
		t.Fatalf("early callback: %v", err)
	}
	if !first.Pending || first.Accepted || first.Duplicate {
		t.Fatalf("early callback outcome: want Pending only, got %+v", first)
	}

	pending := getPending(h.ctx, t, h.uow, "provider-task-early")
	if pending.PayloadHash != asyncSHA256(body) {
		t.Fatalf("pending payloadHash: want sha256 of the body, got %q", pending.PayloadHash)
	}
	if pending.CallbackTokenHash != asyncSHA256(token) {
		t.Fatalf("pending callbackTokenHash: want sha256 of the token, got %q", pending.CallbackTokenHash)
	}
	if pending.ConsumedAt != nil {
		t.Fatalf("pending consumedAt before its Binding exists: want nil, got %v", pending.ConsumedAt)
	}
	if got := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID).Status; got != domain.NodeRunRunning {
		t.Fatalf("NodeRun status after an early callback: want RUNNING (still dispatching), got %s", got)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if len(asyncEventsOfType(events, domain.EventNodeCallbackReceived)) != 0 {
		t.Fatalf("NODE_CALLBACK_RECEIVED for a stored early callback: want none, got %v", execEventTypes(events))
	}

	second, err := h.handleCallback(token, "provider-task-early", body)
	if err != nil {
		t.Fatalf("repeated early callback: %v", err)
	}
	if !second.Pending || !second.Duplicate {
		t.Fatalf("repeated early callback outcome: want Pending and Duplicate, got %+v", second)
	}
}

// TestCallbackIntake_BindingCommitted_ConsumesPendingCallbackOnce proves the third phase
// of async dispatch: after the Binding commits, the stored early callback advances the
// NodeRun exactly once, and the row is marked consumed so no later pass can replay it.
func TestCallbackIntake_BindingCommitted_ConsumesPendingCallbackOnce(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run := h.createRun()
	h.exec.setExternalTaskID("provider-task-early-win")
	outcome := h.claimAsyncNode(run)
	token := outcome.Input.Callback.Token

	if _, err := h.handleCallback(token, "provider-task-early-win", `{"text":"early"}`); err != nil {
		t.Fatalf("early callback: %v", err)
	}
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		t.Fatalf("execute async dispatch: %v", err)
	}

	if got := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun status after the dispatch consumed its early callback: want SUCCEEDED, got %s", got)
	}
	pending := getPending(h.ctx, t, h.uow, "provider-task-early-win")
	if pending.ConsumedAt == nil {
		t.Fatal("pending consumedAt after the dispatch consumed it: want a timestamp, got nil")
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if len(asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeCompleted)) != 1 {
		t.Fatalf("NODE_COMPLETED events after consuming an early callback: want 1, got %v", execEventTypes(events))
	}
	if len(asyncEventsOfType(events, domain.EventNodeCallbackReceived)) != 1 {
		t.Fatalf("NODE_CALLBACK_RECEIVED events after consuming an early callback: want 1, got %v", execEventTypes(events))
	}

	// A re-delivery of the very same early callback must not advance anything a second
	// time, whether it enters through the intake or through the shared resume use case.
	again, err := h.handleCallback(token, "provider-task-early-win", `{"text":"early"}`)
	if err != nil {
		t.Fatalf("re-delivered callback: %v", err)
	}
	if !again.Duplicate {
		t.Fatalf("re-delivered callback outcome: want Duplicate=true, got %+v", again)
	}
	afterEvents := listEvents(h.ctx, t, h.uow, run.ID)
	if len(afterEvents) != len(events) {
		t.Fatalf("events after a re-delivered early callback: want %d unchanged, got %d (%v)", len(events), len(afterEvents), execEventTypes(afterEvents))
	}
}

// TestNodeResume_ReconcilerPathPendingCallbackWithMismatchedToken_NotConsumed proves that
// the rule that an unmatched Pending Callback has no right to advance execution, and the
// Attempt-scoped callback token, hold on the Reconciler's own replay
// path, not only through consumeEarlyCallback's in-process check after a fresh dispatch
// commit. A Pending Callback recorded under Attempt X's own valid credential, but naming
// the external task id a later, unrelated Attempt Y binds to, must not complete Y merely
// because ListConsumableForWaiting would otherwise offer it up: ResumeNode itself (which
// the Reconciler calls verbatim) is where the credential is actually checked.
func TestNodeResume_ReconcilerPathPendingCallbackWithMismatchedToken_NotConsumed(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})

	// Attempt X: fully dispatched under its own external task id, with its own real,
	// verifiable callback token.
	runX := h.createRun()
	dispatchX := h.dispatchAsyncNode(runX, "task-x-owner")

	// Attempt Y: claimed so it has its own callback target, but not dispatched yet --
	// "task-y-target" has no Binding at this point.
	runY := h.createRun()
	h.exec.setExternalTaskID("task-y-target")
	outcomeY := h.claimAsyncNode(runY)

	// A verified delivery, authenticated with X's own valid token, names Y's future
	// external task id before Y's Binding exists. Intake stores it as a Pending Callback
	// keyed by X's credential hash -- this is the scenario a misrouted or replayed valid
	// token produces.
	body := `{"text":"hijacked"}`
	early, err := h.handleCallback(dispatchX.token, "task-y-target", body)
	if err != nil {
		t.Fatalf("early callback naming task-y-target with X's token: %v", err)
	}
	if !early.Pending {
		t.Fatalf("early callback outcome: want Pending, got %+v", early)
	}
	pendingBefore := getPending(h.ctx, t, h.uow, "task-y-target")
	if pendingBefore.CallbackTokenHash != asyncSHA256(dispatchX.token) {
		t.Fatalf("pending callbackTokenHash: want sha256(X's token), got %q", pendingBefore.CallbackTokenHash)
	}

	// Y's own dispatch now commits its Binding for task-y-target. The post-commit
	// consumeEarlyCallback check fires automatically here too and must also refuse this
	// row (same fix, exercised from its other caller); confirm it left WAITING_CALLBACK
	// and an unconsumed row before the Reconciler-style replay below.
	if err := h.svc.Execute(h.ctx, outcomeY); err != nil {
		t.Fatalf("execute Y's dispatch: %v", err)
	}
	if got := getNodeRun(h.ctx, t, h.uow, outcomeY.NodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("NodeRun Y status after its own dispatch commits: want WAITING_CALLBACK, got %s", got)
	}
	if pending := getPending(h.ctx, t, h.uow, "task-y-target"); pending.ConsumedAt != nil {
		t.Fatalf("pending consumedAt after Y's dispatch commit alone: want nil, got %v", pending.ConsumedAt)
	}

	eventsBefore := listEvents(h.ctx, t, h.uow, runY.ID)
	maxSeqBefore := maxEventSeq(eventsBefore)

	// Reconciler path: ListConsumableForWaiting would now offer this row (Binding routes
	// to a DISPATCHED Attempt of a WAITING_CALLBACK NodeRun), so replay it exactly like
	// Reconciler.resumePending does.
	_, err = h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: "task-y-target",
		Payload:        pendingBefore.Payload,
		Source:         domain.CompletionCallback,
		ConsumePending: true,
		PayloadHash:    pendingBefore.PayloadHash,
	})
	var mismatch *service.PendingCallbackCredentialMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("reconciler-path resume of a foreign-credential pending callback: want *PendingCallbackCredentialMismatchError, got %v", err)
	}
	if mismatch.AttemptID != outcomeY.AttemptID {
		t.Fatalf("mismatch error AttemptID: want Y's attempt %s, got %s", outcomeY.AttemptID, mismatch.AttemptID)
	}

	if got := getNodeRun(h.ctx, t, h.uow, outcomeY.NodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("NodeRun Y status after the mismatched resume: want still WAITING_CALLBACK, got %s", got)
	}
	pendingAfter := getPending(h.ctx, t, h.uow, "task-y-target")
	if pendingAfter.ConsumedAt != nil {
		t.Fatalf("pending consumedAt after a rolled-back mismatch: want nil, got %v", pendingAfter.ConsumedAt)
	}
	eventsAfter := listEvents(h.ctx, t, h.uow, runY.ID)
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("events after the mismatched resume: want unchanged (%d), got %d (%v)", len(eventsBefore), len(eventsAfter), execEventTypes(eventsAfter))
	}
	if got := maxEventSeq(eventsAfter); got != maxSeqBefore {
		t.Fatalf("max event seq after the mismatched resume: want unchanged %d, got %d", maxSeqBefore, got)
	}
}

// ---------------------------------------------------------------------------
// Timeout and races
// ---------------------------------------------------------------------------

// TestTimeoutAttempt_DispatchedAttemptPastDeadline_FailsAttemptAndNodeRun covers the
// DISPATCHED-deadline scenario and the wait-timeout edge of the NodeRun state machine: a
// Node that is still waiting when its Attempt deadline passes fails.
func TestTimeoutAttempt_DispatchedAttemptPastDeadline_FailsAttemptAndNodeRun(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-timeout")

	// Before the deadline nothing happens: a timeout sweep must not touch a live Attempt.
	if err := h.svc.TimeoutAttempt(h.ctx, dispatch.attemptID); err != nil {
		t.Fatalf("timeout before the deadline: %v", err)
	}
	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).Status; got != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status before its deadline: want DISPATCHED, got %s", got)
	}

	h.clock.Advance(2 * time.Minute)
	if err := h.svc.TimeoutAttempt(h.ctx, dispatch.attemptID); err != nil {
		t.Fatalf("timeout after the deadline: %v", err)
	}

	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptFailed {
		t.Fatalf("Attempt status after a dispatched timeout: want FAILED, got %s", attempt.Status)
	}
	if attempt.Error == nil || attempt.Error.Code != "TIMEOUT" {
		t.Fatalf("Attempt error after a dispatched timeout: want code TIMEOUT, got %+v", attempt.Error)
	}
	if got := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after a dispatched timeout with no retry left: want FAILED, got %s", got)
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Fatalf("Run status after its only live NodeRun failed: want FAILED, got %s", got)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if len(asyncEventsOfType(events, domain.EventNodeFailed)) != 1 {
		t.Fatalf("NODE_FAILED events after a dispatched timeout: want 1, got %v", execEventTypes(events))
	}
}

// TestTimeoutAttempt_DispatchedKeyedNodeWithAttemptsLeft_FailsWithoutRedispatch pins the
// operative rule for a waiting Attempt that runs out of time: the MVP never automatically
// re-dispatches an external task that has already entered WAITING_CALLBACK, and the
// NodeRun state machine offers WAITING_CALLBACK only the edge to FAILED. The Node here is
// EXTERNAL+KEYED with two attempts to spare, i.e. exactly the case the generic retry
// policy would re-dispatch.
func TestTimeoutAttempt_DispatchedKeyedNodeWithAttemptsLeft_FailsWithoutRedispatch(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-no-redispatch")

	h.clock.Advance(2 * time.Minute)
	if err := h.svc.TimeoutAttempt(h.ctx, dispatch.attemptID); err != nil {
		t.Fatalf("timeout the dispatched attempt: %v", err)
	}

	nodeRun := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after a dispatched timeout with attempts left: want FAILED, got %s", nodeRun.Status)
	}
	if nodeRun.NextAttemptAt != nil {
		t.Fatalf("NodeRun nextAttemptAt after a dispatched timeout: want nil, got %v", nodeRun.NextAttemptAt)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if len(asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeRetrying)) != 0 {
		t.Fatalf("NODE_RETRYING after a dispatched timeout: want none, got %v", execEventTypes(events))
	}
	if len(asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeFailed)) != 1 {
		t.Fatalf("NODE_FAILED after a dispatched timeout: want 1, got %v", execEventTypes(events))
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Fatalf("Run status after its only live NodeRun failed: want FAILED, got %s", got)
	}

	// No second Attempt exists to dispatch: nothing is claimable any more and the Provider
	// was asked for exactly one external task.
	h.clock.Advance(time.Minute)
	next, err := h.svc.Advance(h.ctx, run.ID)
	if err != nil {
		t.Fatalf("advance after a dispatched timeout: %v", err)
	}
	if next.Claimed {
		t.Fatalf("advance after a dispatched timeout: want no claim, got %s attempt %s", next.NodeType, next.AttemptID)
	}
	if got := h.exec.dispatchCount(); got != 1 {
		t.Fatalf("external tasks created for this NodeRun: want 1, got %d", got)
	}
}

// TestNodeDispatch_KeyedRetry_ReusesIdenticalIdempotencyKey pins the EXTERNAL+KEYED
// side-effect rule at the Provider boundary: when a keyed external call enters retry,
// every Provider call carries byte-for-byte the same idempotency key -- the key changing
// across Attempts must fail this test. The retry is produced the only way the NodeRun
// state machine allows one: the first Attempt's deadline expires while it is still
// STARTED (its dispatch is in flight, not yet WAITING_CALLBACK), TimeoutAttempt fails it,
// and EXTERNAL+KEYED is what makes that failure retryable at all. The expired Attempt's
// Provider call then lands late -- the Provider has received the dispatch and its key,
// while the stale commit writes nothing (dispatchNode's lostRace path) -- and the second
// Attempt dispatches for real once the backoff elapses. The unit test
// TestExecute_KeyedExternalNode_PassesNodeRunIDAsIdempotencyKey only pins how one key is
// constructed; this proves reuse across two real dispatches of one NodeRun.
func TestNodeDispatch_KeyedRetry_ReusesIdenticalIdempotencyKey(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	run := h.createRun()

	// First dispatch: claimed, then expired while STARTED. The timeout schedules a second
	// Attempt instead of failing the NodeRun.
	h.exec.setExternalTaskID("provider-task-keyed-first")
	first := h.claimAsyncNode(run)
	h.clock.Advance(2 * time.Minute)
	if err := h.svc.TimeoutAttempt(h.ctx, first.AttemptID); err != nil {
		t.Fatalf("timeout the started attempt: %v", err)
	}
	nodeRun := getNodeRun(h.ctx, t, h.uow, first.NodeRunID)
	if nodeRun.Status != domain.NodeRunRunning || nodeRun.NextAttemptAt == nil {
		t.Fatalf("NodeRun after a keyed STARTED timeout: want RUNNING with a scheduled retry, got %s / %v", nodeRun.Status, nodeRun.NextAttemptAt)
	}

	// The in-flight dispatch of the expired Attempt reaches the Provider (call #1, key and
	// all); its stale commit is refused and writes nothing.
	if err := h.svc.Execute(h.ctx, first); err != nil {
		t.Fatalf("late dispatch of the expired attempt: %v", err)
	}
	if got := getAttempt(h.ctx, t, h.uow, first.AttemptID).Status; got != domain.NodeAttemptFailed {
		t.Fatalf("expired Attempt after its late dispatch commit: want still FAILED, got %s", got)
	}

	// Second dispatch: the backoff elapses and the retry Attempt dispatches (call #2).
	h.clock.Advance(time.Minute)
	second := h.dispatchAsyncNode(run, "provider-task-keyed-second")
	if second.nodeRunID != first.NodeRunID {
		t.Fatalf("retry NodeRun: want the same NodeRun %s, got %s", first.NodeRunID, second.nodeRunID)
	}
	if second.attemptID == first.AttemptID {
		t.Fatal("retry reused the original Attempt id; a retry must create a new Attempt")
	}

	firstAttempt := getAttempt(h.ctx, t, h.uow, first.AttemptID)
	secondAttempt := getAttempt(h.ctx, t, h.uow, second.attemptID)
	if firstAttempt.Status != domain.NodeAttemptFailed {
		t.Fatalf("old Attempt after the retry dispatched: want preserved as FAILED, got %s", firstAttempt.Status)
	}
	if secondAttempt.AttemptNo != firstAttempt.AttemptNo+1 {
		t.Fatalf("attempt numbers across the retry: want %d then %d, got %d then %d",
			firstAttempt.AttemptNo, firstAttempt.AttemptNo+1, firstAttempt.AttemptNo, secondAttempt.AttemptNo)
	}

	// The keyed-retry assertion itself: exactly two Provider calls, one identical key.
	keys := h.exec.idempotencyKeys()
	if len(keys) != 2 {
		t.Fatalf("Provider dispatch calls for this NodeRun: want exactly 2, got %d (%q)", len(keys), keys)
	}
	if keys[0] == "" {
		t.Fatal("idempotency key of an EXTERNAL+KEYED dispatch: want non-empty, got empty")
	}
	if keys[0] != keys[1] {
		t.Fatalf("idempotency key changed across the retry: first call %q, second call %q", keys[0], keys[1])
	}
}

// TestNodeResume_ConcurrentCallbackAndTimeout_OnlyOneCommits is the race in which
// timeout, callback and Provider Poll compete for the same completion right and only one
// commits. Both goroutines start from the same barrier and hit the same Run lock; the
// conditional UPDATE, not the arrival order, decides the winner.
func TestNodeResume_ConcurrentCallbackAndTimeout_OnlyOneCommits(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-race")
	h.clock.Advance(2 * time.Minute)

	// The callback enters through HandleCallback, credential and all: the token outlives the
	// Attempt deadline by the Pending Callback TTL, so a delivery that arrives while the
	// timeout is becoming due is verifiable and contends for the completion right instead of
	// being refused at the door.
	start := make(chan struct{})
	var wg sync.WaitGroup
	var callbackErr, timeoutErr error
	var callbackOutcome service.CallbackOutcome

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		callbackOutcome, callbackErr = h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"generated"}`)
	}()
	go func() {
		defer wg.Done()
		<-start
		timeoutErr = h.svc.TimeoutAttempt(h.ctx, dispatch.attemptID)
	}()
	close(start)
	wg.Wait()

	if callbackErr != nil {
		t.Fatalf("concurrent callback: %v", callbackErr)
	}
	if timeoutErr != nil {
		t.Fatalf("concurrent timeout: %v", timeoutErr)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	completed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCompleted)
	failed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeFailed)
	if len(completed)+len(failed) != 1 {
		t.Fatalf("terminal Node events after the callback/timeout race: want exactly 1, got %d completed and %d failed (%v)",
			len(completed), len(failed), execEventTypes(events))
	}

	nodeRun := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID)
	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if len(completed) == 1 {
		if nodeRun.Status != domain.NodeRunSucceeded || attempt.Status != domain.NodeAttemptSucceeded {
			t.Fatalf("callback won the race: want NodeRun and Attempt SUCCEEDED, got %s / %s", nodeRun.Status, attempt.Status)
		}
		if callbackOutcome.Duplicate {
			t.Fatal("callback won the race but reported itself a duplicate")
		}
	} else {
		if nodeRun.Status != domain.NodeRunFailed || attempt.Status != domain.NodeAttemptFailed {
			t.Fatalf("timeout won the race: want NodeRun and Attempt FAILED, got %s / %s", nodeRun.Status, attempt.Status)
		}
		if !callbackOutcome.Duplicate {
			t.Fatal("timeout won the race but the callback did not report itself a duplicate")
		}
		if len(asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCallbackReceived)) != 0 {
			t.Fatalf("losing callback wrote NODE_CALLBACK_RECEIVED: %v", execEventTypes(events))
		}
	}
}

// TestNodeResume_AfterRestart_ResumesOriginalAttemptFromDatabase covers a restart during
// WAITING_CALLBACK: the waiting Attempt, its Binding and its credential live in
// PostgreSQL, so a brand-new service instance -- fresh registry, fresh plan cache, fresh
// Executor -- resumes the same Attempt.
func TestNodeResume_AfterRestart_ResumesOriginalAttemptFromDatabase(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-restart")

	restarted := h.newService(
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		&asyncFakeExecutor{externalTaskID: "provider-task-restart"})

	outcome, err := restarted.HandleCallback(h.ctx, service.HandleCallback{
		Token:          dispatch.token,
		ExternalTaskID: dispatch.externalTaskID,
		Payload:        json.RawMessage(`{"text":"after restart"}`),
	})
	if err != nil {
		t.Fatalf("callback after restart: %v", err)
	}
	if !outcome.Accepted || outcome.Duplicate {
		t.Fatalf("callback after restart: want accepted and not duplicate, got %+v", outcome)
	}
	if outcome.RunID != run.ID || outcome.NodeRunID != dispatch.nodeRunID {
		t.Fatalf("callback after restart routed to run=%s node_run=%s, want run=%s node_run=%s",
			outcome.RunID, outcome.NodeRunID, run.ID, dispatch.nodeRunID)
	}

	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptSucceeded {
		t.Fatalf("original Attempt after a restart resume: want SUCCEEDED, got %s", attempt.Status)
	}

	// The restarted process drives the Run to completion from persisted state alone.
	for i := 0; i < 10; i++ {
		next, err := restarted.Advance(h.ctx, run.ID)
		if err != nil {
			t.Fatalf("advance after restart: %v", err)
		}
		if !next.Claimed {
			break
		}
		if err := restarted.Execute(h.ctx, next); err != nil {
			t.Fatalf("execute after restart: %v", err)
		}
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("Run status after a restart resume and drain: want COMPLETED, got %s", got)
	}
}

// TestNodeResume_ProviderFailureWithRetriesLeft_FailsWithoutRedispatch pins the rule that a
// definite Provider failure marks the Attempt and NodeRun FAILED, and the MVP never
// automatically re-dispatches an external task that has already entered WAITING_CALLBACK.
// The Node here is EXTERNAL+KEYED with attempts to spare, so
// the generic retry policy would happily re-dispatch it; a reported Provider failure of a
// task that is already waiting must still be terminal.
func TestNodeResume_ProviderFailureWithRetriesLeft_FailsWithoutRedispatch(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-failed")

	outcome, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"status":"FAILED"}`)
	if err != nil {
		t.Fatalf("provider failure callback: %v", err)
	}
	if outcome.Duplicate {
		t.Fatalf("provider failure callback outcome: want the failure to be taken, got %+v", outcome)
	}

	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).Status; got != domain.NodeAttemptFailed {
		t.Fatalf("Attempt status after a reported Provider failure: want FAILED, got %s", got)
	}
	nodeRun := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after a reported Provider failure: want FAILED with no re-dispatch, got %s", nodeRun.Status)
	}
	if nodeRun.NextAttemptAt != nil {
		t.Fatalf("NodeRun nextAttemptAt after a reported Provider failure: want nil, got %v", nodeRun.NextAttemptAt)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if len(asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeRetrying)) != 0 {
		t.Fatalf("NODE_RETRYING after a reported Provider failure: want none, got %v", execEventTypes(events))
	}
	if len(asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeFailed)) != 1 {
		t.Fatalf("NODE_FAILED after a reported Provider failure: want 1, got %v", execEventTypes(events))
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Fatalf("Run status after its only live NodeRun failed: want FAILED, got %s", got)
	}
}
