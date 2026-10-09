// Package generateimage implements the `generate_image` Tool: one synchronous image
// generation request to the deterministic Mock Provider's POST /v1/assets, derived from a
// photo of the Run's photo set and a settings object.
//
// It performs exactly one registered operation. It owns no retry, timeout, state
// transition or Event: a rejected or failed generation is reported upward as an error and
// the Agent Runtime decides what happens next.
//
// Its errors carry only the Tool name, the stable Provider identifier, a fixed operation
// label and an HTTP status code - never the arguments, the settings or a response body.
package generateimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "generate_image"

// FactType is the execution fact a successful generation establishes about the new asset.
const FactType = "image_generated"

// ProviderID is the stable identifier of the Provider this Tool calls; it is not a
// credential.
const ProviderID = "mock-asset-provider-v1"

const assetsPath = "/v1/assets"

// defaultTimeout bounds one request when no HTTP client is injected. The Agent deadline
// still arrives through ctx and wins whenever it is shorter.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds the generation response this Tool reads.
const maxResponseBytes = 64 << 10

// mockControlKey names the settings member that selects a Mock Provider scenario. With the
// value mockControlFail the Provider refuses the generation, which is a definite failure:
// no asset exists afterwards.
const (
	mockControlKey  = "mock"
	mockControlFail = "fail"
	outcomeFailed   = "failed"
)

const inputSchema = `{
  "type": "object",
  "properties": {
    "photoAssetId": {"type": "string", "minLength": 1},
    "settings": {"type": "object"}
  },
  "required": ["photoAssetId", "settings"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "assetRef": {"type": "string", "minLength": 1},
    "imageUrl": {"type": "string", "minLength": 1},
    "settingsDigest": {"type": "string", "minLength": 1}
  },
  "required": ["assetRef", "imageUrl", "settingsDigest"],
  "additionalProperties": false
}`

// Registration returns the `generate_image` ToolRegistration calling the Mock Provider at
// baseURL. A nil client means a default client bounded by defaultTimeout.
//
// Generation creates a Provider asset, so the call has an EXTERNAL side effect. Its
// idempotency is UNKNOWN: the Runtime hands an Agent Tool no idempotency key, so nothing
// here may claim KEYED. Each call counts toward the Agent's generation limit.
//
// A successful result establishes an `image_generated` fact about the returned assetRef,
// bound to the source photo and to the digest of the settings that produced it, so a later
// review or video call can prove which photo and settings the asset came from.
func Registration(baseURL string, client *http.Client) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Description:   "Generate an image asset from one photo of the Run's photo set and a settings object",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       FactType,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/assetRef"},
				BindArguments: map[string]domain.FactPointer{
					"photoAssetId":   {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
					"settingsDigest": {Source: domain.FactPointerResult, Pointer: "/settingsDigest"},
				},
			},
			Requires:                    []domain.FactRequirement{},
			CountsTowardGenerationLimit: true,
		},
		Executor: New(baseURL, client),
	}
}

// Executor implements registry.ToolExecutor for `generate_image`.
type Executor struct {
	baseURL    string
	httpClient *http.Client
}

// New returns an Executor calling the Mock Provider at baseURL.
func New(baseURL string, client *http.Client) *Executor {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Executor{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

type arguments struct {
	PhotoAssetID string          `json:"photoAssetId"`
	Settings     json.RawMessage `json:"settings"`
}

type assetRequest struct {
	PhotoAssetID   string `json:"photoAssetId"`
	SettingsDigest string `json:"settingsDigest"`
	Outcome        string `json:"outcome,omitempty"`
}

type assetResponse struct {
	AssetID  string `json:"assetId"`
	ImageURL string `json:"imageUrl"`
}

// result is the Tool's result; field order is the result's key order.
type result struct {
	AssetRef       string `json:"assetRef"`
	ImageURL       string `json:"imageUrl"`
	SettingsDigest string `json:"settingsDigest"`
}

// Execute sends one generation request and returns the new asset's reference.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.PhotoAssetID == "" {
		return registry.ToolExecutionResult{}, generationError("build request", 0, "arguments carry no photoAssetId")
	}
	digest, settings, err := SettingsDigest(args.Settings)
	if err != nil {
		return registry.ToolExecutionResult{}, generationError("build request", 0, "settings is not a JSON object")
	}
	request := assetRequest{PhotoAssetID: args.PhotoAssetID, SettingsDigest: digest}
	if control, ok := settings[mockControlKey].(string); ok && control == mockControlFail {
		request.Outcome = outcomeFailed
	}

	body, err := json.Marshal(request)
	if err != nil {
		return registry.ToolExecutionResult{}, generationError("build request", 0, "request body could not be encoded")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+assetsPath, bytes.NewReader(body))
	if err != nil {
		return registry.ToolExecutionResult{}, generationError("build request", 0, "request could not be created")
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := e.httpClient.Do(httpRequest)
	if err != nil {
		return registry.ToolExecutionResult{}, generationError("send request", 0, "transport failure")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		// The body is not read into the error: a Provider may echo request content there.
		return registry.ToolExecutionResult{}, generationError("provider rejected the generation", response.StatusCode, "non-200 status")
	}
	var generated assetResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&generated); err != nil {
		return registry.ToolExecutionResult{}, generationError("decode response", response.StatusCode, "response body is not an asset response")
	}
	if generated.AssetID == "" || generated.ImageURL == "" {
		return registry.ToolExecutionResult{}, generationError("decode response", response.StatusCode, "response carries no assetId or imageUrl")
	}

	output, err := json.Marshal(result{AssetRef: generated.AssetID, ImageURL: generated.ImageURL, SettingsDigest: digest})
	if err != nil {
		return registry.ToolExecutionResult{}, generationError("encode result", 0, "result could not be encoded")
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output}}, nil
}

// SettingsDigest returns "sha256:" followed by the lowercase hex SHA-256 of the canonical
// JSON form of a settings object, together with the decoded object. The canonical form
// keeps numbers exactly as written, sorts object keys at every depth, does not escape HTML
// characters and carries no trailing newline, so two settings objects that differ only in
// key order or whitespace share a digest. It must stay byte-compatible with the digest
// the Runtime computes for the same settings, because both are compared as fact bindings.
func SettingsDigest(raw json.RawMessage) (string, map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", nil, fmt.Errorf("decode settings: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", nil, errors.New("decode settings: trailing data after the settings object")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", nil, errors.New("decode settings: settings is not a JSON object")
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(object); err != nil {
		return "", nil, fmt.Errorf("encode settings: %w", err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(canonical.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), object, nil
}

// generationError builds a generation failure from fixed labels only, so no request or
// response content can reach Trace or a log through it.
func generationError(op string, statusCode int, reason string) error {
	if statusCode != 0 {
		return fmt.Errorf("%s: call provider %q: %s (status %d): %w", ToolName, ProviderID, op, statusCode, errors.New(reason))
	}
	return fmt.Errorf("%s: call provider %q: %s: %w", ToolName, ProviderID, op, errors.New(reason))
}
