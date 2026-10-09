// Package mocktask normalises the deterministic HTTP Mock Provider's (cmd/mockprovider)
// asynchronous task interactions, one request per call: POST /v1/tasks accepts one task
// whose result is reported later as a callback, and GET /v1/tasks/{externalTaskId}
// answers one status query for an accepted task.
//
// It converts Emberling's dispatch inputs into that Provider's wire format and the
// Provider's answer back into a registry.ExternalTask or a normalised poll status. It
// never retries, never writes Runtime state, never emits an Event and never selects a
// replacement Provider: a failed dispatch is reported upward as a DispatchError, a status
// query that got no HTTP response as a PollError, and the Execution Service decides what
// happens next.
//
// The one-time plaintext callback token travels in the dispatch body and nowhere else. This
// package logs nothing at all, and its errors carry only the stable Provider identifier,
// the external task id (when the Provider already named one), a short operation label and
// an HTTP status code - never the token, the callback URL, the prompt or a response body.
package mocktask

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ProviderID is the stable identifier of the Provider this Adapter talks to. It is
// persisted in the Callback Binding and shown in Trace; it is not a credential.
const ProviderID = "mock-task-provider-v1"

// tasksPath is the Mock Provider's asynchronous task endpoint.
const tasksPath = "/v1/tasks"

// imagesPath is the prefix of the Mock Provider's image route, which serves the bytes the
// EXTERNAL ImageRef in successPayload points at.
const imagesPath = "/v1/images"

// defaultTimeout bounds one dispatch when the caller injects no HTTP client of its own.
// An Attempt deadline, when the Runtime sets one, still arrives through ctx and wins
// whenever it is shorter.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds the response body this Adapter will read, so a misbehaving
// Provider cannot make one dispatch consume unbounded memory.
const maxResponseBytes = 64 << 10

// mediaType is the media type the Mock Provider's images are declared with. The Provider
// stores no bytes; the callback carries a reference, never binary content.
const mediaType = "image/png"

// directivePrefix marks a prompt as a scenario trigger for the Mock Provider instead of
// plain task content, mirroring mockmodel's `mock:` directives. The Mock Provider's
// /v1/tasks body has no prompt field of its own, so a directive in the prompt is the only
// way an integration test can reach its failure, duplicate and lost-callback scenarios.
const directivePrefix = "mock:"

// Adapter dispatches one task per call to the Mock Provider at baseURL.
type Adapter struct {
	baseURL    string
	httpClient *http.Client
}

