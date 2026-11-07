package reviewartifact

import (
	"context"
	"io"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// Ports are what a review reads through. Every port is required: a process that cannot
// serve one of them fails the call before anything is read or asked.
type Ports struct {
	Models    ModelResolver
	Materials MaterialReader
}

// ModelResolver is the port through which the Tool finds the review model of a call's
// frozen review Model ID. *registry.ModelRegistry satisfies it.
type ModelResolver interface {
	Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool)
}

// MaterialReader is the port through which the Tool opens a text Execution Artifact that
// a committed result of the same Run declared, verified against its committed metadata.
// Its signature is the Run-scoped artifact read's own, built from shared types only and
// identical to the one the line-reading Tool declares, so the assembly wires one bridge
// for every Tool that reads material. The Tool never queries storage or the database.
type MaterialReader interface {
	// OpenText returns the artifact's metadata and its content. The caller closes the
	// content. An artifact that does not exist or is not linked to runID fails with an
	// error wrapping domain.ErrNotFound; any other failure is the Backend's.
	OpenText(ctx context.Context, runID, artifactID string) (domain.Artifact, io.ReadCloser, error)
}
