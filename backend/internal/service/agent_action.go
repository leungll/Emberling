package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the execution of one committed TOOL_CALL Agent Action:
// the transaction pair that claims the Action, calls
// the Tool strictly after COMMIT, and commits the Tool result together with the appended
// Context, the optional new State Version and the next round -- or the shared Tool
// failure transaction when the call did not produce a usable result.
//
// The FINAL Action is not executed here. It has its own completion use case, and a FINAL
// Action this path is handed is left untouched: a committed RUNNING Final Action is
// forbidden, so claiming one here would leave exactly the
// row that must never exist.

// agentActionStartedPayload is AGENT_ACTION_STARTED (Event fields: turnId,
// actionId, optional toolAttemptId, claimSource). ToolAttemptID is optional in the Event
// contract because a FINAL Action creates no Tool Attempt.
type agentActionStartedPayload struct {
	AgentRunID    string             `json:"agentRunId"`
	TurnID        string             `json:"turnId"`
	ActionID      string             `json:"actionId"`
	ToolAttemptID *string            `json:"toolAttemptId,omitempty"`
	ClaimSource   domain.ClaimSource `json:"claimSource"`
}

// agentActionCompletedPayload is AGENT_ACTION_COMPLETED.
// completionSource is what distinguishes this synchronous execution from the callback and
// Provider-poll paths that reach the same Action.
//
// The Tool result itself is deliberately absent: a Tool result can be as large as the
// document it read, and the authoritative copy is the Tool Attempt's Result column, which
// Trace projects. Event payloads stay bounded (CLAUDE.md "Persistence and transactions").
type agentActionCompletedPayload struct {
	AgentRunID       string                  `json:"agentRunId"`
	TurnID           string                  `json:"turnId"`
	ActionID         string                  `json:"actionId"`
	ToolAttemptID    *string                 `json:"toolAttemptId,omitempty"`
	CompletionSource domain.CompletionSource `json:"completionSource"`
}

// agentActionFailedPayload is AGENT_ACTION_FAILED.
type agentActionFailedPayload struct {
	AgentRunID    string                `json:"agentRunId"`
	TurnID        string                `json:"turnId"`
	ActionID      string                `json:"actionId"`
	ToolAttemptID *string               `json:"toolAttemptId,omitempty"`
	FailureSource domain.FailureSource  `json:"failureSource"`
	Error         domain.ExecutionError `json:"error"`
}

// agentStateUpdatedPayload is AGENT_STATE_UPDATED (Event fields: turnId,
// contextVersion, previousStateVersion, stateVersion). It is written only by the
// transaction that really created a new State Version. The State value is not part of it:
// the State Version row is the authority, and a State can carry arbitrary user content.
type agentStateUpdatedPayload struct {
	AgentRunID           string `json:"agentRunId"`
	TurnID               string `json:"turnId"`
	ContextVersion       int    `json:"contextVersion"`
	PreviousStateVersion int    `json:"previousStateVersion"`
	StateVersion         int    `json:"stateVersion"`
}

// agentToolCallMessage is the content of the `assistant` message appended to the Context
// after a successful Tool call: the committed TOOL_CALL Decision, in the shape the next
// model request replays (one assistant message stores the committed TOOL_CALL Decision).
//
// Design decision (the data model fixes that the Decision is saved, not its encoding):
// the state patch is not part of it. The patch is applied to the State chain, which has
// its own authoritative versions; copying it into the transcript would present a second,
// possibly divergent record of the same fact to the model.
type agentToolCallMessage struct {
	Kind      domain.DecisionKind `json:"kind"`
	ToolName  string              `json:"toolName"`
	Arguments json.RawMessage     `json:"arguments"`
}

// agentToolCall is what the claim transaction hands to the Tool call it authorised.
// Everything the call and its result transaction need is read inside the transaction that
// took the claim, so the call itself happens strictly after COMMIT and no
// Run lock is held while external code runs.
type agentToolCall struct {
	runID      string
	nodeRunID  string
	agentRunID string
	turnID     string
	actionID   string
	attemptID  string
	toolName   string

	executor     registry.ToolExecutor
	outputSchema json.RawMessage
	action       registry.ToolAction
	// async is the registered ExecutionKind read under the claim, never inferred from what
	// the Executor returns (CLAUDE.md "Extensions and external calls").
	async bool
	// deadline is the Agent Run's single frozen deadline, which covers every Turn and
	// every Tool call.
	deadline time.Time
}

// agentCommit records what one committed Agent transaction has to wake afterwards.
type agentCommit struct {
	runID   string
	lastSeq int64
	// terminal is true when the transaction failed the Agent NodeRun, which can make
	// Run-level work available and therefore needs the Queue, not only an SSE wake-up.
	terminal bool
}

// wake performs the post-COMMIT notification the transaction owes. It is a no-op for a
// transaction that committed nothing.
func (s *ExecutionService) wake(c agentCommit) {
	switch {
	case c.runID == "":
	case c.terminal:
		s.postCommit(c.runID, c.lastSeq)
	default:
		s.notifyCommitted(c.runID, c.lastSeq)
	}
}

