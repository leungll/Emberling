//go:build integration

// Package integration: this file covers AssetService's upload order, which 10-ops §3
// fixes as "write the binary, verify it, commit the Metadata, and only then return an
// AssetRef". The failure this file exists for is the third step losing: a caller must
// never receive an AssetRef for an Asset PostgreSQL does not know about, and the
// unreferenced binary must not stay on the volume.
package integration

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
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

const assetTestMaxUploadBytes = 1 << 20

// failingCommitUoW commits nothing: the wrapped transaction function runs in full (so the
// Asset row really is inserted) and then the transaction is rolled back, which is what a
// COMMIT failure looks like to the service layer.
type failingCommitUoW struct {
	inner store.UnitOfWork
	err   error
}

func (u failingCommitUoW) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return u.inner.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return u.err
	})
}

func (u failingCommitUoW) WithinReadTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return u.inner.WithinReadTx(ctx, fn)
}

// assetBinaries lists the committed content under root, skipping the staging directory.
func assetBinaries(t *testing.T, root string) []string {
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

func newAssetServiceDeps(t *testing.T, uow store.UnitOfWork) (service.Deps, string) {
	t.Helper()
	root := t.TempDir()
	content := asset.NewStore(root, assetTestMaxUploadBytes)
	if err := content.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("VerifyReadWrite: %v", err)
	}
	return service.Deps{
		UoW:    uow,
		Clock:  fixedClock{at: fixtureTime},
		IDs:    &sequentialIDs{},
		Assets: content,
	}, root
}

func assetRecord(ctx context.Context, t *testing.T, uow store.UnitOfWork, assetID string) (store.AssetRecord, error) {
	t.Helper()
	var record store.AssetRecord
	err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Assets().Get(ctx, assetID)
		return err
	})
	return record, err
}

func TestAssetUpload_Succeeds_RefReturnedOnlyAfterCommit(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	deps, root := newAssetServiceDeps(t, uow)
	svc := service.NewAssetService(deps)

	content := bytes.Repeat([]byte("ember"), 500)
	digest := sha256.Sum256(content)

	ref, err := svc.Upload(ctx, "image/png", bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if ref.MediaType != "image/png" {
		t.Errorf("ref.mediaType: got %q, want image/png", ref.MediaType)
	}
	if ref.SizeBytes != int64(len(content)) {
		t.Errorf("ref.sizeBytes: got %d, want %d", ref.SizeBytes, len(content))
	}
	if ref.SHA256 != hex.EncodeToString(digest[:]) {
		t.Errorf("ref.sha256: got %s, want %s", ref.SHA256, hex.EncodeToString(digest[:]))
	}

	record, err := assetRecord(ctx, t, uow, ref.AssetID)
	if err != nil {
		t.Fatalf("the returned AssetRef has no committed row: %v", err)
	}
	if record.Asset.SizeBytes != ref.SizeBytes || record.Asset.SHA256 != ref.SHA256 {
		t.Errorf("committed metadata %+v disagrees with the returned ref %+v", record.Asset, ref)
	}

	binaries := assetBinaries(t, root)
	if len(binaries) != 1 || binaries[0] != record.StorageKey {
		t.Fatalf("committed binaries: got %v, want exactly [%s]", binaries, record.StorageKey)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(record.StorageKey)))
	if err != nil {
		t.Fatalf("read the committed binary: %v", err)
	}
	if !bytes.Equal(raw, content) {
		t.Errorf("committed binary: got %d bytes, want %d", len(raw), len(content))
	}

	got, reader, err := svc.OpenContent(ctx, ref.AssetID)
	if err != nil {
		t.Fatalf("OpenContent: %v", err)
	}
	defer reader.Close()
	streamed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read streamed content: %v", err)
	}
	if !bytes.Equal(streamed, content) || got.SHA256 != ref.SHA256 {
		t.Errorf("OpenContent returned %d bytes with sha256 %s, want %d bytes with %s",
			len(streamed), got.SHA256, len(content), ref.SHA256)
	}
}

func TestAssetUpload_MetadataCommitFails_BinaryRemovedAndNoRow(t *testing.T) {
	ctx := context.Background()
	realUoW := postgres.NewUnitOfWork(testdb.Open(t))
	deps, root := newAssetServiceDeps(t, realUoW)
	commitErr := errors.New("metadata commit failed")
	deps.UoW = failingCommitUoW{inner: realUoW, err: commitErr}
	svc := service.NewAssetService(deps)

	ref, err := svc.Upload(ctx, "image/png", strings.NewReader("reference image bytes"), 21)

	if !errors.Is(err, commitErr) {
		t.Fatalf("Upload with a failing metadata commit: want the commit error, got %v", err)
	}
	if ref != (domain.AssetRef{}) {
		t.Errorf("Upload returned an AssetRef for an uncommitted Asset: %+v", ref)
	}
	if binaries := assetBinaries(t, root); len(binaries) != 0 {
		t.Errorf("binaries after a failed metadata commit: got %v, want none (10-ops §3 orphan cleanup)", binaries)
	}

	// The id the injected generator minted for this upload: nothing else could have
	// created it, so its absence proves the insert was rolled back.
	if _, err := assetRecord(ctx, t, realUoW, "asset_defsvc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("asset row after a rolled back commit: want domain.ErrNotFound, got %v", err)
	}
}

func TestAssetUpload_UnsupportedMediaType_StoresNothing(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	deps, root := newAssetServiceDeps(t, uow)
	svc := service.NewAssetService(deps)

	_, err := svc.Upload(ctx, "application/pdf", strings.NewReader("%PDF-1.4"), 8)

	if !errors.Is(err, domain.ErrUnsupportedAssetMediaType) {
		t.Fatalf("Upload of an unsupported media type: want ErrUnsupportedAssetMediaType, got %v", err)
	}
	if binaries := assetBinaries(t, root); len(binaries) != 0 {
		t.Errorf("binaries after a rejected media type: got %v, want none", binaries)
	}
}

func TestAssetUpload_DeclaredSizeDisagrees_StoresNothing(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	deps, root := newAssetServiceDeps(t, uow)
	svc := service.NewAssetService(deps)

	_, err := svc.Upload(ctx, "image/png", strings.NewReader("four"), 99)

	if !errors.Is(err, domain.ErrAssetContentMismatch) {
		t.Fatalf("Upload with a disagreeing declared size: want ErrAssetContentMismatch, got %v", err)
	}
	if binaries := assetBinaries(t, root); len(binaries) != 0 {
		t.Errorf("binaries after a content mismatch: got %v, want none", binaries)
	}
}

func TestAssetGet_UnknownAsset_ReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	deps, _ := newAssetServiceDeps(t, uow)
	svc := service.NewAssetService(deps)

	if _, err := svc.Get(ctx, "asset_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get of an unknown asset: want domain.ErrNotFound, got %v", err)
	}
	if _, _, err := svc.OpenContent(ctx, "asset_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("OpenContent of an unknown asset: want domain.ErrNotFound, got %v", err)
	}
}
