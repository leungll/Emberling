//go:build integration

// Final completion tests: the single transaction that closes one committed FINAL Action
// out (docs/06-execution-model.md §1.7, docs/05-data-model.md §1.4). They cover what a
// mock repository cannot prove (CLAUDE.md testing standard): the conditional READY ->
// RUNNING claim that elects one completer, the atomic commit of Action result, Final
// Context Version, optional State Version, FINAL_RESPONSE termination, Agent NodeRun
// output, Run aggregation and downstream READY NodeRuns -- and the invariant that no
// RUNNING Final Action is ever visible outside the transaction.
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

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// agentFinalDefinition is agentLoopDefinition with the two frozen Schemas the Final
// completion use case validates against: the Agent's Output Schema and its State Schema.
// An empty string leaves the corresponding field out of the config, which is what freezes
// "no Schema".
func agentFinalDefinition(workflowID, outputSchema, stateSchema string) domain.Definition {
	def := agentLoopDefinition(workflowID)
	config := map[string]any{
		"instructions": agentInstructions,
		"modelId":      mockmodel.ModelID,
		"allowedTools": []string{lookup.ToolName},
		"maxTurns":     agentMaxTurns,
		"timeoutMs":    agentTimeoutMs,
	}
	if outputSchema != "" {
		config["outputSchema"] = json.RawMessage(outputSchema)
	}
	if stateSchema != "" {
		config["stateSchema"] = json.RawMessage(stateSchema)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		panic("encode agent config: " + err.Error())
	}
	for i := range def.Nodes {
		if def.Nodes[i].ID == "node_agent" {
			def.Nodes[i].Config = encoded
		}
	}
	return def
}

// agentScriptFinal makes the fixture model answer FINAL on every Turn with one scripted
// scenario, and returns the counter that records how often the model was called. A second
// call would mean a committed Decision was regenerated, which docs/09 §3.3 forbids.
func agentScriptFinal(h *agentHarness, scenario mockmodel.Scenario) *atomic.Int32 {
	var generated atomic.Int32
	scenario.Kind = mockmodel.ScenarioFinal
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		generated.Add(1)
		final := scenario
		return &final
	}
	return &generated
}

// agentDrive runs the Advance/Execute loop until no NodeRun is claimable any more, which
// is how a test carries a Run past the Agent node to its own terminal status without a
// background worker.
func agentDrive(h *agentHarness, runID string) {
	h.t.Helper()
	for i := 0; i < 8; i++ {
		outcome := h.advance(runID)
		if !outcome.Claimed {
			return
		}
		h.execute(outcome)
	}
	h.t.Fatalf("run %s did not settle within the bounded drive loop", runID)
}

// agentEventIndex reports where one Event type first occurs, or -1.
func agentEventIndex(types []domain.EventType, want domain.EventType) int {
	for i, got := range types {
		if got == want {
			return i
		}
	}
	return -1
}

// agentReadyFinalAction drives the fixture Run to the point docs/05 §1.4 calls the FINAL
// Action's persistence boundary: the Decision and its single READY Action are committed
// and nothing has executed them, exactly what a crashed process leaves behind. It returns
// the Agent claim outcome and the READY Action.
func agentReadyFinalAction(h *agentHarness, stop *agentStopNotifier, runID string) (service.AdvanceOutcome, domain.AgentAction) {
	h.t.Helper()
	outcome := h.claimAgentNode(runID)

	stopCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop.cancel = cancel
	// Armed during the model call, so the notification that stops the chain is the
	// Decision commit's own: the Action is committed READY and nothing completes it.
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
		h.t.Fatalf("action status = %s, want READY before the completion transaction", action.Status)
	}
	return outcome, action
}

// ---------------------------------------------------------------------------
// Final completion success
// ---------------------------------------------------------------------------

