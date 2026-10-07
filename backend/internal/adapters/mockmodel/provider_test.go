package mockmodel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func userMessage(t *testing.T, text string) registry.ModelMessage {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("json.Marshal(%q) error = %v", text, err)
	}
	return registry.ModelMessage{Role: "user", Content: encoded}
}

func decodeOutput(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		t.Fatalf("decode output %s: %v", raw, err)
	}
	return text
}

func TestProvider_Models_RegistersTextModelV1WithBothCapabilities(t *testing.T) {
	p := NewProvider()
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models() error = %v, want nil", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(Models()) = %d, want 2 (text-model-v1 and image-model-v1)", len(models))
	}
	got := models[0]
	if got.ID != ModelID {
		t.Fatalf("Models()[0].ID = %q, want %q", got.ID, ModelID)
	}
	for _, want := range []string{domain.ModelCapabilityTextGeneration, domain.ModelCapabilityStructuredDecision} {
		if !got.HasCapability(want) {
			t.Fatalf("Models()[0].Capabilities = %v, want to contain %q", got.Capabilities, want)
		}
	}
	if _, err := registry.CompileSchema(got.ConfigSchema); err != nil {
		t.Fatalf("ConfigSchema does not compile: %v", err)
	}
}

// TestProvider_Models_RegistersImageModelV1WithImageGenerationCapability covers the second
// registration this Provider now serves: a Model ID the image_generation Node Type
// can validate against. This Provider never calls Generate for it -- image_generation
// dispatches through a TaskDispatcher (mocktask), not a ModelProvider -- so only the
// registration itself is asserted here.
func TestProvider_Models_RegistersImageModelV1WithImageGenerationCapability(t *testing.T) {
	p := NewProvider()
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models() error = %v, want nil", err)
	}
	got := models[1]
	if got.ID != ImageModelID {
		t.Fatalf("Models()[1].ID = %q, want %q", got.ID, ImageModelID)
	}
	if !got.HasCapability(domain.ModelCapabilityImageGeneration) {
		t.Fatalf("Models()[1].Capabilities = %v, want to contain %q", got.Capabilities, domain.ModelCapabilityImageGeneration)
	}
	if _, err := registry.CompileSchema(got.ConfigSchema); err != nil {
		t.Fatalf("ConfigSchema does not compile: %v", err)
	}
}

func TestProvider_Models_ConfigSchemaRejectsUnknownFieldAndOutOfRangeTemperature(t *testing.T) {
	p := NewProvider()
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models() error = %v, want nil", err)
	}
	schema, err := registry.CompileSchema(models[0].ConfigSchema)
	if err != nil {
		t.Fatalf("CompileSchema() error = %v, want nil", err)
	}
	if err := registry.ValidateValue(schema, map[string]any{"temperature": 5}); err == nil {
		t.Fatal("ValidateValue(temperature=5) error = nil, want error (exceeds maximum 2)")
	}
	if err := registry.ValidateValue(schema, map[string]any{"unexpected": true}); err == nil {
		t.Fatal("ValidateValue(unexpected field) error = nil, want error (additionalProperties: false)")
	}
	if err := registry.ValidateValue(schema, map[string]any{"temperature": 0.5}); err != nil {
		t.Fatalf("ValidateValue(temperature=0.5) error = %v, want nil", err)
	}
}

func TestProvider_Generate_NoDirectiveReturnsFinalEchoDecision(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "hello there")}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if response.Decision.Kind != registry.DecisionFinal {
		t.Fatalf("Decision.Kind = %q, want %q", response.Decision.Kind, registry.DecisionFinal)
	}
	if got := decodeOutput(t, response.Decision.Output); got != "echo: hello there" {
		t.Fatalf("Decision.Output = %q, want %q", got, "echo: hello there")
	}
}

func TestProvider_Generate_FinalDecisionDerivesTokenUsageFromLengths(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "one two three")}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if response.TokenUsage == nil {
		t.Fatal("TokenUsage = nil, want non-nil")
	}
	if response.TokenUsage.InputTokens != 3 {
		t.Fatalf("TokenUsage.InputTokens = %d, want 3", response.TokenUsage.InputTokens)
	}
	// Output is "echo: one two three", i.e. 4 whitespace-separated fields.
	if response.TokenUsage.OutputTokens != 4 {
		t.Fatalf("TokenUsage.OutputTokens = %d, want 4", response.TokenUsage.OutputTokens)
	}
	if response.TokenUsage.TotalTokens != response.TokenUsage.InputTokens+response.TokenUsage.OutputTokens {
		t.Fatalf("TokenUsage.TotalTokens = %d, want InputTokens+OutputTokens", response.TokenUsage.TotalTokens)
	}
}

