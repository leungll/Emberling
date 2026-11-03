package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the handling of an orphaned synchronous Tool Attempt: a STARTED Attempt
// created by a process that no longer exists, so nobody will ever commit its outcome.
//
// The call is never made again. Its Attempt and Action fail with what is actually known
// about the call, and the Agent receives a bounded tool message saying so and decides the
// next step in a new Turn. Whether the outcome is unknown or the call is simply safe to
// repeat comes from the Tool's registered side-effect policy, never from its name.

// Error codes an orphaned Tool Attempt and its Action fail with.
const (
	codeResultUncertain      = "RESULT_UNCERTAIN"
	codeExecutionInterrupted = "EXECUTION_INTERRUPTED"
)

// maxOrphanToolNameBytes bounds the Tool Name repeated in the orphan tool message, so the
// whole message stays within maxOrphanToolMessageBytes whatever Name was registered.
const maxOrphanToolNameBytes = 128

// maxOrphanToolMessageBytes is the upper bound of the orphan tool message content.
const maxOrphanToolMessageBytes = 1024

// orphanDisposition is what the registered side-effect policy says about an orphaned call.
type orphanDisposition int

const (
	// orphanUncertain: an EXTERNAL call with UNKNOWN idempotency may already have taken
	// effect. The Agent continues and is told the outcome is unknown.
	orphanUncertain orphanDisposition = iota
	// orphanInterrupted: a call with no side effect, or a SAFE one, can simply be repeated.
	// The Agent continues and is told so.
	orphanInterrupted
	// orphanTerminal: any other policy, or a Tool no longer registered so that no policy
	// can be read. Its outcome is unknown and the Agent Run ends as TOOL_ERROR.
	orphanTerminal
)

// classifyOrphan maps the Tool's current registration to the handling of its orphaned
// call. registered is false when the Tool Name no longer resolves.
func classifyOrphan(metadata domain.ToolMetadata, registered bool) orphanDisposition {
	switch {
	case !registered:
		return orphanTerminal
	case metadata.SideEffect.Kind == domain.SideEffectNone || metadata.SideEffect.Idempotency == domain.IdempotencySafe:
		return orphanInterrupted
	case metadata.SideEffect.Kind == domain.SideEffectExternal && metadata.SideEffect.Idempotency == domain.IdempotencyUnknown:
		return orphanUncertain
	default:
		return orphanTerminal
	}
}

// orphanExecutionError is the error the Attempt, the Action and AGENT_ACTION_FAILED record.
func orphanExecutionError(disposition orphanDisposition) (domain.ExecutionError, bool) {
	if disposition == orphanInterrupted {
		return domain.ExecutionError{
			Code:    codeExecutionInterrupted,
			Message: "the process running the tool call stopped before it returned; calling it again is safe",
		}, false
	}
	return domain.ExecutionError{
		Code:    codeResultUncertain,
		Message: "the process running the tool call stopped; its outcome is unknown",
	}, true
}

// orphanToolMessage is the content of the `tool` message the Agent receives in place of a
// result. It carries only the Tool Name, the status and the generation count: never the
// call's arguments, a Provider response, a credential or a storage location.
type orphanToolMessage struct {
	Status          string                 `json:"status"`
	Message         string                 `json:"message"`
	GenerationCalls *orphanGenerationCalls `json:"generationCalls,omitempty"`
}

// orphanGenerationCalls reports the generation calls used, including the orphaned one, and
// the frozen limit, null when the Agent Run has none.
type orphanGenerationCalls struct {
	Used  int  `json:"used"`
	Limit *int `json:"limit"`
}

// orphanGenerationCount is what the tool message says about the generation limit.
// counted reports whether the Tool's registration counts toward it.
type orphanGenerationCount struct {
	counted bool
	used    int
	limit   *int
}

