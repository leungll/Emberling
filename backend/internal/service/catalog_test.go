package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// catalogFakeModelProvider is a minimal registry.ModelProvider for CatalogService.Models
// fixtures. Generate is never called: CatalogService.Models only reads the Metadata that
// Models(ctx) hands to registry.ModelRegistry.Register, matching how
// defsFakeNodeExecutor above is only a Metadata carrier for the Node Type case.
type catalogFakeModelProvider struct {
	models []registry.ModelRegistration
}

func (p catalogFakeModelProvider) Models(context.Context) ([]registry.ModelRegistration, error) {
	return p.models, nil
}

func (catalogFakeModelProvider) Generate(context.Context, registry.ModelRequest) (registry.ModelResponse, error) {
	return registry.ModelResponse{}, errDefsFakeNotImplemented
}

// TestCatalogService_Models_ReturnsRegisteredModelMetadata closes the second half of gap 4
// (docs/09-testing-and-acceptance.md §3.4, row "`/models` 查询 Model Registry": expected
// outcome "返回模型 ConfigSchema；Studio 与 Backend 不维护第二套参数类型、默认值或校验规则").
// CatalogService.Models must hand back exactly the ConfigSchema (and other Metadata) the
// Model Provider itself registered -- not a re-derived or re-validated copy -- since a
// second, backend-maintained parameter schema is exactly what the doc row forbids.
func TestCatalogService_Models_ReturnsRegisteredModelMetadata(t *testing.T) {
	models := registry.NewModelRegistry()
	schemaA := json.RawMessage(`{"type":"object","properties":{"temperature":{"type":"number","default":0.2}}}`)
	schemaB := json.RawMessage(`{"type":"object","properties":{"maxTokens":{"type":"integer","default":256}}}`)

	provider := catalogFakeModelProvider{models: []registry.ModelRegistration{
		{ModelMetadata: domain.ModelMetadata{
			ID:           "model-b",
			DisplayName:  "Model B",
			Capabilities: []string{domain.ModelCapabilityTextGeneration},
			ConfigSchema: schemaB,
		}},
		{ModelMetadata: domain.ModelMetadata{
			ID:           "model-a",
			DisplayName:  "Model A",
			Capabilities: []string{domain.ModelCapabilityTextGeneration, domain.ModelCapabilityStructuredDecision},
			ConfigSchema: schemaA,
		}},
	}}
	if err := models.Register(context.Background(), provider); err != nil {
		t.Fatalf("register model provider: %v", err)
	}

	svc := NewCatalogService(Deps{Nodes: registry.NewNodeRegistry(), Models: models, Tools: registry.NewToolRegistry()})

	got := svc.Models()
	if len(got) != 2 {
		t.Fatalf("Models() = %d entries, want 2", len(got))
	}
	// registry.ModelRegistry.ListMetadata sorts by ID; the provider above registers
	// model-b before model-a on purpose so this test would fail if CatalogService.Models
	// forwarded ListMetadata's raw map order instead.
	if got[0].ID != "model-a" || got[1].ID != "model-b" {
		t.Fatalf("Models() order = [%q, %q], want [model-a, model-b]", got[0].ID, got[1].ID)
	}

	modelA := got[0]
	if modelA.DisplayName != "Model A" {
		t.Errorf("Models()[0].DisplayName = %q, want %q", modelA.DisplayName, "Model A")
	}
	if string(modelA.ConfigSchema) != string(schemaA) {
		t.Errorf("Models()[0].ConfigSchema = %s, want exactly the registered schema %s (Backend must not maintain a second parameter schema)", modelA.ConfigSchema, schemaA)
	}
	if len(modelA.Capabilities) != 2 {
		t.Errorf("Models()[0].Capabilities = %v, want 2 entries as registered", modelA.Capabilities)
	}
}

// Note on the second sub-case the task names (an unregistered-but-similarly-named node
// type resolved to the nearest registration instead of failing): this is already covered
// by internal/runtime.TestCompiler_UnknownNodeType_Fails (internal/runtime/compiler_test.go),
// and verified here to actually be the same code path rather than a coincidentally
// adjacent one. registry.NodeRegistry.Get and NodeMetadata (internal/registry/registry.go)
// resolve a Node Type with a single exact map lookup, entries[nodeType] -- there is no
// heuristic, prefix, or capability-based fallback anywhere in the registry for
// TestCompiler_UnknownNodeType_Fails's "does_not_exist" case to have accidentally bypassed.
// A node type that merely looks similar to a registered one (e.g. "text_generation_v2")
// would miss that same map lookup exactly like "does_not_exist" does, producing the same
// CodeUnknownNodeType at the same STRUCTURE stage -- so a second test using a
// similar-looking name would exercise an identical path to the existing test, not a
// distinct one. No second test is added for this sub-case.
