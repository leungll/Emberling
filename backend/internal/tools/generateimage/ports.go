package generateimage

import (
	"context"
	"io"
)

// ImageGenerator is the port through which the Tool asks one image model for one image.
// The Tool selects the generator by the Agent Run's frozen image Model ID; a generator
// performs a single Provider interaction and never retries, falls back or picks another
// model. Its errors carry fixed labels only, never the prompt, the photo or a response
// body.
type ImageGenerator interface {
	Generate(ctx context.Context, request ImageRequest) (GeneratedImage, error)
}

// ImageRequest is one generation: the source photo and the settings every image model
// shares.
type ImageRequest struct {
	PhotoAssetID string
	// OpenPhoto opens the source photo's content. A generator that sends the photo's bytes
	// calls it once and closes the content; one that addresses the photo by its id alone
	// does not call it.
	OpenPhoto func(ctx context.Context) (Photo, error)
	Settings  Settings
	// SettingsDigest is the digest the result and its fact bind; see SettingsDigest.
	SettingsDigest string
}

// Settings is the strict settings object shared by every image model. The Runtime has
// already validated it against the Tool's InputSchema when the Action was claimed.
type Settings struct {
	Prompt string `json:"prompt"`
	// AspectRatio and Resolution are empty when the call leaves them to the model.
	AspectRatio string `json:"aspectRatio,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	Seed        *int64 `json:"seed,omitempty"`
}

// GeneratedImage is one generated image. The Tool reads Content exactly once into
// Execution Artifact storage and closes it. The bytes are all a generator returns: the
// result names the image by the artifact saved from them, never by a Provider's own
// identifier or address, so every image model produces the same result shape.
type GeneratedImage struct {
	Content io.ReadCloser
}

// PhotoReader is the port through which the Tool reads a source photo by its Asset id.
type PhotoReader interface {
	OpenPhoto(ctx context.Context, photoAssetID string) (Photo, error)
}

// Photo is one source photo's content and media type. The reader of Content closes it.
type Photo struct {
	MediaType string
	Content   io.ReadCloser
}
