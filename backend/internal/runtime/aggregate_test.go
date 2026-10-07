package runtime

import (
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// TestAggregate_Priority_FailedBeatsRunning asserts the aggregation priority table: a Run
// with one FAILED NodeRun and one RUNNING NodeRun aggregates to FAILED, not RUNNING.
func TestAggregate_Priority_FailedBeatsRunning(t *testing.T) {
	in := RunAggregateInput{
		NodeStatuses: map[string]domain.NodeRunStatus{
			"a": domain.NodeRunFailed,
			"b": domain.NodeRunRunning,
		},
		AllNodeIDs:   []string{"a", "b"},
		OutputNodeID: "b",
	}

	got := AggregateRunStatus(in)
	if got != domain.RunFailed {
		t.Errorf("AggregateRunStatus() = %q, want %q", got, domain.RunFailed)
	}
}

// TestAggregate_OnlyWaiting_IsPaused asserts a Run whose every NodeRun sits at
// WAITING_CALLBACK (no READY/RUNNING, no FAILED) aggregates to PAUSED.
func TestAggregate_OnlyWaiting_IsPaused(t *testing.T) {
	in := RunAggregateInput{
		NodeStatuses: map[string]domain.NodeRunStatus{
			"a": domain.NodeRunSucceeded,
			"b": domain.NodeRunWaitingCallback,
		},
		AllNodeIDs:   []string{"a", "b"},
		OutputNodeID: "b",
	}

	got := AggregateRunStatus(in)
	if got != domain.RunPaused {
		t.Errorf("AggregateRunStatus() = %q, want %q", got, domain.RunPaused)
	}
}

// TestAggregate_WaitingPlusReady_IsRunningNotPaused asserts that a READY NodeRun
// alongside a WAITING_CALLBACK one still aggregates to RUNNING: MVP allows several
// concurrent READY/WAITING_CALLBACK NodeRuns, and PAUSED only applies when nothing
// besides WAITING_CALLBACK remains.
func TestAggregate_WaitingPlusReady_IsRunningNotPaused(t *testing.T) {
	in := RunAggregateInput{
		NodeStatuses: map[string]domain.NodeRunStatus{
			"a": domain.NodeRunWaitingCallback,
			"b": domain.NodeRunReady,
		},
		AllNodeIDs:   []string{"a", "b"},
		OutputNodeID: "b",
	}

	got := AggregateRunStatus(in)
	if got != domain.RunRunning {
		t.Errorf("AggregateRunStatus() = %q, want %q", got, domain.RunRunning)
	}
}

// TestAggregate_RunningPlusWaiting_IsRunningNotPaused asserts the Run-status derivation
// rule for one combination: a RUNNING NodeRun alongside a WAITING_CALLBACK one aggregates
// to RUNNING, never PAUSED -- a Run must not become PAUSED because one node is waiting
// while another is actively executing.
func TestAggregate_RunningPlusWaiting_IsRunningNotPaused(t *testing.T) {
	in := RunAggregateInput{
		NodeStatuses: map[string]domain.NodeRunStatus{
			"a": domain.NodeRunRunning,
			"b": domain.NodeRunWaitingCallback,
		},
		AllNodeIDs:   []string{"a", "b"},
		OutputNodeID: "b",
	}

	got := AggregateRunStatus(in)
	if got != domain.RunRunning {
		t.Errorf("AggregateRunStatus() = %q, want %q", got, domain.RunRunning)
	}
}

// TestAggregate_AllSucceededWithoutOutput_NotCompleted asserts COMPLETED requires both
// every node SUCCEEDED and the Output Node's result written to Run.output
// -- the former alone must not report COMPLETED.
func TestAggregate_AllSucceededWithoutOutput_NotCompleted(t *testing.T) {
	in := RunAggregateInput{
		NodeStatuses: map[string]domain.NodeRunStatus{
			"a": domain.NodeRunSucceeded,
			"b": domain.NodeRunSucceeded,
		},
		AllNodeIDs:     []string{"a", "b"},
		OutputNodeID:   "b",
		OutputProduced: false,
	}

	got := AggregateRunStatus(in)
	if got == domain.RunCompleted {
		t.Errorf("AggregateRunStatus() = %q, want anything but COMPLETED without OutputProduced", got)
	}
}

// TestAggregate_SucceededDoesNotMaskOtherStatuses pins that a SUCCEEDED NodeRun never
// decides the aggregate on its own: alongside FAILED, READY or WAITING_CALLBACK the Run
// takes that status's aggregate.
func TestAggregate_SucceededDoesNotMaskOtherStatuses(t *testing.T) {
	cases := []struct {
		other domain.NodeRunStatus
		want  domain.RunStatus
	}{
		{domain.NodeRunFailed, domain.RunFailed},
		{domain.NodeRunReady, domain.RunRunning},
		{domain.NodeRunWaitingCallback, domain.RunPaused},
	}
	for _, tc := range cases {
		in := RunAggregateInput{
			NodeStatuses:   map[string]domain.NodeRunStatus{"a": domain.NodeRunSucceeded, "b": tc.other},
			AllNodeIDs:     []string{"a", "b"},
			OutputNodeID:   "b",
			OutputProduced: true,
		}
		if got := AggregateRunStatus(in); got != tc.want {
			t.Errorf("AggregateRunStatus(SUCCEEDED + %s) = %q, want %q", tc.other, got, tc.want)
		}
	}
}

// TestAggregate_MissingNodeRunsNothingActive_IsRunning asserts the other half of the
// "every node has a NodeRun" precondition for COMPLETED: when AllNodeIDs names a node
// that has no NodeRun at all yet (not merely one that hasn't succeeded), and every node
// that *does* have a NodeRun has SUCCEEDED, with OutputProduced still false, the Run must
// aggregate to RUNNING, not COMPLETED -- the missing node's NodeRun has not even been
// created yet, so treating it as done would be wrong, and neither FAILED nor WAITING is
// present to justify any other status.
func TestAggregate_MissingNodeRunsNothingActive_IsRunning(t *testing.T) {
	in := RunAggregateInput{
		NodeStatuses: map[string]domain.NodeRunStatus{
			"a": domain.NodeRunSucceeded,
		},
		AllNodeIDs:     []string{"a", "b"}, // "b" has no NodeRun yet
		OutputNodeID:   "b",
		OutputProduced: false,
	}

	got := AggregateRunStatus(in)
	if got != domain.RunRunning {
		t.Errorf("AggregateRunStatus() = %q, want %q", got, domain.RunRunning)
	}
}

// TestAggregate_AllSucceededWithOutput_IsCompleted is the positive control: once every
// node has SUCCEEDED and OutputProduced is true, the Run aggregates to COMPLETED.
func TestAggregate_AllSucceededWithOutput_IsCompleted(t *testing.T) {
	in := RunAggregateInput{
		NodeStatuses: map[string]domain.NodeRunStatus{
			"a": domain.NodeRunSucceeded,
			"b": domain.NodeRunSucceeded,
		},
		AllNodeIDs:     []string{"a", "b"},
		OutputNodeID:   "b",
		OutputProduced: true,
	}

	got := AggregateRunStatus(in)
	if got != domain.RunCompleted {
		t.Errorf("AggregateRunStatus() = %q, want %q", got, domain.RunCompleted)
	}
}

// TestRunTransitionEvent_NoEventOnSameStatus asserts no Event is reported when the
// aggregated status has not actually changed: a RUN_* Event is written only when the
// aggregated status changes.
func TestRunTransitionEvent_NoEventOnSameStatus(t *testing.T) {
	_, ok := NextRunTransitionEvent(domain.RunRunning, domain.RunRunning)
	if ok {
		t.Error("NextRunTransitionEvent() = ok, want no event when from == to")
	}
}

// TestRunTransitionEvent_PausedToRunning_IsResumed asserts a PAUSED -> RUNNING transition
// (a callback arriving while the Run was paused) reports RUN_RESUMED, not a generic
// RUN_CREATED-shaped event.
func TestRunTransitionEvent_PausedToRunning_IsResumed(t *testing.T) {
	got, ok := NextRunTransitionEvent(domain.RunPaused, domain.RunRunning)
	if !ok {
		t.Fatal("NextRunTransitionEvent() = not ok, want an event")
	}
	if got.Type != domain.EventRunResumed {
		t.Errorf("Type = %q, want %q", got.Type, domain.EventRunResumed)
	}
}

// TestRunTransitionEvent_RunningToFailed_IsRunFailed is the positive control for a
// terminal-failure transition.
func TestRunTransitionEvent_RunningToFailed_IsRunFailed(t *testing.T) {
	got, ok := NextRunTransitionEvent(domain.RunRunning, domain.RunFailed)
	if !ok {
		t.Fatal("NextRunTransitionEvent() = not ok, want an event")
	}
	if got.Type != domain.EventRunFailed {
		t.Errorf("Type = %q, want %q", got.Type, domain.EventRunFailed)
	}
}
