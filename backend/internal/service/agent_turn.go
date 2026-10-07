package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// agentDecisionCommittedPayload is AGENT_DECISION_COMMITTED (Event fields: turnId,
// actionId, kind, Tool name or Final summary). Exactly one of ToolName and
// FinalOutput is present, following the Decision's kind.
type agentDecisionCommittedPayload struct {
	AgentRunID string              `json:"agentRunId"`
	TurnID     string              `json:"turnId"`
	ActionID   string              `json:"actionId"`
	Kind       domain.DecisionKind `json:"kind"`
	ToolName   *string             `json:"toolName,omitempty"`
	// FinalOutput is a bounded summary of the Final output, never the output itself: an
	// Agent's answer can be as large as the document it was derived from, and Event
	// payloads stay bounded (CLAUDE.md "Persistence and transactions").
	FinalOutput *dataSummary `json:"finalOutput,omitempty"`
}

// agentFailedPayload is AGENT_FAILED (Event fields: turnId, termination,
// error).
type agentFailedPayload struct {
	AgentRunID  string                  `json:"agentRunId"`
	TurnID      string                  `json:"turnId"`
	Termination domain.AgentTermination `json:"termination"`
	Error       domain.ExecutionError   `json:"error"`
}

// agentTurnResponse is the bounded record of what the Provider answered, written on the
// Turn together with its COMPLETED status (AgentTurn.Response).
// It is the normalised response summary and nothing else: no Provider-private response
// structure, no credential or header, and not a second copy of the Decision, which is its
// own immutable row.
type agentTurnResponse struct {
	Kind              domain.DecisionKind `json:"kind"`
	ProviderRequestID *string             `json:"providerRequestId,omitempty"`
	FinishReason      *string             `json:"finishReason,omitempty"`
	ResponseSHA256    string              `json:"responseSha256"`
}

// agentModelCall is what the claim transaction hands to the model call it authorised. It
// exists so that everything the Provider needs is read inside the transaction that took
// the claim, and the call itself happens strictly after COMMIT.
type agentModelCall struct {
	runID      string
	nodeRunID  string
	agentRunID string
	turnID     string
	provider   registry.ModelProvider
	request    registry.ModelRequest
	// deadline is the Agent Run's single frozen deadline, which covers the model call as
	// well as every Tool call.
	deadline time.Time
}

// errAgentTurnSuperseded rolls a model-result transaction back when the Turn is no longer
// the RUNNING one this caller claimed. Nothing is written and the caller stops; it is not
// an error to report.
var errAgentTurnSuperseded = errors.New("execution: agent turn superseded")

