//go:build integration

// Package integration: the Context Manifest store against a real PostgreSQL — one
// Manifest per Turn by PRIMARY KEY, the item round trip, and the per-Agent-Run read in
// Turn order.
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

func newManifestItems() []domain.ManifestItem {
	omitted := domain.ManifestReferenceOnly
	return []domain.ManifestItem{
		{Kind: domain.ManifestInstructions, Label: "Instructions", Ref: "ar_1", SHA256: "aa", Length: 12, Included: true},
		{Kind: domain.ManifestToolResult, Label: "lookup result", Ref: "ta_1", Version: "1", SHA256: "bb", Length: 30, Included: true},
		{Kind: domain.ManifestArtifact, Label: "art_1", Ref: "art_1", SHA256: "cc", Length: 4096, Included: false, OmittedReason: &omitted},
	}
}

func createManifest(ctx context.Context, uow store.UnitOfWork, manifest domain.AgentTurnManifest) error {
	return uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentTurnManifests().Create(ctx, manifest)
	})
}

func TestAgentTurnManifestStore_CreateAndGet_RoundTripsItems(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	agentRun := newAgentRun("ar_1", f.nodeRunID)
	createAgentRun(ctx, t, uow, agentRun)
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_1", agentRun.ID, 1))

	want := domain.AgentTurnManifest{
		TurnID: "turn_1", AgentRunID: agentRun.ID, Items: newManifestItems(),
		CreatedAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
	}
	if err := createManifest(ctx, uow, want); err != nil {
		t.Fatalf("create manifest: %v", err)
	}

	var got domain.AgentTurnManifest
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.AgentTurnManifests().GetByTurnID(ctx, "turn_1")
		return err
	}); err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if got.TurnID != want.TurnID || got.AgentRunID != want.AgentRunID || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("manifest identity = %+v, want %+v", got, want)
	}
	if len(got.Items) != len(want.Items) {
		t.Fatalf("items = %d, want %d", len(got.Items), len(want.Items))
	}
	for i := range want.Items {
		w, g := want.Items[i], got.Items[i]
		if g.Kind != w.Kind || g.Label != w.Label || g.Ref != w.Ref || g.Version != w.Version ||
			g.SHA256 != w.SHA256 || g.Length != w.Length || g.Included != w.Included {
			t.Errorf("item %d = %+v, want %+v", i, g, w)
		}
		switch {
		case w.OmittedReason == nil && g.OmittedReason != nil:
			t.Errorf("item %d: omittedReason = %q, want none", i, *g.OmittedReason)
		case w.OmittedReason != nil && (g.OmittedReason == nil || *g.OmittedReason != *w.OmittedReason):
			t.Errorf("item %d: omittedReason = %v, want %q", i, g.OmittedReason, *w.OmittedReason)
		}
	}
}

func TestAgentTurnManifestStore_SecondManifestForSameTurn_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	agentRun := newAgentRun("ar_1", f.nodeRunID)
	createAgentRun(ctx, t, uow, agentRun)
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_1", agentRun.ID, 1))

	first := domain.AgentTurnManifest{TurnID: "turn_1", AgentRunID: agentRun.ID, Items: newManifestItems(), CreatedAt: time.Now().UTC()}
	if err := createManifest(ctx, uow, first); err != nil {
		t.Fatalf("create first manifest: %v", err)
	}
	second := first
	second.Items = nil
	err := createManifest(ctx, uow, second)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second manifest for the same Turn: err = %v, want ErrConflict", err)
	}

	var got domain.AgentTurnManifest
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.AgentTurnManifests().GetByTurnID(ctx, "turn_1")
		return err
	}); err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if len(got.Items) != len(first.Items) {
		t.Fatalf("the committed Manifest was replaced: %d items, want %d", len(got.Items), len(first.Items))
	}
}

func TestAgentTurnManifestStore_ListByAgentRunID_OrdersByTurnNoAndScopesToOneAgentRun(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	agentRun := newAgentRun("ar_1", f.nodeRunID)
	createAgentRun(ctx, t, uow, agentRun)
	// The second Turn's Manifest is written first, so the read order must come from
	// turn_no and not from insertion.
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_1", agentRun.ID, 1))
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_2", agentRun.ID, 2))
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_3", agentRun.ID, 3))
	now := time.Now().UTC()
	for _, turnID := range []string{"turn_2", "turn_1"} {
		if err := createManifest(ctx, uow, domain.AgentTurnManifest{TurnID: turnID, AgentRunID: agentRun.ID, Items: newManifestItems(), CreatedAt: now}); err != nil {
			t.Fatalf("create manifest %s: %v", turnID, err)
		}
	}

	// A second Agent NodeRun in the same Run, so the scope is proved against a database
	// that really holds another Agent Run's Manifest.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Create(ctx, newNodeRun("nr_agent_2", f.runID, "node_agent_2"))
	}); err != nil {
		t.Fatalf("seed a second Agent NodeRun: %v", err)
	}
	otherRun := newAgentRun("ar_2", "nr_agent_2")
	createAgentRun(ctx, t, uow, otherRun)
	createAgentTurn(ctx, t, uow, newAgentTurn("turn_other", otherRun.ID, 1))
	if err := createManifest(ctx, uow, domain.AgentTurnManifest{TurnID: "turn_other", AgentRunID: otherRun.ID, Items: newManifestItems(), CreatedAt: now}); err != nil {
		t.Fatalf("create other manifest: %v", err)
	}

	var got []domain.AgentTurnManifest
	var missing error
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if got, err = tx.AgentTurnManifests().ListByAgentRunID(ctx, agentRun.ID); err != nil {
			return err
		}
		_, missing = tx.AgentTurnManifests().GetByTurnID(ctx, "turn_3")
		return nil
	}); err != nil {
		t.Fatalf("list manifests: %v", err)
	}
	if len(got) != 2 || got[0].TurnID != "turn_1" || got[1].TurnID != "turn_2" {
		ids := make([]string, len(got))
		for i, m := range got {
			ids[i] = m.TurnID
		}
		t.Fatalf("manifests = %v, want [turn_1 turn_2]", ids)
	}
	if !errors.Is(missing, domain.ErrNotFound) {
		t.Fatalf("unclaimed Turn's manifest: err = %v, want ErrNotFound", missing)
	}
}
