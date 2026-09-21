package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type agentDecisionRepository struct {
	conn pgx.Tx
}

const agentDecisionColumns = `id, turn_id, kind, tool_name, arguments, output, state_patch,
	response_summary, created_at`

// Create inserts the immutable Decision. It is committed together with the Turn's
// COMPLETED status and the single READY Action, so a model decision never exists without
// the work item that executes it. UNIQUE (turn_id) allows at most one Decision per Turn,
// and the table CHECK rejects a TOOL_CALL without a Tool or a FINAL without an output.
func (r *agentDecisionRepository) Create(ctx context.Context, decision domain.AgentDecision) error {
	if err := decision.Validate(); err != nil {
		return fmt.Errorf("store/postgres agent_decisions.Create: %w", err)
	}

	const insert = `
		INSERT INTO agent_decisions (` + agentDecisionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	if _, err := r.conn.Exec(ctx, insert,
		decision.ID, decision.TurnID, string(decision.Kind), decision.ToolName,
		nullableJSON(decision.Arguments), nullableJSON(decision.Output),
		nullableJSON(decision.StatePatch), nullableJSON(decision.ResponseSummary),
		decision.CreatedAt,
	); err != nil {
		return mapError("agent_decisions.Create", err, decision.ID, decision.TurnID)
	}
	return nil
}

func (r *agentDecisionRepository) Get(ctx context.Context, decisionID string) (domain.AgentDecision, error) {
	const query = `SELECT ` + agentDecisionColumns + ` FROM agent_decisions WHERE id = $1`

	rows, err := r.conn.Query(ctx, query, decisionID)
	if err != nil {
		return domain.AgentDecision{}, mapError("agent_decisions.Get", err, decisionID)
	}
	decisions, err := scanAgentDecisions(rows)
	if err != nil {
		return domain.AgentDecision{}, mapError("agent_decisions.Get", err, decisionID)
	}
	if len(decisions) == 0 {
		return domain.AgentDecision{}, mapError("agent_decisions.Get", pgx.ErrNoRows, decisionID)
	}
	return decisions[0], nil
}

func scanAgentDecisions(rows pgx.Rows) ([]domain.AgentDecision, error) {
	defer rows.Close()

	out := []domain.AgentDecision{}
	for rows.Next() {
		var (
			decision  domain.AgentDecision
			kind      string
			arguments []byte
			output    []byte
			patch     []byte
			summary   []byte
		)
		if err := rows.Scan(
			&decision.ID, &decision.TurnID, &kind, &decision.ToolName,
			&arguments, &output, &patch, &summary, &decision.CreatedAt,
		); err != nil {
			return nil, err
		}

		decision.Kind = domain.DecisionKind(kind)
		decision.Arguments = json.RawMessage(arguments)
		decision.Output = json.RawMessage(output)
		decision.StatePatch = json.RawMessage(patch)
		decision.ResponseSummary = json.RawMessage(summary)
		out = append(out, decision)
	}
	return out, rows.Err()
}

var _ store.AgentDecisionRepository = (*agentDecisionRepository)(nil)
