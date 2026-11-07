//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/service"
)

// errPostReconcileCrash stands for the process dying at a hook of the post-task
// reconciliation.
var errPostReconcileCrash = errors.New("the process died here")

// reconcileWhenDue advances the clock to the occupation's next query, when that is later,
// and runs one reconciliation pass that must collect no error.
func (f *releaseFixture) reconcileWhenDue(t *testing.T, actionID string) reconciler.Report {
	t.Helper()
	if next := f.occupationOf(t, actionID).NextQueryAt; next != nil {
		if wait := next.Sub(f.h.clock.Now()); wait > 0 {
			f.h.clock.Advance(wait)
		}
	}
	return agentRunOnce(f.h, f.reconciler())
}

// passWithCrash runs one reconciliation pass whose only error must be the injected crash.
func (f *releaseFixture) passWithCrash(t *testing.T) {
	t.Helper()
	report, err := f.reconciler().RunOnce(f.h.ctx)
	if err != nil {
		t.Fatalf("reconciler run once: %v", err)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0].Error(), errPostReconcileCrash.Error()) {
		t.Fatalf("reconciler errors = %v, want exactly the injected crash", report.Errors)
	}
}

// crashingHooks fails the post-task reconciliation of the armed Attempt at one point: after
// its claim committed (afterClaim) or after production answered (otherwise).
func crashingHooks(armed *atomic.Bool, afterClaim bool) service.PollHooks {
	crash := func(context.Context, string) error {
		if armed.Load() {
			return errPostReconcileCrash
		}
		return nil
	}
	if afterClaim {
		return service.PollHooks{AfterClaim: crash}
	}
	return service.PollHooks{AfterAnswer: crash}
}

// Contract case: the process dies after the post-task claim committed and before
// production was asked. The claim's schedule and count are committed, production saw no
// query, and a restarted Backend picks the occupation up from next_query_at and frees the
// target with the answer production gives it.
func TestPostReconcile_CrashAfterClaimBeforeQuery_RestartContinuesFromSchedule(t *testing.T) {
	var armed atomic.Bool
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds, pollHooks: crashingHooks(&armed, true)})
	action, wait := f.startToHumanWait(t, "wf_post_crash_after_claim")
	f.stopWait(t, wait)
	f.production.server.ResumeCompletion()
	path := deploymentsPath + "/" + f.occupationOf(t, action.ID).OperationKey
	queries := f.production.counts.count("GET", path)

	armed.Store(true)
	f.h.clock.Advance(releasePollStep)
	f.passWithCrash(t)
	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationActive || occupation.QueryCount != 1 || occupation.NextQueryAt == nil || !occupation.NextQueryAt.After(f.h.clock.Now()) {
		t.Fatalf("occupation after the crash = %+v, want ACTIVE, one claimed query, the next one scheduled later", occupation)
	}
	if n := f.production.counts.count("GET", path) - queries; n != 0 {
		t.Fatalf("production queries before the crash = %d, want none", n)
	}

	restarted := f.restart(t, service.PollHooks{})
	if report := restarted.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 1 {
		t.Fatalf("reconciliation after the restart released %d occupations, want 1", report.OccupationsReleased)
	}
	occupation = restarted.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonDeployed ||
		occupation.DeployedUncheckedVersion == nil || occupation.QueryCount != 2 {
		t.Errorf("occupation after the restart = %+v, want RELEASED DEPLOYED with the live version after two claims", occupation)
	}
}

