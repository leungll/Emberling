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

// CompleteNode is the parameter struct for ExecutionService.CompleteNode.
type CompleteNode struct {
	AttemptID  string
	Output     registry.NodeOutput
	TokenUsage *domain.TokenUsage
}

type nodeCompletedPayload struct {
	Output           dataSummary             `json:"output"`
	LatencyMs        int64                   `json:"latencyMs"`
	TokenUsage       *domain.TokenUsage      `json:"tokenUsage,omitempty"`
	CompletionSource domain.CompletionSource `json:"completionSource"`
}

// nodeCallbackReceivedPayload is the bounded record of an accepted callback.
// It never carries the body, the token or the token hash:
// payloadHash identifies the delivery, and providerId/externalTaskId stay projected from
// the Callback Binding.
type nodeCallbackReceivedPayload struct {
	AttemptNo         int       `json:"attemptNo"`
	CallbackBindingID string    `json:"callbackBindingId"`
	ReceivedAt        time.Time `json:"receivedAt"`
	PayloadHash       string    `json:"payloadHash"`
}

// completeNodeParams is the internal, parameterised form of a successful completion. The
// synchronous path and the async resume path differ only in which state each transition
// starts from, which completion source the Event records and whether a Pending Callback is
// consumed -- everything downstream (READY successors, Run output, aggregation) is shared.
type completeNodeParams struct {
	attemptID        string
	output           registry.NodeOutput
	tokenUsage       *domain.TokenUsage
	fromAttempt      domain.NodeAttemptStatus
	fromNodeRun      domain.NodeRunStatus
	completionSource domain.CompletionSource
	// callbackBindingID is set only for a resume; it is what NODE_CALLBACK_RECEIVED and
	// the callback metadata refer to.
	callbackBindingID string
	// consumePending, when set, is the external task id of the stored early callback this
	// transaction must consume exactly once.
	consumePending string
	payloadHash    string
}

// CompleteNode records a successful synchronous Node execution result in one transaction:
// it moves the Attempt and NodeRun to SUCCEEDED (each conditionally -- a stale completion
// is silently ignored, never an error), writes Run.output if this was the Output Node,
// creates newly-READY NodeRuns, and re-aggregates the Run status.
func (s *ExecutionService) CompleteNode(ctx context.Context, req CompleteNode) error {
	_, err := s.completeNode(ctx, completeNodeParams{
		attemptID:        req.AttemptID,
		output:           req.Output,
		tokenUsage:       req.TokenUsage,
		fromAttempt:      domain.NodeAttemptStarted,
		fromNodeRun:      domain.NodeRunRunning,
		completionSource: domain.CompletionSyncExecution,
	})
	return err
}

func (s *ExecutionService) completeNode(ctx context.Context, p completeNodeParams) (nodeOutcomeResult, error) {
	var result nodeOutcomeResult
	var runID string
	var lastSeq int64

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, p.attemptID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
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
		plan, err := s.compile(ctx, def)
		if err != nil {
			return fmt.Errorf("execution: recompile definition %s v%d for complete: %w", run.WorkflowID, run.DefinitionVersion, err)
		}

		lock, err := tx.Runs().LockForUpdate(ctx, run.ID)
		if err != nil {
			return err
		}

		now := s.deps.Clock.Now()
		outputBytes, err := json.Marshal(p.output.Ports)
		if err != nil {
			return fmt.Errorf("execution: encode node output: %w", err)
		}

		if err := s.consumePendingCallback(ctx, tx, attempt.ID, attempt.CallbackTokenHash, p.consumePending, p.payloadHash, now); err != nil {
			return err
		}

		if err := tx.NodeAttempts().MarkSucceeded(ctx, attempt.ID, p.fromAttempt, now, outputBytes); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				// A duplicate or late completion of an Attempt that has already been
				// resolved by another path (e.g. TimeoutAttempt won the race first).
				// Ignored, not an error: the transaction commits with no further effect.
				result.duplicate = true
				return nil
			}
			return err
		}

		var latencyMs int64
		if nodeRun.StartedAt != nil {
			latencyMs = now.Sub(*nodeRun.StartedAt).Milliseconds()
		}

		// For a synchronous completion, if NodeAttempts().MarkSucceeded above did not
		// report a stale claim, the NodeRun must still be RUNNING too: a NodeRun's single
		// execution slot and this same Run lock serialize every path (Advance,
		// CompleteNode, FailNode, TimeoutAttempt) that could otherwise move it. A stale
		// claim there would mean that invariant broke, so it is a hard error (rolling back
		// the Attempt success too) rather than a second silent no-op.
		//
		// A resume is different: WAITING_CALLBACK->SUCCEEDED is exactly the conditional
		// UPDATE that elects the single winner among callback, Provider Poll and timeout,
		// so losing it means another path already completed this
		// NodeRun. The whole transaction rolls back: no Event, no seq consumed.
		if err := tx.NodeRuns().MarkSucceeded(ctx, nodeRun.ID, p.fromNodeRun, now, store.NodeRunOutcome{
			Output:     outputBytes,
			TokenUsage: p.tokenUsage,
			LatencyMs:  &latencyMs,
		}); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) && p.fromNodeRun == domain.NodeRunWaitingCallback {
				return errResumeSuperseded
			}
			return err
		}

		// Only the callback that wins the completion right writes
		// NODE_CALLBACK_RECEIVED. A Provider Poll completion must not write it.
		if p.completionSource == domain.CompletionCallback && p.callbackBindingID != "" {
			if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeCallbackReceived, now, nodeCallbackReceivedPayload{
				AttemptNo:         attempt.AttemptNo,
				CallbackBindingID: p.callbackBindingID,
				ReceivedAt:        now,
				PayloadHash:       p.payloadHash,
			}); err != nil {
				return err
			}
		}

		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeCompleted, now, nodeCompletedPayload{
			Output:           summarize(outputBytes),
			LatencyMs:        latencyMs,
			TokenUsage:       p.tokenUsage,
			CompletionSource: p.completionSource,
		}); err != nil {
			return err
		}

		if err := s.advanceAfterNodeSuccess(ctx, tx, lock, run, nodeRun, def, plan, outputBytes, now); err != nil {
			return err
		}
		runID = run.ID
		result.runID = run.ID
		result.nodeRunID = nodeRun.ID
		lastSeq = lock.LastSeq()
		return nil
	})
	if errors.Is(err, errResumeSuperseded) {
		return nodeOutcomeResult{duplicate: true}, nil
	}
	if err != nil {
		return nodeOutcomeResult{}, err
	}
	if runID != "" {
		s.postCommit(runID, lastSeq)
	}
	return result, nil
}

