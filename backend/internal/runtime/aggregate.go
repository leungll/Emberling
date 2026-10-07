package runtime

import "github.com/leungll/Emberling/backend/internal/domain"

// RunAggregateInput is the minimal, already-committed-or-projected fact set
// AggregateRunStatus needs. A Run has no independent status of its own: it is always
// recomputed from NodeRun state.
type RunAggregateInput struct {
	// NodeStatuses is the status of every node that currently has a NodeRun, keyed by
	// node id. It reflects projected state, the same convention the Scheduler uses.
	NodeStatuses map[string]domain.NodeRunStatus
	// AllNodeIDs is every node id the compiled Definition declares. Comparing its length
	// against NodeStatuses tells "every node has a NodeRun and all SUCCEEDED" apart from
	// "some node has no NodeRun yet".
	AllNodeIDs []string
	// OutputNodeID is the Definition's single Output Node.
	OutputNodeID string
	// OutputProduced reports whether the Output Node's result has been written to
	// Run.output. Whether/when that write happens is a service-level concern; this
	// package only consumes the fact.
	OutputProduced bool
}

// AggregateRunStatus computes a Run's status from NodeRun state, applying the MVP
// priority: FAILED beats RUNNING beats PAUSED
// beats COMPLETED.
func AggregateRunStatus(in RunAggregateInput) domain.RunStatus {
	hasReadyOrRunning := false
	hasWaiting := false
	hasFailed := false

	for _, status := range in.NodeStatuses {
		switch status {
		case domain.NodeRunFailed:
			hasFailed = true
		case domain.NodeRunReady, domain.NodeRunRunning:
			hasReadyOrRunning = true
		case domain.NodeRunWaitingCallback:
			hasWaiting = true
		case domain.NodeRunSucceeded:
			// Decides nothing on its own; allSucceeded checks completion below.
		}
	}

	switch {
	case hasFailed:
		return domain.RunFailed
	case hasReadyOrRunning:
		return domain.RunRunning
	case hasWaiting:
		return domain.RunPaused
	}

	if allSucceeded(in) && in.OutputProduced {
		return domain.RunCompleted
	}
	// Every known NodeRun has succeeded, but the Definition still has nodes with no
	// NodeRun yet, or the Output Node has not produced Run.output. Neither FAILED nor
	// WAITING is present, so RUNNING is the only remaining non-terminal status in the
	// Run status table; reporting COMPLETED here would be premature.
	return domain.RunRunning
}

func allSucceeded(in RunAggregateInput) bool {
	if len(in.NodeStatuses) != len(in.AllNodeIDs) {
		return false
	}
	for _, id := range in.AllNodeIDs {
		if in.NodeStatuses[id] != domain.NodeRunSucceeded {
			return false
		}
	}
	return true
}

// RunTransitionEvent is the Event a caller must additionally record when aggregated Run
// status actually changes (only an actual aggregate status change writes the
// corresponding RUN_* Event). It carries no Run id, seq or timestamp: those are assigned
// by store when the Event is persisted, not decided here.
type RunTransitionEvent struct {
	Type domain.EventType
}

// NextRunTransitionEvent returns the Event to record for a from -> to Run status change,
// or false if from == to (no observed change, so no Event). PAUSED -> RUNNING reports
// RUN_RESUMED (the Agent-with-async-tool sequence resumes this way).
// Every "to" that is not one the Run status machine actually reaches from a live Run --
// notably RUNNING reached from anything other than PAUSED, which is Run creation, not a
// transition of an existing Run -- reports no Event, since that case belongs to Run
// creation, not this package.
func NextRunTransitionEvent(from, to domain.RunStatus) (RunTransitionEvent, bool) {
	if from == to {
		return RunTransitionEvent{}, false
	}
	switch to {
	case domain.RunFailed:
		return RunTransitionEvent{Type: domain.EventRunFailed}, true
	case domain.RunCompleted:
		return RunTransitionEvent{Type: domain.EventRunCompleted}, true
	case domain.RunPaused:
		return RunTransitionEvent{Type: domain.EventRunPaused}, true
	case domain.RunRunning:
		if from == domain.RunPaused {
			return RunTransitionEvent{Type: domain.EventRunResumed}, true
		}
		return RunTransitionEvent{}, false
	default:
		return RunTransitionEvent{}, false
	}
}