// TestAgentFinal_NoSchemaStringOutput_CompletesNodeRunAndRun covers the whole Final
// completion transaction of docs/06-execution-model.md §1.7 for the plain case: no Output
// Schema, no state patch. One transaction closes the Action out through RUNNING to
// SUCCEEDED, appends the Final Context Version, records FINAL_RESPONSE and succeeds the
// Agent NodeRun with its output on the `text` port, which makes the downstream node READY
// and eventually completes the Run.
func TestAgentFinal_NoSchemaStringOutput_CompletesNodeRunAndRun(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	generated := agentScriptFinal(h, mockmodel.Scenario{Output: "done"})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-final-string"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: a committed Final Decision is never regenerated", got)
	}

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("turn %s committed no action", outcome.AgentTurnID)
	}
	if action.Type != domain.AgentActionFinal || action.Status != domain.AgentActionSucceeded {
		t.Fatalf("action = (%s, %s), want (FINAL, SUCCEEDED)", action.Type, action.Status)
	}
	if action.StartedAt == nil || action.CompletedAt == nil {
		t.Errorf("action timestamps = (started %v, completed %v), want both set: the Action passed through RUNNING inside the transaction",
			action.StartedAt, action.CompletedAt)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the FINAL action = %d, want 0", len(attempts))
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE", agentRun.Termination)
	}
	if agentRun.TerminatedAt == nil {
		t.Errorf("agent run recorded no terminated_at")
	}
	if agentRun.Error != nil {
		t.Errorf("successful agent run recorded error %+v", *agentRun.Error)
	}
	if got := agentPointers(agentRun); got != [3]int{1, 1, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 1 0]", got)
	}

	contextV1 := agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 1)
	messages := agentContextMessages(t, contextV1.Messages)
	if len(messages) != 2 {
		t.Fatalf("final context version carries %d messages, want 2 (user, assistant)", len(messages))
	}
	if messages[0].Role != runtime.AgentRoleUser {
		t.Errorf("message 0 role = %q, want the preserved user message", messages[0].Role)
	}
	if messages[1].Role != runtime.AgentRoleAssistant {
		t.Errorf("message 1 role = %q, want assistant", messages[1].Role)
	}
	assertSameJSON(t, "final assistant message", json.RawMessage(`"done"`), messages[1].Content)
	if contextV1.SourceTurnID == nil || *contextV1.SourceTurnID != outcome.AgentTurnID {
		t.Errorf("final context version sourceTurnId = %v, want %s", contextV1.SourceTurnID, outcome.AgentTurnID)
	}
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a State Version was created although the Decision carried no state patch")
	}

	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run status = %s, want SUCCEEDED", nodeRun.Status)
	}
	assertSameJSON(t, "agent node run output", json.RawMessage(`{"text":"done"}`), nodeRun.Output)
	if agentAttemptCount(h.ctx, t, h.uow, nodeRun.ID) != 0 {
		t.Errorf("the agent node run created a Node Attempt")
	}

	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	if downstream := execNodeRunByNodeID(t, nodeRuns, "node_output"); downstream.Status != domain.NodeRunReady {
		t.Errorf("downstream node run status = %s, want READY", downstream.Status)
	}

	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	started := agentEventIndex(types, domain.EventAgentActionStarted)
	completed := agentEventIndex(types, domain.EventAgentActionCompleted)
	agentDone := agentEventIndex(types, domain.EventAgentCompleted)
	nodeDone := agentEventIndex(types, domain.EventNodeCompleted)
	if started < 0 || !(started < completed && completed < agentDone && agentDone < nodeDone) {
		t.Errorf("events = %v, want AGENT_ACTION_STARTED < AGENT_ACTION_COMPLETED < AGENT_COMPLETED < NODE_COMPLETED", types)
	}
	if agentCountEventType(types, domain.EventAgentStateUpdated) != 0 {
		t.Errorf("events = %v, want no AGENT_STATE_UPDATED without a state patch", types)
	}
	if agentCountEventType(types, domain.EventAgentFailed) != 0 {
		t.Errorf("events = %v, want no AGENT_FAILED", types)
	}
	final := agentEventPayload(t, listEvents(h.ctx, t, h.uow, run.ID), domain.EventAgentCompleted)
	if final["turnId"] != outcome.AgentTurnID {
		t.Errorf("AGENT_COMPLETED turnId = %v, want %s", final["turnId"], outcome.AgentTurnID)
	}
	if final["termination"] != string(domain.TerminationFinalResponse) {
		t.Errorf("AGENT_COMPLETED termination = %v, want FINAL_RESPONSE", final["termination"])
	}

	agentDrive(h, run.ID)
	persisted := agentRunRow(h.ctx, t, h.uow, run.ID)
	if persisted.Status != domain.RunCompleted {
		t.Fatalf("run status = %s, want COMPLETED", persisted.Status)
	}
	assertSameJSON(t, "run output", json.RawMessage(`{"text":"done"}`), persisted.Output)
}

