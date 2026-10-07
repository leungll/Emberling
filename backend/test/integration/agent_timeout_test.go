//go:build integration

// Agent timeout tests: the single transaction that an expired Agent deadline commits,
// entered directly rather than through the Reconciler scan that agent_reconcile_test.go
// covers.
//
// What they prove is what a mock repository cannot (CLAUDE.md testing standard): a Tool
// result that arrives after the timeout already committed changes nothing, a Tool call
// that runs out of the deadline terminates TIMEOUT and never TOOL_ERROR, and when the
// timeout transaction and the result transaction genuinely race, the conditional updates
// let exactly one of them commit a complete outcome, and the other cannot change the
// terminal state.
//
// They share the agentHarness and the fixture Definition of agent_loop_test.go.
package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// agentPastDeadlineClock starts the injected clock a year before the shared fixture time,
// which is what makes the bounded call context the Agent Tool call runs under genuinely
// expired.
//
// The service derives that context with context.WithDeadline from the Agent Run's frozen
// deadline (a stored timestamp taken from the injected clock), and context.WithDeadline is
// always measured against real time. A test therefore cannot make a Tool observe a
// deadline-exceeded context by advancing the injected clock alone; it has to start the
// injected clock far enough in the real past that the stored deadline is also in the real
// past. A year is used so the test never depends on how close the fixture time happens to
// be to the day it runs.
var agentPastDeadlineClock = fixtureTime.AddDate(-1, 0, 0)

// agentAbandoningTool is a Tool Executor that does what a well-behaved Executor does when
// the Agent Run's deadline runs out during its call: it waits on the call context and
// reports that context's own error, without writing Runtime state or retrying itself
// (CLAUDE.md: an extension owns no retry, timeout or state transition).
type agentAbandoningTool struct {
	mu        sync.Mutex
	calls     int
	sawExpiry bool
}

func (a *agentAbandoningTool) Execute(ctx context.Context, _ registry.ToolAction) (registry.ToolExecutionResult, error) {
	<-ctx.Done()
	a.mu.Lock()
	a.calls++
	a.sawExpiry = ctx.Err() == context.DeadlineExceeded
	a.mu.Unlock()
	return registry.ToolExecutionResult{}, fmt.Errorf("%s: abandoned the call: %w", lookup.ToolName, ctx.Err())
}

func (a *agentAbandoningTool) observed() (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls, a.sawExpiry
}

// agentPausedTool holds the one Tool call of the fixture Agent Loop open until the test
// releases it, then answers successfully. It is the explicit barrier the timeout races in
// this file are built on: no test here sleeps or depends on scheduling order.
type agentPausedTool struct {
	inFlight chan struct{}
	release  chan struct{}
	once     sync.Once
}

func newAgentPausedTool() *agentPausedTool {
	return &agentPausedTool{inFlight: make(chan struct{}), release: make(chan struct{})}
}

func (p *agentPausedTool) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	p.once.Do(func() { close(p.inFlight) })
	<-p.release
	return lookup.Executor{}.Execute(ctx, action)
}

// ---------------------------------------------------------------------------
// A result that arrives after the timeout committed
// ---------------------------------------------------------------------------

