package api

import (
	"net/http"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// nodeTypes serves GET /node-types. Empty metadata collections are JSON arrays under
// the docs/08-interface-spec.md §2 contract, even when a registration stores nil slices.
func (d Deps) nodeTypes(w http.ResponseWriter, r *http.Request) {
	items := d.Catalog.NodeTypes()
	for i := range items {
		items[i] = nodeMetadataForResponse(items[i])
	}
	writeJSON(w, http.StatusOK, listEnvelope[domain.NodeMetadata]{Items: items})
}

func nodeMetadataForResponse(metadata domain.NodeMetadata) domain.NodeMetadata {
	if metadata.Inputs == nil {
		metadata.Inputs = []domain.PortMetadata{}
	}
	if metadata.Outputs == nil {
		metadata.Outputs = []domain.PortMetadata{}
	}
	if metadata.UISchema.Fields == nil {
		metadata.UISchema.Fields = []domain.UIField{}
	}
	return metadata
}

// models serves GET /models. capabilities is a JSON array under the docs/08-interface-spec.md
// §2 contract, even when a registration stores a nil slice.
func (d Deps) models(w http.ResponseWriter, r *http.Request) {
	items := d.Catalog.Models()
	for i := range items {
		items[i] = modelMetadataForResponse(items[i])
	}
	writeJSON(w, http.StatusOK, listEnvelope[domain.ModelMetadata]{Items: items})
}

func modelMetadataForResponse(metadata domain.ModelMetadata) domain.ModelMetadata {
	if metadata.Capabilities == nil {
		metadata.Capabilities = []string{}
	}
	return metadata
}

// tools serves GET /tools.
func (d Deps) tools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, listEnvelope[domain.ToolMetadata]{Items: d.Catalog.Tools()})
}
