package mockproduction

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/mockcontrol"
)

// kindDeploy is the only request kind the record and the barrier distinguish. Queries are
// neither recorded nor held.
const kindDeploy = "deploy"

// recordDeployed is written once per accepted deployment request, after the barrier let
// it through: either a new deployment or a replay of an operationId already deployed.
// The barrier writes the arrived, released, rejected and abandoned lines, and the Server
// writes responded immediately before each response status.
const recordDeployed = "deployed"

// maxSummaryBytes bounds every caller-supplied string copied into the record.
const maxSummaryBytes = 256

// Record is the append-only, file-backed request record WithTestControls writes to.
type Record = mockcontrol.Record

// OpenRecord opens path for appending, creating it when absent, and continues sequence
// numbering after the last line already present.
func OpenRecord(path string) (*Record, error) {
	return mockcontrol.OpenRecord(path)
}

// Option configures optional Server behaviour.
type Option func(*Server)

// WithTestControls enables the test control surface: the barrier (pause, release and its
// state) and the request record served over HTTP. Every deployment request this Server
// receives is appended to record. Without this option none of the /control routes exist
// and nothing is recorded.
func WithTestControls(record *Record) Option {
	return func(s *Server) {
		s.controls = &testControls{record: record, barrier: mockcontrol.NewBarrier(kindDeploy)}
	}
}

// WithHeldNotify makes the Server send the operationId of each deployment request on ch
// right after the barrier has recorded it and registered it as held, so a test can act
// on a held request without polling the barrier state. It has an effect only together
// with WithTestControls; the send gives up when the caller disconnects first.
func WithHeldNotify(ch chan<- string) Option {
	return func(s *Server) { s.heldNotify = ch }
}

type testControls struct {
	record  *Record
	barrier *mockcontrol.Barrier
}

// requestDetail is the Server's own part of an arrived, released, rejected, abandoned or
// responded line. It holds identifiers only, never parameters.
type requestDetail struct {
	OperationID string `json:"operationId,omitempty"`
	ApprovalID  string `json:"approvalId,omitempty"`
	Service     string `json:"service,omitempty"`
	Environment string `json:"environment,omitempty"`
	Status      int    `json:"status,omitempty"`
	Replayed    bool   `json:"replayed,omitempty"`
}

// deployedDetail is the Server's part of a deployed line. Every field is always present,
// so a reader never has to tell an absent replayed flag from a false one.
type deployedDetail struct {
	OperationID string `json:"operationId"`
	ApprovalID  string `json:"approvalId"`
	Service     string `json:"service"`
	Environment string `json:"environment"`
	BaseCommit  string `json:"baseCommit"`
	PatchDigest string `json:"patchDigest"`
	Version     string `json:"version"`
	Replayed    bool   `json:"replayed"`
}

func arrivalDetail(req deployRequest) requestDetail {
	detail := requestDetail{
		OperationID: truncateSummary(req.OperationID),
		ApprovalID:  truncateSummary(req.ApprovalID),
	}
	if req.Target != nil {
		detail.Service = truncateSummary(req.Target.Service)
		detail.Environment = truncateSummary(req.Target.Environment)
	}
	return detail
}

// admit records the arrival of one deployment request and, while the barrier is paused,
// holds it until release. It returns nil when the request may proceed, and returns nil
// immediately without test controls.
func (s *Server) admit(r *http.Request, arrival requestDetail) error {
	c := s.controls
	if c == nil {
		return nil
	}
	req := mockcontrol.Request{
		Kind:   kindDeploy,
		Detail: arrival,
		Ref:    requestDetail{OperationID: arrival.OperationID},
	}
	return c.barrier.Admit(r.Context(), c.record, req, func() {
		if s.heldNotify == nil {
			return
		}
		select {
		case s.heldNotify <- arrival.OperationID:
		case <-r.Context().Done():
		}
	})
}

// recordDeployed writes the deployed line for d. The caller holds s.mu.
func (s *Server) recordDeployed(d deployment, replayed bool) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.Append(mockcontrol.Line{
		Event: recordDeployed,
		Kind:  kindDeploy,
		Detail: deployedDetail{
			OperationID: d.OperationID,
			ApprovalID:  d.ApprovalID,
			Service:     d.Target.Service,
			Environment: d.Target.Environment,
			BaseCommit:  d.BaseCommit,
			PatchDigest: d.PatchDigest,
			Version:     d.Version,
			Replayed:    replayed,
		},
	})
}

// recordResponse writes the responded line immediately before the response status is
// sent. It is a no-op without test controls.
func (s *Server) recordResponse(operationID string, status int, replayed bool) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.Append(mockcontrol.Line{
		Event: mockcontrol.EventResponded,
		Kind:  kindDeploy,
		Detail: requestDetail{
			OperationID: truncateSummary(operationID),
			Status:      status,
			Replayed:    replayed,
		},
	})
}

// reject answers a request that fails validation before it reaches the barrier. With
// test controls its arrival and the error status are still recorded, so the record lists
// every deployment request production received.
func (s *Server) reject(w http.ResponseWriter, arrival requestDetail, status int, message string) {
	if s.controls != nil {
		_, _ = s.controls.record.Append(mockcontrol.Line{Event: mockcontrol.EventArrived, Kind: kindDeploy, Detail: arrival})
		s.recordResponse(arrival.OperationID, status, false)
	}
	writeJSON(w, status, errorResponse{Code: codeInvalidRequest, Message: message})
}

// respondHeldError answers a request admit did not let through.
func (s *Server) respondHeldError(w http.ResponseWriter, operationID string, err error) {
	switch {
	case errors.Is(err, mockcontrol.ErrCallerGone):
		// Nobody is left to read a response; the barrier already wrote abandoned.
	case errors.Is(err, mockcontrol.ErrStopping):
		s.recordResponse(operationID, http.StatusServiceUnavailable, false)
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Code: codeUnavailable, Message: "production is shutting down"})
	default:
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: codeInternal, Message: "request record could not be written"})
	}
}

// StopHolding rejects every request the barrier is holding and disables the barrier, so
// an HTTP server shutdown that waits for in-flight handlers cannot wait on a release that
// will never come. It is a no-op without test controls.
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
