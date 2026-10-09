// Package mockproduction is the deterministic HTTP stand-in for a production deployment
// service. The deploy Tool calls it; cmd/mockproduction serves it as a process and tests
// serve it in-process. Like any external service, it shares no Go type with the Runtime
// that calls it.
//
// All state lives in memory for one process lifetime. With test controls enabled, every
// deployment request it receives is also appended to a file-backed record, so a script
// can prove how many times production was reached for one operationId.
//
// Version scheme: each target (service and environment) has its own counter, starting at
// zero when the process starts. Every new operationId deployed to a target increments it
// and is assigned "v" followed by the new value, so the first deployment to a target is
// "v1", the second "v2" and so on, in the order this process accepted them. A replayed
// operationId never increments the counter.
package mockproduction

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// maxDeployments bounds how many deployments the Server remembers. Once full, the oldest
// is forgotten first: its operationId is then treated as new, and querying it answers 404.
const maxDeployments = 10_000

// maxTargets bounds how many targets the Server tracks. Once full, the least recently
// created target is forgotten and its version counter starts again from zero.
const maxTargets = 10_000

// maxRequestBytes bounds a deployment request body.
const maxRequestBytes = 64 << 10

// maxDigestBytes bounds baseCommit and patchDigest.
const maxDigestBytes = 256

// mockControlKey names the parameters member that selects a scripted outcome. The only
// accepted value is mockReject, which refuses the deployment before anything changes.
const (
	mockControlKey = "mock"
	mockReject     = "reject"
)

// Error codes the Server answers with.
const (
	codeInvalidRequest = "INVALID_REQUEST"
	codeRejected       = "REJECTED"
	codeNotFound       = "NOT_FOUND"
	codeUnavailable    = "UNAVAILABLE"
	codeInternal       = "INTERNAL"
)

var (
	operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
	targetNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Server implements the production deployment routes.
type Server struct {
	router chi.Router
	now    func() time.Time

	// mu guards deployments, deploymentOrder, targets and targetOrder. It is held across
	// the decision to deploy and the deployed record line, so versions for one target rise
	// in the same order as their seq and two concurrent requests carrying one operationId
	// cannot both deploy.
	mu              sync.Mutex
	deployments     map[string]deployment
	deploymentOrder []string
	targets         map[string]targetState
	targetOrder     []string

	// controls is the test control surface (barrier and record). It is nil unless
	// WithTestControls was passed, and every use of it is a no-op when nil.
	controls *testControls
	// heldNotify, when non-nil, receives the operationId of each held request. It is set
	// only by WithHeldNotify.
	heldNotify chan<- string
}

// deployment is one accepted deployment, as GET /v1/deployments/{operationId} reports it.
type deployment struct {
	OperationID string `json:"operationId"`
	ApprovalID  string `json:"approvalId,omitempty"`
	Target      target `json:"target"`
	BaseCommit  string `json:"baseCommit"`
	PatchDigest string `json:"patchDigest"`
	Version     string `json:"version"`
	DeployedAt  string `json:"deployedAt"`
}

type target struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
}

func (t target) key() string { return t.Service + "/" + t.Environment }

// targetState is what one target currently runs.
type targetState struct {
	counter         int
	version         string
	patchDigest     string
	lastOperationID string
}

type deployRequest struct {
	OperationID string          `json:"operationId"`
	ApprovalID  string          `json:"approvalId"`
	Target      *target         `json:"target"`
	BaseCommit  string          `json:"baseCommit"`
	PatchDigest string          `json:"patchDigest"`
	Parameters  json.RawMessage `json:"parameters"`
}

type deployResponse struct {
	OperationID string `json:"operationId"`
	Version     string `json:"version"`
	DeployedAt  string `json:"deployedAt"`
}

