package service

import (
	"context"
	"log/slog"
	"sync"
)

// This file owns the hand-off between the transaction that approves an Agent Action and the
// Tool call that follows it. The approval transaction creates the STARTED Attempt and
// builds the call, exactly as the claim path does; the call itself is then made by a work
// queue worker, never on the goroutine of the request that carried the decision. The queue
// is a latency optimisation only: the committed STARTED Attempt, naming this process as its
// runner, is the durable fact, and a process that dies before the call is made leaves it to
// the orphan scan of the next process.

// maxApprovedCalls bounds how many prepared calls one process holds between COMMIT and the
// worker taking them. It is of the same category as the work queue's own bound.
const maxApprovedCalls = 1024

// approvedCalls holds the calls approval transactions prepared for this process's workers,
// by Action ID. A call carries the Executor and, for an asynchronous Tool, the plaintext
// callback credential that is never persisted, so it cannot be rebuilt from PostgreSQL:
// a call absent here is one this process cannot make, and the worker leaves its STARTED
// Attempt to orphan handling.
type approvedCalls struct {
	mu    sync.Mutex
	calls map[string]*agentToolCall
}

func newApprovedCalls() *approvedCalls {
	return &approvedCalls{calls: make(map[string]*agentToolCall)}
}

// hold keeps call until a worker takes it. It reports false when the bound is reached.
func (a *approvedCalls) hold(call *agentToolCall) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) >= maxApprovedCalls {
		return false
	}
	a.calls[call.actionID] = call
	return true
}

// take removes and returns the call prepared for actionID, if this process holds one.
func (a *approvedCalls) take(actionID string) (*agentToolCall, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	call, ok := a.calls[actionID]
	if ok {
		delete(a.calls, actionID)
	}
	return call, ok
}

// enqueueApprovedCall runs after the approval transaction committed: it holds the prepared
// call for a worker and offers the Action to the work queue. A refused offer, or a full
// hold, drops the call and is logged at error level, because unlike a refused Turn the
// STARTED Attempt is not rediscovered by this process's Reconciler: it names this process
// as its runner, so only a later process's orphan scan returns it to the Agent.
func (s *ExecutionService) enqueueApprovedCall(call *agentToolCall) {
	if !s.approved.hold(call) {
		s.deps.Logger.Error("approved agent action left for orphan handling after a restart: too many prepared calls are waiting for a worker",
			slog.String("run_id", call.runID),
			slog.String("agent_action_id", call.actionID),
			slog.String("tool_attempt_id", call.attemptID))
		return
	}
	if s.deps.Queue.EnqueueAgentAction(call.runID, call.actionID) {
		return
	}
	s.approved.take(call.actionID)
	s.deps.Logger.Error("approved agent action left for orphan handling after a restart: work queue refused it",
		slog.String("run_id", call.runID),
		slog.String("agent_action_id", call.actionID),
		slog.String("tool_attempt_id", call.attemptID))
}

// ContinueAgentAction makes the Tool call of an approved Action whose STARTED Attempt the
// approval transaction committed in this process. It is the use case a work queue worker
// enters for an approved Action, on the worker's own context, and continues on the same
// path a claim continues on after its COMMIT. An Action this process holds no prepared call
// for -- the hold was dropped, or the process restarted -- changes nothing: its Attempt
// stays STARTED under the runner that created it until the orphan scan returns it.
func (s *ExecutionService) ContinueAgentAction(ctx context.Context, actionID string) error {
	call, ok := s.approved.take(actionID)
	if !ok {
		s.deps.Logger.Warn("approved agent action has no prepared call in this process; its attempt is left to orphan handling",
			slog.String("agent_action_id", actionID))
		return nil
	}
	return s.executeClaimedAgentAction(ctx, call)
}
