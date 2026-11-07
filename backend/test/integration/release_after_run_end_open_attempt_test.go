//go:build integration

package integration

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
)

// A Run can end while its release Attempt is still open: STARTED when no worker got to the
// call or the call gave up on the ended Run, DISPATCHED when production took it. No live
// path will ever close that Attempt, and it deliberately stays open: the post-task path
// writes the occupation alone, and an unknown answer writes only outcome_unknown_since. So
// the first unknown answer has to count on the open Attempt too, or the void grace would
// never start and the target would stay occupied for good.

// productionAnswers rewrites armed requests with a fixed response, leaving the rest to the
// real mock production.
type productionAnswers struct {
	inner   http.RoundTripper
	status  atomic.Int32
	body    atomic.Value
	matches func(*http.Request) bool
}

func (p *productionAnswers) arm(status int, body string) {
	p.body.Store(body)
	p.status.Store(int32(status))
}

func (p *productionAnswers) disarm() { p.status.Store(0) }

func (p *productionAnswers) RoundTrip(req *http.Request) (*http.Response, error) {
	if status := p.status.Load(); status != 0 && p.matches(req) {
		return &http.Response{
			StatusCode: int(status), Request: req, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(p.body.Load().(string))),
		}, nil
	}
	return p.inner.RoundTrip(req)
}

func operationQuery(req *http.Request) bool {
	return req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, deploymentsPath+"/")
}

func operationVoid(req *http.Request) bool {
	return req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, voidSuffix)
}

// assertVoidedAfterGrace drives the post-task path from production's first unknown answer:
// the answer starts the grace, nothing is voided before the grace ends, the confirmed void
// frees the target, and the Attempt keeps the status the Run left it with.
func (f *releaseFixture) assertVoidedAfterGrace(t *testing.T, action domain.AgentAction, wantStatus domain.ToolAttemptStatus) {
	t.Helper()
	events := len(f.events(t))
	key := f.occupationOf(t, action.ID).OperationKey
	actionBefore := f.action(t, action.ID)

	if report := f.reconcileWhenDue(t, action.ID); report.OccupationQueriesClaimed != 1 || report.OccupationsReleased != 0 {
		t.Fatalf("first query claimed %d released %d, want one query and nothing released", report.OccupationQueriesClaimed, report.OccupationsReleased)
	}
	first := f.attempts(t, action.ID)[0]
	if first.Status != wantStatus || first.OutcomeUnknownSince == nil {
		t.Fatalf("attempt = %s unknownSince %v, want %s with the first unknown answer recorded", first.Status, first.OutcomeUnknownSince, wantStatus)
	}
	if n := f.production.counts.count("POST", deploymentsPath+"/"+key+voidSuffix); n != 0 {
		t.Fatalf("void requests before the grace = %d, want none", n)
	}

	f.h.clock.Advance(time.Duration(unknownOutcomeBounds.voidGraceMs) * time.Millisecond)
	if report := f.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 1 {
		t.Fatalf("query after the grace released %d, want 1", report.OccupationsReleased)
	}

	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonVoided || occupation.NextQueryAt != nil {
		t.Fatalf("occupation = %+v, want RELEASED as VOIDED with no query left", occupation)
	}
	if n := len(f.events(t)); n != events {
		t.Errorf("events = %d, want %d: the ended Run gets nothing", n, events)
	}
	if got := f.action(t, action.ID); got.Status != actionBefore.Status {
		t.Errorf("action = %s, want it left %s", got.Status, actionBefore.Status)
	}
	if n := f.production.counts.count("POST", deploymentsPath+"/"+key+voidSuffix); n != 1 {
		t.Errorf("void requests = %d, want one", n)
	}
	if status := f.production.operationStatus(t, key); status != "VOIDED" {
		t.Errorf("production operation = %q, want VOIDED", status)
	}
	last := f.attempts(t, action.ID)
	if len(last) != 1 || last[0].Status != wantStatus || !last[0].OutcomeUnknownSince.Equal(*first.OutcomeUnknownSince) {
		t.Errorf("attempts = %+v, want one still %s keeping the first unknown time", last, wantStatus)
	}
}

// Contract case: the Run ends between the approval and the worker. Nothing ever reached
// production, and the STARTED Attempt must not hold the target for good.
func TestReleaseAfterRunEnd_StartedAttempt_NeverReachedProduction_Voided(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds, failingSibling: true})
	action := f.startToRelease(t, "wf_open_started")
	sibling := f.claimSibling(t)
	f.crashBeforeDispatch(t, action)
	f.h.execute(sibling)
	f.assertScheduledByRunEnd(t, action)

	f.assertVoidedAfterGrace(t, action, domain.ToolAttemptStarted)
	if n := f.production.counts.count("POST", deploymentsPath); n != 0 {
		t.Errorf("release requests = %d, want none", n)
	}
}

