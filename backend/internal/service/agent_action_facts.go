package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the execution fact a successful Tool result establishes under its Tool's
// registered production declaration.

// recordProducedFactLocked writes the execution fact of one successful Tool result inside
// the Tool result transaction, under the Run aggregate lock the caller holds, so the fact
// commits or rolls back together with the Tool result, the Action's completion and
// AGENT_ACTION_COMPLETED. A Tool without a production declaration writes nothing.
//
// The fact is read from the committed Decision's arguments and the normalized result.
// When the declaration names a basis fact type, the basis is the newest fact of that type
// about the same subject in the Run.
//
// A non-nil ExecutionError means the result cannot establish the declared fact: it does
// not carry what the declaration promises, or the basis fact is absent. Nothing has been
// written then, and the caller fails the Action through the shared Action failure tail
// instead of committing the round. A returned error rolls the whole transaction back; that
// includes a second fact of the same type for the same Tool Attempt, which breaks the
// single-write rule of facts and is never ignored.
//
// Neither the ExecutionError nor the error echoes argument or result values, which may
// carry signed URLs; they name only the declaration and the subject reference.
func (s *ExecutionService) recordProducedFactLocked(
	ctx context.Context,
	tx store.Tx,
	call agentToolCall,
	arguments json.RawMessage,
	result json.RawMessage,
	now time.Time,
) (*domain.ExecutionError, error) {
	reg, registered := s.deps.Tools.Get(call.toolName)
	if !registered {
		// The registration resolved when the Attempt was claimed or resumed; a Registry
		// that lost it since cannot say which fact the result owes, and none is guessed.
		return nil, fmt.Errorf("execution: tool %q of tool attempt %s is not registered when its result commits", call.toolName, call.attemptID)
	}
	production := reg.Metadata.Produces
	if production == nil {
		return nil, nil
	}

	produced, err := runtime.ExtractProducedFact(*production, arguments, result)
	if err != nil {
		var extractErr *runtime.FactExtractionError
		if !errors.As(err, &extractErr) {
			return nil, fmt.Errorf("execution: extract fact of tool attempt %s: %w", call.attemptID, err)
		}
		details, err := json.Marshal(map[string]string{
			"factType": extractErr.FactType,
			"field":    extractErr.Field,
			"source":   string(extractErr.Source),
			"pointer":  extractErr.Pointer,
			"reason":   string(extractErr.Reason),
		})
		if err != nil {
			return nil, fmt.Errorf("execution: encode fact extraction details of tool attempt %s: %w", call.attemptID, err)
		}
		return &domain.ExecutionError{
			Code:    runtime.CodeFactExtractionFailed,
			Message: fmt.Sprintf("tool %q result does not honour its declared fact: %s", call.toolName, extractErr.Error()),
			Details: details,
		}, nil
	}

	var basisFactID *string
	if produced.BasisFactType != "" {
		basis, err := tx.ExecutionFacts().FindLatest(ctx, call.runID, produced.BasisFactType, produced.SubjectRef)
		if err != nil {
			return nil, err
		}
		if basis == nil {
			details, err := json.Marshal(map[string]string{
				"factType":      produced.FactType,
				"basisFactType": produced.BasisFactType,
				"subject":       produced.SubjectRef,
			})
			if err != nil {
				return nil, fmt.Errorf("execution: encode basis details of tool attempt %s: %w", call.attemptID, err)
			}
			return &domain.ExecutionError{
				Code: runtime.CodeFactBasisMissing,
				Message: fmt.Sprintf("fact %q about subject %q has no basis fact %q in the run",
					produced.FactType, produced.SubjectRef, produced.BasisFactType),
				Details: details,
			}, nil
		}
		basisFactID = &basis.ID
	}

	binding, err := produced.BindingJSON()
	if err != nil {
		return nil, fmt.Errorf("execution: tool attempt %s: %w", call.attemptID, err)
	}
	if _, err := tx.ExecutionFacts().Insert(ctx, domain.ExecutionFact{
		ID:            s.deps.IDs.NewID(domain.IDPrefixExecutionFact),
		RunID:         call.runID,
		AgentRunID:    call.agentRunID,
		ToolAttemptID: call.attemptID,
		FactType:      produced.FactType,
		SubjectRef:    produced.SubjectRef,
		Binding:       binding,
		Verdict:       produced.Verdict,
		BasisFactID:   basisFactID,
		CreatedAt:     now,
	}); err != nil {
		return nil, fmt.Errorf("execution: record fact %q of tool attempt %s: %w", produced.FactType, call.attemptID, err)
	}
	return nil, nil
}
