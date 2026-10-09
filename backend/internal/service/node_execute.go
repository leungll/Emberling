package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// Execute runs the claimed Node Attempt described by outcome. It must be called only
// after the transaction that produced outcome has committed: it resolves
// the Executor from the Node Registry, applies the deadline (if any) to ctx, calls
// Execute, and reports the result through CompleteNode or FailNode -- each of which is
// its own, separate transaction.
func (s *ExecutionService) Execute(ctx context.Context, outcome AdvanceOutcome) error {
	if !outcome.Claimed {
		return fmt.Errorf("execution: Execute called with an unclaimed AdvanceOutcome")
	}

	// A MANAGED_AGENT claim carries no Attempt and no Executor: the work its transaction
	// authorised is the READY Turn it created. claimSource is IMMEDIATE because this is
	// the path that just made the claim; the Reconciler enters the very same use case
	// with RECONCILER.
	if outcome.AgentTurnID != "" {
		return s.AdvanceAgentTurn(ctx, outcome.AgentTurnID, domain.ClaimImmediate)
	}

	reg, ok := s.deps.Nodes.Get(outcome.NodeType)
	if !ok {
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "NODE_TYPE_NOT_REGISTERED", Message: fmt.Sprintf("node type %q is not registered", outcome.NodeType)},
			Uncertain: false,
			Source:    domain.FailureSyncExecution,
		})
	}
	binding, ok := reg.Binding.(registry.ExecutorBinding)
	if !ok {
		// Every other binding kind is driven by its own use case and was routed above;
		// reaching here means a claimed NodeRun whose binding nothing can execute.
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "NODE_BINDING_NOT_EXECUTABLE", Message: fmt.Sprintf("node type %q is not an EXECUTOR binding", outcome.NodeType)},
			Uncertain: false,
			Source:    domain.FailureSyncExecution,
		})
	}

	execCtx := ctx
	if outcome.Deadline != nil {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithDeadline(ctx, *outcome.Deadline)
		defer cancel()
	}

	result, err := binding.Executor.Execute(execCtx, outcome.Input, outcome.Config)
	if err != nil {
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "EXECUTOR_ERROR", Message: err.Error()},
			Uncertain: isUncertainFailure(execCtx, err),
			Source:    domain.FailureSyncExecution,
		})
	}

	switch result.Kind {
	case registry.NodeResultCompleted:
		if result.Output == nil {
			return s.FailNode(ctx, FailNode{
				AttemptID: outcome.AttemptID,
				Error:     domain.ExecutionError{Code: "EXECUTOR_ERROR", Message: "executor reported COMPLETED with no Output"},
				Uncertain: false,
				Source:    domain.FailureSyncExecution,
			})
		}
		return s.CompleteNode(ctx, CompleteNode{
			AttemptID:  outcome.AttemptID,
			Output:     *result.Output,
			TokenUsage: result.TokenUsage,
		})
	case registry.NodeResultDispatched:
		return s.dispatchNode(ctx, outcome, result.ExternalTask)
	default:
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "UNKNOWN_RESULT_KIND", Message: string(result.Kind)},
			Uncertain: false,
			Source:    domain.FailureSyncExecution,
		})
	}
}

// isUncertainFailure classifies a failed Execute call, which decides whether the
// SideEffectPolicy may allow a retry at all. A context deadline is uncertain (the Executor
// was cut off mid-flight; whether the external side effect landed is unknown). Beyond
// that, an Adapter that knows whether its dispatch reached the Provider says so through an
// error exposing Uncertain() bool -- a connection reset before any response is uncertain, a
// rejected request is definite. Emberling reads that classification instead of guessing
// from the Go error type, and treats every unclassified error as definite.
func isUncertainFailure(ctx context.Context, err error) bool {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	var classified interface{ Uncertain() bool }
	if errors.As(err, &classified) {
		return classified.Uncertain()
	}
	return false
}

