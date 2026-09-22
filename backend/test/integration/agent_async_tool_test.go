//go:build integration

// Asynchronous Agent Tool dispatch tests (06 §1.7, 05 §1.6, 08 §4). An ASYNC Tool reuses
// the async Node's persisted Callback Binding: the claim transaction commits the Tool
// Attempt with its callback token hash before the Tool is called, and the dispatch
// transaction commits the Attempt's DISPATCHED status, the Binding, the Action's and the
// Agent NodeRun's WAITING_CALLBACK status, the Run re-aggregation and AGENT_ACTION_WAITING
// together. They share the agentHarness of agent_loop_test.go.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
)

// agentAsyncExternalTaskID is the external task id the scripted async Tool reports.
const agentAsyncExternalTaskID = "provider_task_agent_async"

// agentAsyncTool is a scripted AsyncToolExecutor registered under the real
// `remote_lookup` Metadata, so the Runtime reads ExecutionKind ASYNC from the Registry
// exactly as it would for the Mock Provider-backed Executor. during runs inside the call:
// the only moment at which a test can observe what the claim transaction had committed
// before the Tool was allowed to run.
type agentAsyncTool struct {
	result registry.ToolExecutionResult
	err    error
	during func(ctx context.Context, action registry.ToolAction)
	// onCallback, when set, replaces the real `remote_lookup` payload interpretation; it
	// runs where the resume use case calls OnCallback, outside every lock.
	onCallback func(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error)

	mu        sync.Mutex
	calls     []registry.ToolAction
	callbacks []registry.ToolAsyncState
}

func (a *agentAsyncTool) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	a.mu.Lock()
	a.calls = append(a.calls, action)
	a.mu.Unlock()
	if a.during != nil {
		a.during(ctx, action)
	}
	return a.result, a.err
}

// OnCallback records the restored async state and, unless scripted, interprets the payload
// exactly as the real `remote_lookup` Executor does.
func (a *agentAsyncTool) OnCallback(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
	a.mu.Lock()
	a.callbacks = append(a.callbacks, state)
	a.mu.Unlock()
	if a.onCallback != nil {
		return a.onCallback(ctx, state, payload)
	}
	return remotelookup.New("http://mock-provider.invalid", nil).OnCallback(ctx, state, payload)
}

func (a *agentAsyncTool) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func (a *agentAsyncTool) callbackCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.callbacks)
}

// lastToken is the plaintext callback credential the last Execute call was handed.
func (a *agentAsyncTool) lastToken(t *testing.T) string {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) == 0 || a.calls[len(a.calls)-1].Callback == nil {
		t.Fatal("the async Tool was never handed a callback credential")
	}
	return a.calls[len(a.calls)-1].Callback.Token
}

func agentAsyncDispatched(externalTaskID string) registry.ToolExecutionResult {
	return registry.ToolExecutionResult{
		Kind:         registry.ToolResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: remotelookup.ProviderID, ExternalTaskID: externalTaskID},
	}
}

// newAgentAsyncHarness wires the agentHarness with `remote_lookup` registered around tool,
// saves a Definition whose Agent may call only `remote_lookup`, scripts one TOOL_CALL and
// drives the Run up to the Agent NodeRun claim.
func newAgentAsyncHarness(t *testing.T, workflowID string, tool *agentAsyncTool) (*agentHarness, domain.Run, service.AdvanceOutcome) {
	t.Helper()
	return newAgentAsyncHarnessWithNotifier(t, workflowID, tool, nil)
}

// newAgentAsyncHarnessWithNotifier is newAgentAsyncHarness with the post-COMMIT
// EventNotifier replaced, so a test can stop the in-process chain at a chosen commit.
func newAgentAsyncHarnessWithNotifier(t *testing.T, workflowID string, tool *agentAsyncTool, notifier service.EventNotifier) (*agentHarness, domain.Run, service.AdvanceOutcome) {
	t.Helper()
	return newAgentAsyncHarnessWithOptions(t, workflowID, tool, agentHarnessOptions{Notifier: notifier})
}

// newAgentAsyncHarnessWithOptions is newAgentAsyncHarness with the notifier and the
// WorkEnqueuer taken from opts; the Tools and the Clock are always the async ones.
func newAgentAsyncHarnessWithOptions(t *testing.T, workflowID string, tool *agentAsyncTool, opts agentHarnessOptions) (*agentHarness, domain.Run, service.AdvanceOutcome) {
	t.Helper()
	return newAgentAsyncHarnessMaxTurns(t, workflowID, tool, opts, agentMaxTurns)
}