// ExecuteAgentAction executes one committed TOOL_CALL Agent Action. It is the single Tool
// execution path: immediate advancement after a Decision commits and the Reconciler's
// rediscovery of a READY Action both enter here, differing only in claimSource.
//
// It runs two transactions with one Tool call between them:
//
//  1. Lock the Run, conditionally claim the Action READY->RUNNING, validate the committed
//     Decision against the frozen allowlist and the registered Tool InputSchema, create
//     the STARTED Tool Attempt and write AGENT_ACTION_STARTED. Losing the claim writes
//     nothing; a Decision that may not be executed fails the Action in this same
//     transaction and never reaches the Tool.
//  2. (after COMMIT, holding no lock) call the Tool.
//  3. Lock the Run again and commit the outcome: either the Tool result with the appended
//     Context Version, the optional new State Version and the next READY Turn, or the
//     shared Tool failure transaction.
//
// The model is never called here: the Decision this Action executes was committed by its
// Turn, and recovery advances that Decision instead of producing a new one.
func (s *ExecutionService) ExecuteAgentAction(ctx context.Context, actionID string, claimSource domain.ClaimSource) error {
	call, err := s.claimAgentAction(ctx, actionID, claimSource)
	if err != nil || call == nil {
		return err
	}

	// The Agent Run's deadline bounds the external call, so a Tool cannot hold this
	// goroutine past the point at which the Agent Run may no longer continue.
	callCtx, cancel := context.WithDeadline(ctx, call.deadline)
	defer cancel()

	result, execErr := call.executor.Execute(callCtx, call.action)
	if execErr != nil {
		if agentDeadlineExceeded(callCtx, execErr) {
			// The Agent deadline expired during the call, so this is not the Tool's
			// failure: the termination is TIMEOUT, never TOOL_ERROR.
			return s.TimeoutAgentRun(ctx, call.agentRunID)
		}
		// The MVP does not retry an Agent Tool, so the
		// Tool's own error is the Action's outcome.
		return s.failAgentToolCall(ctx, *call, domain.ExecutionError{
			Code: "TOOL_ERROR", Message: execErr.Error(),
		})
	}

	if call.async {
		return s.recordAgentToolDispatch(ctx, *call, result)
	}

	output, outputErr := agentToolOutput(result, call.toolName)
	if outputErr == nil {
		outputErr = runtime.ValidateToolResult(output, call.outputSchema)
	}
	if outputErr != nil {
		return s.failAgentToolCall(ctx, *call, domain.ExecutionError{
			Code: "TOOL_RESULT_INVALID", Message: outputErr.Error(),
		})
	}

	return s.completeAgentToolCall(ctx, *call, output)
}

// agentToolOutput reads the completed result of one synchronous Tool call.
//
// A DISPATCHED result is a contradiction between the Tool's registered ExecutionKind and
// what its Executor did, and the Runtime must not repair it: routing this Action into the
// asynchronous dispatch path would mean persisting a callback target the claim
// transaction never created. It is reported as a Tool failure, with its own code so the
// registration defect is distinguishable in Trace from a Tool that simply failed.
func agentToolOutput(result registry.ToolExecutionResult, toolName string) (json.RawMessage, error) {
	switch result.Kind {
	case registry.ToolResultCompleted:
		if result.Result == nil {
			return nil, fmt.Errorf("tool %q reported COMPLETED with no result", toolName)
		}
		return result.Result.Output, nil
	case registry.ToolResultDispatched:
		return nil, fmt.Errorf("tool %q is registered as synchronous but dispatched an external task", toolName)
	default:
		return nil, fmt.Errorf("tool %q reported unknown result kind %q", toolName, result.Kind)
	}
}

// agentActionWaitingPayload is AGENT_ACTION_WAITING (Event fields: turnId,
// actionId, toolAttemptId, callbackBindingId). It names the Binding, never the external
// task's credential.
type agentActionWaitingPayload struct {
	AgentRunID        string `json:"agentRunId"`
	TurnID            string `json:"turnId"`
	ActionID          string `json:"actionId"`
	ToolAttemptID     string `json:"toolAttemptId"`
	CallbackBindingID string `json:"callbackBindingId"`
}

