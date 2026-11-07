// Package externaltask speaks the external task protocol over HTTP, one request per call,
// for the task service an external Tool or an asynchronous node dispatches to:
//
//	POST {base}/v1/tasks      submits one task; 202 Accepted creates it, 200 OK answers
//	                          a task the service already holds under the same id
//	GET  {base}/v1/tasks/{id} answers one status query
//
// The submit body is {"taskId", "type", "input", "callbackUrl", "callbackToken"}. The
// status body is {"taskId", "status", "result", "error"}, where status is QUEUED, RUNNING,
// SUCCEEDED or FAILED, result is the result object of a SUCCEEDED task and error is
// {"code", "message"} of a FAILED one. A finished task is also reported to callbackUrl
// with the same status, result and error members as the callback payload, so the caller
// interprets a callback and a terminal status answer with one function.
//
// The Client satisfies the narrow task port a Tool or node declares; it never retries,
// writes no Runtime state, emits no Event and selects no replacement service. The one-time
// plaintext callback token travels in the submit body and nowhere else, this package logs
// nothing, and its errors carry only the task id, a short operation label and an HTTP
// status code - never the token, the callback URL, the service URL, raw transport error
// text, the task input or a response body.
package externaltask

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/registry"
)

const tasksPath = "/v1/tasks"

// DefaultTimeout bounds one request when the caller injects no HTTP client of its own.
// The caller's context still ends a request sooner.
const DefaultTimeout = 30 * time.Second

// maxResponseBytes bounds the response body this client reads, so a misbehaving service
// cannot make one request consume unbounded memory. A status answer carries a task result,
// which is bounded well below this.
const maxResponseBytes = 1 << 20

// Task statuses the service reports.
const (
	StatusQueued    = "QUEUED"
	StatusRunning   = "RUNNING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
)

// Client submits tasks to, and queries, one task service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New returns a Client for the task service at baseURL. A nil httpClient means a default
// client bounded by DefaultTimeout.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
}

// submitRequest is the service's POST /v1/tasks body.
type submitRequest struct {
	TaskID        string          `json:"taskId"`
	Type          string          `json:"type"`
	Input         json.RawMessage `json:"input"`
	CallbackURL   string          `json:"callbackUrl"`
	CallbackToken string          `json:"callbackToken"`
}

// statusResponse is the service's answer to GET /v1/tasks/{id}, read only as far as this
// client classifies it; the body is handed back whole for the caller to interpret.
type statusResponse struct {
	TaskID string          `json:"taskId"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
}

// Submit sends one task and never retries. A 202 Accepted and a 200 OK are both success:
// the second is the service's answer that it already holds the task under this id, which
// is exactly the task the caller meant to send. Any other status, or no answer at all, is
// a *RequestError.
func (c *Client) Submit(ctx context.Context, taskID, taskType string, input json.RawMessage, callback registry.CallbackContext) error {
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(taskType) == "" {
		return &RequestError{TaskID: taskID, Op: "build request", reason: "task id and type are both required"}
	}
	if strings.TrimSpace(callback.URL) == "" || strings.TrimSpace(callback.Token) == "" {
		return &RequestError{TaskID: taskID, Op: "build request", reason: "callback URL and token are both required"}
	}
	if len(input) == 0 {
		input = json.RawMessage(`null`)
	}
	body, err := json.Marshal(submitRequest{TaskID: taskID, Type: taskType, Input: input, CallbackURL: callback.URL, CallbackToken: callback.Token})
	if err != nil {
		return &RequestError{TaskID: taskID, Op: "build request", reason: "request body could not be encoded"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+tasksPath, bytes.NewReader(body))
	if err != nil {
		return &RequestError{TaskID: taskID, Op: "build request", reason: "request could not be created"}
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		// A transport error from net/http quotes the URL, so it is not wrapped; the
		// caller's own context error is, so a cancelled dispatch stays recognisable.
		return &RequestError{TaskID: taskID, Op: "send request", reason: "no response from the task service", err: ctx.Err()}
	}
	defer drain(response)
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		// The body is not read into the error: a service may echo the request, including
		// the callback token, inside its own error body.
		return &RequestError{TaskID: taskID, Op: "service rejected the task", StatusCode: response.StatusCode, reason: "status is not 202 Accepted or 200 OK"}
	}
	return nil
}

// Get sends exactly one status query for taskID and normalises the answer. QUEUED and
// RUNNING are both RUNNING for the caller; SUCCEEDED and FAILED carry the whole status
// body, which holds the same status, result and error members as the task's callback
// payload. A 404 is the service's definite answer that it does not know the task and is
// UNKNOWN_TASK. Any other non-2xx status, an unreadable body, a body about another task,
// an unrecognised status or a SUCCEEDED task without a result is UNKNOWN with a nil error:
// it is not evidence of anything. Only a query that received no HTTP response at all
// returns an error (*RequestError).
func (c *Client) Get(ctx context.Context, taskID string) (registry.PollStatus, json.RawMessage, error) {
	if strings.TrimSpace(taskID) == "" {
		return "", nil, &RequestError{TaskID: taskID, Op: "build status request", reason: "task id is required"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+tasksPath+"/"+url.PathEscape(taskID), nil)
	if err != nil {
		return "", nil, &RequestError{TaskID: taskID, Op: "build status request", reason: "request could not be created"}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", nil, &RequestError{TaskID: taskID, Op: "send status request", reason: "no response from the task service", err: ctx.Err()}
	}
	defer drain(response)

	switch {
	case response.StatusCode == http.StatusNotFound:
		return registry.PollUnknownTask, nil, nil
	case response.StatusCode < 200 || response.StatusCode > 299:
		return registry.PollStatusUnknown, nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return registry.PollStatusUnknown, nil, nil
	}
	var answer statusResponse
	if err := json.Unmarshal(raw, &answer); err != nil || answer.TaskID != taskID {
		return registry.PollStatusUnknown, nil, nil
	}
	switch answer.Status {
	case StatusQueued, StatusRunning:
		return registry.PollRunning, nil, nil
	case StatusSucceeded:
		if !hasValue(answer.Result) {
			return registry.PollStatusUnknown, nil, nil
		}
		return registry.PollSucceeded, json.RawMessage(raw), nil
	case StatusFailed:
		return registry.PollFailed, json.RawMessage(raw), nil
	default:
		return registry.PollStatusUnknown, nil, nil
	}
}

// RequestError is a request the service rejected or never answered. It carries only the
// task id, the operation and the HTTP status when one arrived.
type RequestError struct {
	TaskID     string
	Op         string
	StatusCode int

	reason string
	err    error
}

func (e *RequestError) Error() string {
	msg := fmt.Sprintf("externaltask: %s", e.Op)
	if e.TaskID != "" {
		msg = fmt.Sprintf("externaltask: task %s: %s", e.TaskID, e.Op)
	}
	if e.StatusCode != 0 {
		msg += fmt.Sprintf(" (status %d)", e.StatusCode)
	}
	return msg + ": " + e.reason
}

// Unwrap exposes the caller's context error when it ended the request, and nothing else.
func (e *RequestError) Unwrap() error { return e.err }

func drain(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	_ = response.Body.Close()
}

// hasValue reports whether a decoded JSON member is present and not JSON null.
func hasValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}
