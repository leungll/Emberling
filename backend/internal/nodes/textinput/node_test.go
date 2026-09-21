package textinput

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

func TestTextInputExecutor_ValidateSemantics_MinLengthGreaterThanMaxLengthFails(t *testing.T) {
	err := Executor{}.ValidateSemantics(context.Background(), map[string]any{
		"minLength": float64(10),
		"maxLength": float64(5),
	})
	if err == nil {
		t.Fatal("ValidateSemantics() error = nil, want error for minLength > maxLength")
	}
}

func TestTextInputExecutor_ValidateSemantics_MinLengthLessOrEqualMaxLengthPasses(t *testing.T) {
	err := Executor{}.ValidateSemantics(context.Background(), map[string]any{
		"minLength": float64(5),
		"maxLength": float64(5),
	})
	if err != nil {
		t.Fatalf("ValidateSemantics() error = %v, want nil", err)
	}
}

func TestTextInputExecutor_ValidateSemantics_OnlyOneBoundPresentPasses(t *testing.T) {
	err := Executor{}.ValidateSemantics(context.Background(), map[string]any{
		"minLength": float64(5),
	})
	if err != nil {
		t.Fatalf("ValidateSemantics() error = %v, want nil when maxLength is absent", err)
	}
}

func TestTextInputExecutor_Execute_MissingRequiredKeyFails(t *testing.T) {
	config := map[string]any{"inputKey": "brief", "required": true}
	_, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`{}`)}, config)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing required run input key")
	}
}

func TestTextInputExecutor_Execute_MissingOptionalKeyReturnsNullPort(t *testing.T) {
	config := map[string]any{"inputKey": "brief", "required": false}
	result, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`{}`)}, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(result.Output.Ports["text"]) != "null" {
		t.Fatalf("Execute().Output.Ports[text] = %s, want null for absent optional key", result.Output.Ports["text"])
	}
}

func TestTextInputExecutor_Execute_PresentKeyReturnsValueOnTextPort(t *testing.T) {
	config := map[string]any{"inputKey": "brief", "required": true}
	runInput := json.RawMessage(`{"brief":"A small ember creature"}`)
	result, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: runInput}, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(result.Output.Ports["text"]) != `"A small ember creature"` {
		t.Fatalf("Execute().Output.Ports[text] = %s, want the run input value", result.Output.Ports["text"])
	}
	if result.Kind != registry.NodeResultCompleted {
		t.Fatalf("Execute().Kind = %v, want COMPLETED", result.Kind)
	}
}

func TestTextInputExecutor_Execute_RunInputNotObjectFails(t *testing.T) {
	config := map[string]any{"inputKey": "brief", "required": true}
	_, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`[]`)}, config)
	if err == nil {
		t.Fatal("Execute() error = nil, want error when run input is not a JSON object")
	}
}
