package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type agentWorkspaceRepository struct {
	conn pgx.Tx
}

const agentWorkspaceColumns = `agent_run_id, repository_id, base_commit, allowed_paths, deploy_target, created_at`

// Create inserts the frozen workspace in the caller's Agent Run initialisation
// transaction. The primary key on agent_run_id is what makes a second binding lose.
func (r *agentWorkspaceRepository) Create(ctx context.Context, workspace domain.AgentWorkspace) error {
	allowedPaths, err := json.Marshal(workspace.AllowedPaths)
	if err != nil {
		return fmt.Errorf("store/postgres agent_workspaces.Create: agent_run=%s: marshal allowed paths: %w", workspace.AgentRunID, err)
	}
	var deployTarget any
	if workspace.DeployTarget != nil {
		encoded, err := json.Marshal(workspace.DeployTarget)
		if err != nil {
			return fmt.Errorf("store/postgres agent_workspaces.Create: agent_run=%s: marshal deploy target: %w", workspace.AgentRunID, err)
		}
		deployTarget = encoded
	}

	const insert = `
		INSERT INTO agent_workspaces (` + agentWorkspaceColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := r.conn.Exec(ctx, insert,
		workspace.AgentRunID, workspace.RepositoryID, workspace.BaseCommit, allowedPaths, deployTarget, workspace.CreatedAt,
	); err != nil {
		return mapError("agent_workspaces.Create", err, workspace.AgentRunID)
	}
	return nil
}

func (r *agentWorkspaceRepository) GetByAgentRunID(ctx context.Context, agentRunID string) (domain.AgentWorkspace, error) {
	const query = `SELECT ` + agentWorkspaceColumns + ` FROM agent_workspaces WHERE agent_run_id = $1`

	var (
		workspace    domain.AgentWorkspace
		allowedPaths []byte
		deployTarget []byte
	)
	if err := r.conn.QueryRow(ctx, query, agentRunID).Scan(
		&workspace.AgentRunID, &workspace.RepositoryID, &workspace.BaseCommit, &allowedPaths, &deployTarget, &workspace.CreatedAt,
	); err != nil {
		return domain.AgentWorkspace{}, mapError("agent_workspaces.GetByAgentRunID", err, agentRunID)
	}
	if err := json.Unmarshal(allowedPaths, &workspace.AllowedPaths); err != nil {
		return domain.AgentWorkspace{}, fmt.Errorf("store/postgres agent_workspaces.GetByAgentRunID: agent_run=%s: unmarshal allowed paths: %w", agentRunID, err)
	}
	if len(deployTarget) > 0 {
		var target domain.WorkspaceDeployTarget
		if err := json.Unmarshal(deployTarget, &target); err != nil {
			return domain.AgentWorkspace{}, fmt.Errorf("store/postgres agent_workspaces.GetByAgentRunID: agent_run=%s: unmarshal deploy target: %w", agentRunID, err)
		}
		workspace.DeployTarget = &target
	}
	return workspace, nil
}

var _ store.AgentWorkspaceRepository = (*agentWorkspaceRepository)(nil)
