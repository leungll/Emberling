//go:build integration

// Agent Turn advancement tests: the transaction pair that turns one READY Turn into one
// committed Decision. They share the agentHarness and the fixture Definition of
// agent_loop_test.go.
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
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

func agentDecisionOfTurn(ctx context.Context, t *testing.T, uow store.UnitOfWork, turnID string) (domain.AgentDecision, bool) {
	t.Helper()
	action, found := agentActionOfTurn(ctx, t, uow, turnID)
	if !found {
		return domain.AgentDecision{}, false
	}
	var decision domain.AgentDecision
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		decision, err = tx.AgentDecisions().Get(ctx, action.DecisionID)
		return err
	}); err != nil {
		t.Fatalf("get decision %s: %v", action.DecisionID, err)
	}
	return decision, true
}

func agentActionOfTurn(ctx context.Context, t *testing.T, uow store.UnitOfWork, turnID string) (domain.AgentAction, bool) {
	t.Helper()
	var action domain.AgentAction
	found := true
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		action, err = tx.AgentActions().GetByTurnID(ctx, turnID)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("get action of turn %s: %v", turnID, err)
	}
	return action, found
}

func agentToolAttempts(ctx context.Context, t *testing.T, uow store.UnitOfWork, actionID string) []domain.ToolAttempt {
	t.Helper()
	var attempts []domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempts, err = tx.ToolAttempts().ListByActionID(ctx, actionID)
		return err
	}); err != nil {
		t.Fatalf("list tool attempts of action %s: %v", actionID, err)
	}
	return attempts
}

func containsEventType(types []domain.EventType, want domain.EventType) bool {
	return agentCountEventType(types, want) > 0
}

// TestAgentTurn_ReadyToRunningRace_OnlyOneClaimWins proves the conditional Turn claim is
// what elects the single model caller. Two advancement paths enter
// AdvanceAgentTurn for the same READY Turn, released together by an explicit barrier; the
// loser must stop without an Event and, above all, without a second cost-bearing model
// call.
func TestAgentTurn_ReadyToRunningRace_OnlyOneClaimWins(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-turn-race"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)
	if outcome.AgentTurnID == "" {
		t.Fatalf("agent claim reports no ready Turn to advance")
	}

	// The first Generate call is held inside the Provider until the other caller has
	// finished, so the two claims genuinely overlap. A second call (there must be none)
	// returns immediately, so a lost race fails the assertions below instead of deadlocking.
	var generateCalls atomic.Int32
	inFlight := make(chan struct{}, 2)
	release := make(chan struct{})
	h.provider.BeforeReturn = func(context.Context) {
		if generateCalls.Add(1) == 1 {
			inFlight <- struct{}{}
			<-release
		}
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- h.svc.AdvanceAgentTurn(h.ctx, outcome.AgentTurnID, domain.ClaimImmediate)
		}()
	}
	close(start)
	<-inFlight
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("advance agent turn: %v", err)
		}
	}

	if got := generateCalls.Load(); got != 1 {
		t.Errorf("model Generate calls = %d, want exactly 1", got)
	}
	if got := len(h.provider.Requests()); got != 1 {
		t.Errorf("recorded model requests = %d, want exactly 1", got)
	}

	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, nodeRun.ID)
	if got := agentCountEventType(types, domain.EventAgentTurnStarted); got != 1 {
		t.Errorf("AGENT_TURN_STARTED events = %d, want exactly 1 (events: %v)", got, types)
	}
	if got := agentCountEventType(types, domain.EventAgentDecisionCommitted); got != 1 {
		t.Errorf("AGENT_DECISION_COMMITTED events = %d, want exactly 1 (events: %v)", got, types)
	}

	turn := getAgentTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if turn.Status != domain.AgentTurnCompleted {
		t.Errorf("turn status = %s, want COMPLETED", turn.Status)
	}
}

