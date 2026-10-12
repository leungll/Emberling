package main

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// registerImageTool registers generate_image with one generator per image model this
// process serves. Only the mock image model is wired: its generator calls the Mock
// Provider at mockBaseURL through the bounded dispatch client, which suits a fixture that
// answers at once and must not be stretched to cover a slow real image model.
func registerImageTool(tools *registry.ToolRegistry, mockBaseURL string, dispatchClient *http.Client, photos generateimage.PhotoReader, artifacts generateimage.ArtifactContentStore) error {
	generators := map[string]generateimage.ImageGenerator{
		mockmodel.ImageModelID: generateimage.NewMockGenerator(mockBaseURL, dispatchClient),
	}
	if err := tools.Register(generateimage.Registration(generators, photos, artifacts)); err != nil {
		return fmt.Errorf("register tool: %w", err)
	}
	return nil
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
