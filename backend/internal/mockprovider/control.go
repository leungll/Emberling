package mockprovider

import (
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/mockcontrol"
)

// maxRedeliverableTasks bounds how many accepted tasks the redeliver control can still
// find. The oldest task is forgotten first; redelivering a forgotten task answers 404.
const maxRedeliverableTasks = 10_000

// Option configures optional Server behaviour.
type Option func(*Server)

// WithTestControls enables the demo and test control surface: the external barrier
// (pause, release and its state), the dispatch record served over HTTP, and on-demand
// callback redelivery. Every request this Server receives is appended to record. Without
// this option none of the /control routes exist and nothing is recorded.
func WithTestControls(record *Record) Option {
	return func(s *Server) {
		s.controls = &testControls{
			record:     record,
			barrier:    mockcontrol.NewBarrier(kindTask, kindGenerate),
			tasksByID:  make(map[string]callbackTask),
			deliveries: make(map[string]int),
		}
	}
}

// testControls is the state behind WithTestControls. It is nil on a Server built without
// that option, which is what keeps the default request path unchanged.
type testControls struct {
	record  *Record
	barrier *mockcontrol.Barrier
	// heldHook, when non-nil, receives the externalTaskId (or kind, for requests without
	// one) of each request right after it is recorded and registered as held. It is a test
	// barrier; production code leaves it nil.
	heldHook chan string

	// mu guards tasksByID and taskIDOrder: the callback each accepted task was scheduled
	// with, kept in memory only so the redeliver control can send it again. The callback
	// token lives here exactly as long as it would for any pending delivery and is never
	// written to the record or returned by a control route.
	mu          sync.Mutex
	tasksByID   map[string]callbackTask
	taskIDOrder []string

	// deliveryMu guards deliveries and deliveryOrder, the per-task callback attempt
	// counters. It is held across the record append so delivery numbers for one task rise
	// in the same order as their seq.
	deliveryMu    sync.Mutex
	deliveries    map[string]int
	deliveryOrder []string
}

// admit records the arrival of one external request and, while the barrier is paused for
// its kind, holds it until release. It returns nil when the request may proceed. Without
// test controls it returns nil immediately and records nothing, keeping the default request
// path exactly as it was.
func (s *Server) admit(r *http.Request, arrival recordEntry) error {
	c := s.controls
	if c == nil {
		return nil
	}
	req := mockcontrol.Request{
		Kind:   arrival.Kind,
		Detail: arrival.recordDetail,
		Ref:    recordDetail{ExternalTaskID: arrival.ExternalTaskID},
	}
	return c.barrier.Admit(r.Context(), c.record, req, func() {
		if c.heldHook == nil {
			return
		}
		label := arrival.ExternalTaskID
		if label == "" {
			label = arrival.Kind
		}
		select {
		case c.heldHook <- label:
		case <-r.Context().Done():
		}
	})
}

// recordResponse writes the responded line immediately before the response status is
// sent. It is a no-op without test controls.
func (s *Server) recordResponse(kind, externalTaskID string, status int, replayed bool) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.Append(mockcontrol.Line{
		Event: recordResponded,
		Kind:  kind,
		Detail: recordDetail{
			ExternalTaskID: externalTaskID,
			Status:         status,
			Replayed:       replayed,
		},
	})
}

// rememberTask keeps task so the redeliver control can send its callback again.
func (s *Server) rememberTask(task callbackTask) {
	c := s.controls
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, known := c.tasksByID[task.ExternalTaskID]; !known {
		if len(c.taskIDOrder) >= maxRedeliverableTasks {
			oldest := c.taskIDOrder[0]
			c.taskIDOrder = c.taskIDOrder[1:]
			delete(c.tasksByID, oldest)
		}
		c.taskIDOrder = append(c.taskIDOrder, task.ExternalTaskID)
	}
	c.tasksByID[task.ExternalTaskID] = task
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
		r.Post("/tasks/{externalTaskId}/callback", s.handleRedeliver)
	})
}

// redeliverResponse is the body of a successful POST /control/tasks/{id}/callback.
type redeliverResponse struct {
	ExternalTaskID string `json:"externalTaskId"`
	Status         int    `json:"status,omitempty"`
	Delivered      bool   `json:"delivered"`
}

// handleRedeliver sends an accepted task's callback once more, synchronously, exactly as
// it was first scheduled. It lets a script produce a duplicate or late callback on command
// instead of choosing one up front through delayMs.
func (s *Server) handleRedeliver(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "externalTaskId")
	c := s.controls
	c.mu.Lock()
	task, ok := c.tasksByID[id]
	c.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown externalTaskId"})
		return
	}
	status, err := s.dispatcher.deliver(r.Context(), task)
	writeJSON(w, http.StatusOK, redeliverResponse{ExternalTaskID: id, Status: status, Delivered: err == nil})
}

// maxSummaryBytes bounds every caller-supplied string copied into the record.
const maxSummaryBytes = 256

// truncateSummary bounds a caller-supplied identifier before it is recorded.
func truncateSummary(value string) string {
	if len(value) <= maxSummaryBytes {
		return value
	}
	return value[:maxSummaryBytes]
}

// taskScenarioSummary names the delay and outcome a task request selected, without any
// payload content.
func taskScenarioSummary(req taskRequest) string {
	var delay string
	switch req.DelayMs.Mode {
	case delayLost:
		delay = "lost"
	case delayDuplicate:
		delay = "duplicate"
	case delayBeforeResponse:
		delay = "beforeResponse"
	case delayAfter:
		delay = strconv.FormatInt(req.DelayMs.Duration.Milliseconds(), 10) + "ms"
	default: // delayImmediate
		delay = "0ms"
	}
	var result string
	switch req.Outcome {
	case outcomeFailed:
		result = outcomeFailedName
	case outcomeWrongToken:
		result = outcomeWrongTokenName
	default: // outcomeSucceeded
		result = outcomeSucceededName
	}
	return "delay=" + delay + ",outcome=" + result
}

// reject answers a request that fails before it reaches the barrier. With test controls,
// its arrival and the error status are still recorded, so the record lists every request
// the Provider received.
func (s *Server) reject(w http.ResponseWriter, arrival recordEntry, status int, message string) {
	if s.controls != nil {
		_, _ = s.controls.record.Append(arrival.line(recordArrived))
		s.recordResponse(arrival.Kind, arrival.ExternalTaskID, status, false)
	}
	writeJSON(w, status, errorResponse{Error: message})
}

// respondHeldError answers a request admit did not let through.
func (s *Server) respondHeldError(w http.ResponseWriter, kind, externalTaskID string, err error) {
	switch {
	case errors.Is(err, mockcontrol.ErrCallerGone):
		// Nobody is left to read a response.
	case errors.Is(err, mockcontrol.ErrStopping):
		s.recordResponse(kind, externalTaskID, http.StatusServiceUnavailable, false)
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "mock provider is shutting down"})
	default:
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "dispatch record could not be written"})
	}
}
