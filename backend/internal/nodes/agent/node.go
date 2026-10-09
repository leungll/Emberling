// Package agent registers the built-in `agent` Node Type.
// It carries only the Node's registered Metadata and its ManagedAgentBinding: it owns no
// Executor, no retry, timeout or Event of its own. Actual Agent execution (Turn, Decision,
// Action) is the built-in Agent Runtime's responsibility, reached through
// registry.BindingManagedAgent, not through this package.
package agent

import (
	"encoding/json"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "agent"

// configSchema is the fixed `agent` Node config shape:
// modelId, maxTurns and timeoutMs are mandatory; instructions, modelConfig, allowedTools,
// the three frozen Schemas and the generation limit maxGenerationCalls are optional. It
// matches runtime.AgentNodeConfig field for field so the ConfigSchema stage and the
// Semantics stage (runtime.ParseAgentNodeConfig) never disagree about which top-level
// fields exist.
const configSchema = `{
  "type": "object",
  "properties": {
    "instructions": {"type": "string"},
    "modelId": {"type": "string", "minLength": 1},
    "modelConfig": {"type": "object"},
    "allowedTools": {"type": "array", "items": {"type": "string", "minLength": 1}},
    "contextSchema": {"type": "object"},
    "stateSchema": {"type": "object"},
    "outputSchema": {"type": "object"},
    "maxTurns": {"type": "integer", "minimum": 1},
    "timeoutMs": {"type": "integer", "minimum": 1},
    "maxGenerationCalls": {"type": "integer", "minimum": 1}
  },
  "required": ["modelId", "maxTurns", "timeoutMs"],
  "additionalProperties": false
}`

// Registration returns the Agent NodeRegistration. It has no external side effect: the
// Agent Runtime's model calls, Tool calls and state transitions all happen after their own
// prerequisite transactions commit, never from this registration.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Agent",
			Category:      domain.NodeCategoryAgent,
			ExecutionKind: domain.NodeExecutionManagedAgent,
			Inputs:        []domain.PortMetadata{{Name: "input", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "modelId", Order: 10, Group: domain.UIGroupModel, Widget: domain.UIWidgetModelSelector, Capability: domain.ModelCapabilityStructuredDecision},
				{Path: "instructions", Order: 20, Group: domain.UIGroupBasic, Widget: domain.UIWidgetTextArea},
				{Path: "allowedTools", Order: 30, Group: domain.UIGroupBasic, Widget: domain.UIWidgetToolSelector},
				{Path: "maxTurns", Order: 40, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "timeoutMs", Order: 50, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "maxGenerationCalls", Order: 55, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "modelConfig", Order: 60, Group: domain.UIGroupModelParameters, Widget: domain.UIWidgetDefault},
			}},
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ManagedAgentBinding{RuntimeKey: "builtin"},
	}
}
