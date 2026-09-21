package work

import (
	"context"
	"log/slog"
	"sync"

	"github.com/leungll/Emberling/backend/internal/service"
)

// Executor is the subset of *service.ExecutionService the Pool drives. It is declared
// here, rather than depending on the concrete type, only so a unit test can substitute a
// fake without a database; the real caller always wires *service.ExecutionService.
type Executor interface {
	Advance(ctx context.Context, runID string) (service.AdvanceOutcome, error)
	Execute(ctx context.Context, outcome service.AdvanceOutcome) error
}

// Hooks lets a caller observe or synchronize on Pool activity without sleeps
// (CLAUDE.md testing standard: "concurrency and failure tests use explicit barriers or
// injected hooks"). Both fields are optional.
type Hooks struct {
	BeforeExecute func(runID string)
	AfterExecute  func(runID string)
}

// Pool owns a fixed number of worker goroutines that drain a Queue and drive Executor.
// It is the only component in internal/work that runs a goroutine; Start/Stop make that
// ownership explicit so nothing leaks past shutdown (CLAUDE.md: "make ownership of
// goroutines explicit and stop them during shutdown").
//
// Pool maintains no execution path or business state of its own: every transition still
// happens inside Executor's own transactions (CLAUDE.md package boundaries for `work`).
type Pool struct {
	queue    *Queue
	executor Executor
	workers  int
	hooks    Hooks
	logger   *slog.Logger

	wg      sync.WaitGroup
	cancel  context.CancelFunc
	started bool
}

// NewPool builds a Pool of workers goroutines (at least 1) draining queue and driving
// executor. hooks may be the zero value.
func NewPool(queue *Queue, executor Executor, workers int, hooks Hooks, logger *slog.Logger) *Pool {
	if workers <= 0 {
		workers = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Pool{queue: queue, executor: executor, workers: workers, hooks: hooks, logger: logger}
}

// Start launches the worker goroutines. It must be called at most once per Pool; ctx
// bounds the workers' lifetime in addition to Stop.
func (p *Pool) Start(ctx context.Context) {
	if p.started {
		return
	}
	p.started = true

	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.run(runCtx)
	}
}

// Stop cancels the workers' context and blocks until every worker goroutine started by
// Start has exited. Calling Stop before Start, or twice, is safe.
func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *Pool) run(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case item, ok := <-p.queue.items:
			if !ok {
				return
			}
			p.processItem(ctx, item.RunID)
		}
	}
}

// processItem repeatedly claims and executes work for one Run: Advance, and on a
// successful claim, Execute, looping back to Advance again immediately. It stops the
// moment Advance makes no claim -- either because there is nothing left to do, or
// because the sole remaining candidate (a RUNNING NodeRun whose retry backoff has not
// yet elapsed) is not due. This Pool never waits for that backoff in-process: the
// Reconciler rediscovers the NodeRun once its next_attempt_at has passed
// (docs/06-execution-model.md §1.3), so processItem simply returns rather than sleeping
// or busy-polling.
//
// Advance's transaction has already committed by the time Execute runs (Execute always
// runs after Advance returns), so no transaction is ever held across Execute.
func (p *Pool) processItem(ctx context.Context, runID string) {
	for {
		if ctx.Err() != nil {
			return
		}

		outcome, err := p.executor.Advance(ctx, runID)
		if err != nil {
			p.logger.Error("work: advance failed", "run_id", runID, "error", err)
			return
		}
		if !outcome.Claimed {
			return
		}

		if p.hooks.BeforeExecute != nil {
			p.hooks.BeforeExecute(runID)
		}
		if err := p.executor.Execute(ctx, outcome); err != nil {
			// Execute reports a hard failure only when its own transaction could not be
			// committed (e.g. a database error); an ordinary Node failure is already
			// recorded through FailNode and returns nil here.
			p.logger.Error("work: execute failed", "run_id", runID, "node_run_id", outcome.NodeRunID, "attempt_id", outcome.AttemptID, "error", err)
		}
		if p.hooks.AfterExecute != nil {
			p.hooks.AfterExecute(runID)
		}
	}
}
