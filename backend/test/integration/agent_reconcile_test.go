//go:build integration

// Agent reconciliation tests: the three Agent rows of the Reconciler scan table of
// docs/06-execution-model.md §2.1, proved against real committed PostgreSQL facts rather
// than a mock repository (CLAUDE.md testing standard).
//
// Every test here stops the in-process Agent Loop at one committed boundary, restarts the
// Backend over the same Pool -- a second ExecutionService image, the only recovery source
// invariant #6 allows -- and lets one Reconciler pass carry the work on. What they cover
// is the acceptance list of docs/09-testing-and-acceptance.md §1.4/§1.6 and §3.3: a READY
// Turn, a READY TOOL_CALL Action and a READY FINAL Action are all rediscovered and
// advanced with claimSource = RECONCILER; a RUNNING Turn without a Decision is never
// re-called; an expired Agent deadline terminates through the timeout use case; and a
// RUNNING Agent NodeRun never becomes the source of a fresh Turn.
//
// They share the agentHarness and the fixture Definition of agent_loop_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// agentReconciler builds the Reconciler one restarted Backend runs: the same store, the
// same injected clock and the same ExecutionService the live process would use. It holds
// no Agent state of its own, which is the point -- everything it does must go through a
// service use case.
func agentReconciler(h *agentHarness) *reconciler.Reconciler {
	return reconciler.New(reconciler.Config{
		UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100,
	})
}

// agentRunOnce performs one reconciliation pass and fails the test on a scan error or on
// any per-item error collected into the Report: a lost conditional claim is reported as a
// success, so anything in Errors is a real defect.
func agentRunOnce(h *agentHarness, rec *reconciler.Reconciler) reconciler.Report {
	h.t.Helper()
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		h.t.Fatalf("reconciler run once: %v", err)
	}
	if len(report.Errors) != 0 {
		h.t.Fatalf("reconciler report errors = %v, want none", report.Errors)
	}
	return report
}

// agentPayloadsFor returns the payloads of every Event of one type whose payload names the
// given Turn or Action id, which is how a test asserts about the one work item it drove
// rather than about every round of the loop.
func agentPayloadsFor(t *testing.T, events []domain.Event, typ domain.EventType, key, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range events {
		if ev.Type != typ {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", typ, err)
		}
		if payload[key] == id {
			out = append(out, payload)
		}
	}
	return out
}

// agentOnlyPayloadFor is agentPayloadsFor for the case in which exactly one Event must
// exist.
func agentOnlyPayloadFor(t *testing.T, events []domain.Event, typ domain.EventType, key, id string) map[string]any {
	t.Helper()
	got := agentPayloadsFor(t, events, typ, key, id)
	if len(got) != 1 {
		t.Fatalf("%s events for %s %s = %d, want exactly 1", typ, key, id, len(got))
	}
	return got[0]
}

// agentTermination renders an Agent Run's optional termination for a failure message,
// because the raw pointer prints as an address rather than the reason the Run stopped.
func agentTermination(run domain.AgentRun) string {
	if run.Termination == nil {
		return "<none>"
	}
	return string(*run.Termination)
}

// agentTurnNoExists reports whether one Agent Run has a Turn with the given number. It is
// the store-level way to prove that no extra round was ever created, because a Turn number
// is unique per Agent Run.
func agentTurnNoExists(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, turnNo int) bool {
	t.Helper()
	found := true
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.AgentTurns().GetByRunAndTurnNo(ctx, agentRunID, turnNo)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("get turn %d of agent run %s: %v", turnNo, agentRunID, err)
	}
	return found
}

