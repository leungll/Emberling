//go:build integration

// Artifact link tests: the result transaction of a Tool Attempt that declared Execution
// Artifacts links each of them to the Attempt and its Run, after the artifact's metadata
// row and before the Attempt's conditional update to SUCCEEDED. The link is the only
// basis for reading an artifact through a Run, so it must exist exactly when a committed
// result declared the artifact: a result that loses its race or rolls back leaves none.
//
// They share the generation fixtures of agent_generation_limit_test.go and
// agent_artifact_save_test.go.
package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// artifactLinkRow is one tool_attempt_artifacts row as these tests read it.
type artifactLinkRow struct {
	ToolAttemptID string
	ArtifactID    string
	RunID         string
}

// artifactLinks lists every tool_attempt_artifacts row of the test database.
func artifactLinks(t *testing.T, h *agentHarness) []artifactLinkRow {
	t.Helper()
	rows, err := h.pool.Query(h.ctx, `SELECT tool_attempt_id, artifact_id, run_id FROM tool_attempt_artifacts ORDER BY run_id, tool_attempt_id`)
	if err != nil {
		t.Fatalf("list artifact links: %v", err)
	}
	defer rows.Close()
	var links []artifactLinkRow
	for rows.Next() {
		var link artifactLinkRow
		if err := rows.Scan(&link.ToolAttemptID, &link.ArtifactID, &link.RunID); err != nil {
			t.Fatalf("scan artifact link: %v", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list artifact links: %v", err)
	}
	return links
}

// artifactForRun reads an artifact through runID's links.
func artifactForRun(h *agentHarness, runID, artifactID string) (store.ArtifactRecord, error) {
	var record store.ArtifactRecord
	err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Artifacts().GetForRun(ctx, runID, artifactID)
		return err
	})
	return record, err
}

// completedGeneration runs one generation to completion and returns its Run and its
// SUCCEEDED Attempt.
func completedGeneration(t *testing.T, h *agentHarness, workflowID string) (domain.Run, domain.ToolAttempt) {
	t.Helper()
	run := startGenerationRun(h, workflowID, nil, generationCall("photo-a"))
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)
	action := generationActionOfTurn(t, h, factAgentRunID(t, h, outcome), 1)
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Fatalf("tool attempts of %s = %+v, want exactly one SUCCEEDED", workflowID, attempts)
	}
	return run, attempts[0]
}

// TestAgentToolResult_Generation_LinksArtifactToAttemptAndRun covers the success path:
// the committed result's declared artifact is linked to its Attempt and Run, and reads
// back through that Run with the metadata the result declared.
func TestAgentToolResult_Generation_LinksArtifactToAttemptAndRun(t *testing.T) {
	p := newGenerationProvider(t)
	h, _ := newGenerationHarness(t, p, nil)
	run, attempt := completedGeneration(t, h, "wf-artifact-link-success")
	ref := attemptArtifact(t, attempt)

	links := artifactLinks(t, h)
	want := artifactLinkRow{ToolAttemptID: attempt.ID, ArtifactID: ref.ArtifactID, RunID: run.ID}
	if len(links) != 1 || links[0] != want {
		t.Fatalf("artifact links = %+v, want exactly %+v", links, want)
	}
	record, err := artifactForRun(h, run.ID, ref.ArtifactID)
	if err != nil {
		t.Fatalf("GetForRun: %v", err)
	}
	if record.Artifact.Ref() != ref {
		t.Errorf("artifact read through the Run = %+v, want the declared %+v", record.Artifact.Ref(), ref)
	}
}

