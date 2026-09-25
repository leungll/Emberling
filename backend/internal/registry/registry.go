package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// NodeRegistry resolves a stable Node Type to its NodeRegistration. Registration happens
// once at Backend startup (10 §1 step 3); Get, NodeMetadata and ListMetadata are read
// paths used by the Compiler, the API and Studio after readiness.
type NodeRegistry struct {
	mu      sync.Mutex
	entries map[string]NodeRegistration
}

// NewNodeRegistry returns an empty NodeRegistry. It holds no database handle, HTTP client
// or Provider credential: only registered metadata and Bindings.
func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{entries: make(map[string]NodeRegistration)}
}

// Register validates one Node registration and adds it if valid. It reports every
// violation of 07 §1.1 at once so a broken registration does not need one restart per
// mistake; messages name the Node Type and the rule, never a schema body or credential.
func (r *NodeRegistry) Register(reg NodeRegistration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	if err := reg.Metadata.Validate(); err != nil {
		errs = append(errs, err)
	}
	if reg.Metadata.Type != "" {
		if _, exists := r.entries[reg.Metadata.Type]; exists {
			errs = append(errs, fmt.Errorf("node type %q: already registered", reg.Metadata.Type))
		}
	}
	errs = append(errs, validateNodeBinding(reg.Metadata, reg.Binding)...)
	if len(reg.Metadata.ConfigSchema) > 0 {
		if _, err := CompileSchema(reg.Metadata.ConfigSchema); err != nil {
			errs = append(errs, fmt.Errorf("node type %q: configSchema: %w", reg.Metadata.Type, err))
		} else {
			errs = append(errs, validateNodeUIFields(reg.Metadata.Type, reg.Metadata.ConfigSchema, reg.Metadata.UISchema)...)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("register node type %q: %w", reg.Metadata.Type, errors.Join(errs...))
	}
	r.entries[reg.Metadata.Type] = reg
	return nil
}

// validateNodeBinding checks Binding presence and its agreement with ExecutionKind (07
// §1.1/§1.2): SYNC and ASYNC require an ExecutorBinding, MANAGED_AGENT requires a
// ManagedAgentBinding, and an ASYNC Executor must implement AsyncNodeExecutor.
func validateNodeBinding(metadata domain.NodeMetadata, binding NodeBinding) []error {
	var errs []error
	if binding == nil {
		errs = append(errs, fmt.Errorf("node type %q: binding is missing", metadata.Type))
		return errs
	}

	switch metadata.ExecutionKind {
	case domain.NodeExecutionSync, domain.NodeExecutionAsync:
		executorBinding, ok := binding.(ExecutorBinding)
		if !ok {
			errs = append(errs, fmt.Errorf("node type %q: executionKind %s requires an ExecutorBinding", metadata.Type, metadata.ExecutionKind))
			break
		}
		if executorBinding.Executor == nil {
			errs = append(errs, fmt.Errorf("node type %q: executor is missing", metadata.Type))
			break
		}
		if metadata.ExecutionKind == domain.NodeExecutionAsync {
			if _, ok := executorBinding.Executor.(AsyncNodeExecutor); !ok {
				errs = append(errs, fmt.Errorf("node type %q: executionKind ASYNC requires an AsyncNodeExecutor", metadata.Type))
			}
		}
	case domain.NodeExecutionManagedAgent:
		if _, ok := binding.(ManagedAgentBinding); !ok {
			errs = append(errs, fmt.Errorf("node type %q: executionKind MANAGED_AGENT requires a ManagedAgentBinding", metadata.Type))
		}
	default:
		// Invalid ExecutionKind is already reported by domain.NodeMetadata.Validate.
	}
	return errs
}

// validateNodeUIFields checks every UIField against the compiled ConfigSchema (07 §1.1, 04
// §2.1-2.3): each Path must resolve to a declared top-level property, paths must be
// unique, SELECT needs enum, MODEL_SELECTOR needs a string property and a non-empty
// capability, TOOL_SELECTOR needs an array of strings, and TEXTAREA/PROMPT_EDITOR need a
// string property.
func validateNodeUIFields(nodeType string, configSchema []byte, ui domain.NodeUISchema) []error {
	var errs []error
	seen := make(map[string]struct{}, len(ui.Fields))
	for _, field := range ui.Fields {
		if field.Path == "" {
			// Reported by domain.NodeMetadata.Validate already.
			continue
		}
		if _, duplicate := seen[field.Path]; duplicate {
			errs = append(errs, fmt.Errorf("node type %q: uiSchema field %q: duplicate path", nodeType, field.Path))
			continue
		}
		seen[field.Path] = struct{}{}

		property, err := schemaProperty(configSchema, field.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("node type %q: uiSchema field %q: %w", nodeType, field.Path, err))
			continue
		}

		switch field.Widget {
		case domain.UIWidgetSelect:
			if !propertyHasEnum(property) {
				errs = append(errs, fmt.Errorf("node type %q: uiSchema field %q: widget SELECT requires configSchema enum", nodeType, field.Path))
			}
		case domain.UIWidgetModelSelector:
			if !propertyHasType(property, "string") {
				errs = append(errs, fmt.Errorf("node type %q: uiSchema field %q: widget MODEL_SELECTOR requires a string property", nodeType, field.Path))
			}
			if field.Capability == "" {
				errs = append(errs, fmt.Errorf("node type %q: uiSchema field %q: widget MODEL_SELECTOR requires a non-empty capability", nodeType, field.Path))
			}
		case domain.UIWidgetToolSelector:
			if !propertyHasStringItems(property) {
				errs = append(errs, fmt.Errorf("node type %q: uiSchema field %q: widget TOOL_SELECTOR requires an array of strings", nodeType, field.Path))
			}
		case domain.UIWidgetTextArea, domain.UIWidgetPromptEditor:
			if !propertyHasType(property, "string") {
				errs = append(errs, fmt.Errorf("node type %q: uiSchema field %q: widget %s requires a string property", nodeType, field.Path, field.Widget))
			}
		}
	}
	return errs
}

