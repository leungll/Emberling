package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the pause of an Agent Run's clock. While an Agent waits on something
// outside its own execution -- a person deciding an approval, a release being reconciled --
// its deadline is not running: the timeout path and the expiry scan skip a paused Agent
// Run, and resuming moves the deadline later by exactly the paused duration. Both writes
// happen under the Run aggregate lock the caller holds and commit with the caller's other
// facts; each appends its Event in the same transaction.

// agentPausedPayload is AGENT_PAUSED: why the clock stopped.
type agentPausedPayload struct {
	AgentRunID string                  `json:"agentRunId"`
	Reason     domain.AgentPauseReason `json:"reason"`
}

// agentResumedPayload is AGENT_RESUMED: the reason the pause had, how long it lasted, the
// moved deadline and how much execution time remains at the moment of resuming.
type agentResumedPayload struct {
	AgentRunID  string                  `json:"agentRunId"`
	Reason      domain.AgentPauseReason `json:"reason"`
	PausedMs    int64                   `json:"pausedMs"`
	Deadline    time.Time               `json:"deadline"`
	RemainingMs int64                   `json:"remainingMs"`
}

// errAgentPauseState rejects a pause of an already paused Agent Run or a resume of one that
// is not paused. Actions run one at a time and a pause belongs to the one Action waiting,
// so overlapping pauses are a programming error of the caller, never a race to settle.
var errAgentPauseState = errors.New("execution: agent run pause state does not match the requested change")

// pauseAgentLocked stops the clock of an Agent Run that is not paused and appends
// AGENT_PAUSED. It returns the Agent Run as the transaction now sees it.
func (s *ExecutionService) pauseAgentLocked(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	nodeRun domain.NodeRun,
	agentRun domain.AgentRun,
	reason domain.AgentPauseReason,
	now time.Time,
) (domain.AgentRun, error) {
	if agentRun.PausedAt != nil {
		return agentRun, fmt.Errorf("%w: agent run %s is already paused for %s", errAgentPauseState, agentRun.ID, *agentRun.PauseReason)
	}
	if err := tx.AgentRuns().Pause(ctx, agentRun.ID, reason, now); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return agentRun, fmt.Errorf("%w: pause agent run %s: %w", errAgentPauseState, agentRun.ID, err)
		}
		return agentRun, err
	}
	if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentPaused, now, agentPausedPayload{
		AgentRunID: agentRun.ID,
		Reason:     reason,
	}); err != nil {
		return agentRun, err
	}
	pausedAt := now
	agentRun.PausedAt = &pausedAt
	agentRun.PauseReason = &reason
	return agentRun, nil
}

// resumeAgentLocked restarts the clock of a paused Agent Run: the deadline moves later by
// the paused duration and AGENT_RESUMED is appended. It returns the Agent Run with its
// moved deadline, which every later check in the same transaction must read instead of
// the deadline the caller loaded while the Agent Run was still paused.
func (s *ExecutionService) resumeAgentLocked(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	nodeRun domain.NodeRun,
	agentRun domain.AgentRun,
	now time.Time,
) (domain.AgentRun, error) {
	if agentRun.PausedAt == nil || agentRun.PauseReason == nil {
		return agentRun, fmt.Errorf("%w: agent run %s is not paused", errAgentPauseState, agentRun.ID)
	}
	reason := *agentRun.PauseReason
	pausedFor := now.Sub(*agentRun.PausedAt)
	if pausedFor < 0 {
		pausedFor = 0
	}
	deadline, err := tx.AgentRuns().Resume(ctx, agentRun.ID, now)
	if err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return agentRun, fmt.Errorf("%w: resume agent run %s: %w", errAgentPauseState, agentRun.ID, err)
		}
		return agentRun, err
	}
	if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentResumed, now, agentResumedPayload{
		AgentRunID:  agentRun.ID,
		Reason:      reason,
		PausedMs:    pausedFor.Milliseconds(),
		Deadline:    deadline,
		RemainingMs: deadline.Sub(now).Milliseconds(),
	}); err != nil {
		return agentRun, err
	}
	agentRun.Deadline = deadline
	agentRun.PausedAt = nil
	agentRun.PauseReason = nil
	return agentRun, nil
}
