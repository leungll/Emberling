package service

import (
	"context"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the handling of an orphaned Node Attempt: a STARTED Attempt with no
// deadline, created by a process that no longer exists. Nobody will commit its outcome and
// no timeout will ever fire, so without this path its Run stays stuck on the single
// execution slot and its Provider slot stays taken.
//
// It is the Node counterpart of the orphaned Tool Attempt and ends the way a deadline
// would: the original call is never continued, the Attempt fails through the ordinary
// failure use case, and the registered side-effect policy alone decides whether a new
// Attempt may run. That covers an ASYNC Attempt too. While it is STARTED it has no Callback
// Binding, the Binding can only be written by the process holding the Provider's answer,
// and a callback with no Binding has no right to advance Execution, so no late delivery can
// resolve it. Once DISPATCHED an Attempt depends on no process at all, and it stays with
// its callback, poll and deadline.
//
// Runtime execution is a single process: a runner identity other than this process's means
// that process has gone, which is true only while no second Backend runs against the same
// database. Telling a dead process from a live one would take leases this Runtime does not
// have. Should the premise be broken, the conditional updates still leave one winner, but
// the Attempt of the live process would be failed under it.

// nodeOrphanError is what recovery knows about the call of an orphaned Node Attempt, by the
// classification orphaned Tool calls use, and whether its outcome is uncertain.
func nodeOrphanError(side domain.SideEffectPolicy, registered bool) (domain.ExecutionError, bool) {
	if classifyOrphan(side, registered) == orphanInterrupted {
		return domain.ExecutionError{
			Code:    codeExecutionInterrupted,
			Message: "the process running the node attempt stopped before it returned; running it again is safe",
		}, false
	}
	return domain.ExecutionError{
		Code:    codeResultUncertain,
		Message: "the process running the node attempt stopped; its outcome is unknown",
	}, true
}

// orphanedNode reports whether a Node Attempt is one recovery may take: still STARTED,
// created by a recorded process other than this one, and with no deadline. A deadline
// already bounds an Attempt through the Node timeout, which keeps ending it. An Attempt with
// no recorded process is never taken, because whether that process is alive cannot be known.
func (s *ExecutionService) orphanedNode(attempt domain.NodeAttempt) bool {
	return attempt.Status == domain.NodeAttemptStarted && attempt.DeadlineAt == nil &&
		attempt.RunnerID != nil && *attempt.RunnerID != "" && *attempt.RunnerID != s.deps.RunnerID
}

// HandleOrphanNodeAttempt resolves one orphaned Node Attempt. The Reconciler hands it every
// such Attempt it discovers; this use case owns the decision and failNode every write.
//
// The guard below reads outside the Run lock, because everything it reads is either
// immutable or only moves away from STARTED; a result, a timeout or another handling that
// commits after it loses nothing, since failNode's conditional STARTED -> FAILED update
// elects one winner. Whatever the outcome, Attempt, NodeRun, Provider slot, Events and Run
// aggregate commit together, and nothing external is called here.
func (s *ExecutionService) HandleOrphanNodeAttempt(ctx context.Context, attemptID string) error {
	var orphaned bool
	var execError domain.ExecutionError
	var uncertain bool

	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, attemptID)
		if err != nil {
			return err
		}
		if !s.orphanedNode(attempt) {
			return nil
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		reg, registered := s.deps.Nodes.Get(nodeRun.NodeType)
		orphaned = true
		execError, uncertain = nodeOrphanError(reg.Metadata.SideEffect, registered)
		return nil
	})
	if err != nil || !orphaned {
		return err
	}

	_, err = s.failNode(ctx, failNodeParams{
		attemptID:   attemptID,
		execError:   execError,
		uncertain:   uncertain,
		source:      domain.FailureRunnerLost,
		fromAttempt: domain.NodeAttemptStarted,
		fromNodeRun: domain.NodeRunRunning,
	})
	return err
}
