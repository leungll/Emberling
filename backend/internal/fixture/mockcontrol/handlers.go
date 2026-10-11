package mockcontrol

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// maxControlBodyBytes bounds a control request body; the largest valid one is a short
// JSON object naming request kinds.
const maxControlBodyBytes = 4 << 10

// RecordTruncatedHeader is set on a record response that left later lines out; the caller
// pages forward with ?after=<last seq it received>.
const RecordTruncatedHeader = "X-Mockprovider-Record-Truncated"

// pauseRequest is the optional body of a pause request.
type pauseRequest struct {
	// Kinds limits the barrier to the named pausable kinds; empty holds every kind.
	Kinds []string `json:"kinds,omitempty"`
}

// barrierResponse is the body of every barrier control response.
type barrierResponse struct {
	Paused   bool          `json:"paused"`
	Kinds    []string      `json:"kinds"`
	Held     []HeldRequest `json:"held"`
	Released int           `json:"released,omitempty"`
}

// errorResponse is the body of every non-2xx control response.
type errorResponse struct {
	Error string `json:"error"`
}

// PauseHandler arms b, optionally for the kinds a JSON body {"kinds":[...]} names, and
// answers with the barrier state. A kind b was not built to pause answers 400.
func PauseHandler(b *Barrier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req pauseRequest
		body, err := io.ReadAll(io.LimitReader(r.Body, maxControlBodyBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body could not be read"})
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body is not valid JSON"})
				return
			}
		}
		for _, kind := range req.Kinds {
			if !slices.Contains(b.pausable, kind) {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "kinds may contain only " + quotedList(b.pausable)})
				return
			}
		}
		b.pause(req.Kinds)
		writeBarrier(w, b, 0)
	})
}

// ReleaseHandler releases every request b holds and answers with the barrier state and
// how many requests were released.
func ReleaseHandler(b *Barrier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBarrier(w, b, b.release())
	})
}

// BarrierHandler answers with b's state: whether it is paused, for which kinds, and the
// requests it holds in arrival order.
func BarrierHandler(b *Barrier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBarrier(w, b, 0)
	})
}

func writeBarrier(w http.ResponseWriter, b *Barrier, released int) {
	paused, kinds, held := b.state()
	writeJSON(w, http.StatusOK, barrierResponse{Paused: paused, Kinds: kinds, Held: held, Released: released})
}

// RecordHandler serves record as JSON lines. ?after=<seq> returns only later lines, so a
// script can wait for a new event without re-reading the whole file.
func RecordHandler(record *Record) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var after int64
		if raw := r.URL.Query().Get("after"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 0 {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "after must be a non-negative integer"})
				return
			}
			after = parsed
		}
		var body strings.Builder
		truncated, err := record.readAfter(&body, after)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "record could not be read"})
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		if truncated {
			w.Header().Set(RecordTruncatedHeader, "true")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body.String())
	})
}

// quotedList renders kinds as `"a"`, `"a" and "b"` or `"a", "b" and "c"`.
func quotedList(kinds []string) string {
	quoted := make([]string, len(kinds))
	for i, kind := range kinds {
		quoted[i] = strconv.Quote(kind)
	}
	if len(quoted) <= 1 {
		return strings.Join(quoted, "")
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