// recordAgentToolDispatch is transaction 3 for an ASYNC Tool. A dispatch that
// names its external task commits, in one transaction under the Run aggregate lock, the
// Tool Attempt's DISPATCHED status, the Callback Binding that routes the callback to that
// Attempt, the Action's and the Agent NodeRun's WAITING_CALLBACK status, the Run's
// re-aggregation and AGENT_ACTION_WAITING. Any other result fails the Action through the
// shared Tool failure transaction and creates no Binding.
func (s *ExecutionService) recordAgentToolDispatch(ctx context.Context, call agentToolCall, result registry.ToolExecutionResult) error {
	switch {
	case result.Kind == registry.ToolResultCompleted:
		// The Tool is registered ASYNC but answered synchronously. The Runtime does not
		// repair a contradiction between registered metadata and behaviour: the result is
		// not consumed and the Action fails with a code of its own, so the registration
		// defect stays distinguishable in Trace from a Tool that failed.
		return s.failAgentToolCall(ctx, call, domain.ExecutionError{
			Code:    "TOOL_EXECUTION_KIND_MISMATCH",
			Message: fmt.Sprintf("tool %q is registered as asynchronous but returned a completed result", call.toolName),
		})
	case result.Kind != registry.ToolResultDispatched:
		return s.failAgentToolCall(ctx, call, domain.ExecutionError{
			Code:    "TOOL_ERROR",
			Message: fmt.Sprintf("tool %q reported unknown result kind %q", call.toolName, result.Kind),
		})
	case result.ExternalTask == nil || result.ExternalTask.ProviderID == "" || result.ExternalTask.ExternalTaskID == "":
		// A dispatch no callback can be routed to must fail explicitly: waiting on it
		// would leave the Action to the deadline and present a lost task as recoverable
		// work.
		return s.failAgentToolCall(ctx, call, domain.ExecutionError{
			Code:    "DISPATCH_WITHOUT_EXTERNAL_TASK",
			Message: fmt.Sprintf("tool %q dispatched without a provider and external task id", call.toolName),
		})
	}
	task := *result.ExternalTask

	var commit agentCommit
	var committedAt time.Time
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		run, plan, err := s.runPlan(ctx, tx, call.runID)
		if err != nil {
			return err
		}

		lock, err := tx.Runs().LockForUpdate(ctx, call.runID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()

		// The conditional updates decide the single winner: an Attempt,
		// Action or NodeRun no longer in its pre-dispatch status means the Agent timeout
		// already closed it while the Tool was dispatching. This caller then writes
		// nothing; the external task is left to the Provider, since Emberling promises no
		// cancellation.
		if err := tx.ToolAttempts().MarkDispatched(ctx, call.attemptID, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		if err := tx.AgentActions().MarkWaiting(ctx, call.actionID, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		if err := tx.NodeRuns().MarkWaiting(ctx, call.nodeRunID, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}

		// UNIQUE (provider_id, external_task_id) makes the database, not this process,
		// the authority that one external task routes to exactly one callback target.
		bindingID := s.deps.IDs.NewID(domain.IDPrefixCallbackBinding)
		if err := tx.CallbackBindings().Create(ctx, domain.CallbackBinding{
			ID:             bindingID,
			ProviderID:     task.ProviderID,
			ExternalTaskID: task.ExternalTaskID,
			TargetType:     domain.CallbackTargetToolAttempt,
			TargetID:       call.attemptID,
			CreatedAt:      now,
		}); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				return errDispatchBindingConflict
			}
			return err
		}

		if err := s.appendEvent(ctx, tx, lock, call.runID, &call.nodeRunID, domain.EventAgentActionWaiting, now, agentActionWaitingPayload{
			AgentRunID:        call.agentRunID,
			TurnID:            call.turnID,
			ActionID:          call.actionID,
			ToolAttemptID:     call.attemptID,
			CallbackBindingID: bindingID,
		}); err != nil {
			return err
		}

		// Run status is derived from NodeRun state: with the Agent NodeRun
		// waiting, the Run becomes PAUSED once nothing else is running.
		if err := s.aggregateRunLocked(ctx, tx, lock, run, plan, now); err != nil {
			return err
		}
		commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq()}
		committedAt = now
		return nil
	})
	switch {
	case errors.Is(err, errAgentTurnSuperseded):
		return nil
	case errors.Is(err, errDispatchBindingConflict):
		// The external task is already bound to another target, so this dispatch has no
		// route home and the transaction above rolled back with the Attempt still STARTED.
		// Re-dispatching would hit the same conflict, so the Action fails explicitly.
		return s.failAgentToolCall(ctx, call, domain.ExecutionError{
			Code:    "CALLBACK_BINDING_CONFLICT",
			Message: fmt.Sprintf("external task id %q is already bound to another callback target", task.ExternalTaskID),
		})
	case err != nil:
		return err
	}
	s.wake(commit)

	// A callback that reached the endpoint before this Binding committed was stored as a
	// Pending Callback. Now that the Binding routes it, it is replayed through the same
	// resume use case a live callback enters, which consumes the row in
	// the transaction that acts on it. A failure here leaves the Action waiting,
	// bounded by the Agent deadline. The replay ends at its commit: the next READY Turn goes
	// to the work queue, so this dispatcher never nests a whole further Turn
	// chain -- model call, Tool calls -- inside its own call stack.
	s.consumeEarlyCallback(ctx, task.ExternalTaskID, call.attemptID, committedAt)
	return nil
}

