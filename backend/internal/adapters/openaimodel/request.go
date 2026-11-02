package openaimodel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// Roles of registry.ModelMessage. They are repeated here because the Runtime's own
// constants live in a package an Adapter must not import.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
	roleSystem    = "system"
)

// decisionFormatName names the structured output format in the request.
const decisionFormatName = "agent_decision"

// responseGuide tells the model how to fill the strict Decision form. The member names
// match the derived strict schema, which rejects any other member. arguments and
// statePatch stay JSON-encoded strings; output is the answer itself.
const responseGuide = `Reply with exactly one JSON object that matches the response schema. Every member is required; use null for a member that does not apply.
- To call one tool: set "kind" to "TOOL_CALL", "tool" to the tool name, and "arguments" to a JSON-encoded string of an object that satisfies that tool's input schema. Set "output" to null.
- To finish: set "kind" to "FINAL" and "output" to a value that satisfies the final output schema. Write a text answer as plain text, without JSON-encoding it. Set "tool" and "arguments" to null.
- "statePatch" is null, or a JSON-encoded string of a JSON Merge Patch object applied to the current state.`

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
	Temperature    *float64       `json:"temperature,omitempty"`
	// Provider is nil unless the OpenRouter switch is on, so the request otherwise carries
	// no Provider-specific member at all.
	Provider *providerRouting `json:"provider,omitempty"`
}

// providerRouting is OpenRouter's provider routing object, limited to the one member this
// Adapter may send.
type providerRouting struct {
	RequireParameters bool `json:"require_parameters"`
}

// chatMessage is one Chat Completions message. Content is a string, or for a user
// message built from content parts, a []chatContentPart.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// chatContentPart is one block of a part-array content: a text block, or an image_url
// block whose URL is a data URL holding the image inline.
type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type toolDescription struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

type modelConfig struct {
	Temperature *float64 `json:"temperature"`
}

// buildChatRequest converts the frozen Turn context into one Chat Completions request. It
// adds, drops and reorders nothing: the system message carries the caller's Instructions,
// the Tools in allowlist order, the final output Schema and the current State, and every
// ModelMessage maps onto exactly one chat message.
func (p *Provider) buildChatRequest(request registry.ModelRequest) (chatRequest, error) {
	system, err := systemPrompt(request)
	if err != nil {
		return chatRequest{}, err
	}
	messages := make([]chatMessage, 0, len(request.Messages)+1)
	messages = append(messages, chatMessage{Role: roleSystem, Content: system})
	for i, message := range request.Messages {
		converted, err := chatMessageFor(message)
		if err != nil {
			return chatRequest{}, fmt.Errorf("message %d: %w", i, err)
		}
		messages = append(messages, converted)
	}

	var config modelConfig
	if present(request.ModelConfig) {
		if err := json.Unmarshal(request.ModelConfig, &config); err != nil {
			return chatRequest{}, fmt.Errorf("model config is not a JSON object: %w", err)
		}
	}

	schema, err := p.decision.schemaFor(request.FinalOutputSchema)
	if err != nil {
		return chatRequest{}, err
	}

	var routing *providerRouting
	if p.requireParameters {
		routing = &providerRouting{RequireParameters: true}
	}

	return chatRequest{
		Model:    p.model,
		Messages: messages,
		ResponseFormat: responseFormat{
			Type: "json_schema",
			JSONSchema: jsonSchema{
				Name:   decisionFormatName,
				Strict: true,
				Schema: schema,
			},
		},
		Temperature: config.Temperature,
		Provider:    routing,
	}, nil
}

