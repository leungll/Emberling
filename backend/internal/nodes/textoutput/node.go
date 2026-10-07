// Package textoutput implements the built-in Text Output Node. It is the DAG's
// sole terminal node in a text scenario: its complete logical result becomes both its
// NodeRun output and Run.output.
package textoutput

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "text_output"

// configSchema is empty: Text Output takes no configuration.
const configSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

// Registration returns the Text Output NodeRegistration. It has no external side effect
// and is always safe to repeat.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Text Output",
			Category:      domain.NodeCategoryOutput,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(configSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{}},
	}
}

// Executor implements registry.NodeExecutor for the Text Output Node.
type Executor struct{}

// ValidateSemantics has nothing to check beyond ConfigSchema: Text Output has no config
// fields and no cross-field constraint.
func (Executor) ValidateSemantics(_ context.Context, _ map[string]any) error {
	return nil
}

// Execute republishes the `text` input port as the node's complete logical result. The
// service layer copies this object into Run.output when this node is the Output Node.
func (Executor) Execute(_ context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	value, present := input.Port("text")
	if !present {
		return registry.NodeResult{}, fmt.Errorf("text_output: required input port %q is missing", "text")
	}
	return registry.CompletedResult(map[string]json.RawMessage{"text": value}), nil
}