// buildOrphanToolMessage renders the bounded tool message for one orphaned call. The
// generation sentence follows the Tool's registration, not the disposition, so neither
// kind of message misstates whether the call used up part of the budget.
func buildOrphanToolMessage(toolName string, disposition orphanDisposition, count orphanGenerationCount) (json.RawMessage, error) {
	name := boundedToolName(toolName)
	countSentence, calls := orphanGenerationSentence(count)

	msg := orphanToolMessage{GenerationCalls: calls}
	if disposition == orphanInterrupted {
		msg.Status = codeExecutionInterrupted
		msg.Message = fmt.Sprintf("The %s call was interrupted because the process running it stopped before it returned a result. "+
			"Calling the tool again is safe. %s", name, countSentence)
	} else {
		msg.Status = codeResultUncertain
		msg.Message = fmt.Sprintf("The %s call was interrupted because the process running it stopped. "+
			"Its outcome is unknown: the provider may already have generated and billed the result, but no result is available to reference. "+
			"The call was not retried automatically. %s Decide whether to call the tool again.", name, countSentence)
	}

	return encodeOrphanToolMessage(name, msg)
}

// buildLostTaskToolMessage renders the bounded tool message for an asynchronous call whose
// Provider no longer knows the task it was given, when the registered policy says the call
// is safe to repeat. It has the shape of the interrupted orphan message: the status, a
// message naming only the Tool, and the generation count.
func buildLostTaskToolMessage(toolName string, count orphanGenerationCount) (json.RawMessage, error) {
	name := boundedToolName(toolName)
	countSentence, calls := orphanGenerationSentence(count)
	msg := orphanToolMessage{
		Status: codeExecutionInterrupted,
		Message: fmt.Sprintf("The %s call was interrupted because the external service no longer knows the task it was given, "+
			"for example after it restarted. No result will arrive. Calling the tool again is safe. %s", name, countSentence),
		GenerationCalls: calls,
	}
	return encodeOrphanToolMessage(name, msg)
}

// orphanGenerationSentence renders what the tool message says about the generation limit,
// following the Tool's registration so the message never misstates whether the call used
// up part of the budget.
func orphanGenerationSentence(count orphanGenerationCount) (string, *orphanGenerationCalls) {
	switch {
	case !count.counted:
		return "It does not count toward the generation limit.", nil
	case count.limit != nil:
		limit := *count.limit
		return fmt.Sprintf("It counts toward the generation limit: %d of %d calls used.", count.used, limit),
			&orphanGenerationCalls{Used: count.used, Limit: &limit}
	default:
		return fmt.Sprintf("It counts as a generation call: %d calls used, no limit is set.", count.used),
			&orphanGenerationCalls{Used: count.used}
	}
}

// encodeOrphanToolMessage encodes one interrupted-call tool message and enforces its bound.
func encodeOrphanToolMessage(name string, msg orphanToolMessage) (json.RawMessage, error) {
	content, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("encode orphan tool message for tool %q: %w", name, err)
	}
	if len(content) > maxOrphanToolMessageBytes {
		return nil, fmt.Errorf("orphan tool message for tool %q is %d bytes, over the %d-byte bound", name, len(content), maxOrphanToolMessageBytes)
	}
	return content, nil
}

