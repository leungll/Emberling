//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
)

// A Run can end while its Agent still waits on a release: a parallel branch fails, the Run
// becomes FAILED, and no path of the live Agent will ever settle the release. These tests
// cover the takeover. The transaction that ends the Run gives every ACTIVE occupation its
// first post-task query, the live poll path stands down, and only the post-task
// reconciliation settles the target, from production's definite answer about the operation.

// failingSiblingType is the Node of the parallel branch a release test can add.
const failingSiblingType = "release_failing_sibling"

type failingSiblingExecutor struct{}

func (failingSiblingExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (failingSiblingExecutor) Execute(context.Context, registry.NodeInput, map[string]any) (registry.NodeResult, error) {
	return registry.NodeResult{}, errors.New("the parallel branch failed")
}

// failingSiblingRegistration is a side-effect-free Node that always fails. The default
// execution policy does not retry it, so its first failure ends the Run.
func failingSiblingRegistration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          failingSiblingType,
			DisplayName:   "Failing Sibling",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText}},
			ConfigSchema:  []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: failingSiblingExecutor{}},
	}
}

// claimSibling claims the READY sibling, which the single execution slot allows once the
// Agent NodeRun waits.
func (f *releaseFixture) claimSibling(t *testing.T) service.AdvanceOutcome {
	t.Helper()
	outcome := f.h.advance(f.run.ID)
	if !outcome.Claimed || outcome.NodeType != failingSiblingType {
		t.Fatalf("advance = %+v, want the sibling claimed", outcome)
	}
	return outcome
}

// failSibling runs the sibling and proves it ended the Run.
func (f *releaseFixture) failSibling(t *testing.T) {
	t.Helper()
	f.h.execute(f.claimSibling(t))
	if status := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID).Status; status != domain.RunFailed {
		t.Fatalf("run status after the sibling failed = %s, want FAILED", status)
	}
}

// waitOnRelease approves a release production keeps DEPLOYING and proves the Agent waits on
// it with the Run still alive.
func (f *releaseFixture) waitOnRelease(t *testing.T, workflowID string) domain.AgentAction {
	t.Helper()
	action := f.startToRelease(t, workflowID)
	f.production.server.PauseCompletion()
	f.approve(t, action)
	if got := f.action(t, action.ID); got.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("action = %s, want WAITING_CALLBACK", got.Status)
	}
	if got := f.nodeRun(t); got.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("agent node run = %s, want WAITING_CALLBACK", got.Status)
	}
	return action
}

// dueOccupations lists what the Reconciler discovers at the current time.
func (f *releaseFixture) dueOccupations(t *testing.T) []domain.ReleaseOccupation {
	t.Helper()
	var due []domain.ReleaseOccupation
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		due, err = tx.ReleaseOccupations().ListDueQueries(ctx, f.h.clock.Now(), 10)
		return err
	}); err != nil {
		t.Fatalf("list due queries: %v", err)
	}
	return due
}

// assertScheduledByRunEnd checks the takeover fact: the occupation is still ACTIVE and its
// first query is due at the moment the Run ended, so the next scan finds it.
func (f *releaseFixture) assertScheduledByRunEnd(t *testing.T, action domain.AgentAction) {
	t.Helper()
	occupation := f.occupationOf(t, action.ID)
	run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID)
	if occupation.Status != domain.ReleaseOccupationActive || occupation.QueryCount != 0 {
		t.Fatalf("occupation = %+v, want ACTIVE with no query claimed", occupation)
	}
	if occupation.NextQueryAt == nil || run.CompletedAt == nil || !occupation.NextQueryAt.Equal(*run.CompletedAt) {
		t.Fatalf("first query at %v, run ended at %v: want the schedule written by the transaction that ended the Run", occupation.NextQueryAt, run.CompletedAt)
	}
	due := f.dueOccupations(t)
	if len(due) != 1 || due[0].ID != occupation.ID {
		t.Fatalf("due occupations = %+v, want exactly %s", due, occupation.ID)
	}
}

// assertReleasedOnly checks that a post-task settlement touched the occupation alone: the
// ended Run gained no Event and the Action and its Attempt stayed as the Run left them.
func (f *releaseFixture) assertReleasedOnly(t *testing.T, action domain.AgentAction, reason domain.ReleaseReason, events int) domain.ReleaseOccupation {
	t.Helper()
	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != reason || occupation.NextQueryAt != nil {
		t.Fatalf("occupation = %+v, want RELEASED %s with no query left", occupation, reason)
	}
	if n := len(f.events(t)); n != events {
		t.Errorf("events = %d, want %d: the ended Run gets nothing", n, events)
	}
	if got := f.action(t, action.ID); got.Status != domain.AgentActionWaitingCallback {
		t.Errorf("action = %s, want it left WAITING_CALLBACK", got.Status)
	}
	if _, found := f.activeOccupation(t); found {
		t.Error("the target is still occupied")
	}
	if n := f.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests production received = %d, want one: nothing is sent again", n)
	}
	return occupation
}

