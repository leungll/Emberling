package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the end of a release human wait without a production answer: an
// authenticated person stops waiting, or the wait's own deadline passes. A person cannot
// declare a release deployed or not deployed, so neither ending decides the release. The
// Action fails, its open Attempt fails with an uncertain outcome, the Agent Run ends as
// HUMAN_STOPPED and its NodeRun fails as RELEASE_UNKNOWN, while the deploy target stays
// occupied: the query schedule moves onto the occupation, and the post-task reconciliation
// keeps asking production until a definite answer frees the target.
//
// A stop and a definite production answer race for the same Action. Both take the Run
// aggregate lock and both require the human wait to still be OPEN and the Action to still
// be WAITING_HUMAN, so exactly one of them settles the release: an answer that commits
// first resolves the wait and the stop is refused; a stop that commits first fails the
// Attempt, and the answer that arrives afterwards changes nothing about the ended task and
// only reaches the occupation through the post-task reconciliation.

// codeReleaseUnknown is the error the Action, its Attempt, the Agent Run and the Agent
// NodeRun record when a human wait ends without a definite production answer.
const codeReleaseUnknown = "RELEASE_UNKNOWN"

// humanWaitOutcomeReleaseUnknown is the outcome a stopped or expired human wait records:
// what the release ended as, which is not a production answer.
const humanWaitOutcomeReleaseUnknown = "RELEASE_UNKNOWN"

func releaseUnknownError(status domain.HumanWaitStatus) domain.ExecutionError {
	if status == domain.HumanWaitExpired {
		return domain.ExecutionError{Code: codeReleaseUnknown,
			Message: "the human wait deadline passed without a definite production answer; the release outcome is unknown and the target stays occupied until reconciliation settles it"}
	}
	return domain.ExecutionError{Code: codeReleaseUnknown,
		Message: "a person stopped waiting for the release outcome; it is unknown and the target stays occupied until reconciliation settles it"}
}

// StopHumanWaitOutcome is the human wait as it stands after a stop. Repeated is true when
// the wait had already been stopped and nothing was written.
type StopHumanWaitOutcome struct {
	Wait     domain.HumanWait
	Repeated bool
}

// HumanWaitConflictError refuses a stop that can no longer take effect. Status is the
// wait's current status: RESOLVED when a definite production answer settled the release
// first, EXPIRED when the wait's deadline ended it first, OPEN when the Run or the Agent
// Run has already ended and only the expiry will close the wait. Nothing was written.
type HumanWaitConflictError struct {
	HumanWaitID string
	Status      domain.HumanWaitStatus
}

func (e *HumanWaitConflictError) Error() string {
	if e.Status == domain.HumanWaitOpen {
		return fmt.Sprintf("execution: human wait %s belongs to a run that has ended and can no longer be stopped", e.HumanWaitID)
	}
	return fmt.Sprintf("execution: human wait %s is already %s", e.HumanWaitID, e.Status)
}

// Resolved reports whether the release was settled by a definite production answer.
func (e *HumanWaitConflictError) Resolved() bool { return e.Status == domain.HumanWaitResolved }

// HumanWaitRequestError reports a stop request that is malformed before anything is read.
type HumanWaitRequestError struct {
	Reason string
}

func (e *HumanWaitRequestError) Error() string { return "execution: stop human wait: " + e.Reason }

// StopHumanWait records an authenticated person's decision to stop waiting for a release
// outcome, in one transaction under the Run aggregate lock: the wait moves OPEN -> STOPPED
// with the operator's fixed identity name, and the release ends as unknown (see
// endReleaseHumanWaitLocked). A wait already stopped is answered as Repeated; a wait a
// production answer resolved, or that expired, is a HumanWaitConflictError. After COMMIT
// only the Run is offered to the work queue and the stream is woken; no external call is
// made.
func (s *ExecutionService) StopHumanWait(ctx context.Context, humanWaitID, operator string) (StopHumanWaitOutcome, error) {
	if operator == "" {
		return StopHumanWaitOutcome{}, &HumanWaitRequestError{Reason: "the stopping identity is required"}
	}
	closedBy := operator
	outcome, err := s.endHumanWait(ctx, humanWaitID, domain.HumanWaitStopped, &closedBy)
	if errors.Is(err, errAgentTurnSuperseded) {
		// Another path committed between the read and the conditional update; report what
		// it left behind.
		return StopHumanWaitOutcome{}, s.humanWaitConflict(ctx, humanWaitID)
	}
	return outcome, err
}

