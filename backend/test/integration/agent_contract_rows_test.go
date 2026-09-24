//go:build integration

// Agent Loop contract rows that the sibling agent_*_test.go files leave uncovered or only
// partially covered (docs/09-testing-and-acceptance.md §3.3): the Tool *failure*
// transaction's own rollback, the frozen values the next Turn's ModelRequest must carry,
// the "Context exceeds the model limit" behaviour the MVP actually has, and the two
// Tool-Action state-patch rows (schema failure and semantic no-op).
//
// They share the agentHarness, the fixture Definition and the helpers of
// agent_loop_test.go / agent_action_test.go; nothing here duplicates a fixture that
// already exists there.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// ---------------------------------------------------------------------------
// Row 5: the Tool failure transaction fails before COMMIT
// ---------------------------------------------------------------------------

// TestAgentAction_ToolFailureTxFailure_RollsBackTerminationAndLeavesActionRecoverable
// covers docs/09 §3.3 "Tool failure 事务在 COMMIT 前失败": every failure fact rolls back
// together, so no partial termination, no Context/State Version and no next Turn survive.
//
// Its sibling TestAgentAction_ResultTxFailure_RollsBackAttemptActionVersionsAndTurn covers
// the *success* completion transaction. This one is the mirror image and matters on its
// own: the failure transaction writes a different set of facts (Attempt FAILED, Action
// FAILED, Agent termination, Agent NodeRun FAILED, Run aggregation and three Events), and
// a partial commit there would leave a Run terminated for a Tool failure nobody recorded.
//
// The failure is manufactured the same way as the other rollback tests: the Event ID the
// transaction is about to insert is pre-occupied, so its first Event insert violates the
// primary key. Arming it from inside the Tool call makes the collision land on the
// *failure* transaction's first Event (AGENT_ACTION_FAILED) rather than on one of the
// claim transaction's.
//
// What must survive is exactly what the claim transaction committed: a STARTED Tool
// Attempt under a RUNNING Action -- recoverable work, not a half-failed Agent Run.
func TestAgentAction_ToolFailureTxFailure_RollsBackTerminationAndLeavesActionRecoverable(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	generated := agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName: lookup.ToolName,
		// MissingKey is the `lookup` Tool's one deterministic failure key, so the Tool
		// really is called and really returns an error.
		ToolArguments: json.RawMessage(`{"key":"` + lookup.MissingKey + `"}`),
		StatePatch:    json.RawMessage(`{"seen":["` + lookup.MissingKey + `"]}`),
	})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-tool-failure-rollback"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	const collidingEventID = "ev_agent_tool_failure_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())
	tool.during = func(context.Context, registry.ToolAction) {
		h.ids.ForceNextEventID(collidingEventID)
	}

	err := h.svc.Execute(h.ctx, outcome)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("execute agent node run = %v, want a conflict from the duplicated Event ID", err)
	}
	if got := tool.count(); got != 1 {
		t.Fatalf("tool calls = %d, want exactly 1: the Tool ran and failed before the rolled-back transaction", got)
	}

	// The Action is still the RUNNING work its claim transaction committed.
	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action exists for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionRunning {
		t.Errorf("action status = %s, want RUNNING: the failure transaction rolled back", action.Status)
	}
	if action.CompletedAt != nil || action.Error != nil {
		t.Errorf("rolled-back action recorded completion (completedAt=%v, error=%v)", action.CompletedAt, action.Error)
	}

	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1: the rollback creates no second Attempt", len(attempts))
	}
	attempt := attempts[0]
	if attempt.Status != domain.ToolAttemptStarted {
		t.Errorf("tool attempt status = %s, want STARTED: the FAILED transition rolled back", attempt.Status)
	}
	if attempt.CompletedAt != nil || attempt.Error != nil {
		t.Errorf("rolled-back tool attempt recorded completedAt=%v error=%v, want neither", attempt.CompletedAt, attempt.Error)
	}

	// No termination, and no version the failure transaction would never have written
	// anyway -- both are asserted so a future implementation that moved a pointer before
	// failing is caught here rather than in production.
	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.Termination != nil || agentRun.TerminatedAt != nil || agentRun.Error != nil {
		t.Errorf("agent run terminated as %v after a rolled-back transaction (terminatedAt=%v, error=%v)",
			agentRun.Termination, agentRun.TerminatedAt, agentRun.Error)
	}
	if got := agentPointers(agentRun); got != [3]int{1, 0, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 0 0]", got)
	}
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a Context Version survived the rolled-back failure transaction")
	}
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a State Version survived the rolled-back failure transaction")
	}
	if turns := listReadyTurns(h.ctx, t, h.uow); len(turns) != 0 {
		t.Errorf("ready turns = %d, want 0: a failure transaction starts no round", len(turns))
	}

	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING: the NODE_FAILED transition rolled back", nodeRun.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunRunning {
		t.Errorf("run status = %s, want RUNNING: the Run aggregation rolled back", persisted.Status)
	}

	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	for _, unwanted := range []domain.EventType{
		domain.EventAgentActionFailed, domain.EventAgentFailed, domain.EventNodeFailed,
		domain.EventAgentActionCompleted, domain.EventAgentStateUpdated,
	} {
		if agentCountEventType(types, unwanted) != 0 {
			t.Errorf("events = %v, want no %s", types, unwanted)
		}
	}
	if agentCountEventType(types, domain.EventAgentActionStarted) != 1 {
		t.Errorf("events = %v, want the claim transaction's single AGENT_ACTION_STARTED to survive", types)
	}
	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: a rolled-back transaction does not re-ask the model", got)
	}
}

