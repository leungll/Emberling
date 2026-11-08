//go:build integration

// Node artifact link tests: the completion transaction of a Node Attempt that declared
// Execution Artifacts with its output commits their metadata and links each to the
// Attempt and its Run, in the same transaction as the Attempt's conditional update to
// SUCCEEDED. A completion that does not win that update writes no link, so an artifact a
// failed or superseded Attempt declared is never readable through the Run.
//
// They share the deterministic async Node and harness of service_async_test.go.
package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// nodeLinkRow is one node_attempt_artifacts row as these tests read it.
type nodeLinkRow struct {
	NodeAttemptID string
	ArtifactID    string
	RunID         string
}

func nodeArtifactLinks(t *testing.T, h *asyncHarness) []nodeLinkRow {
	t.Helper()
	rows, err := h.pool.Query(h.ctx, `SELECT node_attempt_id, artifact_id, run_id FROM node_attempt_artifacts ORDER BY run_id, node_attempt_id`)
	if err != nil {
		t.Fatalf("list node artifact links: %v", err)
	}
	defer rows.Close()
	var links []nodeLinkRow
	for rows.Next() {
		var link nodeLinkRow
		if err := rows.Scan(&link.NodeAttemptID, &link.ArtifactID, &link.RunID); err != nil {
			t.Fatalf("scan node artifact link: %v", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list node artifact links: %v", err)
	}
	return links
}

func nodeArtifactForRun(h *asyncHarness, runID, artifactID string) (store.ArtifactRecord, error) {
	var record store.ArtifactRecord
	err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Artifacts().GetForRun(ctx, runID, artifactID)
		return err
	})
	return record, err
}

// asyncArtifactRef is the declaration the fake node returns with its output.
func asyncArtifactRef(t *testing.T) domain.ArtifactRef {
	t.Helper()
	sha := asyncSHA256("S1\t0.000\t1.000\tHello.\n")
	id, err := domain.ArtifactIDForSHA256(sha)
	if err != nil {
		t.Fatalf("artifact id: %v", err)
	}
	return domain.ArtifactRef{ArtifactID: id, MediaType: domain.ArtifactMediaTypeTextPlain, SizeBytes: 21, SHA256: sha}
}

// TestNodeComplete_CallbackDeclaresArtifact_LinksItOnceAndRunReadsIt: the winning
// callback links the declared artifact to its Attempt and Run; a duplicate delivery of
// the same callback writes no second link, and the Run reads the artifact through it.
func TestNodeComplete_CallbackDeclaresArtifact_LinksItOnceAndRunReadsIt(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	ref := asyncArtifactRef(t)
	h.exec.artifacts = []domain.ArtifactRef{ref}
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-artifact")

	if _, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"done"}`); err != nil {
		t.Fatalf("callback: %v", err)
	}
	outcome, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"done"}`)
	if err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}
	if !outcome.Duplicate {
		t.Fatalf("duplicate callback outcome = %+v, want Duplicate", outcome)
	}

	links := nodeArtifactLinks(t, h)
	if len(links) != 1 || links[0] != (nodeLinkRow{NodeAttemptID: dispatch.attemptID, ArtifactID: ref.ArtifactID, RunID: run.ID}) {
		t.Fatalf("node artifact links = %+v, want exactly one for attempt %s", links, dispatch.attemptID)
	}
	record, err := nodeArtifactForRun(h, run.ID, ref.ArtifactID)
	if err != nil {
		t.Fatalf("GetForRun: %v", err)
	}
	if record.Artifact.SHA256 != ref.SHA256 || record.Artifact.MediaType != ref.MediaType || record.Artifact.SizeBytes != ref.SizeBytes {
		t.Fatalf("GetForRun = %+v, want the declared artifact %+v", record.Artifact, ref)
	}
	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).Status; got != domain.NodeAttemptSucceeded {
		t.Fatalf("Attempt status = %s, want SUCCEEDED", got)
	}
}

// TestNodeComplete_TimeoutWonBeforeCallback_DeclaredArtifactLeavesNoLink: the Attempt's
// deadline resolves it FAILED before the callback arrives, so the callback's completion
// loses the conditional update and its artifact declaration leaves no link; the Run
// cannot read the artifact.
func TestNodeComplete_TimeoutWonBeforeCallback_DeclaredArtifactLeavesNoLink(t *testing.T) {
	h := newAsyncHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	ref := asyncArtifactRef(t)
	h.exec.artifacts = []domain.ArtifactRef{ref}
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-late-artifact")

	h.clock.Advance(2 * time.Minute)
	if err := h.svc.TimeoutAttempt(h.ctx, dispatch.attemptID); err != nil {
		t.Fatalf("timeout: %v", err)
	}
	outcome, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"done"}`)
	if err != nil {
		t.Fatalf("late callback: %v", err)
	}
	if !outcome.Duplicate {
		t.Fatalf("late callback outcome = %+v, want Duplicate (the timeout already resolved the Attempt)", outcome)
	}

	if links := nodeArtifactLinks(t, h); len(links) != 0 {
		t.Fatalf("node artifact links after a lost completion = %+v, want none", links)
	}
	if _, err := nodeArtifactForRun(h, run.ID, ref.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetForRun after a lost completion: err = %v, want ErrNotFound", err)
	}
	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).Status; got != domain.NodeAttemptFailed {
		t.Fatalf("Attempt status = %s, want FAILED", got)
	}
}
