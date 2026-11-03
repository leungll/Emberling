//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
)

// assertCallBoundedByConfirmDeadline checks that the release request under the Attempt's
// operation key reached the HTTP client with a live context whose deadline is the
// Attempt's confirm deadline.
func assertCallBoundedByConfirmDeadline(t *testing.T, calls []releaseCallDeadline, attempt domain.ToolAttempt) {
	t.Helper()
	if attempt.ConfirmDeadline == nil {
		t.Fatalf("attempt %d has no confirm deadline", attempt.AttemptNo)
	}
	for _, call := range calls {
		if call.operationKey != operationKey(attempt) {
			continue
		}
		if call.expired || !call.hasDeadline || !call.deadline.Equal(*attempt.ConfirmDeadline) {
			t.Errorf("release call %s: expired %v deadline %v (%v), want a live call bounded by the confirm deadline %s",
				call.operationKey, call.expired, call.deadline, call.hasDeadline, attempt.ConfirmDeadline)
		}
		return
	}
	t.Errorf("release calls = %+v, want one under %s", calls, operationKey(attempt))
}

// The approved release call is bounded by its Attempt's confirm deadline, not by the Agent
// deadline the approval moved: the release wait does not count against the Agent's time.
func TestRelease_Approved_CallDeadlineIsAttemptConfirmDeadline(t *testing.T) {
	production := newProductionFixture(t)
	client, recorder := production.recordingClient()
	f := newReleaseFixture(t, releaseFixtureOptions{production: production, client: client})
	action := f.startToRelease(t, "wf_release_call_deadline")
	f.approve(t, action)

	attempts := f.attempts(t, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if agentRun := f.agentRun(t); attempts[0].ConfirmDeadline != nil && agentRun.Deadline.Equal(*attempts[0].ConfirmDeadline) {
		t.Fatal("the Agent deadline equals the confirm deadline; the test cannot tell which bounds the call")
	}
	assertCallBoundedByConfirmDeadline(t, recorder.releaseCalls(), attempts[0])

	f.poll(t, f.reconciler())
	f.assertDeployedOnce(t, action, operationKey(attempts[0]))
	f.finish(t)
}

// While the Agent is paused for a release its stored deadline is frozen. When the release
// has waited past that instant in real time and production voids the first operation, the
// re-release is still sent: its call is bounded by the new Attempt's confirm deadline. The
// persisted pause is moved back so that the Agent paused ten minutes before a deadline that
// now lies in the past, which is what a long real wait leaves behind; the integration clock
// itself never falls behind real time.
func TestRelease_ReleaseAfterVoid_PastFrozenAgentDeadline_RequestStillReachesProduction(t *testing.T) {
	production := newProductionFixture(t)
	client, recorder := production.recordingClient()
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: recoveryBounds, production: production, client: client})
	action := f.startToRelease(t, "wf_release_frozen_deadline")
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{Unreachable: true})
	f.approve(t, action)
	f.assertOutcomeUnknown(t, action)
	f.production.server.SetReleaseFaults(mockproduction.ReleaseFaults{})

	const remaining = 10 * time.Minute
	frozen := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	agentRunID := f.agentRunID(t)
	if _, err := f.h.pool.Exec(f.h.ctx, `UPDATE agent_runs SET deadline = $2, paused_at = $3 WHERE id = $1 AND paused_at IS NOT NULL`,
		agentRunID, frozen, frozen.Add(-remaining)); err != nil {
		t.Fatalf("move the frozen agent deadline into the past: %v", err)
	}

	rec := f.reconciler()
	f.poll(t, rec)
	f.h.clock.Advance(time.Duration(recoveryBounds.voidGraceMs) * time.Millisecond)
	f.poll(t, rec)
	second := f.assertVoidedAndReleasedAgain(t, action)
	assertCallBoundedByConfirmDeadline(t, recorder.releaseCalls(), second)
	if n := f.production.counts.count("POST", deploymentsPath); n != 2 {
		t.Errorf("release requests production received = %d, want 2: the unanswered one and the re-release", n)
	}

	f.poll(t, rec)
	f.assertDeployedOnce(t, action, operationKey(second))
	if agentRun := f.agentRun(t); agentRun.PausedAt != nil || !agentRun.Deadline.After(f.h.clock.Now()) {
		t.Errorf("agent run after the conclusion: paused %v deadline %s, want resumed with its remaining time", agentRun.PausedAt, agentRun.Deadline)
	}
	f.finish(t)
}
