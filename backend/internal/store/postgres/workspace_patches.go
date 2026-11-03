package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type workspacePatchRepository struct {
	conn pgx.Tx
}

const workspacePatchColumns = `id, agent_run_id, seq, patch, patch_digest, touched_paths, tool_attempt_id, created_at`

// workspacePatchSummaryColumns reads every column but the patch text, with the empty
// string in its place so one scanner serves both shapes.
const workspacePatchSummaryColumns = `id, agent_run_id, seq, '', patch_digest, touched_paths, tool_attempt_id, created_at`

// Append inserts one cumulative patch in the caller's Tool result transaction. The two
// UNIQUE constraints and the length CHECK surface as domain.ErrConflict through mapError.
func (r *workspacePatchRepository) Append(ctx context.Context, patch domain.WorkspacePatch) error {
	touched, err := json.Marshal(patch.TouchedPaths)
	if err != nil {
		return fmt.Errorf("store/postgres workspace_patches.Append: patch=%s: marshal touched paths: %w", patch.ID, err)
	}
	if patch.TouchedPaths == nil {
		touched = []byte(`[]`)
	}

	const insert = `
		INSERT INTO workspace_patches (` + workspacePatchColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	if _, err := r.conn.Exec(ctx, insert,
		patch.ID, patch.AgentRunID, patch.Seq, patch.Patch, patch.PatchDigest, touched, patch.ToolAttemptID, patch.CreatedAt,
	); err != nil {
		return mapError("workspace_patches.Append", err, patch.ID, patch.AgentRunID, patch.Seq)
	}
	return nil
}

func (r *workspacePatchRepository) Latest(ctx context.Context, agentRunID string) (*domain.WorkspacePatch, error) {
	const query = `SELECT ` + workspacePatchColumns + `
		  FROM workspace_patches
		 WHERE agent_run_id = $1
		 ORDER BY seq DESC
		 LIMIT 1`

	rows, err := r.conn.Query(ctx, query, agentRunID)
	if err != nil {
		return nil, mapError("workspace_patches.Latest", err, agentRunID)
	}
	patches, err := scanWorkspacePatches(rows)
	if err != nil {
		return nil, mapError("workspace_patches.Latest", err, agentRunID)
	}
	if len(patches) == 0 {
		return nil, nil
	}
	return &patches[0], nil
}

func (r *workspacePatchRepository) GetByDigest(ctx context.Context, agentRunID, patchDigest string) (*domain.WorkspacePatch, error) {
	const query = `SELECT ` + workspacePatchColumns + `
		  FROM workspace_patches
		 WHERE agent_run_id = $1 AND patch_digest = $2
		 ORDER BY seq DESC
		 LIMIT 1`

	rows, err := r.conn.Query(ctx, query, agentRunID, patchDigest)
	if err != nil {
		return nil, mapError("workspace_patches.GetByDigest", err, agentRunID)
	}
	patches, err := scanWorkspacePatches(rows)
	if err != nil {
		return nil, mapError("workspace_patches.GetByDigest", err, agentRunID)
	}
	if len(patches) == 0 {
		return nil, nil
	}
	return &patches[0], nil
}

func (r *workspacePatchRepository) ListSummariesByAgentRun(ctx context.Context, agentRunID string, limit int) ([]domain.WorkspacePatch, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres workspace_patches.ListSummariesByAgentRun: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + workspacePatchSummaryColumns + `
		  FROM workspace_patches
		 WHERE agent_run_id = $1
		 ORDER BY seq ASC
		 LIMIT $2`

	rows, err := r.conn.Query(ctx, query, agentRunID, limit)
	if err != nil {
		return nil, mapError("workspace_patches.ListSummariesByAgentRun", err, agentRunID)
	}
	patches, err := scanWorkspacePatches(rows)
	if err != nil {
		return nil, mapError("workspace_patches.ListSummariesByAgentRun", err, agentRunID)
	}
	return patches, nil
}

func scanWorkspacePatches(rows pgx.Rows) ([]domain.WorkspacePatch, error) {
	defer rows.Close()

	out := []domain.WorkspacePatch{}
	for rows.Next() {
		var (
			patch   domain.WorkspacePatch
			touched []byte
		)
		if err := rows.Scan(
			&patch.ID, &patch.AgentRunID, &patch.Seq, &patch.Patch, &patch.PatchDigest, &touched, &patch.ToolAttemptID, &patch.CreatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(touched, &patch.TouchedPaths); err != nil {
			return nil, fmt.Errorf("patch=%s: unmarshal touched paths: %w", patch.ID, err)
		}
		out = append(out, patch)
	}
	return out, rows.Err()
}

var _ store.WorkspacePatchRepository = (*workspacePatchRepository)(nil)
