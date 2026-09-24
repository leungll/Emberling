//go:build integration

// Agent Action execution tests: the transaction pair that executes one committed
// TOOL_CALL Action and, on success, starts the next Turn. They cover what a mock
// repository cannot prove (CLAUDE.md testing standard): the conditional READY -> RUNNING
// Action claim that elects one executor, the "Tool call strictly after COMMIT" ordering,
// the single result transaction that commits Tool result, Context append, optional State
// Version and the next READY Turn, and the failure transactions that leave no
// half-applied round behind.
//
// They share the agentHarness and the fixture Definition of agent_loop_test.go.
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
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// agentToolArguments is the fixture TOOL_CALL's arguments, and agentToolResult the
// deterministic result the `lookup` Tool returns for them.
const (
	agentToolArguments = `{"key":"k1"}`
	agentToolResult    = `{"key":"k1","record":"record for k1"}`
)

// agentSecondToolName is a Tool that is registered in this Backend but is not part of the
// fixture Agent Run's frozen allowlist.
const agentSecondToolName = "vault"

func agentSecondToolRegistration(executor registry.ToolExecutor) registry.ToolRegistration {
	metadata := lookup.Registration().Metadata
	metadata.Name = agentSecondToolName
	metadata.Description = "Registered beside lookup, outside the fixture Agent's frozen allowlist"
	return registry.ToolRegistration{Metadata: metadata, Executor: executor}
}

// agentRecordingTool wraps one registered Tool Executor. It records every call, so a test
// can prove the Tool ran exactly once or not at all, and exposes a hook that runs inside
// the call: the only moment at which a test can observe which facts the claim transaction
// had to commit before the Tool was allowed to run.
type agentRecordingTool struct {
	delegate registry.ToolExecutor

	// during is set before the Run starts and only read afterwards.
	during func(ctx context.Context, action registry.ToolAction)

	mu    sync.Mutex
	calls []registry.ToolAction
}

func (r *agentRecordingTool) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, action)
	r.mu.Unlock()
	if r.during != nil {
		r.during(ctx, action)
	}
	return r.delegate.Execute(ctx, action)
}

func (r *agentRecordingTool) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *agentRecordingTool) lastAction(t *testing.T) registry.ToolAction {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatalf("the tool was never called")
	}
	return r.calls[len(r.calls)-1]
}

// agentStopNotifier stops the in-process Agent Loop at one chosen post-COMMIT moment.
//
// It is an EventNotifier because that is the only hook the service exposes between a
// committed transaction and the advancement it chains next. Arming it from inside a model
// or Tool call makes the very next notification the one that transaction publishes, so
// the cancelled context reaches the chained step and nothing before it. What it
// reproduces is the crash-after-COMMIT shape of docs/09-testing-and-acceptance.md: the
// facts are committed, the in-process continuation never happens, and what is left behind
// must be recoverable READY work.
type agentStopNotifier struct {
	cancel context.CancelFunc
	armed  atomic.Bool
}

func (n *agentStopNotifier) arm() { n.armed.Store(true) }

func (n *agentStopNotifier) EventsCommitted(string, int64) {
	if n.armed.CompareAndSwap(true, false) {
		n.cancel()
	}
}

// agentScriptToolCallThenFinal makes the fixture model decide one TOOL_CALL on its first
// Turn and answer FINAL on every later one, so a test drives exactly one Tool round: the
// loop then stops at the next Turn's READY FINAL Action, which the Final completion use
// case owns. The returned counter records how often the model was called.
func agentScriptToolCallThenFinal(h *agentHarness, toolCall mockmodel.Scenario) *atomic.Int32 {
	var generated atomic.Int32
	toolCall.Kind = mockmodel.ScenarioToolCall
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if generated.Add(1) > 1 {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
		call := toolCall
		return &call
	}
	return &generated
}

func agentContextMessages(t *testing.T, raw json.RawMessage) []runtime.AgentContextMessage {
	t.Helper()
	messages, err := runtime.DecodeAgentContext(raw)
	if err != nil {
		t.Fatalf("decode context messages: %v", err)
	}
	return messages
}

func agentHasContextVersion(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, version int) bool {
	t.Helper()
	found := true
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.AgentContextVersions().GetByRunAndVersion(ctx, agentRunID, version)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("get context version %d of agent run %s: %v", version, agentRunID, err)
	}
	return found
}

func agentHasStateVersion(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, version int) bool {
	t.Helper()
	found := true
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.AgentStateVersions().GetByRunAndVersion(ctx, agentRunID, version)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("get state version %d of agent run %s: %v", version, agentRunID, err)
	}
	return found
}

// agentLastEventPayload decodes the most recent Event of one type. Several types occur
// once per round, so a test that asserts about the round it just drove must not read the
// first one.
func agentLastEventPayload(t *testing.T, events []domain.Event, typ domain.EventType) map[string]any {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != typ {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(events[i].Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", typ, err)
		}
		return payload
	}
	t.Fatalf("no %s event among %d events", typ, len(events))
	return nil
}

