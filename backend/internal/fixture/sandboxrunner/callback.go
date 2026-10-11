package sandboxrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// maxCallbackResponseBytes bounds how much of a callback response is drained.
const maxCallbackResponseBytes = 64 << 10

// callbackBody is the delivery body the Emberling callback endpoint accepts.
type callbackBody struct {
	ExternalTaskID string          `json:"externalTaskId"`
	Payload        json.RawMessage `json:"payload"`
}

// deliver performs one callback delivery attempt: with test controls it is first recorded
// and possibly held by the barrier, then sent once, then its outcome is recorded. It never
// retries.
func (s *Server) deliver(ctx context.Context, testID string, destination callbackDestination, payload json.RawMessage, attempt int) {
	if err := s.admitCallback(ctx, testID, attempt); err != nil {
		return
	}
	httpStatus := send(ctx, s.client, testID, destination, payload)
	s.recordCallback(testID, payloadStatus(payload), attempt, httpStatus)
}

// payloadStatus returns the status member of a payload this runner built.
func payloadStatus(payload json.RawMessage) string {
	var head struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(payload, &head)
	return head.Status
}

// send POSTs one callback and returns the receiver's HTTP status, or 0 when no response
// arrived. The receiver's answer is not interpreted: a rejection is a completed attempt
// with a defined answer, and the runner never retries on its own either way. Errors are not
// returned because a transport error quotes the callback URL.
func send(ctx context.Context, client *http.Client, testID string, destination callbackDestination, payload json.RawMessage) int {
	body, err := json.Marshal(callbackBody{ExternalTaskID: testID, Payload: payload})
	if err != nil {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.URL, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(callbackTokenHeader, destination.Token)
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCallbackResponseBytes))
	return resp.StatusCode
}

// validCallbackURL reports whether raw is an absolute http or https URL with a host.
func validCallbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// callbackTarget returns raw without userinfo, query or fragment, for the record: an
// operator can see where a callback goes without the record holding anything a caller
// might have put in those parts.
func callbackTarget(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path}).String()
}
