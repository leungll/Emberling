//go:build integration

// Package integration: this file covers the persisted uncertain flag on a failed Node
// Attempt. The flag commits with the Attempt's FAILED status and its Event, rolls back
// with them, is never rewritten by a losing failure path, and only a FAILED Attempt may
// carry it.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

func getNodeAttempt(ctx context.Context, t *testing.T, uow store.UnitOfWork, attemptID string) domain.NodeAttempt {
	t.Helper()
	var attempt domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempt, err = tx.NodeAttempts().Get(ctx, attemptID)
		return err
	}); err != nil {
		t.Fatalf("get node attempt %s: %v", attemptID, err)
	}
	return attempt
}

// startUncertainAttempt creates a Run and claims its first NodeRun, leaving one STARTED
// Attempt that nothing has executed yet.
func startUncertainAttempt(t *testing.T, h *execHarness, workflowID string) service.AdvanceOutcome {
	t.Helper()
	h.saveDefinition(execDocDefinition(workflowID))
	run := h.createRun(workflowID, 1, `{"brief":"a small ember creature"}`)
	outcome := h.advance(run.ID)
	if !outcome.Claimed || outcome.AttemptID == "" {
		t.Fatalf("advance: want a claimed Node Attempt, got %+v", outcome)
	}
	return outcome
}

func TestNodeAttemptUncertain_FailureRecordsTheDetermination(t *testing.T) {
	for _, uncertain := range []bool{true, false} {
		h := newExecHarness(t)
		outcome := startUncertainAttempt(t, h, "wf_uncertain")

		before := getNodeAttempt(h.ctx, t, h.uow, outcome.AttemptID)
		if before.Uncertain != nil {
			t.Fatalf("STARTED Attempt uncertain = %v, want nil", *before.Uncertain)
		}

		if err := h.svc.FailNode(h.ctx, service.FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "EXECUTOR_ERROR", Message: "provider did not answer"},
			Uncertain: uncertain,
			Source:    domain.FailureSyncExecution,
		}); err != nil {
			t.Fatalf("FailNode(uncertain=%v): %v", uncertain, err)
		}

		after := getNodeAttempt(h.ctx, t, h.uow, outcome.AttemptID)
		if after.Status != domain.NodeAttemptFailed {
			t.Fatalf("Attempt status = %s, want FAILED", after.Status)
		}
		if after.Uncertain == nil || *after.Uncertain != uncertain {
			t.Errorf("Attempt uncertain = %v, want %v", after.Uncertain, uncertain)
		}

		// A second, losing failure of the same Attempt writes nothing.
		if err := h.svc.FailNode(h.ctx, service.FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "TIMEOUT", Message: "late"},
			Uncertain: !uncertain,
			Source:    domain.FailureSyncExecution,
		}); err != nil {
			t.Fatalf("duplicate FailNode: %v", err)
		}
		again := getNodeAttempt(h.ctx, t, h.uow, outcome.AttemptID)
		if again.Uncertain == nil || *again.Uncertain != uncertain || again.Error.Code != "EXECUTOR_ERROR" {
			t.Errorf("Attempt after a losing failure = uncertain %v error %+v, want the first determination kept", again.Uncertain, again.Error)
		}
	}
}

func TestNodeAttemptUncertain_EventInsertFails_RollsBackWithTheFailure(t *testing.T) {
	h := newExecHarness(t)
	outcome := startUncertainAttempt(t, h, "wf_uncertain_rollback")

	const collidingID = "evt_uncertain_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, outcome.RunID, collidingID, h.clock.Now())
	h.ids.ForceNextEventID(collidingID)

	err := h.svc.FailNode(h.ctx, service.FailNode{
		AttemptID: outcome.AttemptID,
		Error:     domain.ExecutionError{Code: "EXECUTOR_ERROR", Message: "provider did not answer"},
		Uncertain: true,
		Source:    domain.FailureSyncExecution,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("FailNode with a forced Event ID collision: err = %v, want wrapped domain.ErrConflict", err)
	}

	after := getNodeAttempt(h.ctx, t, h.uow, outcome.AttemptID)
	if after.Status != domain.NodeAttemptStarted || after.Uncertain != nil {
		t.Errorf("Attempt after rollback = status %s uncertain %v, want STARTED and nil", after.Status, after.Uncertain)
	}
}

func TestNodeAttemptUncertain_NonFailedAttempt_RejectedByConstraint(t *testing.T) {
	h := newExecHarness(t)
	outcome := startUncertainAttempt(t, h, "wf_uncertain_check")

	yes := true
	err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().Create(ctx, domain.NodeAttempt{
			ID:        "attempt_uncertain_started",
			NodeRunID: outcome.NodeRunID,
			AttemptNo: 2,
			Status:    domain.NodeAttemptStarted,
			Input:     json.RawMessage(`{}`),
			StartedAt: h.clock.Now(),
			Uncertain: &yes,
		})
	})
	if err == nil {
		t.Fatal("create a STARTED Attempt carrying uncertain: want a constraint error, got nil")
	}
}
