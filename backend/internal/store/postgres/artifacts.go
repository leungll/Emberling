package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type artifactRepository struct {
	conn pgx.Tx
}

const artifactColumns = `artifact_id, media_type, size_bytes, sha256, storage_key, created_at`

// CreateIfAbsent inserts one Execution Artifact, letting PRIMARY KEY (artifact_id) decide
// duplicates. ON CONFLICT DO NOTHING keeps the first committed row and its created_at;
// when nothing was inserted, the existing row must describe the same content, otherwise
// the id is being reused for different bytes and the caller's transaction must fail.
func (r *artifactRepository) CreateIfAbsent(ctx context.Context, record store.ArtifactRecord) error {
	const insert = `
		INSERT INTO execution_artifacts (` + artifactColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (artifact_id) DO NOTHING`
	tag, err := r.conn.Exec(ctx, insert,
		record.Artifact.ArtifactID,
		record.Artifact.MediaType,
		record.Artifact.SizeBytes,
		record.Artifact.SHA256,
		record.StorageKey,
		record.Artifact.CreatedAt,
	)
	if err != nil {
		return mapError("artifacts.CreateIfAbsent", err, record.Artifact.ArtifactID)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	existing, err := r.Get(ctx, record.Artifact.ArtifactID)
	if err != nil {
		return err
	}
	if existing.Artifact.SHA256 != record.Artifact.SHA256 ||
		existing.Artifact.SizeBytes != record.Artifact.SizeBytes ||
		existing.Artifact.MediaType != record.Artifact.MediaType {
		return fmt.Errorf("store/postgres artifacts.CreateIfAbsent: artifact=%s: existing row describes different content: %w",
			record.Artifact.ArtifactID, domain.ErrConflict)
	}
	return nil
}

// Get returns one Execution Artifact's Metadata and storage key.
func (r *artifactRepository) Get(ctx context.Context, artifactID string) (store.ArtifactRecord, error) {
	const query = `SELECT ` + artifactColumns + ` FROM execution_artifacts WHERE artifact_id = $1`

	var record store.ArtifactRecord
	if err := r.conn.QueryRow(ctx, query, artifactID).Scan(
		&record.Artifact.ArtifactID,
		&record.Artifact.MediaType,
		&record.Artifact.SizeBytes,
		&record.Artifact.SHA256,
		&record.StorageKey,
		&record.Artifact.CreatedAt,
	); err != nil {
		return store.ArtifactRecord{}, mapError("artifacts.Get", err, artifactID)
	}
	return record, nil
}
