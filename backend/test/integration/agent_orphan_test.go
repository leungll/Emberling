//go:build integration

// Orphaned synchronous Tool call tests: a process dies while a synchronous Tool call is in
// flight, leaving its Attempt STARTED. A restarted process -- a new harness over the same
// Pool and clock, with a new runner identity -- hands that Attempt to the orphan use case,
// which never calls the Tool again: in one transaction it fails the Attempt and Action
// with what is known about the call, gives the Agent a bounded tool message and either
// creates the next READY Turn or ends the Agent Run, exactly once, and only while the
// Agent deadline has not passed.
//
// The crash is a panic inside the Tool's Executor, after the claim committed and before
// any result transaction; races use explicit barriers. They share the agentHarness of
// agent_loop_test.go and the generation fixtures of agent_generation_limit_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// orphanCrash is what a crashed Tool call left behind: the Run, its Agent NodeRun claim,
// the Agent Run and the RUNNING Action with its single STARTED Attempt.
type orphanCrash struct {
	run        domain.Run
	outcome    service.AdvanceOutcome
	agentRunID string
	action     domain.AgentAction
	attempt    domain.ToolAttempt
}

// panicLookup is a lookup Executor that crashes the calling goroutine.
type panicLookup struct{}

func (panicLookup) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	panic("simulated crash while the lookup call is in flight")
}

// crashDuringExecute drives the claimed Agent NodeRun until the crash the Executor stages
// and then reads what the claim of model call turnNo committed.
func crashDuringExecute(t *testing.T, h *agentHarness, run domain.Run, outcome service.AdvanceOutcome, turnNo int) orphanCrash {
	t.Helper()
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		h.execute(outcome)
	}()
	agentRunID := factAgentRunID(t, h, outcome)
	action := generationActionOfTurn(t, h, agentRunID, turnNo)
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if action.Status != domain.AgentActionRunning || len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Fatalf("after the crash: action %s with attempts %+v, want RUNNING with one STARTED attempt", action.Status, attempts)
	}
	if attempts[0].RunnerID == nil || *attempts[0].RunnerID != h.runnerID {
		t.Fatalf("crashed attempt runner = %v, want %s", attempts[0].RunnerID, h.runnerID)
	}
	return orphanCrash{run: run, outcome: outcome, agentRunID: agentRunID, action: action, attempt: attempts[0]}
}

// crashGeneration runs generate_image calls on a crashing Backend and crashes inside call
// number crashAt, before its Provider request.
func crashGeneration(t *testing.T, p *generationProvider, workflowID string, limit *int, crashAt int32, calls ...mockmodel.Scenario) (*agentHarness, orphanCrash) {
	t.Helper()
	crashed, executor := newGenerationHarness(t, p, nil)
	run := startGenerationRun(crashed, workflowID, limit, calls...)
	executor.during = func(context.Context, registry.ToolAction) {
		if executor.calls.Load() == crashAt {
			panic("simulated crash after the generation claim committed, before the Provider request")
		}
	}
	outcome := crashed.claimAgentNode(run.ID)
	return crashed, crashDuringExecute(t, crashed, run, outcome, int(crashAt))
}

// crashLookup runs the lookup fixture loop with maxTurns and crashes inside its one call.
func crashLookup(t *testing.T, workflowID string, maxTurns int) (*agentHarness, orphanCrash) {
	t.Helper()
	crashed := newAgentHarness(t, agentHarnessOptions{LookupExecutor: panicLookup{}})
	agentScriptToolCallThenFinal(crashed, agentToolCallScenario())
	def := crashed.saveDefinition(agentLoopDefinitionMaxTurns(workflowID, maxTurns))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := crashed.claimAgentNode(run.ID)
	return crashed, crashDuringExecute(t, crashed, run, outcome, 1)
}

