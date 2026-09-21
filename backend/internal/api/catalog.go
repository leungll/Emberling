package api

import (
	"net/http"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// nodeTypes serves GET /node-types. domain.NodeMetadata already carries the wire-matching
// JSON tags docs/08-interface-spec.md §2 defines, so no DTO conversion is needed.
func (d Deps) nodeTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, listEnvelope[domain.NodeMetadata]{Items: d.Catalog.NodeTypes()})
}

// models serves GET /models.
func (d Deps) models(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, listEnvelope[domain.ModelMetadata]{Items: d.Catalog.Models()})
}

// tools serves GET /tools.
func (d Deps) tools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, listEnvelope[domain.ToolMetadata]{Items: d.Catalog.Tools()})
}
