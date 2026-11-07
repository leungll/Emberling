package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the return of a definite refusal to the Agent: an Action the Runtime, a
// person or the external system refused for a reason the Agent can act on. The Action
// fails, but the Agent Run continues: it receives a bounded tool message saying what was
// refused and why, and decides the next step in a new Turn. Only the closed set of return
// codes may take this path; every other failure keeps ending the Agent Run.
//
// The tail is shared with the orphan handling: the committed Decision and the tool message
// become the next Context Version, and the existing round advancement decides whether a
// READY Turn follows or the round or time limit ends the Agent Run instead.

// errReturnCodeNotReturnable rejects a return outside the closed set before anything is
// written. It is a programming error of the caller, not a Runtime outcome.
var errReturnCodeNotReturnable = errors.New("execution: return code is not in the returnable set")

// maxReturnToolMessageBytes bounds the tool message the Agent receives for a refusal.
const maxReturnToolMessageBytes = 1024

// returnToolMessageStatus is the status every returned refusal carries, so a scripted
// trajectory or a model can match the refusal before reading its code.
const returnToolMessageStatus = "REJECTED"

// returnToolMessage is the content of the `tool` message appended in place of a result. It
// carries the status, the code and a bounded message: never the call's arguments, an
// external response body, a credential or a storage location.
type returnToolMessage struct {
	Status  string                 `json:"status"`
	Code    domain.AgentReturnCode `json:"code"`
	Message string                 `json:"message"`
}

// agentActionReturn is everything the return needs about the Action, read by the caller
// under the Run aggregate lock it already holds. attemptID names the Attempt the caller
// already failed in the same transaction, or nil when the refusal came before any Attempt
// existed.
//
// run and plan are required when the Action is returned from WAITING_CALLBACK,
// WAITING_APPROVAL or a WAITING_HUMAN whose NodeRun waits on production: resuming
// the Agent NodeRun re-derives the Run status from the compiled plan of the Run's immutable
// Definition version. The caller resolves them before taking the Run lock, as every other
// asynchronous resume does, because compiling a Definition is work no Run row lock may be
// held across. A return from RUNNING leaves them empty.
type agentActionReturn struct {
	lock      *store.RunLock
	nodeRun   domain.NodeRun
	agentRun  domain.AgentRun
	turn      domain.AgentTurn
	action    domain.AgentAction
	attemptID *string
	toolName  string
	run       domain.Run
	plan      *runtime.CompiledDefinition
}

