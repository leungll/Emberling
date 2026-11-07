package readmaterial

import (
	"context"
	"io"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// Ports are what the Tool reads material through. A process that never serves the Tool
// may leave them empty; a call then fails before anything is read.
type Ports struct {
	Materials MaterialReader
}

// MaterialReader is the port through which the Tool opens a text Execution Artifact that
// a committed result of the same Run declared, verified against its committed metadata.
// Its signature is the Run-scoped artifact read's own, built from shared types only, so
// another Tool that reads material declares the same interface and the assembly wires
// one bridge for both. The Tool never queries storage or the database itself.
type MaterialReader interface {
	// OpenText returns the artifact's metadata and its content. The caller closes the
	// content. An artifact that does not exist or is not linked to runID fails with an
	// error wrapping domain.ErrNotFound; any other failure is the Backend's.
	OpenText(ctx context.Context, runID, artifactID string) (domain.Artifact, io.ReadCloser, error)
}
