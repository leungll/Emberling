//go:build integration

// Provider polling of asynchronous Agent Tool Attempts. A Tool that declares a poll policy
// has its DISPATCHED Attempt queried by the Reconciler when the persisted next poll time is
// due; a final answer enters the same idempotent resume use case a callback does, so a
// lost callback no longer leaves the Action to the deadline. A Provider that no longer
// knows the task hands a SAFE call back to the Agent as safe to repeat and keeps any other
// Tool waiting. They share the agentHarness of agent_loop_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

// pollableToolName is the stable Tool Name of the scripted pollable async Tool.
const pollableToolName = "pollable_task"

// pollableExternalTaskID is the external task id the scripted pollable Tool reports.
const pollableExternalTaskID = "provider_task_pollable"

// pollableInterval is the registered minimum delay between two polls of one Attempt.
const pollableInterval = 30 * time.Second

// pollableSafe and pollableKeyed are the two side-effect policies the lost-task handling
// distinguishes: a SAFE call is handed back as safe to repeat, a KEYED one keeps waiting.
var (
	pollableSafe  = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe}
	pollableKeyed = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}
)

// pollableAgentTool is a scripted PollableAsyncToolExecutor. Execute reports one dispatch,
// OnCallback interprets a payload as the real `remote_lookup` Executor does, and Poll
// answers whatever the test scripted while recording the restored async state it was
// handed.
type pollableAgentTool struct {
	dispatch registry.ToolExecutionResult
	poll     func(state registry.ToolAsyncState) (registry.PollResult, error)

	mu    sync.Mutex
	calls []registry.ToolAction
	polls []registry.ToolAsyncState
}

func (p *pollableAgentTool) Execute(_ context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, action)
	return p.dispatch, nil
}

func (p *pollableAgentTool) OnCallback(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
	return remotelookup.New("http://mock-provider.invalid", nil).OnCallback(ctx, state, payload)
}

func (p *pollableAgentTool) Poll(_ context.Context, state registry.ToolAsyncState) (registry.PollResult, error) {
	p.mu.Lock()
	p.polls = append(p.polls, state)
	p.mu.Unlock()
	return p.poll(state)
}

func (p *pollableAgentTool) executeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *pollableAgentTool) pollCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.polls)
}

// lastToken is the plaintext callback credential the last Execute call was handed.
func (p *pollableAgentTool) lastToken(t *testing.T) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 || p.calls[len(p.calls)-1].Callback == nil {
		t.Fatal("the pollable Tool was never handed a callback credential")
	}
	return p.calls[len(p.calls)-1].Callback.Token
}

// pollAnswer scripts a Poll that always returns result.
func pollAnswer(result registry.PollResult) func(registry.ToolAsyncState) (registry.PollResult, error) {
	return func(registry.ToolAsyncState) (registry.PollResult, error) { return result, nil }
}

// pollSucceededResult is a final SUCCEEDED Poll answer carrying the Tool result.
func pollSucceededResult() registry.PollResult {
	return registry.PollResult{
		Status:     registry.PollSucceeded,
		ToolResult: &registry.ToolResult{Output: json.RawMessage(agentCallbackResult)},
	}
}

// newPollableTool is a pollable Tool that dispatches pollableExternalTaskID and answers
// every Poll with result.
func newPollableTool(result registry.PollResult) *pollableAgentTool {
	return &pollableAgentTool{
		dispatch: registry.ToolExecutionResult{
			Kind:         registry.ToolResultDispatched,
			ExternalTask: &registry.ExternalTask{ProviderID: remotelookup.ProviderID, ExternalTaskID: pollableExternalTaskID},
		},
		poll: pollAnswer(result),
	}
}

// pollableRegistration registers tool under pollableToolName with the real `remote_lookup`
// schemas, the given side-effect policy and a poll policy of pollableInterval and maxPolls.
func pollableRegistration(tool *pollableAgentTool, side domain.SideEffectPolicy, maxPolls int) registry.ToolRegistration {
	reg := remotelookup.Registration("http://mock-provider.invalid", nil)
	reg.Metadata.Name = pollableToolName
	reg.Metadata.SideEffect = side
	reg.Metadata.Poll = &domain.PollPolicy{IntervalMs: pollableInterval.Milliseconds(), MaxPolls: maxPolls}
	reg.Executor = tool
	return reg
}

// newPollableHarness wires the agentHarness with the pollable Tool registered, saves a
// Definition whose Agent may call only that Tool, scripts one TOOL_CALL then FINAL, and
// drives the Run up to the Agent NodeRun claim.
func newPollableHarness(t *testing.T, workflowID string, tool *pollableAgentTool, side domain.SideEffectPolicy, maxPolls int, opts agentHarnessOptions) (*agentHarness, domain.Run, service.AdvanceOutcome) {
	t.Helper()
	h := newAgentHarness(t, agentHarnessOptions{
		Tools:     []registry.ToolRegistration{pollableRegistration(tool, side, maxPolls)},
		Clock:     newExecClock(time.Now().UTC().Truncate(time.Millisecond)),
		Queue:     opts.Queue,
		WrapUoW:   opts.WrapUoW,
		PollHooks: opts.PollHooks,
	})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: pollableToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})

	def := agentLoopDefinition(workflowID)
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, `["`+pollableToolName+`"]`, 1))
		}
	}
	def = h.saveDefinition(def)
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	return h, run, h.claimAgentNode(run.ID)
}

// pollableDispatched drives a fresh Run until its Tool Action is WAITING_CALLBACK on the
// pollable Tool's external task and returns the DISPATCHED Attempt.
func pollableDispatched(t *testing.T, workflowID string, tool *pollableAgentTool, side domain.SideEffectPolicy, maxPolls int, opts agentHarnessOptions) (*agentHarness, domain.Run, service.AdvanceOutcome, domain.ToolAttempt) {
	t.Helper()
	h, run, outcome := newPollableHarness(t, workflowID, tool, side, maxPolls, opts)
	h.execute(outcome)
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptDispatched {
		t.Fatalf("precondition: tool attempt status = %s, want DISPATCHED", attempt.Status)
	}
	if attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(attempt.DispatchedAt.Add(pollableInterval)) || attempt.PollCount != 0 {
		t.Fatalf("precondition: dispatch scheduled next_poll_at=%v poll_count=%d, want dispatched_at+%s and 0",
			attempt.NextPollAt, attempt.PollCount, pollableInterval)
	}
	return h, run, outcome, attempt
}

// pollableRestart is a process restart while the pollable Tool Action waits: a second
// ExecutionService over the crashed one's database and clock with its own pollable Tool
// and its own Mock Model Provider scripted to answer FINAL; no queue, notifier or memory
// is carried across.
func pollableRestart(t *testing.T, crashed *agentHarness, tool *pollableAgentTool, side domain.SideEffectPolicy, maxPolls int) *agentHarness {
	t.Helper()
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool:  crashed.pool,
		Clock: crashed.clock,
		Tools: []registry.ToolRegistration{pollableRegistration(tool, side, maxPolls)},
	})
	agentScriptFinal(restarted, mockmodel.Scenario{})
	return restarted
}