// TestAgentTimeout_LateToolResultAfterTimeout_WritesNothing covers the rule that a model
// result or callback arriving after the timeout cannot change the terminal state. The
// timeout transaction commits while the Tool is still running; when the Tool then answers
// successfully, its result transaction must find every conditional update already lost
// and leave the committed terminal facts exactly as they are.
func TestAgentTimeout_LateToolResultAfterTimeout_WritesNothing(t *testing.T) {
	paused := newAgentPausedTool()
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: paused})
	agentScriptToolCallThenFinal(h, agentToolCallScenario())
	def := h.saveDefinition(agentLoopDefinition("wf-agent-timeout-late-result"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	executed := make(chan error, 1)
	go func() { executed <- h.svc.Execute(h.ctx, outcome) }()

	// The Tool is now inside its call, so the claim transaction has committed and the
	// result transaction has not started.
	<-paused.inFlight
	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	if err := h.svc.TimeoutAgentRun(h.ctx, outcome.AgentRunID); err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action was committed for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionFailed {
		t.Fatalf("action status after the timeout = %s, want FAILED", action.Status)
	}
	attemptsAfterTimeout := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attemptsAfterTimeout) != 1 || attemptsAfterTimeout[0].Status != domain.ToolAttemptFailed {
		t.Fatalf("tool attempts after the timeout = %+v, want exactly one FAILED", attemptsAfterTimeout)
	}
	eventsAfterTimeout := listEvents(h.ctx, t, h.uow, run.ID)
	agentRunAfterTimeout, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRunAfterTimeout.Termination == nil || *agentRunAfterTimeout.Termination != domain.TerminationTimeout {
		t.Fatalf("agent run termination = %v, want TIMEOUT", agentTermination(agentRunAfterTimeout))
	}

	// The same expired Agent Run reached a second time -- the Reconciler scan racing the
	// in-process deadline check -- must find the outcome already committed and write
	// nothing more: the transaction that committed first owns it.
	if err := h.svc.TimeoutAgentRun(h.ctx, outcome.AgentRunID); err != nil {
		t.Fatalf("second timeout of the same agent run: %v", err)
	}
	if again := listEvents(h.ctx, t, h.uow, run.ID); len(again) != len(eventsAfterTimeout) {
		t.Fatalf("events after a repeated timeout = %d, want it unchanged at %d (%v)",
			len(again), len(eventsAfterTimeout), execEventTypes(again))
	}

	// The Tool now answers successfully, far too late to matter.
	close(paused.release)
	if err := <-executed; err != nil {
		t.Fatalf("the late Tool result transaction returned an error instead of writing nothing: %v", err)
	}

	late := getAgentAction(h.ctx, t, h.uow, action.ID)
	if late.Status != domain.AgentActionFailed {
		t.Errorf("action status after the late Tool result = %s, want it unchanged at FAILED", late.Status)
	}
	attemptsAfterResult := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attemptsAfterResult) != 1 {
		t.Fatalf("tool attempts after the late result = %d, want it unchanged at 1", len(attemptsAfterResult))
	}
	if attemptsAfterResult[0].Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempt status after the late result = %s, want it unchanged at FAILED", attemptsAfterResult[0].Status)
	}
	if attemptsAfterResult[0].Result != nil {
		t.Errorf("the late Tool result was written onto a FAILED attempt: %s", attemptsAfterResult[0].Result)
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("the late Tool result appended a Context Version to a terminated Agent Run")
	}
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("the late Tool result created a State Version on a terminated Agent Run")
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("the late Tool result started a next Turn on a terminated Agent Run")
	}
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination after the late result = %v, want it unchanged at TIMEOUT", agentTermination(agentRun))
	}
	eventsAfterResult := listEvents(h.ctx, t, h.uow, run.ID)
	if len(eventsAfterResult) != len(eventsAfterTimeout) {
		t.Errorf("events after the late Tool result = %d, want it unchanged at %d (%v)",
			len(eventsAfterResult), len(eventsAfterTimeout), execEventTypes(eventsAfterResult))
	}
}

// ---------------------------------------------------------------------------
// A Tool call that runs past the deadline
// ---------------------------------------------------------------------------

