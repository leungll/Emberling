//go:build integration

// Return-to-Agent tests: a definite refusal fails the Action but hands the Agent a bounded
// REJECTED tool message and a new READY Turn, all in one transaction under the Run lock,
// unless the frozen turn bound or the expired deadline ends the Agent Run instead. These
// tests call the locked use case directly inside a Unit of Work transaction, over a real
// Agent Run the service created, so the conditional updates, the Event, the Context
// Version and the Turn are proved against PostgreSQL.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/agent"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// returnClock is a mutable fake clock: the deadline tests move it past the Agent deadline
// instead of waiting.
type returnClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *returnClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *returnClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// recordingQueue records the Turns offered to it after COMMIT. It accepts them unless
// refuse is set, which models a full work queue leaving the Turn to the Reconciler.
type recordingQueue struct {
	mu     sync.Mutex
	turns  []string
	refuse atomic.Bool
}

func (*recordingQueue) EnqueueAdvance(string) bool { return true }

func (q *recordingQueue) EnqueueAgentTurn(_, turnID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.turns = append(q.turns, turnID)
	return !q.refuse.Load()
}

func (q *recordingQueue) offered() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.turns...)
}

// guardedToolName is a test Tool guarded by a fact requirement, registered once with the
// refusal returned to the Agent and once without, so both behaviours are proved on the
// same production claim path.
const guardedToolName = "guarded_tool"

const guardedFactType = "patch_verified"

type guardedExecutor struct{ calls atomic.Int32 }

func (e *guardedExecutor) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	e.calls.Add(1)
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: json.RawMessage(`{"ok":true}`)}}, nil
}

func guardedToolRegistration(returnUnmet bool, executor registry.ToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:                   guardedToolName,
			Description:            "A tool that needs a verified patch",
			InputSchema:            json.RawMessage(`{"type":"object","properties":{"patchDigest":{"type":"string","minLength":1}},"required":["patchDigest"],"additionalProperties":false}`),
			OutputSchema:           json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
			SideEffect:             domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind:          domain.ToolExecutionSync,
			Requires:               []domain.FactRequirement{{FactType: guardedFactType, SubjectArgument: "/patchDigest"}},
			ReturnUnmetRequirement: returnUnmet,
		},
		Executor: executor,
	}
}

// recordingLookup is a lookup Executor whose result carries a workspace patch the lookup
// registration does not declare.
type recordingLookup struct{}

func (recordingLookup) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{
		Output:         json.RawMessage(`{"key":"k","record":"r"}`),
		WorkspacePatch: &registry.WorkspacePatch{Patch: "diff --git a/go.mod b/go.mod", PatchDigest: "sha256:0", TouchedPaths: []string{"go.mod"}},
	}}, nil
}

// errInjectedAppend is the failure a failingEventsTx injects into one Event append.
var errInjectedAppend = errors.New("injected: event append failed")

// failingEventsTx is a store.Tx whose Event repository fails the append of one Event type,
// so a test can make the return transaction fail after its first writes and prove the
// whole transaction rolls back.
type failingEventsTx struct {
	store.Tx
	failOn domain.EventType
}

func (f failingEventsTx) Events() store.EventRepository {
	return failingEvents{EventRepository: f.Tx.Events(), failOn: f.failOn}
}

type failingEvents struct {
	store.EventRepository
	failOn domain.EventType
}

func (f failingEvents) Append(ctx context.Context, lock *store.RunLock, ev domain.Event) (domain.Event, error) {
	if ev.Type == f.failOn {
		return domain.Event{}, errInjectedAppend
	}
	return f.EventRepository.Append(ctx, lock, ev)
}

type returnHarnessOptions struct {
	maxTurns       int
	tools          []registry.ToolRegistration
	lookupExecutor registry.ToolExecutor
}

type returnHarness struct {
	t     *testing.T
	ctx   context.Context
	uow   store.UnitOfWork
	svc   *ExecutionService
	clock *returnClock
	queue *recordingQueue

	runID      string
	nodeRunID  string
	agentRunID string
	turnID     string
}

// The argument value every seeded Decision carries. A tool message that repeats it has
// leaked the call's arguments.
const seededArgumentValue = "secret-argument-value-9f3c"

var returnHarnessSeq atomic.Int64