// nodeDispatchedPayload is the bounded record of a committed dispatch.
// providerId and externalTaskId are deliberately absent:
// they are projected from the Callback Binding and are not copied into Events.
type nodeDispatchedPayload struct {
	AttemptNo         int    `json:"attemptNo"`
	CallbackBindingID string `json:"callbackBindingId"`
}

// errDispatchBindingConflict rolls the dispatch transaction back when the external task id
// the Provider returned is already bound to another Attempt.
var errDispatchBindingConflict = errors.New("execution: callback binding conflict")

// dispatchNode commits the second phase of an async Node's three-phase dispatch:
// the Attempt becomes DISPATCHED, the NodeRun WAITING_CALLBACK, the Callback Binding that
// routes future callbacks is created and NODE_DISPATCHED is appended -- all in one
// transaction, so a callback can never find a route to a NodeRun that is not waiting yet.
// The Provider call itself already happened, outside any transaction and any Run lock.
func (s *ExecutionService) dispatchNode(ctx context.Context, outcome AdvanceOutcome, task *registry.ExternalTask) error {
	if task == nil || task.ExternalTaskID == "" || task.ProviderID == "" {
		// The Provider may have accepted the task even though nothing identifies it, so
		// the result of the external call is unknown and the registered SideEffectPolicy
		// -- not this code path -- decides whether another Attempt is allowed
		// (the Provider accepted the task but no external_task_id was saved).
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error: domain.ExecutionError{
				Code:    "DISPATCH_WITHOUT_EXTERNAL_TASK",
				Message: "executor reported DISPATCHED without a provider id and external task id",
			},
			Uncertain: true,
			Source:    domain.FailureSyncExecution,
		})
	}

	var (
		runID       string
		lastSeq     int64
		bindingID   string
		lostRace    bool
		committedAt time.Time
	)

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, outcome.AttemptID)
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
		plan, err := s.compile(ctx, def)
		if err != nil {
			return fmt.Errorf("execution: recompile definition %s v%d for dispatch: %w", run.WorkflowID, run.DefinitionVersion, err)
		}

		lock, err := tx.Runs().LockForUpdate(ctx, run.ID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()
		committedAt = now

		// A registration that declares a poll policy schedules its first poll one
		// interval after dispatch, as a scheduling fact in this same transaction; it
		// carries no Event. Registrations without a policy are never polled.
		var firstPollAt *time.Time
		if md, ok := s.deps.Nodes.NodeMetadata(nodeRun.NodeType); ok && md.Poll != nil {
			first := now.Add(time.Duration(md.Poll.IntervalMs) * time.Millisecond)
			firstPollAt = &first
		}

		if err := tx.NodeAttempts().MarkDispatched(ctx, attempt.ID, now, firstPollAt); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				// The Attempt is no longer STARTED: a timeout already expired it while the
				// Provider call was in flight. Nothing is written; the external task is
				// left to the Provider (Emberling promises no cancellation).
				lostRace = true
				return nil
			}
			return err
		}
		if err := tx.NodeRuns().MarkWaiting(ctx, nodeRun.ID, now); err != nil {
			return err
		}

		bindingID = s.deps.IDs.NewID(domain.IDPrefixCallbackBinding)
		if err := tx.CallbackBindings().Create(ctx, domain.CallbackBinding{
			ID:             bindingID,
			ProviderID:     task.ProviderID,
			ExternalTaskID: task.ExternalTaskID,
			TargetType:     domain.CallbackTargetNodeAttempt,
			TargetID:       attempt.ID,
			CreatedAt:      now,
		}); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				return errDispatchBindingConflict
			}
			return err
		}

		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeDispatched, now, nodeDispatchedPayload{
			AttemptNo:         attempt.AttemptNo,
			CallbackBindingID: bindingID,
		}); err != nil {
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
		newStatus := runtime.AggregateRunStatus(runtime.RunAggregateInput{
			NodeStatuses:   nodeStatuses,
			AllNodeIDs:     plan.Order,
			OutputNodeID:   plan.OutputNodeID,
			OutputProduced: len(run.Output) > 0,
		})
		if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), newStatus); changed {
			if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: newStatus}); err != nil {
				return err
			}
		}
		if err := tx.Runs().UpdateAggregate(ctx, lock, newStatus, now); err != nil {
			return err
		}
		runID = run.ID
		lastSeq = lock.LastSeq()
		return nil
	})
	if errors.Is(err, errDispatchBindingConflict) {
		// The external task id is already bound to another Attempt, so this dispatch has no
		// route home and the transaction above is rolled back (the Attempt is still
		// STARTED). This is a definite failure of this Attempt: re-dispatching would
		// produce the same conflict. Documented edge; no automatic recovery is invented.
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error: domain.ExecutionError{
				Code:    "CALLBACK_BINDING_CONFLICT",
				Message: fmt.Sprintf("external task id %q is already bound to another Attempt", task.ExternalTaskID),
			},
			Uncertain: false,
			Source:    domain.FailureSyncExecution,
		})
	}
	if err != nil {
		return err
	}
	if lostRace || runID == "" {
		return nil
	}

	s.postCommit(runID, lastSeq)

	// Only now that the Binding is committed can a callback that arrived first be routed.
	// The stored credential hash decides whether that early delivery
	// really belongs to this Attempt.
	s.consumeEarlyCallback(ctx, task.ExternalTaskID, outcome.AttemptID, committedAt)
	return nil
}

