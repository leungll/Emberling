package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the post-task reconciliation of a release target: the Run ended, by any
// path, while its release outcome was unknown, so the occupation still holds the target and
// carries its own query schedule. Each due query is claimed by a conditional update of the
// occupation's schedule, production is asked once after COMMIT, and only a definite answer
// -- DEPLOYED, VOIDED or a refusal -- frees the target, in a transaction that writes the
// occupation row alone. An answer that production does not know the operation records when
// that was first learned on the release Attempt, which is what the void grace counts from.
// The ended Run gets no Event and no state change: its outcome was committed when it ended,
// and a late answer is a fact about the target and the operation, not about the task.
//
// A release Attempt the Run left open stays open. It may be STARTED, when no worker made the
// call or the call gave up on the ended Run, or DISPATCHED, and no path ever closes it: its
// status records how far the task got, and the occupation records how the operation ended.

// Post-task query backoff: the first retry waits the release Tool's poll interval (or the
// fallback when the registration declares none), each later one doubles, up to the cap.
const (
	postReconcileFallbackInterval = 2 * time.Second
	postReconcileMaxInterval      = 5 * time.Minute
	postReconcileMaxDoublings     = 16
)

// ReleaseReconcileOutcome reports one post-task reconciliation call. Claimed is false when
// the query was not due, another scanner claimed it, or the occupation no longer qualifies;
// production was then not asked. Released is true when production's definite answer freed
// the target, with Reason saying which answer.
type ReleaseReconcileOutcome struct {
	Claimed  bool
	Released bool
	Reason   domain.ReleaseReason
	// UnknownRecorded is true when this answer was the first to say production does not
	// know the operation, and its time was written on the release Attempt.
	UnknownRecorded bool
}

// releaseReconcileClaim is what the claim transaction hands to the production query.
type releaseReconcileClaim struct {
	occupationID string
	runID        string
	attemptID    string
	executor     registry.ReconcilableToolExecutor
	state        registry.ToolAsyncState
	void         bool
}

