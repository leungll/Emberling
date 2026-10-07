package runtime

import "github.com/leungll/Emberling/backend/internal/domain"

// NodeRunState is the minimal, already-persisted-or-projected fact the Scheduler needs
// about one node's NodeRun. The Scheduler reasons only about dependency completion and
// node identity, never about execution input/output values
// (the Scheduler only answers what can run next; it writes no database rows and calls
// no Executor).
type NodeRunState struct {
	NodeID string
	Status domain.NodeRunStatus
}

// InitialReady returns the node ids runnable the moment a Run is created: the roots of
// the compiled DAG (no upstream dependency), in stable Order. It is the existing-map-less
// special case of NextReady.
func InitialReady(plan *CompiledDefinition) []string {
	return NextReady(plan, nil)
}

// NextReady returns, in stable Order, every node id that has become runnable given
// existing -- the NodeRunState already known for every node that currently has one.
// A node is runnable when it does not yet have a NodeRun and every one of its upstream
// dependencies has SUCCEEDED.
//
// existing may reflect projected state -- the state a transaction is about to make true,
// not necessarily what is committed on disk yet (computed from the state and output
// this transaction will have once it succeeds). NextReady never proposes a node id
// already present in existing, so it can never suggest a duplicate NodeRun
// ((run_id, node_id) is unique).
//
// Run-wide stop after failure: a FAILED NodeRun is terminal and MVP has no
// cancellation; this is a documented gap (the scheduling rules do not state it
// explicitly). Once any NodeRun in the Run is FAILED, the Run itself can never reach
// COMPLETED again, so NextReady returns nothing at all -- proposing new work on an
// unrelated branch would be dead work with no recovery path. This is a Run-wide rule,
// not a per-node dependency check: it does not matter whether the failed node is
// upstream of the node being considered.
func NextReady(plan *CompiledDefinition, existing map[string]NodeRunState) []string {
	for _, state := range existing {
		if state.Status == domain.NodeRunFailed {
			return nil
		}
	}

	var ready []string
	for _, nodeID := range plan.Order {
		if _, has := existing[nodeID]; has {
			continue
		}
		allUpstreamSucceeded := true
		for _, up := range plan.Upstream[nodeID] {
			state, ok := existing[up]
			if !ok || state.Status != domain.NodeRunSucceeded {
				allUpstreamSucceeded = false
				break
			}
		}
		if allUpstreamSucceeded {
			ready = append(ready, nodeID)
		}
	}
	return ready
}

// SelectNextToExecute picks which single ready node the MVP's one execution slot may
// claim next. MVP uses a manually authored static DAG that supports multiple concurrent
// READY/WAITING_CALLBACK NodeRuns, but the same Run calls only one Node Executor at a
// time. SelectNextToExecute therefore refuses to select anything while existing already
// contains a RUNNING NodeRun, and otherwise returns the first ready node in stable Order.
//
// This is a pure pick, not a claim: the caller must still win the READY -> RUNNING
// conditional update before invoking the Executor.
//
// Run-wide stop after failure: a FAILED NodeRun is terminal and MVP has no
// cancellation; this is a documented gap (the scheduling rules do not state it
// explicitly). Once any NodeRun in the Run is FAILED, SelectNextToExecute refuses to
// claim anything at all, even a NodeRun that reached READY before the failure landed:
// the Run can never reach COMPLETED again, so dispatching more work has no recovery path.
func SelectNextToExecute(plan *CompiledDefinition, existing map[string]NodeRunState, ready []string) (string, bool) {
	for _, state := range existing {
		if state.Status == domain.NodeRunRunning || state.Status == domain.NodeRunFailed {
			return "", false
		}
	}

	orderIndex := make(map[string]int, len(plan.Order))
	for i, id := range plan.Order {
		orderIndex[id] = i
	}

	best := ""
	bestIdx := -1
	for _, id := range ready {
		idx, ok := orderIndex[id]
		if !ok {
			continue
		}
		if bestIdx == -1 || idx < bestIdx {
			best = id
			bestIdx = idx
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}
