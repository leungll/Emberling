package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// fakeCatalog is the M1 test-only NodeCatalog. It registers the 8 MVP Node Types
// (docs/02-scope.md §3.2) with shapes that match the two fixture Definitions plus a few
// deliberate test hooks:
//
//   - text_generation is the one type whose ConfigSchema requires "modelId" (used by
//     TestCompiler_ConfigSchemaViolation_StopsBeforeSemantics); image_generation's
//     ConfigSchema is deliberately permissive (does not require modelId/width), unlike
//     the illustrative example in docs/08-interface-spec.md §2 -- a fixture-only choice,
//     not a claim about the real Registry's future image_generation registration.
//   - text_output additionally declares an optional "note" input port and (test-only) a
//     "text" output port, neither of which exists on a production Output Node; the extra
//     input port lets a test build two distinct Input Nodes feeding one Output Node
//     without triggering AMBIGUOUS_INPUT, and the extra output port lets a test build a
//     syntactically valid edge leaving an Output Node to exercise
//     OUTPUT_NODE_NOT_UNIQUE_SINK. Both are noted in the final report as fixture-only
//     deviations from the real Output Node contract.
//   - Any node config carrying `"__forceSemanticError": true` fails ValidateSemantics
//     regardless of node type, as a generic hook for exercising the Semantics stage.
type fakeCatalog struct {
	metadata map[string]domain.NodeMetadata
	// semanticsCalls counts ValidateSemantics invocations, so a test can assert Compile
	// stopped before the Semantics stage ran at all.
	semanticsCalls int
}

func newFakeCatalog() *fakeCatalog {
	permissiveSchema := json.RawMessage(`{"type":"object"}`)
	textInputSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"inputKey": {"type": "string"},
			"label": {"type": "string"},
			"required": {"type": "boolean"},
			"minLength": {"type": "integer"},
			"maxLength": {"type": "integer"}
		}
	}`)
	imageInputSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"inputKey": {"type": "string"},
			"label": {"type": "string"},
			"required": {"type": "boolean"},
			"acceptedMediaTypes": {"type": "array", "items": {"type": "string"}},
			"maxSizeBytes": {"type": "integer"}
		}
	}`)
	promptTemplateSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"template": {"type": "string"}}
	}`)
	textGenerationSchema := json.RawMessage(`{
		"type": "object",
		"properties": {"modelId": {"type": "string"}},
		"required": ["modelId"]
	}`)
	imageGenerationSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"modelId": {"type": "string"},
			"width": {"type": "integer"}
		}
	}`)
	agentSchema := permissiveSchema

	m := map[string]domain.NodeMetadata{
		NodeTypeTextInput: {
			Type:          NodeTypeTextInput,
			DisplayName:   "Text Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  textInputSchema,
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		NodeTypeImageInput: {
			Type:          NodeTypeImageInput,
			DisplayName:   "Image Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{{Name: "image", DataType: domain.PortTypeImage, Required: true}},
			ConfigSchema:  imageInputSchema,
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		NodeTypePromptTemplate: {
			Type:          NodeTypePromptTemplate,
			DisplayName:   "Prompt Template",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  promptTemplateSchema,
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		NodeTypeTextGeneration: {
			Type:          NodeTypeTextGeneration,
			DisplayName:   "Text Generation",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "prompt", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  textGenerationSchema,
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		NodeTypeImageGeneration: {
			Type:          NodeTypeImageGeneration,
			DisplayName:   "Image Generation",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionAsync,
			Inputs: []domain.PortMetadata{
				{Name: "prompt", DataType: domain.PortTypeText, Required: true},
				{Name: "reference", DataType: domain.PortTypeImage, Required: false},
			},
			Outputs:      []domain.PortMetadata{{Name: "image", DataType: domain.PortTypeImage, Required: true}},
			ConfigSchema: imageGenerationSchema,
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		},
		NodeTypeAgent: {
			Type:          NodeTypeAgent,
			DisplayName:   "Agent",
			Category:      domain.NodeCategoryAgent,
			ExecutionKind: domain.NodeExecutionManagedAgent,
			Inputs:        []domain.PortMetadata{{Name: "input", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  agentSchema,
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		NodeTypeTextOutput: {
			Type:          NodeTypeTextOutput,
			DisplayName:   "Text Output",
			Category:      domain.NodeCategoryOutput,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs: []domain.PortMetadata{
				{Name: "text", DataType: domain.PortTypeText, Required: true},
				{Name: "note", DataType: domain.PortTypeText, Required: false},
			},
			// Test-only: a production Output Node declares no Outputs. This lets one
			// Graph-stage test build a syntactically valid outgoing edge from an Output
			// Node to exercise OUTPUT_NODE_NOT_UNIQUE_SINK.
			Outputs:      []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: false}},
			ConfigSchema: permissiveSchema,
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		NodeTypeMediaOutput: {
			Type:          NodeTypeMediaOutput,
			DisplayName:   "Media Output",
			Category:      domain.NodeCategoryOutput,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs: []domain.PortMetadata{
				{Name: "image", DataType: domain.PortTypeImage, Required: true},
				{Name: "caption", DataType: domain.PortTypeText, Required: true},
			},
			ConfigSchema: permissiveSchema,
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
	}
	return &fakeCatalog{metadata: m}
}

func (c *fakeCatalog) NodeMetadata(nodeType string) (domain.NodeMetadata, bool) {
	m, ok := c.metadata[nodeType]
	return m, ok
}

func (c *fakeCatalog) ValidateSemantics(ctx context.Context, nodeType string, config map[string]any) error {
	c.semanticsCalls++
	if v, ok := config["__forceSemanticError"]; ok {
		if b, _ := v.(bool); b {
			return errors.New("forced semantic failure for test")
		}
	}
	return nil
}

// fixedClock is a deterministic domain.Clock for tests.
type fixedClock struct {
	at time.Time
}

func (c fixedClock) Now() time.Time { return c.at }

// TestCatalog_AgentFixture_MatchesInterfaceSpecPorts pins the fixture's agent ports to
// the MVP node port contract (docs/08-interface-spec.md §2.1 MVP 节点端口: `agent` takes
// `input: text` and produces `text: text`). Every compiler test validates Definitions
// against this fixture, so drift here would let a Definition compile that the real
// Registry rejects.
func TestCatalog_AgentFixture_MatchesInterfaceSpecPorts(t *testing.T) {
	meta, ok := newFakeCatalog().NodeMetadata(NodeTypeAgent)
	if !ok {
		t.Fatalf("NodeMetadata(%q): want registered, got missing", NodeTypeAgent)
	}

	wantInputs := []domain.PortMetadata{{Name: "input", DataType: domain.PortTypeText, Required: true}}
	wantOutputs := []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}}

	if !reflect.DeepEqual(meta.Inputs, wantInputs) {
		t.Errorf("agent Inputs = %+v, want %+v", meta.Inputs, wantInputs)
	}
	if !reflect.DeepEqual(meta.Outputs, wantOutputs) {
		t.Errorf("agent Outputs = %+v, want %+v", meta.Outputs, wantOutputs)
	}
}