// ---------------------------------------------------------------------------
// Row 16: the next Turn's ModelRequest carries only frozen values
// ---------------------------------------------------------------------------

// agentFrozenRequestModelConfig is the modelConfig the row-16 fixture freezes. It is
// deliberately *not* mockmodel's ConfigSchema default, so a request built from the
// Registry instead of from the Agent Run's frozen snapshot is visible.
const agentFrozenRequestModelConfig = `{"temperature":1.5}`

// agentFrozenRequestOutputSchema is the fixture's frozen Final Output Schema. The mock
// Provider's FINAL output is a JSON string, so a string schema keeps the fixture runnable
// past the point this test stops at.
const agentFrozenRequestOutputSchema = `{"type":"string"}`

// agentFrozenRequestStateSchema accepts the fixture's state patch and State Version 0's
// `{}`, so State can carry a non-trivial value the request must reproduce exactly.
const agentFrozenRequestStateSchema = `{"type":"object","additionalProperties":true}`

// agentFrozenRequestDefinition is agentLoopDefinition with every value the ModelRequest
// contract of docs/09 §3.3 names frozen to something distinguishable: two allowed Tools in
// an order that is neither alphabetical nor the Registry's registration order, an explicit
// modelConfig, and both Schemas.
func agentFrozenRequestDefinition(workflowID string) domain.Definition {
	def := agentLoopDefinition(workflowID)
	config := `{
		"instructions": "` + agentInstructions + `",
		"modelId": "` + mockmodel.ModelID + `",
		"modelConfig": ` + agentFrozenRequestModelConfig + `,
		"allowedTools": ["` + agentSecondToolName + `", "` + lookup.ToolName + `"],
		"stateSchema": ` + agentFrozenRequestStateSchema + `,
		"outputSchema": ` + agentFrozenRequestOutputSchema + `,
		"maxTurns": ` + strconv.Itoa(agentMaxTurns) + `,
		"timeoutMs": ` + strconv.Itoa(agentTimeoutMs) + `
	}`
	for i := range def.Nodes {
		if def.Nodes[i].ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(config)
		}
	}
	return def
}

