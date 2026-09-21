// Package prompttemplate implements the built-in Prompt Template Node (02 §3.2). It
// substitutes `{{name}}` placeholders in a fixed template string with upstream input port
// values; it owns no NodeRun state, retry or Event of its own (07 §1.1).
package prompttemplate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "prompt_template"

// configSchema is the sole contract for Prompt Template config: one required template
// string.
const configSchema = `{
  "type": "object",
  "properties": {
    "template": {"type": "string", "minLength": 1}
  },
  "required": ["template"],
  "additionalProperties": false
}`

// placeholderPattern matches a `{{name}}` placeholder. Only identifier-shaped names are
// recognised so a malformed placeholder is left untouched and later rejected by
// ValidateSemantics as an unknown port name.
var placeholderPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// inputPortNames are the node's declared input ports (02 §3.2 node literals: prompt
// template input `text`, required). ValidateSemantics checks every placeholder against
// this fixed set rather than maintaining a second, dynamic port contract.
var inputPortNames = map[string]struct{}{"text": {}}

// Registration returns the Prompt Template NodeRegistration. It has no external side
// effect and is always safe to repeat.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Prompt Template",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "template", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetPromptEditor},
			}},
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{}},
	}
}

// Executor implements registry.NodeExecutor for the Prompt Template Node.
type Executor struct{}

// ValidateSemantics checks the one constraint ConfigSchema cannot express: every
// `{{name}}` placeholder in template must name a declared input port.
func (Executor) ValidateSemantics(_ context.Context, config map[string]any) error {
	template, _ := config["template"].(string)
	for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		name := match[1]
		if _, ok := inputPortNames[name]; !ok {
			return fmt.Errorf("prompt_template: placeholder %q is not an input port", name)
		}
	}
	return nil
}

// Execute substitutes every recognised placeholder with the current value of the
// matching input port and returns the composed string on the `text` output port.
func (Executor) Execute(_ context.Context, input registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	template, _ := config["template"].(string)

	var substituteErr error
	composed := placeholderPattern.ReplaceAllStringFunc(template, func(match string) string {
		name := placeholderPattern.FindStringSubmatch(match)[1]
		value, err := portText(input, name)
		if err != nil {
			substituteErr = err
			return match
		}
		return value
	})
	if substituteErr != nil {
		return registry.NodeResult{}, substituteErr
	}

	encoded, err := json.Marshal(composed)
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("prompt_template: encode result: %w", err)
	}
	return registry.CompletedResult(map[string]json.RawMessage{"text": encoded}), nil
}

// portText decodes one text input port to a plain string. A port carrying JSON null
// (an absent optional upstream value) substitutes as an empty string rather than failing
// the template.
func portText(input registry.NodeInput, name string) (string, error) {
	raw, present := input.Port(name)
	if !present || string(raw) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("prompt_template: input port %q is not a JSON string: %w", name, err)
	}
	return value, nil
}
