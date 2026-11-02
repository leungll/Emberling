// Package taskgateway wraps a synchronous image model as an asynchronous task Provider
// that speaks exactly the task protocol the deterministic Mock Provider speaks: POST
// /v1/tasks accepts one task (202 for a new task, 200 with the original task id for a
// replayed idempotency key), GET /v1/tasks/{externalTaskId} answers its status, and the
// result is delivered later as a callback POST carrying {externalTaskId, payload} with the
// X-Emberling-Callback-Token header. The callback payload and the terminal status payload
// are the same bytes: {status, image, error, usage}.
//
// Accepted tasks run on a fixed pool of workers. A worker resolves the reference image
// (an ASSET reference through Emberling's Asset content route, an EXTERNAL one directly),
// makes exactly one call through the injected ImageGenerator, stages the generated image
// in bounded memory under GET /v1/images/{imageId} and reports an EXTERNAL image
// reference to it. The gateway never retries an upstream call; only the callback delivery
// is retried, with exponential backoff bounded by the task deadline.
//
// The gateway is a stateless demo service: every map, queue and staged image lives in
// memory, is bounded, and is lost on restart. Like the Mock Provider it stands in for an
// external Provider, so it shares no Go type with the Runtime and imports no Runtime
// package; the upstream image model reaches it only through the ImageGenerator port.
//
// Nothing here logs or returns a credential: the callback token travels only from the
// dispatch body to the callback header and is dropped once delivery ends, and log lines
// carry the external task id, the model id, the task status, the error code and HTTP
// status codes only - never a prompt, image bytes, a callback URL or an upstream body.
package taskgateway

import (
	"context"
	"encoding/json"
)

// ImageGenerator is the upstream port: one call generates one image from a prompt and
// one reference image. An implementation makes exactly one upstream request and never
// retries. The text of a returned error is reported as the task's failure message, so it
// must be a fixed label carrying no credential, prompt, image content or upstream body.
type ImageGenerator interface {
	Generate(ctx context.Context, req GenerateRequest) (GeneratedImage, error)
}

// GenerateRequest is one generation: the prompt and the resolved reference image.
type GenerateRequest struct {
	Prompt             string
	ReferenceImage     []byte
	ReferenceMediaType string
}

// GeneratedImage is the single image an upstream call produced. Cost is the charge the
// upstream reported, or nil when it reported none.
type GeneratedImage struct {
	MediaType string
	Content   []byte
	Cost      *float64
}

// Reference sources a dispatch may carry.
const (
	sourceAsset    = "ASSET"
	sourceExternal = "EXTERNAL"
)

// Task statuses, identical to the Mock Provider's.
const (
	statusRunning   = "RUNNING"
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// Failure codes a FAILED payload carries.
const (
	codeReferenceNotFound    = "REFERENCE_NOT_FOUND"
	codeReferenceFetchFailed = "REFERENCE_FETCH_FAILED"
	codeReferenceInvalid     = "REFERENCE_INVALID"
	codeProviderTaskFailed   = "PROVIDER_TASK_FAILED"
	codeTaskExpired          = "GATEWAY_TASK_EXPIRED"
)

// taskRequest is the part of a POST /v1/tasks body the gateway reads. The member names
// are the dispatching Adapter's; scenario members a Mock Provider understands (payload,
// outcome, delayMs, externalTaskId, media) are ignored, as are unknown members.
type taskRequest struct {
	CallbackURL    string          `json:"callbackUrl"`
	CallbackToken  string          `json:"callbackToken"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Prompt         string          `json:"prompt"`
	Reference      *referenceWire  `json:"reference"`
	Options        json.RawMessage `json:"options"`
	Deadline       string          `json:"deadline"`
}

// referenceWire is the reference image as the dispatch carries it: an ImageRef on either
// the ASSET or the EXTERNAL branch.
type referenceWire struct {
	Source    string     `json:"source"`
	Asset     *assetWire `json:"asset"`
	URI       string     `json:"uri"`
	MediaType string     `json:"mediaType"`
}

type assetWire struct {
	AssetID   string `json:"assetId"`
	MediaType string `json:"mediaType"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

// taskOptions is the part of the dispatch options the gateway reads. The requested width
// is deliberately not read: the upstream accepts no width, and the gateway does not
// invent an upstream parameter from it.
type taskOptions struct {
	ModelID string `json:"modelId"`
}

type taskResponse struct {
	ExternalTaskID string `json:"externalTaskId"`
}

// taskStatusResponse answers GET /v1/tasks/{externalTaskId}. Payload is present only
// once the task is terminal. It has no member for the callback token or URL.
type taskStatusResponse struct {
	ExternalTaskID string          `json:"externalTaskId"`
	Status         string          `json:"status"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

// callbackBody is the envelope POSTed to the callback URL.
type callbackBody struct {
	ExternalTaskID string          `json:"externalTaskId"`
	Payload        json.RawMessage `json:"payload"`
}

// resultPayload is the terminal payload. image carries source, uri and mediaType only:
// the receiver rejects unknown members, and the gateway does not guess dimensions.
// usage is omitted, never zero, when the upstream reported no cost.
type resultPayload struct {
	Status string     `json:"status"`
	Image  *imageWire `json:"image,omitempty"`
	Error  *errorWire `json:"error,omitempty"`
	Usage  *usageWire `json:"usage,omitempty"`
}

type imageWire struct {
	Source    string `json:"source"`
	URI       string `json:"uri"`
	MediaType string `json:"mediaType"`
}

type errorWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type usageWire struct {
	Cost float64 `json:"cost"`
}

type errorResponse struct {
	Error string `json:"error"`
}
