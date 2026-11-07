//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

// lockNotAvailable is PostgreSQL's SQLSTATE for a NOWAIT lock request that found the row
// already locked.
const lockNotAvailable = "55P03"

// runLockProbe is the release Tool's HTTP transport in a test that proves no production
// request is made while the Run row is locked: before handing each request to the real
// transport it tries to lock the Run row with NOWAIT from its own connection, which fails
// exactly when some transaction holds that row lock at the moment of the call.
type runLockProbe struct {
	inner http.RoundTripper
	pool  *pgxpool.Pool
	runID atomic.Value // string; empty until the Run exists

	mu       sync.Mutex
	probed   []string
	lockedAt []string
	errs     []error
}

func newRunLockProbe(p *productionFixture) *runLockProbe {
	probe := &runLockProbe{inner: p.srv.Client().Transport}
	probe.runID.Store("")
	return probe
}

func (p *runLockProbe) RoundTrip(req *http.Request) (*http.Response, error) {
	if runID, _ := p.runID.Load().(string); runID != "" {
		held, err := p.runLockHeld(context.Background(), runID)
		call := req.Method + " " + req.URL.Path
		p.mu.Lock()
		p.probed = append(p.probed, call)
		if held {
			p.lockedAt = append(p.lockedAt, call)
		}
		if err != nil {
			p.errs = append(p.errs, err)
		}
		p.mu.Unlock()
	}
	return p.inner.RoundTrip(req)
}