// pollToolAttempt enters the Tool poll use case directly, as the Reconciler does for each
// due Attempt it discovers.
func pollToolAttempt(t *testing.T, h *agentHarness, attempt domain.ToolAttempt) service.PollToolAttemptOutcome {
	t.Helper()
	outcome, err := h.svc.PollToolAttempt(h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	if err != nil {
		t.Fatalf("poll tool attempt %s: %v", attempt.ID, err)
	}
	return outcome
}

// agentPollCompletions counts the AGENT_ACTION_COMPLETED Events whose completion source is
// the given one.
func agentPollCompletions(t *testing.T, events []domain.Event, source domain.CompletionSource) int {
	t.Helper()
	n := 0
	for _, ev := range events {
		if ev.Type != domain.EventAgentActionCompleted {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", ev.Type, err)
		}
		if payload["completionSource"] == string(source) {
			n++
		}
	}
	return n
}

// assertPollResumedOnce proves exactly one PROVIDER_POLL completion resolved the Attempt
// and the Action, the Agent Loop continued with exactly one next Turn, and the Run left
// PAUSED.
func assertPollResumedOnce(t *testing.T, h *agentHarness, run domain.Run, outcome service.AdvanceOutcome, attemptID string) {
	t.Helper()
	attempt := getToolAttempt(t, h, attemptID)
	if attempt.Status != domain.ToolAttemptSucceeded || attempt.CompletedAt == nil {
		t.Errorf("tool attempt = status %s completedAt %v, want SUCCEEDED", attempt.Status, attempt.CompletedAt)
	}
	assertSameJSON(t, "tool attempt result", json.RawMessage(agentCallbackResult), attempt.Result)
	action := getAgentAction(h.ctx, t, h.uow, attempt.ActionID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", action.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s after a polled result", *agentRun.Termination)
	}
	if !agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) || agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 3) {
		t.Errorf("turns after the polled result: want exactly turn 2 created")
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nr.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunRunning {
		t.Errorf("run status = %s, want RUNNING", persisted.Status)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if got := agentCountEventType(execEventTypes(events), domain.EventAgentActionCompleted); got != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED events = %d, want exactly 1", got)
	}
	if got := agentPollCompletions(t, events, domain.CompletionProviderPoll); got != 1 {
		t.Errorf("PROVIDER_POLL completions = %d, want exactly 1", got)
	}
	if got := agentCountEventType(execEventTypes(events), domain.EventAgentTurnReady); got != 2 {
		t.Errorf("AGENT_TURN_READY events = %d, want 2: the first Turn and the one the poll created", got)
	}
}

// TestToolAttemptPoll_CallbackLost_PollCompletesAttemptOnce covers the lost callback: the
// Provider finished but its callback never arrived, so the Reconciler discovers the due
// poll, the Provider's SUCCEEDED answer resolves the DISPATCHED Attempt exactly once with
// completionSource PROVIDER_POLL, the Action advances once, and a later pass finds nothing
// to poll and appends no Event.
func TestToolAttemptPoll_CallbackLost_PollCompletesAttemptOnce(t *testing.T) {
	tool := newPollableTool(pollSucceededResult())
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-lost-callback", tool, pollableSafe, 5, agentHarnessOptions{})

	h.clock.Advance(pollableInterval)
	rec := agentReconciler(h)
	report := agentRunOnce(h, rec)
	if report.DueToolPollsFound != 1 || report.ToolPollsResumed != 1 || report.ToolPollsClaimed != 1 {
		t.Fatalf("report = %+v, want one due tool poll found, claimed and resumed", report)
	}
	if tool.pollCount() != 1 || tool.executeCount() != 1 {
		t.Fatalf("polls = %d executes = %d, want one poll and no second dispatch", tool.pollCount(), tool.executeCount())
	}
	state := tool.polls[0]
	if state.ActionID != attempt.ActionID || state.AgentRunID != outcome.AgentRunID || state.ToolName != pollableToolName ||
		state.AttemptNo != 1 || state.ExternalTask.ExternalTaskID != pollableExternalTaskID || state.ExternalTask.ProviderID != remotelookup.ProviderID {
		t.Errorf("Poll state = %+v, want the committed Action's async state", state)
	}
	assertPollResumedOnce(t, h, run, outcome, attempt.ID)
	eventsAfter := len(listEvents(h.ctx, t, h.uow, run.ID))

	// Nothing is left to poll, and the next pass appends nothing for this Attempt. The
	// READY Turn the resume committed is drained first so the pass does not advance it.
	if err := h.drainTurns(h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}
	eventsAfterTurn := len(listEvents(h.ctx, t, h.uow, run.ID))
	h.clock.Advance(pollableInterval)
	if report := agentRunOnce(h, rec); report.DueToolPollsFound != 0 {
		t.Errorf("second pass report = %+v, want no due tool poll", report)
	}
	if tool.pollCount() != 1 {
		t.Errorf("polls after the second pass = %d, want still 1", tool.pollCount())
	}
	if got := agentPollCompletions(t, listEvents(h.ctx, t, h.uow, run.ID), domain.CompletionProviderPoll); got != 1 {
		t.Errorf("PROVIDER_POLL completions after the second pass = %d, want still 1 (events %d -> %d)", got, eventsAfter, eventsAfterTurn)
	}
}