// Contract case: a sibling fails while the Agent waits on its release. The Run ends through
// the ordinary NodeRun failure, the Agent's own facts stay as they were, and the failure
// transaction left a query schedule on the occupation with no Event of its own.
func TestReleaseAfterRunEnd_SiblingFailsWhileAgentWaits_OccupationScheduled(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{failingSibling: true})
	action := f.waitOnRelease(t, "wf_after_end_scheduled")
	eventsBefore := len(f.events(t))

	f.failSibling(t)

	f.assertScheduledByRunEnd(t, action)
	if got := f.nodeRun(t); got.Status != domain.NodeRunWaitingCallback {
		t.Errorf("agent node run = %s, want WAITING_CALLBACK: the Runtime has no cancellation", got.Status)
	}
	// NODE_STARTED, NODE_FAILED and RUN_FAILED are the whole Event trail of the failure.
	if n := len(f.events(t)) - eventsBefore; n != 3 {
		t.Errorf("events the failure appended = %d, want 3: scheduling a query writes none", n)
	}
}

// Contract case: the Run ends while a person watches the release. The schedule is on the
// occupation from the failure, the wait cannot be stopped any more, and its expiry leaves
// the schedule alone.
func TestReleaseAfterRunEnd_SiblingFailsDuringHumanWait_OccupationScheduled(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds, failingSibling: true})
	action := f.waitOnRelease(t, "wf_after_end_human_wait")
	f.h.clock.Advance(time.Duration(humanWaitBounds.confirmTimeoutMs) * time.Millisecond)
	if err := f.h.svc.OpenReleaseHumanWait(f.h.ctx, f.attempts(t, action.ID)[0].ID); err != nil {
		t.Fatalf("open human wait: %v", err)
	}
	wait, found := f.humanWait(t, action.ID)
	if !found || wait.Status != domain.HumanWaitOpen {
		t.Fatalf("human wait = %+v (%v), want OPEN", wait, found)
	}

	f.failSibling(t)
	f.assertScheduledByRunEnd(t, action)

	var conflict *service.HumanWaitConflictError
	if _, err := f.h.svc.StopHumanWait(f.h.ctx, wait.ID, stopOperator); !errors.As(err, &conflict) || !conflict.RunEnded {
		t.Fatalf("stop of a wait whose Run ended = %v, want a RunEnded conflict", err)
	}
	f.h.clock.Advance(time.Duration(humanWaitBounds.humanWaitTimeoutMs) * time.Millisecond)
	first := f.occupationOf(t, action.ID).NextQueryAt
	if err := f.h.svc.ExpireHumanWait(f.h.ctx, wait.ID); err != nil {
		t.Fatalf("expire human wait: %v", err)
	}
	if after := f.occupationOf(t, action.ID).NextQueryAt; after == nil || !after.Equal(*first) {
		t.Errorf("first query moved from %v to %v by the wait's expiry", first, after)
	}
}