// Get returns the full registration for a Node Type.
func (r *NodeRegistry) Get(nodeType string) (NodeRegistration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.entries[nodeType]
	return reg, ok
}

// NodeMetadata returns only the registered NodeMetadata, the shape Studio and the API
// read. Named NodeMetadata (not Metadata) so that *NodeRegistry satisfies
// runtime.NodeCatalog directly.
func (r *NodeRegistry) NodeMetadata(nodeType string) (domain.NodeMetadata, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.entries[nodeType]
	if !ok {
		return domain.NodeMetadata{}, false
	}
	return reg.Metadata, true
}

// ListMetadata returns every registered NodeMetadata sorted by Type, so Studio and the API
// see a deterministic node catalogue.
func (r *NodeRegistry) ListMetadata() []domain.NodeMetadata {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.NodeMetadata, 0, len(r.entries))
	for _, reg := range r.entries {
		out = append(out, reg.Metadata)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// ValidateSemantics dispatches to the registered Executor's ValidateSemantics (07 §1.1).
// It runs only after ConfigSchema validation already passed elsewhere; a MANAGED_AGENT
// binding has no cross-field semantic check of its own in the MVP and returns nil.
func (r *NodeRegistry) ValidateSemantics(ctx context.Context, nodeType string, config map[string]any) error {
	reg, ok := r.Get(nodeType)
	if !ok {
		return fmt.Errorf("node type %q: not registered", nodeType)
	}
	switch binding := reg.Binding.(type) {
	case ExecutorBinding:
		return binding.Executor.ValidateSemantics(ctx, config)
	case ManagedAgentBinding:
		return nil
	default:
		return fmt.Errorf("node type %q: binding has no recognised kind", nodeType)
	}
}

// ModelRegistry resolves a stable Model ID to its registration and the ModelProvider that
// serves it. MVP registers exactly one ModelProvider, but that Provider may declare
// several compatible models (07 §1.4).
type ModelRegistry struct {
	mu      sync.Mutex
	entries map[string]modelEntry
}

type modelEntry struct {
	registration ModelRegistration
	provider     ModelProvider
}

// NewModelRegistry returns an empty ModelRegistry.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{entries: make(map[string]modelEntry)}
}

