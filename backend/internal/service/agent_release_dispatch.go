package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns what happens to a release Attempt right after its dispatch: production
// accepted the operation, refused it definitely, or gave no answer the Runtime can trust.
// The third case also covers a process that died between the Attempt's COMMIT and the call,
// discovered by the orphan scan: in both the operation may or may not have reached
// production, so the Runtime starts asking instead of guessing.

// releaseOutcomeUnknownPayload is RELEASE_OUTCOME_UNKNOWN: the Runtime stopped knowing
// whether the operation took effect and will query production until it answers.
type releaseOutcomeUnknownPayload struct {
	AgentRunID    string    `json:"agentRunId"`
	TurnID        string    `json:"turnId"`
	ActionID      string    `json:"actionId"`
	ToolAttemptID string    `json:"toolAttemptId"`
	OperationKey  string    `json:"operationKey"`
	Since         time.Time `json:"since"`
}

// releaseSettle is everything the dispatch settlement reads under the Run aggregate lock.
type releaseSettle struct {
	lock     *store.RunLock
	run      domain.Run
	plan     *runtime.CompiledDefinition
	nodeRun  domain.NodeRun
	agentRun domain.AgentRun
	turnID   string
	action   domain.AgentAction
	attempt  domain.ToolAttempt
}

// recordReleaseDispatch is the transaction after a release Tool call, chosen by what
// production answered. A refusal (a definite 409 with a code) is handed back to the Agent
// as RELEASE_REFUSED and frees the target. An accepted operation starts polling. No answer
// -- a transport failure, a timeout, a 5xx -- starts polling too, with the outcome marked
// unknown from now, so the first query is due at once and the void grace starts counting.
// A Tool that answered synchronously contradicts its registration and fails the Action.
func (s *ExecutionService) recordReleaseDispatch(ctx context.Context, call agentToolCall, result registry.ToolExecutionResult, execErr error) error {
	var failure *registry.ProviderFailure
	switch {
	case errors.As(execErr, &failure):
		return s.refuseRelease(ctx, call, failure.Err)
	case execErr != nil:
		s.deps.Logger.WarnContext(ctx, "release dispatch got no answer from production; the outcome will be queried",
			"run_id", call.runID, "action_id", call.actionID, "tool_attempt_id", call.attemptID, "operation_key", call.release.operationKey)
		return s.settleReleaseDispatch(ctx, call, true)
	case result.Kind == registry.ToolResultDispatched:
		if result.ExternalTask == nil || result.ExternalTask.ExternalTaskID != call.release.operationKey {
			// The Binding already routes the operation key the Runtime chose; a Tool that
			// reports another key is logged, because polls will ask under the chosen one.
			s.deps.Logger.WarnContext(ctx, "release tool reported an external task other than its operation key",
				"tool_name", call.toolName, "tool_attempt_id", call.attemptID)
		}
		return s.settleReleaseDispatch(ctx, call, false)
	default:
		return s.failReleaseDispatch(ctx, call, domain.ExecutionError{
			Code:    "TOOL_EXECUTION_KIND_MISMATCH",
			Message: fmt.Sprintf("tool %q is registered as a reconcilable release but returned result kind %q", call.toolName, result.Kind),
		})
	}
}

