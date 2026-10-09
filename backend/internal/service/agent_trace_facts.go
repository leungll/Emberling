package service

import (
	"context"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// maxTraceFacts bounds the execution facts one Agent Trace response carries. The ledger
// is a display projection: when an Agent Run holds more facts, the Trace shows the oldest
// maxTraceFacts and reports that it was cut, while every stored fact stays untouched.
const maxTraceFacts = 256

// AgentTraceFacts is the execution fact ledger of one Agent Run, oldest first (created_at
// then id). Truncated reports that the Agent Run holds more facts than the ledger shows.
type AgentTraceFacts struct {
	Facts     []domain.ExecutionFact
	Truncated bool
}

// AgentGenerationBudget is the generation limit frozen on the Agent Run, nil when it has
// none, next to the calls already made against it. Used is derived the same way the
// claim check derives it: every persisted Tool Attempt of the Agent Run, whatever its
// status, of a Tool the current Registry marks as counting toward the limit. There is no
// counter column, so the Trace cannot disagree with the check that enforces the limit.
type AgentGenerationBudget struct {
	MaxGenerationCalls *int
	Used               int
}

// agentTraceFacts reads the bounded fact ledger of agentRunID inside the caller's read
// transaction. It asks for one fact more than it shows, so a cut ledger is reported
// rather than silently presented as complete.
func agentTraceFacts(ctx context.Context, tx store.Tx, agentRunID string) (AgentTraceFacts, error) {
	facts, err := tx.ExecutionFacts().ListByAgentRun(ctx, agentRunID, maxTraceFacts+1)
	if err != nil {
		return AgentTraceFacts{}, err
	}
	if len(facts) > maxTraceFacts {
		return AgentTraceFacts{Facts: facts[:maxTraceFacts], Truncated: true}, nil
	}
	return AgentTraceFacts{Facts: facts}, nil
}

// agentGenerationBudget derives the generation budget of agentRun inside the caller's
// read transaction. Without a Registry, or when no registered Tool counts toward the
// limit, no call has been made against it and nothing is read.
func (s *QueryService) agentGenerationBudget(ctx context.Context, tx store.Tx, agentRun domain.AgentRun) (AgentGenerationBudget, error) {
	budget := AgentGenerationBudget{MaxGenerationCalls: agentRun.MaxGenerationCalls}
	if s.deps.Tools == nil {
		return budget, nil
	}
	var counted []string
	for _, metadata := range s.deps.Tools.ListMetadata() {
		if metadata.CountsTowardGenerationLimit {
			counted = append(counted, metadata.Name)
		}
	}
	if len(counted) == 0 {
		return budget, nil
	}
	used, err := tx.ToolAttempts().CountByAgentRunForTools(ctx, agentRun.ID, counted)
	if err != nil {
		return AgentGenerationBudget{}, err
	}
	budget.Used = used
	return budget, nil
}