// Contract case: the dispatch was refused an answer and gave up on the ended Run. The
// settlement stands down under the Run lock, which leaves the Attempt STARTED.
func TestReleaseAfterRunEnd_DispatchGaveUpOnEndedRun_Voided(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds, failingSibling: true})
	action := f.startToRelease(t, "wf_open_gave_up")
	sibling := f.claimSibling(t)
	f.decide(t, action)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	f.h.execute(sibling)
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain the dispatch: %v", err)
	}
	if got := f.attempts(t, action.ID)[0]; got.Status != domain.ToolAttemptStarted || got.OutcomeUnknownSince != nil {
		t.Fatalf("attempt = %s unknownSince %v, want STARTED: the settlement stands down on an ended Run", got.Status, got.OutcomeUnknownSince)
	}
	if n := f.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Fatalf("release requests = %d, want the one that got no answer", n)
	}
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})

	f.assertVoidedAfterGrace(t, action, domain.ToolAttemptStarted)
}

// Contract case: production accepted the operation, the Run ended, and production then lost
// it. The Attempt was never unknown while the Run lived.
func TestReleaseAfterRunEnd_DispatchedThenLostByProduction_Voided(t *testing.T) {
	production := newProductionFixture(t, mockproduction.WithCompletionDelay(time.Hour))
	answers := &productionAnswers{inner: production.srv.Client().Transport, matches: operationQuery}
	f := newReleaseFixture(t, releaseFixtureOptions{
		production: production, client: &http.Client{Transport: answers}, bounds: unknownOutcomeBounds, failingSibling: true,
	})
	action := f.startToRelease(t, "wf_open_lost")
	f.approve(t, action)
	f.failSibling(t)
	if got := f.attempts(t, action.ID)[0]; got.OutcomeUnknownSince != nil {
		t.Fatalf("unknown since %v before production lost the operation, want none", got.OutcomeUnknownSince)
	}
	answers.arm(http.StatusNotFound, `{"code":"NOT_FOUND"}`)

	f.assertVoidedAfterGrace(t, action, domain.ToolAttemptDispatched)
}

// A refusal has to be production's definite answer about the operation. Refusing the void
// itself is not: only a confirmed void or a deployment settles the target.
func TestReleaseAfterRunEnd_VoidRejectedByProduction_KeepsTheTarget(t *testing.T) {
	production := newProductionFixture(t)
	answers := &productionAnswers{inner: production.srv.Client().Transport, matches: operationVoid}
	f := newReleaseFixture(t, releaseFixtureOptions{
		production: production, client: &http.Client{Transport: answers}, bounds: unknownOutcomeBounds, failingSibling: true,
	})
	action := f.startToRelease(t, "wf_open_void_rejected")
	sibling := f.claimSibling(t)
	f.crashBeforeDispatch(t, action)
	f.h.execute(sibling)
	if report := f.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 0 {
		t.Fatalf("first query released %d, want none", report.OccupationsReleased)
	}
	f.h.clock.Advance(time.Duration(unknownOutcomeBounds.voidGraceMs) * time.Millisecond)

	// Production can turn a void down because the operation is in flight. That says
	// nothing about the operation, so it only moves the next query.
	answers.arm(http.StatusConflict, `{"code":"VOID_REFUSED"}`)
	before := f.occupationOf(t, action.ID)
	report := f.reconcileWhenDue(t, action.ID)
	after := f.occupationOf(t, action.ID)
	if report.OccupationQueriesClaimed != 1 || report.OccupationsReleased != 0 || after.Status != domain.ReleaseOccupationActive {
		t.Fatalf("rejected void: claimed %d released %d occupation %s, want one claim and the target kept", report.OccupationQueriesClaimed, report.OccupationsReleased, after.Status)
	}
	if after.QueryCount != before.QueryCount+1 || after.NextQueryAt == nil || !after.NextQueryAt.After(*before.NextQueryAt) {
		t.Fatalf("occupation = %+v, want the next query moved later", after)
	}

	answers.disarm()
	if report := f.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 1 {
		t.Fatalf("void production accepts released %d, want 1", report.OccupationsReleased)
	}
	if got := f.occupationOf(t, action.ID); got.ReleaseReason == nil || *got.ReleaseReason != domain.ReleaseReasonVoided {
		t.Fatalf("occupation = %+v, want released as VOIDED", got)
	}
}

