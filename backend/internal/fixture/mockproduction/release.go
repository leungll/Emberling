package mockproduction

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
)

// This file is the asynchronous release surface under /v2/: a deployment is accepted first
// and completes later, can be voided before it starts, and the target reports its health.
// The /v1/ routes and their behaviour are untouched; the two surfaces share only the
// per-target version counter.

// Release statuses as the /v2/ routes report them.
const (
	releaseAccepted  = "ACCEPTED"
	releaseDeploying = "DEPLOYING"
	releaseDeployed  = "DEPLOYED"
	releaseVoided    = "VOIDED"
)

// kindRelease is the record kind of every /v2/ line, so a reader tells a release apart
// from a /v1/ deployment under the same operationId namespace.
const kindRelease = "release"

// Record events written by the release surface, besides the shared arrived/responded ones:
// deployed when a release completes, voided when an operation is recorded void (a known
// one or a tombstone), health when a target's health is decided by a completion, and
// responseDropped when the dropResponse fault closed the connection without answering.
const (
	recordVoided          = "voided"
	recordHealth          = "health"
	recordResponseDropped = "responseDropped"
)

// defaultCompletionDelay is how long an accepted release takes to complete when no
// WithCompletionDelay option is given.
const defaultCompletionDelay = 2 * time.Second

// defaultReleaseRejectCode is the code a reject fault answers with when none is set.
const defaultReleaseRejectCode = "REJECTED"

// maxReleases bounds how many releases the Server remembers, voided tombstones included.
const maxReleases = 10_000

// release is one accepted /v2/ deployment or a voided tombstone.
type release struct {
	OperationID string
	Target      target
	BaseCommit  string
	PatchDigest string
	Status      string
	Version     string
	timer       *time.Timer
}

// ReleaseFaults are the fault switches of the release surface. They are honoured only with
// test controls, so a default deployment never fails on purpose. Each switch stays on until
// cleared.
type ReleaseFaults struct {
	// DropResponse accepts the deployment and closes the connection without a response.
	DropResponse bool `json:"dropResponse"`
	// Unreachable answers every /v2/ request with 503.
	Unreachable bool `json:"unreachable"`
	// Reject refuses every new deployment with 409 and RejectCode.
	Reject     bool   `json:"reject"`
	RejectCode string `json:"rejectCode,omitempty"`
	// Unhealthy makes a completing deployment leave its target unhealthy.
	Unhealthy bool `json:"unhealthy"`
}

// completionGate holds accepted releases in DEPLOYING while completion is paused.
type completionGate struct {
	paused  bool
	waiting []string
}

type releaseResponse struct {
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
	Version     string `json:"version,omitempty"`
}

type releaseTargetResponse struct {
	Version         string `json:"version"`
	PatchDigest     string `json:"patchDigest"`
	Healthy         bool   `json:"healthy"`
	LastOperationID string `json:"lastOperationId"`
}

// healthDetail is the record's part of a health line.
type healthDetail struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
	Healthy     bool   `json:"healthy"`
}

// WithCompletionDelay sets how long an accepted release takes to complete. Zero completes
// it in the accepting request itself.
func WithCompletionDelay(delay time.Duration) Option {
	return func(s *Server) { s.completionDelay = delay }
}

// SetReleaseFaults replaces the fault switches. Without test controls it has no effect.
func (s *Server) SetReleaseFaults(faults ReleaseFaults) {
	if s.controls == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseFaults = faults
}

// PauseCompletion keeps every accepted release in DEPLOYING until ResumeCompletion.
func (s *Server) PauseCompletion() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completion.paused = true
}

// ResumeCompletion completes every release the pause held, in acceptance order, and lets
// later ones complete on their own.
func (s *Server) ResumeCompletion() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completion.paused = false
	waiting := s.completion.waiting
	s.completion.waiting = nil
	for _, operationID := range waiting {
		s.completeReleaseLocked(operationID)
	}
}