// runLockHeld reports whether another transaction holds the Run row lock right now. Its
// own lock attempt is rolled back immediately, so it never delays the Runtime.
func (p *runLockProbe) runLockHeld(ctx context.Context, runID string) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE NOWAIT`, runID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return true, nil
	}
	return false, err
}

func (p *runLockProbe) report() (probed, lockedAt []string, errs []error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.probed...), append([]string(nil), p.lockedAt...), append([]error(nil), p.errs...)
}

// TestAgentCompletion_DeployedThenStatePatchFails_IncompleteWithoutReleaseConfirmation
// drives a FIX_AND_DEPLOY task with real material whose release Decision carries a state
// patch the State Schema rejects. Production deploys the approved patch and the poll learns
// DEPLOYED, but the result transaction cannot commit the round: the release is settled
// first (the Attempt keeps its SUCCEEDED result, the occupation is released as DEPLOYED, the
// Agent's clock resumes) and then the Action fails as INVALID_ACTION and ends the Agent Run.
// No release_confirmed fact is written, because the fact belongs only to a committed round,
// so the completion check judges the task INCOMPLETE and names the missing release
// confirmation beside the missing FINAL. Nothing is left for post-task reconciliation: the
// target is free and no further production query is made. Every production request is made
// without the Run row lock held.
func TestAgentCompletion_DeployedThenStatePatchFails_IncompleteWithoutReleaseConfirmation(t *testing.T) {
	const stateSchema = `{"type":"object","properties":{"count":{"type":"number"}},"additionalProperties":false}`
	production := newProductionFixture(t)
	probe := newRunLockProbe(production)
	f := newReleaseFixture(t, releaseFixtureOptions{production: production, stateSchema: stateSchema,
		client: &http.Client{Transport: probe}})
	// The pool is set before the Run id is published, and the probe reads it only after it
	// loads a non-empty id, so the atomic store orders the two.
	probe.pool = f.h.pool

	action := f.startPatchedReleaseWith(t, "wf_completion_deployed_bad_patch", f.releaseCallWithPatch(t, `{"count":"not-a-number"}`))
	probe.runID.Store(f.run.ID)
	agentRunID := f.agentRunID(t)
	latest := wsLatestPatch(t, f.h, agentRunID)

	approved := f.approve(t, action)
	f.poll(t, f.reconciler())

	// Production did deploy the approved patch exactly once.
	attempts := f.attempts(t, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Fatalf("attempts = %+v, want the one SUCCEEDED: production did deploy", attempts)
	}
	key := operationKey(attempts[0])
	if n := production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests = %d, want one", n)
	}
	if status := production.operationStatus(t, key); status != "DEPLOYED" {
		t.Errorf("production status of %s = %q, want DEPLOYED", key, status)
	}
	target, ok := production.target(t)
	if !ok || target["lastOperationId"] != key || target["patchDigest"] != latest.PatchDigest {
		t.Errorf("production target = %v (%v), want %s with the approved patch %s live", target, ok, key, latest.PatchDigest)
	}
	// The deployment stays on record as the SUCCEEDED Attempt's result: which operation,
	// which version, which patch and whether the target was healthy.
	var result map[string]any
	if err := json.Unmarshal(attempts[0].Result, &result); err != nil {
		t.Fatalf("decode the release attempt result: %v", err)
	}
	if result["operationKey"] != key || result["patchDigest"] != latest.PatchDigest || result["healthy"] != true || result["version"] != target["version"] {
		t.Errorf("release attempt result = %v, want the healthy deployment of %s at version %v", result, key, target["version"])
	}

	// The round failed: the Action, the Agent Run, the Agent NodeRun and the Run end.
	got := f.action(t, action.ID)
	if got.Status != domain.AgentActionFailed || got.Error == nil || got.Error.Code != "INVALID_ACTION" {
		t.Errorf("action = %s %+v, want FAILED INVALID_ACTION", got.Status, got.Error)
	}
	agentRun := f.agentRun(t)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Errorf("agent termination = %s, want INVALID_ACTION", agentTermination(agentRun))
	}
	if agentRun.PausedAt != nil {
		t.Errorf("agent run still paused (%v) after the deployed release", agentRun.PauseReason)
	}
	nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, f.run.ID)
	if nodeRun.Status != domain.NodeRunFailed || nodeRun.Error == nil || nodeRun.Error.Code != "INVALID_ACTION" {
		t.Errorf("agent node run = %s %+v, want FAILED INVALID_ACTION", nodeRun.Status, nodeRun.Error)
	}
	run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID)
	if run.Status != domain.RunFailed || run.Error == nil || run.Error.Code != "INVALID_ACTION" {
		t.Errorf("run = %s %+v, want FAILED with the termination's own INVALID_ACTION error", run.Status, run.Error)
	}

	// No release confirmation was recorded, so the verdict is INCOMPLETE and says why.
	if facts := f.releaseFacts(t); len(facts) != 0 {
		t.Errorf("release facts = %+v, want none from a round that failed", facts)
	}
	completion := completionOf(t, f.h, agentRunID)
	if completion == nil {
		t.Fatalf("agent run %s has no completion verdict", agentRunID)
	}
	if completion.Outcome != domain.CompletionIncomplete || completion.Goal != domain.AgentGoalFixAndDeploy {
		t.Errorf("verdict = %s for %s, want INCOMPLETE for FIX_AND_DEPLOY", completion.Outcome, completion.Goal)
	}
	missing := map[string]bool{}
	for _, item := range completion.Missing {
		missing[item.Code] = true
	}
	if len(missing) != 2 || !missing[runtime.MissingReleaseConfirmation] || !missing[runtime.MissingFinalResponse] {
		t.Errorf("missing = %+v, want exactly the release confirmation and the FINAL", completion.Missing)
	}
	passed := map[string]domain.CompletionCheck{}
	for _, check := range completion.Checks {
		if check.Passed {
			passed[check.Name] = check
		}
	}
	for _, name := range []string{runtime.CheckWorkspacePatch, runtime.CheckPatchVerified, runtime.CheckChangeRequest, runtime.CheckApproval} {
		if _, ok := passed[name]; !ok {
			t.Errorf("check %s did not pass; the change material was complete: %+v", name, completion.Checks)
		}
	}
	if refs := passed[runtime.CheckApproval].Refs; len(refs) != 1 || refs[0] != approved.Approval.ID {
		t.Errorf("approval check refs = %v, want the release's approval %s", refs, approved.Approval.ID)
	}

	// The target is free: the occupation was released as DEPLOYED with the round, and the
	// invariant that an ended Run's ACTIVE occupation has a query schedule holds trivially.
	occupations := f.occupations(t, action.ID)
	if len(occupations) != 1 || occupations[0].Status != domain.ReleaseOccupationReleased ||
		occupations[0].ReleaseReason == nil || *occupations[0].ReleaseReason != domain.ReleaseReasonDeployed {
		t.Errorf("occupations = %+v, want one RELEASED DEPLOYED", occupations)
	}
	if occupations[0].NextQueryAt != nil || occupations[0].DeployedUncheckedVersion != nil {
		t.Errorf("occupation = %+v, want no post-task query schedule and no unchecked version", occupations[0])
	}
	if _, found := f.activeOccupation(t); found {
		t.Error("the target is still occupied after the deployed release")
	}
	if n := endedRunOccupationViolations(t, f); n != 0 {
		t.Errorf("ended-run occupations without a query schedule = %d, want 0", n)
	}

	// Post-task reconciliation has nothing to settle: another pass sends production nothing.
	before := production.counts.count("GET", deploymentsPath+"/"+operationKey(attempts[0]))
	f.poll(t, f.reconciler())
	if after := production.counts.count("GET", deploymentsPath+"/"+operationKey(attempts[0])); after != before {
		t.Errorf("production queries of the settled operation = %d after another pass, want %d", after, before)
	}

	// Every state transition committed its Event, in contiguous seq order.
	events := f.events(t)
	assertContiguousSeq(t, events)
	if completed := f.eventsOf(t, domain.EventAgentActionCompleted, action.ID); len(completed) != 0 {
		t.Errorf("AGENT_ACTION_COMPLETED of the release = %+v, want none", completed)
	}
	failed := f.eventsOf(t, domain.EventAgentActionFailed, action.ID)
	if len(failed) != 1 || failed[0]["toolAttemptId"] != attempts[0].ID {
		t.Errorf("AGENT_ACTION_FAILED of the release = %+v, want one naming the SUCCEEDED Attempt", failed)
	}
	payload := agentLastEventPayload(t, events, domain.EventAgentCompletionEvaluated)
	if payload["outcome"] != string(domain.CompletionIncomplete) || payload["agentRunId"] != agentRunID {
		t.Errorf("AGENT_COMPLETION_EVALUATED payload = %v, want the INCOMPLETE verdict of %s", payload, agentRunID)
	}
	// The result transaction appended exactly these, in this order, after the deployment:
	// the clock resumes, then the Action, the Agent Run, the verdict, the NodeRun and the
	// Run end. The later reconciliation pass appended nothing.
	want := []domain.EventType{
		domain.EventAgentResumed, domain.EventAgentActionFailed, domain.EventAgentFailed,
		domain.EventAgentCompletionEvaluated, domain.EventNodeFailed, domain.EventRunFailed,
	}
	if len(events) < len(want) {
		t.Fatalf("events = %d, want at least %d", len(events), len(want))
	}
	for i, ev := range events[len(events)-len(want):] {
		if ev.Type != want[i] {
			t.Errorf("event %d of the result transaction = %s, want %s", i, ev.Type, want[i])
		}
	}

	// No production request was made while the Run row was locked.
	probed, lockedAt, errs := probe.report()
	if len(errs) != 0 {
		t.Fatalf("run lock probe errors: %v", errs)
	}
	if len(probed) == 0 {
		t.Fatal("no production request passed the run lock probe")
	}
	for _, call := range []string{"POST " + deploymentsPath, "GET " + deploymentsPath + "/" + key, "GET /v2/targets/" + releaseService + "/" + releaseEnvironment} {
		found := false
		for _, p := range probed {
			found = found || p == call
		}
		if !found {
			t.Errorf("production request %s did not pass the run lock probe; probed %v", call, probed)
		}
	}
	if len(lockedAt) != 0 {
		t.Errorf("production requests made while the run row was locked: %v", lockedAt)
	}

	// The probe is not vacuous: with the Run row locked it does report the lock.
	tx, err := f.h.pool.Begin(f.h.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(f.h.ctx) }()
	if _, err := tx.Exec(f.h.ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, f.run.ID); err != nil {
		t.Fatalf("lock the run row: %v", err)
	}
	if held, err := probe.runLockHeld(f.h.ctx, f.run.ID); err != nil || !held {
		t.Errorf("probe with the run row locked = %v, %v, want held", held, err)
	}
}
