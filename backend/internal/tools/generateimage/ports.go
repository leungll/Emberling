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
// Execution Artifact storage and closes it.
type GeneratedImage struct {
	Content io.ReadCloser
	// AssetID and ImageURL are the Provider's own credential-free reference to the
	// generated image. The Tool's result names the image by them, so a generator that
	// returns none cannot complete a call.
	AssetID  string
	ImageURL string
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
