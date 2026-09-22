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
// callback of an ASYNC Agent Tool (06 §1.6, 06 §1.7). It keeps the three phases of the
// Node resume, and ends at its commit:
//
//  1. Routing facts are read from one snapshot without a lock. A Tool Attempt that is no
//     longer DISPATCHED -- already resolved by an earlier delivery, or failed by the Agent
//     timeout -- makes this delivery a Duplicate that writes nothing and consumes no Event
//     seq (09 §3.2). The Binding is kept either way.
//  2. The registered Executor's OnCallback interprets the payload outside every
//     transaction and every lock (invariant #4).
//  3. One transaction under the Run aggregate lock commits the outcome through the same
//     completion or shared Tool failure body the synchronous call uses, with the Attempt
//     required to be DISPATCHED and the Action WAITING_CALLBACK. Losing either conditional
//     update is again a Duplicate.
//
// The next READY Turn the commit created is only offered to the work queue (06 §2.1): no
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

	output, cbErr := executor.OnCallback(ctx, registry.ToolAsyncState{
		AgentRunID: call.agentRunID,
		ActionID:   call.actionID,
		ToolName:   call.toolName,
		AttemptNo:  attempt.AttemptNo,
		ExternalTask: registry.ExternalTask{
			ProviderID:     binding.ProviderID,
			ExternalTaskID: binding.ExternalTaskID,
		},
	}, req.Payload)

	src := asyncToolOutcome(req.Source, s.pendingToConsume(req), req.hash(), attempt.CallbackTokenHash)
	outcome := ResumeOutcome{RunID: call.runID, NodeRunID: call.nodeRunID, AttemptID: attempt.ID}

	execError, interpretErr := agentCallbackOutcome(output, cbErr, reg.Metadata.OutputSchema)
	if interpretErr != nil {
		// 06 §1.6: a payload the Executor cannot interpret is not evidence that the
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

	committed, err := s.commitAgentToolResult(ctx, call, src, output.Output)
	if err != nil {
		return ResumeOutcome{}, err
	}
	outcome.Duplicate = !committed.committed
	if committed.nextTurnID != "" {
		s.enqueueAgentTurn(call.runID, committed.nextTurnID)
	}
	return outcome, nil
}

// agentCallbackOutcome classifies what an async Tool's OnCallback returned. A
// ProviderFailure is the Provider reporting its task failed and becomes the Action's
// failure; a result that violates the registered OutputSchema fails the Action exactly as
// an invalid synchronous result does (TOOL_RESULT_INVALID). Any other error means the
// payload could not be interpreted and is returned as interpretErr, which must change no
// state (06 §1.6).
func agentCallbackOutcome(result registry.ToolResult, cbErr error, outputSchema []byte) (execError *domain.ExecutionError, interpretErr error) {
	if cbErr != nil {
		var providerFailure *registry.ProviderFailure
		if errors.As(cbErr, &providerFailure) {
			failure := providerFailure.Err
			return &failure, nil
		}
		return nil, cbErr
	}
	if err := runtime.ValidateToolResult(result.Output, outputSchema); err != nil {
		return &domain.ExecutionError{Code: "TOOL_RESULT_INVALID", Message: err.Error()}, nil
	}
	return nil, nil
}

// enqueueAgentTurn offers a committed READY Agent Turn to the in-process work queue
// (06 §2.1). It must be called only after the transaction that created the Turn has
// committed. A refusal rolls nothing back: the READY Turn stays recoverable work that the
// Reconciler's scan rediscovers (invariant #6), so it is logged and never returned.
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
