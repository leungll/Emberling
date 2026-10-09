package api

import (
	"net/http"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// nodeTypes serves GET /node-types. Empty metadata collections are JSON arrays under
// the catalog interface contract, even when a registration stores nil slices.
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
	if metadata.FactInputs == nil {
		metadata.FactInputs = []string{}
	}
	return metadata
}

// models serves GET /models. capabilities is a JSON array under the catalog interface
// contract, even when a registration stores a nil slice.
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

// tools serves GET /tools. requires, each requirement's matchBindings and a declared
// production's bindArguments are JSON collections under the catalog interface contract,
// even when a registration stores nil; an undeclared production is omitted.
func (d Deps) tools(w http.ResponseWriter, r *http.Request) {
	items := d.Catalog.Tools()
	for i := range items {
		items[i] = toolMetadataForResponse(items[i])
	}
	writeJSON(w, http.StatusOK, listEnvelope[domain.ToolMetadata]{Items: items})
}

// toolMetadataForResponse builds new collections rather than filling the registered ones
// in place, because the registered slices and maps are shared with the Registry.
func toolMetadataForResponse(metadata domain.ToolMetadata) domain.ToolMetadata {
	requires := make([]domain.FactRequirement, len(metadata.Requires))
	for i, requirement := range metadata.Requires {
		if requirement.MatchBindings == nil {
			requirement.MatchBindings = []string{}
		}
		requires[i] = requirement
	}
	metadata.Requires = requires
	if metadata.Produces != nil {
		produces := *metadata.Produces
		if produces.BindArguments == nil {
			produces.BindArguments = map[string]domain.FactPointer{}
		}
		metadata.Produces = &produces
	}
	return metadata
}