// TestAgentToolResult_StaleAttempt_NoArtifactLinkRow covers a late result: the Agent Run
// times out while the Tool runs, so the result transaction loses its conditional update
// and rolls back with its link. The artifact cannot be read through the Run.
func TestAgentToolResult_StaleAttempt_NoArtifactLinkRow(t *testing.T) {
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	h, executor := newGenerationHarness(t, p, stop)
	run, outcome, action := readyGeneration(t, h, stop, "wf-artifact-link-stale")
	agentRunID := factAgentRunID(t, h, outcome)

	executor.during = func(ctx context.Context, _ registry.ToolAction) {
		h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
		if err := h.svc.TimeoutAgentRun(ctx, agentRunID); err != nil {
			t.Errorf("time out agent run during the tool call: %v", err)
		}
	}
	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute generation: %v", err)
	}

	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed {
		t.Fatalf("tool attempts = %+v, want exactly one FAILED by the timeout", attempts)
	}
	if links := artifactLinks(t, h); len(links) != 0 {
		t.Errorf("artifact links = %+v, want none: the late result rolled back", links)
	}
	objects := artifactObjects(t, h, executor.artifacts)
	if len(objects) != 1 {
		t.Fatalf("artifact objects = %+v, want the one orphan the late call saved", objects)
	}
	if _, err := artifactForRun(h, run.ID, objects[0].ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetForRun of the orphan = %v, want ErrNotFound", err)
	}
}

// TestAgentToolResult_ResultTxFails_NoArtifactLinkRow covers a result transaction that
// fails after the link insert: the link rolls back with the metadata row and the result.
func TestAgentToolResult_ResultTxFails_NoArtifactLinkRow(t *testing.T) {
	h, _, orphan := orphanArtifactByResultRollback(t, "wf-artifact-link-rollback")
	if links := artifactLinks(t, h); len(links) != 0 {
		t.Errorf("artifact links = %+v, want none", links)
	}
	runs := listRunIDs(t, h)
	for _, runID := range runs {
		if _, err := artifactForRun(h, runID, orphan.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetForRun(%s) of the orphan = %v, want ErrNotFound", runID, err)
		}
	}
}

// TestAgentToolResult_TwoRunsSameBytes_EachRunReadsThroughItsOwnLink covers content
// addressing: two Runs generating identical bytes share one artifact row, but each Run
// holds its own link, and a Run without a link cannot read it.
func TestAgentToolResult_TwoRunsSameBytes_EachRunReadsThroughItsOwnLink(t *testing.T) {
	p := newGenerationProvider(t)
	h, _ := newGenerationHarness(t, p, nil)
	runA, attemptA := completedGeneration(t, h, "wf-artifact-link-shared-a")
	runB, attemptB := completedGeneration(t, h, "wf-artifact-link-shared-b")
	ref := attemptArtifact(t, attemptA)
	if attemptArtifact(t, attemptB) != ref {
		t.Fatalf("the two Runs declared different artifacts")
	}

	links := artifactLinks(t, h)
	if len(links) != 2 {
		t.Fatalf("artifact links = %+v, want one per Run", links)
	}
	for _, runID := range []string{runA.ID, runB.ID} {
		if _, err := artifactForRun(h, runID, ref.ArtifactID); err != nil {
			t.Errorf("GetForRun(%s): %v", runID, err)
		}
	}
	if _, err := artifactForRun(h, "run_without_links", ref.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetForRun of a Run without a link = %v, want ErrNotFound", err)
	}
}

// TestArtifactRepository_LinkToToolAttempt_Duplicate_Conflicts covers the link's identity:
// a second link of the same Attempt and artifact fails rather than being ignored.
func TestArtifactRepository_LinkToToolAttempt_Duplicate_Conflicts(t *testing.T) {
	p := newGenerationProvider(t)
	h, _ := newGenerationHarness(t, p, nil)
	run, attempt := completedGeneration(t, h, "wf-artifact-link-duplicate")
	ref := attemptArtifact(t, attempt)

	err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Artifacts().LinkToToolAttempt(ctx, store.ToolAttemptArtifact{
			ToolAttemptID: attempt.ID, ArtifactID: ref.ArtifactID, RunID: run.ID, CreatedAt: h.clock.Now(),
		})
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate link = %v, want ErrConflict", err)
	}
	if links := artifactLinks(t, h); len(links) != 1 {
		t.Errorf("artifact links = %+v, want still exactly one", links)
	}
}

// listRunIDs lists every Run of the test database.
func listRunIDs(t *testing.T, h *agentHarness) []string {
	t.Helper()
	rows, err := h.pool.Query(h.ctx, `SELECT id FROM runs ORDER BY id`)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan run id: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		t.Fatalf("no runs")
	}
	return ids
}
