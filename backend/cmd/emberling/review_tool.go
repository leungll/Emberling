package main

import (
	"context"
	"io"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

// reviewPhotoReader bridges review_asset's photo port to the Asset use case, so a review
// reads the original photo through the same verified path as an Asset download.
type reviewPhotoReader struct {
	assets assetContentOpener
}

func (r reviewPhotoReader) OpenPhoto(ctx context.Context, photoAssetID string) (reviewasset.Image, error) {
	asset, content, err := r.assets.OpenContent(ctx, photoAssetID)
	if err != nil {
		return reviewasset.Image{}, err
	}
	return reviewasset.Image{MediaType: asset.MediaType, Content: content}, nil
}

// runArtifactOpener is the Artifact use case that opens an Execution Artifact a committed
// result of the Run declared, verified against its committed metadata.
type runArtifactOpener interface {
	OpenRunContent(ctx context.Context, runID, artifactID string) (domain.Artifact, io.ReadCloser, error)
}

// reviewArtifactReader bridges review_asset's artifact port to the Run-scoped artifact
// read, so a review only sees a candidate its own Run produced.
type reviewArtifactReader struct {
	artifacts runArtifactOpener
}

func (r reviewArtifactReader) OpenArtifact(ctx context.Context, runID, artifactID string) (reviewasset.Image, error) {
	artifact, content, err := r.artifacts.OpenRunContent(ctx, runID, artifactID)
	if err != nil {
		return reviewasset.Image{}, err
	}
	return reviewasset.Image{MediaType: artifact.MediaType, Content: content}, nil
}
