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
// the three frozen Schemas, the generation limit maxGenerationCalls, the revision limits
// maxFailedFacts, the image model
// imageModelId, the review model reviewModelId, the workspace binding, the goal pointer
// and the human and release wait bounds are optional. It matches runtime.AgentNodeConfig
// field for field so the ConfigSchema stage and the Semantics stage
// (runtime.ParseAgentNodeConfig) never disagree about which top-level fields exist; the
// finer workspace rules (commit format, path shape, registered repository) belong to the
// Semantics stage.
const configSchema = `{
  "type": "object",
  "properties": {
    "instructions": {"type": "string"},
    "modelId": {"type": "string", "minLength": 1, "title": "Decision model"},
    "modelConfig": {"type": "object"},
    "allowedTools": {"type": "array", "items": {"type": "string", "minLength": 1}},
    "contextSchema": {"type": "object"},
    "stateSchema": {"type": "object"},
    "outputSchema": {"type": "object"},
    "maxTurns": {"type": "integer", "minimum": 1},
    "timeoutMs": {"type": "integer", "minimum": 1},
    "maxGenerationCalls": {"type": "integer", "minimum": 1},
    "maxFailedFacts": {"type": "object", "additionalProperties": {"type": "integer", "minimum": 0}},
    "imageModelId": {"type": "string", "minLength": 1, "title": "Image model"},
    "reviewModelId": {"type": "string", "minLength": 1, "title": "Review model"},
    "workspace": {
      "type": "object",
      "properties": {
        "repositoryId": {"type": "string", "minLength": 1},
        "baseCommit": {"type": "string", "minLength": 1},
        "allowedPaths": {"type": "array", "items": {"type": "string", "minLength": 1}},
        "deployTarget": {
          "type": "object",
          "properties": {
            "service": {"type": "string", "minLength": 1},
            "environment": {"type": "string", "minLength": 1}
          },
          "required": ["service", "environment"],
          "additionalProperties": false
        }
      },
      "required": ["repositoryId", "baseCommit", "allowedPaths"],
      "additionalProperties": false
    },
    "goalPointer": {"type": "string", "minLength": 1},
    "approvalTimeoutMs": {"type": "integer", "minimum": 1},
    "releaseConfirmTimeoutMs": {"type": "integer", "minimum": 1},
    "voidGraceMs": {"type": "integer", "minimum": 1},
    "maxReleaseRetries": {"type": "integer", "minimum": 1},
    "humanWaitTimeoutMs": {"type": "integer", "minimum": 1}
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
			Description:   "Works toward a goal by calling tools, one turn at a time.",
			Category:      domain.NodeCategoryAgent,
			ExecutionKind: domain.NodeExecutionManagedAgent,
			// The input port accepts any value: a Text Input's string or a Task Input's object.
			// The Agent Runtime hands it to the model unchanged as the first user message and
			// resolves the goal pointer inside it.
			Inputs: []domain.PortMetadata{{
				Name: "input", DataType: domain.PortTypeAny, Required: true,
				Description: "What the agent starts from.",
			}},
			Outputs: []domain.PortMetadata{{
				Name: "text", DataType: domain.PortTypeText, Required: true,
				Description: "The report the agent hands back when it finishes.",
			}},
			ConfigSchema: json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "modelId", Order: 10, Group: domain.UIGroupModel, Widget: domain.UIWidgetModelSelector, Capability: domain.ModelCapabilityStructuredDecision},
				{Path: "imageModelId", Order: 15, Group: domain.UIGroupModel, Widget: domain.UIWidgetModelSelector, Capability: domain.ModelCapabilityReferenceImageGeneration},
				// reviewModelId lists every registered model: the capability a review model must
				// declare depends on which review Tools the allowlist holds, and the Backend
				// checks it when the Definition is saved.
				{Path: "reviewModelId", Order: 17, Group: domain.UIGroupModel, Widget: domain.UIWidgetModelSelector},
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