// TestAgentTurn_ModelRequest_MatchesFrozenInstructionsToolsSchemaAndVersions covers
// docs/09 §3.3 "创建下一 Turn 的 ModelRequest": after a Tool round, the second request the
// Provider receives must be built entirely from the Agent Run's frozen snapshot and its
// current version pointers -- Instructions, the current Context Version's messages, the
// current State Version's value, the frozen model parameters, the frozen Final Output
// Schema, and the Tool specs in frozen allowlist order.
//
// The second Turn is advanced by hand from the harness's recorded enqueue and stopped as
// soon as its Decision commits, so the Agent Run's pointers are still exactly the ones
// that request was built from; comparing against pointers a later Turn had already moved
// would prove nothing.
func TestAgentTurn_ModelRequest_MatchesFrozenInstructionsToolsSchemaAndVersions(t *testing.T) {
	lookupTool := &agentRecordingTool{delegate: lookup.Executor{}}
	vaultTool := &agentRecordingTool{delegate: lookup.Executor{}}
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, agentHarnessOptions{
		LookupExecutor: lookupTool,
		Tools:          []registry.ToolRegistration{agentSecondToolRegistration(vaultTool)},
		Notifier:       stop,
	})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName:      lookup.ToolName,
		ToolArguments: json.RawMessage(agentToolArguments),
		StatePatch:    json.RawMessage(`{"seen":["k1"]}`),
	})
	def := h.saveDefinition(agentFrozenRequestDefinition("wf-agent-frozen-model-request"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	stopCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop.cancel = cancel
	// Armed inside the *second* model call, so the notification that stops the chain is
	// that Turn's own Decision commit: nothing after it moves a version pointer.
	var modelCalls atomic.Int32
	h.provider.BeforeReturn = func(context.Context) {
		if modelCalls.Add(1) == 2 {
			stop.arm()
		}
	}
	if err := h.svc.Execute(stopCtx, outcome); err != nil {
		t.Fatalf("execute agent node run: %v", err)
	}
	if err := h.drainTurns(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("advance the enqueued second turn = %v, want context.Canceled from the stopped chain", err)
	}
	h.provider.BeforeReturn = nil

	requests := h.provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want exactly 2 (the Tool round's Turn and the next Turn)", len(requests))
	}
	next := requests[1]

	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.CurrentContextVersion != 1 || agentRun.CurrentStateVersion != 1 {
		t.Fatalf("pointers (context %d, state %d), want (1, 1): the fixture's Tool round must have appended both",
			agentRun.CurrentContextVersion, agentRun.CurrentStateVersion)
	}

	// Instructions and Model ID come from the frozen Agent Run, never from the Definition
	// re-read at request time.
	if next.ModelID != agentRun.ModelID {
		t.Errorf("request modelId = %q, want the Agent Run's frozen %q", next.ModelID, agentRun.ModelID)
	}
	if next.Instructions != agentRun.Instructions || next.Instructions != agentInstructions {
		t.Errorf("request instructions = %q, want the frozen %q", next.Instructions, agentInstructions)
	}

	// Messages are the current Context Version, in order and unrewritten. 05 §2 also
	// forbids copying Instructions into the messages.
	stored := agentContextMessages(t, agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 1).Messages)
	if len(next.Messages) != len(stored) {
		t.Fatalf("request messages = %d, want the current Context Version's %d", len(next.Messages), len(stored))
	}
	for i := range stored {
		got, want := next.Messages[i], stored[i]
		if got.Role != want.Role {
			t.Errorf("request message[%d] role = %q, want %q", i, got.Role, want.Role)
		}
		assertSameJSON(t, "request message["+strconv.Itoa(i)+"] content", want.Content, got.Content)
		if !agentSamePointer(got.ToolName, want.ToolName) {
			t.Errorf("request message[%d] toolName = %v, want %v", i, got.ToolName, want.ToolName)
		}
		if !agentSamePointer(got.ToolActionID, want.ToolActionID) {
			t.Errorf("request message[%d] toolActionId = %v, want %v", i, got.ToolActionID, want.ToolActionID)
		}
		if bytes.Contains(got.Content, []byte(agentInstructions)) {
			t.Errorf("request message[%d] carries the Instructions: they travel in Instructions only", i)
		}
	}

	// State is the current State Version's value, not the empty State Version 0 and not a
	// value recomputed from the Decision's patch.
	stateV1 := agentStateVersion(h.ctx, t, h.uow, agentRun.ID, 1)
	assertSameJSON(t, "request state", stateV1.Value, next.State)
	assertSameJSON(t, "request state", json.RawMessage(`{"seen":["k1"]}`), next.State)

	// Model parameters and the Final Output Schema are the Agent Run's frozen snapshots.
	assertSameJSON(t, "request modelConfig", agentRun.ModelConfig, next.ModelConfig)
	assertSameJSON(t, "request modelConfig", json.RawMessage(agentFrozenRequestModelConfig), next.ModelConfig)
	assertSameJSON(t, "request finalOutputSchema", agentRun.OutputSchema, next.FinalOutputSchema)
	assertSameJSON(t, "request finalOutputSchema", json.RawMessage(agentFrozenRequestOutputSchema), next.FinalOutputSchema)

	// Tool specs follow the frozen allowlist order, which is deliberately the reverse of
	// the order the two Tools were registered in, and are built from registered Metadata.
	wantTools := []domain.ToolMetadata{
		agentSecondToolRegistration(vaultTool).Metadata,
		lookup.Registration().Metadata,
	}
	if len(next.Tools) != len(wantTools) {
		t.Fatalf("request tools = %d, want %d (the whole frozen allowlist)", len(next.Tools), len(wantTools))
	}
	for i, want := range wantTools {
		got := next.Tools[i]
		if got.Name != want.Name {
			t.Errorf("request tools[%d].Name = %q, want %q (frozen allowlist order)", i, got.Name, want.Name)
		}
		if got.Description != want.Description {
			t.Errorf("request tools[%d].Description = %q, want the registered %q", i, got.Description, want.Description)
		}
		assertSameJSON(t, "request tools["+strconv.Itoa(i)+"].InputSchema", want.InputSchema, got.InputSchema)
		assertSameJSON(t, "request tools["+strconv.Itoa(i)+"].OutputSchema", want.OutputSchema, got.OutputSchema)
	}

	// The Tool that is merely allowed was never called: allowlist membership is a request
	// input, not an execution.
	if vaultTool.count() != 0 {
		t.Errorf("%s was called %d times, want 0", agentSecondToolName, vaultTool.count())
	}
	if lookupTool.count() != 1 {
		t.Errorf("%s was called %d times, want 1", lookup.ToolName, lookupTool.count())
	}
}

