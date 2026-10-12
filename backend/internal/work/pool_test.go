package work

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// execScriptedExecutor is a work.Executor test double whose Advance/Execute behaviour is
// driven entirely by caller-supplied functions, so pool tests can script exact call-by-call
// outcomes and use channels as barriers instead of sleeping (CLAUDE.md testing standard).
type execScriptedExecutor struct {
	advance func(callNo int) (service.AdvanceOutcome, error)
	execute func(ctx context.Context, callNo int) error

	advanceCalls int32
	executeCalls int32

	// turnCalls records every AdvanceAgentTurn call; it must be buffered by the test.
	turnCalls chan agentTurnCall
}

type agentTurnCall struct {
	turnID      string
	claimSource domain.ClaimSource
}

func (e *execScriptedExecutor) AdvanceAgentTurn(_ context.Context, turnID string, claimSource domain.ClaimSource) error {
	e.turnCalls <- agentTurnCall{turnID: turnID, claimSource: claimSource}
	return nil
}

func (e *execScriptedExecutor) Advance(_ context.Context, _ string) (service.AdvanceOutcome, error) {
	n := int(atomic.AddInt32(&e.advanceCalls, 1))
	return e.advance(n)
}

func (e *execScriptedExecutor) Execute(ctx context.Context, _ service.AdvanceOutcome) error {
	n := int(atomic.AddInt32(&e.executeCalls, 1))
	return e.execute(ctx, n)
}

// TestPool_ProcessesQueuedItem_DrainsUntilUnclaimed proves processItem's drain loop: it
// keeps calling Advance/Execute for one queued Run until Advance makes no further claim,
// then stops without any further Execute call. AfterExecute only proves the first
// iteration finished; the test also waits for the second, no-claim Advance call before
// Stop, because Stop cancels the worker context and would otherwise end the drain loop
// before it reaches that call. Both are channel barriers, not sleeps.
func TestPool_ProcessesQueuedItem_DrainsUntilUnclaimed(t *testing.T) {
	done := make(chan struct{})
	unclaimed := make(chan struct{})

	exec := &execScriptedExecutor{
		advance: func(callNo int) (service.AdvanceOutcome, error) {
			if callNo == 1 {
				return service.AdvanceOutcome{Claimed: true, RunID: "run_x"}, nil
			}
			if callNo == 2 {
				close(unclaimed)
			}
			return service.AdvanceOutcome{Claimed: false}, nil
		},
		execute: func(context.Context, int) error { return nil },
	}
	hooks := Hooks{AfterExecute: func(string) { close(done) }}

	queue := NewQueue(4)
	pool := NewPool(queue, exec, 1, hooks, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Stop()

	if !queue.EnqueueAdvance("run_x") {
		t.Fatal("enqueue run_x: want accepted, got refused")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AfterExecute hook: want fired within 5s, got timeout")
	}
	select {
	case <-unclaimed:
	case <-time.After(5 * time.Second):
		t.Fatal("second Advance call: want made within 5s, got timeout")
	}
	pool.Stop()

	if got := atomic.LoadInt32(&exec.executeCalls); got != 1 {
		t.Fatalf("Execute calls: want exactly 1 (loop stops the moment Advance reports no claim), got %d", got)
	}
	if got := atomic.LoadInt32(&exec.advanceCalls); got != 2 {
		t.Fatalf("Advance calls: want 2 (one claim, one no-claim), got %d", got)
	}
}

// TestPool_Stop_WaitsForInFlightExecuteToFinish proves Stop's explicit-goroutine-ownership
// contract: it must not return while a worker is still inside Execute. The ordering is
// fixed by channel barriers, never by sleeps or by racing two concurrent events:
// entered proves the worker is inside Execute before Stop is called; the in-flight
// Execute then waits for the worker context that Stop cancels, which proves Stop has
// started and makes the drain loop's exit path deterministic (after Execute returns, the
// loop sees the cancelled context and never reaches a second Advance call); proceed,
// released by the test, is the only thing that lets Execute return, so Stop can only
// return after it.
func TestPool_Stop_WaitsForInFlightExecuteToFinish(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	proceed := make(chan struct{})

	exec := &execScriptedExecutor{
		advance: func(callNo int) (service.AdvanceOutcome, error) {
			if callNo == 1 {
				return service.AdvanceOutcome{Claimed: true, RunID: "run_x"}, nil
			}
			return service.AdvanceOutcome{Claimed: false}, nil
		},
		execute: func(ctx context.Context, _ int) error {
			<-ctx.Done()
			close(cancelled)
			<-proceed
			return nil
		},
	}
	hooks := Hooks{BeforeExecute: func(string) { close(entered) }}

	queue := NewQueue(4)
	pool := NewPool(queue, exec, 1, hooks, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	if !queue.EnqueueAdvance("run_x") {
		t.Fatal("enqueue run_x: want accepted, got refused")
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("BeforeExecute hook: want fired within 5s, got timeout")
	}

	stopped := make(chan struct{})
	go func() {
		pool.Stop()
		close(stopped)
	}()

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop: want the worker context cancelled within 5s, got timeout")
	}
	// Execute is still blocked on proceed, so a Stop that had already returned here did
	// not wait for the in-flight call.
	select {
	case <-stopped:
		t.Fatal("Stop returned while Execute was still in flight")
	default:
	}

	close(proceed)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop: want returned within 5s of Execute finishing, got timeout")
	}

	if got := atomic.LoadInt32(&exec.executeCalls); got != 1 {
		t.Fatalf("Execute calls by the time Stop returned: want exactly 1, got %d", got)
	}
	// Execute observed the cancellation before returning, so the drain loop's context
	// check after it must have ended the loop before any second Advance call.
	if got := atomic.LoadInt32(&exec.advanceCalls); got != 1 {
		t.Fatalf("Advance calls by the time Stop returned: want 1, got %d", got)
	}

	// Stop before Start, and Stop twice, must both be safe (pool.go's documented
	// contract).
	fresh := NewPool(NewQueue(1), exec, 1, Hooks{}, nil)
	fresh.Stop()
	pool.Stop()
}

