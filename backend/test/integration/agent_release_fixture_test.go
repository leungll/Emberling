//go:build integration

// The release fixture: one Backend with the workspace Tools, verify_patch against the
// runner stub, create_change_request and deploy_change against an in-process mock
// production, whose Agent is bound to the example repository, may deploy to one frozen
// target and bounds its release waits. Every release test drives the real story up to the
// release call: the baseline is verified, a change request describes it, and the Agent
// asks to release that request, which needs a person's approval.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
	"github.com/leungll/Emberling/backend/internal/tools/workspace"
)

// The frozen deploy target of every release Definition and its occupation key.
const (
	releaseService     = "orders-api"
	releaseEnvironment = "staging"
	releaseTargetKey   = releaseService + "/" + releaseEnvironment
	// releaseTurn is the Turn whose Action is the release call: verify, describe, release.
	releaseTurn = 3
	// releasePollStep is longer than the Tool's poll interval, so one advance makes the
	// next poll due.
	releasePollStep = 3 * time.Second
)

// releaseBounds are the release waits a release Definition configures, in milliseconds;
// zero leaves the bound out of the Definition.
type releaseBounds struct {
	confirmTimeoutMs   int
	voidGraceMs        int
	maxReleaseRetries  int
	humanWaitTimeoutMs int
}

// defaultReleaseBounds keeps every wait far away, so a test that does not drive one never
// meets it.
var defaultReleaseBounds = releaseBounds{confirmTimeoutMs: 3_600_000, voidGraceMs: 3_600_000, maxReleaseRetries: 1, humanWaitTimeoutMs: 3_600_000}

// countingProduction counts the requests the mock production receives, by method and path
// prefix, so a test can prove that a lost request was made again or that nothing was sent.
type countingProduction struct {
	inner http.Handler
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingProduction) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	c.mu.Lock()
	c.calls[key]++
	c.mu.Unlock()
	c.inner.ServeHTTP(w, r)
}

func (c *countingProduction) count(method, path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[method+" "+path]
}

// productionFixture is one mock production shared by every Backend of a test.
type productionFixture struct {
	server  *mockproduction.Server
	counts  *countingProduction
	srv     *httptest.Server
	release *mockproduction.Record
}

func newProductionFixture(t *testing.T, opts ...mockproduction.Option) *productionFixture {
	t.Helper()
	record, err := mockproduction.OpenRecord(filepath.Join(t.TempDir(), "production.jsonl"))
	if err != nil {
		t.Fatalf("open production record: %v", err)
	}
	server := mockproduction.NewServer(append([]mockproduction.Option{mockproduction.WithTestControls(record), mockproduction.WithCompletionDelay(0)}, opts...)...)
	counts := &countingProduction{inner: server, calls: map[string]int{}}
	srv := httptest.NewServer(counts)
	t.Cleanup(func() { server.StopHolding(); srv.Close(); _ = record.Close() })
	return &productionFixture{server: server, counts: counts, srv: srv, release: record}
}

