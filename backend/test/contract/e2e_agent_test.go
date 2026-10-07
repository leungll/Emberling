//go:build integration

// Agent Loop acceptance at the HTTP level (the two-round loop and restart recovery): one
// Run of the agent_lookup fixture drives a TOOL_CALL round and a FINAL round through the
// real work Pool, and two restart scenarios prove that what a stopped process left
// committed is carried on by a second Backend's Reconciler alone.
//
// Everything is observed through the public HTTP surface -- the Snapshot, the Agent Trace
// projection and an SSE replay from seq 0 -- never by calling a service method or reading
// a row. The recovery boundary itself is produced by an injected hook rather than a sleep:
// a service.EventNotifier cancels the work Pool's context at the first commit whose
// persisted facts satisfy the scenario's predicate, which is exactly the crash-after-COMMIT
// shape the persisted-work recovery rule makes recoverable.
package contract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/api"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

// agentE2EWait bounds every wait in this file. It is a safety net against a hung binary,
// never a synchronization device: each wait ends on an event (a stopper barrier, a
// terminal Run status, an SSE frame), not on the clock.
const agentE2EWait = 30 * time.Second

// agentFixtureNodeID is the agent_lookup fixture's Agent node. The Agent NodeRun is
// resolved by it rather than by Node Type, because the Node Type string is an
// implementation detail of internal/nodes/agent that this package does not import.
const agentFixtureNodeID = "node_agent"

// agentE2EToolKey and agentE2EFinalOutput are the scripted TOOL_CALL argument and the
// scripted FINAL answer of the Runs in this file.
const (
	agentE2EToolKey     = "ember-e2e"
	agentE2EFinalOutput = "the agent's final answer"
)

// ---------------------------------------------------------------------------------------
// Stopping one Backend's Agent Loop at a chosen committed boundary
// ---------------------------------------------------------------------------------------

// agentLoopStopper stops one Backend's in-process Agent Loop at the first committed
// boundary whose persisted facts satisfy stopWhen.
//
// It is a service.EventNotifier because that is the only hook the service exposes between
// a committed transaction and the advancement it chains next (the same technique
// test/integration/agent_reconcile_test.go uses). The predicate reads committed rows
// rather than counting notifications, so the boundary is named by the state it leaves
// behind -- "Turn 2 is READY and nobody is advancing it" -- instead of by how many
// transactions happened to get there.
//
// It forwards every notification to the real *api.Hub first, so an SSE client attached to
// the stopped Backend still observes everything that Backend committed.
type agentLoopStopper struct {
	hub *api.Hub

	mu       sync.Mutex
	uow      store.UnitOfWork
	cancel   context.CancelFunc
	stopWhen func(context.Context, store.Tx, string) (bool, error)

	once    sync.Once
	stopped chan struct{}
	failure atomic.Value // error, from a predicate that could not read
}

func newAgentLoopStopper() *agentLoopStopper {
	return &agentLoopStopper{stopped: make(chan struct{})}
}

// notifier is the testEnvOptions.WrapServiceNotifier hook: it binds the Hub the API layer
// subscribes against and returns this stopper as the service's notifier.
func (s *agentLoopStopper) notifier(hub *api.Hub) service.EventNotifier {
	s.hub = hub
	return s
}

// arm binds the stopper to a wired Backend. It must be called before the Run whose loop is
// to be stopped is created, which is also what publishes these fields safely: the HTTP
// request that creates the Run happens after arm returns.
func (s *agentLoopStopper) arm(env *testEnv, stopWhen func(context.Context, store.Tx, string) (bool, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uow, s.cancel, s.stopWhen = env.uow, env.cancelWorkers, stopWhen
}

func (s *agentLoopStopper) EventsCommitted(runID string, lastSeq int64) {
	s.hub.EventsCommitted(runID, lastSeq)

	s.mu.Lock()
	uow, cancel, stopWhen := s.uow, s.cancel, s.stopWhen
	s.mu.Unlock()
	if uow == nil || stopWhen == nil {
		return
	}

	var reached bool
	// Deliberately not the worker's context: the predicate must still be able to read
	// after a previous notification cancelled the Pool.
	err := uow.WithinReadTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		reached, err = stopWhen(ctx, tx, runID)
		return err
	})
	if err != nil {
		s.failure.Store(err)
		return
	}
	if !reached {
		return
	}
	s.once.Do(func() {
		close(s.stopped)
		cancel()
	})
}

