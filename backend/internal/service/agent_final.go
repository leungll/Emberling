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

// This file owns the Final completion use case: the one
// transaction that closes a committed FINAL Agent Action out and, with it, the Agent Run
// and the Agent NodeRun. A FINAL Action calls nothing external, so unlike the Tool path
// there is no COMMIT to wait for between claiming the Action and finishing it -- and
// the acceptance criteria require exactly that: the database never holds a committed
// RUNNING Final Action.

// agentCompletedPayload is AGENT_COMPLETED (Event fields: turnId,
// termination, output summary). The output itself is not in it: an Agent's answer can be
// as large as the document it was derived from, and the authoritative copy is the Agent
// NodeRun's output and the Final Context Version (CLAUDE.md "Persistence and
// transactions").
type agentCompletedPayload struct {
	AgentRunID  string                  `json:"agentRunId"`
	TurnID      string                  `json:"turnId"`
	Termination domain.AgentTermination `json:"termination"`
	Output      dataSummary             `json:"output"`
}

// CompleteAgentFinal completes one committed FINAL Agent Action. It is the single Final
// completion path: immediate advancement after the Decision commits and the Reconciler's
// rediscovery of a READY Final Action both enter here, differing only in claimSource.
//
// Everything happens in one transaction:
//
//  1. Lock the Run and read the Action, its committed Decision, the Agent Run and the
//     current Context and State Versions.
//  2. Conditionally claim the Action READY->RUNNING. Losing the claim writes nothing.
//  3. Validate the Decision's output against the frozen Output Schema and apply the
//     optional state patch to the current State. A deterministic failure fails the Action
//     as INVALID_ACTION in this same transaction, with no Context or State Version, no
//     moved pointer and no partial Agent output.
//  4. On success: Action SUCCEEDED, Final Context Version, the new State Version only when
//     the patch really changed the State, FINAL_RESPONSE termination, the Agent NodeRun's
//     SUCCEEDED result, Run aggregation, downstream READY NodeRuns and every Event.
//
// No external call is made anywhere in it, so the Run lock never covers external code, and
// a transaction that cannot commit leaves the Action READY for the next caller to redo.
func (s *ExecutionService) CompleteAgentFinal(ctx context.Context, actionID string, claimSource domain.ClaimSource) error {
	if !claimSource.IsValid() {
		return fmt.Errorf("execution: complete agent final %s: unknown claim source %q", actionID, claimSource)
	}

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
		run, err := tx.Runs().Get(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		def, err := tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
		if err != nil {
			return err
		}
		// The Definition is compiled before the lock is taken out on purpose: it decides
		// nothing about this Agent, only which NodeRuns the Agent's success makes READY.
		plan, err := s.compile(ctx, def)
		if err != nil {
			return fmt.Errorf("execution: recompile definition %s v%d for agent final: %w",
				run.WorkflowID, run.DefinitionVersion, err)
		}

		lock, err := tx.Runs().LockForUpdate(ctx, run.ID)
		if err != nil {
			return err
		}
		if lock.Status().IsTerminal() {
			return nil
		}

		decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
		if err != nil {
			return err
		}
		// A TOOL_CALL Action belongs to the Tool execution use case. Routing it through
		// here would claim it without ever creating the Tool Attempt its result
		// transaction needs, so it is refused as a caller defect and nothing is written.
		if action.Type != domain.AgentActionFinal || decision.Kind != domain.DecisionFinal {
			return fmt.Errorf("execution: complete agent final %s: action is %s and its decision is %s, not FINAL",
				actionID, action.Type, decision.Kind)
		}

		now := s.deps.Clock.Now()
		won, err := tx.AgentActions().ClaimReady(ctx, actionID, now)
		if err != nil {
			return err
		}
		if !won {
			// Another advancement path already owns this Action's outcome.
			return nil
		}
		// A FINAL Action never has a Tool Attempt, so
		// AGENT_ACTION_STARTED names none.
		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventAgentActionStarted, now, agentActionStartedPayload{
			AgentRunID:  agentRun.ID,
			TurnID:      turn.ID,
			ActionID:    action.ID,
			ClaimSource: claimSource,
		}); err != nil {
			return err
		}

		previousState, err := tx.AgentStateVersions().GetByRunAndVersion(ctx, agentRun.ID, agentRun.CurrentStateVersion)
		if err != nil {
			return err
		}
		patchedState, stateChanged, validationErr := runtime.ApplyStatePatch(previousState.Value, decision.StatePatch)
		if validationErr == nil {
			validationErr = runtime.ValidateAgentState(patchedState, frozenValue(agentRun.StateSchema))
		}
		if outputErr := runtime.ValidateFinalOutput(decision.Output, frozenValue(agentRun.OutputSchema)); outputErr != nil {
			// The output is the Agent's answer, so an invalid output is reported even when
			// the patch is invalid too: it is the more specific reason this Action failed.
			validationErr = outputErr
		}
		if validationErr != nil {
			// A committed, deterministic fact is invalid. The same Final Action is never
			// retried and the model is never asked again.
			if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID, nil, domain.AgentActionRunning,
				domain.FailureSyncExecution, domain.TerminationInvalidAction,
				domain.ExecutionError{Code: "INVALID_ACTION", Message: validationErr.Error()}, now); err != nil {
				return err
			}
			commit = agentCommit{runID: run.ID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		if err := tx.AgentActions().MarkSucceeded(ctx, action.ID, domain.AgentActionRunning, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventAgentActionCompleted, now, agentActionCompletedPayload{
			AgentRunID:       agentRun.ID,
			TurnID:           turn.ID,
			ActionID:         action.ID,
			CompletionSource: domain.CompletionSyncExecution,
		}); err != nil {
			return err
		}

		if err := s.appendFinalContext(ctx, tx, agentRun, turn.ID, decision.Output, now); err != nil {
			return err
		}
		nextContextVersion := agentRun.CurrentContextVersion + 1

		nextStateVersion := agentRun.CurrentStateVersion
		if stateChanged {
			nextStateVersion = agentRun.CurrentStateVersion + 1
			turnID := turn.ID
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
			if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventAgentStateUpdated, now, agentStateUpdatedPayload{
				AgentRunID:           agentRun.ID,
				TurnID:               turn.ID,
				ContextVersion:       nextContextVersion,
				PreviousStateVersion: agentRun.CurrentStateVersion,
				StateVersion:         nextStateVersion,
			}); err != nil {
				return err
			}
		}

		// The Turn number does not move: a Final Decision ends the Agent Run instead of
		// opening another round, and this transaction creates no Turn.
		advanced, err := tx.AgentRuns().AdvancePointers(ctx, agentRun.ID,
			store.AgentRunPointers{
				CurrentTurnNo:         agentRun.CurrentTurnNo,
				CurrentContextVersion: agentRun.CurrentContextVersion,
				CurrentStateVersion:   agentRun.CurrentStateVersion,
			},
			store.AgentRunPointers{
				CurrentTurnNo:         agentRun.CurrentTurnNo,
				CurrentContextVersion: nextContextVersion,
				CurrentStateVersion:   nextStateVersion,
			})
		if err != nil {
			return err
		}
		if !advanced {
			return errAgentTurnSuperseded
		}

		terminated, err := tx.AgentRuns().Terminate(ctx, agentRun.ID, domain.TerminationFinalResponse, now, nil)
		if err != nil {
			return err
		}
		if !terminated {
			return errAgentTurnSuperseded
		}
		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventAgentCompleted, now, agentCompletedPayload{
			AgentRunID:  agentRun.ID,
			TurnID:      turn.ID,
			Termination: domain.TerminationFinalResponse,
			Output:      summarize(decision.Output),
		}); err != nil {
			return err
		}

		if err := s.succeedAgentNodeRun(ctx, tx, lock, run, nodeRun, agentRun.ID, def, plan, decision.Output, now); err != nil {
			return err
		}

		commit = agentCommit{runID: run.ID, lastSeq: lock.LastSeq(), terminal: true}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil
	}
	if err != nil {
		return err
	}
	s.wake(commit)
	return nil
}

