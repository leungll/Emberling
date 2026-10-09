// Package sandboxtest implements the `sandbox_test` Tool: the ASYNC dispatch of one patch
// test to the sandbox runner's POST /v1/tests. The runner applies the patch to a fixed
// base commit, runs the test suite in isolation and reports the outcome later through the
// shared callback endpoint, which resumes the same Tool Attempt.
//
// It performs exactly one registered operation. It owns no retry, timeout, callback
// routing, state transition or Event: a failed dispatch is reported upward as an error and
// the Agent Runtime decides what happens next.
//
// The one-time plaintext callback token travels in the request body and nowhere else, this
// package logs nothing, and its errors carry only the stable Provider identifier, a short
// operation label and an HTTP status code - never the token, the callback URL, the patch
// or a response body.
package sandboxtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "sandbox_test"

// ProviderID is the stable identifier of the sandbox runner this Tool dispatches to. It is
// persisted in the Callback Binding and shown in Trace; it is not a credential.
const ProviderID = "sandboxrunner"

// FactType is the fact a completed test records: the patch, identified by its digest, was
// tested against a base commit, with a pass or fail verdict.
const FactType = "patch_tested"

const testsPath = "/v1/tests"

// defaultTimeout bounds one dispatch when no HTTP client is injected. The Agent deadline
// still arrives through ctx and wins whenever it is shorter.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds the dispatch response this Tool reads.
const maxResponseBytes = 64 << 10

// Callback payload statuses the runner reports.
const (
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// defaultFailureCode is used when the runner reports FAILED without naming a code.
const defaultFailureCode = "SANDBOX_TEST_FAILED"

// maxFailureMessageBytes bounds the runner-supplied failure message, which reaches the
// Tool Attempt's error and Trace.
const maxFailureMessageBytes = 512

const inputSchema = `{
  "type": "object",
  "properties": {
    "baseCommit": {"type": "string", "minLength": 1},
    "patch": {"type": "string", "minLength": 1},
    "mock": {"type": "string"}
  },
  "required": ["baseCommit", "patch"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "passed": {"type": "boolean"},
    "baseCommit": {"type": "string", "minLength": 1},
    "patchDigest": {"type": "string", "minLength": 1},
    "resultDigest": {"type": "string", "minLength": 1},
    "summary": {
      "type": "object",
      "properties": {
        "total": {"type": "integer", "minimum": 0},
        "failed": {"type": "integer", "minimum": 0}
      },
      "required": ["total", "failed"],
      "additionalProperties": false
    }
  },
  "required": ["passed", "baseCommit", "patchDigest", "resultDigest", "summary"],
  "additionalProperties": false
}`

// Registration returns the `sandbox_test` ToolRegistration dispatching to the sandbox
// runner at baseURL. A nil client means a default client bounded by defaultTimeout.
//
// The dispatch starts an external test run, so the call has an EXTERNAL side effect with
// UNKNOWN idempotency. A completed test records a `patch_tested` fact about the patch
// digest, bound to the base commit and the result digest, whose verdict is the pass flag.
// Testing a patch generates nothing, so it does not count toward the generation limit.
func Registration(baseURL string, client *http.Client) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Description:   "Apply a patch to a base commit and run its tests in an isolated sandbox",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionAsync,
			Produces: &domain.FactProduction{
				FactType:       FactType,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/patchDigest"},
				BindArguments: map[string]domain.FactPointer{
					"baseCommit":   {Source: domain.FactPointerResult, Pointer: "/baseCommit"},
					"resultDigest": {Source: domain.FactPointerResult, Pointer: "/resultDigest"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"},
			},
			Requires:                    []domain.FactRequirement{},
			CountsTowardGenerationLimit: false,
		},
		Executor: New(baseURL, client),
	}
}

// Executor implements registry.AsyncToolExecutor for `sandbox_test`.
type Executor struct {
	baseURL    string
	httpClient *http.Client
}

