package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// Both version chains are append-only: neither repository has an update method, because a
// committed Context or State Version is the recovery input a restarted process replays
// from. UNIQUE (agent_run_id, version) keeps each chain linear.

type agentContextVersionRepository struct {
	conn pgx.Tx
}

const agentContextVersionColumns = `id, agent_run_id, version, source_turn_id, messages, created_at`

func (r *agentContextVersionRepository) Create(ctx context.Context, version domain.AgentContextVersion) error {
	const insert = `
		INSERT INTO agent_context_versions (` + agentContextVersionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := r.conn.Exec(ctx, insert,
		version.ID, version.AgentRunID, version.Version, version.SourceTurnID,
		version.Messages, version.CreatedAt,
	); err != nil {
		return mapError("agent_context_versions.Create", err, version.ID, version.AgentRunID, version.Version)
	}
	return nil
}

func (r *agentContextVersionRepository) GetByRunAndVersion(ctx context.Context, agentRunID string, version int) (domain.AgentContextVersion, error) {
	const query = `SELECT ` + agentContextVersionColumns + `
		  FROM agent_context_versions WHERE agent_run_id = $1 AND version = $2`

	rows, err := r.conn.Query(ctx, query, agentRunID, version)
	if err != nil {
		return domain.AgentContextVersion{}, mapError("agent_context_versions.GetByRunAndVersion", err, agentRunID, version)
	}
	versions, err := scanAgentContextVersions(rows)
	if err != nil {
		return domain.AgentContextVersion{}, mapError("agent_context_versions.GetByRunAndVersion", err, agentRunID, version)
	}
	if len(versions) == 0 {
		return domain.AgentContextVersion{}, mapError("agent_context_versions.GetByRunAndVersion", pgx.ErrNoRows, agentRunID, version)
	}
	return versions[0], nil
}

func scanAgentContextVersions(rows pgx.Rows) ([]domain.AgentContextVersion, error) {
	defer rows.Close()

	out := []domain.AgentContextVersion{}
	for rows.Next() {
		var (
			version  domain.AgentContextVersion
			messages []byte
		)
		if err := rows.Scan(
			&version.ID, &version.AgentRunID, &version.Version, &version.SourceTurnID,
			&messages, &version.CreatedAt,
		); err != nil {
			return nil, err
		}
		version.Messages = json.RawMessage(messages)
		out = append(out, version)
	}
	return out, rows.Err()
}

type agentStateVersionRepository struct {
	conn pgx.Tx
}

const agentStateVersionColumns = `id, agent_run_id, version, source_turn_id, value, created_at`

func (r *agentStateVersionRepository) Create(ctx context.Context, version domain.AgentStateVersion) error {
	const insert = `
		INSERT INTO agent_state_versions (` + agentStateVersionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := r.conn.Exec(ctx, insert,
		version.ID, version.AgentRunID, version.Version, version.SourceTurnID,
		version.Value, version.CreatedAt,
	); err != nil {
		return mapError("agent_state_versions.Create", err, version.ID, version.AgentRunID, version.Version)
	}
	return nil
}

func (r *agentStateVersionRepository) GetByRunAndVersion(ctx context.Context, agentRunID string, version int) (domain.AgentStateVersion, error) {
	const query = `SELECT ` + agentStateVersionColumns + `
		  FROM agent_state_versions WHERE agent_run_id = $1 AND version = $2`

	rows, err := r.conn.Query(ctx, query, agentRunID, version)
	if err != nil {
		return domain.AgentStateVersion{}, mapError("agent_state_versions.GetByRunAndVersion", err, agentRunID, version)
	}
	versions, err := scanAgentStateVersions(rows)
	if err != nil {
		return domain.AgentStateVersion{}, mapError("agent_state_versions.GetByRunAndVersion", err, agentRunID, version)
	}
	if len(versions) == 0 {
		return domain.AgentStateVersion{}, mapError("agent_state_versions.GetByRunAndVersion", pgx.ErrNoRows, agentRunID, version)
	}
	return versions[0], nil
}

func scanAgentStateVersions(rows pgx.Rows) ([]domain.AgentStateVersion, error) {
	defer rows.Close()

	out := []domain.AgentStateVersion{}
	for rows.Next() {
		var (
			version domain.AgentStateVersion
			value   []byte
		)
		if err := rows.Scan(
			&version.ID, &version.AgentRunID, &version.Version, &version.SourceTurnID,
			&value, &version.CreatedAt,
		); err != nil {
			return nil, err
		}
		version.Value = json.RawMessage(value)
		out = append(out, version)
	}
	return out, rows.Err()
}

var (
	_ store.AgentContextVersionRepository = (*agentContextVersionRepository)(nil)
	_ store.AgentStateVersionRepository   = (*agentStateVersionRepository)(nil)
)
