package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// agentCompletionRepository persists completion verdicts. The table is write-once: a verdict
// commits with the Agent Run's termination, and the primary key refuses a second one.
type agentCompletionRepository struct {
	conn pgx.Tx
}

const agentCompletionColumns = `agent_run_id, goal, outcome, checks, missing, evaluated_at`

func (r *agentCompletionRepository) Create(ctx context.Context, completion domain.AgentCompletion) error {
	if !completion.Goal.IsValid() {
		return fmt.Errorf("store/postgres agent_completions.Create: agent_run=%s: unknown goal %q", completion.AgentRunID, completion.Goal)
	}
	if !completion.Outcome.IsValid() {
		return fmt.Errorf("store/postgres agent_completions.Create: agent_run=%s: unknown outcome %q", completion.AgentRunID, completion.Outcome)
	}
	checks, err := encodeJSONArray(completion.Checks)
	if err != nil {
		return fmt.Errorf("store/postgres agent_completions.Create: agent_run=%s: encode checks: %w", completion.AgentRunID, err)
	}
	missing, err := encodeJSONArray(completion.Missing)
	if err != nil {
		return fmt.Errorf("store/postgres agent_completions.Create: agent_run=%s: encode missing: %w", completion.AgentRunID, err)
	}

	const insert = `
		INSERT INTO agent_completions (` + agentCompletionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := r.conn.Exec(ctx, insert,
		completion.AgentRunID, string(completion.Goal), string(completion.Outcome), checks, missing, completion.EvaluatedAt,
	); err != nil {
		return mapError("agent_completions.Create", err, completion.AgentRunID)
	}
	return nil
}

func (r *agentCompletionRepository) Get(ctx context.Context, agentRunID string) (domain.AgentCompletion, error) {
	const query = `SELECT ` + agentCompletionColumns + ` FROM agent_completions WHERE agent_run_id = $1`

	var (
		completion domain.AgentCompletion
		goal       string
		outcome    string
		checks     []byte
		missing    []byte
	)
	if err := r.conn.QueryRow(ctx, query, agentRunID).Scan(
		&completion.AgentRunID, &goal, &outcome, &checks, &missing, &completion.EvaluatedAt,
	); err != nil {
		return domain.AgentCompletion{}, mapError("agent_completions.Get", err, agentRunID)
	}
	completion.Goal = domain.AgentGoal(goal)
	completion.Outcome = domain.CompletionOutcome(outcome)
	if err := json.Unmarshal(checks, &completion.Checks); err != nil {
		return domain.AgentCompletion{}, fmt.Errorf("store/postgres agent_completions.Get: agent_run=%s: decode checks: %w", agentRunID, err)
	}
	if err := json.Unmarshal(missing, &completion.Missing); err != nil {
		return domain.AgentCompletion{}, fmt.Errorf("store/postgres agent_completions.Get: agent_run=%s: decode missing: %w", agentRunID, err)
	}
	if completion.Checks == nil {
		completion.Checks = []domain.CompletionCheck{}
	}
	if completion.Missing == nil {
		completion.Missing = []domain.CompletionMissing{}
	}
	return completion, nil
}

// encodeJSONArray renders a slice as a JSON array, `[]` for nil, so the stored column
// always satisfies the array CHECK and a reader never sees the JSON literal null.
func encodeJSONArray[T any](items []T) ([]byte, error) {
	if items == nil {
		items = []T{}
	}
	return json.Marshal(items)
}

var _ store.AgentCompletionRepository = (*agentCompletionRepository)(nil)
