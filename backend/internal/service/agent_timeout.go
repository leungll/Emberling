package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// agentTimeoutError is the failure every timed-out Agent Run, Turn, Action and Tool
// Attempt records. It matches the code the round/deadline check of a successful Tool
// round already writes, so Trace shows one deadline story whichever path noticed it.
func agentTimeoutError() domain.ExecutionError {
	return domain.ExecutionError{Code: "TIMEOUT", Message: "the agent deadline expired"}
}

// agentDeadlineExceeded reports whether an external call ended because the Agent Run's
// frozen deadline expired rather than because the Provider or Tool itself failed. Both
// forms are checked: an Executor that returns ctx.Err() (or wraps it), and one that
// reports its own error after the bounded context was already done. A parent context that
// was cancelled -- process shutdown -- is deliberately not a timeout: it leaves the
// committed work recoverable instead of terminating the Agent Run.
func agentDeadlineExceeded(callErr error, callCtx context.Context) bool {
	return errors.Is(callErr, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded)
}

// TimeoutAgentRun ends one Agent Run whose frozen deadline has passed.
// It is the single Agent timeout path: the Reconciler's
// scan of expired Agent Runs, a Tool call that ran past the deadline and a model call that
// ran past the deadline all enter here.
//
// Everything commits in one transaction, under the Run aggregate lock: the current Turn
// fails when it is still READY or RUNNING, the current Action fails with
// AGENT_ACTION_FAILED and failureSource = TIMEOUT when one exists in a non-terminal state
// (together with its STARTED or DISPATCHED Tool Attempt), the Agent Run terminates with
// TIMEOUT rather than TOOL_ERROR or MODEL_ERROR, AGENT_FAILED is appended, the Agent
// NodeRun fails with NODE_FAILED and the Run re-aggregates.
//
// Nothing at all is written when the Agent Run has already terminated or its deadline has
// not passed on the injected clock: the transaction that committed first owns the outcome,
// and a model result or callback arriving afterwards cannot change the terminal state
// (the single-winner conditional-update rule). No next Turn is ever created here.
func (s *ExecutionService) TimeoutAgentRun(ctx context.Context, agentRunID string) error {
	var commit agentCommit

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		agentRun, err := tx.AgentRuns().Get(ctx, agentRunID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, agentRun.NodeRunID)
		if err != nil {
			return err
		}
		lock, err := tx.Runs().LockForUpdate(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		// Re-read what the lock now protects: the scan that found this Agent Run expired
		// ran in its own read transaction, and an immediate advancement may have
		// terminated it in between.
		if agentRun, err = tx.AgentRuns().Get(ctx, agentRunID); err != nil {
			return err
		}
		if nodeRun, err = tx.NodeRuns().Get(ctx, nodeRun.ID); err != nil {
			return err
		}

		now := s.deps.Clock.Now()
		if agentRun.Termination != nil || now.Before(agentRun.Deadline) {
			return nil
		}
		if nodeRun.Status != domain.NodeRunRunning && nodeRun.Status != domain.NodeRunWaitingCallback {
			// An Agent NodeRun that is neither RUNNING nor WAITING_CALLBACK (on an ASYNC
			// Tool's callback, the deadline covers that wait too) has no Agent
			// Loop left to stop, and failing it would contradict the status its own
			// transaction committed.
			return nil
		}

		execError := agentTimeoutError()
		turn, err := tx.AgentTurns().GetByRunAndTurnNo(ctx, agentRun.ID, agentRun.CurrentTurnNo)
		if err != nil {
			return fmt.Errorf("execution: timeout agent run %s: current turn %d: %w",
				agentRun.ID, agentRun.CurrentTurnNo, err)
		}

		// A COMPLETED Turn stays COMPLETED: the model answered, and it is its Action or
		// the deadline itself that stopped the Agent Run.
		if turn.Status == domain.AgentTurnReady || turn.Status == domain.AgentTurnRunning {
			failed, err := tx.AgentTurns().MarkTimedOut(ctx, turn.ID, now, execError)
			if err != nil {
				return err
			}
			if !failed {
				return errAgentTurnSuperseded
			}
		}

		action, hasAction, err := currentAgentAction(ctx, tx, turn.ID)
		if err != nil {
			return err
		}
		if hasAction && !action.Status.IsTerminal() {
			attemptID, err := timeoutToolAttempt(ctx, tx, action.ID, now, execError)
			if err != nil {
				return err
			}
			failed, err := tx.AgentActions().MarkTimedOut(ctx, action.ID, now, execError)
			if err != nil {
				return err
			}
			if !failed {
				return errAgentTurnSuperseded
			}
			if err := s.recordAgentActionFailureLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID,
				attemptID, domain.FailureTimeout, domain.TerminationTimeout, execError, now); err != nil {
				return err
			}
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		if err := s.terminateAgentRunLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID,
			domain.TerminationTimeout, execError, now); err != nil {
			return err
		}
		commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
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

// currentAgentAction reads the single Action of a Turn, reporting absence instead of an
// error: a Turn that never reached a Decision has none.
func currentAgentAction(ctx context.Context, tx store.Tx, turnID string) (domain.AgentAction, bool, error) {
	action, err := tx.AgentActions().GetByTurnID(ctx, turnID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.AgentAction{}, false, nil
	}
	if err != nil {
		return domain.AgentAction{}, false, err
	}
	return action, true, nil
}

// timeoutToolAttempt fails the Action's in-flight Tool Attempt, if it has one, and returns
// its ID for the AGENT_ACTION_FAILED payload's optional toolAttemptId. A FINAL Action never
// has one, and an Attempt that already completed is left exactly as its own transaction
// committed it -- so it is also not named as the source of this failure.
func timeoutToolAttempt(ctx context.Context, tx store.Tx, actionID string, now time.Time, execError domain.ExecutionError) (*string, error) {
	attempts, err := tx.ToolAttempts().ListByActionID(ctx, actionID)
	if err != nil {
		return nil, err
	}
	for _, attempt := range attempts {
		if attempt.Status != domain.ToolAttemptStarted && attempt.Status != domain.ToolAttemptDispatched {
			continue
		}
		failed, err := tx.ToolAttempts().MarkTimedOut(ctx, attempt.ID, now, execError)
		if err != nil {
			return nil, err
		}
		if !failed {
			return nil, errAgentTurnSuperseded
		}
		attemptID := attempt.ID
		return &attemptID, nil
	}
	return nil, nil
}