// newReturnHarness builds an ExecutionService over a fresh database and drives one
// text_input -> agent -> text_output Run up to the claim that created the Agent Run and its
// READY Turn 1. The Turn is then completed by hand with a TOOL_CALL Decision and its READY
// Action, which stands in for the model answer every return starts from.
func newReturnHarness(t *testing.T, opts returnHarnessOptions) *returnHarness {
	t.Helper()
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	nodeRegistry := registry.NewNodeRegistry()
	modelRegistry := registry.NewModelRegistry()
	toolRegistry := registry.NewToolRegistry()
	if err := modelRegistry.Register(ctx, mockmodel.NewProvider()); err != nil {
		t.Fatalf("register mock model provider: %v", err)
	}
	for _, reg := range []registry.NodeRegistration{textinput.Registration(), textoutput.Registration(), agent.Registration()} {
		if err := nodeRegistry.Register(reg); err != nil {
			t.Fatalf("register node type %s: %v", reg.Metadata.Type, err)
		}
	}
	lookupReg := lookup.Registration()
	if opts.lookupExecutor != nil {
		lookupReg.Executor = opts.lookupExecutor
	}
	for _, reg := range append([]registry.ToolRegistration{lookupReg}, opts.tools...) {
		if err := toolRegistry.Register(reg); err != nil {
			t.Fatalf("register tool %s: %v", reg.Metadata.Name, err)
		}
	}

	clock := &returnClock{now: time.Now().UTC().Truncate(time.Second)}
	queue := &recordingQueue{}
	compiler := runtime.NewCompiler(nodeRegistry, clock)
	svc := NewExecutionService(Deps{
		UoW: uow, Nodes: nodeRegistry, Models: modelRegistry, Tools: toolRegistry, Compiler: compiler,
		Clock: clock, IDs: store.NewUUIDGenerator(), Queue: queue,
		Callback: CallbackConfig{BaseURL: "https://emberling.test", SigningSecret: []byte("return-test-signing-secret"), PendingTTL: time.Hour},
	})
	h := &returnHarness{t: t, ctx: ctx, uow: uow, svc: svc, clock: clock, queue: queue}

	maxTurns := opts.maxTurns
	if maxTurns == 0 {
		maxTurns = 4
	}
	allowed := []string{lookup.ToolName}
	for _, reg := range opts.tools {
		allowed = append(allowed, reg.Metadata.Name)
	}
	allowedJSON, _ := json.Marshal(allowed)
	workflowID := "wf_return_" + strconv.FormatInt(returnHarnessSeq.Add(1), 10)
	def := domain.Definition{
		WorkflowID: workflowID, Version: 1, Name: "Return to Agent", Description: "text_input -> agent -> text_output",
		Nodes: []domain.Node{
			{ID: "node_input", Type: "text_input", Name: "Question", Config: json.RawMessage(`{"inputKey":"question","required":true}`)},
			{ID: "node_agent", Type: "agent", Name: "Agent", Config: json.RawMessage(`{"instructions":"Answer.","modelId":"` + mockmodel.ModelID +
				`","allowedTools":` + string(allowedJSON) + `,"maxTurns":` + strconv.Itoa(maxTurns) + `,"timeoutMs":600000}`)},
			{ID: "node_output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_input", SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
			{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_output", TargetHandle: "text"},
		},
		CreatedAt: clock.Now(),
	}
	plan, err := compiler.Compile(ctx, def)
	if err != nil {
		t.Fatalf("compile definition: %v", err)
	}
	def.RunInputSchema = plan.RunInputSchema
	def.Validation = plan.Validation
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.Definitions().Save(ctx, def) }); err != nil {
		t.Fatalf("save definition: %v", err)
	}

	run, err := svc.CreateRun(ctx, CreateRun{WorkflowID: workflowID, DefinitionVersion: 1, Input: json.RawMessage(`{"question":"what is the record?"}`)})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	h.runID = run.ID
	inputOutcome := h.advance()
	if err := svc.Execute(ctx, inputOutcome); err != nil {
		t.Fatalf("execute text_input: %v", err)
	}
	agentOutcome := h.advance()
	if agentOutcome.AgentRunID == "" || agentOutcome.AgentTurnID == "" {
		t.Fatalf("agent claim = %+v, want an Agent Run and its READY Turn", agentOutcome)
	}
	h.nodeRunID, h.agentRunID, h.turnID = agentOutcome.NodeRunID, agentOutcome.AgentRunID, agentOutcome.AgentTurnID
	return h
}

func (h *returnHarness) advance() AdvanceOutcome {
	h.t.Helper()
	outcome, err := h.svc.Advance(h.ctx, h.runID)
	if err != nil || !outcome.Claimed {
		h.t.Fatalf("advance %s = %+v, %v, want a claim", h.runID, outcome, err)
	}
	return outcome
}