// waitStopped blocks until the Agent Loop has been stopped at the chosen boundary.
func (s *agentLoopStopper) waitStopped(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-s.stopped:
	case <-time.After(timeout):
		if err, ok := s.failure.Load().(error); ok {
			t.Fatalf("the Agent Loop never reached the chosen boundary within %s; the predicate failed to read: %v", timeout, err)
		}
		t.Fatalf("the Agent Loop never reached the chosen boundary within %s", timeout)
	}
}

// agentTurnsOfRun resolves runID's Agent Run and its Turns from committed rows alone. The
// second return value is false while the Agent Run's start transaction has not committed
// yet, which is every notification before AGENT_STARTED.
func agentTurnsOfRun(ctx context.Context, tx store.Tx, runID string) (domain.AgentRun, []domain.AgentTurn, bool, error) {
	nodeRuns, err := tx.NodeRuns().ListByRun(ctx, runID)
	if err != nil {
		return domain.AgentRun{}, nil, false, err
	}
	nodeRunID := ""
	for _, nr := range nodeRuns {
		if nr.NodeID == agentFixtureNodeID {
			nodeRunID = nr.ID
			break
		}
	}
	if nodeRunID == "" {
		return domain.AgentRun{}, nil, false, nil
	}
	agentRun, err := tx.AgentRuns().GetByNodeRunID(ctx, nodeRunID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.AgentRun{}, nil, false, nil
	}
	if err != nil {
		return domain.AgentRun{}, nil, false, err
	}
	turns, err := tx.AgentTurns().ListByAgentRunID(ctx, agentRun.ID)
	if err != nil {
		return domain.AgentRun{}, nil, false, err
	}
	return agentRun, turns, true, nil
}

// stopAtReadyTurn is the crash boundary "Tool result and the next READY Turn are
// committed, then the process dies": the named Turn exists, is READY, and the in-process
// chain that would have claimed it never runs.
func stopAtReadyTurn(turnNo int) func(context.Context, store.Tx, string) (bool, error) {
	return func(ctx context.Context, tx store.Tx, runID string) (bool, error) {
		_, turns, ok, err := agentTurnsOfRun(ctx, tx, runID)
		if err != nil || !ok {
			return false, err
		}
		for _, turn := range turns {
			if turn.TurnNo == turnNo && turn.Status == domain.AgentTurnReady {
				return true, nil
			}
		}
		return false, nil
	}
}

