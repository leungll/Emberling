package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the post-task reconciliation of a release target: the Run ended while its
// release outcome was unknown, so the occupation still holds the target and carries its own
// query schedule. Each due query is claimed by a conditional update of the occupation's
// schedule, production is asked once after COMMIT, and only a definite answer -- DEPLOYED
// or VOIDED -- frees the target, in a transaction that writes the occupation row alone. The
// ended Run gets no Event and no state change: its outcome was committed when it ended, and
// a late answer is a fact about the target, not about the task.

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
}

// releaseReconcileClaim is what the claim transaction hands to the production query.
type releaseReconcileClaim struct {
	occupationID string
	runID        string
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
// After COMMIT production is asked once. DEPLOYED is read back against the target, and the
// occupation is released as DEPLOYED with the version the target runs recorded as live but
// unchecked; VOIDED releases it as VOIDED. Any other answer, or none, leaves the occupation
// ACTIVE for its next scheduled query.
func (s *ExecutionService) ReconcileReleaseOccupation(ctx context.Context, occupationID string) (ReleaseReconcileOutcome, error) {
	claim, err := s.claimReleaseReconcile(ctx, occupationID)
	if err != nil || claim == nil {
		return ReleaseReconcileOutcome{}, err
	}
	outcome := ReleaseReconcileOutcome{Claimed: true}

	var result registry.PollResult
	if claim.void {
		result, err = claim.executor.Void(ctx, claim.state)
	} else {
		result, err = claim.executor.Poll(ctx, claim.state)
	}
	if err != nil {
		// No answer: the claim already scheduled the next query.
		return outcome, fmt.Errorf("execution: reconcile occupation %s under %s: %w", occupationID, claim.state.ExternalTask.ExternalTaskID, err)
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
	case result.Status == registry.PollFailed && result.Error != nil && result.Error.Code == domain.ReleaseCodeVoided:
		released, err := s.releaseOccupationAfterRun(ctx, claim, domain.ReleaseReasonVoided, nil)
		if err != nil {
			return outcome, err
		}
		outcome.Released, outcome.Reason = released, domain.ReleaseReasonVoided
		return outcome, nil
	default:
		// In flight, unknown to production, or no usable answer: the claim already
		// scheduled the next query, and past the void grace the next claim voids.
		return outcome, nil
	}
}

// claimReleaseReconcile is the claim transaction. It returns nil when nothing was claimed.
func (s *ExecutionService) claimReleaseReconcile(ctx context.Context, occupationID string) (*releaseReconcileClaim, error) {
	var claim *releaseReconcileClaim
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
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
		bounds, err := releaseBoundsOf(plan, nodeRun.NodeID)
		if err != nil {
			return err
		}
		attempt, err := releaseAttemptOfOperation(ctx, tx, action.ID, occupation.OperationKey)
		if err != nil {
			return err
		}
		reg, ok := s.deps.Tools.Get(attempt.ToolName)
		executor, polls := pollableToolExecutor(reg, ok)
		reconcilable, isReconcilable := executor.(registry.ReconcilableToolExecutor)
		if !polls || !reg.Metadata.Reconcilable || !isReconcilable {
			return fmt.Errorf("execution: reconcile occupation %s: tool %q is no longer registered as a reconcilable release tool", occupationID, attempt.ToolName)
		}
		view, err := workspaceViewLocked(ctx, tx, agentRun.ID)
		if err != nil {
			return err
		}

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
		next := now.Add(postReconcileBackoff(s.releasePollInterval(attempt.ToolName), occupation.QueryCount))
		claimed, err := tx.ReleaseOccupations().ClaimQuery(ctx, occupationID, now, next)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}
		void := false
		if since := releaseUnknownSince(attempt); since != nil && bounds.voidGrace != nil {
			void = !now.Before(since.Add(*bounds.voidGrace))
		}
		claim = &releaseReconcileClaim{
			occupationID: occupationID,
			runID:        occupation.RunID,
			executor:     reconcilable,
			void:         void,
			state: registry.ToolAsyncState{
				AgentRunID: agentRun.ID,
				ActionID:   action.ID,
				ToolName:   attempt.ToolName,
				AttemptNo:  attempt.AttemptNo,
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
	return claim, nil
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

// releaseUnknownSince is when the Runtime stopped knowing whether the operation took effect:
// the first time production could not account for it, or else the moment its Attempt was
// failed with an uncertain outcome. It is nil for an Attempt whose outcome is still known.
func releaseUnknownSince(attempt domain.ToolAttempt) *time.Time {
	if attempt.OutcomeUnknownSince != nil {
		return attempt.OutcomeUnknownSince
	}
	if attempt.Status == domain.ToolAttemptFailed && attempt.Uncertain != nil && *attempt.Uncertain {
		return attempt.CompletedAt
	}
	return nil
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
