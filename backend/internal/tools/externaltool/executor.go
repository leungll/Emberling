package externaltool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// Task statuses the service reports in a terminal payload.
const (
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// defaultFailureCode is used when the service reports FAILED without naming a code, so a
// persisted failure always carries one.
const defaultFailureCode = "EXTERNAL_TASK_FAILED"

// maxFailureTextBytes bounds the service-supplied failure code and message, which reach
// the Tool Attempt's error and Trace.
const maxFailureTextBytes = 512

// Executor implements registry.PollableAsyncToolExecutor for one Spec. It performs exactly
// one registered operation per call and owns no retry, timeout, callback routing, state
// transition or Event.
type Executor struct {
	toolName   string
	providerID string
	taskType   string
	mapInput   func(registry.ToolAction) (json.RawMessage, error)
	mapResult  func(context.Context, json.RawMessage) (registry.ToolResult, error)
	client     TaskClient
}

// Execute dispatches one task and returns its external identity. The id is computed from
// the Action and Attempt before anything is sent, so it is known even when the service's
// answer is lost, and it is what the Callback Binding records. Being an ASYNC Tool it
// returns a DISPATCHED result or an explicit error, never a completed result.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if action.Callback == nil || strings.TrimSpace(action.Callback.URL) == "" || strings.TrimSpace(action.Callback.Token) == "" {
		// Nothing has been sent, so no task can exist.
		return registry.ToolExecutionResult{}, e.dispatchError("", "build request", errors.New("callback URL and token are both required"))
	}
	if action.ActionID == "" || action.AttemptNo <= 0 {
		return registry.ToolExecutionResult{}, e.dispatchError("", "build request", errors.New("the call names no action id and attempt number"))
	}
	input, err := e.mapInput(action)
	if err != nil {
		return registry.ToolExecutionResult{}, e.dispatchError("", "map input", err)
	}
	taskID := TaskID(action.ActionID, action.AttemptNo)
	if err := e.client.Submit(ctx, taskID, e.taskType, input, *action.Callback); err != nil {
		return registry.ToolExecutionResult{}, e.dispatchError(taskID, "submit task", err)
	}
	return registry.ToolExecutionResult{
		Kind:         registry.ToolResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: e.providerID, ExternalTaskID: taskID},
	}, nil
}

// terminalPayload is the service's report of a finished task, as a callback payload and as
// the body of a terminal status answer alike.
type terminalPayload struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// OnCallback converts one callback payload into the Tool result.
//
// A task the service reports FAILED becomes a *registry.ProviderFailure, which fails the
// Tool Attempt with the callback as its failure source. A payload this Tool cannot
// interpret stays a plain error, since a malformed delivery is not evidence that the task
// failed.
func (e *Executor) OnCallback(ctx context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
	taskID := state.ExternalTask.ExternalTaskID
	var body terminalPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s is not a JSON object", e.toolName, taskID)
	}
	return e.interpret(ctx, taskID, body)
}

// interpret turns a terminal payload into the Tool result or the Provider-reported
// failure. It is the one place a finished task is read, for callbacks and polls alike.
func (e *Executor) interpret(ctx context.Context, taskID string, body terminalPayload) (registry.ToolResult, error) {
	switch body.Status {
	case statusSucceeded:
		if !hasValue(body.Result) {
			return registry.ToolResult{}, fmt.Errorf("%s: payload for external task %s reports %s without a result", e.toolName, taskID, statusSucceeded)
		}
		result, err := e.mapResult(ctx, body.Result)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("%s: map result of external task %s: %w", e.toolName, taskID, err)
		}
		return result, nil
	case statusFailed:
		failure := domain.ExecutionError{Code: defaultFailureCode, Message: "the external task service reported the task failed"}
		if body.Error != nil {
			if body.Error.Code != "" {
				failure.Code = truncateUTF8(body.Error.Code, maxFailureTextBytes)
			}
			if body.Error.Message != "" {
				failure.Message = truncateUTF8(body.Error.Message, maxFailureTextBytes)
			}
		}
		return registry.ToolResult{}, &registry.ProviderFailure{Err: failure}
	default:
		return registry.ToolResult{}, fmt.Errorf("%s: payload for external task %s carries status %q, want %s or %s", e.toolName, taskID, body.Status, statusSucceeded, statusFailed)
	}
}

// Poll asks the service for the task's status once. A service that does not know the task
// answers UNKNOWN_TASK, which the Runtime turns into interrupted work handed back to the
// Agent because every external Tool is safe to repeat. A finished task carries the same
// payload its callback would and is interpreted by the same rules. An answer the client
// could not classify is UNKNOWN and changes nothing; a query that got no answer is an
// error, which the Runtime reports and the already scheduled next poll follows.
func (e *Executor) Poll(ctx context.Context, state registry.ToolAsyncState) (registry.PollResult, error) {
	taskID := state.ExternalTask.ExternalTaskID
	status, payload, err := e.client.Get(ctx, taskID)
	if err != nil {
		return registry.PollResult{}, fmt.Errorf("%s: query external task %s: %w", e.toolName, taskID, err)
	}
	switch status {
	case registry.PollSucceeded, registry.PollFailed:
		var body terminalPayload
		if err := json.Unmarshal(payload, &body); err != nil || body.Status != string(status) {
			return registry.PollResult{Status: registry.PollStatusUnknown}, nil
		}
		result, err := e.interpret(ctx, taskID, body)
		var providerFailure *registry.ProviderFailure
		switch {
		case errors.As(err, &providerFailure):
			failure := providerFailure.Err
			return registry.PollResult{Status: registry.PollFailed, Error: &failure}, nil
		case err != nil:
			return registry.PollResult{}, err
		}
		return registry.PollResult{Status: registry.PollSucceeded, ToolResult: &result}, nil
	case registry.PollRunning, registry.PollUnknownTask:
		return registry.PollResult{Status: status}, nil
	default:
		// UNKNOWN, or a status this Tool does not know: nothing can be concluded.
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
}

// dispatchError builds a dispatch failure from the Tool Name, the task id when one was
// computed, a fixed operation label and the underlying reason. The reason comes from this
// package, a Spec's own mapping or the TaskClient, none of which carries the callback
// token, the callback URL or a response body.
func (e *Executor) dispatchError(taskID, op string, reason error) error {
	if taskID != "" {
		return fmt.Errorf("%s: dispatch external task %s to provider %q: %s: %w", e.toolName, taskID, e.providerID, op, reason)
	}
	return fmt.Errorf("%s: dispatch to provider %q: %s: %w", e.toolName, e.providerID, op, reason)
}

// hasValue reports whether a decoded JSON member is present and not JSON null.
func hasValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

// truncateUTF8 cuts s to at most limit bytes without splitting a multi-byte rune.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
