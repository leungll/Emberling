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

// agentActionFailedPayload is AGENT_ACTION_FAILED. ReturnedToAgent is true only when the
// failure is a definite refusal handed back to the Agent on the return path; it is omitted
// for every failure that ends the Agent Run directly. It says how the failure was handled,
// not that a new Turn exists: when the round advancement in the same transaction then ends
// the Agent Run as TIMEOUT or MAX_TURNS, no Turn receives the refusal message, so a Trace
// reader pairs this Event with the termination Event that follows it.
type agentActionFailedPayload struct {
	AgentRunID      string                `json:"agentRunId"`
	TurnID          string                `json:"turnId"`
	ActionID        string                `json:"actionId"`
	ToolAttemptID   *string               `json:"toolAttemptId,omitempty"`
	FailureSource   domain.FailureSource  `json:"failureSource"`
	Error           domain.ExecutionError `json:"error"`
	ReturnedToAgent bool                  `json:"returnedToAgent,omitempty"`
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
	// records are the record kinds the Tool's registration lets a result carry, read
	// under the claim as outputSchema is.
	records []domain.ToolRecordKind
	action  registry.ToolAction
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
//     Decision against the frozen allowlist and the registered Tool InputSchema, check
//     the fact requirements the Tool declares against the Run's committed facts and the
//     Agent Run's frozen generation limit, create
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
		// Tool's own error, bounded, is the Action's outcome.
		const code = "TOOL_ERROR"
		s.logExecutionFailure(ctx, "agent tool call failed", code, execErr,
			"run_id", call.runID, "agent_run_id", call.agentRunID, "turn_id", call.turnID,
			"action_id", call.actionID, "tool_name", call.toolName)
		return s.failAgentToolCall(ctx, *call, boundedExecutionError(code, execErr))
	}

	if call.async {
		return s.recordAgentToolDispatch(ctx, *call, result)
	}

	output, outputErr := agentToolOutput(result, call.toolName)
	if outputErr == nil {
		outputErr = runtime.ValidateToolResult(output, call.outputSchema)
	}
	var artifacts []domain.ArtifactRef
	if outputErr == nil {
		artifacts = result.Result.Artifacts
		outputErr = validateDeclaredArtifacts(artifacts, call.toolName)
	}
	if outputErr == nil {
		outputErr = validateDeclaredRecords(*result.Result, call.records, call.toolName)
	}
	if outputErr != nil {
		return s.failAgentToolCall(ctx, *call, domain.ExecutionError{
			Code: "TOOL_RESULT_INVALID", Message: outputErr.Error(),
		})
	}

	// The validated output, the declared artifacts, the declared records and the
	// reported charge commit together as the Attempt's result.
	return s.completeAgentToolCall(ctx, *call, registry.ToolResult{
		Output: output, Artifacts: artifacts, Cost: result.Result.Cost,
		WorkspacePatch: result.Result.WorkspacePatch, ChangeRequest: result.Result.ChangeRequest,
	})
}

// validateDeclaredRecords rejects a result that carries a record of a kind the Tool's
// registration does not declare. Which records a Tool may produce is a registered fact,
// never inferred from the result or the Tool Name, so an undeclared record is an invalid
// result rather than something to persist or drop silently.
func validateDeclaredRecords(result registry.ToolResult, declared []domain.ToolRecordKind, toolName string) error {
	carried := []struct {
		kind    domain.ToolRecordKind
		present bool
	}{
		{domain.ToolRecordWorkspacePatch, result.WorkspacePatch != nil},
		{domain.ToolRecordChangeRequest, result.ChangeRequest != nil},
	}
	for _, record := range carried {
		if record.present && !slices.Contains(declared, record.kind) {
			return fmt.Errorf("tool %q returned a %s record its registration does not declare", toolName, record.kind)
		}
	}
	return nil
}

