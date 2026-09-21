package service

import "github.com/leungll/Emberling/backend/internal/domain"

// CatalogService serves the read-only Node/Model/Tool Registry metadata Studio and the
// API need (docs/08-interface-spec.md §2). It never returns an Executor, Adapter or
// Provider credential: the underlying Registries' ListMetadata methods already strip
// those.
type CatalogService struct {
	deps Deps
}

// NewCatalogService builds a CatalogService from the shared Deps skeleton.
func NewCatalogService(deps Deps) *CatalogService {
	return &CatalogService{deps: deps.withDefaults()}
}

// NodeTypes returns every registered Node Type, sorted by Type for a deterministic
// catalogue (registry.NodeRegistry.ListMetadata).
func (s *CatalogService) NodeTypes() []domain.NodeMetadata {
	return s.deps.Nodes.ListMetadata()
}

// Models returns the model catalogue of the single registered Model Provider, sorted by
// ID (registry.ModelRegistry.ListMetadata).
func (s *CatalogService) Models() []domain.ModelMetadata {
	return s.deps.Models.ListMetadata()
}

// Tools returns every registered Tool's Metadata, sorted by Name
// (registry.ToolRegistry.ListMetadata).
func (s *CatalogService) Tools() []domain.ToolMetadata {
	return s.deps.Tools.ListMetadata()
}