// claimAgentAction is transaction 1. It returns nil without error when this caller may not
// execute the Action: another advancement path already won the claim, the Action is no
// longer READY, the Run has already reached a terminal status, or the Action is a FINAL
// one, which belongs to the Final completion use case.
func (s *ExecutionService) claimAgentAction(ctx context.Context, actionID string, claimSource domain.ClaimSource) (*agentToolCall, error) {
	if !claimSource.IsValid() {
		return nil, fmt.Errorf("execution: execute agent action %s: unknown claim source %q", actionID, claimSource)
	}

	var call *agentToolCall
	var commit agentCommit

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		action, err := tx.AgentActions().Get(ctx, actionID)
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

		lock, err := tx.Runs().LockForUpdate(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		if lock.Status().IsTerminal() {
			return nil
		}
		// A FINAL Action is left exactly as it was committed: READY, and therefore still
		// recoverable work for the use case that owns it.
		if action.Type != domain.AgentActionToolCall {
			return nil
		}

		decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
		if err != nil {
			return err
		}

		now := s.deps.Clock.Now()
		won, err := tx.AgentActions().ClaimReady(ctx, actionID, now)
		if err != nil {
			return err
		}
		if !won {
			return nil
		}

		toolName := ""
		if decision.ToolName != nil {
			toolName = *decision.ToolName
		}
		reg, registered := s.deps.Tools.Get(toolName)
		if !registered {
			// The frozen allowlist is a Run-level authority decision, so it answers first:
			// a Tool the Agent Run never allowed is an invalid Action whatever the current
			// Registry holds.
			termination := domain.TerminationInvalidAction
			execError := domain.ExecutionError{
				Code:    "INVALID_ACTION",
				Message: fmt.Sprintf("tool %q is not in the Agent Run's frozen allowlist", toolName),
			}
			if slices.Contains(agentRun.AllowedTools, toolName) {
				// Registry drift: the allowlist resolved when the Agent Run was created
				// and no longer does. There is nothing to call and nothing to substitute
				// (CLAUDE.md "Extensions and external calls"), so the Tool call itself is
				// what failed.
				termination = domain.TerminationToolError
				execError = domain.ExecutionError{
					Code:    "TOOL_NOT_REGISTERED",
					Message: fmt.Sprintf("tool %q of the Agent Run's frozen allowlist is no longer registered", toolName),
				}
			}
			if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID, nil, domain.AgentActionRunning,
				domain.FailureSyncExecution, termination, execError, now); err != nil {
				return err
			}
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		if validationErr := runtime.ValidateToolCall(decision, agentRun.AllowedTools, reg.Metadata); validationErr != nil {
			// A deterministic, committed fact is invalid: no Tool Attempt is created, the
			// Tool is not called and the model is not asked again.
			if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID, nil, domain.AgentActionRunning,
				domain.FailureSyncExecution, domain.TerminationInvalidAction,
				domain.ExecutionError{Code: "INVALID_ACTION", Message: validationErr.Error()}, now); err != nil {
				return err
			}
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		// Attempt 1 is the only Attempt a TOOL_CALL Action ever gets in the MVP; attempt_no
		// exists so a Tool call carries the same audit and callback target model as a Node
		// Attempt. A synchronous call saves no callback token.
		attemptID := s.deps.IDs.NewID(domain.IDPrefixToolAttempt)

		// An ASYNC Tool gets an Attempt-scoped callback credential issued here, before the
		// call, so its hash is committed before any Provider can deliver a callback
		// (the credential is persisted in the claim transaction). Only the hash
		// is stored; the plaintext token reaches the Executor through CallbackContext and
		// nowhere else. As on the Node path, the credential outlives the Agent deadline by
		// the Pending Callback TTL, so a callback racing the timeout transaction competes
		// through the conditional update instead of being refused on its credential.
		async := reg.Metadata.ExecutionKind == domain.ToolExecutionAsync
		var tokenHash *string
		var callbackCtx *registry.CallbackContext
		if async {
			var expiresAt time.Time
			if !agentRun.Deadline.IsZero() {
				expiresAt = agentRun.Deadline.Add(s.deps.Callback.PendingTTL)
			}
			token, err := issueCallbackToken(s.deps.Callback.SigningSecret, attemptID, expiresAt)
			if err != nil {
				return fmt.Errorf("execution: issue callback credential for tool %q of action %s: %w", toolName, action.ID, err)
			}
			hash := hashCallbackToken(token)
			tokenHash = &hash
			callbackCtx = &registry.CallbackContext{URL: s.deps.Callback.BaseURL + CallbackPath, Token: token}
		}

		if err := tx.ToolAttempts().Create(ctx, domain.ToolAttempt{
			ID:                attemptID,
			ActionID:          action.ID,
			AttemptNo:         1,
			ToolName:          toolName,
			Status:            domain.ToolAttemptStarted,
			Input:             decision.Arguments,
			CallbackTokenHash: tokenHash,
			StartedAt:         now,
		}); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentActionStarted, now, agentActionStartedPayload{
			AgentRunID:    agentRun.ID,
			TurnID:        turn.ID,
			ActionID:      action.ID,
			ToolAttemptID: &attemptID,
			ClaimSource:   claimSource,
		}); err != nil {
			return err
		}
		// The claim changes no Run-level status; UpdateAggregate persists the seq watermark
		// AGENT_ACTION_STARTED allocated under the lock.
		if err := tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now); err != nil {
			return err
		}

		commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq()}
		call = &agentToolCall{
			runID:        nodeRun.RunID,
			nodeRunID:    nodeRun.ID,
			agentRunID:   agentRun.ID,
			turnID:       turn.ID,
			actionID:     action.ID,
			attemptID:    attemptID,
			toolName:     toolName,
			executor:     reg.Executor,
			outputSchema: reg.Metadata.OutputSchema,
			action: registry.ToolAction{
				AgentRunID: agentRun.ID,
				TurnID:     turn.ID,
				ActionID:   action.ID,
				ToolName:   toolName,
				AttemptNo:  1,
				Arguments:  decision.Arguments,
				Callback:   callbackCtx,
			},
			async:    async,
			deadline: agentRun.Deadline,
		}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.wake(commit)
	return call, nil
}