// handleOrphan calls the orphan use case on h and fails the test on an error.
func handleOrphan(t *testing.T, h *agentHarness, attemptID string) {
	t.Helper()
	if err := h.svc.HandleOrphanToolAttempt(h.ctx, attemptID); err != nil {
		t.Fatalf("handle orphan tool attempt %s: %v", attemptID, err)
	}
}

// getToolAttempt reads one Tool Attempt.
func getToolAttempt(t *testing.T, h *agentHarness, attemptID string) domain.ToolAttempt {
	t.Helper()
	var attempt domain.ToolAttempt
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempt, err = tx.ToolAttempts().Get(ctx, attemptID)
		return err
	}); err != nil {
		t.Fatalf("get tool attempt %s: %v", attemptID, err)
	}
	return attempt
}

// assertOrphanFailed proves the Attempt and its Action failed with code, the Attempt
// carries the uncertain determination, and exactly one AGENT_ACTION_FAILED names the
// Action with failureSource RUNNER_LOST and the same code.
func assertOrphanFailed(t *testing.T, h *agentHarness, c orphanCrash, code string, uncertain bool) {
	t.Helper()
	attempt := getToolAttempt(t, h, c.attempt.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != code || attempt.CompletedAt == nil {
		t.Errorf("attempt = status %s error %+v, want FAILED with %s", attempt.Status, attempt.Error, code)
	}
	if attempt.Uncertain == nil || *attempt.Uncertain != uncertain {
		t.Errorf("attempt uncertain = %v, want %v", attempt.Uncertain, uncertain)
	}
	if len(attempt.Result) != 0 {
		t.Errorf("attempt result = %s, want none", attempt.Result)
	}
	action := getAgentAction(h.ctx, t, h.uow, c.action.ID)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != code {
		t.Errorf("action = status %s error %+v, want FAILED with %s", action.Status, action.Error, code)
	}
	events := listEvents(h.ctx, t, h.uow, c.run.ID)
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", c.action.ID)
	if failed["failureSource"] != string(domain.FailureRunnerLost) || failed["toolAttemptId"] != c.attempt.ID {
		t.Errorf("AGENT_ACTION_FAILED = %v, want failureSource RUNNER_LOST naming %s", failed, c.attempt.ID)
	}
	if errorObject, _ := failed["error"].(map[string]any); errorObject["code"] != code {
		t.Errorf("AGENT_ACTION_FAILED error = %v, want code %s", failed["error"], code)
	}
}

// orphanToolMessageOf returns the tool message the orphaned round appended, after
// checking the Context Version holds the committed Decision before it.
func orphanToolMessageOf(t *testing.T, h *agentHarness, c orphanCrash, version int) map[string]any {
	t.Helper()
	messages := agentContextMessages(t, agentContextVersion(h.ctx, t, h.uow, c.agentRunID, version).Messages)
	if len(messages) < 2 {
		t.Fatalf("context version %d has %d messages, want the Decision and the tool message", version, len(messages))
	}
	decision, tool := messages[len(messages)-2], messages[len(messages)-1]
	if decision.Role != runtime.AgentRoleAssistant || tool.Role != runtime.AgentRoleTool {
		t.Fatalf("last two messages = %s, %s, want assistant then tool", decision.Role, tool.Role)
	}
	if tool.ToolActionID == nil || *tool.ToolActionID != c.action.ID || tool.ToolName == nil || *tool.ToolName != c.attempt.ToolName {
		t.Errorf("tool message links %v/%v, want action %s and tool %s", tool.ToolActionID, tool.ToolName, c.action.ID, c.attempt.ToolName)
	}
	if len(tool.Content) > 1024 {
		t.Errorf("tool message is %d bytes, want at most 1024", len(tool.Content))
	}
	if strings.Contains(string(tool.Content), string(c.attempt.Input)) {
		t.Errorf("tool message %s repeats the call's arguments", tool.Content)
	}
	var content map[string]any
	if err := json.Unmarshal(tool.Content, &content); err != nil {
		t.Fatalf("decode tool message %s: %v", tool.Content, err)
	}
	return content
}

// readyTurnNos lists the turn numbers of the Agent Run's READY Turns.
func readyTurnNos(t *testing.T, h *agentHarness, agentRunID string) []int {
	t.Helper()
	var out []int
	for _, turn := range listReadyTurns(h.ctx, t, h.uow) {
		if turn.AgentRunID == agentRunID {
			out = append(out, turn.TurnNo)
		}
	}
	return out
}

// agentRunByID reads the Agent Run of a crash.
func agentRunByID(t *testing.T, h *agentHarness, c orphanCrash) domain.AgentRun {
	t.Helper()
	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, c.outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run of node run %s disappeared", c.outcome.NodeRunID)
	}
	return agentRun
}