// unreachable is the /v2/ middleware for the unreachable fault.
func (s *Server) unreachable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		down := s.controls != nil && s.releaseFaults.Unreachable
		s.mu.Unlock()
		if down {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Code: codeUnavailable, Message: "production is unreachable"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleRelease accepts one asynchronous deployment. A known operationId answers with its
// current status and changes nothing; a voided one is refused with 409 VOIDED, so a request
// that arrives after its void can never deploy.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	req, _, err := decodeDeployRequest(r)
	arrival := arrivalDetail(req)
	if err != nil {
		s.rejectRelease(w, arrival, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	if err := s.admitRelease(r, arrival); err != nil {
		s.respondHeldError(w, req.OperationID, err)
		return
	}

	s.mu.Lock()
	if existing, ok := s.releases[req.OperationID]; ok {
		status, version := existing.Status, existing.Version
		s.mu.Unlock()
		if status == releaseVoided {
			s.recordReleaseResponse(req.OperationID, http.StatusConflict)
			writeJSON(w, http.StatusConflict, errorResponse{Code: releaseVoided, Message: "the operation was voided"})
			return
		}
		s.recordReleaseResponse(req.OperationID, http.StatusOK)
		writeJSON(w, http.StatusOK, releaseResponse{OperationID: req.OperationID, Status: status, Version: version})
		return
	}
	faults := s.releaseFaults
	if s.controls == nil {
		faults = ReleaseFaults{}
	}
	if faults.Reject {
		s.mu.Unlock()
		code := faults.RejectCode
		if code == "" {
			code = defaultReleaseRejectCode
		}
		s.recordReleaseResponse(req.OperationID, http.StatusConflict)
		writeJSON(w, http.StatusConflict, errorResponse{Code: code, Message: "deployment rejected by production"})
		return
	}
	accepted := &release{
		OperationID: req.OperationID, Target: *req.Target, BaseCommit: req.BaseCommit,
		PatchDigest: req.PatchDigest, Status: releaseAccepted,
	}
	s.rememberReleaseLocked(accepted)
	s.scheduleCompletionLocked(accepted)
	s.mu.Unlock()

	if faults.DropResponse {
		s.dropResponse(w, req.OperationID)
		return
	}
	s.recordReleaseResponse(req.OperationID, http.StatusAccepted)
	writeJSON(w, http.StatusAccepted, releaseResponse{OperationID: req.OperationID, Status: releaseAccepted})
}

func (s *Server) handleReleaseStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	found, ok := s.releases[chi.URLParam(r, "operationId")]
	var response releaseResponse
	if ok {
		response = releaseResponse{OperationID: found.OperationID, Status: found.Status, Version: found.Version}
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Code: codeNotFound, Message: "unknown operationId"})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleReleaseVoid voids an operation that has not started: an unknown one becomes a
// tombstone that refuses a later arrival, an ACCEPTED one is voided and its completion
// cancelled. An operation that started or finished keeps its status, which is answered.
// Voiding is idempotent.
func (s *Server) handleReleaseVoid(w http.ResponseWriter, r *http.Request) {
	operationID := chi.URLParam(r, "operationId")
	if !operationIDPattern.MatchString(operationID) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: codeInvalidRequest, Message: "operationId is malformed"})
		return
	}
	s.mu.Lock()
	found, ok := s.releases[operationID]
	switch {
	case !ok:
		found = &release{OperationID: operationID, Status: releaseVoided}
		s.rememberReleaseLocked(found)
		s.recordVoidedLocked(found)
	case found.Status == releaseAccepted:
		if found.timer != nil {
			found.timer.Stop()
			found.timer = nil
		}
		found.Status = releaseVoided
		s.recordVoidedLocked(found)
	}
	response := releaseResponse{OperationID: found.OperationID, Status: found.Status, Version: found.Version}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleReleaseTarget(w http.ResponseWriter, r *http.Request) {
	t := target{Service: chi.URLParam(r, "service"), Environment: chi.URLParam(r, "environment")}
	s.mu.Lock()
	state, ok := s.targets[t.key()]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Code: codeNotFound, Message: "target has no deployment"})
		return
	}
	writeJSON(w, http.StatusOK, releaseTargetResponse{
		Version: state.version, PatchDigest: state.patchDigest, Healthy: !state.unhealthy, LastOperationID: state.lastOperationID,
	})
}

