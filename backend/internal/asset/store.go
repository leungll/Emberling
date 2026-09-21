// Package asset stores Asset binary content on a local persistent volume. It owns the
// write order 10-ops §3 fixes — stream the content into a staging object while hashing
// and counting it, then turn that object into an immutable one — and nothing else: it
// never opens a database transaction, decides a Runtime step or learns what an Asset is
// referenced by.
//
// A storage key is an internal system Secret (10-ops §4). It is returned to the service
// layer so the Store can persist it, and it must not travel any further outward.
package asset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// stagingDirName holds content that has been received but is not yet an Asset. It starts
// with a dot so it can never collide with a shard directory, whose name is always two
// characters of an identifier.
const stagingDirName = ".staging"

// dirPerm and filePerm keep Asset content readable by the Backend's own user only: the
// volume holds business content that no other process on the host needs.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// maxAssetIDLength bounds an identifier before it becomes a path component.
const maxAssetIDLength = 128

// StoredContent is what one completed write produced: where the content now lives, and
// the two facts measured from the bytes that actually landed there. The service layer
// commits these into Asset Metadata; SizeBytes and SHA256 are never taken from a client
// claim.
type StoredContent struct {
	StorageKey string
	SizeBytes  int64
	SHA256     string
}

// Store is Asset binary storage rooted at one directory.
type Store struct {
	root     string
	maxBytes int64
}

// NewStore returns a Store over root, rejecting any content above maxBytes. It touches no
// filesystem: the root is created and proven writable by VerifyReadWrite, which is
// readiness step 4 (10-ops §1).
func NewStore(root string, maxBytes int64) *Store {
	return &Store{root: root, maxBytes: maxBytes}
}

// VerifyReadWrite is readiness step 4: the configured root must exist and support the
// write, read and delete this package performs. A Backend that cannot do this must not
// become ready, because an accepted upload would have nowhere to land.
func (s *Store) VerifyReadWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.root == "" {
		return errors.New("asset: storage root is not configured")
	}
	if s.maxBytes <= 0 {
		return errors.New("asset: maximum upload size must be positive")
	}
	staging, err := s.stagingDir()
	if err != nil {
		return err
	}

	probe, err := os.CreateTemp(staging, "readiness-*")
	if err != nil {
		return fmt.Errorf("asset: create readiness probe: %w", redactPath(err))
	}
	probePath := probe.Name()
	defer func() { _ = os.Remove(probePath) }()

	const marker = "emberling-readiness"
	if _, err := probe.WriteString(marker); err != nil {
		_ = probe.Close()
		return fmt.Errorf("asset: write readiness probe: %w", err)
	}
	if err := probe.Close(); err != nil {
		return fmt.Errorf("asset: close readiness probe: %w", err)
	}
	read, err := os.ReadFile(probePath)
	if err != nil {
		return fmt.Errorf("asset: read readiness probe: %w", redactPath(err))
	}
	if string(read) != marker {
		return errors.New("asset: readiness probe read back different content")
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("asset: remove readiness probe: %w", redactPath(err))
	}
	return nil
}

// Write streams r into storage under a key derived from assetID and reports the size and
// digest of the bytes that landed. It enforces the size cap while streaming, so oversized
// content is refused without ever being fully accepted.
//
// The content becomes reachable at its key in one step, after it is complete and fsynced:
// a crash mid-upload leaves a staging file, never a truncated Asset. An existing key is
// never overwritten — replacing content means creating a new Asset (10-ops §3) — so a
// repeated key yields domain.ErrConflict with the committed content untouched.
func (s *Store) Write(ctx context.Context, assetID string, r io.Reader) (StoredContent, error) {
	if err := ctx.Err(); err != nil {
		return StoredContent{}, err
	}
	if s.maxBytes <= 0 {
		return StoredContent{}, errors.New("asset: maximum upload size must be positive")
	}
	key, err := storageKey(assetID)
	if err != nil {
		return StoredContent{}, err
	}
	staging, err := s.stagingDir()
	if err != nil {
		return StoredContent{}, err
	}

	staged, err := os.CreateTemp(staging, "upload-*")
	if err != nil {
		return StoredContent{}, fmt.Errorf("asset: stage content: asset=%s: %w", assetID, redactPath(err))
	}
	stagedPath := staged.Name()
	// Removing the staging file covers every failure path below, and is a no-op once the
	// content has been linked into place and removed explicitly.
	defer func() { _ = os.Remove(stagedPath) }()

	digest := sha256.New()
	// maxBytes+1 is what makes the cap a streaming decision: reading one byte past the
	// limit proves the content is too large without reading the rest of it.
	written, copyErr := io.Copy(io.MultiWriter(staged, digest), io.LimitReader(r, s.maxBytes+1))
	if copyErr != nil {
		_ = staged.Close()
		return StoredContent{}, fmt.Errorf("asset: receive content: asset=%s: %w", assetID, redactPath(copyErr))
	}
	if written > s.maxBytes {
		_ = staged.Close()
		return StoredContent{}, fmt.Errorf("asset: receive content: asset=%s: %w", assetID, domain.ErrAssetTooLarge)
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return StoredContent{}, fmt.Errorf("asset: flush content: asset=%s: %w", assetID, redactPath(err))
	}
	if err := staged.Close(); err != nil {
		return StoredContent{}, fmt.Errorf("asset: close staged content: asset=%s: %w", assetID, redactPath(err))
	}

	destination := s.path(key)
	shardDir := filepath.Dir(destination)
	if err := os.MkdirAll(shardDir, dirPerm); err != nil {
		return StoredContent{}, fmt.Errorf("asset: create shard directory: asset=%s: %w", assetID, redactPath(err))
	}
	// os.Link, not os.Rename: rename silently replaces an existing destination, which
	// would break the immutability an AssetRef promises. Link fails with EEXIST instead,
	// and is equally atomic.
	if err := os.Link(stagedPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return StoredContent{}, fmt.Errorf("asset: content already exists: asset=%s: %w", assetID, domain.ErrConflict)
		}
		return StoredContent{}, fmt.Errorf("asset: commit content: asset=%s: %w", assetID, redactPath(err))
	}
	if err := syncDir(shardDir); err != nil {
		return StoredContent{}, fmt.Errorf("asset: flush shard directory: asset=%s: %w", assetID, redactPath(err))
	}
	if err := os.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return StoredContent{}, fmt.Errorf("asset: clear staged content: asset=%s: %w", assetID, redactPath(err))
	}

	return StoredContent{
		StorageKey: key,
		SizeBytes:  written,
		SHA256:     hex.EncodeToString(digest.Sum(nil)),
	}, nil
}