// consumeEarlyCallback resumes this Attempt from a Pending Callback recorded before its
// Binding committed. Failures are logged rather than returned: the dispatch itself is
// committed, and the Reconciler rediscovers the same persisted pending row.
func (s *ExecutionService) consumeEarlyCallback(ctx context.Context, externalTaskID, attemptID string, now time.Time) {
	var pending domain.PendingCallback
	var found bool

	if err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		row, err := tx.PendingCallbacks().GetByExternalTaskID(ctx, externalTaskID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		if row.ConsumedAt != nil || !now.Before(row.ExpiresAt) {
			return nil
		}
		pending, found = row, true
		return nil
	}); err != nil {
		s.deps.Logger.Warn("read pending callback after dispatch",
			slog.String("external_task_id", externalTaskID),
			slog.String("attempt_id", attemptID),
			slog.String("error", err.Error()))
		return
	}
	if !found {
		return
	}

	// Whether this stored delivery's credential actually belongs to attemptID is decided
	// inside the resume transaction below (consumePendingCallback), which is also where a
	// competing consumer could win the row first. Checking it again here would only be a
	// second, racy opinion.
	if _, err := s.ResumeNode(ctx, ResumeNode{
		ExternalTaskID: externalTaskID,
		Payload:        pending.Payload,
		Source:         domain.CompletionCallback,
		ConsumePending: true,
		PayloadHash:    pending.PayloadHash,
	}); err != nil {
		var rejected *CallbackPayloadRejectedError
		if errors.As(err, &rejected) {
			// The Executor could not interpret the stored body; the NodeRun stays
			// WAITING_CALLBACK, exactly as for a live callback.
			return
		}
		var mismatch *PendingCallbackCredentialMismatchError
		if errors.As(err, &mismatch) {
			// The stored delivery was authenticated with a credential that is not this
			// Attempt's. It is left untouched (never logged with its hash) for its own owner
			// or for expiry to clean up.
			s.deps.Logger.Warn("pending callback credential does not match the dispatched attempt",
				slog.String("external_task_id", externalTaskID),
				slog.String("attempt_id", attemptID))
			return
		}
		s.deps.Logger.Warn("resume from pending callback after dispatch",
			slog.String("external_task_id", externalTaskID),
			slog.String("attempt_id", attemptID),
			slog.String("error", err.Error()))
	}
}
