//go:build integration

// Artifact save tests: generate_image writes the generated image into the
// content-addressed Execution Artifact store during the Tool call, after the claim
// transaction committed, and declares it in its result. The artifact's metadata row is
// committed by the result transaction under the Run lock, together with the Attempt's
// result, so the row exists exactly when the result does. A binary written by a call
// whose result never commits is an orphan object, never a referenced artifact; only the
// operator sweeper removes it.
//
// They share the agentHarness of agent_loop_test.go and the generation fixtures of
// agent_generation_limit_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// artifactSaveResult is the part of a generate_image result these tests read.
type artifactSaveResult struct {
	Artifact domain.ArtifactRef `json:"artifact"`
}

// attemptArtifact decodes the artifact a committed generate_image result declares.
func attemptArtifact(t *testing.T, attempt domain.ToolAttempt) domain.ArtifactRef {
	t.Helper()
	var result artifactSaveResult
	if err := json.Unmarshal(attempt.Result, &result); err != nil {
		t.Fatalf("decode result of attempt %s: %v", attempt.ID, err)
	}
	if err := result.Artifact.Validate(); err != nil {
		t.Fatalf("artifact of attempt %s = %+v: %v", attempt.ID, result.Artifact, err)
	}
	return result.Artifact
}

// countAllArtifactRows counts every execution_artifacts row of the test database.
func countAllArtifactRows(t *testing.T, h *agentHarness) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM execution_artifacts`).Scan(&n); err != nil {
		t.Fatalf("count execution artifacts: %v", err)
	}
	return n
}

// countArtifactRowsByID counts the execution_artifacts rows of one artifact.
func countArtifactRowsByID(t *testing.T, h *agentHarness, artifactID string) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM execution_artifacts WHERE artifact_id = $1`, artifactID).Scan(&n); err != nil {
		t.Fatalf("count execution artifact %s: %v", artifactID, err)
	}
	return n
}

// readArtifactObject reads one saved object from the storage root.
func readArtifactObject(t *testing.T, root, storageKey string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(storageKey)))
	if err != nil {
		t.Fatalf("read artifact object: %v", err)
	}
	return body
}

// artifactObjects lists the saved artifact objects under the harness's storage root.
func artifactObjects(t *testing.T, h *agentHarness, store *asset.ArtifactStore) []asset.ArtifactObject {
	t.Helper()
	objects, err := store.Objects(h.ctx)
	if err != nil {
		t.Fatalf("list artifact objects: %v", err)
	}
	return objects
}

// readyGeneration commits the Run's first TOOL_CALL Decision for generate_image and stops
// before its Action is claimed, returning the READY Action.
func readyGeneration(t *testing.T, h *agentHarness, stop *agentStopNotifier, workflowID string) (domain.Run, service.AdvanceOutcome, domain.AgentAction) {
	t.Helper()
	run := startGenerationRun(h, workflowID, nil, generationCall("photo-a"))
	outcome := stopAtModelCall(t, h, stop, run.ID, 1)
	action := generationActionOfTurn(t, h, factAgentRunID(t, h, outcome), 1)
	if action.Status != domain.AgentActionReady {
		t.Fatalf("generation action = %s, want READY before the claim", action.Status)
	}
	return run, outcome, action
}

// orphanArtifactByResultRollback runs one generation whose result transaction fails on a
// duplicated Event ID after the Tool saved the image, and proves what that leaves: the
// saved object, no metadata row, the Attempt STARTED without a result and the Action
// RUNNING. It returns the harness and the orphaned object.
func orphanArtifactByResultRollback(t *testing.T, workflowID string) (*agentHarness, *generationExecutor, asset.ArtifactObject) {
	t.Helper()
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	h, executor := newGenerationHarness(t, p, stop)
	run, _, action := readyGeneration(t, h, stop, workflowID)

	const collidingEventID = "ev_artifact_result_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingEventID, h.clock.Now())
	// Armed before the Tool call, so the collision hits the first Event of the result
	// transaction, which follows the artifact row insert.
	executor.during = func(context.Context, registry.ToolAction) {
		h.ids.ForceNextEventID(collidingEventID)
	}

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("execute generation = %v, want the conflict from the duplicated Event ID", err)
	}

	if got := getAgentAction(h.ctx, t, h.uow, action.ID).Status; got != domain.AgentActionRunning {
		t.Errorf("action = %s, want RUNNING: the result transaction rolled back", got)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted || len(attempts[0].Result) != 0 {
		t.Fatalf("tool attempts = %+v, want exactly one STARTED without a result", attempts)
	}
	if got := countAllArtifactRows(t, h); got != 0 {
		t.Errorf("execution artifact rows = %d, want 0: the row rolled back with the result", got)
	}
	objects := artifactObjects(t, h, executor.artifacts)
	if len(objects) != 1 {
		t.Fatalf("artifact objects = %+v, want exactly the one the Tool saved", objects)
	}
	if got := p.generations.Load(); got != 1 {
		t.Errorf("mock provider generations = %d, want 1", got)
	}
	return h, executor, objects[0]
}