// agentSamePointer compares two optional message fields by value.
func agentSamePointer(got, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

// ---------------------------------------------------------------------------
// Row 30: Context exceeds the model limit
// ---------------------------------------------------------------------------

// TestAgentTurn_ProviderRejectsOversizedContext_FailsModelErrorWithoutTruncation covers
// docs/09 §3.3 "Context 超过模型限制": the Turn fails explicitly and the MVP never silently
// trims, summarises or drops a message.
//
// There is no context-limit field anywhere in registry.ModelRegistration or the Agent Run,
// so the limit is the Provider's to enforce: it rejects the call. What this test pins is
// therefore the behaviour the MVP does have -- that rejection becomes a MODEL_ERROR
// termination, and the committed Context Version the request was built from is still
// byte-for-byte what the Tool round committed, with no shorter successor written in the
// name of retrying.
func TestAgentTurn_ProviderRejectsOversizedContext_FailsModelErrorWithoutTruncation(t *testing.T) {
	contextLengthErr := errors.New("context_length_exceeded: request exceeds the model's maximum context length")

	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-context-too-long"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}

	// contextAtRequest is read inside the rejected call itself, after the Turn's claim
	// transaction committed and before the failure transaction runs. Comparing it with the
	// row that survives the failure is what proves no later transaction rewrote history.
	var contextAtRequest json.RawMessage
	var rejectedMessages int
	var modelCalls atomic.Int32
	h.provider.Script = func(request registry.ModelRequest) *mockmodel.Scenario {
		if modelCalls.Add(1) == 1 {
			return &mockmodel.Scenario{
				Kind: mockmodel.ScenarioToolCall, ToolName: lookup.ToolName,
				ToolArguments: json.RawMessage(agentToolArguments),
			}
		}
		rejectedMessages = len(request.Messages)
		contextAtRequest = agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 1).Messages
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFail, Err: contextLengthErr}
	}

	h.execute(outcome)

	if got := modelCalls.Load(); got != 2 {
		t.Fatalf("model calls = %d, want 2: the Tool round's Turn and the rejected one", got)
	}

	// The rejected Turn -- not the Tool round's Turn -- is the one that failed, and it
	// produced neither a Decision nor an Action.
	rejectedTurn := agentTurnByNo(h.ctx, t, h.uow, agentRun.ID, 2)
	if rejectedTurn.Status != domain.AgentTurnFailed {
		t.Errorf("turn 2 status = %s, want FAILED", rejectedTurn.Status)
	}
	if rejectedTurn.Error == nil || rejectedTurn.CompletedAt == nil {
		t.Errorf("failed turn 2 records error=%v completedAt=%v, want both", rejectedTurn.Error, rejectedTurn.CompletedAt)
	}
	if _, found := agentDecisionOfTurn(h.ctx, t, h.uow, rejectedTurn.ID); found {
		t.Errorf("a Decision was committed for the rejected Turn")
	}
	if _, found := agentActionOfTurn(h.ctx, t, h.uow, rejectedTurn.ID); found {
		t.Errorf("an Action was created for the rejected Turn")
	}
	if turn1 := agentTurnByNo(h.ctx, t, h.uow, agentRun.ID, 1); turn1.Status != domain.AgentTurnCompleted {
		t.Errorf("turn 1 status = %s, want COMPLETED: the Tool round's Turn is untouched", turn1.Status)
	}

	// The failure is MODEL_ERROR, and it closes the Agent NodeRun and the Run with it.
	failed, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if failed.Termination == nil || *failed.Termination != domain.TerminationModelError {
		t.Fatalf("agent run termination = %v, want MODEL_ERROR", failed.Termination)
	}
	if failed.TerminatedAt == nil || failed.Error == nil {
		t.Errorf("terminated agent run records terminatedAt=%v error=%v, want both", failed.TerminatedAt, failed.Error)
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if persisted := agentRunRow(h.ctx, t, h.uow, run.ID); persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentFailed) != 1 || agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("events = %v, want one AGENT_FAILED and one NODE_FAILED", types)
	}

	// The whole history reached the Provider: the Runtime did not shorten the request to
	// make it fit.
	contextV1 := agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 1)
	survived := agentContextMessages(t, contextV1.Messages)
	if len(survived) != 3 {
		t.Fatalf("context version 1 holds %d messages, want 3 (input user, assistant TOOL_CALL, tool result)", len(survived))
	}
	if rejectedMessages != len(survived) {
		t.Errorf("rejected request carried %d messages, want the current Context Version's %d: the MVP does not trim to fit",
			rejectedMessages, len(survived))
	}

	// Byte-identical to what the Tool round committed: no truncation, no summary, no
	// rewrite of an immutable version.
	if len(contextAtRequest) == 0 {
		t.Fatal("the rejected call observed no Context Version 1")
	}
	if !bytes.Equal(contextAtRequest, contextV1.Messages) {
		t.Errorf("context version 1 changed across the failing Turn:\nbefore %s\nafter  %s", contextAtRequest, contextV1.Messages)
	}
	if survived[0].Role != "user" {
		t.Errorf("context version 1 message[0] role = %q, want the original input user message to still be first", survived[0].Role)
	}

	// No successor version, and the pointer is still the version the failed Turn read.
	terminated, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if terminated.CurrentContextVersion != 1 {
		t.Errorf("current context version = %d, want 1: a failed Turn moves no pointer", terminated.CurrentContextVersion)
	}
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("a Context Version 2 was written for a Turn that never produced a Decision")
	}
}