// TestToolAttemptPoll_RacesCallback_ExactlyOneWinner holds a claimed SUCCEEDED poll and the
// Provider's callback at a barrier just before their state-changing transactions, after
// both routed to the DISPATCHED Attempt, and releases them together. The conditional
// update from DISPATCHED elects exactly one winner; the loser's update affects no row, it
// reports a duplicate and appends no Event.
func TestToolAttemptPoll_RacesCallback_ExactlyOneWinner(t *testing.T) {
	tool := newPollableTool(pollSucceededResult())
	arrived := make(chan struct{})
	release := make(chan struct{})
	claimed := make(chan struct{})
	var barrier *pollArmedBarrierUoW
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-race-callback", tool, pollableSafe, 5, agentHarnessOptions{
		WrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
			barrier = &pollArmedBarrierUoW{UnitOfWork: inner, arrived: arrived, release: release}
			return barrier
		},
		PollHooks: service.PollHooks{AfterClaim: func(context.Context, string) error {
			// The claim committed: from here on every state-changing transaction waits at
			// the barrier, and the callback may start.
			barrier.armed.Store(true)
			close(claimed)
			return nil
		}},
	})
	token := tool.lastToken(t)
	h.clock.Advance(pollableInterval)

	var wg sync.WaitGroup
	var pollOutcome service.PollToolAttemptOutcome
	var callbackOutcome service.CallbackOutcome
	var pollErr, callbackErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		pollOutcome, pollErr = h.svc.PollToolAttempt(h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	}()
	go func() {
		defer wg.Done()
		<-claimed
		callbackOutcome, callbackErr = h.svc.HandleCallback(h.ctx, service.HandleCallback{
			Token: token, ExternalTaskID: pollableExternalTaskID, Payload: json.RawMessage(agentCallbackSucceeded),
		})
	}()
	<-arrived
	<-arrived
	barrier.armed.Store(false)
	close(release)
	wg.Wait()

	if pollErr != nil || callbackErr != nil {
		t.Fatalf("poll err = %v, callback err = %v, want neither", pollErr, callbackErr)
	}
	pollWon := pollOutcome.Kind == service.PollAttemptResumed
	callbackWon := callbackOutcome.Accepted && !callbackOutcome.Duplicate
	if pollWon == callbackWon {
		t.Fatalf("poll outcome = %+v, callback outcome = %+v, want exactly one winner", pollOutcome, callbackOutcome)
	}
	if !pollWon && pollOutcome.Kind != service.PollAttemptDuplicate {
		t.Errorf("losing poll outcome = %+v, want DUPLICATE", pollOutcome)
	}

	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	if got := getAgentAction(h.ctx, t, h.uow, attempt.ActionID).Status; got != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", got)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) || agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 3) {
		t.Errorf("turns after the race: want exactly turn 2 created")
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if got := agentCountEventType(execEventTypes(events), domain.EventAgentActionCompleted); got != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED events = %d, want exactly 1: the loser appends nothing", got)
	}
	wantSource := domain.CompletionCallback
	if pollWon {
		wantSource = domain.CompletionProviderPoll
	}
	if got := agentPollCompletions(t, events, wantSource); got != 1 {
		t.Errorf("completions with source %s = %d, want 1", wantSource, got)
	}
}

// TestToolAttemptPoll_UnknownTaskSafeTool_ReturnsExecutionInterruptedWithOneNewTurn covers
// a runner that restarted and lost the task: the Provider answers UNKNOWN_TASK, and because
// the Tool's registered side effect is SAFE the call is handed back to the Agent as
// EXECUTION_INTERRUPTED -- the Attempt DISPATCHED -> FAILED and known safe to repeat, the
// Action failed, one AGENT_ACTION_FAILED, a bounded tool message and exactly one new READY
// Turn, with the Agent Run continuing and the Tool never re-dispatched by the Runtime.
func TestToolAttemptPoll_UnknownTaskSafeTool_ReturnsExecutionInterruptedWithOneNewTurn(t *testing.T) {
	tool := newPollableTool(registry.PollResult{Status: registry.PollUnknownTask})
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-unknown-safe", tool, pollableSafe, 5, agentHarnessOptions{})
	eventsBefore := len(listEvents(h.ctx, t, h.uow, run.ID))

	h.clock.Advance(pollableInterval)
	report := agentRunOnce(h, agentReconciler(h))
	if report.DueToolPollsFound != 1 || report.ToolPollsReturned != 1 {
		t.Fatalf("report = %+v, want one due tool poll handed back to the Agent", report)
	}

	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTION_INTERRUPTED" || attempt.CompletedAt == nil {
		t.Errorf("attempt = status %s error %+v, want FAILED with EXECUTION_INTERRUPTED", attempt.Status, attempt.Error)
	}
	if attempt.Uncertain == nil || *attempt.Uncertain {
		t.Errorf("attempt uncertain = %v, want false: a SAFE call is known to be repeatable", attempt.Uncertain)
	}
	if len(attempt.Result) != 0 {
		t.Errorf("attempt result = %s, want none", attempt.Result)
	}
	action := getAgentAction(h.ctx, t, h.uow, attempt.ActionID)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Errorf("action = status %s error %+v, want FAILED with EXECUTION_INTERRUPTED", action.Status, action.Error)
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Fatalf("agent run terminated as %s, want it to continue", *agentRun.Termination)
	}
	if got := readyTurnNos(t, h, agentRun.ID); len(got) != 1 || got[0] != 2 {
		t.Errorf("ready turn numbers = %v, want exactly [2]", got)
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 3) {
		t.Error("a Turn 3 exists, want the handback to produce one next Turn only")
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nr.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunRunning {
		t.Errorf("run status = %s, want RUNNING", persisted.Status)
	}

	messages := agentContextMessages(t, agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 1).Messages)
	if len(messages) < 2 {
		t.Fatalf("context version 1 has %d messages, want the Decision and the tool message", len(messages))
	}
	toolMessage := messages[len(messages)-1]
	if toolMessage.ToolActionID == nil || *toolMessage.ToolActionID != action.ID || len(toolMessage.Content) > 1024 {
		t.Errorf("tool message = %+v, want it linked to action %s within 1 KiB", toolMessage, action.ID)
	}
	if strings.Contains(string(toolMessage.Content), string(attempt.Input)) {
		t.Errorf("tool message %s repeats the call's arguments", toolMessage.Content)
	}
	var content map[string]any
	if err := json.Unmarshal(toolMessage.Content, &content); err != nil {
		t.Fatalf("decode tool message: %v", err)
	}
	if content["status"] != "EXECUTION_INTERRUPTED" {
		t.Errorf("tool message status = %v, want EXECUTION_INTERRUPTED", content["status"])
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	tail := execEventTypes(events[eventsBefore:])
	if agentCountEventType(tail, domain.EventAgentActionFailed) != 1 || agentCountEventType(tail, domain.EventAgentTurnReady) != 1 ||
		agentCountEventType(tail, domain.EventRunResumed) != 1 || agentCountEventType(tail, domain.EventAgentFailed) != 0 {
		t.Errorf("handback events = %v, want one AGENT_ACTION_FAILED, one AGENT_TURN_READY, one RUN_RESUMED and no AGENT_FAILED", tail)
	}
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureProviderPoll) || failed["toolAttemptId"] != attempt.ID || failed["returnedToAgent"] != true {
		t.Errorf("AGENT_ACTION_FAILED = %v, want failureSource PROVIDER_POLL naming %s and returnedToAgent", failed, attempt.ID)
	}

	// The Agent decides the next step itself; the Runtime never re-dispatched the call.
	if err := h.drainTurns(h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}
	agentRunOnce(h, agentReconciler(h))
	if tool.executeCount() != 1 {
		t.Errorf("executes = %d, want 1: the lost call is not repeated by the Runtime", tool.executeCount())
	}
	agentRun, _ = agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent run termination = %s, want FINAL_RESPONSE from the next Decision", agentTermination(agentRun))
	}
}

