//go:build integration

// Synchronous Agent Loop advancement through the work queue (06 §1.3, 06 §2.1). A Tool
// round that finishes on the worker's own goroutine commits the next READY Turn and hands
// it to the bounded in-process work queue as an AGENT_TURN item; it does not advance that
// Turn by re-entering the Turn use case from inside the previous one. The queue item is
// only a latency optimisation: a process that dies between the commit and the dequeue
// leaves a persisted READY Turn that the Reconciler of the next process rediscovers and
// advances through the same use case. They share the harnesses of agent_loop_test.go and
// the Pool helpers of agent_async_callback_worker_test.go.
package integration

import (
	"context"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/work"
)

// agentRecordingEnqueuer sits between the ExecutionService and a real bounded work.Queue.
// Run-level Advance offers always reach the queue, so a Pool worker drives the Run from
// CreateRun onward the way cmd/emberling does. Every AGENT_TURN offer is recorded, and is
// forwarded only when forwardTurns is set: a held offer is the queue item a process that
// died right after the result transaction committed would never have dequeued.
type agentRecordingEnqueuer struct {
	queue        *work.Queue
	forwardTurns bool

	mu    sync.Mutex
	turns []string
}

func (e *agentRecordingEnqueuer) EnqueueAdvance(runID string) bool {
	return e.queue.EnqueueAdvance(runID)
}

func (e *agentRecordingEnqueuer) EnqueueAgentTurn(runID, turnID string) bool {
	e.mu.Lock()
	e.turns = append(e.turns, turnID)
	e.mu.Unlock()
	if !e.forwardTurns {
		return true
	}
	return e.queue.EnqueueAgentTurn(runID, turnID)
}

func (e *agentRecordingEnqueuer) enqueuedTurns() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.turns...)
}

// agentTurnIDByNo resolves one Agent Run's Turn by number.
func agentTurnIDByNo(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, turnNo int) string {
	t.Helper()
	var turn domain.AgentTurn
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		turn, err = tx.AgentTurns().GetByRunAndTurnNo(ctx, agentRunID, turnNo)
		return err
	}); err != nil {
		t.Fatalf("get turn %d of agent run %s: %v", turnNo, agentRunID, err)
	}
	return turn.ID
}

// agentScriptToolCallsThenFinal makes the fixture model decide the same TOOL_CALL on each
// of its first `rounds` Turns and answer FINAL on the next one. The returned counter
// records how often the model was called.
func agentScriptToolCallsThenFinal(h *agentHarness, rounds int32, toolCall mockmodel.Scenario) *atomic.Int32 {
	var generated atomic.Int32
	toolCall.Kind = mockmodel.ScenarioToolCall
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if generated.Add(1) > rounds {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
		call := toolCall
		return &call
	}
	return &generated
}

