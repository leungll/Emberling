package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// FailNode is the parameter struct for ExecutionService.FailNode.
type FailNode struct {
	AttemptID string
	Error     domain.ExecutionError
	Uncertain bool
	Source    domain.FailureSource
}

type nodeRetryingPayload struct {
	AttemptNo     int                   `json:"attemptNo"`
	Error         domain.ExecutionError `json:"error"`
	NextAttemptAt time.Time             `json:"nextAttemptAt"`
}

// nodeFailedPayload is NODE_FAILED, with the same two
// variants as nodeStartedPayload: attemptNo for an ordinary node, agentRunId for an Agent
// node (absent as well when the Agent NodeRun failed before its Agent Run was created).
type nodeFailedPayload struct {
	AttemptNo  int                   `json:"attemptNo,omitempty"`
	AgentRunID *string               `json:"agentRunId,omitempty"`
	Error      domain.ExecutionError `json:"error"`
}

// FailNode records a failed Node execution result in one transaction. It applies
// runtime.DecideRetry using the node's ExecutionPolicy and its registered
// SideEffectPolicy: a retryable failure schedules the next Attempt and keeps the NodeRun
// RUNNING (NODE_RETRYING); otherwise the NodeRun fails, the Run records the error, and
// the Run re-aggregates (to RUN_FAILED, since any FAILED NodeRun dominates). It never
// sleeps: NextAttemptAt is a persisted fact the Reconciler and Advance both consult, not
// something this call waits on.
func (s *ExecutionService) FailNode(ctx context.Context, req FailNode) error {
	_, err := s.failNode(ctx, failNodeParams{
		attemptID:   req.AttemptID,
		execError:   req.Error,
		uncertain:   req.Uncertain,
		source:      req.Source,
		fromAttempt: domain.NodeAttemptStarted,
		fromNodeRun: domain.NodeRunRunning,
	})
	return err
}

// failNodeParams is the internal, parameterised form of a failure. A synchronous failure
// resolves a STARTED Attempt of a RUNNING NodeRun; a Provider failure delivered by
// callback/poll and a DISPATCHED-Attempt timeout resolve a DISPATCHED Attempt of a
// WAITING_CALLBACK NodeRun. The retry decision, Event writing and aggregation are shared.
type failNodeParams struct {
	attemptID   string
	execError   domain.ExecutionError
	uncertain   bool
	source      domain.FailureSource
	fromAttempt domain.NodeAttemptStatus
	fromNodeRun domain.NodeRunStatus
	// consumePending mirrors completeNodeParams: a failure delivered through a stored
	// early callback consumes that row in the same transaction.
	consumePending string
	payloadHash    string
	// terminal skips the retry decision entirely. It is set for a Provider-reported
	// failure of a task that is already waiting: such a failure marks Attempt and NodeRun
	// FAILED, and MVP never re-dispatches an external task that reached
	// WAITING_CALLBACK, even when the ExecutionPolicy still has attempts left and the
	// SideEffectPolicy would allow a keyed retry.
	terminal bool
}

