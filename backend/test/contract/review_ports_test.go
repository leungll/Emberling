//go:build integration

package contract

import (
	"context"

	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

// reviewPhotos reads review_asset's original photo through the Asset service the harness
// wires after the Tools, as cmd/emberling bridges it.
type reviewPhotos struct {
	assets *assetPhotos
}

func (p reviewPhotos) OpenPhoto(ctx context.Context, photoAssetID string) (reviewasset.Image, error) {
	asset, content, err := p.assets.assets.OpenContent(ctx, photoAssetID)
	if err != nil {
		return reviewasset.Image{}, err
	}
	return reviewasset.Image{MediaType: asset.MediaType, Content: content}, nil
}

// lateArtifacts holds the Artifact service, which the harness builds after the Tools.
type lateArtifacts struct {
	svc *service.ArtifactService
}

// reviewArtifacts reads review_asset's candidate through the Run-scoped artifact read.
type reviewArtifacts struct {
	env *lateArtifacts
}

func (r reviewArtifacts) OpenArtifact(ctx context.Context, runID, artifactID string) (reviewasset.Image, error) {
	artifact, content, err := r.env.svc.OpenRunContent(ctx, runID, artifactID)
	if err != nil {
		return reviewasset.Image{}, err
	}
	return reviewasset.Image{MediaType: artifact.MediaType, Content: content}, nil
}

// reviewImageMaxBytes is the inline image bound the harness gives review_asset.
func reviewImageMaxBytes(override int64) int64 {
	if override > 0 {
		return override
	}
	return reviewasset.DefaultMaxImageBytes
}
