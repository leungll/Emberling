// Package openaimodel is a registry.ModelProvider for an OpenAI-compatible Chat
// Completions endpoint. One Generate call is one POST to <base URL>/chat/completions that
// asks for strict structured output in the Decision form, and the answer is normalised
// into a registry.ModelResponse.
//
// The Adapter never retries, never writes Runtime state, never emits an Event and never
// selects a replacement model: any answer it cannot normalise is returned as an *Error and
// the caller applies its own model error handling. The API key travels only in the
// Authorization header; it is never part of the request body, a returned error or this
// package's string representation, and this package logs nothing.
package openaimodel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ModelID is the stable Model ID this Adapter registers. A Definition selects it; the
// Provider-side model name stays deployment configuration.
const ModelID = "openai-compatible"

const completionsPath = "/chat/completions"

// maxResponseBytes bounds how much of a Provider answer is read.
const maxResponseBytes = 4 << 20

// maxProviderField bounds every Provider-supplied string copied into a summary or error.
const maxProviderField = 128

const redacted = "[REDACTED]"

// configSchema is the only contract for this model's parameters. temperature has no
// default, so an unset value is not sent and the Provider's own default applies.
const configSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "temperature": {"type": "number", "minimum": 0, "maximum": 2}
  }
}`

// Config is everything one Provider needs. HTTPClient may be nil, in which case calls are
// bounded only by the caller's context.
type Config struct {
	BaseURL        string
	APIKey         string
	Model          string
	HTTPClient     *http.Client
	DecisionSchema json.RawMessage
}

// Provider is the OpenAI-compatible registry.ModelProvider.
type Provider struct {
	endpoint string
	apiKey   string
	model    string
	client   *http.Client
	decision strictDecision
}

var (
	_ registry.ModelProvider = (*Provider)(nil)
	_ fmt.Stringer           = (*Provider)(nil)
	_ fmt.GoStringer         = (*Provider)(nil)
	_ slog.LogValuer         = (*Provider)(nil)
)

// New validates cfg and derives the strict Decision form once.
func New(cfg Config) (*Provider, error) {
	var missing []string
	if strings.TrimSpace(cfg.BaseURL) == "" {
		missing = append(missing, "base URL")
	}
	if cfg.APIKey == "" {
		missing = append(missing, "API key")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		missing = append(missing, "model name")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("openaimodel: missing %s", strings.Join(missing, ", "))
	}
	decision, err := newStrictDecision(cfg.DecisionSchema)
	if err != nil {
		return nil, fmt.Errorf("openaimodel: %w", err)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &Provider{
		endpoint: strings.TrimRight(cfg.BaseURL, "/") + completionsPath,
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		client:   client,
		decision: decision,
	}, nil
}

// String, GoString and LogValue keep the API key out of every rendering of a Provider.
func (p *Provider) String() string {
	return fmt.Sprintf("openaimodel.Provider{endpoint: %s, model: %s, apiKey: %s}", p.endpoint, p.model, redacted)
}

func (p *Provider) GoString() string { return p.String() }

func (p *Provider) LogValue() slog.Value { return slog.StringValue(p.String()) }

// Models returns the single model this Provider serves.
func (p *Provider) Models(context.Context) ([]registry.ModelRegistration, error) {
	return []registry.ModelRegistration{{ModelMetadata: domain.ModelMetadata{
		ID:           ModelID,
		DisplayName:  "OpenAI-compatible (" + p.model + ")",
		Capabilities: []string{domain.ModelCapabilityTextGeneration, domain.ModelCapabilityStructuredDecision},
		ConfigSchema: json.RawMessage(configSchema),
	}}}, nil
}

// Error is a Provider answer, or the absence of one, that could not be normalised.
// StatusCode is the HTTP status when the Provider answered, and 0 otherwise. It wraps the
// caller's context error when that context ended the call, so the caller can tell its own
// deadline from a Provider failure.
type Error struct {
	StatusCode int
	message    string
	cause      error
}

func (e *Error) Error() string { return "openaimodel: " + e.message }

func (e *Error) Unwrap() error { return e.cause }

type chatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
		TotalTokens      *int `json:"total_tokens"`
	} `json:"usage"`
}

type providerError struct {
	Error struct {
		Type any `json:"type"`
		Code any `json:"code"`
	} `json:"error"`
}

// Generate performs exactly one Chat Completions call for request.
func (p *Provider) Generate(ctx context.Context, request registry.ModelRequest) (registry.ModelResponse, error) {
	chat, err := p.buildChatRequest(request)
	if err != nil {
		return registry.ModelResponse{}, p.fail(0, "build request: "+err.Error(), nil)
	}
	body, err := json.Marshal(chat)
	if err != nil {
		return registry.ModelResponse{}, p.fail(0, "encode request: "+err.Error(), nil)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return registry.ModelResponse{}, p.fail(0, "build HTTP request: "+err.Error(), nil)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)

	response, err := p.client.Do(httpRequest)
	if err != nil {
		return registry.ModelResponse{}, p.sendFailure(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return registry.ModelResponse{}, p.sendFailure(ctx, err)
	}
	if len(raw) > maxResponseBytes {
		return registry.ModelResponse{}, p.fail(response.StatusCode, fmt.Sprintf("response body exceeds %d bytes", maxResponseBytes), nil)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return registry.ModelResponse{}, p.fail(response.StatusCode, statusMessage(response.StatusCode, raw), nil)
	}
	return p.normalise(response.StatusCode, raw)
}

// sendFailure distinguishes the caller's context ending the call, which is wrapped so the
// caller recognises its own deadline or cancellation, from a transport failure or this
// client's own timeout, which is reported without wrapping a context error.
func (p *Provider) sendFailure(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return p.fail(0, "call ended by the caller's context", ctxErr)
	}
	return p.fail(0, "send request: "+err.Error(), nil)
}

func (p *Provider) normalise(status int, raw []byte) (registry.ModelResponse, error) {
	var answer chatResponse
	if err := json.Unmarshal(raw, &answer); err != nil {
		return registry.ModelResponse{}, p.fail(status, "response body is not a JSON chat completion", nil)
	}
	if len(answer.Choices) == 0 {
		return registry.ModelResponse{}, p.fail(status, "response has no choices", nil)
	}
	choice := answer.Choices[0]
	if choice.Message.Refusal != nil && *choice.Message.Refusal != "" {
		return registry.ModelResponse{}, p.fail(status, "model refused to answer", nil)
	}
	if choice.Message.Content == nil {
		return registry.ModelResponse{}, p.fail(status, "response message has no content", nil)
	}
	decision, err := p.decision.parse(*choice.Message.Content)
	if err != nil {
		message := err.Error()
		if choice.FinishReason != nil {
			message += " (finish_reason " + bounded(*choice.FinishReason) + ")"
		}
		return registry.ModelResponse{}, p.fail(status, message, nil)
	}
	usage, err := tokenUsage(answer)
	if err != nil {
		return registry.ModelResponse{}, p.fail(status, err.Error(), nil)
	}

	sum := sha256.Sum256(raw)
	summary := registry.ModelResponseSummary{ResponseSHA256: hex.EncodeToString(sum[:])}
	if answer.ID != "" {
		id := bounded(answer.ID)
		summary.ProviderRequestID = &id
	}
	if choice.FinishReason != nil {
		reason := bounded(*choice.FinishReason)
		summary.FinishReason = &reason
	}
	return registry.ModelResponse{Decision: decision, ResponseSummary: summary, TokenUsage: usage}, nil
}

// tokenUsage maps the Provider's usage. No usage is nil, never a zeroed value; a missing
// total is the sum of the two parts; a negative count or a total that disagrees with its
// parts cannot be normalised.
func tokenUsage(answer chatResponse) (*domain.TokenUsage, error) {
	if answer.Usage == nil {
		return nil, nil
	}
	if answer.Usage.PromptTokens == nil || answer.Usage.CompletionTokens == nil {
		return nil, errors.New("usage lacks prompt_tokens or completion_tokens")
	}
	input, output := *answer.Usage.PromptTokens, *answer.Usage.CompletionTokens
	total := input + output
	if answer.Usage.TotalTokens != nil {
		total = *answer.Usage.TotalTokens
	}
	if input < 0 || output < 0 || total != input+output {
		return nil, errors.New("usage token counts are inconsistent")
	}
	return &domain.TokenUsage{InputTokens: input, OutputTokens: output, TotalTokens: total}, nil
}

// statusMessage names the HTTP status and the Provider's error type and code. The
// Provider's own error text is left out: it can echo part of the presented credential.
func statusMessage(status int, raw []byte) string {
	message := fmt.Sprintf("chat completions returned HTTP %d", status)
	var parsed providerError
	if json.Unmarshal(raw, &parsed) != nil {
		return message
	}
	if text := scalarText(parsed.Error.Type); text != "" {
		message += ", type " + text
	}
	if text := scalarText(parsed.Error.Code); text != "" {
		message += ", code " + text
	}
	return message
}

func scalarText(value any) string {
	switch v := value.(type) {
	case string:
		return bounded(v)
	case float64:
		return fmt.Sprintf("%g", v)
	default:
		return ""
	}
}

func bounded(value string) string {
	if len(value) > maxProviderField {
		return value[:maxProviderField]
	}
	return value
}

// fail builds the returned error. The API key is scrubbed from the message as a second
// line of defence: no message is built from it, but a transport error could echo
// request details.
func (p *Provider) fail(status int, message string, cause error) error {
	if p.apiKey != "" {
		message = strings.ReplaceAll(message, p.apiKey, redacted)
	}
	return &Error{StatusCode: status, message: message, cause: cause}
}