// New returns an Adapter that talks to the Mock Provider at baseURL. A nil client means a
// default client bounded by defaultTimeout; callers that own a configured client (an
// httptest server's client, a deployment-wide client) inject it here.
func New(baseURL string, client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Adapter{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

// Dispatch sends one task to the Provider and returns the accepted external task identity.
//
// callback carries the Attempt-scoped callback URL and one-time plaintext token the
// Runtime issued; both are forwarded unchanged so the Provider can report back.
// idempotencyKey is forwarded verbatim and omitted entirely when empty: the Adapter never
// generates a key of its own. options is the node's frozen config, forwarded as
// the Provider's task options.
//
// reference is the optional Reference Image, forwarded as the credential-free
// domain.ImageRef the `reference` port carried and omitted entirely when absent. The
// Adapter never resolves it: it fetches no Asset content, mints no signed URL and reads
// no storage key, because an ImageRef is already the stable reference a Provider is
// given.
func (a *Adapter) Dispatch(ctx context.Context, prompt string, reference *domain.ImageRef, options map[string]any, callback registry.CallbackContext, idempotencyKey string) (registry.ExternalTask, error) {
	if strings.TrimSpace(callback.URL) == "" || strings.TrimSpace(callback.Token) == "" {
		// Nothing has been sent, so the Provider cannot have accepted a task: definite.
		return registry.ExternalTask{}, &DispatchError{
			ProviderID: ProviderID,
			Op:         "build request",
			err:        errors.New("callback URL and token are both required for an asynchronous dispatch"),
		}
	}

	body, err := json.Marshal(a.buildRequest(prompt, reference, options, callback, idempotencyKey))
	if err != nil {
		return registry.ExternalTask{}, &DispatchError{ProviderID: ProviderID, Op: "build request", err: errors.New("request body could not be encoded")}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+tasksPath, bytes.NewReader(body))
	if err != nil {
		return registry.ExternalTask{}, &DispatchError{ProviderID: ProviderID, Op: "build request", err: err}
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := a.httpClient.Do(request)
	if err != nil {
		return registry.ExternalTask{}, &DispatchError{ProviderID: ProviderID, Op: "send request", uncertain: sendFailureIsUncertain(err), err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		// The body is deliberately not read into the error: a Provider may echo the
		// request - including the callback token - back inside its own error body.
		return registry.ExternalTask{}, &DispatchError{
			ProviderID: ProviderID,
			Op:         "provider rejected the dispatch",
			StatusCode: response.StatusCode,
			uncertain:  statusIsUncertain(response.StatusCode),
			err:        errors.New("provider returned a non-2xx status"),
		}
	}

	var accepted taskResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&accepted); err != nil {
		// The Provider answered 2xx, so the task exists; its identity is what was lost.
		return registry.ExternalTask{}, &DispatchError{
			ProviderID: ProviderID,
			Op:         "decode response",
			StatusCode: response.StatusCode,
			uncertain:  true,
			err:        errors.New("accepted response body is not a task response"),
		}
	}
	if accepted.ExternalTaskID == "" {
		return registry.ExternalTask{}, &DispatchError{
			ProviderID: ProviderID,
			Op:         "decode response",
			StatusCode: response.StatusCode,
			uncertain:  true,
			err:        errors.New("accepted response carries no externalTaskId"),
		}
	}

	return registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: accepted.ExternalTaskID}, nil
}

// buildRequest converts the dispatch inputs into the Mock Provider's wire request. A
// `mock:` directive in prompt selects one of the Provider's own scenarios; any other
// prompt asks for the deterministic success payload, which is what carries the image
// reference back in the callback.
func (a *Adapter) buildRequest(prompt string, reference *domain.ImageRef, options map[string]any, callback registry.CallbackContext, idempotencyKey string) taskRequest {
	request := taskRequest{
		CallbackURL:    callback.URL,
		CallbackToken:  callback.Token,
		IdempotencyKey: idempotencyKey,
		Prompt:         prompt,
		Reference:      reference,
		Options:        options,
	}

	directive, isDirective := strings.CutPrefix(prompt, directivePrefix)
	if !isDirective {
		request.Payload = successPayload(a.baseURL, prompt, options)
		return request
	}

	name, argument, _ := strings.Cut(directive, ":")
	switch name {
	case "failed", "wrong-token":
		// The Provider's own outcome payload is used; an explicit payload would override
		// it (cmd/mockprovider: callbackPayload).
		request.Outcome = name
	case "lost", "duplicate":
		request.DelayMs = name
		request.Payload = successPayload(a.baseURL, prompt, options)
	case "delay":
		if milliseconds, err := strconv.Atoi(argument); err == nil && milliseconds >= 0 {
			request.DelayMs = milliseconds
		}
		request.Payload = successPayload(a.baseURL, prompt, options)
	default:
		request.Payload = successPayload(a.baseURL, prompt, options)
	}
	return request
}

// successPayload is the callback payload this Adapter asks the Provider to deliver on
// success. The Mock Provider echoes an explicit payload verbatim, so this is how a
// deterministic image reference reaches the node's OnCallback.
//
// The reference is a domain.ImageRef on the EXTERNAL branch, which is what the data model
// allows the MVP Mock Provider to report: `uri` addresses that Provider's own GET
// /v1/images/{name}.png route, so it is stable, carries no credential and stays readable
// for as long as the Provider serves. No binary content is produced or stored here; the
// name is derived from the prompt, so a replayed dispatch reports the same image.
//
// height is deliberately absent: the Provider is told a requested width only, and an
// ImageRef dimension that was never reported must not be invented.
func successPayload(baseURL, prompt string, options map[string]any) json.RawMessage {
	sum := sha256.Sum256([]byte(prompt))
	image := domain.ImageRef{
		Source:    domain.ImageSourceExternal,
		URI:       fmt.Sprintf("%s%s/%s.png", baseURL, imagesPath, hex.EncodeToString(sum[:8])),
		MediaType: mediaType,
	}
	if width, ok := intOption(options, "width"); ok && width > 0 {
		image.Width = width
	}
	if err := image.Validate(); err != nil {
		// A reference this Adapter cannot vouch for is not sent at all: the Provider then
		// delivers its own default body, which the node rejects as uninterpretable rather
		// than publishing something that is not an ImageRef.
		return nil
	}
	encoded, err := json.Marshal(map[string]any{"status": "SUCCEEDED", "image": image})
	if err != nil {
		return nil
	}
	return encoded
}

// intOption reads one integer option, accepting the float64 a JSON-decoded config carries
// as well as a Go int.
func intOption(options map[string]any, name string) (int, bool) {
	switch value := options[name].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

// taskRequest is this Adapter's view of the Mock Provider's POST /v1/tasks body. Prompt,
// Reference and Options are not part of that Provider's contract - it ignores unknown
// fields - but a real image Provider needs all three, so they are sent rather than
// silently dropped. Reference is a pointer so an absent Reference Image leaves the member
// out entirely instead of sending a null the Provider would have to interpret.
type taskRequest struct {
	CallbackURL    string           `json:"callbackUrl"`
	CallbackToken  string           `json:"callbackToken"`
	IdempotencyKey string           `json:"idempotencyKey,omitempty"`
	Prompt         string           `json:"prompt,omitempty"`
	Reference      *domain.ImageRef `json:"reference,omitempty"`
	Options        map[string]any   `json:"options,omitempty"`
	Payload        json.RawMessage  `json:"payload,omitempty"`
	Outcome        string           `json:"outcome,omitempty"`
	DelayMs        any              `json:"delayMs,omitempty"`
}

// taskResponse is the Mock Provider's synchronous answer to an accepted dispatch.
type taskResponse struct {
	ExternalTaskID string `json:"externalTaskId"`
}

// DispatchError is one failed dispatch. Uncertain reports whether the external task may
// still have been created: the Execution Service needs that fact for FailNode.Uncertain,
// because an uncertain external result forbids an automatic retry unless the registered
// SideEffectPolicy allows it.
//
// It is matched with errors.As - directly, or through the narrow
// `interface{ Uncertain() bool }` - so no caller has to import this package to read the
// classification.
type DispatchError struct {
	ProviderID string
	// ExternalTaskID is set only when the Provider already named the task.
	ExternalTaskID string
	// StatusCode is 0 when no HTTP response arrived.
	StatusCode int
	// Op is a short, stable label of the step that failed.
	Op string

	uncertain bool
	err       error
}

func (e *DispatchError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mocktask: dispatch to provider %q: %s", e.ProviderID, e.Op)
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " (status %d)", e.StatusCode)
	}
	if e.ExternalTaskID != "" {
		fmt.Fprintf(&b, " (externalTaskId %s)", e.ExternalTaskID)
	}
	if e.err != nil {
		fmt.Fprintf(&b, ": %v", e.err)
	}
	return b.String()
}

func (e *DispatchError) Unwrap() error { return e.err }

// Uncertain reports whether the Provider may have accepted the task despite this failure.
func (e *DispatchError) Uncertain() bool { return e.uncertain }

// sendFailureIsUncertain classifies a transport failure. A dial failure proves no byte of
// the request reached the Provider, so no task can exist. Every other transport failure -
// a timeout, a reset, an unexpected EOF - may have happened after the request was written,
// so the task may exist and the outcome is uncertain.
func sendFailureIsUncertain(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return false
	}
	return true
}

// statusIsUncertain classifies a non-2xx response. A 5xx may be reported after the
// Provider already accepted the task internally; a 3xx or 4xx means the request was never
// processed as a task at all.
func statusIsUncertain(statusCode int) bool {
	return statusCode >= 500
}