// newAgentAsyncHarnessMaxTurns is newAgentAsyncHarnessWithOptions with the Agent's frozen
// maxTurns bound chosen by the caller.
func newAgentAsyncHarnessMaxTurns(t *testing.T, workflowID string, tool *agentAsyncTool, opts agentHarnessOptions, maxTurns int) (*agentHarness, domain.Run, service.AdvanceOutcome) {
	t.Helper()
	registration := remotelookup.Registration("http://mock-provider.invalid", nil)
	registration.Executor = tool
	// The Agent deadline also bounds the real Tool call's context, so the fake clock starts
	// at wall-clock time: fixtureTime's deadline has already passed in real time and would
	// expire the call's context before the scripted Tool returns.
	h := newAgentHarness(t, agentHarnessOptions{
		Tools:    []registry.ToolRegistration{registration},
		Clock:    newExecClock(time.Now().UTC().Truncate(time.Millisecond)),
		Notifier: opts.Notifier,
		Queue:    opts.Queue,
	})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: remotelookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})

	def := agentLoopDefinitionMaxTurns(workflowID, maxTurns)
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, `["`+remotelookup.ToolName+`"]`, 1))
		}
	}
	def = h.saveDefinition(def)
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	return h, run, h.claimAgentNode(run.ID)
}

func agentOnlyToolAttempt(t *testing.T, h *agentHarness, actionID string) domain.ToolAttempt {
	t.Helper()
	attempts := agentToolAttempts(h.ctx, t, h.uow, actionID)
	if len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1", len(attempts))
	}
	return attempts[0]
}

func agentToolBindings(t *testing.T, h *agentHarness, attemptID string) []domain.CallbackBinding {
	t.Helper()
	var bindings []domain.CallbackBinding
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		bindings, err = tx.CallbackBindings().ListByTargets(ctx, domain.CallbackTargetToolAttempt, []string{attemptID})
		return err
	}); err != nil {
		t.Fatalf("list tool attempt bindings: %v", err)
	}
	return bindings
}

func agentSHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func assertContiguousSeq(t *testing.T, events []domain.Event) {
	t.Helper()
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d (%s) seq = %d, want %d: seq must be contiguous", i, ev.Type, ev.Seq, i+1)
		}
	}
}