// assertAgentAnswered proves the Agent's next Decision after the orphaned round finished
// the Agent Run with an answer.
func assertAgentAnswered(t *testing.T, h *agentHarness, c orphanCrash) {
	t.Helper()
	agentRun := agentRunByID(t, h, c)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent run termination = %s, want FINAL_RESPONSE from the next Decision", agentTermination(agentRun))
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAgentOrphan_GenerateImageUncertain_CreatesExactlyOneReadyTurn(t *testing.T) {
	p := newGenerationProvider(t)
	crashed, c := crashGeneration(t, p, "wf-orphan-uncertain", generationLimit(3), 1, generationCall("photo-a"))
	before := agentRunByID(t, crashed, c)

	restarted, executor := newGenerationHarnessWith(t, p, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	handleOrphan(t, restarted, c.attempt.ID)

	assertOrphanFailed(t, restarted, c, "RESULT_UNCERTAIN", true)
	if got := readyTurnNos(t, restarted, c.agentRunID); len(got) != 1 || got[0] != 2 {
		t.Fatalf("READY turns = %v, want exactly Turn 2", got)
	}
	after := agentRunByID(t, restarted, c)
	if after.Termination != nil || after.CurrentTurnNo != 2 ||
		after.CurrentContextVersion != before.CurrentContextVersion+1 || after.CurrentStateVersion != before.CurrentStateVersion {
		t.Errorf("agent run = %+v, want unterminated at Turn 2 with one more Context Version and the same State Version", after)
	}
	content := orphanToolMessageOf(t, restarted, c, after.CurrentContextVersion)
	if content["status"] != "RESULT_UNCERTAIN" {
		t.Errorf("tool message status = %v, want RESULT_UNCERTAIN", content["status"])
	}
	message, _ := content["message"].(string)
	if !strings.Contains(message, "outcome is unknown") || !strings.Contains(message, "1 of 3 calls used") {
		t.Errorf("tool message = %q, want the unknown outcome and 1 of 3 calls used", message)
	}
	if calls, _ := content["generationCalls"].(map[string]any); calls["used"] != float64(1) || calls["limit"] != float64(3) {
		t.Errorf("generationCalls = %v, want used 1 limit 3", content["generationCalls"])
	}

	events := listEvents(restarted.ctx, t, restarted.uow, c.run.ID)
	turn2 := agentTurnByNo(restarted.ctx, t, restarted.uow, c.agentRunID, 2)
	agentOnlyPayloadFor(t, events, domain.EventAgentTurnReady, "turnId", turn2.ID)
	if updated := agentPayloadsFor(t, events, domain.EventAgentStateUpdated, "turnId", c.action.TurnID); len(updated) != 0 {
		t.Errorf("AGENT_STATE_UPDATED for the orphaned round = %v, want none", updated)
	}
	if got := agentRunRow(restarted.ctx, t, restarted.uow, c.run.ID).Status; got != domain.RunRunning {
		t.Errorf("run status = %s, want RUNNING", got)
	}

	// A second handling of the same Attempt writes nothing.
	handleOrphan(t, restarted, c.attempt.ID)
	if again := listEvents(restarted.ctx, t, restarted.uow, c.run.ID); len(again) != len(events) {
		t.Errorf("events after a second handling = %d, want still %d", len(again), len(events))
	}
	if got := readyTurnNos(t, restarted, c.agentRunID); len(got) != 1 {
		t.Errorf("READY turns after a second handling = %v, want still one", got)
	}

	// The committed Turn was offered to the queue; the Agent decides from there.
	if err := restarted.drainTurns(restarted.ctx); err != nil {
		t.Fatalf("drain the next Turn: %v", err)
	}
	assertAgentAnswered(t, restarted, c)
	if got := executor.calls.Load(); got != 0 {
		t.Errorf("generate_image executor calls after the restart = %d, want 0", got)
	}
	if got := p.generations.Load(); got != 0 {
		t.Errorf("mock provider generations = %d, want 0: the crashed call never reached it and nothing resent it", got)
	}
}

func TestAgentOrphan_ConcurrentHandlers_ExactlyOneCommits(t *testing.T) {
	p := newGenerationProvider(t)
	crashed, c := crashGeneration(t, p, "wf-orphan-concurrent", nil, 1, generationCall("photo-a"))
	restarted, _ := newGenerationHarnessWith(t, p, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- restarted.svc.HandleOrphanToolAttempt(restarted.ctx, c.attempt.ID)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent handling: %v", err)
		}
	}

	assertOrphanFailed(t, restarted, c, "RESULT_UNCERTAIN", true)
	if got := readyTurnNos(t, restarted, c.agentRunID); len(got) != 1 || got[0] != 2 {
		t.Errorf("READY turns = %v, want exactly Turn 2", got)
	}
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, c.agentRunID, 3) {
		t.Error("a Turn 3 exists, want only the one next Turn")
	}
	events := listEvents(restarted.ctx, t, restarted.uow, c.run.ID)
	if got := agentCountEventType(agentEventTypesFor(events, c.outcome.NodeRunID), domain.EventAgentTurnReady); got != 2 {
		t.Errorf("AGENT_TURN_READY events = %d, want 2 (Turn 1 and the one next Turn)", got)
	}
}