// agentStopAfterToolRound drives the fixture Run through exactly one successful Tool round
// and leaves the next Turn where the result transaction committed it. The service hands
// that Turn to the work enqueuer after COMMIT rather than advancing it on this goroutine,
// and the harness enqueuer only records it, so what is left is the crash-after-COMMIT
// state of docs/09 §3.3 "Tool result 与下一条 READY Turn 提交后崩溃": Turn 1 COMPLETED, its
// Action SUCCEEDED, Turn 2 committed READY and nobody advancing it.
func agentStopAfterToolRound(h *agentHarness, runID string) service.AdvanceOutcome {
	h.t.Helper()
	outcome := h.claimAgentNode(runID)
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		h.t.Fatalf("execute agent node run: %v", err)
	}
	// The recorded enqueue is the work item a dead process would have lost.
	h.dropPendingTurns()
	return outcome
}

// agentStopAfterDecision drives the fixture Run to the persistence boundary of one
// committed Decision: the Turn is COMPLETED, its single Action is READY and nothing has
// executed it. It is agentReadyFinalAction generalised over the Decision kind the script
// produces.
func agentStopAfterDecision(h *agentHarness, stop *agentStopNotifier, runID string) (service.AdvanceOutcome, domain.AgentAction) {
	h.t.Helper()
	outcome := h.claimAgentNode(runID)

	stopCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop.cancel = cancel
	// Armed during the model call, so the notification that stops the chain is the
	// Decision commit's own.
	h.provider.BeforeReturn = func(context.Context) { stop.arm() }
	if err := h.svc.Execute(stopCtx, outcome); !errors.Is(err, context.Canceled) {
		h.t.Fatalf("execute agent node run = %v, want context.Canceled from the stopped chain", err)
	}
	h.provider.BeforeReturn = nil

	action, found := agentActionOfTurn(h.ctx, h.t, h.uow, outcome.AgentTurnID)
	if !found {
		h.t.Fatalf("no action was committed for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionReady {
		h.t.Fatalf("action status = %s, want READY before recovery", action.Status)
	}
	return outcome, action
}

// agentCrashDuringModelCall claims the Agent NodeRun and then dies inside the model call,
// leaving the Turn RUNNING with no Decision -- the one state docs/06 §2.1 forbids the
// Reconciler from re-issuing a model request for.
func agentCrashDuringModelCall(h *agentHarness, runID string) service.AdvanceOutcome {
	h.t.Helper()
	outcome := h.claimAgentNode(runID)
	h.provider.BeforeReturn = func(context.Context) {
		panic("simulated crash after the turn claim committed, before the model answered")
	}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				h.t.Fatalf("the simulated crash did not happen")
			}
		}()
		_ = h.svc.AdvanceAgentTurn(h.ctx, outcome.AgentTurnID, domain.ClaimImmediate)
	}()
	h.provider.BeforeReturn = nil
	return outcome
}

// agentToolCallScenario is the fixture TOOL_CALL the scripted model decides.
func agentToolCallScenario() mockmodel.Scenario {
	return mockmodel.Scenario{ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments)}
}

// ---------------------------------------------------------------------------
// READY Agent Turn
// ---------------------------------------------------------------------------

