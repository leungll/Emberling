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

// This file owns the execution facts of Agent Tool calls: the fact a successful Tool result
// establishes under its Tool's registered production declaration, and the claim-time
// checks of a Tool's declared fact requirements and of the Agent Run's generation limit.

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

// requirementCandidateLimit bounds the facts one requirement check reads. Only the newest
// fact about the subject decides a requirement, so a handful is ample; the bound keeps the
// claim transaction's read independent of how many facts the Run has accumulated.
const requirementCandidateLimit = 8

// unmetFactRequirementLocked evaluates every fact requirement the Tool's current
// registration declares against the facts already committed in the Run. It runs inside
// the claim transaction, under the Run aggregate lock the caller holds, after the call
// passed the allowlist and Input Schema checks and before any Tool Attempt exists, so a
// fact committed by a concurrent result transaction is either fully visible or not yet
// there, and an unmet requirement is decided before anything external can happen.
//
// Requirements are evaluated in declaration order and the first unmet one is returned as
// the PRECONDITION_UNMET ExecutionError the caller fails the Action with. Its details name
// the fact type, the subject reference, the reason and the binding involved; they never
// carry other argument values, which may hold signed URLs. Only persisted facts are
// consulted: whether the Tool that produced them is still registered does not matter.
// A Tool without requirements reads nothing.
func (s *ExecutionService) unmetFactRequirementLocked(
	ctx context.Context,
	tx store.Tx,
	runID string,
	requirements []domain.FactRequirement,
	arguments json.RawMessage,
) (*domain.ExecutionError, error) {
	for _, requirement := range requirements {
		// The matcher reads the subject and every compared argument before it looks at a
		// single candidate. Asked with no candidates, it therefore reports either the
		// argument problem or NO_FACT naming the subject it read; the store query takes
		// the subject from that answer, so the arguments are interpreted in one place.
		_, probe := runtime.MatchRequirement(requirement, arguments, nil)
		if probe == nil {
			return nil, fmt.Errorf("execution: requirement %q of run %s matched without any committed fact", requirement.FactType, runID)
		}
		if probe.Reason != runtime.RequirementNoFact {
			unmet := probe.ExecutionError()
			return &unmet, nil
		}
		subject := probe.Subject

		candidates, err := tx.ExecutionFacts().ListForRequirement(ctx, runID, requirement.FactType, &subject, requirementCandidateLimit)
		if err != nil {
			return nil, fmt.Errorf("execution: list facts %q about %q of run %s: %w", requirement.FactType, subject, runID, err)
		}
		if _, failure := runtime.MatchRequirement(requirement, arguments, candidates); failure != nil {
			unmet := failure.ExecutionError()
			return &unmet, nil
		}
	}
	return nil, nil
}

// generationLimitReachedLocked decides whether one more call of a counted Tool would pass
// the Agent Run's frozen generation limit. It runs inside the claim transaction, under the
// Run aggregate lock the caller holds, after the fact requirements held and before any
// Tool Attempt exists, so concurrent claims of the same Agent Run are serialized by that
// lock and the count they read already includes every Attempt a previous claim committed.
//
// Nothing is checked when the Agent Run has no limit or the Tool's current registration
// does not count toward it. Otherwise the calls already started are derived from the
// persisted Tool Attempts of every Tool the current Registry marks as counting, whatever
// their status: a call that reached the Provider and failed, or whose outcome is
// uncertain, may still have generated something and keeps its share of the budget. There
// is no counter column to drift from those Attempts.
//
// A reached limit is returned as the GENERATION_LIMIT_REACHED ExecutionError the caller
// fails the Action with; its details carry only the limit and the calls used.
func (s *ExecutionService) generationLimitReachedLocked(
	ctx context.Context,
	tx store.Tx,
	agentRun domain.AgentRun,
	tool domain.ToolMetadata,
) (*domain.ExecutionError, error) {
	if agentRun.MaxGenerationCalls == nil || !tool.CountsTowardGenerationLimit {
		return nil, nil
	}

	var counted []string
	for _, metadata := range s.deps.Tools.ListMetadata() {
		if metadata.CountsTowardGenerationLimit {
			counted = append(counted, metadata.Name)
		}
	}
	used, err := tx.ToolAttempts().CountByAgentRunForTools(ctx, agentRun.ID, counted)
	if err != nil {
		return nil, fmt.Errorf("execution: count generation calls of agent run %s: %w", agentRun.ID, err)
	}
	if !runtime.GenerationLimitReached(agentRun.MaxGenerationCalls, used) {
		return nil, nil
	}

	limit := *agentRun.MaxGenerationCalls
	details, err := json.Marshal(map[string]int{"limit": limit, "used": used})
	if err != nil {
		return nil, fmt.Errorf("execution: encode generation limit details of agent run %s: %w", agentRun.ID, err)
	}
	return &domain.ExecutionError{
		Code:    runtime.CodeGenerationLimitReached,
		Message: fmt.Sprintf("tool %q would exceed the agent run's generation limit of %d calls (%d used)", tool.Name, limit, used),
		Details: details,
	}, nil
}