// validateDeclaredArtifacts rejects a declared Execution Artifact whose reference is not
// internally consistent, before any of its metadata can become a row. An artifact declared
// twice is rejected too: the result transaction links each declaration to the Attempt
// once, so a repeated one would fail that transaction instead of failing the Tool call.
func validateDeclaredArtifacts(artifacts []domain.ArtifactRef, toolName string) error {
	declared := make(map[string]bool, len(artifacts))
	for _, ref := range artifacts {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("tool %q declared an invalid artifact: %w", toolName, err)
		}
		if declared[ref.ArtifactID] {
			return fmt.Errorf("tool %q declared artifact %s more than once", toolName, ref.ArtifactID)
		}
		declared[ref.ArtifactID] = true
	}
	return nil
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
		// A registration that declares a poll policy schedules its first poll one
		// interval after dispatch, as a scheduling fact in this same transaction; it
		// carries no Event. Tools without a policy are never polled.
		var firstPollAt *time.Time
		if reg, ok := s.deps.Tools.Get(call.toolName); ok && reg.Metadata.Poll != nil {
			first := now.Add(time.Duration(reg.Metadata.Poll.IntervalMs) * time.Millisecond)
			firstPollAt = &first
		}
		if err := tx.ToolAttempts().MarkDispatched(ctx, call.attemptID, now, firstPollAt); err != nil {
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
	// nextTurnID is the READY Turn a refusal returned to the Agent created, offered to
	// the work queue only after COMMIT.
	var nextTurnID string

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
		// Re-read what the lock now protects: the Agent Run's context and state pointers
		// and the NodeRun status were read before the lock, and a result, a timeout or a
		// returned refusal may have committed in between.
		if agentRun, err = tx.AgentRuns().Get(ctx, agentRun.ID); err != nil {
			return err
		}
		if nodeRun, err = tx.NodeRuns().Get(ctx, nodeRun.ID); err != nil {
			return err
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

		// A valid call of a Tool that requires committed facts is still not executable
		// until those facts hold. An unmet requirement is decided before any Attempt
		// exists, so no external call can follow. A Tool registered to return an unmet
		// requirement hands the Agent a VERIFICATION_MISSING refusal naming the missing
		// fact type, and the Agent decides the next step in a new Turn; every other Tool
		// fails the Action exactly like an invalid Decision.
		unmet, err := s.unmetFactRequirementLocked(ctx, tx, nodeRun.RunID, reg.Metadata.Requires, decision.Arguments)
		if err != nil {
			return err
		}
		if unmet != nil && reg.Metadata.ReturnUnmetRequirement {
			turnID, err := s.returnActionToAgentLocked(ctx, tx, agentActionReturn{
				lock: lock, nodeRun: nodeRun, agentRun: agentRun, turn: turn, action: action, toolName: toolName,
			}, domain.AgentActionRunning, domain.ReturnVerificationMissing, verificationMissingMessage(*unmet), now)
			if err != nil {
				return err
			}
			nextTurnID = turnID
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: turnID == ""}
			return nil
		}
		if unmet != nil {
			execError := unmet.ExecutionError()
			if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID, nil, domain.AgentActionRunning,
				domain.FailureSyncExecution, domain.TerminationInvalidAction, execError, now); err != nil {
				return err
			}
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		// A counted Tool call past the Agent Run's frozen generation limit is rejected the
		// same way, still before any Attempt exists; the Run lock serializes concurrent
		// claims, so the limit can never be overrun.
		overLimit, err := s.generationLimitReachedLocked(ctx, tx, agentRun, reg.Metadata)
		if err != nil {
			return err
		}
		if overLimit != nil {
			if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID, nil, domain.AgentActionRunning,
				domain.FailureSyncExecution, domain.TerminationInvalidAction, *overLimit, now); err != nil {
				return err
			}
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		// A Tool that runs with the node's image model receives the Agent Run's frozen image
		// Model ID, re-resolved here because configuration can change across a restart. An
		// ID that no longer resolves fails the Tool call itself, before any Attempt exists,
		// so nothing external is called and no other model is substituted.
		imageModelID := ""
		if reg.Metadata.UsesImageModel {
			imageModelID = frozenImageModel(agentRun)
			if unusable := unusableModel(s.deps.Models, "image model", imageModelID, domain.ModelCapabilityReferenceImageGeneration); unusable != nil {
				if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID, nil, domain.AgentActionRunning,
					domain.FailureSyncExecution, domain.TerminationToolError, *unusable, now); err != nil {
					return err
				}
				commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
				return nil
			}
		}

		// A Tool that reviews with the node's review model receives the Agent Run's frozen
		// review Model ID under the same rule. An Agent Run that froze none passes no ID,
		// and the Tool applies its mock policy; a frozen ID that no longer resolves fails
		// here and never falls back to that policy.
		reviewModelID := ""
		if reg.Metadata.UsesReviewModel && agentRun.ReviewModelID != nil {
			reviewModelID = frozenReviewModel(agentRun)
			if unusable := unusableModel(s.deps.Models, "review model", reviewModelID, domain.ModelCapabilityVisionInput); unusable != nil {
				if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID, nil, domain.AgentActionRunning,
					domain.FailureSyncExecution, domain.TerminationToolError, *unusable, now); err != nil {
					return err
				}
				commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
				return nil
			}
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

		// The claiming process's identity is written with the STARTED row itself, so a
		// committed Attempt always names the process that will make the call.
		runnerID := s.deps.RunnerID
		if err := tx.ToolAttempts().Create(ctx, domain.ToolAttempt{
			ID:                attemptID,
			ActionID:          action.ID,
			AttemptNo:         1,
			ToolName:          toolName,
			Status:            domain.ToolAttemptStarted,
			Input:             decision.Arguments,
			CallbackTokenHash: tokenHash,
			RunnerID:          &runnerID,
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
			records:      reg.Metadata.Records,
			action: registry.ToolAction{
				RunID:         nodeRun.RunID,
				AgentRunID:    agentRun.ID,
				TurnID:        turn.ID,
				ActionID:      action.ID,
				ToolName:      toolName,
				AttemptNo:     1,
				Arguments:     decision.Arguments,
				ImageModelID:  imageModelID,
				ReviewModelID: reviewModelID,
				Callback:      callbackCtx,
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
	if nextTurnID != "" {
		// The READY Turn is committed; a refused enqueue leaves it to the Reconciler.
		s.enqueueAgentTurn(commit.runID, nextTurnID)
	}
	return call, nil
}