func TestAgentOrphan_DeadlinePassed_TimeoutWinsOrphanWritesNothing(t *testing.T) {
	p := newGenerationProvider(t)
	crashed, c := crashGeneration(t, p, "wf-orphan-deadline", nil, 1, generationCall("photo-a"))
	restarted, _ := newGenerationHarnessWith(t, p, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	eventsBefore := listEvents(restarted.ctx, t, restarted.uow, c.run.ID)

	handleOrphan(t, restarted, c.attempt.ID)
	if attempt := getToolAttempt(t, restarted, c.attempt.ID); attempt.Status != domain.ToolAttemptStarted || attempt.Uncertain != nil {
		t.Fatalf("attempt after the handling past the deadline = %+v, want untouched STARTED", attempt)
	}
	if got := listEvents(restarted.ctx, t, restarted.uow, c.run.ID); len(got) != len(eventsBefore) {
		t.Fatalf("events = %d, want still %d: the orphan handling must write nothing", len(got), len(eventsBefore))
	}

	if err := restarted.svc.TimeoutAgentRun(restarted.ctx, c.agentRunID); err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}
	attempt := getToolAttempt(t, restarted, c.attempt.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "TIMEOUT" || attempt.Uncertain != nil {
		t.Errorf("attempt = %+v, want FAILED by the timeout without an uncertain flag", attempt)
	}
	if agentRun := agentRunByID(t, restarted, c); agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	failed := agentOnlyPayloadFor(t, listEvents(restarted.ctx, t, restarted.uow, c.run.ID), domain.EventAgentActionFailed, "actionId", c.action.ID)
	if failed["failureSource"] != string(domain.FailureTimeout) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want TIMEOUT", failed["failureSource"])
	}
}

