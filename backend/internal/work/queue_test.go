package work

import "testing"

func TestQueue_EnqueueAdvance_AcceptsUntilCapacity(t *testing.T) {
	q := NewQueue(2)

	if !q.EnqueueAdvance("run_1") {
		t.Fatal("enqueue 1/2: want accepted, got refused")
	}
	if !q.EnqueueAdvance("run_2") {
		t.Fatal("enqueue 2/2: want accepted, got refused")
	}

	// The queue is now at capacity. EnqueueAdvance must return false immediately rather
	// than block: a refused enqueue is an ordinary, expected outcome under load (queue.go
	// doc comment), never something the caller waits out.
	if q.EnqueueAdvance("run_3") {
		t.Fatal("enqueue at capacity: want refused (false), got accepted")
	}

	// Draining one slot must make room for exactly one more accepted item.
	<-q.items
	if !q.EnqueueAdvance("run_4") {
		t.Fatal("enqueue after drain: want accepted, got refused")
	}
	if q.EnqueueAdvance("run_5") {
		t.Fatal("enqueue immediately after refilling to capacity: want refused, got accepted")
	}
}

func TestNewQueue_NonPositiveCapacity_FallsBackToDefault(t *testing.T) {
	q := NewQueue(0)
	if cap(q.items) != defaultQueueCapacity {
		t.Fatalf("capacity with non-positive constructor arg: want default %d, got %d", defaultQueueCapacity, cap(q.items))
	}
}
