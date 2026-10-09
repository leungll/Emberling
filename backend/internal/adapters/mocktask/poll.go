package mocktask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// taskStatusResponse is the Mock Provider's answer to GET /v1/tasks/{externalTaskId}.
// Payload is present only once the task is terminal and is the same payload the task's
// callback carries.
type taskStatusResponse struct {
	ExternalTaskID string          `json:"externalTaskId"`
	Status         string          `json:"status"`
	Payload        json.RawMessage `json:"payload"`
}

// PollError is a status query that never produced an HTTP response, such as a refused
// connection, a timeout or a cancelled context. The Adapter does not retry it; the caller
// decides what the missing answer means. It carries only the Provider identifier, the
// external task id and the transport error.
type PollError struct {
	ProviderID     string
	ExternalTaskID string

	err error
}

func (e *PollError) Error() string {
	return fmt.Sprintf("mocktask: poll provider %q for externalTaskId %s: %v", e.ProviderID, e.ExternalTaskID, e.err)
}

func (e *PollError) Unwrap() error { return e.err }

// Poll sends exactly one status query for externalTaskID and normalises the answer.
//
// RUNNING carries no payload. SUCCEEDED and FAILED carry the Provider's terminal payload,
// which is the same body the task's callback delivers, so the node interprets both paths
// with one function. Every answer that names no interpretable task state - a 404 for a
// task the Provider does not know, any other non-2xx status, an unreadable body, a body
// about another task, an unrecognised status or a terminal status without a payload -
// is PollStatusUnknown with a nil error: it is not evidence that the task failed.
//
// Only a query that never received an HTTP response returns an error (*PollError). The
// request carries no callback URL, callback token or Authorization header: a status
// query is identified by the external task id alone.
func (a *Adapter) Poll(ctx context.Context, externalTaskID string) (registry.PollStatus, json.RawMessage, error) {
	if strings.TrimSpace(externalTaskID) == "" {
		return "", nil, &PollError{ProviderID: ProviderID, err: errors.New("externalTaskId is required")}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+tasksPath+"/"+url.PathEscape(externalTaskID), nil)
	if err != nil {
		return "", nil, &PollError{ProviderID: ProviderID, ExternalTaskID: externalTaskID, err: err}
	}

	response, err := a.httpClient.Do(request)
	if err != nil {
		return "", nil, &PollError{ProviderID: ProviderID, ExternalTaskID: externalTaskID, err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return registry.PollStatusUnknown, nil, nil
	}

	var answer taskStatusResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&answer); err != nil {
		return registry.PollStatusUnknown, nil, nil
	}
	if answer.ExternalTaskID != externalTaskID {
		return registry.PollStatusUnknown, nil, nil
	}

	switch answer.Status {
	case string(registry.PollRunning):
		return registry.PollRunning, nil, nil
	case string(registry.PollSucceeded), string(registry.PollFailed):
		if !hasPayload(answer.Payload) {
			return registry.PollStatusUnknown, nil, nil
		}
		return registry.PollStatus(answer.Status), answer.Payload, nil
	default:
		return registry.PollStatusUnknown, nil, nil
	}
}

// hasPayload reports whether a decoded payload member is present and not JSON null.
func hasPayload(payload json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(payload))
	return trimmed != "" && trimmed != "null"
}
