package asset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// sniffBytes is how much of the leading content is kept for media type detection. It
// covers the longest signature checked (RIFF....WEBP).
const sniffBytes = 12

// errContentTooLarge reports content above the cap of the writer that staged it. Each
// store maps it to its own domain error.
var errContentTooLarge = errors.New("content exceeds the size limit")

// stagedContent is one fully received, fsynced and closed staging file, plus the facts
// measured from its bytes.
type stagedContent struct {
	path      string
	sizeBytes int64
	sha256    string
	head      []byte
}

// discard removes the staging file. It is a no-op once the content has been linked into
// place and the staging entry removed.
func (c stagedContent) discard() {
	_ = os.Remove(c.path)
}

// headCapture keeps the first sniffBytes written through it.
type headCapture struct {
	head []byte
}

func (h *headCapture) Write(p []byte) (int, error) {
	if missing := sniffBytes - len(h.head); missing > 0 {
		if missing > len(p) {
			missing = len(p)
		}
		h.head = append(h.head, p[:missing]...)
	}
	return len(p), nil
}

// stageContent streams r into a new staging file under stagingDir while hashing and
// counting it, enforcing maxBytes as a streaming decision, and fsyncs the result. Every
// failure removes the staging file, so a failed write leaves nothing behind but the
// staging directory itself. Errors carry no filesystem path.
func stageContent(stagingDir, pattern string, r io.Reader, maxBytes int64) (stagedContent, error) {
	staged, err := os.CreateTemp(stagingDir, pattern)
	if err != nil {
		return stagedContent{}, fmt.Errorf("stage content: %w", redactPath(err))
	}
	content := stagedContent{path: staged.Name()}
	fail := func(err error) (stagedContent, error) {
		_ = staged.Close()
		content.discard()
		return stagedContent{}, err
	}

	digest := sha256.New()
	head := &headCapture{}
	// maxBytes+1 is what makes the cap a streaming decision: reading one byte past the
	// limit proves the content is too large without reading the rest of it.
	written, err := io.Copy(io.MultiWriter(staged, digest, head), io.LimitReader(r, maxBytes+1))
	if err != nil {
		return fail(fmt.Errorf("receive content: %w", redactPath(err)))
	}
	if written > maxBytes {
		return fail(fmt.Errorf("receive content: %w", errContentTooLarge))
	}
	if err := staged.Sync(); err != nil {
		return fail(fmt.Errorf("flush content: %w", redactPath(err)))
	}
	if err := staged.Close(); err != nil {
		content.discard()
		return stagedContent{}, fmt.Errorf("close staged content: %w", redactPath(err))
	}
	content.sizeBytes = written
	content.sha256 = hex.EncodeToString(digest.Sum(nil))
	content.head = head.head
	return content, nil
}

// linkImmutable makes staged content reachable at destination in one atomic step and
// makes the new directory entry durable. It uses os.Link, not os.Rename: rename silently
// replaces an existing destination, which would break the immutability a reference
// promises. An existing destination is reported as an error wrapping os.ErrExist and is
// left untouched. On success the staging entry is removed.
func linkImmutable(content stagedContent, destination string) error {
	shardDir := filepath.Dir(destination)
	if err := os.MkdirAll(shardDir, dirPerm); err != nil {
		return fmt.Errorf("create shard directory: %w", redactPath(err))
	}
	if err := os.Link(content.path, destination); err != nil {
		return fmt.Errorf("commit content: %w", redactPath(err))
	}
	if err := syncDir(shardDir); err != nil {
		return fmt.Errorf("flush shard directory: %w", redactPath(err))
	}
	if err := os.Remove(content.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear staged content: %w", redactPath(err))
	}
	return nil
}

// probeReadWrite proves dir supports the create, write, read and delete the stores
// perform, leaving nothing behind.
func probeReadWrite(dir string) error {
	probe, err := os.CreateTemp(dir, "readiness-*")
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