// TestToolAttemptPoll_UnknownTaskKeyedTool_KeepsWaitingAndReschedules covers the other
// side-effect class: a Provider that does not know a KEYED Tool's task proves nothing about
// whether the request will still arrive, so the Attempt stays DISPATCHED, the Action keeps
// waiting, only the poll schedule advances and no Event is appended.
func TestToolAttemptPoll_UnknownTaskKeyedTool_KeepsWaitingAndReschedules(t *testing.T) {
	tool := newPollableTool(registry.PollResult{Status: registry.PollUnknownTask})
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-unknown-keyed", tool, pollableKeyed, 5, agentHarnessOptions{})
	before := agentSnapshot(t, h, run, outcome)

	h.clock.Advance(pollableInterval)
	polledAt := h.clock.Now()
	got := pollToolAttempt(t, h, attempt)
	if got.Kind != service.PollAttemptClaimedNotFinal || got.Status != registry.PollUnknownTask {
		t.Fatalf("outcome = %+v, want CLAIMED_NOT_FINAL with status UNKNOWN_TASK", got)
	}

	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptDispatched || attempt.Uncertain != nil || attempt.Error != nil {
		t.Errorf("attempt = %+v, want still DISPATCHED without error or uncertain flag", attempt)
	}
	if attempt.PollCount != 1 || attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(polledAt.Add(pollableInterval)) {
		t.Errorf("poll schedule = count %d next %v, want 1 and %v", attempt.PollCount, attempt.NextPollAt, polledAt.Add(pollableInterval))
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Errorf("state after UNKNOWN_TASK for a KEYED tool = %+v, want unchanged %+v", after, before)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Error("a Turn 2 exists, want the Action to keep waiting")
	}
}

// TestToolAttemptPoll_MaxPollsReached_StopsSchedulingAttemptStaysDispatched covers the poll
// bound: the claim that reaches the registered maximum clears next_poll_at instead of
// scheduling another poll, the Attempt stays DISPATCHED for a callback or the deadline, and
// a later pass finds nothing to poll.
func TestToolAttemptPoll_MaxPollsReached_StopsSchedulingAttemptStaysDispatched(t *testing.T) {
	const maxPolls = 2
	tool := newPollableTool(registry.PollResult{Status: registry.PollRunning})
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-max-polls", tool, pollableSafe, maxPolls, agentHarnessOptions{})
	before := agentSnapshot(t, h, run, outcome)
	rec := agentReconciler(h)

	h.clock.Advance(pollableInterval)
	if report := agentRunOnce(h, rec); report.DueToolPollsFound != 1 || report.ToolPollsClaimed != 1 {
		t.Fatalf("first pass report = %+v, want one due tool poll claimed", report)
	}
	first := getToolAttempt(t, h, attempt.ID)
	if first.PollCount != 1 || first.NextPollAt == nil || !first.NextPollAt.Equal(h.clock.Now().Add(pollableInterval)) {
		t.Fatalf("after the first poll: count %d next %v, want 1 and %v", first.PollCount, first.NextPollAt, h.clock.Now().Add(pollableInterval))
	}

	h.clock.Advance(pollableInterval)
	if report := agentRunOnce(h, rec); report.DueToolPollsFound != 1 || report.ToolPollsClaimed != 1 {
		t.Fatalf("second pass report = %+v, want one due tool poll claimed", report)
	}
	second := getToolAttempt(t, h, attempt.ID)
	if second.Status != domain.ToolAttemptDispatched || second.PollCount != maxPolls || second.NextPollAt != nil {
		t.Errorf("after the poll bound: status %s count %d next %v, want DISPATCHED, %d and no next poll", second.Status, second.PollCount, second.NextPollAt, maxPolls)
	}

	h.clock.Advance(pollableInterval)
	if report := agentRunOnce(h, rec); report.DueToolPollsFound != 0 {
		t.Errorf("third pass report = %+v, want no due tool poll", report)
	}
	if tool.pollCount() != maxPolls {
		t.Errorf("polls = %d, want exactly %d", tool.pollCount(), maxPolls)
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Errorf("state after the poll bound = %+v, want unchanged %+v", after, before)
	}
}

// TestResumeNode_PolledResultForToolWithoutPollPolicy_Rejected covers the route guard: a
// Tool that declares no poll policy (`remote_lookup`) resumes by callback only, so a poll
// result for its TOOL_ATTEMPT Binding is refused and nothing is written, and the poll use
// case claims nothing for it.
func TestResumeNode_PolledResultForToolWithoutPollPolicy_Rejected(t *testing.T) {
	tool := &agentAsyncTool{}
	h, run, outcome, _ := agentDispatchedAsync(t, "wf-tool-poll-undeclared", tool, nil)
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.NextPollAt != nil {
		t.Fatalf("precondition: next_poll_at = %v for a tool without a poll policy, want none", attempt.NextPollAt)
	}
	before := agentSnapshot(t, h, run, outcome)

	_, err := h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: agentAsyncExternalTaskID,
		Polled:         &service.PolledResult{AttemptID: attempt.ID, Result: pollSucceededResult()},
	})
	if !errors.Is(err, service.ErrToolPollNotSupported) {
		t.Fatalf("ResumeNode with a poll result = %v, want ErrToolPollNotSupported", err)
	}
	got := pollToolAttempt(t, h, attempt)
	if got.Kind != service.PollAttemptSkipped || got.SkipReason != service.PollSkipRegistryDrift {
		t.Errorf("poll use case outcome = %+v, want SKIPPED for a registration without a poll policy", got)
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Errorf("state after the refused poll = %+v, want unchanged %+v", after, before)
	}
	if tool.callbackCount() != 0 {
		t.Errorf("OnCallback calls = %d, want 0", tool.callbackCount())
	}
}

// TestToolAttemptPoll_ScheduleSurvivesRestart_ReconcilerCompletesAttempt covers the
// persisted-work recovery rule for the poll schedule: the process that dispatched the Tool
// died, and a fresh ExecutionService and Reconciler over the same database discover the due
// poll from the committed next_poll_at alone, complete the Attempt from the Provider's
// answer without calling the Tool again, and let the Agent Loop finish.
func TestToolAttemptPoll_ScheduleSurvivesRestart_ReconcilerCompletesAttempt(t *testing.T) {
	crashedTool := newPollableTool(registry.PollResult{Status: registry.PollRunning})
	crashed, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-restart", crashedTool, pollableSafe, 5, agentHarnessOptions{})
	crashed.dropPendingTurns()

	restartedTool := newPollableTool(pollSucceededResult())
	restarted := pollableRestart(t, crashed, restartedTool, pollableSafe, 5)
	restarted.clock.Advance(pollableInterval)

	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.DueToolPollsFound != 1 || report.ToolPollsResumed != 1 {
		t.Fatalf("report after restart = %+v, want the due tool poll discovered and resumed", report)
	}
	if restartedTool.executeCount() != 0 || crashedTool.pollCount() != 0 || restartedTool.pollCount() != 1 {
		t.Errorf("restarted executes = %d, crashed polls = %d, restarted polls = %d, want 0, 0 and 1",
			restartedTool.executeCount(), crashedTool.pollCount(), restartedTool.pollCount())
	}
	assertPollResumedOnce(t, restarted, run, outcome, attempt.ID)

	agentRunOnceUntil(t, restarted, run.ID, domain.RunCompleted)
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent run termination = %s, want FINAL_RESPONSE", agentTermination(agentRun))
	}
	if restartedTool.executeCount() != 0 {
		t.Errorf("executes after recovery = %d, want 0: the Tool call is never re-executed", restartedTool.executeCount())
	}
}