func agentPointers(run domain.AgentRun) [3]int {
	return [3]int{run.CurrentTurnNo, run.CurrentContextVersion, run.CurrentStateVersion}
}

// ---------------------------------------------------------------------------
// Tool call success
// ---------------------------------------------------------------------------

// TestAgentAction_ToolCallSuccess_CommitsResultContextAndNextReadyTurn covers the whole
// successful Tool round of docs/06-execution-model.md §1.7: the claim transaction creates
// the STARTED Tool Attempt and AGENT_ACTION_STARTED, and the result transaction commits
// the Tool result, the appended Context Version, the Action's SUCCEEDED status and the
// next READY Turn together.
//
// The result transaction hands the next Turn to the work enqueuer, which this harness
// only records, so the next Turn is observed exactly as it was committed: READY, with its
// immutable Request, and therefore recoverable work rather than a continuation held in
// memory.
func TestAgentAction_ToolCallSuccess_CommitsResultContextAndNextReadyTurn(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	generated := agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-success"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		t.Fatalf("execute agent node run: %v", err)
	}
	if got := tool.count(); got != 1 {
		t.Fatalf("tool calls = %d, want exactly 1", got)
	}
	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: executing a committed Action asks no model", got)
	}

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action was created for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", action.Status)
	}
	if action.StartedAt == nil || action.CompletedAt == nil {
		t.Errorf("executed action records started_at=%v completed_at=%v, want both", action.StartedAt, action.CompletedAt)
	}
	if action.WaitingAt != nil {
		t.Errorf("a synchronous Tool Action recorded waiting_at = %v", *action.WaitingAt)
	}

	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1: the MVP never retries an Agent Tool", len(attempts))
	}
	attempt := attempts[0]
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	if attempt.AttemptNo != 1 || attempt.ToolName != lookup.ToolName {
		t.Errorf("tool attempt = (no %d, tool %s), want (1, %s)", attempt.AttemptNo, attempt.ToolName, lookup.ToolName)
	}
	assertSameJSON(t, "tool attempt input", json.RawMessage(agentToolArguments), attempt.Input)
	assertSameJSON(t, "tool attempt result", json.RawMessage(agentToolResult), attempt.Result)
	if attempt.CallbackTokenHash != nil {
		t.Errorf("a synchronous Tool call persisted a callback token hash")
	}
	if attempt.CompletedAt == nil {
		t.Errorf("succeeded tool attempt records no completed_at")
	}

	called := tool.lastAction(t)
	if called.ActionID != action.ID || called.AttemptNo != 1 || called.ToolName != lookup.ToolName {
		t.Errorf("tool action = %+v, want the committed Action %s attempt 1", called, action.ID)
	}
	if called.Callback != nil {
		t.Errorf("a synchronous Tool call was handed a callback context")
	}

	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s after a successful Tool round", *agentRun.Termination)
	}
	if got := agentPointers(agentRun); got != [3]int{2, 1, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [2 1 0]", got)
	}

	contextV1 := agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 1)
	if contextV1.SourceTurnID == nil || *contextV1.SourceTurnID != outcome.AgentTurnID {
		t.Errorf("context version 1 sourceTurnId = %v, want %s", contextV1.SourceTurnID, outcome.AgentTurnID)
	}
	messages := agentContextMessages(t, contextV1.Messages)
	if len(messages) != 3 {
		t.Fatalf("context version 1 holds %d messages, want 3 (version 0's message plus the Decision and the result)", len(messages))
	}
	previous := agentContextMessages(t, agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 0).Messages)
	if messages[0].Role != previous[0].Role || string(messages[0].Content) != string(previous[0].Content) {
		t.Errorf("context version 1 rewrote version 0's message: %+v, want %+v", messages[0], previous[0])
	}
	if messages[1].Role != runtime.AgentRoleAssistant {
		t.Errorf("appended message 1 role = %q, want assistant (the committed TOOL_CALL Decision)", messages[1].Role)
	}
	if messages[2].Role != runtime.AgentRoleTool {
		t.Errorf("appended message 2 role = %q, want tool (the Tool result)", messages[2].Role)
	}
	if messages[2].ToolActionID == nil || *messages[2].ToolActionID != action.ID {
		t.Errorf("tool message toolActionId = %v, want the stable Action ID %s", messages[2].ToolActionID, action.ID)
	}
	if messages[2].ToolName == nil || *messages[2].ToolName != lookup.ToolName {
		t.Errorf("tool message toolName = %v, want %s", messages[2].ToolName, lookup.ToolName)
	}
	assertSameJSON(t, "tool message content", json.RawMessage(agentToolResult), messages[2].Content)

	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a State Version was created for a Decision that carries no state patch")
	}

	readyTurns := listReadyTurns(h.ctx, t, h.uow)
	if len(readyTurns) != 1 {
		t.Fatalf("ready turns = %d, want exactly 1: the result transaction commits the next round as recoverable work", len(readyTurns))
	}
	next := readyTurns[0]
	if next.AgentRunID != agentRun.ID || next.TurnNo != 2 {
		t.Errorf("next turn = (agent run %s, turn no %d), want (%s, 2)", next.AgentRunID, next.TurnNo, agentRun.ID)
	}
	if next.StartedAt != nil {
		t.Errorf("the next turn has started_at = %v, want nil", *next.StartedAt)
	}
	var request struct {
		ModelID        string `json:"modelId"`
		ContextVersion int    `json:"contextVersion"`
		StateVersion   int    `json:"stateVersion"`
		MessageCount   int    `json:"messageCount"`
	}
	if err := json.Unmarshal(next.Request, &request); err != nil {
		t.Fatalf("decode next turn request: %v", err)
	}
	if request.ModelID != mockmodel.ModelID || request.ContextVersion != 1 || request.StateVersion != 0 || request.MessageCount != 3 {
		t.Errorf("next turn request = %+v, want the frozen model with context 1, state 0 and 3 messages", request)
	}

	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING: a finished Tool is not a finished Agent", nodeRun.Status)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if len(types) < 3 ||
		types[len(types)-3] != domain.EventAgentActionStarted ||
		types[len(types)-2] != domain.EventAgentActionCompleted ||
		types[len(types)-1] != domain.EventAgentTurnReady {
		t.Errorf("agent node run events = %v, want them to end with AGENT_ACTION_STARTED, AGENT_ACTION_COMPLETED, AGENT_TURN_READY", types)
	}
	if agentCountEventType(types, domain.EventAgentStateUpdated) != 0 {
		t.Errorf("events = %v, want no AGENT_STATE_UPDATED: no new State Version was created", types)
	}
	started := agentEventPayload(t, events, domain.EventAgentActionStarted)
	if started["turnId"] != outcome.AgentTurnID || started["actionId"] != action.ID {
		t.Errorf("AGENT_ACTION_STARTED = %v, want turn %s action %s", started, outcome.AgentTurnID, action.ID)
	}
	if started["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_STARTED toolAttemptId = %v, want %s", started["toolAttemptId"], attempt.ID)
	}
	if started["claimSource"] != string(domain.ClaimImmediate) {
		t.Errorf("AGENT_ACTION_STARTED claimSource = %v, want IMMEDIATE", started["claimSource"])
	}
	completed := agentEventPayload(t, events, domain.EventAgentActionCompleted)
	if completed["actionId"] != action.ID || completed["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_COMPLETED = %v, want action %s attempt %s", completed, action.ID, attempt.ID)
	}
	if completed["completionSource"] != string(domain.CompletionSyncExecution) {
		t.Errorf("AGENT_ACTION_COMPLETED completionSource = %v, want SYNC_EXECUTION", completed["completionSource"])
	}
	turnReady := agentLastEventPayload(t, events, domain.EventAgentTurnReady)
	if turnReady["turnId"] != next.ID {
		t.Errorf("AGENT_TURN_READY turnId = %v, want the new turn %s", turnReady["turnId"], next.ID)
	}
	if turnReady["turnNo"] != float64(2) || turnReady["contextVersion"] != float64(1) || turnReady["stateVersion"] != float64(0) {
		t.Errorf("AGENT_TURN_READY = %v, want turn 2 over context 1 and state 0", turnReady)
	}
}