func (s *ExecutionService) failNode(ctx context.Context, p failNodeParams) (nodeOutcomeResult, error) {
	var result nodeOutcomeResult
	var runID string
	var lastSeq int64

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, p.attemptID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		run, err := tx.Runs().Get(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		def, err := tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
		if err != nil {
			return err
		}

		// Recompiling a previously-valid, frozen Definition can fail here for the same
		// registry-drift reason documented on Advance above: the very Node Type this
		// Attempt is failing for may be the one the Registry just dropped (Execute's "Node
		// Type not registered" -> FailNode call is exactly that case). Aggregation below
		// only needs plan.Order/OutputNodeID on the branch where no NodeRun has failed --
		// impossible here, since this call is about to mark one FAILED -- so a
		// registry-drift CompileError does not abort the failure itself; it only means
		// plan stays nil and the aggregation call further down passes zero values instead.
		plan, planErr := s.compile(ctx, def)
		if planErr != nil {
			if _, ok := runtime.AsCompileError(planErr); !ok {
				return fmt.Errorf("execution: recompile definition %s v%d for fail: %w", run.WorkflowID, run.DefinitionVersion, planErr)
			}
			plan = nil
		}

		lock, err := tx.Runs().LockForUpdate(ctx, run.ID)
		if err != nil {
			return err
		}

		now := s.deps.Clock.Now()

		if err := s.consumePendingCallback(ctx, tx, attempt.ID, attempt.CallbackTokenHash, p.consumePending, p.payloadHash, now); err != nil {
			return err
		}

		if err := tx.NodeAttempts().MarkFailed(ctx, attempt.ID, p.fromAttempt, now, p.execError); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				// Late/duplicate failure of an Attempt already resolved elsewhere
				// (e.g. it already succeeded, or a competing callback won). Ignored, not
				// an error: the loser must write nothing.
				result.duplicate = true
				return nil
			}
			return err
		}

		node, ok := nodeByID(def, nodeRun.NodeID)
		if !ok {
			return fmt.Errorf("execution: definition %s v%d no longer declares node %q", def.WorkflowID, def.Version, nodeRun.NodeID)
		}
		reg, regOK := s.deps.Nodes.Get(nodeRun.NodeType)
		var sideEffect domain.SideEffectPolicy
		if regOK {
			sideEffect = reg.Metadata.SideEffect
		}

		decision := runtime.DecideRetry(runtime.RetryInput{
			Policy:     effectivePolicy(node),
			SideEffect: sideEffect,
			AttemptNo:  attempt.AttemptNo,
			// Only an EXTERNAL+KEYED node (image_generation in the MVP) carries a Provider
			// idempotency key: the NodeRun id, which startAttempt puts into NodeInput and
			// which is stable across every Attempt of the same NodeRun, so a retried
			// dispatch presents the exact same key to the Provider. Reporting it here is
			// what lets runtime.DecideRetry allow a retry of a keyed external call at all
			// (SideEffectPolicy); EXTERNAL+UNKNOWN still refuses.
			HasIdempotencyKey: hasIdempotencyKey(sideEffect),
			ResultUncertain:   p.uncertain,
		})
		if !regOK {
			// The Node Type is no longer registered at all (the Registry holds only a
			// similar but incompatible implementation, or the implementation is missing
			// while recovering an existing Run): there is
			// no SideEffectPolicy left to consult, and CLAUDE.md "Extensions and external
			// calls" forbids substituting a heuristic in its place. Fail outright regardless
			// of what DecideRetry computed off the zero-value SideEffectPolicy above.
			decision.Retry = false
		}
		if p.terminal || p.fromNodeRun == domain.NodeRunWaitingCallback {
			// MVP never automatically re-dispatches an external task that entered
			// WAITING_CALLBACK, and the NodeRun state machine leaves a waiting NodeRun only
			// for SUCCEEDED or FAILED. The
			// rule is operative rather than conservative: the external task was already
			// accepted under this NodeRun's Provider idempotency key and external task id, so
			// a re-dispatch could not be routed anyway -- an EXTERNAL+KEYED retry would
			// collide on the existing Callback Binding, and an EXTERNAL+UNKNOWN one must not
			// be repeated at all. Whatever the ExecutionPolicy and the SideEffectPolicy allow
			// for a failure before dispatch, a failure after it is terminal.
			decision.Retry = false
		}

		if decision.Retry {
			// A retry always runs as a new Attempt of a NodeRun that is still RUNNING: only
			// a failure before dispatch can retry, and that NodeRun never left RUNNING.
			nextAttemptAt := now.Add(decision.NextDelay)
			if err := tx.NodeRuns().ScheduleRetry(ctx, nodeRun.ID, nextAttemptAt, now); err != nil {
				return err
			}
			if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeRetrying, now, nodeRetryingPayload{
				AttemptNo: attempt.AttemptNo, Error: p.execError, NextAttemptAt: nextAttemptAt,
			}); err != nil {
				return err
			}
			// The NodeRun is RUNNING again; a Run that had been PAUSED by this NodeRun
			// resumes, which NextRunTransitionEvent turns into RUN_RESUMED. For a
			// synchronous retry the Run was already RUNNING and nothing is written.
			if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), domain.RunRunning); changed {
				if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: domain.RunRunning}); err != nil {
					return err
				}
			}
			if err := tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, now); err != nil {
				return err
			}
			runID = run.ID
			result.runID = run.ID
			result.nodeRunID = nodeRun.ID
			lastSeq = lock.LastSeq()
			return nil
		}

		if err := tx.NodeRuns().MarkFailed(ctx, nodeRun.ID, p.fromNodeRun, now, p.execError); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) && p.fromNodeRun == domain.NodeRunWaitingCallback {
				return errResumeSuperseded
			}
			return err
		}
		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeFailed, now, nodeFailedPayload{
			AttemptNo: attempt.AttemptNo, Error: p.execError,
		}); err != nil {
			return err
		}
		if err := tx.Runs().SetError(ctx, lock, p.execError); err != nil {
			return err
		}

		allNodeRuns, err := tx.NodeRuns().ListByRun(ctx, run.ID)
		if err != nil {
			return err
		}
		nodeStatuses := make(map[string]domain.NodeRunStatus, len(allNodeRuns))
		for _, nr := range allNodeRuns {
			nodeStatuses[nr.NodeID] = nr.Status
		}
		// plan is nil exactly when recompilation hit registry drift (above): nodeStatuses
		// already records the NodeRun this call just marked FAILED, so
		// AggregateRunStatus's hasFailed short-circuit always yields RunFailed here
		// regardless of AllNodeIDs/OutputNodeID -- passing their zero values is safe.
		var allNodeIDs []string
		var outputNodeID string
		if plan != nil {
			allNodeIDs = plan.Order
			outputNodeID = plan.OutputNodeID
		}
		newStatus := runtime.AggregateRunStatus(runtime.RunAggregateInput{
			NodeStatuses:   nodeStatuses,
			AllNodeIDs:     allNodeIDs,
			OutputNodeID:   outputNodeID,
			OutputProduced: len(run.Output) > 0,
		})
		if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), newStatus); changed {
			errCopy := p.execError
			if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: newStatus, Error: &errCopy}); err != nil {
				return err
			}
		}
		if err := tx.Runs().UpdateAggregate(ctx, lock, newStatus, now); err != nil {
			return err
		}
		runID = run.ID
		result.runID = run.ID
		result.nodeRunID = nodeRun.ID
		lastSeq = lock.LastSeq()
		return nil
	})
	if errors.Is(err, errResumeSuperseded) {
		return nodeOutcomeResult{duplicate: true}, nil
	}
	if err != nil {
		return nodeOutcomeResult{}, err
	}
	if runID != "" {
		s.postCommit(runID, lastSeq)
	}
	return result, nil
}