// seedDecision completes Turn 1 with a TOOL_CALL Decision for toolName and its Action,
// claimed to `status` (READY stays unclaimed, RUNNING is claimed, WAITING_CALLBACK is
// claimed and then marked waiting with its NodeRun).
func (h *returnHarness) seedDecision(toolName string, arguments json.RawMessage, status domain.AgentActionStatus) domain.AgentAction {
	h.t.Helper()
	now := h.clock.Now()
	decisionID, actionID := "dec_"+h.agentRunID, "act_"+h.agentRunID
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		// A waiting Action pauses the Run exactly as the dispatch of an asynchronous Tool
		// does: the Run and its plan are resolved before the lock, the Run is re-aggregated
		// under it.
		var run domain.Run
		var plan *runtime.CompiledDefinition
		if status == domain.AgentActionWaitingCallback {
			var err error
			if run, plan, err = h.svc.runPlan(ctx, tx, h.runID); err != nil {
				return err
			}
		}
		lock, err := tx.Runs().LockForUpdate(ctx, h.runID)
		if err != nil {
			return err
		}
		if won, err := tx.AgentTurns().ClaimReady(ctx, h.turnID, now); err != nil || !won {
			return errors.Join(err, errors.New("turn 1 was not claimed"))
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, h.turnID, now, json.RawMessage(`{"kind":"TOOL_CALL"}`), nil, nil); err != nil {
			return err
		}
		if err := tx.AgentDecisions().Create(ctx, domain.AgentDecision{
			ID: decisionID, TurnID: h.turnID, Kind: domain.DecisionToolCall, ToolName: &toolName, Arguments: arguments,
			ResponseSummary: json.RawMessage(`{"finishReason":"tool_call"}`), CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.AgentActions().Create(ctx, domain.AgentAction{
			ID: actionID, TurnID: h.turnID, DecisionID: decisionID, Type: domain.AgentActionToolCall, Status: domain.AgentActionReady, CreatedAt: now,
		}); err != nil {
			return err
		}
		if status == domain.AgentActionReady {
			return nil
		}
		if won, err := tx.AgentActions().ClaimReady(ctx, actionID, now); err != nil || !won {
			return errors.Join(err, errors.New("seeded action was not claimed"))
		}
		if status == domain.AgentActionWaitingCallback {
			if err := tx.AgentActions().MarkWaiting(ctx, actionID, now); err != nil {
				return err
			}
			if err := tx.NodeRuns().MarkWaiting(ctx, h.nodeRunID, now); err != nil {
				return err
			}
			return h.svc.aggregateRunLocked(ctx, tx, lock, run, plan, now)
		}
		return nil
	}); err != nil {
		h.t.Fatalf("seed decision for %s: %v", toolName, err)
	}
	return h.getAction(actionID)
}

func (h *returnHarness) seedLookupDecision(status domain.AgentActionStatus) domain.AgentAction {
	h.t.Helper()
	return h.seedDecision(lookup.ToolName, json.RawMessage(`{"key":"`+seededArgumentValue+`"}`), status)
}

// returnAction calls the locked use case inside one Unit of Work transaction and commits
// it, exactly as a production path would at the end of its own decision: the Run and its
// plan are resolved before the lock when the Action is waiting, and the rest under it.
func (h *returnHarness) returnAction(action domain.AgentAction, from domain.AgentActionStatus, code domain.AgentReturnCode, message string) (string, error) {
	h.t.Helper()
	return h.returnActionWithTx(action, from, code, message, func(tx store.Tx) store.Tx { return tx })
}

// returnActionWithTx is returnAction over a transaction the test may wrap, so a write can
// be made to fail after the first ones succeeded.
func (h *returnHarness) returnActionWithTx(action domain.AgentAction, from domain.AgentActionStatus, code domain.AgentReturnCode, message string, wrap func(store.Tx) store.Tx) (string, error) {
	h.t.Helper()
	var nextTurnID string
	err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		tx = wrap(tx)
		var run domain.Run
		var plan *runtime.CompiledDefinition
		if from == domain.AgentActionWaitingCallback {
			var err error
			if run, plan, err = h.svc.runPlan(ctx, tx, h.runID); err != nil {
				return err
			}
		}
		lock, err := tx.Runs().LockForUpdate(ctx, h.runID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, h.nodeRunID)
		if err != nil {
			return err
		}
		agentRun, err := tx.AgentRuns().Get(ctx, h.agentRunID)
		if err != nil {
			return err
		}
		turn, err := tx.AgentTurns().Get(ctx, action.TurnID)
		if err != nil {
			return err
		}
		ret := agentActionReturn{lock: lock, nodeRun: nodeRun, agentRun: agentRun, turn: turn, action: action, toolName: lookup.ToolName, run: run, plan: plan}
		nextTurnID, err = h.svc.returnActionToAgentLocked(ctx, tx, ret, from, code, message, h.clock.Now())
		return err
	})
	return nextTurnID, err
}

func (h *returnHarness) getAction(actionID string) domain.AgentAction {
	h.t.Helper()
	var action domain.AgentAction
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		action, err = tx.AgentActions().Get(ctx, actionID)
		return err
	}); err != nil {
		h.t.Fatalf("get action %s: %v", actionID, err)
	}
	return action
}

func (h *returnHarness) getAgentRun() domain.AgentRun {
	h.t.Helper()
	var run domain.AgentRun
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		run, err = tx.AgentRuns().Get(ctx, h.agentRunID)
		return err
	}); err != nil {
		h.t.Fatalf("get agent run: %v", err)
	}
	return run
}

func (h *returnHarness) getNodeRun() domain.NodeRun {
	h.t.Helper()
	var nodeRun domain.NodeRun
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		nodeRun, err = tx.NodeRuns().Get(ctx, h.nodeRunID)
		return err
	}); err != nil {
		h.t.Fatalf("get node run: %v", err)
	}
	return nodeRun
}

func (h *returnHarness) getRun() domain.Run {
	h.t.Helper()
	var run domain.Run
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		run, err = tx.Runs().Get(ctx, h.runID)
		return err
	}); err != nil {
		h.t.Fatalf("get run: %v", err)
	}
	return run
}

