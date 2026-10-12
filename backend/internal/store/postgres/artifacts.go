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

// LinkToToolAttempt inserts one tool_attempt_artifacts row. It has no ON CONFLICT clause:
// the result transaction writes each declared link exactly once, so a duplicate is a
// defect that must fail the transaction.
func (r *artifactRepository) LinkToToolAttempt(ctx context.Context, link store.ToolAttemptArtifact) error {
	const insert = `
		INSERT INTO tool_attempt_artifacts (tool_attempt_id, artifact_id, run_id, created_at)
		VALUES ($1, $2, $3, $4)`
	if _, err := r.conn.Exec(ctx, insert, link.ToolAttemptID, link.ArtifactID, link.RunID, link.CreatedAt); err != nil {
		return mapError("artifacts.LinkToToolAttempt", err, link.ToolAttemptID, link.ArtifactID)
	}
	return nil
}

// GetForRun reads an artifact through a link of runID whose Tool Attempt succeeded. A
// missing Run, link or metadata row all leave the join empty and map to the same
// domain.ErrNotFound.
func (r *artifactRepository) GetForRun(ctx context.Context, runID, artifactID string) (store.ArtifactRecord, error) {
	const query = `
		SELECT a.artifact_id, a.media_type, a.size_bytes, a.sha256, a.storage_key, a.created_at
		FROM tool_attempt_artifacts l
		JOIN tool_attempts t ON t.id = l.tool_attempt_id
		JOIN execution_artifacts a ON a.artifact_id = l.artifact_id
		WHERE l.run_id = $1 AND l.artifact_id = $2 AND t.status = 'SUCCEEDED'
		LIMIT 1`

	var record store.ArtifactRecord
	if err := r.conn.QueryRow(ctx, query, runID, artifactID).Scan(
		&record.Artifact.ArtifactID,
		&record.Artifact.MediaType,
		&record.Artifact.SizeBytes,
		&record.Artifact.SHA256,
		&record.StorageKey,
		&record.Artifact.CreatedAt,
	); err != nil {
		return store.ArtifactRecord{}, mapError("artifacts.GetForRun", err, runID, artifactID)
	}
	return record, nil
}