// Open returns the content stored under key for a controlled download. The caller closes
// the reader. An unknown key yields domain.ErrNotFound.
func (s *Store) Open(ctx context.Context, storageKey string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateStorageKey(storageKey); err != nil {
		return nil, err
	}
	file, err := os.Open(s.path(storageKey))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("asset: open content: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("asset: open content: %w", redactPath(err))
	}
	return file, nil
}

// Remove deletes the content stored under key. It is the orphan-cleanup path for content
// whose Metadata transaction did not commit (10-ops §3), so an already absent key is not
// an error: the desired end state is the same.
func (s *Store) Remove(storageKey string) error {
	if err := validateStorageKey(storageKey); err != nil {
		return err
	}
	if err := os.Remove(s.path(storageKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("asset: remove content: %w", redactPath(err))
	}
	return nil
}

// stagingDir returns the staging directory, creating it and the root if needed.
func (s *Store) stagingDir() (string, error) {
	if s.root == "" {
		return "", errors.New("asset: storage root is not configured")
	}
	staging := filepath.Join(s.root, stagingDirName)
	if err := os.MkdirAll(staging, dirPerm); err != nil {
		return "", fmt.Errorf("asset: prepare storage root: %w", redactPath(err))
	}
	return staging, nil
}

// path resolves a validated storage key to its absolute location.
func (s *Store) path(storageKey string) string {
	return filepath.Join(s.root, filepath.FromSlash(storageKey))
}

// storageKey derives "<shard>/<assetID>" from an asset id. The shard spreads Assets over
// directories instead of accumulating every one of them in a single directory; it is
// derived, not stored, but the derived value is what gets persisted as the Asset's
// storage key, so a later change of this function cannot orphan existing content.
func storageKey(assetID string) (string, error) {
	if err := validateAssetID(assetID); err != nil {
		return "", err
	}
	// Domain-prefixed ids ("asset_<uuid>") share their first characters, so the shard is
	// taken from the random part after the prefix.
	shard := assetID
	if i := strings.LastIndex(assetID, "_"); i >= 0 && i+1 < len(assetID) {
		shard = assetID[i+1:]
	}
	shard = (shard + "00")[:2]
	return shard + "/" + assetID, nil
}

// validateAssetID rejects anything that must not become a path component. An asset id
// reaches the filesystem, so traversal, separators and empty values are refused here
// rather than trusted from the caller.
func validateAssetID(assetID string) error {
	if assetID == "" {
		return errors.New("asset: asset id is required")
	}
	if len(assetID) > maxAssetIDLength {
		return errors.New("asset: asset id is too long")
	}
	for _, c := range assetID {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return fmt.Errorf("asset: asset id contains an unsupported character: %q", c)
		}
	}
	return nil
}

// validateStorageKey accepts only keys this package itself produces, so a key read back
// from the database can never resolve outside the root.
func validateStorageKey(storageKey string) error {
	shard, assetID, ok := strings.Cut(storageKey, "/")
	if !ok || len(shard) != 2 {
		return errors.New("asset: malformed storage key")
	}
	if err := validateAssetID(shard); err != nil {
		return errors.New("asset: malformed storage key")
	}
	if err := validateAssetID(assetID); err != nil {
		return errors.New("asset: malformed storage key")
	}
	return nil
}

// syncDir fsyncs a directory so a newly linked entry survives a power loss. Writing the
// file itself is not enough: the directory entry pointing at it is a separate write.
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

// redactPath strips the filesystem path out of an os path or link error. The storage root
// and every storage key are system Secrets (10-ops §4), and these errors are wrapped into
// values that reach logs and API error handling. The underlying cause is preserved, so
// errors.Is against os.ErrNotExist and os.ErrExist still works.
func redactPath(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return fmt.Errorf("%s: %w", linkErr.Op, linkErr.Err)
	}
	return err
}
