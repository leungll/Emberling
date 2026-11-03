//go:build integration

// Authorization and approval tests: a Tool call whose declared risk needs a person's
// decision is suspended, decided once, and executed or returned to the Agent while the
// Agent's clock stays paused for exactly the time the person took. These tests drive the
// real claim and decision use cases over PostgreSQL, so the conditional updates, the Events
// and the deadline arithmetic are proved against committed rows.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// taskInputNodeType is a test Input Node that publishes the object its config holds on
// its text port, so the Agent node receives an input a goalPointer can resolve in.
const taskInputNodeType = "test_task_input"

type taskInputExecutor struct{}

func (taskInputExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (taskInputExecutor) Execute(_ context.Context, _ registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	raw, err := json.Marshal(config["task"])
	if err != nil {
		return registry.NodeResult{}, err
	}
	return registry.CompletedResult(map[string]json.RawMessage{"text": raw}), nil
}

func taskInputRegistration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          taskInputNodeType,
			DisplayName:   "Test Task Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{"task":{"type":"object"}},"required":["task"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: taskInputExecutor{}},
	}
}

// The two Tools the approval tests register: one whose declared risk changes a production
// system and one that does not. Both are synchronous and record every call they receive.
const (
	productionToolName = "test_production_tool"
	lowRiskToolName    = "test_low_risk_tool"
	// approvalWaitMs bounds the approval wait of every Agent node these tests build.
	approvalWaitMs = 3_600_000
)

type recordingToolExecutor struct {
	mu    sync.Mutex
	calls []json.RawMessage
}

func (e *recordingToolExecutor) Execute(_ context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, append(json.RawMessage(nil), action.Arguments...))
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: json.RawMessage(`{"ok":true}`)}}, nil
}

func (e *recordingToolExecutor) received() []json.RawMessage {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]json.RawMessage(nil), e.calls...)
}

func riskToolRegistration(name string, risk domain.ToolAuthorizationRisk, executor registry.ToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          name,
			Description:   "A test tool with a declared risk",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","minLength":1}},"required":["target"],"additionalProperties":false}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionSync,
			Authorization: &domain.ToolAuthorization{Risk: risk},
		},
		Executor: executor,
	}
}

// guardedProductionToolRegistration is the production Tool with a fact requirement no
// test records a fact for, registered to return the unmet requirement to the Agent.
func guardedProductionToolRegistration(executor registry.ToolExecutor) registry.ToolRegistration {
	reg := riskToolRegistration(productionToolName, domain.ToolRiskProduction, executor)
	reg.Metadata.Requires = []domain.FactRequirement{{FactType: guardedFactType, SubjectArgument: "/target"}}
	reg.Metadata.ReturnUnmetRequirement = true
	return reg
}

func productionArguments() json.RawMessage {
	return json.RawMessage(`{"target":"` + seededArgumentValue + `"}`)
}

// sameJSON compares two JSON documents by value: the arguments come back from PostgreSQL
// in its own spacing.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ca, _ := json.Marshal(x)
	cb, _ := json.Marshal(y)
	return string(ca) == string(cb)
}

// decisionGate is the barrier the concurrency tests install as BeforeDecisionLock: every
// decision that reaches it has read the approval PENDING and then waits for the test to
// release it, in the order the test chooses, before it takes the Run lock.
type decisionGate struct {
	mu       sync.Mutex
	arrivals []chan struct{}
	arrived  chan struct{}
}

func newDecisionGate() *decisionGate {
	return &decisionGate{arrived: make(chan struct{}, 8)}
}

func (g *decisionGate) hook(string) {
	release := make(chan struct{})
	g.mu.Lock()
	g.arrivals = append(g.arrivals, release)
	g.mu.Unlock()
	g.arrived <- struct{}{}
	<-release
}

// awaitArrival blocks until one more decision has read the approval and is waiting at the
// gate, and returns its ordinal.
func (g *decisionGate) awaitArrival(t *testing.T) int {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(10 * time.Second):
		t.Fatalf("no decision reached the gate")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.arrivals) - 1
}

func (g *decisionGate) release(i int) {
	g.mu.Lock()
	ch := g.arrivals[i]
	g.mu.Unlock()
	close(ch)
}

// approvalHarness builds a Run whose Agent froze goal, with the production Tool (and the
// low-risk one) allowed, and seeds a READY TOOL_CALL Action of toolName.
func approvalHarness(t *testing.T, goal domain.AgentGoal, executor registry.ToolExecutor, extra ...registry.ToolRegistration) *returnHarness {
	t.Helper()
	tools := append([]registry.ToolRegistration{riskToolRegistration(productionToolName, domain.ToolRiskProduction, executor)}, extra...)
	return newReturnHarness(t, returnHarnessOptions{goal: goal, approvalTimeoutMs: approvalWaitMs, tools: tools})
}

func (h *returnHarness) getApprovalOf(actionID string) domain.Approval {
	h.t.Helper()
	var approval domain.Approval
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		approval, err = tx.Approvals().GetByActionID(ctx, actionID)
		return err
	}); err != nil {
		h.t.Fatalf("get approval of action %s: %v", actionID, err)
	}
	return approval
}

func (h *returnHarness) hasApproval(actionID string) bool {
	h.t.Helper()
	found := true
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Approvals().GetByActionID(ctx, actionID)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		h.t.Fatalf("look up approval of action %s: %v", actionID, err)
	}
	return found
}

func (h *returnHarness) attemptCount(actionID string) int {
	h.t.Helper()
	var n int
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		attempts, err := tx.ToolAttempts().ListByActionID(ctx, actionID)
		n = len(attempts)
		return err
	}); err != nil {
		h.t.Fatalf("list attempts: %v", err)
	}
	return n
}

// payloadsOf decodes every Event of typ the Run has.
func (h *returnHarness) payloadsOf(typ domain.EventType) []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for _, event := range h.events() {
		if event.Type != typ {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			h.t.Fatalf("decode %s payload: %v", typ, err)
		}
		out = append(out, payload)
	}
	return out
}

// suspendForApproval claims the seeded production Action and proves it ended up waiting on
// a person, returning the PENDING approval.
func (h *returnHarness) suspendForApproval(action domain.AgentAction) domain.Approval {
	h.t.Helper()
	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		h.t.Fatalf("execute production action: %v", err)
	}
	if got := h.getAction(action.ID); got.Status != domain.AgentActionWaitingApproval {
		h.t.Fatalf("action = %s, want WAITING_APPROVAL", got.Status)
	}
	approval := h.getApprovalOf(action.ID)
	if approval.Status != domain.ApprovalPending {
		h.t.Fatalf("approval = %s, want PENDING", approval.Status)
	}
	return approval
}