// boundedToolName cuts a Tool Name to maxOrphanToolNameBytes on a rune boundary.
func boundedToolName(name string) string {
	if len(name) <= maxOrphanToolNameBytes {
		return name
	}
	cut := maxOrphanToolNameBytes
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

// HandleOrphanToolAttempt resolves one STARTED synchronous Tool Attempt that a process other
// than this one created and that nobody will complete. The Reconciler hands it every such
// Attempt it discovers; this use case owns every decision and write.
//
// Everything commits in one transaction under the Run aggregate lock: the Attempt's
// conditional STARTED -> FAILED update with its uncertain determination, the Action's
// RUNNING -> FAILED update and AGENT_ACTION_FAILED with failureSource RUNNER_LOST. When the
// registered policy lets the Agent continue, the same transaction appends the committed
// Decision and the bounded tool message as a new Context Version and then either creates
// the next READY Turn or ends the Agent Run at its round limit. Any other policy ends the
// Agent Run as TOOL_ERROR. No Decision state patch is applied, as on every Action failure.
//
// Nothing is written when the Run is terminal, the Attempt is no longer STARTED or belongs
// to this process or to no recorded process, the Agent Run has terminated or its deadline
// has passed (the Agent timeout owns that case), the Agent NodeRun is not RUNNING, the
// Action's Turn is not the current one, or the Tool is now registered as asynchronous. A
// conditional update that loses rolls everything back; a second handling of the same
// Attempt, a late result of the old process and the timeout therefore leave a single
// winner. After COMMIT the next Turn is offered to the work queue; nothing external is
// called here.
func (s *ExecutionService) HandleOrphanToolAttempt(ctx context.Context, attemptID string) error {
	var commit agentCommit
	var nextTurnID string

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.ToolAttempts().Get(ctx, attemptID)
		if err != nil {
			return err
		}
		action, err := tx.AgentActions().Get(ctx, attempt.ActionID)
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
		// A release Attempt whose process died before the call is not failed: production
		// may hold its operation, so the Runtime starts asking, which re-aggregates the
		// Run and therefore needs the compiled plan, resolved before the lock.
		reg, registered := s.deps.Tools.Get(attempt.ToolName)
		var run domain.Run
		var plan *runtime.CompiledDefinition
		if registered && reg.Metadata.Reconcilable {
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
		// Re-read what the lock now protects: the scan that found this Attempt ran in its
		// own read transaction, and a result, a timeout or another handling may have
		// committed in between.
		if attempt, err = tx.ToolAttempts().Get(ctx, attemptID); err != nil {
			return err
		}
		if agentRun, err = tx.AgentRuns().Get(ctx, agentRun.ID); err != nil {
			return err
		}
		if nodeRun, err = tx.NodeRuns().Get(ctx, nodeRun.ID); err != nil {
			return err
		}

		now := s.deps.Clock.Now()
		if !s.orphanedByAnotherRunner(attempt) {
			return nil
		}
		// An expired deadline belongs to the Agent timeout, unless the Agent Run is paused:
		// then its deadline is not running and the orphaned call is still this path's.
		if agentRun.Termination != nil || (agentRun.PausedAt == nil && !now.Before(agentRun.Deadline)) {
			return nil
		}
		if nodeRun.Status != domain.NodeRunRunning || turn.TurnNo != agentRun.CurrentTurnNo {
			return nil
		}
		if registered && reg.Metadata.Reconcilable {
			if action, err = tx.AgentActions().Get(ctx, action.ID); err != nil {
				return err
			}
			if err := s.settleReleaseDispatchLocked(ctx, tx, releaseSettle{
				lock: lock, run: run, plan: plan, nodeRun: nodeRun, agentRun: agentRun, turnID: turn.ID, action: action, attempt: attempt,
			}, true, now); err != nil {
				return err
			}
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq()}
			return nil
		}
		if registered && reg.Metadata.ExecutionKind != domain.ToolExecutionSync {
			// An asynchronous Tool's call may have been accepted with its callback still
			// to come; it stays bounded by the deadline instead.
			return nil
		}

		disposition := classifyOrphan(reg.Metadata, registered)
		execError, uncertain := orphanExecutionError(disposition)
		if err := tx.ToolAttempts().MarkInterrupted(ctx, attempt.ID, now, execError, uncertain); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		attemptRef := attempt.ID

		if disposition == orphanTerminal {
			if err := s.failAgentActionLocked(ctx, tx, lock, nodeRun, agentRun, turn.ID, action.ID, &attemptRef,
				domain.AgentActionRunning, domain.FailureRunnerLost, domain.TerminationToolError, execError, now); err != nil {
				return err
			}
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}

		if err := tx.AgentActions().MarkFailed(ctx, action.ID, domain.AgentActionRunning, now, execError); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return errAgentTurnSuperseded
			}
			return err
		}
		if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentActionFailed, now, agentActionFailedPayload{
			AgentRunID:    agentRun.ID,
			TurnID:        turn.ID,
			ActionID:      action.ID,
			ToolAttemptID: &attemptRef,
			FailureSource: domain.FailureRunnerLost,
			Error:         execError,
		}); err != nil {
			return err
		}

		count, err := s.orphanGenerationCountLocked(ctx, tx, agentRun, reg.Metadata)
		if err != nil {
			return err
		}
		content, err := buildOrphanToolMessage(attempt.ToolName, disposition, count)
		if err != nil {
			return fmt.Errorf("execution: orphan tool attempt %s: %w", attempt.ID, err)
		}
		decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
		if err != nil {
			return err
		}
		call := agentToolCall{
			runID:      nodeRun.RunID,
			nodeRunID:  nodeRun.ID,
			agentRunID: agentRun.ID,
			turnID:     turn.ID,
			actionID:   action.ID,
			attemptID:  attempt.ID,
			toolName:   attempt.ToolName,
		}
		messages, err := s.appendToolRoundContext(ctx, tx, agentRun, call, decision, content, now)
		if err != nil {
			return err
		}

		turnID, err := s.advanceAgentRoundLocked(ctx, tx, lock, nodeRun, agentRun, call, syncToolOutcome, domain.Run{}, nil,
			agentRun.CurrentContextVersion+1, agentRun.CurrentStateVersion, len(messages), now)
		if err != nil {
			return err
		}
		if turnID == "" {
			commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq(), terminal: true}
			return nil
		}
		nextTurnID = turnID
		commit = agentCommit{runID: nodeRun.RunID, lastSeq: lock.LastSeq()}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("execution: handle orphan tool attempt %s: %w", attemptID, err)
	}
	s.wake(commit)
	if nextTurnID != "" {
		// The READY Turn is committed; a refused enqueue leaves it to the Reconciler.
		s.enqueueAgentTurn(commit.runID, nextTurnID)
	}
	return nil
}