func TestProvider_Generate_ResponseSummaryIsBoundedAndDeterministicPerDecision(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "repeatable")}}
	first, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	second, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if first.ResponseSummary.ResponseSHA256 != second.ResponseSummary.ResponseSHA256 {
		t.Fatalf("ResponseSHA256 differs across identical requests: %q vs %q", first.ResponseSummary.ResponseSHA256, second.ResponseSummary.ResponseSHA256)
	}
	if first.ResponseSummary.ProviderRequestID == nil || *first.ResponseSummary.ProviderRequestID == "" {
		t.Fatal("ProviderRequestID = nil/empty, want a bounded deterministic id")
	}
}

func TestProvider_Generate_MockFailDirectiveReturnsErrorAndNoResponse(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "mock:fail")}}
	_, err := p.Generate(context.Background(), request)
	if err == nil {
		t.Fatal("Generate() error = nil, want error for mock:fail directive")
	}
}

func TestProvider_Generate_MockToolCallDirectiveReturnsToolCallDecision(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "mock:tool-call:lookup_customer")}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if response.Decision.Kind != registry.DecisionToolCall {
		t.Fatalf("Decision.Kind = %q, want %q", response.Decision.Kind, registry.DecisionToolCall)
	}
	if response.Decision.ToolName == nil || *response.Decision.ToolName != "lookup_customer" {
		t.Fatalf("Decision.ToolName = %v, want %q", response.Decision.ToolName, "lookup_customer")
	}
	if string(response.Decision.Arguments) != "{}" {
		t.Fatalf("Decision.Arguments = %s, want {} when the directive carries no arguments", response.Decision.Arguments)
	}
}

// TestProvider_Generate_MockToolCallDirectiveWithArguments_SetsDecisionArguments covers
// the tool-call directive with arguments: "mock:tool-call:<name>:<json-arguments>" lets a
// caller reach a non-empty TOOL_CALL over the public HTTP surface (Run input), not only
// through the in-process Script hook. remote_lookup (internal/tools/remotelookup)
// requires a non-empty "key", so a directive that cannot carry arguments can never
// dispatch that Tool.
func TestProvider_Generate_MockToolCallDirectiveWithArguments_SetsDecisionArguments(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{
		userMessage(t, `mock:tool-call:remote_lookup:{"key":"k1"}`),
	}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if response.Decision.Kind != registry.DecisionToolCall {
		t.Fatalf("Decision.Kind = %q, want %q", response.Decision.Kind, registry.DecisionToolCall)
	}
	if response.Decision.ToolName == nil || *response.Decision.ToolName != "remote_lookup" {
		t.Fatalf("Decision.ToolName = %v, want %q", response.Decision.ToolName, "remote_lookup")
	}
	if string(response.Decision.Arguments) != `{"key":"k1"}` {
		t.Fatalf("Decision.Arguments = %s, want %s", response.Decision.Arguments, `{"key":"k1"}`)
	}
}

// TestProvider_Generate_MockToolCallDirectiveMalformedArguments_ReturnsError covers the
// "never a silently empty call" requirement: arguments JSON that does not parse must fail
// Generate outright, not fall back to "{}".
func TestProvider_Generate_MockToolCallDirectiveMalformedArguments_ReturnsError(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{
		userMessage(t, `mock:tool-call:remote_lookup:{not-json`),
	}}
	if _, err := p.Generate(context.Background(), request); err == nil {
		t.Fatal("Generate() error = nil, want error for malformed tool-call arguments JSON")
	}
}

// TestProvider_Generate_MockToolCallDirectiveNoToolName_ReturnsError covers the same
// requirement for a directive that names no Tool at all.
func TestProvider_Generate_MockToolCallDirectiveNoToolName_ReturnsError(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{
		userMessage(t, "mock:tool-call:"),
	}}
	if _, err := p.Generate(context.Background(), request); err == nil {
		t.Fatal("Generate() error = nil, want error when the directive names no Tool")
	}
}

