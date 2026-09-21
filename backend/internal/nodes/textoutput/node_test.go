package textoutput

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

func TestTextOutputExecutor_ValidateSemantics_AlwaysPasses(t *testing.T) {
	if err := (Executor{}).ValidateSemantics(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("ValidateSemantics() error = %v, want nil", err)
	}
}

func TestTextOutputExecutor_Execute_MissingInputPortFails(t *testing.T) {
	if _, err := (Executor{}).Execute(context.Background(), registry.NodeInput{}, map[string]any{}); err == nil {
		t.Fatal("Execute() error = nil, want error for missing required text port")
	}
}

func TestTextOutputExecutor_Execute_RepublishesInputAsOutput(t *testing.T) {
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"text": json.RawMessage(`"final caption"`)}}
	result, err := (Executor{}).Execute(context.Background(), in, map[string]any{})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(result.Output.Ports["text"]) != `"final caption"` {
		t.Fatalf("Execute().Output.Ports[text] = %s, want the input value unchanged", result.Output.Ports["text"])
	}
}
