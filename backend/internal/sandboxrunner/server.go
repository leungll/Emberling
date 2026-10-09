// Package sandboxrunner is the sandbox runner: an external test fixture process that
// receives a `sandbox_test` dispatch, applies the patch to a fixed base commit, runs the
// tests and reports the outcome through the Emberling callback endpoint. It stands in for
// an external service, so it shares no Go type with the Runtime it is dispatched from.
//
// A Backend decides how a test runs. The deterministic mock Backend answers from the
// request's mock mode; the docker Backend runs the embedded fixture module in a locked-down
// container through the docker CLI. Either way the runner owns every goroutine it starts,
// bounds how many tests it accepts, delivers each callback without retrying, and keeps the
// callback token out of logs, errors and the request record.
package sandboxrunner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// testsPath is the dispatch route; a status query appends /{testId}.
const testsPath = "/v1/tests"

// callbackTokenHeader carries the one-time callback token on every delivery.
const callbackTokenHeader = "X-Emberling-Callback-Token"

// Request bounds. A patch is bounded so one request cannot hold unbounded memory; the body
// limit leaves room for the patch's JSON escaping and the other members.
const (
	maxPatchBytes      = 256 << 10
	maxRequestBytes    = 2 * maxPatchBytes
	maxBaseCommitBytes = 128
	maxCallbackBytes   = 2 << 10
)

// maxOutstandingTests bounds accepted tests that have not finished delivering; a dispatch
// beyond it is answered 503 instead of queueing without limit.
const maxOutstandingTests = 64

// maxConcurrentRuns bounds how many tests run at the same time.
const maxConcurrentRuns = 2

// maxKnownTests bounds the status entries kept for GET /v1/tests/{testId}; the oldest is
// forgotten first.
const maxKnownTests = 10_000

// defaultCallbackTimeout bounds one callback delivery when no HTTP client is injected.
const defaultCallbackTimeout = 10 * time.Second

