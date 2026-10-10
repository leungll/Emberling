package asset_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
)

// pngContent is a PNG signature followed by arbitrary bytes: the store sniffs only the
// leading signature, so this is enough to be accepted as image/png.
func pngContent(body string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), body...)
}

func newArtifactStore(t *testing.T, maxBytes int64) (*asset.ArtifactStore, string) {
	t.Helper()
	root := t.TempDir()
	store := asset.NewArtifactStore(root, maxBytes)
	if err := store.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("VerifyReadWrite: %v", err)
	}
	return store, root
}

func stagingEntries(t *testing.T, root string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "artifacts", ".staging"))
	if err != nil {
		t.Fatalf("read staging: %v", err)
	}
	return entries
}

func TestArtifactStore_Write_DerivesIDAndKeyFromTheContentDigest(t *testing.T) {
	store, root := newArtifactStore(t, 1<<20)
	content := pngContent("ember")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	ref, err := store.Write(context.Background(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := domain.ArtifactRef{ArtifactID: "artifact_" + digest[:32], MediaType: "image/png", SizeBytes: int64(len(content)), SHA256: digest}
	if ref != want {
		t.Fatalf("ref = %+v, want %+v", ref, want)
	}
	key, err := asset.ArtifactStorageKey(digest)
	if err != nil {
		t.Fatalf("ArtifactStorageKey: %v", err)
	}
	if wantKey := "artifacts/" + digest[:2] + "/" + digest; key != wantKey {
		t.Fatalf("key = %q, want %q", key, wantKey)
	}
	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(key)))
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatal("stored object differs from the written content")
	}
	if entries := stagingEntries(t, root); len(entries) != 0 {
		t.Fatalf("staging entries after success: %d, want 0", len(entries))
	}
}