// advanceAfterNodeSuccess is the shared tail of every transaction that succeeds one
// NodeRun: the Run output when this was the output node, the downstream NodeRuns this
// success makes READY together with their NODE_READY Events, and the re-aggregated Run
// status with its transition Event.
//
// It is shared rather than duplicated because Run aggregation and downstream scheduling
// are one decision (runtime.AggregateRunStatus, runtime.NextReady) reached from two
// completion shapes: a normal Node Attempt (completeNode) and a MANAGED_AGENT NodeRun,
// which has no Attempt and closes out inside the Final completion transaction. The caller
// holds the Run aggregate lock and has already written the NodeRun's own SUCCEEDED status
// and its NODE_COMPLETED Event.
func (s *ExecutionService) advanceAfterNodeSuccess(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	run domain.Run,
	nodeRun domain.NodeRun,
	def domain.Definition,
	plan *runtime.CompiledDefinition,
	outputBytes json.RawMessage,
	now time.Time,
) error {
	outputProduced := len(run.Output) > 0
	if nodeRun.NodeID == plan.OutputNodeID {
		if err := tx.Runs().SetOutput(ctx, lock, outputBytes); err != nil {
			return err
		}
		outputProduced = true
	}

	allNodeRuns, err := tx.NodeRuns().ListByRun(ctx, run.ID)
	if err != nil {
		return err
	}
	existing := make(map[string]runtime.NodeRunState, len(allNodeRuns))
	nodeStatuses := make(map[string]domain.NodeRunStatus, len(allNodeRuns))
	for _, nr := range allNodeRuns {
		existing[nr.NodeID] = runtime.NodeRunState{NodeID: nr.NodeID, Status: nr.Status}
		nodeStatuses[nr.NodeID] = nr.Status
	}

	newReadyIDs := runtime.NextReady(plan, existing)
	for _, nodeID := range newReadyIDs {
		node, ok := nodeByID(def, nodeID)
		if !ok {
			return fmt.Errorf("execution: compiled plan references unknown node %q", nodeID)
		}
		newNR := domain.NodeRun{
			ID:        s.deps.IDs.NewID(domain.IDPrefixNodeRun),
			RunID:     run.ID,
			NodeID:    nodeID,
			NodeType:  node.Type,
			Status:    domain.NodeRunReady,
			Input:     json.RawMessage(`{}`),
			ReadyAt:   now,
			UpdatedAt: now,
		}
		if err := tx.NodeRuns().Create(ctx, newNR); err != nil {
			return err
		}
		nodeStatuses[nodeID] = domain.NodeRunReady
		if err := s.appendEvent(ctx, tx, lock, run.ID, &newNR.ID, domain.EventNodeReady, now, nodeReadyPayload{
			NodeID: newNR.NodeID, NodeType: newNR.NodeType,
		}); err != nil {
			return err
		}
	}

	newStatus := runtime.AggregateRunStatus(runtime.RunAggregateInput{
		NodeStatuses:   nodeStatuses,
		AllNodeIDs:     plan.Order,
		OutputNodeID:   plan.OutputNodeID,
		OutputProduced: outputProduced,
	})
	if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), newStatus); changed {
		if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: newStatus}); err != nil {
			return err
		}
	}
	return tx.Runs().UpdateAggregate(ctx, lock, newStatus, now)
}