func (h *returnHarness) decide(approvalID string, decision domain.ApprovalStatus, reason string) (DecideApprovalOutcome, error) {
	h.t.Helper()
	return h.svc.DecideApproval(h.ctx, DecideApproval{ApprovalID: approvalID, Decision: decision, Reason: reason, DecidedBy: "test-operator"})
}

// assertReturnedToAgent is assertReturned for a refusal whose next Turn the test did not
// get back from the use case: the one READY Turn the Agent Run has is it.
func assertReturnedToAgent(t *testing.T, h *returnHarness, action domain.AgentAction, code domain.AgentReturnCode) {
	t.Helper()
	turns := h.readyTurns()
	if len(turns) != 1 {
		t.Fatalf("READY turns = %d, want exactly the one the refusal created", len(turns))
	}
	assertReturned(t, h, action, code, turns[0].ID)
}

func assertNotPaused(t *testing.T, agentRun domain.AgentRun) {
	t.Helper()
	if agentRun.PausedAt != nil || agentRun.PauseReason != nil {
		t.Errorf("agent run paused at %v for %v, want the clock running", agentRun.PausedAt, agentRun.PauseReason)
	}
}

// ---------------------------------------------------------------------------------------

func TestAgentAction_ProductionToolUnderPrepareOnly_ReturnsGoalDenied(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalPrepareOnly, executor)
	if goal := h.getAgentRun().Goal; goal == nil || *goal != domain.AgentGoalPrepareOnly {
		t.Fatalf("agent run goal = %v, want PREPARE_ONLY frozen at start", goal)
	}
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute production action: %v", err)
	}

	assertReturnedToAgent(t, h, action, domain.ReturnGoalDenied)
	if len(executor.received()) != 0 {
		t.Errorf("the production tool was called %d times, want 0", len(executor.received()))
	}
	h.assertNoAttempt(action.ID)
	if h.hasApproval(action.ID) {
		t.Errorf("an approval was created for a call the goal denies")
	}
	denied := h.getAction(action.ID)
	if denied.Authorization == nil || *denied.Authorization != domain.AuthorizationDeny || denied.AuthorizationReason == nil {
		t.Errorf("action authorization = %v %v, want DENY with a reason", denied.Authorization, denied.AuthorizationReason)
	}
	assertNotPaused(t, h.getAgentRun())
	if h.eventCount(domain.EventAgentPaused) != 0 || h.eventCount(domain.EventAgentActionWaitingApproval) != 0 {
		t.Errorf("a denied call paused the agent or asked for approval")
	}
}

func TestAgentAction_LowRiskTool_RunsWithoutApproval(t *testing.T) {
	production := &recordingToolExecutor{}
	low := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalPrepareOnly, production, riskToolRegistration(lowRiskToolName, domain.ToolRiskLow, low))
	action := h.seedDecision(lowRiskToolName, productionArguments(), domain.AgentActionReady)

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute low-risk action: %v", err)
	}
	if len(low.received()) != 1 {
		t.Fatalf("the low-risk tool was called %d times, want 1", len(low.received()))
	}
	done := h.getAction(action.ID)
	if done.Status != domain.AgentActionSucceeded || done.Authorization == nil || *done.Authorization != domain.AuthorizationAllow {
		t.Errorf("action = %s authorization %v, want SUCCEEDED with ALLOW", done.Status, done.Authorization)
	}
	if h.hasApproval(action.ID) || h.eventCount(domain.EventAgentPaused) != 0 {
		t.Errorf("a low-risk call created an approval or paused the agent")
	}
}

func TestAgentAction_ProductionToolUnderFixAndDeploy_WaitsForApproval(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	before := h.getAgentRun()
	now := h.clock.Now()

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute production action: %v", err)
	}

	waiting := h.getAction(action.ID)
	if waiting.Status != domain.AgentActionWaitingApproval || waiting.Authorization == nil || *waiting.Authorization != domain.AuthorizationAsk {
		t.Errorf("action = %s authorization %v, want WAITING_APPROVAL with ASK", waiting.Status, waiting.Authorization)
	}
	if waiting.WaitingAt == nil {
		t.Errorf("action waitingAt = nil, want the suspension time")
	}
	approval := h.getApprovalOf(action.ID)
	if approval.Status != domain.ApprovalPending || !approval.RequestedAt.Equal(now) || !approval.Deadline.Equal(now.Add(approvalWaitMs*time.Millisecond)) {
		t.Errorf("approval = %+v, want PENDING requested now with the configured deadline", approval)
	}
	if approval.RunID != h.runID || approval.AgentRunID != h.agentRunID || approval.DecidedAt != nil || approval.DecidedBy != nil {
		t.Errorf("approval identity = %+v, want this Run and Agent Run, undecided", approval)
	}
	agentRun := h.getAgentRun()
	if agentRun.PausedAt == nil || !agentRun.PausedAt.Equal(now) || agentRun.PauseReason == nil || *agentRun.PauseReason != domain.PauseApproval {
		t.Errorf("agent run pause = %v %v, want paused now for APPROVAL", agentRun.PausedAt, agentRun.PauseReason)
	}
	if !agentRun.Deadline.Equal(before.Deadline) {
		t.Errorf("deadline moved on pause: %v -> %v", before.Deadline, agentRun.Deadline)
	}
	if nodeRun := h.getNodeRun(); nodeRun.Status != domain.NodeRunWaitingCallback {
		t.Errorf("agent node run = %s, want WAITING_CALLBACK", nodeRun.Status)
	}
	if run := h.getRun(); run.Status != domain.RunPaused {
		t.Errorf("run = %s, want PAUSED", run.Status)
	}
	if h.eventCount(domain.EventAgentActionWaitingApproval) != 1 || h.eventCount(domain.EventAgentPaused) != 1 {
		t.Errorf("events: waiting approval %d, paused %d, want one each", h.eventCount(domain.EventAgentActionWaitingApproval), h.eventCount(domain.EventAgentPaused))
	}
	if payloads := h.payloadsOf(domain.EventAgentActionWaitingApproval); len(payloads) == 1 && (payloads[0]["approvalId"] != approval.ID || payloads[0]["actionId"] != action.ID) {
		t.Errorf("AGENT_ACTION_WAITING_APPROVAL = %v, want approval %s of action %s", payloads[0], approval.ID, action.ID)
	}
	if h.eventCount(domain.EventAgentActionStarted) != 0 {
		t.Errorf("AGENT_ACTION_STARTED was written for a call nobody approved")
	}
	h.assertNoAttempt(action.ID)
	if len(executor.received()) != 0 {
		t.Errorf("the production tool was called while waiting for approval")
	}
	if nos := h.readyTurnNos(); len(nos) != 0 {
		t.Errorf("READY turns = %v, want none while waiting", nos)
	}
	if offered := h.queue.offered(); len(offered) != 0 {
		t.Errorf("turns offered = %v, want none", offered)
	}
}

