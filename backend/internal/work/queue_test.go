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

// TestQueue_EnqueueAgentTurn_SharesTheBoundAndCarriesTypedIDs covers 06 §2.1: an Agent Turn
// item carries only its work type, the persisted Turn ID and the Run ID, and competes for
// the same bounded capacity as a Run item -- a full queue refuses it without blocking.
func TestQueue_EnqueueAgentTurn_SharesTheBoundAndCarriesTypedIDs(t *testing.T) {
	q := NewQueue(2)
	if !q.EnqueueAdvance("run_1") {
		t.Fatal("enqueue run item: want accepted")
	}
	if !q.EnqueueAgentTurn("run_2", "turn_2") {
		t.Fatal("enqueue agent turn item: want accepted")
	}
	if q.EnqueueAgentTurn("run_3", "turn_3") {
		t.Fatal("enqueue agent turn at capacity: want refused, got accepted")
	}

	if first := <-q.items; first != (Item{Kind: ItemAdvanceRun, RunID: "run_1"}) {
		t.Errorf("first item = %+v, want a Run advance item for run_1", first)
	}
	if second := <-q.items; second != (Item{Kind: ItemAgentTurn, RunID: "run_2", TurnID: "turn_2"}) {
		t.Errorf("second item = %+v, want an AGENT_TURN item for turn_2 of run_2", second)
	}
}
