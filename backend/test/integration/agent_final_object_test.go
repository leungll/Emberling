//go:build integration

// Object-form Final completion tests: an Agent whose outputSchema declares the object form
// hands over a JSON object, the Final completion transaction validates it against the
// frozen Schema and publishes its canonical JSON text on the Agent's `text` port. They
// prove against committed PostgreSQL facts what a pure test cannot: the stored Decision
// and Final Context keep the object, the port text is the canonical encoding of what
// PostgreSQL returns, and immediate advancement, the Reconciler and a lost race all write
// the same bytes exactly once.
//
// They share the agentHarness and the fixture Definition of agent_loop_test.go.
package integration

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// agentObjectOutputSchema is an object-form outputSchema inside the strict output subset,
// with a nullable member, a number and an array, so the canonical text has keys to sort,
// a number literal to keep and an order to preserve.
const agentObjectOutputSchema = `{
	"type": "object",
	"properties": {
		"answer": {"type": "string"},
		"score": {"type": "number"},
		"tags": {"type": "array", "items": {"type": "string"}},
		"meta": {"type": ["object", "null"], "properties": {"id": {"type": "integer"}},
			"required": ["id"], "additionalProperties": false}
	},
	"required": ["answer", "score", "tags", "meta"],
	"additionalProperties": false
}`

// agentObjectOutput is the model's single-encoded object FINAL: unsorted keys, a decimal
// with a trailing zero, an integer beyond float64 precision and HTML-significant text.
const agentObjectOutput = `{"tags":["b","a"],"score":1.50,"meta":{"id":12345678901234567890},"answer":"<ada> & co"}`

// agentObjectCanonicalText is the only text the `text` port may carry for agentObjectOutput.
const agentObjectCanonicalText = `{"answer":"<ada> & co","meta":{"id":12345678901234567890},"score":1.50,"tags":["b","a"]}`

func agentObjectDefinition(workflowID string) domain.Definition {
	return agentFinalDefinition(workflowID, agentObjectOutputSchema, "")
}

// agentPortText reads the `text` port of one NodeRun output.
func agentPortText(t *testing.T, output json.RawMessage) string {
	t.Helper()
	var ports map[string]string
	if err := json.Unmarshal(output, &ports); err != nil {
		t.Fatalf("node run output %s is not a text port map: %v", output, err)
	}
	text, ok := ports["text"]
	if !ok || len(ports) != 1 {
		t.Fatalf("node run output %s, want exactly the text port", output)
	}
	return text
}

func TestAgentFinal_ObjectFormOutput_TextPortCarriesCanonicalJSON(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	generated := agentScriptFinal(h, mockmodel.Scenario{FinalOutput: json.RawMessage(agentObjectOutput)})
	def := h.saveDefinition(agentObjectDefinition("wf-agent-final-object"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	if got := generated.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1", got)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Type != domain.AgentActionFinal || action.Status != domain.AgentActionSucceeded {
		t.Fatalf("action = (%s, %s) %+v, want (FINAL, SUCCEEDED)", action.Type, action.Status, action.Error)
	}
	decision, _ := agentDecisionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	assertSameJSON(t, "committed decision output", json.RawMessage(agentObjectOutput), decision.Output)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE", agentRun.Termination)
	}
	assertSameJSON(t, "frozen output schema", json.RawMessage(agentObjectOutputSchema), agentRun.OutputSchema)
	messages := agentContextMessages(t, agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 1).Messages)
	if len(messages) != 2 {
		t.Fatalf("final context version carries %d messages, want 2", len(messages))
	}
	// The Final Context keeps the object itself, not its text.
	assertSameJSON(t, "final assistant message", json.RawMessage(agentObjectOutput), messages[1].Content)

	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run status = %s, want SUCCEEDED", nodeRun.Status)
	}
	if got := agentPortText(t, nodeRun.Output); got != agentObjectCanonicalText {
		t.Errorf("text port =\n  %s\nwant the canonical JSON text\n  %s", got, agentObjectCanonicalText)
	}
	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	if downstream := execNodeRunByNodeID(t, nodeRuns, "node_output"); downstream.Status != domain.NodeRunReady {
		t.Errorf("downstream node run status = %s, want READY", downstream.Status)
	}

	agentDrive(h, run.ID)
	persisted := agentRunRow(h.ctx, t, h.uow, run.ID)
	if persisted.Status != domain.RunCompleted {
		t.Fatalf("run status = %s, want COMPLETED", persisted.Status)
	}
	if got := agentPortText(t, persisted.Output); got != agentObjectCanonicalText {
		t.Errorf("run output text = %s, want %s", got, agentObjectCanonicalText)
	}
}