// completeAgentToolCall is transaction 3's success half: the whole successful round
// commits together -- the Tool result, the Action's
// SUCCEEDED status, the appended Context Version, the new State Version when the Decision's
// patch really changed the State, the Agent Run's pointers, and either the next READY Turn
// or the termination the round or time limit demands.
func (s *ExecutionService) completeAgentToolCall(ctx context.Context, call agentToolCall, result json.RawMessage) error {
	outcome, err := s.commitAgentToolResult(ctx, call, syncToolOutcome, result)
	if err != nil || outcome.nextTurnID == "" {
		return err
	}
	// The next Turn is committed READY work by the time this line runs, and it is handed
	// to the bounded work queue rather than advanced by calling AdvanceAgentTurn from here
	// (advancement does not use unbounded synchronous recursion; each committed
	// READY item is a new queue item and a new transaction). The synchronous Tool path and
	// the asynchronous resume therefore continue the loop the same way, and this goroutine
	// returns with a flat stack after every round instead of nesting one Turn's use case
	// inside the previous Turn's. A refused enqueue, or a process that dies before the
	// worker dequeues the item, leaves the READY Turn to the Reconciler's rediscovery
	// (the persisted-work recovery rule).
	s.enqueueAgentTurn(call.runID, outcome.nextTurnID)
	return nil
}

// agentToolOutcomeSource says which path delivers a Tool Attempt's outcome and therefore
// which statuses the conditional updates must find: the synchronous call
// resolves a STARTED Attempt of a RUNNING Action, while a callback or Provider poll
// resolves a DISPATCHED Attempt of a WAITING_CALLBACK Action and also returns the waiting
// Agent NodeRun to RUNNING when the loop continues. Requiring the expected "from" status
// is what makes a duplicated, late or timed-out delivery lose without writing anything.
type agentToolOutcomeSource struct {
	fromAttempt      domain.ToolAttemptStatus
	fromAction       domain.AgentActionStatus
	completionSource domain.CompletionSource
	failureSource    domain.FailureSource
	// resumesWaiting is true for the asynchronous resume: the Agent NodeRun is
	// WAITING_CALLBACK and the Run may be PAUSED, so continuing the loop moves the NodeRun
	// back to RUNNING and re-derives the Run status.
	resumesWaiting bool
	// consumePending is the external task id of a stored early callback this outcome is
	// the replay of, or "". The row is consumed in the same transaction that acts on it,
	// so an early callback can advance an Action at most once.
	consumePending string
	payloadHash    string
	// attemptTokenHash is the Tool Attempt's own callback credential hash, which a
	// replayed Pending Callback must match. It is compared, never written.
	attemptTokenHash *string
}

var syncToolOutcome = agentToolOutcomeSource{
	fromAttempt:      domain.ToolAttemptStarted,
	fromAction:       domain.AgentActionRunning,
	completionSource: domain.CompletionSyncExecution,
	failureSource:    domain.FailureSyncExecution,
}

// asyncToolOutcome is the outcome source of a callback or Provider-poll resume of a
// DISPATCHED Tool Attempt.
func asyncToolOutcome(completion domain.CompletionSource, consumePending, payloadHash string, attemptTokenHash *string) agentToolOutcomeSource {
	failure := domain.FailureCallback
	if completion == domain.CompletionProviderPoll {
		failure = domain.FailureProviderPoll
	}
	return agentToolOutcomeSource{
		fromAttempt:      domain.ToolAttemptDispatched,
		fromAction:       domain.AgentActionWaitingCallback,
		completionSource: completion,
		failureSource:    failure,
		resumesWaiting:   true,
		consumePending:   consumePending,
		payloadHash:      payloadHash,
		attemptTokenHash: attemptTokenHash,
	}
}

// agentToolCommit reports what a Tool outcome transaction did. committed is false when a
// conditional update lost, in which case nothing was written and no Event seq consumed.
type agentToolCommit struct {
	committed  bool
	nextTurnID string
}

// consumeReplayedPendingLocked consumes the stored early callback src replays, inside the
// transaction that acts on it. A row that is already consumed, expired or carries another
// payload means another delivery won, which rolls this transaction back as superseded.
func (s *ExecutionService) consumeReplayedPendingLocked(ctx context.Context, tx store.Tx, call agentToolCall, src agentToolOutcomeSource, now time.Time) error {
	err := s.consumePendingCallback(ctx, tx, call.attemptID, src.attemptTokenHash, src.consumePending, src.payloadHash, now)
	if errors.Is(err, errResumeSuperseded) {
		return errAgentTurnSuperseded
	}
	return err
}

