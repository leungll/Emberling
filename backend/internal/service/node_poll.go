package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// PollAttempt is the parameter struct for ExecutionService.PollAttempt. AttemptID is the
// only routing input; NodeRunID and RunID are what the caller discovered and are used for
// logging only, never trusted over the committed facts.
type PollAttempt struct {
	AttemptID string
	NodeRunID string
	RunID     string
}

// PollHooks are optional observation points inside PollAttempt. They exist so failure
// tests can inject a crash or a barrier at an exact point without sleeping.
type PollHooks struct {
	// AfterClaim runs after the claim transaction committed and before the Provider is
	// queried. A non-nil error stops PollAttempt there and is returned, which leaves the
	// world exactly as a process crash at that point would: one poll consumed and the
	// next poll time already pushed back.
	AfterClaim func(ctx context.Context, attemptID string) error
}

// PollAttemptKind classifies what one PollAttempt call did.
type PollAttemptKind string

const (
	// PollAttemptSkipped means no poll was claimed and the Provider was not queried.
	PollAttemptSkipped PollAttemptKind = "SKIPPED"
	// PollAttemptClaimedNotFinal means the poll was claimed and the Provider answered
	// with a status that resolves nothing (RUNNING or UNKNOWN); nothing else was written.
	PollAttemptClaimedNotFinal PollAttemptKind = "CLAIMED_NOT_FINAL"
	// PollAttemptResumed means a final poll result entered the resume use case and won
	// the conditional update that resolved the Attempt.
	PollAttemptResumed PollAttemptKind = "RESUMED"
	// PollAttemptDuplicate means a final poll result entered the resume use case but a
	// callback, a timeout or another poll had already resolved the Attempt.
	PollAttemptDuplicate PollAttemptKind = "DUPLICATE"
)

// PollSkipReason says why a PollAttempt call skipped.
type PollSkipReason string

const (
	PollSkipAttemptNotDispatched PollSkipReason = "ATTEMPT_NOT_DISPATCHED"
	PollSkipNodeRunNotWaiting    PollSkipReason = "NODE_RUN_NOT_WAITING_CALLBACK"
	PollSkipBindingMismatch      PollSkipReason = "BINDING_MISMATCH"
	PollSkipRegistryDrift        PollSkipReason = "REGISTRY_DRIFT"
	PollSkipNotClaimed           PollSkipReason = "NOT_CLAIMED"
)

// PollAttemptOutcome is PollAttempt's result. Status is the normalized Provider answer
// and is empty when the Provider was not queried. Failed and FailureSource mirror
// ResumeOutcome and are only meaningful for RESUMED.
type PollAttemptOutcome struct {
	Kind          PollAttemptKind
	SkipReason    PollSkipReason
	RunID         string
	NodeRunID     string
	AttemptID     string
	Status        registry.PollStatus
	Failed        bool
	FailureSource domain.FailureSource
}

// PollAttempt queries the Provider once for a dispatched Node Attempt whose poll is due
// and feeds a final answer into the single idempotent resume path.
//
// It runs in three stages. The claim transaction reads the Attempt, its NodeRun and its
// Callback Binding, resolves the poll policy from the current Node Registry and claims
// the poll with a conditional update; it never takes the Run lock, because claiming a
// poll is a scheduling fact that changes no business state and writes no Event. The
// Provider is then queried once, outside every transaction and lock, from committed facts
// only. Finally a SUCCEEDED or FAILED answer enters ResumeNode, whose conditional update
// from DISPATCHED decides the single winner against callbacks, timeouts and other polls;
// RUNNING or UNKNOWN writes nothing, because the next poll time was already committed by
// the claim.
//
// A poll that cannot be claimed is skipped without error. A registration that no longer
// declares polling clears the poll schedule and skips; a Callback Binding that does not
// route to the Attempt clears the schedule too and skips with the mismatch error. Neither
// fails the Attempt, which a callback or the deadline still resolves.
func (s *ExecutionService) PollAttempt(ctx context.Context, req PollAttempt) (PollAttemptOutcome, error) {
	if req.AttemptID == "" {
		return PollAttemptOutcome{}, errors.New("execution: a poll names no attempt")
	}
	claim, err := s.claimPoll(ctx, req.AttemptID)
	if err != nil {
		if claim.outcome.SkipReason == PollSkipBindingMismatch {
			s.deps.Logger.WarnContext(ctx, "provider poll refused: callback binding does not route to the attempt",
				"attemptId", req.AttemptID, "nodeRunId", req.NodeRunID, "runId", req.RunID)
		}
		return claim.outcome, err
	}
	if claim.outcome.Kind == PollAttemptSkipped {
		return claim.outcome, nil
	}
	outcome := claim.outcome

	if hook := s.deps.PollHooks.AfterClaim; hook != nil {
		if err := hook(ctx, req.AttemptID); err != nil {
			return outcome, fmt.Errorf("execution: poll attempt %s stopped after its claim committed: %w", req.AttemptID, err)
		}
	}

	result, err := claim.executor.Poll(ctx, claim.state)
	if err != nil {
		// The Provider gave no answer. The claim already pushed the next poll time back,
		// so a later poll, a callback or the deadline still decides the Attempt.
		return outcome, fmt.Errorf("execution: poll attempt %s for external task %s: %w",
			req.AttemptID, claim.state.ExternalTask.ExternalTaskID, err)
	}
	outcome.Status = result.Status
	if !pollIsFinal(result.Status) {
		return outcome, nil
	}

	resumed, err := s.ResumeNode(ctx, ResumeNode{
		ExternalTaskID: claim.state.ExternalTask.ExternalTaskID,
		Polled:         &PolledResult{AttemptID: req.AttemptID, Result: result},
	})
	if err != nil {
		return outcome, err
	}
	if resumed.Duplicate {
		outcome.Kind = PollAttemptDuplicate
		return outcome, nil
	}
	outcome.Kind = PollAttemptResumed
	outcome.Failed = resumed.Failed
	outcome.FailureSource = resumed.FailureSource
	return outcome, nil
}