func TestDecideApproval_Approve_ExecutesToolOnceAndResumes(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	originalDeadline := h.getAgentRun().Deadline
	const paused = 7 * time.Minute
	h.clock.Advance(paused)

	outcome, err := h.decide(approval.ID, domain.ApprovalApproved, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if outcome.Repeated || outcome.Approval.Status != domain.ApprovalApproved || outcome.Approval.DecidedBy == nil || *outcome.Approval.DecidedBy != "test-operator" {
		t.Errorf("outcome = %+v, want a fresh APPROVED decision by test-operator", outcome)
	}

	calls := executor.received()
	if len(calls) != 1 || !sameJSON(calls[0], productionArguments()) {
		t.Fatalf("production tool calls = %s, want exactly one with the committed arguments", calls)
	}
	if h.eventCount(domain.EventAgentActionStarted) != 1 || h.attemptCount(action.ID) != 1 {
		t.Errorf("AGENT_ACTION_STARTED = %d, attempts = %d, want one each", h.eventCount(domain.EventAgentActionStarted), h.attemptCount(action.ID))
	}
	if started := h.payloadsOf(domain.EventAgentActionStarted); len(started) == 1 && started[0]["claimSource"] != string(domain.ClaimImmediate) {
		t.Errorf("AGENT_ACTION_STARTED claimSource = %v, want IMMEDIATE", started[0]["claimSource"])
	}
	done := h.getAction(action.ID)
	if done.Status != domain.AgentActionSucceeded || done.Authorization == nil || *done.Authorization != domain.AuthorizationAsk {
		t.Errorf("action = %s authorization %v, want SUCCEEDED keeping ASK", done.Status, done.Authorization)
	}
	agentRun := h.getAgentRun()
	assertNotPaused(t, agentRun)
	if want := originalDeadline.Add(paused); !agentRun.Deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v (moved by the paused duration)", agentRun.Deadline, want)
	}
	if agentRun.Termination != nil || agentRun.CurrentTurnNo != 2 {
		t.Errorf("agent run = termination %v turn %d, want running at turn 2", agentRun.Termination, agentRun.CurrentTurnNo)
	}
	if nos := h.readyTurnNos(); len(nos) != 1 || nos[0] != 2 {
		t.Errorf("READY turns = %v, want [2]", nos)
	}
	resumed := h.payloadsOf(domain.EventAgentResumed)
	if len(resumed) != 1 || resumed[0]["pausedMs"] != float64(paused.Milliseconds()) || resumed[0]["reason"] != string(domain.PauseApproval) {
		t.Errorf("AGENT_RESUMED = %v, want one with pausedMs %d for APPROVAL", resumed, paused.Milliseconds())
	}
	if decided := h.payloadsOf(domain.EventApprovalDecided); len(decided) != 1 || decided[0]["decision"] != string(domain.ApprovalApproved) || decided[0]["decidedBy"] != "test-operator" {
		t.Errorf("APPROVAL_DECIDED = %v, want one APPROVED by test-operator", decided)
	}
	if run := h.getRun(); run.Status != domain.RunRunning {
		t.Errorf("run = %s, want RUNNING again", run.Status)
	}
}

func TestDecideApproval_ConcurrentApprovals_OneEffectiveSecondRepeated(t *testing.T) {
	executor := &recordingToolExecutor{}
	gate := newDecisionGate()
	h := newReturnHarness(t, returnHarnessOptions{
		goal: domain.AgentGoalFixAndDeploy, approvalTimeoutMs: approvalWaitMs,
		tools:         []registry.ToolRegistration{riskToolRegistration(productionToolName, domain.ToolRiskProduction, executor)},
		approvalHooks: ApprovalHooks{BeforeDecisionLock: gate.hook},
	})
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)

	type result struct {
		outcome DecideApprovalOutcome
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			outcome, err := h.decide(approval.ID, domain.ApprovalApproved, "")
			results <- result{outcome, err}
		}()
	}
	first, second := gate.awaitArrival(t), gate.awaitArrival(t)
	// Both decisions have read the approval PENDING; the lock decides the order.
	gate.release(first)
	gate.release(second)

	repeated := 0
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatalf("approve: %v", r.err)
		}
		if r.outcome.Approval.Status != domain.ApprovalApproved {
			t.Errorf("outcome approval = %s, want APPROVED", r.outcome.Approval.Status)
		}
		if r.outcome.Repeated {
			repeated++
		}
	}
	if repeated != 1 {
		t.Errorf("repeated outcomes = %d, want exactly one of two", repeated)
	}
	if calls := executor.received(); len(calls) != 1 {
		t.Errorf("production tool calls = %d, want 1", len(calls))
	}
	if h.eventCount(domain.EventApprovalDecided) != 1 || h.eventCount(domain.EventAgentResumed) != 1 || h.eventCount(domain.EventAgentActionStarted) != 1 {
		t.Errorf("events decided %d resumed %d started %d, want one each", h.eventCount(domain.EventApprovalDecided), h.eventCount(domain.EventAgentResumed), h.eventCount(domain.EventAgentActionStarted))
	}
	if h.attemptCount(action.ID) != 1 {
		t.Errorf("attempts = %d, want 1", h.attemptCount(action.ID))
	}
}

