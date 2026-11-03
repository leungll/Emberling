package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the answers production gives to the Runtime's queries about a release
// operation: in flight, unknown, deployed, voided or refused. Each definite answer settles
// the Attempt in one transaction under the Run aggregate lock; an in-flight or missing
// answer writes nothing beyond the poll schedule the claim already advanced. A deployed
// operation is confirmed against the target itself before it becomes a fact.

// releaseVoidedPayload is RELEASE_VOIDED: production confirmed that the operation never
// took effect. NextOperationKey and NextToolAttemptID are set when the same transaction
// started the next release Attempt.
type releaseVoidedPayload struct {
	AgentRunID        string  `json:"agentRunId"`
	TurnID            string  `json:"turnId"`
	ActionID          string  `json:"actionId"`
	ToolAttemptID     string  `json:"toolAttemptId"`
	OperationKey      string  `json:"operationKey"`
	NextOperationKey  *string `json:"nextOperationKey,omitempty"`
	NextToolAttemptID *string `json:"nextToolAttemptId,omitempty"`
}

// releasePollClaim is what the poll claim adds for a release Attempt: the reconcilable
// Executor, whether this scan asks production to void the operation instead of querying it,
// the release bounds of the Agent node and the digest the released patch must carry.
type releasePollClaim struct {
	executor    registry.ReconcilableToolExecutor
	void        bool
	bounds      releaseBounds
	patchDigest string
}

// releaseConfirmedOutput is the result a release Attempt commits once production reported
// DEPLOYED and the target was read back. It is the shape the release Tool declares as its
// output: the fact about the patch, binding the operation key and the version, with the
// conclusion healthy and the reason it is not.
type releaseConfirmedOutput struct {
	OperationKey string `json:"operationKey"`
	Version      string `json:"version"`
	PatchDigest  string `json:"patchDigest"`
	Healthy      bool   `json:"healthy"`
	Reason       string `json:"reason,omitempty"`
}

// deployedOperation is what the release Tool reports for a DEPLOYED operation.
type deployedOperation struct {
	OperationKey string `json:"operationKey"`
	Version      string `json:"version"`
}

// pollRelease is the release half of PollToolAttempt after the claim committed: one query
// (or one void request) to production, outside every transaction, and the settlement the
// answer calls for. No answer leaves everything to the next due poll.
func (s *ExecutionService) pollRelease(ctx context.Context, req PollToolAttempt, claim toolPollClaim) (PollToolAttemptOutcome, error) {
	outcome := claim.outcome
	release := claim.release
	var result registry.PollResult
	var err error
	if release.void {
		result, err = release.executor.Void(ctx, claim.state)
	} else {
		result, err = release.executor.Poll(ctx, claim.state)
	}
	if err != nil {
		return outcome, fmt.Errorf("execution: query release of tool attempt %s under %s: %w",
			req.AttemptID, claim.state.ExternalTask.ExternalTaskID, err)
	}
	outcome.Status = result.Status

	switch result.Status {
	case registry.PollSucceeded:
		// Production says DEPLOYED. The target is read back before anything is written,
		// still outside every lock; a target that cannot be read is no answer yet.
		target, err := release.executor.CheckTarget(ctx, claim.state)
		if err != nil {
			return outcome, fmt.Errorf("execution: check release target of tool attempt %s: %w", req.AttemptID, err)
		}
		committed, err := s.concludeRelease(ctx, claim, result, target)
		if err != nil {
			return outcome, err
		}
		if !committed.committed {
			outcome.Kind = PollAttemptDuplicate
			return outcome, nil
		}
		outcome.Kind = PollAttemptResumed
		return outcome, nil
	case registry.PollUnknownTask:
		if err := s.markReleaseUnknownTask(ctx, claim.call); err != nil {
			return outcome, err
		}
		return outcome, nil
	case registry.PollFailed:
		code := ""
		if result.Error != nil {
			code = result.Error.Code
		}
		if code == domain.ReleaseCodeVoided {
			committed, err := s.handleReleaseVoided(ctx, claim)
			if err != nil {
				return outcome, err
			}
			if committed.returned {
				outcome.Kind = PollAttemptReturned
				outcome.Failed = true
				outcome.FailureSource = domain.FailureProviderRefusal
			} else if !committed.committed {
				outcome.Kind = PollAttemptDuplicate
			}
			return outcome, nil
		}
		refusal := domain.ExecutionError{Code: code, Message: "production refused the release operation"}
		if result.Error != nil {
			refusal = *result.Error
		}
		committed, err := s.settleReleaseRefusal(ctx, claim.call, refusal, domain.ReleaseReasonRefused, domain.ReturnReleaseRefused,
			fmt.Sprintf("Production refused the release of %s (%s); nothing was deployed under it.",
				boundedToolName(claim.call.release.operationKey), boundedToolName(code)))
		if err != nil {
			return outcome, err
		}
		if !committed.committed {
			outcome.Kind = PollAttemptDuplicate
			return outcome, nil
		}
		outcome.Kind = PollAttemptReturned
		outcome.Failed = true
		outcome.FailureSource = domain.FailureProviderRefusal
		return outcome, nil
	default:
		// In flight, or no usable answer: the claim already scheduled the next query.
		return outcome, nil
	}
}