// rememberReleaseLocked stores rel, forgetting the oldest release once the bound is reached.
// The caller holds s.mu.
func (s *Server) rememberReleaseLocked(rel *release) {
	if len(s.releaseOrder) >= maxReleases {
		oldest := s.releaseOrder[0]
		if old := s.releases[oldest]; old != nil && old.timer != nil {
			old.timer.Stop()
		}
		delete(s.releases, oldest)
		s.releaseOrder = s.releaseOrder[1:]
	}
	s.releaseOrder = append(s.releaseOrder, rel.OperationID)
	s.releases[rel.OperationID] = rel
}

// scheduleCompletionLocked arranges rel's completion after the configured delay, or
// completes it at once when the delay is zero. The caller holds s.mu.
func (s *Server) scheduleCompletionLocked(rel *release) {
	if s.completionDelay <= 0 {
		s.completeReleaseLocked(rel.OperationID)
		return
	}
	operationID := rel.OperationID
	rel.timer = time.AfterFunc(s.completionDelay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.completeReleaseLocked(operationID)
	})
}

// completeReleaseLocked finishes an ACCEPTED or DEPLOYING release: it becomes DEPLOYED with
// the target's next version and the target's health follows the unhealthy fault. While
// completion is paused the release stays in DEPLOYING and is finished by ResumeCompletion.
// A release that was voided or already finished is left alone. The caller holds s.mu.
func (s *Server) completeReleaseLocked(operationID string) {
	rel, ok := s.releases[operationID]
	if !ok || (rel.Status != releaseAccepted && rel.Status != releaseDeploying) {
		return
	}
	rel.timer = nil
	if s.completion.paused {
		if rel.Status == releaseAccepted {
			rel.Status = releaseDeploying
			s.completion.waiting = append(s.completion.waiting, operationID)
		}
		return
	}
	unhealthy := s.controls != nil && s.releaseFaults.Unhealthy
	state := s.advanceTarget(rel.Target, rel.OperationID, rel.PatchDigest, unhealthy)
	rel.Status = releaseDeployed
	rel.Version = state.version
	s.recordReleaseDeployedLocked(rel)
	s.recordHealthLocked(rel.Target, state)
}

// stopCompletions cancels every pending completion timer.
func (s *Server) stopCompletions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rel := range s.releases {
		if rel.timer != nil {
			rel.timer.Stop()
			rel.timer = nil
		}
	}
}

// dropResponse closes the connection without writing a response, reproducing an answer
// lost on the network after production accepted the deployment.
func (s *Server) dropResponse(w http.ResponseWriter, operationID string) {
	if s.controls != nil {
		_, _ = s.controls.record.Append(mockcontrol.Line{
			Event: recordResponseDropped, Kind: kindRelease,
			Detail: requestDetail{OperationID: truncateSummary(operationID)},
		})
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		// The connection cannot be taken over; failing the handler still answers nothing
		// useful to the caller.
		panic(http.ErrAbortHandler)
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	_ = conn.Close()
}

// admitRelease records the arrival of one release request under its own kind and, while
// the barrier is paused for releases, holds it until release.
func (s *Server) admitRelease(r *http.Request, arrival requestDetail) error {
	c := s.controls
	if c == nil {
		return nil
	}
	req := mockcontrol.Request{Kind: kindRelease, Detail: arrival, Ref: requestDetail{OperationID: arrival.OperationID}}
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

func (s *Server) rejectRelease(w http.ResponseWriter, arrival requestDetail, status int, code, message string) {
	if s.controls != nil {
		_, _ = s.controls.record.Append(mockcontrol.Line{Event: mockcontrol.EventArrived, Kind: kindRelease, Detail: arrival})
		s.recordReleaseResponse(arrival.OperationID, status)
	}
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}

func (s *Server) recordReleaseResponse(operationID string, status int) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.Append(mockcontrol.Line{
		Event: mockcontrol.EventResponded, Kind: kindRelease,
		Detail: requestDetail{OperationID: truncateSummary(operationID), Status: status},
	})
}

// recordReleaseDeployedLocked writes the deployed line of a completed release. The caller
// holds s.mu.
func (s *Server) recordReleaseDeployedLocked(rel *release) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.Append(mockcontrol.Line{
		Event: recordDeployed, Kind: kindRelease,
		Detail: deployedDetail{
			OperationID: rel.OperationID, Service: rel.Target.Service, Environment: rel.Target.Environment,
			BaseCommit: rel.BaseCommit, PatchDigest: rel.PatchDigest, Version: rel.Version,
		},
	})
}

