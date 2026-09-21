//go:build integration

// Package integration: this file covers the assets table — the Metadata half of an Asset.
// Its identity guarantee is a database constraint (PRIMARY KEY on asset_id), not a
// process-local check, so it can only be demonstrated against a real PostgreSQL.
package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

func assetRecordFixture(assetID string) store.AssetRecord {
	return store.AssetRecord{
		Asset: domain.Asset{
			AssetID:   assetID,
			MediaType: "image/png",
			SizeBytes: 2048,
			SHA256:    strings.Repeat("a1", 32),
			CreatedAt: fixtureTime,
		},
		StorageKey: "ab/" + assetID,
	}
}

func TestAssetRepository_CreateAndGet_RoundTripsMetadataAndStorageKey(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	want := assetRecordFixture("asset_roundtrip")

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Assets().Create(ctx, want)
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var got store.AssetRecord
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.Assets().Get(ctx, want.Asset.AssetID)
		return err
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Asset.AssetID != want.Asset.AssetID ||
		got.Asset.MediaType != want.Asset.MediaType ||
		got.Asset.SizeBytes != want.Asset.SizeBytes ||
		got.Asset.SHA256 != want.Asset.SHA256 {
		t.Errorf("metadata: got %+v, want %+v", got.Asset, want.Asset)
	}
	if !got.Asset.CreatedAt.Equal(want.Asset.CreatedAt) {
		t.Errorf("createdAt: got %s, want %s", got.Asset.CreatedAt, want.Asset.CreatedAt)
	}
	if got.StorageKey != want.StorageKey {
		t.Errorf("storage key: got %q, want %q", got.StorageKey, want.StorageKey)
	}
}

func TestAssetRepository_DuplicateAssetID_Conflicts(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	first := assetRecordFixture("asset_duplicate")

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Assets().Create(ctx, first)
	}); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	second := assetRecordFixture("asset_duplicate")
	second.Asset.SizeBytes = 4096
	second.StorageKey = "cd/asset_duplicate"
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Assets().Create(ctx, second)
	})

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Create of the same asset id: want domain.ErrConflict, got %v", err)
	}

	// The first row must be untouched: an asset_id points at immutable content.
	var got store.AssetRecord
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.Assets().Get(ctx, first.Asset.AssetID)
		return err
	}); err != nil {
		t.Fatalf("Get after the rejected duplicate: %v", err)
	}
	if got.Asset.SizeBytes != first.Asset.SizeBytes || got.StorageKey != first.StorageKey {
		t.Errorf("committed row changed: got %+v %q, want %+v %q",
			got.Asset, got.StorageKey, first.Asset, first.StorageKey)
	}
}

func TestAssetRepository_Get_UnknownAssetID_ReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Assets().Get(ctx, "asset_missing")
		return err
	})

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get of an unknown asset: want domain.ErrNotFound, got %v", err)
	}
}