// assertObjectFormRejected asserts the INVALID_ACTION outcome of an object-form Final
// Action: the Decision is kept, nothing the Action would have produced is committed and
// the model is not asked again.
func assertObjectFormRejected(t *testing.T, h *agentHarness, runID string, outcome service.AdvanceOutcome,
	calls int32, wantDecision, wantMessage string,
) {
	t.Helper()
	if calls != 1 {
		t.Errorf("model calls = %d, want 1: an invalid Final Action never asks the model again", calls)
	}
	action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "INVALID_ACTION" {
		t.Fatalf("action = %s %+v, want FAILED with INVALID_ACTION", action.Status, action.Error)
	}
	if !strings.HasPrefix(action.Error.Message, wantMessage) {
		t.Errorf("action error message = %q, want prefix %q", action.Error.Message, wantMessage)
	}
	decision, found := agentDecisionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("the committed Decision disappeared")
	}
	assertSameJSON(t, "committed decision output", json.RawMessage(wantDecision), decision.Output)

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
	nodeRun := agentNodeRun(h.ctx, t, h.uow, runID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if len(nodeRun.Output) != 0 {
		t.Errorf("failed agent node run stored output %s", nodeRun.Output)
	}
	assertNoDownstreamNodeRun(h.ctx, t, h.uow, runID)
}

func TestAgentFinal_ObjectFormInnerShapeMismatch_FailsInvalidActionNoOutput(t *testing.T) {
	const marker = "model-written-value"
	for _, tc := range []struct {
		name     string
		output   string
		location string
	}{
		{"missing member", `{"answer":"` + marker + `","score":1,"tags":[]}`, "/required"},
		{"wrong type", `{"answer":"` + marker + `","score":"high","tags":[],"meta":null}`, "/properties/score/type"},
		{"extra member", `{"answer":"a","score":1,"tags":[],"meta":null,"` + marker + `":true}`, "/additionalProperties"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAgentHarness(t, agentHarnessOptions{})
			generated := agentScriptFinal(h, mockmodel.Scenario{FinalOutput: json.RawMessage(tc.output)})
			def := h.saveDefinition(agentObjectDefinition("wf-agent-final-object-mismatch"))
			run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
			outcome := h.claimAgentNode(run.ID)

			h.execute(outcome)

			assertObjectFormRejected(t, h, run.ID, outcome, generated.Load(), tc.output,
				"INVALID_ACTION: final output: decoded to a JSON object, but the frozen OutputSchema rejects it at "+tc.location)
			for _, event := range listEvents(h.ctx, t, h.uow, run.ID) {
				if strings.Contains(string(event.Payload), marker) {
					t.Errorf("event %s payload repeats the model's output: %s", event.Type, event.Payload)
				}
			}
		})
	}
}

func TestAgentFinal_ObjectFormStringOutput_FailsInvalidActionNamesType(t *testing.T) {
	encoded, err := json.Marshal(agentObjectOutput)
	if err != nil {
		t.Fatal(err)
	}
	h := newAgentHarness(t, agentHarnessOptions{})
	// The object arrives double-encoded: a JSON string whose content is the object.
	generated := agentScriptFinal(h, mockmodel.Scenario{FinalOutput: encoded})
	def := h.saveDefinition(agentObjectDefinition("wf-agent-final-object-string"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	assertObjectFormRejected(t, h, run.ID, outcome, generated.Load(), string(encoded),
		"INVALID_ACTION: final output: decoded to a JSON string, but the frozen OutputSchema requires a JSON object")
}

// TestAgentFinal_ObjectFormDecisionCommittedThenCrash_ReconcilerCompletesSameAction stops
// the process right after the object Decision and its READY Final Action commit. A
// restarted Backend's Reconciler completes that same Action without asking the model, and
// the text it publishes is byte-identical to the text immediate advancement publishes for
// the same committed Decision.
func TestAgentFinal_ObjectFormDecisionCommittedThenCrash_ReconcilerCompletesSameAction(t *testing.T) {
	stop := &agentStopNotifier{}
	crashed := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptFinal(crashed, mockmodel.Scenario{FinalOutput: json.RawMessage(agentObjectOutput)})
	def := crashed.saveDefinition(agentObjectDefinition("wf-agent-final-object-crash"))

	immediateRun := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	crashed.execute(crashed.claimAgentNode(immediateRun.ID))
	immediateText := agentPortText(t, agentNodeRun(crashed.ctx, t, crashed.uow, immediateRun.ID).Output)

	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentReadyFinalAction(crashed, stop, run.ID)

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "must never be produced"})
	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ReadyAgentActionsFound != 1 || report.AgentActionsAdvanced != 1 {
		t.Fatalf("report: want 1 ready Agent Action advanced, got %+v", report)
	}
	if got := generated.Load(); got != 0 {
		t.Errorf("model calls in the restarted process = %d, want 0", got)
	}
	if completed := getAgentAction(restarted.ctx, t, restarted.uow, action.ID); completed.Status != domain.AgentActionSucceeded {
		t.Fatalf("rediscovered final action status = %s %+v, want SUCCEEDED", completed.Status, completed.Error)
	}
	started := agentOnlyPayloadFor(t, listEvents(restarted.ctx, t, restarted.uow, run.ID),
		domain.EventAgentActionStarted, "actionId", action.ID)
	if started["claimSource"] != string(domain.ClaimReconciler) {
		t.Errorf("AGENT_ACTION_STARTED claimSource = %v, want RECONCILER", started["claimSource"])
	}

	recoveredText := agentPortText(t, agentNodeRun(restarted.ctx, t, restarted.uow, run.ID).Output)
	if recoveredText != immediateText {
		t.Errorf("Reconciler text =\n  %s\nimmediate text =\n  %s\nwant byte-identical", recoveredText, immediateText)
	}
	if recoveredText != agentObjectCanonicalText {
		t.Errorf("text port = %s, want %s", recoveredText, agentObjectCanonicalText)
	}
	agentRun, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE", agentRun.Termination)
	}
}

