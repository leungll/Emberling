//go:build integration

// Dispatch-boundary integration tests (the external-dispatch boundary). Everything proven
// here is about the
// one window Emberling cannot put inside a transaction: the Provider call between the
// claim COMMIT and the dispatch COMMIT. The three failure shapes of that window are a
// dispatch whose response carried no external task identity, a response whose external
// task id is already bound to another Attempt, and an Adapter error that classifies the
// remote result as uncertain. Each one ends in a Postgres transaction whose Attempt,
// NodeRun, Run aggregate and Event must agree, so a mock repository cannot stand in for
// PostgreSQL here (CLAUDE.md testing standard).
//
// The fourth test covers the Run aggregate with two NodeRuns waiting at once.
//
// Helpers reuse the `async` harness of service_async_test.go; names added here stay
// prefixed `async` for the same reason.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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

// ---------------------------------------------------------------------------
// Additional fakes and helpers
// ---------------------------------------------------------------------------

// setOmitTaskID makes the fake Provider report DISPATCHED with no ExternalTask at all,
// i.e. the dispatch response that identifies nothing.
func (e *asyncFakeExecutor) setOmitTaskID(v bool) {
	e.mu.Lock()
	e.omitTaskID = v
	e.mu.Unlock()
}

// asyncClassifiedError is the Adapter-style error of the external-dispatch boundary: it
// reports whether the
// external call's result at the Provider is unknown. It mirrors execClassifiedError in
// internal/service/execution_test.go, which proves the same classification against fakes.
type asyncClassifiedError struct{ uncertain bool }

func (e *asyncClassifiedError) Error() string   { return "async_dispatch: provider call failed" }
func (e *asyncClassifiedError) Uncertain() bool { return e.uncertain }

func asyncAttempts(ctx context.Context, t *testing.T, uow store.UnitOfWork, nodeRunID string) []domain.NodeAttempt {
	t.Helper()
	var attempts []domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempts, err = tx.NodeAttempts().ListByNodeRun(ctx, nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("list attempts of %s: %v", nodeRunID, err)
	}
	return attempts
}