// Register calls provider.Models(ctx) once and indexes every returned ModelRegistration by
// ID. It rejects an empty or duplicate ID and a ConfigSchema that does not compile; a
// Registry that fails this check must not let the Backend become ready (10 §1 step 3).
func (r *ModelRegistry) Register(ctx context.Context, provider ModelProvider) error {
	if provider == nil {
		return fmt.Errorf("register model provider: provider is nil")
	}
	models, err := provider.Models(ctx)
	if err != nil {
		return fmt.Errorf("register model provider: list models: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	staged := make(map[string]modelEntry, len(models))
	for _, model := range models {
		var modelErrs []error
		if model.ID == "" {
			modelErrs = append(modelErrs, fmt.Errorf("model id is empty"))
		} else if _, exists := r.entries[model.ID]; exists {
			modelErrs = append(modelErrs, fmt.Errorf("model %q: already registered", model.ID))
		} else if _, exists := staged[model.ID]; exists {
			modelErrs = append(modelErrs, fmt.Errorf("model %q: duplicate id in provider response", model.ID))
		}
		if err := validateSchemaJSON(model.ConfigSchema); err != nil {
			modelErrs = append(modelErrs, fmt.Errorf("model %q: configSchema: %w", model.ID, err))
		}
		if len(modelErrs) > 0 {
			errs = append(errs, modelErrs...)
			continue
		}
		staged[model.ID] = modelEntry{registration: model, provider: provider}
	}

	if len(errs) > 0 {
		return fmt.Errorf("register model provider: %w", errors.Join(errs...))
	}
	for id, entry := range staged {
		r.entries[id] = entry
	}
	return nil
}

// Get returns the registration and the ModelProvider serving one Model ID.
func (r *ModelRegistry) Get(id string) (ModelRegistration, ModelProvider, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[id]
	if !ok {
		return ModelRegistration{}, nil, false
	}
	return entry.registration, entry.provider, true
}

// ListMetadata returns every registered ModelMetadata sorted by ID.
func (r *ModelRegistry) ListMetadata() []domain.ModelMetadata {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.ModelMetadata, 0, len(r.entries))
	for _, entry := range r.entries {
		out = append(out, entry.registration.ModelMetadata)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// validateSchemaJSON reports whether raw is a non-empty, compilable JSON Schema.
func validateSchemaJSON(raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("schema is empty")
	}
	_, err := CompileSchema(raw)
	return err
}

// ToolRegistry resolves a stable Tool Name to its ToolRegistration (07 §1.5). A single
// lookup always returns Metadata and Executor together; Studio and the API must read
// ListMetadata only and never obtain an Executor.
type ToolRegistry struct {
	mu      sync.Mutex
	entries map[string]ToolRegistration
}

// NewToolRegistry returns an empty ToolRegistry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{entries: make(map[string]ToolRegistration)}
}

// Register validates one Tool registration and adds it if valid (07 §1.5): the name must
// be non-empty and unique, both Schemas must compile, an Executor must be present, and an
// ASYNC Tool must implement AsyncToolExecutor.
func (r *ToolRegistry) Register(reg ToolRegistration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	if err := reg.Metadata.Validate(); err != nil {
		errs = append(errs, err)
	}
	if reg.Metadata.Name != "" {
		if _, exists := r.entries[reg.Metadata.Name]; exists {
			errs = append(errs, fmt.Errorf("tool %q: already registered", reg.Metadata.Name))
		}
	}
	if len(reg.Metadata.InputSchema) > 0 {
		if _, err := CompileSchema(reg.Metadata.InputSchema); err != nil {
			errs = append(errs, fmt.Errorf("tool %q: inputSchema: %w", reg.Metadata.Name, err))
		}
	}
	if len(reg.Metadata.OutputSchema) > 0 {
		if _, err := CompileSchema(reg.Metadata.OutputSchema); err != nil {
			errs = append(errs, fmt.Errorf("tool %q: outputSchema: %w", reg.Metadata.Name, err))
		}
	}
	if reg.Executor == nil {
		errs = append(errs, fmt.Errorf("tool %q: executor is missing", reg.Metadata.Name))
	} else if reg.Metadata.ExecutionKind == domain.ToolExecutionAsync {
		if _, ok := reg.Executor.(AsyncToolExecutor); !ok {
			errs = append(errs, fmt.Errorf("tool %q: executionKind ASYNC requires an AsyncToolExecutor", reg.Metadata.Name))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("register tool %q: %w", reg.Metadata.Name, errors.Join(errs...))
	}
	r.entries[reg.Metadata.Name] = reg
	return nil
}

// Get returns the Metadata and Executor for one Tool Name.
func (r *ToolRegistry) Get(name string) (ToolRegistration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.entries[name]
	return reg, ok
}

// ListMetadata returns every registered ToolMetadata sorted by Name. It never exposes an
// Executor: callers that need to invoke a Tool must use Get.
func (r *ToolRegistry) ListMetadata() []domain.ToolMetadata {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.ToolMetadata, 0, len(r.entries))
	for _, reg := range r.entries {
		out = append(out, reg.Metadata)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