// TestAgentLoop_SyncTurns_EachNextTurnEnqueuedNotRecursed covers 06 §1.3 for the
// synchronous Tool path: a three-Turn Agent Run (TOOL_CALL, TOOL_CALL, FINAL) driven by
// one work.Pool worker completes with Turn 2 and Turn 3 each offered to the work queue by
// the round before it and claimed as an AGENT_TURN item, so the Turn use case is entered
// once per Turn from a flat stack rather than nested inside the previous Turn's call.
func TestAgentLoop_SyncTurns_EachNextTurnEnqueuedNotRecursed(t *testing.T) {
	queue := work.NewQueue(16)
	enqueuer := &agentRecordingEnqueuer{queue: queue, forwardTurns: true}
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool, Queue: enqueuer})
	generated := agentScriptToolCallsThenFinal(h, 2, agentToolCallScenario())

	// Sampled inside every Tool call: the goroutine stack at that moment shows how the
	// Turn being executed was entered. A recursive continuation would nest the second
	// round's AdvanceAgentTurn inside the first round's.
	var mu sync.Mutex
	var stacks []string
	tool.during = func(context.Context, registry.ToolAction) {
		mu.Lock()
		defer mu.Unlock()
		stacks = append(stacks, string(debug.Stack()))
	}

	def := h.saveDefinition(agentLoopDefinition("wf-agent-turn-queue"))
	executed := agentStartPool(t, h, queue)
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	agentAwaitRunStatus(t, h, run.ID, domain.RunCompleted, executed)

	if got := tool.count(); got != 2 {
		t.Fatalf("tool calls = %d, want 2", got)
	}
	if got := generated.Load(); got != 3 {
		t.Fatalf("model calls = %d, want 3 (two Tool rounds and one FINAL)", got)
	}

	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, nodeRun.ID)
	if !ok {
		t.Fatalf("no agent run for node run %s", nodeRun.ID)
	}
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE", agentTermination(agentRun))
	}
	turnIDs := []string{
		agentTurnIDByNo(h.ctx, t, h.uow, agentRun.ID, 1),
		agentTurnIDByNo(h.ctx, t, h.uow, agentRun.ID, 2),
		agentTurnIDByNo(h.ctx, t, h.uow, agentRun.ID, 3),
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRun.ID, 4) {
		t.Errorf("a fourth Turn was created after the FINAL Decision")
	}

	// Turn 1 is the Agent NodeRun claim's own work; Turns 2 and 3 are each offered to the
	// queue by the round before them, in order, and nothing else is ever offered.
	if got, want := enqueuer.enqueuedTurns(), turnIDs[1:]; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("AGENT_TURN offers = %v, want %v (Turn 2 then Turn 3)", got, want)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	assertContiguousSeq(t, events)
	for no, turnID := range turnIDs {
		started := agentOnlyPayloadFor(t, events, domain.EventAgentTurnStarted, "turnId", turnID)
		if started["claimSource"] != string(domain.ClaimImmediate) {
			t.Errorf("AGENT_TURN_STARTED claimSource for turn %d = %v, want IMMEDIATE: the worker claimed it", no+1, started["claimSource"])
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(stacks) != 2 {
		t.Fatalf("sampled tool-call stacks = %d, want 2", len(stacks))
	}
	for i, stack := range stacks {
		if depth := strings.Count(stack, ".AdvanceAgentTurn("); depth != 1 {
			t.Errorf("round %d ran with %d AdvanceAgentTurn frames on the stack, want 1: the next Turn must not be advanced by recursion", i+1, depth)
		}
	}
	if !strings.Contains(stacks[1], ".processAgentTurn(") {
		t.Errorf("round 2 was not entered from the Pool's AGENT_TURN item path")
	}
}

// TestAgentLoop_WorkerLostBetweenTurns_ReconcilerRediscoversPersistedReadyTurn covers the
// crash window of 06 §1.3 and docs/09 §3.3 "Tool result 与下一条 READY Turn 提交后崩溃" on
// the synchronous path: the result transaction commits and its queue item is never
// dequeued because the worker is gone. The committed READY Turn is the only continuation
// that survives, and the Reconciler of a restarted Backend claims that Turn -- not a new
// one -- through the same Turn use case and finishes the Run.
func TestAgentLoop_WorkerLostBetweenTurns_ReconcilerRediscoversPersistedReadyTurn(t *testing.T) {
	queue := work.NewQueue(16)
	enqueuer := &agentRecordingEnqueuer{queue: queue, forwardTurns: false}
	tool := &agentRecordingTool{delegate: lookup.Executor{}}
	crashed := newAgentHarness(t, agentHarnessOptions{LookupExecutor: tool, Queue: enqueuer})
	generated := agentScriptToolCallThenFinal(crashed, agentToolCallScenario())

	executed := make(chan string, 64)
	pool := work.NewPool(queue, crashed.svc, 1, work.Hooks{
		AfterExecute: func(runID string) {
			select {
			case executed <- runID:
			default:
			}
		},
	}, nil)
	poolCtx, cancelPool := context.WithCancel(context.Background())
	pool.Start(poolCtx)
	t.Cleanup(func() {
		cancelPool()
		pool.Stop()
	})

	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-turn-queue-lost"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	// The worker runs Turn 1 to the end of its Tool round; the offer is recorded before
	// that item's AfterExecute fires, so the first signal after it appears is the moment
	// the result transaction has committed and its queue item is held. Stopping the Pool
	// here is the process dying before the item was dequeued.
	deadline := time.After(agentPoolWait)
	for len(enqueuer.enqueuedTurns()) == 0 {
		select {
		case <-executed:
		case <-deadline:
			t.Fatalf("the Pool never finished the first Tool round within %s", agentPoolWait)
		}
	}
	cancelPool()
	pool.Stop()

	held := enqueuer.enqueuedTurns()
	if len(held) != 1 {
		t.Fatalf("AGENT_TURN offers after the first round = %v, want exactly the next Turn", held)
	}
	ready := listReadyTurns(crashed.ctx, t, crashed.uow)
	if len(ready) != 1 || ready[0].TurnNo != 2 || ready[0].ID != held[0] {
		t.Fatalf("ready turns after the lost worker = %+v, want exactly the offered Turn 2 %s", ready, held[0])
	}
	turn2 := ready[0]
	if got := tool.count(); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}
	if got := generated.Load(); got != 1 {
		t.Fatalf("model calls before the restart = %d, want 1", got)
	}
	if got := agentRunRow(crashed.ctx, t, crashed.uow, run.ID).Status; got != domain.RunRunning {
		t.Fatalf("run status after the lost worker = %s, want RUNNING", got)
	}
	nodeRun := agentNodeRun(crashed.ctx, t, crashed.uow, run.ID)
	agentRun, _ := agentRunOfNodeRun(crashed.ctx, t, crashed.uow, nodeRun.ID)
	turn1ID := agentTurnIDByNo(crashed.ctx, t, crashed.uow, agentRun.ID, 1)
	if turn := getAgentTurn(crashed.ctx, t, crashed.uow, turn1ID); turn.Status != domain.AgentTurnCompleted {
		t.Fatalf("turn 1 status = %s, want COMPLETED", turn.Status)
	}
	if action, found := agentActionOfTurn(crashed.ctx, t, crashed.uow, turn1ID); !found || action.Status != domain.AgentActionSucceeded {
		t.Fatalf("turn 1 action = %+v (found %v), want SUCCEEDED", action, found)
	}

	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	regenerated := agentScriptFinal(restarted, mockmodel.Scenario{Output: "recovered answer"})
	agentRunOnceUntil(t, restarted, run.ID, domain.RunCompleted)

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	assertContiguousSeq(t, events)
	started := agentOnlyPayloadFor(t, events, domain.EventAgentTurnStarted, "turnId", turn2.ID)
	if started["claimSource"] != string(domain.ClaimReconciler) {
		t.Errorf("AGENT_TURN_STARTED claimSource for the rediscovered turn = %v, want RECONCILER", started["claimSource"])
	}
	if got := regenerated.Load(); got != 1 {
		t.Errorf("model calls in the restarted process = %d, want exactly 1", got)
	}
	if turn := getAgentTurn(restarted.ctx, t, restarted.uow, turn2.ID); turn.Status != domain.AgentTurnCompleted {
		t.Errorf("rediscovered turn status = %s, want COMPLETED", turn.Status)
	}
	if agentTurnNoExists(restarted.ctx, t, restarted.uow, agentRun.ID, 3) {
		t.Errorf("a third Turn was created: recovery must advance the existing READY Turn, not add a round")
	}
	again, _ := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, nodeRun.ID)
	if again.Termination == nil || *again.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent run termination = %v, want FINAL_RESPONSE", agentTermination(again))
	}
}