// settleReleaseDispatch commits the dispatched state of a release Attempt, under the Run
// aggregate lock: the Attempt becomes DISPATCHED with its first poll due, the RUNNING Action
// and the Agent NodeRun start waiting, AGENT_ACTION_WAITING is appended and the Run
// re-aggregates. With unknown true the first poll is due now and RELEASE_OUTCOME_UNKNOWN
// records since when. An Attempt no longer STARTED means the orphan scan of another process
// settled it first; this caller then writes nothing.
func (s *ExecutionService) settleReleaseDispatch(ctx context.Context, call agentToolCall, unknown bool) error {
	var commit agentCommit
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
		if attempt.Status != domain.ToolAttemptStarted {
			return errAgentTurnSuperseded
		}
		action, err := tx.AgentActions().Get(ctx, call.actionID)
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
		if err := s.settleReleaseDispatchLocked(ctx, tx, releaseSettle{
			lock: lock, run: run, plan: plan, nodeRun: nodeRun, agentRun: agentRun, turnID: call.turnID, action: action, attempt: attempt,
		}, unknown, s.deps.Clock.Now()); err != nil {
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

// settleReleaseDispatchLocked is the shared body of the dispatch settlement and of the
// orphan handling of a release Attempt. The Attempt's STARTED -> DISPATCHED update decides
// the single winner; an Action no longer RUNNING (a confirm deadline already handed it to
// a human wait) keeps its status, the Attempt still starts being polled.
func (s *ExecutionService) settleReleaseDispatchLocked(ctx context.Context, tx store.Tx, t releaseSettle, unknown bool, now time.Time) error {
	operationKey := ""
	if t.attempt.OperationKey != nil {
		operationKey = *t.attempt.OperationKey
	}
	first := false
	if unknown {
		var err error
		if first, err = tx.ToolAttempts().MarkOutcomeUnknown(ctx, t.attempt.ID, now, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
	} else {
		firstPoll := now.Add(s.releasePollInterval(t.attempt.ToolName))
		if err := tx.ToolAttempts().MarkDispatched(ctx, t.attempt.ID, now, &firstPoll); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
	}
	// An Action a confirm deadline already handed to a person does not wait on production
	// again: it keeps its status and nothing about a wait is appended for it.
	if t.action.Status == domain.AgentActionRunning {
		if err := s.markReleaseWaitingLocked(ctx, tx, t, now); err != nil {
			return err
		}
	}
	if first {
		if err := s.appendEvent(ctx, tx, t.lock, t.nodeRun.RunID, &t.nodeRun.ID, domain.EventReleaseOutcomeUnknown, now, releaseOutcomeUnknownPayload{
			AgentRunID: t.agentRun.ID, TurnID: t.turnID, ActionID: t.action.ID, ToolAttemptID: t.attempt.ID, OperationKey: operationKey, Since: now,
		}); err != nil {
			return err
		}
	}
	// Run status is derived from NodeRun state: with the Agent NodeRun waiting, the Run
	// becomes PAUSED once nothing else is running.
	return s.aggregateRunLocked(ctx, tx, t.lock, t.run, t.plan, now)
}

// markReleaseWaitingLocked moves a RUNNING release Action, and its Agent NodeRun when it
// is still RUNNING, to waiting on production and appends AGENT_ACTION_WAITING naming the
// Attempt's release Binding.
func (s *ExecutionService) markReleaseWaitingLocked(ctx context.Context, tx store.Tx, t releaseSettle, now time.Time) error {
	if err := tx.AgentActions().MarkWaiting(ctx, t.action.ID, now); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return errAgentTurnSuperseded
		}
		return err
	}
	if t.nodeRun.Status == domain.NodeRunRunning {
		if err := tx.NodeRuns().MarkWaiting(ctx, t.nodeRun.ID, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
	}
	bindings, err := tx.CallbackBindings().ListByTargets(ctx, domain.CallbackTargetToolAttempt, []string{t.attempt.ID})
	if err != nil {
		return err
	}
	bindingID := ""
	if len(bindings) == 1 {
		bindingID = bindings[0].ID
	}
	return s.appendEvent(ctx, tx, t.lock, t.nodeRun.RunID, &t.nodeRun.ID, domain.EventAgentActionWaiting, now, agentActionWaitingPayload{
		AgentRunID: t.agentRun.ID, TurnID: t.turnID, ActionID: t.action.ID, ToolAttemptID: t.attempt.ID, CallbackBindingID: bindingID,
	})
}

// releasePollInterval is the poll interval the release Tool's registration declares.
func (s *ExecutionService) releasePollInterval(toolName string) time.Duration {
	if reg, ok := s.deps.Tools.Get(toolName); ok && reg.Metadata.Poll != nil {
		return time.Duration(reg.Metadata.Poll.IntervalMs) * time.Millisecond
	}
	return 0
}

// refuseRelease hands a release production refused definitely back to the Agent as
// RELEASE_REFUSED, in one transaction under the Run aggregate lock: the Attempt fails with
// production's code and uncertain false, the occupation is released as REFUSED, an open
// human wait closes RESOLVED, the Agent's clock resumes and the Action is returned. It
// serves the dispatch (a STARTED Attempt of a RUNNING Action) and the poll (a DISPATCHED
// Attempt of a waiting Action) alike.
func (s *ExecutionService) refuseRelease(ctx context.Context, call agentToolCall, refusal domain.ExecutionError) error {
	message := fmt.Sprintf("Production refused the release of %s (%s); nothing was deployed under it.",
		boundedToolName(call.release.operationKey), boundedToolName(refusal.Code))
	_, err := s.settleReleaseRefusal(ctx, call, refusal, domain.ReleaseReasonRefused, domain.ReturnReleaseRefused, message)
	return err
}

// settleReleaseRefusal is the shared transaction of RELEASE_REFUSED and RELEASE_NOT_DEPLOYED
// after the Attempt's own failure is known: it fails the Attempt definitely, frees the
// target with reason, closes an open human wait, resumes the clock and returns the Action.
// A caller that already failed the Attempt in its own transaction does not use it.
func (s *ExecutionService) settleReleaseRefusal(ctx context.Context, call agentToolCall, attemptErr domain.ExecutionError, reason domain.ReleaseReason, code domain.AgentReturnCode, message string) (agentToolCommit, error) {
	var commit agentCommit
	var nextTurnID string
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
		if attempt.Status != domain.ToolAttemptStarted && attempt.Status != domain.ToolAttemptDispatched {
			return errAgentTurnSuperseded
		}
		if err := tx.ToolAttempts().MarkFailedDefinite(ctx, attempt.ID, attempt.Status, s.deps.Clock.Now(), attemptErr); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		nextTurnID, err = s.returnReleaseLocked(ctx, tx, lock, call, run, plan, reason, code, message, s.deps.Clock.Now())
		if err != nil {
			return err
		}
		commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq(), terminal: nextTurnID == ""}
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
		s.enqueueAgentTurn(commit.runID, nextTurnID)
	}
	return agentToolCommit{committed: true, nextTurnID: nextTurnID}, nil
}

