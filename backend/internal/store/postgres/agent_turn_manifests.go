package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// agentTurnManifestRepository persists Context Manifests. The table is append-only: a
// Manifest describes the one model request its Turn issued, so there is nothing to update.
type agentTurnManifestRepository struct {
	conn pgx.Tx
}

const agentTurnManifestColumns = `turn_id, agent_run_id, items, created_at`

// Create inserts one Turn's Manifest. The PRIMARY KEY on turn_id turns a second insert for
// the same Turn into domain.ErrConflict.
func (r *agentTurnManifestRepository) Create(ctx context.Context, manifest domain.AgentTurnManifest) error {
	items := manifest.Items
	if items == nil {
		items = []domain.ManifestItem{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("store/postgres agent_turn_manifests.Create: turn=%s: %w", manifest.TurnID, err)
	}

	const insert = `
		INSERT INTO agent_turn_manifests (` + agentTurnManifestColumns + `)
		VALUES ($1, $2, $3, $4)`
	if _, err := r.conn.Exec(ctx, insert,
		manifest.TurnID, manifest.AgentRunID, encoded, manifest.CreatedAt,
	); err != nil {
		return mapError("agent_turn_manifests.Create", err, manifest.TurnID, manifest.AgentRunID)
	}
	return nil
}

func (r *agentTurnManifestRepository) GetByTurnID(ctx context.Context, turnID string) (domain.AgentTurnManifest, error) {
	const query = `SELECT ` + agentTurnManifestColumns + ` FROM agent_turn_manifests WHERE turn_id = $1`

	rows, err := r.conn.Query(ctx, query, turnID)
	if err != nil {
		return domain.AgentTurnManifest{}, mapError("agent_turn_manifests.GetByTurnID", err, turnID)
	}
	manifests, err := scanAgentTurnManifests(rows)
	if err != nil {
		return domain.AgentTurnManifest{}, mapError("agent_turn_manifests.GetByTurnID", err, turnID)
	}
	if len(manifests) == 0 {
		return domain.AgentTurnManifest{}, mapError("agent_turn_manifests.GetByTurnID", pgx.ErrNoRows, turnID)
	}
	return manifests[0], nil
}

// ListByAgentRunID returns one Agent Run's Manifests in Turn order. The join supplies the
// order: a Manifest row carries no turn_no of its own, and turn_no is the persisted order
// of the Agent Loop.
func (r *agentTurnManifestRepository) ListByAgentRunID(ctx context.Context, agentRunID string) ([]domain.AgentTurnManifest, error) {
	const query = `
		SELECT m.turn_id, m.agent_run_id, m.items, m.created_at
		  FROM agent_turn_manifests m
		  JOIN agent_turns t ON t.id = m.turn_id
		 WHERE m.agent_run_id = $1
		 ORDER BY t.turn_no ASC`

	rows, err := r.conn.Query(ctx, query, agentRunID)
	if err != nil {
		return nil, mapError("agent_turn_manifests.ListByAgentRunID", err, agentRunID)
	}
	manifests, err := scanAgentTurnManifests(rows)
	if err != nil {
		return nil, mapError("agent_turn_manifests.ListByAgentRunID", err, agentRunID)
	}
	return manifests, nil
}

func scanAgentTurnManifests(rows pgx.Rows) ([]domain.AgentTurnManifest, error) {
	defer rows.Close()

	out := []domain.AgentTurnManifest{}
	for rows.Next() {
		var (
			manifest domain.AgentTurnManifest
			items    []byte
		)
		if err := rows.Scan(&manifest.TurnID, &manifest.AgentRunID, &items, &manifest.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(items, &manifest.Items); err != nil {
			return nil, fmt.Errorf("turn=%s: decode manifest items: %w", manifest.TurnID, err)
		}
		if manifest.Items == nil {
			manifest.Items = []domain.ManifestItem{}
		}
		out = append(out, manifest)
	}
	return out, rows.Err()
}

var _ store.AgentTurnManifestRepository = (*agentTurnManifestRepository)(nil)