// Contract case: production reports DEPLOYED after the Run ended. The poll that is due
// stands down, so production is asked once, and the post-task reconciliation frees the
// target with the live version recorded as unchecked. A later task can release again.
func TestReleaseAfterRunEnd_Deployed_ReleasedUncheckedAndTargetReusable(t *testing.T) {
	a := newReleaseFixture(t, releaseFixtureOptions{failingSibling: true})
	action := a.waitOnRelease(t, "wf_after_end_deployed")
	a.failSibling(t)
	a.production.server.ResumeCompletion()
	events := len(a.events(t))
	key := a.occupationOf(t, action.ID).OperationKey
	queries := a.production.counts.count("GET", deploymentsPath+"/"+key)

	a.h.clock.Advance(releasePollStep)
	report := agentRunOnce(a.h, a.reconciler())
	if report.ToolPollsClaimed != 0 || report.OccupationsReleased != 1 {
		t.Fatalf("pass claimed %d live polls and released %d occupations, want 0 and 1", report.ToolPollsClaimed, report.OccupationsReleased)
	}

	occupation := a.assertReleasedOnly(t, action, domain.ReleaseReasonDeployed, events)
	if occupation.DeployedUncheckedVersion == nil || *occupation.DeployedUncheckedVersion == "" {
		t.Errorf("occupation = %+v, want the live version recorded as unchecked", occupation)
	}
	attempt := a.attempts(t, action.ID)[0]
	if attempt.Status != domain.ToolAttemptDispatched || attempt.NextPollAt != nil {
		t.Errorf("attempt = %s next poll %v, want it left DISPATCHED with no poll scheduled", attempt.Status, attempt.NextPollAt)
	}
	if n := a.production.counts.count("GET", deploymentsPath+"/"+key) - queries; n != 1 {
		t.Errorf("production queries = %d, want one", n)
	}
	if facts := a.releaseFacts(t); len(facts) != 0 {
		t.Errorf("release facts = %+v, want none: the ended task's conclusion stands", facts)
	}

	b := a.secondTask(t, service.ApprovalHooks{})
	next := b.startToRelease(t, "wf_after_end_deployed_next")
	b.approve(t, next)
	b.poll(t, b.reconciler())
	b.assertDeployedOnce(t, next, domain.ReleaseOperationKey(next.ID, 1))
	b.finish(t)
}

// refusingProduction answers an armed operation query with production's definite 409.
type refusingProduction struct {
	inner http.RoundTripper
	armed atomic.Bool
}

