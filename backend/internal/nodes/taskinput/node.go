// Package taskinput implements the built-in Task Input Node. It reads one Run input field
// by its frozen inputKey, requires it to be a JSON object and republishes it unchanged on
// the node's `task` output port, so a downstream Agent can resolve pointers such as its
// goal inside the task; it owns no NodeRun state, retry or Event of its own.
package taskinput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "task_input"

// configSchema is the sole contract for Task Input config: inputKey and required are
// mandatory; schema is an optional JSON Schema for the task object that flows into the
// generated runInputSchema, where a schema without a type is read as an object schema.
const configSchema = `{
  "type": "object",
  "properties": {
    "inputKey": {"type": "string", "minLength": 1},
    "label": {"type": "string"},
    "required": {"type": "boolean"},
    "schema": {"type": "object"}
  },
  "required": ["inputKey", "required"],
  "additionalProperties": false
}`

// Registration returns the Task Input NodeRegistration. It has no external side effect
// and is always safe to repeat.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Task Input",
			Description:   "Each run starts with this task. You fill in its fields when you start the run.",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs: []domain.PortMetadata{{
				Name: "task", DataType: domain.PortTypeJSON, Required: true,
				Description: "The task fields entered when the run started.",
			}},
			ConfigSchema: json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "inputKey", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "label", Order: 20, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "required", Order: 30, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "schema", Order: 40, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
			}},
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{}},
	}
}

// Executor implements registry.NodeExecutor for the Task Input Node.
type Executor struct{}

// ValidateSemantics checks the one constraint ConfigSchema cannot express: a configured
// task schema that names a type must name "object", because the node only publishes
// objects.
func (Executor) ValidateSemantics(_ context.Context, config map[string]any) error {
	schema, ok := config["schema"].(map[string]any)
	if !ok {
		return nil
	}
	schemaType, present := schema["type"]
	if present && schemaType != "object" {
		return fmt.Errorf("task_input: schema type must be \"object\", got %v", schemaType)
	}
	return nil
}

// Execute reads Run.input[inputKey], requires it to be a JSON object and republishes it
// byte for byte on the `task` port. An absent optional key produces an explicit JSON null
// port value rather than any substituted content.
func (Executor) Execute(_ context.Context, input registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	inputKey, _ := config["inputKey"].(string)
	required, _ := config["required"].(bool)

	var runInput map[string]json.RawMessage
	if len(input.RunInput) > 0 {
		if err := json.Unmarshal(input.RunInput, &runInput); err != nil {
			return registry.NodeResult{}, fmt.Errorf("task_input: run input is not a JSON object: %w", err)
		}
	}

	value, present := runInput[inputKey]
	if !present {
		if required {
			return registry.NodeResult{}, fmt.Errorf("task_input: required run input key %q is missing", inputKey)
		}
		return registry.CompletedResult(map[string]json.RawMessage{"task": json.RawMessage("null")}), nil
	}
	if !bytes.HasPrefix(bytes.TrimSpace(value), []byte("{")) {
		return registry.NodeResult{}, fmt.Errorf("task_input: run input key %q must be a JSON object", inputKey)
	}
	return registry.CompletedResult(map[string]json.RawMessage{"task": value}), nil
}