// TestReconciler_ReadyTurnAfterRestart_ClaimsWithReconcilerSourceAndContinues is
// acceptance item 6 of docs/09-testing-and-acceptance.md §1: the Tool result and the next
// READY Turn are committed, the process dies before immediate advancement, and the
// Reconciler of a restarted Backend must carry the second round through to the Run's
// terminal status -- claiming the existing Turn rather than creating another one.
func TestReconciler_ReadyTurnAfterRestart_ClaimsWithReconcilerSourceAndContinues(t *testing.T) {
	crashed := newAgentHarness(t, agentHarnessOptions{})
	agentScriptToolCallThenFinal(crashed, agentToolCallScenario())
	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-reconcile-turn"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	outcome := agentStopAfterToolRound(crashed, run.ID)

	ready := listReadyTurns(crashed.ctx, t, crashed.uow)
	if len(ready) != 1 || ready[0].TurnNo != 2 {
		t.Fatalf("ready turns after the stopped round = %+v, want exactly Turn 2", ready)
	}
	turn2 := ready[0]

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "recovered answer"})

	rec := agentReconciler(restarted)
	report := agentRunOnce(restarted, rec)
	if report.ReadyAgentTurnsFound != 1 || report.AgentTurnsAdvanced != 1 {
		t.Fatalf("report: want 1 ready Agent Turn advanced, got %+v", report)
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	started := agentOnlyPayloadFor(t, events, domain.EventAgentTurnStarted, "turnId", turn2.ID)
	if started["claimSource"] != string(domain.ClaimReconciler) {
		t.Errorf("AGENT_TURN_STARTED claimSource for the rediscovered turn = %v, want RECONCILER", started["claimSource"])
	}
	if got := generated.Load(); got != 1 {
		t.Errorf("model calls in the restarted process = %d, want exactly 1: the rediscovered Turn is called once", got)
	}

	agentRun, ok := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE", agentTermination(agentRun))
	}
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, agentRun.ID, 3) {
		t.Errorf("a third Turn was created: recovery must advance the existing READY Turn, not add a round")
	}
	if turn := getAgentTurn(restarted.ctx, t, restarted.uow, turn2.ID); turn.Status != domain.AgentTurnCompleted {
		t.Errorf("rediscovered turn status = %s, want COMPLETED", turn.Status)
	}

	// The Final completion transaction made the downstream node READY; the next pass is
	// what carries the Run itself to COMPLETED, still without any in-process continuation.
	agentRunOnce(restarted, rec)
	if got := agentRunRow(restarted.ctx, t, restarted.uow, run.ID).Status; got != domain.RunCompleted {
		t.Errorf("run status after recovery = %s, want COMPLETED", got)
	}
}

// ---------------------------------------------------------------------------
// READY Agent Action
// ---------------------------------------------------------------------------

// TestReconciler_ReadyToolCallActionAfterRestart_ExecutesWithoutModelCall covers the
// READY Agent Action row of the scan table and its "不能重新请求模型或创建新 Decision"
// column: the Decision is committed, the process died before its Action ran, and recovery
// must execute that exact Action. The Tool call itself observes how many model calls the
// restarted process had made by then -- none.
func TestReconciler_ReadyToolCallActionAfterRestart_ExecutesWithoutModelCall(t *testing.T) {
	stop := &agentStopNotifier{}
	crashed := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptToolCallThenFinal(crashed, agentToolCallScenario())
	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-reconcile-action"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	outcome, action := agentStopAfterDecision(crashed, stop, run.ID)
	if action.Type != domain.AgentActionToolCall {
		t.Fatalf("action type = %s, want TOOL_CALL", action.Type)
	}
	decision, _ := agentDecisionOfTurn(crashed.ctx, t, crashed.uow, outcome.AgentTurnID)

	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool: crashed.pool, Clock: crashed.clock, LookupExecutor: tool,
	})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "recovered answer"})

	// Sampled inside the Tool call: at that moment the committed Action is being executed,
	// and nothing may have asked the model in this process yet.
	var modelCallsWhenToolRan int32 = -1
	tool.during = func(context.Context, registry.ToolAction) {
		modelCallsWhenToolRan = generated.Load()
	}

	rec := agentReconciler(restarted)
	report := agentRunOnce(restarted, rec)
	if report.ReadyAgentActionsFound != 1 || report.AgentActionsAdvanced != 1 {
		t.Fatalf("report: want 1 ready Agent Action advanced, got %+v", report)
	}
	if tool.count() != 1 {
		t.Fatalf("tool calls = %d, want exactly 1", tool.count())
	}
	if modelCallsWhenToolRan != 0 {
		t.Errorf("model calls made before the rediscovered Action ran = %d, want 0: executing a committed Decision asks no model",
			modelCallsWhenToolRan)
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	started := agentOnlyPayloadFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID)
	if started["claimSource"] != string(domain.ClaimReconciler) {
		t.Errorf("AGENT_ACTION_STARTED claimSource = %v, want RECONCILER", started["claimSource"])
	}

	if executed := getAgentAction(restarted.ctx, t, restarted.uow, action.ID); executed.Status != domain.AgentActionSucceeded {
		t.Errorf("rediscovered action status = %s, want SUCCEEDED", executed.Status)
	}
	if again, _ := agentDecisionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID); again.ID != decision.ID {
		t.Errorf("decision of the recovered turn = %s, want the committed %s: a Decision is never regenerated", again.ID, decision.ID)
	}

	// The rediscovered Action's result transaction committed Turn 2 READY and handed it
	// to the enqueuer, which this harness only records; further Reconciler passes are what
	// carry the loop and then the Run to completion.
	agentRunOnceUntil(t, restarted, run.ID, domain.RunCompleted)
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE: the loop must run to completion", agentTermination(agentRun))
	}
}

