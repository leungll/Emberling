package api

import "net/http"

// BuildInfo is the build version and source commit of the running binary. Both are
// build-time labels, never configuration or credentials.
type BuildInfo struct {
	Version string
	Commit  string
}

// unknownBuild is reported for a field the build did not inject.
const unknownBuild = "unknown"

type probeResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// probeBody reports status with the build labels. The labels never influence the
// liveness or readiness outcome.
func (d Deps) probeBody(status string) probeResponse {
	out := probeResponse{Status: status, Version: d.Build.Version, Commit: d.Build.Commit}
	if out.Version == "" {
		out.Version = unknownBuild
	}
	if out.Commit == "" {
		out.Commit = unknownBuild
	}
	return out
}

// health serves GET /health: process liveness. It must never fail because a Run failed
// or because work is backing up -- only Probe.Live, which turns false
// solely once shutdown has begun.
func (d Deps) health(w http.ResponseWriter, r *http.Request) {
	if !d.Readiness.Live() {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, codeDependencyUnavailable,
			"the process is shutting down", nil)
		return
	}
	writeJSON(w, http.StatusOK, d.probeBody("ok"))
}

// ready serves GET /ready: whether the full startup gate (steps 1-7)
// has passed. cmd/emberling drives readiness.Probe.Run once at startup and keeps
// reconciler.Reconciler.Run alive for as long as the process accepts requests; this
// handler only ever reads the Probe's current outcome, never re-runs the gate itself.
func (d Deps) ready(w http.ResponseWriter, r *http.Request) {
	if !d.Readiness.Ready() {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, codeDependencyUnavailable,
			"a required startup dependency is not ready", nil)
		return
	}
	writeJSON(w, http.StatusOK, d.probeBody("ready"))
}