// pollClaim is what the claim transaction hands to the Provider query: the Executor
// resolved from the current Registry and the async state restored from committed facts.
type pollClaim struct {
	outcome  PollAttemptOutcome
	executor registry.PollableAsyncNodeExecutor
	state    registry.NodeAsyncState
}

// claimPoll is the claim transaction. It commits either nothing, a cleared poll schedule
// (registry drift, or a Callback Binding that does not route to the Attempt) or one
// claimed poll; the returned outcome is SKIPPED unless the claim
// won, in which case it is CLAIMED_NOT_FINAL until the Provider answers.
func (s *ExecutionService) claimPoll(ctx context.Context, attemptID string) (pollClaim, error) {
	claim := pollClaim{outcome: PollAttemptOutcome{Kind: PollAttemptSkipped, AttemptID: attemptID}}
	skip := func(reason PollSkipReason) {
		claim.outcome.Kind = PollAttemptSkipped
		claim.outcome.SkipReason = reason
	}

	var mismatch error
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, attemptID)
		if err != nil {
			return err
		}
		claim.outcome.NodeRunID = attempt.NodeRunID
		if attempt.Status != domain.NodeAttemptDispatched {
			skip(PollSkipAttemptNotDispatched)
			return nil
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		claim.outcome.RunID = nodeRun.RunID
		if nodeRun.Status != domain.NodeRunWaitingCallback {
			skip(PollSkipNodeRunNotWaiting)
			return nil
		}

		// A poll carries no callback token, so the Callback Binding is its only route and
		// must name exactly this Attempt.
		binding, err := s.pollBinding(ctx, tx, attemptID)
		if err != nil {
			if !errors.Is(err, ErrPollBindingMismatch) && !errors.Is(err, ErrToolPollNotSupported) {
				return err
			}
			// No poll can ever route to this Attempt, so its schedule is cleared and
			// discovery stops listing it on every scan; a callback or the deadline still
			// resolves it. The clear commits and the mismatch is still reported.
			if _, clearErr := tx.NodeAttempts().ClearPoll(ctx, attemptID); clearErr != nil {
				return clearErr
			}
			skip(PollSkipBindingMismatch)
			mismatch = err
			return nil
		}

		reg, ok := s.deps.Nodes.Get(nodeRun.NodeType)
		var executor registry.PollableAsyncNodeExecutor
		if ok && reg.Metadata.Poll != nil {
			if eb, isExec := reg.Binding.(registry.ExecutorBinding); isExec {
				executor, _ = eb.Executor.(registry.PollableAsyncNodeExecutor)
			}
		}
		if executor == nil {
			// The current registration no longer polls this Node Type. The schedule is
			// cleared so the Attempt stops being discovered; a callback or the deadline
			// still resolves it.
			if _, err := tx.NodeAttempts().ClearPoll(ctx, attemptID); err != nil {
				return err
			}
			skip(PollSkipRegistryDrift)
			return nil
		}
		policy := *reg.Metadata.Poll

		run, err := tx.Runs().Get(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		def, err := tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
		if err != nil {
			return err
		}
		node, ok := nodeByID(def, nodeRun.NodeID)
		if !ok {
			return fmt.Errorf("execution: definition %s v%d no longer declares node %q", def.WorkflowID, def.Version, nodeRun.NodeID)
		}
		config, err := decodeConfig(node.Config)
		if err != nil {
			return err
		}

		claimed, err := tx.NodeAttempts().ClaimPoll(ctx, attemptID, s.deps.Clock.Now(),
			time.Duration(policy.IntervalMs)*time.Millisecond, policy.MaxPolls)
		if err != nil {
			return err
		}
		if !claimed {
			// Not due yet, already at the poll limit, or another poller, callback or
			// timeout got there first.
			skip(PollSkipNotClaimed)
			return nil
		}

		claim.outcome.Kind = PollAttemptClaimedNotFinal
		claim.executor = executor
		claim.state = registry.NodeAsyncState{
			RunID:     run.ID,
			NodeRunID: nodeRun.ID,
			AttemptNo: attempt.AttemptNo,
			ExternalTask: registry.ExternalTask{
				ProviderID:     binding.ProviderID,
				ExternalTaskID: binding.ExternalTaskID,
			},
			Config: config,
		}
		return nil
	})
	if err != nil {
		claim.executor = nil
		if claim.outcome.SkipReason == "" {
			claim.outcome.Kind = PollAttemptSkipped
		}
		return claim, err
	}
	if mismatch != nil {
		return claim, mismatch
	}
	return claim, nil
}

// pollBinding returns the single Callback Binding that routes to attemptID. No binding,
// several bindings, or one that does not route to this Node Attempt is a mismatch: the
// poll is refused and nothing is written.
func (s *ExecutionService) pollBinding(ctx context.Context, tx store.Tx, attemptID string) (domain.CallbackBinding, error) {
	bindings, err := tx.CallbackBindings().ListByTargets(ctx, domain.CallbackTargetNodeAttempt, []string{attemptID})
	if err != nil {
		return domain.CallbackBinding{}, err
	}
	if len(bindings) != 1 {
		return domain.CallbackBinding{}, fmt.Errorf("%w: %d callback bindings route to attempt %s", ErrPollBindingMismatch, len(bindings), attemptID)
	}
	if err := checkPollRoute(bindings[0], attemptID); err != nil {
		return domain.CallbackBinding{}, err
	}
	return bindings[0], nil
}
