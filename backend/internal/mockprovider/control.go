package mockprovider

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

// maxControlBodyBytes bounds a control request body; the largest valid one is a short
// JSON object naming a request kind.
const maxControlBodyBytes = 4 << 10

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
			record:    record,
			barrier:   newBarrier(),
			tasksByID: make(map[string]callbackTask),
		}
	}
}

// testControls is the state behind WithTestControls. It is nil on a Server built without
// that option, which is what keeps the default request path unchanged.
type testControls struct {
	record  *Record
	barrier *barrier
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
}

// heldRequest is one request the barrier is currently holding.
type heldRequest struct {
	Kind           string `json:"kind"`
	ExternalTaskID string `json:"externalTaskId,omitempty"`
	ArrivalSeq     int64  `json:"arrivalSeq"`
}

// barrier holds matching requests between pause and release. pause arms a fresh gate;
// release closes it, letting every request waiting on that gate proceed at once, and
// disarms. Pausing again after a release arms a new gate. stop rejects every held request
// and makes later pauses no-ops, so shutdown never waits on a request nobody will release.
type barrier struct {
	mu      sync.Mutex
	paused  bool
	kinds   map[string]bool // empty means every kind
	gate    chan struct{}
	stopped chan struct{}
	isStop  bool
	nextID  int64
	held    map[int64]heldRequest
}

func newBarrier() *barrier {
	return &barrier{stopped: make(chan struct{}), held: make(map[int64]heldRequest)}
}

// pause arms the barrier for kinds (every kind when empty). Pausing an already paused
// barrier keeps its gate and the requests already held, and only replaces the kind filter.
func (b *barrier) pause(kinds []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.isStop {
		return
	}
	filter := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		filter[kind] = true
	}
	b.kinds = filter
	if !b.paused {
		b.paused = true
		b.gate = make(chan struct{})
	}
}

// release lets every held request proceed and disarms the barrier. It returns how many
// requests it released; releasing an unpaused barrier is a no-op that returns 0.
func (b *barrier) release() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.paused {
		return 0
	}
	released := len(b.held)
	close(b.gate)
	b.paused = false
	b.gate = nil
	b.held = make(map[int64]heldRequest)
	return released
}

// stop rejects every held request and disables the barrier for good.
func (b *barrier) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.isStop {
		return
	}
	b.isStop = true
	close(b.stopped)
	b.paused = false
	b.gate = nil
	b.held = make(map[int64]heldRequest)
}

// enter registers req as held when the barrier is paused for its kind. It returns the gate
// to wait on and a ticket for leave, or a nil gate when the request must not be held.
func (b *barrier) enter(req heldRequest) (<-chan struct{}, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.paused || (len(b.kinds) > 0 && !b.kinds[req.Kind]) {
		return nil, 0
	}
	b.nextID++
	b.held[b.nextID] = req
	return b.gate, b.nextID
}

// leave removes a request that stops waiting for a reason other than release.
func (b *barrier) leave(ticket int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.held, ticket)
}

// state returns whether the barrier is paused and which requests it is holding, ordered by
// arrival.
func (b *barrier) state() (bool, []string, []heldRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	held := make([]heldRequest, 0, len(b.held))
	for _, req := range b.held {
		held = append(held, req)
	}
	slices.SortFunc(held, func(a, b heldRequest) int { return cmp.Compare(a.ArrivalSeq, b.ArrivalSeq) })
	kinds := make([]string, 0, len(b.kinds))
	for _, kind := range []string{kindTask, kindGenerate} {
		if b.kinds[kind] {
			kinds = append(kinds, kind)
		}
	}
	return b.paused, kinds, held
}

// errProviderStopping reports that a held request was rejected because the Provider is
// shutting down.
var errProviderStopping = errors.New("mock provider is shutting down")

// errCallerGone reports that the caller disconnected while its request was held.
var errCallerGone = errors.New("caller disconnected while held")