// commitAgentToolResult is the transaction that commits one successful Tool outcome and
// performs the post-COMMIT wake-up it owes. It does not chain the next Turn; the caller
// decides that.
func (s *ExecutionService) commitAgentToolResult(ctx context.Context, call agentToolCall, src agentToolOutcomeSource, result json.RawMessage) (agentToolCommit, error) {
	var commit agentCommit
	var nextTurnID string

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		// The resume re-derives the Run status, which needs the compiled plan of the Run's
		// immutable Definition version; it is resolved before the lock.
		var plan *runtime.CompiledDefinition
		var run domain.Run
		if src.resumesWaiting {
			var err error
			if run, plan, err = s.runPlan(ctx, tx, call.runID); err != nil {
				return err
			}
		}

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
		decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()

		if err := s.consumeReplayedPendingLocked(ctx, tx, call, src, now); err != nil {
			return err
		}
		// The conditional Attempt update is what decides that this caller, and not a stale
		// duplicate of the same Tool call, a late callback or the timeout, owns the result.
		if err := tx.ToolAttempts().MarkSucceeded(ctx, call.attemptID, src.fromAttempt, now, result); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}

		// The state patch is decided before anything else is written: a patch that cannot
		// be applied must leave no Context Version, no State Version and no moved pointer
		// behind.
		previousState, err := tx.AgentStateVersions().GetByRunAndVersion(ctx, agentRun.ID, agentRun.CurrentStateVersion)
		if err != nil {
			return err
		}
		patchedState, stateChanged, patchErr := runtime.ApplyStatePatch(previousState.Value, decision.StatePatch)
		if patchErr == nil {
			patchErr = runtime.ValidateAgentState(patchedState, frozenValue(agentRun.StateSchema))
		}
		if patchErr != nil {
			// The Tool really succeeded, so its Attempt keeps its result; the Action is
			// what fails, deterministically and without a second attempt at the same patch.
			attemptID := call.attemptID
			if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, call.turnID, call.actionID, &attemptID,
				src.fromAction, src.failureSource, domain.TerminationInvalidAction,
				domain.ExecutionError{Code: "INVALID_ACTION", Message: patchErr.Error()}, now); err != nil {
				return err
			}
			commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		if err := tx.AgentActions().MarkSucceeded(ctx, call.actionID, src.fromAction, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		attemptID := call.attemptID
		if err := s.appendEvent(ctx, tx, lock, call.runID, &nodeRun.ID, domain.EventAgentActionCompleted, now, agentActionCompletedPayload{
			AgentRunID:       agentRun.ID,
			TurnID:           call.turnID,
			ActionID:         call.actionID,
			ToolAttemptID:    &attemptID,
			CompletionSource: src.completionSource,
		}); err != nil {
			return err
		}

		messages, err := s.appendToolRoundContext(ctx, tx, agentRun, call, decision, result, now)
		if err != nil {
			return err
		}
		nextContextVersion := agentRun.CurrentContextVersion + 1

		nextStateVersion := agentRun.CurrentStateVersion
		if stateChanged {
			nextStateVersion = agentRun.CurrentStateVersion + 1
			turnID := call.turnID
			if err := tx.AgentStateVersions().Create(ctx, domain.AgentStateVersion{
				ID:           s.deps.IDs.NewID(domain.IDPrefixAgentStateVersion),
				AgentRunID:   agentRun.ID,
				Version:      nextStateVersion,
				SourceTurnID: &turnID,
				Value:        patchedState,
				CreatedAt:    now,
			}); err != nil {
				return err
			}
			if err := s.appendEvent(ctx, tx, lock, call.runID, &nodeRun.ID, domain.EventAgentStateUpdated, now, agentStateUpdatedPayload{
				AgentRunID:           agentRun.ID,
				TurnID:               call.turnID,
				ContextVersion:       nextContextVersion,
				PreviousStateVersion: agentRun.CurrentStateVersion,
				StateVersion:         nextStateVersion,
			}); err != nil {
				return err
			}
		}

		// The round and time limits are checked before the next Turn is created, so no Turn
		// is ever left that nobody is allowed to execute.
		nextTurnNo := agentRun.CurrentTurnNo + 1
		termination := domain.AgentTermination("")
		var execError domain.ExecutionError
		switch {
		case nextTurnNo > agentRun.MaxTurns:
			termination = domain.TerminationMaxTurns
			execError = domain.ExecutionError{
				Code:    "MAX_TURNS",
				Message: fmt.Sprintf("the agent reached its frozen bound of %d turns", agentRun.MaxTurns),
			}
		case !now.Before(agentRun.Deadline):
			termination = domain.TerminationTimeout
			execError = domain.ExecutionError{Code: "TIMEOUT", Message: "the agent deadline expired"}
		}

		from := store.AgentRunPointers{
			CurrentTurnNo:         agentRun.CurrentTurnNo,
			CurrentContextVersion: agentRun.CurrentContextVersion,
			CurrentStateVersion:   agentRun.CurrentStateVersion,
		}
		to := store.AgentRunPointers{
			CurrentTurnNo:         agentRun.CurrentTurnNo,
			CurrentContextVersion: nextContextVersion,
			CurrentStateVersion:   nextStateVersion,
		}
		if termination == "" {
			to.CurrentTurnNo = nextTurnNo
		}
		advanced, err := tx.AgentRuns().AdvancePointers(ctx, agentRun.ID, from, to)
		if err != nil {
			return err
		}
		if !advanced {
			// Another transaction already moved this Agent Run's recovery position, so it
			// owns this round's outcome; nothing here may overwrite it.
			return errAgentTurnSuperseded
		}

		if termination != "" {
			if err := s.terminateAgentRunLocked(ctx, tx, lock, nodeRun, agentRun, call.turnID, termination, execError, now); err != nil {
				return err
			}
			commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		if src.resumesWaiting {
			// The loop continues, so the Agent NodeRun leaves WAITING_CALLBACK in the same
			// transaction that creates the next Turn. A NodeRun no longer waiting
			// means another path already resolved it.
			if err := tx.NodeRuns().Transition(ctx, nodeRun.ID, domain.NodeRunWaitingCallback, domain.NodeRunRunning, now); err != nil {
				if errors.Is(err, domain.ErrStaleClaim) {
					return errAgentTurnSuperseded
				}
				return err
			}
		}

		turnID := s.deps.IDs.NewID(domain.IDPrefixAgentTurn)
		requestBytes, err := json.Marshal(agentTurnRequest{
			ModelID:        agentRun.ModelID,
			ContextVersion: nextContextVersion,
			StateVersion:   nextStateVersion,
			AllowedTools:   agentRun.AllowedTools,
			MessageCount:   len(messages),
		})
		if err != nil {
			return fmt.Errorf("execution: encode agent turn request for agent run %s: %w", agentRun.ID, err)
		}
		if err := tx.AgentTurns().Create(ctx, domain.AgentTurn{
			ID:         turnID,
			AgentRunID: agentRun.ID,
			TurnNo:     nextTurnNo,
			Status:     domain.AgentTurnReady,
			Request:    requestBytes,
		}); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, lock, call.runID, &nodeRun.ID, domain.EventAgentTurnReady, now, agentTurnPayload{
			AgentRunID:     agentRun.ID,
			TurnID:         turnID,
			TurnNo:         nextTurnNo,
			ContextVersion: nextContextVersion,
			StateVersion:   nextStateVersion,
		}); err != nil {
			return err
		}
		if src.resumesWaiting {
			// With the Agent NodeRun RUNNING again, a PAUSED Run becomes RUNNING and
			// RUN_RESUMED commits in the same transaction.
			if err := s.aggregateRunLocked(ctx, tx, lock, run, plan, now); err != nil {
				return err
			}
		} else if err := tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now); err != nil {
			return err
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
	return agentToolCommit{committed: true, nextTurnID: nextTurnID}, nil
}

