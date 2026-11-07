package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the Provider poll of one asynchronous Agent Tool Attempt: the claim of a
// due poll, the single Provider query, and the handling of a Provider that no longer knows
// the task. A final answer enters the same idempotent resume use case a callback does.

// PollToolAttempt is the parameter struct for ExecutionService.PollToolAttempt. AttemptID
// is the only routing input; ActionID is what the caller discovered and is used for
// logging only, never trusted over the committed facts.
type PollToolAttempt struct {
	AttemptID string
	ActionID  string
}

// PollToolAttemptOutcome is PollToolAttempt's result. Status is the normalized Provider
// answer and is empty when the Provider was not queried. Failed and FailureSource mirror
// ResumeOutcome and are only meaningful for RESUMED and RETURNED.
type PollToolAttemptOutcome struct {
	Kind          PollAttemptKind
	SkipReason    PollSkipReason
	RunID         string
	AgentRunID    string
	ActionID      string
	AttemptID     string
	Status        registry.PollStatus
	Failed        bool
	FailureSource domain.FailureSource
}

// PollToolAttempt queries the Provider once for a DISPATCHED Tool Attempt whose poll is due
// and feeds the answer into the single idempotent resume path.
//
// It runs in three stages, as the Node Attempt poll does. The claim transaction reads the
// Attempt, its Action and its Callback Binding, resolves the poll policy from the current
// Tool Registry and claims the poll with a conditional update; it never takes the Run lock,
// because claiming a poll is a scheduling fact that changes no business state and writes no
// Event. The Provider is then queried once, outside every transaction and lock, from
// committed facts only. Finally a SUCCEEDED or FAILED answer enters ResumeNode, whose
// conditional update from DISPATCHED decides the single winner against callbacks, timeouts
// and other polls. RUNNING or UNKNOWN writes nothing, because the next poll time was
// already committed by the claim.
//
// UNKNOWN_TASK, the Provider's definite answer that it does not know the task, is decided
// by the Tool's registered side-effect policy: a call with no side effect or a SAFE one is
// handed back to the Agent as EXECUTION_INTERRUPTED, safe to repeat; any other Tool keeps
// waiting, because a Provider that has not seen a request yet may still receive it, and
// the deadline or a later authoritative answer decides.
//
// A poll that cannot be claimed is skipped without error. A registration that no longer
// declares polling clears the poll schedule and skips; a Callback Binding that does not
// route to the Attempt clears the schedule too and skips with the mismatch error. Neither
// fails the Attempt, which a callback or the deadline still resolves. A release Attempt whose
// Run ended clears its schedule and skips too: the occupation's post-task reconciliation
// owns that operation from then on.
func (s *ExecutionService) PollToolAttempt(ctx context.Context, req PollToolAttempt) (PollToolAttemptOutcome, error) {
	if req.AttemptID == "" {
		return PollToolAttemptOutcome{}, errors.New("execution: a tool poll names no attempt")
	}
	claim, err := s.claimToolPoll(ctx, req.AttemptID)
	if err != nil {
		if claim.outcome.SkipReason == PollSkipBindingMismatch {
			s.deps.Logger.WarnContext(ctx, "provider poll refused: callback binding does not route to the tool attempt",
				"attemptId", req.AttemptID, "actionId", req.ActionID)
		}
		return claim.outcome, err
	}
	if claim.outcome.Kind == PollAttemptSkipped {
		return claim.outcome, nil
	}
	outcome := claim.outcome

	if hook := s.deps.PollHooks.AfterClaim; hook != nil {
		if err := hook(ctx, req.AttemptID); err != nil {
			return outcome, fmt.Errorf("execution: poll tool attempt %s stopped after its claim committed: %w", req.AttemptID, err)
		}
	}
	if claim.release != nil {
		// A release is settled by production's definite answers about its operation, never
		// by the generic resume: deployed is confirmed against the target, voided may start
		// the next Attempt, and refused is a refusal returned to the Agent.
		return s.pollRelease(ctx, req, claim)
	}

	result, err := claim.executor.Poll(ctx, claim.state)
	if err != nil {
		// The Provider gave no answer. The claim already pushed the next poll time back,
		// so a later poll, a callback or the deadline still decides the Attempt.
		return outcome, fmt.Errorf("execution: poll tool attempt %s for external task %s: %w",
			req.AttemptID, claim.state.ExternalTask.ExternalTaskID, err)
	}
	outcome.Status = result.Status

	switch {
	case pollIsFinal(result.Status):
		resumed, err := s.ResumeNode(ctx, ResumeNode{
			ExternalTaskID: claim.state.ExternalTask.ExternalTaskID,
			Polled:         &PolledResult{AttemptID: req.AttemptID, Result: result},
		})
		if err != nil {
			return outcome, err
		}
		if resumed.Duplicate {
			outcome.Kind = PollAttemptDuplicate
			return outcome, nil
		}
		outcome.Kind = PollAttemptResumed
		outcome.Failed = resumed.Failed
		outcome.FailureSource = resumed.FailureSource
		return outcome, nil
	case result.Status == registry.PollUnknownTask && claim.repeatable:
		returned, err := s.returnLostToolAttempt(ctx, claim.call)
		if err != nil {
			return outcome, err
		}
		if !returned.committed {
			outcome.Kind = PollAttemptDuplicate
			return outcome, nil
		}
		outcome.Kind = PollAttemptReturned
		outcome.Failed = true
		outcome.FailureSource = domain.FailureProviderPoll
		return outcome, nil
	default:
		// RUNNING, UNKNOWN, UNKNOWN_TASK of a Tool that keeps waiting, or a status this
		// service does not know: nothing is written; the claim already scheduled the next
		// poll or stopped scheduling at the bound.
		return outcome, nil
	}
}

