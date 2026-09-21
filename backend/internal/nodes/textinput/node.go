// Package textinput implements the built-in Text Input Node (02 §3.2, 08 §1.1). It reads
// one Run input field by its frozen inputKey and republishes it as the node's `text`
// output port; it owns no NodeRun state, retry or Event of its own (07 §1.1).
package textinput

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "text_input"

// configSchema is the sole contract for Text Input config (08 §1.1): inputKey and
// required are mandatory; minLength/maxLength are optional length constraints that flow
// unchanged into the generated runInputSchema (08 §1.3).
const configSchema = `{
  "type": "object",
  "properties": {
    "inputKey": {"type": "string", "minLength": 1},
    "label": {"type": "string"},
    "required": {"type": "boolean"},
    "minLength": {"type": "integer", "minimum": 0},
    "maxLength": {"type": "integer", "minimum": 1}
  },
  "required": ["inputKey", "required"],
  "additionalProperties": false
}`

// Registration returns the Text Input NodeRegistration. It has no external side effect
// and is always safe to repeat.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Text Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "inputKey", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "label", Order: 20, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "required", Order: 30, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "minLength", Order: 40, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "maxLength", Order: 50, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
			}},
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{}},
	}
}

// Executor implements registry.NodeExecutor for the Text Input Node.
type Executor struct{}

// ValidateSemantics checks the one cross-field constraint ConfigSchema cannot express: a
// declared minLength must not exceed maxLength (07 §1.1).
func (Executor) ValidateSemantics(_ context.Context, config map[string]any) error {
	minValue, hasMin := numericField(config, "minLength")
	maxValue, hasMax := numericField(config, "maxLength")
	if !hasMin || !hasMax {
		return nil
	}
	if minValue > maxValue {
		return fmt.Errorf("text_input: minLength (%v) must be <= maxLength (%v)", minValue, maxValue)
	}
	return nil
}

// Execute reads Run.input[inputKey] and republishes it on the `text` port. Per 08 §1.3,
// MVP Input Nodes inject no default: an absent optional key produces an explicit JSON
// null port value rather than any substituted content.
func (Executor) Execute(_ context.Context, input registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	inputKey, _ := config["inputKey"].(string)
	required, _ := config["required"].(bool)

	var runInput map[string]json.RawMessage
	if len(input.RunInput) > 0 {
		if err := json.Unmarshal(input.RunInput, &runInput); err != nil {
			return registry.NodeResult{}, fmt.Errorf("text_input: run input is not a JSON object: %w", err)
		}
	}

	value, present := runInput[inputKey]
	if !present {
		if required {
			return registry.NodeResult{}, fmt.Errorf("text_input: required run input key %q is missing", inputKey)
		}
		value = json.RawMessage("null")
	}
	return registry.CompletedResult(map[string]json.RawMessage{"text": value}), nil
}

// numericField reads a JSON-Schema-validated numeric config field. encoding/json decodes
// a bare JSON number into float64 inside a map[string]any, which is the shape the Runtime
// hands to Executors.
func numericField(config map[string]any, key string) (float64, bool) {
	raw, ok := config[key]
	if !ok {
		return 0, false
	}
	value, ok := raw.(float64)
	return value, ok
}