// Test statuses reported by GET /v1/tests/{testId}. SUCCEEDED and FAILED are also the
// callback payload statuses: SUCCEEDED means the run produced a verdict, which may still be
// a failing test suite, and FAILED means it produced none.
const (
	statusAccepted  = "ACCEPTED"
	statusRunning   = "RUNNING"
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// Mock modes a request may name. The mock Backend accepts each; the docker Backend accepts
// none.
const (
	mockPass      = "pass"
	mockFail      = "fail"
	mockError     = "error"
	mockDuplicate = "duplicate"
	mockLost      = "lost"
)

// testRequest is the POST /v1/tests body. Unknown members are rejected, so a request can
// never select an image, a mount or extra container arguments.
type testRequest struct {
	CallbackURL   string `json:"callbackUrl"`
	CallbackToken string `json:"callbackToken"`
	BaseCommit    string `json:"baseCommit"`
	Patch         string `json:"patch"`
	Mock          string `json:"mock,omitempty"`
}

type acceptedResponse struct {
	TestID string `json:"testId"`
}

type statusResponse struct {
	TestID string          `json:"testId"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// Job is one accepted test as a Backend sees it. It carries no callback credential.
type Job struct {
	TestID      string
	BaseCommit  string
	Patch       string // LF-normalized
	PatchDigest string
	Mock        string
}

// Outcome is what a Backend reports for one Job. Verdict is true when the run produced a
// pass or fail verdict; otherwise FailureCode and FailureMessage say why it produced none.
type Outcome struct {
	Verdict        bool
	Passed         bool
	Total          int
	Failed         int
	FailureCode    string
	FailureMessage string
}

// Backend runs one test. Run must honor ctx: the Server cancels it on shutdown.
type Backend interface {
	// ValidateMock reports whether this Backend accepts the request's mock mode.
	ValidateMock(mock string) error
	Run(ctx context.Context, job Job) Outcome
}

// Option configures optional Server behaviour.
type Option func(*Server)

// WithHTTPClient sets the client callbacks are delivered with.
func WithHTTPClient(client *http.Client) Option {
	return func(s *Server) { s.client = client }
}

// testState is the status of one accepted test.
type testState struct {
	status string
	result json.RawMessage
}

// Server serves the runner routes and owns every test and callback goroutine it starts.
type Server struct {
	backend Backend
	client  *http.Client
	router  chi.Router
	newID   func() (string, error)

	// controls is the barrier, request record and redelivery store. It is nil unless
	// WithTestControls was passed, and every use of it is a no-op when nil.
	controls *testControls

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	runs   chan struct{}

	mu          sync.Mutex
	stopped     bool
	outstanding int
	tests       map[string]*testState
	testOrder   []string
}

// NewServer returns a ready-to-serve Server running tests on backend. The caller owns the
// Server's lifetime and calls Shutdown, after StopHolding and the HTTP server shutdown.
func NewServer(backend Backend, opts ...Option) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		backend: backend,
		client:  &http.Client{Timeout: defaultCallbackTimeout},
		newID:   randomTestID,
		ctx:     ctx,
		cancel:  cancel,
		runs:    make(chan struct{}, maxConcurrentRuns),
		tests:   make(map[string]*testState),
	}
	for _, opt := range opts {
		opt(s)
	}
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Post(testsPath, s.handleCreate)
	r.Get(testsPath+"/{testId}", s.handleStatus)
	if s.controls != nil {
		s.mountControlRoutes(r)
	}
	s.router = r
	return s
}

// ServeHTTP makes Server an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// handleCreate validates one dispatch, records and possibly holds it, then accepts it
// and starts the test in a goroutine the Server owns.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	arrival := testArrival{}
	var req testRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		s.reject(w, arrival, http.StatusBadRequest, "request body must be one JSON object with only callbackUrl, callbackToken, baseCommit, patch and mock")
		return
	}
	arrival.BaseCommit = truncateSummary(req.BaseCommit)
	arrival.CallbackTarget = truncateSummary(callbackTarget(req.CallbackURL))
	arrival.Mock = truncateSummary(req.Mock)
	if message := validateRequest(req); message != "" {
		s.reject(w, arrival, http.StatusBadRequest, message)
		return
	}
	if err := s.backend.ValidateMock(req.Mock); err != nil {
		s.reject(w, arrival, http.StatusBadRequest, err.Error())
		return
	}
	testID, err := s.newID()
	if err != nil {
		s.reject(w, arrival, http.StatusInternalServerError, "testId could not be generated")
		return
	}
	patch := normalizeLF(req.Patch)
	arrival.TestID = testID
	arrival.PatchDigest = PatchDigest(req.Patch)

	// The barrier holds the dispatch after it is recorded and before anything is accepted,
	// so a held dispatch has started no test.
	if err := s.admitTest(r.Context(), arrival); err != nil {
		s.respondHeldError(w, testID, err)
		return
	}
	job := Job{TestID: testID, BaseCommit: req.BaseCommit, Patch: patch, PatchDigest: arrival.PatchDigest, Mock: req.Mock}
	destination := callbackDestination{URL: req.CallbackURL, Token: req.CallbackToken}
	if !s.schedule(job, destination) {
		s.recordResponse(testID, http.StatusServiceUnavailable)
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "sandbox runner is not accepting tests"})
		return
	}
	s.recordResponse(testID, http.StatusAccepted)
	writeJSON(w, http.StatusAccepted, acceptedResponse{TestID: testID})
}

// validateRequest returns a fixed message naming the first invalid member, or "". It never
// echoes a member's value.
func validateRequest(req testRequest) string {
	switch {
	case strings.TrimSpace(req.CallbackURL) == "":
		return "callbackUrl is required"
	case len(req.CallbackURL) > maxCallbackBytes || !validCallbackURL(req.CallbackURL):
		return "callbackUrl must be an absolute http or https URL"
	case strings.TrimSpace(req.CallbackToken) == "":
		return "callbackToken is required"
	case len(req.CallbackToken) > maxCallbackBytes:
		return "callbackToken is too long"
	case strings.TrimSpace(req.BaseCommit) == "":
		return "baseCommit is required"
	case len(req.BaseCommit) > maxBaseCommitBytes:
		return "baseCommit is too long"
	case req.Patch == "":
		return "patch is required"
	case len(req.Patch) > maxPatchBytes:
		return "patch is too large"
	}
	return ""
}

// handleStatus answers GET /v1/tests/{testId} with the test's status and, once it has
// finished, its callback payload.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	testID := chi.URLParam(r, "testId")
	s.mu.Lock()
	state, ok := s.tests[testID]
	var response statusResponse
	if ok {
		response = statusResponse{TestID: testID, Status: state.status, Result: state.result}
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown testId"})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// setState records a test's status, forgetting the oldest test once maxKnownTests is
// reached.
func (s *Server) setState(testID, status string, result json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.tests[testID]; ok {
		state.status, state.result = status, result
		return
	}
	if len(s.testOrder) >= maxKnownTests {
		delete(s.tests, s.testOrder[0])
		s.testOrder = s.testOrder[1:]
	}
	s.testOrder = append(s.testOrder, testID)
	s.tests[testID] = &testState{status: status, result: result}
}

// PatchDigest returns "sha256:" and the hex SHA-256 of patch with CRLF and lone CR line
// endings normalized to LF, so the same change has one digest whatever editor produced it.
func PatchDigest(patch string) string {
	sum := sha256.Sum256([]byte(normalizeLF(patch)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func randomTestID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "test_" + hex.EncodeToString(b[:]), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