// expiryRace suspends a production Action, moves the clock past the approval deadline and
// lets an approval and the expiry both read the approval PENDING before either takes the
// Run lock; the one released first wins. It returns the approval's final state and what
// the losing approve call got back.
func expiryRace(t *testing.T, approveFirst bool) (*recordingToolExecutor, *returnHarness, domain.AgentAction, error) {
	t.Helper()
	executor := &recordingToolExecutor{}
	gate := newDecisionGate()
	h := newReturnHarness(t, returnHarnessOptions{
		goal: domain.AgentGoalFixAndDeploy, approvalTimeoutMs: approvalWaitMs,
		tools:         []registry.ToolRegistration{riskToolRegistration(productionToolName, domain.ToolRiskProduction, executor)},
		approvalHooks: ApprovalHooks{BeforeDecisionLock: gate.hook},
	})
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	h.clock.Advance(approvalWaitMs*time.Millisecond + time.Second)

	approveErr := make(chan error, 1)
	expireErr := make(chan error, 1)
	go func() {
		_, err := h.decide(approval.ID, domain.ApprovalApproved, "")
		approveErr <- err
	}()
	approveAt := gate.awaitArrival(t)
	go func() { expireErr <- h.svc.ExpireApproval(h.ctx, approval.ID) }()
	expireAt := gate.awaitArrival(t)

	var approveResult error
	if approveFirst {
		gate.release(approveAt)
		approveResult = <-approveErr
		gate.release(expireAt)
		if err := <-expireErr; err != nil {
			t.Fatalf("expire after approve: %v", err)
		}
	} else {
		gate.release(expireAt)
		if err := <-expireErr; err != nil {
			t.Fatalf("expire: %v", err)
		}
		gate.release(approveAt)
		approveResult = <-approveErr
	}
	return executor, h, action, approveResult
}

func TestDecideApproval_ApproveBeforeExpiry_ExpiryWritesNothing(t *testing.T) {
	executor, h, action, approveErr := expiryRace(t, true)
	if approveErr != nil {
		t.Fatalf("approve: %v", approveErr)
	}
	if approval := h.getApprovalOf(action.ID); approval.Status != domain.ApprovalApproved {
		t.Errorf("approval = %s, want APPROVED", approval.Status)
	}
	if len(executor.received()) != 1 {
		t.Errorf("production tool calls = %d, want 1", len(executor.received()))
	}
	if h.eventCount(domain.EventApprovalDecided) != 1 || h.eventCount(domain.EventAgentResumed) != 1 {
		t.Errorf("events decided %d resumed %d, want one each: the expiry wrote nothing", h.eventCount(domain.EventApprovalDecided), h.eventCount(domain.EventAgentResumed))
	}
	if h.getAction(action.ID).Status != domain.AgentActionSucceeded {
		t.Errorf("action = %s, want SUCCEEDED", h.getAction(action.ID).Status)
	}
}

func TestExpireApproval_ExpiryBeforeApprove_ApproveConflicts(t *testing.T) {
	executor, h, action, approveErr := expiryRace(t, false)
	var conflict *ApprovalConflictError
	if !errors.As(approveErr, &conflict) || conflict.Status != domain.ApprovalExpired {
		t.Fatalf("approve after expiry = %v, want ApprovalConflictError with EXPIRED", approveErr)
	}
	if approval := h.getApprovalOf(action.ID); approval.Status != domain.ApprovalExpired || approval.DecidedBy != nil {
		t.Errorf("approval = %+v, want EXPIRED with nobody deciding", approval)
	}
	if len(executor.received()) != 0 {
		t.Errorf("production tool was called after its approval expired")
	}
	assertReturnedToAgent(t, h, action, domain.ReturnApprovalExpired)
	if h.eventCount(domain.EventApprovalDecided) != 1 || h.eventCount(domain.EventAgentActionStarted) != 0 {
		t.Errorf("events decided %d started %d, want 1 and 0", h.eventCount(domain.EventApprovalDecided), h.eventCount(domain.EventAgentActionStarted))
	}
}

func TestDecideApproval_RejectThenApprove_Conflict(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)

	if _, err := h.decide(approval.ID, domain.ApprovalRejected, "not during the freeze"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	eventsAfterReject := len(h.events())

	_, err := h.decide(approval.ID, domain.ApprovalApproved, "")
	var conflict *ApprovalConflictError
	if !errors.As(err, &conflict) || conflict.Status != domain.ApprovalRejected || conflict.ApprovalID != approval.ID {
		t.Fatalf("approve after reject = %v, want ApprovalConflictError with REJECTED", err)
	}
	if len(h.events()) != eventsAfterReject {
		t.Errorf("the conflicting approve wrote %d events", len(h.events())-eventsAfterReject)
	}
	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalRejected {
		t.Errorf("approval = %s, want REJECTED kept", got.Status)
	}
	if len(executor.received()) != 0 {
		t.Errorf("production tool was called after a rejection")
	}
	// The same decision again is answered without a write.
	again, err := h.decide(approval.ID, domain.ApprovalRejected, "not during the freeze")
	if err != nil || !again.Repeated || again.Approval.Status != domain.ApprovalRejected {
		t.Errorf("repeated reject = %+v, %v, want Repeated REJECTED", again, err)
	}
	if len(h.events()) != eventsAfterReject {
		t.Errorf("the repeated reject wrote events")
	}
}

func TestDecideApproval_Reject_ReturnsReasonAndResumesClock(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	originalDeadline := h.getAgentRun().Deadline
	const paused = 3 * time.Minute
	h.clock.Advance(paused)
	const reason = "the change window is closed"

	outcome, err := h.decide(approval.ID, domain.ApprovalRejected, reason)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if outcome.Repeated || outcome.Approval.Status != domain.ApprovalRejected || outcome.Approval.Reason == nil || *outcome.Approval.Reason != reason {
		t.Errorf("outcome = %+v, want REJECTED with the reason", outcome)
	}

	assertReturnedToAgent(t, h, action, domain.ReturnApprovalRejected)
	message := h.toolMessage(action.ID)
	if text, _ := message["message"].(string); !strings.Contains(text, reason) {
		t.Errorf("tool message = %v, want the rejection reason in it", message)
	}
	agentRun := h.getAgentRun()
	assertNotPaused(t, agentRun)
	if want := originalDeadline.Add(paused); !agentRun.Deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v", agentRun.Deadline, want)
	}
	if len(executor.received()) != 0 || h.attemptCount(action.ID) != 0 {
		t.Errorf("a rejected call ran or got an Attempt")
	}
	if decided := h.payloadsOf(domain.EventApprovalDecided); len(decided) != 1 || decided[0]["decision"] != string(domain.ApprovalRejected) || decided[0]["reason"] != nil {
		t.Errorf("APPROVAL_DECIDED = %v, want one REJECTED without the reason text", decided)
	}
	if offered := h.queue.offered(); len(offered) != 1 {
		t.Errorf("turns offered = %v, want the next Turn once", offered)
	}
}