// AdvanceAgentTurn drives one Agent Turn from READY to a committed Decision. It is the
// single Turn advancement path: immediate advancement after a claim and (from the
// Reconciler's rediscovery of READY Turns) recovery both enter here, differing only in
// claimSource.
//
// It runs two transactions with one model call between them:
//
//  1. Lock the Run, conditionally claim the Turn READY->RUNNING, write AGENT_TURN_STARTED
//     and read the frozen inputs the model call needs. Losing the claim writes nothing.
//  2. (after COMMIT, holding no lock) call the Model Provider.
//  3. Lock the Run again and commit the result: either the Turn's COMPLETED status with
//     its immutable Decision and single READY Action, or the Turn's FAILED status with
//     the Agent Run's termination and the Agent NodeRun's failure.
//
// A crash between 1 and 3 leaves a RUNNING Turn with no Decision. That is deliberate:
// the model request may already have been billed, so
// recovery rediscovers READY Turns only and never re-issues this one.
//
// Executing the committed Action is a separate, separately claimed step; this use case
// never calls a Tool.
func (s *ExecutionService) AdvanceAgentTurn(ctx context.Context, turnID string, claimSource domain.ClaimSource) error {
	call, err := s.claimAgentTurn(ctx, turnID, claimSource)
	if err != nil || call == nil {
		return err
	}

	// The Agent Run's deadline bounds the model call, so a Provider cannot hold this
	// goroutine past the point at which the Agent Run may no longer continue
	// (the deadline covers model calls, Tool calls and
	// waiting for a callback alike).
	callCtx, cancel := context.WithDeadline(ctx, call.deadline)
	defer cancel()

	response, genErr := call.provider.Generate(callCtx, call.request)
	if genErr != nil {
		if agentDeadlineExceeded(callCtx, genErr) {
			// The deadline, not the Provider, ended this call: the termination is TIMEOUT,
			// and MODEL_ERROR would misreport it.
			return s.TimeoutAgentRun(ctx, call.agentRunID)
		}
		// A Provider error is the Agent's MODEL_ERROR termination: the Adapter has already
		// normalised it, and Emberling does not silently re-ask the model.
		// The Provider's own message is kept; it carries
		// no credential, because an Adapter must not put one there.
		return s.failAgentTurn(ctx, *call, domain.TerminationModelError, domain.ExecutionError{
			Code: "MODEL_ERROR", Message: genErr.Error(),
		})
	}

	decision, parseErr := runtime.ParseModelDecision(runtime.DecisionEnvelope{
		Kind:       domain.DecisionKind(response.Decision.Kind),
		ToolName:   response.Decision.ToolName,
		Arguments:  response.Decision.Arguments,
		Output:     response.Decision.Output,
		StatePatch: response.Decision.StatePatch,
	})
	if parseErr != nil {
		// Only the basic envelope shape is checked here. Arguments, Final output and the
		// patched State are validated against the Agent Run's frozen Schemas when the
		// committed Action executes.
		return s.failAgentTurn(ctx, *call, domain.TerminationInvalidAction, domain.ExecutionError{
			Code: "INVALID_ACTION", Message: parseErr.Error(),
		})
	}

	return s.commitAgentDecision(ctx, *call, decision, response)
}

// claimAgentTurn is transaction 1. It returns nil without error when this caller did not
// win the claim: another advancement path already holds the model call right, or the Turn
// is no longer READY at all.
func (s *ExecutionService) claimAgentTurn(ctx context.Context, turnID string, claimSource domain.ClaimSource) (*agentModelCall, error) {
	if !claimSource.IsValid() {
		return nil, fmt.Errorf("execution: advance agent turn %s: unknown claim source %q", turnID, claimSource)
	}

	var call *agentModelCall
	var notifyRunID string
	var lastSeq int64

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		turn, err := tx.AgentTurns().Get(ctx, turnID)
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

		now := s.deps.Clock.Now()
		won, err := tx.AgentTurns().ClaimReady(ctx, turnID, now)
		if err != nil {
			return err
		}
		if !won {
			return nil
		}

		if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentTurnStarted, now, agentTurnPayload{
			AgentRunID:     agentRun.ID,
			TurnID:         turn.ID,
			TurnNo:         turn.TurnNo,
			ContextVersion: agentRun.CurrentContextVersion,
			StateVersion:   agentRun.CurrentStateVersion,
			ClaimSource:    &claimSource,
		}); err != nil {
			return err
		}
		// The claim changes no Run-level status; UpdateAggregate persists the seq
		// watermark AGENT_TURN_STARTED allocated under the lock.
		if err := tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now); err != nil {
			return err
		}
		notifyRunID = nodeRun.RunID
		lastSeq = lock.LastSeq()

		// The frozen inputs are read here, inside the transaction that already holds the
		// Run lock, rather than in a second read transaction after COMMIT: the Agent Run's
		// pointers and the versions they name are exactly the facts this claim is based
		// on, and re-reading them afterwards would open a window in which they no longer
		// match the Turn that was claimed. Only the Provider call itself is deferred past
		// COMMIT.
		request, buildErr := s.buildModelRequest(ctx, tx, agentRun)
		if buildErr != nil {
			var missing *agentRegistryDriftError
			if errors.As(buildErr, &missing) {
				// Registry drift discovered after the claim: the Agent Run was created
				// when the Model and every Tool still resolved. There is nothing to call,
				// so the claimed Turn fails here, in this same transaction.
				return s.terminateAgentLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID,
					domain.TerminationModelError,
					domain.ExecutionError{Code: "MODEL_ERROR", Message: missing.Error()}, now)
			}
			return buildErr
		}

		provider, ok := s.modelProvider(agentRun.ModelID)
		if !ok {
			return s.terminateAgentLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID,
				domain.TerminationModelError,
				domain.ExecutionError{Code: "MODEL_ERROR", Message: fmt.Sprintf("model %q is not registered", agentRun.ModelID)}, now)
		}

		call = &agentModelCall{
			runID:      nodeRun.RunID,
			nodeRunID:  nodeRun.ID,
			agentRunID: agentRun.ID,
			turnID:     turn.ID,
			provider:   provider,
			request:    request,
			deadline:   agentRun.Deadline,
		}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if notifyRunID != "" {
		// Notify-only: no Node-level work became available here, and the Turn's own
		// continuation is the call this function's caller is about to make.
		s.notifyCommitted(notifyRunID, lastSeq)
	}
	if call == nil {
		return nil, nil
	}
	// A claim that immediately terminated the Agent Run wrote its facts above and must not
	// reach the Provider.
	return call, nil
}