// ---------------------------------------------------------------------------
// Result transaction
// ---------------------------------------------------------------------------

// TestArtifactSave_Generation_CommitsRowWithResult covers the success path: the Attempt's
// result declares the saved artifact, and its metadata row, committed in the same
// transaction, matches the object in the store.
func TestArtifactSave_Generation_CommitsRowWithResult(t *testing.T) {
	p := newGenerationProvider(t)
	h, executor := newGenerationHarness(t, p, nil)
	run := startGenerationRun(h, "wf-artifact-save-success", nil, generationCall("photo-a"))
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)

	action := generationActionOfTurn(t, h, factAgentRunID(t, h, outcome), 1)
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Fatalf("tool attempts = %+v, want exactly one SUCCEEDED", attempts)
	}
	ref := attemptArtifact(t, attempts[0])
	if ref.MediaType != "image/png" {
		t.Errorf("artifact media type = %s, want image/png", ref.MediaType)
	}
	if got := countArtifactRowsByID(t, h, ref.ArtifactID); got != 1 {
		t.Errorf("rows for %s = %d, want 1", ref.ArtifactID, got)
	}
	objects := artifactObjects(t, h, executor.artifacts)
	if len(objects) != 1 || objects[0].ArtifactID != ref.ArtifactID || objects[0].SHA256 != ref.SHA256 {
		t.Fatalf("artifact objects = %+v, want exactly the declared %s", objects, ref.ArtifactID)
	}
	body := readArtifactObject(t, executor.artifactRoot, objects[0].StorageKey)
	if int64(len(body)) != ref.SizeBytes {
		t.Errorf("object size = %d, want the declared %d", len(body), ref.SizeBytes)
	}
}

// TestArtifactSave_ResultTxFails_LeavesOrphanObjectWithoutRow covers the order of the
// save: the binary is written during the Tool call, before the result transaction, so a
// rolled-back result leaves the object behind with no row and no result naming it.
func TestArtifactSave_ResultTxFails_LeavesOrphanObjectWithoutRow(t *testing.T) {
	orphanArtifactByResultRollback(t, "wf-artifact-save-rollback")
}

// TestArtifactSave_DuplicateExecution_AddsNoRow covers duplicate delivery of a completed
// generation: entering the Action's execution again loses the claim, so the Tool is not
// called, the Provider sees no second generation and no further row is written.
func TestArtifactSave_DuplicateExecution_AddsNoRow(t *testing.T) {
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	h, executor := newGenerationHarness(t, p, stop)
	_, _, action := readyGeneration(t, h, stop, "wf-artifact-save-duplicate")

	if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute generation: %v", err)
	}
	if got := countAllArtifactRows(t, h); got != 1 {
		t.Fatalf("execution artifact rows after the generation = %d, want 1", got)
	}
	for _, source := range []domain.ClaimSource{domain.ClaimImmediate, domain.ClaimReconciler} {
		if err := h.svc.ExecuteAgentAction(h.ctx, action.ID, source); err != nil {
			t.Fatalf("duplicate execution from %s: %v", source, err)
		}
	}

	if got := countAllArtifactRows(t, h); got != 1 {
		t.Errorf("execution artifact rows = %d, want still 1", got)
	}
	if got := executor.calls.Load(); got != 1 {
		t.Errorf("generate_image executor calls = %d, want 1", got)
	}
	if got := p.generations.Load(); got != 1 {
		t.Errorf("mock provider generations = %d, want 1", got)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Errorf("tool attempts = %+v, want exactly one SUCCEEDED", attempts)
	}
}

// TestArtifactSave_LateResultOfStaleAttempt_AddsNoRow covers a completion that lost the
// race: the Agent Run times out while the Tool is running, so the Attempt is closed
// before the result arrives. The late result transaction loses its conditional update and
// rolls back, taking the artifact row with it; the saved object stays an orphan.
func TestArtifactSave_LateResultOfStaleAttempt_AddsNoRow(t *testing.T) {
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	h, executor := newGenerationHarness(t, p, stop)
	_, outcome, action := readyGeneration(t, h, stop, "wf-artifact-save-stale")
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
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed || len(attempts[0].Result) != 0 {
		t.Fatalf("tool attempts = %+v, want exactly one FAILED by the timeout, without a result", attempts)
	}
	if got := countAllArtifactRows(t, h); got != 0 {
		t.Errorf("execution artifact rows = %d, want 0", got)
	}
	if objects := artifactObjects(t, h, executor.artifacts); len(objects) != 1 {
		t.Errorf("artifact objects = %+v, want the one orphan the late call saved", objects)
	}
}

