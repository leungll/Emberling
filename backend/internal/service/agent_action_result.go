package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the commit of a Tool outcome for an Agent Action: the success round and the shared Tool failure transaction.

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

		// The declared fact is decided, like the patch, before any write that belongs only
		// to a successful round, so a result that cannot establish it commits the shared
		// Action failure in place of the round: the Attempt keeps its result, no fact, patch
		// or next Turn is written, and the failure keeps this delivery's source.
		factErr, err := s.recordProducedFactLocked(ctx, tx, call, decision.Arguments, result, now)
		if err != nil {
			return err
		}
		if factErr != nil {
			attemptID := call.attemptID
			if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, call.turnID, call.actionID, &attemptID,
				src.fromAction, src.failureSource, domain.TerminationInvalidAction, *factErr, now); err != nil {
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
