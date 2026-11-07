//go:build integration

// Workspace store tests: the frozen workspace binding and the cumulative patch sequence
// against a real PostgreSQL -- the round trip of both rows, the identity constraints that
// stop a second binding, a repeated sequence number or a second patch of one Tool Attempt,
// and the length CHECK that refuses a patch over the stored bound.
package integration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// workspaceStoreFixture seeds a Run, an Agent Run and one decided Turn with a Tool Attempt,
// which is what a patch row references.
type workspaceStoreFixture struct {
	uow        store.UnitOfWork
	agentRunID string
	attemptID  string
}

// seedWorkspaceStore opens the database first and only then derives the context the
// seeding uses, so the database lifetime is bound to the test rather than to a request.
func seedWorkspaceStore(t *testing.T) (context.Context, workspaceStoreFixture) {
	t.Helper()
	pool := testdb.Open(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_ws", "turn_ws_1", "decision_ws_1", "action_ws_1")
	createToolAttempt(ctx, t, uow, newToolAttempt("ta_ws_1", "action_ws_1", 1))
	return ctx, workspaceStoreFixture{uow: uow, agentRunID: "ar_ws", attemptID: "ta_ws_1"}
}

func (f workspaceStoreFixture) appendPatch(ctx context.Context, patch domain.WorkspacePatch) error {
	return f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.WorkspacePatches().Append(ctx, patch)
	})
}

func workspacePatchRow(id, agentRunID string, seq int, patch, attemptID string) domain.WorkspacePatch {
	return domain.WorkspacePatch{
		ID: id, AgentRunID: agentRunID, Seq: seq, Patch: patch,
		PatchDigest: domain.WorkspacePatchDigest(patch), TouchedPaths: domain.WorkspacePatchTouchedPaths(patch),
		ToolAttemptID: attemptID, CreatedAt: fixtureTime,
	}
}

func TestAgentWorkspaceStore_CreateAndGet_RoundTripsFrozenBinding(t *testing.T) {
	ctx, f := seedWorkspaceStore(t)
	want := domain.AgentWorkspace{
		AgentRunID: f.agentRunID, RepositoryID: "vuln-demo", BaseCommit: strings.Repeat("a", 40),
		AllowedPaths: []string{"go.mod", "internal/auth"},
		DeployTarget: &domain.WorkspaceDeployTarget{Service: "orders-api", Environment: "staging"},
		CreatedAt:    fixtureTime,
	}
	if err := f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentWorkspaces().Create(ctx, want)
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	var got domain.AgentWorkspace
	if err := f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.AgentWorkspaces().GetByAgentRunID(ctx, f.agentRunID)
		return err
	}); err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	got.CreatedAt = want.CreatedAt
	if !reflect.DeepEqual(got, want) {
		t.Errorf("workspace = %+v, want %+v", got, want)
	}

	// A second binding of the same Agent Run loses in the database.
	err := f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentWorkspaces().Create(ctx, want)
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second binding = %v, want %v", err, domain.ErrConflict)
	}
	// An Agent Run that froze none is reported as not found, not as an empty binding.
	err = f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.AgentWorkspaces().GetByAgentRunID(ctx, "ar_none")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing workspace = %v, want %v", err, domain.ErrNotFound)
	}
}