// markReleaseUnknownTask records that production does not know the operation. The first
// time it writes since when and appends RELEASE_OUTCOME_UNKNOWN under the Run lock; later
// answers of the same kind write nothing. The operation is never resent: it may still
// arrive, and only the void grace decides what happens then.
func (s *ExecutionService) markReleaseUnknownTask(ctx context.Context, call agentToolCall) error {
	var commit agentCommit
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.ToolAttempts().Get(ctx, call.attemptID)
		if err != nil {
			return err
		}
		if attempt.Status != domain.ToolAttemptDispatched || attempt.OutcomeUnknownSince != nil {
			return errAgentTurnSuperseded
		}
		lock, err := tx.Runs().LockForUpdate(ctx, call.runID)
		if err != nil {
			return err
		}
		if lock.Status().IsTerminal() {
			return errAgentTurnSuperseded
		}
		now := s.deps.Clock.Now()
		nextPoll := now.Add(s.releasePollInterval(call.toolName))
		first, err := tx.ToolAttempts().MarkOutcomeUnknown(ctx, call.attemptID, now, nextPoll)
		if err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		if !first {
			return errAgentTurnSuperseded
		}
		if err := s.appendEvent(ctx, tx, lock, call.runID, &call.nodeRunID, domain.EventReleaseOutcomeUnknown, now, releaseOutcomeUnknownPayload{
			AgentRunID: call.agentRunID, TurnID: call.turnID, ActionID: call.actionID, ToolAttemptID: call.attemptID,
			OperationKey: call.release.operationKey, Since: now,
		}); err != nil {
			return err
		}
		if err := tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now); err != nil {
			return err
		}
		commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq()}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil
	}
	if err != nil {
		return err
	}
	s.wake(commit)
	return nil
}