// toolPollClaim is what the claim transaction hands to the Provider query: the Executor
// resolved from the current Registry, the async state restored from committed facts, the
// identifiers the lost-task handback needs, and whether the registered side-effect policy
// lets a lost call be repeated.
type toolPollClaim struct {
	outcome    PollToolAttemptOutcome
	executor   registry.PollableAsyncToolExecutor
	state      registry.ToolAsyncState
	call       agentToolCall
	repeatable bool
	// release is set for the Attempt of a reconcilable release Tool.
	release *releasePollClaim
}

// claimToolPoll is the claim transaction. It commits either nothing, a cleared poll
// schedule (registry drift, a Callback Binding that does not route to the Attempt, or a
// release whose Run ended) or one claimed poll; the returned outcome is SKIPPED unless the
// claim won, in which case it is CLAIMED_NOT_FINAL until the Provider answers.
func (s *ExecutionService) claimToolPoll(ctx context.Context, attemptID string) (toolPollClaim, error) {
	claim := toolPollClaim{outcome: PollToolAttemptOutcome{Kind: PollAttemptSkipped, AttemptID: attemptID}}
	skip := func(reason PollSkipReason) {
		claim.outcome.Kind = PollAttemptSkipped
		claim.outcome.SkipReason = reason
	}

	var mismatch error
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.ToolAttempts().Get(ctx, attemptID)
		if err != nil {
			return err
		}
		claim.outcome.ActionID = attempt.ActionID
		if attempt.Status != domain.ToolAttemptDispatched {
			skip(PollSkipAttemptNotDispatched)
			return nil
		}
		action, err := tx.AgentActions().Get(ctx, attempt.ActionID)
		if err != nil {
			return err
		}
		// A release Attempt is still polled while a person watches its Action: the human
		// wait never stops the Runtime from learning production's answer.
		reg, ok := s.deps.Tools.Get(attempt.ToolName)
		releaseTool := ok && reg.Metadata.Reconcilable
		if action.Status != domain.AgentActionWaitingCallback && (!releaseTool || action.Status != domain.AgentActionWaitingHuman) {
			skip(PollSkipActionNotWaiting)
			return nil
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
		claim.outcome.RunID = nodeRun.RunID
		claim.outcome.AgentRunID = agentRun.ID

		// A live Run owns its release; the transaction that ended the Run handed the
		// schedule to the occupation, so production is asked along that path alone. This
		// read takes no lock: a Run ending right after it is caught by the conclusion and
		// settlement transactions, which look at the Run under its lock.
		if releaseTool {
			run, err := tx.Runs().Get(ctx, nodeRun.RunID)
			if err != nil {
				return err
			}
			if run.Status.IsTerminal() {
				// Clearing the poll must not take away the last route to the target: a Run
				// that ended before the Run end wrote the schedule has none on its
				// occupation. The fixed order takes the Run lock before occupation rows.
				if _, err := tx.Runs().LockForUpdate(ctx, nodeRun.RunID); err != nil {
					return err
				}
				if err := tx.ReleaseOccupations().ScheduleQueriesOfRun(ctx, nodeRun.RunID, s.deps.Clock.Now()); err != nil {
					return err
				}
				if _, err := tx.ToolAttempts().ClearPoll(ctx, attemptID); err != nil {
					return err
				}
				skip(PollSkipRunEnded)
				return nil
			}
		}

		// A poll carries no callback token, so the Callback Binding is its only route and
		// must name exactly this Attempt.
		binding, err := s.toolPollBinding(ctx, tx, attemptID)
		if err != nil {
			if !errors.Is(err, ErrPollBindingMismatch) {
				return err
			}
			// No poll can ever route to this Attempt, so its schedule is cleared and
			// discovery stops listing it on every scan; a callback or the deadline still
			// resolves it. The clear commits and the mismatch is still reported.
			if _, clearErr := tx.ToolAttempts().ClearPoll(ctx, attemptID); clearErr != nil {
				return clearErr
			}
			skip(PollSkipBindingMismatch)
			mismatch = err
			return nil
		}

		executor, polls := pollableToolExecutor(reg, ok)
		if !polls {
			// The current registration no longer polls this Tool. The schedule is cleared
			// so the Attempt stops being discovered; a callback or the deadline still
			// resolves it.
			if _, err := tx.ToolAttempts().ClearPoll(ctx, attemptID); err != nil {
				return err
			}
			skip(PollSkipRegistryDrift)
			return nil
		}
		policy := *reg.Metadata.Poll

		var release *releasePollClaim
		var releaseTarget *domain.WorkspaceDeployTarget
		if releaseTool {
			release, releaseTarget, err = s.releasePollClaimLocked(ctx, tx, executor, attempt, agentRun, nodeRun)
			if err != nil {
				return err
			}
		}

		claimed, err := tx.ToolAttempts().ClaimPoll(ctx, attemptID, s.deps.Clock.Now(),
			time.Duration(policy.IntervalMs)*time.Millisecond, policy.MaxPolls)
		if err != nil {
			return err
		}
		if !claimed {
			// Not due yet, already at the poll limit, or another poller, callback or
			// timeout got there first.
			skip(PollSkipNotClaimed)
			return nil
		}
		if release != nil && attempt.PollCount+1 >= policy.MaxPolls {
			// The claim took the release's last poll and cleared its schedule. Nothing
			// else changes: the target stays occupied and the Attempt stays open for the
			// confirm deadline and a person to resolve.
			s.deps.Logger.WarnContext(ctx, "release attempt used its last poll; no further poll is scheduled",
				"tool_attempt_id", attemptID, "operation_key", binding.ExternalTaskID)
		}
		// Past the void grace of an operation whose outcome is unknown, this scan asks
		// production to void it instead of querying it. The first request time is a
		// committed fact of the claim; a request that is lost is simply made again by the
		// next claim, and counts as no re-release.
		if release != nil && release.void {
			if err := tx.ToolAttempts().MarkVoidRequested(ctx, attemptID, s.deps.Clock.Now()); err != nil {
				return err
			}
		}

		claim.outcome.Kind = PollAttemptClaimedNotFinal
		claim.executor = executor
		claim.release = release
		claim.state = registry.ToolAsyncState{
			AgentRunID: agentRun.ID,
			ActionID:   action.ID,
			ToolName:   attempt.ToolName,
			AttemptNo:  attempt.AttemptNo,
			ExternalTask: registry.ExternalTask{
				ProviderID:     binding.ProviderID,
				ExternalTaskID: binding.ExternalTaskID,
			},
			ReleaseTarget: releaseTarget,
		}
		claim.call = agentToolCall{
			runID:        nodeRun.RunID,
			nodeRunID:    nodeRun.ID,
			agentRunID:   agentRun.ID,
			turnID:       turn.ID,
			actionID:     action.ID,
			attemptID:    attempt.ID,
			toolName:     attempt.ToolName,
			outputSchema: reg.Metadata.OutputSchema,
		}
		if release != nil {
			claim.call.release = &releaseCall{operationKey: binding.ExternalTaskID, target: releaseTarget}
			claim.call.action = registry.ToolAction{
				RunID: nodeRun.RunID, AgentRunID: agentRun.ID, TurnID: turn.ID, ActionID: action.ID,
				ToolName: attempt.ToolName, AttemptNo: attempt.AttemptNo, Arguments: attempt.Input,
			}
		}
		// The same rule the orphan handling applies decides whether a call the Provider
		// lost may simply be made again: no side effect, or a SAFE one.
		claim.repeatable = classifyOrphan(reg.Metadata.SideEffect, true) == orphanInterrupted
		return nil
	})
	if err != nil {
		claim.executor = nil
		if claim.outcome.SkipReason == "" {
			claim.outcome.Kind = PollAttemptSkipped
		}
		return claim, err
	}
	if mismatch != nil {
		return claim, mismatch
	}
	return claim, nil
}