// TestToolAttemptPoll_ConcurrentScansSameDueAttempt_SecondClaimsNothing holds two polls of
// the same due Attempt at a barrier just before their claim transactions and releases them
// together. The conditional claim elects one: the Provider is queried once, poll_count
// advances once, and the second poll skips without writing anything.
func TestToolAttemptPoll_ConcurrentScansSameDueAttempt_SecondClaimsNothing(t *testing.T) {
	tool := newPollableTool(registry.PollResult{Status: registry.PollRunning})
	arrived := make(chan struct{})
	release := make(chan struct{})
	var barrier *pollArmedBarrierUoW
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-concurrent-scans", tool, pollableSafe, 5, agentHarnessOptions{
		WrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
			barrier = &pollArmedBarrierUoW{UnitOfWork: inner, arrived: arrived, release: release}
			return barrier
		},
	})
	before := agentSnapshot(t, h, run, outcome)
	h.clock.Advance(pollableInterval)
	claimedAt := h.clock.Now()
	barrier.armed.Store(true)

	var wg sync.WaitGroup
	outcomes := make([]service.PollToolAttemptOutcome, 2)
	errs := make([]error, 2)
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i], errs[i] = h.svc.PollToolAttempt(h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
		}(i)
	}
	<-arrived
	<-arrived
	barrier.armed.Store(false)
	close(release)
	wg.Wait()

	var claimed, skipped int
	for i, got := range outcomes {
		if errs[i] != nil {
			t.Fatalf("poll %d: %v", i, errs[i])
		}
		switch {
		case got.Kind == service.PollAttemptClaimedNotFinal:
			claimed++
		case got.Kind == service.PollAttemptSkipped && got.SkipReason == service.PollSkipNotClaimed:
			skipped++
		default:
			t.Errorf("poll %d outcome = %+v, want CLAIMED_NOT_FINAL or SKIPPED/NOT_CLAIMED", i, got)
		}
	}
	if claimed != 1 || skipped != 1 {
		t.Fatalf("claimed = %d skipped = %d, want exactly one of each", claimed, skipped)
	}
	if tool.pollCount() != 1 {
		t.Errorf("Provider polls = %d, want 1", tool.pollCount())
	}
	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptDispatched || attempt.PollCount != 1 || attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(claimedAt.Add(pollableInterval)) {
		t.Errorf("attempt after the race = status %s count %d next %v, want DISPATCHED, 1 and %v", attempt.Status, attempt.PollCount, attempt.NextPollAt, claimedAt.Add(pollableInterval))
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Errorf("state after the race = %+v, want unchanged %+v", after, before)
	}
}

// TestToolAttemptPoll_ProviderQueryFails_OnlyReschedules covers a Provider that gives no
// answer (a 5xx or a transport error): the claim already advanced the poll schedule, the
// error is reported, and nothing else changes -- the Attempt stays DISPATCHED, the Action
// keeps waiting and no Event is appended.
func TestToolAttemptPoll_ProviderQueryFails_OnlyReschedules(t *testing.T) {
	providerDown := errors.New("pollable_task: query provider: 503 service unavailable")
	tool := newPollableTool(registry.PollResult{})
	var polls atomic.Int32
	tool.poll = func(registry.ToolAsyncState) (registry.PollResult, error) {
		polls.Add(1)
		return registry.PollResult{}, providerDown
	}
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-provider-down", tool, pollableSafe, 5, agentHarnessOptions{})
	before := agentSnapshot(t, h, run, outcome)

	h.clock.Advance(pollableInterval)
	polledAt := h.clock.Now()
	got, err := h.svc.PollToolAttempt(h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	if !errors.Is(err, providerDown) {
		t.Fatalf("poll error = %v, want the Provider's error reported", err)
	}
	if got.Kind != service.PollAttemptClaimedNotFinal || got.Status != "" {
		t.Errorf("outcome = %+v, want CLAIMED_NOT_FINAL with no status", got)
	}
	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptDispatched || attempt.Error != nil || attempt.Uncertain != nil {
		t.Errorf("attempt = %+v, want still DISPATCHED without error", attempt)
	}
	if attempt.PollCount != 1 || attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(polledAt.Add(pollableInterval)) {
		t.Errorf("poll schedule = count %d next %v, want 1 and %v", attempt.PollCount, attempt.NextPollAt, polledAt.Add(pollableInterval))
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Errorf("state after the failed query = %+v, want unchanged %+v", after, before)
	}

	// The Reconciler collects the failure and moves on; the next due poll is retried.
	h.clock.Advance(pollableInterval)
	report, runErr := agentReconciler(h).RunOnce(h.ctx)
	if runErr != nil {
		t.Fatalf("reconciler run once: %v", runErr)
	}
	if report.DueToolPollsFound != 1 || len(report.Errors) != 1 || !errors.Is(report.Errors[0], providerDown) {
		t.Errorf("report = %+v, want the due poll found and its Provider error collected", report)
	}
	if polls.Load() != 2 {
		t.Errorf("Provider polls = %d, want 2", polls.Load())
	}
	if got := getToolAttempt(t, h, attempt.ID); got.Status != domain.ToolAttemptDispatched || got.PollCount != 2 {
		t.Errorf("attempt after the second failure = status %s count %d, want DISPATCHED and 2", got.Status, got.PollCount)
	}
}

// listDueToolPolls reads the due Tool polls exactly as the Reconciler's scan does.
func listDueToolPolls(t *testing.T, h *agentHarness, now time.Time) []store.DueToolPoll {
	t.Helper()
	var due []store.DueToolPoll
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		due, err = tx.ToolAttempts().ListDuePolls(ctx, now, 100)
		return err
	}); err != nil {
		t.Fatalf("list due tool polls: %v", err)
	}
	return due
}

// agentActionFailures counts the AGENT_ACTION_FAILED Events whose failure source is the
// given one.
func agentActionFailures(t *testing.T, events []domain.Event, source domain.FailureSource) int {
	t.Helper()
	n := 0
	for _, ev := range events {
		if ev.Type != domain.EventAgentActionFailed {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", ev.Type, err)
		}
		if payload["failureSource"] == string(source) {
			n++
		}
	}
	return n
}