type targetResponse struct {
	Version         string `json:"version"`
	PatchDigest     string `json:"patchDigest"`
	LastOperationID string `json:"lastOperationId"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewServer returns a ready-to-serve Server. With WithTestControls, the caller owns the
// Record and calls StopHolding before shutting the HTTP server down.
func NewServer(opts ...Option) *Server {
	s := &Server{
		now:         time.Now,
		deployments: make(map[string]deployment),
		targets:     make(map[string]targetState),
	}
	for _, opt := range opts {
		opt(s)
	}
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealthz)
	r.Post("/v1/deployments", s.handleDeploy)
	r.Get("/v1/deployments/{operationId}", s.handleDeployment)
	r.Get("/v1/targets/{service}/{environment}", s.handleTarget)
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

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeploy accepts one deployment. A request naming an operationId this Server has
// already deployed answers with the stored result, whatever the rest of its body says
// (including a scripted rejection), and changes nothing.
func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	req, reject, err := decodeDeployRequest(r)
	arrival := arrivalDetail(req)
	if err != nil {
		s.reject(w, arrival, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.admit(r, arrival); err != nil {
		s.respondHeldError(w, req.OperationID, err)
		return
	}
	deployed, replayed, ok := s.deploy(req, reject)
	if !ok {
		s.recordResponse(req.OperationID, http.StatusConflict, false)
		writeJSON(w, http.StatusConflict, errorResponse{Code: codeRejected, Message: "deployment rejected by production"})
		return
	}
	s.recordResponse(req.OperationID, http.StatusOK, replayed)
	writeJSON(w, http.StatusOK, deployResponse{
		OperationID: deployed.OperationID,
		Version:     deployed.Version,
		DeployedAt:  deployed.DeployedAt,
	})
}

// deploy finds the deployment req's operationId already made or, unless reject asks for
// the scripted refusal, applies req; either outcome is recorded as one deployed line. It
// reports the deployment, whether it was a replay, and false when req was refused and
// nothing changed.
func (s *Server) deploy(req deployRequest, reject bool) (deployment, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.deployments[req.OperationID]; ok {
		s.recordDeployed(existing, true)
		return existing, true, true
	}
	if reject {
		return deployment{}, false, false
	}

	t := *req.Target
	state, known := s.targets[t.key()]
	if !known {
		if len(s.targetOrder) >= maxTargets {
			delete(s.targets, s.targetOrder[0])
			s.targetOrder = s.targetOrder[1:]
		}
		s.targetOrder = append(s.targetOrder, t.key())
	}
	state.counter++
	state.version = "v" + strconv.Itoa(state.counter)
	state.patchDigest = req.PatchDigest
	state.lastOperationID = req.OperationID
	s.targets[t.key()] = state

	created := deployment{
		OperationID: req.OperationID,
		ApprovalID:  req.ApprovalID,
		Target:      t,
		BaseCommit:  req.BaseCommit,
		PatchDigest: req.PatchDigest,
		Version:     state.version,
		DeployedAt:  s.now().UTC().Format(time.RFC3339Nano),
	}
	if len(s.deploymentOrder) >= maxDeployments {
		delete(s.deployments, s.deploymentOrder[0])
		s.deploymentOrder = s.deploymentOrder[1:]
	}
	s.deploymentOrder = append(s.deploymentOrder, req.OperationID)
	s.deployments[req.OperationID] = created
	s.recordDeployed(created, false)
	return created, false, true
}

func (s *Server) handleDeployment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	found, ok := s.deployments[chi.URLParam(r, "operationId")]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Code: codeNotFound, Message: "unknown operationId"})
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (s *Server) handleTarget(w http.ResponseWriter, r *http.Request) {
	t := target{Service: chi.URLParam(r, "service"), Environment: chi.URLParam(r, "environment")}
	s.mu.Lock()
	state, ok := s.targets[t.key()]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Code: codeNotFound, Message: "target has no deployment"})
		return
	}
	writeJSON(w, http.StatusOK, targetResponse{
		Version:         state.version,
		PatchDigest:     state.patchDigest,
		LastOperationID: state.lastOperationID,
	})
}

// decodeDeployRequest decodes and validates one deployment request. It reports whether
// the parameters ask for a scripted rejection. The returned request carries whatever
// identifiers were decoded even when validation fails, so the record can name them.
func decodeDeployRequest(r *http.Request) (deployRequest, bool, error) {
	var req deployRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return deployRequest{}, false, errors.New("request body is not a valid deployment request")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return req, false, errors.New("request body has trailing data")
	}
	switch {
	case !operationIDPattern.MatchString(req.OperationID):
		return req, false, errors.New("operationId is missing or malformed")
	case req.ApprovalID != "" && !operationIDPattern.MatchString(req.ApprovalID):
		return req, false, errors.New("approvalId is malformed")
	case req.Target == nil:
		return req, false, errors.New("target is required")
	case !targetNamePattern.MatchString(req.Target.Service):
		return req, false, errors.New("target.service is missing or malformed")
	case !targetNamePattern.MatchString(req.Target.Environment):
		return req, false, errors.New("target.environment is missing or malformed")
	case req.BaseCommit == "" || len(req.BaseCommit) > maxDigestBytes:
		return req, false, errors.New("baseCommit is missing or too long")
	case req.PatchDigest == "" || len(req.PatchDigest) > maxDigestBytes:
		return req, false, errors.New("patchDigest is missing or too long")
	}
	reject, err := scriptedRejection(req.Parameters)
	if err != nil {
		return req, false, err
	}
	return req, reject, nil
}

// scriptedRejection reports whether parameters select the scripted rejection. Absent or
// null parameters select nothing; any other value must be an object, and a mock member,
// when present, must name a known scenario.
func scriptedRejection(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var parameters map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parameters); err != nil || parameters == nil {
		return false, errors.New("parameters must be an object")
	}
	control, ok := parameters[mockControlKey]
	if !ok {
		return false, nil
	}
	var scenario string
	if err := json.Unmarshal(control, &scenario); err != nil || scenario != mockReject {
		return false, errors.New(`parameters.mock may only be "reject"`)
	}
	return true, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