// ReconcileReleaseOccupation asks production once about the operation an ACTIVE occupation
// of an ended Run names, and frees the target when the answer is definite.
//
// The claim transaction takes the Run lock (the fixed order: Run first, then the
// occupation row), re-checks that the occupation is ACTIVE, due and belongs to an ended
// Run, decides between a query and a void request, and moves next_query_at forward by a
// bounded backoff with a conditional update; two scanners of the same occupation therefore
// have one winner, and only the winner asks production. A void is requested instead of a
// query once the operation's outcome has been unknown for the node's void grace -- since
// production last failed to know it, or since the Attempt was failed with an uncertain
// outcome -- exactly as a live release voids, and a void never leads to a new release.
//
// After COMMIT production is asked once, plus one query when a void goes unconfirmed.
// DEPLOYED is read back against the target, and the
// occupation is released as DEPLOYED with the version the target runs recorded as live but
// unchecked; VOIDED releases it as VOIDED; production's definite refusal of the operation,
// which the live path would hand back to the Agent, releases it as REFUSED. An answer the
// live poll path classifies as an unknown operation writes the Attempt's
// outcome_unknown_since once, whatever open or uncertain state the Attempt is in, without
// an Event. An operation production still has in flight, or no answer, only waits for the
// next query.
//
// A permanent fault found before the Run lock -- the release Tool no longer registered,
// the node's release bounds unreadable, no Attempt under the operation key -- cannot be
// fixed by asking again soon: the claim still moves the schedule by the same backoff, and
// the fault is logged once per claim instead of failing every reconciler pass.
func (s *ExecutionService) ReconcileReleaseOccupation(ctx context.Context, occupationID string) (ReleaseReconcileOutcome, error) {
	claim, err := s.claimReleaseReconcile(ctx, occupationID)
	if err != nil || claim == nil {
		return ReleaseReconcileOutcome{}, err
	}
	outcome := ReleaseReconcileOutcome{Claimed: true}
	if hook := s.deps.PollHooks.AfterClaim; hook != nil {
		if err := hook(ctx, claim.attemptID); err != nil {
			return outcome, err
		}
	}

	var result registry.PollResult
	voidAnswer := claim.void
	if claim.void {
		result, err = claim.executor.Void(ctx, claim.state)
		if err != nil || result.Status == registry.PollStatusUnknown {
			// An unconfirmed void says nothing about the operation, and the grace that
			// chose it never restarts, so without a query here a deployment production
			// finished later could never be learned. A query only reads.
			voidAnswer = false
			result, err = claim.executor.Poll(ctx, claim.state)
		}
	} else {
		result, err = claim.executor.Poll(ctx, claim.state)
	}
	if err != nil {
		// No answer: the claim already scheduled the next query.
		return outcome, fmt.Errorf("execution: reconcile occupation %s under %s: %w", occupationID, claim.state.ExternalTask.ExternalTaskID, err)
	}
	if hook := s.deps.PollHooks.AfterAnswer; hook != nil {
		if err := hook(ctx, claim.attemptID); err != nil {
			return outcome, err
		}
	}

	switch {
	case result.Status == registry.PollSucceeded:
		var deployed deployedOperation
		if result.ToolResult == nil || json.Unmarshal(result.ToolResult.Output, &deployed) != nil || deployed.Version == "" {
			return outcome, fmt.Errorf("execution: reconcile occupation %s: production reported DEPLOYED without a version", occupationID)
		}
		target, err := claim.executor.CheckTarget(ctx, claim.state)
		if err != nil {
			return outcome, fmt.Errorf("execution: reconcile occupation %s: check target: %w", occupationID, err)
		}
		version := deployed.Version
		if target.Known {
			version = target.Version
		}
		released, err := s.releaseOccupationAfterRun(ctx, claim, domain.ReleaseReasonDeployed, &version)
		if err != nil {
			return outcome, err
		}
		outcome.Released, outcome.Reason = released, domain.ReleaseReasonDeployed
		return outcome, nil
	case result.Status == registry.PollUnknownTask:
		recorded, err := s.recordReleaseUnknownAfterRun(ctx, claim)
		if err != nil {
			return outcome, err
		}
		outcome.UnknownRecorded = recorded
		return outcome, nil
	case result.Status == registry.PollFailed && result.Error != nil && result.Error.Code == domain.ReleaseCodeVoided:
		released, err := s.releaseOccupationAfterRun(ctx, claim, domain.ReleaseReasonVoided, nil)
		if err != nil {
			return outcome, err
		}
		outcome.Released, outcome.Reason = released, domain.ReleaseReasonVoided
		return outcome, nil
	case result.Status == registry.PollFailed && result.Error != nil && !voidAnswer:
		// The same answer the live poll path takes for a refusal: nothing was deployed. An
		// answer to a void request is never this: turning a void down says nothing about
		// the operation, which may be in flight.
		released, err := s.releaseOccupationAfterRun(ctx, claim, domain.ReleaseReasonRefused, nil)
		if err != nil {
			return outcome, err
		}
		outcome.Released, outcome.Reason = released, domain.ReleaseReasonRefused
		return outcome, nil
	default:
		// In flight, a void not confirmed, or no usable answer: the claim already scheduled
		// the next query. An operation production is still working on is never voided,
		// however long it takes.
		return outcome, nil
	}
}