// ExpireHumanWait ends an OPEN human wait whose deadline has passed on the injected clock
// exactly as a stop does, with status EXPIRED and no closing identity. A wait already
// closed, or whose deadline has not passed, is left untouched. When the Run or the Agent
// Run already ended while the wait was open, the wait still becomes EXPIRED so that it
// leaves the expiry scan, and nothing else is written.
func (s *ExecutionService) ExpireHumanWait(ctx context.Context, humanWaitID string) error {
	_, err := s.endHumanWait(ctx, humanWaitID, domain.HumanWaitExpired, nil)
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil
	}
	return err
}

func (s *ExecutionService) humanWaitConflict(ctx context.Context, humanWaitID string) error {
	var wait domain.HumanWait
	if err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		wait, err = tx.HumanWaits().Get(ctx, humanWaitID)
		return err
	}); err != nil {
		return err
	}
	return &HumanWaitConflictError{HumanWaitID: humanWaitID, Status: wait.Status}
}

// endHumanWait is the single transaction both endings share.
func (s *ExecutionService) endHumanWait(ctx context.Context, humanWaitID string, status domain.HumanWaitStatus, closedBy *string) (StopHumanWaitOutcome, error) {
	var (
		outcome StopHumanWaitOutcome
		commit  agentCommit
	)
	stop := status == domain.HumanWaitStopped
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		wait, err := tx.HumanWaits().Get(ctx, humanWaitID)
		if err != nil {
			return err
		}
		lock, err := tx.Runs().LockForUpdate(ctx, wait.RunID)
		if err != nil {
			return err
		}
		// Re-read what the lock protects: a production answer may have resolved the wait
		// between the first read and the lock.
		if wait, err = tx.HumanWaits().Get(ctx, humanWaitID); err != nil {
			return err
		}
		now := s.deps.Clock.Now()
		if wait.Status != domain.HumanWaitOpen {
			if stop && wait.Status == domain.HumanWaitStopped {
				outcome = StopHumanWaitOutcome{Wait: wait, Repeated: true}
				return nil
			}
			if stop {
				return &HumanWaitConflictError{HumanWaitID: wait.ID, Status: wait.Status}
			}
			return nil
		}
		if !stop && now.Before(wait.Deadline) {
			return nil
		}
		action, err := tx.AgentActions().Get(ctx, wait.ActionID)
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
		releaseOutcome := humanWaitOutcomeReleaseUnknown
		actionable := !lock.Status().IsTerminal() && action.Status == domain.AgentActionWaitingHuman && agentRun.Termination == nil
		if !actionable {
			if stop {
				return &HumanWaitConflictError{HumanWaitID: wait.ID, Status: domain.HumanWaitOpen}
			}
			// Nothing is left to end; the wait still closes so the expiry scan stops
			// listing it. No Run fact changes and no Event is appended.
			won, err := tx.HumanWaits().Close(ctx, wait.ID, domain.HumanWaitExpired, now, nil, &releaseOutcome)
			if err != nil {
				return err
			}
			if !won {
				return errAgentTurnSuperseded
			}
			return nil
		}

		won, err := tx.HumanWaits().Close(ctx, wait.ID, status, now, closedBy, &releaseOutcome)
		if err != nil {
			return err
		}
		if !won {
			return errAgentTurnSuperseded
		}
		wait.Status, wait.ClosedAt, wait.ClosedBy, wait.Outcome = status, &now, closedBy, &releaseOutcome
		if err := s.endReleaseHumanWaitLocked(ctx, tx, lock, nodeRun, agentRun, turn, action, wait, now); err != nil {
			return err
		}
		outcome = StopHumanWaitOutcome{Wait: wait}
		commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
		return nil
	})
	if err != nil {
		return StopHumanWaitOutcome{}, err
	}
	s.wake(commit)
	return outcome, nil
}