func TestExpireApproval_PastDeadline_ReturnsExpiredAndResumesClock(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	originalDeadline := h.getAgentRun().Deadline

	// Before the deadline the expiry is not due: the scan does not list it and the use
	// case leaves it alone.
	h.clock.Advance(approvalWaitMs*time.Millisecond - time.Second)
	if due := h.listExpiredApprovals(); len(due) != 0 {
		t.Errorf("expired approvals before the deadline = %v, want none", due)
	}
	if err := h.svc.ExpireApproval(h.ctx, approval.ID); err != nil {
		t.Fatalf("expire before deadline: %v", err)
	}
	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalPending {
		t.Fatalf("approval before its deadline = %s, want PENDING", got.Status)
	}

	h.clock.Advance(time.Second)
	if due := h.listExpiredApprovals(); len(due) != 1 || due[0].ID != approval.ID {
		t.Errorf("expired approvals at the deadline = %v, want [%s]", due, approval.ID)
	}
	if err := h.svc.ExpireApproval(h.ctx, approval.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	got := h.getApprovalOf(action.ID)
	if got.Status != domain.ApprovalExpired || got.DecidedAt == nil || got.DecidedBy != nil {
		t.Errorf("approval = %+v, want EXPIRED, decided at the deadline, by nobody", got)
	}
	assertReturnedToAgent(t, h, action, domain.ReturnApprovalExpired)
	agentRun := h.getAgentRun()
	assertNotPaused(t, agentRun)
	if want := originalDeadline.Add(approvalWaitMs * time.Millisecond); !agentRun.Deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v", agentRun.Deadline, want)
	}
	if len(executor.received()) != 0 || h.attemptCount(action.ID) != 0 {
		t.Errorf("an expired call ran or got an Attempt")
	}
	// A second expiry pass finds nothing to do and writes nothing.
	events := len(h.events())
	if err := h.svc.ExpireApproval(h.ctx, approval.ID); err != nil {
		t.Fatalf("expire again: %v", err)
	}
	if len(h.events()) != events {
		t.Errorf("a repeated expiry wrote events")
	}
}

func (h *returnHarness) listExpiredApprovals() []domain.Approval {
	h.t.Helper()
	var out []domain.Approval
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.Approvals().ListExpiredPending(ctx, h.clock.Now(), 10)
		return err
	}); err != nil {
		h.t.Fatalf("list expired approvals: %v", err)
	}
	return out
}

func (h *returnHarness) listExpiredAgentRuns() []string {
	h.t.Helper()
	var ids []string
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		runs, err := tx.AgentRuns().ListExpired(ctx, h.clock.Now(), 50)
		for _, run := range runs {
			if run.ID == h.agentRunID {
				ids = append(ids, run.ID)
			}
		}
		return err
	}); err != nil {
		h.t.Fatalf("list expired agent runs: %v", err)
	}
	return ids
}

func TestAgentApprovalWait_OriginalDeadlinePasses_NoTimeoutAndDeadlineShifts(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	before := h.getAgentRun()

	// The Agent node's timeoutMs is 10 minutes; the person takes 25.
	const paused = 25 * time.Minute
	h.clock.Advance(paused)
	if !h.clock.Now().After(before.Deadline) {
		t.Fatalf("test setup: the clock %v is not past the original deadline %v", h.clock.Now(), before.Deadline)
	}
	if expired := h.listExpiredAgentRuns(); len(expired) != 0 {
		t.Errorf("ListExpired returned the paused Agent Run %v", expired)
	}
	if err := h.svc.TimeoutAgentRun(h.ctx, h.agentRunID); err != nil {
		t.Fatalf("timeout while paused: %v", err)
	}
	stillPaused := h.getAgentRun()
	if stillPaused.Termination != nil || stillPaused.PausedAt == nil {
		t.Fatalf("agent run = termination %v paused %v, want still paused and not timed out", stillPaused.Termination, stillPaused.PausedAt)
	}
	if h.eventCount(domain.EventAgentFailed) != 0 {
		t.Errorf("AGENT_FAILED was written for a paused Agent Run")
	}

	if _, err := h.decide(approval.ID, domain.ApprovalApproved, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	agentRun := h.getAgentRun()
	assertNotPaused(t, agentRun)
	if got, want := agentRun.Deadline.Sub(before.Deadline), paused; got != want {
		t.Errorf("deadline moved by %v, want %v (the paused duration)", got, want)
	}
	if !agentRun.StartedAt.Equal(before.StartedAt) {
		t.Errorf("startedAt changed on resume: %v -> %v", before.StartedAt, agentRun.StartedAt)
	}
	if resumed := h.payloadsOf(domain.EventAgentResumed); len(resumed) != 1 || resumed[0]["remainingMs"] != float64(agentRun.Deadline.Sub(h.clock.Now()).Milliseconds()) {
		t.Errorf("AGENT_RESUMED = %v, want remainingMs of the moved deadline", resumed)
	}
	if len(executor.received()) != 1 || agentRun.Termination != nil {
		t.Errorf("tool calls %d termination %v, want the call made and the agent running", len(executor.received()), agentRun.Termination)
	}
	if h.getAction(action.ID).Status != domain.AgentActionSucceeded {
		t.Errorf("action = %s, want SUCCEEDED", h.getAction(action.ID).Status)
	}
}

func TestDecideApproval_AfterRestart_ApprovalPendingAndExecutesOnce(t *testing.T) {
	before := &recordingToolExecutor{}
	h := newReturnHarness(t, returnHarnessOptions{
		goal: domain.AgentGoalFixAndDeploy, approvalTimeoutMs: approvalWaitMs, runnerID: "runner-before-restart",
		tools: []registry.ToolRegistration{riskToolRegistration(productionToolName, domain.ToolRiskProduction, before)},
	})
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)

	// The process restarts: a new service over the same rows, with its own registry.
	after := &recordingToolExecutor{}
	h.restart(returnHarnessOptions{
		runnerID: "runner-after-restart",
		tools:    []registry.ToolRegistration{riskToolRegistration(productionToolName, domain.ToolRiskProduction, after)},
	})
	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalPending || got.ID != approval.ID {
		t.Fatalf("approval after restart = %+v, want the same PENDING approval", got)
	}
	if got := h.getAction(action.ID); got.Status != domain.AgentActionWaitingApproval {
		t.Fatalf("action after restart = %s, want WAITING_APPROVAL", got.Status)
	}
	if agentRun := h.getAgentRun(); agentRun.PausedAt == nil {
		t.Fatalf("agent run after restart is not paused")
	}

	if _, err := h.decide(approval.ID, domain.ApprovalApproved, ""); err != nil {
		t.Fatalf("approve after restart: %v", err)
	}
	if len(before.received()) != 0 {
		t.Errorf("the stopped process's tool was called %d times", len(before.received()))
	}
	if calls := after.received(); len(calls) != 1 || !sameJSON(calls[0], productionArguments()) {
		t.Errorf("tool calls after restart = %s, want exactly the committed arguments once", calls)
	}
	if h.attemptCount(action.ID) != 1 || h.eventCount(domain.EventAgentActionStarted) != 1 {
		t.Errorf("attempts %d started events %d, want one each", h.attemptCount(action.ID), h.eventCount(domain.EventAgentActionStarted))
	}
	if h.getAction(action.ID).Status != domain.AgentActionSucceeded {
		t.Errorf("action = %s, want SUCCEEDED", h.getAction(action.ID).Status)
	}
	assertNotPaused(t, h.getAgentRun())
}