// TestAgentTurn_ModelCallAfterCommit_NotBeforeCommit proves the commit-before-execution
// rule for the Agent Loop: the Provider is called only after the claim transaction has
// committed, and never while the Run aggregate lock is held. The Provider hook reads the
// Turn back on a second database connection: a model call issued inside the claim
// transaction would still see the uncommitted READY row there.
func TestAgentTurn_ModelCallAfterCommit_NotBeforeCommit(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-call-after-commit"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	observer := postgres.NewUnitOfWork(h.pool)
	var observedStatus domain.AgentTurnStatus
	var observedStartedAt *time.Time
	var observedEvents []domain.EventType
	// Only the first Generate call is observed: a later one would overwrite what the
	// earliest model call could see and hide a call issued from inside the claim
	// transaction.
	var observeOnce sync.Once
	h.provider.BeforeReturn = func(ctx context.Context) {
		observeOnce.Do(func() {
			turn := getAgentTurn(ctx, t, observer, outcome.AgentTurnID)
			observedStatus = turn.Status
			observedStartedAt = turn.StartedAt
			observedEvents = agentEventTypesFor(listEvents(ctx, t, observer, run.ID), outcome.NodeRunID)
		})
	}

	if err := h.svc.AdvanceAgentTurn(h.ctx, outcome.AgentTurnID, domain.ClaimImmediate); err != nil {
		t.Fatalf("advance agent turn: %v", err)
	}

	if observedStatus != domain.AgentTurnRunning {
		t.Errorf("turn seen from a second connection during the model call = %s, want RUNNING: the claim had not committed before the Provider was called", observedStatus)
	}
	if observedStartedAt == nil {
		t.Errorf("turn seen from a second connection during the model call has no started_at")
	}
	if !containsEventType(observedEvents, domain.EventAgentTurnStarted) {
		t.Errorf("events committed before the model call = %v, want AGENT_TURN_STARTED among them", observedEvents)
	}
	if containsEventType(observedEvents, domain.EventAgentDecisionCommitted) {
		t.Errorf("AGENT_DECISION_COMMITTED was already committed before the model answered: %v", observedEvents)
	}
}