func TestAgentOrphan_RaceWithTimeoutAgentRun_OneWinner(t *testing.T) {
	p := newGenerationProvider(t)
	crashed, c := crashGeneration(t, p, "wf-orphan-timeout-race", nil, 1, generationCall("photo-a"))
	// The restarted Backend's queue refuses, so nothing advances the next Turn while the
	// two use cases race over it.
	restarted, _ := newGenerationHarnessWith(t, p, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock, Queue: service.NoopEnqueuer{}})

	race := func() {
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			errs <- restarted.svc.HandleOrphanToolAttempt(restarted.ctx, c.attempt.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			errs <- restarted.svc.TimeoutAgentRun(restarted.ctx, c.agentRunID)
		}()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("race: %v", err)
			}
		}
	}

	// Inside the deadline the timeout writes nothing, so the orphan handling wins.
	race()
	assertOrphanFailed(t, restarted, c, "RESULT_UNCERTAIN", true)
	if agentRun := agentRunByID(t, restarted, c); agentRun.Termination != nil {
		t.Fatalf("agent run termination = %s, want none inside the deadline", agentTermination(agentRun))
	}

	// Past the deadline the orphan is already resolved, so only the timeout acts, on the
	// new current Turn.
	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	race()
	assertOrphanFailed(t, restarted, c, "RESULT_UNCERTAIN", true)
	agentRun := agentRunByID(t, restarted, c)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	if turn2 := agentTurnByNo(restarted.ctx, t, restarted.uow, c.agentRunID, 2); turn2.Status != domain.AgentTurnFailed {
		t.Errorf("Turn 2 = %s, want FAILED by the timeout", turn2.Status)
	}
	events := listEvents(restarted.ctx, t, restarted.uow, c.run.ID)
	if got := agentCountEventType(agentEventTypesFor(events, c.outcome.NodeRunID), domain.EventAgentFailed); got != 1 {
		t.Errorf("AGENT_FAILED events = %d, want 1", got)
	}
}

func TestAgentOrphan_EventInsertFails_RollsBackEverything(t *testing.T) {
	p := newGenerationProvider(t)
	crashed, c := crashGeneration(t, p, "wf-orphan-rollback", nil, 1, generationCall("photo-a"))
	restarted, _ := newGenerationHarnessWith(t, p, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	before := agentRunByID(t, restarted, c)

	const collidingID = "evt_orphan_collision"
	execAppendFixedEvent(restarted.ctx, t, restarted.uow, c.run.ID, collidingID, restarted.clock.Now())
	eventsBefore := listEvents(restarted.ctx, t, restarted.uow, c.run.ID)
	restarted.ids.ForceNextEventID(collidingID)

	err := restarted.svc.HandleOrphanToolAttempt(restarted.ctx, c.attempt.ID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("handling with a forced Event ID collision: err = %v, want wrapped domain.ErrConflict", err)
	}
	if attempt := getToolAttempt(t, restarted, c.attempt.ID); attempt.Status != domain.ToolAttemptStarted || attempt.Uncertain != nil || attempt.Error != nil {
		t.Errorf("attempt after rollback = %+v, want untouched STARTED", attempt)
	}
	if action := getAgentAction(restarted.ctx, t, restarted.uow, c.action.ID); action.Status != domain.AgentActionRunning {
		t.Errorf("action after rollback = %s, want RUNNING", action.Status)
	}
	if after := agentRunByID(t, restarted, c); after.CurrentTurnNo != before.CurrentTurnNo || after.CurrentContextVersion != before.CurrentContextVersion {
		t.Errorf("agent run pointers after rollback = turn %d context %d, want unchanged", after.CurrentTurnNo, after.CurrentContextVersion)
	}
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, c.agentRunID, 2) {
		t.Error("a Turn 2 exists after rollback")
	}
	if got := listEvents(restarted.ctx, t, restarted.uow, c.run.ID); len(got) != len(eventsBefore) {
		t.Errorf("events after rollback = %d, want still %d", len(got), len(eventsBefore))
	}

	// The Attempt is still an orphan, and the next handling commits once.
	handleOrphan(t, restarted, c.attempt.ID)
	assertOrphanFailed(t, restarted, c, "RESULT_UNCERTAIN", true)
	if got := readyTurnNos(t, restarted, c.agentRunID); len(got) != 1 || got[0] != 2 {
		t.Errorf("READY turns = %v, want exactly Turn 2", got)
	}
}