func TestWorkspacePatchStore_AppendLatestAndSummaries_RoundTrip(t *testing.T) {
	ctx, f := seedWorkspaceStore(t)
	createToolAttempt(ctx, t, f.uow, newToolAttempt("ta_ws_2", "action_ws_1", 2))

	var latest *domain.WorkspacePatch
	if err := f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		latest, err = tx.WorkspacePatches().Latest(ctx, f.agentRunID)
		return err
	}); err != nil || latest != nil {
		t.Fatalf("latest of an unwritten workspace = %+v, %v; want nil", latest, err)
	}

	first := "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-a\n+b\n"
	second := first + "diff --git a/internal/auth/x.go b/internal/auth/x.go\n--- /dev/null\n+++ b/internal/auth/x.go\n@@ -0,0 +1 @@\n+package auth\n"
	for _, p := range []domain.WorkspacePatch{
		workspacePatchRow("wpatch_1", f.agentRunID, 1, first, f.attemptID),
		workspacePatchRow("wpatch_2", f.agentRunID, 2, second, "ta_ws_2"),
	} {
		if err := f.appendPatch(ctx, p); err != nil {
			t.Fatalf("append patch %d: %v", p.Seq, err)
		}
	}

	var summaries []domain.WorkspacePatch
	if err := f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if latest, err = tx.WorkspacePatches().Latest(ctx, f.agentRunID); err != nil {
			return err
		}
		summaries, err = tx.WorkspacePatches().ListSummariesByAgentRun(ctx, f.agentRunID, 10)
		return err
	}); err != nil {
		t.Fatalf("read patches: %v", err)
	}
	if latest == nil || latest.Seq != 2 || latest.Patch != second || latest.PatchDigest != domain.WorkspacePatchDigest(second) ||
		!reflect.DeepEqual(latest.TouchedPaths, []string{"go.mod", "internal/auth/x.go"}) || latest.ToolAttemptID != "ta_ws_2" {
		t.Errorf("latest = %+v, want seq 2 with the second patch text", latest)
	}
	if len(summaries) != 2 || summaries[0].Seq != 1 || summaries[1].Seq != 2 {
		t.Fatalf("summaries = %+v, want seq 1 and 2 in order", summaries)
	}
	for _, s := range summaries {
		if s.Patch != "" || s.PatchDigest == "" || len(s.TouchedPaths) == 0 {
			t.Errorf("summary %d = %+v, want digest and touched paths without the text", s.Seq, s)
		}
	}
	if _, err := func() ([]domain.WorkspacePatch, error) {
		var out []domain.WorkspacePatch
		err := f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			out, err = tx.WorkspacePatches().ListSummariesByAgentRun(ctx, f.agentRunID, 0)
			return err
		})
		return out, err
	}(); err == nil {
		t.Errorf("a non-positive limit was accepted")
	}
}

func TestWorkspacePatchStore_DuplicateSeqOrToolAttempt_Conflicts(t *testing.T) {
	ctx, f := seedWorkspaceStore(t)
	createToolAttempt(ctx, t, f.uow, newToolAttempt("ta_ws_2", "action_ws_1", 2))
	patch := "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-a\n+b\n"
	if err := f.appendPatch(ctx, workspacePatchRow("wpatch_1", f.agentRunID, 1, patch, f.attemptID)); err != nil {
		t.Fatalf("append first patch: %v", err)
	}
	// The same sequence number from another Attempt loses.
	if err := f.appendPatch(ctx, workspacePatchRow("wpatch_dup_seq", f.agentRunID, 1, patch, "ta_ws_2")); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate seq = %v, want %v", err, domain.ErrConflict)
	}
	// A second patch of the same Tool Attempt loses.
	if err := f.appendPatch(ctx, workspacePatchRow("wpatch_dup_attempt", f.agentRunID, 2, patch, f.attemptID)); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate tool attempt = %v, want %v", err, domain.ErrConflict)
	}
	// The next sequence number from the other Attempt is accepted.
	if err := f.appendPatch(ctx, workspacePatchRow("wpatch_2", f.agentRunID, 2, patch, "ta_ws_2")); err != nil {
		t.Errorf("append seq 2: %v", err)
	}
}

func TestWorkspacePatchStore_PatchOverSizeBound_CheckConstraintRejects(t *testing.T) {
	ctx, f := seedWorkspaceStore(t)
	createToolAttempt(ctx, t, f.uow, newToolAttempt("ta_ws_2", "action_ws_1", 2))

	header := "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-a\n+"
	exact := header + strings.Repeat("x", domain.MaxWorkspacePatchBytes-len(header)-1) + "\n"
	if len(exact) != domain.MaxWorkspacePatchBytes {
		t.Fatalf("fixture patch is %d bytes, want exactly %d", len(exact), domain.MaxWorkspacePatchBytes)
	}
	if err := f.appendPatch(ctx, workspacePatchRow("wpatch_exact", f.agentRunID, 1, exact, f.attemptID)); err != nil {
		t.Fatalf("a patch of exactly the bound was refused: %v", err)
	}

	over := exact + "y"
	err := f.appendPatch(ctx, workspacePatchRow("wpatch_over", f.agentRunID, 2, over, "ta_ws_2"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a patch over the bound = %v, want %v from the CHECK constraint", err, domain.ErrConflict)
	}
	var latest *domain.WorkspacePatch
	if err := f.uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		latest, err = tx.WorkspacePatches().Latest(ctx, f.agentRunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Seq != 1 {
		t.Errorf("latest after the refused append = %+v, want seq 1 untouched", latest)
	}
}