// TestAgentAsyncTool_Dispatch_CommitsAttemptBindingActionNodeRunAndRunInOneTx covers
// 06 §1.7: the dispatch transaction marks the Tool Attempt DISPATCHED, saves the Binding
// that routes to it, puts the Action and the Agent NodeRun into WAITING_CALLBACK,
// re-aggregates the Run and writes AGENT_ACTION_WAITING.
func TestAgentAsyncTool_Dispatch_CommitsAttemptBindingActionNodeRunAndRunInOneTx(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-dispatch", tool)

	h.execute(outcome)

	if tool.count() != 1 {
		t.Fatalf("tool calls = %d, want exactly 1", tool.count())
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionWaitingCallback || action.WaitingAt == nil {
		t.Errorf("action status = %s waitingAt = %v, want WAITING_CALLBACK with waiting_at", action.Status, action.WaitingAt)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptDispatched || attempt.DispatchedAt == nil {
		t.Errorf("tool attempt status = %s dispatchedAt = %v, want DISPATCHED with dispatched_at", attempt.Status, attempt.DispatchedAt)
	}
	if attempt.CallbackTokenHash == nil {
		t.Errorf("async tool attempt has no callback token hash")
	}

	var binding domain.CallbackBinding
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		binding, err = tx.CallbackBindings().GetByExternalTaskID(ctx, agentAsyncExternalTaskID)
		return err
	}); err != nil {
		t.Fatalf("binding for %s: %v", agentAsyncExternalTaskID, err)
	}
	if binding.TargetType != domain.CallbackTargetToolAttempt || binding.TargetID != attempt.ID || binding.ProviderID != remotelookup.ProviderID {
		t.Errorf("binding = %+v, want TOOL_ATTEMPT %s from %s", binding, attempt.ID, remotelookup.ProviderID)
	}

	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunWaitingCallback {
		t.Errorf("agent node run status = %s, want WAITING_CALLBACK", nr.Status)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID); got.Status != domain.RunPaused {
		t.Errorf("run status = %s, want PAUSED: nothing else is runnable", got.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s while waiting for a callback", *agentRun.Termination)
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("a next Turn exists before the Tool result arrived")
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	types := execEventTypes(events)
	if agentCountEventType(types, domain.EventAgentActionWaiting) != 1 || agentCountEventType(types, domain.EventRunPaused) != 1 {
		t.Fatalf("events = %v, want exactly one AGENT_ACTION_WAITING and one RUN_PAUSED", types)
	}
	waiting := agentEventPayload(t, events, domain.EventAgentActionWaiting)
	if waiting["toolAttemptId"] != attempt.ID || waiting["callbackBindingId"] != binding.ID || waiting["actionId"] != action.ID {
		t.Errorf("AGENT_ACTION_WAITING payload = %v, want toolAttemptId %s callbackBindingId %s actionId %s", waiting, attempt.ID, binding.ID, action.ID)
	}
	if last := types[len(types)-1]; last != domain.EventRunPaused || types[len(types)-2] != domain.EventAgentActionWaiting {
		t.Errorf("event tail = %v, want AGENT_ACTION_WAITING then RUN_PAUSED", types[len(types)-2:])
	}
}

// TestAgentAsyncTool_ExecutorInvokedOnlyAfterClaimCommit_TokenHashPersistedFirst covers
// 05 §2 Tool Attempt and 08 §4: the callback token hash is committed before the external
// call, and only the Executor receives the plaintext token.
func TestAgentAsyncTool_ExecutorInvokedOnlyAfterClaimCommit_TokenHashPersistedFirst(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	var seen struct {
		action   registry.ToolAction
		attempts []domain.ToolAttempt
		err      error
	}
	var h *agentHarness
	tool.during = func(ctx context.Context, action registry.ToolAction) {
		seen.action = action
		// A fresh transaction sees committed rows only.
		seen.err = h.uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			seen.attempts, err = tx.ToolAttempts().ListByActionID(ctx, action.ActionID)
			return err
		})
	}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-token-first", tool)

	h.execute(outcome)

	if seen.err != nil {
		t.Fatalf("read tool attempts during the call: %v", seen.err)
	}
	if len(seen.attempts) != 1 {
		t.Fatalf("committed tool attempts at call time = %d, want 1", len(seen.attempts))
	}
	committed := seen.attempts[0]
	if committed.Status != domain.ToolAttemptStarted {
		t.Errorf("committed tool attempt status at call time = %s, want STARTED", committed.Status)
	}
	if seen.action.Callback == nil || seen.action.Callback.Token == "" {
		t.Fatalf("executor received no callback credential: %+v", seen.action.Callback)
	}
	if seen.action.Callback.URL != asyncBaseURL+service.CallbackPath {
		t.Errorf("callback URL = %q, want %q", seen.action.Callback.URL, asyncBaseURL+service.CallbackPath)
	}
	if committed.CallbackTokenHash == nil || *committed.CallbackTokenHash != agentSHA256Hex(seen.action.Callback.Token) {
		t.Errorf("committed callback token hash = %v, want sha256 of the plaintext handed to the Executor", committed.CallbackTokenHash)
	}
	for _, ev := range listEvents(h.ctx, t, h.uow, run.ID) {
		if strings.Contains(string(ev.Payload), seen.action.Callback.Token) {
			t.Errorf("event %s payload carries the plaintext callback token", ev.Type)
		}
	}
}

// TestAgentAsyncTool_DispatchTxEventInsertFails_RollsBackEverything covers invariant #3:
// when AGENT_ACTION_WAITING cannot be inserted, no part of the dispatch transaction is
// left behind.
func TestAgentAsyncTool_DispatchTxEventInsertFails_RollsBackEverything(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	var h *agentHarness
	const collidingEventID = "ev_agent_async_dispatch_collision"
	tool.during = func(context.Context, registry.ToolAction) {
		// Armed during the Tool call, so the collision hits the dispatch transaction's
		// first Event and none of the claim transaction's.
		h.ids.ForceNextEventID(collidingEventID)
	}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-rollback", tool)
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())

	err := h.svc.Execute(h.ctx, outcome)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("execute = %v, want the conflict from the duplicated Event ID", err)
	}

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionRunning || action.WaitingAt != nil {
		t.Errorf("action status = %s waitingAt = %v, want RUNNING without waiting_at", action.Status, action.WaitingAt)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptStarted || attempt.DispatchedAt != nil {
		t.Errorf("tool attempt status = %s dispatchedAt = %v, want STARTED", attempt.Status, attempt.DispatchedAt)
	}
	if attempt.CallbackTokenHash == nil {
		t.Errorf("the claim transaction's callback token hash did not survive the dispatch rollback")
	}
	if bindings := agentToolBindings(t, h, attempt.ID); len(bindings) != 0 {
		t.Errorf("bindings after rollback = %+v, want none", bindings)
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nr.Status)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID); got.Status != domain.RunRunning {
		t.Errorf("run status = %s, want RUNNING", got.Status)
	}
	types := execEventTypes(listEvents(h.ctx, t, h.uow, run.ID))
	if agentCountEventType(types, domain.EventAgentActionWaiting) != 0 || agentCountEventType(types, domain.EventRunPaused) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_WAITING and no RUN_PAUSED", types)
	}
}