// TestAgentFinal_WithStatePatch_CreatesStateVersionAndEvent covers the optional half of
// the same transaction (docs/05-data-model.md §1.4): a Final state patch that really
// changes the State creates exactly one new State Version and one AGENT_STATE_UPDATED
// naming the previous and next State Version and the Context Version committed with them.
//
// Order: AGENT_ACTION_COMPLETED, then AGENT_STATE_UPDATED, then AGENT_COMPLETED -- the
// State change is part of what the Action produced, and the Agent's terminal Event closes
// the Agent Run after every fact it terminated on is committed.
func TestAgentFinal_WithStatePatch_CreatesStateVersionAndEvent(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	agentScriptFinal(h, mockmodel.Scenario{Output: "done", StatePatch: json.RawMessage(`{"done":true}`)})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-final-state-patch"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if got := agentPointers(agentRun); got != [3]int{1, 1, 1} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 1 1]", got)
	}
	stateV1 := agentStateVersion(h.ctx, t, h.uow, agentRun.ID, 1)
	assertSameJSON(t, "state version 1", json.RawMessage(`{"done":true}`), stateV1.Value)
	if stateV1.SourceTurnID == nil || *stateV1.SourceTurnID != outcome.AgentTurnID {
		t.Errorf("state version 1 sourceTurnId = %v, want %s", stateV1.SourceTurnID, outcome.AgentTurnID)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentStateUpdated) != 1 {
		t.Fatalf("events = %v, want exactly one AGENT_STATE_UPDATED", types)
	}
	completed := agentEventIndex(types, domain.EventAgentActionCompleted)
	updated := agentEventIndex(types, domain.EventAgentStateUpdated)
	agentDone := agentEventIndex(types, domain.EventAgentCompleted)
	if !(completed < updated && updated < agentDone) {
		t.Errorf("events = %v, want AGENT_ACTION_COMPLETED < AGENT_STATE_UPDATED < AGENT_COMPLETED", types)
	}
	payload := agentEventPayload(t, events, domain.EventAgentStateUpdated)
	if payload["previousStateVersion"] != float64(0) || payload["stateVersion"] != float64(1) {
		t.Errorf("AGENT_STATE_UPDATED versions = %v, want previous 0 and next 1", payload)
	}
	if payload["contextVersion"] != float64(1) {
		t.Errorf("AGENT_STATE_UPDATED contextVersion = %v, want 1", payload["contextVersion"])
	}
	if payload["turnId"] != outcome.AgentTurnID {
		t.Errorf("AGENT_STATE_UPDATED turnId = %v, want %s", payload["turnId"], outcome.AgentTurnID)
	}
}

// ---------------------------------------------------------------------------
// Deterministic validation failures
// ---------------------------------------------------------------------------

// TestAgentFinal_OutputFailsSchema_FailsInvalidActionNoVersions covers
// docs/09-testing-and-acceptance.md §3.3: a committed Final Decision whose output does not
// satisfy the frozen Output Schema fails the Action as INVALID_ACTION in the same
// transaction, and commits no partial State, no Agent output and no successful termination.
// The model is not asked again and the Decision is left exactly as it was committed.
func TestAgentFinal_OutputFailsSchema_FailsInvalidActionNoVersions(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	generated := agentScriptFinal(h, mockmodel.Scenario{Output: "x"})
	def := h.saveDefinition(agentFinalDefinition("wf-agent-final-output-schema",
		// The frozen Schema accepts "" because ParseAgentNodeConfig requires an
		// outputSchema to accept a string, and rejects the model's "x".
		`{"type":"string","enum":["","approved"]}`, ""))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: an invalid Final Action never asks the model again", got)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionFailed {
		t.Fatalf("action status = %s, want FAILED", action.Status)
	}
	if action.Error == nil || action.Error.Code != "INVALID_ACTION" {
		t.Errorf("action error = %+v, want INVALID_ACTION", action.Error)
	}
	decision, found := agentDecisionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("the committed Decision disappeared")
	}
	assertSameJSON(t, "committed decision output", json.RawMessage(`"x"`), decision.Output)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Fatalf("agent run termination = %v, want INVALID_ACTION", agentRun.Termination)
	}
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a Final Context Version was committed by a failed Action")
	}
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a State Version was committed by a failed Action")
	}
	if got := agentPointers(agentRun); got != [3]int{1, 0, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 0 0]", got)
	}

	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if len(nodeRun.Output) != 0 {
		t.Errorf("failed agent node run stored output %s", nodeRun.Output)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}

	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentActionCompleted) != 0 {
		t.Errorf("events = %v, want no AGENT_ACTION_COMPLETED", types)
	}
	failed := agentEventIndex(types, domain.EventAgentActionFailed)
	agentFailed := agentEventIndex(types, domain.EventAgentFailed)
	nodeFailed := agentEventIndex(types, domain.EventNodeFailed)
	if failed < 0 || !(failed < agentFailed && agentFailed < nodeFailed) {
		t.Errorf("events = %v, want AGENT_ACTION_FAILED < AGENT_FAILED < NODE_FAILED", types)
	}
	payload := agentEventPayload(t, listEvents(h.ctx, t, h.uow, run.ID), domain.EventAgentActionFailed)
	if payload["failureSource"] != string(domain.FailureSyncExecution) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want SYNC_EXECUTION", payload["failureSource"])
	}
	if _, present := payload["toolAttemptId"]; present {
		t.Errorf("AGENT_ACTION_FAILED names a Tool Attempt: a FINAL Action never has one")
	}
}

