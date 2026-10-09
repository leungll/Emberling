package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type executionFactRepository struct {
	conn pgx.Tx
}

const executionFactColumns = `id, run_id, agent_run_id, tool_attempt_id, fact_type, subject_ref,
	binding, verdict, basis_fact_id, created_at`

// executionFactDuplicateConstraint is the UNIQUE (tool_attempt_id, fact_type)
// constraint. Only this violation means "the same Tool result committed a fact twice";
// other conflicts (a dangling reference, a duplicated id) keep the generic mapping.
const executionFactDuplicateConstraint = "execution_facts_tool_attempt_fact_type_key"

// Insert writes the fact in the caller's Tool result transaction and returns the stored
// row. An empty binding is stored as the empty object.
func (r *executionFactRepository) Insert(ctx context.Context, fact domain.ExecutionFact) (domain.ExecutionFact, error) {
	binding := []byte(fact.Binding)
	if len(binding) == 0 {
		binding = []byte(`{}`)
	}

	const insert = `
		INSERT INTO execution_facts (` + executionFactColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING ` + executionFactColumns

	rows, err := r.conn.Query(ctx, insert,
		fact.ID, fact.RunID, fact.AgentRunID, fact.ToolAttemptID, fact.FactType, fact.SubjectRef,
		binding, fact.Verdict, fact.BasisFactID, fact.CreatedAt,
	)
	if err != nil {
		return domain.ExecutionFact{}, mapExecutionFactInsertError(err, fact)
	}
	facts, err := scanExecutionFacts(rows)
	if err != nil {
		return domain.ExecutionFact{}, mapExecutionFactInsertError(err, fact)
	}
	if len(facts) != 1 {
		return domain.ExecutionFact{}, fmt.Errorf("store/postgres execution_facts.Insert: fact=%s returned %d rows, want 1",
			fact.ID, len(facts))
	}
	return facts[0], nil
}

func mapExecutionFactInsertError(err error, fact domain.ExecutionFact) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolation &&
		pgErr.ConstraintName == executionFactDuplicateConstraint {
		return fmt.Errorf("store/postgres execution_facts.Insert [%s %s %s]: %w",
			fact.ID, fact.ToolAttemptID, fact.FactType, domain.ErrExecutionFactDuplicate)
	}
	return mapError("execution_facts.Insert", err, fact.ID, fact.ToolAttemptID, fact.FactType)
}

func (r *executionFactRepository) FindLatest(ctx context.Context, runID, factType, subjectRef string) (*domain.ExecutionFact, error) {
	const query = `SELECT ` + executionFactColumns + `
		  FROM execution_facts
		 WHERE run_id = $1 AND fact_type = $2 AND subject_ref = $3
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`

	rows, err := r.conn.Query(ctx, query, runID, factType, subjectRef)
	if err != nil {
		return nil, mapError("execution_facts.FindLatest", err, runID, factType)
	}
	facts, err := scanExecutionFacts(rows)
	if err != nil {
		return nil, mapError("execution_facts.FindLatest", err, runID, factType)
	}
	if len(facts) == 0 {
		return nil, nil
	}
	return &facts[0], nil
}

func (r *executionFactRepository) ListForRequirement(ctx context.Context, runID, factType string, subjectRef *string, limit int) ([]domain.ExecutionFact, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres execution_facts.ListForRequirement: limit must be positive, got %d", limit)
	}

	// A NULL subject parameter disables the subject filter.
	const query = `SELECT ` + executionFactColumns + `
		  FROM execution_facts
		 WHERE run_id = $1 AND fact_type = $2
		   AND ($3::text IS NULL OR subject_ref = $3::text)
		 ORDER BY created_at DESC, id DESC
		 LIMIT $4`

	rows, err := r.conn.Query(ctx, query, runID, factType, subjectRef, limit)
	if err != nil {
		return nil, mapError("execution_facts.ListForRequirement", err, runID, factType)
	}
	facts, err := scanExecutionFacts(rows)
	if err != nil {
		return nil, mapError("execution_facts.ListForRequirement", err, runID, factType)
	}
	return facts, nil
}

func (r *executionFactRepository) ListByAgentRun(ctx context.Context, agentRunID string, limit int) ([]domain.ExecutionFact, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres execution_facts.ListByAgentRun: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + executionFactColumns + `
		  FROM execution_facts
		 WHERE agent_run_id = $1
		 ORDER BY created_at ASC, id ASC
		 LIMIT $2`

	rows, err := r.conn.Query(ctx, query, agentRunID, limit)
	if err != nil {
		return nil, mapError("execution_facts.ListByAgentRun", err, agentRunID)
	}
	facts, err := scanExecutionFacts(rows)
	if err != nil {
		return nil, mapError("execution_facts.ListByAgentRun", err, agentRunID)
	}
	return facts, nil
}

func (r *executionFactRepository) ListByRunAndType(ctx context.Context, runID, factType string, limit int) ([]domain.ExecutionFact, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres execution_facts.ListByRunAndType: limit must be positive, got %d", limit)
	}

	const query = `SELECT ` + executionFactColumns + `
		  FROM execution_facts
		 WHERE run_id = $1 AND fact_type = $2
		 ORDER BY created_at ASC, id ASC
		 LIMIT $3`

	rows, err := r.conn.Query(ctx, query, runID, factType, limit)
	if err != nil {
		return nil, mapError("execution_facts.ListByRunAndType", err, runID, factType)
	}
	facts, err := scanExecutionFacts(rows)
	if err != nil {
		return nil, mapError("execution_facts.ListByRunAndType", err, runID, factType)
	}
	return facts, nil
}

func scanExecutionFacts(rows pgx.Rows) ([]domain.ExecutionFact, error) {
	defer rows.Close()

	out := []domain.ExecutionFact{}
	for rows.Next() {
		var fact domain.ExecutionFact
		if err := rows.Scan(
			&fact.ID, &fact.RunID, &fact.AgentRunID, &fact.ToolAttemptID, &fact.FactType, &fact.SubjectRef,
			&fact.Binding, &fact.Verdict, &fact.BasisFactID, &fact.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, fact)
	}
	return out, rows.Err()
}

var _ store.ExecutionFactRepository = (*executionFactRepository)(nil)
