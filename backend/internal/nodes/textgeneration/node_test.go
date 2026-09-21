package textgeneration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// stubModelProvider is a deterministic ModelProvider used to test the node in isolation
// from any real Adapter.
type stubModelProvider struct {
	response    registry.ModelResponse
	err         error
	lastRequest registry.ModelRequest
}

func (p *stubModelProvider) Models(context.Context) ([]registry.ModelRegistration, error) {
	return nil, nil
}

func (p *stubModelProvider) Generate(_ context.Context, request registry.ModelRequest) (registry.ModelResponse, error) {
	p.lastRequest = request
	return p.response, p.err
}

// stubResolver is a minimal ModelResolver backed by a fixed model/provider pair.
type stubResolver struct {
	model    registry.ModelRegistration
	provider registry.ModelProvider
	ok       bool
}

func (s stubResolver) Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool) {
	if !s.ok {
		return registry.ModelRegistration{}, nil, false
	}
	return s.model, s.provider, true
}

func validModel() registry.ModelRegistration {
	return registry.ModelRegistration{ModelMetadata: domain.ModelMetadata{
		ID:           "text-model-v1",
		DisplayName:  "Text Model v1",
		Capabilities: []string{domain.ModelCapabilityTextGeneration},
		ConfigSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"properties": {"temperature": {"type": "number", "minimum": 0, "maximum": 2}}
		}`),
	}}
}

func finalResponse(output string) registry.ModelResponse {
	encoded, _ := json.Marshal(output)
	return registry.ModelResponse{Decision: registry.ModelDecision{Kind: registry.DecisionFinal, Output: encoded}}
}

func TestRegistration_Validate_Passes(t *testing.T) {
	resolver := stubResolver{model: validModel(), provider: &stubModelProvider{}, ok: true}
	if err := registry.NewNodeRegistry().Register(Registration(resolver)); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
}

func TestTextGenerationExecutor_ValidateSemantics_UnknownModelFails(t *testing.T) {
	e := Executor{resolver: stubResolver{ok: false}}
	err := e.ValidateSemantics(context.Background(), map[string]any{"modelId": "does-not-exist"})
	if err == nil {
		t.Fatal("ValidateSemantics() error = nil, want error for unregistered model")
	}
}

func TestTextGenerationExecutor_ValidateSemantics_MissingCapabilityFails(t *testing.T) {
	model := validModel()
	model.Capabilities = []string{domain.ModelCapabilityImageGeneration}
	e := Executor{resolver: stubResolver{model: model, provider: &stubModelProvider{}, ok: true}}
	err := e.ValidateSemantics(context.Background(), map[string]any{"modelId": model.ID})
	if err == nil {
		t.Fatal("ValidateSemantics() error = nil, want error for missing text_generation capability")
	}
}

func TestTextGenerationExecutor_ValidateSemantics_ModelConfigSchemaViolationFails(t *testing.T) {
	e := Executor{resolver: stubResolver{model: validModel(), provider: &stubModelProvider{}, ok: true}}
	config := map[string]any{
		"modelId":     "text-model-v1",
		"modelConfig": map[string]any{"temperature": 5}, // exceeds maximum: 2
	}
	if err := e.ValidateSemantics(context.Background(), config); err == nil {
		t.Fatal("ValidateSemantics() error = nil, want error for modelConfig violating model ConfigSchema")
	}
}

func TestTextGenerationExecutor_ValidateSemantics_ValidConfigPasses(t *testing.T) {
	e := Executor{resolver: stubResolver{model: validModel(), provider: &stubModelProvider{}, ok: true}}
	config := map[string]any{
		"modelId":     "text-model-v1",
		"modelConfig": map[string]any{"temperature": 0.5},
	}
	if err := e.ValidateSemantics(context.Background(), config); err != nil {
		t.Fatalf("ValidateSemantics() error = %v, want nil", err)
	}
}

func TestTextGenerationExecutor_ValidateSemantics_AbsentModelConfigDefaultsToEmptyObject(t *testing.T) {
	e := Executor{resolver: stubResolver{model: validModel(), provider: &stubModelProvider{}, ok: true}}
	if err := e.ValidateSemantics(context.Background(), map[string]any{"modelId": "text-model-v1"}); err != nil {
		t.Fatalf("ValidateSemantics() error = %v, want nil when modelConfig is absent", err)
	}
}

func TestTextGenerationExecutor_Execute_MissingPromptPortFails(t *testing.T) {
	e := Executor{resolver: stubResolver{model: validModel(), provider: &stubModelProvider{}, ok: true}}
	_, err := e.Execute(context.Background(), registry.NodeInput{}, map[string]any{"modelId": "text-model-v1"})
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing prompt port")
	}
}

func TestTextGenerationExecutor_Execute_UnregisteredModelFails(t *testing.T) {
	e := Executor{resolver: stubResolver{ok: false}}
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"prompt": json.RawMessage(`"hello"`)}}
	_, err := e.Execute(context.Background(), in, map[string]any{"modelId": "does-not-exist"})
	if err == nil {
		t.Fatal("Execute() error = nil, want error for unregistered model")
	}
}

func TestTextGenerationExecutor_Execute_GenerateErrorPropagates(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	provider := &stubModelProvider{err: wantErr}
	e := Executor{resolver: stubResolver{model: validModel(), provider: provider, ok: true}}
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"prompt": json.RawMessage(`"hello"`)}}
	_, err := e.Execute(context.Background(), in, map[string]any{"modelId": "text-model-v1"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want wrapped %v", err, wantErr)
	}
}

func TestTextGenerationExecutor_Execute_ToolCallDecisionFails(t *testing.T) {
	provider := &stubModelProvider{response: registry.ModelResponse{
		Decision: registry.ModelDecision{Kind: registry.DecisionToolCall},
	}}
	e := Executor{resolver: stubResolver{model: validModel(), provider: provider, ok: true}}
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"prompt": json.RawMessage(`"hello"`)}}
	_, err := e.Execute(context.Background(), in, map[string]any{"modelId": "text-model-v1"})
	if err == nil {
		t.Fatal("Execute() error = nil, want error for TOOL_CALL decision")
	}
}

func TestTextGenerationExecutor_Execute_NonStringFinalOutputFails(t *testing.T) {
	provider := &stubModelProvider{response: registry.ModelResponse{
		Decision: registry.ModelDecision{Kind: registry.DecisionFinal, Output: json.RawMessage(`123`)},
	}}
	e := Executor{resolver: stubResolver{model: validModel(), provider: provider, ok: true}}
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"prompt": json.RawMessage(`"hello"`)}}
	_, err := e.Execute(context.Background(), in, map[string]any{"modelId": "text-model-v1"})
	if err == nil {
		t.Fatal("Execute() error = nil, want error for non-string FINAL output")
	}
}

func TestTextGenerationExecutor_Execute_FinalDecisionReturnsTextPortAndTokenUsage(t *testing.T) {
	response := finalResponse("echo: hello")
	response.TokenUsage = &domain.TokenUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}
	provider := &stubModelProvider{response: response}
	e := Executor{resolver: stubResolver{model: validModel(), provider: provider, ok: true}}
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"prompt": json.RawMessage(`"hello"`)}}
	result, err := e.Execute(context.Background(), in, map[string]any{"modelId": "text-model-v1", "systemPrompt": "be terse"})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	var got string
	if err := json.Unmarshal(result.Output.Ports["text"], &got); err != nil {
		t.Fatalf("output text is not a JSON string: %v", err)
	}
	if got != "echo: hello" {
		t.Fatalf("Execute() output = %q, want %q", got, "echo: hello")
	}
	if result.TokenUsage == nil || result.TokenUsage.TotalTokens != 3 {
		t.Fatalf("Execute().TokenUsage = %v, want forwarded usage", result.TokenUsage)
	}

	if provider.lastRequest.Instructions != "be terse" {
		t.Fatalf("Generate() Instructions = %q, want systemPrompt forwarded", provider.lastRequest.Instructions)
	}
	if len(provider.lastRequest.Messages) != 1 || provider.lastRequest.Messages[0].Role != "user" {
		t.Fatalf("Generate() Messages = %v, want one user message", provider.lastRequest.Messages)
	}
	if len(provider.lastRequest.Tools) != 0 {
		t.Fatalf("Generate() Tools = %v, want none", provider.lastRequest.Tools)
	}
}