// post sends a control or production request of a test's own, such as a late arrival of an
// operation the Runtime voided.
func (p *productionFixture) post(t *testing.T, path, body string) int {
	t.Helper()
	resp, err := http.Post(p.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// target reads the deploy target back as production reports it; ok is false before any
// deployment reached it.
func (p *productionFixture) target(t *testing.T) (map[string]any, bool) {
	t.Helper()
	resp, err := http.Get(p.srv.URL + "/v2/targets/" + releaseService + "/" + releaseEnvironment)
	if err != nil {
		t.Fatalf("GET target: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode target: %v", err)
	}
	return out, true
}

// releaseFixtureOptions build one Backend of the release story. pool and clock, when set,
// make it a restarted process over an earlier Backend's committed facts.
type releaseFixtureOptions struct {
	bounds        releaseBounds
	production    *productionFixture
	pool          *pgxpool.Pool
	clock         *execClock
	runnerID      string
	pollHooks     service.PollHooks
	approvalHooks service.ApprovalHooks
	// client, when set, replaces the release Tool's HTTP client.
	client *http.Client
	// stateSchema, when set, is the Agent node's State Schema.
	stateSchema string
}

type releaseFixture struct {
	h           *agentHarness
	repo        *workspaceRepo
	stub        *verifyRunnerStub
	artifacts   *asset.ArtifactStore
	production  *productionFixture
	bounds      releaseBounds
	stateSchema string
	run         domain.Run
	outcome     service.AdvanceOutcome
}

func newReleaseFixture(t *testing.T, opts releaseFixtureOptions) *releaseFixture {
	t.Helper()
	if opts.production == nil {
		opts.production = newProductionFixture(t)
	}
	if opts.bounds == (releaseBounds{}) {
		opts.bounds = defaultReleaseBounds
	}
	stub := newVerifyRunnerStub()
	// Several tasks share one database in a test; each verification needs its own id.
	stub.verificationID = store.NewUUIDGenerator().NewID("verify")
	runner := httptest.NewServer(stub)
	t.Cleanup(runner.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	repo := newWorkspaceRepo(t)
	client := opts.client
	if client == nil {
		client = opts.production.srv.Client()
	}
	tools := append(workspace.Registrations(workspace.NewLocalGit(map[string]string{wsRepositoryID: repo.path})),
		verifypatch.Registration(runner.URL, runner.Client(), artifacts),
		changerequest.Registration(artifacts),
		deployrelease.Registration(opts.production.srv.URL, client),
	)
	h := newAgentHarness(t, agentHarnessOptions{
		Tools: tools, Nodes: []registry.NodeRegistration{approvalTaskInputRegistration()},
		Pool: opts.pool, Clock: opts.clock, RunnerID: opts.runnerID,
		PollHooks: opts.pollHooks, ApprovalHooks: opts.approvalHooks,
		WorkspaceRepositories: []string{wsRepositoryID},
	})
	return &releaseFixture{h: h, repo: repo, stub: stub, artifacts: artifacts, production: opts.production, bounds: opts.bounds, stateSchema: opts.stateSchema}
}

// restart is a new Backend over the fixture's committed facts and clock, with a new runner
// identity and nothing carried over in memory; the mock production is the same one.
func (f *releaseFixture) restart(t *testing.T, pollHooks service.PollHooks) *releaseFixture {
	t.Helper()
	restarted := newReleaseFixture(t, releaseFixtureOptions{
		bounds: f.bounds, production: f.production, pool: f.h.pool, clock: f.h.clock, pollHooks: pollHooks, stateSchema: f.stateSchema,
	})
	restarted.run, restarted.outcome = f.run, f.outcome
	restarted.h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
	}
	return restarted
}

// definition is the release story's graph: a task input carrying the FIX_AND_DEPLOY goal,
// the Agent bound to the repository with the frozen deploy target and the release bounds,
// and a text output.
func (f *releaseFixture) definition(workflowID string) domain.Definition {
	tools := append(append([]string{}, wsAllTools...), verifypatch.ToolName, changerequest.ToolName, deployrelease.ToolName)
	config := map[string]any{
		"instructions":      agentInstructions,
		"modelId":           mockmodel.ModelID,
		"allowedTools":      tools,
		"maxTurns":          wsMaxTurns,
		"timeoutMs":         600000,
		"goalPointer":       "/goal",
		"approvalTimeoutMs": approvalWaitMs,
		"workspace": map[string]any{
			"repositoryId": wsRepositoryID,
			"baseCommit":   f.repo.commit,
			"allowedPaths": []string{"go.mod", "internal/auth/"},
			"deployTarget": map[string]string{"service": releaseService, "environment": releaseEnvironment},
		},
	}
	if f.stateSchema != "" {
		config["stateSchema"] = json.RawMessage(f.stateSchema)
	}
	for key, value := range map[string]int{
		"releaseConfirmTimeoutMs": f.bounds.confirmTimeoutMs, "voidGraceMs": f.bounds.voidGraceMs,
		"maxReleaseRetries": f.bounds.maxReleaseRetries, "humanWaitTimeoutMs": f.bounds.humanWaitTimeoutMs,
	} {
		if value > 0 {
			config[key] = value
		}
	}
	encoded, _ := json.Marshal(config)
	return domain.Definition{
		WorkflowID: workflowID, Version: 1, Name: "Release", Description: "task input -> agent -> text_output",
		Nodes: []domain.Node{
			{ID: "node_input", Type: approvalTaskInputType, Name: "Task",
				Config: json.RawMessage(`{"task":{"goal":"FIX_AND_DEPLOY","question":"` + agentQuestion + `"}}`)},
			{ID: "node_agent", Type: "agent", Name: "Agent", Config: encoded},
			{ID: "node_output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_input", SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
			{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_output", TargetHandle: "text"},
		},
	}
}

// start saves the Definition, creates the Run, claims the Agent node and scripts the
// model: the given calls in order, then FINAL. The first Turn is executed and every Turn
// the loop commits is drained.
func (f *releaseFixture) start(t *testing.T, workflowID string, calls ...crCall) {
	t.Helper()
	saved := f.h.saveDefinition(f.definition(workflowID))
	f.run = f.h.createRun(saved.WorkflowID, saved.Version, `{}`)
	f.outcome = f.h.claimAgentNode(f.run.ID)
	var turn atomic.Int32
	f.h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		n := int(turn.Add(1))
		if n > len(calls) {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
		call := calls[n-1]()
		call.Kind = mockmodel.ScenarioToolCall
		return &call
	}
	f.h.execute(f.outcome)
}

// releaseCall names, when the model is asked, the one change request the Agent Run holds.
func (f *releaseFixture) releaseCall(t *testing.T) crCall {
	return func() mockmodel.Scenario {
		requests := changeRequestsOf(t, f.h, f.agentRunID(t))
		if len(requests) != 1 {
			t.Fatalf("change requests when the release is asked = %d, want one", len(requests))
		}
		return wsCall(deployrelease.ToolName, map[string]any{"changeRequestId": requests[0].ID})
	}
}

// startToRelease drives the story to the release call waiting on a person: verify the
// baseline, describe it, release it. It returns the WAITING_APPROVAL release Action.
func (f *releaseFixture) startToRelease(t *testing.T, workflowID string, between ...crCall) domain.AgentAction {
	t.Helper()
	return f.startToReleaseWith(t, workflowID, f.releaseCall(t), between...)
}

// startToReleaseWith is startToRelease with the release Decision the given call scripts.
func (f *releaseFixture) startToReleaseWith(t *testing.T, workflowID string, release crCall, between ...crCall) domain.AgentAction {
	t.Helper()
	calls := []crCall{verifyBaselineCall(), func() mockmodel.Scenario {
		return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), f.instructionsBasis(t)))
	}}
	calls = append(calls, between...)
	calls = append(calls, release)
	f.start(t, workflowID, calls...)
	f.deliverVerification(t, true)
	action := f.actionOfTurn(t, releaseTurn+len(between))
	if action.Status != domain.AgentActionWaitingApproval {
		t.Fatalf("release action = %s %+v, want WAITING_APPROVAL", action.Status, action.Error)
	}
	return action
}

func (f *releaseFixture) agentRunID(t *testing.T) string {
	t.Helper()
	return factAgentRunID(t, f.h, f.outcome)
}

func (f *releaseFixture) instructionsBasis(t *testing.T) map[string]any {
	t.Helper()
	return basisItem("The task instructions require the mirrored fixed version.", 1, f.agentRunID(t))
}

func (f *releaseFixture) deliverVerification(t *testing.T, passed bool) {
	t.Helper()
	token, _ := f.stub.dispatched(t)
	delivered, err := f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{
		Token: token, ExternalTaskID: f.stub.externalTaskID(),
		Payload: verifySucceededPayload(f.repo.commit, baselineDigest(), "sha256:verified", passed),
	})
	if err != nil || !delivered.Accepted || delivered.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns after the verification: %v", err)
	}
}

