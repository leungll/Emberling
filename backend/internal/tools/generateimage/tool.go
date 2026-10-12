// Package generateimage implements the `generate_image` Tool: one synchronous image
// generation with the Agent node's selected image model, from a photo of the Run's photo
// set and the strict settings object every image model shares, followed by saving the
// generated image as an immutable Execution Artifact.
//
// It performs exactly one registered operation. It owns no retry, timeout, state
// transition or Event: a rejected or failed generation is reported upward as an error and
// the Agent Runtime decides what happens next.
//
// Its errors carry only the Tool name, a stable model or Provider identifier, a fixed
// operation label and an HTTP status code - never the arguments, the settings or a
// response body.
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
	"maps"
	"net/url"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "generate_image"

// FactType is the execution fact a successful generation establishes about the new asset.
const FactType = "image_generated"

// ArtifactContentStore is the port through which the Tool saves the generated image as
// content-addressed Execution Artifact content. Write streams r into immutable storage and
// returns the reference derived from the bytes that landed; it never writes metadata, so
// content whose result never commits remains an orphan for the offline sweeper.
type ArtifactContentStore interface {
	Write(ctx context.Context, r io.Reader) (domain.ArtifactRef, error)
}

// inputSchema is shared by every image model. The Runtime validates a Decision's
// arguments against it when the Action is claimed, so settings outside it fail as an
// invalid Action before any Tool Attempt or external request exists.
const inputSchema = `{
  "type": "object",
  "properties": {
    "photoAssetId": {"type": "string", "minLength": 1},
    "settings": {
      "type": "object",
      "properties": {
        "prompt": {"type": "string", "minLength": 1, "maxLength": 4000},
        "aspectRatio": {"enum": ["1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9"]},
        "resolution": {"enum": ["1K"]},
        "seed": {"type": "integer", "minimum": 0, "maximum": 2147483647}
      },
      "required": ["prompt"],
      "additionalProperties": false
    }
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

// Registration returns the `generate_image` ToolRegistration. generators maps each image
// Model ID this process can serve to its generator; photos reads source photos for a
// generator that sends their bytes; artifacts saves generated images.
//
// The Tool declares UsesImageModel, so every call carries the Agent Run's frozen image
// Model ID and the Tool uses exactly that model's generator. An ID with no generator is a
// wiring defect: the call fails before any request, and no other generator is
// substituted.
//
// The generated image is written through artifacts with the call context, so the result
// carries an `artifact` reference and declares it in ToolResult.Artifacts for the Runtime
// to commit with the result.
//
// The result has the same shape whichever image model generated it: assetRef is the
// artifact id, and imageUrl is the site-relative path of the Run-scoped artifact content
// route. The path names no origin, so it stays valid wherever the Backend is deployed and
// can be bound into an immutable fact.
//
// Generation runs on an external image model, so the call has an EXTERNAL side effect. Its
// idempotency is UNKNOWN: the Runtime hands an Agent Tool no idempotency key, so nothing
// here may claim KEYED. Each call counts toward the Agent's generation limit.
//
// A successful result establishes an `image_generated` fact about the returned assetRef,
// bound to the source photo and to the digest of the settings that produced it, so a later
// review or video call can prove which photo and settings the asset came from. The fact
// also binds imageUrl and the full artifact reference, so a delivery node can present the
// generated image from the fact rather than from a URL the model reports.
func Registration(generators map[string]ImageGenerator, photos PhotoReader, artifacts ArtifactContentStore) registry.ToolRegistration {
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
					"artifact":       {Source: domain.FactPointerResult, Pointer: "/artifact"},
					"imageUrl":       {Source: domain.FactPointerResult, Pointer: "/imageUrl"},
					"photoAssetId":   {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
					"settingsDigest": {Source: domain.FactPointerResult, Pointer: "/settingsDigest"},
				},
			},
			Requires:                    []domain.FactRequirement{},
			CountsTowardGenerationLimit: true,
			UsesImageModel:              true,
		},
		Executor: New(generators, photos, artifacts),
	}
}

// Executor implements registry.ToolExecutor for `generate_image`.
type Executor struct {
	generators map[string]ImageGenerator
	photos     PhotoReader
	artifacts  ArtifactContentStore
}

// New returns an Executor that generates with the generator registered for each call's
// image Model ID and saves generated images through artifacts.
func New(generators map[string]ImageGenerator, photos PhotoReader, artifacts ArtifactContentStore) *Executor {
	return &Executor{generators: maps.Clone(generators), photos: photos, artifacts: artifacts}
}

type arguments struct {
	PhotoAssetID string          `json:"photoAssetId"`
	Settings     json.RawMessage `json:"settings"`
}

// result is the Tool's result; field order is the result's key order.
type result struct {
	AssetRef       string             `json:"assetRef"`
	ImageURL       string             `json:"imageUrl"`
	SettingsDigest string             `json:"settingsDigest"`
	Artifact       domain.ArtifactRef `json:"artifact"`
}

// Execute generates one image with the call's image model, saves it as an Execution
// Artifact and returns the new asset's reference together with the artifact's.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if e.artifacts == nil {
		return registry.ToolExecutionResult{}, toolError("save image", "artifact storage is not configured")
	}
	if action.RunID == "" {
		return registry.ToolExecutionResult{}, toolError("build request", "the call carries no Run id")
	}
	generator := e.generators[action.ImageModelID]
	if generator == nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: select generator: image model %q has no generator in this process", ToolName, action.ImageModelID)
	}
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.PhotoAssetID == "" {
		return registry.ToolExecutionResult{}, toolError("build request", "arguments carry no photoAssetId")
	}
	digest, _, err := SettingsDigest(args.Settings)
	if err != nil {
		return registry.ToolExecutionResult{}, toolError("build request", "settings is not a JSON object")
	}
	settings, err := decodeSettings(args.Settings)
	if err != nil {
		return registry.ToolExecutionResult{}, toolError("build request", "settings do not match the shared image settings")
	}

	photoAssetID := args.PhotoAssetID
	generated, err := generator.Generate(ctx, ImageRequest{
		PhotoAssetID:   photoAssetID,
		OpenPhoto:      func(ctx context.Context) (Photo, error) { return e.openPhoto(ctx, photoAssetID) },
		Settings:       settings,
		SettingsDigest: digest,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, err
	}
	if generated.Content == nil {
		return registry.ToolExecutionResult{}, toolError("save image", "generator returned no image content")
	}
	defer func() { _ = generated.Content.Close() }()

	artifact, err := e.artifacts.Write(ctx, generated.Content)
	if err != nil {
		reason := "image could not be stored"
		switch {
		case errors.Is(err, domain.ErrArtifactTooLarge):
			reason = "image exceeds the artifact size limit"
		case errors.Is(err, domain.ErrUnsupportedAssetMediaType):
			reason = "image is not a supported image type"
		}
		return registry.ToolExecutionResult{}, toolError("save image", reason)
	}

	output, err := json.Marshal(result{
		AssetRef:       artifact.ArtifactID,
		ImageURL:       ArtifactContentPath(action.RunID, artifact.ArtifactID),
		SettingsDigest: digest,
		Artifact:       artifact,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, toolError("encode result", "result could not be encoded")
	}
	return registry.ToolExecutionResult{
		Kind:   registry.ToolResultCompleted,
		Result: &registry.ToolResult{Output: output, Artifacts: []domain.ArtifactRef{artifact}},
	}, nil
}

// ArtifactContentPath is the site-relative path of the Run-scoped route that serves an
// artifact a committed result of runID declared. It carries no origin, so it neither
// depends on nor reveals where the Backend is deployed.
func ArtifactContentPath(runID, artifactID string) string {
	return "/api/runs/" + url.PathEscape(runID) + "/artifacts/" + url.PathEscape(artifactID) + "/content"
}

// openPhoto reads one source photo through the photo port on a generator's request.
func (e *Executor) openPhoto(ctx context.Context, photoAssetID string) (Photo, error) {
	if e.photos == nil {
		return Photo{}, toolError("read photo", "photo storage is not configured")
	}
	photo, err := e.photos.OpenPhoto(ctx, photoAssetID)
	if err != nil {
		return Photo{}, toolError("read photo", "photo could not be read")
	}
	return photo, nil
}

// decodeSettings decodes the shared settings object strictly. The claim already
// validated it against the InputSchema; a member this type does not know still fails
// here rather than being dropped silently.
func decodeSettings(raw json.RawMessage) (Settings, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var settings Settings
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	if settings.Prompt == "" {
		return Settings{}, errors.New("decode settings: prompt is empty")
	}
	return settings, nil
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

// generationError builds a Provider interaction failure from fixed labels only, so no
// request or response content can reach Trace or a log through it.
func generationError(op string, statusCode int, reason string) error {
	if statusCode != 0 {
		return fmt.Errorf("%s: call provider %q: %s (status %d): %w", ToolName, ProviderID, op, statusCode, errors.New(reason))
	}
	return fmt.Errorf("%s: call provider %q: %s: %w", ToolName, ProviderID, op, errors.New(reason))
}

// toolError builds a Tool failure outside any Provider interaction from fixed labels
// only.
func toolError(op string, reason string) error {
	return fmt.Errorf("%s: %s: %w", ToolName, op, errors.New(reason))
}