// pollableToolExecutor resolves the pollable Executor of a registration. Whether a Tool is
// polled is decided by its registered ExecutionKind and Poll policy, never by what its
// Executor happens to implement; a registration whose Executor does not match its
// declaration is not polled either.
func pollableToolExecutor(reg registry.ToolRegistration, registered bool) (registry.PollableAsyncToolExecutor, bool) {
	if !registered || reg.Metadata.ExecutionKind != domain.ToolExecutionAsync || reg.Metadata.Poll == nil {
		return nil, false
	}
	executor, ok := reg.Executor.(registry.PollableAsyncToolExecutor)
	if !ok {
		return nil, false
	}
	return executor, true
}

// toolPollBinding returns the single Callback Binding that routes to the Tool Attempt. No
// binding, several bindings, or one that does not route to this Attempt is a mismatch: the
// poll is refused and nothing is written.
func (s *ExecutionService) toolPollBinding(ctx context.Context, tx store.Tx, attemptID string) (domain.CallbackBinding, error) {
	bindings, err := tx.CallbackBindings().ListByTargets(ctx, domain.CallbackTargetToolAttempt, []string{attemptID})
	if err != nil {
		return domain.CallbackBinding{}, err
	}
	if len(bindings) != 1 {
		return domain.CallbackBinding{}, fmt.Errorf("%w: %d callback bindings route to tool attempt %s", ErrPollBindingMismatch, len(bindings), attemptID)
	}
	if err := checkPollRoute(bindings[0], attemptID); err != nil {
		return domain.CallbackBinding{}, err
	}
	return bindings[0], nil
}

