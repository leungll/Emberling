package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// resumeToolAttempt is ResumeNode's branch for a TOOL_ATTEMPT Callback Binding: the
// callback, or the final Provider poll result, of an ASYNC Agent Tool. It keeps the three
// phases of the Node resume, and ends at its commit:
//
//  1. Routing facts are read from one snapshot without a lock. A Tool Attempt that is no
//     longer DISPATCHED -- already resolved by an earlier delivery, or failed by the Agent
//     timeout -- makes this delivery a Duplicate that writes nothing and consumes no Event
//     seq. The Binding is kept either way.
//  2. The registered Executor's OnCallback interprets the payload outside every
//     transaction and every lock. A poll result arrives already normalized by the
//     Executor's Poll and skips this step; a non-final one writes nothing.
//  3. One transaction under the Run aggregate lock commits the outcome through the same
//     completion or shared Tool failure body the synchronous call uses, with the Attempt
//     required to be DISPATCHED and the Action WAITING_CALLBACK. Losing either conditional
//     update is again a Duplicate, whichever of a callback and a poll got there first.
//
// The next READY Turn the commit created is only offered to the work queue: no
// model or Tool call ever runs on the goroutine or under the context of the callback
// request, which a Provider may drop at any moment. The queue is a latency optimisation
// only; the committed READY Turn is the recovery source, so a refused enqueue is left to
// the Reconciler and never reported to the Provider as a failure.
func (s *ExecutionService) resumeToolAttempt(ctx context.Context, req ResumeNode, binding domain.CallbackBinding) (ResumeOutcome, error) {
	var (
		attempt  domain.ToolAttempt
		run      domain.Run
		call     agentToolCall
		resolved bool
	)
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempt, err = tx.ToolAttempts().Get(ctx, binding.TargetID)
		if err != nil {
			return err
		}
		if attempt.Status != domain.ToolAttemptDispatched {
			return nil
		}
		action, err := tx.AgentActions().Get(ctx, attempt.ActionID)
		if err != nil {
			return err
		}
		if action.Status != domain.AgentActionWaitingCallback {
			return nil
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
		if run, err = tx.Runs().Get(ctx, nodeRun.RunID); err != nil {
			return err
		}
		call = agentToolCall{
			runID:      nodeRun.RunID,
			nodeRunID:  nodeRun.ID,
			agentRunID: agentRun.ID,
			turnID:     turn.ID,
			actionID:   action.ID,
			attemptID:  attempt.ID,
			toolName:   attempt.ToolName,
			deadline:   agentRun.Deadline,
		}
		resolved = true
		return nil
	})
	if err != nil {
		return ResumeOutcome{}, err
	}
	if !resolved {
		return ResumeOutcome{AttemptID: attempt.ID, Duplicate: true}, nil
	}

	executor, reg, err := s.asyncToolExecutor(attempt, run)
	if err != nil {
		return ResumeOutcome{}, err
	}
	call.outputSchema = reg.Metadata.OutputSchema
	outcome := ResumeOutcome{RunID: call.runID, NodeRunID: call.nodeRunID, AttemptID: attempt.ID}

	var output registry.ToolResult
	var cbErr error
	if req.Polled != nil {
		if !pollIsFinal(req.Polled.Result.Status) {
			// The Provider has not finished, or its answer could not be classified:
			// nothing is written, and a later callback, poll or the deadline decides.
			return outcome, nil
		}
		var failure *registry.ProviderFailure
		output, failure, err = polledToolOutcome(req.ExternalTaskID, req.Polled.Result)
		if err != nil {
			return ResumeOutcome{}, err
		}
		if failure != nil {
			cbErr = failure
		}
	} else {
		output, cbErr = executor.OnCallback(ctx, registry.ToolAsyncState{
			AgentRunID: call.agentRunID,
			ActionID:   call.actionID,
			ToolName:   call.toolName,
			AttemptNo:  attempt.AttemptNo,
			ExternalTask: registry.ExternalTask{
				ProviderID:     binding.ProviderID,
				ExternalTaskID: binding.ExternalTaskID,
			},
		}, req.Payload)
	}

	src := asyncToolOutcome(req.source(), s.pendingToConsume(req), req.hash(), attempt.CallbackTokenHash)

	execError, interpretErr := agentCallbackOutcome(output, cbErr, reg.Metadata)
	if interpretErr != nil {
		// A payload the Executor cannot interpret is not evidence that the
		// external task failed. Nothing is persisted and the Action keeps waiting.
		return outcome, &CallbackPayloadRejectedError{ExternalTaskID: req.ExternalTaskID, Err: interpretErr}
	}

	if execError != nil {
		committed, err := s.commitAgentToolFailure(ctx, call, src, *execError)
		if err != nil {
			return ResumeOutcome{}, err
		}
		outcome.Duplicate = !committed.committed
		outcome.Failed = committed.committed
		outcome.FailureSource = src.failureSource
		return outcome, nil
	}

	// The Execution Artifacts an asynchronous result declares, such as a verification log,
	// were written to content storage by the Executor before it returned; their metadata
	// commits with the result below, the same way a synchronous result's does.
	committed, err := s.commitAgentToolResult(ctx, call, src, registry.ToolResult{Output: output.Output, Artifacts: output.Artifacts, Cost: output.Cost})
	if err != nil {
		return ResumeOutcome{}, err
	}
	outcome.Duplicate = !committed.committed
	if committed.nextTurnID != "" {
		s.enqueueAgentTurn(call.runID, committed.nextTurnID)
	}
	return outcome, nil
}