// TestAgentAction_ToolCallWithStatePatch_CreatesStateVersionAndEvent covers the optional
// half of the same transaction (docs/05-data-model.md §2.3, docs/09 §1): a patch that
// really changes the State creates one new State Version and writes AGENT_STATE_UPDATED
// recording the previous and next State Version and the Context Version committed with
// them.
func TestAgentAction_ToolCallWithStatePatch_CreatesStateVersionAndEvent(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName:      lookup.ToolName,
		ToolArguments: json.RawMessage(agentToolArguments),
		StatePatch:    json.RawMessage(`{"seen":["k1"]}`),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-state-patch"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	// Context 2 is the second Turn's Final Context Version; the State pointer stays at the
	// single Version this Tool Call's patch created.
	if got := agentPointers(agentRun); got != [3]int{2, 2, 1} {
		t.Errorf("pointers (turn, context, state) = %v, want [2 2 1]", got)
	}
	stateV1 := agentStateVersion(h.ctx, t, h.uow, agentRun.ID, 1)
	assertSameJSON(t, "state version 1", json.RawMessage(`{"seen":["k1"]}`), stateV1.Value)
	if stateV1.SourceTurnID == nil || *stateV1.SourceTurnID != outcome.AgentTurnID {
		t.Errorf("state version 1 sourceTurnId = %v, want %s", stateV1.SourceTurnID, outcome.AgentTurnID)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentStateUpdated) != 1 {
		t.Fatalf("events = %v, want exactly one AGENT_STATE_UPDATED", types)
	}
	updated := agentEventPayload(t, events, domain.EventAgentStateUpdated)
	if updated["turnId"] != outcome.AgentTurnID {
		t.Errorf("AGENT_STATE_UPDATED turnId = %v, want %s", updated["turnId"], outcome.AgentTurnID)
	}
	if updated["previousStateVersion"] != float64(0) || updated["stateVersion"] != float64(1) {
		t.Errorf("AGENT_STATE_UPDATED versions = %v, want previous 0 and next 1", updated)
	}
	if updated["contextVersion"] != float64(1) {
		t.Errorf("AGENT_STATE_UPDATED contextVersion = %v, want the Context Version committed with it (1)", updated["contextVersion"])
	}
}

