package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// changeRequestRepository persists change requests. The table is append-only: a request
// binds a patch to the verification it passed at one moment, so there is nothing to update.
type changeRequestRepository struct {
	conn pgx.Tx
}

const changeRequestColumns = `id, agent_run_id, patch_digest, verification_fact_id, content_artifact_id,
	content_digest, decision_basis, tool_attempt_id, created_at`

// CreateOrGet inserts the request unless the Agent Run already holds one with the same
// content digest. The insert and the read happen in the caller's transaction under the Run
// aggregate lock, so the row the read returns is the one the conflicting insert met.
func (r *changeRequestRepository) CreateOrGet(ctx context.Context, request domain.ChangeRequest) (domain.ChangeRequest, bool, error) {
	basis := []byte(request.DecisionBasis)
	if len(basis) == 0 {
		basis = []byte(`[]`)
	}

	const insert = `
		INSERT INTO change_requests (` + changeRequestColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT ON CONSTRAINT change_requests_agent_run_content_key DO NOTHING
		RETURNING ` + changeRequestColumns

	rows, err := r.conn.Query(ctx, insert,
		request.ID, request.AgentRunID, request.PatchDigest, request.VerificationFactID, request.ContentArtifactID,
		request.ContentDigest, basis, request.ToolAttemptID, request.CreatedAt,
	)
	if err != nil {
		return domain.ChangeRequest{}, false, mapError("change_requests.CreateOrGet", err, request.ID, request.AgentRunID)
	}
	inserted, err := scanChangeRequests(rows)
	if err != nil {
		return domain.ChangeRequest{}, false, mapError("change_requests.CreateOrGet", err, request.ID, request.AgentRunID)
	}
	if len(inserted) == 1 {
		return inserted[0], true, nil
	}

	const existing = `SELECT ` + changeRequestColumns + `
		  FROM change_requests
		 WHERE agent_run_id = $1 AND content_digest = $2`
	rows, err = r.conn.Query(ctx, existing, request.AgentRunID, request.ContentDigest)
	if err != nil {
		return domain.ChangeRequest{}, false, mapError("change_requests.CreateOrGet", err, request.AgentRunID, request.ContentDigest)
	}
	found, err := scanChangeRequests(rows)
	if err != nil {
		return domain.ChangeRequest{}, false, mapError("change_requests.CreateOrGet", err, request.AgentRunID, request.ContentDigest)
	}
	if len(found) != 1 {
		return domain.ChangeRequest{}, false, fmt.Errorf("store/postgres change_requests.CreateOrGet: agent_run=%s content=%s: insert was skipped but %d existing rows were found, want 1",
			request.AgentRunID, request.ContentDigest, len(found))
	}
	return found[0], false, nil
}

func (r *changeRequestRepository) Get(ctx context.Context, id string) (domain.ChangeRequest, error) {
	const query = `SELECT ` + changeRequestColumns + ` FROM change_requests WHERE id = $1`

	rows, err := r.conn.Query(ctx, query, id)
	if err != nil {
		return domain.ChangeRequest{}, mapError("change_requests.Get", err, id)
	}
	requests, err := scanChangeRequests(rows)
	if err != nil {
		return domain.ChangeRequest{}, mapError("change_requests.Get", err, id)
	}
	if len(requests) == 0 {
		return domain.ChangeRequest{}, mapError("change_requests.Get", pgx.ErrNoRows, id)
	}
	return requests[0], nil
}

func (r *changeRequestRepository) ListByAgentRun(ctx context.Context, agentRunID string, limit int) ([]domain.ChangeRequest, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres change_requests.ListByAgentRun: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + changeRequestColumns + `
		  FROM change_requests
		 WHERE agent_run_id = $1
		 ORDER BY created_at ASC, id ASC
		 LIMIT $2`

	rows, err := r.conn.Query(ctx, query, agentRunID, limit)
	if err != nil {
		return nil, mapError("change_requests.ListByAgentRun", err, agentRunID)
	}
	requests, err := scanChangeRequests(rows)
	if err != nil {
		return nil, mapError("change_requests.ListByAgentRun", err, agentRunID)
	}
	return requests, nil
}

func scanChangeRequests(rows pgx.Rows) ([]domain.ChangeRequest, error) {
	defer rows.Close()

	out := []domain.ChangeRequest{}
	for rows.Next() {
		var (
			request domain.ChangeRequest
			basis   []byte
		)
		if err := rows.Scan(
			&request.ID, &request.AgentRunID, &request.PatchDigest, &request.VerificationFactID, &request.ContentArtifactID,
			&request.ContentDigest, &basis, &request.ToolAttemptID, &request.CreatedAt,
		); err != nil {
			return nil, err
		}
		request.DecisionBasis = basis
		out = append(out, request)
	}
	return out, rows.Err()
}

var _ store.ChangeRequestRepository = (*changeRequestRepository)(nil)