func (r *refusingProduction) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.armed.Load() && req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, deploymentsPath+"/") {
		return &http.Response{
			StatusCode: http.StatusConflict, Request: req, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"code":"RELEASE_WINDOW_CLOSED"}`)),
		}, nil
	}
	return r.inner.RoundTrip(req)
}

// Contract case: production refuses the operation definitely after the Run ended. The
// answer the live path would hand back to the Agent frees the target here instead; a
// refusal is a definite result, so it must not be waited on as if it were no answer.
func TestReleaseAfterRunEnd_Refused_ReleasedAsRefused(t *testing.T) {
	production := newProductionFixture(t)
	refusal := &refusingProduction{inner: production.srv.Client().Transport}
	f := newReleaseFixture(t, releaseFixtureOptions{
		production: production, client: &http.Client{Transport: refusal}, failingSibling: true,
	})
	action := f.waitOnRelease(t, "wf_after_end_refused")
	f.failSibling(t)
	events := len(f.events(t))

	refusal.armed.Store(true)
	if report := f.postReconcile(t); report.OccupationsReleased != 1 {
		t.Fatalf("pass released %d occupations, want 1", report.OccupationsReleased)
	}
	occupation := f.assertReleasedOnly(t, action, domain.ReleaseReasonRefused, events)
	if occupation.DeployedUncheckedVersion != nil {
		t.Errorf("unchecked version = %q, want none: nothing was deployed", *occupation.DeployedUncheckedVersion)
	}
}

// Contract case: the outcome was unknown when the Run ended and production never got the
// operation. Past the void grace only the post-task reconciliation asks for the void, the
// confirmed void frees the target, and the operation is not sent again.
func TestReleaseAfterRunEnd_Voided_ReleasedAsVoidedWithoutResend(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds, failingSibling: true})
	action := f.startToRelease(t, "wf_after_end_voided")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	f.approve(t, action)
	f.assertOutcomeUnknown(t, action)
	f.failSibling(t)
	f.assertScheduledByRunEnd(t, action)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})
	events := len(f.events(t))

	f.h.clock.Advance(time.Duration(unknownOutcomeBounds.voidGraceMs) * time.Millisecond)
	if report := f.postReconcile(t); report.OccupationsReleased != 1 || report.ToolPollsClaimed != 0 {
		t.Fatalf("pass claimed %d live polls and released %d occupations, want 0 and 1", report.ToolPollsClaimed, report.OccupationsReleased)
	}
	occupation := f.assertReleasedOnly(t, action, domain.ReleaseReasonVoided, events)
	if status := f.production.operationStatus(t, occupation.OperationKey); status != "VOIDED" {
		t.Errorf("production operation = %q, want VOIDED", status)
	}
	if attempts := f.attempts(t, action.ID); len(attempts) != 1 {
		t.Errorf("attempts = %d, want one: a void after the Run ended starts no new release", len(attempts))
	}
}

// Contract case: production gives no answer. The target stays occupied however often the
// reconciliation asks, the schedule keeps moving, nothing is sent again, and another
// task's release is refused until a definite answer arrives.
func TestReleaseAfterRunEnd_StillUnknown_KeepsTargetAndKeepsAsking(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds, failingSibling: true})
	action := f.startToRelease(t, "wf_after_end_unknown")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	f.approve(t, action)
	f.failSibling(t)

	for pass := 1; pass <= 3; pass++ {
		report := f.reconcileWhenDue(t, action.ID)
		if report.OccupationQueriesClaimed != 1 || report.OccupationsReleased != 0 {
			t.Fatalf("pass %d: claimed %d released %d, want one query and nothing released", pass, report.OccupationQueriesClaimed, report.OccupationsReleased)
		}
		occupation := f.occupationOf(t, action.ID)
		if occupation.Status != domain.ReleaseOccupationActive || occupation.QueryCount != pass || occupation.NextQueryAt == nil || !occupation.NextQueryAt.After(f.h.clock.Now()) {
			t.Fatalf("pass %d: occupation = %+v, want ACTIVE with the next query later", pass, occupation)
		}
	}
	if n := f.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests production received = %d, want only the unanswered one", n)
	}

	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})
	b := f.secondTask(t, service.ApprovalHooks{})
	b.start(t, "wf_after_end_unknown_next", verifyBaselineCall(), func() mockmodel.Scenario {
		return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), b.instructionsBasis(t)))
	}, b.releaseCall(t))
	b.deliverVerification(t, true)
	b.assertReturnedBeforeAttempt(t, b.actionOfTurn(t, releaseTurn), releaseTurn, domain.ReturnTargetOccupied)
	b.finish(t)
}

// Contract case: the process dies right after the transaction that ended the Run. Nothing
// but committed facts survives, and the restarted Reconciler finds and settles the
// occupation from the schedule that transaction wrote.
func TestReleaseAfterRunEnd_CrashAfterRunEndCommit_RestartFindsOccupation(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{failingSibling: true})
	action := f.waitOnRelease(t, "wf_after_end_crash")
	f.failSibling(t)
	events := len(f.events(t))

	restarted := f.restart(t, service.PollHooks{})
	restarted.production.server.ResumeCompletion()
	if due := restarted.dueOccupations(t); len(due) != 1 {
		t.Fatalf("due occupations after the restart = %+v, want one", due)
	}
	if report := restarted.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 1 {
		t.Fatalf("reconciliation after the restart released %d occupations, want 1", report.OccupationsReleased)
	}
	restarted.assertReleasedOnly(t, action, domain.ReleaseReasonDeployed, events)
}

// errRunEndWrite stands for a failure of the status write that ends a Run.
var errRunEndWrite = errors.New("the run status write failed")

// failingRunEndUoW fails the armed terminal status write of a Run. The status write comes
// after every other write of the transaction, so its failure proves those roll back.
type failingRunEndUoW struct {
	store.UnitOfWork
	armed *atomic.Bool
}

func (u failingRunEndUoW) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return u.UnitOfWork.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return fn(ctx, failingRunEndTx{Tx: tx, armed: u.armed})
	})
}

type failingRunEndTx struct {
	store.Tx
	armed *atomic.Bool
}

func (t failingRunEndTx) Runs() store.RunRepository {
	return failingRunEndRuns{RunRepository: t.Tx.Runs(), armed: t.armed}
}

type failingRunEndRuns struct {
	store.RunRepository
	armed *atomic.Bool
}

func (r failingRunEndRuns) UpdateAggregate(ctx context.Context, lock *store.RunLock, status domain.RunStatus, now time.Time) error {
	if status.IsTerminal() && r.armed.CompareAndSwap(true, false) {
		return errRunEndWrite
	}
	return r.RunRepository.UpdateAggregate(ctx, lock, status, now)
}

// Contract case: the schedule and the Run's end are one transaction. When the terminal
// status write fails, neither the failure nor the schedule survives, and the retry
// commits both.
func TestReleaseAfterRunEnd_RunEndWriteFails_RollsBackSchedule(t *testing.T) {
	var armed atomic.Bool
	f := newReleaseFixture(t, releaseFixtureOptions{failingSibling: true, wrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
		return failingRunEndUoW{UnitOfWork: inner, armed: &armed}
	}})
	action := f.waitOnRelease(t, "wf_after_end_rollback")
	outcome := f.claimSibling(t)

	armed.Store(true)
	if err := f.h.svc.Execute(f.h.ctx, outcome); !errors.Is(err, errRunEndWrite) {
		t.Fatalf("execute = %v, want the injected status write failure", err)
	}
	if status := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID).Status; status.IsTerminal() {
		t.Fatalf("run status = %s after the rollback", status)
	}
	if occupation := f.occupationOf(t, action.ID); occupation.NextQueryAt != nil {
		t.Fatalf("first query at %v survived a rolled-back Run end", occupation.NextQueryAt)
	}

	f.h.execute(outcome)
	if status := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID).Status; status != domain.RunFailed {
		t.Fatalf("run status after the retry = %s, want FAILED", status)
	}
	f.assertScheduledByRunEnd(t, action)
}

// Contract case: a poll claimed while the Run was alive gets DEPLOYED after the Run ended.
// Its conclusion stands down under the Run lock and writes nothing on the ended Run: the
// answer reaches the target through the post-task reconciliation alone.
func TestReleaseAfterRunEnd_PollInFlightWhenRunEnds_ConclusionHandedOver(t *testing.T) {
	var (
		f     *releaseFixture
		armed atomic.Bool
	)
	hooks := service.PollHooks{AfterClaim: func(context.Context, string) error {
		if armed.CompareAndSwap(true, false) {
			f.failSibling(t)
		}
		return nil
	}}
	f = newReleaseFixture(t, releaseFixtureOptions{pollHooks: hooks, failingSibling: true})
	action := f.waitOnRelease(t, "wf_after_end_poll_in_flight")
	f.production.server.ResumeCompletion()
	attempt := f.attempts(t, action.ID)[0]

	armed.Store(true)
	f.h.clock.Advance(releasePollStep)
	outcome, err := f.h.svc.PollToolAttempt(f.h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: action.ID})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if outcome.Kind != service.PollAttemptDuplicate {
		t.Fatalf("poll outcome = %+v, want a duplicate: the Run ended before the answer", outcome)
	}
	events := len(f.events(t))
	if got := f.attempts(t, action.ID)[0]; got.Status != domain.ToolAttemptDispatched {
		t.Errorf("attempt = %s, want it left DISPATCHED", got.Status)
	}
	if occupation := f.occupationOf(t, action.ID); occupation.Status != domain.ReleaseOccupationActive || occupation.NextQueryAt == nil {
		t.Fatalf("occupation = %+v, want ACTIVE with the schedule the Run's end wrote", occupation)
	}

	if report := f.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 1 {
		t.Fatalf("reconciliation released %d occupations, want 1", report.OccupationsReleased)
	}
	f.assertReleasedOnly(t, action, domain.ReleaseReasonDeployed, events)
}

// Contract case: two scanners take the same due occupation of a Run a sibling ended, and a
// third call repeats the work afterwards. Both scanners queue on the Run lock, the
// conditional claim lets one of them ask production, and the repeat finds nothing due.
func TestReleaseAfterRunEnd_ConcurrentAndDuplicateReconcile_OneWinner(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{failingSibling: true})
	action := f.waitOnRelease(t, "wf_after_end_scanners")
	f.failSibling(t)
	occupation := f.occupationOf(t, action.ID)
	path := deploymentsPath + "/" + occupation.OperationKey
	before := f.production.counts.count("GET", path)

	gate, err := lockRunRow(f.h.ctx, f.h.pool, f.run.ID)
	if err != nil {
		t.Fatalf("take the run lock gate: %v", err)
	}
	t.Cleanup(func() { gate.close(context.Background()) })
	outcomes := make(chan service.ReleaseReconcileOutcome, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			outcome, err := f.h.svc.ReconcileReleaseOccupation(f.h.ctx, occupation.ID)
			outcomes <- outcome
			errs <- err
		}()
	}
	gate.awaitWaiters(t, 2)
	gate.open(t)
	claimed := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Errorf("reconcile: %v", err)
		}
		if (<-outcomes).Claimed {
			claimed++
		}
	}
	if claimed != 1 {
		t.Errorf("claimed queries = %d, want exactly 1", claimed)
	}

	repeat, err := f.h.svc.ReconcileReleaseOccupation(f.h.ctx, occupation.ID)
	if err != nil {
		t.Fatalf("repeated reconcile: %v", err)
	}
	if repeat.Claimed {
		t.Errorf("repeated reconcile = %+v, want nothing claimed before the next due time", repeat)
	}
	if n := f.production.counts.count("GET", path) - before; n != 1 {
		t.Errorf("production queries of %s = %d, want exactly 1", occupation.OperationKey, n)
	}
	if after := f.occupationOf(t, action.ID); after.QueryCount != 1 {
		t.Errorf("query count = %d, want 1", after.QueryCount)
	}
}
