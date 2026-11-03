package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the human wait of a release: when a release Attempt has no definite
// production answer by its confirm deadline, the Action is handed to a person while the
// Runtime keeps polling and, past the void grace, keeps asking production to void the
// operation. A definite answer that arrives later settles the release and closes the wait.

// agentActionWaitingHumanPayload is AGENT_ACTION_WAITING_HUMAN: the release Action a person
// now watches, its Attempt, the wait and the wait's own deadline.
type agentActionWaitingHumanPayload struct {
	AgentRunID    string    `json:"agentRunId"`
	TurnID        string    `json:"turnId"`
	ActionID      string    `json:"actionId"`
	ToolAttemptID string    `json:"toolAttemptId"`
	HumanWaitID   string    `json:"humanWaitId"`
	Deadline      time.Time `json:"deadline"`
}

// codeHumanWaitTimeoutNotConfigured is reported, not written, when a release Attempt reaches
// its confirm deadline but the Agent node bounds no human wait: the wait cannot be opened
// without a deadline, so the release stays with the polling and void paths alone.
var errHumanWaitTimeoutNotConfigured = errors.New("execution: open release human wait: the Agent node sets a releaseConfirmTimeoutMs but no humanWaitTimeoutMs")

// OpenReleaseHumanWait hands a release Attempt that passed its confirm deadline without a
// definite production answer to a person, in one transaction under the Run aggregate lock:
// the Action moves from RUNNING or WAITING_CALLBACK to WAITING_HUMAN, an OPEN human wait is
// inserted with the node's human wait bound as its deadline, the paused Agent Run's pause
// reason becomes HUMAN without touching when the pause began, and AGENT_ACTION_WAITING_HUMAN
// is appended. Polling and voiding of the Attempt continue unchanged. An Attempt whose
// deadline has not passed, that production already answered, or whose Action no longer
// waits on production is left untouched.
func (s *ExecutionService) OpenReleaseHumanWait(ctx context.Context, attemptID string) error {
	var commit agentCommit
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.ToolAttempts().Get(ctx, attemptID)
		if err != nil {
			return err
		}
		action, err := tx.AgentActions().Get(ctx, attempt.ActionID)
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
		// The wait bound comes from the Run's immutable Definition version, compiled
		// before the lock as every other plan read is.
		_, plan, err := s.runPlan(ctx, tx, nodeRun.RunID)
		if err != nil {
			return err
		}
		bounds, err := releaseBoundsOf(plan, nodeRun.NodeID)
		if err != nil {
			return err
		}

		lock, err := tx.Runs().LockForUpdate(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		if lock.Status().IsTerminal() {
			return nil
		}
		// Re-read what the lock protects: the scan ran in its own read transaction.
		if attempt, err = tx.ToolAttempts().Get(ctx, attemptID); err != nil {
			return err
		}
		if action, err = tx.AgentActions().Get(ctx, attempt.ActionID); err != nil {
			return err
		}
		if agentRun, err = tx.AgentRuns().Get(ctx, agentRun.ID); err != nil {
			return err
		}
		now := s.deps.Clock.Now()
		if attempt.ConfirmDeadline == nil || now.Before(*attempt.ConfirmDeadline) || agentRun.Termination != nil {
			return nil
		}
		if attempt.Status != domain.ToolAttemptStarted && attempt.Status != domain.ToolAttemptDispatched {
			return nil
		}
		if action.Status != domain.AgentActionRunning && action.Status != domain.AgentActionWaitingCallback {
			return nil
		}
		if bounds.humanWaitTimeout == nil {
			return errHumanWaitTimeoutNotConfigured
		}

		if err := tx.AgentActions().MarkWaitingHuman(ctx, action.ID, action.Status, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		wait := domain.HumanWait{
			ID:       s.deps.IDs.NewID(domain.IDPrefixHumanWait),
			ActionID: action.ID,
			RunID:    nodeRun.RunID,
			Status:   domain.HumanWaitOpen,
			OpenedAt: now,
			Deadline: now.Add(*bounds.humanWaitTimeout),
		}
		if err := tx.HumanWaits().Create(ctx, wait); err != nil {
			return err
		}
		// The Agent stays paused: only why it waits changes, so the paused time keeps
		// accumulating from the release pause and is credited back as one when it ends.
		if agentRun.PausedAt != nil {
			if err := tx.AgentRuns().SetPauseReason(ctx, agentRun.ID, domain.PauseHuman); err != nil {
				if errors.Is(err, domain.ErrStaleClaim) {
					return errAgentTurnSuperseded
				}
				return err
			}
		}
		if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentActionWaitingHuman, now, agentActionWaitingHumanPayload{
			AgentRunID: agentRun.ID, TurnID: turn.ID, ActionID: action.ID, ToolAttemptID: attempt.ID, HumanWaitID: wait.ID, Deadline: wait.Deadline,
		}); err != nil {
			return err
		}
		if err := tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now); err != nil {
			return err
		}
		commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq()}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("execution: open release human wait for tool attempt %s: %w", attemptID, err)
	}
	if commit.runID != "" {
		s.wake(commit)
	}
	return nil
}

// closeHumanWaitLocked closes the OPEN human wait of the Action, if there is one, with the
// given terminal status and outcome, in the caller's transaction. A wait already closed by
// another path is left as it was: the first closing wins and this one writes nothing.
func closeHumanWaitLocked(ctx context.Context, tx store.Tx, actionID string, status domain.HumanWaitStatus, outcome *string, now time.Time) error {
	wait, err := tx.HumanWaits().GetByActionID(ctx, actionID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if wait.Status != domain.HumanWaitOpen {
		return nil
	}
	_, err = tx.HumanWaits().Close(ctx, wait.ID, status, now, nil, outcome)
	return err
}
