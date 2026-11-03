//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// TestAgentRunStore_Resume_ClockBehindPause_LeavesDeadline proves the resume of a paused
// Agent Run never moves the deadline earlier: when the clock the resume reads is behind
// the pause time, the deadline stays where it was and only the pause is cleared.
func TestAgentRunStore_Resume_ClockBehindPause_LeavesDeadline(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	deadline := fixtureTime.Add(10 * time.Minute)
	pausedAt := fixtureTime.Add(time.Minute)

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.NodeRuns().Create(ctx, newNodeRun("nr_pause", f.runID, "node_pause")); err != nil {
			return err
		}
		agentRun := newAgentRun("ar_pause", "nr_pause")
		agentRun.Deadline = deadline
		if err := tx.AgentRuns().Create(ctx, agentRun); err != nil {
			return err
		}
		return tx.AgentRuns().Pause(ctx, "ar_pause", domain.PauseApproval, pausedAt)
	}); err != nil {
		t.Fatalf("seed paused agent run: %v", err)
	}

	var resumedDeadline time.Time
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		resumedDeadline, err = tx.AgentRuns().Resume(ctx, "ar_pause", pausedAt.Add(-time.Minute))
		return err
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumedDeadline.Equal(deadline) {
		t.Errorf("resumed deadline = %v, want unchanged %v", resumedDeadline, deadline)
	}
	var agentRun domain.AgentRun
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		agentRun, err = tx.AgentRuns().Get(ctx, "ar_pause")
		return err
	}); err != nil {
		t.Fatalf("get agent run: %v", err)
	}
	if agentRun.PausedAt != nil || agentRun.PauseReason != nil || !agentRun.Deadline.Equal(deadline) {
		t.Errorf("agent run = paused %v reason %v deadline %v, want running with deadline %v", agentRun.PausedAt, agentRun.PauseReason, agentRun.Deadline, deadline)
	}
}
