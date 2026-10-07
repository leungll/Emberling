// Package work is the in-process latency optimization layer over ExecutionService.
// It calls the same service use cases the Reconciler calls (internal/reconciler) and
// maintains no execution path or business state of its own (CLAUDE.md package
// boundaries): every state transition still happens inside service.ExecutionService's
// own transactions. Persisted READY NodeRuns and Agent Turns remain the durable source of
// recoverable work; this package only shortens the time between COMMIT and the next
// advancement for the common, still-running process.
package work

// ItemKind is the work type of a queued Item.
type ItemKind int

const (
	// ItemAdvanceRun means "this Run may have a READY NodeRun to advance."
	ItemAdvanceRun ItemKind = iota + 1
	// ItemAgentTurn means "this persisted Agent Turn of this Run was committed READY."
	ItemAgentTurn
)

// Item is one unit of queued work. An item carries only its work type, the
// persisted object's ID and the Run ID -- never authoritative state or execution input,
// which the use case re-reads from PostgreSQL.
type Item struct {
	Kind   ItemKind
	RunID  string
	TurnID string
}

// defaultQueueCapacity bounds Queue when NewQueue is given a non-positive capacity. It
// is an MVP default (same category as service's plan cache bound), not a documented
// requirement.
const defaultQueueCapacity = 1024

// Queue is a bounded, non-blocking queue of Items. It backs service.WorkEnqueuer: a
// refused (full) enqueue must never roll back or lose a committed fact, because the
// Reconciler rediscovers the same persisted READY work independently of this queue.
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
	return q.offer(Item{Kind: ItemAdvanceRun, RunID: runID})
}

// EnqueueAgentTurn offers a committed READY Agent Turn to the queue without blocking,
// with the same bound and the same meaning of a false result as EnqueueAdvance.
func (q *Queue) EnqueueAgentTurn(runID, turnID string) bool {
	return q.offer(Item{Kind: ItemAgentTurn, RunID: runID, TurnID: turnID})
}

func (q *Queue) offer(item Item) bool {
	select {
	case q.items <- item:
		return true
	default:
		return false
	}
}