// TestReconciler_ReadyFinalActionAfterRestart_CompletesWithoutModelCallOrToolAttempt is
// the FINAL half of the same row (docs/09 §3.3 "Final Decision 与唯一 READY Final Action
// 提交后、完成事务前崩溃"): the Reconciler advances that Action, never re-asks the model
// and never creates a Tool Attempt for it.
func TestReconciler_ReadyFinalActionAfterRestart_CompletesWithoutModelCallOrToolAttempt(t *testing.T) {
	stop := &agentStopNotifier{}
	crashed := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptFinal(crashed, mockmodel.Scenario{Output: "the committed answer"})
	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-reconcile-final"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	outcome, action := agentReadyFinalAction(crashed, stop, run.ID)
	if action.Type != domain.AgentActionFinal {
		t.Fatalf("action type = %s, want FINAL", action.Type)
	}

	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool: crashed.pool, Clock: crashed.clock, LookupExecutor: tool,
	})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "must never be produced"})

	rec := agentReconciler(restarted)
	report := agentRunOnce(restarted, rec)
	if report.ReadyAgentActionsFound != 1 || report.AgentActionsAdvanced != 1 {
		t.Fatalf("report: want 1 ready Agent Action advanced, got %+v", report)
	}
	if got := generated.Load(); got != 0 {
		t.Errorf("model calls in the restarted process = %d, want 0: a committed Final Decision is completed, not regenerated", got)
	}
	if got := tool.count(); got != 0 {
		t.Errorf("tool calls = %d, want 0 for a FINAL action", got)
	}
	if attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the FINAL action = %d, want 0", len(attempts))
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	started := agentOnlyPayloadFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID)
	if started["claimSource"] != string(domain.ClaimReconciler) {
		t.Errorf("AGENT_ACTION_STARTED claimSource = %v, want RECONCILER", started["claimSource"])
	}

	if completed := getAgentAction(restarted.ctx, t, restarted.uow, action.ID); completed.Status != domain.AgentActionSucceeded {
		t.Errorf("rediscovered final action status = %s, want SUCCEEDED", completed.Status)
	}
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE", agentTermination(agentRun))
	}
	agentRunOnce(restarted, rec)
	if got := agentRunRow(restarted.ctx, t, restarted.uow, run.ID).Status; got != domain.RunCompleted {
		t.Errorf("run status after recovery = %s, want COMPLETED", got)
	}
}

// ---------------------------------------------------------------------------
// RUNNING Turn: not recoverable work
// ---------------------------------------------------------------------------

// TestReconciler_RunningTurnWithoutDecision_NotRecalledBeforeDeadline is the "不能重调
// RUNNING Turn 的模型请求" column of the scan table. The model request may already be in
// flight, so until the Agent deadline expires the only correct action is none at all.
func TestReconciler_RunningTurnWithoutDecision_NotRecalledBeforeDeadline(t *testing.T) {
	crashed := newAgentHarness(t, agentHarnessOptions{})
	agentScriptFinal(crashed, mockmodel.Scenario{Output: "never returned"})
	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-reconcile-running"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	outcome := agentCrashDuringModelCall(crashed, run.ID)

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "must never be produced"})

	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ReadyAgentTurnsFound != 0 || report.AgentTurnsAdvanced != 0 {
		t.Errorf("report: want no Agent Turn rediscovered for a RUNNING turn, got %+v", report)
	}
	if report.ExpiredAgentRunsFound != 0 {
		t.Errorf("report: want no expired Agent Run before the deadline, got %+v", report)
	}
	if got := generated.Load(); got != 0 {
		t.Errorf("model calls in the restarted process = %d, want 0", got)
	}

	turn := getAgentTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if turn.Status != domain.AgentTurnRunning {
		t.Errorf("turn status after reconciliation = %s, want RUNNING: nothing may change it before the deadline", turn.Status)
	}
	if _, found := agentDecisionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID); found {
		t.Errorf("a decision was committed for a turn whose model call never returned")
	}
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s before its deadline", *agentRun.Termination)
	}
}