// TestAgentTurn_ToolCallDecision_CommitsDecisionAndReadyAction covers the model-result
// transaction of a TOOL_CALL round on its own: the Turn completes with its response, the
// immutable Decision and the single READY Action commit with it, and
// AGENT_DECISION_COMMITTED records the pair. The Tool itself is not called by this
// transaction; executing the committed Action is the next, separately claimed step.
//
// The in-process chain is stopped as soon as that transaction commits, which is what makes
// the assertion possible at all: what is observed afterwards is exactly what the Decision
// commit left behind -- a READY Action and no Tool Attempt -- rather than whatever the
// Action execution that normally follows it went on to do.
func TestAgentTurn_ToolCallDecision_CommitsDecisionAndReadyAction(t *testing.T) {
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{
			Kind:          mockmodel.ScenarioToolCall,
			ToolName:      lookup.ToolName,
			ToolArguments: json.RawMessage(`{"key":"ada"}`),
		}
	}
	def := h.saveDefinition(agentLoopDefinition("wf-agent-tool-decision"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	stopCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop.cancel = cancel
	// Armed during the model call, so the notification that stops the chain is the
	// Decision commit's own.
	h.provider.BeforeReturn = func(context.Context) { stop.arm() }
	if err := h.svc.Execute(stopCtx, outcome); !errors.Is(err, context.Canceled) {
		t.Fatalf("execute agent node run = %v, want context.Canceled from the stopped chain", err)
	}

	turn := getAgentTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if turn.Status != domain.AgentTurnCompleted {
		t.Errorf("turn status = %s, want COMPLETED", turn.Status)
	}
	if len(turn.Response) == 0 {
		t.Errorf("completed turn records no response summary")
	}
	if turn.TokenUsage == nil {
		t.Errorf("completed turn records no token usage")
	}
	if turn.CompletedAt == nil {
		t.Errorf("completed turn records no completed_at")
	}
	if turn.Error != nil {
		t.Errorf("completed turn records an error: %+v", *turn.Error)
	}

	decision, found := agentDecisionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no decision was committed for turn %s", outcome.AgentTurnID)
	}
	if decision.Kind != domain.DecisionToolCall {
		t.Errorf("decision kind = %s, want TOOL_CALL", decision.Kind)
	}
	if decision.ToolName == nil || *decision.ToolName != lookup.ToolName {
		t.Errorf("decision toolName = %v, want %s", decision.ToolName, lookup.ToolName)
	}
	assertSameJSON(t, "decision arguments", json.RawMessage(`{"key":"ada"}`), decision.Arguments)
	if len(decision.ResponseSummary) == 0 {
		t.Errorf("committed decision records no bounded response summary")
	}

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action was created for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionReady {
		t.Errorf("action status = %s, want READY: the committed Action is claimed and executed separately", action.Status)
	}
	if action.Type != domain.AgentActionToolCall {
		t.Errorf("action type = %s, want TOOL_CALL", action.Type)
	}
	if action.DecisionID != decision.ID {
		t.Errorf("action decisionId = %s, want %s", action.DecisionID, decision.ID)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts = %d, want 0: committing a Decision must not call the Tool", len(attempts))
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s after a TOOL_CALL decision", *agentRun.Termination)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	committed := agentEventPayload(t, events, domain.EventAgentDecisionCommitted)
	if committed["turnId"] != outcome.AgentTurnID {
		t.Errorf("AGENT_DECISION_COMMITTED turnId = %v, want %s", committed["turnId"], outcome.AgentTurnID)
	}
	if committed["actionId"] != action.ID {
		t.Errorf("AGENT_DECISION_COMMITTED actionId = %v, want %s", committed["actionId"], action.ID)
	}
	if committed["kind"] != string(domain.DecisionToolCall) {
		t.Errorf("AGENT_DECISION_COMMITTED kind = %v, want TOOL_CALL", committed["kind"])
	}
	if committed["toolName"] != lookup.ToolName {
		t.Errorf("AGENT_DECISION_COMMITTED toolName = %v, want %s", committed["toolName"], lookup.ToolName)
	}

	// A committed Decision is a fact the model is never asked to produce again: the READY
	// work left behind is the Action, not a second Turn.
	if turns := listReadyTurns(h.ctx, t, h.uow); len(turns) != 0 {
		t.Errorf("ready turns after a committed decision = %d, want 0", len(turns))
	}
}

// TestAgentTurn_ProviderFailure_TerminatesModelErrorNoDecision covers the failure shape of
// the model-result transaction: a Provider error terminates the Agent Run as MODEL_ERROR
// and fails the Agent NodeRun with the Run, in one transaction, while leaving no Decision
// and no Action behind.
func TestAgentTurn_ProviderFailure_TerminatesModelErrorNoDecision(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFail}
	}
	def := h.saveDefinition(agentLoopDefinition("wf-agent-model-error"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	assertAgentTerminated(t, h, run.ID, outcome, domain.TerminationModelError)
}

// TestAgentDecision_BasicShapeInvalid_TerminatesInvalidActionNoDecisionRow covers the
// other failure of the same transaction: the Provider answered, but the decision envelope
// fails runtime.ParseModelDecision's basic shape check. Nothing about it may be committed
// -- a malformed Decision is not persisted and the model is not asked again -- and the
// Agent Run terminates as INVALID_ACTION rather than MODEL_ERROR.
func TestAgentDecision_BasicShapeInvalid_TerminatesInvalidActionNoDecisionRow(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioInvalidDecision}
	}
	def := h.saveDefinition(agentLoopDefinition("wf-agent-invalid-decision"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	assertAgentTerminated(t, h, run.ID, outcome, domain.TerminationInvalidAction)
}

// assertAgentTerminated is the shared assertion of the two model-result failure paths:
// both commit the same facts and differ only in the recorded termination reason (every
// non-FINAL_RESPONSE termination fails the Agent NodeRun).
func assertAgentTerminated(t *testing.T, h *agentHarness, runID string, outcome service.AdvanceOutcome, want domain.AgentTermination) {
	t.Helper()

	turn := getAgentTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if turn.Status != domain.AgentTurnFailed {
		t.Errorf("turn status = %s, want FAILED", turn.Status)
	}
	if turn.Error == nil {
		t.Errorf("failed turn records no error")
	}
	if turn.CompletedAt == nil {
		t.Errorf("failed turn records no completed_at")
	}

	if _, found := agentDecisionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID); found {
		t.Errorf("a decision was committed for a failed turn")
	}
	if _, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID); found {
		t.Errorf("an action was created for a failed turn")
	}

	agentRun, found := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !found {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.Termination == nil {
		t.Errorf("agent run termination = none, want %s", want)
	} else if *agentRun.Termination != want {
		t.Errorf("agent run termination = %s, want %s", *agentRun.Termination, want)
	}
	if agentRun.TerminatedAt == nil {
		t.Errorf("terminated agent run records no terminated_at")
	}
	if agentRun.Error == nil {
		t.Errorf("terminated agent run records no error")
	}

	nodeRun := agentNodeRun(h.ctx, t, h.uow, runID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if got := agentAttemptCount(h.ctx, t, h.uow, nodeRun.ID); got != 0 {
		t.Errorf("node attempts = %d, want 0", got)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, runID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	assertNoDownstreamNodeRun(h.ctx, t, h.uow, runID)

	events := listEvents(h.ctx, t, h.uow, runID)
	types := agentEventTypesFor(events, nodeRun.ID)
	if agentCountEventType(types, domain.EventAgentFailed) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_FAILED", types)
	}
	if agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("events = %v, want exactly one NODE_FAILED", types)
	}
	if agentCountEventType(types, domain.EventAgentDecisionCommitted) != 0 {
		t.Errorf("events = %v, want no AGENT_DECISION_COMMITTED", types)
	}
	failed := agentEventPayload(t, events, domain.EventAgentFailed)
	if failed["turnId"] != outcome.AgentTurnID {
		t.Errorf("AGENT_FAILED turnId = %v, want %s", failed["turnId"], outcome.AgentTurnID)
	}
	if failed["termination"] != string(want) {
		t.Errorf("AGENT_FAILED termination = %v, want %s", failed["termination"], want)
	}
	nodeFailed := agentEventPayload(t, events, domain.EventNodeFailed)
	if nodeFailed["agentRunId"] != agentRun.ID {
		t.Errorf("NODE_FAILED agentRunId = %v, want %s", nodeFailed["agentRunId"], agentRun.ID)
	}
}