// readyTurns returns this Agent Run's READY Turns as the Reconciler's rediscovery query
// lists them.
func (h *returnHarness) readyTurns() []domain.AgentTurn {
	h.t.Helper()
	var out []domain.AgentTurn
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		turns, err := tx.AgentTurns().ListReady(ctx, 10)
		for _, turn := range turns {
			if turn.AgentRunID == h.agentRunID {
				out = append(out, turn)
			}
		}
		return err
	}); err != nil {
		h.t.Fatalf("list READY turns: %v", err)
	}
	return out
}

func (h *returnHarness) readyTurnNos() []int {
	h.t.Helper()
	var out []int
	for _, turn := range h.readyTurns() {
		out = append(out, turn.TurnNo)
	}
	return out
}

// eventCount returns how many Events of typ the Run has.
func (h *returnHarness) eventCount(typ domain.EventType) int {
	h.t.Helper()
	n := 0
	for _, event := range h.events() {
		if event.Type == typ {
			n++
		}
	}
	return n
}

func (h *returnHarness) events() []domain.Event {
	h.t.Helper()
	var out []domain.Event
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.Events().ListAfter(ctx, h.runID, 0, 200)
		return err
	}); err != nil {
		h.t.Fatalf("list events: %v", err)
	}
	return out
}

// actionFailedPayloads returns every AGENT_ACTION_FAILED payload naming actionID.
func (h *returnHarness) actionFailedPayloads(actionID string) []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for _, event := range h.events() {
		if event.Type != domain.EventAgentActionFailed {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			h.t.Fatalf("decode %s payload: %v", event.Type, err)
		}
		if payload["actionId"] == actionID {
			out = append(out, payload)
		}
	}
	return out
}

func (h *returnHarness) hasContextVersion(version int) bool {
	h.t.Helper()
	found := true
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.AgentContextVersions().GetByRunAndVersion(ctx, h.agentRunID, version)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		h.t.Fatalf("read context version %d: %v", version, err)
	}
	return found
}

// toolMessage returns the REJECTED tool message of Context Version 1 after checking the
// Decision precedes it and the message is bounded and free of the call's arguments.
func (h *returnHarness) toolMessage(actionID string) map[string]any {
	h.t.Helper()
	var version domain.AgentContextVersion
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		version, err = tx.AgentContextVersions().GetByRunAndVersion(ctx, h.agentRunID, 1)
		return err
	}); err != nil {
		h.t.Fatalf("read context version 1: %v", err)
	}
	messages, err := runtime.DecodeAgentContext(version.Messages)
	if err != nil {
		h.t.Fatalf("decode context version 1: %v", err)
	}
	if len(messages) < 2 {
		h.t.Fatalf("context version 1 has %d messages, want the Decision and the tool message", len(messages))
	}
	decision, tool := messages[len(messages)-2], messages[len(messages)-1]
	if decision.Role != runtime.AgentRoleAssistant || tool.Role != runtime.AgentRoleTool {
		h.t.Fatalf("last two messages = %s, %s, want assistant then tool", decision.Role, tool.Role)
	}
	if tool.ToolActionID == nil || *tool.ToolActionID != actionID {
		h.t.Errorf("tool message links action %v, want %s", tool.ToolActionID, actionID)
	}
	if len(tool.Content) > maxReturnToolMessageBytes {
		h.t.Errorf("tool message is %d bytes, want at most %d", len(tool.Content), maxReturnToolMessageBytes)
	}
	if strings.Contains(string(tool.Content), seededArgumentValue) {
		h.t.Errorf("tool message %s repeats the call's arguments", tool.Content)
	}
	var content map[string]any
	if err := json.Unmarshal(tool.Content, &content); err != nil {
		h.t.Fatalf("decode tool message %s: %v", tool.Content, err)
	}
	return content
}