// claimReleaseReconcile is the claim transaction. It returns nil when nothing was claimed
// or when the claim found a permanent fault, which it logs after COMMIT.
func (s *ExecutionService) claimReleaseReconcile(ctx context.Context, occupationID string) (*releaseReconcileClaim, error) {
	var (
		claim     *releaseReconcileClaim
		permanent error
	)
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		claim, permanent = nil, nil
		occupation, err := tx.ReleaseOccupations().Get(ctx, occupationID)
		if err != nil {
			return err
		}
		if occupation.Status != domain.ReleaseOccupationActive || occupation.NextQueryAt == nil {
			return nil
		}
		action, err := tx.AgentActions().Get(ctx, occupation.ActionID)
		if err != nil {
			return err
		}
		turn, err := tx.AgentTurns().Get(ctx, action.TurnID)
		if err != nil {
			return err
		}
		agentRun, err := tx.AgentRuns().Get(ctx, turn.AgentRunID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, agentRun.NodeRunID)
		if err != nil {
			return err
		}
		_, plan, err := s.runPlan(ctx, tx, occupation.RunID)
		if err != nil {
			return err
		}
		view, err := workspaceViewLocked(ctx, tx, agentRun.ID)
		if err != nil {
			return err
		}
		prepared, fault := s.prepareReleaseReconcile(ctx, tx, plan, nodeRun.NodeID, action.ID, occupation.OperationKey)

		lock, err := tx.Runs().LockForUpdate(ctx, occupation.RunID)
		if err != nil {
			return err
		}
		// A live Run owns its release: the poll path settles it, and only the transaction
		// that ends the Run hands the schedule to the occupation.
		if !lock.Status().IsTerminal() {
			return nil
		}
		now := s.deps.Clock.Now()
		next := now.Add(postReconcileBackoff(s.releasePollInterval(prepared.attempt.ToolName), occupation.QueryCount))
		claimed, err := tx.ReleaseOccupations().ClaimQuery(ctx, occupationID, now, next)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}
		if fault != nil {
			permanent = fault
			return nil
		}
		void := false
		if since := releaseUnknownSince(prepared.attempt); since != nil && prepared.bounds.voidGrace != nil {
			void = !now.Before(since.Add(*prepared.bounds.voidGrace))
		}
		claim = &releaseReconcileClaim{
			occupationID: occupationID,
			runID:        occupation.RunID,
			attemptID:    prepared.attempt.ID,
			executor:     prepared.executor,
			void:         void,
			state: registry.ToolAsyncState{
				AgentRunID: agentRun.ID,
				ActionID:   action.ID,
				ToolName:   prepared.attempt.ToolName,
				AttemptNo:  prepared.attempt.AttemptNo,
				ExternalTask: registry.ExternalTask{
					ProviderID:     domain.ReleaseProviderID,
					ExternalTaskID: occupation.OperationKey,
				},
				ReleaseTarget: view.DeployTarget,
			},
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("execution: claim reconciliation of occupation %s: %w", occupationID, err)
	}
	if permanent != nil {
		s.deps.Logger.ErrorContext(ctx, "post-task reconciliation cannot query production for this occupation; its next query was moved by the backoff",
			"occupation_id", occupationID, "error", permanent)
	}
	return claim, nil
}

// preparedReleaseReconcile is what the claim needs to query production: the release
// Attempt under the occupation's operation key, the node's release bounds and the
// reconcilable Executor of the Attempt's Tool.
type preparedReleaseReconcile struct {
	attempt  domain.ToolAttempt
	bounds   releaseBounds
	executor registry.ReconcilableToolExecutor
}

// prepareReleaseReconcile resolves what a query needs. Its error is a permanent fault of
// the occupation's registration or facts, not a failed read.
func (s *ExecutionService) prepareReleaseReconcile(ctx context.Context, tx store.Tx, plan *runtime.CompiledDefinition, nodeID, actionID, operationKey string) (preparedReleaseReconcile, error) {
	var prepared preparedReleaseReconcile
	bounds, err := releaseBoundsOf(plan, nodeID)
	if err != nil {
		return prepared, err
	}
	prepared.bounds = bounds
	attempt, err := releaseAttemptOfOperation(ctx, tx, actionID, operationKey)
	if err != nil {
		return prepared, err
	}
	prepared.attempt = attempt
	reg, ok := s.deps.Tools.Get(attempt.ToolName)
	executor, polls := pollableToolExecutor(reg, ok)
	reconcilable, isReconcilable := executor.(registry.ReconcilableToolExecutor)
	if !polls || !reg.Metadata.Reconcilable || !isReconcilable {
		return prepared, fmt.Errorf("execution: tool %q is no longer registered as a reconcilable release tool", attempt.ToolName)
	}
	prepared.executor = reconcilable
	return prepared, nil
}

