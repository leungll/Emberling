// Package remotelookup implements the `remote_lookup` Tool: the ASYNC counterpart of the
// built-in `lookup` Tool. It dispatches one task to the deterministic Mock Provider's
// POST /v1/tasks and reports the record later through the shared callback endpoint
// (docs/07-extensibility.md §1.5: an async Tool returns an external task id and resumes the
// same Tool Attempt through a callback).
//
// It performs exactly one registered operation. It owns no retry, timeout, callback
// routing, state transition or Event: a failed dispatch is reported upward as an error and
// the Agent Runtime decides what happens next.
//
// Secret handling mirrors the mocktask Adapter: the one-time plaintext callback token
// travels in the request body and nowhere else, this package logs nothing, and its errors
// carry only the stable Provider identifier, a short operation label and an HTTP status
// code - never the token, the callback URL, the arguments or a response body.
package remotelookup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "remote_lookup"

// ProviderID is the stable identifier of the Provider this Tool dispatches to. It is
// persisted in the Callback Binding and shown in Trace; it is not a credential.
const ProviderID = "mock-task-provider-v1"

const tasksPath = "/v1/tasks"

// defaultTimeout bounds one dispatch when no HTTP client is injected. The Agent deadline
// still arrives through ctx and wins whenever it is shorter.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds the dispatch response this Tool reads.
const maxResponseBytes = 64 << 10

// directivePrefix marks a key as a Mock Provider scenario trigger, mirroring the mocktask
// Adapter's prompt directives, so integration tests can reach the Provider's failure,
// lost and duplicate callback scenarios through a real registered Tool call.
const directivePrefix = "mock:"

// Callback payload statuses the Provider reports.
const (
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// defaultFailureCode is used when the Provider reports FAILED without naming a code.
const defaultFailureCode = "PROVIDER_TASK_FAILED"

// maxFailureMessageBytes bounds the Provider-supplied failure message. That text is
// Provider-controlled and reaches the Tool Attempt's error and Trace, so it must stay
// bounded however large the callback payload is.
const maxFailureMessageBytes = 512

const inputSchema = `{
  "type": "object",
  "properties": {
    "key": {"type": "string", "minLength": 1}
  },
  "required": ["key"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "key": {"type": "string"},
    "record": {"type": "string"}
  },
  "required": ["key", "record"],
  "additionalProperties": false
}`

// Registration returns the `remote_lookup` ToolRegistration dispatching to the Mock
// Provider at baseURL. A nil client means a default client bounded by defaultTimeout.
//
// The dispatch creates an external task, so the call has an EXTERNAL side effect. Its
// idempotency is UNKNOWN: the Runtime hands an Agent Tool no idempotency key, so a replayed
// dispatch could create a second task, and nothing here may claim KEYED
// (docs/07-extensibility.md §1.2). The MVP never retries an Agent Tool anyway.
func Registration(baseURL string, client *http.Client) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Description:   "Read a deterministic record through an asynchronous Provider task",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionAsync,
		},
		Executor: New(baseURL, client),
	}
}

// Executor implements registry.AsyncToolExecutor for `remote_lookup`.
type Executor struct {
	baseURL    string
	httpClient *http.Client
}

