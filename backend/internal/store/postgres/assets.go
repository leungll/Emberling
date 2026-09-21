package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type assetRepository struct {
	conn pgx.Tx
}

const assetColumns = `asset_id, media_type, size_bytes, sha256, storage_key, created_at`

// Create inserts one Asset. There is no upsert: PRIMARY KEY (asset_id) is the single
// arbiter of identity, so a repeated id is a conflict rather than a rewrite of content
// another Run may already reference.
func (r *assetRepository) Create(ctx context.Context, record store.AssetRecord) error {
	const insert = `
		INSERT INTO assets (` + assetColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := r.conn.Exec(ctx, insert,
		record.Asset.AssetID,
		record.Asset.MediaType,
		record.Asset.SizeBytes,
		record.Asset.SHA256,
		record.StorageKey,
		record.Asset.CreatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("store/postgres assets.Create: asset=%s: %w", record.Asset.AssetID, domain.ErrConflict)
		}
		return mapError("assets.Create", err, record.Asset.AssetID)
	}
	return nil
}

// Get returns one Asset's Metadata and storage key.
func (r *assetRepository) Get(ctx context.Context, assetID string) (store.AssetRecord, error) {
	const query = `SELECT ` + assetColumns + ` FROM assets WHERE asset_id = $1`

	var record store.AssetRecord
	if err := r.conn.QueryRow(ctx, query, assetID).Scan(
		&record.Asset.AssetID,
		&record.Asset.MediaType,
		&record.Asset.SizeBytes,
		&record.Asset.SHA256,
		&record.StorageKey,
		&record.Asset.CreatedAt,
	); err != nil {
		return store.AssetRecord{}, mapError("assets.Get", err, assetID)
	}
	return record, nil
}