// TestAgentFinal_ObjectFormCrashAfterCommit_DownstreamRunsOnce loses everything after the
// Final completion COMMIT: the harness drops the post-COMMIT advancement and the process
// restarts. The persisted READY downstream NodeRun is the only recovery source; the
// Reconciler runs it exactly once, it receives the canonical text, and the Agent NodeRun's
// committed output never changes.
func TestAgentFinal_ObjectFormCrashAfterCommit_DownstreamRunsOnce(t *testing.T) {
	crashed := newAgentHarness(t, agentHarnessOptions{})
	agentScriptFinal(crashed, mockmodel.Scenario{FinalOutput: json.RawMessage(agentObjectOutput)})
	def := crashed.saveDefinition(agentObjectDefinition("wf-agent-final-object-after-commit"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	crashed.execute(crashed.claimAgentNode(run.ID))

	committed := agentNodeRun(crashed.ctx, t, crashed.uow, run.ID)
	if committed.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run status = %s, want SUCCEEDED before the crash", committed.Status)
	}
	downstream := execNodeRunByNodeID(t, listNodeRuns(crashed.ctx, t, crashed.uow, run.ID), "node_output")
	if downstream.Status != domain.NodeRunReady {
		t.Fatalf("downstream node run status = %s, want the persisted READY work", downstream.Status)
	}

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	generated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "must never be produced"})
	agentRunOnceUntil(t, restarted, run.ID, domain.RunCompleted)
	eventsAfterRecovery := len(listEvents(restarted.ctx, t, restarted.uow, run.ID))
	agentRunOnce(restarted, agentReconciler(restarted))

	if got := generated.Load(); got != 0 {
		t.Errorf("model calls in the restarted process = %d, want 0", got)
	}
	if got := len(listEvents(restarted.ctx, t, restarted.uow, run.ID)); got != eventsAfterRecovery {
		t.Errorf("events = %d after a further pass, want %d: the downstream node ran once", got, eventsAfterRecovery)
	}
	executed := execNodeRunByNodeID(t, listNodeRuns(restarted.ctx, t, restarted.uow, run.ID), "node_output")
	if got := agentAttemptCount(restarted.ctx, t, restarted.uow, executed.ID); got != 1 {
		t.Errorf("downstream node attempts = %d, want exactly 1", got)
	}
	types := agentEventTypesFor(listEvents(restarted.ctx, t, restarted.uow, run.ID), executed.ID)
	if got := agentCountEventType(types, domain.EventNodeCompleted); got != 1 {
		t.Errorf("downstream NODE_COMPLETED events = %d, want 1: %v", got, types)
	}
	if got := agentPortText(t, executed.Output); got != agentObjectCanonicalText {
		t.Errorf("downstream text = %s, want %s", got, agentObjectCanonicalText)
	}
	after := agentNodeRun(restarted.ctx, t, restarted.uow, run.ID)
	if string(after.Output) != string(committed.Output) {
		t.Errorf("agent node run output changed from %s to %s", committed.Output, after.Output)
	}
}

// TestAgentFinal_ObjectFormReadyToRunningRace_OnlyOneCompletionWins releases immediate
// advancement and the Reconciler onto the same READY object-form Final Action together.
// The conditional READY -> RUNNING claim elects one completer; the loser writes nothing,
// and the one committed text is the canonical text.
func TestAgentFinal_ObjectFormReadyToRunningRace_OnlyOneCompletionWins(t *testing.T) {
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptFinal(h, mockmodel.Scenario{FinalOutput: json.RawMessage(agentObjectOutput)})
	def := h.saveDefinition(agentObjectDefinition("wf-agent-final-object-race"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentReadyFinalAction(h, stop, run.ID)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, source := range []domain.ClaimSource{domain.ClaimImmediate, domain.ClaimReconciler} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- h.svc.CompleteAgentFinal(h.ctx, action.ID, source)
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

	if completed := getAgentAction(h.ctx, t, h.uow, action.ID); completed.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED", completed.Status)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentHasContextVersion(h.ctx, t, h.uow, agentRun.ID, 2) {
		t.Errorf("the loser committed a second Context Version")
	}
	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if got := agentPortText(t, nodeRun.Output); got != agentObjectCanonicalText {
		t.Errorf("text port = %s, want %s", got, agentObjectCanonicalText)
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
	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	downstream := 0
	for _, nr := range nodeRuns {
		if nr.NodeID == "node_output" {
			downstream++
		}
	}
	if downstream != 1 {
		t.Errorf("downstream node runs = %d, want 1", downstream)
	}
}
