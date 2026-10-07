package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewRouter builds the Emberling HTTP API: every core REST route, plus /health, /ready,
// the async Node callback contract (POST /api/callbacks), the Asset routes and the Agent
// Trace query.
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
			// so no storage location is ever handed to a client.
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