// asyncBindingRowCount counts Callback Binding rows for an external task id. The unique
// index behind the conflict is the UNIQUE constraint on callback_bindings.external_task_id
// (migrations/00001_initial.sql), named callback_bindings_external_task_id_key by
// PostgreSQL; the repository has no count method, so this asserts on the table directly.
func asyncBindingRowCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool, externalTaskID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM callback_bindings WHERE external_task_id = $1`, externalTaskID,
	).Scan(&count); err != nil {
		t.Fatalf("count callback bindings for %s: %v", externalTaskID, err)
	}
	return count
}

// asyncBindingsOfAttempt reads the Bindings routing to one Attempt, which is how "no
// Binding was created" is observed from the Runtime's own routing contract.
func asyncBindingsOfAttempt(ctx context.Context, t *testing.T, uow store.UnitOfWork, attemptID string) []domain.CallbackBinding {
	t.Helper()
	var bindings []domain.CallbackBinding
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		bindings, err = tx.CallbackBindings().ListByTargets(ctx, domain.CallbackTargetNodeAttempt, []string{attemptID})
		return err
	}); err != nil {
		t.Fatalf("list bindings of attempt %s: %v", attemptID, err)
	}
	return bindings
}

// ---------------------------------------------------------------------------
// 1. Dispatch accepted, but nothing identifies the external task
// ---------------------------------------------------------------------------

// TestNodeDispatch_ProviderAcceptedWithoutExternalTaskID_FailsAttemptAsUncertain covers a
// Provider that accepted the task while the dispatch response was lost, so the NodeRun
// fails at the known recovery boundary, and the external-dispatch case in which the
// Provider accepted the task but no external_task_id was saved locally. Emberling cannot
// know whether the Provider accepted the task, so the Attempt fails as uncertain with
// code DISPATCH_WITHOUT_EXTERNAL_TASK; for this EXTERNAL+UNKNOWN node that classification
// -- not an exhausted attempt budget, the policy here still has two attempts left -- is
// what forbids a re-dispatch. No Binding exists, so no callback could ever route home
// either.
func TestNodeDispatch_ProviderAcceptedWithoutExternalTaskID_FailsAttemptAsUncertain(t *testing.T) {
	cases := []struct {
		name string
		// arrange makes the fake Provider return a dispatch that identifies nothing.
		arrange func(h *asyncHarness)
		// externalTaskID is the id the test then proves nothing was bound to.
		externalTaskID string
	}{
		{
			name:           "no external task in the dispatch response",
			arrange:        func(h *asyncHarness) { h.exec.setOmitTaskID(true) },
			externalTaskID: "",
		},
		{
			name:           "provider id present but empty external task id",
			arrange:        func(h *asyncHarness) { h.exec.setExternalTaskID("") },
			externalTaskID: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAsyncHarness(t,
				domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
				domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed})
			run := h.createRun()
			tc.arrange(h)

			outcome := h.claimAsyncNode(run)
			if err := h.svc.Execute(h.ctx, outcome); err != nil {
				t.Fatalf("execute a dispatch without an external task id: %v", err)
			}

			attempt := getAttempt(h.ctx, t, h.uow, outcome.AttemptID)
			if attempt.Status != domain.NodeAttemptFailed {
				t.Fatalf("Attempt status after a dispatch without an external task id: want FAILED, got %s", attempt.Status)
			}
			if attempt.Error == nil || attempt.Error.Code != "DISPATCH_WITHOUT_EXTERNAL_TASK" {
				t.Fatalf("Attempt error code: want DISPATCH_WITHOUT_EXTERNAL_TASK, got %+v", attempt.Error)
			}
			if attempt.DispatchedAt != nil {
				t.Fatalf("Attempt dispatchedAt for a dispatch that was never bound: want nil, got %v", attempt.DispatchedAt)
			}

			// No Callback Binding: neither for the attempt, nor for the empty id the
			// Provider (did not) return.
			if got := asyncBindingsOfAttempt(h.ctx, t, h.uow, outcome.AttemptID); len(got) != 0 {
				t.Fatalf("Callback Bindings routing to the failed Attempt: want none, got %d", len(got))
			}
			if got := asyncBindingRowCount(h.ctx, t, h.pool, tc.externalTaskID); got != 0 {
				t.Fatalf("callback_bindings rows for external task id %q: want 0, got %d", tc.externalTaskID, got)
			}

			// `uncertain` is not a persisted column (node_attempts has none): it is an
			// input to runtime.DecideRetry, so the only way PostgreSQL can show it is the
			// decision it produced. Here it is true and the SideEffectPolicy is
			// EXTERNAL+UNKNOWN, so the retry is refused even though MaxAttempts=3 leaves
			// room, and NodeRun and Run fail in the same transaction as NODE_FAILED.
			nodeRun := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
			if nodeRun.Status != domain.NodeRunFailed {
				t.Fatalf("NodeRun status after an uncertain dispatch failure: want FAILED, got %s", nodeRun.Status)
			}
			if nodeRun.NextAttemptAt != nil {
				t.Fatalf("NodeRun nextAttemptAt after an uncertain EXTERNAL+UNKNOWN dispatch failure: want nil, got %v", nodeRun.NextAttemptAt)
			}
			if nodeRun.Error == nil || nodeRun.Error.Code != "DISPATCH_WITHOUT_EXTERNAL_TASK" {
				t.Fatalf("NodeRun error code: want DISPATCH_WITHOUT_EXTERNAL_TASK, got %+v", nodeRun.Error)
			}
			if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
				t.Fatalf("Run status after its only live NodeRun failed: want FAILED, got %s", got)
			}

			events := listEvents(h.ctx, t, h.uow, run.ID)
			if got := asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeFailed); len(got) != 1 {
				t.Fatalf("NODE_FAILED events for the failed dispatch: want 1, got %d (%v)", len(got), execEventTypes(events))
			}
			if got := asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeRetrying); len(got) != 0 {
				t.Fatalf("NODE_RETRYING after an uncertain EXTERNAL+UNKNOWN dispatch failure: want none, got %v", execEventTypes(events))
			}
			if got := asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeDispatched); len(got) != 0 {
				t.Fatalf("NODE_DISPATCHED for a dispatch that bound nothing: want none, got %v", execEventTypes(events))
			}

			// Nothing is claimable any more and exactly one Attempt exists: the external
			// task, if the Provider did accept one, is never re-created.
			h.clock.Advance(5 * time.Minute)
			next, err := h.svc.Advance(h.ctx, run.ID)
			if err != nil {
				t.Fatalf("advance after an uncertain dispatch failure: %v", err)
			}
			if next.Claimed {
				t.Fatalf("advance after an uncertain dispatch failure: want no claim, got %s attempt %s", next.NodeType, next.AttemptID)
			}
			if got := asyncAttempts(h.ctx, t, h.uow, outcome.NodeRunID); len(got) != 1 {
				t.Fatalf("Attempts of the dispatched NodeRun: want 1 (no re-dispatch), got %d", len(got))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. External task id already bound to another Attempt
// ---------------------------------------------------------------------------

// TestNodeDispatch_ExternalTaskIDAlreadyBoundToAnotherAttempt_FailsDefinitely proves the
// CALLBACK_BINDING_CONFLICT path of dispatchNode: a dispatch whose external task id is
// already routed to another Attempt has no route home, so its transaction rolls back
// (errDispatchBindingConflict) and the Attempt fails *definitely* -- uncertain = false,
// because a rejected route is not an unknown Provider outcome. The observable difference
// between definite and uncertain is the retry: this EXTERNAL+UNKNOWN node may take its
// second Attempt only because the failure is definite. The winning Binding
// is untouched and still routes the external task id to the first Run's Attempt; the
// UNIQUE constraint on callback_bindings.external_task_id
// (callback_bindings_external_task_id_key) is what makes the second insert fail at all.
func TestNodeDispatch_ExternalTaskIDAlreadyBoundToAnotherAttempt_FailsDefinitely(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		domain.ExecutionPolicy{MaxAttempts: 2, Backoff: domain.BackoffFixed})

	const sharedTaskID = "provider-task-shared"
	firstRun := h.createRun()
	first := h.dispatchAsyncNode(firstRun, sharedTaskID)
	if got := getNodeRun(h.ctx, t, h.uow, first.nodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("first NodeRun after its dispatch committed: want WAITING_CALLBACK, got %s", got)
	}

	// A second Run dispatches and the Provider hands back the same external task id.
	secondRun := h.createRun()
	second := h.dispatchAsyncNode(secondRun, sharedTaskID)

	conflicted := getAttempt(h.ctx, t, h.uow, second.attemptID)
	if conflicted.Status != domain.NodeAttemptFailed {
		t.Fatalf("second Attempt after a binding conflict: want FAILED, got %s", conflicted.Status)
	}
	if conflicted.Error == nil || conflicted.Error.Code != "CALLBACK_BINDING_CONFLICT" {
		t.Fatalf("second Attempt error code: want CALLBACK_BINDING_CONFLICT, got %+v", conflicted.Error)
	}
	if conflicted.DispatchedAt != nil {
		t.Fatalf("second Attempt dispatchedAt after its transaction rolled back: want nil, got %v", conflicted.DispatchedAt)
	}
	if got := asyncBindingsOfAttempt(h.ctx, t, h.uow, second.attemptID); len(got) != 0 {
		t.Fatalf("Callback Bindings routing to the conflicted Attempt: want none, got %d", len(got))
	}
	if got := asyncBindingRowCount(h.ctx, t, h.pool, sharedTaskID); got != 1 {
		t.Fatalf("callback_bindings rows for %q after the conflict: want 1, got %d", sharedTaskID, got)
	}
	binding := getBinding(h.ctx, t, h.uow, sharedTaskID)
	if binding.TargetID != first.attemptID {
		t.Fatalf("Callback Binding target after the conflict: want the first Attempt %s, got %s", first.attemptID, binding.TargetID)
	}

	// Definite failure with an attempt left: the NodeRun stays RUNNING with a scheduled
	// retry. An uncertain failure of this EXTERNAL+UNKNOWN node could not have produced
	// this, which is how "uncertain = false" is observable.
	retrying := getNodeRun(h.ctx, t, h.uow, second.nodeRunID)
	if retrying.Status != domain.NodeRunRunning {
		t.Fatalf("second NodeRun after a definite conflict with an attempt left: want RUNNING, got %s", retrying.Status)
	}
	if retrying.NextAttemptAt == nil {
		t.Fatalf("second NodeRun nextAttemptAt after a definite conflict with an attempt left: want a timestamp, got nil")
	}
	secondEvents := listEvents(h.ctx, t, h.uow, secondRun.ID)
	if got := asyncNodeEventsOfType(secondEvents, second.nodeRunID, domain.EventNodeRetrying); len(got) != 1 {
		t.Fatalf("NODE_RETRYING after a definite binding conflict: want 1, got %d (%v)", len(got), execEventTypes(secondEvents))
	}

	// The retry hits the same conflict and exhausts MaxAttempts: now the NodeRun, the Run
	// aggregate and NODE_FAILED commit together.
	h.clock.Advance(2 * time.Minute)
	retryOutcome := h.claimAsyncNode(secondRun)
	if retryOutcome.AttemptNo != 2 {
		t.Fatalf("retry Attempt number: want 2, got %d", retryOutcome.AttemptNo)
	}
	if err := h.svc.Execute(h.ctx, retryOutcome); err != nil {
		t.Fatalf("execute the retried dispatch: %v", err)
	}

	failedRun := getNodeRun(h.ctx, t, h.uow, second.nodeRunID)
	if failedRun.Status != domain.NodeRunFailed {
		t.Fatalf("second NodeRun after the retry conflicted again: want FAILED, got %s", failedRun.Status)
	}
	if failedRun.Error == nil || failedRun.Error.Code != "CALLBACK_BINDING_CONFLICT" {
		t.Fatalf("second NodeRun error code: want CALLBACK_BINDING_CONFLICT, got %+v", failedRun.Error)
	}
	secondEvents = listEvents(h.ctx, t, h.uow, secondRun.ID)
	if got := asyncNodeEventsOfType(secondEvents, second.nodeRunID, domain.EventNodeFailed); len(got) != 1 {
		t.Fatalf("NODE_FAILED after the exhausted binding conflict: want 1, got %d (%v)", len(got), execEventTypes(secondEvents))
	}
	if got := getRun(h.ctx, t, h.uow, secondRun.ID).Status; got != domain.RunFailed {
		t.Fatalf("second Run status after its async NodeRun failed: want FAILED, got %s", got)
	}
	if got := asyncBindingRowCount(h.ctx, t, h.pool, sharedTaskID); got != 1 {
		t.Fatalf("callback_bindings rows for %q after both conflicts: want 1, got %d", sharedTaskID, got)
	}

	// The first Run never noticed: it is still waiting, and the external task id still
	// routes its callback home.
	if got := getRun(h.ctx, t, h.uow, firstRun.ID).Status; got != domain.RunPaused {
		t.Fatalf("first Run status after the second Run's conflict: want PAUSED, got %s", got)
	}
	outcome, err := h.handleCallback(first.token, sharedTaskID, `{"text":"generated"}`)
	if err != nil {
		t.Fatalf("callback for the surviving binding: %v", err)
	}
	if !outcome.Accepted || outcome.Duplicate {
		t.Fatalf("callback for the surviving binding: want accepted and not duplicate, got %+v", outcome)
	}
	if outcome.RunID != firstRun.ID || outcome.NodeRunID != first.nodeRunID {
		t.Fatalf("callback routed to run=%s node_run=%s, want the first Run %s / %s",
			outcome.RunID, outcome.NodeRunID, firstRun.ID, first.nodeRunID)
	}
	if got := getAttempt(h.ctx, t, h.uow, first.attemptID).Status; got != domain.NodeAttemptSucceeded {
		t.Fatalf("first Attempt after its callback: want SUCCEEDED, got %s", got)
	}
}

// ---------------------------------------------------------------------------
// 3. Adapter-classified dispatch errors
// ---------------------------------------------------------------------------

// TestNodeDispatch_AdapterReportsUncertainError_FailsAttemptAsUncertainInPostgres is the
// PostgreSQL counterpart of TestExecute_DispatchErrorReportingUncertain/Definite in
// internal/service/execution_test.go: the same two Adapter classifications, but with the
// real transaction, the real retry scheduling and the real Event log. The dispatch boundary
// makes
// the classification -- not the Go error type -- decide whether a retry is permitted at
// all, so the two halves differ in exactly one bit: Uncertain() true or false.
func TestNodeDispatch_AdapterReportsUncertainError_FailsAttemptAsUncertainInPostgres(t *testing.T) {
	externalUnknown := domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}
	policy := domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed}

	// --- uncertain: the Provider may already hold the task, so nothing may be repeated.
	h := newAsyncHarness(t, externalUnknown, policy)
	run := h.createRun()
	h.exec.setDispatchErr(&asyncClassifiedError{uncertain: true})

	outcome := h.claimAsyncNode(run)
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		t.Fatalf("execute an uncertain dispatch failure: %v", err)
	}

	attempt := getAttempt(h.ctx, t, h.uow, outcome.AttemptID)
	if attempt.Status != domain.NodeAttemptFailed {
		t.Fatalf("Attempt status after an uncertain dispatch error: want FAILED, got %s", attempt.Status)
	}
	if attempt.Error == nil || attempt.Error.Code != "EXECUTOR_ERROR" {
		t.Fatalf("Attempt error code after an uncertain dispatch error: want EXECUTOR_ERROR, got %+v", attempt.Error)
	}
	nodeRun := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Fatalf("NodeRun status after an uncertain EXTERNAL+UNKNOWN dispatch error: want FAILED, got %s", nodeRun.Status)
	}
	if nodeRun.NextAttemptAt != nil {
		t.Fatalf("NodeRun nextAttemptAt after an uncertain EXTERNAL+UNKNOWN dispatch error: want nil, got %v", nodeRun.NextAttemptAt)
	}
	if got := asyncBindingsOfAttempt(h.ctx, t, h.uow, outcome.AttemptID); len(got) != 0 {
		t.Fatalf("Callback Bindings after a dispatch that never reached the Provider: want none, got %d", len(got))
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if got := asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeFailed); len(got) != 1 {
		t.Fatalf("NODE_FAILED after an uncertain dispatch error: want 1, got %d (%v)", len(got), execEventTypes(events))
	}
	if got := asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeRetrying); len(got) != 0 {
		t.Fatalf("NODE_RETRYING after an uncertain EXTERNAL+UNKNOWN dispatch error: want none, got %v", execEventTypes(events))
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Fatalf("Run status after its only live NodeRun failed: want FAILED, got %s", got)
	}
	h.clock.Advance(5 * time.Minute)
	next, err := h.svc.Advance(h.ctx, run.ID)
	if err != nil {
		t.Fatalf("advance after an uncertain dispatch error: %v", err)
	}
	if next.Claimed {
		t.Fatalf("advance after an uncertain dispatch error: want no claim, got attempt %s", next.AttemptID)
	}

	// --- definite: the Provider certainly did not accept the task, so the retry policy
	// applies normally and a second Attempt is scheduled and then created.
	h2 := newAsyncHarness(t, externalUnknown, policy)
	run2 := h2.createRun()
	h2.exec.setDispatchErr(&asyncClassifiedError{uncertain: false})

	outcome2 := h2.claimAsyncNode(run2)
	if err := h2.svc.Execute(h2.ctx, outcome2); err != nil {
		t.Fatalf("execute a definite dispatch failure: %v", err)
	}

	retrying := getNodeRun(h2.ctx, t, h2.uow, outcome2.NodeRunID)
	if retrying.Status != domain.NodeRunRunning {
		t.Fatalf("NodeRun status after a definite, retryable dispatch error: want RUNNING, got %s", retrying.Status)
	}
	if retrying.NextAttemptAt == nil {
		t.Fatalf("NodeRun nextAttemptAt after a definite, retryable dispatch error: want a timestamp, got nil")
	}
	events2 := listEvents(h2.ctx, t, h2.uow, run2.ID)
	if got := asyncNodeEventsOfType(events2, outcome2.NodeRunID, domain.EventNodeRetrying); len(got) != 1 {
		t.Fatalf("NODE_RETRYING after a definite dispatch error with attempts left: want 1, got %d (%v)", len(got), execEventTypes(events2))
	}
	if got := asyncNodeEventsOfType(events2, outcome2.NodeRunID, domain.EventNodeFailed); len(got) != 0 {
		t.Fatalf("NODE_FAILED after a definite dispatch error with attempts left: want none, got %v", execEventTypes(events2))
	}

	// After the backoff the retry really exists as a new Attempt, and once the Provider
	// answers it dispatches normally: the definite classification cost nothing but a wait.
	h2.clock.Advance(2 * time.Minute)
	h2.exec.setDispatchErr(nil)
	h2.exec.setExternalTaskID("provider-task-after-definite-failure")
	retryOutcome := h2.claimAsyncNode(run2)
	if retryOutcome.AttemptNo != 2 {
		t.Fatalf("retry Attempt number after a definite dispatch error: want 2, got %d", retryOutcome.AttemptNo)
	}
	if retryOutcome.AttemptID == outcome2.AttemptID {
		t.Fatalf("retry reused Attempt %s instead of creating a new one", retryOutcome.AttemptID)
	}
	if err := h2.svc.Execute(h2.ctx, retryOutcome); err != nil {
		t.Fatalf("execute the retried dispatch: %v", err)
	}
	if got := getAttempt(h2.ctx, t, h2.uow, retryOutcome.AttemptID).Status; got != domain.NodeAttemptDispatched {
		t.Fatalf("retried Attempt status: want DISPATCHED, got %s", got)
	}
	if got := len(asyncAttempts(h2.ctx, t, h2.uow, outcome2.NodeRunID)); got != 2 {
		t.Fatalf("Attempts of the retried NodeRun: want 2 (the old Attempt is preserved), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// 4. Two NodeRuns waiting at once
// ---------------------------------------------------------------------------

const asyncJoinNodeType = "async_join"

// asyncJoinExecutor is a deterministic synchronous Node with two required text inputs. It
// exists so a Definition can fan out into two independent async Nodes and still have the
// single Output Node the compiler requires: a required input port accepts exactly one
// incoming edge (runtime/graph.go AMBIGUOUS_INPUT), so the two branches must rejoin
// through a node that declares two ports.
type asyncJoinExecutor struct{}

func (asyncJoinExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (asyncJoinExecutor) Execute(_ context.Context, in registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	var joined []string
	for _, port := range []string{"left", "right"} {
		raw, present := in.Port(port)
		if !present {
			return registry.NodeResult{}, fmt.Errorf("async_join: required input port %q is missing", port)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return registry.NodeResult{}, fmt.Errorf("async_join: input port %q is not a JSON string: %w", port, err)
		}
		joined = append(joined, value)
	}
	return registry.CompletedResult(map[string]json.RawMessage{
		"text": json.RawMessage(strconv.Quote(joined[0] + "+" + joined[1])),
	}), nil
}

func asyncJoinRegistration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          asyncJoinNodeType,
			DisplayName:   "Async Join",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs: []domain.PortMetadata{
				{Name: "left", DataType: domain.PortTypeText, Required: true},
				{Name: "right", DataType: domain.PortTypeText, Required: true},
			},
			Outputs:      []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText}},
			ConfigSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: asyncJoinExecutor{}},
	}
}

// asyncFanOutHarness is the asyncHarness of service_async_test.go with one extra node type
// and a Definition that fans out into two async Nodes. It is kept separate rather than
// generalising that harness, whose single-async-node Definition every other test relies on.
type asyncFanOutHarness struct {
	t   *testing.T
	ctx context.Context

	pool  *pgxpool.Pool
	uow   store.UnitOfWork
	clock *execClock
	exec  *asyncFakeExecutor
	svc   *service.ExecutionService
	def   domain.Definition
}

func newAsyncFanOutHarness(t *testing.T) *asyncFanOutHarness {
	t.Helper()
	pool := testdb.Open(t)
	h := &asyncFanOutHarness{t: t, ctx: context.Background(), pool: pool, uow: postgres.NewUnitOfWork(pool)}
	h.clock = newExecClock(fixtureTime)
	h.exec = &asyncFakeExecutor{}

	side := domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}
	nodes := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(),
		textoutput.Registration(),
		asyncRegistration(h.exec, side),
		asyncJoinRegistration(),
	} {
		if err := nodes.Register(reg); err != nil {
			t.Fatalf("register node type: %v", err)
		}
	}
	compiler := runtime.NewCompiler(nodes, h.clock)
	h.svc = service.NewExecutionService(service.Deps{
		UoW:      h.uow,
		Nodes:    nodes,
		Compiler: compiler,
		Clock:    h.clock,
		IDs:      newExecForcedIDs(),
		Callback: service.CallbackConfig{
			BaseURL:       asyncBaseURL,
			SigningSecret: []byte(asyncSigningSecret),
			PendingTTL:    time.Hour,
		},
	})

	def := domain.Definition{
		WorkflowID:  "wf_fanout_" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Version:     1,
		Name:        "Two waiting async nodes",
		Description: "text_input -> (async_dispatch x2) -> async_join -> text_output",
		Nodes: []domain.Node{
			{ID: "input", Type: "text_input", Name: "Brief", Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			{ID: "left", Type: asyncNodeType, Name: "Left dispatch", Config: json.RawMessage(`{}`)},
			{ID: "right", Type: asyncNodeType, Name: "Right dispatch", Config: json.RawMessage(`{}`)},
			{ID: "join", Type: asyncJoinNodeType, Name: "Join", Config: json.RawMessage(`{}`)},
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "input", SourceHandle: "text", Target: "left", TargetHandle: "text"},
			{ID: "e2", Source: "input", SourceHandle: "text", Target: "right", TargetHandle: "text"},
			{ID: "e3", Source: "left", SourceHandle: "text", Target: "join", TargetHandle: "left"},
			{ID: "e4", Source: "right", SourceHandle: "text", Target: "join", TargetHandle: "right"},
			{ID: "e5", Source: "join", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: fixtureTime,
	}
	plan, err := compiler.Compile(h.ctx, def)
	if err != nil {
		t.Fatalf("compile fan-out definition: %v", err)
	}
	def.RunInputSchema = plan.RunInputSchema
	def.Validation = plan.Validation
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	}); err != nil {
		t.Fatalf("save fan-out definition: %v", err)
	}
	h.def = def
	return h
}

// dispatchAll advances the Run until nothing can be claimed, dispatching every async Node
// it meets with its own external task id. Only one Executor call is in flight at a time,
// which is the serial execution the test requires.
func (h *asyncFanOutHarness) dispatchAll(run domain.Run) map[string]asyncDispatch {
	h.t.Helper()
	dispatches := make(map[string]asyncDispatch, 2)
	for i := 0; i < 10; i++ {
		outcome, err := h.svc.Advance(h.ctx, run.ID)
		if err != nil {
			h.t.Fatalf("advance: %v", err)
		}
		if !outcome.Claimed {
			return dispatches
		}
		nodeRun := getNodeRun(h.ctx, h.t, h.uow, outcome.NodeRunID)
		if outcome.NodeType == asyncNodeType {
			if outcome.Input.Callback == nil || outcome.Input.Callback.Token == "" {
				h.t.Fatal("claimed async Attempt carries no callback credential")
			}
			externalTaskID := "provider-task-" + nodeRun.NodeID
			h.exec.setExternalTaskID(externalTaskID)
			dispatches[nodeRun.NodeID] = asyncDispatch{
				run: run, nodeRunID: outcome.NodeRunID, attemptID: outcome.AttemptID,
				token: outcome.Input.Callback.Token, externalTaskID: externalTaskID,
			}
		}
		if err := h.svc.Execute(h.ctx, outcome); err != nil {
			h.t.Fatalf("execute %s: %v", outcome.NodeType, err)
		}
	}
	h.t.Fatal("advance: the Run never stopped producing claimable work")
	return nil
}

// TestRunAggregate_TwoWaitingCallbackNodeRuns_RunIsPausedUntilOneResumes covers one Run
// with several WAITING_CALLBACK NodeRuns: every waiting fact is retained, and with no
// READY or RUNNING NodeRun the Run is PAUSED. The aggregation rule is
// runtime.AggregateRunStatus: FAILED beats RUNNING beats PAUSED, so resuming one of the
// two waiting NodeRuns leaves the Run PAUSED as long as its completion unlocked no
// downstream work -- the join node here needs both branches -- and only the second
// resume, which does make the join READY, returns the Run to RUNNING.
func TestRunAggregate_TwoWaitingCallbackNodeRuns_RunIsPausedUntilOneResumes(t *testing.T) {
	h := newAsyncFanOutHarness(t)
	run, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: h.def.WorkflowID, DefinitionVersion: h.def.Version,
		Input: json.RawMessage(`{"brief":"ember"}`),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	dispatches := h.dispatchAll(run)
	if len(dispatches) != 2 {
		t.Fatalf("async Nodes dispatched: want 2, got %d", len(dispatches))
	}

	// Both waiting facts are persisted: Attempt DISPATCHED, NodeRun WAITING_CALLBACK with
	// waitingAt, and a Callback Binding routing each external task id back to its Attempt.
	for _, nodeID := range []string{"left", "right"} {
		d := dispatches[nodeID]
		attempt := getAttempt(h.ctx, t, h.uow, d.attemptID)
		if attempt.Status != domain.NodeAttemptDispatched {
			t.Fatalf("%s Attempt status: want DISPATCHED, got %s", nodeID, attempt.Status)
		}
		nodeRun := getNodeRun(h.ctx, t, h.uow, d.nodeRunID)
		if nodeRun.Status != domain.NodeRunWaitingCallback {
			t.Fatalf("%s NodeRun status: want WAITING_CALLBACK, got %s", nodeID, nodeRun.Status)
		}
		if nodeRun.WaitingAt == nil {
			t.Fatalf("%s NodeRun waitingAt: want a timestamp, got nil", nodeID)
		}
		binding := getBinding(h.ctx, t, h.uow, d.externalTaskID)
		if binding.TargetID != d.attemptID {
			t.Fatalf("%s Callback Binding target: want %s, got %s", nodeID, d.attemptID, binding.TargetID)
		}
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunPaused {
		t.Fatalf("Run status with two WAITING_CALLBACK NodeRuns and nothing READY or RUNNING: want PAUSED, got %s", got)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if got := asyncEventsOfType(events, domain.EventRunPaused); len(got) != 1 {
		t.Fatalf("RUN_PAUSED events: want 1, got %d (%v)", len(got), execEventTypes(events))
	}

	// First resume: that branch succeeds, but the join node still waits for the other one,
	// so no NodeRun is READY or RUNNING and the Run stays PAUSED.
	first, err := h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: dispatches["left"].externalTaskID,
		Payload:        json.RawMessage(`{"text":"alpha"}`),
		Source:         domain.CompletionCallback,
	})
	if err != nil {
		t.Fatalf("resume the left branch: %v", err)
	}
	if first.Duplicate || first.Failed {
		t.Fatalf("resume the left branch: want a fresh success, got %+v", first)
	}
	if got := getNodeRun(h.ctx, t, h.uow, dispatches["left"].nodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("left NodeRun after its callback: want SUCCEEDED, got %s", got)
	}
	if got := getNodeRun(h.ctx, t, h.uow, dispatches["right"].nodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("right NodeRun after the left branch resumed: want WAITING_CALLBACK, got %s", got)
	}
	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	for _, nr := range nodeRuns {
		if nr.NodeID == "join" {
			t.Fatalf("join NodeRun exists after only one branch completed: status %s", nr.Status)
		}
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunPaused {
		t.Fatalf("Run status after one of two waiting NodeRuns resumed with no downstream work unlocked: want PAUSED, got %s", got)
	}
	events = listEvents(h.ctx, t, h.uow, run.ID)
	if got := asyncEventsOfType(events, domain.EventRunResumed); len(got) != 0 {
		t.Fatalf("RUN_RESUMED after the first of two resumes: want none, got %v", execEventTypes(events))
	}

	// Second resume: the join node becomes READY in the same transaction, so the Run
	// returns to RUNNING and records exactly one RUN_RESUMED.
	second, err := h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: dispatches["right"].externalTaskID,
		Payload:        json.RawMessage(`{"text":"beta"}`),
		Source:         domain.CompletionCallback,
	})
	if err != nil {
		t.Fatalf("resume the right branch: %v", err)
	}
	if second.Duplicate || second.Failed {
		t.Fatalf("resume the right branch: want a fresh success, got %+v", second)
	}
	if got := execNodeRunByNodeID(t, listNodeRuns(h.ctx, t, h.uow, run.ID), "join").Status; got != domain.NodeRunReady {
		t.Fatalf("join NodeRun after both branches completed: want READY, got %s", got)
	}
	if got := getRun(h.ctx, t, h.uow, run.ID).Status; got != domain.RunRunning {
		t.Fatalf("Run status after the second resume unlocked downstream work: want RUNNING, got %s", got)
	}
	events = listEvents(h.ctx, t, h.uow, run.ID)
	if got := asyncEventsOfType(events, domain.EventRunResumed); len(got) != 1 {
		t.Fatalf("RUN_RESUMED after the second resume: want 1, got %d (%v)", len(got), execEventTypes(events))
	}

	// Draining the remaining synchronous work completes the Run.
	for i := 0; i < 10; i++ {
		next, err := h.svc.Advance(h.ctx, run.ID)
		if err != nil {
			t.Fatalf("advance after both resumes: %v", err)
		}
		if !next.Claimed {
			break
		}
		if err := h.svc.Execute(h.ctx, next); err != nil {
			t.Fatalf("execute %s: %v", next.NodeType, err)
		}
	}
	finished := getRun(h.ctx, t, h.uow, run.ID)
	if finished.Status != domain.RunCompleted {
		t.Fatalf("Run status after both branches resumed and the DAG drained: want COMPLETED, got %s", finished.Status)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(finished.Output, &output); err != nil {
		t.Fatalf("decode Run.output: %v", err)
	}
	if string(output["text"]) != `"alpha+beta"` {
		t.Fatalf("Run.output text: want %q, got %s", "alpha+beta", output["text"])
	}
}
