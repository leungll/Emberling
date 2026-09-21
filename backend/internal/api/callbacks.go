package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/leungll/Emberling/backend/internal/service"
)

// callbackTokenHeader carries the Attempt-scoped credential the Runtime issued before
// dispatch (docs/08-interface-spec.md §4). It is read once, forwarded to
// service.HandleCallback for verification, and never logged or echoed back.
const callbackTokenHeader = "X-Emberling-Callback-Token"

// callbackRequest is the wire shape of POST /api/callbacks (08 §4).
type callbackRequest struct {
	ExternalTaskID string          `json:"externalTaskId"`
	Payload        json.RawMessage `json:"payload"`
}

// callbackResponse is the single confirmation body every accepted callback answers with
// (08 §4): Accepted is always true for a non-error response, Pending marks an early
// delivery stored before its Callback Binding committed, and Duplicate marks a
// re-delivery, late delivery or delivery for a superseded Attempt.
type callbackResponse struct {
	Accepted  bool `json:"accepted"`
	Pending   bool `json:"pending"`
	Duplicate bool `json:"duplicate"`
}

// handleCallback serves POST /api/callbacks, the single entry point Node and Tool
// Attempt dispatches share (08 §4). It never queries a repository or advances an
// execution itself: every routing and state decision is service.ExecutionService's
// HandleCallback/ResumeNode use case.
//
// Response mapping: a missing or invalid credential answers 401 before the body is even
// read; a malformed body answers 400; an oversized body answers 413; a payload the
// registered Executor cannot interpret is a deliberate no-op and still answers 200 (the
// NodeRun stays WAITING_CALLBACK); every other accepted delivery answers 200 or 202 per
// CallbackOutcome. No branch below ever includes the token, its hash or the payload in
// the response or in a log.
func (d Deps) handleCallback(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get(callbackTokenHeader)
	if strings.TrimSpace(token) == "" {
		writeErrorEnvelope(w, http.StatusUnauthorized, codeInvalidCallbackCredential,
			"callback credential is missing", nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, int64(d.CallbackMaxPayloadBytes))
	var req callbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
				"callback payload exceeds the maximum allowed size", nil)
			return
		}
		// The decode error itself is never surfaced: a malformed body can carry the
		// caller's own echoed token or other content that must not appear in a response.
		writeErrorEnvelope(w, http.StatusBadRequest, codeInvalidCallbackPayload,
			"callback body is not valid JSON", nil)
		return
	}
	if strings.TrimSpace(req.ExternalTaskID) == "" {
		writeErrorEnvelope(w, http.StatusBadRequest, codeInvalidCallbackPayload,
			"externalTaskId is required", nil)
		return
	}
	if len(req.Payload) == 0 {
		writeErrorEnvelope(w, http.StatusBadRequest, codeInvalidCallbackPayload,
			"payload is required", nil)
		return
	}

	outcome, err := d.Execution.HandleCallback(r.Context(), service.HandleCallback{
		Token:          token,
		ExternalTaskID: req.ExternalTaskID,
		Payload:        req.Payload,
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidCallbackCredential) {
			writeErrorEnvelope(w, http.StatusUnauthorized, codeInvalidCallbackCredential,
				"callback credential is invalid or expired", nil)
			return
		}
		var rejected *service.CallbackPayloadRejectedError
		if errors.As(err, &rejected) {
			// 06 §1.6: the registered Executor could not interpret this payload. This is
			// deliberately not a state change -- the NodeRun stays WAITING_CALLBACK -- so
			// the Provider still gets an idempotent-looking 200 rather than a retry signal
			// that would only reproduce the same unparseable body.
			writeJSON(w, http.StatusOK, callbackResponse{Accepted: true})
			return
		}
		writeError(w, d.Logger, err, "")
		return
	}

	status := http.StatusOK
	if outcome.Pending {
		status = http.StatusAccepted
	}
	writeJSON(w, status, callbackResponse{
		Accepted:  true,
		Pending:   outcome.Pending,
		Duplicate: outcome.Duplicate,
	})
}