// TestAgentTurn_RestartAfterClaim_RunningTurnIsNotRecalled is the crash-after-COMMIT case
// the persisted-work recovery rule accepts by design: the claim transaction committed,
// then the process died before the model answered. The Turn stays RUNNING, and recovery
// must leave it alone -- the Reconciler rediscovers READY Turns only, because re-issuing
// a model request that may already be in flight buys a duplicate cost-bearing call.
func TestAgentTurn_RestartAfterClaim_RunningTurnIsNotRecalled(t *testing.T) {
	crashed := newAgentHarness(t, agentHarnessOptions{})
	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-restart"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := crashed.claimAgentNode(run.ID)

	crashed.provider.BeforeReturn = func(context.Context) {
		panic("simulated crash after the claim committed, before the model answered")
	}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		_ = crashed.svc.AdvanceAgentTurn(crashed.ctx, outcome.AgentTurnID, domain.ClaimImmediate)
	}()

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})

	turn := getAgentTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if turn.Status != domain.AgentTurnRunning {
		t.Fatalf("turn status after the crash = %s, want RUNNING", turn.Status)
	}
	if ready := listReadyTurns(restarted.ctx, t, restarted.uow); len(ready) != 0 {
		t.Errorf("recoverable ready turns = %d, want 0: a RUNNING Turn is not rediscoverable work", len(ready))
	}

	// Even asked directly -- as a Reconciler scan that raced the crash would -- the
	// restarted process must not call the model again.
	if err := restarted.svc.AdvanceAgentTurn(restarted.ctx, outcome.AgentTurnID, domain.ClaimReconciler); err != nil {
		t.Fatalf("advance a RUNNING turn after restart: %v", err)
	}
	if got := len(restarted.provider.Requests()); got != 0 {
		t.Errorf("model requests made by the restarted process = %d, want 0", got)
	}
	if _, found := agentDecisionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID); found {
		t.Errorf("a decision was committed for a turn whose model call never returned")
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if got := agentCountEventType(types, domain.EventAgentTurnStarted); got != 1 {
		t.Errorf("AGENT_TURN_STARTED events = %d, want exactly 1 (events: %v)", got, types)
	}
}

