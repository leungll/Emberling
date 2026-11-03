package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the goal-based completion check: the step of an Agent Run's terminating
// transaction that reads the facts the Run recorded, decides the task result against the
// frozen goal, writes the verdict and appends AGENT_COMPLETION_EVALUATED. The decision
// itself is runtime.EvaluateCompletion; this file only gathers its input and persists its
// output, in the same transaction as the termination it belongs to. An Agent Run without a
// frozen goal is never evaluated: nothing is read, written or appended for it.

// maxCompletionRecords bounds every list the completion check reads: facts, change
// requests and approvals of one Agent Run. A Run that recorded more than this has long
// exceeded any realistic repair task, and the bound keeps the terminating transaction's
// reads finite.
const maxCompletionRecords = 256

// agentCompletionEvaluatedPayload is AGENT_COMPLETION_EVALUATED: a bounded summary of the
// verdict. The checks and the missing descriptions live in the agent_completions row, which
// Trace projects; the Event carries the outcome, the missing codes and how many checks ran.
type agentCompletionEvaluatedPayload struct {
	AgentRunID string                   `json:"agentRunId"`
	Goal       domain.AgentGoal         `json:"goal"`
	Outcome    domain.CompletionOutcome `json:"outcome"`
	Missing    []string                 `json:"missing"`
	CheckCount int                      `json:"checkCount"`
}

// evaluateAgentCompletionLocked runs the completion check for one Agent Run inside the
// transaction that terminates it, under the Run aggregate lock the caller holds. It returns
// nil when the Agent Run froze no goal: such an Agent is not a goal-bound task, and its
// behaviour is exactly what it was before the check existed.
//
// Otherwise it reads the latest workspace patch, the Agent Run's facts, change requests and
// approvals, decides the verdict, inserts the agent_completions row and appends
// AGENT_COMPLETION_EVALUATED. The row and the Event commit with the termination; a failure
// of either rolls the whole termination back, so a terminated goal-bound Agent Run always
// has its verdict and a verdict never exists without its termination.
func (s *ExecutionService) evaluateAgentCompletionLocked(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	nodeRun domain.NodeRun,
	agentRun domain.AgentRun,
	termination domain.AgentTermination,
	now time.Time,
) (*runtime.CompletionResult, error) {
	if agentRun.Goal == nil {
		return nil, nil
	}
	input, err := s.completionInput(ctx, tx, agentRun, termination)
	if err != nil {
		return nil, fmt.Errorf("execution: completion check of agent run %s: %w", agentRun.ID, err)
	}
	result := runtime.EvaluateCompletion(input)

	if err := tx.AgentCompletions().Create(ctx, domain.AgentCompletion{
		AgentRunID:  agentRun.ID,
		Goal:        *agentRun.Goal,
		Outcome:     result.Outcome,
		Checks:      result.Checks,
		Missing:     result.Missing,
		EvaluatedAt: now,
	}); err != nil {
		return nil, fmt.Errorf("execution: record completion of agent run %s: %w", agentRun.ID, err)
	}
	if err := s.appendEvent(ctx, tx, lock, nodeRun.RunID, &nodeRun.ID, domain.EventAgentCompletionEvaluated, now, agentCompletionEvaluatedPayload{
		AgentRunID: agentRun.ID,
		Goal:       *agentRun.Goal,
		Outcome:    result.Outcome,
		Missing:    completionMissingCodes(result),
		CheckCount: len(result.Checks),
	}); err != nil {
		return nil, err
	}
	return &result, nil
}

