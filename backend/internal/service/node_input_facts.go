package service

import (
	"context"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// maxNodeInputFactsPerType bounds how many facts of one type a node receives. A Run's
// facts are bounded only indirectly (one fact per type per Tool Attempt, Tool Attempts per
// Agent by maxTurns), so the read is capped here and an overflow is reported to the node
// instead of being dropped silently.
const maxNodeInputFactsPerType = 256

// nodeInputFacts reads, for a Node Type whose registered Metadata declares FactInputs, this
// Run's committed execution facts of each declared type. It runs inside the transaction that
// starts the Attempt, under the Run lock, and only reads: the node then checks a set of
// facts that was committed before its Attempt started, and never queries facts itself. A
// Node Type without FactInputs, or one no longer registered, receives nothing.
func (s *ExecutionService) nodeInputFacts(ctx context.Context, tx store.Tx, runID, nodeType string) (map[string]registry.FactSet, error) {
	metadata, ok := s.deps.Nodes.NodeMetadata(nodeType)
	if !ok || len(metadata.FactInputs) == 0 {
		return nil, nil
	}
	facts := make(map[string]registry.FactSet, len(metadata.FactInputs))
	for _, factType := range metadata.FactInputs {
		// One row beyond the bound tells an exactly full set apart from an overflowing one.
		rows, err := tx.ExecutionFacts().ListByRunAndType(ctx, runID, factType, maxNodeInputFactsPerType+1)
		if err != nil {
			return nil, fmt.Errorf("execution: read %q facts of run %s for node type %q: %w", factType, runID, nodeType, err)
		}
		facts[factType] = boundedFactSet(rows, maxNodeInputFactsPerType)
	}
	return facts, nil
}

// boundedFactSet keeps the oldest limit facts of rows, which arrive oldest first, and
// marks the set truncated when rows held more.
func boundedFactSet(rows []domain.ExecutionFact, limit int) registry.FactSet {
	if len(rows) > limit {
		return registry.FactSet{Facts: rows[:limit], Truncated: true}
	}
	if rows == nil {
		rows = []domain.ExecutionFact{}
	}
	return registry.FactSet{Facts: rows}
}
