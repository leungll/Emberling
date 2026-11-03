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
	// OperationKey and OccupationID are set for a release Attempt: the key production
	// deduplicates the operation by and the occupation of the deploy target it holds.
	OperationKey *string `json:"operationKey,omitempty"`
	OccupationID *string `json:"occupationId,omitempty"`
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
	// release is set for the Attempt of a reconcilable release Tool, whose dispatch,
	// polling and settlement take the release paths instead of the generic ones.
	release *releaseCall
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
//     Agent Run's frozen generation limit, decide the Action's authorization from the
//     Tool's declared risk and the frozen goal, create the STARTED Tool Attempt and write
//     AGENT_ACTION_STARTED. Losing the claim writes nothing; a Decision that may not be
//     executed fails the Action in this same transaction and never reaches the Tool; a
//     Decision a person must approve suspends the Action and pauses the Agent instead,
//     and this call ends after that COMMIT.
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
	return s.executeClaimedAgentAction(ctx, call)
}

// executeClaimedAgentAction is steps 2 and 3 for an Action whose claim transaction has
// committed with a STARTED Attempt: the Tool call, holding no lock, and the transaction
// that commits its outcome. The claim path enters here right after its COMMIT; the
// approval decision enters here after the transaction that approved the Action committed
// the same STARTED Attempt, so an approved Action is executed once and never claimed twice.
func (s *ExecutionService) executeClaimedAgentAction(ctx context.Context, call *agentToolCall) error {
	// The Agent Run's deadline bounds the external call, so a Tool cannot hold this
	// goroutine past the point at which the Agent Run may no longer continue.
	callCtx, cancel := context.WithDeadline(ctx, call.deadline)
	defer cancel()

	result, execErr := call.executor.Execute(callCtx, call.action)
	if call.release != nil {
		// A release is never a TIMEOUT or a TOOL_ERROR of the Agent: production either
		// accepted the operation, refused it definitely, or gave no answer, and each of
		// the three is a release fact of its own.
		return s.recordReleaseDispatch(ctx, *call, result, execErr)
	}
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

// agentActionClaim is what the claim transaction has read about one TOOL_CALL Action and
// holds under the Run aggregate lock: the Action, its Decision, the Agent Run and NodeRun
// as the lock protects them, and the registration the call resolves to. The approval
// decision rebuilds the same view for an Action it hands the execution right back to, so
// the pre-execution checks and the Attempt creation are written once and run from both.
//
// run and plan are resolved before the lock when the transaction may have to re-aggregate
// the Run -- a Tool whose declared risk can suspend the Action, or an Action resuming from
// an approval -- because compiling a Definition is work no Run row lock may be held across.
// commit and nextTurnID are what the transaction owes after COMMIT.
type agentActionClaim struct {
	lock        *store.RunLock
	nodeRun     domain.NodeRun
	agentRun    domain.AgentRun
	turn        domain.AgentTurn
	action      domain.AgentAction
	decision    domain.AgentDecision
	toolName    string
	reg         registry.ToolRegistration
	claimSource domain.ClaimSource
	run         domain.Run
	plan        *runtime.CompiledDefinition
	// occupation is the ACTIVE deploy-target occupation the Action already holds, set
	// only by a re-release; the first release Attempt acquires one.
	occupation *domain.ReleaseOccupation

	commit     agentCommit
	nextTurnID string
}

// toolActionInputs are the per-call inputs the pre-execution checks resolved for the Tool
// call: the frozen image and review Model IDs, the frozen workspace view and the committed
// facts that satisfied the Tool's requirements.
type toolActionInputs struct {
	imageModelID  string
	reviewModelID string
	workspace     *registry.WorkspaceView
	requiredFacts []domain.ExecutionFact
}

// toolActionVerdict is the result of the pre-execution checks: the inputs of a call that
// may run, or exactly one of a terminal failure and a refusal returned to the Agent.
type toolActionVerdict struct {
	inputs toolActionInputs
	fail   *toolActionFailure
	ret    *toolActionReturn
}

// toolActionFailure ends the Agent Run: the Action fails and the termination is recorded.
type toolActionFailure struct {
	termination domain.AgentTermination
	execError   domain.ExecutionError
}

// toolActionReturn hands a definite refusal back to the Agent in a new Turn.
type toolActionReturn struct {
	code    domain.AgentReturnCode
	message string
}

func (v toolActionVerdict) ok() bool { return v.fail == nil && v.ret == nil }

func failVerdict(termination domain.AgentTermination, execError domain.ExecutionError) toolActionVerdict {
	return toolActionVerdict{fail: &toolActionFailure{termination: termination, execError: execError}}
}

func returnVerdict(code domain.AgentReturnCode, message string) toolActionVerdict {
	return toolActionVerdict{ret: &toolActionReturn{code: code, message: message}}
}

// claimAgentAction is transaction 1. It returns nil without error when this caller may not
// execute the Action: another advancement path already won the claim, the Action is no
// longer READY, the Run has already reached a terminal status, the Action is a FINAL one,
// which belongs to the Final completion use case, or the Action was suspended for a human
// decision, which the approval use case continues.
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
		// The Decision is immutable once committed, so it is read before the lock; the
		// Tool it names decides whether this transaction may have to suspend the Action
		// and re-derive the Run status, which needs the Run and its compiled plan resolved
		// before the lock is taken.
		decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
		if err != nil {
			return err
		}
		toolName := ""
		if decision.ToolName != nil {
			toolName = *decision.ToolName
		}
		var run domain.Run
		var plan *runtime.CompiledDefinition
		if reg, ok := s.deps.Tools.Get(toolName); ok && reg.Metadata.EffectiveRisk() == domain.ToolRiskProduction {
			if run, plan, err = s.runPlan(ctx, tx, nodeRun.RunID); err != nil {
				return err
			}
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

		now := s.deps.Clock.Now()
		won, err := tx.AgentActions().ClaimReady(ctx, actionID, now)
		if err != nil {
			return err
		}
		if !won {
			return nil
		}
		action.Status = domain.AgentActionRunning

		c := &agentActionClaim{
			lock: lock, nodeRun: nodeRun, agentRun: agentRun, turn: turn, action: action, decision: decision,
			toolName: toolName, claimSource: claimSource, run: run, plan: plan,
		}
		verdict, err := s.checkToolActionLocked(ctx, tx, c)
		if err != nil {
			return err
		}
		if !verdict.ok() {
			if err := s.settleToolActionLocked(ctx, tx, c, verdict, now); err != nil {
				return err
			}
			commit, nextTurnID = c.commit, c.nextTurnID
			return nil
		}

		// The authorization decision is made once, here, from the Tool's declared risk and
		// the goal the Agent Run froze; every check that could make the call invalid has
		// already passed, so a person is only ever asked about a call that would run. An
		// approved Action re-enters execution through the approval use case and never
		// reaches this point again.
		authorization, reason := runtime.DecideAuthorization(c.reg.Metadata.Authorization, agentRun.Goal)
		switch authorization {
		case domain.AuthorizationDeny:
			if err := tx.AgentActions().SetAuthorization(ctx, action.ID, authorization, &reason); err != nil {
				return err
			}
			if err := s.settleToolActionLocked(ctx, tx, c, returnVerdict(domain.ReturnGoalDenied, reason), now); err != nil {
				return err
			}
			commit, nextTurnID = c.commit, c.nextTurnID
			return nil
		case domain.AuthorizationAsk:
			if err := s.enterApprovalWaitLocked(ctx, tx, c, now); err != nil {
				return err
			}
			commit, nextTurnID = c.commit, c.nextTurnID
			return nil
		case domain.AuthorizationAllow:
			if err := tx.AgentActions().SetAuthorization(ctx, action.ID, authorization, nil); err != nil {
				return err
			}
		default:
			return fmt.Errorf("execution: execute agent action %s: unknown authorization decision %q", action.ID, authorization)
		}

		call, err = s.startToolAttemptLocked(ctx, tx, c, verdict.inputs, now)
		if err != nil {
			return err
		}
		commit, nextTurnID = c.commit, c.nextTurnID
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

// checkToolActionLocked runs every pre-execution check of a RUNNING TOOL_CALL Action, in
// order: the frozen allowlist and the current registration, the committed Decision against
// the registered InputSchema, the frozen workspace with the written path's scope and the
// named patch, the fact requirements the Tool declares, the frozen generation limit, the
// frozen image and review models, and the decision basis and review fact a change request
// cites. It
// decides, it does not write: the caller settles a failing verdict so the approval path can
// resume the Agent's clock first. A passing verdict carries the
// inputs the Tool call needs. The registration it resolves is left on c for the caller.
func (s *ExecutionService) checkToolActionLocked(ctx context.Context, tx store.Tx, c *agentActionClaim) (toolActionVerdict, error) {
	reg, registered := s.deps.Tools.Get(c.toolName)
	if !registered {
		// The frozen allowlist is a Run-level authority decision, so it answers first:
		// a Tool the Agent Run never allowed is an invalid Action whatever the current
		// Registry holds.
		if slices.Contains(c.agentRun.AllowedTools, c.toolName) {
			// Registry drift: the allowlist resolved when the Agent Run was created
			// and no longer does. There is nothing to call and nothing to substitute
			// (CLAUDE.md "Extensions and external calls"), so the Tool call itself is
			// what failed.
			return failVerdict(domain.TerminationToolError, domain.ExecutionError{
				Code:    "TOOL_NOT_REGISTERED",
				Message: fmt.Sprintf("tool %q of the Agent Run's frozen allowlist is no longer registered", c.toolName),
			}), nil
		}
		return failVerdict(domain.TerminationInvalidAction, domain.ExecutionError{
			Code:    "INVALID_ACTION",
			Message: fmt.Sprintf("tool %q is not in the Agent Run's frozen allowlist", c.toolName),
		}), nil
	}
	c.reg = reg

	if validationErr := runtime.ValidateToolCall(c.decision, c.agentRun.AllowedTools, reg.Metadata); validationErr != nil {
		// A deterministic, committed fact is invalid: no Tool Attempt is created, the
		// Tool is not called and the model is not asked again.
		return failVerdict(domain.TerminationInvalidAction, domain.ExecutionError{Code: "INVALID_ACTION", Message: validationErr.Error()}), nil
	}

	var inputs toolActionInputs
	// A workspace Tool receives the Agent Run's frozen workspace: the binding, the newest
	// recorded patch and its sequence, read under this lock so the view names exactly
	// the version the call runs against. An Agent Run that froze none cannot run the
	// Tool at all; that is a Definition defect, so the Action fails before any Attempt
	// exists. A Tool that declares the path it writes has that path checked against
	// the frozen allowed paths here, and a path outside them is handed back to the
	// Agent as a SCOPE_DENIED refusal: no Attempt, no executor call, no patch.
	if reg.Metadata.UsesWorkspace {
		view, err := workspaceViewLocked(ctx, tx, c.agentRun.ID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return failVerdict(domain.TerminationToolError, domain.ExecutionError{
				Code:    codeWorkspaceNotConfigured,
				Message: fmt.Sprintf("tool %q needs a workspace, but the Agent node froze none", c.toolName),
			}), nil
		case err != nil:
			return toolActionVerdict{}, err
		}
		inputs.workspace = view
		if refusal := workspaceWriteScopeRefusal(reg.Metadata, c.decision.Arguments, view.AllowedPaths); refusal != nil {
			return returnVerdict(domain.ReturnScopeDenied, scopeDeniedMessage(refusal, view.AllowedPaths)), nil
		}
		// A Tool that acts on a named patch must name the newest one. The digest it names
		// is compared with the view read under this lock, and a stale one is handed back as
		// a STALE_PATCH refusal: no Attempt, no dispatch, so no result can ever bind to a
		// superseded patch. The workspace is checked before the Tool's fact requirements,
		// so a superseded patch is reported as stale even when facts about it exist: the
		// Agent has to move to the current patch either way.
		if named, stale := workspacePatchStale(reg.Metadata, c.decision.Arguments, view); stale {
			return returnVerdict(domain.ReturnStalePatch, stalePatchMessage(named, view)), nil
		}
		// A release names a change request of this Agent Run that describes the newest
		// patch, and goes to a deploy target nothing else occupies; both are decided here,
		// before an approval is asked for and again when it is granted.
		if reg.Metadata.OccupiesReleaseTarget {
			verdict, err := checkReleaseActionLocked(ctx, tx, c, view)
			if err != nil || !verdict.ok() {
				return verdict, err
			}
		}
	}

	// A valid call of a Tool that requires committed facts is still not executable
	// until those facts hold. An unmet requirement is decided before any Attempt
	// exists, so no external call can follow. A Tool registered to return an unmet
	// requirement hands the Agent a VERIFICATION_MISSING refusal naming the missing
	// fact type, and the Agent decides the next step in a new Turn; every other Tool
	// fails the Action exactly like an invalid Decision.
	requiredFacts, unmet, err := s.unmetFactRequirementLocked(ctx, tx, c.nodeRun.RunID, reg.Metadata.Requires, c.decision.Arguments)
	if err != nil {
		return toolActionVerdict{}, err
	}
	if unmet != nil && reg.Metadata.ReturnUnmetRequirement {
		return returnVerdict(domain.ReturnVerificationMissing, verificationMissingMessage(*unmet)), nil
	}
	if unmet != nil {
		return failVerdict(domain.TerminationInvalidAction, unmet.ExecutionError()), nil
	}

	// A counted Tool call past the Agent Run's frozen generation limit is rejected the
	// same way, still before any Attempt exists; the Run lock serializes concurrent
	// claims, so the limit can never be overrun.
	overLimit, err := s.generationLimitReachedLocked(ctx, tx, c.agentRun, reg.Metadata)
	if err != nil {
		return toolActionVerdict{}, err
	}
	if overLimit != nil {
		return failVerdict(domain.TerminationInvalidAction, *overLimit), nil
	}

	inputs.requiredFacts = requiredFacts

	// A Tool that runs with the node's image model receives the Agent Run's frozen image
	// Model ID, re-resolved here because configuration can change across a restart. An
	// ID that no longer resolves fails the Tool call itself, before any Attempt exists,
	// so nothing external is called and no other model is substituted.
	if reg.Metadata.UsesImageModel {
		inputs.imageModelID = frozenImageModel(c.agentRun)
		if unusable := unusableModel(s.deps.Models, "image model", inputs.imageModelID, domain.ModelCapabilityReferenceImageGeneration); unusable != nil {
			return failVerdict(domain.TerminationToolError, *unusable), nil
		}
	}

	// A Tool that reviews with the node's review model receives the Agent Run's frozen
	// review Model ID under the same rule, checked against the capability the Tool's
	// registration names. An Agent Run that froze none passes no ID to a Tool that allows
	// it, and that Tool applies its mock policy; a Tool that requires an independent review
	// model fails here instead. A frozen ID that no longer resolves fails here too and never
	// falls back to a mock policy.
	if reg.Metadata.UsesReviewModel && (c.agentRun.ReviewModelID != nil || reg.Metadata.RequiresReviewModel) {
		inputs.reviewModelID = frozenReviewModel(c.agentRun)
		if unusable := unusableReviewModel(s.deps.Models, inputs.reviewModelID, []string{reg.Metadata.ReviewModelCapability}); unusable != nil {
			return failVerdict(domain.TerminationToolError, *unusable), nil
		}
	}

	// A Tool that records a change request cites, in its decision basis, items of the
	// Turns' Context Manifests. Every cited item must exist in the Manifest of the Turn it
	// names, which this transaction reads under the lock; a reference that resolves to
	// nothing is a defect of the committed Decision, so the Action fails before any
	// Attempt exists and the model is not asked again.
	basis, basisErr := decisionBasisOfArguments(reg.Metadata, c.decision.Arguments)
	if basisErr == nil && len(basis) > 0 {
		basisErr = checkDecisionBasisLocked(ctx, tx, c.agentRun.ID, basis)
	}
	if basisErr != nil {
		var invalid *errDecisionBasisInvalid
		if errors.As(basisErr, &invalid) {
			return failVerdict(domain.TerminationInvalidAction, domain.ExecutionError{
				Code:    codeDecisionBasisInvalid,
				Message: fmt.Sprintf("tool %q cites a decision basis the runtime cannot resolve: %s", c.toolName, basisErr.Error()),
			}), nil
		}
		return toolActionVerdict{}, basisErr
	}

	// A change request may attach the review of its patch by citing the review fact. The
	// claim resolves the fact under the lock and checks it reviews the workspace's current
	// patch in this Run, which the stale-patch check above has just confirmed the call
	// names; the fact then travels with the required facts so the Tool can render it. A
	// citation that resolves to anything else is a defect of the committed Decision and
	// fails the Action the same way as an unresolvable decision basis.
	reviewFactID, reviewErr := reviewFactOfArguments(reg.Metadata, c.decision.Arguments)
	if reviewErr == nil && reviewFactID != "" {
		if inputs.workspace == nil {
			reviewErr = &errDecisionBasisInvalid{reason: "a review can only be attached to the patch of a workspace"}
		} else {
			var review domain.ExecutionFact
			if review, reviewErr = checkReviewFactLocked(ctx, tx, c.nodeRun.RunID, inputs.workspace.PatchDigest, reviewFactID); reviewErr == nil {
				inputs.requiredFacts = append(inputs.requiredFacts, review)
			}
		}
	}
	if reviewErr != nil {
		var invalid *errDecisionBasisInvalid
		if errors.As(reviewErr, &invalid) {
			return failVerdict(domain.TerminationInvalidAction, domain.ExecutionError{
				Code:    codeDecisionBasisInvalid,
				Message: fmt.Sprintf("tool %q cites a review fact the runtime cannot accept: %s", c.toolName, reviewErr.Error()),
			}), nil
		}
		return toolActionVerdict{}, reviewErr
	}
	return toolActionVerdict{inputs: inputs}, nil
}

// settleToolActionLocked writes a failing verdict for the RUNNING Action c holds: a
// terminal failure ends the Agent Run, a refusal is returned to the Agent in a new Turn. It
// records on c what the transaction owes after COMMIT.
func (s *ExecutionService) settleToolActionLocked(ctx context.Context, tx store.Tx, c *agentActionClaim, verdict toolActionVerdict, now time.Time) error {
	switch {
	case verdict.fail != nil:
		if err := s.failAgentActionLocked(ctx, tx, c.lock, c.nodeRun, c.agentRun, c.turn.ID, c.action.ID, nil, domain.AgentActionRunning,
			domain.FailureSyncExecution, verdict.fail.termination, verdict.fail.execError, now); err != nil {
			return err
		}
		c.commit = agentCommit{runID: c.nodeRun.RunID, lastSeq: c.lock.LastSeq(), terminal: true}
		return nil
	case verdict.ret != nil:
		turnID, err := s.returnActionToAgentLocked(ctx, tx, agentActionReturn{
			lock: c.lock, nodeRun: c.nodeRun, agentRun: c.agentRun, turn: c.turn, action: c.action, toolName: c.toolName,
		}, domain.AgentActionRunning, verdict.ret.code, verdict.ret.message, now)
		if err != nil {
			return err
		}
		c.nextTurnID = turnID
		c.commit = agentCommit{runID: c.nodeRun.RunID, lastSeq: c.lock.LastSeq(), terminal: turnID == ""}
		return nil
	default:
		return fmt.Errorf("execution: settle agent action %s: the verdict allows the call", c.action.ID)
	}
}

// startToolAttemptLocked creates the STARTED Tool Attempt of the RUNNING Action c holds,
// appends AGENT_ACTION_STARTED and builds the call the caller makes after COMMIT. It is the
// one place an Attempt is created for a TOOL_CALL Action: the claim path reaches it when the
// authorization allows the call, the approval path when a person approved it.
func (s *ExecutionService) startToolAttemptLocked(ctx context.Context, tx store.Tx, c *agentActionClaim, inputs toolActionInputs, now time.Time) (*agentToolCall, error) {
	if c.reg.Metadata.Reconcilable {
		// A release Attempt carries an operation key instead of a callback credential and
		// occupies its deploy target. A target another Action took since the checks ran is
		// a refusal returned to the Agent, settled here with nothing of the release written.
		call, occupied, err := s.startReleaseAttemptLocked(ctx, tx, c, inputs, now)
		if err != nil {
			return nil, err
		}
		if occupied {
			return nil, s.settleToolActionLocked(ctx, tx, c, returnVerdict(domain.ReturnTargetOccupied, targetOccupiedMessage(*inputs.workspace.DeployTarget)), now)
		}
		return call, nil
	}
	// Attempt 1 is the only Attempt a non-release TOOL_CALL Action ever gets; attempt_no
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
	//
	// The expiry is fixed from the deadline as it is now and is not moved when a pause
	// later shifts the deadline. That is safe because Actions run one at a time and the
	// Agent pauses only while an Action waits on a person or on a release, so no callback-
	// bearing Tool is ever in flight while the deadline moves; a release Tool is polled
	// and issues no credential.
	async := c.reg.Metadata.ExecutionKind == domain.ToolExecutionAsync
	var tokenHash *string
	var callbackCtx *registry.CallbackContext
	if async {
		var expiresAt time.Time
		if !c.agentRun.Deadline.IsZero() {
			expiresAt = c.agentRun.Deadline.Add(s.deps.Callback.PendingTTL)
		}
		token, err := issueCallbackToken(s.deps.Callback.SigningSecret, attemptID, expiresAt)
		if err != nil {
			return nil, fmt.Errorf("execution: issue callback credential for tool %q of action %s: %w", c.toolName, c.action.ID, err)
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
		ActionID:          c.action.ID,
		AttemptNo:         1,
		ToolName:          c.toolName,
		Status:            domain.ToolAttemptStarted,
		Input:             c.decision.Arguments,
		CallbackTokenHash: tokenHash,
		RunnerID:          &runnerID,
		StartedAt:         now,
	}); err != nil {
		return nil, err
	}
	if err := s.appendEvent(ctx, tx, c.lock, c.nodeRun.RunID, &c.nodeRun.ID, domain.EventAgentActionStarted, now, agentActionStartedPayload{
		AgentRunID:    c.agentRun.ID,
		TurnID:        c.turn.ID,
		ActionID:      c.action.ID,
		ToolAttemptID: &attemptID,
		ClaimSource:   c.claimSource,
	}); err != nil {
		return nil, err
	}
	// Starting the Attempt changes no Run-level status; UpdateAggregate persists the seq
	// watermark AGENT_ACTION_STARTED allocated under the lock.
	if err := tx.Runs().UpdateAggregate(ctx, c.lock, c.lock.Status(), now); err != nil {
		return nil, err
	}

	c.commit = agentCommit{runID: c.nodeRun.RunID, lastSeq: c.lock.LastSeq()}
	return &agentToolCall{
		runID:        c.nodeRun.RunID,
		nodeRunID:    c.nodeRun.ID,
		agentRunID:   c.agentRun.ID,
		turnID:       c.turn.ID,
		actionID:     c.action.ID,
		attemptID:    attemptID,
		toolName:     c.toolName,
		executor:     c.reg.Executor,
		outputSchema: c.reg.Metadata.OutputSchema,
		records:      c.reg.Metadata.Records,
		action: registry.ToolAction{
			RunID:         c.nodeRun.RunID,
			AgentRunID:    c.agentRun.ID,
			TurnID:        c.turn.ID,
			ActionID:      c.action.ID,
			ToolName:      c.toolName,
			AttemptNo:     1,
			Arguments:     c.decision.Arguments,
			ImageModelID:  inputs.imageModelID,
			ReviewModelID: inputs.reviewModelID,
			Workspace:     inputs.workspace,
			RequiredFacts: inputs.requiredFacts,
			Callback:      callbackCtx,
		},
		async:    async,
		deadline: c.agentRun.Deadline,
	}, nil
}