// admit records the arrival of one external request and, while the barrier is paused for
// its kind, holds it until release. It returns nil when the request may proceed. Without
// test controls it returns nil immediately and records nothing, keeping the default request
// path exactly as it was.
func (s *Server) admit(r *http.Request, arrival recordEntry) error {
	c := s.controls
	if c == nil {
		return nil
	}
	arrival.Event = recordArrived
	seq, err := c.record.append(arrival)
	if err != nil {
		return err
	}
	gate, ticket := c.barrier.enter(heldRequest{Kind: arrival.Kind, ExternalTaskID: arrival.ExternalTaskID, ArrivalSeq: seq})
	if gate == nil {
		return nil
	}
	if c.heldHook != nil {
		label := arrival.ExternalTaskID
		if label == "" {
			label = arrival.Kind
		}
		select {
		case c.heldHook <- label:
		case <-r.Context().Done():
		}
	}

	outcome := recordEntry{Kind: arrival.Kind, ExternalTaskID: arrival.ExternalTaskID}
	select {
	case <-gate:
		outcome.Event = recordReleased
		_, _ = c.record.append(outcome)
		return nil
	case <-c.barrier.stopped:
		outcome.Event = recordRejected
		_, _ = c.record.append(outcome)
		return errProviderStopping
	case <-r.Context().Done():
		c.barrier.leave(ticket)
		outcome.Event = recordAbandoned
		_, _ = c.record.append(outcome)
		return errCallerGone
	}
}

// recordResponse writes the responded line immediately before the response status is
// sent. It is a no-op without test controls.
func (s *Server) recordResponse(kind, externalTaskID string, status int, replayed bool) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.append(recordEntry{
		Event:          recordResponded,
		Kind:           kind,
		ExternalTaskID: externalTaskID,
		Status:         status,
		Replayed:       replayed,
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
		s.controls.barrier.stop()
	}
}

func (s *Server) mountControlRoutes(r chi.Router) {
	r.Route("/control", func(r chi.Router) {
		r.Post("/pause", s.handlePause)
		r.Post("/release", s.handleRelease)
		r.Get("/barrier", s.handleBarrier)
		r.Get("/record", s.handleRecord)
		r.Post("/tasks/{externalTaskId}/callback", s.handleRedeliver)
	})
}

// pauseRequest is the optional body of POST /control/pause.
type pauseRequest struct {
	// Kinds limits the barrier to "task" and/or "generate" requests; empty holds both.
	Kinds []string `json:"kinds,omitempty"`
}

// barrierResponse is the body of every barrier control response.
type barrierResponse struct {
	Paused   bool          `json:"paused"`
	Kinds    []string      `json:"kinds"`
	Held     []heldRequest `json:"held"`
	Released int           `json:"released,omitempty"`
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
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
		if kind != kindTask && kind != kindGenerate {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: `kinds may contain only "task" and "generate"`})
			return
		}
	}
	s.controls.barrier.pause(req.Kinds)
	s.writeBarrier(w, 0)
}

func (s *Server) handleRelease(w http.ResponseWriter, _ *http.Request) {
	s.writeBarrier(w, s.controls.barrier.release())
}

func (s *Server) handleBarrier(w http.ResponseWriter, _ *http.Request) {
	s.writeBarrier(w, 0)
}

func (s *Server) writeBarrier(w http.ResponseWriter, released int) {
	paused, kinds, held := s.controls.barrier.state()
	writeJSON(w, http.StatusOK, barrierResponse{Paused: paused, Kinds: kinds, Held: held, Released: released})
}

// handleRecord serves the dispatch record as JSON lines. ?after=<seq> returns only later
// lines, so a script can wait for a new event without re-reading the whole file.
func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request) {
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
	truncated, err := s.controls.record.readAfter(&body, after)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "record could not be read"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	if truncated {
		w.Header().Set("X-Mockprovider-Record-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body.String())
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
		arrival.Event = recordArrived
		_, _ = s.controls.record.append(arrival)
		s.recordResponse(arrival.Kind, arrival.ExternalTaskID, status, false)
	}
	writeJSON(w, status, errorResponse{Error: message})
}

// respondHeldError answers a request admit did not let through.
func (s *Server) respondHeldError(w http.ResponseWriter, kind, externalTaskID string, err error) {
	switch {
	case errors.Is(err, errCallerGone):
		// Nobody is left to read a response.
	case errors.Is(err, errProviderStopping):
		s.recordResponse(kind, externalTaskID, http.StatusServiceUnavailable, false)
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "mock provider is shutting down"})
	default:
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "dispatch record could not be written"})
	}
}