func systemPrompt(request registry.ModelRequest) (string, error) {
	var b strings.Builder
	if instructions := strings.TrimSpace(request.Instructions); instructions != "" {
		b.WriteString(instructions)
		b.WriteString("\n\n")
	}
	b.WriteString("## Response format\n")
	b.WriteString(responseGuide)

	b.WriteString("\n\n## Tools\n")
	if len(request.Tools) == 0 {
		b.WriteString("No tools are available; reply with FINAL.")
	} else {
		tools := make([]toolDescription, 0, len(request.Tools))
		for _, tool := range request.Tools {
			tools = append(tools, toolDescription{
				Name:         tool.Name,
				Description:  tool.Description,
				InputSchema:  presentOrNil(tool.InputSchema),
				OutputSchema: presentOrNil(tool.OutputSchema),
			})
		}
		encoded, err := json.Marshal(tools)
		if err != nil {
			return "", fmt.Errorf("encode tools: %w", err)
		}
		b.Write(encoded)
	}

	if present(request.FinalOutputSchema) {
		b.WriteString("\n\n## Final output schema\n")
		b.Write(compact(request.FinalOutputSchema))
	}
	if present(request.State) {
		b.WriteString("\n\n## Current state\n")
		b.Write(compact(request.State))
	}
	return b.String(), nil
}

// chatMessageFor maps one ModelMessage. An assistant message that records a committed
// TOOL_CALL is paired with the following tool message through the Action ID, so the
// Provider sees the call and its result as one exchange.
func chatMessageFor(message registry.ModelMessage) (chatMessage, error) {
	if len(message.Parts) > 0 {
		return partsMessage(message)
	}
	switch message.Role {
	case roleUser:
		return chatMessage{Role: roleUser, Content: contentText(message.Content)}, nil
	case roleAssistant:
		converted := chatMessage{Role: roleAssistant, Content: contentText(message.Content)}
		if message.ToolActionID != nil && message.ToolName != nil {
			converted.ToolCalls = []chatToolCall{{
				ID:   *message.ToolActionID,
				Type: "function",
				Function: chatFunctionCall{
					Name:      *message.ToolName,
					Arguments: toolCallArguments(message.Content),
				},
			}}
		}
		return converted, nil
	case roleTool:
		if message.ToolActionID == nil || *message.ToolActionID == "" {
			return chatMessage{}, errors.New("tool message names no Action")
		}
		return chatMessage{Role: roleTool, Content: contentText(message.Content), ToolCallID: *message.ToolActionID}, nil
	default:
		return chatMessage{}, fmt.Errorf("unknown role %q", message.Role)
	}
}

// partsMessage maps a user message whose content is a part array. Errors name the part
// index and the problem only, never the part's text or image bytes.
func partsMessage(message registry.ModelMessage) (chatMessage, error) {
	if message.Role != roleUser {
		return chatMessage{}, fmt.Errorf("content parts are only accepted on a user message, not %q", message.Role)
	}
	if present(message.Content) {
		return chatMessage{}, errors.New("message carries both content and content parts")
	}
	parts := make([]chatContentPart, 0, len(message.Parts))
	for i, part := range message.Parts {
		switch part.Type {
		case registry.ContentPartText:
			parts = append(parts, chatContentPart{Type: "text", Text: part.Text})
		case registry.ContentPartImage:
			if part.Image == nil || len(part.Image.Data) == 0 || strings.TrimSpace(part.Image.MediaType) == "" {
				return chatMessage{}, fmt.Errorf("content part %d: image part has no media type or no bytes", i)
			}
			url := "data:" + part.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Image.Data)
			parts = append(parts, chatContentPart{Type: "image_url", ImageURL: &chatImageURL{URL: url}})
		default:
			return chatMessage{}, fmt.Errorf("content part %d: unknown part type", i)
		}
	}
	return chatMessage{Role: roleUser, Content: parts}, nil
}

// contentText sends a JSON string as its text and any other JSON value as its JSON text.
func contentText(content json.RawMessage) string {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	return string(compact(content))
}

// toolCallArguments reads the arguments member of a recorded TOOL_CALL Decision. A record
// without one yields an empty object, which the Provider accepts as "no arguments".
func toolCallArguments(content json.RawMessage) string {
	var recorded struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(content, &recorded); err != nil || !present(recorded.Arguments) {
		return "{}"
	}
	return string(compact(recorded.Arguments))
}

func present(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func presentOrNil(raw json.RawMessage) json.RawMessage {
	if !present(raw) {
		return nil
	}
	return compact(raw)
}

func compact(raw json.RawMessage) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return bytes.TrimSpace(raw)
	}
	return out.Bytes()
}