// completionInput gathers everything the completion check reads about one Agent Run. The
// reads are bounded and happen inside the caller's transaction, so the verdict rests on
// exactly the facts that are committed when the Agent Run terminates.
func (s *ExecutionService) completionInput(ctx context.Context, tx store.Tx, agentRun domain.AgentRun, termination domain.AgentTermination) (runtime.CompletionInput, error) {
	input := runtime.CompletionInput{Goal: *agentRun.Goal, Termination: termination}

	latest, err := tx.WorkspacePatches().Latest(ctx, agentRun.ID)
	if err != nil {
		return runtime.CompletionInput{}, err
	}
	if latest != nil {
		input.LatestPatch = &runtime.CompletionPatch{ID: latest.ID, Seq: latest.Seq, Digest: latest.PatchDigest}
	}

	facts, err := tx.ExecutionFacts().ListByAgentRun(ctx, agentRun.ID, maxCompletionRecords)
	if err != nil {
		return runtime.CompletionInput{}, err
	}
	for _, fact := range facts {
		converted := runtime.CompletionFact{ID: fact.ID, Subject: fact.SubjectRef, Verdict: fact.Verdict, Binding: completionBinding(fact.Binding)}
		switch fact.FactType {
		case domain.FactTypePatchVerified:
			input.Verifications = append(input.Verifications, converted)
		case domain.FactTypePatchReviewed:
			input.Reviews = append(input.Reviews, converted)
		case domain.FactTypeReleaseConfirmed:
			input.Releases = append(input.Releases, converted)
		}
	}

	requests, err := tx.ChangeRequests().ListByAgentRun(ctx, agentRun.ID, maxCompletionRecords)
	if err != nil {
		return runtime.CompletionInput{}, err
	}
	for _, request := range requests {
		input.ChangeRequests = append(input.ChangeRequests, runtime.CompletionChangeRequest{
			ID: request.ID, PatchDigest: request.PatchDigest, VerificationFactID: request.VerificationFactID,
			ContentArtifactID: request.ContentArtifactID,
		})
	}

	approvals, err := tx.Approvals().ListByAgentRun(ctx, agentRun.ID, maxCompletionRecords)
	if err != nil {
		return runtime.CompletionInput{}, err
	}
	for _, approval := range approvals {
		input.Approvals = append(input.Approvals, runtime.CompletionApproval{ID: approval.ID, Status: approval.Status})
	}

	// Review is part of the change material only when the Agent could have the patch
	// reviewed: one of its allowed Tools declares, in its registered metadata, that it
	// produces the review fact. The Tool's name decides nothing.
	for _, name := range agentRun.AllowedTools {
		reg, ok := s.deps.Tools.Get(name)
		if ok && reg.Metadata.Produces != nil && reg.Metadata.Produces.FactType == domain.FactTypePatchReviewed {
			input.ReviewRequired = true
			break
		}
	}
	return input, nil
}

// completionBinding reads a fact's binding object as name to string. Bindings are recorded
// as canonical strings; a value of another JSON type is kept as its JSON text, so the check
// can still compare it and nothing is silently dropped.
func completionBinding(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	out := make(map[string]string, len(object))
	for name, value := range object {
		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			out[name] = text
			continue
		}
		out[name] = string(value)
	}
	return out
}

func completionMissingCodes(result runtime.CompletionResult) []string {
	codes := make([]string, 0, len(result.Missing))
	for _, item := range result.Missing {
		codes = append(codes, item.Code)
	}
	return codes
}

// completionExecutionError is the failure an Agent NodeRun records when the Agent declared
// completion but the check did not accept it. INCOMPLETE names the missing items by their
// codes; RELEASE_UNKNOWN names the unresolved release.
func completionExecutionError(goal domain.AgentGoal, result runtime.CompletionResult) domain.ExecutionError {
	if result.Outcome == domain.CompletionReleaseUnknown {
		return domain.ExecutionError{Code: runtime.CodeReleaseUnknown,
			Message: "the release outcome is unknown; the target stays occupied until reconciliation settles it"}
	}
	return domain.ExecutionError{Code: runtime.CodeTaskIncomplete,
		Message: fmt.Sprintf("the task goal %s is not met; missing: %s", goal, strings.Join(completionMissingCodes(result), ", "))}
}

// agentCompletionOf reads the verdict of one Agent Run inside the caller's read transaction,
// or nil when none was evaluated: the Agent Run has no goal, or has not terminated.
func agentCompletionOf(ctx context.Context, tx store.Tx, agentRunID string) (*domain.AgentCompletion, error) {
	completion, err := tx.AgentCompletions().Get(ctx, agentRunID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &completion, nil
}
