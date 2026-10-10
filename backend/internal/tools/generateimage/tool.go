// Package generateimage implements the `generate_image` Tool: one synchronous image
// generation request to the deterministic Mock Provider's POST /v1/assets, derived from a
// photo of the Run's photo set and a settings object, followed by saving the generated
// image as an immutable Execution Artifact.
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
	"net/url"
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

// ArtifactContentStore is the port through which the Tool saves the generated image as
// content-addressed Execution Artifact content. Write streams r into immutable storage and
// returns the reference derived from the bytes that landed; it never writes metadata, so
// content whose result never commits remains an orphan for the offline sweeper.
type ArtifactContentStore interface {
	Write(ctx context.Context, r io.Reader) (domain.ArtifactRef, error)
}

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
    "settingsDigest": {"type": "string", "minLength": 1},
    "artifact": {
      "type": "object",
      "properties": {
        "artifactId": {"type": "string", "pattern": "^artifact_[0-9a-f]{32}$"},
        "mediaType": {"enum": ["image/png", "image/jpeg", "image/webp"]},
        "sizeBytes": {"type": "integer", "minimum": 1},
        "sha256": {"type": "string", "pattern": "^[0-9a-f]{64}$"}
      },
      "required": ["artifactId", "mediaType", "sizeBytes", "sha256"],
      "additionalProperties": false
    }
  },
  "required": ["assetRef", "imageUrl", "settingsDigest", "artifact"],
  "additionalProperties": false
}`

// Registration returns the `generate_image` ToolRegistration calling the Mock Provider at
// baseURL and saving generated images through artifacts. A nil client means a default
// client bounded by defaultTimeout.
//
// After the Provider answers, the Tool fetches the generated image with the same call
// context and writes it through artifacts, so the result carries an `artifact` reference
// and declares it in ToolResult.Artifacts for the Runtime to commit with the result. The
// image is fetched from baseURL, not from the returned imageUrl's own origin: only the
// scheme and host of imageUrl are replaced by those of baseURL, and the path is kept. The
// imageUrl may name a public origin the Backend cannot reach (a browser-facing address in
// a container deployment), and fetching only from the configured Provider means a
// Provider response can never point the Backend at an arbitrary host. The result keeps
// the public imageUrl unchanged.
//
// Generation creates a Provider asset, so the call has an EXTERNAL side effect. Its
// idempotency is UNKNOWN: the Runtime hands an Agent Tool no idempotency key, so nothing
// here may claim KEYED. Each call counts toward the Agent's generation limit.
//
// A successful result establishes an `image_generated` fact about the returned assetRef,
// bound to the source photo and to the digest of the settings that produced it, so a later
// review or video call can prove which photo and settings the asset came from. The fact
// also binds the Provider's imageUrl, a credential-free address of the generated asset, so
// a delivery node can present the generated image from the fact rather than from a URL the
// model reports.
func Registration(baseURL string, client *http.Client, artifacts ArtifactContentStore) registry.ToolRegistration {
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
					"imageUrl":       {Source: domain.FactPointerResult, Pointer: "/imageUrl"},
					"photoAssetId":   {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
					"settingsDigest": {Source: domain.FactPointerResult, Pointer: "/settingsDigest"},
				},
			},
			Requires:                    []domain.FactRequirement{},
			CountsTowardGenerationLimit: true,
		},
		Executor: New(baseURL, client, artifacts),
	}
}

// Executor implements registry.ToolExecutor for `generate_image`.
type Executor struct {
	baseURL    string
	httpClient *http.Client
	artifacts  ArtifactContentStore
}

// New returns an Executor calling the Mock Provider at baseURL and saving generated
// images through artifacts.
func New(baseURL string, client *http.Client, artifacts ArtifactContentStore) *Executor {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Executor{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client, artifacts: artifacts}
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
	AssetRef       string             `json:"assetRef"`
	ImageURL       string             `json:"imageUrl"`
	SettingsDigest string             `json:"settingsDigest"`
	Artifact       domain.ArtifactRef `json:"artifact"`
}

// Execute sends one generation request, saves the generated image as an Execution
// Artifact and returns the new asset's reference together with the artifact's.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if e.artifacts == nil {
		return registry.ToolExecutionResult{}, generationError("save image", 0, "artifact storage is not configured")
	}
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

	artifact, err := e.saveImage(ctx, generated.ImageURL)
	if err != nil {
		return registry.ToolExecutionResult{}, err
	}

	output, err := json.Marshal(result{AssetRef: generated.AssetID, ImageURL: generated.ImageURL, SettingsDigest: digest, Artifact: artifact})
	if err != nil {
		return registry.ToolExecutionResult{}, generationError("encode result", 0, "result could not be encoded")
	}
	return registry.ToolExecutionResult{
		Kind:   registry.ToolResultCompleted,
		Result: &registry.ToolResult{Output: output, Artifacts: []domain.ArtifactRef{artifact}},
	}, nil
}

// saveImage fetches the generated image from the configured Provider and writes it
// through the artifact port. It runs once: a failed fetch or write is reported as an
// error, never retried, because the generation itself already happened and its side
// effect is not idempotent. Errors carry fixed labels only - never the image URL, a
// storage key or a response body.
func (e *Executor) saveImage(ctx context.Context, imageURL string) (domain.ArtifactRef, error) {
	fetchURL, err := e.fetchURL(imageURL)
	if err != nil {
		return domain.ArtifactRef{}, generationError("fetch image", 0, "imageUrl is not a usable image address")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return domain.ArtifactRef{}, generationError("fetch image", 0, "request could not be created")
	}
	response, err := e.httpClient.Do(request)
	if err != nil {
		return domain.ArtifactRef{}, generationError("fetch image", 0, "transport failure")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return domain.ArtifactRef{}, generationError("fetch image", response.StatusCode, "non-200 status")
	}
	ref, err := e.artifacts.Write(ctx, response.Body)
	if err != nil {
		reason := "image could not be stored"
		switch {
		case errors.Is(err, domain.ErrArtifactTooLarge):
			reason = "image exceeds the artifact size limit"
		case errors.Is(err, domain.ErrUnsupportedAssetMediaType):
			reason = "image is not a supported image type"
		}
		return domain.ArtifactRef{}, generationError("save image", 0, reason)
	}
	return ref, nil
}

// fetchURL replaces the scheme and host of imageURL with those of the configured Provider
// base URL, keeping the path and query.
func (e *Executor) fetchURL(imageURL string) (string, error) {
	base, err := url.Parse(e.baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return "", errors.New("provider base URL is not absolute")
	}
	image, err := url.Parse(imageURL)
	if err != nil || image.Host == "" || (image.Scheme != "http" && image.Scheme != "https") || image.Path == "" {
		return "", errors.New("imageUrl is not an absolute http(s) URL")
	}
	fetch := url.URL{Scheme: base.Scheme, Host: base.Host, Path: image.Path, RawPath: image.RawPath, RawQuery: image.RawQuery}
	return fetch.String(), nil
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