// recordReleaseUnknownAfterRun writes, under the Run lock and only while the occupation is
// still ACTIVE, the first time production said it does not know the operation. The void
// grace counts from it. It is a fact about the external operation, not about the ended
// Run, so no Event is appended.
func (s *ExecutionService) recordReleaseUnknownAfterRun(ctx context.Context, claim *releaseReconcileClaim) (bool, error) {
	var recorded bool
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		recorded = false
		if _, err := tx.Runs().LockForUpdate(ctx, claim.runID); err != nil {
			return err
		}
		occupation, err := tx.ReleaseOccupations().Get(ctx, claim.occupationID)
		if err != nil {
			return err
		}
		if occupation.Status != domain.ReleaseOccupationActive {
			return nil
		}
		recorded, err = tx.ToolAttempts().RecordOutcomeUnknownSince(ctx, claim.attemptID, s.deps.Clock.Now())
		return err
	})
	if err != nil {
		return false, fmt.Errorf("execution: record unknown operation of occupation %s: %w", claim.occupationID, err)
	}
	return recorded, nil
}

// releaseOccupationAfterRun writes production's definite answer onto the occupation in one
// transaction under the Run lock, touching the occupation row alone. It reports false when
// another scanner already freed the target.
func (s *ExecutionService) releaseOccupationAfterRun(ctx context.Context, claim *releaseReconcileClaim, reason domain.ReleaseReason, version *string) (bool, error) {
	var released bool
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.Runs().LockForUpdate(ctx, claim.runID); err != nil {
			return err
		}
		var err error
		released, err = tx.ReleaseOccupations().ReleaseAfterRun(ctx, claim.occupationID, reason, s.deps.Clock.Now(), version)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("execution: release occupation %s as %s: %w", claim.occupationID, reason, err)
	}
	return released, nil
}

// releaseAttemptOfOperation finds the Action's release Attempt carrying the operation key
// the occupation names.
func releaseAttemptOfOperation(ctx context.Context, tx store.Tx, actionID, operationKey string) (domain.ToolAttempt, error) {
	attempts, err := tx.ToolAttempts().ListByActionID(ctx, actionID)
	if err != nil {
		return domain.ToolAttempt{}, err
	}
	for _, attempt := range attempts {
		if attempt.OperationKey != nil && *attempt.OperationKey == operationKey {
			return attempt, nil
		}
	}
	return domain.ToolAttempt{}, fmt.Errorf("execution: action %s has no release attempt under %s: %w", actionID, operationKey, domain.ErrNotFound)
}

// releaseUnknownSince is when production was first found not to know the operation, or
// nil while it never said so. The void grace counts from this moment alone: an operation
// that production still has in flight is never voided, and a stop or an expiry that made
// the Attempt uncertain is not evidence that production lost the operation.
func releaseUnknownSince(attempt domain.ToolAttempt) *time.Time {
	return attempt.OutcomeUnknownSince
}

// postReconcileBackoff is the wait before the next post-task query after queries so far.
func postReconcileBackoff(base time.Duration, queries int) time.Duration {
	if base <= 0 {
		base = postReconcileFallbackInterval
	}
	if queries > postReconcileMaxDoublings {
		queries = postReconcileMaxDoublings
	}
	wait := base
	for i := 0; i < queries && wait < postReconcileMaxInterval; i++ {
		wait *= 2
	}
	if wait > postReconcileMaxInterval {
		wait = postReconcileMaxInterval
	}
	return wait
}