// TestAgentFinal_StatePatchFailsSchema_FailsInvalidActionNoVersions is the same
// deterministic failure for the other validated fact (docs/05-data-model.md §1.4): the
// patch applies, but the resulting complete State does not satisfy the frozen State
// Schema, so no State Version is created and the pointer does not move.
func TestAgentFinal_StatePatchFailsSchema_FailsInvalidActionNoVersions(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	agentScriptFinal(h, mockmodel.Scenario{Output: "done", StatePatch: json.RawMessage(`{"n":"bad"}`)})
	def := h.saveDefinition(agentFinalDefinition("wf-agent-final-state-schema", "",
		`{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionFailed {
		t.Fatalf("action status = %s, want FAILED", action.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Fatalf("agent run termination = %v, want INVALID_ACTION", agentRun.Termination)
	}
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a State Version was created although the patched State fails the State Schema")
	}
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a Final Context Version was committed by a failed Action")
	}
	if got := agentPointers(agentRun); got != [3]int{1, 0, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 0 0]", got)
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentStateUpdated) != 0 {
		t.Errorf("events = %v, want no AGENT_STATE_UPDATED", types)
	}
}

// ---------------------------------------------------------------------------
// Races, rollback and rejected input
// ---------------------------------------------------------------------------

// TestAgentFinal_ReadyToRunningRace_OnlyOneCompletionWins proves the conditional claim is
// what elects the single completer when immediate advancement and the Reconciler reach the
// same Final Action (invariant #7, docs/09 §3.3). The loser writes nothing at all: one
// Event pair, one Final Context Version, one NodeRun completion.
func TestAgentFinal_ReadyToRunningRace_OnlyOneCompletionWins(t *testing.T) {
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptFinal(h, mockmodel.Scenario{Output: "done"})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-final-race"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentReadyFinalAction(h, stop, run.ID)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- h.svc.CompleteAgentFinal(h.ctx, action.ID, domain.ClaimImmediate)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("complete agent final: %v", err)
		}
	}

	completed, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if completed.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", completed.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if got := agentPointers(agentRun); got != [3]int{1, 1, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 1 0]: only one completion moved them", got)
	}
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("the loser committed a second Context Version")
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunSucceeded {
		t.Errorf("agent node run status = %s, want SUCCEEDED", nodeRun.Status)
	}

	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	for _, typ := range []domain.EventType{
		domain.EventAgentActionStarted, domain.EventAgentActionCompleted,
		domain.EventAgentCompleted, domain.EventNodeCompleted,
	} {
		if got := agentCountEventType(types, typ); got != 1 {
			t.Errorf("%s events = %d, want exactly 1: %v", typ, got, types)
		}
	}
}

// TestAgentFinal_NeverCommitsRunningFinalAction proves docs/09 §3.3's "no committed
// RUNNING Final Action": the claim and the completion are the same transaction, so a
// second connection watching the row while the completion runs can only ever read the
// committed READY status or the committed SUCCEEDED one.
//
// The observer polls from its own pooled connection rather than using an in-transaction
// hook, because the service exposes none inside this transaction; a missed sample can only
// weaken the proof, never turn a correct implementation red.
func TestAgentFinal_NeverCommitsRunningFinalAction(t *testing.T) {
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptFinal(h, mockmodel.Scenario{Output: "done"})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-final-no-running"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	_, action := agentReadyFinalAction(h, stop, run.ID)

	done := make(chan struct{})
	observed := make(chan []domain.AgentActionStatus, 1)
	go func() {
		seen := map[domain.AgentActionStatus]bool{}
		var order []domain.AgentActionStatus
		for {
			select {
			case <-done:
				observed <- order
				return
			default:
			}
			var current domain.AgentAction
			if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
				var err error
				current, err = tx.AgentActions().Get(ctx, action.ID)
				return err
			}); err != nil {
				continue
			}
			if !seen[current.Status] {
				seen[current.Status] = true
				order = append(order, current.Status)
			}
		}
	}()

	if err := h.svc.CompleteAgentFinal(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("complete agent final: %v", err)
	}
	close(done)

	for _, status := range <-observed {
		if status != domain.AgentActionReady && status != domain.AgentActionSucceeded {
			t.Errorf("a second connection observed a Final Action in %s; only READY or SUCCEEDED may ever be committed", status)
		}
	}
	if completed, _ := agentActionOfTurn(h.ctx, t, h.uow, action.TurnID); completed.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", completed.Status)
	}
}

// TestAgentFinal_TxFailureBeforeCommit_ActionStaysReady covers docs/09 §3.3: when the
// completion transaction cannot commit, every write in it rolls back together and the
// Action is still the READY work the Reconciler rediscovers -- and completing it a second
// time succeeds.
//
// The failure is manufactured the same way as the Node and Tool paths' rollback tests: the
// Event ID the transaction is about to insert is pre-occupied.
func TestAgentFinal_TxFailureBeforeCommit_ActionStaysReady(t *testing.T) {
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptFinal(h, mockmodel.Scenario{Output: "done"})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-final-rollback"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentReadyFinalAction(h, stop, run.ID)

	const collidingEventID = "ev_agent_final_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())
	h.ids.ForceNextEventID(collidingEventID)

	if err := h.svc.CompleteAgentFinal(h.ctx, action.ID, domain.ClaimImmediate); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("complete agent final = %v, want a conflict from the duplicated Event ID", err)
	}

	rolledBack, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if rolledBack.Status != domain.AgentActionReady {
		t.Errorf("action status = %s, want READY: the completion transaction rolled back", rolledBack.Status)
	}
	if rolledBack.StartedAt != nil || rolledBack.CompletedAt != nil {
		t.Errorf("rolled-back action recorded timestamps (started %v, completed %v)", rolledBack.StartedAt, rolledBack.CompletedAt)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a Context Version survived the rolled-back completion transaction")
	}
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s after a rolled-back transaction", *agentRun.Termination)
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nodeRun.Status)
	}

	if err := h.svc.CompleteAgentFinal(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("second complete agent final: %v", err)
	}
	retried, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if retried.Status != domain.AgentActionSucceeded {
		t.Errorf("action status after the retried completion = %s, want SUCCEEDED", retried.Status)
	}
	if !agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("the retried completion committed no Final Context Version")
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunSucceeded {
		t.Errorf("agent node run status = %s, want SUCCEEDED after the retried completion", nodeRun.Status)
	}
}

// TestAgentFinal_ToolCallActionRejected_NoWrite pins the use case boundary: a TOOL_CALL
// Action handed to the Final completion use case is a caller defect, not a state to
// repair. It is refused with an error and nothing is written, so the Action stays the
// READY work its own use case executes.
func TestAgentFinal_ToolCallActionRejected_NoWrite(t *testing.T) {
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-final-tool-call"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentReadyFinalAction(h, stop, run.ID)
	if action.Type != domain.AgentActionToolCall {
		t.Fatalf("action type = %s, want TOOL_CALL for this test", action.Type)
	}

	before := len(listEvents(h.ctx, t, h.uow, run.ID))
	if err := h.svc.CompleteAgentFinal(h.ctx, action.ID, domain.ClaimImmediate); err == nil {
		t.Fatalf("complete agent final on a TOOL_CALL action = nil, want an error")
	}

	unchanged, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if unchanged.Status != domain.AgentActionReady || unchanged.StartedAt != nil {
		t.Errorf("action = (%s, started %v), want an untouched READY row", unchanged.Status, unchanged.StartedAt)
	}
	if got := len(listEvents(h.ctx, t, h.uow, run.ID)); got != before {
		t.Errorf("events = %d, want %d: the refused call wrote nothing", got, before)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Errorf("agent run terminated as %s after a refused call", *agentRun.Termination)
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nodeRun.Status)
	}
}

// ---------------------------------------------------------------------------
// The whole loop
// ---------------------------------------------------------------------------

// TestAgentLoop_ToolCallThenFinal_RunCompletesWithDownstreamOutput drives the Agent Loop
// the way it runs in production: a TOOL_CALL round feeds its result into the next Turn,
// whose FINAL Decision is completed by the Final completion use case, which succeeds the
// Agent NodeRun and lets the downstream node produce the Run output.
func TestAgentLoop_ToolCallThenFinal_RunCompletesWithDownstreamOutput(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	var generated atomic.Int32
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if generated.Add(1) > 1 {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "answer: k1"}
		}
		return &mockmodel.Scenario{
			Kind: mockmodel.ScenarioToolCall, ToolName: lookup.ToolName,
			ToolArguments: json.RawMessage(agentToolArguments),
		}
	}
	def := h.saveDefinition(agentLoopDefinition("wf-agent-final-two-rounds"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	if got := generated.Load(); got != 2 {
		t.Errorf("model calls = %d, want 2: the Tool result starts exactly one further round", got)
	}
	if got := tool.count(); got != 1 {
		t.Errorf("tool calls = %d, want 1", got)
	}
	// The second Turn's model request is the proof that the Tool result really reached
	// the model: it was built from Context Version 1.
	requests := h.provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("recorded model requests = %d, want 2", len(requests))
	}
	if last := requests[1]; len(last.Messages) != 3 {
		t.Errorf("second model request carries %d messages, want 3", len(last.Messages))
	} else if last.Messages[2].Role != runtime.AgentRoleTool || last.Messages[2].ToolActionID == nil {
		t.Errorf("second model request's last message = (%q, action %v), want a tool message naming its Action",
			last.Messages[2].Role, last.Messages[2].ToolActionID)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	secondTurnID, _ := agentLastEventPayload(t, events, domain.EventAgentTurnReady)["turnId"].(string)
	if secondTurnID == "" || secondTurnID == outcome.AgentTurnID {
		t.Fatalf("no second turn was created: last AGENT_TURN_READY names %q", secondTurnID)
	}
	if turn := getAgentTurn(h.ctx, t, h.uow, secondTurnID); turn.Status != domain.AgentTurnCompleted || turn.TurnNo != 2 {
		t.Errorf("turn 2 = (status %s, no %d), want (COMPLETED, 2)", turn.Status, turn.TurnNo)
	}
	finalAction, _ := agentActionOfTurn(h.ctx, t, h.uow, secondTurnID)
	if finalAction.Type != domain.AgentActionFinal || finalAction.Status != domain.AgentActionSucceeded {
		t.Errorf("turn 2 action = (%s, %s), want (FINAL, SUCCEEDED)", finalAction.Type, finalAction.Status)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, finalAction.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the FINAL action = %d, want 0", len(attempts))
	}

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if got := agentPointers(agentRun); got != [3]int{2, 2, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [2 2 0]", got)
	}
	messages := agentContextMessages(t, agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 2).Messages)
	if len(messages) != 4 {
		t.Fatalf("context version 2 carries %d messages, want 4 (user, assistant tool call, tool result, assistant final)", len(messages))
	}
	wantRoles := []string{runtime.AgentRoleUser, runtime.AgentRoleAssistant, runtime.AgentRoleTool, runtime.AgentRoleAssistant}
	for i, want := range wantRoles {
		if messages[i].Role != want {
			t.Errorf("message %d role = %q, want %q", i, messages[i].Role, want)
		}
	}
	assertSameJSON(t, "final assistant message", json.RawMessage(`"answer: k1"`), messages[3].Content)

	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentTurnReady) != 2 {
		t.Errorf("events = %v, want two AGENT_TURN_READY", types)
	}
	if agentCountEventType(types, domain.EventAgentDecisionCommitted) != 2 {
		t.Errorf("events = %v, want two AGENT_DECISION_COMMITTED", types)
	}
	if agentCountEventType(types, domain.EventAgentFailed) != 0 {
		t.Errorf("events = %v, want no AGENT_FAILED", types)
	}

	agentDrive(h, run.ID)
	persisted := agentRunRow(h.ctx, t, h.uow, run.ID)
	if persisted.Status != domain.RunCompleted {
		t.Fatalf("run status = %s, want COMPLETED", persisted.Status)
	}
	assertSameJSON(t, "run output", json.RawMessage(`{"text":"answer: k1"}`), persisted.Output)
}