// ---------------------------------------------------------------------------
// Expired Agent deadline
// ---------------------------------------------------------------------------

// TestReconciler_ExpiredAgentDeadline_TerminatesTimeoutAtomically is the Agent deadline
// row of the scan table and docs/06-execution-model.md §1.7's timeout transaction: the
// RUNNING Turn that never produced a Decision, the Agent Run, the Agent NodeRun and the
// Run all end together, and nothing invents an Action failure or a next round to get
// there.
func TestReconciler_ExpiredAgentDeadline_TerminatesTimeoutAtomically(t *testing.T) {
	crashed := newAgentHarness(t, agentHarnessOptions{})
	agentScriptFinal(crashed, mockmodel.Scenario{Output: "never returned"})
	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-reconcile-deadline"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	outcome := agentCrashDuringModelCall(crashed, run.ID)

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "must never be produced"})
	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)

	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ExpiredAgentRunsFound != 1 || report.AgentRunsTimedOut != 1 {
		t.Fatalf("report: want 1 expired Agent Run timed out, got %+v", report)
	}
	if got := generated.Load(); got != 0 {
		t.Errorf("model calls while terminating an expired Agent Run = %d, want 0", got)
	}

	turn := getAgentTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if turn.Status != domain.AgentTurnFailed {
		t.Errorf("turn status = %s, want FAILED", turn.Status)
	}
	if turn.Error == nil || turn.Error.Code != "TIMEOUT" {
		t.Errorf("turn error = %+v, want code TIMEOUT", turn.Error)
	}
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("agent run termination = %v, want TIMEOUT", agentTermination(agentRun))
	}
	if agentRun.TerminatedAt == nil {
		t.Errorf("a terminated agent run recorded no terminated_at")
	}
	if nodeRun := agentNodeRun(restarted.ctx, t, restarted.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if got := agentRunRow(restarted.ctx, t, restarted.uow, run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentFailed) != 1 {
		t.Errorf("AGENT_FAILED events = %v, want exactly 1", types)
	}
	if agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("NODE_FAILED events = %v, want exactly 1", types)
	}
	if agentCountEventType(types, domain.EventAgentActionFailed) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_FAILED: this Turn never committed an Action", types)
	}
	if payload := agentEventPayload(t, events, domain.EventAgentFailed); payload["termination"] != string(domain.TerminationTimeout) {
		t.Errorf("AGENT_FAILED termination = %v, want TIMEOUT", payload["termination"])
	}

	if _, found := agentDecisionOfTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID); found {
		t.Errorf("the timeout transaction committed a Decision")
	}
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, agentRun.ID, 2) {
		t.Errorf("the timeout transaction created a next Turn, which 06 §2.1 forbids")
	}
}

