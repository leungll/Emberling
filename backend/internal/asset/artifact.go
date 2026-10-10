package asset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// artifactsDirName is the subtree of the storage root that holds Execution Artifacts. An
// Asset shard directory is always exactly two characters, so it can never collide with
// this name.
const artifactsDirName = "artifacts"

// ArtifactStore is content-addressed storage for immutable binaries an Execution
// produced, such as generated images. It shares the Asset write order — stage while
// hashing, fsync, then link into an immutable key — but the key is derived from the
// content digest, so identical bytes always land on one object and writing them again is
// a success rather than a conflict.
//
// Like Store, it never opens a database transaction and never decides whether an object
// is referenced. Content whose metadata never commits stays on disk as an orphan until
// the offline sweeper confirms no row references it.
type ArtifactStore struct {
	root     string
	maxBytes int64
}

// ArtifactObject is one committed object found by enumeration. StorageKey is a system
// Secret: it is returned so the sweeper can remove the object and must not be printed.
type ArtifactObject struct {
	ArtifactID string
	SHA256     string
	StorageKey string
	ModTime    time.Time
}

// StagedLeftover is one staging file that never became an object, typically left behind
// by a crash mid-write.
type StagedLeftover struct {
	Name    string
	ModTime time.Time
}

// NewArtifactStore returns an ArtifactStore under the "artifacts" subtree of storageRoot,
// rejecting content above maxBytes. It touches no filesystem.
func NewArtifactStore(storageRoot string, maxBytes int64) *ArtifactStore {
	return &ArtifactStore{root: storageRoot, maxBytes: maxBytes}
}

// ArtifactStorageKey derives the storage key of the object holding content with the given
// lowercase hex SHA-256 digest: "artifacts/<first two hex>/<sha256>". It is relative to
// the storage root and is what an execution_artifacts row persists.
func ArtifactStorageKey(sha256 string) (string, error) {
	if _, err := domain.ArtifactIDForSHA256(sha256); err != nil {
		return "", fmt.Errorf("asset: %w", err)
	}
	return artifactsDirName + "/" + sha256[:2] + "/" + sha256, nil
}

// VerifyReadWrite proves the artifact subtree supports the write, read and delete this
// store performs. It is folded into the Asset storage readiness step.
func (s *ArtifactStore) VerifyReadWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.maxBytes <= 0 {
		return errors.New("asset: maximum artifact size must be positive")
	}
	staging, err := s.stagingDir()
	if err != nil {
		return err
	}
	return probeReadWrite(staging)
}

// Write streams r into content-addressed storage and returns its reference. Size is
// capped while streaming; the media type is sniffed from the leading bytes and only
// PNG, JPEG and WebP are accepted. An object already present under the derived key is a
// success: it holds the same bytes, and it is never overwritten. Errors never carry a
// path or storage key.
func (s *ArtifactStore) Write(ctx context.Context, r io.Reader) (domain.ArtifactRef, error) {
	if err := ctx.Err(); err != nil {
		return domain.ArtifactRef{}, err
	}
	if s.maxBytes <= 0 {
		return domain.ArtifactRef{}, errors.New("asset: maximum artifact size must be positive")
	}
	staging, err := s.stagingDir()
	if err != nil {
		return domain.ArtifactRef{}, err
	}

	content, err := stageContent(staging, "artifact-*", r, s.maxBytes)
	if err != nil {
		if errors.Is(err, errContentTooLarge) {
			return domain.ArtifactRef{}, fmt.Errorf("asset: artifact: %w", domain.ErrArtifactTooLarge)
		}
		return domain.ArtifactRef{}, fmt.Errorf("asset: artifact: %w", err)
	}
	defer content.discard()

	mediaType := sniffImageMediaType(content.head)
	if mediaType == "" {
		return domain.ArtifactRef{}, fmt.Errorf("asset: artifact: content is not a supported image: %w", domain.ErrUnsupportedAssetMediaType)
	}
	ref := domain.ArtifactRef{MediaType: mediaType, SizeBytes: content.sizeBytes, SHA256: content.sha256}
	if ref.ArtifactID, err = domain.ArtifactIDForSHA256(content.sha256); err != nil {
		return domain.ArtifactRef{}, fmt.Errorf("asset: artifact: %w", err)
	}
	if err := ref.Validate(); err != nil {
		return domain.ArtifactRef{}, fmt.Errorf("asset: artifact: %w", err)
	}
	key, err := ArtifactStorageKey(content.sha256)
	if err != nil {
		return domain.ArtifactRef{}, err
	}
	if err := linkImmutable(content, s.path(key)); err != nil && !errors.Is(err, os.ErrExist) {
		return domain.ArtifactRef{}, fmt.Errorf("asset: artifact %s: %w", ref.ArtifactID, err)
	}
	return ref, nil
}