// runPlan reads a Run and compiles the immutable Definition version it is bound to.
func (s *ExecutionService) runPlan(ctx context.Context, tx store.Tx, runID string) (domain.Run, *runtime.CompiledDefinition, error) {
	run, err := tx.Runs().Get(ctx, runID)
	if err != nil {
		return domain.Run{}, nil, err
	}
	def, err := tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
	if err != nil {
		return domain.Run{}, nil, err
	}
	plan, err := s.compile(ctx, def)
	if err != nil {
		return domain.Run{}, nil, fmt.Errorf("execution: recompile definition %s v%d for run %s: %w", run.WorkflowID, run.DefinitionVersion, run.ID, err)
	}
	return run, plan, nil
}

// aggregateRunLocked derives the Run status from its NodeRuns under the Run
// aggregate lock the caller holds, writes the Run transition Event when the status
// changes, and persists the status together with the seq watermark.
func (s *ExecutionService) aggregateRunLocked(ctx context.Context, tx store.Tx, lock *store.RunLock, run domain.Run, plan *runtime.CompiledDefinition, now time.Time) error {
	allNodeRuns, err := tx.NodeRuns().ListByRun(ctx, run.ID)
	if err != nil {
		return err
	}
	nodeStatuses := make(map[string]domain.NodeRunStatus, len(allNodeRuns))
	for _, nr := range allNodeRuns {
		nodeStatuses[nr.NodeID] = nr.Status
	}
	newStatus := runtime.AggregateRunStatus(runtime.RunAggregateInput{
		NodeStatuses:   nodeStatuses,
		AllNodeIDs:     plan.Order,
		OutputNodeID:   plan.OutputNodeID,
		OutputProduced: len(run.Output) > 0,
	})
	if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), newStatus); changed {
		if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: newStatus}); err != nil {
			return err
		}
	}
	return tx.Runs().UpdateAggregate(ctx, lock, newStatus, now)
}

// appendToolRoundContext creates the Context Version one successful Tool round produces:
// every message of the previous version, unchanged and in order, followed by the committed
// TOOL_CALL Decision as an `assistant` message and the Tool result as a `tool` message
// linked to the call by the stable Action ID and Tool Name.
// History is never reordered or rewritten.
func (s *ExecutionService) appendToolRoundContext(
	ctx context.Context,
	tx store.Tx,
	agentRun domain.AgentRun,
	call agentToolCall,
	decision domain.AgentDecision,
	result json.RawMessage,
	now time.Time,
) ([]runtime.AgentContextMessage, error) {
	previous, err := tx.AgentContextVersions().GetByRunAndVersion(ctx, agentRun.ID, agentRun.CurrentContextVersion)
	if err != nil {
		return nil, err
	}
	stored, err := runtime.DecodeAgentContext(previous.Messages)
	if err != nil {
		return nil, fmt.Errorf("execution: agent run %s context version %d: %w",
			agentRun.ID, previous.Version, err)
	}

	decisionContent, err := json.Marshal(agentToolCallMessage{
		Kind:      decision.Kind,
		ToolName:  call.toolName,
		Arguments: decision.Arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("execution: encode agent tool call message for action %s: %w", call.actionID, err)
	}

	actionID := call.actionID
	toolName := call.toolName
	messages := make([]runtime.AgentContextMessage, 0, len(stored)+2)
	messages = append(messages, stored...)
	messages = append(messages,
		runtime.AgentContextMessage{
			Role:         runtime.AgentRoleAssistant,
			Content:      decisionContent,
			ToolName:     &toolName,
			ToolActionID: &actionID,
		},
		runtime.AgentContextMessage{
			Role:         runtime.AgentRoleTool,
			Content:      result,
			ToolName:     &toolName,
			ToolActionID: &actionID,
		},
	)

	messageBytes, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("execution: encode agent context version for agent run %s: %w", agentRun.ID, err)
	}
	turnID := call.turnID
	if err := tx.AgentContextVersions().Create(ctx, domain.AgentContextVersion{
		ID:           s.deps.IDs.NewID(domain.IDPrefixAgentContextVersion),
		AgentRunID:   agentRun.ID,
		Version:      agentRun.CurrentContextVersion + 1,
		SourceTurnID: &turnID,
		Messages:     messageBytes,
		CreatedAt:    now,
	}); err != nil {
		return nil, err
	}
	return messages, nil
}