// agentTurnByNo resolves one Agent Run's Turn by its number, which is how a test that
// drove more than one round names the Turn it is asserting about.
func agentTurnByNo(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, turnNo int) domain.AgentTurn {
	t.Helper()
	var turn domain.AgentTurn
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		turn, err = tx.AgentTurns().GetByRunAndTurnNo(ctx, agentRunID, turnNo)
		return err
	}); err != nil {
		t.Fatalf("get turn %d of agent run %s: %v", turnNo, agentRunID, err)
	}
	return turn
}

// ---------------------------------------------------------------------------
// Row 32: a Tool-Action state patch that fails the frozen State Schema
// ---------------------------------------------------------------------------

// TestAgentAction_ToolCallStatePatchFailsSchema_FailsInvalidActionNoStateVersion covers
// docs/09 §3.3 "State patch 应用失败或结果不通过 State Schema" on the Tool-Action path (its
// sibling TestAgentFinal_StatePatchFailsSchema_FailsInvalidActionNoVersions covers the
// FINAL path): the Action fails as INVALID_ACTION, no State Version is created, the
// current pointer does not move and no AGENT_STATE_UPDATED is written.
//
// The Tool itself succeeds, which is the whole point: docs/05-data-model.md §1.4 makes a
// TOOL_CALL's patch atomic with the Tool result and the Context append, so a patch the
// frozen State Schema rejects must undo the entire round rather than keep the half of it
// that worked. assertAgentActionFailed pins that the Context Version is not written either.
func TestAgentAction_ToolCallStatePatchFailsSchema_FailsInvalidActionNoStateVersion(t *testing.T) {
	const stateSchema = `{
		"type": "object",
		"properties": {"count": {"type": "number"}},
		"additionalProperties": false
	}`
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})
	generated := agentScriptToolCallThenFinal(h, mockmodel.Scenario{
		ToolName:      lookup.ToolName,
		ToolArguments: json.RawMessage(agentToolArguments),
		// Applies cleanly as a Merge Patch, but the *whole* patched State then fails the
		// frozen Schema, which is the check 05 §1.4 requires.
		StatePatch: json.RawMessage(`{"count":"not-a-number"}`),
	})
	def := h.saveDefinition(agentFinalDefinition("wf-agent-tool-patch-schema", "", stateSchema))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	action := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationInvalidAction, domain.FailureSyncExecution)
	if action.Error == nil || action.Error.Code != "INVALID_ACTION" {
		t.Errorf("failed action error = %v, want code INVALID_ACTION", action.Error)
	}

	// The Tool really ran and really succeeded: its Attempt keeps its result, so the Trace
	// still shows the external call that was made.
	if got := tool.count(); got != 1 {
		t.Errorf("tool calls = %d, want 1: the patch is validated after the Tool returns", got)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1", len(attempts))
	}
	if attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempt status = %s, want SUCCEEDED: the Tool is not what failed", attempts[0].Status)
	}
	assertSameJSON(t, "tool attempt result", json.RawMessage(agentToolResult), attempts[0].Result)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.CurrentStateVersion != 0 {
		t.Errorf("current state version = %d, want 0: a rejected patch moves no pointer", agentRun.CurrentStateVersion)
	}
	if got := agentPointers(agentRun); got != [3]int{1, 0, 0} {
		t.Errorf("pointers (turn, context, state) = %v, want [1 0 0]", got)
	}
	// 05 §1.4 makes the patch atomic with the Tool result and the Context append, so the
	// Context Version must be absent too -- the implementation decides the patch before it
	// writes anything (internal/service/agent_action.go, completeAgentToolCall).
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Errorf("a Context Version was committed for a round that failed as INVALID_ACTION")
	}

	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentStateUpdated) != 0 {
		t.Errorf("events = %v, want no AGENT_STATE_UPDATED", types)
	}
	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: a rejected patch is not re-asked", got)
	}
}