// orphanedByAnotherRunner reports whether a Tool Attempt is still STARTED and was created by
// a recorded process other than this one. An Attempt with no recorded process is never an
// orphan: whether its process is still alive cannot be known.
func (s *ExecutionService) orphanedByAnotherRunner(attempt domain.ToolAttempt) bool {
	return attempt.Status == domain.ToolAttemptStarted &&
		attempt.RunnerID != nil && *attempt.RunnerID != "" && *attempt.RunnerID != s.deps.RunnerID
}

// orphanGenerationCountLocked reads the generation calls the Agent Run has used, the
// orphaned Attempt included, when the Tool's registration counts toward the limit. The
// count is derived from persisted Attempts of every counting Tool, as the claim-time limit
// check derives it.
func (s *ExecutionService) orphanGenerationCountLocked(ctx context.Context, tx store.Tx, agentRun domain.AgentRun, tool domain.ToolMetadata) (orphanGenerationCount, error) {
	if !tool.CountsTowardGenerationLimit {
		return orphanGenerationCount{}, nil
	}
	var counted []string
	for _, metadata := range s.deps.Tools.ListMetadata() {
		if metadata.CountsTowardGenerationLimit {
			counted = append(counted, metadata.Name)
		}
	}
	used, err := tx.ToolAttempts().CountByAgentRunForTools(ctx, agentRun.ID, counted)
	if err != nil {
		return orphanGenerationCount{}, fmt.Errorf("execution: count generation calls of agent run %s: %w", agentRun.ID, err)
	}
	return orphanGenerationCount{counted: true, used: used, limit: agentRun.MaxGenerationCalls}, nil
}
