package prompttemplate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

func TestRegistration_Validate_Passes(t *testing.T) {
	if err := registry.NewNodeRegistry().Register(Registration()); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
}

func TestPromptTemplateExecutor_ValidateSemantics_UnknownPlaceholderFails(t *testing.T) {
	err := Executor{}.ValidateSemantics(context.Background(), map[string]any{"template": "Rewrite: {{image}}"})
	if err == nil {
		t.Fatal("ValidateSemantics() error = nil, want error for placeholder not in input ports")
	}
}

func TestPromptTemplateExecutor_ValidateSemantics_KnownPlaceholderPasses(t *testing.T) {
	err := Executor{}.ValidateSemantics(context.Background(), map[string]any{"template": "Rewrite: {{text}}"})
	if err != nil {
		t.Fatalf("ValidateSemantics() error = %v, want nil", err)
	}
}

func TestPromptTemplateExecutor_Execute_SubstitutesInputPort(t *testing.T) {
	config := map[string]any{"template": "Rewrite this brief: {{text}}"}
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`"A small ember creature"`)}}
	result, err := Executor{}.Execute(context.Background(), in, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	var got string
	if err := json.Unmarshal(result.Output.Ports["text"], &got); err != nil {
		t.Fatalf("output text is not a JSON string: %v", err)
	}
	want := "Rewrite this brief: A small ember creature"
	if got != want {
		t.Fatalf("Execute() output = %q, want %q", got, want)
	}
}

func TestPromptTemplateExecutor_Execute_MissingPortSubstitutesEmptyString(t *testing.T) {
	config := map[string]any{"template": "Rewrite: {{text}}"}
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`null`)}}
	result, err := Executor{}.Execute(context.Background(), in, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	var got string
	if err := json.Unmarshal(result.Output.Ports["text"], &got); err != nil {
		t.Fatalf("output text is not a JSON string: %v", err)
	}
	if got != "Rewrite: " {
		t.Fatalf("Execute() output = %q, want %q", got, "Rewrite: ")
	}
}

func TestPromptTemplateExecutor_Execute_NonStringPortFails(t *testing.T) {
	config := map[string]any{"template": "{{text}}"}
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`123`)}}
	if _, err := (Executor{}).Execute(context.Background(), in, config); err == nil {
		t.Fatal("Execute() error = nil, want error for non-string port value")
	}
}