// A rejected void must not end the questions: production may have deployed the operation
// after losing track of it, and only a query can learn that.
func TestReleaseAfterRunEnd_VoidRejectedThenDeployed_ReleasedFromTheQuery(t *testing.T) {
	production := newProductionFixture(t, mockproduction.WithCompletionDelay(time.Hour))
	voids := &productionAnswers{inner: production.srv.Client().Transport, matches: operationVoid}
	queries := &productionAnswers{inner: voids, matches: operationQuery}
	f := newReleaseFixture(t, releaseFixtureOptions{
		production: production, client: &http.Client{Transport: queries}, bounds: unknownOutcomeBounds, failingSibling: true,
	})
	action := f.startToRelease(t, "wf_open_void_then_deployed")
	f.approve(t, action)
	f.failSibling(t)
	queries.arm(http.StatusNotFound, `{"code":"NOT_FOUND"}`)
	if report := f.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 0 {
		t.Fatalf("first query released %d, want none", report.OccupationsReleased)
	}
	f.h.clock.Advance(time.Duration(unknownOutcomeBounds.voidGraceMs) * time.Millisecond)

	voids.arm(http.StatusConflict, `{"code":"VOID_REFUSED"}`)
	queries.arm(http.StatusOK, fmt.Sprintf(`{"operationId":%q,"status":"DEPLOYED","version":"v2"}`, f.occupationOf(t, action.ID).OperationKey))
	report := f.reconcileWhenDue(t, action.ID)

	if report.OccupationQueriesClaimed != 1 || report.OccupationsReleased != 1 {
		t.Fatalf("claimed %d released %d, want one claim that frees the target", report.OccupationQueriesClaimed, report.OccupationsReleased)
	}
	if got := f.occupationOf(t, action.ID); got.ReleaseReason == nil || *got.ReleaseReason != domain.ReleaseReasonDeployed {
		t.Fatalf("occupation = %+v, want released as DEPLOYED", got)
	}
	if n := production.counts.count("POST", deploymentsPath); n != 1 {
		t.Fatalf("release requests = %d, want the original one only", n)
	}
}

// A 409 that does not carry production's code is not its definite refusal.
func TestReleaseAfterRunEnd_ConflictWithoutACode_KeepsTheTarget(t *testing.T) {
	production := newProductionFixture(t)
	answers := &productionAnswers{inner: production.srv.Client().Transport, matches: operationQuery}
	f := newReleaseFixture(t, releaseFixtureOptions{
		production: production, client: &http.Client{Transport: answers}, failingSibling: true,
	})
	action := f.waitOnRelease(t, "wf_open_conflict")
	f.failSibling(t)

	answers.arm(http.StatusConflict, `<html>conflict</html>`)
	report := f.postReconcile(t)

	if report.OccupationQueriesClaimed != 1 || report.OccupationsReleased != 0 {
		t.Fatalf("claimed %d released %d, want one claim and nothing released", report.OccupationQueriesClaimed, report.OccupationsReleased)
	}
	if got := f.occupationOf(t, action.ID); got.Status != domain.ReleaseOccupationActive {
		t.Fatalf("occupation = %s, want ACTIVE", got.Status)
	}
}

// An ended Run whose occupation has no query schedule, left by a build from before the Run
// end wrote it. Its live poll is the only thing that still reaches it, so clearing that poll
// must hand the schedule over or nothing would ever settle the target.
func TestReleaseAfterRunEnd_OccupationWithoutSchedule_PollHandsItOver(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{failingSibling: true})
	action := f.waitOnRelease(t, "wf_open_legacy")
	f.failSibling(t)
	if _, err := f.h.pool.Exec(f.h.ctx, `UPDATE release_occupations SET next_query_at = NULL WHERE action_id = $1`, action.ID); err != nil {
		t.Fatalf("remove the schedule: %v", err)
	}

	report := f.postReconcile(t)

	if report.ToolPollsSkipped != 1 {
		t.Fatalf("pass skipped %d polls, want the one of the ended Run", report.ToolPollsSkipped)
	}
	attempt := f.attempts(t, action.ID)[0]
	occupation := f.occupationOf(t, action.ID)
	if attempt.NextPollAt != nil || occupation.NextQueryAt == nil {
		t.Fatalf("next poll %v next query %v, want the schedule moved from the Attempt to the occupation", attempt.NextPollAt, occupation.NextQueryAt)
	}
	f.production.server.ResumeCompletion()
	if report := f.reconcileWhenDue(t, action.ID); report.OccupationQueriesClaimed != 1 {
		t.Fatalf("query claimed %d, want the handed-over schedule to reach production", report.OccupationQueriesClaimed)
	}
}
