package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// listEnvelope wraps every list response in the {"items": [...]} shape the REST interface
// uses uniformly for definitions, node-types, models, tools and events.
type listEnvelope[T any] struct {
	Items []T `json:"items"`
}

// badRequestError marks an error that writeError maps to HTTP 400 VALIDATION_FAILED:
// malformed JSON, an unparsable path/query parameter, or another request-shape problem
// that never reaches a service method.
type badRequestError struct {
	message string
}

func (e *badRequestError) Error() string { return e.message }

// decodeJSON bounds r.Body to maxRequestBodyBytes and decodes it into dst. On failure it
// writes the appropriate error envelope itself (400 for malformed JSON, 413 for an
// oversized body) and reports false so the caller can simply return.
func decodeJSON(w http.ResponseWriter, logger *slog.Logger, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
				"request body exceeds the maximum allowed size", nil)
			return false
		}
		// The decode error itself is never surfaced: it can quote fragments of the
		// offending body, which may carry node config or other request content that
		// must not appear in an error response.
		writeError(w, logger, &badRequestError{message: "request body is not valid JSON"}, "")
		return false
	}
	return true
}

// writeJSON encodes body as the response, setting the status and content type first.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