// TestArtifactSave_TwoRunsSameBytes_ShareOneObjectAndRow covers content addressing across
// Runs: two generations returning identical bytes save one object and one row, and both
// Attempts succeed naming the same artifact.
func TestArtifactSave_TwoRunsSameBytes_ShareOneObjectAndRow(t *testing.T) {
	p := newGenerationProvider(t)
	h, executor := newGenerationHarness(t, p, nil)

	var refs []domain.ArtifactRef
	for _, workflowID := range []string{"wf-artifact-save-shared-a", "wf-artifact-save-shared-b"} {
		run := startGenerationRun(h, workflowID, nil, generationCall("photo-a"))
		outcome := h.claimAgentNode(run.ID)
		h.execute(outcome)
		action := generationActionOfTurn(t, h, factAgentRunID(t, h, outcome), 1)
		attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
		if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
			t.Fatalf("tool attempts of %s = %+v, want exactly one SUCCEEDED", workflowID, attempts)
		}
		refs = append(refs, attemptArtifact(t, attempts[0]))
	}

	if refs[0] != refs[1] {
		t.Errorf("artifacts = %+v and %+v, want the same content-addressed artifact", refs[0], refs[1])
	}
	if got := p.generations.Load(); got != 2 {
		t.Errorf("mock provider generations = %d, want 2", got)
	}
	if objects := artifactObjects(t, h, executor.artifacts); len(objects) != 1 {
		t.Errorf("artifact objects = %+v, want exactly one", objects)
	}
	if got := countAllArtifactRows(t, h); got != 1 {
		t.Errorf("execution artifact rows = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Crash and restart
// ---------------------------------------------------------------------------

// crashAfterSave saves the image through the real store and then crashes the process,
// so the binary exists while nothing after the Tool call committed.
type crashAfterSave struct {
	delegate *asset.ArtifactStore
}

func (c crashAfterSave) Write(ctx context.Context, r io.Reader) (domain.ArtifactRef, error) {
	if _, err := c.delegate.Write(ctx, r); err != nil {
		return domain.ArtifactRef{}, err
	}
	panic("simulated crash after the tool saved the image, before its result committed")
}

// TestArtifactSave_CrashAfterSave_RestartDoesNotResend covers a crash between the saved
// binary and the result transaction. After the restart the Attempt is still STARTED: an
// uncertain external call is never re-executed, so the Reconciler neither claims the
// Action again nor reaches the Provider, and the saved object has no row.
func TestArtifactSave_CrashAfterSave_RestartDoesNotResend(t *testing.T) {
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	h, executor := newGenerationHarness(t, p, stop)
	executor.delegate = generateimage.New(p.url, &http.Client{Timeout: 5 * time.Second}, crashAfterSave{delegate: executor.artifacts})
	_, _, action := readyGeneration(t, h, stop, "wf-artifact-save-crash")

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		_ = h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate)
	}()

	// The restarted Backend registers the same counting Executor, so any re-execution
	// would show in its call count and in the Provider's generations.
	registration := generateimage.Registration(p.url, nil, executor.artifacts)
	registration.Executor = executor
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool: h.pool, Clock: h.clock, Tools: []registry.ToolRegistration{registration},
	})
	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ExpiredAgentRunsFound != 0 {
		t.Fatalf("report = %+v, want no expired Agent Run before the deadline", report)
	}

	if got := getAgentAction(restarted.ctx, t, restarted.uow, action.ID).Status; got != domain.AgentActionRunning {
		t.Errorf("action = %s, want still RUNNING", got)
	}
	attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted || len(attempts[0].Result) != 0 {
		t.Fatalf("tool attempts = %+v, want exactly one STARTED without a result", attempts)
	}
	if got := executor.calls.Load(); got != 1 {
		t.Errorf("generate_image executor calls = %d, want 1", got)
	}
	if got := p.generations.Load(); got != 1 {
		t.Errorf("mock provider generations = %d, want 1: the restart must not resend", got)
	}
	if got := countAllArtifactRows(t, restarted); got != 0 {
		t.Errorf("execution artifact rows = %d, want 0", got)
	}
	if objects := artifactObjects(t, restarted, executor.artifacts); len(objects) != 1 {
		t.Errorf("artifact objects = %+v, want the one orphan saved before the crash", objects)
	}
}