// assertReturned proves one returned refusal: the Action FAILED with code, exactly one
// AGENT_ACTION_FAILED with returnedToAgent and the mapped failure source, a REJECTED tool
// message with the code, and exactly one new READY Turn, the one the use case reported.
func assertReturned(t *testing.T, h *returnHarness, action domain.AgentAction, code domain.AgentReturnCode, nextTurnID string) {
	t.Helper()
	wantSource, _ := code.FailureSource()

	failed := h.getAction(action.ID)
	if failed.Status != domain.AgentActionFailed || failed.Error == nil || failed.Error.Code != string(code) {
		t.Errorf("action = %s %+v, want FAILED with %s", failed.Status, failed.Error, code)
	}
	payloads := h.actionFailedPayloads(action.ID)
	if len(payloads) != 1 {
		t.Fatalf("AGENT_ACTION_FAILED for %s = %d events, want exactly 1", action.ID, len(payloads))
	}
	payload := payloads[0]
	if payload["returnedToAgent"] != true || payload["failureSource"] != string(wantSource) {
		t.Errorf("AGENT_ACTION_FAILED = %v, want returnedToAgent true and failureSource %s", payload, wantSource)
	}
	if errorObject, _ := payload["error"].(map[string]any); errorObject["code"] != string(code) {
		t.Errorf("AGENT_ACTION_FAILED error = %v, want code %s", payload["error"], code)
	}

	message := h.toolMessage(action.ID)
	if message["status"] != returnToolMessageStatus || message["code"] != string(code) {
		t.Errorf("tool message = %v, want status REJECTED with code %s", message, code)
	}

	if nextTurnID == "" {
		t.Fatalf("return reported no next Turn")
	}
	if nos := h.readyTurnNos(); len(nos) != 1 || nos[0] != 2 {
		t.Errorf("READY turns = %v, want exactly [2]", nos)
	}
	agentRun := h.getAgentRun()
	if agentRun.Termination != nil || agentRun.CurrentTurnNo != 2 || agentRun.CurrentContextVersion != 1 {
		t.Errorf("agent run = termination %v turn %d context %d, want running at turn 2 with context version 1",
			agentRun.Termination, agentRun.CurrentTurnNo, agentRun.CurrentContextVersion)
	}
	if nodeRun := h.getNodeRun(); nodeRun.Status != domain.NodeRunRunning {
		t.Errorf("agent node run = %s, want RUNNING", nodeRun.Status)
	}
}

// returnedOnce runs the common single-code scenario: a RUNNING Action returned with code.
func returnedOnce(t *testing.T, code domain.AgentReturnCode) {
	t.Helper()
	h := newReturnHarness(t, returnHarnessOptions{})
	action := h.seedLookupDecision(domain.AgentActionRunning)
	nextTurnID, err := h.returnAction(action, domain.AgentActionRunning, code, "the call was refused")
	if err != nil {
		t.Fatalf("return %s: %v", code, err)
	}
	assertReturned(t, h, action, code, nextTurnID)
}

func TestReturnActionToAgent_ScopeDenied_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnScopeDenied)
}

func TestReturnActionToAgent_StalePatch_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnStalePatch)
}

func TestReturnActionToAgent_VerificationMissing_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnVerificationMissing)
}

func TestReturnActionToAgent_GoalDenied_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnGoalDenied)
}

func TestReturnActionToAgent_TargetOccupied_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnTargetOccupied)
}

func TestReturnActionToAgent_ApprovalRejected_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnApprovalRejected)
}

func TestReturnActionToAgent_ApprovalExpired_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnApprovalExpired)
}

func TestReturnActionToAgent_ReleaseRefused_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnReleaseRefused)
}

func TestReturnActionToAgent_ReleaseNotDeployed_NewReadyTurn(t *testing.T) {
	returnedOnce(t, domain.ReturnReleaseNotDeployed)
}

func TestReturnActionToAgent_WaitingCallbackAction_ResumesNodeRunAndNewReadyTurn(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{})
	action := h.seedLookupDecision(domain.AgentActionWaitingCallback)
	if nodeRun := h.getNodeRun(); nodeRun.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("seeded node run = %s, want WAITING_CALLBACK", nodeRun.Status)
	}
	if run := h.getRun(); run.Status != domain.RunPaused {
		t.Fatalf("seeded run = %s, want PAUSED while the Agent waits", run.Status)
	}
	resumedBefore := h.eventCount(domain.EventRunResumed)

	nextTurnID, err := h.returnAction(action, domain.AgentActionWaitingCallback, domain.ReturnReleaseRefused, "production is outside its release window")
	if err != nil {
		t.Fatalf("return from WAITING_CALLBACK: %v", err)
	}
	assertReturned(t, h, action, domain.ReturnReleaseRefused, nextTurnID)
	if run := h.getRun(); run.Status != domain.RunRunning {
		t.Errorf("run = %s after the return, want RUNNING", run.Status)
	}
	if got := h.eventCount(domain.EventRunResumed); got != resumedBefore+1 {
		t.Errorf("RUN_RESUMED events = %d, want %d (one written with the return)", got, resumedBefore+1)
	}
}