func TestAgentOrphan_UncertainAttemptCounted_NextGenerationAtLimitRejected(t *testing.T) {
	p := newGenerationProvider(t)
	crashed, c := crashGeneration(t, p, "wf-orphan-limit", generationLimit(2), 2,
		generationCall("photo-a"), generationCall("photo-b"))
	if first := generationActionOfTurn(t, crashed, c.agentRunID, 1); first.Status != domain.AgentActionSucceeded {
		t.Fatalf("first generation = %s, want SUCCEEDED before the crash", first.Status)
	}

	restarted, executor := newGenerationHarnessWith(t, p, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	// After the uncertain result the Agent decides to generate again.
	factScriptToolCalls(restarted, generationCall("photo-c"))
	handleOrphan(t, restarted, c.attempt.ID)
	assertOrphanFailed(t, restarted, c, "RESULT_UNCERTAIN", true)
	content := orphanToolMessageOf(t, restarted, c, agentRunByID(t, restarted, c).CurrentContextVersion)
	if message, _ := content["message"].(string); !strings.Contains(message, "2 of 2 calls used") {
		t.Errorf("tool message = %q, want 2 of 2 calls used", message)
	}

	if err := restarted.drainTurns(restarted.ctx); err != nil {
		t.Fatalf("drain the next Turn: %v", err)
	}
	assertGenerationLimitRejected(t, restarted, c.run.ID, generationActionOfTurn(t, restarted, c.agentRunID, 3), 2, 2)
	assertAgentInvalidAction(t, restarted, c.run.ID, c.outcome)
	if got := countAgentRunAttempts(t, restarted, c.agentRunID, generateimage.ToolName); got != 2 {
		t.Errorf("generation calls used = %d, want 2", got)
	}
	if got := executor.calls.Load(); got != 0 {
		t.Errorf("generate_image executor calls after the restart = %d, want 0", got)
	}
	if got := p.generations.Load(); got != 1 {
		t.Errorf("mock provider generations = %d, want only the first", got)
	}
}

func TestAgentOrphan_CrashDuringImageCall_ProviderReceivesOneRequest(t *testing.T) {
	upstream := newRealImageUpstream(t)
	upstream.holdImages(t)
	wiring := newRealImageWiring(t, upstream, &http.Client{Transport: crashingTransport{held: upstream.held, crashed: &atomic.Bool{}}})
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, wiring.options(agentHarnessOptions{Notifier: stop}))
	run, outcome, action := readyRealImageGeneration(t, h, stop, "wf-orphan-real-image")

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		_ = h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate)
	}()
	assertAttemptStillStarted(t, h, action.ID)
	attempt := agentToolAttempts(h.ctx, t, h.uow, action.ID)[0]
	c := orphanCrash{run: run, outcome: outcome, agentRunID: factAgentRunID(t, h, outcome), action: action, attempt: attempt}

	// The Provider stops holding, so a resent request would be answered at once and show
	// in its record.
	upstream.control(t, "/control/release", "")
	restarted := newAgentHarness(t, wiring.options(agentHarnessOptions{Pool: h.pool, Clock: h.clock}))
	callsBefore := wiring.executor.calls.Load()
	handleOrphan(t, restarted, attempt.ID)
	assertOrphanFailed(t, restarted, c, "RESULT_UNCERTAIN", true)
	if err := restarted.drainTurns(restarted.ctx); err != nil {
		t.Fatalf("drain the next Turn: %v", err)
	}
	assertAgentAnswered(t, restarted, c)

	if got := wiring.executor.calls.Load(); got != callsBefore {
		t.Errorf("generate_image executor calls after the restart = %d, want still %d", got, callsBefore)
	}
	if record := upstream.imagesRecord(t); record.arrived != 1 {
		t.Errorf("Images API requests = %d, want exactly 1: the restart must not resend\n%s", record.arrived, record.raw)
	}
}