// concludeRelease commits a DEPLOYED operation as the release's conclusion through the same
// result transaction every Tool success takes, with the release additions: the occupation
// is released as DEPLOYED, an open human wait closes RESOLVED and the Agent's clock resumes
// before the round advances. The conclusion is healthy only when the target reports the
// deployed version and the approved patch and is healthy; otherwise the fact's verdict is
// false and the tool message names why. The Action succeeds either way: production did
// deploy, and the Agent decides what an unhealthy result means.
func (s *ExecutionService) concludeRelease(ctx context.Context, claim toolPollClaim, result registry.PollResult, target registry.TargetState) (agentToolCommit, error) {
	call := claim.call
	var deployed deployedOperation
	if result.ToolResult == nil || json.Unmarshal(result.ToolResult.Output, &deployed) != nil || deployed.Version == "" {
		return agentToolCommit{}, fmt.Errorf("execution: release of tool attempt %s: production reported DEPLOYED without a version", call.attemptID)
	}
	output := releaseConfirmedOutput{
		OperationKey: call.release.operationKey,
		Version:      deployed.Version,
		PatchDigest:  claim.release.patchDigest,
		Healthy:      true,
	}
	switch {
	case !target.Known:
		output.Healthy, output.Reason = false, "the deploy target reports no deployment"
	case target.Version != deployed.Version:
		output.Healthy, output.Reason = false, fmt.Sprintf("the deploy target runs version %s, not the deployed %s", boundedToolName(target.Version), boundedToolName(deployed.Version))
	case target.PatchDigest != claim.release.patchDigest:
		output.Healthy, output.Reason = false, "the deploy target runs a patch other than the approved one"
	case !target.Healthy:
		output.Healthy, output.Reason = false, "the deploy target reports the deployed version as unhealthy"
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return agentToolCommit{}, fmt.Errorf("execution: encode release conclusion of tool attempt %s: %w", call.attemptID, err)
	}
	if err := runtime.ValidateToolResult(encoded, call.outputSchema); err != nil {
		return agentToolCommit{}, fmt.Errorf("execution: release conclusion of tool attempt %s does not match the tool's output schema: %w", call.attemptID, err)
	}
	src := agentToolOutcomeSource{
		fromAttempt:      domain.ToolAttemptDispatched,
		fromAction:       domain.AgentActionWaitingCallback,
		completionSource: domain.CompletionProviderPoll,
		failureSource:    domain.FailureProviderPoll,
		resumesWaiting:   true,
	}
	committed, err := s.commitAgentToolResult(ctx, call, src, registry.ToolResult{Output: encoded})
	if err == nil && committed.nextTurnID != "" {
		// The READY Turn is committed; a refused enqueue leaves it to the Reconciler.
		s.enqueueAgentTurn(call.runID, committed.nextTurnID)
	}
	return committed, err
}

// settleReleaseSuccessLocked is what the result transaction does for a release before the
// Action succeeds: the target is freed as DEPLOYED, an open human wait closes RESOLVED and
// the Agent's clock resumes, so the round advancement that follows compares the moved
// deadline. It returns the Agent Run as the transaction now sees it.
func (s *ExecutionService) settleReleaseSuccessLocked(ctx context.Context, tx store.Tx, lock *store.RunLock, nodeRun domain.NodeRun, agentRun domain.AgentRun, actionID string, now time.Time) (domain.AgentRun, error) {
	if err := releaseOccupationLocked(ctx, tx, actionID, domain.ReleaseReasonDeployed, now); err != nil {
		return agentRun, err
	}
	outcome := string(domain.ReleaseReasonDeployed)
	if err := closeHumanWaitLocked(ctx, tx, actionID, domain.HumanWaitResolved, &outcome, now); err != nil {
		return agentRun, err
	}
	if agentRun.PausedAt == nil {
		return agentRun, nil
	}
	return s.resumeAgentLocked(ctx, tx, lock, nodeRun, agentRun, now)
}

// releaseVoidCommit reports what handleReleaseVoided committed: nothing (a competing path
// settled the Attempt first), a re-release whose call is made after COMMIT, or a return of
// the Action to the Agent.
type releaseVoidCommit struct {
	committed bool
	returned  bool
}