// endReleaseHumanWaitLocked ends the release whose human wait the caller just closed, under
// the Run aggregate lock and in the caller's transaction: the open release Attempt fails
// with an uncertain outcome, the Action fails WAITING_HUMAN -> FAILED, the occupation stays
// ACTIVE with its first post-task query due now, the Agent's clock stops being paused
// (AGENT_RESUMED records how long the wait lasted), AGENT_ACTION_FAILED names the wait, and
// the Agent Run terminates as HUMAN_STOPPED, failing its NodeRun as RELEASE_UNKNOWN and
// re-aggregating the Run. Every conditional update that affects no row rolls the whole
// transaction back.
func (s *ExecutionService) endReleaseHumanWaitLocked(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	nodeRun domain.NodeRun,
	agentRun domain.AgentRun,
	turn domain.AgentTurn,
	action domain.AgentAction,
	wait domain.HumanWait,
	now time.Time,
) error {
	execError := releaseUnknownError(wait.Status)
	attemptID, err := failOpenReleaseAttempt(ctx, tx, action.ID, now, execError)
	if err != nil {
		return err
	}
	if err := tx.AgentActions().MarkFailed(ctx, action.ID, domain.AgentActionWaitingHuman, now, execError); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return errAgentTurnSuperseded
		}
		return err
	}
	// The target stays occupied: nobody knows whether the operation took effect. The
	// occupation now carries the query schedule, so the reconciliation does not depend on
	// the ended Run or on any process memory.
	occupation, err := activeOccupationOfAction(ctx, tx, action.ID)
	if err != nil {
		return err
	}
	if occupation != nil {
		if err := tx.ReleaseOccupations().ScheduleQuery(ctx, occupation.ID, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
	}
	if agentRun.PausedAt != nil {
		if agentRun, err = s.resumeAgentLocked(ctx, tx, lock, nodeRun, agentRun, now); err != nil {
			return err
		}
	}
	waitID := wait.ID
	if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentActionFailed, now, agentActionFailedPayload{
		AgentRunID:    agentRun.ID,
		TurnID:        turn.ID,
		ActionID:      action.ID,
		ToolAttemptID: attemptID,
		FailureSource: domain.FailureHuman,
		Error:         execError,
		HumanWaitID:   &waitID,
	}); err != nil {
		return err
	}
	return s.terminateAgentRunLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, domain.TerminationHumanStopped, execError, now)
}

// failOpenReleaseAttempt fails the Action's release Attempt that is still STARTED or
// DISPATCHED with uncertain = true and returns its id; an Action whose Attempts all
// completed returns nil.
func failOpenReleaseAttempt(ctx context.Context, tx store.Tx, actionID string, now time.Time, execError domain.ExecutionError) (*string, error) {
	attempts, err := tx.ToolAttempts().ListByActionID(ctx, actionID)
	if err != nil {
		return nil, err
	}
	for _, attempt := range attempts {
		switch attempt.Status {
		case domain.ToolAttemptDispatched:
			err = tx.ToolAttempts().MarkLost(ctx, attempt.ID, now, execError, true)
		case domain.ToolAttemptStarted:
			err = tx.ToolAttempts().MarkInterrupted(ctx, attempt.ID, now, execError, true)
		default:
			continue
		}
		if err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return nil, errAgentTurnSuperseded
			}
			return nil, err
		}
		id := attempt.ID
		return &id, nil
	}
	return nil, nil
}