// returnReleaseLocked is the tail every definite non-deployment shares, after the Attempt
// was failed in the same transaction: the occupation is released with reason, an open
// human wait closes RESOLVED with the same reason, the Agent's clock resumes and the Action
// is handed back to the Agent with code, from whatever waiting status it is in.
func (s *ExecutionService) returnReleaseLocked(ctx context.Context, tx store.Tx, lock *store.RunLock, call agentToolCall, run domain.Run, plan *runtime.CompiledDefinition, reason domain.ReleaseReason, code domain.AgentReturnCode, message string, now time.Time) (string, error) {
	nodeRun, err := tx.NodeRuns().Get(ctx, call.nodeRunID)
	if err != nil {
		return "", err
	}
	agentRun, err := tx.AgentRuns().Get(ctx, call.agentRunID)
	if err != nil {
		return "", err
	}
	action, err := tx.AgentActions().Get(ctx, call.actionID)
	if err != nil {
		return "", err
	}
	turn, err := tx.AgentTurns().Get(ctx, action.TurnID)
	if err != nil {
		return "", err
	}
	if err := releaseOccupationLocked(ctx, tx, action.ID, reason, now); err != nil {
		return "", err
	}
	outcome := string(reason)
	if err := closeHumanWaitLocked(ctx, tx, action.ID, domain.HumanWaitResolved, &outcome, now); err != nil {
		return "", err
	}
	if agentRun.PausedAt != nil {
		if agentRun, err = s.resumeAgentLocked(ctx, tx, lock, nodeRun, agentRun, now); err != nil {
			return "", err
		}
	}
	attemptID := call.attemptID
	return s.returnActionToAgentLocked(ctx, tx, agentActionReturn{
		lock: lock, nodeRun: nodeRun, agentRun: agentRun, turn: turn, action: action, attemptID: &attemptID,
		toolName: call.toolName, run: run, plan: plan,
	}, action.Status, code, message, now)
}

// failReleaseDispatch fails the Action of a release whose Tool contradicted its
// registration: the Attempt fails from STARTED, the target is freed as NOT_DEPLOYED, an open
// human wait closes, and the Action fails from RUNNING or WAITING_HUMAN with the Agent Run
// ending as a Tool error, as the generic Tool failure does.
func (s *ExecutionService) failReleaseDispatch(ctx context.Context, call agentToolCall, execError domain.ExecutionError) error {
	var commit agentCommit
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, call.runID)
		if err != nil {
			return err
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
		// A confirm deadline may have handed the Action to a person while the call was
		// out; the Action fails from whichever of the two it is in.
		if action.Status != domain.AgentActionRunning && action.Status != domain.AgentActionWaitingHuman {
			return errAgentTurnSuperseded
		}
		now := s.deps.Clock.Now()
		if err := tx.ToolAttempts().MarkFailed(ctx, call.attemptID, domain.ToolAttemptStarted, now, execError); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		if err := releaseOccupationLocked(ctx, tx, call.actionID, domain.ReleaseReasonNotDeployed, now); err != nil {
			return err
		}
		outcome := string(domain.ReleaseReasonNotDeployed)
		if err := closeHumanWaitLocked(ctx, tx, call.actionID, domain.HumanWaitResolved, &outcome, now); err != nil {
			return err
		}
		attemptID := call.attemptID
		if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, call.turnID, call.actionID, &attemptID,
			action.Status, domain.FailureSyncExecution, domain.TerminationToolError, execError, now); err != nil {
			return err
		}
		commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq(), terminal: true}
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
