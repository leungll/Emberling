package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ArtifactContentReader is the read side of Execution Artifact content storage.
// internal/asset implements it over the content-addressed artifact subtree.
type ArtifactContentReader interface {
	// Open returns the object stored under a key. The reader can seek, so content can be
	// verified in full and then served from the start.
	Open(ctx context.Context, storageKey string) (io.ReadSeekCloser, error)
}

// errArtifactContentUnavailable reports artifact content that is missing or does not match
// its committed metadata. It deliberately wraps neither domain.ErrNotFound nor a storage
// error: the artifact exists for the Run, so the failure is the Backend's, and nothing of
// the content or its storage location may reach the caller.
var errArtifactContentUnavailable = errors.New("artifact content is missing or does not match its metadata")

// ArtifactService owns the Run-scoped read of Execution Artifact content. The read is a
// projection: it writes no row, changes no execution state and appends no Event, and it is
// never part of execution or recovery.
type ArtifactService struct {
	deps Deps
}

// NewArtifactService builds an ArtifactService from the shared Deps skeleton.
func NewArtifactService(deps Deps) *ArtifactService {
	return &ArtifactService{deps: deps.withDefaults()}
}

// OpenRunContent returns the metadata and content of an Execution Artifact that a
// committed result of runID declared. The caller closes the reader.
//
// Only a link written by a SUCCEEDED Tool Attempt of this Run makes an artifact readable
// here; content addressing lets other Runs share the same artifact, but each reads it only
// through its own link. A Run that does not exist, an artifact the Run never declared and
// an artifact without metadata are one domain.ErrNotFound, so a Run reveals nothing about
// another Run's artifacts.
//
// The content is read once in full and checked against the committed size and sha256
// before the reader is handed back at its start, so a caller never streams content that
// differs from what the result declared. Missing or mismatching content is an error that
// names the artifact id only, never a storage key or path.
func (s *ArtifactService) OpenRunContent(ctx context.Context, runID, artifactID string) (domain.Artifact, io.ReadCloser, error) {
	var record store.ArtifactRecord
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Artifacts().GetForRun(ctx, runID, artifactID)
		return err
	})
	if err != nil {
		return domain.Artifact{}, nil, fmt.Errorf("service artifacts.OpenRunContent: run=%s artifact=%s: %w", runID, artifactID, err)
	}
	if s.deps.ArtifactContent == nil {
		return domain.Artifact{}, nil, fmt.Errorf("service artifacts.OpenRunContent: artifact=%s: artifact content storage is not configured", artifactID)
	}
	if !domain.IsSupportedAssetMediaType(record.Artifact.MediaType) {
		return domain.Artifact{}, nil, fmt.Errorf("service artifacts.OpenRunContent: artifact=%s: %w", artifactID, errArtifactContentUnavailable)
	}

	content, err := s.deps.ArtifactContent.Open(ctx, record.StorageKey)
	if err != nil {
		// The storage error is not wrapped: a missing object must not read as a 404, and
		// the artifact id already identifies the failure.
		return domain.Artifact{}, nil, fmt.Errorf("service artifacts.OpenRunContent: artifact=%s: %w", artifactID, errArtifactContentUnavailable)
	}
	if err := verifyArtifactContent(content, record.Artifact); err != nil {
		_ = content.Close()
		return domain.Artifact{}, nil, fmt.Errorf("service artifacts.OpenRunContent: artifact=%s: %w", artifactID, err)
	}
	return record.Artifact, content, nil
}

// verifyArtifactContent reads content once, bounded by one byte past the committed size,
// compares its size and sha256 with the metadata, and rewinds it to the start.
func verifyArtifactContent(content io.ReadSeeker, artifact domain.Artifact) error {
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(content, artifact.SizeBytes+1))
	if err != nil || n != artifact.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return errArtifactContentUnavailable
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return errArtifactContentUnavailable
	}
	return nil
}
