// Package textgeneration implements the built-in Text Generation Node (02 §3.2). It calls
// a registered ModelProvider synchronously through the fixed Decision envelope (07 §1.4)
// and republishes the model's FINAL output on the `text` port; it owns no NodeRun state,
// retry or Event of its own (07 §1.1).
package textgeneration

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "text_generation"

// finalOutputSchema fixes the model's FINAL output to a plain string: this node has one
// text output port and does not accept a structured Final result.
const finalOutputSchema = `{"type": "string"}`

// configSchema is the sole contract for Text Generation config: modelId is mandatory,
// modelConfig defaults to an empty object, and systemPrompt is optional.
const configSchema = `{
  "type": "object",
  "properties": {
    "modelId": {"type": "string", "minLength": 1},
    "modelConfig": {"type": "object", "default": {}},
    "systemPrompt": {"type": "string"}
  },
  "required": ["modelId"],
  "additionalProperties": false
}`

// ModelResolver resolves a Model ID to its registration and serving ModelProvider.
// *registry.ModelRegistry satisfies this interface; the node package depends only on the
// narrow shape it actually calls, not on the concrete Registry type.
type ModelResolver interface {
	Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool)
}

// Registration returns the Text Generation NodeRegistration bound to resolver. resolver is
// consulted at ValidateSemantics and Execute time, never cached across calls, so a
// Registry update between Definition save and Run creation is observed correctly.
func Registration(resolver ModelResolver) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Text Generation",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "prompt", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "modelId", Order: 10, Group: domain.UIGroupModel, Widget: domain.UIWidgetModelSelector, Capability: domain.ModelCapabilityTextGeneration},
				{Path: "modelConfig", Order: 20, Group: domain.UIGroupModelParameters, Widget: domain.UIWidgetDefault},
				{Path: "systemPrompt", Order: 30, Group: domain.UIGroupBasic, Widget: domain.UIWidgetTextArea},
			}},
			// A synchronous model call has no external_task_id to serve as an idempotency
			// key, so a timeout or dropped response leaves the call result unproven (07
			// §1.2: EXTERNAL + UNKNOWN forbids automatic re-dispatch).
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{resolver: resolver}},
	}
}

// Executor implements registry.NodeExecutor for the Text Generation Node.
type Executor struct {
	resolver ModelResolver
}

// ValidateSemantics checks what ConfigSchema cannot express: the model exists, declares
// text_generation capability, and modelConfig satisfies that model's own ConfigSchema (07
// §1.4).
func (e Executor) ValidateSemantics(_ context.Context, config map[string]any) error {
	modelID, _ := config["modelId"].(string)
	if modelID == "" {
		return fmt.Errorf("text_generation: modelId is required")
	}
	model, _, ok := e.resolver.Get(modelID)
	if !ok {
		return fmt.Errorf("text_generation: model %q is not registered", modelID)
	}
	if !model.HasCapability(domain.ModelCapabilityTextGeneration) {
		return fmt.Errorf("text_generation: model %q lacks capability %q", modelID, domain.ModelCapabilityTextGeneration)
	}

	schema, err := registry.CompileSchema(model.ConfigSchema)
	if err != nil {
		return fmt.Errorf("text_generation: model %q configSchema: %w", modelID, err)
	}
	if err := registry.ValidateValue(schema, modelConfigValue(config)); err != nil {
		return fmt.Errorf("text_generation: modelConfig does not satisfy model %q configSchema: %w", modelID, err)
	}
	return nil
}

// Execute builds the fixed ModelRequest, calls Generate and requires a FINAL Decision with
// a string Output; a TOOL_CALL Decision is a contract violation for this node, not a
// choice it can act on.
func (e Executor) Execute(ctx context.Context, input registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	modelID, _ := config["modelId"].(string)
	systemPrompt, _ := config["systemPrompt"].(string)

	promptRaw, present := input.Port("prompt")
	if !present {
		return registry.NodeResult{}, fmt.Errorf("text_generation: required input port %q is missing", "prompt")
	}
	var promptText string
	if err := json.Unmarshal(promptRaw, &promptText); err != nil {
		return registry.NodeResult{}, fmt.Errorf("text_generation: input port %q is not a JSON string: %w", "prompt", err)
	}

	model, provider, ok := e.resolver.Get(modelID)
	if !ok {
		return registry.NodeResult{}, fmt.Errorf("text_generation: model %q is not registered", modelID)
	}

	modelConfigRaw, err := json.Marshal(modelConfigValue(config))
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("text_generation: encode modelConfig: %w", err)
	}

	request := registry.ModelRequest{
		ModelID:           model.ID,
		Instructions:      systemPrompt,
		Messages:          []registry.ModelMessage{{Role: "user", Content: promptRaw}},
		ModelConfig:       modelConfigRaw,
		FinalOutputSchema: json.RawMessage(finalOutputSchema),
		DecisionSchema:    registry.DecisionSchema(),
	}

	response, err := provider.Generate(ctx, request)
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("text_generation: generate: %w", err)
	}
	if response.Decision.Kind != registry.DecisionFinal {
		return registry.NodeResult{}, fmt.Errorf("text_generation: model returned %s, want %s", response.Decision.Kind, registry.DecisionFinal)
	}
	var outputText string
	if err := json.Unmarshal(response.Decision.Output, &outputText); err != nil {
		return registry.NodeResult{}, fmt.Errorf("text_generation: final output is not a JSON string: %w", err)
	}

	return registry.NodeResult{
		Kind:       registry.NodeResultCompleted,
		Output:     &registry.NodeOutput{Ports: map[string]json.RawMessage{"text": response.Decision.Output}},
		TokenUsage: response.TokenUsage,
	}, nil
}

// modelConfigValue reads the optional modelConfig field, defaulting to an empty object so
// a Generate call always carries a value the model's ConfigSchema can validate.
func modelConfigValue(config map[string]any) map[string]any {
	value, _ := config["modelConfig"].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}