// Contract case: the process dies after production answered DEPLOYED and before the result
// transaction. Nothing of the answer was written; a restarted Backend asks again when the
// schedule falls due, records the answer once, and production received the release once.
func TestPostReconcile_CrashAfterAnswerBeforeResult_RestartAsksAgainAndReleases(t *testing.T) {
	var armed atomic.Bool
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds, pollHooks: crashingHooks(&armed, false)})
	action, wait := f.startToHumanWait(t, "wf_post_crash_after_answer")
	f.stopWait(t, wait)
	f.production.server.ResumeCompletion()
	events := len(f.events(t))

	armed.Store(true)
	f.h.clock.Advance(releasePollStep)
	f.passWithCrash(t)
	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationActive || occupation.QueryCount != 1 || occupation.DeployedUncheckedVersion != nil || occupation.NextQueryAt == nil {
		t.Fatalf("occupation after the crash = %+v, want ACTIVE with the answer unrecorded and the next query scheduled", occupation)
	}

	restarted := f.restart(t, service.PollHooks{})
	if report := restarted.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 1 {
		t.Fatalf("reconciliation after the restart released %d occupations, want 1", report.OccupationsReleased)
	}
	occupation = restarted.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonDeployed || occupation.DeployedUncheckedVersion == nil {
		t.Errorf("occupation after the restart = %+v, want RELEASED DEPLOYED with the live version", occupation)
	}
	if n := f.production.counts.count("POST", deploymentsPath); n != 1 {
		t.Errorf("release requests production received = %d, want one: nothing is released again", n)
	}
	if n := len(restarted.events(t)); n != events {
		t.Errorf("events = %d, want %d: the ended Run gets nothing", n, events)
	}
}

// Contract case: production still has the operation in flight long after the void grace.
// The post-task reconciliation only queries it, backing off, and never asks for a void: an
// operation production knows is not lost, however long it takes.
func TestPostReconcile_InFlightPastVoidGrace_OnlyQueriesNeverVoids(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds})
	action, wait := f.startToHumanWait(t, "wf_post_in_flight")
	f.stopWait(t, wait)
	key := f.occupationOf(t, action.ID).OperationKey
	f.h.clock.Advance(3 * time.Duration(unknownOutcomeBounds.voidGraceMs) * time.Millisecond)

	for pass := 1; pass <= 4; pass++ {
		if report := f.reconcileWhenDue(t, action.ID); report.OccupationQueriesClaimed != 1 || report.OccupationsReleased != 0 {
			t.Fatalf("pass %d: claimed %d released %d, want one query and nothing released", pass, report.OccupationQueriesClaimed, report.OccupationsReleased)
		}
	}
	if n := f.production.counts.count("POST", deploymentsPath+"/"+key+voidSuffix); n != 0 {
		t.Errorf("void requests = %d, want none for an operation production still deploys", n)
	}
	if status := f.production.operationStatus(t, key); status != "DEPLOYING" {
		t.Errorf("production operation = %q, want still DEPLOYING", status)
	}
	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationActive || occupation.QueryCount != 4 {
		t.Errorf("occupation = %s after %d queries, want ACTIVE after four", occupation.Status, occupation.QueryCount)
	}
	if attempt := f.attempts(t, action.ID)[0]; attempt.OutcomeUnknownSince != nil {
		t.Errorf("attempt unknown since %v, want unset: production always knew the operation", attempt.OutcomeUnknownSince)
	}
}