func (f *releaseFixture) actionOfTurn(t *testing.T, turnNo int) domain.AgentAction {
	t.Helper()
	return wsActionOfTurn(t, f.h, f.agentRunID(t), turnNo)
}

func (f *releaseFixture) action(t *testing.T, actionID string) domain.AgentAction {
	t.Helper()
	var action domain.AgentAction
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		action, err = tx.AgentActions().Get(ctx, actionID)
		return err
	}); err != nil {
		t.Fatalf("get action %s: %v", actionID, err)
	}
	return action
}

func (f *releaseFixture) agentRun(t *testing.T) domain.AgentRun {
	t.Helper()
	agentRun, ok := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run of node run %s not found", f.outcome.NodeRunID)
	}
	return agentRun
}

// approve decides the release approval and drains the worker call the decision enqueued,
// which is the dispatch to production.
func (f *releaseFixture) approve(t *testing.T, action domain.AgentAction) service.DecideApprovalOutcome {
	t.Helper()
	outcome := f.decide(t, action)
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain after approval: %v", err)
	}
	return outcome
}

// decide approves without draining: the approved call stays in the queue, as it does in a
// process that dies before a worker takes it.
func (f *releaseFixture) decide(t *testing.T, action domain.AgentAction) service.DecideApprovalOutcome {
	t.Helper()
	approval := approvalOfAction(f.h.ctx, t, f.h.uow, action.ID)
	outcome, err := f.h.svc.DecideApproval(f.h.ctx, service.DecideApproval{
		ApprovalID: approval.ID, Decision: domain.ApprovalApproved, DecidedBy: "release-operator",
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return outcome
}

// dropQueuedActions forgets the approved call the queue holds: the process died right after
// the approval committed, before any worker made the call.
func (f *releaseFixture) dropQueuedActions() {
	f.h.turns.mu.Lock()
	defer f.h.turns.mu.Unlock()
	f.h.turns.pendingActions = nil
}

func (f *releaseFixture) reconciler() *reconciler.Reconciler {
	return agentReconciler(f.h)
}

// poll advances the clock past the poll interval and runs one reconciliation pass, which
// is how a release learns production's answer.
func (f *releaseFixture) poll(t *testing.T, rec *reconciler.Reconciler) reconciler.Report {
	t.Helper()
	f.h.clock.Advance(releasePollStep)
	report := agentRunOnce(f.h, rec)
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns after the poll: %v", err)
	}
	return report
}

func (f *releaseFixture) attempts(t *testing.T, actionID string) []domain.ToolAttempt {
	t.Helper()
	return agentToolAttempts(f.h.ctx, t, f.h.uow, actionID)
}

func (f *releaseFixture) occupations(t *testing.T, actionID string) []domain.ReleaseOccupation {
	t.Helper()
	var out []domain.ReleaseOccupation
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.ReleaseOccupations().ListByActionID(ctx, actionID)
		return err
	}); err != nil {
		t.Fatalf("list occupations: %v", err)
	}
	return out
}