func TestReturnActionToAgent_TimeoutWonFirst_WritesNothing(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{})
	action := h.seedLookupDecision(domain.AgentActionRunning)
	// The deadline passes and the timeout commits first: it fails the Action and ends the
	// Agent Run as TIMEOUT.
	h.clock.Advance(601 * time.Second)
	if err := h.svc.TimeoutAgentRun(h.ctx, h.agentRunID); err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}
	agentRunBefore := h.getAgentRun()
	if agentRunBefore.Termination == nil || *agentRunBefore.Termination != domain.TerminationTimeout {
		t.Fatalf("agent run termination = %v after the timeout, want TIMEOUT", agentRunBefore.Termination)
	}
	eventsBefore := len(h.events())
	contextBefore := h.hasContextVersion(1)

	nextTurnID, err := h.returnAction(action, domain.AgentActionRunning, domain.ReturnApprovalRejected, "the reviewer rejected the release")
	if !errors.Is(err, errAgentTurnSuperseded) || nextTurnID != "" {
		t.Fatalf("return after the timeout won = %q, %v, want errAgentTurnSuperseded and no turn", nextTurnID, err)
	}
	if failed := h.getAction(action.ID); failed.Status != domain.AgentActionFailed || failed.Error == nil || failed.Error.Code == string(domain.ReturnApprovalRejected) {
		t.Errorf("action = %s %+v, want the timeout's FAILED left untouched", failed.Status, failed.Error)
	}
	if got := len(h.events()); got != eventsBefore {
		t.Errorf("events = %d, want %d (nothing appended by the losing return)", got, eventsBefore)
	}
	if h.hasContextVersion(1) != contextBefore {
		t.Errorf("context version 1 present = %v, want unchanged %v", !contextBefore, contextBefore)
	}
	agentRun := h.getAgentRun()
	if agentRun.CurrentTurnNo != agentRunBefore.CurrentTurnNo || agentRun.CurrentContextVersion != agentRunBefore.CurrentContextVersion ||
		agentRun.CurrentStateVersion != agentRunBefore.CurrentStateVersion || agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run = %+v, want the pointers and TIMEOUT termination of %+v", agentRun, agentRunBefore)
	}
	if nos := h.readyTurnNos(); len(nos) != 0 {
		t.Errorf("READY turns = %v, want none", nos)
	}
}

func TestReturnActionToAgent_WriteFailsBeforeCommit_RollsBackEverything(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{})
	action := h.seedLookupDecision(domain.AgentActionRunning)
	eventsBefore := len(h.events())

	// The Action is failed, AGENT_ACTION_FAILED and the Context Version are written, then
	// the append of AGENT_TURN_READY for the new Turn fails: the transaction must leave no
	// trace of any of them.
	nextTurnID, err := h.returnActionWithTx(action, domain.AgentActionRunning, domain.ReturnScopeDenied, "path outside the allowed directories",
		func(tx store.Tx) store.Tx { return failingEventsTx{Tx: tx, failOn: domain.EventAgentTurnReady} })
	if !errors.Is(err, errInjectedAppend) || nextTurnID != "" {
		t.Fatalf("return with a failing append = %q, %v, want the injected error and no turn", nextTurnID, err)
	}
	if got := h.getAction(action.ID); got.Status != domain.AgentActionRunning || got.Error != nil {
		t.Errorf("action = %s %+v, want still RUNNING without an error", got.Status, got.Error)
	}
	if got := len(h.events()); got != eventsBefore {
		t.Errorf("events = %d, want %d (the rolled back AGENT_ACTION_FAILED must not exist)", got, eventsBefore)
	}
	if h.hasContextVersion(1) {
		t.Errorf("context version 1 exists, want it rolled back")
	}
	if nos := h.readyTurnNos(); len(nos) != 0 {
		t.Errorf("READY turns = %v, want none", nos)
	}
	if run := h.getAgentRun(); run.CurrentTurnNo != 1 || run.CurrentContextVersion != 0 || run.Termination != nil {
		t.Errorf("agent run = turn %d context %d termination %v, want untouched turn 1 context 0", run.CurrentTurnNo, run.CurrentContextVersion, run.Termination)
	}
}

func TestReturnActionToAgent_DeadlinePassed_TerminatesTimeoutNoNewTurn(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{})
	action := h.seedLookupDecision(domain.AgentActionRunning)
	h.clock.Advance(601 * time.Second)

	nextTurnID, err := h.returnAction(action, domain.AgentActionRunning, domain.ReturnApprovalExpired, "the approval expired")
	if err != nil {
		t.Fatalf("return past the deadline: %v", err)
	}
	if nextTurnID != "" {
		t.Errorf("return past the deadline reported next turn %s, want none", nextTurnID)
	}
	agentRun := h.getAgentRun()
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %v, want TIMEOUT", agentRun.Termination)
	}
	if nos := h.readyTurnNos(); len(nos) != 0 {
		t.Errorf("READY turns = %v, want none", nos)
	}
	if failed := h.getAction(action.ID); failed.Status != domain.AgentActionFailed || failed.Error == nil || failed.Error.Code != string(domain.ReturnApprovalExpired) {
		t.Errorf("action = %s %+v, want FAILED with APPROVAL_EXPIRED", failed.Status, failed.Error)
	}
	if payloads := h.actionFailedPayloads(action.ID); len(payloads) != 1 || payloads[0]["returnedToAgent"] != true {
		t.Errorf("AGENT_ACTION_FAILED = %v, want exactly one with returnedToAgent", payloads)
	}
	if nodeRun := h.getNodeRun(); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run = %s, want FAILED", nodeRun.Status)
	}
}