// Remove deletes the object under storageKey. It is the sweeper's path for an object no
// metadata references, so an already absent object is not an error.
func (s *ArtifactStore) Remove(storageKey string) error {
	if err := validateArtifactStorageKey(storageKey); err != nil {
		return err
	}
	if err := os.Remove(s.path(storageKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("asset: remove artifact: %w", redactPath(err))
	}
	return nil
}

// RemoveStaged deletes one staging leftover by the name enumeration returned. An already
// absent file is not an error.
func (s *ArtifactStore) RemoveStaged(name string) error {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return errors.New("asset: malformed staging name")
	}
	staging := filepath.Join(s.root, artifactsDirName, stagingDirName)
	if err := os.Remove(filepath.Join(staging, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("asset: remove staged artifact: %w", redactPath(err))
	}
	return nil
}

// Objects enumerates every committed object with its modification time. Entries that are
// not objects this store could have written are skipped rather than reported, so a
// stray file can never be mistaken for an artifact. A missing subtree is empty.
func (s *ArtifactStore) Objects(ctx context.Context) ([]ArtifactObject, error) {
	base := filepath.Join(s.root, artifactsDirName)
	shards, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("asset: list artifacts: %w", redactPath(err))
	}
	var objects []ArtifactObject
	for _, shard := range shards {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !shard.IsDir() || shard.Name() == stagingDirName {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(base, shard.Name()))
		if err != nil {
			return nil, fmt.Errorf("asset: list artifacts: %w", redactPath(err))
		}
		for _, entry := range entries {
			key := artifactsDirName + "/" + shard.Name() + "/" + entry.Name()
			if !entry.Type().IsRegular() || validateArtifactStorageKey(key) != nil {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("asset: stat artifact: %w", redactPath(err))
			}
			id, _ := domain.ArtifactIDForSHA256(entry.Name())
			objects = append(objects, ArtifactObject{ArtifactID: id, SHA256: entry.Name(), StorageKey: key, ModTime: info.ModTime()})
		}
	}
	return objects, nil
}

// StagedLeftovers enumerates files left in the artifact staging directory.
func (s *ArtifactStore) StagedLeftovers(ctx context.Context) ([]StagedLeftover, error) {
	staging := filepath.Join(s.root, artifactsDirName, stagingDirName)
	entries, err := os.ReadDir(staging)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("asset: list staged artifacts: %w", redactPath(err))
	}
	var leftovers []StagedLeftover
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("asset: stat staged artifact: %w", redactPath(err))
		}
		leftovers = append(leftovers, StagedLeftover{Name: entry.Name(), ModTime: info.ModTime()})
	}
	return leftovers, nil
}

// stagingDir returns the artifact staging directory, creating it if needed. It lives
// inside the artifact subtree so a staged file and its object share a filesystem, which
// os.Link requires.
func (s *ArtifactStore) stagingDir() (string, error) {
	if s.root == "" {
		return "", errors.New("asset: storage root is not configured")
	}
	staging := filepath.Join(s.root, artifactsDirName, stagingDirName)
	if err := os.MkdirAll(staging, dirPerm); err != nil {
		return "", fmt.Errorf("asset: prepare artifact storage: %w", redactPath(err))
	}
	return staging, nil
}

func (s *ArtifactStore) path(storageKey string) string {
	return filepath.Join(s.root, filepath.FromSlash(storageKey))
}

// validateArtifactStorageKey accepts only keys ArtifactStorageKey produces, so a key read
// back from the database or the filesystem can never resolve outside the subtree.
func validateArtifactStorageKey(storageKey string) error {
	parts := strings.Split(storageKey, "/")
	if len(parts) != 3 || parts[0] != artifactsDirName {
		return errors.New("asset: malformed artifact storage key")
	}
	if want, err := ArtifactStorageKey(parts[2]); err != nil || want != storageKey {
		return errors.New("asset: malformed artifact storage key")
	}
	return nil
}

// sniffImageMediaType detects the supported image formats from their leading signature.
// It returns "" for anything else: the declared Content-Type of a remote response is not
// trusted.
func sniffImageMediaType(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}