// New returns an Executor dispatching to the sandbox runner at baseURL.
func New(baseURL string, client *http.Client) *Executor {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Executor{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

type arguments struct {
	BaseCommit string `json:"baseCommit"`
	Patch      string `json:"patch"`
	Mock       string `json:"mock"`
}

// testRequest is this Tool's view of the runner's POST /v1/tests body. It carries no
// image, mount or command: the runner decides how a test runs.
type testRequest struct {
	CallbackURL   string `json:"callbackUrl"`
	CallbackToken string `json:"callbackToken"`
	BaseCommit    string `json:"baseCommit"`
	Patch         string `json:"patch"`
	Mock          string `json:"mock,omitempty"`
}

type testResponse struct {
	TestID string `json:"testId"`
}

type summary struct {
	Total  int `json:"total"`
	Failed int `json:"failed"`
}

// result is the Tool's result, republished member by member from the callback.
type result struct {
	Passed       bool    `json:"passed"`
	BaseCommit   string  `json:"baseCommit"`
	PatchDigest  string  `json:"patchDigest"`
	ResultDigest string  `json:"resultDigest"`
	Summary      summary `json:"summary"`
}

// Execute dispatches one patch test and returns its external identity. Being an ASYNC Tool
// it returns a DISPATCHED result or an explicit error, never a completed result.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if action.Callback == nil || strings.TrimSpace(action.Callback.URL) == "" || strings.TrimSpace(action.Callback.Token) == "" {
		// Nothing has been sent, so no test can exist.
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "callback URL and token are both required")
	}
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.BaseCommit == "" || args.Patch == "" {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "arguments carry no baseCommit and patch")
	}

	body, err := json.Marshal(testRequest{
		CallbackURL:   action.Callback.URL,
		CallbackToken: action.Callback.Token,
		BaseCommit:    args.BaseCommit,
		Patch:         args.Patch,
		Mock:          args.Mock,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "request body could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+testsPath, bytes.NewReader(body))
	if err != nil {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "request could not be created")
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := e.httpClient.Do(request)
	if err != nil {
		// A transport error from net/http quotes the URL, so it is not wrapped.
		return registry.ToolExecutionResult{}, dispatchError("send request", 0, "transport failure")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusAccepted {
		// The body is not read into the error: a runner may echo the request, including
		// the callback token and the patch, inside its own error body.
		return registry.ToolExecutionResult{}, dispatchError("runner rejected the dispatch", response.StatusCode, "status is not 202 Accepted")
	}
	var accepted testResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&accepted); err != nil {
		return registry.ToolExecutionResult{}, dispatchError("decode response", response.StatusCode, "accepted response body is not a test response")
	}
	if accepted.TestID == "" {
		return registry.ToolExecutionResult{}, dispatchError("decode response", response.StatusCode, "accepted response carries no testId")
	}
	return registry.ToolExecutionResult{
		Kind:         registry.ToolResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: accepted.TestID},
	}, nil
}

// callbackPayload is the runner's callback body as this Tool reads it.
type callbackPayload struct {
	Status       string   `json:"status"`
	Passed       *bool    `json:"passed"`
	BaseCommit   string   `json:"baseCommit"`
	PatchDigest  string   `json:"patchDigest"`
	ResultDigest string   `json:"resultDigest"`
	Summary      *summary `json:"summary"`
	Error        *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// OnCallback converts one runner callback payload into the Tool result.
//
// A reported test-run failure becomes a *registry.ProviderFailure, which fails the Tool
// Attempt with the callback as its failure source. A failing test suite is not such a
// failure: it is a SUCCEEDED run whose verdict is false. A payload this Tool cannot
// interpret stays a plain error, since a malformed delivery is not evidence that the run
// failed. Only the declared result members are republished.
func (e *Executor) OnCallback(_ context.Context, state registry.ToolAsyncState, payload []byte) (registry.ToolResult, error) {
	testID := state.ExternalTask.ExternalTaskID
	var body callbackPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s is not a JSON object", ToolName, testID)
	}
	switch body.Status {
	case statusSucceeded:
		if body.Passed == nil || body.BaseCommit == "" || body.PatchDigest == "" || body.ResultDigest == "" || body.Summary == nil {
			return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s reports %s without passed, baseCommit, patchDigest, resultDigest and summary", ToolName, testID, statusSucceeded)
		}
		if body.Summary.Total < 0 || body.Summary.Failed < 0 {
			return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s reports a negative test count", ToolName, testID)
		}
		output, err := json.Marshal(result{
			Passed:       *body.Passed,
			BaseCommit:   body.BaseCommit,
			PatchDigest:  body.PatchDigest,
			ResultDigest: body.ResultDigest,
			Summary:      *body.Summary,
		})
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("%s: encode result for external task %s: %w", ToolName, testID, err)
		}
		return registry.ToolResult{Output: output}, nil
	case statusFailed:
		failure := domain.ExecutionError{Code: defaultFailureCode, Message: "sandbox runner reported the test run failed"}
		if body.Error != nil {
			if body.Error.Code != "" {
				failure.Code = truncateUTF8(body.Error.Code, maxFailureMessageBytes)
			}
			if body.Error.Message != "" {
				failure.Message = truncateUTF8(body.Error.Message, maxFailureMessageBytes)
			}
		}
		return registry.ToolResult{}, &registry.ProviderFailure{Err: failure}
	default:
		return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s carries status %q, want %s or %s", ToolName, testID, body.Status, statusSucceeded, statusFailed)
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