// polledToolOutcome turns a final Tool poll result into what OnCallback would have
// returned: the Tool result for SUCCEEDED, or the Provider-reported failure for FAILED, so
// both deliveries are classified by the same rule. The failure is typed as the
// ProviderFailure OnCallback returns for a task the Provider reports failed, and only that:
// a poll never produces the "payload could not be interpreted" error a callback can, so
// the caller turns a non-nil failure into the callback error and leaves nil as nil. A
// SUCCEEDED result without ToolResult, or a FAILED one without Error, is an Executor
// contract violation and changes nothing.
func polledToolOutcome(externalTaskID string, result registry.PollResult) (registry.ToolResult, *registry.ProviderFailure, error) {
	switch result.Status {
	case registry.PollSucceeded:
		if result.ToolResult == nil {
			return registry.ToolResult{}, nil, fmt.Errorf("execution: poll result for external task %s reports SUCCEEDED without a tool result", externalTaskID)
		}
		return *result.ToolResult, nil, nil
	case registry.PollFailed:
		if result.Error == nil {
			return registry.ToolResult{}, nil, fmt.Errorf("execution: poll result for external task %s reports FAILED without an error", externalTaskID)
		}
		return registry.ToolResult{}, &registry.ProviderFailure{Err: *result.Error}, nil
	default:
		return registry.ToolResult{}, nil, fmt.Errorf("execution: poll result for external task %s has non-final status %q", externalTaskID, result.Status)
	}
}

// agentCallbackOutcome classifies what an async Tool's OnCallback returned. A
// ProviderFailure is the Provider reporting its task failed and becomes the Action's
// failure; a result that violates the registered OutputSchema, or carries a record the
// registration does not declare, fails the Action exactly as an invalid synchronous result
// does (TOOL_RESULT_INVALID). Any other error means the payload could not be interpreted
// and is returned as interpretErr, which must change no state.
func agentCallbackOutcome(result registry.ToolResult, cbErr error, metadata domain.ToolMetadata) (execError *domain.ExecutionError, interpretErr error) {
	if cbErr != nil {
		var providerFailure *registry.ProviderFailure
		if errors.As(cbErr, &providerFailure) {
			failure := providerFailure.Err
			return &failure, nil
		}
		return nil, cbErr
	}
	if err := runtime.ValidateToolResult(result.Output, metadata.OutputSchema); err != nil {
		return &domain.ExecutionError{Code: "TOOL_RESULT_INVALID", Message: err.Error()}, nil
	}
	if err := validateDeclaredArtifacts(result.Artifacts, metadata.Name); err != nil {
		return &domain.ExecutionError{Code: "TOOL_RESULT_INVALID", Message: err.Error()}, nil
	}
	if err := validateDeclaredRecords(result, metadata.Records, metadata.Name); err != nil {
		return &domain.ExecutionError{Code: "TOOL_RESULT_INVALID", Message: err.Error()}, nil
	}
	return nil, nil
}

// enqueueAgentTurn offers a committed READY Agent Turn to the in-process work queue.
// It must be called only after the transaction that created the Turn has
// committed. A refusal rolls nothing back: the READY Turn stays recoverable work that the
// Reconciler's scan rediscovers, so it is logged and never returned.
func (s *ExecutionService) enqueueAgentTurn(runID, turnID string) {
	if s.deps.Queue.EnqueueAgentTurn(runID, turnID) {
		return
	}
	s.deps.Logger.Info("agent turn left for the reconciler: work queue refused it",
		slog.String("run_id", runID),
		slog.String("agent_turn_id", turnID))
}

// asyncToolExecutor resolves the AsyncToolExecutor of a Tool Attempt from the Tool
// Registry. Whether a Tool is asynchronous is decided by its registered ExecutionKind,
// never by what its Executor happens to implement. A missing or no longer asynchronous
// registration fails explicitly with the RegistryResolutionError the async Node resume
// uses and changes no state -- the closest available Tool is never substituted (CLAUDE.md
// "Extensions and external calls").
func (s *ExecutionService) asyncToolExecutor(attempt domain.ToolAttempt, run domain.Run) (registry.AsyncToolExecutor, registry.ToolRegistration, error) {
	resolutionErr := func(message string) error {
		return &RegistryResolutionError{
			WorkflowID: run.WorkflowID,
			Version:    run.DefinitionVersion,
			Underlying: &runtime.CompileError{
				Stage:  runtime.StageSemantics,
				Errors: []runtime.ValidationError{{Code: runtime.CodeToolNotFound, Message: message}},
			},
		}
	}
	reg, ok := s.deps.Tools.Get(attempt.ToolName)
	if !ok {
		return nil, registry.ToolRegistration{}, resolutionErr(fmt.Sprintf("tool %q of tool attempt %s is no longer registered", attempt.ToolName, attempt.ID))
	}
	if reg.Metadata.ExecutionKind != domain.ToolExecutionAsync {
		return nil, registry.ToolRegistration{}, resolutionErr(fmt.Sprintf("tool %q of tool attempt %s is no longer registered as asynchronous", attempt.ToolName, attempt.ID))
	}
	executor, ok := reg.Executor.(registry.AsyncToolExecutor)
	if !ok {
		return nil, registry.ToolRegistration{}, resolutionErr(fmt.Sprintf("tool %q of tool attempt %s does not implement AsyncToolExecutor", attempt.ToolName, attempt.ID))
	}
	return executor, reg, nil
}