// TestPool_AgentTurnItem_DispatchesToAdvanceAgentTurn covers the typed queue item:
// an AGENT_TURN item carries the persisted Turn ID and its Run ID, and the Pool
// hands it to the Agent Turn use case as immediate advancement -- never to the Run-level
// Advance -- under the same Before/AfterExecute hooks a Run item uses.
func TestPool_AgentTurnItem_DispatchesToAdvanceAgentTurn(t *testing.T) {
	var hooked []string
	done := make(chan struct{})
	exec := &execScriptedExecutor{
		advance: func(int) (service.AdvanceOutcome, error) {
			t.Error("Advance called for an AGENT_TURN item")
			return service.AdvanceOutcome{}, nil
		},
		execute:   func(context.Context, int) error { return nil },
		turnCalls: make(chan agentTurnCall, 1),
	}
	hooks := Hooks{
		BeforeExecute: func(runID string) { hooked = append(hooked, "before:"+runID) },
		AfterExecute: func(runID string) {
			hooked = append(hooked, "after:"+runID)
			close(done)
		},
	}

	queue := NewQueue(4)
	pool := NewPool(queue, exec, 1, hooks, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Stop()

	if !queue.EnqueueAgentTurn("run_a", "turn_1") {
		t.Fatal("enqueue agent turn: want accepted, got refused")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AfterExecute hook: want fired within 5s, got timeout")
	}
	pool.Stop()

	select {
	case call := <-exec.turnCalls:
		if call.turnID != "turn_1" || call.claimSource != domain.ClaimImmediate {
			t.Errorf("AdvanceAgentTurn(%q, %s), want (turn_1, IMMEDIATE)", call.turnID, call.claimSource)
		}
	default:
		t.Fatal("AdvanceAgentTurn was not called for the AGENT_TURN item")
	}
	if len(hooked) != 2 || hooked[0] != "before:run_a" || hooked[1] != "after:run_a" {
		t.Errorf("hooks = %v, want before and after for run_a", hooked)
	}
	if got := atomic.LoadInt32(&exec.advanceCalls); got != 0 {
		t.Errorf("Advance calls = %d, want 0", got)
	}
}