// handleReleaseVoided settles production's confirmation that the operation was voided, in
// one transaction under the Run aggregate lock: the Attempt fails with code VOIDED and
// uncertain false and RELEASE_VOIDED is appended. When the node leaves re-releases
// (Attempts so far minus one is below maxReleaseRetries) and the Action still waits on
// production, the same transaction starts Attempt n+1 under the next operation key: the
// Action takes its execution right back from WAITING_CALLBACK, the Agent NodeRun leaves its
// wait, the occupation is re-keyed and the new Attempt is dispatched after COMMIT. When no
// re-release is left, or the Action already waits on a person, the target is freed as
// NOT_DEPLOYED and the Action is returned as RELEASE_NOT_DEPLOYED with the clock resumed.
func (s *ExecutionService) handleReleaseVoided(ctx context.Context, claim toolPollClaim) (releaseVoidCommit, error) {
	call := claim.call
	var commit agentCommit
	var nextTurnID string
	var next *agentToolCall
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		run, plan, err := s.runPlan(ctx, tx, call.runID)
		if err != nil {
			return err
		}
		lock, err := tx.Runs().LockForUpdate(ctx, call.runID)
		if err != nil {
			return err
		}
		if lock.Status().IsTerminal() {
			return errAgentTurnSuperseded
		}
		attempt, err := tx.ToolAttempts().Get(ctx, call.attemptID)
		if err != nil {
			return err
		}
		if attempt.Status != domain.ToolAttemptDispatched {
			return errAgentTurnSuperseded
		}
		action, err := tx.AgentActions().Get(ctx, call.actionID)
		if err != nil {
			return err
		}
		turn, err := tx.AgentTurns().Get(ctx, action.TurnID)
		if err != nil {
			return err
		}
		agentRun, err := tx.AgentRuns().Get(ctx, call.agentRunID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, call.nodeRunID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()
		voided := domain.ExecutionError{Code: domain.ReleaseCodeVoided, Message: "production confirmed that the release operation was voided; nothing was deployed under it"}
		if err := tx.ToolAttempts().MarkFailedDefinite(ctx, attempt.ID, domain.ToolAttemptDispatched, now, voided); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		attempts, err := tx.ToolAttempts().ListByActionID(ctx, action.ID)
		if err != nil {
			return err
		}
		used := len(attempts) - 1
		retriesLeft := claim.release.bounds.maxRetries != nil && used < *claim.release.bounds.maxRetries &&
			action.Status == domain.AgentActionWaitingCallback

		payload := releaseVoidedPayload{
			AgentRunID: agentRun.ID, TurnID: turn.ID, ActionID: action.ID, ToolAttemptID: attempt.ID, OperationKey: call.release.operationKey,
		}
		if !retriesLeft {
			if err := s.appendEvent(ctx, tx, lock, call.runID, &nodeRun.ID, domain.EventReleaseVoided, now, payload); err != nil {
				return err
			}
			message := fmt.Sprintf("The release was voided %d time(s) and no re-release is left; nothing was deployed.", len(attempts))
			if action.Status == domain.AgentActionWaitingHuman {
				// Once a person watches the release the Runtime starts no new production
				// operation of its own, whatever re-releases the node would still allow.
				message = "The release was voided after its confirm deadline passed; nothing was deployed. Ask for the release again if it is still needed; a new release needs a new approval."
			}
			nextTurnID, err = s.returnReleaseLocked(ctx, tx, lock, call, run, plan, domain.ReleaseReasonNotDeployed, domain.ReturnReleaseNotDeployed, message, now)
			if err != nil {
				return err
			}
			commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq(), terminal: nextTurnID == ""}
			return nil
		}

		nextKey := domain.ReleaseOperationKey(action.ID, len(attempts)+1)
		payload.NextOperationKey = &nextKey
		// The Action takes its execution right back and the Agent NodeRun leaves its wait,
		// exactly as an approved Action does; the dispatch that follows COMMIT puts both
		// back to waiting.
		if err := tx.AgentActions().ResumeForRelease(ctx, action.ID, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		action.Status = domain.AgentActionRunning
		if nodeRun.Status == domain.NodeRunWaitingCallback {
			if err := tx.NodeRuns().Transition(ctx, nodeRun.ID, domain.NodeRunWaitingCallback, domain.NodeRunRunning, now); err != nil {
				if errors.Is(err, domain.ErrStaleClaim) {
					return errAgentTurnSuperseded
				}
				return err
			}
			nodeRun.Status = domain.NodeRunRunning
			if err := s.aggregateRunLocked(ctx, tx, lock, run, plan, now); err != nil {
				return err
			}
			// Re-reading the row this transaction holds refreshes the lock's status, which
			// the Attempt creation below writes back as the seq watermark.
			if lock, err = tx.Runs().LockForUpdate(ctx, call.runID); err != nil {
				return err
			}
		}
		decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
		if err != nil {
			return err
		}
		reg, ok := s.deps.Tools.Get(call.toolName)
		if !ok {
			return fmt.Errorf("execution: re-release of action %s: tool %q is no longer registered", action.ID, call.toolName)
		}
		view, err := workspaceViewLocked(ctx, tx, agentRun.ID)
		if err != nil {
			return err
		}
		occupation, err := activeOccupationOfAction(ctx, tx, action.ID)
		if err != nil {
			return err
		}
		c := &agentActionClaim{
			lock: lock, nodeRun: nodeRun, agentRun: agentRun, turn: turn, action: action, decision: decision,
			toolName: call.toolName, reg: reg, claimSource: domain.ClaimReconciler, run: run, plan: plan, occupation: occupation,
		}
		started, occupied, err := s.startReleaseAttemptLocked(ctx, tx, c, toolActionInputs{workspace: view}, now)
		if err != nil {
			return err
		}
		if occupied {
			return fmt.Errorf("execution: re-release of action %s: its own occupation was not found", action.ID)
		}
		payload.NextToolAttemptID = &started.attemptID
		if err := s.appendEvent(ctx, tx, lock, call.runID, &nodeRun.ID, domain.EventReleaseVoided, now, payload); err != nil {
			return err
		}
		if err := tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now); err != nil {
			return err
		}
		next = started
		commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq()}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return releaseVoidCommit{}, nil
	}
	if err != nil {
		return releaseVoidCommit{}, err
	}
	s.wake(commit)
	if nextTurnID != "" {
		s.enqueueAgentTurn(commit.runID, nextTurnID)
	}
	if next != nil {
		// The re-release is committed; the new operation is sent after COMMIT on the same
		// path every release Attempt takes.
		return releaseVoidCommit{committed: true}, s.executeClaimedAgentAction(ctx, next)
	}
	return releaseVoidCommit{committed: true, returned: true}, nil
}