// TestReconciler_ExpiredDeadlineWithReadyTurn_TimesOutWithoutClaimingTheTurn is why the
// deadline row of the scan table runs before the READY Turn row. An expired Agent Run may
// still hold a READY Turn; claiming it would write AGENT_TURN_STARTED and pay for a model
// call on behalf of a round that can never finish.
func TestReconciler_ExpiredDeadlineWithReadyTurn_TimesOutWithoutClaimingTheTurn(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	generated := agentScriptFinal(h, mockmodel.Scenario{Output: "must never be produced"})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-reconcile-deadline-ready"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	// The claim transaction committed Turn 1 READY and stopped there: the process never
	// advanced it, and by the time reconciliation runs the deadline has passed.
	outcome := h.claimAgentNode(run.ID)
	if ready := listReadyTurns(h.ctx, t, h.uow); len(ready) != 1 {
		t.Fatalf("ready turns = %d, want exactly 1 before the deadline passes", len(ready))
	}
	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)

	report := agentRunOnce(h, agentReconciler(h))
	if report.ExpiredAgentRunsFound != 1 || report.AgentRunsTimedOut != 1 {
		t.Fatalf("report: want 1 expired Agent Run timed out, got %+v", report)
	}
	if report.AgentTurnsAdvanced != 0 {
		t.Errorf("report: the READY turn of an expired Agent Run was advanced: %+v", report)
	}
	if got := generated.Load(); got != 0 {
		t.Errorf("model calls = %d, want 0: an expired Agent Run must not buy another model call", got)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentTurnStarted) != 0 {
		t.Errorf("events = %v, want no AGENT_TURN_STARTED for a Turn of an already-expired Agent Run", types)
	}
	if turn := getAgentTurn(h.ctx, t, h.uow, outcome.AgentTurnID); turn.Status != domain.AgentTurnFailed {
		t.Errorf("turn status = %s, want FAILED: the deadline ends a READY Turn too", turn.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %v, want TIMEOUT", agentTermination(agentRun))
	}
}

// TestReconciler_ExpiredDeadlineWithRunningToolAction_FailsActionWithTimeoutSource is
// docs/06-execution-model.md:453 in full: when a current Action exists, the timeout
// transaction writes AGENT_ACTION_FAILED with failureSource = TIMEOUT and names the Tool
// Attempt it closed, and the Agent Run's termination stays TIMEOUT rather than TOOL_ERROR
// (docs/09 §3.3 "不得误记为 TOOL_ERROR").
func TestReconciler_ExpiredDeadlineWithRunningToolAction_FailsActionWithTimeoutSource(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	crashed := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	agentScriptToolCallThenFinal(crashed, agentToolCallScenario())
	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-reconcile-deadline-action"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := crashed.claimAgentNode(run.ID)

	// The claim transaction committed the RUNNING Action and its STARTED Tool Attempt;
	// the process then died before the Tool answered.
	tool.during = func(context.Context, registry.ToolAction) {
		panic("simulated crash after the action claim committed, before the tool answered")
	}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		_ = crashed.svc.Execute(crashed.ctx, outcome)
	}()

	action, found := agentActionOfTurn(crashed.ctx, t, crashed.uow, outcome.AgentTurnID)
	if !found || action.Status != domain.AgentActionRunning {
		t.Fatalf("action after the crash = %+v, want a RUNNING one", action)
	}
	attempts := agentToolAttempts(crashed.ctx, t, crashed.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Fatalf("tool attempts after the crash = %+v, want exactly one STARTED", attempts)
	}
	attempt := attempts[0]

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)

	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ExpiredAgentRunsFound != 1 || report.AgentRunsTimedOut != 1 {
		t.Fatalf("report: want 1 expired Agent Run timed out, got %+v", report)
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureTimeout) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want TIMEOUT", failed["failureSource"])
	}
	if failed["toolAttemptId"] != attempt.ID {
		t.Errorf("AGENT_ACTION_FAILED toolAttemptId = %v, want the STARTED attempt %s", failed["toolAttemptId"], attempt.ID)
	}

	if got := getAgentAction(restarted.ctx, t, restarted.uow, action.ID); got.Status != domain.AgentActionFailed {
		t.Errorf("action status = %s, want FAILED", got.Status)
	}
	closed := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID)
	if len(closed) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1: the MVP never retries an Agent Tool", len(closed))
	}
	if closed[0].Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempt status = %s, want FAILED", closed[0].Status)
	}
	if closed[0].Error == nil || closed[0].Error.Code != "TIMEOUT" {
		t.Errorf("tool attempt error = %+v, want code TIMEOUT", closed[0].Error)
	}

	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("agent run termination = %v, want TIMEOUT, never TOOL_ERROR", agentTermination(agentRun))
	}
	if turn := getAgentTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID); turn.Status != domain.AgentTurnCompleted {
		t.Errorf("turn status = %s, want COMPLETED: the model answered, so the deadline does not rewrite that fact", turn.Status)
	}
	if nodeRun := agentNodeRun(restarted.ctx, t, restarted.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if got := agentRunRow(restarted.ctx, t, restarted.uow, run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, agentRun.ID, 2) {
		t.Errorf("the timeout transaction created a next Turn")
	}
}