// activeOccupation returns the ACTIVE occupation of the target, or false when it is free.
func (f *releaseFixture) activeOccupation(t *testing.T) (domain.ReleaseOccupation, bool) {
	t.Helper()
	var occupation domain.ReleaseOccupation
	found := true
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		occupation, err = tx.ReleaseOccupations().GetActiveByTarget(ctx, releaseTargetKey)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("active occupation: %v", err)
	}
	return occupation, found
}

func (f *releaseFixture) humanWait(t *testing.T, actionID string) (domain.HumanWait, bool) {
	t.Helper()
	var wait domain.HumanWait
	found := true
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		wait, err = tx.HumanWaits().GetByActionID(ctx, actionID)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("human wait: %v", err)
	}
	return wait, found
}

func (f *releaseFixture) bindings(t *testing.T, attemptID string) []domain.CallbackBinding {
	t.Helper()
	return agentToolBindings(t, f.h, attemptID)
}

func (f *releaseFixture) events(t *testing.T) []domain.Event {
	t.Helper()
	return listEvents(f.h.ctx, t, f.h.uow, f.run.ID)
}

// eventsOf returns the payloads of every Event of typ about the Action.
func (f *releaseFixture) eventsOf(t *testing.T, typ domain.EventType, actionID string) []map[string]any {
	t.Helper()
	return agentPayloadsFor(t, f.events(t), typ, "actionId", actionID)
}

