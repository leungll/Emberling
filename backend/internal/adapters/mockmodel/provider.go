// Package mockmodel is a deterministic, in-process registry.ModelProvider used as a
// recovery and failure test fixture (09 §4.4). It registers `text-model-v1` and
// `image-model-v1`, and never calls a live third-party endpoint; every scenario is
// driven by explicit, injected data rather than randomness or wall-clock delay.
package mockmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ModelID is the stable Model ID of this Provider's text generation model.
const ModelID = "text-model-v1"

// ImageModelID is the stable Model ID of this Provider's image-generation-capable model.
// It exists so the image_generation Node Type (internal/nodes/imagegeneration) has a
// registered Model ID to validate against; this Provider never calls Generate for it --
// image_generation dispatches through a TaskDispatcher (mocktask), not a ModelProvider.
const ImageModelID = "image-model-v1"

// mockDirectivePrefix marks the last user message as a scenario trigger instead of plain
// prompt content: "mock:fail", "mock:tool-call:<name>" or "mock:invalid-decision".
const mockDirectivePrefix = "mock:"

// configSchema is the sole contract for this model's parameters (07 §1.4): one optional
// temperature bounded to [0, 2] with a documented default.
const configSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "temperature": {"type": "number", "minimum": 0, "maximum": 2, "default": 0.2}
  }
}`

// ScenarioKind selects one deterministic Generate behaviour.
type ScenarioKind string

const (
	// ScenarioFinal returns a FINAL Decision. It is the default when no directive or
	// Script overrides it.
	ScenarioFinal ScenarioKind = "final"
	// ScenarioFail simulates a Provider response that cannot be normalised at all (07
	// §1.4: MODEL_ERROR path). Generate returns a non-nil error and no ModelResponse.
	ScenarioFail ScenarioKind = "fail"
	// ScenarioToolCall returns a TOOL_CALL Decision, reserved for the M3 Agent Loop
	// track.
	ScenarioToolCall ScenarioKind = "tool-call"
	// ScenarioInvalidDecision returns a Decision whose envelope is normalised but
	// structurally invalid (07 §1.4: INVALID_ACTION path) - here, a TOOL_CALL missing
	// its required tool name and arguments.
	ScenarioInvalidDecision ScenarioKind = "invalid-decision"
)

// Scenario is one deterministic Generate outcome. The zero value is ScenarioFinal with the
// default echo Output.
type Scenario struct {
	Kind ScenarioKind

	// Output overrides the FINAL output text; empty means the default `echo: <prompt>`.
	Output string
	// ToolName and ToolArguments are used only by ScenarioToolCall.
	ToolName      string
	ToolArguments json.RawMessage
	// StatePatch is the Decision's optional state patch (RFC 7386). It is scripted, not
	// derived: the Agent Loop's State transition is a Runtime decision, and this Provider
	// only has to be able to return a Decision that carries one.
	StatePatch json.RawMessage
	// OmitTokenUsage reproduces a Provider that reports no Token usage at all. 09 §3.3
	// requires the normalised result to be nil in that case rather than a zeroed
	// domain.TokenUsage, since "the Provider reported 0 tokens" and "the Provider
	// reported nothing" are different persisted facts.
	OmitTokenUsage bool
	// Err overrides the error returned for ScenarioFail; nil means a default error.
	Err error
}

// Provider is a deterministic, in-process registry.ModelProvider. It holds no Provider
// credential, endpoint or HTTP client: everything it returns is computed from the request
// it receives.
type Provider struct {
	// Script inspects an incoming request and may return an override Scenario. Returning
	// nil falls back to the `mock:` directive in the last user message, or ScenarioFinal.
	Script func(request registry.ModelRequest) *Scenario

	// BeforeReturn is called immediately before Generate returns, after the scenario is
	// resolved and recorded. Tests use it as a barrier instead of a sleep.
	BeforeReturn func(ctx context.Context)

	mu          sync.Mutex
	requests    []registry.ModelRequest
	maxRecorded int
}

// defaultMaxRecorded bounds request recording so a long-running test suite cannot grow
// this fixture's memory without limit.
const defaultMaxRecorded = 256

// NewProvider returns a ready-to-use mock Provider with no Script and no BeforeReturn
// hook.
func NewProvider() *Provider {
	return &Provider{maxRecorded: defaultMaxRecorded}
}

// Models returns the models this Provider serves.
func (p *Provider) Models(context.Context) ([]registry.ModelRegistration, error) {
	return []registry.ModelRegistration{
		{ModelMetadata: domain.ModelMetadata{
			ID:           ModelID,
			DisplayName:  "Mock Text Model v1",
			Capabilities: []string{domain.ModelCapabilityTextGeneration, domain.ModelCapabilityStructuredDecision},
			ConfigSchema: json.RawMessage(configSchema),
		}},
		{ModelMetadata: domain.ModelMetadata{
			ID:           ImageModelID,
			DisplayName:  "Mock Image Model v1",
			Capabilities: []string{domain.ModelCapabilityImageGeneration},
			ConfigSchema: json.RawMessage(configSchema),
		}},
	}, nil
}

// Generate resolves a Scenario for request, records the request and returns the
// deterministic ModelResponse (or error) for that Scenario. It never retries and never
// calls out to a real Provider.
func (p *Provider) Generate(ctx context.Context, request registry.ModelRequest) (registry.ModelResponse, error) {
	p.record(request)

	scenario := p.resolveScenario(request)
	promptText := lastUserMessageText(request)

	if p.BeforeReturn != nil {
		p.BeforeReturn(ctx)
	}

	switch scenario.Kind {
	case ScenarioFail:
		if scenario.Err != nil {
			return registry.ModelResponse{}, fmt.Errorf("mockmodel: scenario fail: %w", scenario.Err)
		}
		return registry.ModelResponse{}, fmt.Errorf("mockmodel: scenario fail: provider response could not be normalised")

	case ScenarioToolCall:
		toolName := scenario.ToolName
		arguments := scenario.ToolArguments
		if arguments == nil {
			arguments = json.RawMessage(`{}`)
		}
		decision := registry.ModelDecision{
			Kind: registry.DecisionToolCall, ToolName: &toolName, Arguments: arguments,
			StatePatch: scenario.StatePatch,
		}
		return p.finish(scenario, decision, promptText, ""), nil

	case ScenarioInvalidDecision:
		// Kind is a recognised value, but TOOL_CALL requires ToolName and Arguments (07
		// §1.4); leaving both empty makes the envelope fail the Runtime's basic shape
		// check without this Adapter failing outright.
		decision := registry.ModelDecision{Kind: registry.DecisionToolCall}
		return p.finish(scenario, decision, promptText, ""), nil

	default:
		output := scenario.Output
		if output == "" {
			output = "echo: " + promptText
		}
		encodedOutput, err := json.Marshal(output)
		if err != nil {
			return registry.ModelResponse{}, fmt.Errorf("mockmodel: encode output: %w", err)
		}
		decision := registry.ModelDecision{
			Kind: registry.DecisionFinal, Output: encodedOutput, StatePatch: scenario.StatePatch,
		}
		return p.finish(scenario, decision, promptText, output), nil
	}
}

// finish builds the common ModelResponse envelope around decision: a bounded response
// summary and Token usage derived deterministically from prompt and output length, or no
// Token usage at all when the Scenario omits it.
func (p *Provider) finish(scenario Scenario, decision registry.ModelDecision, promptText, outputText string) registry.ModelResponse {
	encodedDecision, _ := json.Marshal(decision)
	sum := sha256.Sum256(encodedDecision)
	requestID := "mock-" + hex.EncodeToString(sum[:8])
	finishReason := "stop"

	response := registry.ModelResponse{
		Decision: decision,
		ResponseSummary: registry.ModelResponseSummary{
			ProviderRequestID: &requestID,
			FinishReason:      &finishReason,
			ResponseSHA256:    hex.EncodeToString(sum[:]),
		},
	}
	if scenario.OmitTokenUsage {
		return response
	}
	inputTokens := len(strings.Fields(promptText))
	outputTokens := len(strings.Fields(outputText))
	response.TokenUsage = &domain.TokenUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
	}
	return response
}

// resolveScenario applies Script first, then the `mock:` directive in the last user
// message, and defaults to ScenarioFinal.
func (p *Provider) resolveScenario(request registry.ModelRequest) Scenario {
	if p.Script != nil {
		if scenario := p.Script(request); scenario != nil {
			return *scenario
		}
	}
	text := lastUserMessageText(request)
	if !strings.HasPrefix(text, mockDirectivePrefix) {
		return Scenario{Kind: ScenarioFinal}
	}
	rest := strings.TrimPrefix(text, mockDirectivePrefix)
	kind, arg, _ := strings.Cut(rest, ":")
	switch ScenarioKind(kind) {
	case ScenarioFail:
		return Scenario{Kind: ScenarioFail}
	case ScenarioToolCall:
		return Scenario{Kind: ScenarioToolCall, ToolName: arg}
	case ScenarioInvalidDecision:
		return Scenario{Kind: ScenarioInvalidDecision}
	default:
		return Scenario{Kind: ScenarioFinal}
	}
}

// lastUserMessageText decodes the most recent `user` message as plain text. A message
// whose Content is not a JSON string decodes to its raw bytes so a directive is never
// silently dropped.
func lastUserMessageText(request registry.ModelRequest) string {
	for i := len(request.Messages) - 1; i >= 0; i-- {
		message := request.Messages[i]
		if message.Role != "user" {
			continue
		}
		var text string
		if err := json.Unmarshal(message.Content, &text); err == nil {
			return text
		}
		return string(message.Content)
	}
	return ""
}

// record appends request to the bounded recording, dropping the oldest entry once the
// cap is reached. It never logs request content (10 §2); recording exists only for
// in-process test assertions that hold this Provider directly.
func (p *Provider) record(request registry.ModelRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) >= p.maxRecorded {
		p.requests = p.requests[1:]
	}
	p.requests = append(p.requests, request)
}

// Requests returns a copy of every recorded request, oldest first.
func (p *Provider) Requests() []registry.ModelRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]registry.ModelRequest, len(p.requests))
	copy(out, p.requests)
	return out
}
