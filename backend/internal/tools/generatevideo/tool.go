// Package generatevideo implements the `generate_video` Tool: the ASYNC dispatch of one
// video generation task for a reviewed image asset to the deterministic Mock Provider's
// POST /v1/tasks. The Provider reports the video URL later through the shared callback
// endpoint, which resumes the same Tool Attempt.
//
// It performs exactly one registered operation. It owns no retry, timeout, callback
// routing, state transition or Event: a failed dispatch is reported upward as an error and
// the Agent Runtime decides what happens next.
//
// The one-time plaintext callback token travels in the request body and nowhere else, this
// package logs nothing, and its errors carry only the stable Provider identifier, a short
// operation label and an HTTP status code - never the token, the callback URL, the
// arguments or a response body.
package generatevideo

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
const ToolName = "generate_video"

// ProviderID is the stable identifier of the Provider this Tool dispatches to. It is
// persisted in the Callback Binding and shown in Trace; it is not a credential.
const ProviderID = "mock-task-provider-v1"

// reviewedFactType is the fact a video dispatch requires: a passing review of the same
// asset, made for the same photo.
const reviewedFactType = "asset_reviewed"

const tasksPath = "/v1/tasks"

// mediaVideo asks the Provider for a video task, whose success callback carries the URL of
// the generated video.
const mediaVideo = "video"

// defaultTimeout bounds one dispatch when no HTTP client is injected. The Agent deadline
// still arrives through ctx and wins whenever it is shorter.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds the dispatch response this Tool reads.
const maxResponseBytes = 64 << 10

// mockControlKey names the settings member that selects a Mock Provider scenario:
// "failed", "lost", "duplicate" or "delay:<ms>". Any other value, or none, asks for the
// immediate success callback.
const mockControlKey = "mock"

// Callback payload statuses the Provider reports.
const (
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// defaultFailureCode is used when the Provider reports FAILED without naming a code.
const defaultFailureCode = "PROVIDER_TASK_FAILED"

// maxFailureMessageBytes bounds the Provider-supplied failure message, which reaches the
// Tool Attempt's error and Trace.
const maxFailureMessageBytes = 512

const inputSchema = `{
  "type": "object",
  "properties": {
    "assetRef": {"type": "string", "minLength": 1},
    "photoAssetId": {"type": "string", "minLength": 1},
    "settings": {"type": "object"}
  },
  "required": ["assetRef", "photoAssetId", "settings"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "videoUrl": {"type": "string", "minLength": 1}
  },
  "required": ["videoUrl"],
  "additionalProperties": false
}`

// Registration returns the `generate_video` ToolRegistration dispatching to the Mock
// Provider at baseURL. A nil client means a default client bounded by defaultTimeout.
//
// The dispatch creates an external task, so the call has an EXTERNAL side effect with
// UNKNOWN idempotency: the Runtime hands an Agent Tool no idempotency key. The Action
// requires a committed `asset_reviewed` fact about the same assetRef, bound to the same
// photo, whose verdict passed; without it the Runtime refuses the Action before anything is
// dispatched.
func Registration(baseURL string, client *http.Client) registry.ToolRegistration {
	requirePassed := true
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Description:   "Generate a video from a reviewed image asset through an asynchronous Provider task",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionAsync,
			Requires: []domain.FactRequirement{{
				FactType:        reviewedFactType,
				SubjectArgument: "/assetRef",
				MatchBindings:   []string{"photoAssetId"},
				RequireVerdict:  &requirePassed,
			}},
		},
		Executor: New(baseURL, client),
	}
}

// Executor implements registry.AsyncToolExecutor for `generate_video`.
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
	AssetRef     string         `json:"assetRef"`
	PhotoAssetID string         `json:"photoAssetId"`
	Settings     map[string]any `json:"settings"`
}

// taskRequest is this Tool's view of the Mock Provider's POST /v1/tasks body. It sends no
// payload: for a video task the Provider supplies the success payload with the video URL.
type taskRequest struct {
	CallbackURL   string `json:"callbackUrl"`
	CallbackToken string `json:"callbackToken"`
	Media         string `json:"media"`
	Outcome       string `json:"outcome,omitempty"`
	DelayMs       any    `json:"delayMs,omitempty"`
}

type taskResponse struct {
	ExternalTaskID string `json:"externalTaskId"`
}

// result is the Tool's result.
type result struct {
	VideoURL string `json:"videoUrl"`
}

// Execute dispatches one video task and returns its external identity. Being an ASYNC Tool
// it returns a DISPATCHED result or an explicit error, never a completed result.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if action.Callback == nil || strings.TrimSpace(action.Callback.URL) == "" || strings.TrimSpace(action.Callback.Token) == "" {
		// Nothing has been sent, so no task can exist.
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "callback URL and token are both required")
	}
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.AssetRef == "" || args.PhotoAssetID == "" || args.Settings == nil {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "arguments carry no assetRef, photoAssetId and settings")
	}

	body, err := json.Marshal(buildRequest(args.Settings, *action.Callback))
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
		return registry.ToolExecutionResult{}, dispatchError("send request", 0, "transport failure")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		// The body is not read into the error: a Provider may echo the request, including
		// the callback token, inside its own error body.
		return registry.ToolExecutionResult{}, dispatchError("provider rejected the dispatch", response.StatusCode, "non-2xx status")
	}
	var accepted taskResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&accepted); err != nil {
		return registry.ToolExecutionResult{}, dispatchError("decode response", response.StatusCode, "accepted response body is not a task response")
	}
	if accepted.ExternalTaskID == "" {
		return registry.ToolExecutionResult{}, dispatchError("decode response", response.StatusCode, "accepted response carries no externalTaskId")
	}
	return registry.ToolExecutionResult{
		Kind:         registry.ToolResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: accepted.ExternalTaskID},
	}, nil
}

// buildRequest converts one call into the Provider's wire request, translating the
// settings' mock control into the Provider's scenario fields.
func buildRequest(settings map[string]any, callback registry.CallbackContext) taskRequest {
	request := taskRequest{CallbackURL: callback.URL, CallbackToken: callback.Token, Media: mediaVideo}
	control, _ := settings[mockControlKey].(string)
	name, argument, _ := strings.Cut(control, ":")
	switch name {
	case "failed":
		request.Outcome = name
	case "lost", "duplicate":
		request.DelayMs = name
	case "delay":
		if milliseconds, err := strconv.Atoi(argument); err == nil && milliseconds >= 0 {
			request.DelayMs = milliseconds
		}
	}
	return request
}

// callbackPayload is the Provider's callback body as this Tool reads it.
type callbackPayload struct {
	Status   string  `json:"status"`
	VideoURL *string `json:"videoUrl"`
	Error    *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// OnCallback converts one Provider callback payload into the Tool result.
//
// A reported task failure becomes a *registry.ProviderFailure, which fails the Tool
// Attempt with the callback as its failure source. A payload this Tool cannot interpret
// stays a plain error: a malformed delivery is not evidence that the external task failed.
// Only videoUrl is republished, so a Provider-private member never reaches the Agent
// Context.
func (e *Executor) OnCallback(_ context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
	var body callbackPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s is not a JSON object", ToolName, state.ExternalTask.ExternalTaskID)
	}
	switch body.Status {
	case statusSucceeded:
		if body.VideoURL == nil || *body.VideoURL == "" {
			return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s reports %s without videoUrl", ToolName, state.ExternalTask.ExternalTaskID, statusSucceeded)
		}
		output, err := json.Marshal(result{VideoURL: *body.VideoURL})
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
