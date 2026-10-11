package main

import "net/http"

// runningBody and failedBody are the two fixed result forms.
var (
	runningBody = encodeResult(resultBody{State: "running"})
	failedBody  = encodeResult(resultBody{State: "failed", Reason: workspaceInvalidReason})
)

// ServeHTTP serves the read-only probe and result endpoints. Every endpoint answers GET
// only; the supervisor accepts no writes.
func (s *supervisor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/healthz":
		w.WriteHeader(http.StatusOK)
	case "/readyz":
		// Ready means the workspace is written and the command has started; a refused
		// workspace never becomes ready.
		if p, _ := s.snapshot(); p == phaseRunning || p == phaseDone {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	case "/v1/result":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(s.resultBytes())
	default:
		http.NotFound(w, r)
	}
}

// resultBytes returns the encoded result for the current phase. The done form appears only
// after the command has exited, and no partial output is reported before that.
func (s *supervisor) resultBytes() []byte {
	switch p, done := s.snapshot(); p {
	case phaseInvalid:
		return failedBody
	case phaseDone:
		return done
	case phasePreparing, phaseRunning:
	}
	return runningBody
}
