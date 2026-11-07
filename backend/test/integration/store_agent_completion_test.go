//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// TestAgentCompletionStore_CreateTwice_SecondLosesToPrimaryKey covers the write-once rule
// of a verdict: the first insert reads back whole, with its checks and missing items in
// order, and a second insert for the same Agent Run is refused by the primary key as a
// conflict rather than silently replacing or duplicating the verdict.
func TestAgentCompletionStore_CreateTwice_SecondLosesToPrimaryKey(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	agentRun := newAgentRun("ar_completion", f.nodeRunID)
	createAgentRun(ctx, t, uow, agentRun)

	want := domain.AgentCompletion{
		AgentRunID: agentRun.ID,
		Goal:       domain.AgentGoalPrepareOnly,
		Outcome:    domain.CompletionIncomplete,
		Checks: []domain.CompletionCheck{
			{Name: "workspace_patch_recorded", Passed: true, Refs: []string{"wpatch_1"}},
			{Name: "latest_patch_verified", Passed: false, Refs: []string{}},
		},
		Missing:     []domain.CompletionMissing{{Code: "PATCH_VERIFICATION_MISSING", Description: "No verification is recorded for the latest patch."}},
		EvaluatedAt: fixtureTime.Add(time.Minute),
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentCompletions().Create(ctx, want)
	}); err != nil {
		t.Fatalf("create completion: %v", err)
	}

	var got domain.AgentCompletion
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.AgentCompletions().Get(ctx, agentRun.ID)
		return err
	}); err != nil {
		t.Fatalf("get completion: %v", err)
	}
	if got.AgentRunID != want.AgentRunID || got.Goal != want.Goal || got.Outcome != want.Outcome || !got.EvaluatedAt.Equal(want.EvaluatedAt) {
		t.Errorf("completion = %+v, want %+v", got, want)
	}
	if len(got.Checks) != 2 || got.Checks[0].Name != "workspace_patch_recorded" || !got.Checks[0].Passed || got.Checks[0].Refs[0] != "wpatch_1" ||
		got.Checks[1].Passed || len(got.Checks[1].Refs) != 0 {
		t.Errorf("checks = %+v, want the two recorded checks in order", got.Checks)
	}
	if len(got.Missing) != 1 || got.Missing[0] != want.Missing[0] {
		t.Errorf("missing = %+v, want %+v", got.Missing, want.Missing)
	}

	second := want
	second.Outcome = domain.CompletionReadyForApproval
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentCompletions().Create(ctx, second)
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second create = %v, want domain.ErrConflict from the primary key", err)
	}
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.AgentCompletions().Get(ctx, agentRun.ID)
		return err
	}); err != nil {
		t.Fatalf("get completion after the refused insert: %v", err)
	}
	if got.Outcome != domain.CompletionIncomplete {
		t.Errorf("outcome after the refused insert = %s, want the first verdict INCOMPLETE unchanged", got.Outcome)
	}

	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.AgentCompletions().Get(ctx, "ar_never_evaluated")
		return err
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get of an unevaluated agent run = %v, want domain.ErrNotFound", err)
	}
}
