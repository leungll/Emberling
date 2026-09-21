// Package mockprovider is the deterministic HTTP Mock Provider used for integration and
// failure tests (07 §1 "Mock external systems through the deterministic Mock Provider").
// It stands in for a real external model/tool Provider: it never calls a live third-party
// endpoint, and it never reaches into this repository's internal Runtime packages
// (registry, domain, adapters) — a real external Provider would not share Go types with
// Emberling either, so this package owns its own minimal request/response shapes. It is
// served as a standalone process by cmd/mockprovider and in-process (httptest.Server) by
// the contract tests that drive a real dispatch/callback round trip.
//
// POST /v1/text/generate selects a synchronous scenario through `scenario`: "final"
// (default), "tool-call", "invalid-decision" or "fail".
//
// POST /v1/tasks selects an asynchronous scenario through three independent fields:
//
//	delayMs:        0 (default, immediate) | <milliseconds> | "lost" | "duplicate" | "beforeResponse"
//	outcome:        "succeeded" (default) | "failed" | "wrong-token"
//	idempotencyKey: optional; replaying a key returns the first task's externalTaskId
//	                with HTTP 200 and schedules no further callback
//
// delayMs decides when and how often the callback is delivered, outcome decides what it
// contains, and idempotencyKey decides whether a repeated dispatch creates a new task at
// all. An explicit `payload` is still forwarded verbatim and overrides outcome's default
// payload.
//
// GET /v1/images/{name}.png serves one fixed, deterministic PNG under any name, with no
// credential of any kind. It is what makes an image reference in a task callback payload
// genuinely readable instead of a URL that resolves to nothing.
package mockprovider

import (
	"encoding/json"
	"fmt"
	"time"
)

// delayMode selects how handleTasks' Dispatcher delivers the scheduled callback.
type delayMode int

const (
	// delayImmediate delivers the callback once, with no artificial delay. It is the
	// zero value so an absent delayMs field defaults to it.
	delayImmediate delayMode = iota
	// delayAfter delivers the callback once, after Duration has elapsed.
	delayAfter
	// delayLost never delivers the callback, simulating a Provider that accepted a task
	// but never reports back.
	delayLost
	// delayDuplicate delivers the callback twice with no delay, simulating a Provider
	// retrying its own callback delivery.
	delayDuplicate
	// delayBeforeResponse delivers the callback synchronously before /v1/tasks' own HTTP
	// response is written, so a test can observe callback-before-dispatch-ack ordering.
	delayBeforeResponse
)

// delaySpec is the parsed form of the request body's `delayMs` field. That field accepts
// either a non-negative integer number of milliseconds (0 meaning delayImmediate) or one
// of three sentinel strings ("lost", "duplicate", "beforeResponse") naming a scenario that
// cannot be expressed as a duration.
type delaySpec struct {
	Mode     delayMode
	Duration time.Duration
}

// MarshalJSON is the inverse of UnmarshalJSON, so a delaySpec built in Go (as this
// package's own tests do) round-trips to the same wire format real callers send.
func (d delaySpec) MarshalJSON() ([]byte, error) {
	switch d.Mode {
	case delayLost:
		return json.Marshal("lost")
	case delayDuplicate:
		return json.Marshal("duplicate")
	case delayBeforeResponse:
		return json.Marshal("beforeResponse")
	case delayAfter:
		return json.Marshal(d.Duration.Milliseconds())
	default: // delayImmediate
		return json.Marshal(0)
	}
}

// UnmarshalJSON accepts a JSON number or one of the sentinel strings. An absent field
// leaves the zero value (delayImmediate, 0), so callers may omit delayMs entirely.
func (d *delaySpec) UnmarshalJSON(data []byte) error {
	var sentinel string
	if err := json.Unmarshal(data, &sentinel); err == nil {
		switch sentinel {
		case "lost":
			*d = delaySpec{Mode: delayLost}
		case "duplicate":
			*d = delaySpec{Mode: delayDuplicate}
		case "beforeResponse":
			*d = delaySpec{Mode: delayBeforeResponse}
		default:
			return fmt.Errorf("mockprovider: delayMs %q is not one of \"lost\", \"duplicate\", \"beforeResponse\"", sentinel)
		}
		return nil
	}

	var milliseconds int64
	if err := json.Unmarshal(data, &milliseconds); err != nil {
		return fmt.Errorf("mockprovider: delayMs must be a non-negative integer of milliseconds or one of \"lost\", \"duplicate\", \"beforeResponse\": %w", err)
	}
	if milliseconds < 0 {
		return fmt.Errorf("mockprovider: delayMs must be >= 0, got %d", milliseconds)
	}
	if milliseconds == 0 {
		*d = delaySpec{Mode: delayImmediate}
		return nil
	}
	*d = delaySpec{Mode: delayAfter, Duration: time.Duration(milliseconds) * time.Millisecond}
	return nil
}

// outcome selects what the scheduled callback reports. It is orthogonal to delaySpec:
// delayMs decides when (and how often) a callback is delivered, outcome decides what that
// delivery contains.
type outcome int

const (
	// outcomeSucceeded delivers a normal callback. It is the zero value, so an absent
	// outcome field keeps the pre-existing behaviour.
	outcomeSucceeded outcome = iota
	// outcomeFailed delivers a well-formed callback whose payload reports that the
	// Provider's own task failed. The delivery itself still succeeds and is authenticated
	// with the real callback token: this is a Provider-reported failure, not a transport
	// or credential failure.
	outcomeFailed
	// outcomeWrongToken delivers the callback with a deliberately mismatched
	// X-Emberling-Callback-Token, so a test can exercise 08 §4's 401 rule (token invalid:
	// payload not saved, no recovery use case entered).
	outcomeWrongToken
)