// agentRegistryDriftError reports that a Model ID or a stable Tool Name frozen into an
// Agent Run no longer resolves. It is separate from a plain error so the claim transaction
// can turn it into a MODEL_ERROR termination instead of failing the whole call.
type agentRegistryDriftError struct{ message string }

func (e *agentRegistryDriftError) Error() string { return e.message }

// buildModelRequest assembles the Turn's model request from the Agent Run's frozen
// configuration and the Context and State Versions its pointers name. Every value comes
// from committed facts: an Adapter may not re-read a newer Definition, and this function
// may not substitute a "closest" Tool for a missing one.
func (s *ExecutionService) buildModelRequest(ctx context.Context, tx store.Tx, agentRun domain.AgentRun) (registry.ModelRequest, error) {
	contextVersion, err := tx.AgentContextVersions().GetByRunAndVersion(ctx, agentRun.ID, agentRun.CurrentContextVersion)
	if err != nil {
		return registry.ModelRequest{}, err
	}
	stateVersion, err := tx.AgentStateVersions().GetByRunAndVersion(ctx, agentRun.ID, agentRun.CurrentStateVersion)
	if err != nil {
		return registry.ModelRequest{}, err
	}

	stored, err := runtime.DecodeAgentContext(contextVersion.Messages)
	if err != nil {
		return registry.ModelRequest{}, fmt.Errorf("execution: agent run %s context version %d: %w",
			agentRun.ID, contextVersion.Version, err)
	}
	messages := make([]registry.ModelMessage, 0, len(stored))
	for _, message := range stored {
		messages = append(messages, registry.ModelMessage{
			Role:         message.Role,
			Content:      message.Content,
			ToolName:     message.ToolName,
			ToolActionID: message.ToolActionID,
		})
	}

	// Tools are built in the frozen allowlist order, from registered Metadata only.
	tools := make([]registry.ModelToolSpec, 0, len(agentRun.AllowedTools))
	for _, name := range agentRun.AllowedTools {
		reg, ok := s.deps.Tools.Get(name)
		if !ok {
			return registry.ModelRequest{}, &agentRegistryDriftError{
				message: fmt.Sprintf("tool %q of the Agent Run's frozen allowlist is no longer registered", name),
			}
		}
		tools = append(tools, registry.ModelToolSpec{
			Name:         reg.Metadata.Name,
			Description:  reg.Metadata.Description,
			InputSchema:  reg.Metadata.InputSchema,
			OutputSchema: reg.Metadata.OutputSchema,
		})
	}

	return registry.ModelRequest{
		ModelID:           agentRun.ModelID,
		Instructions:      agentRun.Instructions,
		Messages:          messages,
		State:             stateVersion.Value,
		Tools:             tools,
		ModelConfig:       frozenValue(agentRun.ModelConfig),
		FinalOutputSchema: frozenValue(agentRun.OutputSchema),
		DecisionSchema:    registry.DecisionSchema(),
	}, nil
}