// TestProvider_Generate_MockToolCallDirectiveWithPriorToolMessage_ReturnsFinal covers the
// second-turn behaviour the Agent Loop's persisted safe points force on this fixture:
// Context Version 0's "user" message is the directive text, and it never gets
// rewritten, so it is still the last "user" role message once a "tool" role message has
// been appended after the Tool result comes back. Without special handling this Provider
// would read the same directive again and loop TOOL_CALL forever; instead it must answer
// FINAL once a "tool" message is present, so the Agent Loop can reach a terminal Turn.
func TestProvider_Generate_MockToolCallDirectiveWithPriorToolMessage_ReturnsFinal(t *testing.T) {
	p := NewProvider()
	toolName := "remote_lookup"
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{
		userMessage(t, `mock:tool-call:remote_lookup:{"key":"k1"}`),
		{Role: "assistant", Content: json.RawMessage(`{"kind":"TOOL_CALL"}`)},
		{Role: "tool", Content: json.RawMessage(`{"key":"k1","record":"record for k1"}`), ToolName: &toolName},
	}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if response.Decision.Kind != registry.DecisionFinal {
		t.Fatalf("Decision.Kind = %q, want %q (the directive must not loop once a tool result exists)", response.Decision.Kind, registry.DecisionFinal)
	}
	got := decodeOutput(t, response.Decision.Output)
	if !strings.Contains(got, "record for k1") {
		t.Fatalf("Decision.Output = %q, want it to echo the prior tool result", got)
	}
}

func TestProvider_Generate_MockInvalidDecisionDirectiveOmitsRequiredToolFields(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "mock:invalid-decision")}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if response.Decision.Kind != registry.DecisionToolCall {
		t.Fatalf("Decision.Kind = %q, want %q", response.Decision.Kind, registry.DecisionToolCall)
	}
	if response.Decision.ToolName != nil {
		t.Fatalf("Decision.ToolName = %v, want nil (invalid envelope)", response.Decision.ToolName)
	}
	if len(response.Decision.Arguments) != 0 {
		t.Fatalf("Decision.Arguments = %s, want empty (invalid envelope)", response.Decision.Arguments)
	}
}

func TestProvider_Generate_ScriptOverridesDirective(t *testing.T) {
	p := NewProvider()
	p.Script = func(registry.ModelRequest) *Scenario {
		return &Scenario{Kind: ScenarioFinal, Output: "scripted"}
	}
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "mock:fail")}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil (Script overrides the mock:fail directive)", err)
	}
	if got := decodeOutput(t, response.Decision.Output); got != "scripted" {
		t.Fatalf("Decision.Output = %q, want %q", got, "scripted")
	}
}

func TestProvider_Generate_ScriptNilFallsBackToDirective(t *testing.T) {
	p := NewProvider()
	p.Script = func(registry.ModelRequest) *Scenario { return nil }
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "mock:fail")}}
	if _, err := p.Generate(context.Background(), request); err == nil {
		t.Fatal("Generate() error = nil, want error: Script returning nil should fall back to the mock:fail directive")
	}
}

func TestProvider_Generate_ScenarioFailWithErrOverrideWrapsThatError(t *testing.T) {
	wantErr := errors.New("injected provider failure")
	p := NewProvider()
	p.Script = func(registry.ModelRequest) *Scenario {
		return &Scenario{Kind: ScenarioFail, Err: wantErr}
	}
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "irrelevant")}}
	_, err := p.Generate(context.Background(), request)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Generate() error = %v, want wrapped %v", err, wantErr)
	}
}

func TestProvider_Generate_BeforeReturnHookRunsBeforeGenerateReturns(t *testing.T) {
	p := NewProvider()
	var called bool
	p.BeforeReturn = func(context.Context) { called = true }
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "hello")}}
	if _, err := p.Generate(context.Background(), request); err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if !called {
		t.Fatal("BeforeReturn was not invoked")
	}
}

func TestProvider_Requests_RecordsEachGenerateCallInOrder(t *testing.T) {
	p := NewProvider()
	ctx := context.Background()
	if _, err := p.Generate(ctx, registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "first")}}); err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if _, err := p.Generate(ctx, registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "second")}}); err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	requests := p.Requests()
	if len(requests) != 2 {
		t.Fatalf("len(Requests()) = %d, want 2", len(requests))
	}
	if got := decodeOutput(t, requests[0].Messages[0].Content); got != "first" {
		t.Fatalf("Requests()[0] first user message = %q, want %q", got, "first")
	}
	if got := decodeOutput(t, requests[1].Messages[0].Content); got != "second" {
		t.Fatalf("Requests()[1] first user message = %q, want %q", got, "second")
	}
}

func TestProvider_Requests_IsBoundedAndDropsOldestEntry(t *testing.T) {
	p := &Provider{maxRecorded: 2}
	ctx := context.Background()
	for _, text := range []string{"a", "b", "c"} {
		if _, err := p.Generate(ctx, registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, text)}}); err != nil {
			t.Fatalf("Generate() error = %v, want nil", err)
		}
	}
	requests := p.Requests()
	if len(requests) != 2 {
		t.Fatalf("len(Requests()) = %d, want 2 (bounded)", len(requests))
	}
	if got := decodeOutput(t, requests[0].Messages[0].Content); got != "b" {
		t.Fatalf("Requests()[0] = %q, want %q (oldest \"a\" dropped)", got, "b")
	}
	if got := decodeOutput(t, requests[1].Messages[0].Content); got != "c" {
		t.Fatalf("Requests()[1] = %q, want %q", got, "c")
	}
}

