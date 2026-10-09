package openaimodel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

const testKey = "sk-test-0123456789abcdef"

// captured is what the fake Provider observed of one request.
type captured struct {
	path          string
	authorization string
	body          []byte
}

// fakeProvider answers every request with status and body and records it.
func fakeProvider(t *testing.T, status int, body string) (*httptest.Server, *captured) {
	t.Helper()
	seen := &captured{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen.path = r.URL.Path
		seen.authorization = r.Header.Get("Authorization")
		seen.body = raw
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, seen
}

func newProvider(t *testing.T, baseURL string, client *http.Client) *openaimodel.Provider {
	t.Helper()
	if client == nil {
		client = &http.Client{}
	}
	provider, err := openaimodel.New(openaimodel.Config{
		BaseURL:        baseURL + "/v1/",
		APIKey:         testKey,
		Model:          "gpt-test",
		HTTPClient:     client,
		DecisionSchema: registry.DecisionSchema(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider
}

// completion wraps content as the first choice of a Chat Completions answer.
func completion(t *testing.T, content string, usage string) string {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":` + string(encoded) + `},"finish_reason":"stop"}]`
	if usage != "" {
		body += `,"usage":` + usage
	}
	return body + "}"
}

func strPtr(value string) *string { return &value }

func raw(value string) json.RawMessage { return json.RawMessage(value) }

// agentRequest is a second-Turn Agent request: the initial user input, one committed
// TOOL_CALL with its Tool result, the frozen allowlist, the final output Schema and State.
func agentRequest() registry.ModelRequest {
	actionID := "act_1"
	tool := "lookup"
	return registry.ModelRequest{
		ModelID:      openaimodel.ModelID,
		Instructions: "Find the answer.",
		Messages: []registry.ModelMessage{
			{Role: "user", Content: raw(`"What is the capital?"`)},
			{Role: "assistant", Content: raw(`{"kind":"TOOL_CALL","toolName":"lookup","arguments":{"key":"capital"}}`), ToolName: &tool, ToolActionID: &actionID},
			{Role: "tool", Content: raw(`{"value":"Paris"}`), ToolName: &tool, ToolActionID: &actionID},
		},
		State: raw(`{"step":1}`),
		Tools: []registry.ModelToolSpec{{
			Name: "lookup", Description: "Look a key up.",
			InputSchema:  raw(`{"type":"object","properties":{"key":{"type":"string"}}}`),
			OutputSchema: raw(`{"type":"object"}`),
		}},
		ModelConfig:       raw(`{"temperature":0.3}`),
		FinalOutputSchema: raw(`{"type":"string"}`),
		DecisionSchema:    registry.DecisionSchema(),
	}
}

func TestProvider_Generate_ToolCallAnswer_ReturnsDecisionAndUsage(t *testing.T) {
	content := `{"kind":"TOOL_CALL","tool":"lookup","arguments":"{\"key\":\"capital\"}","output":null,"statePatch":"{\"step\":2}"}`
	server, seen := fakeProvider(t, http.StatusOK, completion(t, content, `{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150}`))
	provider := newProvider(t, server.URL, server.Client())

	response, err := provider.Generate(context.Background(), agentRequest())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	want := registry.ModelDecision{
		Kind: registry.DecisionToolCall, ToolName: strPtr("lookup"),
		Arguments: raw(`{"key":"capital"}`), StatePatch: raw(`{"step":2}`),
	}
	if diff := cmp.Diff(want, response.Decision); diff != "" {
		t.Errorf("Decision mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(&domain.TokenUsage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150}, response.TokenUsage); diff != "" {
		t.Errorf("TokenUsage mismatch (-want +got):\n%s", diff)
	}
	if got := response.ResponseSummary; got.ProviderRequestID == nil || *got.ProviderRequestID != "chatcmpl-1" ||
		got.FinishReason == nil || *got.FinishReason != "stop" || len(got.ResponseSHA256) != 64 {
		t.Errorf("ResponseSummary = %+v, want id chatcmpl-1, finish_reason stop and a SHA-256", got)
	}
	if seen.path != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", seen.path)
	}
	if _, err := runtime.ParseModelDecision(envelope(response.Decision)); err != nil {
		t.Errorf("ParseModelDecision() error = %v, want the normalised Decision to pass the Runtime shape check", err)
	}
}

func TestProvider_Generate_FinalAnswer_ReturnsDecodedOutput(t *testing.T) {
	content := `{"kind":"FINAL","tool":null,"arguments":null,"output":"\"Paris\"","statePatch":null}`
	server, _ := fakeProvider(t, http.StatusOK, completion(t, content, `{"prompt_tokens":7,"completion_tokens":3}`))
	provider := newProvider(t, server.URL, server.Client())

	response, err := provider.Generate(context.Background(), agentRequest())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	want := registry.ModelDecision{Kind: registry.DecisionFinal, Output: raw(`"Paris"`)}
	if diff := cmp.Diff(want, response.Decision); diff != "" {
		t.Errorf("Decision mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(&domain.TokenUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}, response.TokenUsage); diff != "" {
		t.Errorf("TokenUsage without total_tokens mismatch (-want +got):\n%s", diff)
	}
}

func TestProvider_Generate_NoUsage_ReturnsNilTokenUsage(t *testing.T) {
	content := `{"kind":"FINAL","tool":null,"arguments":null,"output":"\"ok\"","statePatch":null}`
	server, _ := fakeProvider(t, http.StatusOK, completion(t, content, ""))
	provider := newProvider(t, server.URL, server.Client())

	response, err := provider.Generate(context.Background(), agentRequest())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.TokenUsage != nil {
		t.Errorf("TokenUsage = %+v, want nil when the Provider reports no usage", response.TokenUsage)
	}
}

// TestProvider_Generate_IncompleteToolCall_LeavesShapeCheckToRuntime pins the boundary
// between normalisation and the Runtime's Decision shape check: an answer in the strict
// form whose TOOL_CALL names no tool is normalised, and the Runtime rejects it as an
// invalid action rather than the Adapter reporting a model error.
func TestProvider_Generate_IncompleteToolCall_LeavesShapeCheckToRuntime(t *testing.T) {
	content := `{"kind":"TOOL_CALL","tool":null,"arguments":null,"output":null,"statePatch":null}`
	server, _ := fakeProvider(t, http.StatusOK, completion(t, content, ""))
	provider := newProvider(t, server.URL, server.Client())

	response, err := provider.Generate(context.Background(), agentRequest())
	if err != nil {
		t.Fatalf("Generate() error = %v, want a normalised Decision", err)
	}
	_, err = runtime.ParseModelDecision(envelope(response.Decision))
	if _, ok := runtime.AsInvalidActionError(err); !ok {
		t.Fatalf("ParseModelDecision() error = %v, want an invalid action", err)
	}
}

func TestProvider_Generate_RequestBody_UsesStrictDecisionFormat(t *testing.T) {
	content := `{"kind":"FINAL","tool":null,"arguments":null,"output":"\"Paris\"","statePatch":null}`
	server, seen := fakeProvider(t, http.StatusOK, completion(t, content, ""))
	provider := newProvider(t, server.URL, server.Client())

	if _, err := provider.Generate(context.Background(), agentRequest()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	var body struct {
		Model          string   `json:"model"`
		Temperature    *float64 `json:"temperature"`
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
		Messages []struct {
			Role       string  `json:"role"`
			Content    *string `json:"content"`
			ToolCallID string  `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}

	if body.Model != "gpt-test" {
		t.Errorf("model = %q, want gpt-test", body.Model)
	}
	if body.Temperature == nil || *body.Temperature != 0.3 {
		t.Errorf("temperature = %v, want 0.3 from the frozen model config", body.Temperature)
	}
	format := body.ResponseFormat
	if format.Type != "json_schema" || format.JSONSchema.Name != "agent_decision" || !format.JSONSchema.Strict {
		t.Errorf("response_format = %+v, want strict json_schema agent_decision", format)
	}
	wantSchema := `{
	  "type": "object",
	  "additionalProperties": false,
	  "properties": {
	    "kind": {"type": "string", "enum": ["TOOL_CALL", "FINAL"]},
	    "tool": {"type": ["string", "null"]},
	    "arguments": {"type": ["string", "null"], "description": "JSON-encoded value, or null when it does not apply"},
	    "output": {"type": ["string", "null"], "description": "JSON-encoded value, or null when it does not apply"},
	    "statePatch": {"type": ["string", "null"], "description": "JSON-encoded value, or null when it does not apply"}
	  },
	  "required": ["kind", "arguments", "output", "statePatch", "tool"]
	}`
	if diff := cmp.Diff(decode(t, []byte(wantSchema)), decode(t, format.JSONSchema.Schema)); diff != "" {
		t.Errorf("strict Decision schema mismatch (-want +got):\n%s", diff)
	}

	if len(body.Messages) != 4 {
		t.Fatalf("messages = %d, want system + user + assistant + tool", len(body.Messages))
	}
	system, user, assistant, tool := body.Messages[0], body.Messages[1], body.Messages[2], body.Messages[3]
	if system.Role != "system" || system.Content == nil {
		t.Fatalf("messages[0] = %+v, want the system prompt", system)
	}
	for _, part := range []string{
		"Find the answer.",
		`"name":"lookup"`, `"description":"Look a key up."`,
		`"inputSchema":{"type":"object","properties":{"key":{"type":"string"}}}`,
		"## Final output schema\n{\"type\":\"string\"}",
		"## Current state\n{\"step\":1}",
	} {
		if !strings.Contains(*system.Content, part) {
			t.Errorf("system prompt lacks %q:\n%s", part, *system.Content)
		}
	}
	if user.Role != "user" || user.Content == nil || *user.Content != "What is the capital?" {
		t.Errorf("messages[1] = %+v, want the user input as text", user)
	}
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("messages[2] = %+v, want one assistant tool call", assistant)
	}
	call := assistant.ToolCalls[0]
	if call.ID != "act_1" || call.Type != "function" || call.Function.Name != "lookup" || call.Function.Arguments != `{"key":"capital"}` {
		t.Errorf("assistant tool call = %+v, want act_1 lookup {\"key\":\"capital\"}", call)
	}
	if tool.Role != "tool" || tool.ToolCallID != "act_1" || tool.Content == nil || *tool.Content != `{"value":"Paris"}` {
		t.Errorf("messages[3] = %+v, want the Tool result paired with act_1", tool)
	}
}

func TestProvider_Generate_RequestNeverCarriesAPIKeyInBody(t *testing.T) {
	content := `{"kind":"FINAL","tool":null,"arguments":null,"output":"\"ok\"","statePatch":null}`
	server, seen := fakeProvider(t, http.StatusOK, completion(t, content, ""))
	provider := newProvider(t, server.URL, server.Client())

	if _, err := provider.Generate(context.Background(), agentRequest()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if bytes.Contains(seen.body, []byte(testKey)) {
		t.Errorf("request body carries the API key: %s", seen.body)
	}
	if seen.authorization != "Bearer "+testKey {
		t.Errorf("Authorization header = %q, want the bearer credential", seen.authorization)
	}
}

func TestProvider_Generate_ProviderRejects_ReturnsStatusWithoutKey(t *testing.T) {
	// The body imitates a Provider that echoes part of the presented key in its message.
	echo := `{"error":{"message":"Incorrect API key provided: ` + testKey + `","type":"invalid_request_error","code":"invalid_api_key"}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"unauthorized", http.StatusUnauthorized, echo, []string{"HTTP 401", "invalid_request_error", "invalid_api_key"}},
		{"server error", http.StatusInternalServerError, `upstream exploded`, []string{"HTTP 500"}},
		{"rate limited", http.StatusTooManyRequests, `{"error":{"type":"rate_limit","code":429}}`, []string{"HTTP 429", "rate_limit", "429"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := fakeProvider(t, tc.status, tc.body)
			provider := newProvider(t, server.URL, server.Client())

			_, err := provider.Generate(context.Background(), agentRequest())

			var modelErr *openaimodel.Error
			if !errors.As(err, &modelErr) {
				t.Fatalf("Generate() error = %v, want *openaimodel.Error", err)
			}
			if modelErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", modelErr.StatusCode, tc.status)
			}
			for _, part := range tc.want {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q lacks %q", err.Error(), part)
				}
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "Incorrect API key") {
				t.Errorf("error carries Provider text or the key: %q", err.Error())
			}
		})
	}
}

// blockingProvider holds every request until the client gives up on it. entered is closed
// once a request has arrived, which is the barrier a test waits on.
func blockingProvider(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	entered := make(chan struct{})
	var once sync.Once
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	return server, entered
}

func TestProvider_Generate_CallerContextCancelled_WrapsContextError(t *testing.T) {
	server, entered := blockingProvider(t)
	provider := newProvider(t, server.URL, server.Client())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-entered
		cancel()
	}()
	_, err := provider.Generate(ctx, agentRequest())

	var modelErr *openaimodel.Error
	if !errors.As(err, &modelErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v, want *openaimodel.Error wrapping context.Canceled", err)
	}
}

func TestProvider_Generate_CallerDeadlinePassed_WrapsDeadlineExceeded(t *testing.T) {
	server, _ := fakeProvider(t, http.StatusOK, "{}")
	provider := newProvider(t, server.URL, server.Client())

	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	_, err := provider.Generate(ctx, agentRequest())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate() error = %v, want it to wrap context.DeadlineExceeded so the Runtime sees its own deadline", err)
	}
}

// TestProvider_Generate_ClientTimeout_IsNotReportedAsCallerDeadline guards the Agent
// timeout decision: the Adapter's own HTTP timeout is a model error, not the Agent Run's
// deadline, so it must not wrap context.DeadlineExceeded.
func TestProvider_Generate_ClientTimeout_IsNotReportedAsCallerDeadline(t *testing.T) {
	server, _ := blockingProvider(t)
	client := server.Client()
	client.Timeout = time.Millisecond
	provider := newProvider(t, server.URL, client)

	_, err := provider.Generate(context.Background(), agentRequest())

	var modelErr *openaimodel.Error
	if !errors.As(err, &modelErr) {
		t.Fatalf("Generate() error = %v, want *openaimodel.Error", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Generate() error = %v wraps context.DeadlineExceeded, want a plain model error", err)
	}
}

func TestProvider_Generate_UnnormalisableAnswer_ReturnsModelError(t *testing.T) {
	valid := `{"kind":"FINAL","tool":null,"arguments":null,"output":"\"ok\"","statePatch":null}`
	for _, tc := range []struct {
		name string
		body string
	}{
		{"body not JSON", `<html>bad gateway</html>`},
		{"no choices", `{"id":"x","choices":[]}`},
		{"content null", `{"choices":[{"message":{"content":null}}]}`},
		{"refusal", `{"choices":[{"message":{"content":null,"refusal":"I cannot help with that."}}]}`},
		{"content not JSON", completion(t, "Sure! Here is the answer.", "")},
		{"content missing a member", completion(t, `{"kind":"FINAL","output":"\"ok\""}`, "")},
		{"content with unknown kind", completion(t, `{"kind":"MAYBE","tool":null,"arguments":null,"output":null,"statePatch":null}`, "")},
		{"content with extra member", completion(t, `{"kind":"FINAL","tool":null,"arguments":null,"output":"\"ok\"","statePatch":null,"why":"x"}`, "")},
		{"encoded member not JSON", completion(t, `{"kind":"FINAL","tool":null,"arguments":null,"output":"Paris","statePatch":null}`, "")},
		{"arguments not a string", completion(t, `{"kind":"TOOL_CALL","tool":"lookup","arguments":{"key":"x"},"output":null,"statePatch":null}`, "")},
		{"negative usage", completion(t, valid, `{"prompt_tokens":-1,"completion_tokens":3,"total_tokens":2}`)},
		{"inconsistent total", completion(t, valid, `{"prompt_tokens":1,"completion_tokens":3,"total_tokens":9}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := fakeProvider(t, http.StatusOK, tc.body)
			provider := newProvider(t, server.URL, server.Client())

			_, err := provider.Generate(context.Background(), agentRequest())

			var modelErr *openaimodel.Error
			if !errors.As(err, &modelErr) {
				t.Fatalf("Generate() error = %v, want *openaimodel.Error", err)
			}
			if modelErr.StatusCode != http.StatusOK {
				t.Errorf("StatusCode = %d, want 200", modelErr.StatusCode)
			}
			if strings.Contains(err.Error(), "I cannot help") {
				t.Errorf("error repeats Provider text: %q", err.Error())
			}
		})
	}
}

func TestProvider_Generate_ToolMessageWithoutAction_FailsBeforeSending(t *testing.T) {
	server, seen := fakeProvider(t, http.StatusOK, "{}")
	provider := newProvider(t, server.URL, server.Client())
	request := agentRequest()
	request.Messages[2].ToolActionID = nil

	_, err := provider.Generate(context.Background(), request)

	var modelErr *openaimodel.Error
	if !errors.As(err, &modelErr) {
		t.Fatalf("Generate() error = %v, want *openaimodel.Error", err)
	}
	if seen.body != nil {
		t.Errorf("Provider received a request: %s", seen.body)
	}
}

func TestProvider_Rendering_RedactsAPIKey(t *testing.T) {
	provider := newProvider(t, "https://llm.example", nil)

	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("provider", "value", provider)
	for _, rendered := range []string{
		fmt.Sprintf("%v", provider), fmt.Sprintf("%+v", provider), fmt.Sprintf("%#v", provider),
		provider.String(), logged.String(),
	} {
		if strings.Contains(rendered, testKey) {
			t.Errorf("rendering leaks the API key: %s", rendered)
		}
	}
}

func TestNew_MissingSetting_FailsWithoutRepeatingKey(t *testing.T) {
	_, err := openaimodel.New(openaimodel.Config{APIKey: testKey, DecisionSchema: registry.DecisionSchema()})
	if err == nil {
		t.Fatal("New() error = nil, want missing base URL and model name")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("error leaks the API key: %q", err.Error())
	}
}

func TestNew_DecisionSchemaWithUnknownMember_Fails(t *testing.T) {
	schema := `{"oneOf":[{"properties":{"kind":{"const":"FINAL"},"output":{},"confidence":{"type":"number"}}}]}`
	_, err := openaimodel.New(openaimodel.Config{
		BaseURL: "https://llm.example", APIKey: testKey, Model: "m", DecisionSchema: raw(schema),
	})
	if err == nil || !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("New() error = %v, want a refusal naming the unknown member", err)
	}
}

func TestProvider_Models_RegistersStableModelID(t *testing.T) {
	provider := newProvider(t, "https://llm.example", nil)
	models := registry.NewModelRegistry()
	if err := models.Register(context.Background(), provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	registration, served, ok := models.Get(openaimodel.ModelID)
	if !ok || served != registry.ModelProvider(provider) {
		t.Fatalf("Get(%q) = %v, want this Provider", openaimodel.ModelID, ok)
	}
	for _, capability := range []string{domain.ModelCapabilityStructuredDecision, domain.ModelCapabilityTextGeneration} {
		if !registration.HasCapability(capability) {
			t.Errorf("registration lacks capability %q", capability)
		}
	}
	if strings.Contains(registration.DisplayName, testKey) {
		t.Errorf("DisplayName leaks the API key: %q", registration.DisplayName)
	}
}

func envelope(decision registry.ModelDecision) runtime.DecisionEnvelope {
	return runtime.DecisionEnvelope{
		Kind:       domain.DecisionKind(decision.Kind),
		ToolName:   decision.ToolName,
		Arguments:  decision.Arguments,
		Output:     decision.Output,
		StatePatch: decision.StatePatch,
	}
}

func decode(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return value
}