func TestReturnActionToAgent_MaxTurnsReached_TerminatesMaxTurns(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{maxTurns: 1})
	action := h.seedLookupDecision(domain.AgentActionRunning)

	nextTurnID, err := h.returnAction(action, domain.AgentActionRunning, domain.ReturnGoalDenied, "the goal of this run does not allow a release")
	if err != nil {
		t.Fatalf("return at the turn bound: %v", err)
	}
	if nextTurnID != "" {
		t.Errorf("return at the turn bound reported next turn %s, want none", nextTurnID)
	}
	agentRun := h.getAgentRun()
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationMaxTurns {
		t.Errorf("agent run termination = %v, want MAX_TURNS", agentRun.Termination)
	}
	if nos := h.readyTurnNos(); len(nos) != 0 {
		t.Errorf("READY turns = %v, want none", nos)
	}
	if message := h.toolMessage(action.ID); message["code"] != string(domain.ReturnGoalDenied) {
		t.Errorf("tool message = %v, want the GOAL_DENIED refusal recorded before the termination", message)
	}
}

func TestReturnActionToAgent_FromStatusMismatch_WritesNothing(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{})
	// The Action is still READY: the competing path this caller lost to never claimed it,
	// so a return "from RUNNING" matches no row.
	action := h.seedLookupDecision(domain.AgentActionReady)
	eventsBefore := len(h.events())

	nextTurnID, err := h.returnAction(action, domain.AgentActionRunning, domain.ReturnScopeDenied, "path outside the allowed directories")
	if !errors.Is(err, errAgentTurnSuperseded) || nextTurnID != "" {
		t.Fatalf("return with a stale from status = %q, %v, want errAgentTurnSuperseded and no turn", nextTurnID, err)
	}
	if got := h.getAction(action.ID); got.Status != domain.AgentActionReady || got.Error != nil {
		t.Errorf("action = %s %+v, want untouched READY", got.Status, got.Error)
	}
	if got := len(h.events()); got != eventsBefore {
		t.Errorf("events = %d, want %d (nothing appended)", got, eventsBefore)
	}
	if h.hasContextVersion(1) {
		t.Errorf("context version 1 exists, want none")
	}
	if nos := h.readyTurnNos(); len(nos) != 0 {
		t.Errorf("READY turns = %v, want none", nos)
	}
	if run := h.getAgentRun(); run.CurrentTurnNo != 1 || run.Termination != nil {
		t.Errorf("agent run = turn %d termination %v, want untouched turn 1", run.CurrentTurnNo, run.Termination)
	}
}

func TestReturnActionToAgent_CodeOutsideSet_Rejected(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{})
	action := h.seedLookupDecision(domain.AgentActionRunning)
	eventsBefore := len(h.events())

	for _, code := range []domain.AgentReturnCode{"TOOL_ERROR", "PRECONDITION_UNMET", "RESULT_UNCERTAIN", ""} {
		nextTurnID, err := h.returnAction(action, domain.AgentActionRunning, code, "not returnable")
		if !errors.Is(err, errReturnCodeNotReturnable) || nextTurnID != "" {
			t.Errorf("return with %q = %q, %v, want errReturnCodeNotReturnable", code, nextTurnID, err)
		}
	}
	if got := h.getAction(action.ID); got.Status != domain.AgentActionRunning {
		t.Errorf("action = %s, want still RUNNING", got.Status)
	}
	if got := len(h.events()); got != eventsBefore {
		t.Errorf("events = %d, want %d (nothing appended)", got, eventsBefore)
	}
	if h.hasContextVersion(1) {
		t.Errorf("context version 1 exists, want none")
	}
}

func TestReturnActionToAgent_OversizedMessage_ToolMessageBoundedWithoutArguments(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{})
	action := h.seedLookupDecision(domain.AgentActionRunning)
	long := strings.Repeat("é", 3000) + " " + seededArgumentValue

	nextTurnID, err := h.returnAction(action, domain.AgentActionRunning, domain.ReturnStalePatch, long)
	if err != nil {
		t.Fatalf("return with an oversized message: %v", err)
	}
	assertReturned(t, h, action, domain.ReturnStalePatch, nextTurnID)
	message := h.toolMessage(action.ID)
	text, _ := message["message"].(string)
	if len(text) == 0 || len(text) > maxReturnMessageBytes || !strings.HasPrefix(text, "éé") {
		t.Errorf("message = %d bytes %q, want a non-empty prefix of the original within %d bytes", len(text), text[:min(len(text), 20)], maxReturnMessageBytes)
	}
	if failed := h.getAction(action.ID); failed.Error == nil || len(failed.Error.Message) > maxReturnMessageBytes {
		t.Errorf("action error message is not bounded: %+v", failed.Error)
	}
}

func TestClaimAgentAction_ReturnUnmetRequirementOn_ReturnsVerificationMissingWithoutAttempt(t *testing.T) {
	executor := &guardedExecutor{}
	h := newReturnHarness(t, returnHarnessOptions{tools: []registry.ToolRegistration{guardedToolRegistration(true, executor)}})
	action := h.seedDecision(guardedToolName, json.RawMessage(`{"patchDigest":"`+seededArgumentValue+`"}`), domain.AgentActionReady)

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute guarded action: %v", err)
	}
	if executor.calls.Load() != 0 {
		t.Errorf("the guarded tool was called %d times, want 0", executor.calls.Load())
	}
	offered := h.queue.offered()
	if len(offered) != 1 {
		t.Fatalf("turns offered to the queue after COMMIT = %v, want exactly the new Turn", offered)
	}
	h.assertNoAttempt(action.ID)
	assertReturned(t, h, action, domain.ReturnVerificationMissing, offered[0])
	message := h.toolMessage(action.ID)
	text, _ := message["message"].(string)
	if !strings.Contains(text, `"`+guardedFactType+`"`) {
		t.Errorf("tool message %q does not name the missing fact type %s", text, guardedFactType)
	}
}