// assertLostTaskReturned proves the UNKNOWN_TASK handback committed exactly once: the
// Attempt FAILED as safe to repeat, the Action FAILED, one PROVIDER_POLL AGENT_ACTION_FAILED
// and exactly one next Turn.
func assertLostTaskReturned(t *testing.T, h *agentHarness, run domain.Run, outcome service.AdvanceOutcome, attemptID string) {
	t.Helper()
	attempt := getToolAttempt(t, h, attemptID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Errorf("attempt = status %s error %+v, want FAILED with EXECUTION_INTERRUPTED", attempt.Status, attempt.Error)
	}
	if got := getAgentAction(h.ctx, t, h.uow, attempt.ActionID).Status; got != domain.AgentActionFailed {
		t.Errorf("action status = %s, want FAILED", got)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s after the handback", *agentRun.Termination)
	}
	if got := readyTurnNos(t, h, agentRun.ID); len(got) != 1 || got[0] != 2 {
		t.Errorf("ready turn numbers = %v, want exactly [2]", got)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if got := agentActionFailures(t, events, domain.FailureProviderPoll); got != 1 {
		t.Errorf("PROVIDER_POLL action failures = %d, want exactly 1", got)
	}
	if got := agentCountEventType(execEventTypes(events), domain.EventAgentActionCompleted); got != 0 {
		t.Errorf("AGENT_ACTION_COMPLETED events = %d, want 0", got)
	}
}

// TestToolAttemptPoll_ReturnRacesCallback_ExactlyOneWinner holds the UNKNOWN_TASK handback
// transaction and the Provider's late SUCCEEDED callback at a barrier after both routed to
// the DISPATCHED Attempt, and releases them together. The conditional update from
// DISPATCHED elects exactly one: the loser changes neither Attempt, Action nor Event
// stream, consumes no Event seq, and reports a duplicate.
func TestToolAttemptPoll_ReturnRacesCallback_ExactlyOneWinner(t *testing.T) {
	tool := newPollableTool(registry.PollResult{Status: registry.PollUnknownTask})
	arrived := make(chan struct{})
	release := make(chan struct{})
	claimed := make(chan struct{})
	var barrier *pollArmedBarrierUoW
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-return-race-callback", tool, pollableSafe, 5, agentHarnessOptions{
		WrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
			barrier = &pollArmedBarrierUoW{UnitOfWork: inner, arrived: arrived, release: release}
			return barrier
		},
		PollHooks: service.PollHooks{AfterClaim: func(context.Context, string) error {
			barrier.armed.Store(true)
			close(claimed)
			return nil
		}},
	})
	token := tool.lastToken(t)
	h.clock.Advance(pollableInterval)

	var wg sync.WaitGroup
	var pollOutcome service.PollToolAttemptOutcome
	var callbackOutcome service.CallbackOutcome
	var pollErr, callbackErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		pollOutcome, pollErr = h.svc.PollToolAttempt(h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	}()
	go func() {
		defer wg.Done()
		<-claimed
		callbackOutcome, callbackErr = h.svc.HandleCallback(h.ctx, service.HandleCallback{
			Token: token, ExternalTaskID: pollableExternalTaskID, Payload: json.RawMessage(agentCallbackSucceeded),
		})
	}()
	<-arrived
	<-arrived
	barrier.armed.Store(false)
	close(release)
	wg.Wait()

	if pollErr != nil || callbackErr != nil {
		t.Fatalf("poll err = %v, callback err = %v, want neither", pollErr, callbackErr)
	}
	returnWon := pollOutcome.Kind == service.PollAttemptReturned
	callbackWon := callbackOutcome.Accepted && !callbackOutcome.Duplicate
	if returnWon == callbackWon {
		t.Fatalf("poll outcome = %+v, callback outcome = %+v, want exactly one winner", pollOutcome, callbackOutcome)
	}
	if !returnWon && pollOutcome.Kind != service.PollAttemptDuplicate {
		t.Errorf("losing poll outcome = %+v, want DUPLICATE", pollOutcome)
	}

	attempt = getToolAttempt(t, h, attempt.ID)
	action := getAgentAction(h.ctx, t, h.uow, attempt.ActionID)
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	failures := agentActionFailures(t, events, domain.FailureProviderPoll)
	completions := agentPollCompletions(t, events, domain.CompletionCallback)
	if returnWon {
		if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTION_INTERRUPTED" || action.Status != domain.AgentActionFailed {
			t.Errorf("after the handback won: attempt %s %+v action %s, want FAILED/EXECUTION_INTERRUPTED and FAILED", attempt.Status, attempt.Error, action.Status)
		}
		if failures != 1 || completions != 0 {
			t.Errorf("PROVIDER_POLL failures = %d, CALLBACK completions = %d, want 1 and 0", failures, completions)
		}
	} else {
		if attempt.Status != domain.ToolAttemptSucceeded || attempt.Error != nil || action.Status != domain.AgentActionSucceeded {
			t.Errorf("after the callback won: attempt %s %+v action %s, want SUCCEEDED and SUCCEEDED", attempt.Status, attempt.Error, action.Status)
		}
		if failures != 0 || completions != 1 {
			t.Errorf("PROVIDER_POLL failures = %d, CALLBACK completions = %d, want 0 and 1", failures, completions)
		}
	}
	if got := agentCountEventType(execEventTypes(events), domain.EventAgentActionFailed) +
		agentCountEventType(execEventTypes(events), domain.EventAgentActionCompleted); got != 1 {
		t.Errorf("action outcome events = %d, want exactly 1: the loser appends nothing", got)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.CurrentContextVersion != 1 {
		t.Errorf("current context version = %d, want 1: exactly one tool round was appended", agentRun.CurrentContextVersion)
	}
	if !agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) || agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 3) {
		t.Errorf("turns after the race: want exactly turn 2 created")
	}
}

// TestToolAttemptPoll_ReturnAfterTimeoutCommitted_WritesNothing covers the handback racing
// the Agent timeout past the deadline: the handback transaction is held after its claim,
// the timeout commits first and owns the Attempt, the Action and the Agent Run, and the
// released handback finds its conditional update affects no row, so it writes nothing,
// consumes no Event seq and reports a duplicate.
func TestToolAttemptPoll_ReturnAfterTimeoutCommitted_WritesNothing(t *testing.T) {
	tool := newPollableTool(registry.PollResult{Status: registry.PollUnknownTask})
	arrived := make(chan struct{})
	release := make(chan struct{})
	var barrier *pollArmedBarrierUoW
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-return-vs-timeout", tool, pollableSafe, 5, agentHarnessOptions{
		WrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
			barrier = &pollArmedBarrierUoW{UnitOfWork: inner, arrived: arrived, release: release}
			return barrier
		},
		PollHooks: service.PollHooks{AfterClaim: func(context.Context, string) error {
			barrier.armed.Store(true)
			return nil
		}},
	})
	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)

	var wg sync.WaitGroup
	var pollOutcome service.PollToolAttemptOutcome
	var pollErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollOutcome, pollErr = h.svc.PollToolAttempt(h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	}()
	// The handback transaction is waiting; the timeout commits meanwhile.
	<-arrived
	barrier.armed.Store(false)
	if err := h.svc.TimeoutAgentRun(h.ctx, outcome.AgentRunID); err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}
	eventsAfterTimeout := listEvents(h.ctx, t, h.uow, run.ID)
	close(release)
	wg.Wait()

	if pollErr != nil {
		t.Fatalf("poll: %v", pollErr)
	}
	if pollOutcome.Kind != service.PollAttemptDuplicate {
		t.Errorf("poll outcome = %+v, want DUPLICATE: the timeout already owned the Attempt", pollOutcome)
	}
	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "TIMEOUT" {
		t.Errorf("attempt = status %s error %+v, want FAILED by the timeout", attempt.Status, attempt.Error)
	}
	if got := getAgentAction(h.ctx, t, h.uow, attempt.ActionID); got.Status != domain.AgentActionFailed || got.Error == nil || got.Error.Code != "TIMEOUT" {
		t.Errorf("action = status %s error %+v, want FAILED by the timeout", got.Status, got.Error)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	if agentRun.CurrentContextVersion != 0 || agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("context version = %d, turn 2 exists = %v, want 0 and false: the handback wrote nothing",
			agentRun.CurrentContextVersion, agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2))
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	if len(events) != len(eventsAfterTimeout) {
		t.Errorf("events after the released handback = %d, want %d: nothing appended", len(events), len(eventsAfterTimeout))
	}
	if got := agentActionFailures(t, events, domain.FailureProviderPoll); got != 0 {
		t.Errorf("PROVIDER_POLL action failures = %d, want 0", got)
	}
	if got := agentActionFailures(t, events, domain.FailureTimeout); got != 1 {
		t.Errorf("TIMEOUT action failures = %d, want 1", got)
	}
}