func TestProvider_Requests_ReturnsACopyNotTheInternalSlice(t *testing.T) {
	p := NewProvider()
	ctx := context.Background()
	if _, err := p.Generate(ctx, registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "hello")}}); err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	requests := p.Requests()
	requests[0] = registry.ModelRequest{}
	again := p.Requests()
	if len(again[0].Messages) == 0 {
		t.Fatal("mutating the slice returned by Requests() corrupted the Provider's internal recording")
	}
}

// TestProvider_Generate_ScenarioOmitTokenUsage_NormalisesToNil covers the negative
// half of the Token usage acceptance row: a Provider that reports no usage at all must normalise to
// TokenUsage == nil, not to a zeroed domain.TokenUsage. The distinction is a persisted
// fact -- agent_turns.token_usage is nullable, and a zeroed struct would claim the model
// call consumed exactly zero tokens rather than that the Provider said nothing.
//
// It is asserted for every Decision-returning Scenario, since all three share one
// response envelope builder and a per-kind regression would otherwise go unnoticed.
func TestProvider_Generate_ScenarioOmitTokenUsage_NormalisesToNil(t *testing.T) {
	cases := []struct {
		name     string
		scenario Scenario
	}{
		{"final", Scenario{Kind: ScenarioFinal, Output: "done", OmitTokenUsage: true}},
		{"tool-call", Scenario{Kind: ScenarioToolCall, ToolName: "lookup", OmitTokenUsage: true}},
		{"invalid-decision", Scenario{Kind: ScenarioInvalidDecision, OmitTokenUsage: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProvider()
			scenario := tc.scenario
			p.Script = func(registry.ModelRequest) *Scenario { return &scenario }
			request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "one two three")}}

			response, err := p.Generate(context.Background(), request)
			if err != nil {
				t.Fatalf("Generate() error = %v, want nil", err)
			}
			if response.TokenUsage != nil {
				t.Fatalf("TokenUsage = %+v, want nil: the Provider reported no usage", *response.TokenUsage)
			}
			// The rest of the envelope is unaffected: omitting usage is not an error and
			// must not cost the Turn its bounded response diagnostics.
			if response.ResponseSummary.ResponseSHA256 == "" {
				t.Errorf("ResponseSummary.ResponseSHA256 is empty, want the usual bounded summary")
			}
			if response.ResponseSummary.ProviderRequestID == nil || *response.ResponseSummary.ProviderRequestID == "" {
				t.Errorf("ResponseSummary.ProviderRequestID = %v, want a bounded deterministic id", response.ResponseSummary.ProviderRequestID)
			}
		})
	}
}

// TestProvider_Generate_ReportedTokenUsageIsNonNegativeAndTotalsExactly covers the
// positive half of the same Token usage row for the boundary the length-derived count makes
// reachable: an empty prompt and an empty output. There is no separate usage
// normalisation function anywhere in internal/registry or this package -- a Provider's
// returned *domain.TokenUsage is the normalised value and the Runtime stores it as-is
// (internal/service/agent_turn.go, AgentTurns().MarkCompleted) -- so the invariant
// "non-negative input/output, total exactly their sum" has to hold at this Adapter's
// boundary, including where both counts collapse to zero.
func TestProvider_Generate_ReportedTokenUsageIsNonNegativeAndTotalsExactly(t *testing.T) {
	p := NewProvider()
	p.Script = func(registry.ModelRequest) *Scenario {
		// A tool-call Decision has no output text at all, so OutputTokens is 0 rather
		// than merely small.
		return &Scenario{Kind: ScenarioToolCall, ToolName: "lookup", ToolArguments: json.RawMessage(`{"key":"k1"}`)}
	}
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "")}}

	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if response.TokenUsage == nil {
		t.Fatal("TokenUsage = nil, want non-nil: this Scenario does not omit usage")
	}
	usage := *response.TokenUsage
	if usage.InputTokens < 0 || usage.OutputTokens < 0 {
		t.Errorf("TokenUsage = %+v, want non-negative input and output counts", usage)
	}
	if usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		t.Errorf("TokenUsage.TotalTokens = %d, want InputTokens+OutputTokens = %d",
			usage.TotalTokens, usage.InputTokens+usage.OutputTokens)
	}
	if usage.OutputTokens != 0 {
		t.Errorf("TokenUsage.OutputTokens = %d, want 0 for a TOOL_CALL Decision with no output text", usage.OutputTokens)
	}
}
