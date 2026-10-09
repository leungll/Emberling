package sandboxrunner

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/mockcontrol"
)

// Request kinds the record and the barrier distinguish. A pause naming kindTest holds a
// dispatch before it is accepted; one naming kindCallback holds a finished test's callback
// before it is delivered.
const (
	kindTest     = "test"
	kindCallback = "callback"
)

// Record events the runner writes besides the barrier's own.
const (
	eventStarted     = "started"
	eventCompleted   = "completed"
	eventCallback    = "callback"
	eventLost        = "lost"
	eventRedelivered = "redelivered"
)

// maxSummaryBytes bounds every caller-supplied string copied into the record.
const maxSummaryBytes = 256

// WithTestControls enables the demo and test control surface: the external barrier
// (pause, release and its state) and the request record served over HTTP, both shared with
// the other fixtures through mockcontrol, plus storing each finished result in store so a
// restarted runner can redeliver it. Without this option none of the /control routes
// exist, nothing is recorded and nothing is stored.
func WithTestControls(record *mockcontrol.Record, store *RedeliveryStore) Option {
	return func(s *Server) {
		s.controls = &testControls{
			record:  record,
			store:   store,
			barrier: mockcontrol.NewBarrier(kindTest, kindCallback),
		}
	}
}

// testControls is the state behind WithTestControls; nil without that option.
type testControls struct {
	record  *mockcontrol.Record
	store   *RedeliveryStore
	barrier *mockcontrol.Barrier
	// heldHook, when non-nil, receives the kind of each request right after it is recorded
	// and registered as held. It is a test barrier; production code leaves it nil.
	heldHook chan string
}

// Record line details. None has a field that could carry the callback token, the full
// callback URL, the patch or a callback payload.
type (
	testArrival struct {
		TestID         string `json:"testId,omitempty"`
		BaseCommit     string `json:"baseCommit,omitempty"`
		PatchDigest    string `json:"patchDigest,omitempty"`
		Mock           string `json:"mock,omitempty"`
		CallbackTarget string `json:"callbackTarget,omitempty"`
	}
	testRef struct {
		TestID string `json:"testId"`
	}
	respondedDetail struct {
		TestID string `json:"testId,omitempty"`
		Status int    `json:"status"`
	}
	completedDetail struct {
		TestID       string `json:"testId"`
		Status       string `json:"status"`
		Passed       *bool  `json:"passed,omitempty"`
		PatchDigest  string `json:"patchDigest"`
		ResultDigest string `json:"resultDigest,omitempty"`
		Code         string `json:"code,omitempty"`
	}
	callbackArrival struct {
		TestID  string `json:"testId"`
		Attempt int    `json:"attempt"`
	}
	callbackDetail struct {
		TestID     string `json:"testId"`
		Status     string `json:"status"`
		Attempt    int    `json:"attempt"`
		HTTPStatus int    `json:"httpStatus"`
	}
)

func (s *Server) appendLine(event, kind string, detail any) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.Append(mockcontrol.Line{Event: event, Kind: kind, Detail: detail})
}

// admit records one arrival and, while the barrier is paused for its kind, holds it. It
// returns nil at once without test controls.
func (s *Server) admit(ctx context.Context, kind string, detail any, ref any) error {
	c := s.controls
	if c == nil {
		return nil
	}
	return c.barrier.Admit(ctx, c.record, mockcontrol.Request{Kind: kind, Detail: detail, Ref: ref}, func() {
		if c.heldHook == nil {
			return
		}
		select {
		case c.heldHook <- kind:
		case <-ctx.Done():
		}
	})
}

func (s *Server) admitTest(ctx context.Context, arrival testArrival) error {
	return s.admit(ctx, kindTest, arrival, testRef{TestID: arrival.TestID})
}

func (s *Server) admitCallback(ctx context.Context, testID string, attempt int) error {
	return s.admit(ctx, kindCallback, callbackArrival{TestID: testID, Attempt: attempt}, testRef{TestID: testID})
}

func (s *Server) recordResponse(testID string, status int) {
	s.appendLine(mockcontrol.EventResponded, kindTest, respondedDetail{TestID: testID, Status: status})
}

func (s *Server) recordStarted(job Job) {
	s.appendLine(eventStarted, kindTest, testRef{TestID: job.TestID})
}

func (s *Server) recordCompleted(job Job, status string, outcome Outcome) {
	detail := completedDetail{TestID: job.TestID, Status: status, PatchDigest: job.PatchDigest}
	if outcome.Verdict {
		passed := outcome.Passed
		detail.Passed = &passed
		detail.ResultDigest = ResultDigest(job.BaseCommit, job.PatchDigest, outcome.Passed, outcome.Total, outcome.Failed)
	} else {
		detail.Code = truncateSummary(outcome.FailureCode)
	}
	s.appendLine(eventCompleted, kindTest, detail)
}

func (s *Server) recordCallback(testID, status string, attempt, httpStatus int) {
	s.appendLine(eventCallback, kindCallback, callbackDetail{TestID: testID, Status: status, Attempt: attempt, HTTPStatus: httpStatus})
}

func (s *Server) recordLost(testID string) {
	s.appendLine(eventLost, kindCallback, testRef{TestID: testID})
}

// storeForRedelivery keeps a finished result so a restarted runner can deliver it again.
// A store failure is not fatal to the current delivery: it only loses that redelivery.
func (s *Server) storeForRedelivery(entry redeliveryEntry) {
	if s.controls == nil || s.controls.store == nil {
		return
	}
	_ = s.controls.store.Append(entry)
}

// reject answers a dispatch that fails before it reaches the barrier. With test controls
// its arrival and the error status are still recorded, so the record lists every dispatch
// the runner received.
func (s *Server) reject(w http.ResponseWriter, arrival testArrival, status int, message string) {
	if s.controls != nil {
		_, _ = s.controls.record.Append(mockcontrol.Line{Event: mockcontrol.EventArrived, Kind: kindTest, Detail: arrival})
		s.recordResponse(arrival.TestID, status)
	}
	writeJSON(w, status, errorResponse{Error: message})
}

// respondHeldError answers a dispatch admit did not let through.
func (s *Server) respondHeldError(w http.ResponseWriter, testID string, err error) {
	switch {
	case errors.Is(err, mockcontrol.ErrCallerGone):
		// Nobody is left to read a response.
	case errors.Is(err, mockcontrol.ErrStopping):
		s.recordResponse(testID, http.StatusServiceUnavailable)
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "sandbox runner is shutting down"})
	default:
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "request record could not be written"})
	}
}

// StopHolding rejects every request the barrier holds and disables it, so neither the HTTP
// server shutdown nor Shutdown waits on a release that will never come. It is a no-op
// without test controls.
func (s *Server) StopHolding() {
	if s.controls != nil {
		s.controls.barrier.Stop()
	}
}

func (s *Server) mountControlRoutes(r chi.Router) {
	r.Route("/control", func(r chi.Router) {
		r.Method(http.MethodPost, "/pause", mockcontrol.PauseHandler(s.controls.barrier))
		r.Method(http.MethodPost, "/release", mockcontrol.ReleaseHandler(s.controls.barrier))
		r.Method(http.MethodGet, "/barrier", mockcontrol.BarrierHandler(s.controls.barrier))
		r.Method(http.MethodGet, "/record", mockcontrol.RecordHandler(s.controls.record))
	})
}

// truncateSummary bounds a caller-supplied identifier before it is recorded.
func truncateSummary(value string) string {
	if len(value) <= maxSummaryBytes {
		return value
	}
	return value[:maxSummaryBytes]
}
