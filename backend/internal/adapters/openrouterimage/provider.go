// Package openrouterimage turns one image generation request into exactly one call to
// the OpenRouter Images API, POST <base URL>/images, and returns the single generated
// image as a stream of decoded bytes.
//
// One Provider serves one configured image model. It registers with the reference image
// generation capability, meaning it can generate an image from one reference image and a
// prompt; it supplies no Decision, so
// Generate always refuses. The Adapter never retries, never falls back to another model,
// never sends OpenRouter routing fields, never writes Runtime state and never emits an
// Event: any answer it cannot normalise is returned as an *Error and the caller applies
// its own Tool failure handling. The API key travels only in the Authorization header; it
// is never part of the request body, a returned error or this package's string
// representation, and this package logs nothing.
package openrouterimage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const imagesPath = "/images"

const redacted = "[REDACTED]"

// configSchema accepts only an empty object: an image model takes no Definition-level
// parameters, its per-call settings arrive with each request.
const configSchema = `{"type": "object", "additionalProperties": false}`

// Config is everything one Provider needs. ModelID is the stable ID a Definition selects,
// DisplayName is what Studio shows and Description is the optional one-line note Studio
// shows with it, while Model is the upstream model name, which
// stays deployment configuration and is never written into a Definition. MaxImageBytes
// caps the decoded image and, through it, how much of the response body is read.
// HTTPClient carries the Adapter's own connection and per-call limits; when nil, calls
// are bounded only by the caller's context.
type Config struct {
	ModelID       string
	DisplayName   string
	Description   string
	BaseURL       string
	APIKey        string
	Model         string
	HTTPClient    *http.Client
	MaxImageBytes int64
}

// Provider is the OpenRouter Images API Adapter for one image model.
type Provider struct {
	modelID       string
	displayName   string
	description   string
	endpoint      string
	apiKey        string
	model         string
	client        *http.Client
	maxImageBytes int64
	maxBodyBytes  int64
}

var (
	_ registry.ModelProvider = (*Provider)(nil)
	_ fmt.Stringer           = Provider{}
	_ fmt.GoStringer         = Provider{}
	_ slog.LogValuer         = Provider{}
)

// New validates cfg. Errors name the missing settings, never their values.
func New(cfg Config) (*Provider, error) {
	var missing []string
	if strings.TrimSpace(cfg.ModelID) == "" {
		missing = append(missing, "model ID")
	}
	if strings.TrimSpace(cfg.DisplayName) == "" {
		missing = append(missing, "display name")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		missing = append(missing, "base URL")
	}
	if cfg.APIKey == "" {
		missing = append(missing, "API key")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		missing = append(missing, "model name")
	}
	if cfg.MaxImageBytes <= 0 {
		missing = append(missing, "positive maximum image size")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("openrouterimage: missing %s", strings.Join(missing, ", "))
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &Provider{
		modelID:       cfg.ModelID,
		displayName:   cfg.DisplayName,
		description:   cfg.Description,
		endpoint:      strings.TrimRight(cfg.BaseURL, "/") + imagesPath,
		apiKey:        cfg.APIKey,
		model:         cfg.Model,
		client:        client,
		maxImageBytes: cfg.MaxImageBytes,
		maxBodyBytes:  maxBodyBytes(cfg.MaxImageBytes),
	}, nil
}

// maxBodyBytes is the base64 length of the largest accepted image plus a fixed allowance
// for the JSON envelope, usage and other small members.
func maxBodyBytes(maxImageBytes int64) int64 {
	return (maxImageBytes+2)/3*4 + 64<<10
}

// String, GoString and LogValue keep the API key out of every rendering of a Provider,
// whether it is formatted by value or by pointer.
func (p Provider) String() string {
	return fmt.Sprintf("openrouterimage.Provider{modelID: %s, endpoint: %s, model: %s, apiKey: %s}", p.modelID, p.endpoint, p.model, redacted)
}

func (p Provider) GoString() string { return p.String() }

func (p Provider) LogValue() slog.Value { return slog.StringValue(p.String()) }

// Models returns the single image model this Provider serves, under its configured ID.
func (p *Provider) Models(context.Context) ([]registry.ModelRegistration, error) {
	return []registry.ModelRegistration{{ModelMetadata: domain.ModelMetadata{
		ID:           p.modelID,
		DisplayName:  p.displayName,
		Description:  p.description,
		Capabilities: []string{domain.ModelCapabilityReferenceImageGeneration},
		ConfigSchema: json.RawMessage(configSchema),
	}}}, nil
}

// Generate refuses every decision request without contacting the Provider: an image
// model produces images, not Agent Decisions.
func (p *Provider) Generate(context.Context, registry.ModelRequest) (registry.ModelResponse, error) {
	return registry.ModelResponse{}, &Error{message: "image model " + p.modelID + " does not answer decision requests"}
}

// Error is a request that could not be sent, or a Provider answer that could not be
// normalised. StatusCode is the HTTP status when the Provider answered, and 0 otherwise.
// The message is a fixed label: it never carries the key, the prompt, the reference image
// or any part of the Provider's response body. It wraps the caller's context error only
// when that context ended the call, so the caller can tell its own deadline from the
// Adapter's own timeout or a transport failure.
type Error struct {
	StatusCode int
	message    string
	cause      error
}

func (e *Error) Error() string { return "openrouterimage: " + e.message }

func (e *Error) Unwrap() error { return e.cause }
