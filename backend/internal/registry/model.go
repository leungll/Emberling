package registry

import (
	"context"
	"encoding/json"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// ModelRegistration is one stable Model ID a Definition may store (07 §1.4). It embeds
// domain.ModelMetadata so the Registry, the `/models` response and Studio read exactly
// one description of a model; ConfigSchema stays the only contract for model parameters.
type ModelRegistration struct {
	domain.ModelMetadata
}

// HasCapability reports whether the model declares the named result capability. A
// capability names the result Emberling needs, not a Provider-native API.
func (r ModelRegistration) HasCapability(capability string) bool {
	for _, declared := range r.Capabilities {
		if declared == capability {
			return true
		}
	}
	return false
}

// ModelProvider converts Emberling's unified model request into one Provider protocol and
// the response back into a unified structure. It authenticates, converts, normalises
// errors and extracts Token usage. It does not select a Tool, apply a state patch,
// advance an Action or repeat a cost-bearing call invisibly to the Runtime (07 §1.4).
type ModelProvider interface {
	Models(ctx context.Context) ([]ModelRegistration, error)
	Generate(ctx context.Context, request ModelRequest) (ModelResponse, error)
}

// ModelMessage is one message of the current Context Version. The system prompt travels
// in ModelRequest.Instructions and is never copied into Messages.
type ModelMessage struct {
	// Role is user, assistant or tool.
	Role string
	// Content is the complete logical content or an immutable ArtifactRef.
	Content json.RawMessage
	// ToolName and ToolActionID are required on a tool message and empty otherwise.
	ToolName     *string
	ToolActionID *string
}

// ModelToolSpec is one Tool as the model sees it. It is built from ToolMetadata in the
// frozen allowlist order; the Adapter may not add, drop or reorder Tools.
type ModelToolSpec struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
}

// ModelRequest is the complete call contract between the Runtime and an Adapter. It holds
// only values already frozen for this Turn: an Adapter may not re-read a newer Definition
// or supply Tools of its own.
type ModelRequest struct {
	ModelID           string
	Instructions      string
	Messages          []ModelMessage
	State             json.RawMessage
	Tools             []ModelToolSpec
	ModelConfig       json.RawMessage
	FinalOutputSchema json.RawMessage
	DecisionSchema    json.RawMessage
}

// DecisionKind is the mutually exclusive shape of one model Decision.
type DecisionKind string

const (
	DecisionToolCall DecisionKind = "TOOL_CALL"
	DecisionFinal    DecisionKind = "FINAL"
)

func (k DecisionKind) IsValid() bool {
	return k == DecisionToolCall || k == DecisionFinal
}

// ModelDecision is the normalised model result. The Runtime validates the envelope shape
// before it commits an immutable Decision; arguments, output and the patched State are
// checked later against the frozen Schemas.
type ModelDecision struct {
	Kind DecisionKind
	// ToolName and Arguments are required for TOOL_CALL.
	ToolName  *string
	Arguments json.RawMessage
	// Output is required for FINAL.
	Output json.RawMessage
	// StatePatch is optional for both kinds.
	StatePatch json.RawMessage
}

// ModelResponseSummary is a bounded diagnostic summary. It is not a recovery or execution
// input, and it never carries the Provider's private response structure.
type ModelResponseSummary struct {
	ProviderRequestID *string
	FinishReason      *string
	// ResponseSHA256 is the hash of the raw Provider response.
	ResponseSHA256 string
}

// ModelResponse is what an Adapter returns for one Generate call. TokenUsage is nil when
// the Provider reported none.
type ModelResponse struct {
	Decision        ModelDecision
	ResponseSummary ModelResponseSummary
	TokenUsage      *domain.TokenUsage
}

// decisionSchema is the fixed envelope of 07 §1.4. It constrains only the TOOL_CALL and
// FINAL envelope: Tool arguments, Final output and the patched State are validated by the
// Runtime against their own frozen Schemas.
const decisionSchema = `{
  "type": "object",
  "oneOf": [
    {
      "properties": {
        "kind": {"const": "TOOL_CALL"},
        "tool": {"type": "string", "minLength": 1},
        "arguments": {},
        "statePatch": {"type": "object"}
      },
      "required": ["kind", "tool", "arguments"],
      "additionalProperties": false
    },
    {
      "properties": {
        "kind": {"const": "FINAL"},
        "output": {},
        "statePatch": {"type": "object"}
      },
      "required": ["kind", "output"],
      "additionalProperties": false
    }
  ]
}`

// DecisionSchema returns a copy of the fixed Decision envelope Schema. Callers receive
// their own bytes so that a request cannot mutate the shared contract.
func DecisionSchema() json.RawMessage {
	return json.RawMessage(decisionSchema)
}