// stopAtReadyActionOfTurn is the boundary one committed Decision leaves behind: the Turn is
// COMPLETED, its single Action is READY, and nothing has executed it. A restarted Backend
// must execute that very Action, never regenerate the Decision.
func stopAtReadyActionOfTurn(turnNo int) func(context.Context, store.Tx, string) (bool, error) {
	return func(ctx context.Context, tx store.Tx, runID string) (bool, error) {
		_, turns, ok, err := agentTurnsOfRun(ctx, tx, runID)
		if err != nil || !ok {
			return false, err
		}
		for _, turn := range turns {
			if turn.TurnNo != turnNo {
				continue
			}
			action, err := tx.AgentActions().GetByTurnID(ctx, turn.ID)
			if errors.Is(err, domain.ErrNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return action.Status == domain.AgentActionReady, nil
		}
		return false, nil
	}
}

// ---------------------------------------------------------------------------------------
// Observing a Run through SSE
// ---------------------------------------------------------------------------------------

// streamedEvent is one Event as an SSE client receives it: the frame's id and event name
// alongside the decoded domain.Event the frame's data carries.
type streamedEvent struct {
	seq       int64
	typ       string
	nodeRunID string
	payload   map[string]any
}

// replayEvents opens GET /api/runs/{id}/events at afterSeq=0 and reads until the Run's
// terminal Event, returning every frame in the order the stream delivered it. PostgreSQL
// Event rows are the authority for SSE, so this replay is the full committed history of
// the Run -- including the Events a previous, now-stopped Backend wrote.
func (e *testEnv) replayEvents(t *testing.T, runID string, timeout time.Duration) []streamedEvent {
	t.Helper()
	resp, reader := e.openSSE(t, runID, 0, "")
	defer resp.Body.Close()

	var out []streamedEvent
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("the SSE replay of run %s did not reach a terminal Event within %s; got %v", runID, timeout, streamedTypes(out))
		}
		frame := waitForNextSSEFrame(t, reader, remaining, "replaying run "+runID+" from seq 0")

		var ev struct {
			NodeRunID *string         `json:"nodeRunId"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(frame.data), &ev); err != nil {
			t.Fatalf("decode SSE frame data for seq %d: %v; data=%s", frame.id, err, frame.data)
		}
		streamed := streamedEvent{seq: frame.id, typ: frame.event}
		if ev.NodeRunID != nil {
			streamed.nodeRunID = *ev.NodeRunID
		}
		if len(ev.Payload) > 0 {
			if err := json.Unmarshal(ev.Payload, &streamed.payload); err != nil {
				t.Fatalf("decode payload of SSE frame seq %d (%s): %v", frame.id, frame.event, err)
			}
		}
		out = append(out, streamed)

		if frame.event == "RUN_COMPLETED" || frame.event == "RUN_FAILED" {
			return out
		}
	}
}

func streamedTypes(events []streamedEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.typ)
	}
	return out
}

// assertAscendingSeq fails when the replay was not delivered in strictly ascending seq
// order starting at 1 -- the SSE ordering contract.
func assertAscendingSeq(t *testing.T, events []streamedEvent) {
	t.Helper()
	for i, ev := range events {
		if ev.seq != int64(i+1) {
			t.Fatalf("SSE replay event[%d] seq = %d, want %d (ordered seq replay): %v", i, ev.seq, i+1, streamedTypes(events))
		}
	}
}

// streamedOfType returns every replayed Event of one type.
func streamedOfType(events []streamedEvent, typ string) []streamedEvent {
	var out []streamedEvent
	for _, ev := range events {
		if ev.typ == typ {
			out = append(out, ev)
		}
	}
	return out
}

// agentEventSequence keeps only the Events of the Agent NodeRun (plus the Run's own
// terminal Event) whose type is one the scenario names, so the assertion is about the
// order and multiplicity of the Agent Loop's own Events rather than about every Event the
// three-node graph happens to produce.
func agentEventSequence(events []streamedEvent, agentNodeRunID string, want []string) []string {
	wanted := make(map[string]struct{}, len(want))
	for _, typ := range want {
		wanted[typ] = struct{}{}
	}
	var got []string
	for _, ev := range events {
		if _, ok := wanted[ev.typ]; !ok {
			continue
		}
		if ev.nodeRunID != "" && ev.nodeRunID != agentNodeRunID {
			continue
		}
		got = append(got, ev.typ)
	}
	return got
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// scriptFinalOnly makes a Backend's model answer FINAL on every Turn it is asked about,
// and counts the calls. A restarted Backend is scripted with it so that "the model was
// called exactly once in the second process" is a statement about that process alone.
func scriptFinalOnly(env *testEnv, output string) *atomic.Int32 {
	var calls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		calls.Add(1)
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: output}
	}
	return &calls
}

// agentNodeRunIDOf returns the Agent NodeRun's id from a Run Snapshot.
func (e *testEnv) agentNodeRunIDOf(t *testing.T, runID string) string {
	t.Helper()
	resp, body := e.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s status = %d, body=%s", runID, resp.StatusCode, body)
	}
	return nodeRunIDOf(t, decodeBody[map[string]any](t, body), agentFixtureNodeID)
}

// turnStatuses projects the Agent Trace response as turnNo -> status, which is how a
// restart test states the boundary it stopped at in terms of the public projection.
func turnStatuses(t *testing.T, body []byte) map[float64]string {
	t.Helper()
	trace := decodeBody[map[string]any](t, body)
	turns, _ := trace["turns"].([]any)
	out := make(map[float64]string, len(turns))
	for _, raw := range turns {
		turn, _ := raw.(map[string]any)
		no, _ := turn["turnNo"].(float64)
		status, _ := turn["status"].(string)
		out[no] = status
	}
	return out
}

// ---------------------------------------------------------------------------------------
// 1. The two-round Agent Loop, end to end
// ---------------------------------------------------------------------------------------

// TestE2E_AgentTwoRoundLoop_RunCompletesWithAgentEvents is the HTTP-level acceptance of the
// Agent Loop: one Run whose Agent decides a TOOL_CALL, executes the lookup Tool, decides
// FINAL on the next Turn and completes the Run, with the Agent Event vocabulary of
// the data model delivered over SSE in seq order, and the Agent's answer
// carried to the downstream text_output NodeRun.
func TestE2E_AgentTwoRoundLoop_RunCompletesWithAgentEvents(t *testing.T) {
	env := newTestEnv(t)
	calls := scriptToolCallThenFinal(env, agentE2EToolKey, agentE2EFinalOutput)
	workflowID, version := saveAgentFixture(t, env)

	runID := createAgentRun(t, env, workflowID, version, "what is the answer?")
	events := env.replayEvents(t, runID, agentE2EWait)
	assertAscendingSeq(t, events)

	snapshot := env.waitForTerminal(t, runID, agentE2EWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want %q; events=%v", status, "COMPLETED", streamedTypes(events))
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("model calls = %d, want 2 (one TOOL_CALL Turn, one FINAL Turn)", got)
	}

	agentNodeRunID := nodeRunIDOf(t, snapshot, agentFixtureNodeID)
	want := []string{
		"NODE_STARTED", "AGENT_STARTED",
		"AGENT_TURN_READY", "AGENT_TURN_STARTED", "AGENT_DECISION_COMMITTED",
		"AGENT_ACTION_STARTED", "AGENT_ACTION_COMPLETED",
		"AGENT_TURN_READY", "AGENT_TURN_STARTED", "AGENT_DECISION_COMMITTED",
		"AGENT_ACTION_STARTED", "AGENT_ACTION_COMPLETED",
		"AGENT_COMPLETED", "NODE_COMPLETED", "RUN_COMPLETED",
	}
	if got := agentEventSequence(events, agentNodeRunID, want); !sameStrings(got, want) {
		t.Fatalf("Agent Events over SSE =\n  %v\nwant\n  %v\n(full stream: %v)", got, want, streamedTypes(events))
	}

	// The two rounds are distinguishable in the stream itself, not only by position: the
	// second AGENT_TURN_STARTED names turnNo 2, and both were claimed immediately because
	// no restart happened.
	started := streamedOfType(events, "AGENT_TURN_STARTED")
	if len(started) != 2 {
		t.Fatalf("AGENT_TURN_STARTED events = %d, want 2", len(started))
	}
	for i, ev := range started {
		if no, _ := ev.payload["turnNo"].(float64); int(no) != i+1 {
			t.Errorf("AGENT_TURN_STARTED[%d].turnNo = %v, want %d", i, ev.payload["turnNo"], i+1)
		}
		if source, _ := ev.payload["claimSource"].(string); source != string(domain.ClaimImmediate) {
			t.Errorf("AGENT_TURN_STARTED[%d].claimSource = %q, want %q", i, source, domain.ClaimImmediate)
		}
	}
	if decisions := streamedOfType(events, "AGENT_DECISION_COMMITTED"); len(decisions) == 2 {
		if kind, _ := decisions[0].payload["kind"].(string); kind != "TOOL_CALL" {
			t.Errorf("first Decision kind = %q, want %q", kind, "TOOL_CALL")
		}
		if kind, _ := decisions[1].payload["kind"].(string); kind != "FINAL" {
			t.Errorf("second Decision kind = %q, want %q", kind, "FINAL")
		}
	}

	// The Agent's answer reaches the downstream Node: text_output's NodeRun output is the
	// FINAL output, which is what makes the Agent a Node like any other.
	nodeRuns, _ := snapshot["nodeRuns"].([]any)
	found := false
	for _, raw := range nodeRuns {
		nodeRun, _ := raw.(map[string]any)
		if id, _ := nodeRun["nodeId"].(string); id != "node_output" {
			continue
		}
		found = true
		if status, _ := nodeRun["status"].(string); status != "SUCCEEDED" {
			t.Errorf("downstream text_output NodeRun status = %q, want %q", status, "SUCCEEDED")
		}
		output, _ := nodeRun["output"].(map[string]any)
		if text, _ := output["text"].(string); text != agentE2EFinalOutput {
			t.Errorf("downstream text_output NodeRun output.text = %v, want %q", output["text"], agentE2EFinalOutput)
		}
	}
	if !found {
		t.Fatalf("snapshot has no NodeRun for node_output: %v", snapshot["nodeRuns"])
	}
}

// ---------------------------------------------------------------------------------------
// 2. Restart after the Tool round: the Reconciler finishes the second Turn
// ---------------------------------------------------------------------------------------

// TestE2E_AgentRestartAfterToolRound_ReconcilerFinishesSecondTurn stops the first Backend
// the instant the Tool result and the next READY Turn commit, and proves that a second
// Backend over the same database -- driven by one Reconciler pass, with no in-process queue
// item, notification or memory carried across -- claims that very Turn and carries the Run
// to COMPLETED.
func TestE2E_AgentRestartAfterToolRound_ReconcilerFinishesSecondTurn(t *testing.T) {
	stopper := newAgentLoopStopper()
	first := newTestEnvWithOptions(t, testEnvOptions{WrapServiceNotifier: stopper.notifier})
	stopper.arm(first, stopAtReadyTurn(2))

	firstCalls := scriptToolCallThenFinal(first, agentE2EToolKey, agentE2EFinalOutput)
	workflowID, version := saveAgentFixture(t, first)
	runID := createAgentRun(t, first, workflowID, version, "what is the answer?")

	stopper.waitStopped(t, agentE2EWait)
	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("model calls in the first process = %d, want 1 (the Tool round only)", got)
	}

	// The boundary is stated through the public projection: Turn 1 is done, Turn 2 is
	// committed READY, and nothing in this process will advance it.
	agentNodeRunID := first.agentNodeRunIDOf(t, runID)
	status, body := getAgentTrace(t, first, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace before the restart: status = %d, body=%s", status, body)
	}
	if statuses := turnStatuses(t, body); statuses[1] != "COMPLETED" || statuses[2] != "READY" {
		t.Fatalf("Turn statuses before the restart = %v, want turn 1 COMPLETED and turn 2 READY; body=%s", statuses, body)
	}

	// --- restart: same database, new process image.
	pool := first.pool
	first.stop()
	second := newTestEnvWithOptions(t, testEnvOptions{Pool: pool})
	secondCalls := scriptFinalOnly(second, agentE2EFinalOutput)

	rec := reconciler.New(reconciler.Config{
		UoW: second.uow, Executor: second.execution, Clock: domain.SystemClock{}, BatchLimit: 100,
	})
	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reconciler RunOnce: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("reconciler report errors = %v, want none", report.Errors)
	}
	if report.ReadyAgentTurnsFound == 0 {
		t.Fatalf("reconciler found no READY Agent Turn; report = %+v", report)
	}

	snapshot := second.waitForTerminal(t, runID, agentE2EWait)
	run, _ := snapshot["run"].(map[string]any)
	if got, _ := run["status"].(string); got != "COMPLETED" {
		t.Fatalf("Run status after the restart = %q, want %q", got, "COMPLETED")
	}
	if got := secondCalls.Load(); got != 1 {
		t.Errorf("model calls in the second process = %d, want exactly 1 (Turn 2 only)", got)
	}

	events := second.replayEvents(t, runID, agentE2EWait)
	assertAscendingSeq(t, events)

	var turnTwoStarts []streamedEvent
	for _, ev := range streamedOfType(events, "AGENT_TURN_STARTED") {
		if no, _ := ev.payload["turnNo"].(float64); int(no) == 2 {
			turnTwoStarts = append(turnTwoStarts, ev)
		}
	}
	if len(turnTwoStarts) != 1 {
		t.Fatalf("AGENT_TURN_STARTED events for turn 2 = %d, want exactly 1: %v", len(turnTwoStarts), streamedTypes(events))
	}
	if source, _ := turnTwoStarts[0].payload["claimSource"].(string); source != string(domain.ClaimReconciler) {
		t.Errorf("turn 2 claimSource = %q, want %q", source, domain.ClaimReconciler)
	}
	if got := len(streamedOfType(events, "AGENT_TURN_READY")); got != 2 {
		t.Errorf("AGENT_TURN_READY events = %d, want 2 (recovery claims the persisted Turn, it does not create another)", got)
	}
	if got := len(streamedOfType(events, "AGENT_COMPLETED")); got != 1 {
		t.Errorf("AGENT_COMPLETED events = %d, want 1: %v", got, streamedTypes(events))
	}

	status, body = getAgentTrace(t, second, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace after the restart: status = %d, body=%s", status, body)
	}
	if statuses := turnStatuses(t, body); len(statuses) != 2 || statuses[1] != "COMPLETED" || statuses[2] != "COMPLETED" {
		t.Errorf("Turn statuses after the restart = %v, want exactly two COMPLETED Turns; body=%s", statuses, body)
	}
}

// ---------------------------------------------------------------------------------------
// 3. Restart after a committed Decision: the original Action is executed
// ---------------------------------------------------------------------------------------

// TestE2E_AgentRestartAfterDecision_OriginalActionExecutedNoSecondDecision stops the first
// Backend the instant Turn 1's Decision commits, leaving a READY TOOL_CALL Action nobody
// executed. A committed Agent Decision is never regenerated (CLAUDE.md: "never substitute
// the closest extension or regenerate a committed Agent Decision"), so the restarted
// Backend must execute that very Action -- exactly one AGENT_DECISION_COMMITTED for turn 1
// exists across both processes, and the Action was claimed by the Reconciler.
func TestE2E_AgentRestartAfterDecision_OriginalActionExecutedNoSecondDecision(t *testing.T) {
	stopper := newAgentLoopStopper()
	first := newTestEnvWithOptions(t, testEnvOptions{WrapServiceNotifier: stopper.notifier})
	stopper.arm(first, stopAtReadyActionOfTurn(1))

	firstCalls := scriptToolCallThenFinal(first, agentE2EToolKey, agentE2EFinalOutput)
	workflowID, version := saveAgentFixture(t, first)
	runID := createAgentRun(t, first, workflowID, version, "what is the answer?")

	stopper.waitStopped(t, agentE2EWait)
	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("model calls in the first process = %d, want 1 (Turn 1's Decision only)", got)
	}

	agentNodeRunID := first.agentNodeRunIDOf(t, runID)
	status, body := getAgentTrace(t, first, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace before the restart: status = %d, body=%s", status, body)
	}
	trace := decodeBody[map[string]any](t, body)
	turns, _ := trace["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("Turns before the restart = %d, want 1; body=%s", len(turns), body)
	}
	turnOne, _ := turns[0].(map[string]any)
	action, _ := turnOne["action"].(map[string]any)
	if action == nil {
		t.Fatalf("Turn 1 projects no committed Action before the restart: %s", body)
	}
	actionID, _ := action["id"].(string)
	if got, _ := action["status"].(string); got != "READY" {
		t.Fatalf("Turn 1 Action status before the restart = %q, want %q", got, "READY")
	}

	// --- restart: same database, new process image.
	pool := first.pool
	first.stop()
	second := newTestEnvWithOptions(t, testEnvOptions{Pool: pool})
	secondCalls := scriptFinalOnly(second, agentE2EFinalOutput)

	rec := reconciler.New(reconciler.Config{
		UoW: second.uow, Executor: second.execution, Clock: domain.SystemClock{}, BatchLimit: 100,
	})
	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reconciler RunOnce: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("reconciler report errors = %v, want none", report.Errors)
	}
	if report.ReadyAgentActionsFound == 0 {
		t.Fatalf("reconciler found no READY Agent Action; report = %+v", report)
	}

	snapshot := second.waitForTerminal(t, runID, agentE2EWait)
	run, _ := snapshot["run"].(map[string]any)
	if got, _ := run["status"].(string); got != "COMPLETED" {
		t.Fatalf("Run status after the restart = %q, want %q", got, "COMPLETED")
	}
	// Turn 2's FINAL Decision is the only model call the second process may make: Turn 1's
	// Decision was already committed before it started.
	if got := secondCalls.Load(); got != 1 {
		t.Errorf("model calls in the second process = %d, want exactly 1 (Turn 2 only)", got)
	}

	events := second.replayEvents(t, runID, agentE2EWait)
	assertAscendingSeq(t, events)

	turnOneID, _ := turnOne["id"].(string)
	var turnOneDecisions, turnOneActionStarts []streamedEvent
	for _, ev := range streamedOfType(events, "AGENT_DECISION_COMMITTED") {
		if id, _ := ev.payload["turnId"].(string); id == turnOneID {
			turnOneDecisions = append(turnOneDecisions, ev)
		}
	}
	for _, ev := range streamedOfType(events, "AGENT_ACTION_STARTED") {
		if id, _ := ev.payload["actionId"].(string); id == actionID {
			turnOneActionStarts = append(turnOneActionStarts, ev)
		}
	}
	if len(turnOneDecisions) != 1 {
		t.Fatalf("AGENT_DECISION_COMMITTED events for turn 1 across both processes = %d, want exactly 1 (a committed Decision is never regenerated)", len(turnOneDecisions))
	}
	if id, _ := turnOneDecisions[0].payload["actionId"].(string); id != actionID {
		t.Errorf("turn 1 Decision names actionId %q, want the Action the restart found READY (%q)", id, actionID)
	}
	if len(turnOneActionStarts) != 1 {
		t.Fatalf("AGENT_ACTION_STARTED events for the recovered Action = %d, want exactly 1: %v", len(turnOneActionStarts), streamedTypes(events))
	}
	if source, _ := turnOneActionStarts[0].payload["claimSource"].(string); source != string(domain.ClaimReconciler) {
		t.Errorf("recovered Action claimSource = %q, want %q", source, domain.ClaimReconciler)
	}

	status, body = getAgentTrace(t, second, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace after the restart: status = %d, body=%s", status, body)
	}
	if statuses := turnStatuses(t, body); len(statuses) != 2 || statuses[1] != "COMPLETED" || statuses[2] != "COMPLETED" {
		t.Errorf("Turn statuses after the restart = %v, want exactly two COMPLETED Turns; body=%s", statuses, body)
	}
}