// TestToolAttemptPoll_ReturnTxFailsBeforeCommit_RolledBackAndCompletedByNextPoll places a
// write failure inside the handback transaction after AGENT_ACTION_FAILED was appended, on
// the Event the round advancement appends next. The whole transaction rolls back: the
// Attempt stays DISPATCHED with only its poll schedule advanced, no Event, Context Version
// or Turn survives, and the next due poll completes the handback.
func TestToolAttemptPoll_ReturnTxFailsBeforeCommit_RolledBackAndCompletedByNextPoll(t *testing.T) {
	tool := newPollableTool(registry.PollResult{Status: registry.PollUnknownTask})
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-return-rollback", tool, pollableSafe, 5, agentHarnessOptions{})

	const collidingEventID = "ev_tool_poll_return_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())
	before := agentSnapshot(t, h, run, outcome)
	// The first Event the handback appends is AGENT_ACTION_FAILED; the collision lands on
	// the one after it.
	h.ids.ForceEventIDAfter(1, collidingEventID)

	h.clock.Advance(pollableInterval)
	polledAt := h.clock.Now()
	_, err := h.svc.PollToolAttempt(h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("poll with a colliding Event ID = %v, want a conflict from the duplicated Event ID", err)
	}
	if tool.pollCount() != 1 {
		t.Fatalf("provider polls = %d, want 1", tool.pollCount())
	}

	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptDispatched || attempt.Error != nil || attempt.Uncertain != nil || attempt.CompletedAt != nil {
		t.Errorf("attempt after the rollback = %+v, want still DISPATCHED without an outcome", attempt)
	}
	if attempt.PollCount != 1 || attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(polledAt.Add(pollableInterval)) {
		t.Errorf("poll schedule = count %d next %v, want 1 and %v: the claim committed on its own", attempt.PollCount, attempt.NextPollAt, polledAt.Add(pollableInterval))
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Errorf("state after the rolled-back handback = %+v, want unchanged %+v", after, before)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.CurrentContextVersion != 0 || agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a Context Version survived the rolled-back handback (current %d)", agentRun.CurrentContextVersion)
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Error("a Turn 2 survived the rolled-back handback")
	}

	// The next due poll repeats the handback and commits it.
	h.clock.Advance(pollableInterval)
	if got := pollToolAttempt(t, h, attempt); got.Kind != service.PollAttemptReturned {
		t.Fatalf("second poll outcome = %+v, want RETURNED", got)
	}
	assertLostTaskReturned(t, h, run, outcome, attempt.ID)
}

// TestToolAttemptPoll_ReturnEnqueueRefused_ReconcilerAdvancesReadyTurn covers the
// post-COMMIT boundary of the handback: the work queue refuses the next Turn, the READY
// Turn is nevertheless committed, and the Reconciler rediscovers and advances it from the
// persisted fact alone.
func TestToolAttemptPoll_ReturnEnqueueRefused_ReconcilerAdvancesReadyTurn(t *testing.T) {
	tool := newPollableTool(registry.PollResult{Status: registry.PollUnknownTask})
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-return-enqueue-refused", tool, pollableSafe, 5, agentHarnessOptions{
		Queue: service.NoopEnqueuer{},
	})

	h.clock.Advance(pollableInterval)
	if got := pollToolAttempt(t, h, attempt); got.Kind != service.PollAttemptReturned {
		t.Fatalf("poll outcome = %+v, want RETURNED", got)
	}
	assertLostTaskReturned(t, h, run, outcome, attempt.ID)
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if turn2 := agentTurnByNo(h.ctx, t, h.uow, agentRun.ID, 2); turn2.Status != domain.AgentTurnReady {
		t.Fatalf("turn 2 status = %s, want READY while the queue refused it", turn2.Status)
	}

	report := agentRunOnce(h, agentReconciler(h))
	if report.ReadyAgentTurnsFound != 1 || report.AgentTurnsAdvanced != 1 {
		t.Fatalf("report = %+v, want the refused READY Turn rediscovered and advanced", report)
	}
	agentRunOnceUntil(t, h, run.ID, domain.RunCompleted)
	agentRun, _ = agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent run termination = %s, want FINAL_RESPONSE from the next Decision", agentTermination(agentRun))
	}
	if tool.executeCount() != 1 {
		t.Errorf("executes = %d, want 1: the lost call is not repeated by the Runtime", tool.executeCount())
	}
}