// returnActionToAgentLocked hands a definite refusal back to the Agent inside a
// transaction whose Run aggregate lock the caller holds. In that transaction it moves the
// Action from `from` to FAILED by conditional update, appends AGENT_ACTION_FAILED with
// returnedToAgent and the failure source the code maps to, appends the committed Decision
// and the bounded REJECTED tool message as the next Context Version, and advances the
// round: the next READY Turn is created, or the frozen turn bound or the expired deadline
// ends the Agent Run as MAX_TURNS or TIMEOUT. It returns the new Turn's id, or "" when the
// Agent Run terminated.
//
// Everything commits together or not at all. A conditional update that affects no row,
// because another path already moved the Action out of `from`, returns
// errAgentTurnSuperseded before anything is appended, so the losing caller writes nothing;
// a crash before COMMIT leaves the Action where it was for the path that raised the
// refusal to decide again. The Attempt, when there is one, is the caller's business: it
// is failed in the same transaction before this call. After COMMIT the caller offers the
// new Turn to the work queue; a refused enqueue leaves it to the Reconciler.
func (s *ExecutionService) returnActionToAgentLocked(
	ctx context.Context,
	tx store.Tx,
	ret agentActionReturn,
	from domain.AgentActionStatus,
	code domain.AgentReturnCode,
	message string,
	now time.Time,
) (string, error) {
	failureSource, returnable := code.FailureSource()
	if !returnable {
		return "", fmt.Errorf("%w: %q", errReturnCodeNotReturnable, code)
	}
	switch from {
	case domain.AgentActionRunning, domain.AgentActionWaitingCallback, domain.AgentActionWaitingApproval, domain.AgentActionWaitingHuman:
	default:
		return "", fmt.Errorf("execution: return action %s to agent from %s: only a RUNNING, WAITING_CALLBACK, WAITING_APPROVAL or WAITING_HUMAN action can be returned", ret.action.ID, from)
	}
	// A release Action handed to a person may have been dispatched (its NodeRun waits on
	// production) or not (its NodeRun still runs); the NodeRun says which.
	resumesWaiting := from == domain.AgentActionWaitingCallback || from == domain.AgentActionWaitingApproval ||
		(from == domain.AgentActionWaitingHuman && ret.nodeRun.Status == domain.NodeRunWaitingCallback)
	if resumesWaiting && ret.plan == nil {
		return "", fmt.Errorf("execution: return action %s to agent from %s: the caller must resolve the run and its plan before the lock", ret.action.ID, from)
	}

	content, err := buildReturnToolMessage(code, message)
	if err != nil {
		return "", fmt.Errorf("execution: return action %s to agent: %w", ret.action.ID, err)
	}
	execError := domain.ExecutionError{Code: string(code), Message: boundedReturnMessage(message)}

	if err := tx.AgentActions().MarkFailed(ctx, ret.action.ID, from, now, execError); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return "", errAgentTurnSuperseded
		}
		return "", err
	}
	if err := s.appendEvent(ctx, tx, ret.lock, ret.nodeRun.RunID, &ret.nodeRun.ID, domain.EventAgentActionFailed, now, agentActionFailedPayload{
		AgentRunID:      ret.agentRun.ID,
		TurnID:          ret.turn.ID,
		ActionID:        ret.action.ID,
		ToolAttemptID:   ret.attemptID,
		FailureSource:   failureSource,
		Error:           execError,
		ReturnedToAgent: true,
	}); err != nil {
		return "", err
	}

	decision, err := tx.AgentDecisions().Get(ctx, ret.action.DecisionID)
	if err != nil {
		return "", err
	}
	call := agentToolCall{
		runID:      ret.nodeRun.RunID,
		nodeRunID:  ret.nodeRun.ID,
		agentRunID: ret.agentRun.ID,
		turnID:     ret.turn.ID,
		actionID:   ret.action.ID,
		toolName:   ret.toolName,
	}
	if ret.attemptID != nil {
		call.attemptID = *ret.attemptID
	}
	messages, err := s.appendToolRoundContext(ctx, tx, ret.agentRun, call, decision, content, now)
	if err != nil {
		return "", err
	}

	// A refusal of a waiting Action -- on a callback or on a person -- resumes the Agent
	// NodeRun from WAITING_CALLBACK, which re-derives the Run status from the Run and plan
	// the caller resolved before the lock.
	src := syncToolOutcome
	if resumesWaiting {
		src = agentToolOutcomeSource{fromAction: from, failureSource: failureSource, resumesWaiting: true}
	}
	return s.advanceAgentRoundLocked(ctx, tx, ret.lock, ret.nodeRun, ret.agentRun, call, src, ret.run, ret.plan,
		ret.agentRun.CurrentContextVersion+1, ret.agentRun.CurrentStateVersion, len(messages), now)
}

// buildReturnToolMessage renders the bounded tool message of one refusal. The message is
// cut on a rune boundary so the whole content stays within maxReturnToolMessageBytes.
func buildReturnToolMessage(code domain.AgentReturnCode, message string) (json.RawMessage, error) {
	content, err := json.Marshal(returnToolMessage{Status: returnToolMessageStatus, Code: code, Message: boundedReturnMessage(message)})
	if err != nil {
		return nil, fmt.Errorf("encode %s tool message: %w", code, err)
	}
	if len(content) > maxReturnToolMessageBytes {
		return nil, fmt.Errorf("%s tool message is %d bytes, over the %d-byte bound", code, len(content), maxReturnToolMessageBytes)
	}
	return content, nil
}

// maxReturnMessageBytes leaves room inside maxReturnToolMessageBytes for the status, the
// longest code and the JSON framing, with JSON escaping of the message accounted for by
// the final length check in buildReturnToolMessage.
const maxReturnMessageBytes = 512

// boundedReturnMessage cuts a refusal message to maxReturnMessageBytes on a rune boundary.
func boundedReturnMessage(message string) string {
	if len(message) <= maxReturnMessageBytes {
		return message
	}
	cut := maxReturnMessageBytes
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut]
}

// verificationMissingMessage tells the Agent which fact type an unmet requirement needs.
// It names the fact type and the reason only, never the subject or any other argument.
func verificationMissingMessage(unmet runtime.RequirementFailure) string {
	return fmt.Sprintf("The call needs a committed %q fact before it can run (%s). Establish that fact and call the tool again.", unmet.FactType, unmet.Reason)
}