// failAgentToolCall is the shared Tool failure use case:
// one transaction that takes the failure completion
// right from the current Tool Attempt and Action status and then commits, atomically, the
// Tool Attempt's FAILED status, the Action's FAILED status, the Agent Run's TOOL_ERROR
// termination, the Agent NodeRun's failure, the Run's re-aggregation and the three Events.
//
// It applies no state patch, creates no Context or State Version and starts no next Turn.
// The committed Decision is left untouched, and the MVP does not retry the Tool.
//
// This is the synchronous entry, with failureSource SYNC_EXECUTION; the callback and
// Provider-poll resume enter the same transaction body (commitAgentToolFailure) with their
// own source and "from" statuses.
func (s *ExecutionService) failAgentToolCall(ctx context.Context, call agentToolCall, execError domain.ExecutionError) error {
	_, err := s.commitAgentToolFailure(ctx, call, syncToolOutcome, execError)
	return err
}

// commitAgentToolFailure is the transaction body of the shared Tool failure use case.
func (s *ExecutionService) commitAgentToolFailure(ctx context.Context, call agentToolCall, src agentToolOutcomeSource, execError domain.ExecutionError) (agentToolCommit, error) {
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
		now := s.deps.Clock.Now()

		if err := s.consumeReplayedPendingLocked(ctx, tx, call, src, now); err != nil {
			return err
		}
		if err := tx.ToolAttempts().MarkFailed(ctx, call.attemptID, src.fromAttempt, now, execError); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		attemptID := call.attemptID
		if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, call.turnID, call.actionID, &attemptID,
			src.fromAction, src.failureSource, domain.TerminationToolError, execError, now); err != nil {
			return err
		}

		commit = agentCommit{runID: call.runID, lastSeq: lock.LastSeq(), terminal: true}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return agentToolCommit{}, nil
	}
	if err != nil {
		return agentToolCommit{}, err
	}
	s.wake(commit)
	return agentToolCommit{committed: true}, nil
}

// failAgentActionLocked fails one Agent Action and the Agent Run it belongs to, inside a
// transaction whose Run aggregate lock the caller already holds. It is the shared tail of
// every Action failure: an invalid committed Decision, a failed Tool call and a state
// patch that cannot be applied.
//
// The Action's conditional update can lose -- the Action is no longer in fromAction
// (RUNNING for a synchronous path, WAITING_CALLBACK for an asynchronous resume), so
// another path already completed it -- which rolls the whole transaction back through
// errAgentTurnSuperseded and leaves this caller writing nothing. The Turn is deliberately
// not touched: it completed when the model answered.
func (s *ExecutionService) failAgentActionLocked(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	nodeRun domain.NodeRun,
	agentRun domain.AgentRun,
	turnID string,
	actionID string,
	attemptID *string,
	fromAction domain.AgentActionStatus,
	failureSource domain.FailureSource,
	termination domain.AgentTermination,
	execError domain.ExecutionError,
	now time.Time,
) error {
	if err := tx.AgentActions().MarkFailed(ctx, actionID, fromAction, now, execError); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return errAgentTurnSuperseded
		}
		return err
	}
	return s.recordAgentActionFailureLocked(ctx, tx, lock, nodeRun, agentRun, turnID, actionID, attemptID,
		failureSource, termination, execError, now)
}

// recordAgentActionFailureLocked is failAgentActionLocked without the Action's own status
// update: AGENT_ACTION_FAILED plus the Agent Run termination tail. The Agent timeout
// transaction needs exactly this half, because the Action it fails may still be READY and
// therefore takes a different conditional update (store.AgentActionRepository.MarkTimedOut)
// than every other failure path.
func (s *ExecutionService) recordAgentActionFailureLocked(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	nodeRun domain.NodeRun,
	agentRun domain.AgentRun,
	turnID string,
	actionID string,
	attemptID *string,
	failureSource domain.FailureSource,
	termination domain.AgentTermination,
	execError domain.ExecutionError,
	now time.Time,
) error {
	if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentActionFailed, now, agentActionFailedPayload{
		AgentRunID:    agentRun.ID,
		TurnID:        turnID,
		ActionID:      actionID,
		ToolAttemptID: attemptID,
		FailureSource: failureSource,
		Error:         execError,
	}); err != nil {
		return err
	}
	return s.terminateAgentRunLocked(ctx, tx, lock, nodeRun, agentRun, turnID, termination, execError, now)
}
