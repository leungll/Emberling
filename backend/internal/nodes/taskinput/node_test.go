package taskinput

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

func TestRegistration_Validate_Passes(t *testing.T) {
	if err := registry.NewNodeRegistry().Register(Registration()); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
}

func TestTaskInputExecutor_ValidateSemantics_SchemaOfAnotherTypeFails(t *testing.T) {
	err := Executor{}.ValidateSemantics(context.Background(), map[string]any{
		"inputKey": "task", "required": true,
		"schema": map[string]any{"type": "string"},
	})
	if err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("ValidateSemantics() error = %v, want a rejection of a non-object schema", err)
	}
}

func TestTaskInputExecutor_ValidateSemantics_ObjectSchemaOrNoTypePasses(t *testing.T) {
	for name, schema := range map[string]map[string]any{
		"object type": {"type": "object", "required": []any{"goal"}},
		"no type":     {"properties": map[string]any{"goal": map[string]any{"type": "string"}}},
	} {
		t.Run(name, func(t *testing.T) {
			err := Executor{}.ValidateSemantics(context.Background(), map[string]any{"inputKey": "task", "required": true, "schema": schema})
			if err != nil {
				t.Fatalf("ValidateSemantics() error = %v, want nil", err)
			}
		})
	}
}

func TestTaskInputExecutor_Execute_NonObjectInputRejected(t *testing.T) {
	config := map[string]any{"inputKey": "task", "required": true}
	for name, input := range map[string]string{
		"string": `{"task":"fix GO-2025-3553"}`,
		"array":  `{"task":["fix"]}`,
		"number": `{"task":3}`,
		"null":   `{"task":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(input)}, config)
			if err == nil || !strings.Contains(err.Error(), "JSON object") {
				t.Fatalf("Execute() error = %v, want the non-object task rejected", err)
			}
		})
	}
}

func TestTaskInputExecutor_Execute_MissingRequiredKeyFails(t *testing.T) {
	config := map[string]any{"inputKey": "task", "required": true}
	_, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`{}`)}, config)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing required run input key")
	}
}

func TestTaskInputExecutor_Execute_MissingOptionalKeyReturnsNullPort(t *testing.T) {
	config := map[string]any{"inputKey": "task", "required": false}
	result, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`{}`)}, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(result.Output.Ports["task"]) != "null" {
		t.Fatalf("Execute().Output.Ports[task] = %s, want null for absent optional key", result.Output.Ports["task"])
	}
}

func TestTaskInputExecutor_Execute_ObjectPublishedUnchanged(t *testing.T) {
	config := map[string]any{"inputKey": "task", "required": true}
	task := `{"advisory":"GO-2025-3553","goal":"PREPARE_ONLY","nested":{"keep":[1,2,3]}}`
	result, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`{"task":` + task + `}`)}, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(result.Output.Ports["task"]) != task {
		t.Fatalf("Execute().Output.Ports[task] = %s, want the task object byte for byte", result.Output.Ports["task"])
	}
	if result.Kind != registry.NodeResultCompleted {
		t.Fatalf("Execute().Kind = %v, want COMPLETED", result.Kind)
	}
}

func TestTaskInputExecutor_Execute_RunInputNotObjectFails(t *testing.T) {
	config := map[string]any{"inputKey": "task", "required": true}
	_, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`[]`)}, config)
	if err == nil {
		t.Fatal("Execute() error = nil, want error when run input is not a JSON object")
	}
}
