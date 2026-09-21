package api

import "net/http"

// health serves GET /health: process liveness. It must never fail because a Run failed
// or because work is backing up (docs/10-ops.md §1) -- only Probe.Live, which turns false
// solely once shutdown has begun.
func (d Deps) health(w http.ResponseWriter, r *http.Request) {
	if !d.Readiness.Live() {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, codeDependencyUnavailable,
			"the process is shutting down", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready serves GET /ready: whether the full startup gate (docs/10-ops.md §1, steps 1-7)
// has passed. cmd/emberling drives readiness.Probe.Run once at startup and keeps
// reconciler.Reconciler.Run alive for as long as the process accepts requests; this
// handler only ever reads the Probe's current outcome, never re-runs the gate itself.
func (d Deps) ready(w http.ResponseWriter, r *http.Request) {
	if !d.Readiness.Ready() {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, codeDependencyUnavailable,
			"a required startup dependency is not ready", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