// releaseFacts returns the release_confirmed facts of the Agent Run.
func (f *releaseFixture) releaseFacts(t *testing.T) []domain.ExecutionFact {
	t.Helper()
	var out []domain.ExecutionFact
	for _, fact := range listFactsByAgentRun(f.h.ctx, t, f.h.uow, f.agentRunID(t)) {
		if fact.FactType == deployrelease.FactType {
			out = append(out, fact)
		}
	}
	return out
}

// toolMessage returns the tool message the round of the Action appended.
func (f *releaseFixture) toolMessage(t *testing.T, turnNo int, actionID string) map[string]any {
	t.Helper()
	return wsToolMessage(t, f.h, f.agentRunID(t), turnNo, actionID)
}

// operationKey is the production-side key of the Attempt.
func operationKey(attempt domain.ToolAttempt) string {
	if attempt.OperationKey == nil {
		return ""
	}
	return *attempt.OperationKey
}

// finish drains the Turns the settlement created and ends the Run, then checks the Agent
// answered FINAL and the Run completed.
func (f *releaseFixture) finish(t *testing.T) {
	t.Helper()
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}
	agentDrive(f.h, f.run.ID)
	agentRun := f.agentRun(t)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %s, want FINAL_RESPONSE", agentTermination(agentRun))
	}
	if run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID); run.Status != domain.RunCompleted {
		t.Errorf("run = %s, want COMPLETED", run.Status)
	}
}

// releaseCallDeadline is the deadline of one release request as the release Tool's HTTP
// client saw it, with the operation it carried.
type releaseCallDeadline struct {
	operationKey string
	deadline     time.Time
	hasDeadline  bool
	expired      bool
}

// deadlineRecorder is the release Tool's HTTP transport in a test that asserts what bounds
// the release call: it records the context deadline of every release request before
// handing it to the real transport.
type deadlineRecorder struct {
	inner http.RoundTripper
	mu    sync.Mutex
	calls []releaseCallDeadline
}

func (r *deadlineRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && req.URL.Path == "/v2/deployments" {
		call := releaseCallDeadline{expired: req.Context().Err() != nil}
		call.deadline, call.hasDeadline = req.Context().Deadline()
		var body struct {
			OperationID string `json:"operationId"`
		}
		if req.GetBody != nil {
			if copied, err := req.GetBody(); err == nil {
				_ = json.NewDecoder(copied).Decode(&body)
				_ = copied.Close()
			}
		}
		call.operationKey = body.OperationID
		r.mu.Lock()
		r.calls = append(r.calls, call)
		r.mu.Unlock()
	}
	return r.inner.RoundTrip(req)
}

func (r *deadlineRecorder) releaseCalls() []releaseCallDeadline {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]releaseCallDeadline(nil), r.calls...)
}

// recordingClient returns an HTTP client for the production fixture whose release requests
// pass through a deadlineRecorder.
func (p *productionFixture) recordingClient() (*http.Client, *deadlineRecorder) {
	base := p.srv.Client()
	recorder := &deadlineRecorder{inner: base.Transport}
	return &http.Client{Transport: recorder}, recorder
}

// releaseCallWithPatch is releaseCall with a state patch on the release Decision.
func (f *releaseFixture) releaseCallWithPatch(t *testing.T, patch string) crCall {
	call := f.releaseCall(t)
	return func() mockmodel.Scenario {
		scenario := call()
		scenario.StatePatch = json.RawMessage(patch)
		return scenario
	}
}

// operationStatus is production's own status of one release operation, or "" when it does
// not know the operation.
func (p *productionFixture) operationStatus(t *testing.T, operationKey string) string {
	t.Helper()
	resp, err := http.Get(p.srv.URL + "/v2/deployments/" + operationKey)
	if err != nil {
		t.Fatalf("GET operation %s: %v", operationKey, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ""
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode operation %s: %v", operationKey, err)
	}
	return out.Status
}