// TestToolAttemptPoll_CrashAfterClaimBeforeProviderQuery_RestartContinuesSchedule covers
// the process that dies between the committed claim and the Provider query: the claim's
// poll_count and next_poll_at stay committed, the Provider was never asked, and a
// restarted Backend's Reconciler continues from the persisted schedule and completes the
// Attempt from the Provider's answer.
func TestToolAttemptPoll_CrashAfterClaimBeforeProviderQuery_RestartContinuesSchedule(t *testing.T) {
	crashedTool := newPollableTool(pollSucceededResult())
	crashErr := errors.New("process died after the claim committed")
	crashed, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-crash-after-claim", crashedTool, pollableSafe, 5, agentHarnessOptions{
		PollHooks: service.PollHooks{AfterClaim: func(context.Context, string) error { return crashErr }},
	})
	before := agentSnapshot(t, crashed, run, outcome)

	crashed.clock.Advance(pollableInterval)
	claimedAt := crashed.clock.Now()
	_, err := crashed.svc.PollToolAttempt(crashed.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	if !errors.Is(err, crashErr) {
		t.Fatalf("poll stopped after its claim = %v, want the injected crash", err)
	}
	if crashedTool.pollCount() != 0 {
		t.Fatalf("provider polls before the crash = %d, want 0", crashedTool.pollCount())
	}
	attempt = getToolAttempt(t, crashed, attempt.ID)
	if attempt.Status != domain.ToolAttemptDispatched || attempt.PollCount != 1 || attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(claimedAt.Add(pollableInterval)) {
		t.Fatalf("attempt after the crash = status %s count %d next %v, want DISPATCHED, 1 and %v",
			attempt.Status, attempt.PollCount, attempt.NextPollAt, claimedAt.Add(pollableInterval))
	}
	if after := agentSnapshot(t, crashed, run, outcome); after != before {
		t.Errorf("state after the crash = %+v, want unchanged %+v", after, before)
	}
	crashed.dropPendingTurns()

	restartedTool := newPollableTool(pollSucceededResult())
	restarted := pollableRestart(t, crashed, restartedTool, pollableSafe, 5)
	// Not due yet: the restarted Reconciler honours the schedule the claim committed.
	if report := agentRunOnce(restarted, agentReconciler(restarted)); report.DueToolPollsFound != 0 {
		t.Errorf("report before the next poll time = %+v, want nothing due", report)
	}
	restarted.clock.Advance(pollableInterval)
	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.DueToolPollsFound != 1 || report.ToolPollsResumed != 1 {
		t.Fatalf("report after restart = %+v, want the due tool poll discovered and resumed", report)
	}
	if restartedTool.pollCount() != 1 || restartedTool.executeCount() != 0 || crashedTool.pollCount() != 0 {
		t.Errorf("restarted polls = %d executes = %d, crashed polls = %d, want 1, 0 and 0",
			restartedTool.pollCount(), restartedTool.executeCount(), crashedTool.pollCount())
	}
	assertPollResumedOnce(t, restarted, run, outcome, attempt.ID)
	if got := getToolAttempt(t, restarted, attempt.ID); got.PollCount != 2 {
		t.Errorf("poll count after recovery = %d, want 2", got.PollCount)
	}
}

// TestToolAttemptPoll_ReturnPastAgentDeadline_TerminatesTimeoutWithoutNewTurn covers the
// handback of a lost task after the Agent deadline passed: the Attempt and Action still
// fail with EXECUTION_INTERRUPTED, but the round advancement ends the Agent Run with
// TIMEOUT instead of creating a next Turn.
func TestToolAttemptPoll_ReturnPastAgentDeadline_TerminatesTimeoutWithoutNewTurn(t *testing.T) {
	tool := newPollableTool(registry.PollResult{Status: registry.PollUnknownTask})
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-return-past-deadline", tool, pollableSafe, 5, agentHarnessOptions{})
	eventsBefore := len(listEvents(h.ctx, t, h.uow, run.ID))

	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	got := pollToolAttempt(t, h, attempt)
	if got.Kind != service.PollAttemptReturned || !got.Failed || got.FailureSource != domain.FailureProviderPoll {
		t.Fatalf("poll outcome = %+v, want RETURNED with failure source PROVIDER_POLL", got)
	}

	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Errorf("attempt = status %s error %+v, want FAILED with EXECUTION_INTERRUPTED", attempt.Status, attempt.Error)
	}
	if action := getAgentAction(h.ctx, t, h.uow, attempt.ActionID); action.Status != domain.AgentActionFailed {
		t.Errorf("action status = %s, want FAILED", action.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Error("a Turn 2 exists, want none past the deadline")
	}
	if nr := agentNodeRun(h.ctx, t, h.uow, run.ID); nr.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nr.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	tail := execEventTypes(events[eventsBefore:])
	if agentCountEventType(tail, domain.EventAgentActionFailed) != 1 || agentCountEventType(tail, domain.EventAgentFailed) != 1 ||
		agentCountEventType(tail, domain.EventAgentTurnReady) != 0 {
		t.Errorf("events past the deadline = %v, want one AGENT_ACTION_FAILED, one AGENT_FAILED and no AGENT_TURN_READY", tail)
	}
}

// TestToolAttemptPoll_BindingDoesNotRouteToAttempt_ClearsScheduleAndRefuses covers a
// Callback Binding set that does not route to the Attempt: the poll is refused with the
// binding mismatch, the poll schedule is cleared so the scan stops listing the Attempt,
// the Provider is never queried, and the Attempt keeps waiting for its callback or the
// deadline unchanged.
func TestToolAttemptPoll_BindingDoesNotRouteToAttempt_ClearsScheduleAndRefuses(t *testing.T) {
	tool := newPollableTool(pollSucceededResult())
	h, run, outcome, attempt := pollableDispatched(t, "wf-tool-poll-binding-mismatch", tool, pollableSafe, 5, agentHarnessOptions{})
	// A second Binding claiming the same Attempt for another external task: a poll could
	// not tell which task to ask the Provider about, so nothing routes.
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.CallbackBindings().Create(ctx, domain.CallbackBinding{
			ID: "cb_tool_poll_second_route", ProviderID: remotelookup.ProviderID, ExternalTaskID: "provider_task_other",
			TargetType: domain.CallbackTargetToolAttempt, TargetID: attempt.ID, CreatedAt: h.clock.Now(),
		})
	}); err != nil {
		t.Fatalf("create the second binding: %v", err)
	}
	before := agentSnapshot(t, h, run, outcome)

	h.clock.Advance(pollableInterval)
	if due := listDueToolPolls(t, h, h.clock.Now()); len(due) != 1 || due[0].AttemptID != attempt.ID {
		t.Fatalf("due polls before the refused poll = %+v, want the attempt listed", due)
	}
	got, err := h.svc.PollToolAttempt(h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	if !errors.Is(err, service.ErrPollBindingMismatch) {
		t.Fatalf("poll with a binding that does not route = %v, want ErrPollBindingMismatch", err)
	}
	if got.Kind != service.PollAttemptSkipped || got.SkipReason != service.PollSkipBindingMismatch {
		t.Errorf("outcome = %+v, want SKIPPED with BINDING_MISMATCH", got)
	}
	if tool.pollCount() != 0 {
		t.Errorf("provider polls = %d, want 0", tool.pollCount())
	}

	attempt = getToolAttempt(t, h, attempt.ID)
	if attempt.Status != domain.ToolAttemptDispatched || attempt.NextPollAt != nil || attempt.PollCount != 0 {
		t.Errorf("attempt = status %s next %v count %d, want DISPATCHED with the schedule cleared", attempt.Status, attempt.NextPollAt, attempt.PollCount)
	}
	if due := listDueToolPolls(t, h, h.clock.Now().Add(24*time.Hour)); len(due) != 0 {
		t.Errorf("due polls after the cleared schedule = %+v, want none", due)
	}
	if report := agentRunOnce(h, agentReconciler(h)); report.DueToolPollsFound != 0 {
		t.Errorf("reconciler report = %+v, want no due tool poll", report)
	}
	if after := agentSnapshot(t, h, run, outcome); after != before {
		t.Errorf("state after the refused poll = %+v, want unchanged %+v", after, before)
	}
}