// outcomeSucceededName and friends are the wire spellings of outcome.
const (
	outcomeSucceededName  = "succeeded"
	outcomeFailedName     = "failed"
	outcomeWrongTokenName = "wrong-token"
)

// MarshalJSON keeps a Go-built taskRequest (as this package's own tests build) on the same
// wire format real callers send.
func (o outcome) MarshalJSON() ([]byte, error) {
	switch o {
	case outcomeFailed:
		return json.Marshal(outcomeFailedName)
	case outcomeWrongToken:
		return json.Marshal(outcomeWrongTokenName)
	default: // outcomeSucceeded
		return json.Marshal(outcomeSucceededName)
	}
}

// UnmarshalJSON accepts one of the three sentinel strings. An absent field leaves the zero
// value (outcomeSucceeded), so callers may omit outcome entirely.
func (o *outcome) UnmarshalJSON(data []byte) error {
	var sentinel string
	if err := json.Unmarshal(data, &sentinel); err != nil {
		return fmt.Errorf("mockprovider: outcome must be one of %q, %q, %q: %w", outcomeSucceededName, outcomeFailedName, outcomeWrongTokenName, err)
	}
	switch sentinel {
	case outcomeSucceededName:
		*o = outcomeSucceeded
	case outcomeFailedName:
		*o = outcomeFailed
	case outcomeWrongTokenName:
		*o = outcomeWrongToken
	default:
		return fmt.Errorf("mockprovider: outcome %q is not one of %q, %q, %q", sentinel, outcomeSucceededName, outcomeFailedName, outcomeWrongTokenName)
	}
	return nil
}

// failedPayload is the callback payload delivered for outcome "failed". 08 §4 fixes the
// callback envelope (`externalTaskId` plus an opaque `payload`) but prescribes no shape for
// the payload itself, so this package defines one, deterministic and stable enough for a
// caller's AsyncNodeExecutor to branch on.
var failedPayload = json.RawMessage(
	`{"status":"FAILED","error":{"code":"PROVIDER_TASK_FAILED","message":"mock provider: task failed by scenario"}}`,
)

// succeededPayload is the default callback payload for outcome "succeeded" when the caller
// supplies no payload of its own. It carries the same `status` discriminator as
// failedPayload so a callback can be classified without inspecting anything else.
var succeededPayload = json.RawMessage(`{"status":"SUCCEEDED"}`)

// wrongTokenSuffix is appended to the real callback token for outcome "wrong-token". The
// real token is never logged or echoed anywhere; the suffix only guarantees the delivered
// value differs from the credential Emberling issued.
const wrongTokenSuffix = "-wrong"

// taskRequest is the body of POST /v1/tasks: a request to schedule one asynchronous
// callback, mirroring how a real external Provider accepts a task and reports back later.
type taskRequest struct {
	// ExternalTaskID is used verbatim when set; handleTasks generates one otherwise. A
	// caller supplies it to test callback matching against a task ID it already knows.
	ExternalTaskID string `json:"externalTaskId,omitempty"`
	// CallbackURL and CallbackToken are required: they are exactly what a real Provider
	// receives from Emberling at dispatch time (08 §4) and must echo back unmodified.
	CallbackURL   string          `json:"callbackUrl"`
	CallbackToken string          `json:"callbackToken"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	DelayMs       delaySpec       `json:"delayMs,omitempty"`
	// Outcome selects what the callback reports; an absent field means "succeeded". A
	// caller-supplied Payload always wins over the outcome's own default payload, so
	// payload pass-through keeps working unchanged.
	Outcome outcome `json:"outcome,omitempty"`
	// IdempotencyKey, when set, deduplicates retried dispatches of the same logical task
	// (06 §3: an EXTERNAL+KEYED retry is only allowed while it reuses the same Provider
	// idempotency key). A replayed key returns the first task's externalTaskId and
	// schedules no further callback. Keys are scoped to one server instance.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// taskResponse is the synchronous 202 response to POST /v1/tasks.
type taskResponse struct {
	ExternalTaskID string `json:"externalTaskId"`
}

// callbackBody is the JSON body mockprovider POSTs to CallbackURL. Its shape is fixed by
// 08 §4's Callback Contract: `externalTaskId` plus an opaque `payload`.
type callbackBody struct {
	ExternalTaskID string          `json:"externalTaskId"`
	Payload        json.RawMessage `json:"payload"`
}

// generateRequest is the body of POST /v1/text/generate: a minimal, HTTP-native
// generation request independent of this repository's internal registry.ModelRequest
// shape, since a real external Provider would define its own wire format.
type generateRequest struct {
	Prompt string `json:"prompt"`
	// Scenario selects a deterministic outcome; "" defaults to "final". Recognised
	// values are "final", "fail", "tool-call" and "invalid-decision".
	Scenario string `json:"scenario,omitempty"`
	// ToolName is used only when Scenario is "tool-call".
	ToolName string `json:"toolName,omitempty"`
}

// decision mirrors the fixed Decision envelope's two kinds (07 §1.4) in this package's own
// wire format: FINAL carries Output, TOOL_CALL carries ToolName and Arguments.
type decision struct {
	Kind      string          `json:"kind"`
	Output    string          `json:"output,omitempty"`
	ToolName  string          `json:"toolName,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// generateResponse is the body of a successful POST /v1/text/generate response.
type generateResponse struct {
	Decision decision `json:"decision"`
}

// errorResponse is the body of every non-2xx response this package returns.
type errorResponse struct {
	Error string `json:"error"`
}
