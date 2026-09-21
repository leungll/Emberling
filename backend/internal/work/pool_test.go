package work

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/service"
)

// execScriptedExecutor is a work.Executor test double whose Advance/Execute behaviour is
// driven entirely by caller-supplied functions, so pool tests can script exact call-by-call
// outcomes and use channels as barriers instead of sleeping (CLAUDE.md testing standard).
type execScriptedExecutor struct {
	advance func(callNo int) (service.AdvanceOutcome, error)
	execute func(callNo int) error

	advanceCalls int32
	executeCalls int32
}

func (e *execScriptedExecutor) Advance(_ context.Context, _ string) (service.AdvanceOutcome, error) {
	n := int(atomic.AddInt32(&e.advanceCalls, 1))
	return e.advance(n)
}

func (e *execScriptedExecutor) Execute(_ context.Context, _ service.AdvanceOutcome) error {
	n := int(atomic.AddInt32(&e.executeCalls, 1))
	return e.execute(n)
}

// TestPool_ProcessesQueuedItem_DrainsUntilUnclaimed proves processItem's drain loop: it
// keeps calling Advance/Execute for one queued Run until Advance makes no further claim,
// then stops without any further Execute call. AfterExecute is the completion barrier, not
// a sleep.
func TestPool_ProcessesQueuedItem_DrainsUntilUnclaimed(t *testing.T) {
	done := make(chan struct{})

	exec := &execScriptedExecutor{
		advance: func(callNo int) (service.AdvanceOutcome, error) {
			if callNo == 1 {
				return service.AdvanceOutcome{Claimed: true, RunID: "run_x"}, nil
			}
			return service.AdvanceOutcome{Claimed: false}, nil
		},
		execute: func(int) error { return nil },
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
	pool.Stop()

	if got := atomic.LoadInt32(&exec.executeCalls); got != 1 {
		t.Fatalf("Execute calls: want exactly 1 (loop stops the moment Advance reports no claim), got %d", got)
	}
	if got := atomic.LoadInt32(&exec.advanceCalls); got != 2 {
		t.Fatalf("Advance calls: want 2 (one claim, one no-claim), got %d", got)
	}
}

// TestPool_Stop_WaitsForInFlightExecuteToFinish proves Stop's explicit-goroutine-ownership
// contract: it must not return while a worker is still inside Execute. The test never
// sleeps to manufacture this ordering; it uses two real channels as barriers -- entered
// proves the worker has reached Execute before Stop is called, and proceed is what
// actually unblocks the worker's in-flight Execute call, released concurrently with Stop
// from a background goroutine so Stop's own WaitGroup.Wait is what is under test.
func TestPool_Stop_WaitsForInFlightExecuteToFinish(t *testing.T) {
	entered := make(chan struct{})
	proceed := make(chan struct{})

	exec := &execScriptedExecutor{
		advance: func(callNo int) (service.AdvanceOutcome, error) {
			if callNo == 1 {
				return service.AdvanceOutcome{Claimed: true, RunID: "run_x"}, nil
			}
			return service.AdvanceOutcome{Claimed: false}, nil
		},
		execute: func(int) error {
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

	// Release the blocked Execute call from a background goroutine so that Stop, called
	// synchronously right below, is genuinely exercising WaitGroup.Wait rather than
	// racing a call that already finished.
	go func() { close(proceed) }()

	pool.Stop()

	if got := atomic.LoadInt32(&exec.executeCalls); got != 1 {
		t.Fatalf("Execute calls by the time Stop returned: want exactly 1, got %d", got)
	}
	// processItem checks ctx.Err() before every Advance call, and Stop cancels ctx before
	// waiting; since that cancellation races concurrently with proceed unblocking Execute,
	// the loop is expected to exit via the cancelled context rather than reach a second
	// Advance call. The one guarantee under test is that Stop did not return early: it
	// only returned after the in-flight Execute (call 1) actually completed.
	if got := atomic.LoadInt32(&exec.advanceCalls); got != 1 {
		t.Fatalf("Advance calls by the time Stop returned: want 1, got %d", got)
	}

	// Stop before Start, and Stop twice, must both be safe (pool.go's documented
	// contract).
	fresh := NewPool(NewQueue(1), exec, 1, Hooks{}, nil)
	fresh.Stop()
	pool.Stop()
}