// modelProvider resolves the Provider that serves one frozen Model ID.
func (s *ExecutionService) modelProvider(modelID string) (registry.ModelProvider, bool) {
	_, provider, ok := s.deps.Models.Get(modelID)
	return provider, ok
}

// commitAgentDecision is the success half of transaction 3: the Turn completes with its
// bounded response, and the immutable Decision and its single READY Action commit in the
// same transaction, together with AGENT_DECISION_COMMITTED. The Action is not executed
// here -- whoever claims it next executes the Decision that was already made.
func (s *ExecutionService) commitAgentDecision(ctx context.Context, call agentModelCall, decision domain.AgentDecision, response registry.ModelResponse) error {
	var notifyRunID string
	var lastSeq int64
	// actionID names the committed Action this process may advance next, and actionType
	// which use case owns it: a TOOL_CALL is executed, a FINAL is completed.
	var actionID string
	var actionType domain.AgentActionType

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		lock, err := tx.Runs().LockForUpdate(ctx, call.runID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()

		responseBytes, err := json.Marshal(agentTurnResponse{
			Kind:              decision.Kind,
			ProviderRequestID: response.ResponseSummary.ProviderRequestID,
			FinishReason:      response.ResponseSummary.FinishReason,
			ResponseSHA256:    response.ResponseSummary.ResponseSHA256,
		})
		if err != nil {
			return fmt.Errorf("execution: encode agent turn response: %w", err)
		}

		// MarkCompleted is conditional on the Turn still being RUNNING: it is what decides
		// that this caller, and not a later duplicate delivery, owns the result.
		if err := tx.AgentTurns().MarkCompleted(ctx, call.turnID, now, responseBytes, response.TokenUsage); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}

		decision.ID = s.deps.IDs.NewID(domain.IDPrefixAgentDecision)
		decision.TurnID = call.turnID
		decision.ResponseSummary = responseBytes
		decision.CreatedAt = now
		if err := tx.AgentDecisions().Create(ctx, decision); err != nil {
			return err
		}

		actionType = domain.AgentActionFinal
		if decision.Kind == domain.DecisionToolCall {
			actionType = domain.AgentActionToolCall
		}
		action := domain.AgentAction{
			ID:         s.deps.IDs.NewID(domain.IDPrefixAgentAction),
			TurnID:     call.turnID,
			DecisionID: decision.ID,
			Type:       actionType,
			Status:     domain.AgentActionReady,
			CreatedAt:  now,
		}
		if err := tx.AgentActions().Create(ctx, action); err != nil {
			return err
		}

		payload := agentDecisionCommittedPayload{
			AgentRunID: call.agentRunID,
			TurnID:     call.turnID,
			ActionID:   action.ID,
			Kind:       decision.Kind,
			ToolName:   decision.ToolName,
		}
		if decision.Kind == domain.DecisionFinal {
			summary := summarize(decision.Output)
			payload.FinalOutput = &summary
		}
		if err := s.appendEvent(ctx, tx, lock, call.runID, &call.nodeRunID, domain.EventAgentDecisionCommitted, now, payload); err != nil {
			return err
		}
		if err := tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now); err != nil {
			return err
		}
		notifyRunID = call.runID
		lastSeq = lock.LastSeq()
		actionID = action.ID
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil
	}
	if err != nil {
		return err
	}
	if notifyRunID != "" {
		s.notifyCommitted(notifyRunID, lastSeq)
	}
	if actionID == "" {
		return nil
	}
	// The Action is committed READY work before this call is made, so a process that dies
	// here loses only latency: the Reconciler rediscovers the same Action and advances the
	// same Decision. claimSource is IMMEDIATE because this is the path that
	// just committed it.
	if actionType == domain.AgentActionFinal {
		return s.CompleteAgentFinal(ctx, actionID, domain.ClaimImmediate)
	}
	return s.ExecuteAgentAction(ctx, actionID, domain.ClaimImmediate)
}