// assertAgentAsyncToolFailed checks the shared Tool failure outcome for an async Tool
// whose dispatch never produced a route home: the Attempt keeps its token hash, fails, and
// no Binding exists.
func assertAgentAsyncToolFailed(t *testing.T, h *agentHarness, run domain.Run, outcome service.AdvanceOutcome, wantCode string) {
	t.Helper()
	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationToolError, domain.FailureSyncExecution)
	if action.Error == nil || action.Error.Code != wantCode {
		t.Errorf("action error = %+v, want code %s", action.Error, wantCode)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.DispatchedAt != nil {
		t.Errorf("tool attempt status = %s dispatchedAt = %v, want FAILED and never dispatched", attempt.Status, attempt.DispatchedAt)
	}
	if attempt.CallbackTokenHash == nil {
		t.Errorf("async tool attempt has no callback token hash")
	}
	if len(attempt.Result) != 0 {
		t.Errorf("failed tool attempt stored a result: %s", attempt.Result)
	}
	if bindings := agentToolBindings(t, h, attempt.ID); len(bindings) != 0 {
		t.Errorf("bindings = %+v, want none", bindings)
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nr.Status)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID); got.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got.Status)
	}
	types := execEventTypes(listEvents(h.ctx, t, h.uow, run.ID))
	if agentCountEventType(types, domain.EventAgentActionWaiting) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_WAITING", types)
	}
}

func TestAgentAsyncTool_ExecuteReturnsError_FailsActionWithToolError(t *testing.T) {
	tool := &agentAsyncTool{err: errors.New("remote_lookup: dispatch to provider: send request: transport failure")}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-execute-error", tool)

	h.execute(outcome)

	assertAgentAsyncToolFailed(t, h, run, outcome, "TOOL_ERROR")
}

// TestAgentAsyncTool_ExecutorReportsCompleted_FailsAsRegistrationContradictionNoBinding
// covers 07 §1.5: an ASYNC Tool must return dispatch information or an explicit error. A
// completed result contradicts its registration and is not repaired into a success.
func TestAgentAsyncTool_ExecutorReportsCompleted_FailsAsRegistrationContradictionNoBinding(t *testing.T) {
	tool := &agentAsyncTool{result: registry.ToolExecutionResult{
		Kind:   registry.ToolResultCompleted,
		Result: &registry.ToolResult{Output: json.RawMessage(agentToolResult)},
	}}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-completed", tool)

	h.execute(outcome)

	assertAgentAsyncToolFailed(t, h, run, outcome, "TOOL_EXECUTION_KIND_MISMATCH")
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("the contradictory completed result was appended to the Agent Context")
	}
}

// TestAgentAsyncTool_DispatchedWithoutExternalTaskID_FailsExplicitlyNotRecoverable covers
// 09 §3.7: a Provider that accepted a task but left no external task id is an explicit
// failure at the declared recovery boundary, never a recoverable wait.
func TestAgentAsyncTool_DispatchedWithoutExternalTaskID_FailsExplicitlyNotRecoverable(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched("")}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-no-task-id", tool)

	h.execute(outcome)

	assertAgentAsyncToolFailed(t, h, run, outcome, "DISPATCH_WITHOUT_EXTERNAL_TASK")
}

// TestAgentAsyncTool_DeadlineExpiresWhileWaitingCallback_TimeoutWinsNotToolError covers
// 06 §1.7: the Agent deadline covers the wait for a callback, and the timeout transaction
// closes the DISPATCHED Attempt, the WAITING_CALLBACK Action, the Agent Run, the Agent
// NodeRun and the Run together with TIMEOUT as the source and termination.
func TestAgentAsyncTool_DeadlineExpiresWhileWaitingCallback_TimeoutWinsNotToolError(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-timeout", tool)
	h.execute(outcome)

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
	}
	eventsBefore := len(listEvents(h.ctx, t, h.uow, run.ID))

	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	if err := h.svc.TimeoutAgentRun(h.ctx, outcome.AgentRunID); err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}

	action = getAgentAction(h.ctx, t, h.uow, action.ID)
	if action.Status != domain.AgentActionFailed {
		t.Errorf("action status = %s, want FAILED", action.Status)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempt status = %s, want FAILED", attempt.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nr.Status)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID); got.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got.Status)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	tail := execEventTypes(events[eventsBefore:])
	for _, want := range []domain.EventType{domain.EventAgentActionFailed, domain.EventAgentFailed, domain.EventNodeFailed} {
		if agentCountEventType(tail, want) != 1 {
			t.Errorf("timeout events = %v, want exactly one %s", tail, want)
		}
	}
	failed := agentEventPayload(t, events, domain.EventAgentActionFailed)
	if failed["failureSource"] != string(domain.FailureTimeout) || failed["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_FAILED payload = %v, want failureSource TIMEOUT for %s", failed, attempt.ID)
	}
}