// appendFinalContext creates the Final Context Version: every
// message of the previous version, unchanged and in order, followed by one `assistant`
// message carrying the canonical Final output. History is never reordered or rewritten.
func (s *ExecutionService) appendFinalContext(
	ctx context.Context,
	tx store.Tx,
	agentRun domain.AgentRun,
	turnID string,
	output json.RawMessage,
	now time.Time,
) error {
	previous, err := tx.AgentContextVersions().GetByRunAndVersion(ctx, agentRun.ID, agentRun.CurrentContextVersion)
	if err != nil {
		return err
	}
	stored, err := runtime.DecodeAgentContext(previous.Messages)
	if err != nil {
		return fmt.Errorf("execution: agent run %s context version %d: %w", agentRun.ID, previous.Version, err)
	}

	messages := make([]runtime.AgentContextMessage, 0, len(stored)+1)
	messages = append(messages, stored...)
	// The message content is the validated Final output itself, not a wrapper: it is the
	// Agent's answer, and a Context Version must hold the complete logical value a
	// restarted process would replay.
	messages = append(messages, runtime.AgentContextMessage{
		Role:    runtime.AgentRoleAssistant,
		Content: output,
	})

	messageBytes, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("execution: encode final context version for agent run %s: %w", agentRun.ID, err)
	}
	sourceTurnID := turnID
	return tx.AgentContextVersions().Create(ctx, domain.AgentContextVersion{
		ID:           s.deps.IDs.NewID(domain.IDPrefixAgentContextVersion),
		AgentRunID:   agentRun.ID,
		Version:      agentRun.CurrentContextVersion + 1,
		SourceTurnID: &sourceTurnID,
		Messages:     messageBytes,
		CreatedAt:    now,
	})
}