// TestAgentTurn_RegistryDriftAfterAgentRunCreated_TerminatesModelErrorWithoutModelCall
// covers the mid-run half of a Tool disappearing from the Registry, for the Turn side:
// the Agent Run was created while `lookup` still resolved -- the allowlist is frozen and
// Turn 1 is committed READY -- and then a restarted Backend without that Tool claims the
// Turn. Building the model request discovers the drift inside the claim transaction, so
// the Provider is never called, the claimed Turn fails, the Agent Run terminates as
// MODEL_ERROR, and no Decision exists. The frozen allowlist is not silently shortened to
// make the request buildable.
func TestAgentTurn_RegistryDriftAfterAgentRunCreated_TerminatesModelErrorWithoutModelCall(t *testing.T) {
	first := newAgentHarness(t, agentHarnessOptions{})
	def := first.saveDefinition(agentLoopDefinition("wf-agent-turn-registry-drift"))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	// The claim transaction created the Agent Run (allowlist resolved and frozen) and
	// committed Turn 1 READY; nothing has advanced it yet.
	outcome := first.claimAgentNode(run.ID)
	if outcome.AgentTurnID == "" {
		t.Fatalf("agent claim reports no ready Turn to advance")
	}
	before, found := agentRunOfNodeRun(first.ctx, t, first.uow, outcome.NodeRunID)
	if !found {
		t.Fatalf("no agent run was created for node run %s", outcome.NodeRunID)
	}
	if len(before.AllowedTools) != 1 || before.AllowedTools[0] != lookup.ToolName {
		t.Fatalf("frozen allowedTools = %v, want [%s]", before.AllowedTools, lookup.ToolName)
	}
	if ready := listReadyTurns(first.ctx, t, first.uow); len(ready) != 1 || ready[0].ID != outcome.AgentTurnID {
		t.Fatalf("ready turns before the restart = %+v, want exactly the claimed agent's Turn 1", ready)
	}

	// A second Backend over the same committed facts, whose Tool Registry no longer has
	// `lookup`. Its Reconciler would rediscover the READY Turn, so the claim is entered
	// exactly as that pass would enter it.
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool: first.pool, SkipLookupTool: true, Clock: first.clock,
	})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "must never be produced"})

	if err := restarted.svc.AdvanceAgentTurn(restarted.ctx, outcome.AgentTurnID, domain.ClaimReconciler); err != nil {
		t.Fatalf("advance agent turn against the drifted registry: %v", err)
	}

	if got := generated.Load(); got != 0 {
		t.Errorf("model calls = %d, want 0: an unresolvable allowlist has nothing to send to a Provider", got)
	}
	if got := len(restarted.provider.Requests()); got != 0 {
		t.Errorf("recorded model requests = %d, want 0", got)
	}

	// Shared terminal shape: Turn FAILED, no Decision, no Action, Agent Run terminated as
	// MODEL_ERROR, Agent NodeRun and Run FAILED, one AGENT_FAILED and one NODE_FAILED.
	assertAgentTerminated(t, restarted, run.ID, outcome, domain.TerminationModelError)

	turn := getAgentTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if turn.Error == nil || turn.Error.Code != "MODEL_ERROR" {
		t.Errorf("failed turn error = %+v, want code MODEL_ERROR", turn.Error)
	}
	if turn.Error != nil && !strings.Contains(turn.Error.Message, lookup.ToolName) {
		t.Errorf("failed turn error message = %q, want it to name the missing tool %q", turn.Error.Message, lookup.ToolName)
	}

	after, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if len(after.AllowedTools) != 1 || after.AllowedTools[0] != lookup.ToolName {
		t.Errorf("frozen allowedTools after the drift = %v, want %v unchanged: a missing Tool is never silently dropped",
			after.AllowedTools, before.AllowedTools)
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if len(types) < 3 ||
		types[len(types)-3] != domain.EventAgentTurnStarted ||
		types[len(types)-2] != domain.EventAgentFailed ||
		types[len(types)-1] != domain.EventNodeFailed {
		t.Errorf("agent node run events = %v, want them to end with AGENT_TURN_STARTED, AGENT_FAILED, NODE_FAILED", types)
	}
	if agentCountEventType(types, domain.EventAgentStarted) != 1 || agentCountEventType(types, domain.EventAgentTurnReady) != 1 {
		t.Errorf("agent node run events = %v, want the pre-drift AGENT_STARTED and AGENT_TURN_READY retained exactly once", types)
	}
	started := agentOnlyPayloadFor(t, events, domain.EventAgentTurnStarted, "turnId", outcome.AgentTurnID)
	if started["claimSource"] != string(domain.ClaimReconciler) {
		t.Errorf("AGENT_TURN_STARTED claimSource = %v, want RECONCILER", started["claimSource"])
	}
}
