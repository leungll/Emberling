package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Agent Context message roles (docs/05-data-model.md §2 AgentContextVersion). Instructions
// are not one of them: the system prompt travels beside the Context in every model request
// (registry.ModelRequest.Instructions) and is never copied into the message chain, so that
// re-freezing or re-reading Instructions can never be confused with rewriting history.
const (
	AgentRoleUser      = "user"
	AgentRoleAssistant = "assistant"
	AgentRoleTool      = "tool"
)

// AgentContextMessage is one entry of an Agent Run's immutable message chain, in exactly
// the shape persisted in domain.AgentContextVersion.Messages. It is the Runtime's own
// storage shape, deliberately separate from registry.ModelMessage: an Adapter-facing type
// must not become the persisted format, or a Provider package change would silently
// rewrite committed recovery input. service maps one onto the other.
type AgentContextMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	// ToolName and ToolActionID are set only on a tool message, naming the committed
	// Action whose Tool produced the content.
	ToolName     *string `json:"toolName,omitempty"`
	ToolActionID *string `json:"toolActionId,omitempty"`
}

// InitialAgentContext builds Context Version 0 from the Agent NodeRun's validated input
// port value (docs/05-data-model.md §2: "Version 0 holds the Agent Node's validated
// upstream input"). It is exactly one `user` message carrying that value unchanged; the
// frozen Instructions are not part of it.
//
// An absent value is an error rather than an empty message: an Agent Run whose Context
// Version 0 carries nothing would ask the model a question the Definition never produced,
// and the initialisation transaction must fail the Agent NodeRun explicitly instead.
func InitialAgentContext(input json.RawMessage) ([]AgentContextMessage, error) {
	content := presentJSON(input)
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		return nil, fmt.Errorf("agent context: the Agent node's input port produced no value")
	}
	if !json.Valid(content) {
		return nil, fmt.Errorf("agent context: the Agent node's input port value is not valid JSON")
	}
	return []AgentContextMessage{{Role: AgentRoleUser, Content: content}}, nil
}

// DecodeAgentContext reads back a committed Context Version's messages. A restarted
// process replays from these bytes, so a malformed chain is an error, never a silently
// shortened Context.
func DecodeAgentContext(raw json.RawMessage) ([]AgentContextMessage, error) {
	if len(presentJSON(raw)) == 0 {
		return nil, fmt.Errorf("agent context: version carries no messages")
	}
	var messages []AgentContextMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("agent context: decode messages: %w", err)
	}
	for i, message := range messages {
		switch message.Role {
		case AgentRoleUser, AgentRoleAssistant, AgentRoleTool:
		default:
			return nil, fmt.Errorf("agent context: message %d has unknown role %q", i, message.Role)
		}
	}
	return messages, nil
}