func (s *Server) recordVoidedLocked(rel *release) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.Append(mockcontrol.Line{
		Event: recordVoided, Kind: kindRelease,
		Detail: requestDetail{OperationID: rel.OperationID, Service: rel.Target.Service, Environment: rel.Target.Environment},
	})
}

func (s *Server) recordHealthLocked(t target, state targetState) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.Append(mockcontrol.Line{
		Event: recordHealth, Kind: kindRelease,
		Detail: healthDetail{Service: t.Service, Environment: t.Environment, Version: state.version, Healthy: !state.unhealthy},
	})
}

// replayedLine is the subset of a record line the replay reads.
type replayedLine struct {
	Event       string `json:"event"`
	Kind        string `json:"kind"`
	OperationID string `json:"operationId"`
	Service     string `json:"service"`
	Environment string `json:"environment"`
	BaseCommit  string `json:"baseCommit"`
	PatchDigest string `json:"patchDigest"`
	Version     string `json:"version"`
	Healthy     *bool  `json:"healthy"`
}

// replayReleases rebuilds the release and target state an earlier process persisted in the
// record: completed releases, voided operations and target health. Releases that were only
// accepted when that process stopped are not in the record and so are unknown afterwards,
// exactly as a production service that lost its in-flight work would report them.
func (s *Server) replayReleases() {
	_ = mockcontrol.ScanRecord(s.controls.record.Path(), func(raw []byte) {
		var line replayedLine
		if err := json.Unmarshal(raw, &line); err != nil || line.Kind != kindRelease {
			return
		}
		t := target{Service: line.Service, Environment: line.Environment}
		switch line.Event {
		case recordDeployed:
			rel := &release{
				OperationID: line.OperationID, Target: t, BaseCommit: line.BaseCommit, PatchDigest: line.PatchDigest,
				Status: releaseDeployed, Version: line.Version,
			}
			s.rememberReleaseLocked(rel)
			state := s.advanceTarget(t, rel.OperationID, rel.PatchDigest, false)
			if n, err := strconv.Atoi(strings.TrimPrefix(line.Version, "v")); err == nil && n > state.counter {
				state.counter, state.version = n, line.Version
				s.targets[t.key()] = state
			}
		case recordVoided:
			s.rememberReleaseLocked(&release{OperationID: line.OperationID, Target: t, Status: releaseVoided})
		case recordHealth:
			if state, ok := s.targets[t.key()]; ok && line.Healthy != nil && state.version == line.Version {
				state.unhealthy = !*line.Healthy
				s.targets[t.key()] = state
			}
		}
	})
}

// handleReleaseFaults replaces the fault switches over HTTP; an empty body clears them.
func (s *Server) handleReleaseFaults(w http.ResponseWriter, r *http.Request) {
	var faults ReleaseFaults
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil || (len(body) > 0 && json.Unmarshal(body, &faults) != nil) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: codeInvalidRequest, Message: "body must be a faults object"})
		return
	}
	s.SetReleaseFaults(faults)
	writeJSON(w, http.StatusOK, faults)
}

// handleReleaseCompletion pauses or resumes completion over HTTP: {"paused": true|false}.
func (s *Server) handleReleaseCompletion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: codeInvalidRequest, Message: "body must name paused"})
		return
	}
	if req.Paused {
		s.PauseCompletion()
	} else {
		s.ResumeCompletion()
	}
	writeJSON(w, http.StatusOK, req)
}