// ---------------------------------------------------------------------------
// RUNNING Agent NodeRun is never a source of work
// ---------------------------------------------------------------------------

// TestReconciler_AgentScans_NeverCreateTurnFromRunningNodeRun covers the last paragraph of
// docs/06-execution-model.md §2.1: "Reconciler 不扫描 RUNNING Agent NodeRun 来创建 Turn."
//
// The state it needs -- a RUNNING Agent NodeRun whose only Turn is COMPLETED, whose only
// Action SUCCEEDED, and which has no next Turn -- is one the Runtime never commits, because
// the round's result transaction creates the next Turn atomically. It is manufactured here
// by deleting that committed next Turn directly, which is exactly the gap a Reconciler
// that derived rounds from the NodeRun would try to fill. Nothing may fill it: the correct
// behaviour is to leave the Agent Run alone until its deadline.
func TestReconciler_AgentScans_NeverCreateTurnFromRunningNodeRun(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	agentScriptToolCallThenFinal(h, agentToolCallScenario())
	def := h.saveDefinition(agentLoopDefinition("wf-agent-reconcile-noderun"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	outcome := agentStopAfterToolRound(h, run.ID)
	ready := listReadyTurns(h.ctx, t, h.uow)
	if len(ready) != 1 || ready[0].TurnNo != 2 {
		t.Fatalf("ready turns after the stopped round = %+v, want exactly Turn 2", ready)
	}
	if _, err := h.pool.Exec(h.ctx, `DELETE FROM agent_turns WHERE id = $1`, ready[0].ID); err != nil {
		t.Fatalf("remove the committed next turn: %v", err)
	}

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: h.pool, Clock: h.clock})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "must never be produced"})

	nodeRun := agentNodeRun(restarted.ctx, t, restarted.uow, run.ID)
	if nodeRun.Status != domain.NodeRunRunning {
		t.Fatalf("agent node run status = %s, want RUNNING for this case", nodeRun.Status)
	}
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, nodeRun.ID)
	completed := getAgentTurn(restarted.ctx, t, restarted.uow, outcome.AgentTurnID)
	if completed.Status != domain.AgentTurnCompleted {
		t.Fatalf("turn 1 status = %s, want COMPLETED", completed.Status)
	}

	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ReadyAgentTurnsFound != 0 || report.AgentTurnsAdvanced != 0 {
		t.Errorf("report: want no Agent Turn work from a RUNNING Agent NodeRun, got %+v", report)
	}
	if report.ReadyAgentActionsFound != 0 {
		t.Errorf("report: want no Agent Action work, got %+v", report)
	}
	if got := generated.Load(); got != 0 {
		t.Errorf("model calls = %d, want 0: a RUNNING Agent NodeRun is not a source of rounds", got)
	}
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, agentRun.ID, 2) {
		t.Errorf("the Reconciler derived a Turn from the RUNNING Agent NodeRun")
	}
	if got := agentNodeRun(restarted.ctx, t, restarted.uow, run.ID); got.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING: reconciliation changed business state directly", got.Status)
	}
	if again, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, nodeRun.ID); again.Termination != nil {
		t.Errorf("agent run terminated as %s before its deadline", *again.Termination)
	}
}
