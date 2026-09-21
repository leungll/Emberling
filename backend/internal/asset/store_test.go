package asset_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
)

func newStore(t *testing.T, maxBytes int64) (*asset.Store, string) {
	t.Helper()
	root := t.TempDir()
	store := asset.NewStore(root, maxBytes)
	if err := store.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("VerifyReadWrite: %v", err)
	}
	return store, root
}

// committedKeys lists the storage keys that exist under root, ignoring the staging
// directory: a write that fails must leave the key namespace exactly as it found it.
func committedKeys(t *testing.T, root string) []string {
	t.Helper()
	var keys []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." {
				return fs.SkipDir
			}
			return nil
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return keys
}

func TestAssetStore_Write_ReportsStreamedHashAndSize(t *testing.T) {
	store, root := newStore(t, 1<<20)
	content := bytes.Repeat([]byte("ember"), 1000)
	sum := sha256.Sum256(content)

	stored, err := store.Write(context.Background(), "asset_abcdef", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if stored.SizeBytes != int64(len(content)) {
		t.Errorf("SizeBytes: got %d, want %d", stored.SizeBytes, len(content))
	}
	if stored.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("SHA256: got %s, want %s", stored.SHA256, hex.EncodeToString(sum[:]))
	}
	if keys := committedKeys(t, root); len(keys) != 1 || keys[0] != stored.StorageKey {
		t.Errorf("committed keys: got %v, want exactly [%s]", keys, stored.StorageKey)
	}

	rc, err := store.Open(context.Background(), stored.StorageKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content: got %d bytes, want %d", len(got), len(content))
	}
}

func TestAssetStore_Write_ExceedsMaxBytes_FailsWhileStreamingAndCommitsNothing(t *testing.T) {
	store, root := newStore(t, 16)

	_, err := store.Write(context.Background(), "asset_toolarge", bytes.NewReader(bytes.Repeat([]byte("x"), 17)))

	if !errors.Is(err, domain.ErrAssetTooLarge) {
		t.Fatalf("Write oversized content: want ErrAssetTooLarge, got %v", err)
	}
	if keys := committedKeys(t, root); len(keys) != 0 {
		t.Errorf("committed keys after a rejected write: got %v, want none", keys)
	}
}

func TestAssetStore_Write_ExactlyMaxBytes_Succeeds(t *testing.T) {
	store, _ := newStore(t, 16)

	stored, err := store.Write(context.Background(), "asset_atcap", bytes.NewReader(bytes.Repeat([]byte("x"), 16)))
	if err != nil {
		t.Fatalf("Write at the cap: %v", err)
	}
	if stored.SizeBytes != 16 {
		t.Errorf("SizeBytes: got %d, want 16", stored.SizeBytes)
	}
}

func TestAssetStore_Write_ExistingKey_NeverOverwrites(t *testing.T) {
	store, root := newStore(t, 1<<20)
	first, err := store.Write(context.Background(), "asset_same", strings.NewReader("original"))
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}

	_, err = store.Write(context.Background(), "asset_same", strings.NewReader("replacement"))

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Write of the same asset id: want ErrConflict, got %v", err)
	}
	rc, err := store.Open(context.Background(), first.StorageKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "original" {
		t.Errorf("content after a rejected overwrite: got %q, want %q", got, "original")
	}
	if keys := committedKeys(t, root); len(keys) != 1 {
		t.Errorf("committed keys: got %v, want exactly one", keys)
	}
}

// failingReader stops mid-stream, which is how a client that disconnects halfway through
// an upload reaches the storage layer.
type failingReader struct {
	data   []byte
	offset int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, errors.New("connection reset")
	}
	n := copy(p, r.data[r.offset:r.offset+1])
	r.offset += n
	return n, nil
}

func TestAssetStore_Write_ReaderFails_CommitsNoPartialContent(t *testing.T) {
	store, root := newStore(t, 1<<20)

	_, err := store.Write(context.Background(), "asset_partial", &failingReader{data: []byte("half")})

	if err == nil {
		t.Fatal("Write with a failing reader: want error, got nil")
	}
	if keys := committedKeys(t, root); len(keys) != 0 {
		t.Errorf("committed keys after a failed stream: got %v, want none", keys)
	}
}

func TestAssetStore_Write_UnsafeAssetID_IsRejected(t *testing.T) {
	store, root := newStore(t, 1<<20)

	for _, assetID := range []string{"", "..", "../escape", "a/b", `a\b`, "asset_ok/../../etc"} {
		if _, err := store.Write(context.Background(), assetID, strings.NewReader("x")); err == nil {
			t.Errorf("Write with asset id %q: want error, got nil", assetID)
		}
	}
	if keys := committedKeys(t, root); len(keys) != 0 {
		t.Errorf("committed keys: got %v, want none", keys)
	}
}

func TestAssetStore_Open_UnsafeOrUnknownKey_ReturnsNotFound(t *testing.T) {
	store, _ := newStore(t, 1<<20)

	for _, key := range []string{"ab/asset_missing", "../../etc/passwd", ""} {
		if _, err := store.Open(context.Background(), key); err == nil {
			t.Errorf("Open(%q): want error, got nil", key)
		}
	}
}

func TestAssetStore_Remove_DeletesTheBinaryAndIsIdempotent(t *testing.T) {
	store, root := newStore(t, 1<<20)
	stored, err := store.Write(context.Background(), "asset_orphan", strings.NewReader("orphan"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := store.Remove(stored.StorageKey); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := store.Remove(stored.StorageKey); err != nil {
		t.Fatalf("second Remove: want nil for an already removed key, got %v", err)
	}
	if keys := committedKeys(t, root); len(keys) != 0 {
		t.Errorf("committed keys after Remove: got %v, want none", keys)
	}
}

func TestAssetStore_VerifyReadWrite_UnwritableRoot_Fails(t *testing.T) {
	parent := t.TempDir()
	// A file where the root directory must be: the root can neither be created nor
	// written, which is exactly what readiness step 4 has to catch.
	root := filepath.Join(parent, "root")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := asset.NewStore(root, 1<<20).VerifyReadWrite(context.Background()); err == nil {
		t.Fatal("VerifyReadWrite with an unusable root: want error, got nil")
	}
}

func TestAssetStore_VerifyReadWrite_LeavesNoProbeBehind(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assets")
	store := asset.NewStore(root, 1<<20)

	if err := store.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("VerifyReadWrite: %v", err)
	}

	if keys := committedKeys(t, root); len(keys) != 0 {
		t.Errorf("files under the root after the probe: got %v, want none", keys)
	}
}
