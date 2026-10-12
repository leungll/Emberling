package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// imageConnectTimeout bounds establishing a connection to a real image model: the TCP dial
// and the TLS handshake. Once connected, waiting for the image is not bounded separately.
const imageConnectTimeout = 10 * time.Second

// imageToolWiring is what generate_image needs from this process. The mock image model
// calls the Mock Provider at MockBaseURL through DispatchClient, the short client that
// suits a fixture answering at once; every real image model in ImageModels calls its
// Provider through its own image client and never through DispatchClient. MaxPhotoBytes
// caps the source photo a real model sends.
type imageToolWiring struct {
	MockBaseURL    string
	DispatchClient *http.Client
	ImageModels    []imageModel
	Photos         generateimage.PhotoReader
	Artifacts      generateimage.ArtifactContentStore
	MaxPhotoBytes  int64
}

// registerImageTool registers generate_image with one generator per image model this
// process serves: the mock image model always, and each configured real image model under
// its own Model ID.
func registerImageTool(tools *registry.ToolRegistry, wiring imageToolWiring) error {
	generators := map[string]generateimage.ImageGenerator{
		mockmodel.ImageModelID: generateimage.NewMockGenerator(wiring.MockBaseURL, wiring.DispatchClient),
	}
	for _, model := range wiring.ImageModels {
		if _, taken := generators[model.id]; taken {
			return fmt.Errorf("register tool: image model %q has two generators", model.id)
		}
		generators[model.id] = referenceImageGenerator{modelID: model.id, provider: model.provider, maxPhotoBytes: wiring.MaxPhotoBytes}
	}
	if err := tools.Register(generateimage.Registration(generators, wiring.Photos, wiring.Artifacts)); err != nil {
		return fmt.Errorf("register tool: %w", err)
	}
	return nil
}

// newImageHTTPClient returns the client every real image model shares. Connecting is
// bounded by imageConnectTimeout; there is deliberately no response header timeout,
// because a real image model may take minutes before it answers. callTimeout bounds one
// whole call, and the caller's context - the Agent Run deadline - may end it sooner, so
// the effective limit is the smaller of the two.
//
// The per-call bound is the client's own timeout rather than a context derived from the
// caller's: the Adapter then reports it as its own timeout, a Tool failure, and never as
// the caller's deadline, which would end the Agent with TIMEOUT instead. Neither limit
// resends the request.
func newImageHTTPClient(callTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = imageDialer().DialContext
	transport.TLSHandshakeTimeout = imageConnectTimeout
	transport.ResponseHeaderTimeout = 0
	return &http.Client{Transport: transport, Timeout: callTimeout}
}

func imageDialer() *net.Dialer {
	return &net.Dialer{Timeout: imageConnectTimeout, KeepAlive: 30 * time.Second}
}

// referenceImageGenerator bridges generate_image's generator port to one real image
// model: it reads the source photo's bytes through the photo port, makes exactly one
// Images API call and hands the decoded image stream to the Tool, which saves it as an
// Execution Artifact. It never retries.
type referenceImageGenerator struct {
	modelID       string
	provider      *openrouterimage.Provider
	maxPhotoBytes int64
}

func (g referenceImageGenerator) Generate(ctx context.Context, request generateimage.ImageRequest) (generateimage.GeneratedImage, error) {
	photo, err := request.OpenPhoto(ctx)
	if err != nil {
		return generateimage.GeneratedImage{}, err
	}
	reference, err := readPhoto(photo, g.maxPhotoBytes)
	if err != nil {
		return generateimage.GeneratedImage{}, fmt.Errorf("%s: image model %q: %w", generateimage.ToolName, g.modelID, err)
	}
	image, err := g.provider.GenerateImage(ctx, openrouterimage.ImageRequest{
		Prompt:             request.Settings.Prompt,
		AspectRatio:        request.Settings.AspectRatio,
		Resolution:         request.Settings.Resolution,
		Seed:               request.Settings.Seed,
		ReferenceImage:     reference,
		ReferenceMediaType: photo.MediaType,
	})
	if err != nil {
		// The Adapter's error is a fixed label; it wraps a context error only when the
		// caller's context ended the call.
		return generateimage.GeneratedImage{}, fmt.Errorf("%s: image model %q: %w", generateimage.ToolName, g.modelID, err)
	}
	return generateimage.GeneratedImage{Content: io.NopCloser(image.Content), Model: image.Model, Cost: image.Cost}, nil
}

// readPhoto reads and closes the source photo, refusing one over maxBytes rather than
// sending a truncated image.
func readPhoto(photo generateimage.Photo, maxBytes int64) ([]byte, error) {
	defer func() { _ = photo.Content.Close() }()
	if maxBytes <= 0 {
		return nil, fmt.Errorf("read photo: maximum photo size is not configured")
	}
	content, err := io.ReadAll(io.LimitReader(photo.Content, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read photo: photo could not be read")
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("read photo: photo exceeds %d bytes", maxBytes)
	}
	return content, nil
}

// assetContentOpener is the Asset use case that opens an uploaded Asset's content.
type assetContentOpener interface {
	OpenContent(ctx context.Context, assetID string) (domain.Asset, io.ReadCloser, error)
}

// assetPhotoReader bridges generate_image's photo port to the Asset use case, so a
// generator reads a source photo through the same verified path as an Asset download.
type assetPhotoReader struct {
	assets assetContentOpener
}

func (r assetPhotoReader) OpenPhoto(ctx context.Context, photoAssetID string) (generateimage.Photo, error) {
	asset, content, err := r.assets.OpenContent(ctx, photoAssetID)
	if err != nil {
		return generateimage.Photo{}, err
	}
	return generateimage.Photo{MediaType: asset.MediaType, Content: content}, nil
}
