package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// AssetContentStore is the Asset binary storage port. internal/asset implements it over a
// local persistent volume; this package only needs the three operations the fixed upload
// order requires.
type AssetContentStore interface {
	// Write streams content into storage and reports the size and digest of the bytes
	// that landed, together with the internal storage key they landed under.
	Write(ctx context.Context, assetID string, r io.Reader) (asset.StoredContent, error)

	// Open returns the content stored under a key, for a controlled download.
	Open(ctx context.Context, storageKey string) (io.ReadCloser, error)

	// Remove deletes content whose Metadata never committed.
	Remove(storageKey string) error
}

// AssetService owns the Asset use cases: upload, Metadata read and controlled download.
// An Asset is not Execution State — it belongs to no Run and produces no Event
// — so this service takes no Run aggregate lock and appends nothing to an Event log.
type AssetService struct {
	deps Deps
}

// NewAssetService builds an AssetService from the shared Deps skeleton.
func NewAssetService(deps Deps) *AssetService {
	return &AssetService{deps: deps.withDefaults()}
}

// Upload performs the fixed upload order: stream the content into immutable
// storage while measuring it, verify what landed, commit the Metadata, and only then
// return an AssetRef. A caller therefore never receives a reference to an Asset that
// PostgreSQL does not know about, or whose binary is incomplete.
//
// declaredSize is the size the transport reported, or zero when it reported none. It is a
// cross-check against the bytes that actually landed, never the recorded size: SizeBytes
// and SHA256 always come from the stream itself.
//
// When the Metadata transaction fails, the unreferenced binary is removed on a best-effort
// basis. A failure to remove it is logged and not returned: the caller's upload failed
// either way, and the upload order leaves an unreferenced binary to cleanup, never to a
// half-committed Asset.
func (s *AssetService) Upload(ctx context.Context, mediaType string, r io.Reader, declaredSize int64) (domain.AssetRef, error) {
	if !domain.IsSupportedAssetMediaType(mediaType) {
		return domain.AssetRef{}, fmt.Errorf("service assets.Upload: media type %q: %w",
			mediaType, domain.ErrUnsupportedAssetMediaType)
	}

	assetID := s.deps.IDs.NewID(domain.IDPrefixAsset)

	stored, err := s.deps.Assets.Write(ctx, assetID, r)
	if err != nil {
		return domain.AssetRef{}, fmt.Errorf("service assets.Upload: asset=%s: %w", assetID, err)
	}

	record := store.AssetRecord{
		Asset: domain.Asset{
			AssetID:   assetID,
			MediaType: mediaType,
			SizeBytes: stored.SizeBytes,
			SHA256:    stored.SHA256,
			CreatedAt: s.deps.Clock.Now(),
		},
		StorageKey: stored.StorageKey,
	}

	if err := s.verifyStored(record.Asset, declaredSize); err != nil {
		s.removeOrphan(assetID, stored.StorageKey)
		return domain.AssetRef{}, err
	}

	if err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Assets().Create(ctx, record)
	}); err != nil {
		s.removeOrphan(assetID, stored.StorageKey)
		return domain.AssetRef{}, fmt.Errorf("service assets.Upload: commit metadata: asset=%s: %w", assetID, err)
	}

	return record.Asset.Ref(), nil
}

// Get returns one Asset's Metadata. The storage key stays inside the Store boundary.
func (s *AssetService) Get(ctx context.Context, assetID string) (domain.Asset, error) {
	record, err := s.record(ctx, assetID)
	if err != nil {
		return domain.Asset{}, err
	}
	return record.Asset, nil
}

// OpenContent returns an Asset's Metadata together with a reader over its content, for
// the controlled download that replaces a signed URL. The caller closes the reader.
func (s *AssetService) OpenContent(ctx context.Context, assetID string) (domain.Asset, io.ReadCloser, error) {
	record, err := s.record(ctx, assetID)
	if err != nil {
		return domain.Asset{}, nil, err
	}
	content, err := s.deps.Assets.Open(ctx, record.StorageKey)
	if err != nil {
		// The storage key is never included: it is a system Secret, and the asset id
		// already identifies the failure.
		return domain.Asset{}, nil, fmt.Errorf("service assets.OpenContent: asset=%s: %w", assetID, err)
	}
	return record.Asset, content, nil
}

// record reads one assets row in its own read transaction.
func (s *AssetService) record(ctx context.Context, assetID string) (store.AssetRecord, error) {
	var record store.AssetRecord
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Assets().Get(ctx, assetID)
		return err
	})
	if err != nil {
		return store.AssetRecord{}, fmt.Errorf("service assets: asset=%s: %w", assetID, err)
	}
	return record, nil
}

// verifyStored is the "verify" step between writing the binary and committing Metadata:
// the Metadata about to be committed must describe the content that actually landed.
func (s *AssetService) verifyStored(a domain.Asset, declaredSize int64) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("service assets.Upload: %w", err)
	}
	if declaredSize > 0 && declaredSize != a.SizeBytes {
		return fmt.Errorf("service assets.Upload: asset=%s: received %d bytes, caller declared %d: %w",
			a.AssetID, a.SizeBytes, declaredSize, domain.ErrAssetContentMismatch)
	}
	return nil
}

// removeOrphan deletes a binary whose Asset never became a fact. The storage key is never
// logged: it is a system Secret, and the asset id is enough to investigate.
func (s *AssetService) removeOrphan(assetID, storageKey string) {
	if err := s.deps.Assets.Remove(storageKey); err != nil && !errors.Is(err, domain.ErrNotFound) {
		s.deps.Logger.Warn("service assets: unreferenced asset content left on storage",
			slog.String("asset_id", assetID), slog.String("error", err.Error()))
	}
}