// TestAgentTimeout_ToolExceedsDeadline_TerminatesTimeoutNotToolError covers an Agent
// deadline that expires while a Tool runs or waits: the timeout use case wins completion,
// the termination is TIMEOUT and is never misrecorded as TOOL_ERROR. The Tool reports an
// error, but it is the deadline's error, so recording it as the Tool's failure would tell
// Trace the wrong story about why the Agent Run stopped.
func TestAgentTimeout_ToolExceedsDeadline_TerminatesTimeoutNotToolError(t *testing.T) {
	tool := &agentAbandoningTool{}
	h := newAgentHarness(t, agentHarnessOptions{
		LookupExecutor: tool, Clock: newExecClock(agentPastDeadlineClock),
	})
	agentScriptToolCallThenFinal(h, agentToolCallScenario())
	def := h.saveDefinition(agentLoopDefinition("wf-agent-timeout-tool-deadline"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	// The frozen deadline has passed on the injected clock as well, which is what lets the
	// timeout transaction commit rather than decide the Agent Run is still live.
	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	h.execute(outcome)

	calls, sawExpiry := tool.observed()
	if calls != 1 {
		t.Fatalf("tool calls = %d, want exactly 1", calls)
	}
	if !sawExpiry {
		t.Fatalf("the tool was not handed a context bounded by the agent deadline")
	}

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action was committed for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionFailed {
		t.Errorf("action status = %s, want FAILED", action.Status)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1", len(attempts))
	}
	if attempts[0].Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempt status = %s, want FAILED", attempts[0].Status)
	}
	if attempts[0].Error == nil || attempts[0].Error.Code != "TIMEOUT" {
		t.Errorf("tool attempt error = %+v, want code TIMEOUT rather than the Tool's own failure", attempts[0].Error)
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("agent run termination = %v, want TIMEOUT, never TOOL_ERROR", agentTermination(agentRun))
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureTimeout) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want TIMEOUT", failed["failureSource"])
	}
	if failed["toolAttemptId"] != attempts[0].ID {
		t.Errorf("AGENT_ACTION_FAILED toolAttemptId = %v, want %s", failed["toolAttemptId"], attempts[0].ID)
	}
	if payload := agentEventPayload(t, events, domain.EventAgentFailed); payload["termination"] != string(domain.TerminationTimeout) {
		t.Errorf("AGENT_FAILED termination = %v, want TIMEOUT", payload["termination"])
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("a next Turn was created for a timed-out Agent Run")
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
}

// ---------------------------------------------------------------------------
// Timeout racing the Tool result
// ---------------------------------------------------------------------------

// TestAgentTimeout_ConcurrentTimeoutAndResult_OnlyOneWins is the
// single-winner conditional-update rule for the Agent
// deadline: the timeout transaction and the successful Tool result transaction are
// released together by an explicit barrier and both try to close the same Action out. Only
// one may commit, and whichever one does must leave a complete, internally consistent
// outcome -- never a mixture such as a SUCCEEDED Attempt beside an AGENT_ACTION_FAILED.
//
// Both outcomes terminate the Agent Run as TIMEOUT: the clock is already past the frozen
// deadline, so even the winning result transaction's own post-success deadline check stops
// the loop rather than starting a round that could never finish.
func TestAgentTimeout_ConcurrentTimeoutAndResult_OnlyOneWins(t *testing.T) {
	paused := newAgentPausedTool()
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: paused})
	agentScriptToolCallThenFinal(h, agentToolCallScenario())
	def := h.saveDefinition(agentLoopDefinition("wf-agent-timeout-race"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	executed := make(chan error, 1)
	go func() { executed <- h.svc.Execute(h.ctx, outcome) }()

	<-paused.inFlight
	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)

	start := make(chan struct{})
	timedOut := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		timedOut <- h.svc.TimeoutAgentRun(h.ctx, outcome.AgentRunID)
	}()

	// The barrier: the timeout transaction begins and the held Tool answers at the same
	// time, so the two result transactions genuinely overlap.
	close(start)
	close(paused.release)
	wg.Wait()

	if err := <-timedOut; err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}
	if err := <-executed; err != nil {
		t.Fatalf("execute agent node run: %v", err)
	}

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action was committed for turn %s", outcome.AgentTurnID)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1: the MVP never retries an Agent Tool", len(attempts))
	}
	attempt := attempts[0]
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	events := listEvents(h.ctx, t, h.uow, run.ID)
	completedEvents := agentPayloadsFor(t, events, domain.EventAgentActionCompleted, "actionId", action.ID)
	failedEvents := agentPayloadsFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)

	// Facts both outcomes owe, whichever transaction won.
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("agent run termination = %v, want TIMEOUT from either winner", agentTermination(agentRun))
	}
	if got := agentCountEventType(agentEventTypesFor(events, outcome.NodeRunID), domain.EventAgentFailed); got != 1 {
		t.Errorf("AGENT_FAILED events = %d, want exactly 1: only one transaction may terminate the Agent Run", got)
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("a next Turn was created past the deadline")
	}
	if len(completedEvents)+len(failedEvents) != 1 {
		t.Fatalf("the raced Action recorded %d completed and %d failed events, want exactly one outcome event in total",
			len(completedEvents), len(failedEvents))
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}

	switch attempt.Status {
	case domain.ToolAttemptSucceeded:
		// The result transaction won: the Tool's answer is preserved in full, and the
		// deadline stopped the loop only afterwards.
		if action.Status != domain.AgentActionSucceeded {
			t.Errorf("action status = %s beside a SUCCEEDED attempt, want SUCCEEDED", action.Status)
		}
		if len(completedEvents) != 1 {
			t.Errorf("a SUCCEEDED attempt recorded no AGENT_ACTION_COMPLETED")
		}
		assertSameJSON(t, "tool attempt result", []byte(agentToolResult), attempt.Result)
		if !agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
			t.Errorf("a successful Tool round committed no Context Version")
		}
	case domain.ToolAttemptFailed:
		// The timeout transaction won: nothing of the Tool's answer was applied.
		if action.Status != domain.AgentActionFailed {
			t.Errorf("action status = %s beside a FAILED attempt, want FAILED", action.Status)
		}
		if len(failedEvents) != 1 {
			t.Fatalf("a FAILED attempt recorded no AGENT_ACTION_FAILED")
		}
		if failedEvents[0]["failureSource"] != string(domain.FailureTimeout) {
			t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want TIMEOUT", failedEvents[0]["failureSource"])
		}
		if attempt.Error == nil || attempt.Error.Code != "TIMEOUT" {
			t.Errorf("tool attempt error = %+v, want code TIMEOUT", attempt.Error)
		}
		if attempt.Result != nil {
			t.Errorf("a timed-out attempt kept the Tool's result: %s", attempt.Result)
		}
		if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
			t.Errorf("the losing Tool result still appended a Context Version")
		}
	default:
		t.Fatalf("tool attempt status = %s, want one of SUCCEEDED or FAILED", attempt.Status)
	}
}