// succeedAgentNodeRun succeeds one Agent NodeRun inside the transaction that terminated
// its Agent Run as FINAL_RESPONSE, and is the mirror image of failAgentNodeRun: it is
// deliberately not completeNode, which is keyed on a Node Attempt (to find the NodeRun and
// to stamp nodeCompletedPayload) and opens its own transaction -- an Agent NodeRun has no
// Attempt, and the Agent NodeRun's result must commit with the Action's. The downstream
// scheduling and Run aggregation that follow are the shared ones, not a second copy.
//
// The NodeRun's TokenUsage is the sum of its Agent Run's Turn usage, written with the
// SUCCEEDED transition; each Turn keeps its own usage as the per-call record.
func (s *ExecutionService) succeedAgentNodeRun(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	run domain.Run,
	nodeRun domain.NodeRun,
	agentRunID string,
	def domain.Definition,
	plan *runtime.CompiledDefinition,
	output json.RawMessage,
	now time.Time,
) error {
	outputBytes, err := s.agentNodeOutput(nodeRun.NodeType, output)
	if err != nil {
		return err
	}

	usage, err := agentRunTokenUsage(ctx, tx, agentRunID)
	if err != nil {
		return err
	}

	var latencyMs int64
	if nodeRun.StartedAt != nil {
		latencyMs = now.Sub(*nodeRun.StartedAt).Milliseconds()
	}
	if err := tx.NodeRuns().MarkSucceeded(ctx, nodeRun.ID, domain.NodeRunRunning, now, store.NodeRunOutcome{
		Output:     outputBytes,
		TokenUsage: usage,
		LatencyMs:  &latencyMs,
	}); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeCompleted, now, nodeCompletedPayload{
		Output:           summarize(outputBytes),
		LatencyMs:        latencyMs,
		TokenUsage:       usage,
		CompletionSource: domain.CompletionSyncExecution,
	}); err != nil {
		return err
	}
	return s.advanceAfterNodeSuccess(ctx, tx, lock, run, nodeRun, def, plan, outputBytes, now)
}

// agentNodeOutput maps one validated Final output onto the Agent Node Type's registered
// output port. Which port that is comes from the registered Metadata, not a constant this
// package restates (CLAUDE.md "Extensions and external calls").
//
// The port's registered DataType is `text`, and no design document says how a structured
// output (one validated against a frozen Output Schema that is not a string) reaches it.
// The decision here is to publish it as its canonical JSON text, so the port always holds
// the value its registered DataType promises and the complete answer still reaches the
// downstream node. The Agent's own structured value stays authoritative in the Decision
// and in the Final Context Version.
func (s *ExecutionService) agentNodeOutput(nodeType string, output json.RawMessage) (json.RawMessage, error) {
	reg, ok := s.deps.Nodes.Get(nodeType)
	if !ok {
		return nil, fmt.Errorf("execution: node type %q is not registered", nodeType)
	}
	if len(reg.Metadata.Outputs) != 1 {
		return nil, fmt.Errorf("execution: node type %q declares %d output ports, want exactly one",
			nodeType, len(reg.Metadata.Outputs))
	}

	value := output
	var text string
	if err := json.Unmarshal(output, &text); err != nil {
		encoded, err := json.Marshal(string(output))
		if err != nil {
			return nil, fmt.Errorf("execution: encode agent output for node type %q: %w", nodeType, err)
		}
		value = encoded
	}

	outputBytes, err := json.Marshal(map[string]json.RawMessage{reg.Metadata.Outputs[0].Name: value})
	if err != nil {
		return nil, fmt.Errorf("execution: encode agent node output for node type %q: %w", nodeType, err)
	}
	return outputBytes, nil
}
