package main

import (
	"context"
	"io"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// materialReader bridges a material-reading Tool's port to the Run-scoped artifact read,
// so a Tool only sees text an artifact its own Run produced holds, verified against the
// committed metadata. One bridge serves every Tool that declares the port, since the
// port's signature is the artifact read's own.
type materialReader struct {
	artifacts runArtifactOpener
}

func (r materialReader) OpenText(ctx context.Context, runID, artifactID string) (domain.Artifact, io.ReadCloser, error) {
	return r.artifacts.OpenRunContent(ctx, runID, artifactID)
}