// lostToolAttemptError is the error a dispatched call the Provider no longer knows fails
// with. It is the same code the orphan handling uses for a call that is safe to repeat.
var lostToolAttemptError = domain.ExecutionError{
	Code:    codeExecutionInterrupted,
	Message: "the external service no longer knows the dispatched task, so no result will arrive; calling the tool again is safe",
}

// returnLostToolAttempt hands a dispatched call whose Provider no longer knows the task
// back to the Agent, in one transaction under the Run aggregate lock: the Attempt's
// conditional DISPATCHED -> FAILED update with the outcome known to be safe to repeat, the
// Action's WAITING_CALLBACK -> FAILED update, AGENT_ACTION_FAILED with failureSource
// PROVIDER_POLL and returnedToAgent, the committed Decision and a bounded tool message as
// the next Context Version, and the round advancement that creates the next READY Turn or
// ends the Agent Run at its round or time limit. The Agent NodeRun leaves WAITING_CALLBACK
// and the Run is re-aggregated in the same transaction.
//
// A conditional update that affects no row -- a callback, the timeout or another poll
// already resolved the Attempt -- rolls everything back and reports committed=false, so the
// losing caller writes nothing and consumes no Event seq. After COMMIT the next Turn is
// offered to the work queue; a refused enqueue leaves it to the Reconciler.
func (s *ExecutionService) returnLostToolAttempt(ctx context.Context, call agentToolCall) (agentToolCommit, error) {
	var commit agentCommit
	var nextTurnID string

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		// Resuming the waiting Agent NodeRun re-derives the Run status, which needs the
		// compiled plan of the Run's immutable Definition version; it is resolved before
		// the lock, as every other asynchronous resume does.
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
		nodeRun, err := tx.NodeRuns().Get(ctx, call.nodeRunID)
		if err != nil {
			return err
		}
		agentRun, err := tx.AgentRuns().Get(ctx, call.agentRunID)
		if err != nil {
			return err
		}
		action, err := tx.AgentActions().Get(ctx, call.actionID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()

		// The conditional updates decide that this poll, and not a callback, the timeout
		// or another poll, owns the outcome. A SAFE call is known to be repeatable, so the
		// uncertain determination is false.
		if err := tx.ToolAttempts().MarkLost(ctx, call.attemptID, now, lostToolAttemptError, false); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		if err := tx.AgentActions().MarkFailed(ctx, call.actionID, domain.AgentActionWaitingCallback, now, lostToolAttemptError); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		attemptRef := call.attemptID
		if err := s.appendEvent(ctx, tx, lock, call.runID, &nodeRun.ID, domain.EventAgentActionFailed, now, agentActionFailedPayload{
			AgentRunID:      agentRun.ID,
			TurnID:          call.turnID,
			ActionID:        call.actionID,
			ToolAttemptID:   &attemptRef,
			FailureSource:   domain.FailureProviderPoll,
			Error:           lostToolAttemptError,
			ReturnedToAgent: true,
		}); err != nil {
			return err
		}

		reg, _ := s.deps.Tools.Get(call.toolName)
		count, err := s.orphanGenerationCountLocked(ctx, tx, agentRun, reg.Metadata)
		if err != nil {
			return err
		}
		content, err := buildLostTaskToolMessage(call.toolName, count)
		if err != nil {
			return fmt.Errorf("execution: lost tool attempt %s: %w", call.attemptID, err)
		}
		decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
		if err != nil {
			return err
		}
		messages, err := s.appendToolRoundContext(ctx, tx, agentRun, call, decision, content, now)
		if err != nil {
			return err
		}

		src := agentToolOutcomeSource{
			fromAttempt:      domain.ToolAttemptDispatched,
			fromAction:       domain.AgentActionWaitingCallback,
			completionSource: domain.CompletionProviderPoll,
			failureSource:    domain.FailureProviderPoll,
			resumesWaiting:   true,
		}
		turnID, err := s.advanceAgentRoundLocked(ctx, tx, lock, nodeRun, agentRun, call, src, run, plan,
			agentRun.CurrentContextVersion+1, agentRun.CurrentStateVersion, len(messages), now)
		if err != nil {
			return err
		}
		if turnID == "" {
			commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}
		nextTurnID = turnID
		commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq()}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return agentToolCommit{}, nil
	}
	if err != nil {
		return agentToolCommit{}, err
	}
	s.wake(commit)
	if nextTurnID != "" {
		// The READY Turn is committed; a refused enqueue leaves it to the Reconciler.
		s.enqueueAgentTurn(commit.runID, nextTurnID)
	}
	return agentToolCommit{committed: true, nextTurnID: nextTurnID}, nil
}