// reviewAssetRef finds the generated asset reference in a Tool result message.
var reviewAssetRef = regexp.MustCompile(`"assetRef"\s*:\s*"(artifact_[0-9a-f]{32})"`)

// newReviewHarness is a Backend whose Tool Registry holds lookup, the counting
// generate_image against p and review_asset with the given Executor, or its registered one
// when review is nil.
func newReviewHarness(t *testing.T, p *generationProvider, opts agentHarnessOptions, review registry.ToolExecutor) *agentHarness {
	t.Helper()
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	generation := generateimage.Registration(nil, nil, artifacts)
	generation.Executor = mockImageExecutor(p.url, artifacts)
	reviewRegistration := reviewasset.Registration()
	if review != nil {
		reviewRegistration.Executor = review
	}
	opts.Tools = []registry.ToolRegistration{generation, reviewRegistration}
	return newAgentHarness(t, opts)
}

// panicReview is a review_asset Executor that crashes the calling goroutine.
type panicReview struct{}

func (panicReview) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	panic("simulated crash while the review_asset call is in flight")
}

func TestAgentOrphan_ReviewAssetInterrupted_ContinuesGenerationCountUnchanged(t *testing.T) {
	p := newGenerationProvider(t)
	crashed := newReviewHarness(t, p, agentHarnessOptions{}, panicReview{})

	var calls atomic.Int32
	crashed.provider.Script = func(request registry.ModelRequest) *mockmodel.Scenario {
		switch calls.Add(1) {
		case 1:
			call := generationCall("photo-a")
			call.Kind = mockmodel.ScenarioToolCall
			return &call
		case 2:
			assetRef := ""
			for i := len(request.Messages) - 1; i >= 0; i-- {
				if request.Messages[i].Role == "tool" {
					if m := reviewAssetRef.FindStringSubmatch(string(request.Messages[i].Content)); m != nil {
						assetRef = m[1]
					}
					break
				}
			}
			return &mockmodel.Scenario{
				Kind:          mockmodel.ScenarioToolCall,
				ToolName:      reviewasset.ToolName,
				ToolArguments: json.RawMessage(`{"assetRef":"` + assetRef + `","photoAssetId":"photo-a","mock":"pass"}`),
			}
		default:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
	}
	def := generationDefinition("wf-orphan-review", nil)
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+generateimage.ToolName+`","`+lookup.ToolName+`"]`,
				`["`+generateimage.ToolName+`","`+lookup.ToolName+`","`+reviewasset.ToolName+`"]`, 1))
		}
	}
	def = crashed.saveDefinition(def)
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := crashed.claimAgentNode(run.ID)
	c := crashDuringExecute(t, crashed, run, outcome, 2)
	if c.attempt.ToolName != reviewasset.ToolName {
		t.Fatalf("crashed attempt is %s, want %s", c.attempt.ToolName, reviewasset.ToolName)
	}
	generationsBefore := countAgentRunAttempts(t, crashed, c.agentRunID, generateimage.ToolName)
	if generationsBefore != 1 {
		t.Fatalf("generation calls before the restart = %d, want 1", generationsBefore)
	}

	restarted := newReviewHarness(t, p, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock}, nil)
	handleOrphan(t, restarted, c.attempt.ID)
	assertOrphanFailed(t, restarted, c, "EXECUTION_INTERRUPTED", false)
	content := orphanToolMessageOf(t, restarted, c, agentRunByID(t, restarted, c).CurrentContextVersion)
	message, _ := content["message"].(string)
	if content["status"] != "EXECUTION_INTERRUPTED" || !strings.Contains(message, "Calling the tool again is safe.") ||
		!strings.Contains(message, "It does not count toward the generation limit.") {
		t.Errorf("tool message = %v, want the interrupted, safe-to-repeat, uncounted message", content)
	}
	if _, present := content["generationCalls"]; present {
		t.Errorf("tool message generationCalls = %v, want it omitted", content["generationCalls"])
	}
	if got := readyTurnNos(t, restarted, c.agentRunID); len(got) != 1 || got[0] != 3 {
		t.Fatalf("READY turns = %v, want exactly Turn 3", got)
	}
	if err := restarted.drainTurns(restarted.ctx); err != nil {
		t.Fatalf("drain the next Turn: %v", err)
	}
	assertAgentAnswered(t, restarted, c)
	if got := countAgentRunAttempts(t, restarted, c.agentRunID, generateimage.ToolName); got != generationsBefore {
		t.Errorf("generation calls after the restart = %d, want still %d", got, generationsBefore)
	}
}