// TestAgentAsyncTool_DispatchLosesToAgentTimeout_WritesNothing covers the timeout race of
// invariant #7: the Agent timeout commits while the ASYNC Tool is still dispatching, so
// the dispatch transaction finds the Attempt no longer STARTED and writes nothing -- no
// Binding, no AGENT_ACTION_WAITING, and the committed TIMEOUT outcome stands.
func TestAgentAsyncTool_DispatchLosesToAgentTimeout_WritesNothing(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	var h *agentHarness
	var agentRunID string
	var timeoutErr error
	tool.during = func(context.Context, registry.ToolAction) {
		// Only the injected clock passes the deadline; the call's real context stays live,
		// so the Tool returns its dispatch and the dispatch transaction really runs.
		h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
		timeoutErr = h.svc.TimeoutAgentRun(h.ctx, agentRunID)
	}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-dispatch-loses", tool)
	agentRunID = outcome.AgentRunID

	h.execute(outcome)
	if timeoutErr != nil {
		t.Fatalf("timeout agent run during the call: %v", timeoutErr)
	}
	if tool.count() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.count())
	}

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationTimeout, domain.FailureTimeout)
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.DispatchedAt != nil {
		t.Errorf("tool attempt status = %s dispatchedAt = %v, want FAILED and never dispatched", attempt.Status, attempt.DispatchedAt)
	}
	if bindings := agentToolBindings(t, h, attempt.ID); len(bindings) != 0 {
		t.Errorf("bindings = %+v, want none: the losing dispatch must write nothing", bindings)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID); got.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got.Status)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	types := execEventTypes(events)
	if agentCountEventType(types, domain.EventAgentActionWaiting) != 0 || agentCountEventType(types, domain.EventRunPaused) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_WAITING and no RUN_PAUSED", types)
	}
	failed := agentEventPayload(t, events, domain.EventAgentActionFailed)
	if failed["failureSource"] != string(domain.FailureTimeout) || failed["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_FAILED payload = %v, want failureSource TIMEOUT for %s", failed, attempt.ID)
	}
}

// TestAgentAsyncTool_BindingConflict_FailsActionKeepsExistingBinding covers the database
// deduplication guarantee: UNIQUE external_task_id refuses a second route for an external
// task already bound elsewhere. The dispatch rolls back, the Action fails explicitly with
// CALLBACK_BINDING_CONFLICT, and the pre-existing Binding is left untouched.
func TestAgentAsyncTool_BindingConflict_FailsActionKeepsExistingBinding(t *testing.T) {
	tool := &agentAsyncTool{result: agentAsyncDispatched(agentAsyncExternalTaskID)}
	h, run, outcome := newAgentAsyncHarness(t, "wf-agent-async-binding-conflict", tool)

	existing := domain.CallbackBinding{
		ID:             "binding_preexisting",
		ProviderID:     remotelookup.ProviderID,
		ExternalTaskID: agentAsyncExternalTaskID,
		TargetType:     domain.CallbackTargetNodeAttempt,
		TargetID:       "attempt_preexisting",
		CreatedAt:      h.clock.Now(),
	}
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.CallbackBindings().Create(ctx, existing)
	}); err != nil {
		t.Fatalf("pre-create binding: %v", err)
	}

	h.execute(outcome)

	assertAgentAsyncToolFailed(t, h, run, outcome, "CALLBACK_BINDING_CONFLICT")
	var bound domain.CallbackBinding
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		bound, err = tx.CallbackBindings().GetByExternalTaskID(ctx, agentAsyncExternalTaskID)
		return err
	}); err != nil {
		t.Fatalf("read binding for %s: %v", agentAsyncExternalTaskID, err)
	}
	if bound.ID != existing.ID || bound.TargetType != existing.TargetType || bound.TargetID != existing.TargetID {
		t.Errorf("binding for %s = %+v, want the pre-existing %+v unchanged", agentAsyncExternalTaskID, bound, existing)
	}
}