func TestArtifactStore_Write_AlreadyPresent_SucceedsWithoutOverwriting(t *testing.T) {
	store, root := newArtifactStore(t, 1<<20)
	content := pngContent("same")
	first, err := store.Write(context.Background(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}
	key, _ := asset.ArtifactStorageKey(first.SHA256)
	path := filepath.Join(root, filepath.FromSlash(key))
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	second, err := store.Write(context.Background(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if second != first {
		t.Fatalf("second ref = %+v, want %+v", second, first)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the existing object was replaced")
	}
	if entries := stagingEntries(t, root); len(entries) != 0 {
		t.Fatalf("staging entries after duplicate write: %d, want 0", len(entries))
	}
}

func TestArtifactStore_Write_ExceedsMaxBytes_FailsAndLeavesNothing(t *testing.T) {
	store, root := newArtifactStore(t, 16)

	_, err := store.Write(context.Background(), bytes.NewReader(pngContent(strings.Repeat("x", 9))))

	if !errors.Is(err, domain.ErrArtifactTooLarge) {
		t.Fatalf("Write error = %v, want ErrArtifactTooLarge", err)
	}
	objects, listErr := store.Objects(context.Background())
	if listErr != nil || len(objects) != 0 {
		t.Fatalf("objects = %v (err %v), want none", objects, listErr)
	}
	if entries := stagingEntries(t, root); len(entries) != 0 {
		t.Fatalf("staging entries after oversize: %d, want 0", len(entries))
	}
}

func TestArtifactStore_Write_SupportedSignatures_AreSniffed(t *testing.T) {
	store, _ := newArtifactStore(t, 1<<20)
	cases := map[string][]byte{
		"image/png":  pngContent("p"),
		"image/jpeg": []byte("\xff\xd8\xff\xe0jpeg-body"),
		"image/webp": []byte("RIFF\x10\x00\x00\x00WEBPVP8 "),
	}
	for want, content := range cases {
		ref, err := store.Write(context.Background(), bytes.NewReader(content))
		if err != nil {
			t.Fatalf("Write %s: %v", want, err)
		}
		if ref.MediaType != want {
			t.Fatalf("mediaType = %q, want %q", ref.MediaType, want)
		}
	}
}

func TestArtifactStore_Write_NonImage_IsRejectedAndLeavesNothing(t *testing.T) {
	store, root := newArtifactStore(t, 1<<20)

	_, err := store.Write(context.Background(), strings.NewReader("<html>not an image</html>"))

	if !errors.Is(err, domain.ErrUnsupportedAssetMediaType) {
		t.Fatalf("Write error = %v, want ErrUnsupportedAssetMediaType", err)
	}
	if objects, _ := store.Objects(context.Background()); len(objects) != 0 {
		t.Fatalf("objects after rejection: %v", objects)
	}
	if entries := stagingEntries(t, root); len(entries) != 0 {
		t.Fatalf("staging entries after rejection: %d, want 0", len(entries))
	}
}

func TestArtifactStore_Write_ReaderFails_CleansStagingAndErrorHasNoPath(t *testing.T) {
	store, root := newArtifactStore(t, 1<<20)

	_, err := store.Write(context.Background(), &failingReader{data: pngContent("half")})

	if err == nil {
		t.Fatal("Write with a failing reader: want error")
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "artifacts/") {
		t.Fatalf("error leaks a path: %v", err)
	}
	if entries := stagingEntries(t, root); len(entries) != 0 {
		t.Fatalf("staging entries after a failed stream: %d, want 0", len(entries))
	}
}

func TestArtifactStore_Remove_DeletesAndIsIdempotent(t *testing.T) {
	store, root := newArtifactStore(t, 1<<20)
	ref, err := store.Write(context.Background(), bytes.NewReader(pngContent("gone")))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	key, _ := asset.ArtifactStorageKey(ref.SHA256)

	for i := 0; i < 2; i++ {
		if err := store.Remove(key); err != nil {
			t.Fatalf("Remove #%d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(key))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object still present: %v", err)
	}
}

func TestArtifactStore_Remove_MalformedKey_IsRejected(t *testing.T) {
	store, _ := newArtifactStore(t, 1<<20)
	for _, key := range []string{"", "../etc/passwd", "artifacts/../x", "ab/asset_1", "artifacts/00/" + strings.Repeat("a", 64)} {
		if err := store.Remove(key); err == nil {
			t.Fatalf("Remove(%q) succeeded, want error", key)
		}
	}
}

func TestArtifactStore_Objects_EnumeratesCommittedObjectsOnly(t *testing.T) {
	store, root := newArtifactStore(t, 1<<20)
	ref, err := store.Write(context.Background(), bytes.NewReader(pngContent("listed")))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// A stray file in the subtree is never mistaken for an artifact.
	if err := os.WriteFile(filepath.Join(root, "artifacts", ref.SHA256[:2], "stray"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write stray: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", ".staging", "artifact-left"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write leftover: %v", err)
	}

	objects, err := store.Objects(context.Background())
	if err != nil {
		t.Fatalf("Objects: %v", err)
	}
	if len(objects) != 1 || objects[0].ArtifactID != ref.ArtifactID || objects[0].SHA256 != ref.SHA256 || objects[0].ModTime.IsZero() {
		t.Fatalf("objects = %+v, want one object for %s", objects, ref.ArtifactID)
	}

	leftovers, err := store.StagedLeftovers(context.Background())
	if err != nil {
		t.Fatalf("StagedLeftovers: %v", err)
	}
	if len(leftovers) != 1 || leftovers[0].Name != "artifact-left" {
		t.Fatalf("leftovers = %+v, want artifact-left", leftovers)
	}
	if err := store.RemoveStaged("artifact-left"); err != nil {
		t.Fatalf("RemoveStaged: %v", err)
	}
	if err := store.RemoveStaged("../escape"); err == nil {
		t.Fatal("RemoveStaged accepted a path")
	}
	if leftovers, _ := store.StagedLeftovers(context.Background()); len(leftovers) != 0 {
		t.Fatalf("leftovers after removal: %+v", leftovers)
	}
}

func TestArtifactStore_Objects_MissingSubtree_IsEmpty(t *testing.T) {
	store := asset.NewArtifactStore(t.TempDir(), 1<<20)
	objects, err := store.Objects(context.Background())
	if err != nil || len(objects) != 0 {
		t.Fatalf("Objects = %v, %v; want empty", objects, err)
	}
}

func TestArtifactStore_SharesRootWithAssets_WithoutCollision(t *testing.T) {
	root := t.TempDir()
	assets := asset.NewStore(root, 1<<20)
	artifacts := asset.NewArtifactStore(root, 1<<20)
	if _, err := assets.Write(context.Background(), "asset_ar", bytes.NewReader([]byte("asset"))); err != nil {
		t.Fatalf("asset Write: %v", err)
	}
	if _, err := artifacts.Write(context.Background(), bytes.NewReader(pngContent("artifact"))); err != nil {
		t.Fatalf("artifact Write: %v", err)
	}
	objects, err := artifacts.Objects(context.Background())
	if err != nil || len(objects) != 1 {
		t.Fatalf("Objects = %v, %v; want only the artifact", objects, err)
	}
}
