package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewRouter builds the Emberling HTTP API: every M1-scope route from
// docs/08-interface-spec.md §3, plus /health, /ready, the M2 async Node callback contract
// (§4, POST /api/callbacks), the M5 Asset routes (§3.2) and the M3 Agent Trace query
// (§3.4).
func NewRouter(deps Deps) http.Handler {
	deps = deps.withDefaults()

	r := chi.NewRouter()

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeErrorEnvelope(w, http.StatusNotFound, codeNotFoundRoute, "no route matches this request", nil)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		writeErrorEnvelope(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed on this route", nil)
	})

	r.Get("/health", deps.health)
	r.Get("/ready", deps.ready)

	r.Route("/api", func(r chi.Router) {
		r.Route("/definitions", func(r chi.Router) {
			r.Get("/", deps.listDefinitions)
			r.Post("/", deps.createDefinition)
			r.Post("/validate", deps.validateDefinition)
			r.Get("/{workflowId}", deps.getDefinition)
			r.Put("/{workflowId}", deps.saveDefinition)
			r.Get("/{workflowId}/versions/{version}", deps.getDefinitionVersion)
		})

		r.Get("/node-types", deps.nodeTypes)
		r.Get("/models", deps.models)
		r.Get("/tools", deps.tools)

		r.Post("/callbacks", deps.handleCallback)

		r.Route("/assets", func(r chi.Router) {
			r.Post("/", deps.uploadAsset)
			r.Get("/{assetId}", deps.getAsset)
			// Content is served by the Backend itself rather than through a signed URL,
			// so no storage location is ever handed to a client (10-ops §4).
			r.Get("/{assetId}/content", deps.getAssetContent)
		})

		r.Route("/runs", func(r chi.Router) {
			r.Post("/", deps.createRun)
			r.Get("/{runId}", deps.getRunSnapshot)
			r.Get("/{runId}/events", deps.handleEvents)
			r.Get("/{runId}/nodes/{nodeRunId}", deps.getNodeRunDetail)
			r.Get("/{runId}/nodes/{nodeRunId}/agent", deps.getAgentTrace)
		})
	})

	return r
}
