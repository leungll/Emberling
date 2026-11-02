package reviewasset

import (
	"context"
	"io"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// Ports are what a review with a real model reads through. The mock review policy uses
// none of them, so a process that serves only the mock policy may leave them empty; a
// review with a model then fails before any model call.
type Ports struct {
	Photos    PhotoReader
	Artifacts ArtifactReader
	Models    ModelResolver
	// MaxImageBytes bounds each image the review sends inline. An image over it fails the
	// call before the model is asked; images are never resized.
	MaxImageBytes int64
}

// DefaultMaxImageBytes is the inline image bound a deployment uses unless it has a reason
// to choose another: vision models commonly refuse a larger inline image.
const DefaultMaxImageBytes = 5 << 20

// PhotoReader is the port through which the Tool reads the original photo by its Asset
// id.
type PhotoReader interface {
	OpenPhoto(ctx context.Context, photoAssetID string) (Image, error)
}

// ArtifactReader is the port through which the Tool reads the candidate image: an
// Execution Artifact a committed result of the same Run declared.
type ArtifactReader interface {
	OpenArtifact(ctx context.Context, runID, artifactID string) (Image, error)
}

// ModelResolver is the port through which the Tool finds the review model of a call's
// frozen review Model ID. *registry.ModelRegistry satisfies it.
type ModelResolver interface {
	Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool)
}

// Image is one image's media type and content. The reader of Content closes it.
type Image struct {
	MediaType string
	Content   io.ReadCloser
}