// ---------------------------------------------------------------------------
// Row 33: a patch whose result equals the current State
// ---------------------------------------------------------------------------

// TestAgentAction_ToolCallStatePatchNoOp_NoStateVersionNoEvent covers docs/09 §3.3
// "没有 state patch，或 patch 结果与当前 State 相同": no State Version and no
// AGENT_STATE_UPDATED. docs/05-data-model.md §1.4 adds that the comparison is JSON
// semantic -- "object 字段顺序不构成变化" -- so the fixture drives three Tool rounds:
// one that really changes the State, one that restates the same values, and one that
// restates a nested object with its keys in a different order.
//
// The State Version chain has to stay at V0/V1 across all three, because a "new" version
// identical to its predecessor is an unbounded write amplification in a loop and would
// make every Turn's State pointer move for no recorded change.
func TestAgentAction_ToolCallStatePatchNoOp_NoStateVersionNoEvent(t *testing.T) {
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool})

	patches := []string{
		`{"a":1,"b":{"x":1,"y":2}}`, // turn 1: a real change -> State Version 1
		`{"a":1}`,                   // turn 2: restates a current value -> no change
		`{"b":{"y":2,"x":1}}`,       // turn 3: same nested object, different key order -> no change
	}
	var modelCalls atomic.Int32
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		turn := int(modelCalls.Add(1))
		if turn > len(patches) {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
		return &mockmodel.Scenario{
			Kind: mockmodel.ScenarioToolCall, ToolName: lookup.ToolName,
			ToolArguments: json.RawMessage(agentToolArguments),
			StatePatch:    json.RawMessage(patches[turn-1]),
		}
	}
	// agentMaxTurns is 4: three Tool rounds and the FINAL Turn fit exactly.
	def := h.saveDefinition(agentLoopDefinition("wf-agent-tool-patch-noop"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	if got := modelCalls.Load(); got != 4 {
		t.Fatalf("model calls = %d, want 4 (three Tool rounds and one FINAL)", got)
	}
	if got := tool.count(); got != 3 {
		t.Errorf("tool calls = %d, want 3", got)
	}

	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE", agentRun.Termination)
	}

	// The chain is V0 and V1 only: the two no-op rounds wrote nothing.
	if agentRun.CurrentStateVersion != 1 {
		t.Errorf("current state version = %d, want 1: only the first patch changed the State", agentRun.CurrentStateVersion)
	}
	if !agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 1) {
		t.Fatalf("state version 1 is missing: the first patch really did change the State")
	}
	if agentHasStateVersion(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("a State Version 2 was created by a patch whose result equals the current State")
	}
	assertSameJSON(t, "state version 1",
		json.RawMessage(`{"a":1,"b":{"x":1,"y":2}}`),
		agentStateVersion(h.ctx, t, h.uow, agentRun.ID, 1).Value)

	// Exactly one AGENT_STATE_UPDATED across the whole Run, from the one round that
	// changed the State.
	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if got := agentCountEventType(types, domain.EventAgentStateUpdated); got != 1 {
		t.Errorf("AGENT_STATE_UPDATED events = %d, want exactly 1: events = %v", got, types)
	}
	updated := agentEventPayload(t, events, domain.EventAgentStateUpdated)
	if updated["previousStateVersion"] != float64(0) || updated["stateVersion"] != float64(1) {
		t.Errorf("AGENT_STATE_UPDATED versions = %v, want previous 0 and next 1", updated)
	}

	// The no-op rounds still happened: each appended its own Context Version, so the State
	// short-circuit is about State only and never skips the round itself.
	if agentRun.CurrentContextVersion != 4 {
		t.Errorf("current context version = %d, want 4 (three Tool rounds and the Final append)", agentRun.CurrentContextVersion)
	}
	if agentCountEventType(types, domain.EventAgentActionCompleted) != 4 {
		t.Errorf("events = %v, want four AGENT_ACTION_COMPLETED (three Tool Actions and the Final)", types)
	}
}