// TestAgentAction_ReadyToRunningRace_OnlyOneClaimWins proves the conditional Action claim
// is what elects the single executor (invariant #7). The in-process loop is stopped right
// after the Decision commits, which leaves the READY Action the Reconciler would
// rediscover; two advancement paths then enter ExecuteAgentAction for it, and the loser
// must stop without a second Tool Attempt and without a second real Tool call.
func TestAgentAction_ReadyToRunningRace_OnlyOneClaimWins(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool, Notifier: stop})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-race"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	stopCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop.cancel = cancel
	// Armed during the model call, so the notification that stops the chain is the
	// Decision commit's own: the Action is committed READY and nothing executes it.
	h.provider.BeforeReturn = func(context.Context) { stop.arm() }
	if err := h.svc.Execute(stopCtx, outcome); !errors.Is(err, context.Canceled) {
		t.Fatalf("execute agent node run = %v, want context.Canceled from the stopped chain", err)
	}

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action was committed for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionReady {
		t.Fatalf("action status = %s, want READY before the race", action.Status)
	}

	// The winner is held inside the Tool call until the loser has finished, so the two
	// claims genuinely overlap. A second call (there must be none) is not held, so a lost
	// race fails the assertions below instead of deadlocking.
	var toolCalls atomic.Int32
	inFlight := make(chan struct{}, 2)
	release := make(chan struct{})
	tool.during = func(context.Context, registry.ToolAction) {
		if toolCalls.Add(1) == 1 {
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
			errs <- h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate)
		}()
	}
	close(start)
	<-inFlight
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("execute agent action: %v", err)
		}
	}

	if got := tool.count(); got != 1 {
		t.Errorf("tool calls = %d, want exactly 1: the loser must not call the Tool again", got)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Errorf("tool attempts = %d, want exactly 1", len(attempts))
	}
	if executed, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID); executed.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", executed.Status)
	}
	// Counted for the raced Action alone: the loop runs on past it, and the second Turn's
	// FINAL Action writes its own started/completed pair.
	events := listEvents(h.ctx, t, h.uow, run.ID)
	countForAction := func(want domain.EventType) int {
		count := 0
		for _, ev := range events {
			if ev.Type != want {
				continue
			}
			var payload struct {
				ActionID string `json:"actionId"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("decode %s payload: %v", want, err)
			}
			if payload.ActionID == action.ID {
				count++
			}
		}
		return count
	}
	if got := countForAction(domain.EventAgentActionStarted); got != 1 {
		t.Errorf("AGENT_ACTION_STARTED events for the raced action = %d, want exactly 1", got)
	}
	if got := countForAction(domain.EventAgentActionCompleted); got != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED events for the raced action = %d, want exactly 1", got)
	}
}

// TestAgentAction_ToolCalledAfterCommit_NotBeforeCommit proves invariant #4 for the Tool
// call: everything the claim transaction owes -- the RUNNING Action, the STARTED Tool
// Attempt and AGENT_ACTION_STARTED -- is visible from a second connection while the Tool
// is running, and the result transaction's facts are not.
func TestAgentAction_ToolCalledAfterCommit_NotBeforeCommit(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-after-commit"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	observer := postgres.NewUnitOfWork(h.pool)
	var observedAction domain.AgentAction
	var observedAttempts []domain.ToolAttempt
	var observedEvents []domain.EventType
	var observeOnce sync.Once
	tool.during = func(ctx context.Context, called registry.ToolAction) {
		observeOnce.Do(func() {
			observedAction, _ = agentActionOfTurn(ctx, t, observer, outcome.AgentTurnID)
			observedAttempts = agentToolAttempts(ctx, t, observer, called.ActionID)
			observedEvents = agentEventTypesFor(listEvents(ctx, t, observer, run.ID), outcome.NodeRunID)
		})
	}

	h.execute(outcome)

	if observedAction.Status != domain.AgentActionRunning {
		t.Errorf("action seen from a second connection during the Tool call = %s, want RUNNING: the claim had not committed before the Tool was called", observedAction.Status)
	}
	if observedAction.StartedAt == nil {
		t.Errorf("action seen during the Tool call records no started_at")
	}
	if len(observedAttempts) != 1 || observedAttempts[0].Status != domain.ToolAttemptStarted {
		t.Errorf("tool attempts seen during the Tool call = %+v, want exactly one STARTED", observedAttempts)
	}
	if !containsEventType(observedEvents, domain.EventAgentActionStarted) {
		t.Errorf("events committed before the Tool call = %v, want AGENT_ACTION_STARTED among them", observedEvents)
	}
	if containsEventType(observedEvents, domain.EventAgentActionCompleted) {
		t.Errorf("AGENT_ACTION_COMPLETED was committed before the Tool returned: %v", observedEvents)
	}
}

// ---------------------------------------------------------------------------
// Invalid action: the Tool is never called
// ---------------------------------------------------------------------------

// assertAgentActionFailed is the shared assertion of the Action failure transaction
// (docs/06-execution-model.md §1.7 and docs/09-testing-and-acceptance.md §1): the Action
// fails, the Agent Run records why it stopped, the Agent NodeRun fails with the Run, and
// the committed Decision is left untouched -- all in one transaction, with no new Context
// or State Version and no next Turn.
func assertAgentActionFailed(
	t *testing.T,
	h *agentHarness,
	runID string,
	outcome service.AdvanceOutcome,
	want domain.AgentTermination,
	wantSource domain.FailureSource,
) domain.AgentAction {
	t.Helper()

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action exists for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionFailed {
		t.Errorf("action status = %s, want FAILED", action.Status)
	}
	if action.Error == nil {
		t.Errorf("failed action records no error")
	}
	if action.CompletedAt == nil {
		t.Errorf("failed action records no completed_at")
	}

	if turn := getAgentTurn(h.ctx, t, h.uow, outcome.AgentTurnID); turn.Status != domain.AgentTurnCompleted {
		t.Errorf("turn status = %s, want COMPLETED: the model answered, the Action is what failed", turn.Status)
	}
	if _, ok := agentDecisionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID); !ok {
		t.Errorf("the committed Decision disappeared with the failed Action")
	}

	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.Termination == nil || *agentRun.Termination != want {
		t.Errorf("agent run termination = %v, want %s", agentRun.Termination, want)
	}
	if agentRun.TerminatedAt == nil || agentRun.Error == nil {
		t.Errorf("terminated agent run records terminatedAt=%v error=%v, want both", agentRun.TerminatedAt, agentRun.Error)
	}
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a Context Version was created by a failing Action")
	}
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a State Version was created by a failing Action")
	}
	if turns := listReadyTurns(h.ctx, t, h.uow); len(turns) != 0 {
		t.Errorf("ready turns after a failed Action = %d, want 0", len(turns))
	}

	nodeRun := agentNodeRun(h.ctx, t, h.uow, runID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, runID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	assertNoDownstreamNodeRun(h.ctx, t, h.uow, runID)

	events := listEvents(h.ctx, t, h.uow, runID)
	types := agentEventTypesFor(events, nodeRun.ID)
	if agentCountEventType(types, domain.EventAgentActionFailed) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_ACTION_FAILED", types)
	}
	if agentCountEventType(types, domain.EventAgentFailed) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_FAILED", types)
	}
	if agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("events = %v, want exactly one NODE_FAILED", types)
	}
	if agentCountEventType(types, domain.EventAgentActionCompleted) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_COMPLETED", types)
	}
	if agentCountEventType(types, domain.EventAgentTurnReady) != 1 {
		t.Errorf("events = %v, want no second AGENT_TURN_READY: a failed Action starts no round", types)
	}
	failed := agentEventPayload(t, events, domain.EventAgentActionFailed)
	if failed["turnId"] != outcome.AgentTurnID || failed["actionId"] != action.ID {
		t.Errorf("AGENT_ACTION_FAILED = %v, want turn %s action %s", failed, outcome.AgentTurnID, action.ID)
	}
	if failed["failureSource"] != string(wantSource) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want %s", failed["failureSource"], wantSource)
	}
	if _, hasError := failed["error"].(map[string]any); !hasError {
		t.Errorf("AGENT_ACTION_FAILED carries no error object: %v", failed)
	}
	agentFailed := agentEventPayload(t, events, domain.EventAgentFailed)
	if agentFailed["termination"] != string(want) {
		t.Errorf("AGENT_FAILED termination = %v, want %s", agentFailed["termination"], want)
	}
	return action
}

// TestAgentAction_ToolNotInAllowlist_FailsInvalidActionWithoutToolCall covers
// docs/09-testing-and-acceptance.md §1: a committed Decision naming a Tool outside the
// Agent Run's frozen allowlist fails the Action as INVALID_ACTION. The registered Tool
// must not be called (the allowlist is a Run-level authority decision, not a Registry
// lookup), no Tool Attempt is created, and the model is not asked again.
func TestAgentAction_ToolNotInAllowlist_FailsInvalidActionWithoutToolCall(t *testing.T) {
	allowed := &agentRecordingTool{delegate: lookup.Executor{}}
	forbidden := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{
		LookupExecutor: allowed,
		Tools:          []registry.ToolRegistration{agentSecondToolRegistration(forbidden)},
	})
	generated := agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: agentSecondToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-not-allowed"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationInvalidAction, domain.FailureSyncExecution)
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts = %d, want 0: a Tool that may not be called gets no Attempt", len(attempts))
	}
	if forbidden.count() != 0 || allowed.count() != 0 {
		t.Errorf("tool calls = (%s %d, %s %d), want none", agentSecondToolName, forbidden.count(), lookup.ToolName, allowed.count())
	}
	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: an invalid Decision is never re-asked", got)
	}
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentActionStarted) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_STARTED: no Tool Attempt was ever created", types)
	}
}

// TestAgentAction_ToolUnregisteredAfterDecisionCommitted_FailsToolErrorWithoutNewDecision
// covers the mid-run half of docs/09-testing-and-acceptance.md §3.4 "已提交 Decision 指向的
// Tool 缺失": the TOOL_CALL Decision and its READY Action are committed while `lookup`
// still resolved, and a restarted Backend without that Tool then claims the Action. The
// Tool is in the frozen allowlist, so this is registry drift and not an invalid Decision:
// the Action fails as TOOL_ERROR with code TOOL_NOT_REGISTERED -- never INVALID_ACTION --
// no Tool Attempt is created, the model is not re-asked, and the committed Decision is the
// only one that ever exists.
func TestAgentAction_ToolUnregisteredAfterDecisionCommitted_FailsToolErrorWithoutNewDecision(t *testing.T) {
	stop := &agentStopNotifier{}
	first := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptToolCallThenFinal(first, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := first.saveDefinition(agentLoopDefinition("wf-agent-action-registry-drift"))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	// The Decision commit is the boundary: Turn 1 COMPLETED, its TOOL_CALL Action READY,
	// nothing has executed it.
	outcome, action := agentStopAfterDecision(first, stop, run.ID)
	if action.Type != domain.AgentActionToolCall {
		t.Fatalf("action type = %s, want TOOL_CALL", action.Type)
	}
	decision, found := agentDecisionOfTurn(first.ctx, t, first.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no decision was committed for turn %s", outcome.AgentTurnID)
	}

	// A second Backend over the same committed facts, whose Tool Registry no longer has
	// `lookup`. Its Reconciler would rediscover the READY Action, so the claim is entered
	// exactly as that pass would enter it.
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool: first.pool, SkipLookupTool: true, Clock: first.clock,
	})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "must never be produced"})

	if err := restarted.svc.ExecuteAgentAction(restarted.ctx, action.ID, domain.ClaimReconciler); err != nil {
		t.Fatalf("execute agent action against the drifted registry: %v", err)
	}

	if got := generated.Load(); got != 0 {
		t.Errorf("model calls = %d, want 0: a committed Decision is never regenerated for a missing Tool", got)
	}
	if got := len(restarted.provider.Requests()); got != 0 {
		t.Errorf("recorded model requests = %d, want 0", got)
	}

	// Shared terminal shape: Action FAILED, Turn stays COMPLETED, the committed Decision is
	// retained, Agent Run terminated, Agent NodeRun and Run FAILED, failureSource
	// SYNC_EXECUTION -- with termination TOOL_ERROR, not INVALID_ACTION.
	failed := assertAgentActionFailed(t, restarted, run.ID, outcome, domain.TerminationToolError, domain.FailureSyncExecution)
	if failed.Error == nil || failed.Error.Code != "TOOL_NOT_REGISTERED" {
		t.Errorf("failed action error = %+v, want code TOOL_NOT_REGISTERED: an allowlisted Tool that vanished is drift, not an invalid Decision", failed.Error)
	}

	if attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts = %d, want 0: there is nothing to call, so no Attempt is created", len(attempts))
	}

	again, foundAgain := agentDecisionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if !foundAgain || again.ID != decision.ID {
		t.Errorf("decision of the drifted turn = (%v, found=%t), want the committed %s: a second Decision must never exist", again.ID, foundAgain, decision.ID)
	}
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, agentRun.ID, 2) {
		t.Errorf("a second Turn was created for an Action that failed on registry drift")
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentActionStarted) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_STARTED: the drift is discovered before any Attempt exists", types)
	}
	if agentCountEventType(types, domain.EventAgentDecisionCommitted) != 1 {
		t.Errorf("events = %v, want the pre-drift AGENT_DECISION_COMMITTED retained exactly once", types)
	}
	if len(types) < 3 ||
		types[len(types)-3] != domain.EventAgentActionFailed ||
		types[len(types)-2] != domain.EventAgentFailed ||
		types[len(types)-1] != domain.EventNodeFailed {
		t.Errorf("agent node run events = %v, want them to end with AGENT_ACTION_FAILED, AGENT_FAILED, NODE_FAILED", types)
	}
	failedEvent := agentEventPayload(t, events, domain.EventAgentActionFailed)
	if errObj, ok := failedEvent["error"].(map[string]any); !ok || errObj["code"] != "TOOL_NOT_REGISTERED" {
		t.Errorf("AGENT_ACTION_FAILED error = %v, want code TOOL_NOT_REGISTERED", failedEvent["error"])
	}
}

// TestAgentAction_ArgumentsFailInputSchema_FailsInvalidActionWithoutToolCall is the same
// rule for the second execution-time check: arguments that do not satisfy the registered
// Tool InputSchema fail the Action as INVALID_ACTION before the Tool is called.
func TestAgentAction_ArgumentsFailInputSchema_FailsInvalidActionWithoutToolCall(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	generated := agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(`{}`),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-bad-arguments"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationInvalidAction, domain.FailureSyncExecution)
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts = %d, want 0", len(attempts))
	}
	if got := tool.count(); got != 0 {
		t.Errorf("tool calls = %d, want 0: arguments are validated before the Tool is called", got)
	}
	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Tool failure
// ---------------------------------------------------------------------------

// TestAgentAction_SyncToolFailure_TerminatesToolErrorNoPatchNoNextTurn covers the shared
// Tool failure transaction: Tool Attempt FAILED, Action FAILED, termination TOOL_ERROR,
// Agent NodeRun FAILED, Run aggregation and the three Events commit together, with
// failureSource SYNC_EXECUTION. The Decision's state patch is deliberately non-empty: a
// failure transaction applies no patch, creates no version and starts no next Turn.
func TestAgentAction_SyncToolFailure_TerminatesToolErrorNoPatchNoNextTurn(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	generated := agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName:      lookup.ToolName,
		ToolArguments: json.RawMessage(`{"key":"` + lookup.MissingKey + `"}`),
		StatePatch:    json.RawMessage(`{"seen":["` + lookup.MissingKey + `"]}`),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-tool-error"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationToolError, domain.FailureSyncExecution)

	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1: the MVP never retries an Agent Tool", len(attempts))
	}
	attempt := attempts[0]
	if attempt.Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempt status = %s, want FAILED", attempt.Status)
	}
	if attempt.Error == nil || attempt.CompletedAt == nil {
		t.Errorf("failed tool attempt records error=%v completedAt=%v, want both", attempt.Error, attempt.CompletedAt)
	}
	if len(attempt.Result) != 0 {
		t.Errorf("failed tool attempt stored a result: %s", attempt.Result)
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if got := agentPointers(agentRun); got != [3]int{1, 0, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 0 0]: a failure moves no pointer", got)
	}

	decision, _ := agentDecisionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	assertSameJSON(t, "committed decision arguments", json.RawMessage(`{"key":"`+lookup.MissingKey+`"}`), decision.Arguments)
	assertSameJSON(t, "committed decision state patch", json.RawMessage(`{"seen":["`+lookup.MissingKey+`"]}`), decision.StatePatch)

	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: a failed Tool is not answered by a new model call", got)
	}
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentActionStarted) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_ACTION_STARTED: the Tool really was called", types)
	}
	failed := agentEventPayload(t, listEvents(h.ctx, t, h.uow, run.ID), domain.EventAgentActionFailed)
	if failed["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_FAILED toolAttemptId = %v, want %s", failed["toolAttemptId"], attempt.ID)
	}
}

// ---------------------------------------------------------------------------
// Round and time limits
// ---------------------------------------------------------------------------

// TestAgentAction_MaxTurnsReachedAfterToolSuccess_TerminatesWithoutNewTurn covers
// docs/06-execution-model.md §1.7: after a successful Tool the Runtime must check
// max_turns *before* creating the next Turn. At the bound, the same transaction that
// commits the Tool result terminates the Agent Run as MAX_TURNS and fails the Agent
// NodeRun, leaving no Turn nobody may execute.
func TestAgentAction_MaxTurnsReachedAfterToolSuccess_TerminatesWithoutNewTurn(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	generated := agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := h.saveDefinition(agentLoopDefinitionMaxTurns("wf-agent-action-max-turns", 1))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED: the Tool result is committed, the round bound is what stops the Agent", action.Status)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempts = %+v, want exactly one SUCCEEDED", attempts)
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationMaxTurns {
		t.Errorf("agent run termination = %v, want MAX_TURNS", agentRun.Termination)
	}
	if got := agentPointers(agentRun); got != [3]int{1, 1, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 1 0]: the Tool result is committed, no round 2 exists", got)
	}
	if !agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("the Tool result's Context Version was not committed")
	}
	if turns := listReadyTurns(h.ctx, t, h.uow); len(turns) != 0 {
		t.Errorf("ready turns = %d, want 0: no Turn may be left that nobody is allowed to run", len(turns))
	}
	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1", got)
	}
	if got := tool.count(); got != 1 {
		t.Errorf("tool calls = %d, want 1", got)
	}

	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentTurnReady) != 1 {
		t.Errorf("events = %v, want no second AGENT_TURN_READY", types)
	}
	if agentCountEventType(types, domain.EventAgentActionCompleted) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_ACTION_COMPLETED", types)
	}
	if agentCountEventType(types, domain.EventAgentActionFailed) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_FAILED: the Action succeeded", types)
	}
	if agentCountEventType(types, domain.EventAgentFailed) != 1 || agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("events = %v, want one AGENT_FAILED and one NODE_FAILED", types)
	}
	if payload := agentEventPayload(t, events, domain.EventAgentFailed); payload["termination"] != string(domain.TerminationMaxTurns) {
		t.Errorf("AGENT_FAILED termination = %v, want MAX_TURNS", payload["termination"])
	}
}

// TestAgentAction_DeadlinePassedAfterToolSuccess_TerminatesTimeoutWithoutNewTurn is the
// deadline half of the same check. The clock is advanced past the frozen Agent deadline
// while the Tool is running, so the result transaction must commit the Tool result and
// terminate the Agent Run as TIMEOUT rather than starting a round that could never
// finish.
func TestAgentAction_DeadlinePassedAfterToolSuccess_TerminatesTimeoutWithoutNewTurn(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-deadline"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	tool.during = func(context.Context, registry.ToolAction) {
		h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	}

	h.execute(outcome)

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED: the Tool answered before the deadline check", action.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %v, want TIMEOUT", agentRun.Termination)
	}
	if got := agentPointers(agentRun); got != [3]int{1, 1, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 1 0]", got)
	}
	if turns := listReadyTurns(h.ctx, t, h.uow); len(turns) != 0 {
		t.Errorf("ready turns = %d, want 0", len(turns))
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentTurnReady) != 1 {
		t.Errorf("events = %v, want no second AGENT_TURN_READY", types)
	}
	if payload := agentEventPayload(t, events, domain.EventAgentFailed); payload["termination"] != string(domain.TerminationTimeout) {
		t.Errorf("AGENT_FAILED termination = %v, want TIMEOUT", payload["termination"])
	}
}

// ---------------------------------------------------------------------------
// Result transaction failure
// ---------------------------------------------------------------------------

// TestAgentAction_ResultTxFailure_RollsBackAttemptActionVersionsAndTurn covers
// docs/09-testing-and-acceptance.md §1: when the result transaction cannot commit, every
// write in it rolls back together. What is left is exactly the state the claim
// transaction committed -- a STARTED Tool Attempt under a RUNNING Action -- with no
// Context Version, no State Version and no next Turn, so nothing half-applied is visible.
//
// The failure is manufactured the same way as the Node path's rollback test: the Event ID
// the result transaction is about to insert is pre-occupied, so the Event insert violates
// the primary key.
func TestAgentAction_ResultTxFailure_RollsBackAttemptActionVersionsAndTurn(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-action-result-rollback"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	const collidingEventID = "ev_agent_action_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())
	// Armed during the Tool call, so the collision hits the first Event the *result*
	// transaction appends, not one of the claim transaction's.
	tool.during = func(context.Context, registry.ToolAction) {
		h.ids.ForceNextEventID(collidingEventID)
	}

	err := h.svc.Execute(h.ctx, outcome)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("execute agent node run = %v, want a conflict from the duplicated Event ID", err)
	}

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionRunning {
		t.Errorf("action status = %s, want RUNNING: the result transaction rolled back", action.Status)
	}
	if action.CompletedAt != nil {
		t.Errorf("rolled-back action recorded completed_at = %v", *action.CompletedAt)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Fatalf("tool attempts = %+v, want exactly one still STARTED", attempts)
	}
	if len(attempts[0].Result) != 0 {
		t.Errorf("rolled-back tool attempt stored a result: %s", attempts[0].Result)
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a Context Version survived the rolled-back result transaction")
	}
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a State Version survived the rolled-back result transaction")
	}
	if got := agentPointers(agentRun); got != [3]int{1, 0, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 0 0]", got)
	}
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s after a rolled-back transaction", *agentRun.Termination)
	}
	if turns := listReadyTurns(h.ctx, t, h.uow); len(turns) != 0 {
		t.Errorf("ready turns = %d, want 0: the next Turn rolled back with its transaction", len(turns))
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nodeRun.Status)
	}
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentActionCompleted) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_COMPLETED", types)
	}
	if agentCountEventType(types, domain.EventAgentTurnReady) != 1 {
		t.Errorf("events = %v, want no second AGENT_TURN_READY", types)
	}
}
