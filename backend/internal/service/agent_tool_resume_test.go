package service

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const agentResumeOutputSchema = `{"type":"object","required":["key"],"properties":{"key":{"type":"string"}},"additionalProperties":false}`

// TestAgentCallbackOutcome_ClassifiesProviderFailureInvalidResultAndUninterpretable covers
// the three ways an async Tool callback can end: a Provider-reported
// failure fails the Action with the Provider's error, a result the registered OutputSchema
// rejects fails it as TOOL_RESULT_INVALID, and any other Executor error is an
// uninterpretable payload that must not change state.
func TestAgentCallbackOutcome_ClassifiesProviderFailureInvalidResultAndUninterpretable(t *testing.T) {
	schema := json.RawMessage(agentResumeOutputSchema)

	execErr, interpretErr := agentCallbackOutcome(registry.ToolResult{Output: json.RawMessage(`{"key":"k1"}`)}, nil, schema)
	if execErr != nil || interpretErr != nil {
		t.Errorf("valid result: execErr = %+v interpretErr = %v, want neither", execErr, interpretErr)
	}

	providerErr := domain.ExecutionError{Code: "REMOTE_LOOKUP_FAILED", Message: "not found"}
	execErr, interpretErr = agentCallbackOutcome(registry.ToolResult{}, &registry.ProviderFailure{Err: providerErr}, schema)
	if interpretErr != nil || execErr == nil || execErr.Code != providerErr.Code || execErr.Message != providerErr.Message {
		t.Errorf("provider failure: execErr = %+v interpretErr = %v, want the Provider's error", execErr, interpretErr)
	}

	execErr, interpretErr = agentCallbackOutcome(registry.ToolResult{Output: json.RawMessage(`{"record":"x"}`)}, nil, schema)
	if interpretErr != nil || execErr == nil || execErr.Code != "TOOL_RESULT_INVALID" {
		t.Errorf("schema violation: execErr = %+v interpretErr = %v, want TOOL_RESULT_INVALID", execErr, interpretErr)
	}

	garbled := errors.New("unknown status")
	execErr, interpretErr = agentCallbackOutcome(registry.ToolResult{}, garbled, schema)
	if execErr != nil || !errors.Is(interpretErr, garbled) {
		t.Errorf("uninterpretable payload: execErr = %+v interpretErr = %v, want the Executor error and no failure", execErr, interpretErr)
	}
}

// TestAsyncToolOutcome_ResolvesDispatchedAttemptOfWaitingAction covers the "from" statuses
// the asynchronous resume requires and the failureSource each delivery path
// records, next to the synchronous source it must not be confused with.
func TestAsyncToolOutcome_ResolvesDispatchedAttemptOfWaitingAction(t *testing.T) {
	hash := "h"
	callback := asyncToolOutcome(domain.CompletionCallback, "task_1", "payload_hash", &hash)
	if callback.fromAttempt != domain.ToolAttemptDispatched || callback.fromAction != domain.AgentActionWaitingCallback || !callback.resumesWaiting {
		t.Errorf("callback source = %+v, want DISPATCHED / WAITING_CALLBACK resuming the waiting NodeRun", callback)
	}
	if callback.completionSource != domain.CompletionCallback || callback.failureSource != domain.FailureCallback {
		t.Errorf("callback sources = %s / %s, want CALLBACK / CALLBACK", callback.completionSource, callback.failureSource)
	}
	if callback.consumePending != "task_1" || callback.payloadHash != "payload_hash" || callback.attemptTokenHash != &hash {
		t.Errorf("callback pending replay = %+v, want the stored row's identity", callback)
	}

	poll := asyncToolOutcome(domain.CompletionProviderPoll, "", "", nil)
	if poll.failureSource != domain.FailureProviderPoll || poll.completionSource != domain.CompletionProviderPoll {
		t.Errorf("poll sources = %s / %s, want PROVIDER_POLL", poll.completionSource, poll.failureSource)
	}

	if syncToolOutcome.fromAttempt != domain.ToolAttemptStarted || syncToolOutcome.fromAction != domain.AgentActionRunning ||
		syncToolOutcome.resumesWaiting || syncToolOutcome.failureSource != domain.FailureSyncExecution {
		t.Errorf("sync source = %+v, want STARTED / RUNNING / SYNC_EXECUTION without resume", syncToolOutcome)
	}
}