// Contract case: the release never reached production and a person stopped it, so no
// unknown-since time exists. The first post-task query learns that production does not
// know the operation and writes that time on the Attempt once, without an Event; queries
// inside the void grace from that moment only query, and the first one past it voids the
// operation and frees the target as VOIDED.
func TestPostReconcile_UnknownAnswerRecorded_VoidsOnlyAfterGraceFromIt(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: unknownOutcomeBounds})
	action := f.startToRelease(t, "wf_post_unknown_answer")
	f.crashBeforeDispatch(t, action)
	wait := f.openHumanWait(t, action)
	f.stopWait(t, wait)
	key := f.occupationOf(t, action.ID).OperationKey
	voidPath := deploymentsPath + "/" + key + voidSuffix
	events := len(f.events(t))

	if report := f.reconcileWhenDue(t, action.ID); report.OccupationQueriesClaimed != 1 || report.OccupationsReleased != 0 {
		t.Fatalf("first query: claimed %d released %d, want one query and nothing released", report.OccupationQueriesClaimed, report.OccupationsReleased)
	}
	since := f.attempts(t, action.ID)[0].OutcomeUnknownSince
	if since == nil || !since.Equal(f.h.clock.Now()) {
		t.Fatalf("attempt unknown since = %v, want the first query's time %v", since, f.h.clock.Now())
	}
	if n := len(f.events(t)); n != events {
		t.Errorf("events = %d after the unknown answer, want %d: no Event for the ended Run", n, events)
	}

	grace := time.Duration(unknownOutcomeBounds.voidGraceMs) * time.Millisecond
	for f.occupationOf(t, action.ID).NextQueryAt.Before(since.Add(grace)) {
		f.reconcileWhenDue(t, action.ID)
		if n := f.production.counts.count("POST", voidPath); n != 0 {
			t.Fatalf("void requested at %v, inside the grace that ends at %v", f.h.clock.Now(), since.Add(grace))
		}
		if again := f.attempts(t, action.ID)[0].OutcomeUnknownSince; again == nil || !again.Equal(*since) {
			t.Fatalf("unknown since moved to %v, want it kept at %v", again, since)
		}
	}

	if report := f.reconcileWhenDue(t, action.ID); report.OccupationsReleased != 1 {
		t.Fatalf("query past the grace released %d occupations, want 1", report.OccupationsReleased)
	}
	if n := f.production.counts.count("POST", voidPath); n != 1 {
		t.Errorf("void requests = %d, want one", n)
	}
	occupation := f.occupationOf(t, action.ID)
	if occupation.Status != domain.ReleaseOccupationReleased || occupation.ReleaseReason == nil || *occupation.ReleaseReason != domain.ReleaseReasonVoided {
		t.Errorf("occupation = %+v, want RELEASED VOIDED", occupation)
	}
	if n := f.production.counts.count("POST", deploymentsPath); n != 0 {
		t.Errorf("release requests production received = %d, want none: nothing is released", n)
	}
}

// Contract case: an occupation whose operation key names no Attempt of its Action is a
// permanent fault. The claim still moves the schedule by the backoff and the pass collects
// no error, so the same fault is not reported on every pass; production is never asked.
func TestPostReconcile_PermanentFault_ScheduleAdvancedNoPassError(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action, wait := f.startToHumanWait(t, "wf_post_permanent_fault")
	f.stopWait(t, wait)
	occupation := f.occupationOf(t, action.ID)
	path := deploymentsPath + "/" + occupation.OperationKey
	queries := f.production.counts.count("GET", path)
	if _, err := f.h.pool.Exec(f.h.ctx, `UPDATE release_occupations SET operation_key = 'op_missing_1' WHERE id = $1`, occupation.ID); err != nil {
		t.Fatalf("break the operation key: %v", err)
	}

	first := f.reconcileWhenDue(t, action.ID)
	afterFirst := f.occupationOf(t, action.ID)
	second := f.reconcileWhenDue(t, action.ID)
	afterSecond := f.occupationOf(t, action.ID)
	if first.OccupationQueriesClaimed != 0 || second.OccupationQueriesClaimed != 0 {
		t.Errorf("claimed queries = %d and %d, want none counted as a production query", first.OccupationQueriesClaimed, second.OccupationQueriesClaimed)
	}
	if afterFirst.QueryCount != 1 || afterSecond.QueryCount != 2 || afterFirst.NextQueryAt == nil || afterSecond.NextQueryAt == nil ||
		!afterSecond.NextQueryAt.After(*afterFirst.NextQueryAt) {
		t.Errorf("schedule = count %d next %v, then count %d next %v; want each pass to move it later", afterFirst.QueryCount, afterFirst.NextQueryAt, afterSecond.QueryCount, afterSecond.NextQueryAt)
	}
	if afterSecond.Status != domain.ReleaseOccupationActive {
		t.Errorf("occupation = %s, want still ACTIVE", afterSecond.Status)
	}
	if n := f.production.counts.count("GET", path) - queries; n != 0 {
		t.Errorf("production queries = %d, want none", n)
	}
}
