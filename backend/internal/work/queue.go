// Package work is the in-process latency optimization layer over ExecutionService.
// It calls the same service use cases the Reconciler calls (internal/reconciler) and
// maintains no execution path or business state of its own (CLAUDE.md package
// boundaries): every state transition still happens inside service.ExecutionService's
// own transactions. Persisted READY NodeRuns remain the durable source of recoverable
// work; this package only shortens the time between COMMIT and the next Advance call
// for the common, still-running process (invariant #6).
package work

// Item is one unit of queued work: "this Run may have something to advance."
type Item struct {
	RunID string
}

// defaultQueueCapacity bounds Queue when NewQueue is given a non-positive capacity. It
// is an MVP default (same category as service's plan cache bound), not a documented
// requirement.
const defaultQueueCapacity = 1024

// Queue is a bounded, non-blocking queue of Items. It backs
// service.WorkEnqueuer.EnqueueAdvance: a refused (full) enqueue must never roll back or
// lose a committed fact, because the Reconciler rediscovers the same persisted READY
// work independently of this queue (invariant #6, docs/06-execution-model.md §1.3).
type Queue struct {
	items chan Item
}

// NewQueue builds a Queue bounded at capacity (or defaultQueueCapacity if capacity is
// not positive).
func NewQueue(capacity int) *Queue {
	if capacity <= 0 {
		capacity = defaultQueueCapacity
	}
	return &Queue{items: make(chan Item, capacity)}
}

// EnqueueAdvance offers runID to the queue without blocking. It reports true if the
// item was accepted and false if the queue is at capacity. A false result is an
// ordinary, expected outcome under load, not an error: the caller (ExecutionService's
// post-COMMIT step) must not treat it as one.
func (q *Queue) EnqueueAdvance(runID string) bool {
	select {
	case q.items <- Item{RunID: runID}:
		return true
	default:
		return false
	}
}