// failAgentTurn is the failure half of transaction 3, shared by a Provider error
// (MODEL_ERROR) and an unusable decision envelope (INVALID_ACTION). Both commit the same
// facts: the Turn fails, the Agent Run records why it stopped, the Agent NodeRun fails and
// the Run re-aggregates -- with no Decision and no Action, because neither was ever a
// committed fact.
func (s *ExecutionService) failAgentTurn(ctx context.Context, call agentModelCall, termination domain.AgentTermination, execError domain.ExecutionError) error {
	var notifyRunID string
	var lastSeq int64

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

		if err := s.terminateAgentLocked(ctx, tx, lock, nodeRun, agentRun, call.turnID, termination, execError, now); err != nil {
			return err
		}
		notifyRunID = call.runID
		lastSeq = lock.LastSeq()
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil
	}
	if err != nil {
		return err
	}
	if notifyRunID != "" {
		s.postCommit(notifyRunID, lastSeq)
	}
	return nil
}

// terminateAgentLocked writes one Agent Run's terminal facts inside a transaction whose
// Run aggregate lock the caller already holds: the RUNNING Turn fails, the Agent Run
// records its termination, AGENT_FAILED is appended, and the shared Agent NodeRun failure
// tail fails the NodeRun and re-aggregates the Run (the Tool failure transaction,
// reused for a model failure).
//
// Both conditional updates can lose: a Turn that is no longer RUNNING, or an Agent Run
// something else already terminated, mean another path owns this outcome. Either rolls the
// whole transaction back through errAgentTurnSuperseded, so a loser writes nothing at all.
func (s *ExecutionService) terminateAgentLocked(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	nodeRun domain.NodeRun,
	agentRun domain.AgentRun,
	turnID string,
	termination domain.AgentTermination,
	execError domain.ExecutionError,
	now time.Time,
) error {
	if err := tx.AgentTurns().MarkFailed(ctx, turnID, now, execError); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return errAgentTurnSuperseded
		}
		return err
	}
	return s.terminateAgentRunLocked(ctx, tx, lock, nodeRun, agentRun, turnID, termination, execError, now)
}

// terminateAgentRunLocked is terminateAgentLocked without the Turn's own failure. It is
// what the Agent Action use cases need: an Action executes a Decision its Turn already
// committed, so that Turn is COMPLETED and must stay COMPLETED -- the model answered, and
// it is the Action, the round bound or the deadline that stopped the Agent Run.
func (s *ExecutionService) terminateAgentRunLocked(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	nodeRun domain.NodeRun,
	agentRun domain.AgentRun,
	turnID string,
	termination domain.AgentTermination,
	execError domain.ExecutionError,
	now time.Time,
) error {
	terminated, err := tx.AgentRuns().Terminate(ctx, agentRun.ID, termination, now, &execError)
	if err != nil {
		return err
	}
	if !terminated {
		return errAgentTurnSuperseded
	}

	if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentFailed, now, agentFailedPayload{
		AgentRunID:  agentRun.ID,
		TurnID:      turnID,
		Termination: termination,
		Error:       execError,
	}); err != nil {
		return err
	}

	run, err := tx.Runs().Get(ctx, nodeRun.RunID)
	if err != nil {
		return err
	}
	agentRunID := agentRun.ID
	return s.failAgentNodeRun(ctx, tx, lock, run, nodeRun, &agentRunID, execError, now)
}