func TestClaimAgentAction_ReturnedTurnEnqueueRefused_TurnCommittedForReconciler(t *testing.T) {
	executor := &guardedExecutor{}
	h := newReturnHarness(t, returnHarnessOptions{tools: []registry.ToolRegistration{guardedToolRegistration(true, executor)}})
	h.queue.refuse.Store(true)
	action := h.seedDecision(guardedToolName, json.RawMessage(`{"patchDigest":"`+seededArgumentValue+`"}`), domain.AgentActionReady)

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute guarded action with a refusing queue: %v", err)
	}
	offered := h.queue.offered()
	if len(offered) != 1 {
		t.Fatalf("turns offered to the queue after COMMIT = %v, want the new Turn offered once and refused", offered)
	}
	// The refused Turn is committed, READY, and exactly what the Reconciler's rediscovery
	// query returns.
	ready := h.readyTurns()
	if len(ready) != 1 || ready[0].ID != offered[0] || ready[0].TurnNo != 2 {
		t.Fatalf("READY turns in the database = %+v, want exactly the refused turn %s as turn 2", ready, offered[0])
	}
	h.assertNoAttempt(action.ID)
	assertReturned(t, h, action, domain.ReturnVerificationMissing, ready[0].ID)
}

func TestClaimAgentAction_ReturnUnmetRequirementOff_TerminatesPreconditionUnmet(t *testing.T) {
	executor := &guardedExecutor{}
	h := newReturnHarness(t, returnHarnessOptions{tools: []registry.ToolRegistration{guardedToolRegistration(false, executor)}})
	action := h.seedDecision(guardedToolName, json.RawMessage(`{"patchDigest":"`+seededArgumentValue+`"}`), domain.AgentActionReady)

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute guarded action: %v", err)
	}
	if executor.calls.Load() != 0 {
		t.Errorf("the guarded tool was called %d times, want 0", executor.calls.Load())
	}
	h.assertNoAttempt(action.ID)
	failed := h.getAction(action.ID)
	if failed.Status != domain.AgentActionFailed || failed.Error == nil || failed.Error.Code != runtime.CodePreconditionUnmet {
		t.Errorf("action = %s %+v, want FAILED with PRECONDITION_UNMET", failed.Status, failed.Error)
	}
	agentRun := h.getAgentRun()
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Errorf("agent run termination = %v, want INVALID_ACTION", agentRun.Termination)
	}
	if payloads := h.actionFailedPayloads(action.ID); len(payloads) != 1 || payloads[0]["returnedToAgent"] != nil || payloads[0]["failureSource"] != string(domain.FailureSyncExecution) {
		t.Errorf("AGENT_ACTION_FAILED = %v, want one terminal SYNC_EXECUTION failure without returnedToAgent", payloads)
	}
	if nos := h.readyTurnNos(); len(nos) != 0 {
		t.Errorf("READY turns = %v, want none", nos)
	}
	if offered := h.queue.offered(); len(offered) != 0 {
		t.Errorf("turns offered = %v, want none", offered)
	}
}

func TestExecuteAgentAction_ResultWithUndeclaredRecord_ToolResultInvalid(t *testing.T) {
	h := newReturnHarness(t, returnHarnessOptions{lookupExecutor: recordingLookup{}})
	action := h.seedLookupDecision(domain.AgentActionReady)

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute lookup: %v", err)
	}
	failed := h.getAction(action.ID)
	if failed.Status != domain.AgentActionFailed || failed.Error == nil || failed.Error.Code != "TOOL_RESULT_INVALID" {
		t.Errorf("action = %s %+v, want FAILED with TOOL_RESULT_INVALID", failed.Status, failed.Error)
	}
	if failed.Error != nil && !strings.Contains(failed.Error.Message, "WORKSPACE_PATCH") {
		t.Errorf("error message %q does not name the undeclared record kind", failed.Error.Message)
	}
	if agentRun := h.getAgentRun(); agentRun.Termination == nil || *agentRun.Termination != domain.TerminationToolError {
		t.Errorf("agent run termination = %v, want TOOL_ERROR", agentRun.Termination)
	}
}

func (h *returnHarness) assertNoAttempt(actionID string) {
	h.t.Helper()
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		attempts, err := tx.ToolAttempts().ListByActionID(ctx, actionID)
		if err != nil {
			return err
		}
		if len(attempts) != 0 {
			h.t.Errorf("tool attempts of %s = %d, want none", actionID, len(attempts))
		}
		return nil
	}); err != nil {
		h.t.Fatalf("list attempts: %v", err)
	}
}
