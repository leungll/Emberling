package service

import (
	"context"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// TimeoutAttempt expires one Attempt if it is still STARTED and its deadline has
// passed; otherwise it is a no-op (already resolved, or not yet due -- a late
// completion after a timeout already fired must not change terminal state, which is
// exactly the stale-claim guard FailNode's own Attempt transition enforces). It performs
// a read-only guard transaction to decide whether to act, then -- only if warranted --
// delegates to FailNode's own transaction with code TIMEOUT, Source TIMEOUT, and
// Uncertain=true exactly when the node's registered SideEffectPolicy is EXTERNAL (an
// EXTERNAL call cut off by a deadline has an unproven remote result; a NONE-side-effect
// node has nothing to be uncertain about).
//
// A NodeRun whose Node Type the Registry no longer carries is a second, unconditional
// reason to fail rather than the ordinary TIMEOUT path: recovery also requires the
// current Runtime Registry to resolve the Node, Provider and Tool types the bound
// Definition uses, and a missing or incompatible one must fail deterministically and
// retain Trace. The acceptance criteria repeat this for recovery, as does CLAUDE.md
// "Extensions and external calls" ("A missing or incompatible registration fails
// explicitly and retains Trace").
// This mirrors Execute's own unregistered-Node-Type handling (this file, ~833-841): same
// NODE_TYPE_NOT_REGISTERED code, same Uncertain=false (no SideEffectPolicy survives a
// dropped registration to judge uncertainty by), same terminal failNode transaction --
// so the Attempt/NodeRun reach FAILED with a retained NODE_FAILED Event instead of
// bubbling a bare error that would abort the guard transaction and leave the row stuck
// DISPATCHED/WAITING_CALLBACK (or STARTED/RUNNING) forever. failNode already tolerates a
// missing registration gracefully on its own registry lookup (it forces decision.Retry =
// false when regOK is false), so routing through it here needs no new status or code.
func (s *ExecutionService) TimeoutAttempt(ctx context.Context, attemptID string) error {
	var expired bool
	var uncertain bool
	var missingNodeType string
	fromAttempt := domain.NodeAttemptStarted
	fromNodeRun := domain.NodeRunRunning

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, attemptID)
		if err != nil {
			return err
		}
		switch attempt.Status {
		case domain.NodeAttemptStarted:
			fromAttempt, fromNodeRun = domain.NodeAttemptStarted, domain.NodeRunRunning
		case domain.NodeAttemptDispatched:
			// Wait timeout: a dispatched Attempt whose deadline passed competes with
			// the callback and with Provider Poll for the same completion right. The
			// conditional transitions in failNode elect the single winner.
			fromAttempt, fromNodeRun = domain.NodeAttemptDispatched, domain.NodeRunWaitingCallback
		default:
			return nil
		}
		if attempt.DeadlineAt == nil {
			return nil
		}
		now := s.deps.Clock.Now()
		if now.Before(*attempt.DeadlineAt) {
			return nil
		}

		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		reg, ok := s.deps.Nodes.Get(nodeRun.NodeType)
		if !ok {
			// Registry drift, not an ordinary deadline: record which type is gone and
			// still fall through to failNode below rather than aborting the guard
			// transaction (which committed nothing and left the row stuck -- the bug
			// this comment block explains above).
			expired = true
			missingNodeType = nodeRun.NodeType
			return nil
		}

		expired = true
		uncertain = reg.Metadata.SideEffect.Kind == domain.SideEffectExternal
		return nil
	})
	if err != nil {
		return err
	}
	if !expired {
		return nil
	}

	execError := domain.ExecutionError{Code: "TIMEOUT", Message: "attempt deadline exceeded"}
	// FailureSource records which path produced the failure, not which persisted fact it
	// produced (nodeFailedPayload/NODE_FAILED never persists it -- see failNodeParams and
	// ResumeOutcome's doc comments); TIMEOUT stays accurate for the registry-drift case
	// too, since it was this timeout scan, not a synchronous Execute call, that surfaced
	// the dropped registration.
	source := domain.FailureTimeout
	if missingNodeType != "" {
		execError = domain.ExecutionError{Code: "NODE_TYPE_NOT_REGISTERED", Message: fmt.Sprintf("node type %q is not registered", missingNodeType)}
		uncertain = false
	}

	_, err = s.failNode(ctx, failNodeParams{
		attemptID:   attemptID,
		execError:   execError,
		uncertain:   uncertain,
		source:      source,
		fromAttempt: fromAttempt,
		fromNodeRun: fromNodeRun,
		// A timeout of an Attempt that already reached the Provider resolves the NodeRun:
		// re-dispatching an external task that entered WAITING_CALLBACK is forbidden.
		// failNode forces Retry=false unconditionally for the registry-drift case too, so
		// this terminal flag only changes which NodeRun/Run guard failNode uses; it does
		// not need its own registry-drift branch.
		terminal: fromNodeRun == domain.NodeRunWaitingCallback,
	})
	return err
}