// releasePollClaimLocked assembles, inside the poll claim transaction, what a release poll
// needs beyond the generic claim: the reconcilable Executor, the frozen deploy target, the
// release bounds of the Agent node, the digest of the approved patch and whether the void
// grace has passed for an operation whose outcome is unknown.
func (s *ExecutionService) releasePollClaimLocked(ctx context.Context, tx store.Tx, executor registry.PollableAsyncToolExecutor, attempt domain.ToolAttempt, agentRun domain.AgentRun, nodeRun domain.NodeRun) (*releasePollClaim, *domain.WorkspaceDeployTarget, error) {
	reconcilable, ok := executor.(registry.ReconcilableToolExecutor)
	if !ok {
		return nil, nil, fmt.Errorf("execution: tool %q is registered reconcilable but its executor cannot void or check a target", attempt.ToolName)
	}
	_, plan, err := s.runPlan(ctx, tx, nodeRun.RunID)
	if err != nil {
		return nil, nil, err
	}
	bounds, err := releaseBoundsOf(plan, nodeRun.NodeID)
	if err != nil {
		return nil, nil, err
	}
	view, err := workspaceViewLocked(ctx, tx, agentRun.ID)
	if err != nil {
		return nil, nil, err
	}
	var args releaseArguments
	patchDigest := string(view.PatchDigest)
	if json.Unmarshal(attempt.Input, &args) == nil && args.ChangeRequestID != "" {
		request, err := tx.ChangeRequests().Get(ctx, args.ChangeRequestID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, nil, err
		}
		if err == nil {
			patchDigest = request.PatchDigest
		}
	}
	release := &releasePollClaim{executor: reconcilable, bounds: bounds, patchDigest: patchDigest}
	if attempt.OutcomeUnknownSince != nil && bounds.voidGrace != nil {
		release.void = !s.deps.Clock.Now().Before(attempt.OutcomeUnknownSince.Add(*bounds.voidGrace))
	}
	return release, view.DeployTarget, nil
}