func TestDecideApproval_RequirementNoLongerMet_ReturnsVerificationMissingKeepsApproved(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)

	// While the person decides, the process restarts with the Tool now guarded by a fact
	// requirement nothing has recorded.
	h.restart(returnHarnessOptions{tools: []registry.ToolRegistration{guardedProductionToolRegistration(executor)}})
	const paused = 2 * time.Minute
	originalDeadline := h.getAgentRun().Deadline
	h.clock.Advance(paused)

	outcome, err := h.decide(approval.ID, domain.ApprovalApproved, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if outcome.Approval.Status != domain.ApprovalApproved {
		t.Errorf("outcome approval = %s, want APPROVED", outcome.Approval.Status)
	}
	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalApproved {
		t.Errorf("approval = %s, want APPROVED kept although the call was refused", got.Status)
	}
	assertReturnedToAgent(t, h, action, domain.ReturnVerificationMissing)
	h.assertNoAttempt(action.ID)
	if len(executor.received()) != 0 || h.eventCount(domain.EventAgentActionStarted) != 0 {
		t.Errorf("a call whose requirement is unmet ran or started an Attempt")
	}
	agentRun := h.getAgentRun()
	assertNotPaused(t, agentRun)
	if want := originalDeadline.Add(paused); !agentRun.Deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v", agentRun.Deadline, want)
	}
}

// failingNodeType is a test node whose every execution fails, so a Run can end while its
// Agent sibling is still waiting.
const failingNodeType = "test_failing_node"

type failingNodeExecutor struct{}

func (failingNodeExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (failingNodeExecutor) Execute(context.Context, registry.NodeInput, map[string]any) (registry.NodeResult, error) {
	return registry.NodeResult{}, errors.New("the sibling node always fails")
}

func failingNodeRegistration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          failingNodeType,
			DisplayName:   "Test Failing Node",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "image", DataType: domain.PortTypeImage, Required: true}},
			ConfigSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: failingNodeExecutor{}},
	}
}

// endRunThroughSibling claims and executes the failing sibling node, which the scheduler
// releases once the Agent NodeRun waits; its failure fails the Run while the Agent still
// waits for its approval.
func (h *returnHarness) endRunThroughSibling() {
	h.t.Helper()
	sibling := h.advance()
	if sibling.AgentRunID != "" {
		h.t.Fatalf("advance claimed an Agent NodeRun, want the failing sibling")
	}
	if err := h.svc.Execute(h.ctx, sibling); err != nil {
		h.t.Fatalf("execute failing sibling: %v", err)
	}
	if run := h.getRun(); run.Status != domain.RunFailed {
		h.t.Fatalf("run = %s after the sibling failed, want FAILED", run.Status)
	}
}

// reconcilableToolName is a test Tool registered as an asynchronous, polled, reconcilable
// production Tool: the shape of a release Tool, dispatching an external task it never
// completes in these tests.
const reconcilableToolName = "test_reconcilable_tool"

type reconcilableToolExecutor struct {
	mu    sync.Mutex
	calls []registry.ToolAction
}

func (e *reconcilableToolExecutor) Execute(_ context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, action)
	return registry.ToolExecutionResult{
		Kind:         registry.ToolResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: "test-release-provider", ExternalTaskID: "release-task-1"},
	}, nil
}

func (e *reconcilableToolExecutor) OnCallback(context.Context, registry.ToolAsyncState, []byte) (registry.ToolResult, error) {
	return registry.ToolResult{}, errors.New("the test release tool takes no callback")
}

func (e *reconcilableToolExecutor) Poll(context.Context, registry.ToolAsyncState) (registry.PollResult, error) {
	return registry.PollResult{Status: registry.PollRunning}, nil
}

func reconcilableToolRegistration(executor *reconcilableToolExecutor) registry.ToolRegistration {
	reg := riskToolRegistration(reconcilableToolName, domain.ToolRiskProduction, executor)
	reg.Metadata.ExecutionKind = domain.ToolExecutionAsync
	reg.Metadata.Poll = &domain.PollPolicy{IntervalMs: 30_000, MaxPolls: 10}
	reg.Metadata.Reconcilable = true
	return reg
}

func TestDecideApproval_RunEndedBeforeDecision_NotActionable(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := newReturnHarness(t, returnHarnessOptions{
		goal: domain.AgentGoalFixAndDeploy, approvalTimeoutMs: approvalWaitMs, failingSibling: true,
		tools: []registry.ToolRegistration{riskToolRegistration(productionToolName, domain.ToolRiskProduction, executor)},
	})
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	h.endRunThroughSibling()

	_, err := h.decide(approval.ID, domain.ApprovalApproved, "")
	var notActionable *ApprovalNotActionableError
	if !errors.As(err, &notActionable) || notActionable.ApprovalID != approval.ID {
		t.Fatalf("approve after the run ended = %v, want ApprovalNotActionableError for %s", err, approval.ID)
	}
	var conflict *ApprovalConflictError
	if errors.As(err, &conflict) {
		t.Errorf("the error must not read as an already decided approval")
	}
	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalPending {
		t.Errorf("approval = %s, want still PENDING: the decision wrote nothing", got.Status)
	}
	if h.attemptCount(action.ID) != 0 || len(executor.received()) != 0 {
		t.Errorf("a decision on an ended run created an Attempt or ran the tool")
	}
	if h.eventCount(domain.EventApprovalDecided) != 0 {
		t.Errorf("APPROVAL_DECIDED was written by a decision that cannot act")
	}
}