// New returns an Executor dispatching to the Mock Provider at baseURL.
func New(baseURL string, client *http.Client) *Executor {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Executor{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

type arguments struct {
	Key string `json:"key"`
}

// record is the Tool's result; field order is the result's key order.
type record struct {
	Key    string `json:"key"`
	Record string `json:"record"`
}

// taskRequest is this Tool's view of the Mock Provider's POST /v1/tasks body.
type taskRequest struct {
	CallbackURL   string          `json:"callbackUrl"`
	CallbackToken string          `json:"callbackToken"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Outcome       string          `json:"outcome,omitempty"`
	DelayMs       any             `json:"delayMs,omitempty"`
}

type taskResponse struct {
	ExternalTaskID string `json:"externalTaskId"`
}

// Execute dispatches one task and returns its external identity. Being an ASYNC Tool it
// returns a DISPATCHED result or an explicit error, never a completed result.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if action.Callback == nil || strings.TrimSpace(action.Callback.URL) == "" || strings.TrimSpace(action.Callback.Token) == "" {
		// Nothing has been sent, so no task can exist.
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "callback URL and token are both required")
	}
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.Key == "" {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "arguments carry no key")
	}

	body, err := json.Marshal(buildRequest(args.Key, *action.Callback))
	if err != nil {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "request body could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+tasksPath, bytes.NewReader(body))
	if err != nil {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "request could not be created")
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := e.httpClient.Do(request)
	if err != nil {
		// A *url.Error names the request URL, not the callback URL; it is still reduced to
		// a fixed label so no transport detail can carry request content.
		return registry.ToolExecutionResult{}, dispatchError("send request", 0, "transport failure")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		// The body is deliberately not read into the error: a Provider may echo the
		// request, including the callback token, inside its own error body.
		return registry.ToolExecutionResult{}, dispatchError("provider rejected the dispatch", response.StatusCode, "non-2xx status")
	}
	var accepted taskResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&accepted); err != nil {
		return registry.ToolExecutionResult{}, dispatchError("decode response", response.StatusCode, "accepted response body is not a task response")
	}
	if accepted.ExternalTaskID == "" {
		// The Provider accepted a task this Runtime can never route a callback to; the
		// Runtime fails the Action explicitly rather than treating it as recoverable
		// (docs/09-testing-and-acceptance.md §3.7).
		return registry.ToolExecutionResult{}, dispatchError("decode response", response.StatusCode, "accepted response carries no externalTaskId")
	}

	return registry.ToolExecutionResult{
		Kind:         registry.ToolResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: accepted.ExternalTaskID},
	}, nil
}

// buildRequest converts one call into the Provider's wire request. A `mock:` directive in
// key selects one of the Provider's scenarios; any other key asks for the deterministic
// success payload that carries the record back in the callback.
func buildRequest(key string, callback registry.CallbackContext) taskRequest {
	request := taskRequest{CallbackURL: callback.URL, CallbackToken: callback.Token}
	directive, isDirective := strings.CutPrefix(key, directivePrefix)
	name, argument, _ := strings.Cut(directive, ":")
	switch {
	case isDirective && name == "failed":
		// The Provider's own failure payload is used; an explicit payload would override it.
		request.Outcome = name
		return request
	case isDirective && (name == "lost" || name == "duplicate"):
		request.DelayMs = name
	case isDirective && name == "delay":
		if milliseconds, err := strconv.Atoi(argument); err == nil && milliseconds >= 0 {
			request.DelayMs = milliseconds
		}
	}
	request.Payload = successPayload(key)
	return request
}

func successPayload(key string) json.RawMessage {
	encoded, err := json.Marshal(map[string]string{
		"status": statusSucceeded, "key": key, "record": "record for " + key,
	})
	if err != nil {
		return nil
	}
	return encoded
}

// callbackPayload is the Provider's callback body as this Tool reads it.
type callbackPayload struct {
	Status string  `json:"status"`
	Key    *string `json:"key"`
	Record *string `json:"record"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// OnCallback converts one Provider callback payload into the Tool result.
//
// A reported task failure becomes a *registry.ProviderFailure, which fails the Tool
// Attempt with the callback as its failure source. A payload this Tool cannot interpret
// stays a plain error: a malformed delivery is not evidence that the external task failed.
// Only key and record are republished, so a Provider-private member never reaches the
// Agent Context.
func (e *Executor) OnCallback(_ context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
	var body callbackPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s is not a JSON object", ToolName, state.ExternalTask.ExternalTaskID)
	}
	switch body.Status {
	case statusSucceeded:
		if body.Key == nil || body.Record == nil {
			return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s reports %s without key and record", ToolName, state.ExternalTask.ExternalTaskID, statusSucceeded)
		}
		output, err := json.Marshal(record{Key: *body.Key, Record: *body.Record})
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("%s: encode result for external task %s: %w", ToolName, state.ExternalTask.ExternalTaskID, err)
		}
		return registry.ToolResult{Output: output}, nil
	case statusFailed:
		failure := domain.ExecutionError{Code: defaultFailureCode, Message: "provider reported the external task failed"}
		if body.Error != nil {
			if body.Error.Code != "" {
				failure.Code = body.Error.Code
			}
			if body.Error.Message != "" {
				failure.Message = truncateUTF8(body.Error.Message, maxFailureMessageBytes)
			}
		}
		return registry.ToolResult{}, &registry.ProviderFailure{Err: failure}
	default:
		return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s carries status %q, want %s or %s", ToolName, state.ExternalTask.ExternalTaskID, body.Status, statusSucceeded, statusFailed)
	}
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

// dispatchError builds a dispatch failure from fixed labels only, so no request or
// response content can reach Trace or a log through it.
func dispatchError(op string, statusCode int, reason string) error {
	if statusCode != 0 {
		return fmt.Errorf("%s: dispatch to provider %q: %s (status %d): %w", ToolName, ProviderID, op, statusCode, errors.New(reason))
	}
	return fmt.Errorf("%s: dispatch to provider %q: %s: %w", ToolName, ProviderID, op, errors.New(reason))
}