func TestAgentOrphan_AtMaxTurns_EndsAsMaxTurns(t *testing.T) {
	crashed, c := crashLookup(t, "wf-orphan-max-turns", 1)
	before := agentRunByID(t, crashed, c)

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	handleOrphan(t, restarted, c.attempt.ID)

	assertOrphanFailed(t, restarted, c, "EXECUTION_INTERRUPTED", false)
	after := agentRunByID(t, restarted, c)
	if after.Termination == nil || *after.Termination != domain.TerminationMaxTurns {
		t.Fatalf("agent run termination = %s, want MAX_TURNS", agentTermination(after))
	}
	if after.CurrentContextVersion != before.CurrentContextVersion+1 || after.CurrentTurnNo != 1 {
		t.Errorf("agent run pointers = turn %d context %d, want turn 1 and one more Context Version", after.CurrentTurnNo, after.CurrentContextVersion)
	}
	orphanToolMessageOf(t, restarted, c, after.CurrentContextVersion)
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, c.agentRunID, 2) {
		t.Error("a Turn 2 exists past the round limit")
	}
	if got := agentNodeRun(restarted.ctx, t, restarted.uow, c.run.ID).Status; got != domain.NodeRunFailed {
		t.Errorf("agent node run = %s, want FAILED", got)
	}
}

func TestAgentOrphan_KeyedOrUnregisteredTool_EndsAsToolError(t *testing.T) {
	keyed := lookup.Registration()
	keyed.Metadata.SideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}
	for _, tc := range []struct {
		name  string
		tools []registry.ToolRegistration
	}{
		{name: "no longer registered"},
		{name: "external keyed", tools: []registry.ToolRegistration{keyed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crashed, c := crashLookup(t, "wf-orphan-terminal", agentMaxTurns)
			restarted := newAgentHarness(t, agentHarnessOptions{
				Pool: crashed.pool, Clock: crashed.clock, SkipLookupTool: true, Tools: tc.tools,
			})
			handleOrphan(t, restarted, c.attempt.ID)

			assertOrphanFailed(t, restarted, c, "RESULT_UNCERTAIN", true)
			agentRun := agentRunByID(t, restarted, c)
			if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationToolError {
				t.Errorf("agent run termination = %s, want TOOL_ERROR", agentTermination(agentRun))
			}
			if agentTurnNoExists(restarted.ctx, t, restarted.uow, c.agentRunID, 2) {
				t.Error("a Turn 2 exists, want the Agent Run ended")
			}
			if got := agentRunRow(restarted.ctx, t, restarted.uow, c.run.ID).Status; got != domain.RunFailed {
				t.Errorf("run status = %s, want FAILED", got)
			}
		})
	}
}