func TestExpireApproval_RunEndedBeforeDeadline_ExpiresWithoutReturn(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := newReturnHarness(t, returnHarnessOptions{
		goal: domain.AgentGoalFixAndDeploy, approvalTimeoutMs: approvalWaitMs, failingSibling: true,
		tools: []registry.ToolRegistration{riskToolRegistration(productionToolName, domain.ToolRiskProduction, executor)},
	})
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	h.endRunThroughSibling()
	pausedAgentRun := h.getAgentRun()
	h.clock.Advance(time.Duration(approvalWaitMs)*time.Millisecond + time.Second)

	if expired := h.listExpiredApprovals(); len(expired) != 1 || expired[0].ID != approval.ID {
		t.Fatalf("expired approvals before the scan = %v, want [%s]", expired, approval.ID)
	}
	if err := h.svc.ExpireApproval(h.ctx, approval.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalExpired || got.DecidedAt == nil || got.DecidedBy != nil {
		t.Errorf("approval = %+v, want EXPIRED with a decided time and no deciding person", got)
	}
	if remaining := h.listExpiredApprovals(); len(remaining) != 0 {
		t.Errorf("the expired approval is still listed for the scan")
	}
	if decided := h.payloadsOf(domain.EventApprovalDecided); len(decided) != 1 || decided[0]["decision"] != string(domain.ApprovalExpired) {
		t.Errorf("APPROVAL_DECIDED = %v, want one EXPIRED", decided)
	}
	// Nothing is returned to an Agent whose Run has ended: no refusal, no new Turn, and
	// the clock stays as the termination left it.
	if got := h.getAction(action.ID); got.Status != domain.AgentActionWaitingApproval {
		t.Errorf("action = %s, want left WAITING_APPROVAL", got.Status)
	}
	if h.eventCount(domain.EventAgentActionFailed) != 0 || h.eventCount(domain.EventAgentResumed) != 0 || len(h.readyTurns()) != 0 {
		t.Errorf("an ended run got a refusal, a resumed clock or a new Turn")
	}
	agentRun := h.getAgentRun()
	if agentRun.PausedAt == nil || !agentRun.Deadline.Equal(pausedAgentRun.Deadline) {
		t.Errorf("agent run = paused %v deadline %v, want unchanged (%v, %v)", agentRun.PausedAt, agentRun.Deadline, pausedAgentRun.PausedAt, pausedAgentRun.Deadline)
	}
	if run := h.getRun(); run.Status != domain.RunFailed {
		t.Errorf("run = %s, want still FAILED", run.Status)
	}

	// A decision after the expiry is a conflict with the recorded EXPIRED decision.
	_, err := h.decide(approval.ID, domain.ApprovalApproved, "")
	var conflict *ApprovalConflictError
	if !errors.As(err, &conflict) || conflict.Status != domain.ApprovalExpired {
		t.Errorf("approve after expiry = %v, want a conflict with EXPIRED", err)
	}
}

func TestDecideApproval_ProcessDiesBeforeWorkerCall_OrphanHandlingReturnsAction(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	// No worker takes the approved call: the process dies between COMMIT and the call.
	h.queue.mu.Lock()
	h.queue.svc = nil
	h.queue.mu.Unlock()
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	h.clock.Advance(time.Minute)

	outcome, err := h.decide(approval.ID, domain.ApprovalApproved, "")
	if err != nil || outcome.Approval.Status != domain.ApprovalApproved {
		t.Fatalf("approve = %+v, %v, want APPROVED", outcome, err)
	}
	if offered := h.queue.offeredActions(); len(offered) != 1 || offered[0] != action.ID {
		t.Fatalf("actions offered to the queue = %v, want [%s]", offered, action.ID)
	}
	if len(executor.received()) != 0 {
		t.Fatalf("the tool ran on the request goroutine")
	}
	attempts := h.toolAttempts(action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Fatalf("attempts = %+v, want one STARTED attempt committed by the approval", attempts)
	}
	if got := h.getAction(action.ID); got.Status != domain.AgentActionRunning {
		t.Fatalf("action = %s, want RUNNING with its STARTED attempt", got.Status)
	}

	// A new process over the same database: its runner identity differs, so the orphan
	// scan finds the STARTED Attempt and the orphan use case returns it to the Agent.
	h.restart(returnHarnessOptions{goal: domain.AgentGoalFixAndDeploy, approvalTimeoutMs: approvalWaitMs,
		tools: []registry.ToolRegistration{riskToolRegistration(productionToolName, domain.ToolRiskProduction, executor)}})
	orphans := h.listOrphanStarted()
	if len(orphans) != 1 || orphans[0].ID != attempts[0].ID {
		t.Fatalf("orphan scan = %v, want the approved attempt", orphans)
	}
	if err := h.svc.HandleOrphanToolAttempt(h.ctx, attempts[0].ID); err != nil {
		t.Fatalf("handle orphan: %v", err)
	}
	after := h.toolAttempts(action.ID)
	if len(after) != 1 || after[0].Status != domain.ToolAttemptFailed {
		t.Errorf("attempt = %+v, want FAILED by orphan handling", after)
	}
	if got := h.getAction(action.ID); got.Status != domain.AgentActionFailed {
		t.Errorf("action = %s, want FAILED and returned to the Agent", got.Status)
	}
	if len(executor.received()) != 0 {
		t.Errorf("the tool ran in the restarted process without a prepared call")
	}
	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalApproved {
		t.Errorf("approval = %s, want still APPROVED", got.Status)
	}
}

func TestDecideApproval_ReconcilableTool_ResumesClockThenPausesForRelease(t *testing.T) {
	executor := &reconcilableToolExecutor{}
	h := newReturnHarness(t, returnHarnessOptions{
		goal: domain.AgentGoalFixAndDeploy, approvalTimeoutMs: approvalWaitMs,
		tools: []registry.ToolRegistration{reconcilableToolRegistration(executor)},
	})
	// Hold the prepared call instead of making it, so its deadline can be inspected.
	h.queue.mu.Lock()
	h.queue.svc = nil
	h.queue.mu.Unlock()
	action := h.seedDecision(reconcilableToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	originalDeadline := h.getAgentRun().Deadline
	const paused = 11 * time.Minute
	h.clock.Advance(paused)
	decidedAt := h.clock.Now()

	if _, err := h.decide(approval.ID, domain.ApprovalApproved, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	movedDeadline := originalDeadline.Add(paused)
	call, ok := h.svc.approved.take(action.ID)
	if !ok {
		t.Fatalf("no prepared call for %s", action.ID)
	}
	if !call.deadline.Equal(movedDeadline) {
		t.Errorf("call deadline = %v, want the moved deadline %v", call.deadline, movedDeadline)
	}
	if call.action.Callback == nil || call.action.Callback.Token == "" {
		t.Fatalf("an asynchronous call was prepared without its callback credential")
	}
	// The credential was issued from the moved deadline: a claim of it at the old expiry
	// plus the pending TTL is already past it, the moved one is not.
	parsed, err := verifyCallbackToken([]byte("return-test-signing-secret"), call.action.Callback.Token, decidedAt)
	if err != nil || parsed.AttemptID != call.attemptID {
		t.Fatalf("prepared credential = %+v, %v, want one for attempt %s", parsed, err, call.attemptID)
	}
	if want := movedDeadline.Add(time.Hour); !parsed.ExpiresAt.Equal(want) {
		t.Errorf("credential expires %v, want the moved deadline plus the pending TTL (%v), not %v", parsed.ExpiresAt, want, originalDeadline.Add(time.Hour))
	}
	agentRun := h.getAgentRun()
	if !agentRun.Deadline.Equal(movedDeadline) {
		t.Errorf("deadline = %v, want %v", agentRun.Deadline, movedDeadline)
	}
	if agentRun.PausedAt == nil || agentRun.PauseReason == nil || *agentRun.PauseReason != domain.PauseRelease || !agentRun.PausedAt.Equal(decidedAt) {
		t.Errorf("agent run = paused %v for %v, want paused for RELEASE since %v", agentRun.PausedAt, agentRun.PauseReason, decidedAt)
	}
	resumed := h.payloadsOf(domain.EventAgentResumed)
	pausedEvents := h.payloadsOf(domain.EventAgentPaused)
	if len(resumed) != 1 || resumed[0]["reason"] != string(domain.PauseApproval) || resumed[0]["pausedMs"] != float64(paused.Milliseconds()) {
		t.Errorf("AGENT_RESUMED = %v, want the approval pause credited back", resumed)
	}
	if len(pausedEvents) != 2 || pausedEvents[1]["reason"] != string(domain.PauseRelease) {
		t.Errorf("AGENT_PAUSED = %v, want APPROVAL then RELEASE", pausedEvents)
	}
	if h.attemptCount(action.ID) != 1 {
		t.Errorf("attempts = %d, want 1", h.attemptCount(action.ID))
	}
}

func TestDecideApproval_Reject400ByteReason_CarriedWholeToAgent(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	// 200 two-byte characters: exactly the 400-byte bound, and not ASCII.
	reason := strings.Repeat("é", 200)
	if len(reason) != maxApprovalReasonBytes {
		t.Fatalf("reason is %d bytes, want %d", len(reason), maxApprovalReasonBytes)
	}

	if _, err := h.decide(approval.ID, domain.ApprovalRejected, reason+"x"); err == nil {
		t.Fatalf("a 401-byte reason was accepted")
	}
	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalPending {
		t.Fatalf("an oversized reason changed the approval to %s", got.Status)
	}
	outcome, err := h.decide(approval.ID, domain.ApprovalRejected, reason)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if outcome.Approval.Reason == nil || *outcome.Approval.Reason != reason {
		t.Errorf("recorded reason = %v, want the whole reason", outcome.Approval.Reason)
	}
	assertReturnedToAgent(t, h, action, domain.ReturnApprovalRejected)
	message := h.toolMessage(action.ID)
	text, _ := message["message"].(string)
	if !strings.Contains(text, reason) {
		t.Errorf("tool message = %q, want the whole %d-byte reason in it", text, len(reason))
	}
}

func TestDecideApproval_ToolUnregisteredAfterRestart_FailsActionKeepsApproved(t *testing.T) {
	executor := &recordingToolExecutor{}
	h := approvalHarness(t, domain.AgentGoalFixAndDeploy, executor)
	action := h.seedDecision(productionToolName, productionArguments(), domain.AgentActionReady)
	approval := h.suspendForApproval(action)
	h.clock.Advance(time.Minute)
	// The process restarts without the production Tool registered.
	h.restart(returnHarnessOptions{goal: domain.AgentGoalFixAndDeploy, approvalTimeoutMs: approvalWaitMs})

	outcome, err := h.decide(approval.ID, domain.ApprovalApproved, "")
	if err != nil || outcome.Approval.Status != domain.ApprovalApproved {
		t.Fatalf("approve = %+v, %v, want the APPROVED decision recorded", outcome, err)
	}
	if got := h.getApprovalOf(action.ID); got.Status != domain.ApprovalApproved {
		t.Errorf("approval = %s, want APPROVED kept", got.Status)
	}
	got := h.getAction(action.ID)
	if got.Status != domain.AgentActionFailed || got.Error == nil {
		t.Fatalf("action = %s %+v, want FAILED with the registration error", got.Status, got.Error)
	}
	if h.attemptCount(action.ID) != 0 || len(executor.received()) != 0 || len(h.queue.offeredActions()) != 0 {
		t.Errorf("an unregistered tool got an Attempt, a call or a queue offer")
	}
	agentRun := h.getAgentRun()
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationToolError {
		t.Errorf("agent run termination = %v, want TOOL_ERROR", agentRun.Termination)
	}
	assertNotPaused(t, agentRun)
	if run := h.getRun(); run.Status != domain.RunFailed {
		t.Errorf("run = %s, want FAILED", run.Status)
	}
}

func (h *returnHarness) toolAttempts(actionID string) []domain.ToolAttempt {
	h.t.Helper()
	var out []domain.ToolAttempt
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.ToolAttempts().ListByActionID(ctx, actionID)
		return err
	}); err != nil {
		h.t.Fatalf("list attempts: %v", err)
	}
	return out
}

// listOrphanStarted is the scan the Reconciler of the harness's current process runs.
func (h *returnHarness) listOrphanStarted() []domain.ToolAttempt {
	h.t.Helper()
	var out []domain.ToolAttempt
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.ToolAttempts().ListOrphanStarted(ctx, h.svc.deps.RunnerID, h.clock.Now(), 10)
		return err
	}); err != nil {
		h.t.Fatalf("list orphan attempts: %v", err)
	}
	return out
}
